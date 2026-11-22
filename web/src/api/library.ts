/**
 * Local, Plex and Jellyfin libraries (`/api/library/…`). Every call names the
 * profile; the server checks each library's access list and kids rating caps.
 * Mirrors native-tvos APIClient+Library.swift, LibraryModels.swift, LibraryVersions.swift.
 */
import { apiRequest } from './client';
import type { ImageRef, Title } from './titles';

const seg = encodeURIComponent;

export interface LibraryInfo {
  id: string;
  name: string;
  /** movie | show | other */
  type: string;
  /** idle | scanning | complete | failed */
  lastScanStatus?: string;
  /** local | plex | jellyfin */
  sourceType?: string;
  sourceServerName?: string;
}

export interface LibraryProbe {
  durationSeconds?: number;
  videoCodec?: string;
  width?: number;
  height?: number;
  hdrFormat?: string;
  audioCodecs?: string[];
}

export interface LibraryItem {
  id: string;
  fileName?: string;
  seasonNumber?: number;
  episodeNumber?: number;
  episodeTitle?: string;
  episodeOverview?: string;
  episodeImage?: ImageRef;
  detectedTitle?: string;
  probe?: LibraryProbe;
  sizeBytes?: number;
  sourceType?: string;
  sourceServerName?: string;
  versionLabel?: string;
}

export interface LibraryEpisode {
  episodeNumber: number;
  episodeTitle?: string;
  episodeOverview?: string;
  episodeImage?: ImageRef;
  items: LibraryItem[];
}

export interface LibrarySeason {
  seasonNumber: number;
  episodes: LibraryEpisode[];
}

export interface LibraryGroup {
  id: string;
  /** title | show | file */
  groupType?: string;
  libraryType?: string;
  title: string;
  overview?: string;
  year?: number;
  tmdbId?: number;
  tvdbId?: number;
  imdbId?: string;
  poster?: ImageRef;
  textPoster?: ImageRef;
  backdrop?: ImageRef;
  textBackdrop?: ImageRef;
  items?: LibraryItem[];
  seasons?: LibrarySeason[];
}

export interface LibraryGroupPage {
  groups: LibraryGroup[];
  total: number;
  alphabetBuckets?: string[];
}

export interface LibraryPlaybackInfo {
  itemId: string;
  streamPath: string;
  durationSeconds?: number;
}

export type LibrarySort = 'default' | 'added' | 'name' | 'year';
export type LibraryTypeFilter = 'all' | 'movie' | 'show';

export interface LibraryBrowseOptions {
  sort?: LibrarySort;
  dir?: 'asc' | 'desc';
  type?: LibraryTypeFilter;
  q?: string;
  letter?: string;
}

export const LIBRARY_PAGE_SIZE = 60;

export const libraryKeys = {
  all: ['library'] as const,
  libraries: (profileId: string) => [...libraryKeys.all, profileId, 'libraries'] as const,
  groups: (profileId: string, libraryId: string, options: LibraryBrowseOptions) =>
    [...libraryKeys.all, profileId, 'groups', libraryId, options] as const,
  group: (profileId: string, libraryId: string, groupId: string) =>
    [...libraryKeys.all, profileId, 'group', libraryId, groupId] as const,
};

// MARK: helpers

export const sourceLabel = (sourceType?: string) => {
  switch (sourceType?.toLowerCase()) {
    case 'plex':
      return 'Plex';
    case 'jellyfin':
      return 'Jellyfin';
    default:
      return 'Local';
  }
};

export const isShow = (g: LibraryGroup) => g.groupType === 'show' || g.libraryType === 'show';
export const hasFiles = (g: LibraryGroup) => !!g.items?.length || !!g.seasons?.length;
export const groupPoster = (g: LibraryGroup) => g.textPoster?.url ?? g.poster?.url;
export const groupBackdrop = (g: LibraryGroup) => g.backdrop?.url ?? g.textBackdrop?.url;

/** The catalog title the group was matched to (tmdb, else tvdb, else imdb); null when unmatched. */
export function catalogTitleId(g: LibraryGroup): string | null {
  const show = isShow(g);
  if (g.tmdbId && g.tmdbId > 0) return show ? `tmdb:tv:${g.tmdbId}` : `tmdb:movie:${g.tmdbId}`;
  if (g.tvdbId && g.tvdbId > 0) return show ? `tvdb:series:${g.tvdbId}` : `tvdb:movie:${g.tvdbId}`;
  if (g.imdbId) return g.imdbId;
  return null;
}

/** A matched group as a Title for detailsLink(); null when unmatched (no details page). */
export function groupToTitle(g: LibraryGroup): Title | null {
  const id = catalogTitleId(g);
  if (!id) return null;
  return {
    id,
    name: g.title,
    year: g.year || undefined,
    overview: g.overview,
    mediaType: isShow(g) ? 'series' : 'movie',
    tmdbId: g.tmdbId && g.tmdbId > 0 ? g.tmdbId : undefined,
    tvdbId: g.tvdbId && g.tvdbId > 0 ? g.tvdbId : undefined,
    imdbId: g.imdbId || undefined,
    poster: g.poster,
    textPoster: g.textPoster,
    backdrop: g.backdrop,
  };
}

/** "4K · HEVC · Dolby Vision" from the probe, else the server's version name, else the file. */
export function versionQuality(item: LibraryItem): string {
  const p = item.probe ?? {};
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
            : null;
  const c = p.videoCodec?.toLowerCase();
  const codec = !c
    ? null
    : c === 'hevc' || c === 'h265'
      ? 'HEVC'
      : c === 'h264' || c === 'avc' || c === 'avc1'
        ? 'H.264'
        : c.toUpperCase();
  const r = p.hdrFormat?.toLowerCase();
  const hdr = !r
    ? null
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
  if (item.versionLabel) return item.versionLabel;
  return item.fileName ?? 'Library file';
}

/** "21.1 GB · 26 Mbps". */
export function versionSize(item: LibraryItem): string | null {
  const bytes = item.sizeBytes ?? 0;
  if (bytes <= 0) return null;
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = bytes;
  let u = 0;
  while (v >= 1000 && u < units.length - 1) {
    v /= 1000;
    u++;
  }
  const parts = [`${v >= 100 || u === 0 ? Math.round(v) : v.toFixed(1)} ${units[u]}`];
  const secs = item.probe?.durationSeconds ?? 0;
  if (secs > 0) parts.push(`${Math.round((bytes * 8) / secs / 1_000_000)} Mbps`);
  return parts.join(' · ');
}

// MARK: requests

export const listLibraries = (profileId: string, signal?: AbortSignal) =>
  apiRequest<LibraryInfo[]>('/library/libraries', { query: { profileId }, signal });

/** Default and Date Added are newest first for every source (native libraryPage). */
export function groupsQuery(options: LibraryBrowseOptions) {
  const sort = options.sort ?? 'default';
  const key = sort === 'name' ? 'name' : sort === 'year' ? 'year' : 'created';
  const dir = sort === 'default' ? 'desc' : (options.dir ?? (sort === 'name' ? 'asc' : 'desc'));
  return {
    sort: key,
    dir,
    ...(options.letter && sort === 'name' ? { alphabet: options.letter } : {}),
    ...(options.type && options.type !== 'all' ? { mediaType: options.type } : {}),
    ...(options.q?.trim() ? { query: options.q.trim() } : {}),
  };
}

export const listGroups = (
  profileId: string,
  libraryId: string,
  options: LibraryBrowseOptions,
  offset: number,
  signal?: AbortSignal,
  { limit = LIBRARY_PAGE_SIZE, cards = true } = {},
) =>
  apiRequest<LibraryGroupPage>(`/library/libraries/${seg(libraryId)}/groups`, {
    query: {
      limit,
      offset,
      userId: profileId,
      ...groupsQuery(options),
      ...(cards ? { include: 'cards' } : {}),
    },
    signal,
  });

/** A card's group with its files: there's no single-group call, so search and match the id. */
export async function fetchGroupWithFiles(
  profileId: string,
  libraryId: string,
  group: LibraryGroup,
  signal?: AbortSignal,
): Promise<LibraryGroup> {
  if (hasFiles(group)) return group;
  const page = await listGroups(profileId, libraryId, { q: group.title }, 0, signal, {
    limit: 200,
    cards: false,
  });
  const found = page.groups.find((g) => g.id === group.id);
  if (!found) throw new Error('This title is no longer in the library.');
  return found;
}

export const libraryPlayback = (profileId: string, itemId: string, profileName?: string) =>
  apiRequest<LibraryPlaybackInfo>(`/library/items/${seg(itemId)}/playback`, {
    query: { profileId, profileName },
  });

/** `models.ResumeState` (the server decides whether a resume is worth offering). */
export interface LibraryResume {
  position: number;
  duration: number;
  percent: number;
  eligible: boolean;
}

/**
 * Where an unmatched library file was left. Its progress is keyed by its stream path (native
 * LibraryFileStart). Stream paths carry a `/`, which the `history/progress/{type}/{id}` route
 * can't match, so this reads the progress list and picks the row. Undefined when there is
 * nothing worth resuming or it can't be read.
 */
export async function libraryResume(
  profileId: string,
  mediaType: 'movie' | 'episode',
  streamPath: string,
  signal?: AbortSignal,
): Promise<LibraryResume | undefined> {
  try {
    const rows = await apiRequest<
      { mediaType?: string; itemId?: string; resume?: LibraryResume }[] | null
    >(`/users/${seg(profileId)}/history/progress`, { signal });
    const r = (rows ?? []).find(
      (p) => p.itemId === streamPath && p.mediaType === mediaType,
    )?.resume;
    return r && r.eligible && r.position > 0 ? r : undefined;
  } catch {
    return undefined;
  }
}
