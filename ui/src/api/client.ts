import type {
  RunResult,
  EvalConfig,
  TraceResult,
  MetricMetadata,
  StandardResponse,
  ConvertTracesResponse,
  Run,
  RunStatus,
  RunResultRow,
  EvalSet,
  EvalSetSummary,
  UsageSeries,
  ActivitySubject,
  TurnsResult,
  LimitItem,
  UsageCall,
  UsageSession,
  OnlineStatus,
  OnlineSeriesPoint,
  OnlineAlert,
  OnlineFinding,
  OnlineCorrection,
  OnlineLabel,
  OnlineLabelInput,
  OnlineSessionScore,
} from '../lib/types';
import { config } from '../config';
import { withTenant } from '../lib/tenant';
import { materialize, SessionEvalSetFile, SessionTraceFile } from '../lib/sessionFiles';
import { evalSetFromWireFormat, evalSetToWireFormat } from '../lib/evalset-builder';

export class StorageUnavailableError extends Error {
  constructor() {
    super(
      'Runs are unavailable: the run store is not configured or failed to open at server startup. ' +
        'Set --session-db (EGO_SESSION_DB_PATH) and check the server logs.',
    );
    this.name = 'StorageUnavailableError';
  }
}

const API_BASE_URL = `${config.api.baseUrl}/api`;

// A 503 from ego itself carries its JSON envelope and says a store or feature is not configured. A 503 from the
// gateway in front of it (no healthy backend while the pod is replaced) is a plain-text outage, and reading it as
// "not configured" told people online evaluation was off during every rollout.
async function serverSaysUnavailable(response: Response): Promise<boolean> {
  if (response.status !== 503) return false;
  if (!(response.headers.get('content-type') ?? '').includes('application/json')) return false;
  try {
    const json = await response.clone().json();
    return typeof json?.error === 'string';
  } catch {
    return false;
  }
}

async function unwrap<T>(response: Response): Promise<T> {
  const json: StandardResponse<T> = await response.json();
  if (json.error) {
    throw new Error(json.error);
  }
  return json.data;
}

// Sessions the server holds are named by id; anything else (an upload, an edited trace) is sent as a file.
function namedSessions(traceFiles: File[]): string[] | null {
  if (traceFiles.length === 0 || !traceFiles.every((f) => f instanceof SessionTraceFile)) return null;
  return traceFiles.map((f) => (f as SessionTraceFile).sessionId);
}

export async function convertTraces(traceFiles: File[]): Promise<ConvertTracesResponse> {
  const sessionIds = namedSessions(traceFiles);
  let init: RequestInit;
  if (sessionIds) {
    init = { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ session_ids: sessionIds }) };
  } else {
    const formData = new FormData();
    (await Promise.all(traceFiles.map(materialize))).forEach(file => formData.append('trace_files', file));
    init = { method: 'POST', body: formData };
  }

  const response = await fetch(`${API_BASE_URL}/convert`, init);

  if (!response.ok) {
    let errorMessage = `API error: ${response.status} ${response.statusText}`;
    try {
      const errorData = await response.json();
      if (errorData.detail) {
        errorMessage = errorData.detail;
      }
    } catch {
      // Fallback to statusText
    }
    throw new Error(errorMessage);
  }

  return unwrap<ConvertTracesResponse>(response);
}

// The evaluate request: session ids and the config as JSON when the server holds every trace (and the golden
// eval set), else the files as a multipart upload.
async function evaluateRequest(traceFiles: File[], evalSetFile: File | null, evalConfig: EvalConfig, traceIds?: string[]): Promise<RequestInit> {
  const sessionIds = namedSessions(traceFiles);
  if (sessionIds && (evalSetFile === null || evalSetFile instanceof SessionEvalSetFile)) {
    return {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        config: evalConfig,
        session_ids: sessionIds,
        ...(evalSetFile instanceof SessionEvalSetFile ? { eval_set_session_id: evalSetFile.sessionId } : {}),
        ...(traceIds ? { trace_ids: traceIds } : {}),
      }),
    };
  }
  const formData = new FormData();
  (await Promise.all(traceFiles.map(materialize))).forEach(file => formData.append('trace_files', file));
  if (evalSetFile) formData.append('eval_set_file', await materialize(evalSetFile));
  formData.append('config', JSON.stringify(evalConfig));
  if (traceIds) formData.append('trace_ids', JSON.stringify(traceIds));
  return { method: 'POST', body: formData };
}

export async function evaluateTracesAPI(
  traceFiles: File[],
  evalSetFile: File | null,
  evalConfig: EvalConfig
): Promise<RunResult> {
  try {
    const response = await fetch(`${API_BASE_URL}/evaluate`, await evaluateRequest(traceFiles, evalSetFile, evalConfig));

    if (!response.ok) {
      let errorMessage = `API error: ${response.status} ${response.statusText}`;
      try {
        const errorData = await response.json();
        if (errorData.detail) {
          errorMessage = errorData.detail;
        }
      } catch {
        // Fallback to statusText if JSON parsing fails
      }
      throw new Error(errorMessage);
    }

    return unwrap<RunResult>(response);

  } catch (error) {
    if (error instanceof Error) {
      throw error;
    }
    throw new Error('Unknown error occurred while evaluating traces');
  }
}

export async function evaluateTracesStreaming(
  traceFiles: File[],
  evalSetFile: File | null,
  evalConfig: EvalConfig,
  onProgress: (message: string) => void,
  onTraceProgress: (traceId: string, status: string, partialResult?: TraceResult) => void,
  onComplete: (result: RunResult) => void,
  onError: (error: Error) => void,
  signal?: AbortSignal,
  // The traces to score when the reader picked some of the sessions' turns; all of them when absent.
  traceIds?: string[]
): Promise<void> {
  try {
    const response = await fetch(`${API_BASE_URL}/evaluate/stream`, { ...(await evaluateRequest(traceFiles, evalSetFile, evalConfig, traceIds)), signal });

    if (!response.ok) {
      throw new Error(`HTTP ${response.status}: ${response.statusText}`);
    }

    const reader = response.body!.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let eventType = '';

    while (true) {
      const { done, value } = await reader.read();

      if (done) break;

      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');

      buffer = lines.pop() || '';

      for (const line of lines) {
        if (line.startsWith('event: ')) {
          eventType = line.slice(7).trim();
        } else if (line.startsWith('data: ')) {
          const eventData = JSON.parse(line.slice(6));

          if (eventType === 'performance_metrics') {
            const tm = eventData.traceMetadata ?? {};
            const partialResult: TraceResult = {
              traceId: eventData.traceId,
              numInvocations: 0,
              metricResults: [],
              conversionWarnings: [],
              performanceMetrics: eventData.performanceMetrics,
              agentName: tm.agentName,
              agentId: tm.agentId,
              sessionId: tm.sessionName,
              model: tm.model,
              responseModel: tm.responseModel,
              provider: tm.provider,
              startTime: tm.startTime,
              userInputPreview: tm.userInputPreview,
              finalOutputPreview: tm.finalOutputPreview,
            };
            onTraceProgress(eventData.traceId, 'loading', partialResult);
            eventType = '';
          } else if (eventData.error) {
            onError(new Error(eventData.error));
            return;
          } else if (eventData.done) {
            onComplete(eventData.result as RunResult);
            return;
          } else if (eventData.traceProgress) {
            const tp = eventData.traceProgress;
            const partialResult = tp.partialResult as TraceResult | undefined;
            onTraceProgress(tp.traceId, tp.status || '', partialResult);
          } else if (eventData.message) {
            onProgress(eventData.message);
          }
        }
      }
    }
  } catch (error) {
    // Stopping is the reader's own act, reported by whoever aborted.
    if (signal?.aborted) return;
    if (error instanceof Error) {
      onError(error);
    } else {
      onError(new Error('Unknown error occurred during streaming evaluation'));
    }
  }
}

export async function listRuns(options?: {
  status?: RunStatus[];
  limit?: number;
  before?: string;
}): Promise<Run[]> {
  const params = new URLSearchParams();
  (options?.status ?? []).forEach(s => params.append('status', s));
  if (options?.limit != null) params.set('limit', String(options.limit));
  if (options?.before) params.set('before', options.before);
  const query = params.toString();

  const response = await fetch(withTenant(`${config.api.endpoints.runs}${query ? `?${query}` : ''}`));

  if (await serverSaysUnavailable(response)) {
    throw new StorageUnavailableError();
  }
  if (!response.ok) {
    throw new Error(`Failed to list runs: ${response.statusText}`);
  }
  return unwrap<Run[]>(response);
}

export async function getRun(runId: string): Promise<Run> {
  const response = await fetch(`${config.api.endpoints.runs}/${runId}`);
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to fetch run: ${response.statusText}`);
  return unwrap<Run>(response);
}

export async function getRunResults(runId: string): Promise<RunResultRow[]> {
  const response = await fetch(`${config.api.endpoints.runs}/${runId}/results`);
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to fetch run results: ${response.statusText}`);
  return unwrap<RunResultRow[]>(response);
}

// httpStatusLabel builds a fallback reason string for a failed response.
// response.statusText is frequently "" for HTTP/2 responses (the reason
// phrase browsers report is only ever populated for HTTP/1.1 - see
// https://fetch.spec.whatwg.org/#concept-response-status-message), which
// this app's Gateway-fronted deployment serves over. Without this fallback,
// a non-JSON error body (e.g. a plain-text 404/502 from an old binary or
// the Gateway itself, rather than our JSON error envelope) surfaces as an
// uninformative "Failed to X: " with nothing after the colon.
function httpStatusLabel(response: Response): string {
  return response.statusText || `HTTP ${response.status}`;
}

async function readErrorMessage(response: Response, fallbackPrefix: string): Promise<string> {
  try {
    const errorData = await response.json();
    if (errorData.error) return errorData.error;
  } catch {
    // Fallback below.
  }
  return `${fallbackPrefix}: ${httpStatusLabel(response)}`;
}

export async function listEvalSets(): Promise<EvalSetSummary[]> {
  const response = await fetch(config.api.endpoints.evalSets);
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(await readErrorMessage(response, 'Failed to list eval sets'));
  return unwrap<EvalSetSummary[]>(response);
}

export async function getEvalSet(evalSetId: string): Promise<EvalSet> {
  const response = await fetch(`${config.api.endpoints.evalSets}/${evalSetId}`);
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(await readErrorMessage(response, 'Failed to fetch eval set'));
  const raw = await unwrap<any>(response);
  return evalSetFromWireFormat(raw);
}

export async function saveEvalSet(evalSet: EvalSet): Promise<EvalSetSummary> {
  const response = await fetch(config.api.endpoints.evalSets, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(evalSetToWireFormat(evalSet)),
  });
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(await readErrorMessage(response, 'Failed to save eval set'));
  return unwrap<EvalSetSummary>(response);
}

export async function listMetrics(): Promise<MetricMetadata[]> {
  try {
    const response = await fetch(`${API_BASE_URL}/metrics`);

    if (!response.ok) {
      throw new Error(`Failed to fetch metrics: ${response.statusText}`);
    }

    return unwrap<MetricMetadata[]>(response);
  } catch (error) {
    console.error('Failed to list metrics:', error);
    throw error;
  }
}

export async function validateEvalSet(evalSetFile: File): Promise<{ valid: boolean; evalSetId?: string; numCases?: number; errors?: string[] }> {
  const formData = new FormData();
  formData.append('eval_set_file', evalSetFile);

  try {
    const response = await fetch(`${API_BASE_URL}/validate/eval-set`, {
      method: 'POST',
      body: formData,
    });

    if (!response.ok) {
      throw new Error(`Failed to validate eval set: ${response.statusText}`);
    }

    return unwrap<{ valid: boolean; evalSetId?: string; numCases?: number; errors?: string[] }>(response);
  } catch (error) {
    console.error('Failed to validate eval set:', error);
    throw error;
  }
}

export async function getConfig(): Promise<{ apiKeys: { google: boolean; anthropic: boolean; openai: boolean } }> {
  const response = await fetch(`${API_BASE_URL}/config`);
  if (!response.ok) {
    throw new Error(`Failed to fetch config: ${response.statusText}`);
  }
  return unwrap(response);
}

export async function healthCheck(): Promise<{ status: string; version: string }> {
  try {
    const response = await fetch(`${API_BASE_URL}/health`);

    if (!response.ok) {
      throw new Error(`Health check failed: ${response.statusText}`);
    }

    return unwrap<{ status: string; version: string }>(response);
  } catch (error) {
    console.error('Health check failed:', error);
    throw error;
  }
}

export async function getUsage(hours: number): Promise<UsageSeries> {
  const response = await fetch(withTenant(`${config.api.endpoints.usage}?hours=${hours}`));
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load usage: ${httpStatusLabel(response)}`);
  return unwrap<UsageSeries>(response);
}

/** The turns of one agent in a tenant that began in the last `minutes`, oldest first. */
export async function getActivityTurns(agent: string, tenant: string | undefined, minutes: number): Promise<TurnsResult> {
  const response = await fetch(
    `${config.api.endpoints.activityTurns}?agent=${encodeURIComponent(agent)}&tenant=${encodeURIComponent(tenant ?? '')}&minutes=${minutes}`,
  );
  if (!response.ok) throw new Error(`Failed to load turns: ${httpStatusLabel(response)}`);
  return unwrap<TurnsResult>(response);
}

/** The agents that work in turns, the one seen last first (the picked tenant narrows them). */
export async function getActivity(): Promise<ActivitySubject[]> {
  const response = await fetch(withTenant(config.api.endpoints.activity));
  if (!response.ok) throw new Error(`Failed to load activity: ${httpStatusLabel(response)}`);
  return unwrap<ActivitySubject[]>(response);
}

export async function getUsageLimits(): Promise<LimitItem[]> {
  const response = await fetch(config.api.endpoints.usageLimits);
  if (!response.ok) throw new Error(`Failed to load limits: ${httpStatusLabel(response)}`);
  return unwrap<LimitItem[]>(response);
}

export interface FailedSession {
  sessionId: string;
  lastAt: number;
  tenant: string;
  kind: string;
  // The metrics that failed, comma separated, and the lowest of their scores.
  metrics: string;
  lowest: number;
}

/** The sessions that failed an online evaluation (a score below its threshold) in the last 24 hours, the one scored
 * last first. */
export async function getFailedSessions(limit = 5): Promise<FailedSession[]> {
  const response = await fetch(withTenant(`${config.api.endpoints.online}/failed?hours=24&limit=${limit}`));
  if (!response.ok) throw new Error(`Failed to load failed sessions: ${httpStatusLabel(response)}`);
  return unwrap<FailedSession[]>(response);
}

export interface FailedCallSession {
  sessionId: string;
  lastAt: number;
  failed: number;
  calls: number;
  kinds: string;
  tenants: string;
}

/** The sessions with failed LLM calls in the last 24 hours, the one that failed last first. */
export async function getFailedCallSessions(limit = 5): Promise<FailedCallSession[]> {
  const response = await fetch(withTenant(`${config.api.endpoints.usage}/failed?hours=24&limit=${limit}`));
  if (!response.ok) throw new Error(`Failed to load failed sessions: ${httpStatusLabel(response)}`);
  return unwrap<FailedCallSession[]>(response);
}

export async function getUsageSessions(hours: number, limit = 25): Promise<UsageSession[]> {
  const response = await fetch(withTenant(`${config.api.endpoints.usage}/calls?hours=${hours}&limit=${limit}&group=session`));
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load top sessions: ${httpStatusLabel(response)}`);
  return unwrap<UsageSession[]>(response);
}

export async function getUsageCalls(hours: number, limit = 25): Promise<UsageCall[]> {
  const response = await fetch(withTenant(`${config.api.endpoints.usage}/calls?hours=${hours}&limit=${limit}`));
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load top calls: ${httpStatusLabel(response)}`);
  return unwrap<UsageCall[]>(response);
}

export async function getOnlineStatus(): Promise<OnlineStatus> {
  const response = await fetch(withTenant(`${config.api.endpoints.online}/status`));
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load online evaluation status: ${httpStatusLabel(response)}`);
  return unwrap<OnlineStatus>(response);
}

export async function getOnlineSeries(hours: number): Promise<OnlineSeriesPoint[]> {
  const response = await fetch(withTenant(`${config.api.endpoints.online}/series?hours=${hours}`));
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load quality series: ${httpStatusLabel(response)}`);
  return unwrap<OnlineSeriesPoint[]>(response);
}

export async function getOnlineAlerts(state: 'open' | 'all' = 'open'): Promise<OnlineAlert[]> {
  const response = await fetch(withTenant(`${config.api.endpoints.online}/alerts?state=${state}`));
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load quality alerts: ${httpStatusLabel(response)}`);
  return unwrap<OnlineAlert[]>(response);
}

export async function getOnlineFindings(): Promise<OnlineFinding[]> {
  const response = await fetch(withTenant(`${config.api.endpoints.onlineFindings}?state=all`));
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(await readErrorMessage(response, 'Failed to load findings'));
  return unwrap<OnlineFinding[]>(response);
}

export async function getOnlineCorrections(): Promise<OnlineCorrection[]> {
  const response = await fetch(withTenant(`${config.api.endpoints.onlineCorrections}?state=all`));
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(await readErrorMessage(response, 'Failed to load corrections'));
  return unwrap<OnlineCorrection[]>(response);
}

// The labels of one session, newest first.
export async function getOnlineLabels(sessionId: string): Promise<OnlineLabel[]> {
  const response = await fetch(`${config.api.endpoints.onlineLabels}?session=${encodeURIComponent(sessionId)}`);
  if (await serverSaysUnavailable(response)) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(await readErrorMessage(response, 'Failed to load labels'));
  return unwrap<OnlineLabel[]>(response);
}

export async function postOnlineLabel(input: OnlineLabelInput): Promise<OnlineLabel> {
  const response = await fetch(config.api.endpoints.onlineLabels, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  });
  if (!response.ok) throw new Error(await readErrorMessage(response, 'Failed to save the label'));
  return unwrap<OnlineLabel>(response);
}

// The online scores of one session, or null when it was never scored (or online evaluation is off, or the
// caller may not see it): the session card then has nothing to label.
export async function getOnlineSessionScores(sessionId: string): Promise<OnlineSessionScore[] | null> {
  return (await getOnlineSessionEvidence(sessionId))?.metrics ?? null;
}

/** The scores of one session with the judge's evidence behind each, and the hourly run it was scored in. */
export async function getOnlineSessionEvidence(sessionId: string): Promise<{ run_id?: string; metrics: OnlineSessionScore[] } | null> {
  const response = await fetch(`${config.api.endpoints.online}/sessions/${encodeURIComponent(sessionId)}/explain`);
  if (!response.ok) return null;
  const ev = await unwrap<{ run_id?: string; metrics: OnlineSessionScore[] }>(response);
  return { run_id: ev.run_id, metrics: ev.metrics ?? [] };
}

// One row of GET /api/streaming/session-spans - a span
// with all its attributes (status/exception fields included) and its depth
// in the trace tree.
export interface SessionSpan {
  span_id: string;
  parent_span_id?: string;
  operation_name: string;
  start_time: number; // microseconds since epoch
  duration: number; // microseconds
  depth: number;
  tags: Record<string, unknown>;
}

export async function getSessionSpans(sessionId: string): Promise<SessionSpan[]> {
  const response = await fetch(`${config.api.endpoints.streamingSessionSpans}?session_id=${encodeURIComponent(sessionId)}`);
  if (!response.ok) throw new Error(`Failed to load spans: ${httpStatusLabel(response)}`);
  const data = await unwrap<{ sessionId: string; spans: SessionSpan[] }>(response);
  return data.spans;
}
