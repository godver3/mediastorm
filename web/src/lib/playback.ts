import { serverBasePath } from '@/api/basePath';
import { getClientId } from '@/api/storage';
import { sessionStore } from '@/auth/sessionStore';

/**
 * Query parameters read by the server-rendered player at /watch/playback.html
 * (backend/handlers/web_templates/playback.html, `routeParam(...)`). All values
 * are strings in the URL; numbers/booleans are stringified here. `token`,
 * `clientId` and `profileUnlock` are filled in by openPlayback().
 */
export interface PlaybackParams {
  /** Profile to play as (also accepted as `userId`). Defaults to the selected profile. */
  profileId?: string;
  profileName?: string;
  /** Display metadata. */
  title?: string;
  seriesTitle?: string;
  episodeName?: string;
  year?: string | number;
  displayName?: string;
  headerImage?: string;
  posterUrl?: string;
  logoUrl?: string;
  logoIsDark?: '1' | '';
  /** 'movie' | 'episode' | 'series' | 'live' | ... */
  mediaType?: string;
  seasonNumber?: string | number;
  episodeNumber?: string | number;
  /** IDs used for progress, subtitles and next-episode lookup. */
  titleId?: string;
  imdbId?: string;
  tmdbId?: string;
  tvdbId?: string;
  seriesIdentifier?: string;
  /** Source: a resolved stream path (playback.html decodes it), or a direct `movie` stream URL. */
  sourcePath?: string;
  movie?: string;
  sourceServiceType?: string;
  prequeueId?: string;
  hlsSessionId?: string;
  /** Seconds to start from, or a percentage when no offset is known. */
  startOffset?: number;
  actualStartOffset?: number;
  startPercent?: number;
  durationHint?: number;
  /** Track preselection (stream indexes as strings). */
  preselectedAudioTrack?: string;
  preselectedSubtitleTrack?: string;
  /** HDR / codec hints. */
  dv?: string;
  dvProfile?: string;
  hdr10?: string;
  forceAAC?: string;
  /** Live TV / recordings. */
  liveSourceUrl?: string;
  tvgId?: string;
  growingRecording?: string;
  recordingEndsAt?: string;
  /** Watch party / share modes. */
  watchPartyRoomId?: string;
  shareMode?: string;
  sharedSource?: string;
  /**
   * Where the player's back/close returns. NOTE: playback.html currently only
   * honours same-origin paths ending in `/watch/details`; anything else falls back to
   * the referrer / history. Phase 3 needs to widen that check for /watch-next.
   */
  returnTo?: string;
  /** Any other param the Expo app passes through (e.g. stremioStreamIndex). */
  [extra: string]: string | number | boolean | undefined;
}

/** Build the absolute /watch/playback.html URL with auth + the given params. */
export function buildPlaybackUrl(params: PlaybackParams, origin = window.location.origin): string {
  const url = new URL(`${serverBasePath()}/watch/playback.html`, origin);
  const state = sessionStore.getState();
  const merged: PlaybackParams = { profileId: state.profileId ?? undefined, ...params };
  for (const [key, value] of Object.entries(merged)) {
    if (value === undefined || value === null || value === '' || value === false) continue;
    url.searchParams.set(key, String(value));
  }
  const token = sessionStore.getToken();
  if (token) url.searchParams.set('token', token);
  url.searchParams.set('clientId', getClientId());
  const pid = merged.profileId ?? merged.userId;
  const unlock = sessionStore.getUnlockHeader(typeof pid === 'string' ? [pid] : undefined);
  if (unlock && !url.searchParams.has('profileUnlock'))
    url.searchParams.set('profileUnlock', unlock);
  return url.toString();
}

/** Hand off to the server-rendered player (full page navigation). */
export function openPlayback(params: PlaybackParams): void {
  window.location.assign(buildPlaybackUrl(params));
}
