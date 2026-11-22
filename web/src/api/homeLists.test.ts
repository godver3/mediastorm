import { describe, expect, it } from 'vitest';
import {
  homeShelfAllowed,
  homeViewIds,
  listShelfQuery,
  planShelves,
  resolveHomeView,
  toHomeSettings,
  type ContinueWatchingItem,
} from './home';
import {
  becauseYouWatched,
  connectedTiles,
  hubSections,
  resolveListKey,
  serviceTiles,
  shelfBrowseList,
} from './homeLists';
import { parseListKey } from './lists';

const mdb = {
  id: 'mdb-1',
  name: 'Best of A24',
  type: 'mdblist',
  enabled: true,
  order: 3,
  listUrl: 'https://mdblist.com/lists/x/a24',
};
const settings = toHomeSettings({
  homeShelves: {
    itemCap: 20,
    shelves: [
      { id: 'continue-watching', enabled: true, order: 0 },
      { id: 'trending-movies', enabled: true, order: 1 },
      { id: 'trending-tv', enabled: true, order: 2, limit: 5 },
      mdb,
      { id: 'genre-28-movie', enabled: true, order: 4 },
      { id: 'my-upcoming', enabled: true, order: 5 },
      { id: 'dashboard', enabled: true, order: 6 },
      {
        id: 'hub',
        name: 'Collections',
        type: 'collection-hub',
        enabled: true,
        order: 7,
        collectionItems: [
          { id: 'c2', name: 'Action', sourceShelfId: 'genre-28-movie', order: 2 },
          { id: 'c1', sourceShelfId: 'mdb-1', heroArtUrl: 'https://img/a24.jpg', order: 1 },
          { id: 'c3', sourceShelfId: 'missing' },
        ],
      },
    ],
    views: {
      'page-anime': { name: 'Anime', mediaFilter: 'shows', mode: 'custom', shelves: [mdb] },
      'page-old': { name: 'Old', deleted: true },
    },
  },
});

describe('See all', () => {
  it('links rows to their list unless the shelf has its own limit', () => {
    const plan = planShelves(settings);
    const by = (id: string) => plan.find((p) => p.id === id)!;
    expect(by('continue-watching').seeAll).toBe('continue-watching');
    expect(by('trending-movies').seeAll).toBe('trending.movie');
    expect(by('trending-tv')).toMatchObject({ limited: true, seeAll: undefined, cap: 5 });
    expect(by('mdb-1').seeAll).toBe('shelf.mdb-1');
    expect(by('genre-28-movie').seeAll).toBe('genre.movie.28');
    expect(by('dashboard').seeAll).toBeUndefined();
    for (const p of plan) if (p.seeAll) expect(parseListKey(p.seeAll)).not.toBeNull();
  });

  it('resolves a shelf key to its display-list query', () => {
    expect(shelfBrowseList('mdb-1', settings)).toEqual({
      kind: 'remote',
      id: 'mdb-1',
      name: 'Best of A24',
      query: {
        name: 'Best of A24',
        source: 'mdblist',
        url: 'https://mdblist.com/lists/x/a24',
      },
    });
    expect(resolveListKey({ kind: 'shelf', shelfId: 'dashboard' }, settings)).toBeNull();
    expect(resolveListKey({ kind: 'shelf', shelfId: 'mdb-1' }, undefined)).toBeUndefined();
    expect(resolveListKey({ kind: 'hidden' }, undefined)).toEqual({ kind: 'hidden' });
    expect(resolveListKey({ kind: 'service', serviceId: 'netflix', part: 1 }, settings)).toEqual(
      expect.objectContaining({
        kind: 'remote',
        name: 'Netflix · Shows',
        query: expect.objectContaining({ source: 'mdblist' }),
      }),
    );
  });
});

describe('Home views', () => {
  it('lists All, Movies, Shows and live custom pages', () => {
    expect(homeViewIds(settings.views)).toEqual(['all', 'movies', 'shows', 'page-anime']);
  });

  it('drops shelves a Movies view cannot show and tags list queries', () => {
    const movies = resolveHomeView(settings, 'movies');
    expect(movies.filter).toBe('movies');
    expect(movies.shelves.map((s) => s.id)).not.toContain('trending-tv');
    expect(movies.shelves.map((s) => s.id)).not.toContain('my-upcoming');
    expect(movies.shelves.map((s) => s.id)).not.toContain('dashboard');
    const row = planShelves(movies).find((p) => p.id === 'trending-movies')!;
    expect(listShelfQuery(row, movies).homeView).toBe('movies');
    expect(homeShelfAllowed({ id: 'genre-18-tv' }, 'movies')).toBe(false);
  });

  it('uses a custom page’s own shelves, and falls back to All for unknown views', () => {
    const anime = resolveHomeView(settings, 'page-anime');
    expect(anime.shelves.map((s) => s.id)).toEqual(['mdb-1']);
    expect(anime.filter).toBe('shows');
    expect(resolveHomeView(settings, 'page-old').view).toBe('all');
    expect(resolveHomeView(settings, 'nope').view).toBe('all');
  });
});

describe('Lists catalog', () => {
  it('builds connected, hub and service tiles from Home shelves', () => {
    expect(connectedTiles(settings.shelves)).toEqual([
      { key: 'shelf.mdb-1', title: 'Best of A24', kicker: 'MDBList' },
    ]);
    expect(hubSections(settings.shelves)).toEqual([
      {
        title: 'Collections',
        tiles: [
          {
            key: 'shelf.mdb-1',
            title: 'Best of A24',
            imageUrl: 'https://img/a24.jpg',
            logoUrl: undefined,
          },
          { key: 'genre.movie.28', title: 'Action', imageUrl: undefined, logoUrl: undefined },
        ],
      },
    ]);
    expect(serviceTiles(settings.shelves)[0]).toMatchObject({
      key: 'service.netflix.0',
      title: 'Netflix',
      kicker: 'Movies',
    });
  });

  it('alternates movie and show seeds, three each at most', () => {
    const cw = (id: string, tmdb: string, title: string): ContinueWatchingItem => ({
      seriesId: id,
      seriesTitle: title,
      externalIds: { tmdb },
    });
    const seeds = becauseYouWatched([
      cw('tmdb:tv:1', '1', 'S1'),
      cw('tmdb:movie:2', '2', 'M1'),
      cw('tmdb:tv:1', '1', 'S1 again'),
      cw('tmdb:tv:3', '3', 'S2'),
      cw('tmdb:movie:4', '4', 'M2'),
      cw('tmdb:movie:5', '5', 'M3'),
      cw('tmdb:movie:6', '6', 'M4'),
      { seriesId: 'x', seriesTitle: 'No ids' },
    ]);
    expect(seeds.map((s) => s.seed)).toEqual(['M1', 'S1', 'M2', 'S2', 'M3']);
    expect(seeds[1]).toMatchObject({ kind: 'similar', mediaType: 'series', tmdbId: '1' });
  });
});
