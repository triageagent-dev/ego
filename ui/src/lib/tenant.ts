import { useSyncExternalStore } from 'react';

// The tenant the reader picked in the switcher; '' means every tenant their role covers. A tenant key is
// "<sender>/<tenant>" (two senders can both send a tenant called "acme"); "<sender>/*" means all of a sender's.
const KEY = 'ego.tenant';
const CHANGED = 'ego:tenant';

export interface TenantOption {
  key: string;
  sender?: string;
  label: string;
  sessions: number;
}

export function selectedTenant(): string {
  try {
    return localStorage.getItem(KEY) ?? '';
  } catch {
    return '';
  }
}

export function setSelectedTenant(key: string): void {
  try {
    if (key) localStorage.setItem(KEY, key);
    else localStorage.removeItem(KEY);
  } catch {
    // blocked storage: the pick just won't survive a reload
  }
  window.dispatchEvent(new Event(CHANGED));
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener(CHANGED, onChange);
  window.addEventListener('storage', onChange);
  return () => {
    window.removeEventListener(CHANGED, onChange);
    window.removeEventListener('storage', onChange);
  };
}

export function useSelectedTenant(): string {
  return useSyncExternalStore(subscribe, selectedTenant, () => '');
}

// Adds ?tenant= to a URL when a tenant is picked, for the endpoints the server narrows (usage, quality).
export function withTenant(url: string): string {
  const t = selectedTenant();
  if (!t) return url;
  return `${url}${url.includes('?') ? '&' : '?'}tenant=${encodeURIComponent(t)}`;
}

// Whether a session with these tenant keys belongs to the picked tenant.
export function inTenant(sessionTenants: string[] | undefined, picked: string): boolean {
  if (!picked) return true;
  if (!sessionTenants) return false;
  if (picked.endsWith('/*')) {
    const prefix = picked.slice(0, -1);
    return sessionTenants.some((k) => k.startsWith(prefix));
  }
  return sessionTenants.includes(picked);
}

export async function fetchTenants(): Promise<TenantOption[]> {
  const res = await fetch('/api/tenants');
  if (!res.ok) return [];
  const body = await res.json();
  return (body.data ?? []) as TenantOption[];
}
