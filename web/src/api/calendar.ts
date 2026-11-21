import { apiRequest } from './client';
import type { Title } from './titles';

/**
 * Calendar (mirrors native-tvos StrmrTV/API/APIClient+Calendar.swift, CalendarItem in
 * APIClient+Home.swift and the pure grouping in Calendar/CalendarModel.swift).
 */

/** GET /api/users/{id}/calendar item (backend services/calendar). */
export interface CalendarItem {
  title: string;
  episodeTitle?: string;
  episodeOverview?: string;
  /** 'movie' | 'series' */
  mediaType: string;
  seasonNumber?: number;
  episodeNumber?: number;
  /** YYYY-MM-DD in the viewer's time zone. */
  airDate: string;
  /** HH:MM in the viewer's time zone. */
  airTime?: string;
  network?: string;
  /** Movies: theatrical, digital, physical... */
  releaseType?: string;
  posterUrl?: string;
  textPosterUrl?: string;
  backdropUrl?: string;
  textBackdropUrl?: string;
  backdropUrls?: string[];
  overview?: string;
  logo?: { url: string };
  genres?: string[];
  runtimeMinutes?: number;
  year?: number;
  externalIds?: Record<string, string>;
  /** watchlist | history | trending | top-trending | mdblist */
  source?: string;
  /** Some servers send the title id; natives fall back to the external ids. */
  titleId?: string;
}

/** `source=` the server is asked for; 'all' sends none (CalendarSource). */
export type CalendarSource =
  'all' | 'watchlist' | 'history' | 'trending' | 'top-trending' | 'mdblist';
/** Which releases are kept client-side (CalendarFilter). */
export type CalendarType = 'all' | 'series' | 'movies';

export const CALENDAR_SOURCES: { value: CalendarSource; label: string }[] = [
  { value: 'all', label: 'Everything' },
  { value: 'watchlist', label: 'Watchlist' },
  { value: 'history', label: 'Watching' },
  { value: 'trending', label: 'Trending' },
  { value: 'top-trending', label: 'Top' },
];

export const calendarKeys = {
  all: ['calendar'] as const,
  list: (profileId: string, source: CalendarSource) =>
    [...calendarKeys.all, profileId, 'list', source] as const,
  enrichment: (profileId: string, key: string) =>
    [...calendarKeys.all, profileId, 'enrichment', key] as const,
};

export function viewerTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC';
  } catch {
    return 'UTC';
  }
}

/** 90 days ahead, 21 back, in the viewer's time zone (as the native/Expo calendar asks). */
export async function getCalendar(
  profileId: string,
  source: CalendarSource,
  signal?: AbortSignal,
): Promise<CalendarItem[]> {
  const res = await apiRequest<{ items?: CalendarItem[] | null }>(
    `/users/${encodeURIComponent(profileId)}/calendar`,
    {
      query: {
        days: 90,
        recentDays: 21,
        tz: viewerTimeZone(),
        source: source === 'all' ? undefined : source,
      },
      signal,
    },
  );
  return res?.items ?? [];
}

// MARK: pure model

export interface CalendarDay {
  key: string;
  items: CalendarItem[];
}

/** A release's identity within the calendar (buildCalendarItemKey). */
export function calendarItemKey(item: CalendarItem): string {
  const ids = item.externalIds ?? {};
  const identity =
    (ids.tmdb && `tmdb:${ids.tmdb}`) ||
    (ids.tvdb && `tvdb:${ids.tvdb}`) ||
    ids.imdb ||
    item.title.trim().toLowerCase();
  return [
    identity,
    item.mediaType,
    item.airDate,
    String(item.seasonNumber ?? 0),
    String(item.episodeNumber ?? 0),
    item.releaseType ?? '',
  ].join(':');
}

/** Releases with an air date passing `type`, grouped by day; within a day by time, all-day last. */
export function groupCalendarDays(items: CalendarItem[], type: CalendarType): CalendarDay[] {
  const kept = items
    .map((item, index) => ({ item, index }))
    .filter(({ item }) => {
      if (!item.airDate) return false;
      if (type === 'series') return item.mediaType === 'series';
      if (type === 'movies') return item.mediaType === 'movie';
      return true;
    });
  kept.sort((a, b) => {
    if (a.item.airDate !== b.item.airDate) return a.item.airDate < b.item.airDate ? -1 : 1;
    const at = a.item.airTime ?? '99:99';
    const bt = b.item.airTime ?? '99:99';
    if (at !== bt) return at < bt ? -1 : 1;
    return a.index - b.index;
  });
  const days: CalendarDay[] = [];
  for (const { item } of kept) {
    const last = days[days.length - 1];
    if (last && last.key === item.airDate) last.items.push(item);
    else days.push({ key: item.airDate, items: [item] });
  }
  return days;
}

export const pad2 = (n: number) => String(n).padStart(2, '0');

/** YYYY-MM-DD in local time. */
export function toDateKey(date: Date): string {
  return `${date.getFullYear()}-${pad2(date.getMonth() + 1)}-${pad2(date.getDate())}`;
}

/** Local midnight of a YYYY-MM-DD key. */
export function fromDateKey(key: string): Date {
  const [y, m, d] = key.split('-').map((p) => Number(p));
  return new Date(y || 1970, (m || 1) - 1, d || 1);
}

export function addDays(key: string, days: number): string {
  const d = fromDateKey(key);
  d.setDate(d.getDate() + days);
  return toDateKey(d);
}

/** 42 day keys (6 weeks, Monday first) covering the month that contains `key`. */
export function monthGrid(key: string): string[] {
  const first = fromDateKey(key);
  first.setDate(1);
  const offset = (first.getDay() + 6) % 7;
  const start = toDateKey(first);
  return Array.from({ length: 42 }, (_, i) => addDays(start, i - offset));
}

/** The 7 day keys (Monday first) of the week containing `key`. */
export function weekDays(key: string): string[] {
  const d = fromDateKey(key);
  const offset = (d.getDay() + 6) % 7;
  return Array.from({ length: 7 }, (_, i) => addDays(key, i - offset));
}

export function itemSubtitle(item: CalendarItem): string {
  if (item.mediaType === 'movie') {
    const kind = item.releaseType ? `${capitalize(item.releaseType)} release` : 'Movie';
    return kind;
  }
  const se =
    item.seasonNumber != null && item.episodeNumber != null
      ? `S${item.seasonNumber} · E${item.episodeNumber}`
      : '';
  return [se, item.episodeTitle].filter(Boolean).join(' — ') || 'New episode';
}

export function formatAirTime(time: string | undefined): string | null {
  if (!time) return null;
  const [h, m] = time.split(':').map(Number);
  if (!Number.isFinite(h) || !Number.isFinite(m)) return time;
  const d = new Date(2000, 0, 1, h, m);
  return d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
}

const capitalize = (s: string) => s.charAt(0).toUpperCase() + s.slice(1);

/** Title-shaped link data for detailsLink() (titleId falls back like the native enrichment). */
export function calendarItemTitle(
  item: CalendarItem,
): Pick<Title, 'id' | 'name' | 'mediaType' | 'year' | 'tmdbId' | 'tvdbId' | 'imdbId'> {
  const ids = item.externalIds ?? {};
  const tmdb = Number(ids.tmdb);
  const tvdb = Number(ids.tvdb);
  const mediaType = item.mediaType === 'movie' ? 'movie' : 'series';
  // Same order as native CalendarItem.titleID.
  const id =
    item.titleId ||
    (mediaType === 'movie'
      ? (ids.tmdb && `tmdb:movie:${ids.tmdb}`) || ids.imdb || (ids.tvdb && `tvdb:movie:${ids.tvdb}`)
      : (ids.tmdb && `tmdb:tv:${ids.tmdb}`) || (ids.tvdb && `tvdb:${ids.tvdb}`) || ids.imdb) ||
    item.title;
  return {
    id,
    name: item.title,
    mediaType,
    year: item.year,
    tmdbId: Number.isFinite(tmdb) && tmdb > 0 ? tmdb : undefined,
    tvdbId: Number.isFinite(tvdb) && tvdb > 0 ? tvdb : undefined,
    imdbId: ids.imdb,
  };
}

// MARK: enrichment (detail panel)

export interface CalendarEnrichment {
  synopsis?: string;
  backdropUrl?: string;
  year?: number;
  runtimeMinutes?: number;
  certification?: string;
  genres: string[];
  cast: string[];
}

interface DetailsTitle extends Title {
  credits?: { cast?: { name: string; order?: number }[] };
}
interface SeriesDetailsResponse {
  title: DetailsTitle;
  seasons?: {
    number: number;
    episodes?: { episodeNumber: number; overview?: string; runtimeMinutes?: number }[];
  }[];
}

export function mapMovieEnrichment(movie: DetailsTitle): CalendarEnrichment {
  return {
    synopsis: movie.overview || undefined,
    backdropUrl: movie.backdrop?.url,
    year: movie.year,
    runtimeMinutes: movie.runtimeMinutes,
    certification: movie.certification,
    genres: movie.genres ?? [],
    cast: (movie.credits?.cast ?? []).slice(0, 8).map((c) => c.name),
  };
}

/** The show's, with the episode's overview and runtime in place of the show's. */
export function mapSeriesEnrichment(
  series: SeriesDetailsResponse,
  item: CalendarItem,
): CalendarEnrichment {
  const base = mapMovieEnrichment(series.title);
  const episode = series.seasons
    ?.find((s) => s.number === item.seasonNumber)
    ?.episodes?.find((e) => e.episodeNumber === item.episodeNumber);
  return {
    ...base,
    synopsis: episode?.overview || item.episodeOverview || item.overview || base.synopsis,
    runtimeMinutes: episode?.runtimeMinutes || base.runtimeMinutes,
  };
}

export async function getCalendarEnrichment(
  profileId: string,
  item: CalendarItem,
  signal?: AbortSignal,
): Promise<CalendarEnrichment> {
  const link = calendarItemTitle(item);
  const ids = item.externalIds ?? {};
  const query = {
    titleId: link.id,
    name: item.title,
    userId: profileId,
    year: item.year,
    tmdbId: ids.tmdb,
    tvdbId: ids.tvdb,
  };
  if (item.mediaType === 'movie') {
    const movie = await apiRequest<DetailsTitle>('/metadata/movies/details', {
      query: { ...query, imdbId: ids.imdb },
      signal,
    });
    return mapMovieEnrichment(movie);
  }
  const series = await apiRequest<SeriesDetailsResponse>('/metadata/series/details', {
    query,
    signal,
  });
  return mapSeriesEnrichment(series, item);
}
