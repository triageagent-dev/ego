import { config } from '../config';

// A trace or an eval set the server already holds, shown on the evaluate page like an uploaded file. The server is
// given its session id (a few bytes) instead of its text; the text is fetched only when somebody opens it in the
// editor, or when it has to be sent as a real file because the request mixes in an edited or uploaded one.

async function postJSON<T>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  });
  if (!response.ok) throw new Error(`HTTP ${response.status} ${response.statusText}`);
  return (await response.json()).data as T;
}

export class SessionTraceFile extends File {
  readonly sessionId: string;

  constructor(sessionId: string) {
    super([], `trace_${sessionId}.jsonl`, { type: 'application/json' });
    this.sessionId = sessionId;
  }

  async fetchText(): Promise<string> {
    const data = await postJSON<{ traceContent: string }>(config.api.endpoints.streamingGetTrace, { session_id: this.sessionId });
    return data.traceContent;
  }
}

export class SessionEvalSetFile extends File {
  readonly sessionId: string;

  constructor(sessionId: string) {
    super([], `eval_set_${sessionId}.json`, { type: 'application/json' });
    this.sessionId = sessionId;
  }

  async fetchText(): Promise<string> {
    const data = await postJSON<{ evalSet: unknown }>(config.api.endpoints.streamingCreateEvalSet, {
      session_id: this.sessionId,
      eval_set_id: `golden_${this.sessionId}`,
    });
    return JSON.stringify(data.evalSet, null, 2);
  }
}

// A saved eval set, named by its id: the conversations in it stay on the server, so the request carries a few bytes
// and nothing a gateway's content rules could take for an attack.
export class StoredEvalSetFile extends File {
  readonly evalSetId: string;

  constructor(evalSetId: string) {
    super([], `${evalSetId}.json`, { type: 'application/json' });
    this.evalSetId = evalSetId;
  }

  async fetchText(): Promise<string> {
    const response = await fetch(`${config.api.endpoints.evalSets}/${encodeURIComponent(this.evalSetId)}`);
    if (!response.ok) throw new Error(`HTTP ${response.status} ${response.statusText}`);
    return JSON.stringify((await response.json()).data, null, 2);
  }
}

type SessionFile = SessionTraceFile | SessionEvalSetFile | StoredEvalSetFile;

export function isSessionFile(file: File): file is SessionFile {
  return file instanceof SessionTraceFile || file instanceof SessionEvalSetFile || file instanceof StoredEvalSetFile;
}

/** The file itself: a session file is read from the server into a real one, anything else is returned as is. */
export async function materialize(file: File): Promise<File> {
  if (!isSessionFile(file)) return file;
  return new File([await file.fetchText()], file.name, { type: file.type });
}
