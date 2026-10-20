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
  UsageCall,
  UsageSession,
  OnlineStatus,
  OnlineSeriesPoint,
  OnlineAlert,
} from '../lib/types';
import { config } from '../config';
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

async function unwrap<T>(response: Response): Promise<T> {
  const json: StandardResponse<T> = await response.json();
  if (json.error) {
    throw new Error(json.error);
  }
  return json.data;
}

export async function convertTraces(traceFiles: File[]): Promise<ConvertTracesResponse> {
  const formData = new FormData();
  traceFiles.forEach(file => formData.append('trace_files', file));

  const response = await fetch(`${API_BASE_URL}/convert`, {
    method: 'POST',
    body: formData,
  });

  if (!response.ok) {
    let errorMessage = `API error: ${response.statusText}`;
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

export async function evaluateTracesAPI(
  traceFiles: File[],
  evalSetFile: File | null,
  evalConfig: EvalConfig
): Promise<RunResult> {
  const formData = new FormData();

  traceFiles.forEach(file => {
    formData.append('trace_files', file);
  });

  if (evalSetFile) {
    formData.append('eval_set_file', evalSetFile);
  }

  formData.append('config', JSON.stringify(evalConfig));

  try {
    const response = await fetch(`${API_BASE_URL}/evaluate`, {
      method: 'POST',
      body: formData,
    });

    if (!response.ok) {
      let errorMessage = `API error: ${response.statusText}`;
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
  onError: (error: Error) => void
): Promise<void> {
  const formData = new FormData();

  traceFiles.forEach(file => {
    formData.append('trace_files', file);
  });

  if (evalSetFile) {
    formData.append('eval_set_file', evalSetFile);
  }

  formData.append('config', JSON.stringify(evalConfig));

  try {
    const response = await fetch(`${API_BASE_URL}/evaluate/stream`, {
      method: 'POST',
      body: formData,
    });

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

  const response = await fetch(`${config.api.endpoints.runs}${query ? `?${query}` : ''}`);

  if (response.status === 503) {
    throw new StorageUnavailableError();
  }
  if (!response.ok) {
    throw new Error(`Failed to list runs: ${response.statusText}`);
  }
  return unwrap<Run[]>(response);
}

export async function getRun(runId: string): Promise<Run> {
  const response = await fetch(`${config.api.endpoints.runs}/${runId}`);
  if (response.status === 503) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to fetch run: ${response.statusText}`);
  return unwrap<Run>(response);
}

export async function getRunResults(runId: string): Promise<RunResultRow[]> {
  const response = await fetch(`${config.api.endpoints.runs}/${runId}/results`);
  if (response.status === 503) throw new StorageUnavailableError();
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
  if (response.status === 503) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(await readErrorMessage(response, 'Failed to list eval sets'));
  return unwrap<EvalSetSummary[]>(response);
}

export async function getEvalSet(evalSetId: string): Promise<EvalSet> {
  const response = await fetch(`${config.api.endpoints.evalSets}/${evalSetId}`);
  if (response.status === 503) throw new StorageUnavailableError();
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
  if (response.status === 503) throw new StorageUnavailableError();
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

export async function generateBugReport(diagnostics: {
  user_description: string;
  browser_info: Record<string, unknown>;
  console_logs: unknown[];
  app_state: Record<string, unknown>;
  network_errors: unknown[];
}): Promise<Blob> {
  const response = await fetch(config.api.endpoints.debugBundle, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(diagnostics),
  });

  if (!response.ok) {
    let errorMessage = `Failed to generate debug bundle: ${response.statusText}`;
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

  return response.blob();
}

export async function loadBugReport(
  file: File,
): Promise<{ loadedSessions: string[]; count: number }> {
  const formData = new FormData();
  formData.append('file', file);

  const response = await fetch(config.api.endpoints.debugLoad, {
    method: 'POST',
    body: formData,
  });

  if (!response.ok) {
    let errorMessage = `Failed to load debug bundle: ${response.statusText}`;
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

  return unwrap<{ loadedSessions: string[]; count: number }>(response);
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
  const response = await fetch(`${config.api.endpoints.usage}?hours=${hours}`);
  if (response.status === 503) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load usage: ${httpStatusLabel(response)}`);
  return unwrap<UsageSeries>(response);
}

export async function getUsageSessions(hours: number, limit = 25): Promise<UsageSession[]> {
  const response = await fetch(`${config.api.endpoints.usage}/calls?hours=${hours}&limit=${limit}&group=session`);
  if (response.status === 503) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load top sessions: ${httpStatusLabel(response)}`);
  return unwrap<UsageSession[]>(response);
}

export async function getUsageCalls(hours: number, limit = 25): Promise<UsageCall[]> {
  const response = await fetch(`${config.api.endpoints.usage}/calls?hours=${hours}&limit=${limit}`);
  if (response.status === 503) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load top calls: ${httpStatusLabel(response)}`);
  return unwrap<UsageCall[]>(response);
}

export async function getOnlineStatus(): Promise<OnlineStatus> {
  const response = await fetch(`${config.api.endpoints.online}/status`);
  if (response.status === 503) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load online evaluation status: ${httpStatusLabel(response)}`);
  return unwrap<OnlineStatus>(response);
}

export async function getOnlineSeries(hours: number): Promise<OnlineSeriesPoint[]> {
  const response = await fetch(`${config.api.endpoints.online}/series?hours=${hours}`);
  if (response.status === 503) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load quality series: ${httpStatusLabel(response)}`);
  return unwrap<OnlineSeriesPoint[]>(response);
}

export async function getOnlineAlerts(state: 'open' | 'all' = 'open'): Promise<OnlineAlert[]> {
  const response = await fetch(`${config.api.endpoints.online}/alerts?state=${state}`);
  if (response.status === 503) throw new StorageUnavailableError();
  if (!response.ok) throw new Error(`Failed to load quality alerts: ${httpStatusLabel(response)}`);
  return unwrap<OnlineAlert[]>(response);
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
