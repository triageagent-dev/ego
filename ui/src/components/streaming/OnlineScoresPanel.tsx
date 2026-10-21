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
              onClick={() => label(s.metric, 'agree')} title="Agree: the judge's score is right" aria-label="agree">
              <ThumbIcon up />
            </button>
            <button css={[btnStyle, existing?.verdict === 'disagree' && onStyle]} disabled={busy === s.metric}
              onClick={() => label(s.metric, 'disagree')} title="Disagree: the judge's score is wrong" aria-label="disagree">
              <ThumbIcon />
            </button>
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

// A thumb, up for agree and down for disagree, drawn in the button's colour.
const ThumbIcon = ({ up = false }: { up?: boolean }) => (
  <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"
    style={up ? undefined : { transform: 'scaleY(-1)' }}>
    <path d="M14 9V5a3 3 0 0 0-3-3l-4 9v11h11.28a2 2 0 0 0 2-1.7l1.38-9a2 2 0 0 0-2-2.3H14z" />
    <path d="M7 22H4a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2h3" />
  </svg>
);

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
