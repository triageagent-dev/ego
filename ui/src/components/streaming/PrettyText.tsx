// Readable rendering for
// long LLM prompts and answers. Keeps line breaks, pretty-prints JSON, and
// renders the light Markdown prompts use (#/##/### headings, - and 1.
// lists, **bold**, `code`, ``` fences). Builds React nodes only - never
// injects HTML.
import type { ReactNode } from 'react';

const codeStyle = {
  fontFamily: 'monospace',
  fontSize: '0.9em',
  background: 'var(--bg-primary)',
  borderRadius: 'var(--radius)',
  padding: '1px 5px',
};

// **bold** and `code` inside one line.
function inline(text: string, keyBase: string): ReactNode[] {
  const out: ReactNode[] = [];
  const re = /(\*\*[^*]+\*\*|`[^`]+`)/g;
  let last = 0;
  let m: RegExpExecArray | null;
  let i = 0;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const tok = m[0];
    out.push(tok.startsWith('**')
      ? <strong key={`${keyBase}-${i++}`}>{tok.slice(2, -2)}</strong>
      : <code key={`${keyBase}-${i++}`} style={codeStyle}>{tok.slice(1, -1)}</code>);
    last = m.index + tok.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return out;
}

function asJSON(text: string): string | null {
  const t = text.trim();
  if (!(t.startsWith('{') || t.startsWith('['))) return null;
  try {
    return JSON.stringify(JSON.parse(t), null, 2);
  } catch {
    return null;
  }
}

// Indents text that starts like JSON but does not parse: a value cut short by an attribute limit, or one followed by
// more text, still reads as a tree. Strings are kept as written; `rest` is whatever follows the top-level value.
export function lenientJSON(text: string): { pretty: string; rest: string } | null {
  const t = text.trim();
  if (!/^(\{\s*"|\[\s*[[{"])/.test(t)) return null;
  const pad = (depth: number) => '\n' + '  '.repeat(depth);
  let out = '';
  let depth = 0;
  let inString = false;
  let escaped = false;
  let i = 0;
  for (; i < t.length; i++) {
    const c = t[i];
    if (inString) {
      out += c;
      if (escaped) escaped = false;
      else if (c === '\\') escaped = true;
      else if (c === '"') inString = false;
      continue;
    }
    if (c === '"') {
      inString = true;
      out += c;
    } else if (c === '{' || c === '[') {
      let j = i + 1;
      while (j < t.length && /\s/.test(t[j])) j++;
      if (t[j] === (c === '{' ? '}' : ']')) {
        out += c + t[j];
        i = j;
        if (depth === 0) {
          i++;
          break;
        }
      } else {
        depth++;
        out += c + pad(depth);
      }
    } else if (c === '}' || c === ']') {
      depth--;
      if (depth < 0) return null;
      out += pad(depth) + c;
      if (depth === 0) {
        i++;
        break;
      }
    } else if (c === ',') {
      out += ',' + pad(depth);
    } else if (c === ':') {
      out += ': ';
    } else if (!/\s/.test(c)) {
      out += c;
    }
  }
  return { pretty: out, rest: t.slice(i).trim() };
}

const preStyle = { ...codeStyle, display: 'block', padding: '10px 12px', margin: 0, whiteSpace: 'pre-wrap', overflowX: 'auto' } as const;

export function PrettyText({ text }: { text: string }) {
  const json = asJSON(text);
  if (json) {
    return <pre style={preStyle}>{json}</pre>;
  }
  const loose = lenientJSON(text);
  if (loose) {
    return (
      <div>
        <pre style={preStyle}>{loose.pretty}</pre>
        {loose.rest && <div style={{ marginTop: '8px' }}><PrettyText text={loose.rest} /></div>}
      </div>
    );
  }

  const blocks: ReactNode[] = [];
  const lines = text.replace(/\r\n/g, '\n').split('\n');
  let para: string[] = [];
  let list: { ordered: boolean; items: string[] } | null = null;

  const flushPara = () => {
    if (!para.length) return;
    const k = `p${blocks.length}`;
    blocks.push(<p key={k} style={{ margin: '0 0 8px' }}>{para.flatMap((l, i) => i ? [<br key={`${k}-br${i}`} />, ...inline(l, `${k}-${i}`)] : inline(l, `${k}-${i}`))}</p>);
    para = [];
  };
  const flushList = () => {
    if (!list) return;
    const k = `l${blocks.length}`;
    const items = list.items.map((it, i) => <li key={`${k}-${i}`} style={{ margin: '2px 0' }}>{inline(it, `${k}-${i}`)}</li>);
    blocks.push(list.ordered
      ? <ol key={k} style={{ margin: '0 0 8px', paddingLeft: '22px' }}>{items}</ol>
      : <ul key={k} style={{ margin: '0 0 8px', paddingLeft: '22px' }}>{items}</ul>);
    list = null;
  };

  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.trim().startsWith('```')) {
      flushPara(); flushList();
      const body: string[] = [];
      for (i++; i < lines.length && !lines[i].trim().startsWith('```'); i++) body.push(lines[i]);
      blocks.push(<pre key={`c${blocks.length}`} style={{ ...codeStyle, display: 'block', padding: '10px 12px', margin: '0 0 8px', whiteSpace: 'pre-wrap', overflowX: 'auto' }}>{body.join('\n')}</pre>);
      continue;
    }
    const h = /^(#{1,6})\s+(.*)$/.exec(line);
    if (h) {
      flushPara(); flushList();
      const size = h[1].length === 1 ? '16px' : h[1].length === 2 ? '15px' : '14px';
      blocks.push(<div key={`h${blocks.length}`} style={{ fontSize: size, fontWeight: 700, margin: '12px 0 6px', color: 'var(--text-primary)' }}>{inline(h[2], `h${blocks.length}`)}</div>);
      continue;
    }
    const li = /^\s*(?:([-*•])|(\d+)[.)])\s+(.*)$/.exec(line);
    if (li) {
      flushPara();
      const ordered = !!li[2];
      if (list && list.ordered !== ordered) flushList();
      if (!list) list = { ordered, items: [] };
      list.items.push(li[3]);
      continue;
    }
    if (!line.trim()) { flushPara(); flushList(); continue; }
    flushList();
    para.push(line);
  }
  flushPara(); flushList();
  return <div>{blocks}</div>;
}
