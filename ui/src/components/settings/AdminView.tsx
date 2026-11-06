import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { css } from '@emotion/react';
import { Button, Checkbox, Input, Modal, Popconfirm, Select, Table, Tabs } from 'antd';
import { Check, Copy } from 'lucide-react';
import { createSender, deleteSender, getAdminOverview, mintSenderToken, revokeSenderToken } from '../../api/client';
import type { AdminOverview, AdminSender, AdminTenant } from '../../lib/types';
import { PageTitle } from '../shell/PageTitle';
import { TenantTrends } from './TenantTrends';
import { pageStyle, subtitleStyle, titleStyle } from '../ui/page';

// 1,234,567 -> 1.2M
function short(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e3) return `${(n / 1e3).toFixed(1)}k`;
  return String(n);
}

function ago(ms?: number): string {
  if (!ms) return '-';
  const s = Math.max(0, Math.round((Date.now() - ms) / 1000));
  if (s < 90) return `${s}s ago`;
  if (s < 5400) return `${Math.round(s / 60)}m ago`;
  if (s < 129600) return `${Math.round(s / 3600)}h ago`;
  return `${Math.round(s / 86400)}d ago`;
}

const day = (iso: string) => iso.slice(0, 10);

interface Minted {
  sender: string;
  tenant?: string;
  token: string;
}

/** The token is on screen once; ego keeps only its hash. */
const MintedToken: React.FC<{ minted: Minted | null; onClose: () => void }> = ({ minted, onClose }) => {
  const [copied, setCopied] = useState(false);
  return (
    <Modal open={minted !== null} title="ingest token" onCancel={onClose} footer={<Button onClick={onClose}>Done</Button>} destroyOnClose>
      {minted && (
        <>
          <p css={hintStyle}>
            For sender <b>{minted.sender}</b>
            {minted.tenant ? <> and tenant <b>{minted.tenant}</b></> : null}. It is shown once and cannot be read again: put it in a secret store now.
          </p>
          <div css={tokenBoxStyle}>
            <code css={tokenStyle}>{minted.token}</code>
            <Button
              size="small"
              icon={copied ? <Check size={14} /> : <Copy size={14} />}
              onClick={() => {
                void navigator.clipboard.writeText(minted.token);
                setCopied(true);
              }}
            >
              {copied ? 'Copied' : 'Copy'}
            </Button>
          </div>
        </>
      )}
    </Modal>
  );
};

/** A new key: for a sender that exists, or for a new one, which is registered first. */
const NewKey: React.FC<{ senders: string[]; initialSender?: string; onDone: (m: Minted) => void; onCancel: () => void }> = ({ senders, initialSender, onDone, onCancel }) => {
  const [sender, setSender] = useState(initialSender ?? '');
  const [tenant, setTenant] = useState('');
  const [tenantName, setTenantName] = useState('');
  const [label, setLabel] = useState('');
  const [mcp, setMcp] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    setBusy(true);
    setError('');
    try {
      const id = sender.trim();
      if (!senders.includes(id)) await createSender(id);
      const out = await mintSenderToken(id, {
        label: label.trim() || undefined,
        tenant: tenant.trim() || undefined,
        tenantName: tenantName.trim() || undefined,
        mcp,
      });
      onDone({ sender: id, tenant: tenant.trim() || undefined, token: out.token });
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open title="new key" onCancel={onCancel} onOk={submit} okText="Mint" okButtonProps={{ disabled: !sender.trim() || (tenantName.trim() !== '' && tenant.trim() === ''), loading: busy }} destroyOnClose>
      <div css={formStyle}>
        <label>
          <span css={fieldLabelStyle}>Sender</span>
          <Input value={sender} onChange={(e) => setSender(e.target.value)} placeholder="lowercase letters, digits and dashes" list="ego-senders" />
          <datalist id="ego-senders">{senders.map((s) => <option key={s} value={s} />)}</datalist>
          <span css={noteStyle}>{sender.trim() && !senders.includes(sender.trim()) ? 'A new sender: it is registered with this key.' : 'The source of the traces.'}</span>
        </label>
        <label>
          <span css={fieldLabelStyle}>Tenant</span>
          <Input value={tenant} onChange={(e) => setTenant(e.target.value)} placeholder="optional: every trace this key sends belongs to it" />
        </label>
        <label>
          <span css={fieldLabelStyle}>Tenant name</span>
          <Input value={tenantName} onChange={(e) => setTenantName(e.target.value)} placeholder="optional display name, needs a tenant" />
        </label>
        <label>
          <span css={fieldLabelStyle}>Label</span>
          <Input value={label} onChange={(e) => setLabel(e.target.value)} placeholder="what the key is for" />
        </label>
        <Checkbox checked={mcp} onChange={(e) => setMcp(e.target.checked)}>
          May also read back over /mcp
        </Checkbox>
        {error && <div css={errorStyle}>{error}</div>}
      </div>
    </Modal>
  );
};

const tenantCellStyle = css`
  display: flex;
  align-items: flex-start;
  gap: 10px;
`;

const swatchStyle = css`
  flex: none;
  width: 11px;
  height: 11px;
  margin-top: 5px;
  border-radius: 2px;
`;

const byNumber = (pick: (t: AdminTenant) => number) => (a: AdminTenant, b: AdminTenant) => pick(a) - pick(b);
const nameOf = (t: AdminTenant) => (t.name || t.label).toLowerCase();

const TenantsTab: React.FC<{ data: AdminOverview }> = ({ data }) => {
  const [query, setQuery] = useState('');
  const [sender, setSender] = useState<string | undefined>();
  const [withTraffic, setWithTraffic] = useState(false);
  // Each tenant's line colour in the trend chart, so the rows below it are its legend.
  const [colors, setColors] = useState<Map<string, string>>(new Map());

  const senders = useMemo(() => [...new Set(data.tenants.map((t) => t.sender).filter((x): x is string => !!x))].sort(), [data.tenants]);
  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    return data.tenants.filter(
      (t) =>
        (!q || t.key.toLowerCase().includes(q) || (t.name ?? '').toLowerCase().includes(q)) &&
        (!sender || t.sender === sender) &&
        (!withTraffic || t.calls > 0),
    );
  }, [data.tenants, query, sender, withTraffic]);

  return (
    <>
      <p css={hintStyle}>Every tenant the server knows, with its LLM calls since {new Date(data.since).toISOString().slice(0, 10)}. Click a column to sort.</p>
      <div css={filterBarStyle}>
        <Input.Search allowClear placeholder="search name, sender or tenant" value={query} onChange={(e) => setQuery(e.target.value)} style={{ maxWidth: 320 }} />
        <Select allowClear placeholder="all senders" value={sender} onChange={setSender} style={{ minWidth: 170 }} options={senders.map((x) => ({ value: x, label: x }))} />
        <Checkbox checked={withTraffic} onChange={(e) => setWithTraffic(e.target.checked)}>
          with calls in the window
        </Checkbox>
        <span css={noteStyle}>
          {rows.length === data.tenants.length ? `${rows.length} tenants` : `${rows.length} of ${data.tenants.length}`}
        </span>
      </div>
      <TenantTrends tenants={rows} onColors={setColors} />
      <Table<AdminTenant>
        size="small"
        rowKey="key"
        pagination={false}
        dataSource={rows}
        showSorterTooltip={false}
        columns={[
          {
            title: 'tenant',
            sorter: (a, b) => nameOf(a).localeCompare(nameOf(b)),
            render: (_, t) => (
              <div css={tenantCellStyle}>
                <span css={swatchStyle} style={{ background: colors.get(t.key) ?? 'transparent' }} title={colors.has(t.key) ? 'its line in the chart' : 'no data in the chart'} />
                <div>
                  <div>{t.name || t.label}</div>
                  <div css={monoStyle}>{t.key}</div>
                </div>
              </div>
            ),
          },
          { title: 'sessions', dataIndex: 'sessions', align: 'right', sorter: byNumber((t) => t.sessions) },
          { title: 'calls', dataIndex: 'calls', align: 'right', sorter: byNumber((t) => t.calls), defaultSortOrder: 'descend', render: (n: number) => short(n) },
          { title: 'errors', dataIndex: 'errors', align: 'right', sorter: byNumber((t) => t.errors), render: (n: number) => (n ? short(n) : '-') },
          { title: 'tokens in', dataIndex: 'inputTokens', align: 'right', sorter: byNumber((t) => t.inputTokens), render: (n: number) => short(n) },
          { title: 'tokens out', dataIndex: 'outputTokens', align: 'right', sorter: byNumber((t) => t.outputTokens), render: (n: number) => short(n) },
          { title: 'last call', dataIndex: 'lastCall', align: 'right', sorter: byNumber((t) => t.lastCall ?? 0), render: (n?: number) => ago(n) },
          { title: 'keys', dataIndex: 'tokens', align: 'right', sorter: byNumber((t) => t.tokens), render: (n: number) => n || '-' },
        ]}
      />
    </>
  );
};

const KeysTab: React.FC<{ data: AdminOverview; reload: () => void }> = ({ data, reload }) => {
  const [newKeyFor, setNewKeyFor] = useState<string | null>(null);
  const [minted, setMinted] = useState<Minted | null>(null);
  const [error, setError] = useState('');
  const [query, setQuery] = useState('');

  const run = async (action: () => Promise<void>) => {
    setError('');
    try {
      await action();
      reload();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  const ids = data.senders.map((s) => s.id);
  // A sender shows when its id matches, or with only the keys whose label or tenant match.
  const q = query.trim().toLowerCase();
  const shown = data.senders
    .map((s) => (!q || s.id.toLowerCase().includes(q) ? s : { ...s, tokens: s.tokens.filter((t) => `${t.label ?? ''} ${t.tenant ?? ''}`.toLowerCase().includes(q)) }))
    .filter((s) => !q || s.id.toLowerCase().includes(q) || s.tokens.length > 0);
  return (
    <>
      <div css={toolbarStyle}>
        <p css={hintStyle}>A key is how a sender's traces are let in. A key bound to a tenant puts everything it sends in that tenant.</p>
        <Button type="primary" size="small" onClick={() => setNewKeyFor('')}>New key</Button>
      </div>
      <div css={filterBarStyle}>
        <Input.Search allowClear placeholder="search sender, label or tenant" value={query} onChange={(e) => setQuery(e.target.value)} style={{ maxWidth: 320 }} />
        <span css={noteStyle}>{shown.length === data.senders.length ? `${shown.length} senders` : `${shown.length} of ${data.senders.length}`}</span>
      </div>
      {error && <div css={errorStyle}>{error}</div>}
      {data.senders.length === 0 && <div css={noteStyle}>No sender yet.</div>}
      {shown.map((s: AdminSender) => (
        <section key={s.id} css={senderStyle}>
          <header css={senderHeadStyle}>
            <span css={monoStyle}>{s.id}</span>
            <span css={noteStyle}>since {day(s.createdAt)}</span>
            <span css={{ flex: 1 }} />
            <Button size="small" onClick={() => setNewKeyFor(s.id)}>New key</Button>
            <Popconfirm title={`Delete sender ${s.id}?`} description="Its keys stop working. Its sessions stay." okText="Delete" okButtonProps={{ danger: true }} onConfirm={() => run(() => deleteSender(s.id))}>
              <Button size="small" danger>Delete</Button>
            </Popconfirm>
          </header>
          {s.tokens.length === 0 ? (
            <div css={noteStyle}>No key.</div>
          ) : (
            <Table
              size="small"
              rowKey="id"
              pagination={false}
              showHeader={false}
              dataSource={s.tokens}
              columns={[
                { render: (_, t) => <span>{t.label || <span css={noteStyle}>no label</span>}</span> },
                { render: (_, t) => <span css={monoStyle}>{t.tenant ? `tenant ${t.tenant}` : 'any tenant'}</span> },
                { render: (_, t) => (t.mcp ? 'send and read' : 'send only') },
                { render: (_, t) => <span css={noteStyle}>{day(t.createdAt)}</span> },
                {
                  align: 'right',
                  render: (_, t) => (
                    <Popconfirm title="Revoke this key?" description="What uses it is refused at once." okText="Revoke" okButtonProps={{ danger: true }} onConfirm={() => run(() => revokeSenderToken(s.id, t.id))}>
                      <Button size="small" type="link" danger>Revoke</Button>
                    </Popconfirm>
                  ),
                },
              ]}
            />
          )}
        </section>
      ))}
      {newKeyFor !== null && (
        <NewKey
          senders={ids}
          initialSender={newKeyFor || undefined}
          onCancel={() => setNewKeyFor(null)}
          onDone={(m) => {
            setNewKeyFor(null);
            setMinted(m);
            reload();
          }}
        />
      )}
      <MintedToken minted={minted} onClose={() => setMinted(null)} />
    </>
  );
};

/** The admin page: every tenant with its figures, and every sender with its keys. */
export const AdminView: React.FC = () => {
  const [data, setData] = useState<AdminOverview | null>(null);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    try {
      setData(await getAdminOverview());
      setError('');
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  return (
    <div css={pageStyle}>
      <PageTitle>
        <h1 css={titleStyle}>admin</h1>
        <p css={subtitleStyle}>every tenant with its figures, and the keys that let a sender's traces in</p>
      </PageTitle>
      <div css={bodyStyle}>
        {error && <div css={errorStyle}>{error}</div>}
        {data === null && !error && <div css={noteStyle}>Loading…</div>}
        {data && (
          <Tabs
            items={[
              { key: 'tenants', label: `tenants (${data.tenants.length})`, children: <TenantsTab data={data} /> },
              { key: 'keys', label: `keys (${data.senders.reduce((n, s) => n + s.tokens.length, 0)})`, children: <KeysTab data={data} reload={() => void load()} /> },
            ]}
          />
        )}
      </div>
    </div>
  );
};

const bodyStyle = css`
  max-width: 1100px;
`;

const hintStyle = css`
  margin: 0 0 12px;
  color: var(--text-muted);
  font-size: 12px;
`;

const noteStyle = css`
  color: var(--text-muted);
  font-size: 11.5px;
`;

const errorStyle = css`
  margin: 0 0 10px;
  color: var(--status-failure);
  font-size: 12px;
`;

const monoStyle = css`
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: 12px;
`;

const filterBarStyle = css`
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 12px;
  margin-bottom: 12px;
`;

const toolbarStyle = css`
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
`;

const senderStyle = css`
  margin: 0 0 14px;
  padding: 10px 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
`;

const senderHeadStyle = css`
  display: flex;
  align-items: center;
  gap: 10px;
  margin-bottom: 8px;
`;

const formStyle = css`
  display: flex;
  flex-direction: column;
  gap: 12px;
`;

const fieldLabelStyle = css`
  display: block;
  margin-bottom: 3px;
  color: var(--text-secondary);
  font-size: 12px;
`;

const tokenBoxStyle = css`
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius);
  background: var(--bg-elevated);
`;

const tokenStyle = css`
  flex: 1;
  min-width: 0;
  overflow-wrap: anywhere;
  font-family: var(--font-mono);
  font-size: 12px;
`;
