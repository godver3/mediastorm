import { describe, expect, it } from 'vitest';
import { toLibraryVersions, versionQuality, youTubeEmbedUrl, sortCollection } from '@/api/details';
import { sourceQuery, toSourceResult } from '@/api/playback';
import { detailsReturnPath, vodPlaybackParams } from '@/lib/playback';
import { prequeueBody, streamFromPrequeue, webTracks } from './resolve';
import { requestFromBundle, resumeStart } from './usePlayTitle';
import { infoLine, ratingChips, formatRuntime } from '@/features/details/format';

const series = {
  mediaType: 'series' as const,
  titleId: 'tmdb:tv:1396',
  name: 'Breaking Bad',
  year: 2008,
  tmdbId: 1396,
};

describe('prequeue body', () => {
  it('mirrors the native body with string ids and episode', () => {
    const body = prequeueBody({
      title: series,
      episode: { seasonNumber: 2, episodeNumber: 3, numbering: { seriesId: 's', ordering: 'dvd' } },
      start: { seconds: 61 },
    });
    expect(body).toEqual({
      titleId: 'tmdb:tv:1396',
      titleName: 'Breaking Bad',
      mediaType: 'series',
      tmdbId: '1396',
      year: 2008,
      seasonNumber: 2,
      episodeNumber: 3,
      numbering: { seriesId: 's', ordering: 'dvd' },
      startOffset: 61,
    });
  });
});

describe('tracks', () => {
  it('prefers the text subtitle of the track selection', () => {
    expect(
      webTracks({
        audio: { streamIndex: 1, typeIndex: 0 },
        subtitle: { streamIndex: 5, typeIndex: 0, bitmap: true },
        textSubtitle: { streamIndex: 6, typeIndex: 1 },
      }),
    ).toEqual({ audioTrack: 1, subtitleTrack: 6 });
  });
  it('drops bitmap legacy subtitles', () => {
    expect(
      webTracks(undefined, {
        selectedAudioTrack: 2,
        selectedSubtitleTrack: 4,
        subtitleTracks: [{ index: 4, isBitmap: true }],
      }),
    ).toEqual({ audioTrack: 2, subtitleTrack: undefined });
  });
});

describe('vod playback params', () => {
  it('builds episode params from a ready prequeue', () => {
    const stream = streamFromPrequeue({
      prequeueId: 'pq1',
      status: 'ready',
      streamPath: '/debrid/a b.mkv',
      hasDolbyVision: true,
      dolbyVisionProfile: 'dvhe.05',
      selectedAudioTrack: 1,
      selectedSubtitleTrack: -1,
    });
    const p = vodPlaybackParams(series, stream, {
      episode: { seasonNumber: 1, episodeNumber: 2, name: 'Cat’s in the Bag' },
      start: { seconds: 90 },
      returnTo: '/watch/details?x=1',
    });
    expect(p).toMatchObject({
      mediaType: 'episode',
      seriesTitle: 'Breaking Bad',
      seasonNumber: 1,
      episodeNumber: 2,
      sourcePath: encodeURIComponent('/debrid/a b.mkv'),
      prequeueId: 'pq1',
      dv: '1',
      dvProfile: 'dvhe.05',
      preselectedAudioTrack: '1',
      startOffset: 90,
      tmdbId: '1396',
      returnTo: '/watch/details?x=1',
    });
    expect(p.preselectedSubtitleTrack).toBeUndefined();
  });

  it('uses startPercent when only a percentage is known', () => {
    const p = vodPlaybackParams(
      { ...series, mediaType: 'movie' },
      { streamPath: 'x' },
      { start: { percent: 40 } },
    );
    expect(p.startPercent).toBe(40);
    expect(p.startOffset).toBeUndefined();
    expect(p.mediaType).toBe('movie');
  });

  it('returns to the app details route under the server base path', () => {
    expect(
      detailsReturnPath(
        { mediaType: 'movie', titleId: 'tmdb:movie:949', name: 'Heat', year: 1995 },
        '/media/watch/details',
      ),
    ).toBe('/media/watch/details?mediaType=movie&titleId=tmdb%3Amovie%3A949&name=Heat&year=1995');
  });
});

describe('bundle requests', () => {
  it('resumes the up-next episode', () => {
    const req = requestFromBundle(series, {
      seriesDetails: {
        title: { id: 't', name: 'BB', mediaType: 'series' },
        seasons: [
          {
            id: 's1',
            name: 'Season 1',
            number: 1,
            episodes: [
              {
                id: 'e',
                name: 'Pilot',
                seasonNumber: 1,
                episodeNumber: 1,
                resume: { position: 300, duration: 3000, percent: 10, eligible: true },
              },
            ],
          },
        ],
      },
      upNextEpisode: { seasonNumber: 1, episodeNumber: 1 },
    });
    expect(req.episode?.name).toBe('Pilot');
    expect(req.start).toEqual({ seconds: 300 });
  });
  it('start null plays from the beginning', () => {
    const req = requestFromBundle(
      { ...series, mediaType: 'movie' },
      { resume: { position: 5, duration: 9, percent: 50, eligible: true } },
      undefined,
      null,
    );
    expect(req.start).toBeUndefined();
  });
  it('ignores ineligible resume', () => {
    expect(resumeStart({ position: 5, duration: 9, percent: 1, eligible: false })).toBeUndefined();
  });
});

describe('mappers', () => {
  it('maps source results and queries', () => {
    const r = toSourceResult(
      {
        title: 'X.2160p',
        guid: 'g',
        sizeBytes: 5,
        attributes: { resolution: '2160p', n: 3 },
        sourceHealth: { state: 'pending', token: 't' },
      },
      4,
    );
    expect(r).toMatchObject({
      id: 'g',
      index: 4,
      attributes: { resolution: '2160p', n: '3' },
      health: { state: 'pending', token: 't' },
    });
    expect(toSourceResult({}, 0)).toBeNull();
    expect(sourceQuery('Show', 2020, { season: 1, episode: 9 })).toBe('Show S01E09');
    expect(sourceQuery('Heat', 1995)).toBe('Heat 1995');
  });
  it('groups library copies and labels them', () => {
    const v = toLibraryVersions([
      {
        libraryId: 'l',
        libraryName: 'Films',
        group: {
          id: 'g',
          title: 'Heat',
          items: [
            {
              id: 'i1',
              sourceType: 'plex',
              probe: { width: 3840, height: 1600, videoCodec: 'hevc', hdrFormat: 'dolby_vision' },
            },
          ],
        },
      },
      {
        libraryId: 'l2',
        libraryName: 'TV',
        group: {
          id: 'g2',
          title: 'S',
          seasons: [{ seasonNumber: 1, episodes: [{ episodeNumber: 2, items: [{ id: 'i2' }] }] }],
        },
      },
    ]);
    expect(v.movie).toHaveLength(1);
    expect(versionQuality(v.movie[0]!)).toBe('4K · HEVC · Dolby Vision');
    expect(
      versionQuality({
        item: { id: 'i', probe: { width: 720, height: 576, videoCodec: 'h264', interlaced: true } },
        libraryName: 'Aufnahmen',
      }),
    ).toMatch(/^576i · /);
    expect(v.episodes['1:2']).toHaveLength(1);
  });
  it('embeds YouTube trailers only', () => {
    expect(youTubeEmbedUrl({ name: 't', url: 'https://www.youtube.com/watch?v=abc' })).toContain(
      '/embed/abc',
    );
    expect(youTubeEmbedUrl({ name: 't', url: 'https://cdn.example/t.mp4' })).toBeUndefined();
  });
  it('sorts collections by year, undated last', () => {
    const out = sortCollection([
      { id: 'a', name: 'B', mediaType: 'movie' },
      { id: 'b', name: 'A', mediaType: 'movie', year: 2003 },
      { id: 'c', name: 'C', mediaType: 'movie', year: 1999 },
    ]);
    expect(out.map((t) => t.id)).toEqual(['c', 'b', 'a']);
  });
  it('formats info line and ratings', () => {
    expect(formatRuntime(170)).toBe('2h 50m');
    expect(
      infoLine({
        id: 'x',
        name: 'Heat',
        mediaType: 'movie',
        year: 1995,
        runtimeMinutes: 170,
        certification: 'R',
      }),
    ).toEqual(['1995', '2h 50m', 'R']);
    expect(
      ratingChips([
        { source: 'tomatoes', value: 88, max: 100 },
        { source: 'imdb', value: 8.3, max: 10 },
      ]).map((r) => r.text),
    ).toEqual(['8.3', '88%']);
  });
});
