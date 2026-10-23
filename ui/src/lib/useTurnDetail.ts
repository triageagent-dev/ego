import { useEffect, useState } from 'react';
import { getSessionSpans, type SessionSpan } from '../api/client';
import { resolveTurn, type TurnDetail } from './turnSteps';
import type { TurnRow } from './types';

// The spans of a session are read once for all the turns picked in it, and again after a short while.
const cache = new Map<string, { at: number; spans: Promise<SessionSpan[]> }>();
const CACHE_MS = 15_000;
function spansOf(session: string): Promise<SessionSpan[]> {
  const hit = cache.get(session);
  if (hit && Date.now() - hit.at < CACHE_MS) return hit.spans;
  const spans = getSessionSpans(session);
  cache.set(session, { at: Date.now(), spans });
  spans.catch(() => cache.delete(session));
  return spans;
}

interface Loaded {
  key: string;
  detail: TurnDetail | null;
  error: string | null;
}

// The inside of a turn: undefined while it loads, null when its session no longer holds its spans.
export function useTurnDetail(turn: TurnRow | null): { detail: TurnDetail | null | undefined; error: string | null } {
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const key = turn ? `${turn.session}:${turn.rootSpanId}` : '';
  useEffect(() => {
    if (!turn) return;
    let live = true;
    spansOf(turn.session)
      .then((spans) => live && setLoaded({ key, detail: resolveTurn(spans, turn.rootSpanId) ?? null, error: null }))
      .catch((e: Error) => live && setLoaded({ key, detail: null, error: e.message }));
    return () => { live = false; };
  }, [key, turn]);
  // What was loaded belongs to the turn it was loaded for.
  if (!turn || !loaded || loaded.key !== key) return { detail: undefined, error: null };
  return { detail: loaded.detail, error: loaded.error };
}
