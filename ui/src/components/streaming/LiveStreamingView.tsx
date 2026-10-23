import { useEffect, useRef, useState } from 'react';
import { useTraceContext } from '../../context/TraceContext';
import { SessionCard } from './SessionCard';
import { SessionDetails } from './SessionDetails';
import { config } from '../../config';
import { onFeedMessage, onFeedOpen } from '../../lib/connection';
import { parseHash, sessionLink } from '../shell/routes';
import { CursorPager } from '../ui/Pager';
import { useSelectedTenant } from '../../lib/tenant';
import { store, stored } from '../../lib/stored';
import { SessionEvalSetFile, SessionTraceFile } from '../../lib/sessionFiles';
import type { ConversationElement, LiveSession, StreamingInvocation } from '../../lib/types';
import { ThreadsPanel } from './ThreadsPanel';
import { ActivityPanel } from './ActivityPanel';
import { PageTitle } from '../shell/PageTitle';
import { subtitleStyle, titleStyle } from '../ui/page';

type TimeRangeKey = '1h' | '6h' | '12h' | 'day' | '7d' | 'all';

// Finished sessions per page: cards are tall, so a page of 50 reads as endless scrolling.
const SESSIONS_PAGE = 20;

const TIME_RANGES: Array<{ key: TimeRangeKey; label: string; ms: number | null }> = [
  { key: '1h', label: '1h', ms: 60 * 60 * 1000 },
  { key: '6h', label: '6h', ms: 6 * 60 * 60 * 1000 },
  { key: '12h', label: '12h', ms: 12 * 60 * 60 * 1000 },
  { key: 'day', label: 'Day', ms: 24 * 60 * 60 * 1000 },
  { key: '7d', label: '7d', ms: 7 * 24 * 60 * 60 * 1000 },
  { key: 'all', label: 'All', ms: null },
];

function invocationsToElements(invocations: StreamingInvocation[]): ConversationElement[] {
  return invocations.flatMap((inv, idx) => {
    const elements: ConversationElement[] = [];
    const base = idx * 1000;
    let seq = 0;

    if (inv.userText) {
      elements.push({
        type: 'user_input',
        timestamp: base + seq++,
        invocationId: inv.invocationId,
        data: { text: inv.userText },
      });
    }
    for (const tc of inv.toolCalls || []) {
      elements.push({
        type: 'tool_call',
        timestamp: base + seq++,
        invocationId: inv.invocationId,
        data: { toolCall: tc },
      });
      const tr = inv.toolResponses?.find(r => r.id === tc.id || r.name === tc.name);
      if (tr) {
        elements.push({
          type: 'tool_result',
          timestamp: base + seq++,
          invocationId: inv.invocationId,
          data: { response: tr.response, isError: !!tr.response?.isError },
        });
      }
    }
    if (inv.agentText) {
      elements.push({
        type: 'agent_response',
        timestamp: base + seq++,
        invocationId: inv.invocationId,
        data: { text: inv.agentText },
      });
    }
    return elements;
  });
}

export function LiveStreamingView() {
  const { state, actions } = useTraceContext();
  const activeSessions = state.streamingSessions;
  const setActiveSessions = actions.setStreamingSessions;
  // The search text is remembered per browser, with the toggles and the tenant below; the range always starts at
  // 1h, since a wide one lists thousands of sessions.
  const [timeRange, setTimeRange] = useState<TimeRangeKey>('1h');
  const pickedTenant = useSelectedTenant();
  const [searchQuery, setSearchQuery] = useState(() => stored('ego.search') ?? '');
  // The thread whose id was put in the search box by a click (a part chip, the threads list), and the list on show:
  // sessions, or the threads they are parts of.
  const [threadSearch, setThreadSearch] = useState('');
  // Sessions, the threads they are parts of, or the activity (turns over time) of one thread.
  const [listMode, setListMode] = useState<'sessions' | 'threads' | 'activity'>('sessions');
  const [activityThread, setActivityThread] = useState('');
  const showThreads = listMode === 'threads';
  const searchThread = (thread: string) => {
    setThreadSearch(thread);
    setSearchQuery(thread);
    setListMode('sessions');
    setFocusId(null);
  };
  const [debouncedQuery, setDebouncedQuery] = useState(() => stored('ego.search') ?? '');
  useEffect(() => store('ego.search', searchQuery), [searchQuery]);
  // A session linked to by id (#sessions?session=...): shown alone and opened.
  const [focusId, setFocusId] = useState<string | null>(() => parseHash(location.hash).params.get('session'));
  // The session whose details are open in the side panel (a double click on its row).
  const [openId, setOpenId] = useState<string | null>(null);
  const toggleOpen = (id: string) => setOpenId((cur) => (cur === id ? null : id));
  useEffect(() => {
    if (!openId) return;
    const onKey = (e: KeyboardEvent) => {
      const el = e.target as HTMLElement | null;
      if (e.defaultPrevented) return;
      if (e.key === 'Escape' && !(el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT'))) setOpenId(null);
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [openId]);
  // A session linked to by id opens at once.
  useEffect(() => {
    if (focusId) setOpenId(focusId);
  }, [focusId]);
  // A tenant switch starts clean: the page, the open panel, the golden pick and the pager belong to the tenant
  // they were made in, and a failed first fetch of the new tenant must not leave the old one's rows on show.
  const firstTenantRef = useRef(true);
  useEffect(() => {
    if (firstTenantRef.current) {
      firstTenantRef.current = false;
      return;
    }
    setPageIds([]);
    setNextCursor(null);
    setListTotal(null);
    setOpenId(null);
    setSelectedGoldenId(null);
  }, [pickedTenant]);
  const loadSessionRef = useRef<(id: string) => void>(() => {});
  const fetchPageRef = useRef<(cursor: string) => void>(() => {});
  // The list is paged by the server: the ids of the page on show, and the cursor of the next one.
  const [pageIds, setPageIds] = useState<string[]>([]);
  const [nextCursor, setNextCursor] = useState<string | null>(null);
  // While a page is on its way nextCursor is still the previous answer's, so the pager must not use it.
  const [pageLoading, setPageLoading] = useState(false);
  // How many sessions match the filters: counted by the server with the first page, kept while paging on.
  const [listTotal, setListTotal] = useState<{ n: number; approx: boolean } | null>(null);
  // Sessions whose trace made no LLM call (a server's per-request or
  // per-event trace) are hidden unless asked for; the choice is remembered
  // per browser.
  const [showNoLLM, setShowNoLLM] = useState<boolean>(() => {
    try {
      return localStorage.getItem('ego.showNoLLM') === '1';
    } catch {
      return false;
    }
  });
  const toggleShowNoLLM = () => {
    setShowNoLLM(v => {
      try {
        localStorage.setItem('ego.showNoLLM', v ? '0' : '1');
      } catch {
        // storage unavailable: the toggle still works for this page view
      }
      return !v;
    });
  };
  // Only the sessions online evaluation scored: the ones with judge scores to read and label. Remembered per
  // browser like the non-LLM choice.
  const [scoredOnly, setScoredOnly] = useState<boolean>(() => {
    try {
      return localStorage.getItem('ego.scoredOnly') === '1';
    } catch {
      return false;
    }
  });
  const toggleScoredOnly = () => {
    setScoredOnly(v => {
      try {
        localStorage.setItem('ego.scoredOnly', v ? '0' : '1');
      } catch {
        // storage unavailable: the toggle still works for this page view
      }
      return !v;
    });
  };
  // What the server is asked for. A change of any of these starts again at the newest page.
  // A search for a session id (one long word) looks everywhere: a session that began days ago, has no LLM call or is
  // not scored is still the one asked for, so the range and those filters do not apply to it.
  // So is the search a part chip or the threads list started: the parts of one thread span days.
  const byId = /^\S{16,}$/.test(debouncedQuery) || (debouncedQuery !== '' && debouncedQuery === threadSearch);
  const listParams = (() => {
    const p = new URLSearchParams({ limit: String(SESSIONS_PAGE) });
    const ms = TIME_RANGES.find(r => r.key === timeRange)?.ms;
    if (ms && !byId) p.set('since', `${ms / 3_600_000}h`);
    if (pickedTenant) p.set('tenant', pickedTenant.endsWith('/*') ? pickedTenant.slice(0, -1) : pickedTenant);
    if (debouncedQuery) p.set('q', debouncedQuery);
    if (!showNoLLM && !byId) p.set('llm', 'true');
    if (scoredOnly && !byId) p.set('scored', 'true');
    return p;
  })();
  const listKey = listParams.toString();
  const listParamsRef = useRef(listParams);
  // cursors[i] is the cursor that fetched page i; the first page has none.
  const [stack, setStack] = useState<{ key: string; cursors: string[] }>({ key: listKey, cursors: [''] });
  const cursors = stack.key === listKey ? stack.cursors : [''];
  const pageIdx = cursors.length - 1;
  const cursorsRef = useRef(cursors);
  // The feed handlers read the current filter and page through refs; synced before the fetch effect below runs.
  useEffect(() => {
    listParamsRef.current = listParams;
    cursorsRef.current = cursors;
  });
  const goToPage = (next: string[]) => setStack({ key: listKey, cursors: next });
  const [selectedGoldenId, setSelectedGoldenId] = useState<string | null>(null);
  // Shown under the Continue button: an alert() is easy to miss, and a browser can be told to hide them.
  const [evaluationError, setEvaluationError] = useState<string | null>(null);
  // Sessions explicitly opted in as extra comparison traces alongside the
  // golden run. Empty by default - evaluating just the golden session on its
  // own (e.g. with a judge-only metric like safety_v1) must not silently
  // drag in every other session the user has ever captured.
  const [comparisonSessionIds, setComparisonSessionIds] = useState<Set<string>>(new Set());

  const toggleComparisonSession = (sessionId: string) => {
    setComparisonSessionIds(prev => {
      const next = new Set(prev);
      if (next.has(sessionId)) next.delete(sessionId);
      else next.add(sessionId);
      return next;
    });
  };

  // Opt-ins are scoped to the current golden pick - switching (or clearing)
  // the golden session clears any comparison sessions chosen for the old one.
  useEffect(() => {
    setComparisonSessionIds(new Set());
  }, [selectedGoldenId]);

  const mountedRef = useRef(true);
  // What the user removed from the view. A re-fetch of the session list (on connect, or on a resync after dropped
  // events) must not bring it back. Lives for this page view.
  const [removedIds, setRemovedIds] = useState<Set<string>>(new Set());
  // Only the live sessions are listed. A toggle like the others, remembered per browser, so it can be turned off.
  const [hideDone, setHideDone] = useState(() => stored('ego.hideDone') === '1');
  const toggleHideDone = () =>
    setHideDone((v) => {
      store('ego.hideDone', v ? '' : '1');
      return !v;
    });

  useEffect(() => {
    mountedRef.current = true;

    type ServerSession = {
      sessionId: string;
      traceId: string;
      evalSetId: string | null;
      spanCount: number;
      isComplete: boolean;
      startedAt: string;
      completedAt?: string | null;
      metadata: Record<string, unknown>;
      invocations?: StreamingInvocation[];
      rootSpanName?: string;
      agents?: string[];
      tenants?: string[];
      models?: string[];
      llmCalls?: number;
      errors?: number;
      turns?: number;
      inputTokens?: number;
      outputTokens?: number;
      durationMs?: number;
      scored?: boolean;
      thread?: string;
      part?: number;
    };

    // Folds server summaries into the shared session map: unknown ones are added, known ones take the server's
    // identities and, when their completion event was missed, the server's completion.
    const mergeSessions = (sessions: ServerSession[]) => {
      setActiveSessions(prev => {
        const newMap = new Map(prev);
        for (const s of sessions) {
          let existing = newMap.get(s.sessionId);
          if (existing) {
            // The live counters only hear what arrives while the page is open; the server's totals cover the whole
            // session, so they are the floor the counters never fall under.
            const live = existing.liveStats;
            const inTokens = Math.max(live?.totalInputTokens ?? 0, s.inputTokens ?? 0);
            const outTokens = Math.max(live?.totalOutputTokens ?? 0, s.outputTokens ?? 0);
            if (live && (inTokens !== live.totalInputTokens || outTokens !== live.totalOutputTokens)) {
              existing = { ...existing, liveStats: { ...live, totalInputTokens: inTokens, totalOutputTokens: outTokens } };
              newMap.set(s.sessionId, existing);
            }
            if (s.scored && !existing.scored) {
              existing = { ...existing, scored: true };
              newMap.set(s.sessionId, existing);
            }
            if (s.thread && (s.thread !== existing.thread || s.part !== existing.part)) {
              existing = { ...existing, thread: s.thread, part: s.part };
              newMap.set(s.sessionId, existing);
            }
            if (s.agents?.join() !== existing.agents?.join() || s.models?.join() !== existing.models?.join() || s.tenants?.join() !== existing.tenants?.join()) {
              newMap.set(s.sessionId, { ...existing, agents: s.agents, models: s.models, tenants: s.tenants });
            }
            // Events for this session may have been dropped: take the
            // server's completion rather than staying "active" forever.
            if (existing.status === 'active' && s.isComplete) {
              newMap.set(s.sessionId, {
                ...existing,
                status: 'complete',
                agents: s.agents,
                tenants: s.tenants,
                models: s.models,
                invocations: s.invocations,
                liveElements: s.invocations?.length
                  ? invocationsToElements(s.invocations)
                  : existing.liveElements,
                completedAt: s.completedAt,
                rootSpanName: s.rootSpanName,
                llmCalls: s.llmCalls,
                errors: s.errors,
                turns: s.turns,
                durationMs: s.durationMs,
              });
            }
            continue;
          }
          newMap.set(s.sessionId, {
            sessionId: s.sessionId,
            traceId: s.traceId,
            evalSetId: s.evalSetId,
            spans: [],
            status: s.isComplete ? 'complete' : 'active',
            metadata: (s.metadata ?? {}) as Record<string, string>,
            invocations: s.invocations,
            liveElements: s.invocations?.length
              ? invocationsToElements(s.invocations)
              : [],
            liveStats: { totalInputTokens: s.inputTokens ?? 0, totalOutputTokens: s.outputTokens ?? 0 },
            startedAt: s.startedAt,
            completedAt: s.completedAt,
            rootSpanName: s.rootSpanName,
            agents: s.agents,
            tenants: s.tenants,
            models: s.models,
            llmCalls: s.llmCalls,
            errors: s.errors,
            turns: s.turns,
            durationMs: s.durationMs,
            scored: s.scored,
            thread: s.thread,
            part: s.part,
          });
        }
        return newMap;
      });
    };

    // Fetches just one session (read back from the archive if it left memory) and shows it even if it was
    // cleared or removed from this view before.
    const fetchExistingSessions = async (onlyId: string) => {
      try {
        const res = await fetch(`${config.api.endpoints.streamingSessions}?id=${encodeURIComponent(onlyId)}`);
        if (!res.ok) return;
        const envelope = await res.json();
        if (!mountedRef.current) return;
        mergeSessions(envelope.data ?? []);
      } catch {
        // backend not ready yet - will retry on next SSE connect
      }
    };

    // One page of the list, from the server: it filters, orders and cuts the page, so the browser holds a page
    // however many sessions the server has. A later answer for an older request is dropped.
    let pageSeq = 0;
    const fetchPage = async (cursor: string) => {
      const seq = ++pageSeq;
      setPageLoading(true);
      try {
        const params = new URLSearchParams(listParamsRef.current);
        if (cursor) params.set('cursor', cursor);
        const res = await fetch(`${config.api.endpoints.streamingSessions}?${params}`);
        if (!res.ok) return;
        const envelope = await res.json();
        if (!mountedRef.current || seq !== pageSeq) return;
        const sessions: ServerSession[] = envelope.data ?? [];
        mergeSessions(sessions);
        setPageIds(sessions.map(s => s.sessionId));
        setNextCursor(envelope.next ?? null);
        if (typeof envelope.total === 'number') setListTotal({ n: envelope.total, approx: !!envelope.totalApprox });
      } catch {
        // backend not ready yet - will retry on next SSE connect
      } finally {
        if (mountedRef.current && seq === pageSeq) setPageLoading(false);
      }
    };

    // One re-fetch for a burst of resync signals or unknown-session events. Only the newest page follows the
    // live feed; a page further back is a stable position and is left as it is.
    let resyncTimer: ReturnType<typeof setTimeout> | null = null;
    loadSessionRef.current = (id) => void fetchExistingSessions(id);
    fetchPageRef.current = (cursor) => void fetchPage(cursor);
    const requestResync = () => {
      if (resyncTimer || cursorsRef.current.length > 1) return;
      resyncTimer = setTimeout(() => {
        resyncTimer = null;
        fetchPage('');
      }, 1000);
    };

    // The socket itself is the app's (lib/connection.ts); this view takes the events while it is on screen.
    const handleMessage = (data: any) => {
      if (!mountedRef.current) return;
      if (import.meta.env.DEV) {
        console.log('[Streaming] Received event:', data.type, data);
      }

      switch (data.type) {
        case 'session_started':
          if (import.meta.env.DEV) {
            console.log('[Streaming] New session started:', data.session.sessionId);
          }
          setActiveSessions(prev => {
            const newMap = new Map(prev);
            newMap.set(data.session.sessionId, {
              sessionId: data.session.sessionId,
              traceId: data.session.traceId,
              evalSetId: data.session.evalSetId,
              spans: [],
              status: 'active',
              metadata: data.session.metadata || {},
              liveElements: [],
              liveStats: {
                totalInputTokens: 0,
                totalOutputTokens: 0,
              },
              startedAt: data.session.startedAt,
              rootSpanName: data.session.rootSpanName,
              agents: data.session.agents,
              tenants: data.session.tenants,
              models: data.session.models,
              llmCalls: data.session.llmCalls,
              durationMs: data.session.durationMs,
              thread: data.session.thread,
              part: data.session.part,
            });
            return newMap;
          });
          requestResync();
          break;

        case 'resync':
          // The server dropped events for this connection.
          requestResync();
          break;

        case 'user_input':
          setActiveSessions(prev => {
            const session = prev.get(data.sessionId);
            if (!session) {
              requestResync();
              return prev;
            }

            const newMap = new Map(prev);
            newMap.set(data.sessionId, {
              ...session,
              liveElements: [
                ...session.liveElements,
                {
                  type: 'user_input',
                  timestamp: data.timestamp,
                  invocationId: data.invocationId,
                  data: { text: data.text },
                },
              ],
            });
            return newMap;
          });
          break;

        case 'tool_call':
          setActiveSessions(prev => {
            const session = prev.get(data.sessionId);
            if (!session) {
              requestResync();
              return prev;
            }

            const newMap = new Map(prev);
            newMap.set(data.sessionId, {
              ...session,
              liveElements: [
                ...session.liveElements,
                {
                  type: 'tool_call',
                  timestamp: data.timestamp,
                  invocationId: data.invocationId,
                  data: { toolCall: data.toolCall },
                },
              ],
            });
            return newMap;
          });
          break;

        case 'tool_result':
          setActiveSessions(prev => {
            const session = prev.get(data.sessionId);
            if (!session) {
              requestResync();
              return prev;
            }

            const newMap = new Map(prev);
            newMap.set(data.sessionId, {
              ...session,
              liveElements: [
                ...session.liveElements,
                {
                  type: 'tool_result',
                  timestamp: data.timestamp,
                  invocationId: data.invocationId,
                  data: { response: data.response, isError: data.isError },
                },
              ],
            });
            return newMap;
          });
          break;

        case 'agent_response':
          setActiveSessions(prev => {
            const session = prev.get(data.sessionId);
            if (!session) {
              requestResync();
              return prev;
            }

            const newMap = new Map(prev);
            newMap.set(data.sessionId, {
              ...session,
              liveElements: [
                ...session.liveElements,
                {
                  type: 'agent_response',
                  timestamp: data.timestamp,
                  invocationId: data.invocationId,
                  data: { text: data.text },
                },
              ],
            });
            return newMap;
          });
          break;

        case 'token_update':
          if (import.meta.env.DEV) {
            console.log('[Streaming] Token update:', data);
          }
          setActiveSessions(prev => {
            const session = prev.get(data.sessionId);
            // The list holds one page; a session that is not on it (older, another tenant, outside the range) is not an error.
            if (!session) return prev;

            const newStats = {
              ...session.liveStats,
              totalInputTokens: session.liveStats.totalInputTokens + (data.inputTokens || 0),
              totalOutputTokens: session.liveStats.totalOutputTokens + (data.outputTokens || 0),
              ...(data.model && data.model !== 'unknown' ? { model: data.model } : {}),
            };

            if (import.meta.env.DEV) {
              console.log('[Streaming] New stats:', newStats);
            }

            const newMap = new Map(prev);
            newMap.set(data.sessionId, {
              ...session,
              liveStats: newStats,
            });
            return newMap;
          });
          break;

        case 'session_removed':
          if (import.meta.env.DEV) {
            console.log('[Streaming] Session removed:', data.sessionId, 'absorbed by:', data.absorbedBy);
          }
          setActiveSessions(prev => {
            const newMap = new Map(prev);
            newMap.delete(data.sessionId);
            return newMap;
          });
          break;

        case 'session_complete':
          if (import.meta.env.DEV) {
            console.log('[Streaming] Session complete with invocations:', data.invocations?.length);
          }
          setActiveSessions(prev => {
            const session = prev.get(data.sessionId);

            const newMap = new Map(prev);

            if (!session) {
              if (import.meta.env.DEV) {
                console.warn('[Streaming] Session not found, creating it now:', data.sessionId);
              }
              newMap.set(data.sessionId, {
                sessionId: data.sessionId,
                traceId: 'unknown',
                evalSetId: null,
                spans: [],
                status: 'complete',
                metadata: {},
                invocations: data.invocations,
                liveElements: data.invocations?.length
                  ? invocationsToElements(data.invocations)
                  : [],
                liveStats: {
                  totalInputTokens: 0,
                  totalOutputTokens: 0,
                },
                startedAt: new Date().toISOString(),
                completedAt: data.completedAt,
                rootSpanName: data.rootSpanName,
                llmCalls: data.llmCalls,
                errors: data.errors,
                turns: data.turns,
                durationMs: data.durationMs,
              });
            } else {
              newMap.set(data.sessionId, {
                ...session,
                status: 'complete',
                invocations: data.invocations,
                liveElements: data.invocations?.length
                  ? invocationsToElements(data.invocations)
                  : session.liveElements,
                completedAt: data.completedAt,
                rootSpanName: data.rootSpanName,
                llmCalls: data.llmCalls,
                errors: data.errors,
                turns: data.turns,
                durationMs: data.durationMs,
              });
            }

            return newMap;
          });
          // The agent, model and tenant badges come from the spans seen so far; the last spans of a session
          // (a voice session's model turns) arrive after it started, so take the final set from the server.
          requestResync();
          break;

      }
    };

    const offMessage = onFeedMessage(handleMessage);
    // After the feed reconnects, events may have been missed: read the page again.
    const offOpen = onFeedOpen(() => fetchPage(cursorsRef.current[cursorsRef.current.length - 1]));

    return () => {
      offMessage();
      offOpen();
      mountedRef.current = false;
      if (resyncTimer) clearTimeout(resyncTimer);
    };
  }, []);

  useEffect(() => {
    const onHash = () => setFocusId(parseHash(location.hash).params.get('session'));
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);

  // The box filters on the server, so typing waits for a pause before asking.
  useEffect(() => {
    const t = setTimeout(() => setDebouncedQuery(searchQuery.trim()), 300);
    return () => clearTimeout(t);
  }, [searchQuery]);

  // A new page, or a new filter: ask the server. Declared after the feed effect, so fetchPageRef is set.
  useEffect(() => {
    fetchPageRef.current(cursors[cursors.length - 1]);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [listKey, cursors.join('|')]);

  // Declared after the feed effect above, so loadSessionRef is already set when this first runs.
  useEffect(() => {
    if (!focusId) return;
    loadSessionRef.current(focusId);
  }, [focusId]);

  // The evaluate page is given the sessions by id: the server already holds their traces, so nothing is downloaded
  // here and nothing is uploaded later (see lib/sessionFiles.ts).
  const handleContinueToEvaluation = () => {
    if (!selectedGoldenId) return;
    setEvaluationError(null);

    if (!activeSessions.get(selectedGoldenId)?.invocations?.length) {
      setEvaluationError('This session has no conversation to use as the golden reference.');
      return;
    }

    // The golden session plus whatever was checked "Compare". It is plotted as a reference in the charts and tagged
    // via goldenTraceId so the results view keeps it out of pass/fail scoring (scoring it against the eval set
    // derived from itself is a trivial pass). With nothing to compare it against it is not tagged: that is the
    // single-session flow, e.g. a reference-free judge metric like safety_v1, which needs its only row in the table.
    const sessionIds = Array.from(new Set([selectedGoldenId, ...comparisonSessionIds]))
      .filter((id) => activeSessions.get(id)?.invocations?.length);
    const goldenTraceId = comparisonSessionIds.size > 0 ? (activeSessions.get(selectedGoldenId)?.traceId ?? null) : null;

    actions.setEvaluationOrigin('streaming');
    actions.setTraceFiles(sessionIds.map((id) => new SessionTraceFile(id)));
    actions.setEvalSet(new SessionEvalSetFile(selectedGoldenId));
    actions.setGoldenTraceId(goldenTraceId);
    actions.setCurrentView('upload');
  };

  const byStartedAtDesc = (a: LiveSession, b: LiveSession) =>
    (Date.parse(b.startedAt) || 0) - (Date.parse(a.startedAt) || 0);

  // The page on show, as the server cut it. What was cleared or removed from this view stays out when the page is
  // fetched again.
  const pageSessions = pageIds
    .map(id => activeSessions.get(id))
    .filter((s): s is LiveSession => !!s)
    .filter(s => !removedIds.has(s.sessionId));
  // A linked session is shown alone, whatever the time range, search and non-LLM filter say.
  const focused = focusId ? Array.from(activeSessions.values()).filter(s => s.sessionId === focusId) : null;
  const openSession = openId && !removedIds.has(openId) ? activeSessions.get(openId) : undefined;
  const sessions = focused ?? pageSessions;
  const activeLiveSessions = sessions
    .filter(s => s.status === 'active')
    .sort(byStartedAtDesc);
  const completedSessions = sessions
    .filter(s => s.status === 'complete' && (!hideDone || byId))
    .sort(byStartedAtDesc);
  const allSessions = [...activeLiveSessions, ...completedSessions];
  const renderPager = (inline: boolean) => !focused && !hideDone && (pageIdx > 0 || nextCursor) ? (
    <CursorPager
      inline={inline}
      page={pageIdx}
      total={listTotal?.n}
      approx={listTotal?.approx}
      pageSize={SESSIONS_PAGE}
      hasNext={!!nextCursor}
      onFirst={() => goToPage([''])}
      onPrev={() => goToPage(cursors.slice(0, -1))}
      onNext={() => nextCursor && !pageLoading && nextCursor !== cursors[pageIdx] && goToPage([...cursors, nextCursor])}
    />
  ) : null;
  // In the filters row at the top, and again below the list, where the next page is wanted.
  const pagerInRow = renderPager(true);
  const pager = renderPager(false);

  const listBody = allSessions.length === 0 ? (
    <>
    <p style={{ margin: 0, fontSize: '12px', color: 'var(--text-muted)' }}>
      {hideDone ? 'no live sessions, the finished ones are hidden' : debouncedQuery || pickedTenant
        ? 'no sessions match'
        : timeRange === 'all'
          ? 'no sessions'
          : 'no sessions in this range'}
      {hideDone && (
        <button
          onClick={toggleHideDone}
          style={{ marginLeft: '10px', padding: 0, border: 'none', background: 'none', color: 'var(--accent-primary)', font: 'inherit', cursor: 'pointer' }}
        >
          show finished
        </button>
      )}
      {!hideDone && timeRange !== 'all' && (
        <button
          onClick={() => setTimeRange('all')}
          style={{
            marginLeft: '10px',
            padding: 0,
            border: 'none',
            background: 'none',
            color: 'var(--accent-primary)',
            font: 'inherit',
            cursor: 'pointer',
          }}
        >
          show all
        </button>
      )}
    </p>
    </>
  ) : (
    <div>
      {activeLiveSessions.length > 0 && (
        <div>
          <h2 style={{
            fontSize: '12.5px',
            fontWeight: 400,
            color: 'var(--accent-cyan)',
            marginBottom: '6px',
            display: 'flex',
            alignItems: 'center',
            gap: '8px',
          }}>
            live ({activeLiveSessions.length})
          </h2>
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: '6px', marginBottom: '14px' }}>
            {activeLiveSessions.map(session => (
              <SessionCard
                key={session.sessionId}
                isOpen={openId === session.sessionId}
                onSearchThread={searchThread}
                onOpen={() => toggleOpen(session.sessionId)}
                session={session}
                isSelected={selectedGoldenId === session.sessionId}
                onSelect={() => setSelectedGoldenId(
                  selectedGoldenId === session.sessionId ? null : session.sessionId
                )}
              />
            ))}
          </div>
        </div>
      )}

      {completedSessions.length > 0 && (
        <div>
          {/* The heading only tells finished sessions from live ones; alone, the list starts level with the side panel. */}
          {activeLiveSessions.length > 0 && (
            <h2 style={{
              fontSize: '12.5px',
              fontWeight: 400,
              color: 'var(--accent-cyan)',
              marginBottom: '6px',
              display: 'flex',
              alignItems: 'center',
              gap: '8px',
            }}>
              done
            </h2>
          )}
          <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: '6px' }}>
            {completedSessions.map(session => (
              <SessionCard
                key={session.sessionId}
                isOpen={openId === session.sessionId}
                onSearchThread={searchThread}
                onOpen={() => toggleOpen(session.sessionId)}
                session={session}
                isSelected={selectedGoldenId === session.sessionId}
                onSelect={() => setSelectedGoldenId(
                  selectedGoldenId === session.sessionId ? null : session.sessionId
                )}
                onRemove={() => {
                  setRemovedIds(prev => new Set(prev).add(session.sessionId));
                  actions.removeSession(session.sessionId);
                  if (openId === session.sessionId) setOpenId(null);
                  if (selectedGoldenId === session.sessionId) setSelectedGoldenId(null);
                  setComparisonSessionIds(prev => {
                    if (!prev.has(session.sessionId)) return prev;
                    const next = new Set(prev);
                    next.delete(session.sessionId);
                    return next;
                  });
                }}
                compareOption={
                  selectedGoldenId && selectedGoldenId !== session.sessionId
                    ? { checked: comparisonSessionIds.has(session.sessionId), onToggle: () => toggleComparisonSession(session.sessionId) }
                    : undefined
                }
              />
            ))}
          </div>
        </div>
      )}
      {pager}
    </div>
  );

  // Sessions or threads: a thread is the id an agent keeps (for weeks, if it is always on), and its sessions are the
  // parts ego scores.
  const listSwitch = (
    <span style={{ display: 'inline-flex', gap: '2px', padding: '1px', borderRadius: 'var(--radius)', border: '1px solid var(--border-default)' }}>
      {(['sessions', 'threads', 'activity'] as const).map((label) => (
        <button
          key={label}
          onClick={() => setListMode(label)}
          aria-pressed={listMode === label}
          style={{
            padding: '1px 9px',
            borderRadius: 'var(--radius)',
            border: 'none',
            background: listMode === label ? 'var(--bg-selected)' : 'transparent',
            color: listMode === label ? 'var(--accent-magenta)' : 'var(--text-tertiary)',
            fontSize: '12px',
            cursor: 'pointer',
          }}
        >
          {label}
        </button>
      ))}
    </span>
  );

  if (listMode === 'activity') {
    return (
      <div style={{ padding: '12px 18px 28px' }}>
        <PageTitle style={{ display: 'flex', alignItems: 'baseline', gap: '14px', flexWrap: 'wrap', marginBottom: '8px' }}>
          <h1 css={titleStyle}>activity</h1>
          <p css={subtitleStyle}>how an always-on agent works, turn by turn</p>
          {listSwitch}
        </PageTitle>
        <ActivityPanel
          initialThread={activityThread || undefined}
          onOpenSession={(id) => {
            // The sessions list shows a linked session alone: leave the activity page for it, and say which.
            setListMode('sessions');
            setFocusId(id);
            location.hash = sessionLink(id);
          }}
        />
      </div>
    );
  }

  if (showThreads) {
    return (
      <div style={{ padding: '12px 18px 28px' }}>
        <PageTitle style={{ display: 'flex', alignItems: 'baseline', gap: '14px', flexWrap: 'wrap', marginBottom: '8px' }}>
          <h1 css={titleStyle}>threads</h1>
          <p css={subtitleStyle}>the ids your agents keep; each is cut into the sessions that get scored</p>
          {listSwitch}
        </PageTitle>
        <ThreadsPanel onPick={searchThread} onActivity={(thread) => { setActivityThread(thread); setListMode('activity'); }} />
      </div>
    );
  }

  return (
    <div style={{
      padding: '12px 18px 28px',
    }}>
      <div style={{
        marginBottom: '12px',
      }}>
        <PageTitle style={{ display: 'flex', alignItems: 'baseline', gap: '14px', flexWrap: 'wrap', marginBottom: '8px' }}>
          <h1 css={titleStyle}>sessions</h1>
          <p css={subtitleStyle}>pick a golden session, then evaluate the rest</p>
          {listSwitch}
        </PageTitle>

        <div style={{
          display: 'flex',
          gap: '10px',
          alignItems: 'center',
          justifyContent: 'flex-end',
          flexWrap: 'wrap',
        }}>
          {focusId && (
            <span style={{ fontSize: '12px', color: 'var(--text-tertiary)' }}>
              showing one session{' '}
              <button
                onClick={() => { location.hash = '#sessions'; }}
                style={{ padding: 0, border: 'none', background: 'none', color: 'var(--accent-primary)', font: 'inherit', cursor: 'pointer' }}
              >
                show all
              </button>
            </span>
          )}
          {pagerInRow}
          <button
            onClick={toggleHideDone}
            aria-pressed={hideDone}
            title="List only the live sessions"
            style={{
              padding: '3px 9px',
              borderRadius: 'var(--radius)',
              border: `1px solid ${hideDone ? 'var(--accent-primary)' : 'var(--border-default)'}`,
              background: 'transparent',
              color: hideDone ? 'var(--text-primary)' : 'var(--text-tertiary)',
              fontSize: '12px',
              cursor: 'pointer',
              whiteSpace: 'nowrap',
            }}
          >
            hide done
          </button>
          <button
            onClick={toggleShowNoLLM}
            aria-pressed={showNoLLM}
            title="Sessions whose trace made no LLM call, such as a server's per-event traces"
            style={{
              padding: '3px 9px',
              borderRadius: 'var(--radius)',
              border: `1px solid ${showNoLLM ? 'var(--accent-primary)' : 'var(--border-default)'}`,
              background: 'transparent',
              color: showNoLLM ? 'var(--text-primary)' : 'var(--text-tertiary)',
              fontSize: '12px',
              cursor: 'pointer',
              whiteSpace: 'nowrap',
            }}
          >
            {showNoLLM ? 'hide non-LLM' : 'show non-LLM'}
          </button>
          <button
            onClick={toggleScoredOnly}
            aria-pressed={scoredOnly}
            title="Only sessions online evaluation scored: their judge scores can be read and labelled"
            style={{
              padding: '3px 9px',
              borderRadius: 'var(--radius)',
              border: `1px solid ${scoredOnly ? 'var(--accent-primary)' : 'var(--border-default)'}`,
              background: 'transparent',
              color: scoredOnly ? 'var(--text-primary)' : 'var(--text-tertiary)',
              fontSize: '12px',
              cursor: 'pointer',
              whiteSpace: 'nowrap',
            }}
          >
            scored only
          </button>



          <div style={{
            display: 'flex',
            gap: '2px',
            padding: '1px',
            borderRadius: 'var(--radius)',
            border: '1px solid var(--border-default)',
          }}>
            {TIME_RANGES.map(range => (
              <button
                key={range.key}
                onClick={() => setTimeRange(range.key)}
                style={{
                  padding: '2px 9px',
                  borderRadius: 'var(--radius)',
                  border: 'none',
                  background: timeRange === range.key ? 'var(--bg-selected)' : 'transparent',
                  color: timeRange === range.key ? 'var(--accent-magenta)' : 'var(--text-tertiary)',
                  fontFamily: 'var(--font-mono)',
                  fontSize: '12px',
                  cursor: 'pointer',
                  transition: 'all 0.15s',
                }}
              >
                {range.label}
              </button>
            ))}
          </div>
          <input
            type="text"
            value={searchQuery}
            onChange={e => setSearchQuery(e.target.value)}
            onKeyDown={e => { if (e.key === 'Escape') setSearchQuery(''); }}
            placeholder="Search sessions…"
            aria-label="Search sessions"
            style={{
              padding: '3px 9px',
              borderRadius: 'var(--radius)',
              border: '1px solid var(--border-default)',
              background: 'transparent',
              color: 'var(--text-primary)',
              fontFamily: 'var(--font-mono)',
              fontSize: '12px',
              width: '170px',
              outline: 'none',
            }}
          />
        </div>
      </div>

      {selectedGoldenId && (
        <div style={{
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'space-between',
          gap: '16px',
          padding: '10px 14px',
          marginBottom: '12px',
          background: 'var(--card-bg)',
          border: '1px solid var(--border-default)',
          borderRadius: 'var(--radius)',
        }}>
          <div style={{ fontSize: '14px', color: 'var(--text-secondary)' }}>
            <span style={{ color: 'var(--text-primary)' }}>
              {comparisonSessionIds.size > 0
                ? `golden session and ${comparisonSessionIds.size} to compare`
                : 'golden session selected'}
            </span>
            {comparisonSessionIds.size === 0 && ' · tick compare on other sessions to score them against it'}
            {evaluationError && (
              <div role="alert" style={{ fontSize: '13px', color: 'var(--status-failure)', marginTop: '4px' }}>
                {evaluationError}
              </div>
            )}
          </div>

          <button
            onClick={handleContinueToEvaluation}
            style={{
              height: '30px',
              padding: '0 16px',
              borderRadius: 'var(--radius)',
              background: 'var(--accent-primary)',
              border: 'none',
              color: '#fff',
              fontSize: '14px',
              fontWeight: 500,
              cursor: 'pointer',
              whiteSpace: 'nowrap',
            }}
          >
            evaluate →
          </button>
        </div>
      )}


      <div style={{ display: 'flex', alignItems: 'flex-start', gap: '14px' }}>
        {/* With the details open the list keeps a fixed, narrow column and the details take the rest. */}
        <div style={openSession ? { flex: '0 0 460px', minWidth: 0 } : { flex: 1, minWidth: 0 }}>{listBody}</div>
        {openSession && (
          <aside style={{
            flex: 1,
            minWidth: '420px',
            position: 'sticky',
            top: 0,
            maxHeight: 'calc(var(--view-h, 100vh) - 70px)',
            overflowY: 'auto',
            padding: '12px 14px',
            background: 'var(--card-bg)',
            border: '1px solid var(--border-default)',
            borderRadius: 'var(--radius)',
          }}>
            <div style={{ display: 'flex', alignItems: 'flex-start', gap: '10px', marginBottom: '12px' }}>
              <div style={{ flex: 1, minWidth: 0, fontSize: '13px', color: 'var(--text-secondary)', wordBreak: 'break-all' }}>
                <span style={{ color: 'var(--text-tertiary)' }}>session </span>
                <span style={{ fontFamily: 'monospace', userSelect: 'all' }}>{openSession.sessionId}</span>
                {openSession.thread && ((openSession.part ?? 1) > 1 || openSession.thread !== openSession.sessionId) && (
                  <div title="The sender's own id for the work this session is a part of">
                    <span style={{ color: 'var(--text-tertiary)' }}>thread </span>
                    <button
                      onClick={() => searchThread(openSession.thread!)}
                      title="List every part of this thread"
                      style={{ padding: 0, border: 'none', background: 'none', color: 'var(--accent-primary)', font: 'inherit', fontFamily: 'monospace', cursor: 'pointer', wordBreak: 'break-all', textAlign: 'left' }}
                    >
                      {openSession.thread}
                    </button>
                    <span style={{ color: 'var(--text-tertiary)' }}> · part {openSession.part ?? 1}</span>
                  </div>
                )}
              </div>
              <button
                onClick={() => setOpenId(null)}
                title="Close (Esc)"
                aria-label="Close details"
                style={{
                  flexShrink: 0,
                  padding: '0 8px',
                  border: '1px solid var(--border-default)',
                  borderRadius: 'var(--radius)',
                  background: 'transparent',
                  color: 'var(--text-tertiary)',
                  fontSize: '15px',
                  lineHeight: '22px',
                  cursor: 'pointer',
                }}
              >
                ×
              </button>
            </div>
            <SessionDetails key={openSession.sessionId} session={openSession} />
          </aside>
        )}
      </div>
    </div>
  );
}
