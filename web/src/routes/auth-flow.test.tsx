import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { sessionStore } from '@/auth/sessionStore';
import { renderApp } from '@/test/renderApp';

const fetchMock = vi.fn<typeof fetch>();
const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const session = {
  token: 't',
  expiresAt: '2099-01-01T00:00:00Z',
  accountId: 'a',
  username: 'u',
  isMaster: false,
};
const profiles = [
  {
    id: 'p1',
    accountId: 'a',
    name: 'Alice',
    hasPin: false,
    hasIcon: false,
    isKidsProfile: false,
    allowShareLinks: false,
    createdAt: '',
    updatedAt: '',
  },
  {
    id: 'p2',
    accountId: 'a',
    name: 'Bob',
    hasPin: true,
    pinLength: 4,
    hasIcon: false,
    isKidsProfile: false,
    allowShareLinks: false,
    createdAt: '',
    updatedAt: '',
  },
];

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock);
  sessionStore.logout();
});
afterEach(() => {
  fetchMock.mockReset();
  vi.unstubAllGlobals();
});

function route(handlers: Record<string, () => Response>) {
  fetchMock.mockImplementation(async (input) => {
    const path = String(input).split('?')[0]!;
    const h = handlers[path];
    return h ? h() : json(404, { error: 'not found' });
  });
}

describe('auth flow', () => {
  it('redirects to login and shows the backend error', async () => {
    route({
      '/api/auth/login': () =>
        json(401, { error: 'invalid username or password', code: 'invalid_credentials' }),
    });
    renderApp('/');
    await userEvent.type(await screen.findByLabelText('Username'), 'me');
    await userEvent.type(screen.getByLabelText('Password'), 'bad');
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Incorrect username or password.');
    const body = JSON.parse(String(fetchMock.mock.calls[0]![1]!.body));
    expect(body).toEqual({ rememberMe: true, username: 'me', password: 'bad' });
  });

  it('logs in, picks a profile, and lands in the shell', async () => {
    route({
      '/api/auth/login': () => json(200, session),
      '/api/users': () => json(200, profiles),
      '/api/users/p1/settings': () =>
        json(200, { display: { appearance: { accentColor: '#ff0000' } } }),
    });
    renderApp('/login');
    await userEvent.type(await screen.findByLabelText('Username'), 'me');
    await userEvent.type(screen.getByLabelText('Password'), 'pw');
    await userEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Alice' }));
    expect(await screen.findByRole('navigation', { name: 'Main' })).toBeInTheDocument();
    expect(sessionStore.getState().profileId).toBe('p1');
    await waitFor(() =>
      expect(document.documentElement.style.getPropertyValue('--accent')).toBe('#ff0000'),
    );
  });

  it('asks for a PIN and stores the unlock token', async () => {
    sessionStore.login(session);
    route({
      '/api/users': () => json(200, profiles),
      '/api/users/p2/pin/verify': () =>
        json(200, { valid: true, unlockToken: 'pu_x', expiresAt: '2099-01-01T00:00:00Z' }),
      '/api/users/p2/settings': () => json(200, {}),
    });
    renderApp('/profiles');
    await userEvent.click(await screen.findByRole('button', { name: 'Bob' }));
    await userEvent.type(await screen.findByLabelText('PIN'), '1234');
    await userEvent.click(screen.getByRole('button', { name: 'Unlock' }));
    await screen.findByRole('navigation', { name: 'Main' });
    expect(sessionStore.hasValidUnlock('p2')).toBe(true);
  });

  it('global search navigates to /search?q=', async () => {
    sessionStore.login(session);
    sessionStore.selectProfile('p1');
    route({
      '/api/users': () => json(200, profiles),
      '/api/users/p1/settings': () => json(200, {}),
    });
    const { router } = renderApp('/');
    await userEvent.type(await screen.findByRole('searchbox', { name: 'Search' }), 'heat{Enter}');
    await waitFor(() => expect(router.state.location.pathname).toBe('/search'));
    expect(router.state.location.search).toEqual({ q: 'heat' });
  });
});
