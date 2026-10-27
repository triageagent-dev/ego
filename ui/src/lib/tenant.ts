import { useEffect, useRef, useState, useSyncExternalStore } from 'react';
import { onFeedMessage, onFeedOpen } from './connection';

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

let namesLoaded = false;
let namesLoading = false;

// Loads the names once: whoever shows a name asks for it, so it does not depend on the tenant switcher (which is
// not on screen when the sidebar is hidden) having been drawn. Later changes arrive with the switcher's reloads.
function ensureNames(): void {
  if (namesLoading || namesLoaded) return;
  namesLoading = true;
  fetchTenants()
    .then(() => {
      namesLoaded = true;
    })
    .catch(() => undefined)
    .finally(() => {
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

/** The tenants the reader may pick from, read again when the feed says sessions were written for one the list does
 * not have, or that data changed, or after it reconnects; null until the first answer. A refresh that fails
 * (a sign-in that lapsed, a rollout) keeps the last list, so the pick is not judged against an empty one. A
 * remembered pick that a successful answer does not cover is dropped rather than shown as an empty view. */
export function useTenantOptions(): TenantOption[] | null {
  const picked = useSelectedTenant();
  const [options, setOptions] = useState<TenantOption[] | null>(null);
  const known = useRef<Set<string>>(new Set());

  useEffect(() => {
    let active = true;
    let busy = false;
    let again = false;
    // A change that arrives while the list is being read folds into one more read after it.
    const load = () => {
      if (busy) {
        again = true;
        return;
      }
      busy = true;
      fetchTenants()
        .then((o) => {
          if (!active) return;
          known.current = new Set(o.map((x) => x.key));
          setOptions(o);
        })
        .catch(() => {
          // keep the previous list; a transient failure must not reset the pick
        })
        .finally(() => {
          busy = false;
          if (again && active) {
            again = false;
            load();
          }
        });
    };
    load();
    const offMessage = onFeedMessage((data) => {
      if (data?.type === 'resync' || (data?.type === 'changed' && data.topic === 'usage')) load();
      else if (data?.type === 'session_started') {
        const keys: string[] = data.session?.tenants ?? [];
        if (keys.some((k) => !known.current.has(k))) load();
      }
    });
    const offOpen = onFeedOpen(load);
    return () => {
      active = false;
      offMessage();
      offOpen();
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
