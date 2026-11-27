import { afterEach, describe, expect, it, vi } from 'vitest';
import { readLiveMemory, writeLiveMemory } from '@/components/live/liveMemory';
import { canPageEarlier, guideStart, HOUR, shiftGuideStart } from '@/lib/guide';
import {
  canReplay,
  catchupStartQuery,
  fetchCatchup,
  fetchStremioStreams,
  guideHistory,
  hasStremioSources,
  liveStartQuery,
  mapChannel,
  mapStremioStreams,
  type LiveChannel,
} from './live';

const DAY = 24 * HOUR;
const now = Date.parse('2026-11-21T18:10:00Z');
const channel: LiveChannel = {
  id: 'cu:42',
  name: 'Catch-up Test',
  url: 'http://provider/live/u/p/42.ts',
  sourceId: 'cu',
  tvgId: 'cu42',
  catchup: { days: 3, mode: 'xc' },
};

afterEach(() => vi.restoreAllMocks());

const mockFetch = (body: unknown) =>
  vi
    .spyOn(globalThis, 'fetch')
    .mockResolvedValue(
      new Response(JSON.stringify(body), {
        status: 200,
        headers: { 'content-type': 'application/json' },
      }),
    );

describe('catch-up', () => {
  it('maps the channel catch-up and drops empty windows', () => {
    expect(
      mapChannel({ id: 'a', url: 'u', catchup: { days: 7, mode: 'default' } })?.catchup,
    ).toEqual({
      days: 7,
      mode: 'default',
    });
    expect(mapChannel({ id: 'a', url: 'u', catchup: { days: 0 } })?.catchup).toBeUndefined();
    expect(mapChannel({ id: 'a', url: 'u' })?.catchup).toBeUndefined();
  });

  it('replays programmes that started over a minute ago inside the window', () => {
    expect(canReplay(channel, { start: now - HOUR }, now)).toBe(true);
    expect(canReplay(channel, { start: now - 30_000 }, now)).toBe(false);
    expect(canReplay(channel, { start: now - 4 * DAY }, now)).toBe(false);
    expect(canReplay({ catchup: undefined }, { start: now - HOUR }, now)).toBe(false);
  });

  it('pages the guide back as far as the longest archive, capped at a week', () => {
    expect(guideHistory([{}])).toBe(30 * 60_000);
    expect(guideHistory([{}, channel])).toBe(3 * DAY);
    expect(guideHistory([{ catchup: { days: 30 } }])).toBe(7 * DAY);
    const live = guideStart(now);
    const back = shiftGuideStart(live, -1, now, 3 * DAY);
    expect(back).toBe(live - 6 * HOUR);
    expect(shiftGuideStart(back, 1, now, 3 * DAY)).toBe(live);
    expect(shiftGuideStart(live, 1, now, 3 * DAY)).toBe(live);
    expect(canPageEarlier(live, now, 30 * 60_000)).toBe(false);
    expect(canPageEarlier(live, now, 3 * DAY)).toBe(true);
    let s = live;
    for (let i = 0; i < 20; i++) s = shiftGuideStart(s, -1, now, 3 * DAY);
    expect(s).toBeGreaterThanOrEqual(now - 3 * DAY - 30 * 60_000);
    expect(canPageEarlier(s, now, 3 * DAY)).toBe(false);
  });

  it('asks the server for the archive and tunes it like a channel', async () => {
    const spy = mockFetch({
      url: 'http://provider/timeshift/x.ts',
      offsetSeconds: 0,
      durationSeconds: 3600,
    });
    const program = { title: 'News', start: now - 2 * HOUR, stop: now - HOUR };
    const archive = await fetchCatchup('p1', channel, program);
    const url = new URL(String(spy.mock.calls[0]?.[0]), 'http://x');
    expect(url.pathname).toMatch(/\/api\/live\/catchup$/);
    expect(url.searchParams.get('channelId')).toBe('cu:42');
    expect(url.searchParams.get('sourceId')).toBe('cu');
    expect(url.searchParams.get('start')).toBe(new Date(program.start).toISOString());
    expect(url.searchParams.get('offset')).toBe('0');
    const q = catchupStartQuery(channel, archive, program, 'p1');
    expect(q.url).toBe('http://provider/timeshift/x.ts');
    expect(q.liveSourceUrl).toBe('http://provider/timeshift/x.ts');
    expect(q.title).toBe('News');
    expect(q.format).toBe('hls');
    expect(q.timeshift).toBeUndefined(); // a replay starts at the programme, not behind a window
    expect(q.videoCopy).toBeUndefined(); // a switch to the encode would restart the archive
  });
});

describe('Stremio Choose Source', () => {
  const stremio: LiveChannel = {
    id: 'st:1',
    name: 'Sports 1',
    url: 'https://addon.example/stream/tv/sports1.json',
  };

  it('detects Stremio stream resources', () => {
    expect(hasStremioSources(stremio)).toBe(true);
    expect(hasStremioSources({ url: 'https://addon.example/stream/tv/a.json?x=1' })).toBe(true);
    expect(hasStremioSources(channel)).toBe(false);
  });

  it('maps stream options, skipping ones without an index', () => {
    expect(
      mapStremioStreams([
        { index: 0, label: '1080p · Feed A', name: 'A' },
        { index: 2, title: 'Feed C' },
        { label: 'no index' },
      ]),
    ).toEqual([
      { index: 0, label: '1080p · Feed A', name: 'A', title: undefined, description: undefined },
      { index: 2, label: 'Feed C', name: undefined, title: 'Feed C', description: undefined },
    ]);
  });

  it('fetches streams and tunes the picked index', async () => {
    const spy = mockFetch({ streams: [{ index: 1, label: 'B' }] });
    expect(await fetchStremioStreams('p1', stremio)).toHaveLength(1);
    const url = new URL(String(spy.mock.calls[0]?.[0]), 'http://x');
    expect(url.pathname).toMatch(/\/api\/live\/stremio\/streams$/);
    expect(url.searchParams.get('url')).toBe(stremio.url);
    expect(url.searchParams.get('profileId')).toBe('p1');
    expect(liveStartQuery(stremio, 'p1', { stremioStreamIndex: 1 }).stremioStreamIndex).toBe('1');
    expect(liveStartQuery(stremio, 'p1').stremioStreamIndex).toBeUndefined();
  });
});

describe('live memory', () => {
  it('keeps categories and view per profile', () => {
    writeLiveMemory('p1', { categories: ['News', 'Sports', 'News'], view: 'channels' });
    writeLiveMemory('p2', { categories: [] });
    expect(readLiveMemory('p1')).toEqual({ categories: ['News', 'Sports'], view: 'channels' });
    expect(readLiveMemory('p2')).toEqual({ categories: [], view: undefined });
    expect(readLiveMemory('nobody')).toEqual({ categories: [], view: undefined });
  });
});
