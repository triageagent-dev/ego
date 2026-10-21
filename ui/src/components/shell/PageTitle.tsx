import React, { useSyncExternalStore } from 'react';
import { createPortal } from 'react-dom';

// A view's title and subtitle are drawn in the header bar, which has the room; until the bar has mounted they are
// drawn in the view itself. The view renders them once.
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
  const el = useSyncExternalStore(subscribe, () => slot);
  if (el) return createPortal(children, el);
  return <div className={className} style={style}>{children}</div>;
};
