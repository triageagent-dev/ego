import { useEffect, useRef, useSyncExternalStore } from 'react';
import { config } from '../config';

// The UI's link to the ego server. One websocket (/ws/ui-updates) is open for as long as the app is: it carries
// the live session events, and the server sends a heartbeat on it, so a link that went quiet is noticed from the
// socket alone, with no polling. Only while the socket is down is the server asked (GET /api/health, once per
// attempt) whether it answers at all, which tells a dead server from a feed that is merely reconnecting.
//  server: whether the server answers;
//  feed:   the websocket.
export type ServerState = 'unknown' | 'up' | 'down';
export type FeedState = 'connecting' | 'open' | 'closed';
export type Level = 'ok' | 'warn' | 'down';

interface Snapshot {
  server: ServerState;
  feed: FeedState;
  version: string;
  checkedAt: number;
}

let snap: Snapshot = { server: 'unknown', feed: 'connecting', version: '', checkedAt: 0 };
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

export function connectionLevel(s: Snapshot): Level {
  if (s.server === 'down') return 'down';
  if (s.feed === 'open') return 'ok';
  return 'warn';
}

export function describe(s: Snapshot): string {
  if (s.server === 'down') return 'server unreachable';
  if (s.feed === 'connecting') return 'connecting to the live feed';
  if (s.feed === 'closed') return 'live feed disconnected, reconnecting';
  return s.version ? `connected to ego ${s.version}` : 'connected';
}

// The server sends a heartbeat every 15 s; a socket that has said nothing for three of them is dropped and redone.
const STALE_MS = 45_000;
const PROBE_TIMEOUT_MS = 4_000;
const MAX_BACKOFF_MS = 10_000;
// A socket that has neither opened nor closed by then is stuck; closing it hands over to the reconnect in onclose.
const CONNECT_TIMEOUT_MS = 10_000;

let ws: WebSocket | null = null;
let started = false;
let backoff = 1_000;
let retryTimer: ReturnType<typeof setTimeout> | null = null;
let watchTimer: ReturnType<typeof setTimeout> | null = null;
// When the open socket last said anything; 0 until its first message (the heartbeat counts), so a socket that
// opened and then stays silent is judged from openedAt.
let lastSeen = 0;
let openedAt = 0;

const messageListeners = new Set<(data: any) => void>();
const openListeners = new Set<() => void>();

/** Every event the server sends on the feed, parsed; the heartbeat is not passed on. */
export function onFeedMessage(fn: (data: any) => void): () => void {
  messageListeners.add(fn);
  return () => messageListeners.delete(fn);
}

/** The feed (re)connected: events may have been missed meanwhile, so read the current state again. */
export function onFeedOpen(fn: () => void): () => void {
  openListeners.add(fn);
  return () => openListeners.delete(fn);
}

async function health(): Promise<{ version?: string } | null> {
  const ctl = new AbortController();
  const t = setTimeout(() => ctl.abort(), PROBE_TIMEOUT_MS);
  try {
    const res = await fetch(config.api.endpoints.health, { signal: ctl.signal, cache: 'no-store' });
    if (!res.ok) return null;
    return ((await res.json()) as { data?: { version?: string } }).data ?? {};
  } catch {
    return null;
  } finally {
    clearTimeout(t);
  }
}

// Counts the health probes started on close; an answer from an older probe, or one that lands after the socket
// reopened, must not overwrite the state the live socket set.
let probeSeq = 0;

// Whether the open socket has been silent for longer than it may be.
function stale(): boolean {
  return Date.now() - (lastSeen || openedAt) > STALE_MS;
}

// Silence is the one thing no message can announce, so each message (the heartbeat included) re-arms a single
// dead-man timer that drops the socket when the next one does not come.
function armWatch(socket: WebSocket) {
  if (watchTimer) clearTimeout(watchTimer);
  watchTimer = setTimeout(() => {
    watchTimer = null;
    if (ws === socket && socket.readyState === WebSocket.OPEN) socket.close();
  }, STALE_MS);
}

function disarmWatch() {
  if (watchTimer) clearTimeout(watchTimer);
  watchTimer = null;
}

function scheduleRetry() {
  retryTimer = setTimeout(() => {
    retryTimer = null;
    connect();
  }, backoff);
  backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
}

function connect() {
  if (!started || ws) return;
  set({ feed: 'connecting' });
  let socket: WebSocket;
  try {
    socket = new WebSocket(config.websocket.uiUpdatesWs);
  } catch {
    // a malformed URL or a policy refusal throws instead of closing
    set({ feed: 'closed', checkedAt: Date.now() });
    scheduleRetry();
    return;
  }
  ws = socket;
  lastSeen = 0;
  openedAt = 0;
  const connectTimer = setTimeout(() => {
    if (ws === socket && socket.readyState === WebSocket.CONNECTING) socket.close();
  }, CONNECT_TIMEOUT_MS);

  // A handler of a socket that was replaced or stopped must not touch the current state.
  socket.onopen = () => {
    clearTimeout(connectTimer);
    if (ws !== socket) return;
    probeSeq++;
    backoff = 1_000;
    openedAt = Date.now();
    armWatch(socket);
    set({ server: 'up', feed: 'open', checkedAt: Date.now() });
    openListeners.forEach((l) => l());
    void health().then((h) => h?.version && set({ version: h.version }));
  };

  socket.onmessage = (event) => {
    if (ws !== socket) return;
    lastSeen = Date.now();
    armWatch(socket);
    let data: any;
    try {
      data = JSON.parse(event.data);
    } catch {
      return;
    }
    if (data?.type === 'heartbeat') return;
    messageListeners.forEach((l) => l(data));
  };

  // onclose follows every failure, including a connection that never opened, so reconnecting lives here only.
  socket.onclose = () => {
    clearTimeout(connectTimer);
    if (ws !== socket) return;
    ws = null;
    disarmWatch();
    if (!started) return;
    set({ feed: 'closed', checkedAt: Date.now() });
    const probe = ++probeSeq;
    void health().then((h) => {
      if (!started || probe !== probeSeq) return;
      set({ server: h ? 'up' : 'down', checkedAt: Date.now() });
    });
    scheduleRetry();
  };
}

function reconnectNow() {
  if (!started || ws) return;
  if (retryTimer) {
    clearTimeout(retryTimer);
    retryTimer = null;
  }
  backoff = 1_000;
  connect();
}

// Opens the feed and keeps it open until the returned function is called. Safe to call twice.
export function startConnectionMonitor(): () => void {
  if (started) return () => {};
  started = true;
  connect();

  const onVisible = () => {
    if (document.visibilityState !== 'visible') return;
    if (ws?.readyState === WebSocket.OPEN && stale()) ws.close();
    else reconnectNow();
  };
  const onOnline = () => reconnectNow();
  const onOffline = () => ws?.close();
  window.addEventListener('online', onOnline);
  window.addEventListener('offline', onOffline);
  document.addEventListener('visibilitychange', onVisible);

  return () => {
    started = false;
    disarmWatch();
    if (retryTimer) clearTimeout(retryTimer);
    retryTimer = null;
    window.removeEventListener('online', onOnline);
    window.removeEventListener('offline', onOffline);
    document.removeEventListener('visibilitychange', onVisible);
    const socket = ws;
    ws = null;
    socket?.close();
    snap = { server: 'unknown', feed: 'connecting', version: snap.version, checkedAt: 0 };
  };
}

/** Runs load now, whenever the server says data of the topic(s) changed, and after the feed reconnects or resyncs.
 * load may change (its filters did): it then runs again at once. Replaces a refresh timer and its button.
 * A change that arrives while load is running is not lost and not stacked: load runs once more when it ends, so
 * continuous ingest costs one refetch per answer. load gets a function that says whether its call is still the
 * newest; an older, slower answer must be dropped rather than shown over the newer one. */
export function useLiveData(topic: string | string[], load: (current: () => boolean) => unknown): void {
  const latest = useRef(load);
  latest.current = load;
  const seq = useRef(0);
  const busy = useRef(false);
  const again = useRef(false);
  const run = useRef(async () => {
    if (busy.current) {
      again.current = true;
      return;
    }
    busy.current = true;
    try {
      do {
        again.current = false;
        const mine = ++seq.current;
        await latest.current(() => mine === seq.current);
      } while (again.current);
    } finally {
      busy.current = false;
    }
  });
  useEffect(() => {
    // The old call, if one is running, is already out of date.
    seq.current++;
    void run.current();
  }, [load]);
  const topics = ([] as string[]).concat(topic).join('|');
  useEffect(() => {
    const wanted = topics.split('|');
    const offMessage = onFeedMessage((data) => {
      if (data?.type === 'resync' || (data?.type === 'changed' && wanted.includes(data.topic))) void run.current();
    });
    const offOpen = onFeedOpen(() => void run.current());
    return () => {
      offMessage();
      offOpen();
    };
  }, [topics]);
}

function subscribe(l: () => void) {
  listeners.add(l);
  return () => listeners.delete(l);
}

export function useConnection(): Snapshot {
  return useSyncExternalStore(subscribe, () => snap);
}
