// Per-browser memory of a choice. Storage can be blocked or full; the choice then just lasts for the page view.

export function stored(key: string): string | null {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
}

export function store(key: string, value: string): void {
  try {
    if (value) localStorage.setItem(key, value);
    else localStorage.removeItem(key);
  } catch {
    // blocked or full: not remembered
  }
}

// Forgets every remembered choice whose key starts with prefix.
export function clearStored(prefix: string): void {
  try {
    for (let i = localStorage.length - 1; i >= 0; i--) {
      const key = localStorage.key(i);
      if (key?.startsWith(prefix)) localStorage.removeItem(key);
    }
  } catch {
    // blocked: nothing was remembered
  }
}
