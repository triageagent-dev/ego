import { useSyncExternalStore } from 'react';
import { config } from '../config';

// What the UI knows about its link to the ego server, for the status dot.
//  server: whether GET /api/health answers (polled, so a dead link is noticed even when the browser's
//          websocket has not noticed yet);
//  feed:   the live-feed websocket, only meaningful while the sessions view is open.
export type ServerState = 'unknown' | 'up' | 'down';
export type FeedState = 'idle' | 'connecting' | 'open' | 'closed';
export type Level = 'ok' | 'warn' | 'down';

interface Snapshot {
  server: ServerState;
  feed: FeedState;
  version: string;
  checkedAt: number;
}

let snap: Snapshot = { server: 'unknown', feed: 'idle', version: '', checkedAt: 0 };
const listeners = new Set<() => void>();

function set(next: Partial<Snapshot>) {
  const merged = { ...snap, ...next };
  if (
    merged.server === snap.server &&
    merged.feed === snap.feed &&
    merged.version === snap.version &&
    merged.checkedAt === snap.checkedAt
  ) {
    return;
  }
  snap = merged;
  listeners.forEach((l) => l());
}

export function setFeed(feed: FeedState): void {
  set({ feed });
}

export function connectionLevel(s: Snapshot): Level {
  if (s.server === 'down') return 'down';
  if (s.feed === 'connecting' || s.feed === 'closed') return 'warn';
  return 'ok';
}

export function describe(s: Snapshot): string {
  if (s.server === 'down') return 'server unreachable';
  if (s.feed === 'connecting') return 'connecting to the live feed';
  if (s.feed === 'closed') return 'live feed disconnected, reconnecting';
  if (s.server === 'unknown') return 'checking the server';
  return s.version ? `connected to ego ${s.version}` : 'connected';
}

const POLL_MS = 10_000;
const TIMEOUT_MS = 4_000;
let timer: ReturnType<typeof setInterval> | null = null;
const downListeners = new Set<() => void>();
const upListeners = new Set<() => void>();

async function probe(): Promise<void> {
  const ctl = new AbortController();
  const t = setTimeout(() => ctl.abort(), TIMEOUT_MS);
  const before = snap.server;
  try {
    const res = await fetch(config.api.endpoints.health, { signal: ctl.signal, cache: 'no-store' });
    if (!res.ok) throw new Error(String(res.status));
    const body = (await res.json()) as { data?: { version?: string } };
    set({ server: 'up', version: body.data?.version ?? snap.version, checkedAt: Date.now() });
    if (before === 'down') upListeners.forEach((l) => l());
  } catch {
    set({ server: 'down', checkedAt: Date.now() });
    if (before !== 'down') downListeners.forEach((l) => l());
  } finally {
    clearTimeout(t);
  }
}

// Polls the server while the app is open, and re-checks as soon as the browser reports a network change or
// the tab becomes visible again. Returns a stop function.
export function startConnectionMonitor(): () => void {
  if (timer) return () => {};
  void probe();
  timer = setInterval(() => void probe(), POLL_MS);
  const recheck = () => void probe();
  const onVisible = () => {
    if (document.visibilityState === 'visible') recheck();
  };
  window.addEventListener('online', recheck);
  window.addEventListener('offline', recheck);
  document.addEventListener('visibilitychange', onVisible);
  return () => {
    if (timer) clearInterval(timer);
    timer = null;
    window.removeEventListener('online', recheck);
    window.removeEventListener('offline', recheck);
    document.removeEventListener('visibilitychange', onVisible);
  };
}

// The server stopped answering / started answering again. The live feed uses these to drop a half-open
// socket and to reconnect at once instead of waiting out its backoff.
export function onServerDown(fn: () => void): () => void {
  downListeners.add(fn);
  return () => downListeners.delete(fn);
}

export function onServerUp(fn: () => void): () => void {
  upListeners.add(fn);
  return () => upListeners.delete(fn);
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useConnection(): Snapshot {
  return useSyncExternalStore(subscribe, () => snap);
}
