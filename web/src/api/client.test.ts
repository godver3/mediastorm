import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { sessionStore } from '@/auth/sessionStore';
import { ApiError, apiRequest, invalidatesSession, profileIdsForRequest } from './client';
import type { AuthSession } from './types';

const farFuture = new Date(Date.now() + 30 * 24 * 3600 * 1000).toISOString();
const session = (token: string, expiresAt = farFuture): AuthSession => ({
  token,
  expiresAt,
  accountId: 'acc',
  username: 'u',
  isMaster: false,
});

function json(status: number, body: unknown) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

const fetchMock = vi.fn<typeof fetch>();

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock);
  sessionStore.logout();
});
afterEach(() => {
  fetchMock.mockReset();
  vi.unstubAllGlobals();
});

const headersOf = (call: number) =>
  new Headers(fetchMock.mock.calls[call]![1]!.headers as HeadersInit);

describe('apiRequest', () => {
  it('sends bearer token, client id, and only the named profile unlock', async () => {
    sessionStore.login(session('tok'));
    sessionStore.setUnlock('p1', 'pu_1');
    sessionStore.setUnlock('p2', 'pu_2');
    fetchMock.mockResolvedValueOnce(json(200, []));
    await apiRequest('/users/p1/watchlist');
    const [url] = fetchMock.mock.calls[0]!;
    expect(url).toBe('/api/users/p1/watchlist');
    const h = headersOf(0);
    expect(h.get('Authorization')).toBe('Bearer tok');
    expect(h.get('X-Client-ID')).toMatch(/^web-/);
    expect(h.get('X-Profile-Unlock')).toBe('pu_1');
    expect(h.get('X-PIN')).toBeNull();
  });

  it('throws ApiError with status and code', async () => {
    fetchMock.mockResolvedValueOnce(
      json(401, { error: 'invalid username or password', code: 'invalid_credentials' }),
    );
    const err = (await apiRequest('/auth/login', {
      method: 'POST',
      body: {},
      noAuthRecovery: true,
    }).catch((e: unknown) => e)) as ApiError;
    expect(err).toBeInstanceOf(ApiError);
    expect(err.status).toBe(401);
    expect(err.code).toBe('invalid_credentials');
    expect(err.message).toBe('invalid username or password');
  });

  it('refreshes once on session_expired and retries with the new token', async () => {
    sessionStore.login(session('old'));
    fetchMock
      .mockResolvedValueOnce(json(401, { error: 'x', code: 'session_expired' }))
      .mockResolvedValueOnce(json(200, session('new')))
      .mockResolvedValueOnce(json(200, { ok: true }));
    await expect(apiRequest('/users')).resolves.toEqual({ ok: true });
    expect(fetchMock.mock.calls[1]![0]).toBe('/api/auth/refresh');
    expect(headersOf(1).get('Authorization')).toBe('Bearer old');
    expect(headersOf(2).get('Authorization')).toBe('Bearer new');
    expect(sessionStore.getToken()).toBe('new');
  });

  it('signs out when refresh is rejected', async () => {
    sessionStore.login(session('old'));
    sessionStore.selectProfile('p1');
    fetchMock
      .mockResolvedValueOnce(json(401, { error: 'x', code: 'session_expired' }))
      .mockResolvedValueOnce(json(401, { error: 'x', code: 'session_expired' }));
    await expect(apiRequest('/users')).rejects.toBeInstanceOf(ApiError);
    expect(sessionStore.getState().session).toBeNull();
    expect(sessionStore.getState().profileId).toBeNull();
  });

  it('does not refresh or sign out on a wrong PIN', async () => {
    sessionStore.login(session('tok'));
    fetchMock.mockResolvedValueOnce(json(401, { error: 'invalid PIN', code: 'pin_invalid' }));
    await expect(
      apiRequest('/users/p1/pin/verify', { method: 'POST', body: { pin: '1' } }),
    ).rejects.toBeInstanceOf(ApiError);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(sessionStore.getToken()).toBe('tok');
  });

  it('refreshes ahead of expiry inside the 7-day window', async () => {
    sessionStore.login(session('old', new Date(Date.now() + 3600_000).toISOString()));
    fetchMock.mockResolvedValueOnce(json(200, session('new'))).mockResolvedValueOnce(json(200, []));
    await apiRequest('/users');
    expect(fetchMock.mock.calls[0]![0]).toBe('/api/auth/refresh');
    expect(headersOf(1).get('Authorization')).toBe('Bearer new');
  });

  it('deselects the active profile on profile_locked', async () => {
    sessionStore.login(session('tok'));
    sessionStore.selectProfile('p1');
    sessionStore.setUnlock('p1', 'pu_1');
    fetchMock.mockResolvedValueOnce(
      json(403, { error: 'locked', code: 'profile_locked', profileId: 'p1' }),
    );
    await expect(apiRequest('/users/p1/settings')).rejects.toBeInstanceOf(ApiError);
    expect(sessionStore.getState().profileId).toBeNull();
    expect(sessionStore.hasValidUnlock('p1')).toBe(false);
  });
});

describe('helpers', () => {
  it('finds profile ids like the server middleware', () => {
    expect(profileIdsForRequest('/users/a%20b/x', { profileId: 'c' }, { userId: 'd' })).toEqual([
      'a b',
      'c',
      'd',
    ]);
    expect(profileIdsForRequest('/branding')).toEqual([]);
  });

  it('treats bare 401s (older servers) as session loss unless about the PIN', () => {
    const mk = (body: string) => new ApiError({ status: 401, message: body, body, url: '' });
    expect(invalidatesSession(mk('unauthorized'))).toBe(true);
    expect(invalidatesSession(mk('invalid PIN'))).toBe(false);
  });
});
