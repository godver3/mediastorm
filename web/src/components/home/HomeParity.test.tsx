import { screen, waitFor } from '@testing-library/react';
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

const homeSettings = {
  homeShelves: {
    shelves: [
      { id: 'continue-watching', name: 'Continue Watching', enabled: true, order: 0 },
      { id: 'trending-movies', name: 'Trending Movies', enabled: true, order: 2 },
      {
        id: 'mdb-1',
        name: 'Best of A24',
        type: 'mdblist',
        listUrl: 'https://mdblist.com/lists/x/a24',
        enabled: true,
        order: 1,
      },
    ],
  },
};

let cw: Record<string, unknown>[] = [];
const calls: { url: URL; init?: RequestInit }[] = [];

beforeEach(() => {
  cw = [
    {
      seriesId: 'tmdb:movie:949',
      seriesTitle: 'Heat',
      year: 1995,
      percentWatched: 40,
      externalIds: { tmdb: '949' },
    },
    {
      seriesId: 'tmdb:tv:1396',
      seriesTitle: 'Breaking Bad',
      externalIds: { tmdb: '1396' },
      nextEpisode: { seasonNumber: 2, episodeNumber: 3, title: 'Bit by a Dead Bee' },
    },
  ];
  calls.length = 0;
  vi.stubGlobal('fetch', fetchMock);
  fetchMock.mockImplementation(async (input, init) => {
    const url = new URL(String(input), 'http://localhost');
    calls.push({ url, init });
    const p = url.pathname;
    if (p === '/api/users') return json(200, [profile]);
    if (p === '/api/users/p1/settings') return json(200, homeSettings);
    if (p === '/api/settings') return json(200, { metadata: { aiApiKey: '••••' } });
    if (p === '/api/users/p1/history/continue') return json(200, cw);
    if (p === '/api/users/p1/custom-lists') return json(200, []);
    if (p === '/api/users/p1/hidden-items') return json(200, []);
    if (p === '/api/users/p1/history/continue/hide') {
      const { seriesId } = JSON.parse(String(init?.body)) as { seriesId: string };
      cw = cw.filter((c) => c.seriesId !== seriesId);
      return json(200, {});
    }
    if (p === '/api/users/p1/display-list') {
      return json(200, {
        items: [
          {
            rank: 1,
            title: { id: 'tmdb:movie:1', name: 'Moonlight', mediaType: 'movie', year: 2016 },
          },
        ],
        total: 40,
      });
    }
    return json(404, { error: 'not found' });
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
});

describe('Home parity', () => {
  it('hides a Continue Watching card from its menu', async () => {
    renderApp('/');
    expect(await screen.findByRole('link', { name: 'Heat' })).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Actions for Heat' }));
    await userEvent.click(
      await screen.findByRole('menuitem', { name: 'Hide from Continue Watching' }),
    );
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Heat' })).toBeNull());
    const hide = calls.find((c) => c.url.pathname.endsWith('/history/continue/hide'));
    expect(JSON.parse(String(hide?.init?.body))).toEqual({ seriesId: 'tmdb:movie:949' });
    expect(screen.getByRole('link', { name: 'Breaking Bad' })).toBeInTheDocument();
  });

  it('offers an episode Mark as Watched for a series card', async () => {
    renderApp('/');
    await userEvent.click(await screen.findByRole('button', { name: 'Actions for Breaking Bad' }));
    expect(
      await screen.findByRole('menuitem', { name: 'Mark S02E03 as Watched' }),
    ).toBeInTheDocument();
  });

  it('opens a connected shelf’s See all as a list grid', async () => {
    const { router } = renderApp('/');
    const seeAll = await screen.findByRole('link', { name: 'See all Best of A24' });
    await userEvent.click(seeAll);
    await waitFor(() => expect(router.state.location.pathname).toBe('/lists/shelf.mdb-1'));
    expect(await screen.findByRole('heading', { name: 'Best of A24' })).toBeInTheDocument();
    const grid = () =>
      calls
        .filter((c) => c.url.pathname === '/api/users/p1/display-list')
        .map((c) => c.url.searchParams)
        .find((q) => q.get('limit') === '60');
    await waitFor(() => expect(grid()?.get('source')).toBe('mdblist'));
    expect(grid()?.get('url')).toBe('https://mdblist.com/lists/x/a24');
  });

  it('switches Home to the Movies view', async () => {
    const { router } = renderApp('/');
    await userEvent.click(await screen.findByRole('radio', { name: 'Movies' }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ view: 'movies' }));
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Breaking Bad' })).toBeNull());
    expect(screen.getByRole('link', { name: 'Heat' })).toBeInTheDocument();
    await waitFor(() =>
      expect(calls.some((c) => c.url.searchParams.get('homeView') === 'movies')).toBe(true),
    );
  });

  it('shows Continue Watching and connected lists on the Lists page', async () => {
    renderApp('/lists');
    expect(await screen.findByRole('link', { name: /Continue Watching/ })).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'Connected lists' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Ask AI' })).toBeInTheDocument();
  });
});
