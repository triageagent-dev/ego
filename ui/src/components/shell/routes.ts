import type { ViewType } from '../../lib/types';

export interface Tab {
  key: string;
  label: string;
  view: ViewType;
  // The other views that belong under this tab.
  also?: ViewType[];
}

export const TABS: Tab[] = [
  { key: '1', label: 'sessions', view: 'streaming', also: ['annotation-queue'] },
  { key: '2', label: 'evaluate', view: 'upload', also: ['dashboard', 'inspector', 'comparison'] },
  { key: '3', label: 'runs', view: 'runs' },
  { key: '4', label: 'tokens', view: 'usage' },
  { key: '5', label: 'quality', view: 'quality' },
  { key: '6', label: 'sets', view: 'builder' },
];

export function tabForView(view: ViewType): Tab | undefined {
  return TABS.find((t) => t.view === view || t.also?.includes(view));
}

export function viewForHash(hash: string): ViewType | null {
  const name = hash.replace(/^#\/?/, '');
  return TABS.find((t) => t.label === name)?.view ?? null;
}
