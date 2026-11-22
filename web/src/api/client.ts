import { pinPrompt } from '@/auth/pinPrompt';
import { sessionStore } from '@/auth/sessionStore';
import { apiPath } from './basePath';
import { getClientId } from './storage';
import type { AuthSession } from './types';

/** Error thrown for any non-2xx response. `code` is the backend's stable apierror code. */
export class ApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly body: unknown;
  readonly url: string;
  /** For 403 profile_locked: the profile that needs unlocking. */
  readonly profileId?: string;

  constructor(opts: {
    status: number;
    message: string;
    code?: string;
    body: unknown;
    url: string;
  }) {
    super(opts.message);
    this.name = 'ApiError';
    this.status = opts.status;
    this.code = opts.code;
    this.body = opts.body;
    this.url = opts.url;
    const pid = (opts.body as { profileId?: unknown } | null)?.profileId;
    this.profileId = typeof pid === 'string' ? pid : undefined;
  }
}

/** Codes meaning the session itself is unusable (refresh, then sign out). */
export const SESSION_INVALID_CODES = new Set([
  'unauthorized',
  'session_expired',
  'account_expired',
]);

export const isSessionInvalidError = (err: unknown): boolean =>
  err instanceof ApiError && err.status === 401 && SESSION_INVALID_CODES.has(err.code ?? '');

/** Wrong or missing profile PIN (the session is fine). Mirrors native APIError.isWrongPIN. */
export const isWrongPinError = (err: unknown): boolean => {
  if (!(err instanceof ApiError)) return false;
  if (err.code) return err.code === 'pin_invalid' || err.code === 'pin_required';
  return err.status === 401 && /pin/i.test(typeof err.body === 'string' ? err.body : err.message);
};

/**
 * The session is gone: a session code, or a bare 401 from an older server that
 * is not a PIN failure. Mirrors native APIError.invalidatesSession.
 */
export const invalidatesSession = (err: unknown): boolean => {
  if (!(err instanceof ApiError)) return false;
  if (err.code) return SESSION_INVALID_CODES.has(err.code);
  return err.status === 401 && !isWrongPinError(err);
};

/** Refresh ahead of expiry when less than this remains (native refreshWindow: 7 days). */
export const REFRESH_WINDOW_MS = 7 * 24 * 3600 * 1000;

export function sessionNeedsRefresh(expiresAt: string | undefined, now = Date.now()): boolean {
  const t = expiresAt ? Date.parse(expiresAt) : NaN;
  return Number.isFinite(t) && t - now < REFRESH_WINDOW_MS;
}

/**
 * Profile ids a request names, the way the server's PIN middleware finds them:
 * /users/{id}/... or /profiles/{id}/..., profileId/userId in the query, and
 * top-level profileId/userId in a JSON body. Only these profiles' unlock tokens
 * are sent (same as the native clients).
 */
export function profileIdsForRequest(
  endpoint: string,
  query?: Record<string, QueryValue>,
  body?: unknown,
): string[] {
  const ids: string[] = [];
  const add = (v: unknown) => {
    if (typeof v !== 'string') return;
    const id = v.trim();
    if (id && !ids.includes(id)) ids.push(id);
  };
  const parts = endpoint.split('?')[0]!.split('/').filter(Boolean);
  if (parts.length >= 2 && (parts[0] === 'users' || parts[0] === 'profiles')) {
    try {
      add(decodeURIComponent(parts[1]!));
    } catch {
      add(parts[1]);
    }
  }
  const b = body && typeof body === 'object' ? (body as Record<string, unknown>) : {};
  for (const key of ['profileId', 'userId']) {
    add(query?.[key]);
    add(b[key]);
  }
  return ids;
}

export const isProfileLockedError = (err: unknown): boolean =>
  err instanceof ApiError && err.status === 403 && err.code === 'profile_locked';

export type QueryValue = string | number | boolean | null | undefined;

export interface RequestOptions {
  method?: 'GET' | 'POST' | 'PUT' | 'PATCH' | 'DELETE';
  /** JSON-serialised request body. */
  body?: unknown;
  query?: Record<string, QueryValue>;
  signal?: AbortSignal;
  headers?: Record<string, string>;
  /** Skip the refresh-and-retry behaviour (used by the auth endpoints themselves). */
  noAuthRecovery?: boolean;
}

function buildUrl(endpoint: string, query?: Record<string, QueryValue>): string {
  let url = apiPath(endpoint);
  if (query) {
    const params = new URLSearchParams();
    for (const [k, v] of Object.entries(query)) {
      if (v !== undefined && v !== null && v !== '') params.set(k, String(v));
    }
    const qs = params.toString();
    if (qs) url += (url.includes('?') ? '&' : '?') + qs;
  }
  return url;
}

/**
 * Headers for a request: X-Client-ID always, Bearer token when signed in, and
 * X-Profile-Unlock with the unlock tokens of the profiles the request names.
 * Exported for code that must fetch manually.
 */
export function authHeaders(profileIds: string[] = []): Record<string, string> {
  const headers: Record<string, string> = { 'X-Client-ID': getClientId() };
  const token = sessionStore.getToken();
  if (token) headers.Authorization = `Bearer ${token}`;
  const unlock = sessionStore.getUnlockHeader(profileIds);
  if (unlock) headers['X-Profile-Unlock'] = unlock;
  return headers;
}

async function parseBody(response: Response): Promise<unknown> {
  const text = await response.text();
  if (!text) return null;
  try {
    return JSON.parse(text);
  } catch {
    return text;
  }
}

async function performRequest<T>(endpoint: string, opts: RequestOptions): Promise<T> {
  const url = buildUrl(endpoint, opts.query);
  const headers: Record<string, string> = {
    Accept: 'application/json',
    ...authHeaders(profileIdsForRequest(endpoint, opts.query, opts.body)),
    ...opts.headers,
  };
  let body: BodyInit | undefined;
  if (opts.body !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.body);
  }
  const response = await fetch(url, {
    method: opts.method ?? 'GET',
    headers,
    body,
    signal: opts.signal,
  });
  const data = await parseBody(response);
  if (!response.ok) {
    const envelope = (data && typeof data === 'object' ? data : {}) as {
      error?: unknown;
      code?: unknown;
    };
    const message =
      typeof envelope.error === 'string'
        ? envelope.error
        : typeof data === 'string' && data.trim()
          ? data.trim()
          : `Request failed (${response.status})`;
    throw new ApiError({
      status: response.status,
      message,
      code: typeof envelope.code === 'string' ? envelope.code : undefined,
      body: data,
      url,
    });
  }
  return data as T;
}

let refreshInFlight: Promise<AuthSession | null> | null = null;

/**
 * Rotate the session via POST /api/auth/refresh. Single-flight: the server
 * deletes the old token as soon as it issues a new one, so concurrent callers
 * must share one request. Resolves null on failure; a 401 also signs out.
 */
export function refreshSession(): Promise<AuthSession | null> {
  if (!refreshInFlight) {
    refreshInFlight = (async () => {
      const token = sessionStore.getToken();
      if (!token) return null;
      try {
        const next = await performRequest<AuthSession>('/auth/refresh', { method: 'POST' });
        if (!next?.token || sessionStore.getToken() !== token) return null;
        sessionStore.replaceSession(next);
        return next;
      } catch (err) {
        // Rejected (vs. a network failure): the session cannot be recovered.
        const rejected =
          err instanceof ApiError && (invalidatesSession(err) || [403, 404].includes(err.status));
        if (rejected && sessionStore.getToken() === token) {
          sessionStore.logout();
        }
        return null;
      }
    })().finally(() => {
      refreshInFlight = null;
    });
  }
  return refreshInFlight;
}

/**
 * Typed JSON request against /api. Like the native clients it refreshes ahead
 * of expiry (inside REFRESH_WINDOW_MS), and when the server says the session is
 * gone it refreshes once and retries once; a rejected refresh signs out. On a
 * 403 profile_locked the stale unlock is dropped and the user is asked for the PIN
 * in place (auth/pinPrompt); on success the request is retried once. Cancelling
 * deselects the active profile, so the route guard sends the user to the picker.
 */
export async function apiRequest<T>(endpoint: string, opts: RequestOptions = {}): Promise<T> {
  if (!opts.noAuthRecovery && sessionNeedsRefresh(sessionStore.getState().session?.expiresAt)) {
    await refreshSession();
  }
  const tokenUsed = sessionStore.getToken();
  try {
    return await performRequest<T>(endpoint, opts);
  } catch (err) {
    if (!(err instanceof ApiError) || opts.noAuthRecovery) throw err;
    if (invalidatesSession(err) && tokenUsed) {
      // Another request may already have rotated the token.
      if (sessionStore.getToken() !== tokenUsed && sessionStore.getToken()) {
        return performRequest<T>(endpoint, opts);
      }
      if (await refreshSession()) return performRequest<T>(endpoint, opts);
      throw err;
    }
    if (isProfileLockedError(err)) {
      const { profileId } = sessionStore.getState();
      const locked = err.profileId ?? profileId;
      if (locked) {
        sessionStore.clearUnlock(locked);
        // Prompt for the PIN in place and retry once (native behaviour). Cancelling
        // (or no prompt UI mounted) falls back to deselecting the active profile.
        if (await pinPrompt.request(locked)) return performRequest<T>(endpoint, opts);
        if (locked === profileId) sessionStore.selectProfile(null);
      }
    }
    throw err;
  }
}
