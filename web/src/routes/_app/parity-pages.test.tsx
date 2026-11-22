import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { sessionStore } from '@/auth/sessionStore';
import { renderApp } from '@/test/renderApp';

const fetchMock = vi.fn<typeof fetch>();
const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const profile = {
  id: 'p1',
  accountId: 'a',
  name: 'Alice',
  hasPin: false,
  hasIcon: false,
  isKidsProfile: false,
  allowShareLinks: false,
  createdAt: '',
  updatedAt: '',
};

type Handler = (url: URL, init?: RequestInit) => Response | undefined;
let handler: Handler = () => undefined;
let settings: Record<string, unknown> = {};

beforeEach(() => {
  settings = {
    display: { navigationTabVisibility: ['home', 'search', 'live'], unknownField: 7 },
    calendar: { mdblists: true },
    homeShelves: {
      shelves: [
        { id: 'watchlist', name: 'Watchlist', enabled: true, order: 0, type: 'builtin' },
        {
          id: 'm1',
          name: 'Oscars',
          enabled: true,
          order: 1,
          type: 'mdblist',
          listUrl: 'https://mdblist.com/lists/u/oscars',
        },
      ],
    },
  };
  vi.stubGlobal('fetch', fetchMock);
  fetchMock.mockImplementation(async (input, init) => {
    const url = new URL(String(input), 'http://localhost');
    if (url.pathname === '/api/users') return json(200, [profile]);
    if (url.pathname === '/api/users/p1/settings') {
      if (init?.method === 'PUT') settings = JSON.parse(String(init.body));
      return json(200, settings);
    }
    return handler(url, init) ?? json(404, { error: 'not found' });
  });
  sessionStore.login({
    token: 't',
    expiresAt: '2099-01-01T00:00:00Z',
    accountId: 'a',
    username: 'u',
    isMaster: false,
  });
  sessionStore.selectProfile('p1');
});
afterEach(() => {
  fetchMock.mockReset();
  vi.unstubAllGlobals();
  sessionStore.logout();
  handler = () => undefined;
});

describe('settings parity', () => {
  it('hides sidebar tabs per profile and saves a toggle keeping unknown fields', async () => {
    renderApp('/settings?section=menu');
    const nav = await screen.findByRole('navigation', { name: 'Main' });
    await waitFor(() => expect(within(nav).queryByRole('link', { name: 'Sports' })).toBeNull());
    expect(within(nav).getByRole('link', { name: 'Live TV' })).toBeInTheDocument();

    await userEvent.click(await screen.findByRole('switch', { name: 'Sports' }));
    await waitFor(() =>
      expect(within(nav).getByRole('link', { name: 'Sports' })).toBeInTheDocument(),
    );
    const display = settings.display as Record<string, unknown>;
    expect(display.navigationTabVisibility).toEqual(['home', 'search', 'live', 'sports']);
    expect(display.unknownField).toBe(7);
  });

  it('toggles a single MDBList shelf in the calendar', async () => {
    renderApp('/settings?section=calendar');
    await userEvent.click(await screen.findByRole('switch', { name: 'Oscars' }));
    await waitFor(() =>
      expect((settings.calendar as Record<string, unknown>).mdblistShelves).toEqual({ m1: false }),
    );
  });
});

describe('sports parity', () => {
  it('follows a team from the standings table', async () => {
    let saved: unknown;
    handler = (url, init) => {
      if (url.pathname === '/api/sports/status') return json(200, { enabled: true });
      if (url.pathname === '/api/sports/leagues')
        return json(200, { leagues: [{ id: 'nba', name: 'NBA', enabled: true }] });
      if (url.pathname === '/api/users/p1/sports/preferences') {
        if (init?.method === 'PUT') {
          saved = JSON.parse(String(init.body));
          return json(200, { ...(saved as object), revision: 4 });
        }
        return json(200, { revision: 3, preferences: { version: 1, teamIds: [] } });
      }
      if (url.pathname === '/api/sports/standings/nba')
        return json(200, {
          league: 'nba',
          state: 'available',
          groups: [
            {
              id: 'east',
              title: 'Eastern Conference',
              columns: [{ key: 'overall', label: 'W-L' }],
              rows: [
                { id: '2', name: 'Boston Celtics', kind: 'team', values: { overall: '12-3' } },
              ],
            },
          ],
        });
      return undefined;
    };
    renderApp('/sports?tab=standings');
    expect(await screen.findByText('Boston Celtics')).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Follow Boston Celtics' }));
    await waitFor(() =>
      expect(saved).toMatchObject({ revision: 3, preferences: { teamIds: ['nba:2'] } }),
    );
    expect(await screen.findByRole('button', { name: 'Unfollow Boston Celtics' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
  });
});
