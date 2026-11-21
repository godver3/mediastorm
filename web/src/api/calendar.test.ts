import { describe, expect, it } from 'vitest';
import {
  calendarItemKey,
  calendarItemTitle,
  groupCalendarDays,
  mapSeriesEnrichment,
  monthGrid,
  weekDays,
  type CalendarItem,
} from './calendar';

const ep = (p: Partial<CalendarItem>): CalendarItem => ({
  title: 'Severance',
  mediaType: 'series',
  airDate: '2026-11-21',
  ...p,
});

describe('groupCalendarDays', () => {
  it('groups by day, sorts by time with all-day last, keeps server order on ties', () => {
    const items = [
      ep({ title: 'B', airDate: '2026-11-22' }),
      ep({ title: 'C', airTime: '21:00' }),
      ep({ title: 'D' }),
      ep({ title: 'A', airTime: '09:00' }),
      ep({ title: 'M', mediaType: 'movie', airDate: '2026-11-22' }),
      ep({ title: 'X', airDate: '' }),
    ];
    const days = groupCalendarDays(items, 'all');
    expect(days.map((d) => d.key)).toEqual(['2026-11-21', '2026-11-22']);
    expect(days[0]!.items.map((i) => i.title)).toEqual(['A', 'C', 'D']);
    expect(groupCalendarDays(items, 'movies').flatMap((d) => d.items.map((i) => i.title))).toEqual([
      'M',
    ]);
    expect(groupCalendarDays(items, 'series').flatMap((d) => d.items)).toHaveLength(4);
  });
});

describe('item identity and links', () => {
  it('builds a stable key from external ids', () => {
    expect(
      calendarItemKey(ep({ externalIds: { tmdb: '95396' }, seasonNumber: 2, episodeNumber: 3 })),
    ).toBe('tmdb:95396:series:2026-11-21:2:3:');
  });
  it('derives the title id like native CalendarItem.titleID', () => {
    expect(calendarItemTitle(ep({ externalIds: { tmdb: '95396', tvdb: '1' } })).id).toBe(
      'tmdb:tv:95396',
    );
    expect(calendarItemTitle(ep({ externalIds: { tvdb: '371980' } })).id).toBe('tvdb:371980');
    const movie = calendarItemTitle(
      ep({ mediaType: 'movie', externalIds: { imdb: 'tt1', tmdb: '7' } }),
    );
    expect(movie).toMatchObject({
      id: 'tmdb:movie:7',
      mediaType: 'movie',
      tmdbId: 7,
      imdbId: 'tt1',
    });
  });
});

describe('date grids', () => {
  it('month grid is 42 days starting on a Monday', () => {
    const grid = monthGrid('2026-11-21');
    expect(grid).toHaveLength(42);
    expect(grid[0]).toBe('2026-10-26');
    expect(grid).toContain('2026-11-30');
  });
  it('week is Monday to Sunday', () => {
    expect(weekDays('2026-11-21')).toEqual([
      '2026-11-16',
      '2026-11-17',
      '2026-11-18',
      '2026-11-19',
      '2026-11-20',
      '2026-11-21',
      '2026-11-22',
    ]);
  });
});

describe('mapSeriesEnrichment', () => {
  it('prefers the episode overview and runtime, keeps top 8 cast', () => {
    const cast = Array.from({ length: 10 }, (_, i) => ({ name: `Actor ${i}` }));
    const e = mapSeriesEnrichment(
      {
        title: {
          id: 's',
          name: 'S',
          mediaType: 'series',
          overview: 'Show',
          runtimeMinutes: 50,
          genres: ['Drama'],
          credits: { cast },
        },
        seasons: [
          { number: 2, episodes: [{ episodeNumber: 3, overview: 'Episode', runtimeMinutes: 42 }] },
        ],
      },
      ep({ seasonNumber: 2, episodeNumber: 3 }),
    );
    expect(e.synopsis).toBe('Episode');
    expect(e.runtimeMinutes).toBe(42);
    expect(e.cast).toHaveLength(8);
    expect(e.genres).toEqual(['Drama']);
  });
});
