import { describe, expect, it } from 'vitest';
import {
  addingRecent,
  failureNotice,
  mergeResults,
  personIdFromTitle,
  readRecent,
  RECENT_LIMIT,
  writeRecent,
  type SearchResult,
} from './search';

const r = (id: string, name: string, mediaType: string, score?: number): SearchResult => ({
  title: { id, name, mediaType },
  score,
});

describe('mergeResults', () => {
  it('dedupes by id keeping the best score, exact person first, then score', () => {
    const out = mergeResults(
      [
        r('tmdb:movie:1', 'Heat', 'movie', 50),
        r('tmdb:tv:2', 'Heat Wave', 'series', 80),
        r('tmdb:movie:1', 'Heat', 'movie', 90),
        r('tmdb:person:3', 'Al  Pacino', 'person', 10),
        r('tmdb:movie:4', 'Tie A', 'movie', 20),
        r('tmdb:movie:5', 'Tie B', 'movie', 20),
      ],
      'al pacino',
    );
    expect(out.map((x) => x.title.id)).toEqual([
      'tmdb:person:3',
      'tmdb:movie:1',
      'tmdb:tv:2',
      'tmdb:movie:4',
      'tmdb:movie:5',
    ]);
    expect(out[1]!.score).toBe(90);
  });
});

describe('failureNotice', () => {
  it('names the failed searches', () => {
    expect(failureNotice(new Set())).toBeUndefined();
    expect(failureNotice(new Set(['person']))).toBe(
      'Actor search is unavailable. Results may be incomplete.',
    );
    expect(failureNotice(new Set(['movie', 'series', 'person']))).toMatch(/couldn’t complete/);
  });
});

describe('recent searches', () => {
  it('puts the newest first, drops case-insensitive repeats and caps the list', () => {
    let items: string[] = [];
    for (let i = 0; i < 10; i++) items = addingRecent(items, `q${i}`);
    expect(items).toHaveLength(RECENT_LIMIT);
    expect(items[0]).toBe('q9');
    expect(addingRecent(['Heat', 'Dune'], ' heat ')).toEqual(['heat', 'Dune']);
    expect(addingRecent(['a'], '  ')).toEqual(['a']);
  });

  it('stores per profile', () => {
    writeRecent('p1', ['dune']);
    expect(readRecent('p1')).toEqual(['dune']);
    expect(readRecent('p2')).toEqual([]);
    writeRecent('p1', []);
    expect(readRecent('p1')).toEqual([]);
  });
});

describe('personIdFromTitle', () => {
  it('reads tmdbId or the tmdb:person id', () => {
    expect(personIdFromTitle({ id: 'x', tmdbId: 7 })).toBe(7);
    expect(personIdFromTitle({ id: 'tmdb:person:1158' })).toBe(1158);
    expect(personIdFromTitle({ id: 'tt1' })).toBeUndefined();
  });
});
