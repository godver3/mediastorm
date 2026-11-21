/** localStorage helpers that never throw (private mode, blocked storage). */
export function readJSON<T>(key: string): T | null {
  try {
    const raw = localStorage.getItem(key);
    return raw ? (JSON.parse(raw) as T) : null;
  } catch {
    return null;
  }
}

export function writeJSON(key: string, value: unknown): void {
  try {
    if (value === null || value === undefined) localStorage.removeItem(key);
    else localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* storage unavailable */
  }
}

export const STORAGE_KEYS = {
  session: 'msweb.session',
  profile: 'msweb.profileId',
  unlocks: 'msweb.profileUnlocks',
  clientId: 'msweb.clientId',
  sidebar: 'msweb.sidebarCollapsed',
} as const;

let memoryClientId: string | null = null;

/** Stable per-browser client ID, sent as X-Client-ID. */
export function getClientId(): string {
  const stored = readJSON<string>(STORAGE_KEYS.clientId);
  if (stored) return stored;
  if (memoryClientId) return memoryClientId;
  const id =
    typeof crypto !== 'undefined' && 'randomUUID' in crypto
      ? crypto.randomUUID()
      : `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}`;
  memoryClientId = `web-${id}`;
  writeJSON(STORAGE_KEYS.clientId, memoryClientId);
  return memoryClientId;
}
