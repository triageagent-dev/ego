import React, { useState } from 'react';
import { css } from '@emotion/react';
import { Button, Input, Select } from 'antd';
import { X } from 'lucide-react';
import { deleteRubricSet, saveRubricSet } from '../../api/client';
import type { RubricKind, RubricSet } from '../../lib/types';

export const RUBRIC_KIND_LABEL: Record<RubricKind, string> = {
  final_response: 'final response quality',
  tool_use: 'tool use quality',
};

/** The saved rubric sets, and a form to write one: a name, the metric it is for, and one rubric per line. */
export const RubricSetsPanel: React.FC<{
  sets: RubricSet[] | null;
  onChanged: (created?: RubricSet) => void;
}> = ({ sets, onChanged }) => {
  const [name, setName] = useState('');
  const [kind, setKind] = useState<RubricKind>('final_response');
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const create = async () => {
    const rubrics = text.split('\n').map((l) => l.trim()).filter(Boolean);
    if (!name.trim()) return setError('Give the rubric set a name.');
    if (rubrics.length === 0) return setError('Write at least one rubric, one per line.');
    setBusy(true);
    setError('');
    try {
      const saved = await saveRubricSet({ name: name.trim(), kind, rubrics });
      setName('');
      setText('');
      onChanged(saved);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const remove = async (id: string) => {
    setError('');
    try {
      await deleteRubricSet(id);
      onChanged();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <div css={panelStyle}>
      {sets === null && <div css={noteStyle}>Loading rubric sets…</div>}
      {sets !== null && sets.length === 0 && <div css={noteStyle}>No rubric sets yet.</div>}
      {sets?.map((s) => (
        <div key={s.rubricSetId} css={rowStyle}>
          <span css={nameStyle} title={s.rubrics.join('\n')}>{s.name}</span>
          <span css={metaStyle}>{RUBRIC_KIND_LABEL[s.kind]} · {s.rubrics.length} rubric{s.rubrics.length === 1 ? '' : 's'}</span>
          <button css={removeStyle} onClick={() => remove(s.rubricSetId)} title="Delete this rubric set" aria-label="Delete this rubric set">
            <X size={12} />
          </button>
        </div>
      ))}

      <label css={labelStyle}>Name</label>
      <Input size="small" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. grounded answers" />
      <label css={labelStyle}>For</label>
      <Select
        size="small"
        value={kind}
        onChange={setKind}
        options={(Object.keys(RUBRIC_KIND_LABEL) as RubricKind[]).map((k) => ({ value: k, label: RUBRIC_KIND_LABEL[k] }))}
      />
      <label css={labelStyle}>Rubrics, one per line</label>
      <Input.TextArea rows={5} value={text} onChange={(e) => setText(e.target.value)} placeholder="Each claim is backed by a tool result" />
      <div>
        <Button size="small" type="primary" loading={busy} onClick={create}>Save rubric set</Button>
      </div>
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

const rowStyle = css`
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 5px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-bg);
  font-size: 12px;
`;

const nameStyle = css`
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-primary);
`;

const metaStyle = css`
  flex-shrink: 0;
  color: var(--text-muted);
  font-family: var(--font-mono);
  font-size: 11px;
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

const labelStyle = css`
  color: var(--text-muted);
  font-size: 11.5px;
`;

const errorStyle = css`
  color: var(--status-failure);
  font-size: 12px;
`;
