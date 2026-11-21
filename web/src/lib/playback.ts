import { serverBasePath } from '@/api/basePath';
import { getClientId } from '@/api/storage';
import { sessionStore } from '@/auth/sessionStore';
import type { DetailsSearch } from './links';

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
   * Where the player's back/close returns: a same-origin path ending in `/watch/details`
   * or `/watch-next/details` (see `detailsReturnPath()`). Signed-out visits to the
   * player are also redirected there by the backend.
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

/**
 * The app's details URL path (with server base path and app base), used as the
 * player's `returnTo`. playback.html strips nothing we use and adds `_playbackReturn`.
 */
export function detailsReturnPath(
  search: DetailsSearch,
  pathname: string = window.location.pathname,
): string {
  const appBase = import.meta.env.BASE_URL.replace(/\/+$/, '');
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(search)) {
    if (value === undefined || value === null || value === '') continue;
    params.set(key, String(value));
  }
  return `${serverBasePath(pathname)}${appBase}/details?${params.toString()}`;
}

/** What is being played, for the player's metadata params. */
export interface PlaybackTitleInfo {
  mediaType: 'movie' | 'series';
  titleId: string;
  name: string;
  year?: number;
  tmdbId?: number;
  tvdbId?: number;
  imdbId?: string;
  posterUrl?: string;
  backdropUrl?: string;
  logoUrl?: string;
}

export interface PlaybackEpisodeInfo {
  seasonNumber: number;
  episodeNumber: number;
  name?: string;
  imageUrl?: string;
  airedDate?: string;
}

/** The resolved stream (prequeue status, manual resolve or library file). */
export interface ResolvedStream {
  streamPath: string;
  prequeueId?: string;
  displayName?: string;
  serviceType?: string;
  debridProvider?: string;
  passthroughName?: string;
  dv?: boolean;
  dvProfile?: string;
  hdr10?: boolean;
  forceAAC?: boolean;
  durationSeconds?: number;
  /** ffprobe stream indexes (what playback.html expects), -1/undefined = default. */
  audioTrack?: number;
  subtitleTrack?: number;
}

/** Where to start: seconds, or a percentage when only that is known. */
export type StartPoint = { seconds: number } | { percent: number } | undefined;

/**
 * PlaybackParams for a VOD hand-off, as the Expo web app builds them
 * (frontend/features/details/hooks/usePlayback.ts web branch): display metadata,
 * IDs for progress / next-episode lookup, the stream path (encoded once more, as
 * playback.html decodes route values twice), track preselection and resume point.
 */
export function vodPlaybackParams(
  title: PlaybackTitleInfo,
  stream: ResolvedStream,
  opts: { episode?: PlaybackEpisodeInfo; start?: StartPoint; returnTo?: string } = {},
): PlaybackParams {
  const { episode, start } = opts;
  const isEpisode = title.mediaType === 'series' && !!episode;
  const params: PlaybackParams = {
    title: title.name,
    mediaType: isEpisode ? 'episode' : 'movie',
    headerImage: episode?.imageUrl || title.backdropUrl || title.posterUrl,
    posterUrl: title.posterUrl,
    logoUrl: title.logoUrl,
    year: title.year,
    titleId: title.titleId,
    imdbId: title.imdbId,
    tmdbId: title.tmdbId ? String(title.tmdbId) : undefined,
    tvdbId: title.tvdbId ? String(title.tvdbId) : undefined,
    sourcePath: encodeURIComponent(stream.streamPath),
    displayName: stream.displayName,
    sourceServiceType: stream.serviceType,
    debridProvider: stream.debridProvider,
    passthroughName: stream.passthroughName,
    prequeueId: stream.prequeueId,
    dv: stream.dv ? '1' : undefined,
    dvProfile: stream.dvProfile,
    hdr10: stream.hdr10 ? '1' : undefined,
    forceAAC: stream.forceAAC ? '1' : undefined,
    durationHint: stream.durationSeconds || undefined,
    returnTo: opts.returnTo,
  };
  if (isEpisode && episode) {
    params.seriesTitle = title.name;
    params.seasonNumber = episode.seasonNumber;
    params.episodeNumber = episode.episodeNumber;
    params.episodeName = episode.name;
    params.seriesIdentifier = title.titleId;
    if (episode.airedDate) params.releaseDate = episode.airedDate;
  }
  if (typeof stream.audioTrack === 'number' && stream.audioTrack >= 0)
    params.preselectedAudioTrack = String(stream.audioTrack);
  if (typeof stream.subtitleTrack === 'number' && stream.subtitleTrack >= 0)
    params.preselectedSubtitleTrack = String(stream.subtitleTrack);
  if (start && 'seconds' in start && start.seconds > 0) params.startOffset = start.seconds;
  else if (start && 'percent' in start && start.percent > 0) params.startPercent = start.percent;
  return params;
}
