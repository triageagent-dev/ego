import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { Button, Input, InputNumber } from 'antd';
import { Plus, X } from 'lucide-react';
import { getEvalLimits, resetEvalLimits, saveEvalLimits } from '../../api/client';
import type { EvalLimits, EvalLimitsState } from '../../lib/types';

interface ModelRow {
  name: string;
  weight?: number;
  dailyTokens?: number;
}

const rowsOf = (l: EvalLimits): ModelRow[] =>
  Object.entries(l.models ?? {}).map(([name, m]) => ({ name, weight: m.weight || undefined, dailyTokens: m.dailyTokens || undefined }));

const limitsOf = (userConcurrent: number | undefined, userPerMinute: number | undefined, rows: ModelRow[]): EvalLimits => {
  const models: NonNullable<EvalLimits['models']> = {};
  for (const r of rows) models[r.name.trim()] = { weight: r.weight, dailyTokens: r.dailyTokens };
  return { userConcurrent, userPerMinute, models };
};

/** The limits on interactive evaluation: how much one member may run, and what each judge model may use. Admin only. */
export const EvalLimitsPanel: React.FC = () => {
  const [loaded, setLoaded] = useState<EvalLimitsState | null>(null);
  const [userConcurrent, setUserConcurrent] = useState<number | undefined>();
  const [userPerMinute, setUserPerMinute] = useState<number | undefined>();
  const [rows, setRows] = useState<ModelRow[]>([]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [unavailable, setUnavailable] = useState(false);

  const adopt = useCallback((s: EvalLimitsState) => {
    setLoaded(s);
    setUserConcurrent(s.userConcurrent || undefined);
    setUserPerMinute(s.userPerMinute || undefined);
    setRows(rowsOf(s));
  }, []);

  useEffect(() => {
    getEvalLimits().then(adopt).catch(() => setUnavailable(true));
  }, [adopt]);

  const names = rows.map((r) => r.name.trim());
  const invalid = names.some((n) => !/^(\*|[A-Za-z0-9._-]{1,64})$/.test(n)) || new Set(names).size !== names.length;
  const dirty = useMemo(
    () =>
      loaded !== null &&
      JSON.stringify(limitsOf(userConcurrent, userPerMinute, rows)) !== JSON.stringify(limitsOf(loaded.userConcurrent || undefined, loaded.userPerMinute || undefined, rowsOf(loaded))),
    [loaded, userConcurrent, userPerMinute, rows],
  );

  if (unavailable) return null;
  if (loaded === null) return <div css={noteStyle}>Loading…</div>;

  // On Kubernetes the limits come from the chart (GitOps): shown here, changed there.
  const fixed = loaded.source === 'file';
  const update = (i: number, patch: Partial<ModelRow>) => setRows(rows.map((r, k) => (k === i ? { ...r, ...patch } : r)));

  const run = async (action: () => Promise<EvalLimitsState>) => {
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

  return (
    <div css={panelStyle}>
      {fixed && (
        <div css={noteStyle}>Set in the server's configuration (the chart value <code>evalLimits</code>): change them there.</div>
      )}
      <div css={pairStyle}>
        <div>
          <label css={labelStyle}>Evaluations one member runs at once</label>
          <InputNumber size="small" disabled={fixed} min={1} step={1} value={userConcurrent} placeholder={String(loaded.defaults.userConcurrent)} onChange={(v) => setUserConcurrent(v ?? undefined)} />
        </div>
        <div>
          <label css={labelStyle}>Evaluations one member starts a minute</label>
          <InputNumber size="small" disabled={fixed} min={1} step={1} value={userPerMinute} placeholder={String(loaded.defaults.userPerMinute)} onChange={(v) => setUserPerMinute(v ?? undefined)} />
        </div>
      </div>
      <div css={hintStyle}>Admins are not held to these two.</div>

      <div css={groupTitleStyle}>Judge models</div>
      <div css={hintStyle}>
        <b>Weight</b> is what one token of the model counts as against the day's budget and a member's share: 1 for the model you measure
        by ({loaded.defaultModel}), 4 for one that costs four times as much. <b>Daily tokens</b> caps that model alone, whoever asks; it is
        counted as the model reports it. Name <code>*</code> for any model not listed.
      </div>
      {rows.length === 0 && <div css={noteStyle}>No model has a weight or a cap of its own.</div>}
      {rows.map((r, i) => (
        <div key={i} css={modelRowStyle}>
          <Input size="small" disabled={fixed} value={r.name} onChange={(e) => update(i, { name: e.target.value })} placeholder={`model name, e.g. ${loaded.defaultModel}`} />
          <InputNumber size="small" disabled={fixed} min={0} step={0.5} value={r.weight} placeholder="weight 1" onChange={(v) => update(i, { weight: v ?? undefined })} />
          <InputNumber size="small" disabled={fixed} min={0} step={100000} value={r.dailyTokens} placeholder="no cap" onChange={(v) => update(i, { dailyTokens: v ?? undefined })} />
          {!fixed && (
            <button css={removeStyle} onClick={() => setRows(rows.filter((_, k) => k !== i))} title="Remove this model" aria-label="Remove this model">
              <X size={12} />
            </button>
          )}
        </div>
      ))}
      {!fixed && (
        <>
          <div css={actionsStyle}>
            <Button size="small" icon={<Plus size={12} />} onClick={() => setRows([...rows, { name: rows.length === 0 ? loaded.defaultModel : '' }])}>
              Add a model
            </Button>
          </div>

          <div css={actionsStyle}>
            <Button size="small" type="primary" loading={busy} disabled={!dirty || invalid} onClick={() => run(() => saveEvalLimits(limitsOf(userConcurrent, userPerMinute, rows)))}>
              Save
            </Button>
            {dirty && <Button size="small" onClick={() => adopt(loaded)}>Discard changes</Button>}
            {loaded.source === 'stored' && !dirty && (
              <Button size="small" loading={busy} onClick={() => run(resetEvalLimits)} title="Drop what was saved here and use the built-in limits">
                Use the built-in limits
              </Button>
            )}
          </div>
        </>
      )}
      {invalid && dirty && <div css={warnStyle}>A model name is letters, digits and . _ - (or *), and appears once.</div>}
      {!fixed && !loaded.persisted && <div css={warnStyle}>This server keeps no archive: what you save here is lost at a restart.</div>}
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

const hintStyle = css`
  color: var(--text-muted);
  font-size: 11.5px;
  line-height: 1.5;
`;

const groupTitleStyle = css`
  margin-top: 6px;
  color: var(--text-primary);
  font-size: 12px;
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

const modelRowStyle = css`
  display: grid;
  grid-template-columns: minmax(0, 1fr) 90px 120px auto;
  align-items: center;
  gap: 6px;
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

const actionsStyle = css`
  display: flex;
  flex-wrap: wrap;
  gap: 8px;
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
