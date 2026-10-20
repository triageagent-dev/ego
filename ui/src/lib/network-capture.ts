interface NetworkError {
  timestamp: string;
  url: string;
  method: string;
  status: number | null;
  statusText: string;
  error: string;
}

const MAX_ERRORS = 100;
const errors: NetworkError[] = [];

let installed = false;
let sessionExpiredNotified = false;

type SessionExpiredListener = () => void;
let sessionExpiredListeners: SessionExpiredListener[] = [];

/** Subscribe to "the session cookie is no longer valid" (first 401 seen by
 * any intercepted fetch call). Returns an unsubscribe function. */
export function onSessionExpired(listener: SessionExpiredListener): () => void {
  sessionExpiredListeners.push(listener);
  return () => {
    sessionExpiredListeners = sessionExpiredListeners.filter(l => l !== listener);
  };
}

export function installNetworkCapture(): void {
  if (installed) return;
  installed = true;

  const originalFetch = window.fetch;

  window.fetch = async (...args: Parameters<typeof fetch>) => {
    const [input, init] = args;
    const url =
      typeof input === 'string'
        ? input
        : input instanceof URL
          ? input.href
          : input.url;
    const method = init?.method || 'GET';

    try {
      const response = await originalFetch(...args);
      if (!response.ok) {
        errors.push({
          timestamp: new Date().toISOString(),
          url,
          method,
          status: response.status,
          statusText: response.statusText,
          error: `HTTP ${response.status}`,
        });
        if (errors.length > MAX_ERRORS) errors.shift();

        // No session (never logged in) or an expired one: every API call
        // 401s the same way once the gate is on, so this catches both
        // without each of the dozens of call sites checking individually.
        // This must NOT navigate directly: a fetch() callback runs outside
        // any user gesture, and browsers block (or silently swallow) a
        // script-initiated cross-origin top-level navigation from there -
        // window.location.href = '/auth/login' would same-origin-redirect
        // fine, but the server's own 302 from there on to GitHub's
        // cross-origin OAuth authorize URL is exactly the hop that gets
        // blocked ("Unsafe attempt to load URL ... from frame ..."),
        // wedging the page in a half-navigated, still-logged-out state with
        // no way to retry. Notify subscribers instead so the UI can show a
        // real <a href> link, which carries a genuine click/user gesture.
        if (response.status === 401 && !sessionExpiredNotified) {
          sessionExpiredNotified = true;
          sessionExpiredListeners.forEach(l => l());
        }
      }
      return response;
    } catch (err) {
      errors.push({
        timestamp: new Date().toISOString(),
        url,
        method,
        status: null,
        statusText: '',
        error: err instanceof Error ? err.message : String(err),
      });
      if (errors.length > MAX_ERRORS) errors.shift();
      throw err;
    }
  };
}

export function getNetworkErrors(): NetworkError[] {
  return [...errors];
}
