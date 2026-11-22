import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { sessionStore } from '@/auth/sessionStore';
import { renderApp } from '@/test/renderApp';

const fetchMock = vi.fn<typeof fetch>();
const json = (body: unknown, status = 200) =>
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

const seriesBundle = {
  seriesDetails: {
    title: {
      id: 'tmdb:tv:1396',
      name: 'Breaking Bad',
      mediaType: 'series',
      year: 2008,
      overview: 'A chemistry teacher.',
      genres: ['Drama'],
      ratings: [{ source: 'imdb', value: 9.5, max: 10 }],
      credits: { cast: [{ id: 1, name: 'Bryan Cranston', character: 'Walter White' }] },
    },
    seasons: [
      {
        id: 's1',
        name: 'Season 1',
        number: 1,
        episodes: [
          {
            id: 'e1',
            name: 'Pilot',
            seasonNumber: 1,
            episodeNumber: 1,
            airedDate: '2008-01-20',
            watched: true,
            itemId: 'i1',
          },
          {
            id: 'e2',
            name: 'Cat’s in the Bag',
            seasonNumber: 1,
            episodeNumber: 2,
            airedDate: '2008-01-27',
            resume: { position: 600, duration: 2880, percent: 20, eligible: true },
          },
        ],
      },
      {
        id: 's2',
        name: 'Season 2',
        number: 2,
        episodes: [
          {
            id: 'e3',
            name: 'Seven Thirty-Seven',
            seasonNumber: 2,
            episodeNumber: 1,
            airedDate: '2009-03-08',
          },
        ],
      },
    ],
  },
  upNextEpisode: { seasonNumber: 1, episodeNumber: 2 },
  resume: { position: 600, duration: 2880, percent: 20, eligible: true },
  inWatchlist: false,
  similar: [{ id: 'tmdb:tv:60059', name: 'Better Call Saul', mediaType: 'series', year: 2015 }],
};

const calls: { path: string; method: string; body?: unknown }[] = [];

beforeEach(() => {
  calls.length = 0;
  vi.stubGlobal('fetch', fetchMock);
  sessionStore.login({
    token: 't',
    expiresAt: '2099-01-01T00:00:00Z',
    accountId: 'a',
    username: 'u',
    isMaster: false,
  });
  sessionStore.selectProfile('p1');
  fetchMock.mockImplementation(async (input, init) => {
    const path = String(input).split('?')[0]!;
    const method = init?.method ?? 'GET';
    calls.push({ path, method, body: init?.body ? JSON.parse(String(init.body)) : undefined });
    if (path === '/api/users') return json([profile]);
    if (path === '/api/users/p1/settings') return json({});
    if (path === '/api/users/p1/details-bundle') return json(seriesBundle);
    if (path === '/api/library/matches') return json([]);
    if (path === '/api/users/p1/watchlist') return json({ id: 'tmdb:tv:1396' });
    if (path === '/api/users/p1/history/watched/bulk') return json({});
    return json({ error: 'nf' }, 404);
  });
});
afterEach(() => {
  fetchMock.mockReset();
  vi.unstubAllGlobals();
  sessionStore.logout();
});

const url = '/details?mediaType=series&titleId=tmdb%3Atv%3A1396&name=Breaking%20Bad';

describe('details page', () => {
  it('renders hero, resume action, seasons and episodes', async () => {
    renderApp(url);
    expect(
      await screen.findByRole('button', { name: /Resume S01E02/ }, { timeout: 4000 }),
    ).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Breaking Bad' })).toBeInTheDocument();
    expect(screen.getByText('A chemistry teacher.')).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Season 1', selected: true })).toBeInTheDocument();
    expect(screen.getByText('Pilot')).toBeInTheDocument();
    expect(screen.getByText('Bryan Cranston')).toBeInTheDocument();
    expect(screen.getAllByText('Better Call Saul').length).toBeGreaterThan(0);
  });

  it('switches seasons via the URL and toggles watchlist / episode watched', async () => {
    const user = userEvent.setup();
    const { router } = renderApp(url);
    await user.click(await screen.findByRole('tab', { name: 'Season 2' }));
    expect(await screen.findByText('Seven Thirty-Seven')).toBeInTheDocument();
    expect(router.state.location.search).toMatchObject({ season: 2 });

    await user.click(screen.getByRole('button', { name: 'Add to watchlist' }));
    await waitFor(() =>
      expect(calls.some((c) => c.path === '/api/users/p1/watchlist' && c.method === 'POST')).toBe(
        true,
      ),
    );
    expect(screen.getByRole('button', { name: 'In watchlist' })).toHaveAttribute(
      'aria-pressed',
      'true',
    );

    await user.click(screen.getByRole('button', { name: 'Mark episode 1 watched' }));
    await waitFor(() => {
      const bulk = calls.find((c) => c.path === '/api/users/p1/history/watched/bulk');
      expect(bulk?.body).toEqual([
        expect.objectContaining({
          mediaType: 'episode',
          seasonNumber: 2,
          episodeNumber: 1,
          watched: true,
          seriesId: 'tmdb:tv:1396',
        }),
      ]);
    });
  });
});
