import { useState, type ReactNode } from 'react';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { PrettyText } from './PrettyText';

// User and agent messages open by default and collapse one by one (a prompt can be thousands of words); collapsed,
// the header shows a one-line preview and the length. Text goes through PrettyText.
function MessageBlock({ label, color, text, extra }: { label: string; color: string; text: string; extra?: ReactNode }) {
  const [open, setOpen] = useState(true);
  const preview = text.replace(/\s+/g, ' ').trim();
  return (
    <div style={{ marginBottom: '12px' }}>
      <div
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onClick={() => setOpen(o => !o)}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); setOpen(o => !o); } }}
        style={{ display: 'flex', alignItems: 'center', gap: '8px', cursor: 'pointer', userSelect: 'none', minWidth: 0 }}
      >
        {open ? <ChevronDown size={14} style={{ flexShrink: 0, color: 'var(--text-tertiary)' }} /> : <ChevronRight size={14} style={{ flexShrink: 0, color: 'var(--text-tertiary)' }} />}
        <span style={{
          fontSize: '10px',
          color,
          fontWeight: 700,
          textTransform: 'uppercase' as const,
          letterSpacing: '0.5px',
          display: 'flex',
          alignItems: 'center',
          gap: '6px',
          flexShrink: 0,
        }}>
          {label}
          {extra}
        </span>
        {!open && preview && (
          <span style={{ fontSize: '12px', color: 'var(--text-secondary)', overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', minWidth: 0, flex: 1 }}>
            {preview}
          </span>
        )}
        <span style={{ fontSize: '10px', color: 'var(--text-tertiary)', flexShrink: 0, marginLeft: open ? 'auto' : 0 }}>
          {text.length.toLocaleString()} chars
        </span>
      </div>
      {open && (
        <div style={{
          fontSize: '12.5px',
          color: 'var(--text-primary)',
          lineHeight: '1.5',
          marginTop: '6px',
          paddingLeft: '18px',
        }}>
          <PrettyText text={text} />
        </div>
      )}
    </div>
  );
}

interface UserMessageProps {
  text: string;
  timestamp: number;
}

export function UserMessage({ text }: UserMessageProps) {
  return <MessageBlock label="User" color="#a79ff0" text={text} />;
}

interface ToolCallMessageProps {
  name: string;
  args: Record<string, any>;
  timestamp: number;
  latencyMs?: number;
  resultBytes?: number;
}

function formatToolLatency(ms: number): string {
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(1)}s`;
}

export function ToolCallMessage({ name, args, latencyMs, resultBytes }: ToolCallMessageProps) {
  const argsStr = Object.keys(args).length > 0
    ? Object.keys(args).map(k => `${k}=${JSON.stringify(args[k])}`).join(', ')
    : '';

  return (
    <div style={{
      marginBottom: '2px',
      paddingLeft: '12px',
      display: 'flex',
      alignItems: 'baseline',
      gap: '8px',
    }}>
      <div style={{
        fontSize: '12px',
        color: '#b5abfc',
        fontFamily: 'monospace',
        fontWeight: 500,
      }}>
        → {name}({argsStr})
      </div>
      {(latencyMs != null || resultBytes != null) && (
        <span style={{
          fontSize: '10px',
          color: 'var(--text-tertiary)',
          fontFamily: 'monospace',
          whiteSpace: 'nowrap',
        }}>
          {latencyMs != null && formatToolLatency(latencyMs)}
          {latencyMs != null && resultBytes != null && ' · '}
          {resultBytes != null && `${resultBytes.toLocaleString()}B`}
        </span>
      )}
    </div>
  );
}

const TRUNCATE_LENGTH = 120;

interface ToolResultMessageProps {
  response: Record<string, any>;
  isError?: boolean;
  timestamp: number;
}

export function ToolResultMessage({ response, isError }: ToolResultMessageProps) {
  const [expanded, setExpanded] = useState(false);
  const jsonStr = JSON.stringify(response);
  const needsTruncation = jsonStr.length > TRUNCATE_LENGTH;
  const displayStr = !expanded && needsTruncation
    ? jsonStr.slice(0, TRUNCATE_LENGTH) + '\u2026'
    : JSON.stringify(response, null, expanded ? 2 : undefined);

  const color = isError ? '#ef4444' : '#10b981';

  return (
    <div style={{
      marginBottom: '12px',
      paddingLeft: '12px',
    }}>
      <div
        style={{
          fontSize: '11px',
          color,
          fontFamily: 'monospace',
          fontWeight: 400,
          cursor: needsTruncation ? 'pointer' : 'default',
          whiteSpace: expanded ? 'pre-wrap' : 'nowrap',
          overflow: 'hidden',
          textOverflow: 'ellipsis',
        }}
        onClick={needsTruncation ? () => setExpanded(e => !e) : undefined}
        title={needsTruncation ? (expanded ? 'Click to collapse' : 'Click to expand') : undefined}
      >
        ← {displayStr}
      </div>
    </div>
  );
}

interface AgentMessageProps {
  text: string;
  timestamp: number;
  isStreaming?: boolean;
}

export function AgentMessage({ text, isStreaming }: AgentMessageProps) {
  if (!text) {
    return (
      <div style={{ marginBottom: '12px', fontSize: '10px', color: '#10b981', fontWeight: 700, textTransform: 'uppercase' as const, letterSpacing: '0.5px' }}>
        Agent <em style={{ color: 'var(--text-tertiary)', textTransform: 'none', fontWeight: 400 }}>Thinking...</em>
      </div>
    );
  }
  return (
    <MessageBlock
      label="Agent"
      color="#10b981"
      text={text}
      extra={isStreaming ? <span style={{
        display: 'inline-block',
        width: '6px',
        height: '6px',
        borderRadius: '50%',
        background: '#10b981',
        animation: 'pulse 1.5s ease-in-out infinite',
      }} /> : undefined}
    />
  );
}
