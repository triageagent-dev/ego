import React, { useEffect, useState } from 'react';
import { css } from '@emotion/react';
import { Button, Checkbox } from 'antd';
import { config } from '../../config';
import { formatStarted, modelsLabel } from '../../lib/trace-helpers';
import { selectedTenant, tenantName, useTenantNames } from '../../lib/tenant';

interface PickableSession {
  sessionId: string;
  startedAt: string;
  isComplete: boolean;
  rootSpanName?: string;
  models?: string[];
  tenants?: string[];
  errors?: number;
}

const PAGE = 30;

// The recent finished sessions the server holds, to score without leaving the page: the list the sessions page shows,
// cut to a short page and searchable, with a tick on each.
export const SessionPicker: React.FC<{ onUse: (sessionIds: string[]) => void }> = ({ onUse }) => {
  const [sessions, setSessions] = useState<PickableSession[] | null>(null);
  const [query, setQuery] = useState('');
  const [ticked, setTicked] = useState<Set<string>>(new Set());
  useTenantNames();

  useEffect(() => {
    let active = true;
    const timer = setTimeout(async () => {
      const params = new URLSearchParams({ limit: String(PAGE), llm: 'true' });
      const tenant = selectedTenant();
      // "<sender>/*" goes as "<sender>/": gateways refuse a "/*" in a query string.
      if (tenant) params.set('tenant', tenant.endsWith('/*') ? tenant.slice(0, -1) : tenant);
      if (query.trim()) params.set('q', query.trim());
      try {
        const res = await fetch(`${config.api.endpoints.streamingSessions}?${params}`);
        const body = res.ok ? await res.json() : { data: [] };
        if (active) setSessions(((body.data ?? []) as PickableSession[]).filter((s) => s.isComplete));
      } catch {
        if (active) setSessions([]);
      }
    }, query ? 300 : 0);
    return () => {
      active = false;
      clearTimeout(timer);
    };
  }, [query]);

  const toggle = (id: string) =>
    setTicked((prev) => {
      const next = new Set(prev);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  return (
    <div css={wrapStyle}>
      <div css={headStyle}>
        <span css={titleStyle}>or pick from sessions</span>
        <input
          css={searchStyle}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Escape') setQuery('');
          }}
          placeholder="Search sessions…"
          aria-label="Search sessions"
        />
      </div>

      {sessions === null && <div css={noteStyle}>Loading sessions…</div>}
      {sessions !== null && sessions.length === 0 && <div css={noteStyle}>No finished sessions{query ? ' match' : ' yet'}.</div>}
      {sessions !== null && sessions.length > 0 && (
        <div css={listStyle}>
          {sessions.map((s) => {
            const models = modelsLabel(s.models ?? []);
            return (
              <label key={s.sessionId} css={[rowStyle, ticked.has(s.sessionId) && rowOnStyle]} title={s.sessionId}>
                <Checkbox checked={ticked.has(s.sessionId)} onChange={() => toggle(s.sessionId)} />
                <span css={whenStyle}>{formatStarted(s.startedAt)}</span>
                {s.tenants?.[0] && <span css={tenantStyle}>{tenantName(s.tenants[0])}</span>}
                <span css={kindStyle}>{s.rootSpanName ?? ''}</span>
                {models && <span css={modelStyle}>{models}</span>}
                {(s.errors ?? 0) > 0 && <span css={errorStyle}>{s.errors} err</span>}
              </label>
            );
          })}
        </div>
      )}

      <div css={footStyle}>
        <Button
          type="primary"
          size="small"
          disabled={ticked.size === 0}
          onClick={() => onUse(Array.from(ticked))}
        >
          Use {ticked.size || ''} session{ticked.size === 1 ? '' : 's'}
        </Button>
        {sessions !== null && sessions.length > 0 && (
          <Button size="small" onClick={() => setTicked(ticked.size ? new Set() : new Set(sessions.map((s) => s.sessionId)))}>
            {ticked.size ? 'Clear' : 'Select all'}
          </Button>
        )}
      </div>
    </div>
  );
};

const wrapStyle = css`
  margin-top: 14px;
`;

const headStyle = css`
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 8px;
`;

const titleStyle = css`
  color: var(--text-muted);
  font-size: 12px;
`;

const searchStyle = css`
  width: 200px;
  padding: 3px 9px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: transparent;
  color: var(--text-primary);
  font-size: 12px;

  &:focus {
    outline: none;
    border-color: var(--accent-primary);
  }
`;

const noteStyle = css`
  padding: 10px 0;
  color: var(--text-secondary);
  font-size: 12px;
`;

const listStyle = css`
  display: flex;
  flex-direction: column;
  gap: 4px;
`;

const rowStyle = css`
  display: flex;
  align-items: center;
  gap: 10px;
  min-width: 0;
  padding: 5px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--card-bg);
  font-size: 12px;
  cursor: pointer;

  &:hover {
    background: var(--bg-elevated);
  }
`;

const rowOnStyle = css`
  border-color: var(--accent-primary);
  background: var(--bg-elevated);
`;

const whenStyle = css`
  flex-shrink: 0;
  color: #d6c28f;
  font-family: var(--font-mono);
`;

const tenantStyle = css`
  flex-shrink: 0;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  padding: 0 6px;
  border-radius: var(--radius);
  background: rgba(167, 159, 240, 0.12);
  color: #b5abfc;
  font-size: 11px;
  line-height: 18px;
`;

const kindStyle = css`
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: #5eead4;
`;

const modelStyle = css`
  flex-shrink: 0;
  max-width: 180px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 11px;
`;

const errorStyle = css`
  flex-shrink: 0;
  color: #ef4444;
`;

const footStyle = css`
  display: flex;
  gap: 8px;
  margin-top: 10px;
`;
