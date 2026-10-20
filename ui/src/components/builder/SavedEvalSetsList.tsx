import React, { useEffect, useState } from 'react';
import { css } from '@emotion/react';
import { FileJson, Loader2 } from 'lucide-react';
import { message } from 'antd';
import { listEvalSets, getEvalSet, StorageUnavailableError } from '../../api/client';
import { useTraceContext } from '../../context/TraceContext';
import type { EvalSetSummary } from '../../lib/types';

/**
 * Lists EvalSets previously persisted via the "Save EvalSet" button (POST
 * /api/evalsets - see evalsetsstore.go), so a saved EvalSet is something a
 * user can come back to instead of only ever living in a downloaded JSON
 * file. Renders nothing while loading, on a fetch error, or when no
 * eval sets have been saved yet (including when --session-db isn't
 * configured at all) rather than showing an empty/broken list on the
 * Builder's landing screen.
 */
export const SavedEvalSetsList: React.FC = () => {
  const { actions } = useTraceContext();
  const [evalSets, setEvalSets] = useState<EvalSetSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [openingId, setOpeningId] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    listEvalSets()
      .then((data) => {
        if (!cancelled) setEvalSets(data);
      })
      .catch((err) => {
        if (!cancelled && !(err instanceof StorageUnavailableError)) {
          console.error('Failed to list eval sets:', err);
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const handleOpen = async (evalSetId: string) => {
    setOpeningId(evalSetId);
    try {
      const evalSet = await getEvalSet(evalSetId);
      actions.setBuilderEvalSet(evalSet);
    } catch (err) {
      console.error('Failed to load eval set:', err);
      message.error('Failed to load saved EvalSet.');
    } finally {
      setOpeningId(null);
    }
  };

  if (loading || evalSets.length === 0) {
    return null;
  }

  return (
    <div css={containerStyle}>
      <h3 css={headingStyle}>Saved EvalSets</h3>
      <ul css={listStyle}>
        {evalSets.map((es) => (
          <li key={es.evalSetId} css={itemStyle} onClick={() => handleOpen(es.evalSetId)}>
            {openingId === es.evalSetId ? (
              <Loader2 size={16} css={spinStyle} />
            ) : (
              <FileJson size={16} />
            )}
            <div css={itemTextStyle}>
              <div css={itemNameStyle}>{es.name || es.evalSetId}</div>
              <div css={itemMetaStyle}>
                {es.numCases} case{es.numCases === 1 ? '' : 's'} · updated {new Date(es.updatedAt).toLocaleString()}
              </div>
            </div>
          </li>
        ))}
      </ul>
    </div>
  );
};

const containerStyle = css`
  max-width: 600px;
  width: 100%;
  margin-top: 32px;
`;

const headingStyle = css`
  font-size: 0.875rem;
  font-weight: 600;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--text-secondary);
  margin: 0 0 12px 0;
`;

const listStyle = css`
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 8px;
`;

const itemStyle = css`
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 16px;
  border: 1px solid var(--border-default);
  border-radius: 8px;
  background: var(--bg-surface);
  cursor: pointer;
  transition: all 0.15s ease;
  color: var(--accent-primary);

  &:hover {
    border-color: var(--accent-primary);
    background: var(--bg-elevated);
  }
`;

const itemTextStyle = css`
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
`;

const itemNameStyle = css`
  font-size: 0.9375rem;
  font-weight: 500;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
`;

const itemMetaStyle = css`
  font-size: 0.75rem;
  color: var(--text-secondary);
`;

const spinStyle = css`
  animation: spin 1s linear infinite;
  @keyframes spin {
    from { transform: rotate(0deg); }
    to { transform: rotate(360deg); }
  }
`;
