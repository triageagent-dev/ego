import { useEffect, useRef, useState } from 'react';
import { useTraceContext } from '../../context/TraceContext';
import { SessionCard } from './SessionCard';
import { config } from '../../config';
import { onServerDown, onServerUp, setFeed } from '../../lib/connection';
import { parseHash } from '../shell/routes';
import { Pager, clampPage } from '../ui/Pager';
import { inTenant, tenantName, useSelectedTenant } from '../../lib/tenant';
import type { ConversationElement, LiveSession, StreamingInvocation } from '../../lib/types';

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
  const [timeRange, setTimeRange] = useState<TimeRangeKey>('1h');
  const [donePage, setDonePage] = useState(0);
  const pickedTenant = useSelectedTenant();
  const [searchQuery, setSearchQuery] = useState('');
  // A session linked to by id (#sessions?session=...): shown alone and opened.
  const [focusId, setFocusId] = useState<string | null>(() => parseHash(location.hash).params.get('session'));
  const loadSessionRef = useRef<(id: string) => void>(() => {});
  const searchSessionsRef = useRef<(q: string) => void>(() => {});
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
  const [selectedGoldenId, setSelectedGoldenId] = useState<string | null>(null);
  const [isPreparingEvaluation, setIsPreparingEvaluation] = useState(false);
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

  const totalQueuedSessions = state.annotationQueues.reduce((sum, q) => sum + q.items.length, 0);

  const handleAddToQueue = (session: LiveSession, queueId: string) => {
    actions.addToAnnotationQueue(queueId, session);
    actions.setCurrentAnnotationQueueId(queueId);
  };

  const handleCreateAndAddToQueue = (session: LiveSession, name: string) => {
    const id = actions.createAnnotationQueue(name);
    actions.addToAnnotationQueue(id, session);
    actions.setCurrentAnnotationQueueId(id);
  };

  const getQueueNamesForSession = (sessionId: string): string[] =>
    state.annotationQueues
      .filter(q => q.items.some(item => item.sessionId === sessionId))
      .map(q => q.name);

  // The live feed is a WebSocket rather than SSE: gateways buffer
  // long-lived SSE responses. See config.ts's uiUpdatesWs and
  // internal/api/wsupdates.go.
  const wsRef = useRef<WebSocket | null>(null);
  const retryTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const retryDelayRef = useRef(1000);
  const mountedRef = useRef(true);
  // What the user cleared or removed from the view. A re-fetch of the
  // session list (on connect, or on a resync after dropped events) must not
  // bring those back. Lives for this page view, as the clear itself does.
  const clearedBeforeRef = useRef<number>(0);
  const removedIdsRef = useRef<Set<string>>(new Set());

  useEffect(() => {
    mountedRef.current = true;

    // With an id, fetches just that session (read back from the archive if it left memory) and shows it even
    // if it was cleared or removed from this view before.
    const fetchExistingSessions = async (onlyId?: string, query?: string) => {
      try {
        const res = await fetch(
          onlyId
            ? `${config.api.endpoints.streamingSessions}?id=${encodeURIComponent(onlyId)}`
            : query
              ? `${config.api.endpoints.streamingSessions}?q=${encodeURIComponent(query)}`
              : config.api.endpoints.streamingSessions,
        );
        if (!res.ok) return;
        const envelope = await res.json();
        const sessions: Array<{
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
          durationMs?: number;
        }> = envelope.data;
        if (!mountedRef.current) return;

        setActiveSessions(prev => {
          const newMap = new Map(prev);
          for (const s of sessions) {
            const existing = newMap.get(s.sessionId);
            if (existing) {
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
                  durationMs: s.durationMs,
                });
              }
              continue;
            }
            if (!onlyId && !query && removedIdsRef.current.has(s.sessionId)) continue;
            if (!onlyId && !query && s.isComplete && Date.parse(s.startedAt) < clearedBeforeRef.current) continue;
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
              liveStats: { totalInputTokens: 0, totalOutputTokens: 0 },
              startedAt: s.startedAt,
              completedAt: s.completedAt,
              rootSpanName: s.rootSpanName,
              agents: s.agents,
              tenants: s.tenants,
              models: s.models,
              llmCalls: s.llmCalls,
              errors: s.errors,
              durationMs: s.durationMs,
            });
          }
          return newMap;
        });
      } catch {
        // backend not ready yet - will retry on next SSE connect
      }
    };

    // One re-fetch for a burst of resync signals or unknown-session events.
    let resyncTimer: ReturnType<typeof setTimeout> | null = null;
    loadSessionRef.current = (id) => void fetchExistingSessions(id);
    searchSessionsRef.current = (q) => void fetchExistingSessions(undefined, q);
    const requestResync = () => {
      if (resyncTimer) return;
      resyncTimer = setTimeout(() => {
        resyncTimer = null;
        fetchExistingSessions();
      }, 1000);
    };

    const connectWS = () => {
      if (!mountedRef.current) return;

      if (import.meta.env.DEV) {
        console.log('[Streaming] Setting up WebSocket connection');
      }
      setFeed('connecting');
      const ws = new WebSocket(config.websocket.uiUpdatesWs);
      wsRef.current = ws;

      ws.onopen = () => {
        if (import.meta.env.DEV) {
          console.log('[Streaming] WebSocket connected');
        }
        retryDelayRef.current = 1000;
        setFeed('open');
        fetchExistingSessions();
      };

      // onclose fires for every disconnection (clean or not, including
      // right after a failed connection's onerror), so reconnect lives
      // here only, not duplicated in onerror.
      ws.onclose = () => {
        if (!mountedRef.current) return;
        setFeed('closed');
        if (import.meta.env.DEV) {
          console.log(`[Streaming] WebSocket closed, reconnecting in ${retryDelayRef.current}ms`);
        }
        scheduleReconnect();
      };

      ws.onerror = () => {
        if (import.meta.env.DEV) {
          console.log('[Streaming] WebSocket error');
        }
      };

      ws.onmessage = (event) => {
        const data = JSON.parse(event.data);
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
              });
              return newMap;
            });
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
              if (!session) {
                console.warn('[Streaming] Token update for unknown session:', data.sessionId);
                return prev;
              }

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
    };

    const scheduleReconnect = () => {
      if (!mountedRef.current) return;
      wsRef.current?.close();
      wsRef.current = null;

      retryTimeoutRef.current = setTimeout(() => {
        retryTimeoutRef.current = null;
        retryDelayRef.current = Math.min(retryDelayRef.current * 2, 30000);
        connectWS();
      }, retryDelayRef.current);
    };

    connectWS();

    // The health poll notices a dead link before a half-open socket does: drop the socket so it reconnects,
    // and when the server answers again reconnect at once instead of waiting out the backoff.
    const offDown = onServerDown(() => wsRef.current?.close());
    const offUp = onServerUp(() => {
      const state = wsRef.current?.readyState;
      if (state === WebSocket.OPEN || state === WebSocket.CONNECTING) return;
      if (retryTimeoutRef.current) {
        clearTimeout(retryTimeoutRef.current);
        retryTimeoutRef.current = null;
      }
      retryDelayRef.current = 1000;
      connectWS();
    });

    return () => {
      offDown();
      offUp();
      setFeed('idle');
      mountedRef.current = false;
      if (import.meta.env.DEV) {
        console.log('[Streaming] Closing WebSocket connection');
      }
      wsRef.current?.close();
      wsRef.current = null;
      if (retryTimeoutRef.current) {
        clearTimeout(retryTimeoutRef.current);
        retryTimeoutRef.current = null;
      }
      if (resyncTimer) clearTimeout(resyncTimer);
    };
  }, []);

  useEffect(() => {
    const onHash = () => setFocusId(parseHash(location.hash).params.get('session'));
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);

  // The box also searches the archive: a session evicted from memory is fetched by the query and joins the list,
  // where the same client-side match shows it.
  useEffect(() => {
    const q = searchQuery.trim();
    if (q.length < 3) return;
    const t = setTimeout(() => searchSessionsRef.current(q), 400);
    return () => clearTimeout(t);
  }, [searchQuery]);

  // Declared after the feed effect above, so loadSessionRef is already set when this first runs.
  useEffect(() => {
    if (!focusId) return;
    removedIdsRef.current.delete(focusId);
    loadSessionRef.current(focusId);
  }, [focusId]);

  const handleContinueToEvaluation = async () => {
    if (!selectedGoldenId) return;

    setIsPreparingEvaluation(true);

    try {
      const goldenSession = activeSessions.get(selectedGoldenId);
      if (!goldenSession) {
        throw new Error('Golden session not found');
      }

      if (import.meta.env.DEV) {
        console.log('Creating eval set from golden session:', selectedGoldenId);
      }

      const evalSetResponse = await fetch(config.api.endpoints.streamingCreateEvalSet, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          session_id: selectedGoldenId,
          eval_set_id: `golden_${selectedGoldenId}`,
        }),
      });

      if (!evalSetResponse.ok) {
        throw new Error('Failed to create eval set from golden session');
      }

      const evalSetEnvelope = await evalSetResponse.json();
      const evalSetBlob = new Blob([JSON.stringify(evalSetEnvelope.data.evalSet, null, 2)], { type: 'application/json' });
      const evalSetFile = new File([evalSetBlob], `eval_set_${selectedGoldenId}.json`, { type: 'application/json' });

      if (import.meta.env.DEV) {
        console.log('Fetching traces for all sessions');
      }
      // Only the golden session plus whatever the user explicitly checked
      // "Compare" on. Include the golden alongside those so its performance
      // is plotted as a reference in the charts - it's tagged via
      // goldenTraceId so the results view keeps it out of pass/fail scoring
      // (scoring it against the eval set derived from itself is a trivial
      // pass).
      const sessionIds = Array.from(new Set([selectedGoldenId, ...comparisonSessionIds]));
      // Only tag as "golden" (excluded from the results
      // table entirely, see DashboardView.tsx) when there's actually
      // something to compare it against. With zero comparison sessions
      // checked, the UI already relabels this "EvalSet Selected" rather than
      // "Golden Run Selected" (see the button below) - that's the supported
      // single-session flow (e.g. running a judge-only, reference-free
      // metric like safety_v1 against just one session), and unconditionally
      // excluding its only row left the results table permanently empty for
      // that case.
      const goldenTraceId = comparisonSessionIds.size > 0 ? (activeSessions.get(selectedGoldenId)?.traceId ?? null) : null;

      const traceFiles = await Promise.all(
        sessionIds.map(async (sessionId) => {
          const session = activeSessions.get(sessionId);
          if (!session || !session.invocations || session.invocations.length === 0) {
            return null;
          }

          if (import.meta.env.DEV) {
            console.log(`Fetching trace for session: ${sessionId}`);
          }

          const traceResponse = await fetch(config.api.endpoints.streamingGetTrace, {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ session_id: sessionId }),
          });

          if (!traceResponse.ok) {
            console.warn(`Failed to get trace for session ${sessionId}`);
            return null;
          }

          const traceEnvelope = await traceResponse.json();
          const traceBlob = new Blob([traceEnvelope.data.traceContent], { type: 'application/json' });
          return new File([traceBlob], `trace_${sessionId}.jsonl`, { type: 'application/json' });
        })
      );

      const validTraceFiles = traceFiles.filter((f): f is File => f !== null);

      if (import.meta.env.DEV) {
        console.log(`Loaded ${validTraceFiles.length} trace files and 1 eval set`);
      }

      actions.setEvaluationOrigin('streaming');
      actions.setTraceFiles(validTraceFiles);
      actions.setEvalSet(evalSetFile);
      actions.setGoldenTraceId(goldenTraceId);
      actions.setCurrentView('upload');

    } catch (error) {
      console.error('Failed to prepare evaluation:', error);
      alert(`Failed to prepare evaluation: ${error instanceof Error ? error.message : 'Unknown error'}`);
    } finally {
      setIsPreparingEvaluation(false);
    }
  };

  const byStartedAtDesc = (a: LiveSession, b: LiveSession) =>
    (Date.parse(b.startedAt) || 0) - (Date.parse(a.startedAt) || 0);

  const cutoff = TIME_RANGES.find(r => r.key === timeRange)?.ms;
  const withinRange = (s: LiveSession) => {
    if (cutoff == null) return true;
    const started = Date.parse(s.startedAt);
    return !Number.isNaN(started) && started >= Date.now() - cutoff;
  };

  const normalizedQuery = searchQuery.trim().toLowerCase();
  // Matches what the card header shows: tenant, agent kind, model, provider, ids, root span name, counts and
  // start time. Conversation text and metadata are left out, so "voice" finds voice sessions and not every
  // session whose prompt mentions the word.
  const matchesSearch = (s: LiveSession) => {
    if (!normalizedQuery) return true;

    const firstModelInfo = s.invocations?.[0]?.modelInfo;
    const modelName = s.metadata?.model || s.liveStats?.model || firstModelInfo?.models?.[0] || '';
    const totalTokens = (s.liveStats?.totalInputTokens || 0) + (s.liveStats?.totalOutputTokens || 0);

    const haystacks: string[] = [
      s.sessionId,
      s.traceId,
      s.evalSetId ?? '',
      s.rootSpanName ?? '',
      ...(s.agents ?? []),
      ...(s.tenants ?? []).flatMap(t => [t, tenantName(t)]),
      ...(s.models ?? []),
      modelName,
      firstModelInfo?.provider || '',
      String(s.invocations?.length ?? ''),
      totalTokens ? String(totalTokens) : '',
      s.startedAt ? new Date(s.startedAt).toLocaleString() : '',
    ];

    return haystacks.some(h => h.toLowerCase().includes(normalizedQuery));
  };

  // A session made no LLM call once it is complete, or once its root span
  // has arrived (the root ends last, so the trace is in) with none so far.
  const hasNoLLM = (s: LiveSession) =>
    !s.invocations?.length && (s.llmCalls ?? 0) === 0 &&
    (s.status === 'complete' || !!s.rootSpanName);
  // A linked session is shown alone, whatever the time range, search and non-LLM filter say.
  const focused = focusId ? Array.from(activeSessions.values()).filter(s => s.sessionId === focusId) : null;
  const matched = focused ?? Array.from(activeSessions.values()).filter(withinRange).filter(matchesSearch).filter(s => inTenant(s.tenants, pickedTenant));
  const hiddenNoLLM = focused || showNoLLM ? 0 : matched.filter(hasNoLLM).length;
  const sessions = focused || showNoLLM ? matched : matched.filter(s => !hasNoLLM(s));
  const activeLiveSessions = sessions
    .filter(s => s.status === 'active')
    .sort(byStartedAtDesc);
  const completedSessions = sessions
    .filter(s => s.status === 'complete')
    .sort(byStartedAtDesc);
  const allSessions = [...activeLiveSessions, ...completedSessions];
  // Finished sessions are shown a page at a time; live ones always are all shown.
  const donePageIdx = clampPage(donePage, completedSessions.length, SESSIONS_PAGE);
  const visibleCompleted = completedSessions.slice(donePageIdx * SESSIONS_PAGE, (donePageIdx + 1) * SESSIONS_PAGE);

  return (
    <div style={{
      padding: '12px 18px 28px',
    }}>
      <div style={{
        marginBottom: '12px',
      }}>
        <div style={{
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
          flexWrap: 'wrap',
          gap: '10px',
          marginBottom: '8px',
        }}>
          <div style={{ display: 'flex', alignItems: 'baseline', gap: '14px', flexWrap: 'wrap' }}>
            <h1 style={{
              fontSize: '15px',
              fontWeight: 600,
              margin: 0,
              color: 'var(--accent-cyan)',
            }}>
              sessions
            </h1>
            <p style={{
              fontSize: '12px',
              color: 'var(--text-muted)',
              margin: 0,
            }}>
              pick a golden session, then evaluate the rest
            </p>
          </div>

          <div style={{
            display: 'flex',
            gap: '10px',
            alignItems: 'center',
          }}>
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
              {showNoLLM ? 'hide non-LLM' : `show non-LLM${hiddenNoLLM ? ` (${hiddenNoLLM})` : ''}`}
            </button>

            <input
              type="text"
              value={searchQuery}
              onChange={e => setSearchQuery(e.target.value)}
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

          </div>
        </div>

        {allSessions.length > 0 && (
          <div style={{
            padding: '5px 10px',
            borderRadius: 'var(--radius)',
            border: '1px solid var(--border)',
            display: 'flex',
            gap: '16px',
            alignItems: 'center',
            justifyContent: 'space-between',
          }}>
            <span style={{
              fontSize: '12px',
              color: 'var(--text-tertiary)',
            }}>
              {activeLiveSessions.length} live · {completedSessions.length} done
              {focusId && (
                <>
                  {' · '}showing {focusId}{' '}
                  <button
                    onClick={() => { location.hash = '#sessions'; }}
                    style={{ padding: 0, border: 'none', background: 'none', color: 'var(--accent-primary)', font: 'inherit', cursor: 'pointer' }}
                  >
                    show all
                  </button>
                </>
              )}
            </span>
            {completedSessions.length > 0 && (
              <button
                onClick={() => {
                  clearedBeforeRef.current = Date.now();
                  actions.clearAllSessions();
                  if (selectedGoldenId && completedSessions.some(s => s.sessionId === selectedGoldenId)) {
                    setSelectedGoldenId(null);
                  }
                }}
                style={{
                  padding: '1px 9px',
                  borderRadius: 'var(--radius)',
                  background: 'transparent',
                  border: '1px solid var(--border-default)',
                  color: 'var(--text-tertiary)',
                  fontFamily: 'var(--font-mono)',
                  fontSize: '12px',
                  cursor: 'pointer',
                  transition: 'all 0.2s',
                }}
              >
                clear done
              </button>
            )}
          </div>
        )}
      </div>

      {totalQueuedSessions > 0 && (
        <div style={{
          padding: '16px 20px',
          background: 'linear-gradient(135deg, rgba(181, 171, 252, 0.08) 0%, rgba(167, 159, 240, 0.08) 100%)',
          borderRadius: 'var(--radius)',
          marginBottom: '16px',
          border: '2px solid rgba(181, 171, 252, 0.2)',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
        }}>
          <div>
            <div style={{ fontSize: '12px', fontWeight: 700, color: '#b5abfc', marginBottom: '4px', textTransform: 'uppercase', letterSpacing: '0.5px' }}>
              Annotation Queues
            </div>
            <p style={{ fontSize: '14px', color: 'var(--text-secondary)', margin: 0 }}>
              {totalQueuedSessions} session{totalQueuedSessions !== 1 ? 's' : ''} across {state.annotationQueues.length} queue{state.annotationQueues.length !== 1 ? 's' : ''}
            </p>
          </div>
          <button
            onClick={() => actions.setCurrentView('annotation-queue')}
            style={{
              padding: '8px 20px', borderRadius: 'var(--radius)',
              background: 'rgba(181, 171, 252, 0.15)',
              border: '1.5px solid rgba(181, 171, 252, 0.4)',
              color: '#b5abfc', fontSize: '13px', fontWeight: 600,
              cursor: 'pointer', transition: 'all 0.2s', whiteSpace: 'nowrap',
            }}
          >
            Open Annotation Queues →
          </button>
        </div>
      )}

      {selectedGoldenId && (
        <div style={{
          padding: '24px',
          background: 'linear-gradient(135deg, rgba(167, 159, 240, 0.08) 0%, rgba(181, 171, 252, 0.08) 100%)',
          borderRadius: 'var(--radius)',
          marginBottom: '24px',
          border: '2px solid rgba(167, 159, 240, 0.2)',
          display: 'flex',
          justifyContent: 'space-between',
          alignItems: 'center',
        }}>
          <div>
            <div style={{
              fontSize: '12px',
              fontWeight: 700,
              color: '#a79ff0',
              marginBottom: '8px',
              textTransform: 'uppercase',
              letterSpacing: '0.5px',
            }}>
              {comparisonSessionIds.size > 0 ? 'Golden Run Selected' : 'EvalSet Selected'}
            </div>
            <p style={{
              fontSize: '16px',
              fontWeight: 600,
              color: 'var(--text-primary)',
              marginBottom: '6px',
            }}>
              {selectedGoldenId}
            </p>
            <p style={{
              fontSize: '14px',
              color: 'var(--text-secondary)',
              margin: 0,
            }}>
              {comparisonSessionIds.size > 0
                ? `Ready to evaluate ${comparisonSessionIds.size} session${comparisonSessionIds.size !== 1 ? 's' : ''} against this baseline`
                : 'Evaluating this session on its own. Check "Compare" on a completed session below to add it.'}
            </p>
          </div>

          <button
            onClick={handleContinueToEvaluation}
            disabled={isPreparingEvaluation}
            style={{
              height: '44px',
              padding: '0 32px',
              borderRadius: 'var(--radius)',
              background: isPreparingEvaluation ? 'var(--bg-surface)' : 'var(--accent-primary)',
              border: isPreparingEvaluation ? '1px solid var(--border-default)' : 'none',
              color: isPreparingEvaluation ? 'var(--text-secondary)' : '#fff',
              fontSize: '15px',
              fontWeight: 600,
              cursor: isPreparingEvaluation ? 'not-allowed' : 'pointer',
              opacity: isPreparingEvaluation ? 0.4 : 1,
              transition: 'all 0.3s ease',
              boxShadow: isPreparingEvaluation ? 'none' : '0 0 20px rgba(181, 171, 252, 0.3)',
              whiteSpace: 'nowrap',
              display: 'flex',
              alignItems: 'center',
              gap: '10px',
            }}
            onMouseEnter={(e) => {
              if (!isPreparingEvaluation) {
                e.currentTarget.style.transform = 'translateY(-2px)';
                e.currentTarget.style.boxShadow = '0 0 30px rgba(181, 171, 252, 0.5)';
              }
            }}
            onMouseLeave={(e) => {
              if (!isPreparingEvaluation) {
                e.currentTarget.style.transform = 'translateY(0)';
                e.currentTarget.style.boxShadow = '0 0 20px rgba(181, 171, 252, 0.3)';
              }
            }}
          >
            {isPreparingEvaluation ? 'Preparing...' : 'Continue to Evaluation →'}
          </button>
        </div>
      )}


      {allSessions.length === 0 ? (
        <p style={{ margin: 0, fontSize: '12px', color: 'var(--text-muted)' }}>
          {hiddenNoLLM > 0
            ? `${hiddenNoLLM} hidden (no LLM call)`
            : timeRange === 'all'
              ? 'no sessions'
              : 'no sessions in this range'}
          {timeRange !== 'all' && (
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
                    defaultExpanded={focusId === session.sessionId}
                    session={session}
                    isSelected={selectedGoldenId === session.sessionId}
                    onSelect={() => setSelectedGoldenId(
                      selectedGoldenId === session.sessionId ? null : session.sessionId
                    )}
                    annotationQueues={state.annotationQueues}
                    onAddToQueue={(queueId) => handleAddToQueue(session, queueId)}
                    onCreateAndAddToQueue={(name) => handleCreateAndAddToQueue(session, name)}
                    queueNames={getQueueNamesForSession(session.sessionId)}
                  />
                ))}
              </div>
            </div>
          )}

          {completedSessions.length > 0 && (
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
                done ({completedSessions.length})
              </h2>
              <Pager total={completedSessions.length} page={donePageIdx} onChange={setDonePage} pageSize={SESSIONS_PAGE} />
              <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0, 1fr)', gap: '6px' }}>
                {visibleCompleted.map(session => (
                  <SessionCard
                    key={session.sessionId}
                    defaultExpanded={focusId === session.sessionId}
                    session={session}
                    isSelected={selectedGoldenId === session.sessionId}
                    onSelect={() => setSelectedGoldenId(
                      selectedGoldenId === session.sessionId ? null : session.sessionId
                    )}
                    onRemove={() => {
                      removedIdsRef.current.add(session.sessionId);
                      actions.removeSession(session.sessionId);
                      if (selectedGoldenId === session.sessionId) setSelectedGoldenId(null);
                      setComparisonSessionIds(prev => {
                        if (!prev.has(session.sessionId)) return prev;
                        const next = new Set(prev);
                        next.delete(session.sessionId);
                        return next;
                      });
                    }}
                    annotationQueues={state.annotationQueues}
                    onAddToQueue={(queueId) => handleAddToQueue(session, queueId)}
                    onCreateAndAddToQueue={(name) => handleCreateAndAddToQueue(session, name)}
                    queueNames={getQueueNamesForSession(session.sessionId)}
                    compareOption={
                      selectedGoldenId && selectedGoldenId !== session.sessionId
                        ? { checked: comparisonSessionIds.has(session.sessionId), onToggle: () => toggleComparisonSession(session.sessionId) }
                        : undefined
                    }
                  />
                ))}
              </div>
              <Pager total={completedSessions.length} page={donePageIdx} onChange={setDonePage} pageSize={SESSIONS_PAGE} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}
