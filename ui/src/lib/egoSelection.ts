import { useSyncExternalStore } from 'react';
import { store, stored } from './stored';

// What the ego page and the sidebar both show: the window of the score and the agent it is the score of. One place
// holds it, so the number in the tile, in the sidebar and in the turns block is the same number. It is remembered
// per browser.

export const EGO_RANGES: { label: string; hours: number }[] = [
  { label: '24h', hours: 24 },
  { label: '7d', hours: 24 * 7 },
  { label: '30d', hours: 24 * 30 },
];

interface Selection {
  hours: number;
  // The agent and tenant as the Activity view keys them; empty until one is picked.
  agent: string;
}

function read(): Selection {
  const h = Number(stored('ego.quality.hours'));
  return { hours: EGO_RANGES.some((r) => r.hours === h) ? h : 24, agent: stored('ego.activity.agent') || '' };
}

let current = read();
const listeners = new Set<() => void>();

function set(next: Selection) {
  current = next;
  store('ego.quality.hours', String(next.hours));
  store('ego.activity.agent', next.agent);
  listeners.forEach((l) => l());
}

export const setEgoHours = (hours: number) => set({ ...current, hours });
export const setEgoAgent = (agent: string) => set({ ...current, agent });

export function useEgoSelection(): Selection {
  return useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => current,
  );
}
