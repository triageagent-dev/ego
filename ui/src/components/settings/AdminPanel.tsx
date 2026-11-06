import React, { useCallback, useState } from 'react';
import { css } from '@emotion/react';
import { Button, Checkbox, Drawer, Input, Modal, Popconfirm, Table, Tabs } from 'antd';
import { Check, Copy } from 'lucide-react';
import { createSender, deleteSender, getAdminOverview, mintSenderToken, revokeSenderToken } from '../../api/client';
import type { AdminOverview, AdminSender, AdminTenant } from '../../lib/types';

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

const TenantsTab: React.FC<{ data: AdminOverview }> = ({ data }) => (
  <>
    <p css={hintStyle}>Every tenant the server knows, with its LLM calls since {new Date(data.since).toISOString().slice(0, 10)}.</p>
    <Table<AdminTenant>
      size="small"
      rowKey="key"
      pagination={false}
      dataSource={data.tenants}
      columns={[
        {
          title: 'tenant',
          render: (_, t) => (
            <div>
              <div>{t.name || t.label}</div>
              <div css={monoStyle}>{t.key}</div>
            </div>
          ),
        },
        { title: 'sessions', dataIndex: 'sessions', align: 'right' },
        { title: 'calls', dataIndex: 'calls', align: 'right', render: (n: number) => short(n) },
        { title: 'errors', dataIndex: 'errors', align: 'right', render: (n: number) => (n ? short(n) : '-') },
        { title: 'tokens in', dataIndex: 'inputTokens', align: 'right', render: (n: number) => short(n) },
        { title: 'tokens out', dataIndex: 'outputTokens', align: 'right', render: (n: number) => short(n) },
        { title: 'last call', dataIndex: 'lastCall', align: 'right', render: (n?: number) => ago(n) },
        { title: 'keys', dataIndex: 'tokens', align: 'right', render: (n: number) => n || '-' },
      ]}
    />
  </>
);

const KeysTab: React.FC<{ data: AdminOverview; reload: () => void }> = ({ data, reload }) => {
  const [newKeyFor, setNewKeyFor] = useState<string | null>(null);
  const [minted, setMinted] = useState<Minted | null>(null);
  const [error, setError] = useState('');

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
  return (
    <>
      <div css={toolbarStyle}>
        <p css={hintStyle}>A key is how a sender's traces are let in. A key bound to a tenant puts everything it sends in that tenant.</p>
        <Button type="primary" size="small" onClick={() => setNewKeyFor('')}>New key</Button>
      </div>
      {error && <div css={errorStyle}>{error}</div>}
      {data.senders.length === 0 && <div css={noteStyle}>No sender yet.</div>}
      {data.senders.map((s: AdminSender) => (
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

/** The admin's view of every tenant and key, opened from the settings page. */
export const AdminPanel: React.FC = () => {
  const [open, setOpen] = useState(false);
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

  return (
    <>
      <Button
        onClick={() => {
          setOpen(true);
          void load();
        }}
      >
        Tenants and keys
      </Button>
      <Drawer open={open} onClose={() => setOpen(false)} width={860} title="tenants and keys" destroyOnClose>
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
      </Drawer>
    </>
  );
};

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
