import { useCallback, useEffect, useState } from 'react';

// "tui" is the terminal look (top tab bar, monospace, dense); "classic" is the
// roomier look with a left sidebar, rounded corners and a proportional font.
export type UiMode = 'tui' | 'classic';

const KEY = 'ego.mode';
const CHANGED = 'ego:mode';
const CLASSIC_FONTS =
  'https://fonts.googleapis.com/css2?family=Archivo:wght@400;500;600;700&family=IBM+Plex+Mono:wght@400;500;600&display=swap';

export function storedMode(): UiMode {
  try {
    // Classic is the default; the terminal look is kept for whoever chose it.
    return localStorage.getItem(KEY) === 'tui' ? 'tui' : 'classic';
  } catch {
    return 'classic';
  }
}

// Sets the mode on <html data-mode> (the CSS tokens key off it) and remembers it.
export function applyMode(mode: UiMode, persist = true): void {
  document.documentElement.dataset.mode = mode;
  if (mode === 'classic' && !document.getElementById('ego-classic-fonts')) {
    const link = document.createElement('link');
    link.id = 'ego-classic-fonts';
    link.rel = 'stylesheet';
    link.href = CLASSIC_FONTS;
    document.head.appendChild(link);
  }
  if (persist) {
    try {
      localStorage.setItem(KEY, mode);
    } catch {
      // private window or blocked storage: the choice just won't persist
    }
  }
  window.dispatchEvent(new Event(CHANGED));
}

export function useUiMode(): [UiMode, (m: UiMode) => void] {
  const [mode, setModeState] = useState<UiMode>(() =>
    document.documentElement.dataset.mode === 'tui' ? 'tui' : 'classic',
  );
  useEffect(() => {
    const sync = () => setModeState(document.documentElement.dataset.mode === 'tui' ? 'tui' : 'classic');
    window.addEventListener(CHANGED, sync);
    return () => window.removeEventListener(CHANGED, sync);
  }, []);
  const setMode = useCallback((m: UiMode) => applyMode(m), []);
  return [mode, setMode];
}
