import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  fetchSchedule,
  guideKey,
  livePlaybackParams,
  liveStartErrorMessage,
  liveStartQuery,
  mapChannel,
  mapChannelsPage,
  mapProgram,
  normalizeLogoUrl,
  normalizeSchedule,
  saveChannelLists,
  type LiveChannel,
} from './live';

const channel: LiveChannel = {
  id: 'src1:bbc1',
  name: 'BBC One',
  url: 'http://provider/live/1.ts',
  logo: 'https://logo/bbc1.png',
  group: 'UK',
  tvgId: 'bbc1.uk',
  sourceId: 'src1',
  playbackId: 'bbc1',
};

afterEach(() => vi.unstubAllGlobals());

describe('live mappers', () => {
  it('rewrites GitHub blob logos to raw URLs', () => {
    expect(normalizeLogoUrl('https://github.com/a/b/blob/main/x.png')).toBe(
      'https://raw.githubusercontent.com/a/b/main/x.png',
    );
    expect(normalizeLogoUrl('https://x/y.png')).toBe('https://x/y.png');
  });

  it('maps channels and drops ones without id or url', () => {
    expect(mapChannel({ id: 'a', url: 'u', name: 'A', tvgId: '' })).toEqual(
      expect.objectContaining({ id: 'a', name: 'A', tvgId: undefined }),
    );
    expect(mapChannel({ id: 'a' })).toBeNull();
    const page = mapChannelsPage({
      channels: [{ id: 'a', url: 'u' }, { name: 'bad' }],
      total: 10,
      hasMore: true,
      availableCategories: ['News', 3],
      sources: [{ id: 's', name: 'Src' }],
    });
    expect(page.channels).toHaveLength(1);
    expect(page.total).toBe(10);
    expect(page.hasMore).toBe(true);
    expect(page.availableCategories).toEqual(['News']);
    expect(page.sources).toEqual([{ id: 's', name: 'Src' }]);
  });

  it('uses tvgId, else id, as the guide key', () => {
    expect(guideKey(channel)).toBe('bbc1.uk');
    expect(guideKey({ id: 'x' })).toBe('x');
  });

  it('parses programmes and airing flags', () => {
    const p = mapProgram({
      title: 'News',
      start: '2026-11-21T18:00:00Z',
      stop: '2026-11-21T18:30:00.000Z',
      new: true,
    });
    expect(p?.start).toBe(Date.parse('2026-11-21T18:00:00Z'));
    expect(p?.isNew).toBe(true);
    expect(p?.isRerun).toBe(false);
    expect(mapProgram({ title: 'x', start: 'nope', stop: 'nope' })).toBeNull();
  });

  it('dedupes by start and trims overlaps', () => {
    const mk = (title: string, s: number, e: number) => ({
      title,
      start: s,
      stop: e,
      isNew: false,
      isRerun: false,
    });
    const out = normalizeSchedule([mk('b', 30, 90), mk('a', 0, 45), mk('a2', 0, 40)]);
    expect(out.map((p) => [p.title, p.start, p.stop])).toEqual([
      ['a2', 0, 30],
      ['b', 30, 90],
    ]);
  });
});

describe('live tuning', () => {
  it('builds the web hls/start query', () => {
    vi.stubGlobal('MediaSource', class {});
    const q = liveStartQuery(channel, 'p1', { programTitle: 'News', profileName: 'Liam' });
    expect(q).toMatchObject({
      url: channel.url,
      target: 'web',
      format: 'hls',
      profileId: 'p1',
      title: 'News',
      channelId: 'bbc1',
      sourceId: 'src1',
      itemId: 'bbc1.uk',
      profileName: 'Liam',
      liveChannelLogo: channel.logo,
      timeshift: '1',
      videoCopy: '1',
    });
    vi.unstubAllGlobals();
  });

  it('asks for the video copy only where hls.js has Media Source Extensions', () => {
    vi.stubGlobal('MediaSource', undefined);
    vi.stubGlobal('ManagedMediaSource', undefined);
    expect(liveStartQuery(channel, 'p1').videoCopy).toBeUndefined();
    vi.stubGlobal('ManagedMediaSource', class {});
    expect(liveStartQuery(channel, 'p1').videoCopy).toBe('1');
    vi.unstubAllGlobals();
  });

  it('hands an HLS session to playback.html', () => {
    const p = livePlaybackParams(channel, {
      sessionId: 'abc',
      playlistUrl: '/video/hls/abc/stream.m3u8',
    });
    expect(p).toMatchObject({
      movie: '/video/hls/abc/stream.m3u8',
      hlsSessionId: 'abc',
      mediaType: 'channel',
      tvgId: 'bbc1.uk',
      liveSourceUrl: channel.url,
      livePlaybackId: 'bbc1',
    });
    const direct = livePlaybackParams(channel, { isDirect: true, streamUrl: '/live/stream?x=1' });
    expect(direct.movie).toBe('/live/stream?x=1');
    expect(direct.hlsSessionId).toBeUndefined();
    expect(() => livePlaybackParams(channel, {})).toThrow();
  });

  it('tells the player whether the session keeps a timeshift window', () => {
    const playlist = { sessionId: 'abc', playlistUrl: '/video/hls/abc/stream.m3u8' };
    expect(livePlaybackParams(channel, { ...playlist, timeshiftWindowSeconds: 3600 }).liveTimeshift).toBe('1');
    expect(livePlaybackParams(channel, { ...playlist, timeshiftWindowSeconds: 0 }).liveTimeshift).toBeUndefined();
    expect(livePlaybackParams(channel, playlist).liveTimeshift).toBeUndefined();
  });

  it('explains provider stream limits', () => {
    expect(
      liveStartErrorMessage({
        status: 429,
        body: { provider: 'xtream', currentStreams: 2, maxStreams: 2 },
      }),
    ).toBe('XTREAM limit reached (2/2)');
  });
});

describe('live requests', () => {
  it('fills missing schedule keys with empty lists', async () => {
    const fetchMock = vi.fn(async (url: string) => {
      expect(url).toContain('/api/live/epg/schedule/batch?');
      expect(url).toContain('channels=a%2Cb');
      expect(url).toContain('profileId=p1');
      return new Response(
        JSON.stringify({
          a: [{ title: 'X', start: '2026-11-21T18:00:00Z', stop: '2026-11-21T19:00:00Z' }],
          b: null,
        }),
      );
    });
    vi.stubGlobal('fetch', fetchMock);
    const out = await fetchSchedule('p1', ['a', 'b'], -30, 13);
    expect(out.a).toHaveLength(1);
    expect(out.b).toEqual([]);
  });

  it('saves favorites without clobbering other settings', async () => {
    const calls: { method: string; body?: unknown }[] = [];
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init: RequestInit) => {
        calls.push({
          method: init.method ?? 'GET',
          body: init.body && JSON.parse(String(init.body)),
        });
        return new Response(
          init.method === 'PUT'
            ? ''
            : JSON.stringify({ display: { x: 1 }, liveTV: { other: true, favoriteChannels: [] } }),
        );
      }),
    );
    await saveChannelLists('p1', { favorites: ['a'], hidden: ['b'] });
    expect(calls[1]).toEqual({
      method: 'PUT',
      body: {
        display: { x: 1 },
        liveTV: { other: true, favoriteChannels: ['a'], hiddenChannels: ['b'] },
      },
    });
  });
});
