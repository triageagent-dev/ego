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
  // The eval set builder is reached from a result's "save as eval set"; its tab is gone, creating one is a section of
  // the evaluate page.
  { key: '2', label: 'evaluate', view: 'upload', also: ['dashboard', 'inspector', 'comparison', 'builder'] },
  { key: '3', label: 'runs', view: 'runs' },
  { key: '4', label: 'ego', view: 'quality' },
  { key: '5', label: 'usage', view: 'usage' },
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
  // The tabs were called "tokens" and "quality" before; old links keep working.
  const label = name === 'tokens' ? 'usage' : name === 'quality' ? 'ego' : name;
  return TABS.find((t) => t.label === label)?.view ?? null;
}

/** A link target that opens the sessions tab on one session, expanded. */
export function sessionLink(sessionId: string): string {
  return `#sessions?session=${encodeURIComponent(sessionId)}`;
}
