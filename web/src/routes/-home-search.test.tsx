import { screen, waitFor, within } from '@testing-library/react';
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
const title = (id: string, name: string, mediaType = 'movie', extra = {}) => ({
  id,
  name,
  mediaType,
  year: 2024,
  backdrop: { url: `https://img/${id}.jpg` },
  ...extra,
});

function route(handlers: Record<string, (url: URL) => Response>) {
  fetchMock.mockImplementation(async (input) => {
    const url = new URL(String(input), 'http://localhost');
    const h = handlers[url.pathname];
    return h ? h(url) : json({ error: 'not found' }, 404);
  });
}

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock);
  localStorage.clear();
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
});

describe('Home', () => {
  it('renders the hero and configured shelves in order, linking cards to details', async () => {
    route({
      '/api/users': () => json([profile]),
      '/api/users/p1/settings': () =>
        json({
          homeShelves: {
            shelves: [
              { id: 'top-ten', name: 'Top 10 Today', enabled: true, order: 0 },
              { id: 'continue-watching', name: 'Continue Watching', enabled: true, order: 1 },
              { id: 'watchlist', name: 'Your Watchlist', enabled: true, order: 2 },
              { id: 'calendar', name: 'Coming Up', enabled: false, order: 3 },
            ],
          },
        }),
      '/api/users/p1/custom-lists': () => json([]),
      '/api/users/p1/history/continue': () =>
        json([
          {
            seriesId: 'tmdb:tv:100',
            seriesTitle: 'Severance',
            externalIds: { tmdb: '100' },
            nextEpisode: { seasonNumber: 2, episodeNumber: 4, title: 'Woe’s Hollow' },
            resume: { position: 10, duration: 100, percent: 10, eligible: true },
          },
        ]),
      '/api/users/p1/display-list': (url) => {
        const source = url.searchParams.get('source');
        if (source === 'top-ten')
          return json({
            items: [
              { rank: 1, title: title('tmdb:movie:1', 'Dune: Part Two', 'movie', { tmdbId: 1 }) },
            ],
            total: 1,
          });
        if (source === 'watchlist')
          return json({
            items: [{ id: 'tmdb:movie:2', mediaType: 'movie', name: 'Heat', year: 1995 }],
            total: 1,
          });
        return json({ items: [], total: 0 });
      },
    });
    renderApp('/');
    expect(await screen.findByRole('region', { name: 'Top 10 Today' })).toBeInTheDocument();
    const more = await screen.findByRole('link', { name: /More info/ });
    expect(more.getAttribute('href')).toContain('/details?');
    expect(more.getAttribute('href')).toContain('titleId=tmdb%3Amovie%3A1');

    const cw = await screen.findByRole('link', { name: 'Severance' });
    expect(cw.getAttribute('href')).toContain('season=2');
    expect(cw.getAttribute('href')).toContain('episode=4');
    expect(await screen.findByRole('link', { name: 'Heat' })).toBeInTheDocument();
    const headings = screen.getAllByRole('heading', { level: 2 }).map((h) => h.textContent);
    expect(headings.indexOf('Continue Watching')).toBeLessThan(
      headings.findIndex((h) => h?.startsWith('Your Watchlist')),
    );
    expect(headings).not.toContain('Coming Up');

    const calls = fetchMock.mock.calls.map((c) => String(c[0]));
    expect(calls.some((c) => c.includes('source=top-ten'))).toBe(true);
  });
});

describe('Search', () => {
  it('shows Top 10 before typing, then grouped results; remembers searches', async () => {
    route({
      '/api/users': () => json([profile]),
      '/api/users/p1/settings': () => json({}),
      '/api/discover/top-ten': () =>
        json({ items: [{ rank: 1, title: title('tmdb:movie:9', 'Wicked') }] }),
      '/api/search': (url) => {
        expect(url.searchParams.get('userId')).toBe('p1');
        const type = url.searchParams.get('type');
        if (type === 'movie') return json([{ title: title('tmdb:movie:1', 'Heat'), score: 90 }]);
        if (type === 'series') return json([]);
        return json([
          {
            title: { id: 'tmdb:person:1158', name: 'Al Pacino', mediaType: 'person', tmdbId: 1158 },
            score: 40,
          },
        ]);
      },
    });
    const { router } = renderApp('/search');
    expect(await screen.findByRole('link', { name: 'Wicked' })).toBeInTheDocument();

    await userEvent.type(screen.getByLabelText('Search movies, shows and people'), 'heat{enter}');
    await waitFor(() => expect(router.state.location.search).toMatchObject({ q: 'heat' }));
    const movies = await screen.findByRole('region', { name: 'Movies' });
    expect(within(movies).getByRole('link', { name: 'Heat' })).toBeInTheDocument();
    const people = screen.getByRole('region', { name: 'People' });
    expect(within(people).getByRole('link', { name: 'Al Pacino' }).getAttribute('href')).toContain(
      '/person?id=1158',
    );

    await userEvent.click(screen.getByRole('tab', { name: /Movies/ }));
    await waitFor(() => expect(screen.queryByRole('region', { name: 'People' })).toBeNull());
    expect(router.state.location.search).toMatchObject({ q: 'heat', type: 'movie' });
    expect(JSON.parse(localStorage.getItem('msweb.recentSearches.p1')!)).toEqual(['heat']);
  });
});
