import { useSyncExternalStore } from 'react';

// The tenant the reader picked in the switcher; '' means every tenant their role covers. A tenant key is
// "<sender>/<tenant>" (two senders can both send a tenant called "acme"); "<sender>/*" means all of a sender's.
const KEY = 'ego.tenant';
const CHANGED = 'ego:tenant';

export interface TenantOption {
  key: string;
  sender?: string;
  label: string;
  // Display name an admin gave the tenant; the key stays its identity.
  name?: string;
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
  // "<sender>/*" goes as "<sender>/": gateways refuse a "/*" in a query string, however it is encoded.
  const wire = t.endsWith('/*') ? t.slice(0, -1) : t;
  return `${url}${url.includes('?') ? '&' : '?'}tenant=${encodeURIComponent(wire)}`;
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

const NAMES_CHANGED = 'ego:tenant-names';
let names: Record<string, string> = {};
let namesVersion = 0;

// What a tenant key is called where the UI shows it: the admin-given name, else the key itself.
export function tenantName(key: string): string {
  return names[key] || key;
}

// Names every comma-separated tenant key in a list, as the usage endpoints return them.
export function tenantNames(keys: string): string {
  return keys.split(',').map((k) => tenantName(k.trim())).join(', ');
}

function setNames(options: TenantOption[]): void {
  const next: Record<string, string> = {};
  for (const o of options) if (o.name) next[o.key] = o.name;
  if (JSON.stringify(next) === JSON.stringify(names)) return;
  names = next;
  namesVersion++;
  window.dispatchEvent(new Event(NAMES_CHANGED));
}

// Re-renders the component when the tenant names are loaded or change.
export function useTenantNames(): void {
  useSyncExternalStore(
    (onChange) => {
      window.addEventListener(NAMES_CHANGED, onChange);
      return () => window.removeEventListener(NAMES_CHANGED, onChange);
    },
    () => namesVersion,
    () => 0,
  );
}

export async function fetchTenants(): Promise<TenantOption[]> {
  const res = await fetch('/api/tenants');
  if (!res.ok) return [];
  const body = await res.json();
  const options = (body.data ?? []) as TenantOption[];
  setNames(options);
  return options;
}
