import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { findHidden, identityTokens, listItemMatches, orderingLabel } from '@/api/details';
import {
  audioLabel,
  layerDetailsSettings,
  releaseTitle,
  trackDefaults,
  type PrequeueStatus,
} from '@/api/playback';
import { sessionStore } from '@/auth/sessionStore';
import { applyTrackOverrides, resolvePlayback } from '@/features/playback/resolve';
import type { PlaybackParams } from '@/lib/playback';
import { listNameProblem } from './ListsDialog';
import { tracksSummary } from './TracksDialog';
import { resolveTrailer } from './TrailerDialog';

const heat = {
  mediaType: 'movie' as const,
  titleId: 'tmdb:movie:949',
  name: 'Heat',
  tmdbId: 949,
  imdbId: 'tt0113277',
};

describe('hidden titles', () => {
  it('matches stored items by any shared identity, as native tokens do', () => {
    expect(identityTokens('tv', 'tmdb:tv:1396')).toEqual(
      new Set(['series:tmdb:tv:1396', 'series:tmdb:1396', 'series:1396']),
    );
    expect(findHidden([{ id: 'tt0113277', mediaType: 'movie' }], heat)?.id).toBe('tt0113277');
    expect(
      findHidden([{ id: 'x', mediaType: 'movie', externalIds: { tmdb: '949' } }], heat),
    ).toBeDefined();
    expect(findHidden([{ id: 'tt0113277', mediaType: 'series' }], heat)).toBeUndefined();
  });
});

describe('custom list membership', () => {
  it('matches by id or a shared external id, never across media types', () => {
    expect(listItemMatches({ id: 'tmdb:movie:949', mediaType: 'movie' }, heat)).toBe(true);
    expect(
      listItemMatches(
        { id: 'other', mediaType: 'movie', externalIds: { imdb: 'tt0113277' } },
        heat,
      ),
    ).toBe(true);
    expect(listItemMatches({ id: 'tmdb:movie:949', mediaType: 'series' }, heat)).toBe(false);
  });
  it('validates new list names like native', () => {
    expect(listNameProblem('  ', [])).toMatch(/Enter a name/);
    expect(listNameProblem('set aside', [])).toMatch(/reserved/);
    expect(listNameProblem('Faves', [{ id: '1', name: 'faves' }])).toMatch(/already/);
    expect(listNameProblem('New', [{ id: '1', name: 'faves' }])).toBeUndefined();
  });
});

describe('orderings', () => {
  it('labels the official ordering as the default', () => {
    expect(orderingLabel({ type: 'official', name: 'Aired Order', isOfficial: true })).toBe(
      'Aired Order (Default)',
    );
    expect(orderingLabel({ type: 'dvd' })).toBe('dvd');
  });
});

describe('details playback settings', () => {
  it('keeps warm-up off unless enabled; the device wins over the profile', () => {
    expect(layerDetailsSettings(null, null).prequeueOnOpen).toBe(false);
    expect(
      layerDetailsSettings({ playback: { disablePrequeue: false } }, null).prequeueOnOpen,
    ).toBe(true);
    expect(
      layerDetailsSettings({ playback: { disablePrequeue: false } }, { disablePrequeue: true })
        .prequeueOnOpen,
    ).toBe(false);
    expect(
      layerDetailsSettings(
        { display: { showDetailsSelectedTracks: true } },
        { showDetailsPrequeueFilename: true },
      ),
    ).toEqual({ prequeueOnOpen: false, showFilename: true, showSelectedTracks: true });
  });
});

const ready: PrequeueStatus = {
  prequeueId: 'pq',
  status: 'ready',
  streamPath: '/debrid/rd/Heat.1995.2160p.REMUX.mkv',
  audioTracks: [
    { index: 1, language: 'eng', codec: 'truehd', profile: 'TrueHD + Atmos' },
    { index: 2, language: 'fre', codec: 'ac3' },
  ],
  subtitleTracks: [
    { index: 0, absoluteIndex: 3, language: 'eng', codec: 'subrip' },
    { index: 1, absoluteIndex: 4, language: 'eng', codec: 'hdmv_pgs_subtitle', isBitmap: true },
  ],
  trackSelection: {
    audio: { streamIndex: 1, typeIndex: 0 },
    subtitle: { streamIndex: 3, typeIndex: 0 },
    subtitleMode: 'on',
  },
};

describe('prequeue tracks', () => {
  it('names the release and the tracks Play will use', () => {
    expect(releaseTitle(ready)).toBe('Heat.1995.2160p.REMUX.mkv');
    expect(releaseTitle({ ...ready, passthroughName: 'AIO name\nline 2' })).toBe('AIO name');
    expect(trackDefaults(ready)).toEqual({ audio: 1, subtitle: 3 });
    expect(
      trackDefaults({ ...ready, trackSelection: { ...ready.trackSelection, subtitleMode: 'off' } })
        .subtitle,
    ).toBeUndefined();
    expect(audioLabel(ready.audioTracks![1]!)).toBe('French · AC-3');
    expect(tracksSummary(ready, {})).toEqual({
      audio: 'English · TrueHD + Atmos',
      subtitle: 'English',
    });
    expect(tracksSummary(ready, { audio: 2, subtitle: 'off' })).toEqual({
      audio: 'French · AC-3',
      subtitle: undefined,
    });
  });
  it('puts picks over the params; off is -1', () => {
    const p = { preselectedAudioTrack: '1', preselectedSubtitleTrack: '3' } as PlaybackParams;
    applyTrackOverrides(p, { audio: 2, subtitle: 'off' });
    expect(p).toMatchObject({ preselectedAudioTrack: '2', preselectedSubtitleTrack: '-1' });
  });
});

const fetchMock = vi.fn<typeof fetch>();
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('warm prequeue reuse and trailers', () => {
  const calls: string[] = [];
  beforeEach(() => {
    calls.length = 0;
    vi.stubGlobal('fetch', fetchMock);
    sessionStore.login({
      token: 'tok',
      expiresAt: '2099-01-01T00:00:00Z',
      accountId: 'a',
      username: 'u',
      isMaster: false,
    });
  });
  afterEach(() => {
    fetchMock.mockReset();
    vi.unstubAllGlobals();
    sessionStore.logout();
  });

  it('plays the warm stream with the picks when it is for the same title', async () => {
    fetchMock.mockImplementation(async (input, init) => {
      const path = String(input).split('?')[0]!;
      calls.push(`${init?.method ?? 'GET'} ${path}`);
      if (path === '/api/playback/prequeue/warm') return json({ ...ready, prequeueId: 'warm' });
      return json({}, 404);
    });
    const params = await resolvePlayback(
      { title: heat, warmPrequeueId: 'warm', tracks: { audio: 2, subtitle: 'off' } },
      { profileId: 'p1' },
    );
    expect(calls).toEqual(['GET /api/playback/prequeue/warm']);
    expect(params).toMatchObject({
      prequeueId: 'warm',
      preselectedAudioTrack: '2',
      preselectedSubtitleTrack: '-1',
    });
  });

  it('starts fresh, without the picks, when the warm-up resolved another episode', async () => {
    fetchMock.mockImplementation(async (input, init) => {
      const path = String(input).split('?')[0]!;
      calls.push(`${init?.method ?? 'GET'} ${path}`);
      if (path === '/api/playback/prequeue/warm')
        return json({ ...ready, targetEpisode: { seasonNumber: 1, episodeNumber: 1 } });
      if (path === '/api/playback/prequeue') return json({ prequeueId: 'fresh', status: 'queued' });
      if (path === '/api/playback/prequeue/fresh')
        return json({ ...ready, prequeueId: 'fresh', trackSelection: undefined });
      return json({}, 404);
    });
    const params = await resolvePlayback(
      {
        title: { ...heat, mediaType: 'series', titleId: 's' },
        episode: { seasonNumber: 1, episodeNumber: 2 },
        warmPrequeueId: 'warm',
        tracks: { audio: 2 },
      },
      { profileId: 'p1' },
    );
    expect(calls).toContain('POST /api/playback/prequeue');
    expect(params.prequeueId).toBe('fresh');
    expect(params.preselectedAudioTrack).toBeUndefined();
  });

  it('plays a YouTube trailer from the server download, else the embed', async () => {
    fetchMock.mockImplementation(async (input) => {
      const path = String(input).split('?')[0]!;
      if (path === '/api/metadata/trailers/prequeue') return json({ id: 't1', status: 'pending' });
      if (path === '/api/metadata/trailers/prequeue/status')
        return json({ id: 't1', status: 'ready' });
      return json({}, 404);
    });
    const trailer = { name: 'T', url: 'https://www.youtube.com/watch?v=abc', key: 'abc' };
    const src = await resolveTrailer(trailer);
    expect(src).toEqual({
      kind: 'video',
      url: '/api/metadata/trailers/prequeue/serve?id=t1&token=tok',
    });

    fetchMock.mockImplementation(async () => json({ id: 't1', status: 'failed' }));
    expect((await resolveTrailer(trailer)).kind).toBe('embed');
    expect(await resolveTrailer({ name: 'V', url: 'https://cdn.example/t.mp4' })).toEqual({
      kind: 'video',
      url: 'https://cdn.example/t.mp4',
    });
  });
});
