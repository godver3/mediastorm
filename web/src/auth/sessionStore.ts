import { useSyncExternalStore } from 'react';
import { readJSON, STORAGE_KEYS, writeJSON } from '@/api/storage';
import type { AuthSession } from '@/api/types';

/** Treat unlock tokens as expired slightly early so we never send one about to be rejected. */
const UNLOCK_SKEW_MS = 30_000;

export interface ProfileUnlock {
  token: string;
  /** Epoch ms, or null when the server reported no expiry. */
  expiresAt: number | null;
}

export interface SessionState {
  session: AuthSession | null;
  profileId: string | null;
  unlocks: Record<string, ProfileUnlock>;
}

type Listener = () => void;

const listeners = new Set<Listener>();
let state: SessionState = load();

function load(): SessionState {
  return {
    session: readJSON<AuthSession>(STORAGE_KEYS.session),
    profileId: readJSON<string>(STORAGE_KEYS.profile),
    unlocks: readJSON<Record<string, ProfileUnlock>>(STORAGE_KEYS.unlocks) ?? {},
  };
}

function set(next: Partial<SessionState>) {
  state = { ...state, ...next };
  if ('session' in next) writeJSON(STORAGE_KEYS.session, state.session);
  if ('profileId' in next) writeJSON(STORAGE_KEYS.profile, state.profileId);
  if ('unlocks' in next) writeJSON(STORAGE_KEYS.unlocks, state.unlocks);
  listeners.forEach((l) => l());
}

function isValidUnlock(u: ProfileUnlock | undefined, now = Date.now()): u is ProfileUnlock {
  return !!u && (u.expiresAt === null || u.expiresAt - UNLOCK_SKEW_MS > now);
}

/**
 * Single source of truth for the session token, the selected profile and its
 * PIN unlock token. Persisted to localStorage. Read imperatively from the API
 * client and reactively via useSessionState().
 */
export const sessionStore = {
  getState: (): SessionState => state,
  subscribe(listener: Listener): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
  getToken: (): string | null => state.session?.token ?? null,
  /** A fresh login replaces everything (profile choice and unlocks are session-bound). */
  login(session: AuthSession) {
    set({ session, profileId: null, unlocks: {} });
  },
  /** A rotated session keeps the profile and unlocks (the server rebinds them). */
  replaceSession(session: AuthSession) {
    set({ session });
  },
  logout() {
    set({ session: null, profileId: null, unlocks: {} });
  },
  selectProfile(profileId: string | null) {
    // Keep only the active profile's unlock, like the Expo app.
    const unlocks: Record<string, ProfileUnlock> = {};
    if (profileId && isValidUnlock(state.unlocks[profileId])) {
      unlocks[profileId] = state.unlocks[profileId];
    }
    set({ profileId, unlocks });
  },
  setUnlock(profileId: string, token: string, expiresAt?: string) {
    const parsed = expiresAt ? Date.parse(expiresAt) : NaN;
    set({
      unlocks: {
        ...state.unlocks,
        [profileId]: { token, expiresAt: Number.isFinite(parsed) ? parsed : null },
      },
    });
  },
  clearUnlock(profileId: string) {
    const unlocks = { ...state.unlocks };
    delete unlocks[profileId];
    set({ unlocks });
  },
  /**
   * Comma-joined valid unlock tokens for X-Profile-Unlock ('' when none). With
   * profileIds, only those profiles' tokens; without, every stored token.
   */
  getUnlockHeader(profileIds?: string[]): string {
    const now = Date.now();
    const ids = profileIds ?? Object.keys(state.unlocks);
    return ids
      .map((id) => state.unlocks[id])
      .filter((u): u is ProfileUnlock => isValidUnlock(u, now))
      .map((u) => u.token)
      .join(',');
  },
  hasValidUnlock(profileId: string): boolean {
    return isValidUnlock(state.unlocks[profileId]);
  },
  /** Test helper: re-read from storage. */
  reload() {
    state = load();
    listeners.forEach((l) => l());
  },
};

export function useSessionState(): SessionState {
  return useSyncExternalStore(sessionStore.subscribe, sessionStore.getState, sessionStore.getState);
}
