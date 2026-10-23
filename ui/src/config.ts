const API_BASE_URL = import.meta.env.VITE_API_BASE_URL ??
    (import.meta.env.DEV ? 'http://localhost:8001' : '');

const WS_BASE_URL = import.meta.env.VITE_WS_URL ??
    (import.meta.env.DEV ? 'ws://localhost:8001'
     : `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}`);

// Path prefix this app is served under behind a reverse proxy (e.g.
// "/ego"), baked in at build time via VITE_API_BASE_URL - the same value
// API_BASE_URL uses in production. Resolved separately from API_BASE_URL
// because in DEV mode API_BASE_URL is a full origin ("http://localhost:8001"),
// not a path, so reusing it directly would double up the host. Used by
// uiUpdatesWs below (opened by lib/connection.ts).
const BASE_PATH = import.meta.env.DEV ? '' : (import.meta.env.VITE_API_BASE_URL ?? '');

export const REPO_URL = 'https://github.com/triageagent-dev/ego';

export const config = {
  api: {
    baseUrl: API_BASE_URL,
    endpoints: {
      health: `${API_BASE_URL}/api/health`,
      metrics: `${API_BASE_URL}/api/metrics`,
      evaluate: `${API_BASE_URL}/api/evaluate`,
      evaluateStream: `${API_BASE_URL}/api/evaluate/stream`,
      runs: `${API_BASE_URL}/api/runs`,
      usage: `${API_BASE_URL}/api/usage`,
      threads: `${API_BASE_URL}/api/threads`,
      threadTurns: `${API_BASE_URL}/api/threads/turns`,
      activity: `${API_BASE_URL}/api/activity`,
      usageLimits: `${API_BASE_URL}/api/usage/limits`,
      online: `${API_BASE_URL}/api/online`,
      onlineFindings: `${API_BASE_URL}/api/online/findings`,
      onlineCorrections: `${API_BASE_URL}/api/online/corrections`,
      onlineLabels: `${API_BASE_URL}/api/online/labels`,
      evalSets: `${API_BASE_URL}/api/evalsets`,
      validateEvalSet: `${API_BASE_URL}/api/validate/eval-set`,
      streamingCreateEvalSet: `${API_BASE_URL}/api/streaming/create-eval-set`,
      streamingGetTrace: `${API_BASE_URL}/api/streaming/get-trace`,
      streamingSessionSpans: `${API_BASE_URL}/api/streaming/session-spans`,
      streamingSessions: `${API_BASE_URL}/api/streaming/sessions`,
      uiUpdatesStream: `${API_BASE_URL}/stream/ui-updates`,
      authMe: `${API_BASE_URL}/auth/me`,
      authLogin: `${API_BASE_URL}/auth/login`,
      authLogout: `${API_BASE_URL}/auth/logout`,
    },
  },
  websocket: {
    tracesUrl: `${WS_BASE_URL}/ws/traces`,
    // See BASE_PATH's comment above.
    uiUpdatesWs: `${WS_BASE_URL}${BASE_PATH}/ws/ui-updates`,
  },
} as const;
