import { afterEach, describe, expect, it } from 'vitest';
import { sessionStore } from '@/auth/sessionStore';
import { artworkUrl } from '@/lib/artwork';
import { catalogTitleId, groupsQuery, groupToTitle, versionQuality, versionSize } from './library';

describe('library mappers', () => {
  it('names the matched catalog title', () => {
    expect(catalogTitleId({ id: 'g', title: 'Heat', tmdbId: 949 })).toBe('tmdb:movie:949');
    expect(catalogTitleId({ id: 'g', title: 'Lost', groupType: 'show', tvdbId: 73739 })).toBe(
      'tvdb:series:73739',
    );
    expect(catalogTitleId({ id: 'g', title: 'Home video' })).toBeNull();
    expect(groupToTitle({ id: 'g', title: 'Home video' })).toBeNull();
    expect(
      groupToTitle({ id: 'g', title: 'Lost', libraryType: 'show', tmdbId: 4607 }),
    ).toMatchObject({ id: 'tmdb:tv:4607', mediaType: 'series', tmdbId: 4607 });
  });

  it('describes versions', () => {
    expect(
      versionQuality({
        id: 'i',
        probe: { width: 3840, height: 1608, videoCodec: 'hevc', hdrFormat: 'Dolby Vision' },
      }),
    ).toBe('4K · HEVC · Dolby Vision');
    expect(versionQuality({ id: 'i', versionLabel: '1080p · H264' })).toBe('1080p · H264');
    expect(versionQuality({ id: 'i', fileName: 'a.mkv' })).toBe('a.mkv');
    expect(
      versionSize({ id: 'i', sizeBytes: 21_100_000_000, probe: { durationSeconds: 6500 } }),
    ).toBe('21.1 GB · 26 Mbps');
  });

  it('builds the groups query like native libraryPage', () => {
    expect(groupsQuery({})).toEqual({ sort: 'created', dir: 'desc' });
    expect(groupsQuery({ sort: 'name', letter: 'C', q: ' heat ', type: 'movie' })).toEqual({
      sort: 'name',
      dir: 'asc',
      alphabet: 'C',
      query: 'heat',
      mediaType: 'movie',
    });
    expect(groupsQuery({ sort: 'year', dir: 'asc', letter: 'C' })).toEqual({
      sort: 'year',
      dir: 'asc',
    });
  });
});

describe('artworkUrl', () => {
  afterEach(() => sessionStore.logout());
  it('passes absolute URLs through', () => {
    expect(artworkUrl('https://image.tmdb.org/t/p/w342/a.jpg')).toBe(
      'https://image.tmdb.org/t/p/w342/a.jpg',
    );
    expect(artworkUrl(undefined)).toBeUndefined();
  });
  it('adds the token and profile to server artwork', () => {
    sessionStore.login({
      token: 'tok',
      expiresAt: '2099-01-01T00:00:00Z',
      accountId: 'a',
      username: 'u',
      isMaster: false,
    });
    sessionStore.selectProfile('p1');
    const url = new URL(artworkUrl('/api/library/items/i1/artwork/poster?w=300')!, 'http://x');
    expect(url.pathname).toBe('/api/library/items/i1/artwork/poster');
    expect(url.searchParams.get('w')).toBe('300');
    expect(url.searchParams.get('token')).toBe('tok');
    expect(url.searchParams.get('profileId')).toBe('p1');
  });
});
