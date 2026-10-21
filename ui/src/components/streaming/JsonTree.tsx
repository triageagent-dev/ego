// A JSON value as a tree whose objects and arrays fold. Messages often carry JSON (a prompt that is a payload, an
// answer that is a document), and reading it flat means scrolling past what is not needed.
import { useEffect, useState } from 'react';
import { ChevronDown, ChevronRight, ChevronsDownUp, ChevronsUpDown } from 'lucide-react';

export type Json = null | boolean | number | string | Json[] | { [key: string]: Json };

// The two outer levels start open, deeper ones folded.
const OPEN_DEPTH = 2;

// What every node does when "expand all" / "collapse all" is pressed: n changes with each press.
interface Force {
  open: boolean;
  n: number;
}

const mono = { fontFamily: 'monospace', fontSize: '12px', lineHeight: 1.5 } as const;

const iconButton = {
  display: 'inline-flex',
  alignItems: 'center',
  padding: '2px 4px',
  border: '1px solid var(--border-default)',
  borderRadius: 'var(--radius)',
  background: 'transparent',
  color: 'var(--text-tertiary)',
  cursor: 'pointer',
} as const;

function Scalar({ v }: { v: Exclude<Json, Json[] | { [key: string]: Json }> }) {
  if (typeof v === 'string') return <span style={{ color: '#86efac', whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>"{v}"</span>;
  if (typeof v === 'number') return <span style={{ color: '#fb923c' }}>{String(v)}</span>;
  return <span style={{ color: 'var(--text-tertiary)' }}>{String(v)}</span>;
}

function Node({ name, value, depth, force, truncated }: { name?: string; value: Json; depth: number; force: Force; truncated?: boolean }) {
  const container = value !== null && typeof value === 'object';
  const [open, setOpen] = useState(depth < OPEN_DEPTH);
  useEffect(() => {
    if (force.n) setOpen(force.open);
  }, [force]);

  const label = name !== undefined && <span style={{ color: '#a79ff0' }}>{name}: </span>;
  if (!container) {
    return <div style={{ paddingLeft: '16px' }}>{label}<Scalar v={value as never} /></div>;
  }

  const entries: Array<[string | undefined, Json]> = Array.isArray(value)
    ? value.map((v) => [undefined, v])
    : Object.entries(value);
  const [openBr, closeBr] = Array.isArray(value) ? ['[', ']'] : ['{', '}'];
  const Chevron = open ? ChevronDown : ChevronRight;

  return (
    <div>
      <div
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onClick={() => setOpen((o) => !o)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault();
            setOpen((o) => !o);
          }
        }}
        style={{ display: 'flex', alignItems: 'center', gap: '2px', cursor: 'pointer', userSelect: 'none' }}
      >
        <Chevron size={14} style={{ flexShrink: 0, color: 'var(--text-tertiary)' }} />
        {label}
        <span style={{ color: 'var(--text-secondary)' }}>
          {openBr}
          {!open && <span style={{ color: 'var(--text-tertiary)' }}> {entries.length} {Array.isArray(value) ? 'items' : 'keys'}{truncated ? ' …' : ''} </span>}
          {!open && closeBr}
        </span>
      </div>
      {open && (
        <>
          <div style={{ paddingLeft: '16px', borderLeft: '1px solid var(--border-default)', marginLeft: '7px' }}>
            {entries.map(([k, v], i) => (
              <Node key={`${k ?? ''}:${i}`} name={k === undefined ? undefined : `"${k}"`} value={v} depth={depth + 1} force={force} />
            ))}
            {truncated && <div style={{ paddingLeft: '16px', color: 'var(--text-tertiary)' }}>… cut short</div>}
          </div>
          <div style={{ paddingLeft: '16px', color: 'var(--text-secondary)' }}>{closeBr}</div>
        </>
      )}
    </div>
  );
}

export function JsonTree({ value, truncated }: { value: Json; truncated?: boolean }) {
  const [force, setForce] = useState<Force>({ open: true, n: 0 });
  const container = value !== null && typeof value === 'object';
  return (
    <div style={{ ...mono, position: 'relative', background: 'var(--bg-primary)', borderRadius: 'var(--radius)', padding: '8px 10px' }}>
      {container && (
        <div style={{ position: 'absolute', top: 6, right: 6, display: 'flex', gap: '4px' }}>
          <button style={iconButton} title="Expand all" aria-label="Expand all" onClick={() => setForce((f) => ({ open: true, n: f.n + 1 }))}>
            <ChevronsUpDown size={13} />
          </button>
          <button style={iconButton} title="Collapse all" aria-label="Collapse all" onClick={() => setForce((f) => ({ open: false, n: f.n + 1 }))}>
            <ChevronsDownUp size={13} />
          </button>
        </div>
      )}
      <Node value={value} depth={0} force={force} truncated={truncated} />
    </div>
  );
}

// Reads JSON that may be cut short (an attribute limit) or followed by more text: containers still open at the end
// of the input are closed, and `end` is where the top-level value stopped, so the caller can show what follows.
export function parseLooseJSON(text: string): { value: Json; end: number; truncated: boolean } | null {
  let i = 0;
  let truncated = false;
  const ws = () => {
    while (i < text.length && /\s/.test(text[i])) i++;
  };
  const eof = () => i >= text.length;

  const string = (): string => {
    const start = i;
    i++; // opening quote
    let escaped = false;
    for (; i < text.length; i++) {
      const c = text[i];
      if (escaped) escaped = false;
      else if (c === '\\') escaped = true;
      else if (c === '"') {
        i++;
        return JSON.parse(text.slice(start, i)) as string;
      }
    }
    truncated = true;
    return text.slice(start + 1).replace(/\\n/g, '\n').replace(/\\"/g, '"').replace(/\\\\/g, '\\');
  };

  const value = (): Json => {
    ws();
    if (eof()) {
      truncated = true;
      return null;
    }
    const c = text[i];
    if (c === '{') {
      i++;
      const obj: { [key: string]: Json } = {};
      for (;;) {
        ws();
        if (eof()) {
          truncated = true;
          return obj;
        }
        if (text[i] === '}') {
          i++;
          return obj;
        }
        if (text[i] !== '"') throw new Error('key');
        const key = string();
        ws();
        if (eof()) {
          truncated = true;
          return obj;
        }
        if (text[i] !== ':') throw new Error('colon');
        i++;
        obj[key] = value();
        ws();
        if (text[i] === ',') i++;
        else if (text[i] !== '}' && !eof()) throw new Error('separator');
      }
    }
    if (c === '[') {
      i++;
      const arr: Json[] = [];
      for (;;) {
        ws();
        if (eof()) {
          truncated = true;
          return arr;
        }
        if (text[i] === ']') {
          i++;
          return arr;
        }
        arr.push(value());
        ws();
        if (text[i] === ',') i++;
        else if (text[i] !== ']' && !eof()) throw new Error('separator');
      }
    }
    if (c === '"') return string();
    const lit = /^(?:-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?|true|false|null)/.exec(text.slice(i));
    if (!lit) throw new Error('value');
    i += lit[0].length;
    return JSON.parse(lit[0]) as Json;
  };

  const t = text.trim();
  if (!/^(\{\s*"|\[\s*[[{"\d-tfn])/.test(t) && !/^[{[]\s*[}\]]/.test(t)) return null;
  text = t;
  try {
    const v = value();
    // A bracketed number or word with prose after it ("[1] see above", "[0.5] of the budget") is a citation, not an
    // array: an array is JSON only when it is all of the text. An object may still be followed by prose.
    if (t[0] === '[' && text.slice(i).trim()) return null;
    return { value: v, end: i, truncated };
  } catch {
    return null;
  }
}
