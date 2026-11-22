import { describe, expect, it } from 'vitest';
import {
  airBadge,
  calendarTitleId,
  comingUp,
  continueWatchingCard,
  DEFAULT_STREAMING_SERVICES,
  heroShelf,
  matchesListed,
  planShelves,
  recentlyAired,
  remoteQuery,
  resolveServices,
  toHomeSettings,
  type CalendarItem,
  type ContinueWatchingItem,
} from './home';

const settings = (shelves: Parameters<typeof toHomeSettings>[0]) => toHomeSettings(shelves);

describe('toHomeSettings', () => {
  it('sorts shelves and applies defaults', () => {
    const s = settings({
      homeShelves: {
        shelves: [
          { id: 'b', order: 2 },
          { id: 'a', order: 1 },
        ],
      },
      display: { hideWatched: true },
    });
    expect(s.shelves.map((x) => x.id)).toEqual(['a', 'b']);
    expect(s.itemCap).toBe(20);
    expect(s.hideWatched).toBe(true);
    expect(s.excludeUpcomingFromContinue).toBe(true);
    expect(s.topShelfMode).toBe('default');
  });
});

describe('planShelves', () => {
  const base = settings({
    homeShelves: {
      itemCap: 15,
      shelves: [
        { id: 'top-ten', name: 'Top 10 Today', enabled: true, order: 0 },
        { id: 'continue-watching', name: 'Continue Watching', enabled: true, order: 1 },
        { id: 'watch-something', name: 'Watch Something', enabled: true, order: 2 },
        { id: 'tonight', name: 'Tonight', enabled: false, order: 3 },
        { id: 'watch-together', name: 'Watch Together', enabled: true, order: -1 },
        { id: 'trending-movies', name: 'Trending Movies', enabled: true, order: 9, limit: 5 },
        { id: 'genre-28-movie', name: 'Action', enabled: true, order: 10 },
        {
          id: 'trakt-1',
          name: 'My Trakt',
          type: 'trakt',
          enabled: true,
          order: 11,
          traktAccountId: 'acc',
          traktListType: 'watchlist',
        },
        { id: 'broken-mdb', type: 'mdblist', enabled: true, order: 12 },
      ],
    },
  });

  it('keeps enabled, known shelves in order with caps and card styles', () => {
    const plan = planShelves(base);
    expect(plan.map((p) => p.id)).toEqual([
      'top-ten',
      'continue-watching',
      'watch-something',
      'trending-movies',
      'genre-28-movie',
      'trakt-1',
    ]);
    expect(plan.find((p) => p.id === 'trending-movies')!.cap).toBe(5);
    expect(plan.find((p) => p.id === 'top-ten')!.cap).toBe(15);
    expect(plan.find((p) => p.id === 'continue-watching')!.landscape).toBe(true);
    expect(plan.find((p) => p.id === 'top-ten')!.landscape).toBe(false);
    expect(plan.find((p) => p.id === 'genre-28-movie')!.source).toEqual({
      kind: 'list',
      query: { source: 'genre', genreId: '28', mediaType: 'movie' },
      ranked: false,
      shape: 'ranked',
    });
    expect(heroShelf(plan)?.id).toBe('top-ten');
  });

  it('drops top-ten when the top shelf is disabled and swaps in a source shelf', () => {
    expect(planShelves({ ...base, topShelfMode: 'disabled' }).some((p) => p.id === 'top-ten')).toBe(
      false,
    );
    const swapped = planShelves({ ...base, topShelfMode: 'shelf', topShelfSourceId: 'trakt-1' });
    expect(swapped[0]!.id).toBe('trakt-1');
    expect(swapped.some((p) => p.id === 'top-ten')).toBe(false);
  });

  it('gives curated kids profiles their list rows and no watch-something', () => {
    const plan = planShelves(base, {
      isKidsProfile: true,
      kidsMode: 'content_list',
      kidsAllowedLists: ['https://mdblist.com/lists/u/pixar-classics'],
    });
    const ids = plan.map((p) => p.id);
    expect(ids).not.toContain('top-ten');
    expect(ids).not.toContain('trending-movies');
    expect(ids).not.toContain('watch-something');
    const kids = plan.find((p) => p.id === 'kids-list-0')!;
    expect(kids.title).toBe('Pixar Classics');
  });
});

describe('remoteQuery', () => {
  it('builds external list queries like the native client', () => {
    expect(remoteQuery({ id: 'x', name: 'L', type: 'letterboxd', letterboxdListUrl: 'u' })).toEqual(
      { name: 'L', source: 'letterboxd-list', listUrl: 'u' },
    );
    expect(
      remoteQuery({ id: 'x', type: 'simkl', simklAccountId: 'a', simklMediaType: 'movies' }),
    ).toEqual({ name: 'x', source: 'simkl-list', accountId: 'a', mediaType: 'movies' });
    expect(
      remoteQuery({ id: 'x', name: 'M', type: 'mdblist', listUrl: ' u ', hideUnreleased: true }),
    ).toEqual({ name: 'M', hideUnreleased: 'true', source: 'mdblist', url: 'u' });
    expect(remoteQuery({ id: 'x', type: 'trakt', traktAccountId: 'a' })).toBeUndefined();
  });
});

const cw = (over: Partial<ContinueWatchingItem>): ContinueWatchingItem => ({
  seriesId: 'tmdb:tv:1399',
  seriesTitle: 'Game of Thrones',
  externalIds: { tmdb: '1399', imdb: 'tt0944947' },
  ...over,
});

describe('continueWatchingCard', () => {
  it('maps the next episode, resume progress and ids', () => {
    const card = continueWatchingCard(
      cw({
        nextEpisode: { seasonNumber: 2, episodeNumber: 3, title: 'What Is Dead May Never Die' },
        resume: { position: 600, duration: 3000, percent: 20, eligible: true },
        backdropUrl: 'b',
      }),
    );
    expect(card.subtitle).toBe('S02E03 · What Is Dead May Never Die');
    expect(card.progress).toBeCloseTo(0.2);
    expect(card.season).toBe(2);
    expect(card.episode).toBe(3);
    expect(card.link).toMatchObject({ mediaType: 'series', tmdbId: 1399, imdbId: 'tt0944947' });
  });

  it('uses percentWatched for movies without resume state', () => {
    const card = continueWatchingCard(
      cw({
        seriesId: 'tmdb:movie:27205',
        seriesTitle: 'Inception',
        year: 2010,
        percentWatched: 50,
      }),
    );
    expect(card.subtitle).toBe('2010');
    expect(card.progress).toBe(0.5);
    expect(card.link?.mediaType).toBe('movie');
    expect(card.season).toBeUndefined();
  });
});

describe('matchesListed', () => {
  it('matches by id or shared provider id of the same kind', () => {
    const item = cw({});
    expect(matchesListed({ id: 'TMDB:TV:1399', mediaType: 'series' }, item)).toBe(true);
    expect(
      matchesListed({ id: 'x', mediaType: 'series', externalIds: { imdb: 'TT0944947' } }, item),
    ).toBe(true);
    expect(matchesListed({ id: 'tmdb:tv:1399', mediaType: 'movie' }, item)).toBe(false);
  });
});

describe('calendar rows', () => {
  const now = new Date(2026, 10, 21, 12, 0);
  const item = (over: Partial<CalendarItem>): CalendarItem => ({
    title: 'Show',
    mediaType: 'series',
    airDate: '2026-11-22',
    externalIds: { tmdb: '1' },
    seasonNumber: 1,
    episodeNumber: 1,
    ...over,
  });

  it('builds the details id like native', () => {
    expect(calendarTitleId(item({}))).toBe('tmdb:tv:1');
    expect(calendarTitleId(item({ mediaType: 'movie', externalIds: { imdb: 'tt1' } }))).toBe('tt1');
  });

  it('coming up: one per title from today, earliest first', () => {
    const cards = comingUp(
      [
        item({ airDate: '2026-11-25', episodeNumber: 2 }),
        item({ airDate: '2026-11-22' }),
        item({ title: 'Old', airDate: '2026-11-01', externalIds: { tmdb: '2' } }),
        item({
          title: 'Film',
          mediaType: 'movie',
          airDate: '2026-11-23',
          externalIds: { tmdb: '3' },
        }),
      ],
      10,
      now,
    );
    expect(cards.map((c) => c.title)).toEqual(['Show', 'Film']);
    expect(cards[0]!.subtitle).toBe('S01E01');
    expect(cards[0]!.badge).toBe('Tomorrow');
  });

  it('recently aired: watchlist source by default, newest first', () => {
    const cards = recentlyAired(
      [
        item({ airDate: '2026-11-10', source: 'watchlist' }),
        item({
          title: 'B',
          airDate: '2026-11-19',
          source: 'watchlist',
          externalIds: { tmdb: '9' },
        }),
        item({ title: 'T', airDate: '2026-11-20', source: 'trending', externalIds: { tmdb: '8' } }),
        item({
          title: 'Future',
          airDate: '2026-11-30',
          source: 'watchlist',
          externalIds: { tmdb: '7' },
        }),
      ],
      undefined,
      10,
      now,
    );
    expect(cards.map((c) => c.title)).toEqual(['B', 'Show']);
  });

  it('airBadge is empty once aired', () => {
    expect(airBadge({ airDate: '2026-11-20' }, now)).toBeUndefined();
    expect(airBadge({ airDateTimeUTC: new Date(2026, 10, 21, 20).toISOString() }, now)).toBe(
      'Today',
    );
  });
});

describe('resolveServices', () => {
  it('falls back to the defaults and cleans configured services', () => {
    expect(resolveServices(undefined)).toBe(DEFAULT_STREAMING_SERVICES);
    const out = resolveServices([
      { id: 'a', name: 'A', logoUrl: 'l', order: 2, lists: [{ url: 'u' }, { url: 'u' }] },
      {
        id: 'b',
        name: 'B',
        logoUrl: 'l',
        order: 1,
        lists: [{ url: 'https://mdblist.com/lists/snoak/top-hulu-movies/' }],
      },
      { id: 'c', name: 'C', logoUrl: 'l', enabled: false, lists: [{ url: 'x' }] },
      { id: 'd', name: 'D', lists: [{ url: 'x' }] },
    ]);
    expect(out.map((s) => s.id)).toEqual(['b', 'a']);
    expect(out[1]!.lists).toHaveLength(1);
    expect(out[0]!.lists[0]!.url).toBe('https://mdblist.com/lists/azodath/top-hulu-movies/json');
  });
});
