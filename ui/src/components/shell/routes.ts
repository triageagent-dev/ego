import type { ViewType } from '../../lib/types';

export interface Tab {
  key: string;
  label: string;
  view: ViewType;
  // The other views that belong under this tab.
  also?: ViewType[];
}

export const TABS: Tab[] = [
  { key: '1', label: 'sessions', view: 'streaming' },
  { key: '2', label: 'evaluate', view: 'upload', also: ['dashboard', 'inspector', 'comparison'] },
  { key: '3', label: 'runs', view: 'runs' },
  { key: '4', label: 'usage', view: 'usage' },
  { key: '5', label: 'quality', view: 'quality' },
  { key: '6', label: 'sets', view: 'builder' },
];

export function tabForView(view: ViewType): Tab | undefined {
  return TABS.find((t) => t.view === view || t.also?.includes(view));
}

// "#sessions?session=abc" -> { name: "sessions", params: session=abc }
export function parseHash(hash: string): { name: string; params: URLSearchParams } {
  const raw = hash.replace(/^#\/?/, '');
  const q = raw.indexOf('?');
  return {
    name: q < 0 ? raw : raw.slice(0, q),
    params: new URLSearchParams(q < 0 ? '' : raw.slice(q + 1)),
  };
}

export function viewForHash(hash: string): ViewType | null {
  const { name } = parseHash(hash);
  // Settings is not a tab: it opens from the account menu.
  if (name === 'settings') return 'settings';
  // The tab was called "tokens" before; old links keep working.
  return TABS.find((t) => t.label === (name === 'tokens' ? 'usage' : name))?.view ?? null;
}

/** A link target that opens the sessions tab on one session, expanded. */
export function sessionLink(sessionId: string): string {
  return `#sessions?session=${encodeURIComponent(sessionId)}`;
}
