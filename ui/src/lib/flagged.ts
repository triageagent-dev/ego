import { useSyncExternalStore } from 'react';
import { clearStored, store, stored } from './stored';
import { selectedTenant } from './tenant';

// Sessions the reader flagged for quick access, shown in the sidebar. Kept per browser and per picked tenant (the
// key for "every tenant" is the bare prefix), with the label each had when it was flagged so the sidebar can name
// it without loading it. An eighth flag replaces the oldest.
const KEY = 'ego.flagged';
const CHANGED = 'ego:flagged';
const TENANT_CHANGED = 'ego:tenant';
export const MAX_FLAGGED = 7;

export interface Flag {
  id: string;
  label: string;
}

function keyOf(): string {
  const tenant = selectedTenant();
  return tenant ? `${KEY}:${tenant}` : KEY;
}

function parse(text: string | null): Flag[] {
  try {
    const parsed = JSON.parse(text ?? '[]');
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((f): f is Flag => typeof f?.id === 'string' && typeof f?.label === 'string')
      .slice(-MAX_FLAGGED);
  } catch {
    return [];
  }
}

// The lists in memory are the truth, so flagging works with storage blocked; storage is written through and read
// back only when it changed under us (another tab). useSyncExternalStore compares snapshots by identity, which
// the kept lists give it.
const memory = new Map<string, Flag[]>();

function flagsOf(key: string): Flag[] {
  let flags = memory.get(key);
  if (!flags) {
    flags = parse(stored(key));
    memory.set(key, flags);
  }
  return flags;
}

function snapshot(): Flag[] {
  return flagsOf(keyOf());
}

function write(key: string, flags: Flag[]): void {
  memory.set(key, flags);
  store(key, flags.length ? JSON.stringify(flags) : '');
  window.dispatchEvent(new Event(CHANGED));
}

export function toggleFlag(id: string, label: string): void {
  const key = keyOf();
  const flags = flagsOf(key);
  if (flags.some((f) => f.id === id)) write(key, flags.filter((f) => f.id !== id));
  else write(key, [...flags, { id, label }].slice(-MAX_FLAGGED));
}

/** Forgets every flag of every tenant, for the next person on this browser. */
export function clearFlags(): void {
  memory.clear();
  clearStored(KEY);
  window.dispatchEvent(new Event(CHANGED));
}

function subscribe(onChange: () => void): () => void {
  const onStorage = () => {
    memory.clear();
    onChange();
  };
  window.addEventListener(CHANGED, onChange);
  window.addEventListener(TENANT_CHANGED, onChange);
  window.addEventListener('storage', onStorage);
  return () => {
    window.removeEventListener(CHANGED, onChange);
    window.removeEventListener(TENANT_CHANGED, onChange);
    window.removeEventListener('storage', onStorage);
  };
}

export function useFlagged(): Flag[] {
  return useSyncExternalStore(subscribe, snapshot, () => []);
}
