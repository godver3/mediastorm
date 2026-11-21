/**
 * Details page API: the per-profile details bundle, watched / watchlist toggles,
 * collections, trailers and library copies. Mirrors native-tvos
 * StrmrTV/API/APIClient+Playback.swift (detailsBundle), +Watched, +Lists (watchlist),
 * +Related (collection), +Trailers, +Library (matches) and TitleModels.swift.
 * Shapes match backend handlers/details_bundle.go and models/metadata.go.
 */
import { ApiError, apiRequest } from './client';
import type { ImageRef, Title } from './titles';

// ---------------------------------------------------------------- models

/** `models.ResumeState`: the server's resume decision (no client thresholds needed). */
export interface ResumeState {
  position: number;
  duration: number;
  percent: number;
  eligible: boolean;
}

/** `models.EpisodeNumbering`: the ordering an episode's numbers are in. */
export interface EpisodeNumbering {
  seriesId: string;
  ordering?: string;
}

/** `models.SeriesEpisode` (+ per-user fields on /details-bundle). */
export interface Episode {
  id: string;
  name: string;
  overview?: string;
  seasonNumber: number;
  episodeNumber: number;
  absoluteEpisodeNumber?: number;
  runtimeMinutes?: number;
  image?: ImageRef;
  airedDate?: string;
  itemId?: string;
  watched?: boolean;
  resume?: ResumeState;
  numbering?: EpisodeNumbering;
}

export interface Season {
  id: string;
  name: string;
  number: number;
  overview?: string;
  image?: ImageRef;
  episodeCount?: number;
  episodes: Episode[];
}

export interface SeriesDetails {
  title: Title;
  seasons: Season[];
  availableOrderings?: { type: string; name?: string }[];
  activeOrdering?: string;
}

/** `models.EpisodeReference` (the episode details offers to play). */
export interface EpisodeRef {
  seasonNumber: number;
  episodeNumber: number;
  absoluteEpisodeNumber?: number;
  title?: string;
  overview?: string;
  runtimeMinutes?: number;
  image?: ImageRef;
  airDate?: string;
  itemId?: string;
  numbering?: EpisodeNumbering;
}

export interface Trailer {
  name: string;
  url: string;
  site?: string;
  type?: string;
  key?: string;
  embedUrl?: string;
  thumbnailUrl?: string;
  official?: boolean;
}

export interface TrailerList {
  primaryTrailer?: Trailer;
  trailers?: Trailer[];
}

export interface ContentPreference {
  audioLanguage?: string;
  subtitleLanguage?: string;
  subtitleMode?: string;
}

/** `GET /api/users/{id}/details-bundle`. */
export interface DetailsBundle {
  seriesDetails?: SeriesDetails | null;
  movieDetails?: Title | null;
  upNextEpisode?: EpisodeRef;
  resume?: ResumeState;
  inWatchlist?: boolean;
  watched?: boolean;
  similar?: Title[] | null;
  trailers?: TrailerList | null;
  contentPreference?: ContentPreference | null;
}

/** What the details page needs to identify a title (DetailsSearch / native DetailsTarget). */
export interface DetailsTarget {
  mediaType: 'movie' | 'series';
  titleId: string;
  name: string;
  year?: number;
  tmdbId?: number;
  tvdbId?: number;
  imdbId?: string;
  posterUrl?: string;
  backdropUrl?: string;
  overview?: string;
}

// ---------------------------------------------------------------- keys

export const detailsKeys = {
  all: ['details'] as const,
  bundle: (profileId: string, mediaType: string, titleId: string) =>
    [...detailsKeys.all, profileId, 'bundle', mediaType, titleId] as const,
  library: (profileId: string, mediaType: string, titleId: string) =>
    [...detailsKeys.all, profileId, 'library', mediaType, titleId] as const,
  collection: (id: number) => [...detailsKeys.all, 'collection', id] as const,
};

const seg = encodeURIComponent;

// ---------------------------------------------------------------- requests

export function getDetailsBundle(profileId: string, t: DetailsTarget, signal?: AbortSignal) {
  return apiRequest<DetailsBundle>(`/users/${seg(profileId)}/details-bundle`, {
    signal,
    query: {
      type: t.mediaType,
      titleId: t.titleId,
      name: t.name,
      tmdbId: t.tmdbId,
      imdbId: t.imdbId,
      tvdbId: t.tvdbId,
      year: t.year,
    },
  });
}

/** Release order, undated last (native collectionMovies). */
export function sortCollection(movies: Title[]): Title[] {
  const key = (t: Title) => (t.year && t.year > 0 ? t.year : Number.MAX_SAFE_INTEGER);
  return [...movies].sort((a, b) => key(a) - key(b) || a.name.localeCompare(b.name));
}

export async function getCollection(id: number, signal?: AbortSignal) {
  const details = await apiRequest<{ id: number; name: string; movies?: Title[] }>(
    '/metadata/collection',
    { signal, query: { id } },
  );
  return { ...details, movies: sortCollection(details.movies ?? []) };
}

function externalIds(t: DetailsTarget): Record<string, string> | undefined {
  const ids: Record<string, string> = {};
  if (t.tmdbId) ids.tmdb = String(t.tmdbId);
  if (t.imdbId) ids.imdb = t.imdbId;
  if (t.tvdbId) ids.tvdb = String(t.tvdbId);
  return Object.keys(ids).length ? ids : undefined;
}

/** One `history/watched/bulk` update (native APIClient+Watched). */
export type WatchedUpdate = Record<string, unknown>;

export function movieWatchedUpdate(t: DetailsTarget, watched: boolean): WatchedUpdate {
  return {
    watched,
    name: t.name,
    mediaType: 'movie',
    itemId: t.titleId,
    ...(t.year ? { year: t.year } : {}),
    ...(externalIds(t) ? { externalIds: externalIds(t) } : {}),
  };
}

export function episodeWatchedUpdate(
  series: DetailsTarget,
  ep: Pick<Episode, 'itemId' | 'name' | 'seasonNumber' | 'episodeNumber'>,
  watched: boolean,
): WatchedUpdate {
  return {
    watched,
    name: ep.name || series.name,
    mediaType: 'episode',
    ...(ep.itemId ? { itemId: ep.itemId } : {}),
    seriesId: series.titleId,
    seriesName: series.name,
    seasonNumber: ep.seasonNumber,
    episodeNumber: ep.episodeNumber,
    ...(externalIds(series) ? { externalIds: externalIds(series) } : {}),
  };
}

export function setWatchedBulk(
  profileId: string,
  updates: WatchedUpdate[],
  scope?: 'show' | 'season',
) {
  return apiRequest<unknown>(`/users/${seg(profileId)}/history/watched/bulk`, {
    method: 'POST',
    body: updates,
    query: scope ? { scope } : undefined,
  });
}

/** `WatchlistUpsert` body (native DetailsTarget.watchlistUpsert). */
export function watchlistUpsert(t: DetailsTarget) {
  return {
    id: t.titleId,
    mediaType: t.mediaType,
    name: t.name,
    ...(t.overview ? { overview: t.overview } : {}),
    ...(t.year ? { year: t.year } : {}),
    ...(t.posterUrl ? { posterUrl: t.posterUrl } : {}),
    ...(t.backdropUrl ? { backdropUrl: t.backdropUrl } : {}),
    ...(externalIds(t) ? { externalIds: externalIds(t) } : {}),
  };
}

export function addToWatchlist(profileId: string, t: DetailsTarget) {
  return apiRequest<unknown>(`/users/${seg(profileId)}/watchlist`, {
    method: 'POST',
    body: watchlistUpsert(t),
  });
}

/** A 404 means it was already gone, which counts as success (native). */
export async function removeFromWatchlist(profileId: string, t: DetailsTarget) {
  try {
    await apiRequest<unknown>(
      `/users/${seg(profileId)}/watchlist/${seg(t.mediaType)}/${seg(t.titleId)}`,
      { method: 'DELETE' },
    );
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return;
    throw err;
  }
}

/** The trailer details plays: the server's pick, else the first. */
export const primaryTrailer = (list?: TrailerList | null): Trailer | undefined =>
  list?.primaryTrailer ?? list?.trailers?.[0];

/** A YouTube embed URL for the trailer, or undefined when it isn't on YouTube. */
export function youTubeEmbedUrl(trailer: Trailer): string | undefined {
  let key = trailer.key;
  if (!key) {
    try {
      const u = new URL(trailer.url);
      const host = u.hostname.replace(/^www\.|^m\./, '');
      if (host === 'youtu.be') key = u.pathname.slice(1);
      else if (host === 'youtube.com') key = u.searchParams.get('v') ?? undefined;
    } catch {
      /* not a URL */
    }
  } else if (trailer.site && trailer.site.toLowerCase() !== 'youtube') {
    key = undefined;
  }
  return key
    ? `https://www.youtube-nocookie.com/embed/${encodeURIComponent(key)}?autoplay=1&rel=0`
    : undefined;
}

// ---------------------------------------------------------------- library copies

export interface LibraryProbe {
  durationSeconds?: number;
  videoCodec?: string;
  width?: number;
  height?: number;
  hdrFormat?: string;
}

export interface LibraryItem {
  id: string;
  fileName?: string;
  seasonNumber?: number;
  episodeNumber?: number;
  probe?: LibraryProbe;
  sizeBytes?: number;
  sourceType?: string;
  versionLabel?: string;
}

export interface LibraryGroup {
  id: string;
  title: string;
  items?: LibraryItem[];
  seasons?: { seasonNumber: number; episodes: { episodeNumber: number; items: LibraryItem[] }[] }[];
}

export interface LibraryMatch {
  libraryId: string;
  libraryName: string;
  group: LibraryGroup;
}

/** One copy of a movie / episode in a library (native LibraryVersion). */
export interface LibraryVersion {
  item: LibraryItem;
  libraryName: string;
}

/** A title's copies: movie versions and episode copies keyed `s:e` (native LibraryVersions). */
export interface LibraryVersions {
  movie: LibraryVersion[];
  episodes: Record<string, LibraryVersion[]>;
}

export const episodeKey = (season: number, episode: number) => `${season}:${episode}`;

export function toLibraryVersions(matches: LibraryMatch[]): LibraryVersions {
  const out: LibraryVersions = { movie: [], episodes: {} };
  for (const m of matches) {
    for (const item of m.group.items ?? []) {
      if (!out.movie.some((v) => v.item.id === item.id))
        out.movie.push({ item, libraryName: m.libraryName });
    }
    for (const season of m.group.seasons ?? []) {
      for (const ep of season.episodes) {
        const key = episodeKey(season.seasonNumber, ep.episodeNumber);
        const list = (out.episodes[key] ??= []);
        for (const item of ep.items) {
          if (!list.some((v) => v.item.id === item.id))
            list.push({ item, libraryName: m.libraryName });
        }
      }
    }
  }
  return out;
}

export function libraryMatches(profileId: string, t: DetailsTarget, signal?: AbortSignal) {
  return apiRequest<LibraryMatch[]>('/library/matches', {
    signal,
    query: {
      mediaType: t.mediaType,
      title: t.name,
      profileId,
      titleId: t.titleId,
      tmdbId: t.tmdbId,
      imdbId: t.imdbId,
      tvdbId: t.tvdbId,
      year: t.year,
    },
  });
}

export interface LibraryPlaybackInfo {
  itemId: string;
  streamPath: string;
  durationSeconds?: number;
}

export function libraryPlayback(itemId: string, profileId: string, profileName?: string) {
  return apiRequest<LibraryPlaybackInfo>(`/library/items/${seg(itemId)}/playback`, {
    query: { profileId, profileName },
  });
}

const SOURCE_LABELS: Record<string, string> = { plex: 'Plex', jellyfin: 'Jellyfin' };

/** "Plex · Films". */
export const versionOrigin = (v: LibraryVersion) =>
  `${SOURCE_LABELS[(v.item.sourceType ?? '').toLowerCase()] ?? 'Local'} · ${v.libraryName}`;

/** "4K · HEVC · HDR10", else the server's version name, else the file name. */
export function versionQuality(v: LibraryVersion): string {
  const p = v.item.probe ?? {};
  const w = p.width ?? 0;
  const h = p.height ?? 0;
  const res =
    w >= 3200 || h >= 1800
      ? '4K'
      : w >= 1800 || h >= 1000
        ? '1080p'
        : w >= 1200 || h >= 700
          ? '720p'
          : h > 0
            ? `${h}p`
            : undefined;
  const c = (p.videoCodec ?? '').toLowerCase();
  const codec = !c
    ? undefined
    : c === 'hevc' || c === 'h265'
      ? 'HEVC'
      : ['h264', 'avc', 'avc1'].includes(c)
        ? 'H.264'
        : c.toUpperCase();
  const r = (p.hdrFormat ?? '').toLowerCase();
  const hdr = !r
    ? undefined
    : r.includes('dolby') || r.includes('dovi') || r === 'dv'
      ? 'Dolby Vision'
      : r.includes('10+') || r.includes('10plus')
        ? 'HDR10+'
        : r.includes('hlg')
          ? 'HLG'
          : r.includes('hdr') || r.includes('pq')
            ? 'HDR10'
            : r.toUpperCase();
  const parts = [res, codec, hdr].filter(Boolean);
  if (parts.length) return parts.join(' · ');
  return v.item.versionLabel || v.item.fileName || 'Library file';
}
