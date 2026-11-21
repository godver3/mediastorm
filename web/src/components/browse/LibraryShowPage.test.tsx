import { afterEach, describe, expect, it, vi } from 'vitest';
import { libraryResume } from '@/api/library';
import { orderSeasons } from './LibraryShowPage';
import { clock } from './useLibraryStart';

const json = (body: unknown) =>
  new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
  });

afterEach(() => vi.unstubAllGlobals());

describe('library show page helpers', () => {
  it('puts specials after the real seasons', () => {
    const s = (n: number) => ({ seasonNumber: n, episodes: [] });
    expect(orderSeasons([s(2), s(0), s(1)]).map((x) => x.seasonNumber)).toEqual([1, 2, 0]);
  });

  it('formats resume points', () => {
    expect(clock(65)).toBe('1:05');
    expect(clock(3725)).toBe('1:02:05');
  });

  it('finds a file’s resume point by its stream path', async () => {
    const fetchMock = vi.fn<typeof fetch>(async () =>
      json([
        {
          mediaType: 'episode',
          itemId: 'localmedia:a/b.mkv',
          resume: { position: 0, duration: 10, percent: 0, eligible: false },
        },
        {
          mediaType: 'episode',
          itemId: 'localmedia:x/y.mkv',
          resume: { position: 600, duration: 2400, percent: 25, eligible: true },
        },
      ]),
    );
    vi.stubGlobal('fetch', fetchMock);
    expect(await libraryResume('p1', 'episode', 'localmedia:x/y.mkv')).toMatchObject({
      position: 600,
    });
    expect(await libraryResume('p1', 'episode', 'localmedia:a/b.mkv')).toBeUndefined();
    expect(await libraryResume('p1', 'movie', 'localmedia:x/y.mkv')).toBeUndefined();
    expect(String(fetchMock.mock.calls[0]![0])).toContain('/api/users/p1/history/progress');
  });
});
