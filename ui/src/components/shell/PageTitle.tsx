import React, { useSyncExternalStore } from 'react';
import { createPortal } from 'react-dom';
import { useUiMode } from '../../lib/mode';

// A view's title and subtitle are drawn in the classic layout's header bar, which has the room, and above the view
// in the terminal layout, which has no header. The view renders them once; where they land depends on the mode.
let slot: HTMLElement | null = null;
const listeners = new Set<() => void>();

/** The header's title area (ClassicHeader hands it over when it mounts). */
export function setTitleSlot(el: HTMLElement | null): void {
  if (slot === el) return;
  slot = el;
  listeners.forEach((l) => l());
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

interface PageTitleProps {
  children: React.ReactNode;
  // The element that holds the title when it is drawn in the page itself.
  className?: string;
  style?: React.CSSProperties;
}

export const PageTitle: React.FC<PageTitleProps> = ({ children, className, style }) => {
  const [mode] = useUiMode();
  const el = useSyncExternalStore(subscribe, () => slot);
  if (mode === 'classic' && el) return createPortal(children, el);
  return <div className={className} style={style}>{children}</div>;
};
