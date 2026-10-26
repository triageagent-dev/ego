import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { Button, Input, InputNumber, Select } from 'antd';
import { Plus, X } from 'lucide-react';
import { getOnlineJobs, listMetrics, listRubricSets, resetOnlineJobs, saveOnlineJobs } from '../../api/client';
import { RUBRIC_KIND_OF } from '../../lib/eval-config';
import type { MetricMetadata, OnlineJob, OnlineJobs, RubricSet } from '../../lib/types';
import { AVAILABLE_METRICS } from '../../lib/types';

const DEFAULT_TURNS = 3;

// What a live session can be scored with: no golden conversation and not Vertex's managed service, whose cost the
// server's budget cannot see.
function usable(m: MetricMetadata): boolean {
  return !m.requiresEvalSet && !m.requiresGCP && m.working !== false;
}

const sameRubrics = (a: string[] = [], b: string[] = []) => a.length === b.length && a.every((r, i) => r === b[i]);

/** The online loop's jobs: which kinds of session are scored, with which metrics, how many an hour. */
export const ScoringJobsPanel: React.FC<{ canEdit: boolean }> = ({ canEdit }) => {
  const [loaded, setLoaded] = useState<OnlineJobs | null>(null);
  const [rate, setRate] = useState(6);
  const [jobs, setJobs] = useState<OnlineJob[]>([]);
  const [open, setOpen] = useState<number | null>(null);
  const [metrics, setMetrics] = useState<MetricMetadata[]>(AVAILABLE_METRICS.filter(usable));
  const [rubricSets, setRubricSets] = useState<RubricSet[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [unavailable, setUnavailable] = useState(false);

  const adopt = useCallback((j: OnlineJobs) => {
    setLoaded(j);
    setRate(j.perKindPerHour);
    setJobs(j.profiles);
    setOpen(null);
  }, []);

  useEffect(() => {
    getOnlineJobs().then(adopt).catch(() => setUnavailable(true));
    listMetrics().then((ms) => setMetrics(ms.filter(usable))).catch(() => undefined);
    listRubricSets().then(setRubricSets).catch(() => undefined);
  }, [adopt]);

  const dirty = useMemo(
    () => loaded !== null && JSON.stringify({ rate, jobs }) !== JSON.stringify({ rate: loaded.perKindPerHour, jobs: loaded.profiles }),
    [loaded, rate, jobs],
  );

  if (unavailable) return <div css={noteStyle}>Not available: online scoring is off on this server, or this account may not see it.</div>;
  if (loaded === null) return <div css={noteStyle}>Loading…</div>;

  const update = (i: number, patch: Partial<OnlineJob>) => setJobs(jobs.map((j, k) => (k === i ? { ...j, ...patch } : j)));
  const add = (kind: string) => {
    setJobs([...jobs, { kind, evaluators: [] }]);
    setOpen(jobs.length);
  };
  const setMetricNames = (i: number, names: string[]) =>
    update(i, { evaluators: names.map((name) => jobs[i].evaluators.find((e) => e.name === name) ?? { name }) });
  const setRubrics = (i: number, metric: string, rubrics: string[]) =>
    update(i, { evaluators: jobs[i].evaluators.map((e) => (e.name === metric ? { ...e, rubrics } : e)) });

  const run = async (action: () => Promise<OnlineJobs>) => {
    setBusy(true);
    setError('');
    try {
      adopt(await action());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const missing = jobs.some((j) => !j.kind.trim() || j.evaluators.length === 0);
  const unscored = loaded.unscored.filter((u) => !jobs.some((j) => j.kind === u.kind));

  return (
    <div css={panelStyle}>
      {jobs.length === 0 && <div css={noteStyle}>No jobs: nothing is being scored.</div>}
      {jobs.map((job, i) => (
        <div key={i} css={cardStyle}>
          <div css={rowStyle} onClick={() => setOpen(open === i ? null : i)}>
            <span css={kindStyle}>{job.kind || 'new job'}</span>
            <span css={metaStyle}>
              {job.evaluators.map((e) => e.name.replace(/_v\d+$/, '')).join(', ') || 'no metric yet'}
            </span>
            <span css={metaStyle}>
              {job.perHour ?? rate}/h · last {job.maxInvocations ?? DEFAULT_TURNS} turn{(job.maxInvocations ?? DEFAULT_TURNS) === 1 ? '' : 's'}
            </span>
            {canEdit && (
              <button
                css={removeStyle}
                onClick={(e) => {
                  e.stopPropagation();
                  setJobs(jobs.filter((_, k) => k !== i));
                  setOpen(null);
                }}
                title="Remove this job"
                aria-label="Remove this job"
              >
                <X size={12} />
              </button>
            )}
          </div>
          {open === i && (
            <fieldset css={editorStyle} disabled={!canEdit}>
              <label css={labelStyle}>Kind of session</label>
              <Input size="small" value={job.kind} onChange={(e) => update(i, { kind: e.target.value })} placeholder="chat, or a pattern like *-spot" />
              <label css={labelStyle}>Scored with</label>
              <Select
                size="small"
                mode="multiple"
                value={job.evaluators.map((e) => e.name)}
                onChange={(names) => setMetricNames(i, names)}
                options={metrics.map((m) => ({ value: m.name, label: m.name, title: m.description }))}
                placeholder="one or more metrics"
              />
              {job.evaluators.filter((e) => RUBRIC_KIND_OF[e.name]).map((e) => {
                const options = rubricSets.filter((s) => s.kind === RUBRIC_KIND_OF[e.name]);
                const chosen = options.find((s) => sameRubrics(s.rubrics, e.rubrics));
                return (
                  <React.Fragment key={e.name}>
                    <label css={labelStyle}>Rubrics for {e.name.replace(/_v\d+$/, '')}</label>
                    <Select
                      size="small"
                      value={chosen?.rubricSetId}
                      onChange={(id) => setRubrics(i, e.name, options.find((s) => s.rubricSetId === id)?.rubrics ?? [])}
                      options={options.map((s) => ({ value: s.rubricSetId, label: `${s.name} (${s.rubrics.length})` }))}
                      placeholder={e.rubrics?.length ? `${e.rubrics.length} written in the server's configuration` : 'pick a rubric set'}
                      notFoundContent="No rubric sets of this kind; write one on the evaluate page."
                    />
                    {!e.rubrics?.length && <div css={warnStyle}>This metric needs rubrics.</div>}
                  </React.Fragment>
                );
              })}
              <div css={pairStyle}>
                <div>
                  <label css={labelStyle}>Sessions an hour</label>
                  <InputNumber size="small" min={0.1} step={1} value={job.perHour} placeholder={String(rate)} onChange={(v) => update(i, { perHour: v ?? undefined })} />
                </div>
                <div>
                  <label css={labelStyle}>Last turns scored</label>
                  <InputNumber size="small" min={1} max={20} step={1} value={job.maxInvocations} placeholder={String(DEFAULT_TURNS)} onChange={(v) => update(i, { maxInvocations: v ?? undefined })} />
                </div>
              </div>
            </fieldset>
          )}
        </div>
      ))}

      {unscored.length > 0 && (
        <div css={unscoredStyle}>
          <span css={labelStyle}>Sent sessions, nothing scores them:</span>
          {unscored.map((u) => (
            <button key={u.kind} css={chipStyle} disabled={!canEdit} onClick={() => add(u.kind)} title="Add a job for this kind">
              {u.kind} <span css={metaStyle}>{u.sessions}</span>
            </button>
          ))}
        </div>
      )}

      {canEdit && (
        <>
          <div css={pairStyle}>
            <div>
              <label css={labelStyle}>Default sessions an hour, per kind and tenant</label>
              <InputNumber size="small" min={0.1} step={1} value={rate} onChange={(v) => setRate(v ?? 6)} />
            </div>
            <div css={addStyle}>
              <Button size="small" icon={<Plus size={12} />} onClick={() => add('')}>Add a job</Button>
            </div>
          </div>
          <div css={actionsStyle}>
            <Button size="small" type="primary" loading={busy} disabled={!dirty || missing} onClick={() => run(() => saveOnlineJobs({ perKindPerHour: rate, profiles: jobs }))}>
              Save
            </Button>
            {dirty && <Button size="small" onClick={() => adopt(loaded)}>Discard changes</Button>}
            {loaded.source === 'stored' && !dirty && (
              <Button size="small" loading={busy} onClick={() => run(resetOnlineJobs)} title="Drop what was saved here and score as the server's configuration says">
                Use the server's configuration
              </Button>
            )}
          </div>
          {missing && dirty && <div css={warnStyle}>Every job needs a kind and at least one metric.</div>}
        </>
      )}
      {error && <div css={errorStyle}>{error}</div>}
    </div>
  );
};

const panelStyle = css`
  display: flex;
  flex-direction: column;
  gap: 6px;
`;

const noteStyle = css`
  padding: 6px 0;
  color: var(--text-secondary);
  font-size: 12px;
`;

const cardStyle = css`
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-bg);
  font-size: 12px;
`;

const rowStyle = css`
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 5px 10px;
  cursor: pointer;
`;

const kindStyle = css`
  flex-shrink: 0;
  color: var(--text-primary);
  font-family: var(--font-mono);
`;

const metaStyle = css`
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: 11px;

  &:nth-of-type(2) {
    flex: 1;
  }
`;

const removeStyle = css`
  display: inline-flex;
  padding: 3px;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;

  &:hover {
    color: var(--text-primary);
  }
`;

const editorStyle = css`
  display: flex;
  flex-direction: column;
  gap: 6px;
  min-width: 0;
  margin: 0;
  padding: 8px 10px 10px;
  border: none;
  border-top: 1px solid var(--border-default);
`;

const pairStyle = css`
  display: flex;
  flex-wrap: wrap;
  align-items: flex-end;
  gap: 16px;

  > div {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
`;

const addStyle = css`
  margin-left: auto;
`;

const actionsStyle = css`
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
`;

const unscoredStyle = css`
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
`;

const chipStyle = css`
  padding: 2px 8px;
  border: 1px dashed var(--border-default);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 11px;
  cursor: pointer;

  &:hover:not(:disabled) {
    border-color: var(--accent-cyan);
  }
`;

const labelStyle = css`
  color: var(--text-muted);
  font-size: 11.5px;
`;

const warnStyle = css`
  color: var(--status-warning);
  font-size: 12px;
`;

const errorStyle = css`
  color: var(--status-failure);
  font-size: 12px;
`;
