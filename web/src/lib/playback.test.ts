import { describe, expect, it } from 'vitest';
import { sessionStore } from '@/auth/sessionStore';
import { buildPlaybackUrl } from './playback';

describe('buildPlaybackUrl', () => {
  it('adds token, clientId, active profile and its unlock', () => {
    sessionStore.login({
      token: 'tok',
      expiresAt: '',
      accountId: 'a',
      username: 'u',
      isMaster: false,
    });
    sessionStore.selectProfile('p1');
    sessionStore.setUnlock('p1', 'pu_1');
    const url = new URL(
      buildPlaybackUrl(
        { title: 'Heat', mediaType: 'movie', startOffset: 12.5, sourcePath: '' },
        'http://x',
      ),
    );
    expect(url.pathname).toBe('/watch/playback.html');
    expect(url.searchParams.get('token')).toBe('tok');
    expect(url.searchParams.get('profileId')).toBe('p1');
    expect(url.searchParams.get('profileUnlock')).toBe('pu_1');
    expect(url.searchParams.get('title')).toBe('Heat');
    expect(url.searchParams.get('startOffset')).toBe('12.5');
    expect(url.searchParams.get('clientId')).toMatch(/^web-/);
    expect(url.searchParams.has('sourcePath')).toBe(false);
  });
});
