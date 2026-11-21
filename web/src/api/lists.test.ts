import { describe, expect, it } from 'vitest';
import {
  askAIList,
  itemToTitle,
  listKey,
  listQuery,
  listTitle,
  optionsQuery,
  parseListKey,
  toBrowsePage,
  type BrowseList,
} from './lists';

describe('list keys', () => {
  const lists: BrowseList[] = [
    { kind: 'watchlist' },
    { kind: 'custom', id: 'abc.12:3' },
    { kind: 'permanent-prequeue' },
    { kind: 'top-ten' },
    { kind: 'top-ten', mediaType: 'series' },
    { kind: 'trending', mediaType: 'movie' },
    { kind: 'popular-on-server' },
    { kind: 'personalized' },
    { kind: 'genre', mediaType: 'series', genreId: 10765 },
    { kind: 'decade', mediaType: 'movie', decade: 1990 },
  ];
  it.each(lists)('round-trips %o', (list) => {
    expect(parseListKey(listKey(list))).toEqual(list);
  });
  it('rejects junk', () => {
    expect(parseListKey('nope')).toBeNull();
    expect(parseListKey('genre.movie.x')).toBeNull();
    expect(parseListKey('trending.anime')).toBeNull();
  });
  it('names lists like native', () => {
    expect(listTitle({ kind: 'genre', mediaType: 'series', genreId: 10765 })).toBe(
      'Sci-Fi & Fantasy Shows',
    );
    expect(listTitle({ kind: 'decade', mediaType: 'movie', decade: 1980 })).toBe('1980s Movies');
    expect(listTitle({ kind: 'top-ten' })).toBe('Top 10 Today');
  });
});

describe('display-list query', () => {
  it('maps sources', () => {
    expect(listQuery({ kind: 'custom', id: 'l1' })).toEqual({
      source: 'custom-list',
      listId: 'l1',
    });
    expect(listQuery({ kind: 'genre', mediaType: 'movie', genreId: 28 })).toEqual({
      source: 'genre',
      genreId: '28',
      mediaType: 'movie',
    });
  });
  it('adds sort and filters for own lists', () => {
    expect(
      optionsQuery(
        { kind: 'watchlist' },
        {
          sort: 'name',
          type: 'series',
          status: 'in_progress',
          genres: ['Drama', 'Comedy'],
          letter: 'B',
        },
      ),
    ).toEqual({
      sortBy: 'name',
      sortDirection: 'asc',
      includeFacets: 'true',
      filterMediaType: 'series',
      watchStatus: 'in_progress',
      genres: 'Comedy,Drama',
      alphabet: 'B',
    });
  });
  it('drops filters and unsupported sorts for charts', () => {
    expect(optionsQuery({ kind: 'top-ten' }, { sort: 'year', type: 'movie' })).toEqual({});
    expect(
      optionsQuery({ kind: 'trending', mediaType: 'movie' }, { sort: 'rating', dir: 'asc' }),
    ).toEqual({ sortBy: 'rating', sortDirection: 'asc' });
  });
  it("sends the rating source for the profile's own lists only", () => {
    expect(
      optionsQuery({ kind: 'watchlist' }, { sort: 'rating', ratingSource: 'tomatoes' }),
    ).toMatchObject({ sortBy: 'rating', ratingSource: 'tomatoes' });
    expect(optionsQuery({ kind: 'watchlist' }, { sort: 'rating' })).toMatchObject({
      ratingSource: 'imdb',
    });
  });
  it('round-trips remote, Ask AI and special keys', () => {
    expect(parseListKey('shelf.mdb-1')).toEqual({ kind: 'shelf', shelfId: 'mdb-1' });
    const ai = askAIList('cozy 90s mysteries');
    expect(listKey(ai)).toBe('ai.cozy 90s mysteries');
    expect(parseListKey(listKey(ai))).toEqual(ai);
    expect(listQuery(ai)).toMatchObject({ source: 'custom-ai', q: 'cozy 90s mysteries' });
    expect(listKey({ kind: 'remote', id: 'x.y', name: 'X', query: {} })).toBe('shelf.x.y');
    expect(parseListKey('continue-watching')).toEqual({ kind: 'continue-watching' });
    expect(parseListKey('hidden')).toEqual({ kind: 'hidden' });
    expect(parseListKey('ai.')).toBeNull();
    const similar: BrowseList = {
      kind: 'similar',
      mediaType: 'series',
      tmdbId: '1399',
      seed: 'G.O.T',
    };
    expect(parseListKey(listKey(similar))).toEqual(similar);
    expect(listQuery(similar)).toEqual({ source: 'similar', tmdbId: '1399', mediaType: 'series' });
    expect(parseListKey('service.netflix.2')).toEqual({
      kind: 'service',
      serviceId: 'netflix',
      part: 2,
    });
  });
  it('reads plain-title pages (similar)', () => {
    const page = toBrowsePage({ kind: 'similar', mediaType: 'movie', tmdbId: '1', seed: 'X' }, {
      items: [{ id: 'tmdb:movie:2', name: 'Two', mediaType: 'movie' }],
      total: 1,
    } as never);
    expect(page.entries.map((e) => e.title.name)).toEqual(['Two']);
  });
});

describe('mappers', () => {
  it('maps a watchlist item to a Title', () => {
    const t = itemToTitle({
      id: 'tmdb:tv:1396',
      mediaType: 'series',
      name: 'Breaking Bad',
      year: 2008,
      posterUrl: 'p.jpg',
      externalIds: { tmdb: '1396', imdb: 'tt0903747', tvdb: '81189' },
      watchState: 'partial',
      unwatchedCount: 3,
    });
    expect(t).toMatchObject({
      id: 'tmdb:tv:1396',
      mediaType: 'series',
      tmdbId: 1396,
      tvdbId: 81189,
      imdbId: 'tt0903747',
      poster: { url: 'p.jpg' },
      watchState: 'partial',
      unwatchedCount: 3,
    });
  });
  it('builds pages for both item shapes', () => {
    const items = toBrowsePage(
      { kind: 'watchlist' },
      {
        items: [{ id: 'x', mediaType: 'movie', name: 'Heat' }],
        total: 1,
        sourceTotal: 40,
        genres: ['Crime'],
        alphabetBuckets: ['H'],
      },
    );
    expect(items.total).toBe(40);
    expect(items.entries[0]).toMatchObject({ key: 'movie:x', item: { id: 'x' } });
    expect(items.genres).toEqual(['Crime']);
    const ranked = toBrowsePage(
      { kind: 'top-ten' },
      { items: [{ rank: 1, title: { id: 'y', name: 'Dune', mediaType: 'movie' } }], total: 1 },
    );
    expect(ranked.entries[0]).toMatchObject({ key: 'movie:y', rank: 1, title: { name: 'Dune' } });
    expect(ranked.entries[0]!.item).toBeUndefined();
  });
});
