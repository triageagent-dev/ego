import React, { useState } from 'react';
import { css } from '@emotion/react';
import { Button, Input } from 'antd';
import { Plus, X } from 'lucide-react';
import { saveEvalSet } from '../../api/client';
import { validateEvalSet } from '../../lib/evalset-builder';
import type { EvalSet, Invocation } from '../../lib/types';

interface DraftCase {
  user: string;
  answer: string;
  // Tool names, comma separated: the trajectory the agent is expected to follow.
  tools: string;
}

const emptyCase = (): DraftCase => ({ user: '', answer: '', tools: '' });

function invocationOf(c: DraftCase, n: number): Invocation {
  const tools = c.tools.split(',').map((t) => t.trim()).filter(Boolean);
  return {
    invocationId: `inv_${n}`,
    userContent: { role: 'user', parts: [{ text: c.user.trim() }] },
    finalResponse: { role: 'model', parts: [{ text: c.answer.trim() }] },
    intermediateData: { toolUses: tools.map((name) => ({ name, args: {} })), toolResponses: [] },
  };
}

/** A golden set written here: a name and one or more cases (what the user asks, the answer that is right, the tools
 * that should be called). It is saved as an eval set, so it is there next time too. */
export const NewGoldenForm: React.FC<{ onCreated: (evalSetId: string) => void }> = ({ onCreated }) => {
  const [name, setName] = useState('');
  const [cases, setCases] = useState<DraftCase[]>([emptyCase()]);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const change = (i: number, patch: Partial<DraftCase>) =>
    setCases((cs) => cs.map((c, j) => (j === i ? { ...c, ...patch } : c)));

  const create = async () => {
    const filled = cases.filter((c) => c.user.trim());
    const slug = name.trim().toLowerCase().replace(/[^a-z0-9]+/g, '_').replace(/^_|_$/g, '') || 'golden';
    const evalSet: EvalSet = {
      eval_set_id: `${slug}_${Date.now().toString(36)}`,
      name: name.trim(),
      description: 'Written on the evaluate page',
      eval_cases: filled.map((c, i) => ({ eval_id: `case_${i + 1}`, conversation: [invocationOf(c, i + 1)] })),
    };
    const problems = validateEvalSet(evalSet);
    if (problems.length) {
      setError(problems.includes('name is required') ? 'Give the golden set a name.' : 'Write what the user asks in at least one case.');
      return;
    }
    setBusy(true);
    setError('');
    try {
      const saved = await saveEvalSet(evalSet);
      onCreated(saved.evalSetId);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div css={formStyle}>
      <label css={labelStyle}>Name</label>
      <Input size="small" value={name} onChange={(e) => setName(e.target.value)} placeholder="e.g. refund requests" />

      {cases.map((c, i) => (
        <div key={i} css={caseStyle}>
          <div css={caseHeadStyle}>
            <span>case {i + 1}</span>
            {cases.length > 1 && (
              <button css={removeStyle} onClick={() => setCases((cs) => cs.filter((_, j) => j !== i))} title="Remove this case" aria-label="Remove this case">
                <X size={12} />
              </button>
            )}
          </div>
          <label css={labelStyle}>The user asks</label>
          <Input.TextArea rows={2} value={c.user} onChange={(e) => change(i, { user: e.target.value })} placeholder="What the user says" />
          <label css={labelStyle}>A good answer</label>
          <Input.TextArea rows={2} value={c.answer} onChange={(e) => change(i, { answer: e.target.value })} placeholder="What the agent should answer (optional)" />
          <label css={labelStyle}>Tools it should call</label>
          <Input size="small" value={c.tools} onChange={(e) => change(i, { tools: e.target.value })} placeholder="lookup_order, issue_refund (optional, in order)" />
        </div>
      ))}

      <div css={actionsStyle}>
        <Button size="small" icon={<Plus size={12} />} onClick={() => setCases((cs) => [...cs, emptyCase()])}>Add a case</Button>
        <Button size="small" type="primary" loading={busy} onClick={create}>Create and use</Button>
      </div>
      {error && <div css={errorStyle}>{error}</div>}
    </div>
  );
};

const formStyle = css`
  display: flex;
  flex-direction: column;
  gap: 6px;
`;

const labelStyle = css`
  color: var(--text-muted);
  font-size: 11.5px;
`;

const caseStyle = css`
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 8px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-bg);
`;

const caseHeadStyle = css`
  display: flex;
  justify-content: space-between;
  align-items: center;
  color: var(--accent-cyan);
  font-size: 12px;
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
  gap: 8px;
  margin-top: 4px;
`;

const errorStyle = css`
  color: var(--status-failure);
  font-size: 12px;
`;
