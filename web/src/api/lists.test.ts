import { describe, expect, it } from 'vitest';
import {
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
    ).toEqual({ sortBy: 'rating', sortDirection: 'asc', ratingSource: 'imdb' });
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
