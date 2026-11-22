import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { sessionStore } from '@/auth/sessionStore';
import { renderApp } from '@/test/renderApp';
import { resetWarmPrequeues } from './useWarmPrequeue';

const openPlayback = vi.hoisted(() => vi.fn());
vi.mock('@/lib/playback', async (orig) => ({
  ...(await orig<typeof import('@/lib/playback')>()),
  openPlayback,
}));

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

const movieBundle = {
  movieDetails: {
    id: 'tmdb:movie:949',
    name: 'Heat',
    mediaType: 'movie',
    year: 1995,
    tmdbId: 949,
  },
  resume: { position: 3874, duration: 10200, percent: 38, eligible: true },
};

const seriesBundle = {
  seriesDetails: {
    title: { id: 'tmdb:tv:1396', name: 'Breaking Bad', mediaType: 'series', tvdbId: 81189 },
    seasons: [
      {
        id: 's1',
        name: 'Season 1',
        number: 1,
        episodes: [{ id: 'e1', name: 'Pilot', seasonNumber: 1, episodeNumber: 1 }],
      },
    ],
    availableOrderings: [
      { type: 'official', name: 'Aired Order', isOfficial: true },
      { type: 'dvd', name: 'DVD Order' },
    ],
    activeOrdering: 'official',
  },
  upNextEpisode: { seasonNumber: 1, episodeNumber: 1 },
};

const warmStatus = {
  prequeueId: 'warm',
  status: 'ready',
  streamPath: '/debrid/Heat.1995.2160p.REMUX.mkv',
  audioTracks: [
    { index: 1, language: 'eng', codec: 'truehd' },
    { index: 2, language: 'fre', codec: 'ac3' },
  ],
  subtitleTracks: [{ index: 0, absoluteIndex: 3, language: 'eng', codec: 'subrip' }],
  trackSelection: { audio: { streamIndex: 1, typeIndex: 0 }, subtitleMode: 'off' },
};

const calls: { path: string; method: string; query: URLSearchParams; body?: unknown }[] = [];
let settings: Record<string, unknown> = {};
let libraryItems: unknown[] = [];
let memberships: unknown[] = [];

beforeEach(() => {
  calls.length = 0;
  settings = {};
  libraryItems = [];
  memberships = [];
  resetWarmPrequeues();
  openPlayback.mockReset();
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
    const u = new URL(String(input), 'http://x');
    const path = u.pathname;
    const method = init?.method ?? 'GET';
    calls.push({
      path,
      method,
      query: u.searchParams,
      body: init?.body ? JSON.parse(String(init.body)) : undefined,
    });
    if (path === '/api/users') return json([profile]);
    if (path === '/api/users/p1/settings') return json(settings);
    if (path === '/api/users/p1/details-bundle')
      return json(u.searchParams.get('type') === 'movie' ? movieBundle : seriesBundle);
    if (path === '/api/library/matches')
      return json(
        libraryItems.length
          ? [
              {
                libraryId: 'l',
                libraryName: 'Films',
                group: { id: 'g', title: 'Heat', items: libraryItems },
              },
            ]
          : [],
      );
    if (path === '/api/library/items/li1/playback')
      return json({ itemId: 'li1', streamPath: '/library/heat.mkv' });
    if (path === '/api/users/p1/hidden-items' && method === 'GET') return json([]);
    if (path === '/api/users/p1/hidden-items')
      return json({ id: 'tmdb:movie:949', mediaType: 'movie' });
    if (path === '/api/users/p1/custom-lists' && method === 'GET')
      return json([
        { id: 'l1', name: 'Heists', itemCount: 2 },
        { id: 'sa', name: 'Set Aside', itemCount: 1 },
      ]);
    if (path === '/api/users/p1/custom-lists') return json({ id: 'l2', name: 'Faves' });
    if (path === '/api/users/p1/custom-lists/l1/items' && method === 'GET')
      return json(memberships);
    if (path.startsWith('/api/users/p1/custom-lists/')) return json({});
    if (path === '/api/users/p1/series-ordering/81189') return json({ seasonType: 'dvd' });
    if (path === '/api/playback/prequeue') return json({ prequeueId: 'warm', status: 'queued' });
    if (path === '/api/playback/prequeue/warm') return json(warmStatus);
    return json({ error: 'nf' }, 404);
  });
});
afterEach(() => {
  fetchMock.mockReset();
  vi.unstubAllGlobals();
  sessionStore.logout();
});

const movieUrl = '/details?mediaType=movie&titleId=tmdb%3Amovie%3A949&name=Heat&tmdbId=949';
const seriesUrl = '/details?mediaType=series&titleId=tmdb%3Atv%3A1396&name=Breaking%20Bad';
const T = { timeout: 4000 };

describe('details actions (native parity)', () => {
  it('adds to and removes from custom lists, and creates one with the title in it', async () => {
    memberships = [{ id: 'tmdb:movie:949', mediaType: 'movie' }];
    const user = userEvent.setup();
    renderApp(movieUrl);
    await user.click(await screen.findByRole('button', { name: 'Lists' }, T));
    const heists = await screen.findByRole('checkbox', { name: /Heists/ }, T);
    expect(heists).toHaveAttribute('aria-checked', 'true');
    expect(screen.queryByRole('checkbox', { name: /Set Aside/ })).toBeNull();
    await user.click(heists);
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === 'DELETE' &&
            c.path === '/api/users/p1/custom-lists/l1/items/movie/tmdb%3Amovie%3A949',
        ),
      ).toBe(true),
    );
    await user.type(screen.getByRole('textbox', { name: 'New list name' }), 'Faves');
    await user.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.method === 'POST' && c.path === '/api/users/p1/custom-lists/l2/items')
          ?.body,
      ).toMatchObject({ id: 'tmdb:movie:949', mediaType: 'movie', name: 'Heat' }),
    );
  });

  it('hides the title from the profile', async () => {
    const user = userEvent.setup();
    renderApp(movieUrl);
    await user.click(await screen.findByRole('button', { name: 'More options' }, T));
    await user.click(await screen.findByRole('menuitem', { name: /Hide from this profile/ }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.method === 'POST' && c.path === '/api/users/p1/hidden-items')?.body,
      ).toMatchObject({ id: 'tmdb:movie:949', mediaType: 'movie', externalIds: { tmdb: '949' } }),
    );
  });

  it('warms the prequeue only when enabled, and plays it with the picked tracks', async () => {
    settings = {
      playback: { disablePrequeue: false },
      display: { showDetailsSelectedTracks: true },
    };
    const user = userEvent.setup();
    renderApp(movieUrl);
    await user.click(await screen.findByRole('button', { name: 'Audio & Subtitles' }, T));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByText('French · AC-3'));
    await user.click(within(dialog).getByText('English'));
    await user.click(within(dialog).getByRole('button', { name: 'Play' }));
    await waitFor(() => expect(openPlayback).toHaveBeenCalled());
    expect(openPlayback.mock.calls[0]![0]).toMatchObject({
      prequeueId: 'warm',
      preselectedAudioTrack: '2',
      preselectedSubtitleTrack: '3',
      startOffset: 3874,
    });
    // One warm-up, reused by Play.
    expect(calls.filter((c) => c.path === '/api/playback/prequeue').length).toBe(1);
    expect(screen.getByTestId('details-info-line')).toHaveTextContent('Audio: French · AC-3');
  });

  it('does not warm up when the profile leaves prequeue off (the server default)', async () => {
    settings = { playback: { disablePrequeue: true } };
    renderApp(movieUrl);
    await screen.findByRole('button', { name: /Resume/ }, T);
    await new Promise((r) => setTimeout(r, 50));
    expect(calls.some((c) => c.path === '/api/playback/prequeue')).toBe(false);
    expect(screen.queryByRole('button', { name: 'Audio & Subtitles' })).toBeNull();
  });

  it('asks Resume or Start over after picking a version', async () => {
    libraryItems = [
      { id: 'li1', fileName: 'Heat 2160p.mkv', probe: { width: 3840, height: 1600 } },
      { id: 'li2', fileName: 'Heat 1080p.mkv', probe: { width: 1920, height: 800 } },
    ];
    const user = userEvent.setup();
    renderApp(movieUrl);
    await user.click(await screen.findByRole('button', { name: '2 versions' }, T));
    await user.click(await screen.findByText('Films · Heat 2160p.mkv', { exact: false }));
    expect(await screen.findByRole('button', { name: 'Resume from 1:04:34' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Start over' }));
    await waitFor(() => expect(openPlayback).toHaveBeenCalled());
    const params = openPlayback.mock.calls[0]![0];
    expect(params.sourcePath).toBe(encodeURIComponent('/library/heat.mkv'));
    expect(params.startOffset).toBeUndefined();
  });

  it('switches the episode ordering and saves it for the series', async () => {
    const user = userEvent.setup();
    renderApp(seriesUrl);
    await user.click(await screen.findByRole('button', { name: 'More options' }, T));
    await user.click(await screen.findByRole('menuitemradio', { name: 'DVD Order' }));
    await waitFor(() =>
      expect(
        calls.find((c) => c.method === 'PUT' && c.path === '/api/users/p1/series-ordering/81189')
          ?.body,
      ).toEqual({ seasonType: 'dvd' }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.path === '/api/users/p1/details-bundle' && c.query.get('seasonType') === 'dvd',
        ),
      ).toBe(true),
    );
  });
});
