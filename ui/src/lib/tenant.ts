import { useEffect, useState, useSyncExternalStore } from 'react';

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

const NAMES_REFRESH_MS = 60_000;
let namesLoadedAt = 0;
let namesLoading = false;

// Loads the names unless they are fresh: whoever shows a name asks for it, so it does not depend on the tenant
// switcher (which is not on screen when the sidebar is hidden) having been drawn.
function ensureNames(): void {
  if (namesLoading || Date.now() - namesLoadedAt < NAMES_REFRESH_MS) return;
  namesLoading = true;
  fetchTenants()
    .catch(() => undefined)
    .finally(() => {
      namesLoadedAt = Date.now();
      namesLoading = false;
    });
}

// Re-renders the component when the tenant names are loaded or change, and makes sure they are loaded.
export function useTenantNames(): void {
  useEffect(ensureNames, []);
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
  if (!res.ok) throw new Error(`tenants: HTTP ${res.status}`);
  const body = await res.json();
  const options = (body.data ?? []) as TenantOption[];
  setNames(options);
  return options;
}

const OPTIONS_REFRESH_MS = 60_000;

/** The tenants the reader may pick from, refreshed every minute; null until the first answer. A refresh that fails
 * (a sign-in that lapsed, a rollout) keeps the last list, so the pick is not judged against an empty one. A
 * remembered pick that a successful answer does not cover is dropped rather than shown as an empty view. */
export function useTenantOptions(): TenantOption[] | null {
  const picked = useSelectedTenant();
  const [options, setOptions] = useState<TenantOption[] | null>(null);

  useEffect(() => {
    let active = true;
    const load = () =>
      fetchTenants()
        .then((o) => active && setOptions(o))
        .catch(() => {
          // keep the previous list; a transient failure must not reset the pick
        });
    load();
    const id = setInterval(load, OPTIONS_REFRESH_MS);
    return () => {
      active = false;
      clearInterval(id);
    };
  }, []);

  useEffect(() => {
    if (!options || !picked) return;
    const known = options.some((o) => o.key === picked) ||
      (picked.endsWith('/*') && options.some((o) => o.key.startsWith(picked.slice(0, -1))));
    if (!known) setSelectedTenant('');
  }, [options, picked]);

  return options;
}
