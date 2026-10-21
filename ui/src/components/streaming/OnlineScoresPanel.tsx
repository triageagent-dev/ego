import React, { useCallback, useEffect, useState } from 'react';
import { css } from '@emotion/react';
import { getOnlineLabels, getOnlineSessionEvidence, postOnlineLabel } from '../../api/client';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { ScoreDetails } from './ScoreDetails';
import type { OnlineLabel, OnlineSessionScore } from '../../lib/types';

// A person's verdict on the judge's score of one metric of one session: thumbs and a note. The label is the
// score of record (docs/rsi.md, section 4): a thumb down asks for the right score, which replaces the judge's
// wherever ego aggregates; a thumb up confirms the judge's. `latest` is the label that counts for the metric
// (the server lists them newest first); onLabeled reloads them. Clicks stay inside the control, so it can sit on
// a row that toggles on click.
interface LabelButtonsProps {
  sessionId: string;
  metric: string;
  latest?: OnlineLabel;
  onLabeled: () => void | Promise<void>;
}

export const LabelButtons: React.FC<LabelButtonsProps> = ({ sessionId, metric, latest, onLabeled }) => {
  const [note, setNote] = useState('');
  const [busy, setBusy] = useState(false);
  const [picking, setPicking] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const label = async (verdict: 'agree' | 'disagree', score?: number) => {
    setBusy(true);
    setError(null);
    setPicking(false);
    try {
      await postOnlineLabel({ sessionId, metric, verdict, score, note: note.trim() || undefined });
      setNote('');
      await onLabeled();
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <span css={groupStyle} onClick={e => e.stopPropagation()}>
      <span css={thumbsStyle}>
        <button css={[btnStyle, latest?.verdict === 'agree' && onStyle]} disabled={busy}
          onClick={() => label('agree')} title="Agree: the judge's score is right" aria-label="agree">
          <ThumbIcon up />
        </button>
        <button css={[btnStyle, (latest?.verdict === 'disagree' || picking) && onStyle]} disabled={busy}
          onClick={() => setPicking(p => !p)} title="Disagree: the judge's score is wrong" aria-label="disagree">
          <ThumbIcon />
        </button>
        {picking && (
          <span css={pickStyle}>
            <span css={dimStyle}>right score</span>
            {[0, 0.25, 0.5, 0.75, 1].map(v => (
              <button key={v} css={btnStyle} disabled={busy} onClick={() => label('disagree', v)}>{v}</button>
            ))}
            <button css={btnStyle} disabled={busy} onClick={() => label('disagree')}
              title="Disagree without a score: the session then counts nowhere until someone scores it">?</button>
          </span>
        )}
      </span>
      <input css={noteStyle} placeholder="note (optional)" maxLength={1000} value={note}
        onChange={e => setNote(e.target.value)} onKeyDown={e => e.stopPropagation()} />
      {/* Always rendered: the row must fill all five grid cells, or the next row starts in the wrong column. */}
      <span css={dimStyle}>
        {latest
          ? `${latest.by ? `${latest.by} ` : ''}${latest.verdict}d${latest.score !== undefined ? ` (${latest.score})` : ''}${latest.note ? `: ${latest.note}` : ''}`
          : null}
      </span>
      {error && <span css={errStyle}>{error}</span>}
    </span>
  );
};

// The online scores of one session, each with the thumbs. Renders nothing for a session that online evaluation
// never scored.
interface Props {
  sessionId: string;
}

export const OnlineScoresPanel: React.FC<Props> = ({ sessionId }) => {
  const [scores, setScores] = useState<OnlineSessionScore[] | null>(null);
  const [labels, setLabels] = useState<OnlineLabel[]>([]);
  const [runId, setRunId] = useState<string | undefined>();
  // The metrics whose details are shown: the ones that failed start open, as that is what a person checks first.
  const [shown, setShown] = useState<Record<string, boolean>>({});

  const load = useCallback(async () => {
    try {
      const ev = await getOnlineSessionEvidence(sessionId);
      const s = ev?.metrics ?? null;
      setScores(s);
      setRunId(ev?.run_id);
      setLabels(s && s.length > 0 ? await getOnlineLabels(sessionId) : []);
    } catch {
      setScores(null);
    }
  }, [sessionId]);

  useEffect(() => {
    load();
  }, [load]);

  const reloadLabels = useCallback(async () => setLabels(await getOnlineLabels(sessionId)), [sessionId]);

  const scored = (scores ?? []).filter(s => s.score !== null);
  if (scored.length === 0) return null;

  // Labels come newest first; the first of a metric is the one that counts.
  const latest = (metric: string) => labels.find(l => l.metric === metric);

  return (
    <div css={panelStyle}>
      <div css={headStyle}>
        online scores
        {runId && <a css={runLinkStyle} href={`#runs?run=${encodeURIComponent(runId)}`} title="The hourly run this session was scored in">run details ›</a>}
      </div>
      {scored.map(s => {
        const open = shown[s.metric] ?? s.status === 'failed';
        return (
        <React.Fragment key={s.metric}>
        <div css={rowStyle}>
          <button css={metricButtonStyle} onClick={() => setShown(p => ({ ...p, [s.metric]: !open }))} aria-expanded={open} title="The judge's reasons for this score">
            {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
            <span css={metricStyle}>{s.metric}</span>
          </button>
          <span css={scoreStyle}>
            {s.human_score !== undefined && s.human_score !== s.score
              ? <><s css={dimStyle}>{s.score!.toFixed(2)}</s> {s.human_score.toFixed(2)}</>
              : s.score!.toFixed(2)}
          </span>
          <LabelButtons sessionId={sessionId} metric={s.metric} latest={latest(s.metric)} onLabeled={reloadLabels} />
        </div>
        {open && <ScoreDetails s={s} />}
        </React.Fragment>
        );
      })}
    </div>
  );
};

// A thumb, up for agree and down for disagree, drawn in the button's colour.
const ThumbIcon = ({ up = false }: { up?: boolean }) => (
  <svg width="17" height="17" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2"
    strokeLinecap="round" strokeLinejoin="round" aria-hidden="true"
    style={up ? undefined : { transform: 'scaleY(-1)' }}>
    <path d="M14 9V5a3 3 0 0 0-3-3l-4 9v11h11.28a2 2 0 0 0 2-1.7l1.38-9a2 2 0 0 0-2-2.3H14z" />
    <path d="M7 22H4a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2h3" />
  </svg>
);

// The buttons, the note and the verdict are cells of the panel's grid, so every row lines up under the same columns.
const groupStyle = css`
  display: contents;
`;

const panelStyle = css`
  padding: 8px 0;
  margin-bottom: 8px;
  border-bottom: 1px solid var(--border);
  font-size: 14px;
  display: grid;
  /* metric | score | thumbs | note | verdict */
  grid-template-columns: max-content max-content max-content minmax(160px, 420px) 1fr;
  align-items: center;
  column-gap: 8px;
  row-gap: 8px;
`;

const thumbsStyle = css`
  display: inline-flex;
  gap: 4px;
  align-items: center;
`;

const pickStyle = css`
  display: inline-flex;
  gap: 4px;
  align-items: center;
  margin-left: 6px;
`;

const metricButtonStyle = css`
  display: inline-flex;
  align-items: center;
  gap: 4px;
  padding: 0;
  border: none;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;

  &:hover span {
    color: var(--text-primary);
  }
`;

const runLinkStyle = css`
  margin-left: 12px;
  font-weight: 400;
  text-transform: none;
  color: var(--accent-primary);
  text-decoration: none;

  &:hover {
    text-decoration: underline;
  }
`;

const headStyle = css`
  font-size: 12px;
  font-weight: 600;
  text-transform: uppercase;
  color: var(--text-tertiary);
  margin-bottom: 4px;
  grid-column: 1 / -1;
`;

const rowStyle = css`
  display: contents;
`;

const metricStyle = css`
  font-family: var(--font-mono);
  color: var(--text-secondary);
`;

const scoreStyle = css`
  font-family: var(--font-mono);
  color: var(--text-primary);
  text-align: right;
  font-variant-numeric: tabular-nums;
`;

const btnStyle = css`
  display: inline-flex;
  align-items: center;
  padding: 3px 10px;
  border-radius: var(--radius);
  border: 1px solid var(--border-default);
  background: transparent;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 14px;
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
  width: 100%;
  padding: 4px 8px;
  border-radius: var(--radius);
  border: 1px solid var(--border-default);
  background: transparent;
  color: var(--text-primary);
  font-size: 14px;
`;

const dimStyle = css`
  color: var(--text-muted);
`;

const errStyle = css`
  grid-column: 1 / -1;
  color: var(--status-failure);
`;
