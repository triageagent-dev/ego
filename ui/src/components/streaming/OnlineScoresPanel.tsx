import React, { useCallback, useEffect, useState } from 'react';
import { css } from '@emotion/react';
import { getOnlineLabels, getOnlineSessionScores, postOnlineLabel } from '../../api/client';
import type { OnlineLabel, OnlineSessionScore } from '../../lib/types';

// The online scores of one session, each with agree / disagree: a person's verdict on the judge's score. The
// labels measure how far each metric's judge can be trusted (docs/rsi.md, section 4). Renders nothing for a
// session that online evaluation never scored.

interface Props {
  sessionId: string;
}

export const OnlineScoresPanel: React.FC<Props> = ({ sessionId }) => {
  const [scores, setScores] = useState<OnlineSessionScore[] | null>(null);
  const [labels, setLabels] = useState<OnlineLabel[]>([]);
  const [notes, setNotes] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const s = await getOnlineSessionScores(sessionId);
      setScores(s);
      setLabels(s && s.length > 0 ? await getOnlineLabels(sessionId) : []);
    } catch {
      setScores(null);
    }
  }, [sessionId]);

  useEffect(() => {
    load();
  }, [load]);

  const label = async (metric: string, verdict: 'agree' | 'disagree') => {
    setBusy(metric);
    setError(null);
    try {
      await postOnlineLabel({ sessionId, metric, verdict, note: notes[metric]?.trim() || undefined });
      setNotes(n => ({ ...n, [metric]: '' }));
      setLabels(await getOnlineLabels(sessionId));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const scored = (scores ?? []).filter(s => s.score !== null);
  if (scored.length === 0) return null;

  // Labels come newest first; the first of a metric is the one that counts.
  const latest = (metric: string) => labels.find(l => l.metric === metric);

  return (
    <div css={panelStyle}>
      <div css={headStyle}>online scores</div>
      {scored.map(s => {
        const existing = latest(s.metric);
        return (
          <div key={s.metric} css={rowStyle}>
            <span css={metricStyle}>{s.metric}</span>
            <span css={scoreStyle}>{s.score!.toFixed(2)}</span>
            <button css={[btnStyle, existing?.verdict === 'agree' && onStyle]} disabled={busy === s.metric}
              onClick={() => label(s.metric, 'agree')} title="The judge's score is right">agree</button>
            <button css={[btnStyle, existing?.verdict === 'disagree' && onStyle]} disabled={busy === s.metric}
              onClick={() => label(s.metric, 'disagree')} title="The judge's score is wrong">disagree</button>
            <input css={noteStyle} placeholder="note (optional)" maxLength={1000} value={notes[s.metric] ?? ''}
              onChange={e => setNotes(n => ({ ...n, [s.metric]: e.target.value }))} />
            {existing && (
              <span css={dimStyle}>
                {existing.by ? `${existing.by} ` : ''}{existing.verdict}d{existing.note ? `: ${existing.note}` : ''}
              </span>
            )}
          </div>
        );
      })}
      {error && <div css={errStyle}>{error}</div>}
    </div>
  );
};

const panelStyle = css`
  padding: 8px 0;
  margin-bottom: 8px;
  border-bottom: 1px solid var(--border);
  font-size: 12px;
`;

const headStyle = css`
  font-size: 10px;
  font-weight: 600;
  text-transform: uppercase;
  color: var(--text-tertiary);
  margin-bottom: 4px;
`;

const rowStyle = css`
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  padding: 2px 0;
`;

const metricStyle = css`
  font-family: var(--font-mono);
  color: var(--text-secondary);
`;

const scoreStyle = css`
  font-family: var(--font-mono);
  color: var(--text-primary);
  min-width: 34px;
`;

const btnStyle = css`
  padding: 1px 8px;
  border-radius: var(--radius);
  border: 1px solid var(--border-default);
  background: transparent;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 11px;
  cursor: pointer;
  &:hover:not(:disabled) {
    color: var(--text-primary);
    border-color: var(--accent-primary);
  }
`;

const onStyle = css`
  background: var(--bg-selected);
  color: var(--accent-magenta);
`;

const noteStyle = css`
  flex: 1 1 140px;
  min-width: 100px;
  max-width: 280px;
  padding: 1px 6px;
  border-radius: var(--radius);
  border: 1px solid var(--border-default);
  background: transparent;
  color: var(--text-primary);
  font-size: 11px;
`;

const dimStyle = css`
  color: var(--text-muted);
`;

const errStyle = css`
  margin-top: 4px;
  color: var(--status-failure);
`;
