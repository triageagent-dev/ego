import { useSyncExternalStore } from 'react';
import { store, stored } from './stored';

// Sessions the reader flagged for quick access, shown in the sidebar. Kept per browser, with the label each had when
// it was flagged so the sidebar can name it without loading it. A sixth flag replaces the oldest.
const KEY = 'ego.flagged';
const CHANGED = 'ego:flagged';
export const MAX_FLAGGED = 5;

export interface Flag {
  id: string;
  label: string;
}

function read(): Flag[] {
  try {
    const parsed = JSON.parse(stored(KEY) ?? '[]');
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((f): f is Flag => typeof f?.id === 'string' && typeof f?.label === 'string')
      .slice(-MAX_FLAGGED);
  } catch {
    return [];
  }
}

// useSyncExternalStore compares snapshots by identity: keep the parsed list until the stored text changes.
let cachedText: string | null = null;
let cached: Flag[] = [];

function snapshot(): Flag[] {
  const text = stored(KEY);
  if (text !== cachedText) {
    cachedText = text;
    cached = read();
  }
  return cached;
}

function write(flags: Flag[]): void {
  store(KEY, flags.length ? JSON.stringify(flags) : '');
  window.dispatchEvent(new Event(CHANGED));
}

export function toggleFlag(id: string, label: string): void {
  const flags = read();
  if (flags.some((f) => f.id === id)) write(flags.filter((f) => f.id !== id));
  else write([...flags, { id, label }].slice(-MAX_FLAGGED));
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener(CHANGED, onChange);
  window.addEventListener('storage', onChange);
  return () => {
    window.removeEventListener(CHANGED, onChange);
    window.removeEventListener('storage', onChange);
  };
}

export function useFlagged(): Flag[] {
  return useSyncExternalStore(subscribe, snapshot, () => []);
}
