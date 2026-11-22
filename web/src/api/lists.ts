/**
 * Watchlist, custom lists and the server's discovery lists. Everything is read
 * through `/users/{id}/display-list`, which sorts, filters and pages server-side
 * (mirrors native-tvos APIClient+Lists.swift, APIClient+Browse.swift, BrowseModels.swift).
 */
import { ApiError, apiRequest } from './client';
import type { Rating, Title } from './titles';

const seg = encodeURIComponent;
const userPath = (profileId: string) => `/users/${seg(profileId)}`;

// MARK: models

/** `models.WatchlistItem` (watchlist / custom list / permanent prequeue entries). */
export interface WatchlistItem {
  id: string;
  mediaType: string;
  name: string;
  overview?: string;
  year?: number;
  posterUrl?: string;
  textPosterUrl?: string;
  backdropUrl?: string;
  logoUrl?: string;
  addedAt?: string;
  externalIds?: Record<string, string>;
  genres?: string[];
  runtimeMinutes?: number;
  watchState?: string;
  unwatchedCount?: number;
  ratings?: Rating[];
  status?: string;
  releaseDate?: string;
  availability?: string;
}

/** `display-list?source=trending|top-ten|…` entry. */
export interface RankedItem {
  rank: number;
  title: Title;
}

export interface DisplayListPage<T> {
  items: T[];
  total: number;
  sourceTotal?: number;
  unfilteredTotal?: number;
  genres?: string[];
  alphabetBuckets?: string[];
}

export interface CustomList {
  id: string;
  name: string;
  itemCount?: number;
  createdAt?: string;
  updatedAt?: string;
}

/** `POST …/watchlist` and `…/custom-lists/{id}/items` body; the server rejects unknown fields. */
export interface WatchlistUpsert {
  id: string;
  mediaType: string;
  name: string;
  overview?: string;
  year?: number;
  posterUrl?: string;
  textPosterUrl?: string;
  backdropUrl?: string;
  externalIds?: Record<string, string>;
}

/** One grid entry, whatever the source's item shape. `item` is set for removable list items. */
export interface BrowseEntry {
  key: string;
  title: Title;
  item?: WatchlistItem;
  rank?: number;
}

export interface BrowsePage {
  entries: BrowseEntry[];
  /** Whole list size after filtering. */
  total: number;
  genres: string[];
  letters: string[];
}

// MARK: browse lists (BrowseModels.swift BrowseList)

export type BrowseKind = 'movie' | 'series';
export type BrowseSort = 'default' | 'added' | 'name' | 'year' | 'duration' | 'rating';
export type WatchFilter = 'all' | 'unwatched' | 'in_progress' | 'watched';

export type BrowseList =
  | { kind: 'watchlist' }
  | { kind: 'custom'; id: string; name?: string }
  | { kind: 'permanent-prequeue' }
  | { kind: 'top-ten'; mediaType?: BrowseKind }
  | { kind: 'trending'; mediaType: BrowseKind }
  | { kind: 'popular-on-server' }
  | { kind: 'personalized' }
  | { kind: 'genre'; mediaType: BrowseKind; genreId: number }
  | { kind: 'decade'; mediaType: BrowseKind; decade: number }
  /** A list configured as a Home shelf (MDBList, Trakt, Simkl, Stremio, TMDB…) or Ask AI. */
  | { kind: 'remote'; id: string; name: string; query: Record<string, string> }
  /** Titles like one the profile is watching ("Because You Watched …"). */
  | { kind: 'similar'; mediaType: BrowseKind; tmdbId: string; seed: string };

/** Lists with their own pages rather than a `display-list` grid (BrowseList.continueWatching / .hidden). */
export type SpecialList = { kind: 'continue-watching' } | { kind: 'hidden' };

/** What a `/lists/$listKey` URL names; a `shelf` is resolved against Home's shelves. */
export type ParsedListKey =
  | BrowseList
  | SpecialList
  | { kind: 'shelf'; shelfId: string }
  | { kind: 'service'; serviceId: string; part: number };

/** The server's AI picks for a description, cached server-side per prompt (BrowseList.askAI). */
export function askAIList(prompt: string): BrowseList {
  const name = `Ask AI: ${prompt}`;
  return { kind: 'remote', id: 'custom-ai', name, query: { source: 'custom-ai', q: prompt, name } };
}

export type RatingSource =
  'imdb' | 'tmdb' | 'trakt' | 'letterboxd' | 'tomatoes' | 'audience' | 'metacritic';

export const RATING_SOURCES: { value: RatingSource; label: string }[] = [
  { value: 'imdb', label: 'IMDb' },
  { value: 'tmdb', label: 'TMDB' },
  { value: 'trakt', label: 'Trakt' },
  { value: 'letterboxd', label: 'Letterboxd' },
  { value: 'tomatoes', label: 'Rotten Tomatoes' },
  { value: 'audience', label: 'RT Audience' },
  { value: 'metacritic', label: 'Metacritic' },
];

export interface Genre {
  id: number;
  name: string;
}

/** TMDB genre ids (movie and TV ids differ); the server passes them to TMDB discover. */
export const GENRES: Record<BrowseKind, Genre[]> = {
  movie: [
    { id: 28, name: 'Action' },
    { id: 12, name: 'Adventure' },
    { id: 16, name: 'Animation' },
    { id: 35, name: 'Comedy' },
    { id: 80, name: 'Crime' },
    { id: 99, name: 'Documentary' },
    { id: 18, name: 'Drama' },
    { id: 10751, name: 'Family' },
    { id: 14, name: 'Fantasy' },
    { id: 36, name: 'History' },
    { id: 27, name: 'Horror' },
    { id: 10402, name: 'Music' },
    { id: 9648, name: 'Mystery' },
    { id: 10749, name: 'Romance' },
    { id: 878, name: 'Science Fiction' },
    { id: 53, name: 'Thriller' },
    { id: 10752, name: 'War' },
    { id: 37, name: 'Western' },
  ],
  series: [
    { id: 10759, name: 'Action & Adventure' },
    { id: 16, name: 'Animation' },
    { id: 35, name: 'Comedy' },
    { id: 80, name: 'Crime' },
    { id: 99, name: 'Documentary' },
    { id: 18, name: 'Drama' },
    { id: 10751, name: 'Family' },
    { id: 10762, name: 'Kids' },
    { id: 9648, name: 'Mystery' },
    { id: 10764, name: 'Reality' },
    { id: 10765, name: 'Sci-Fi & Fantasy' },
    { id: 10768, name: 'War & Politics' },
    { id: 37, name: 'Western' },
  ],
};

/** Decades TMDB discover has enough titles for, newest first. */
export const decades = (kind: BrowseKind): number[] => {
  const out: number[] = [];
  for (let d = 2020; d >= (kind === 'movie' ? 1930 : 1950); d -= 10) out.push(d);
  return out;
};

const noun = (k: BrowseKind) => (k === 'movie' ? 'Movies' : 'Shows');

export function listTitle(list: BrowseList): string {
  switch (list.kind) {
    case 'watchlist':
      return 'Watchlist';
    case 'custom':
      return list.name ?? 'List';
    case 'permanent-prequeue':
      return 'Permanent Prequeue';
    case 'top-ten':
      return list.mediaType ? `Top 10 ${noun(list.mediaType)}` : 'Top 10 Today';
    case 'trending':
      return `Trending ${noun(list.mediaType)}`;
    case 'popular-on-server':
      return 'Popular on This Server';
    case 'personalized':
      return 'Recommended for You';
    case 'genre': {
      const g = GENRES[list.mediaType].find((x) => x.id === list.genreId);
      return `${g?.name ?? 'Genre'} ${noun(list.mediaType)}`;
    }
    case 'decade':
      return `${list.decade}s ${noun(list.mediaType)}`;
    case 'remote':
      return list.name;
    case 'similar':
      return `Because You Watched ${list.seed}`;
  }
}

/** Whether entries are list items (`WatchlistItem`) rather than ranked titles. */
export const isItemList = (list: BrowseList) =>
  list.kind === 'watchlist' || list.kind === 'custom' || list.kind === 'permanent-prequeue';

/** The sorts the server applies for a source (BrowseList.sorts). */
export function listSorts(list: BrowseList): BrowseSort[] {
  if (isItemList(list)) return ['default', 'added', 'name', 'year', 'duration', 'rating'];
  if (
    list.kind === 'genre' ||
    list.kind === 'decade' ||
    list.kind === 'trending' ||
    list.kind === 'remote'
  )
    return ['default', 'name', 'year', 'rating'];
  return [];
}

/** Movies/Shows, watch status, genre facets and A–Z: the profile's own lists only. */
export const listFilters = isItemList;

export const SORT_LABELS: Record<BrowseSort, string> = {
  default: 'Default',
  added: 'Date Added',
  name: 'Name',
  year: 'Year',
  duration: 'Duration',
  rating: 'Rating',
};

export const WATCH_LABELS: Record<WatchFilter, string> = {
  all: 'Any status',
  unwatched: 'Unwatched',
  in_progress: 'In progress',
  watched: 'Watched',
};

/** URL key for a list (`/lists/$listKey`): custom.<id>, top-ten.movie, genre.series.18 … */
export function listKey(list: BrowseList | SpecialList): string {
  switch (list.kind) {
    case 'remote':
      return list.id === 'custom-ai' ? `ai.${list.query.q ?? ''}` : `shelf.${list.id}`;
    case 'similar':
      return `similar.${list.mediaType}.${list.tmdbId}.${list.seed}`;
    case 'custom':
      return `custom.${list.id}`;
    case 'top-ten':
      return list.mediaType ? `top-ten.${list.mediaType}` : 'top-ten';
    case 'trending':
      return `trending.${list.mediaType}`;
    case 'genre':
      return `genre.${list.mediaType}.${list.genreId}`;
    case 'decade':
      return `decade.${list.mediaType}.${list.decade}`;
    default:
      return list.kind;
  }
}

const asKind = (v: string | undefined): BrowseKind | undefined =>
  v === 'movie' || v === 'series' ? v : undefined;

export function parseListKey(key: string): ParsedListKey | null {
  const [kind, a, b] = key.split('.');
  const n = Number(b);
  switch (kind) {
    case 'continue-watching':
    case 'hidden':
      return { kind };
    case 'shelf':
      return a ? { kind, shelfId: key.slice('shelf.'.length) } : null;
    case 'similar': {
      const seed = key.split('.').slice(3).join('.');
      return asKind(a) && b && /^\d+$/.test(b) && seed
        ? { kind, mediaType: asKind(a)!, tmdbId: b, seed }
        : null;
    }
    case 'service': {
      const part = Number(b ?? 0);
      return a && Number.isInteger(part) && part >= 0 ? { kind, serviceId: a, part } : null;
    }
    case 'ai': {
      const prompt = key.slice('ai.'.length).trim();
      return prompt ? askAIList(prompt) : null;
    }
    case 'watchlist':
    case 'permanent-prequeue':
    case 'popular-on-server':
    case 'personalized':
      return { kind };
    case 'custom':
      return a ? { kind, id: key.slice('custom.'.length) } : null;
    case 'top-ten':
      return a === undefined ? { kind } : asKind(a) ? { kind, mediaType: asKind(a) } : null;
    case 'trending':
      return asKind(a) ? { kind, mediaType: asKind(a)! } : null;
    case 'genre':
      return asKind(a) && Number.isInteger(n) && n > 0
        ? { kind, mediaType: asKind(a)!, genreId: n }
        : null;
    case 'decade':
      return asKind(a) && Number.isInteger(n) && n > 1800
        ? { kind, mediaType: asKind(a)!, decade: n }
        : null;
    default:
      return null;
  }
}

/** `display-list` query for a list, without paging. */
export function listQuery(list: BrowseList): Record<string, string> {
  switch (list.kind) {
    case 'watchlist':
      return { source: 'watchlist' };
    case 'custom':
      return { source: 'custom-list', listId: list.id };
    case 'permanent-prequeue':
      return { source: 'permanent-prequeue' };
    case 'top-ten':
      return list.mediaType
        ? { source: 'top-ten', mediaType: list.mediaType }
        : { source: 'top-ten' };
    case 'trending':
      return { source: 'trending', mediaType: list.mediaType };
    case 'popular-on-server':
      return { source: 'popular-on-server' };
    case 'personalized':
      return { source: 'personalized' };
    case 'genre':
      return { source: 'genre', genreId: String(list.genreId), mediaType: list.mediaType };
    case 'decade':
      return { source: 'decade', decade: String(list.decade), mediaType: list.mediaType };
    case 'remote':
      return { ...list.query };
    case 'similar':
      return { source: 'similar', tmdbId: list.tmdbId, mediaType: list.mediaType };
  }
}

export interface BrowseOptions {
  sort?: BrowseSort;
  dir?: 'asc' | 'desc';
  type?: BrowseKind;
  status?: WatchFilter;
  genres?: string[];
  letter?: string;
  ratingSource?: RatingSource;
}

/** Options → query params (BrowseOptions.query(for:)). */
export function optionsQuery(list: BrowseList, o: BrowseOptions): Record<string, string> {
  const q: Record<string, string> = {};
  const sort = o.sort ?? 'default';
  if (sort !== 'default' && listSorts(list).includes(sort)) {
    q.sortBy = sort;
    q.sortDirection = o.dir ?? (sort === 'name' ? 'asc' : 'desc');
    if (sort === 'rating' && listFilters(list)) q.ratingSource = o.ratingSource ?? 'imdb';
  }
  if (!listFilters(list)) return q;
  q.includeFacets = 'true';
  if (o.type) q.filterMediaType = o.type;
  if (o.status && o.status !== 'all') q.watchStatus = o.status;
  if (o.genres?.length) q.genres = [...o.genres].sort().join(',');
  if (o.letter && sort === 'name') q.alphabet = o.letter;
  return q;
}

// MARK: mappers

const num = (v: string | undefined) => {
  const n = Number(v);
  return Number.isFinite(n) && n > 0 ? n : undefined;
};

/** A list item as the shared Title (for cards and detailsLink). */
export function itemToTitle(item: WatchlistItem): Title {
  const ext = item.externalIds ?? {};
  return {
    id: item.id,
    name: item.name,
    overview: item.overview,
    year: item.year || undefined,
    mediaType: item.mediaType === 'movie' ? 'movie' : 'series',
    poster: item.posterUrl ? { url: item.posterUrl } : undefined,
    textPoster: item.textPosterUrl ? { url: item.textPosterUrl } : undefined,
    backdrop: item.backdropUrl ? { url: item.backdropUrl } : undefined,
    logo: item.logoUrl ? { url: item.logoUrl } : undefined,
    imdbId: ext.imdb || undefined,
    tmdbId: num(ext.tmdb),
    tvdbId: num(ext.tvdb),
    genres: item.genres,
    runtimeMinutes: item.runtimeMinutes,
    watchState: item.watchState,
    unwatchedCount: item.unwatchedCount,
    ratings: item.ratings,
    status: item.status,
    releaseDate: item.releaseDate,
    availability: item.availability,
  };
}

/** The list's whole size (DisplayListPage.listTotal). */
export const pageTotal = (p: DisplayListPage<unknown>) =>
  Math.max(p.sourceTotal ?? p.total ?? 0, p.items?.length ?? 0);

export function toBrowsePage(list: BrowseList, raw: DisplayListPage<unknown>): BrowsePage {
  const items = raw.items ?? [];
  const entries: BrowseEntry[] = isItemList(list)
    ? (items as WatchlistItem[]).map((item) => ({
        key: `${item.mediaType}:${item.id}`,
        title: itemToTitle(item),
        item,
      }))
    : (items as (RankedItem | Title)[])
        .map((r) =>
          r && 'title' in r && r.title && typeof r.title === 'object'
            ? { title: r.title, rank: list.kind === 'similar' ? undefined : r.rank }
            : { title: r as Title, rank: undefined },
        )
        .filter((r) => r.title && r.title.id && r.title.name)
        .map((r) => ({ key: `${r.title.mediaType}:${r.title.id}`, title: r.title, rank: r.rank }));
  return {
    entries,
    total: pageTotal(raw),
    genres: raw.genres ?? [],
    letters: raw.alphabetBuckets ?? [],
  };
}

// MARK: requests

export const BROWSE_PAGE_SIZE = 60;

export const listsKeys = {
  all: ['lists'] as const,
  custom: (profileId: string) => [...listsKeys.all, profileId, 'custom'] as const,
  browse: (profileId: string, key: string, options: BrowseOptions) =>
    [...listsKeys.all, profileId, 'browse', key, options] as const,
  browseAll: (profileId: string) => [...listsKeys.all, profileId, 'browse'] as const,
};

export async function browsePage(
  profileId: string,
  list: BrowseList,
  options: BrowseOptions,
  offset: number,
  signal?: AbortSignal,
  limit = BROWSE_PAGE_SIZE,
): Promise<BrowsePage> {
  const raw = await apiRequest<DisplayListPage<unknown>>(`${userPath(profileId)}/display-list`, {
    query: {
      ...listQuery(list),
      ...optionsQuery(list, options),
      limit,
      offset,
    },
    signal,
  });
  return toBrowsePage(list, raw);
}

export const listCustomLists = (profileId: string, signal?: AbortSignal) =>
  apiRequest<CustomList[]>(`${userPath(profileId)}/custom-lists`, { signal });

export const createCustomList = (profileId: string, name: string) =>
  apiRequest<CustomList>(`${userPath(profileId)}/custom-lists`, { method: 'POST', body: { name } });

export const renameCustomList = (profileId: string, id: string, name: string) =>
  apiRequest<CustomList>(`${userPath(profileId)}/custom-lists/${seg(id)}`, {
    method: 'PATCH',
    body: { name },
  });

export const deleteCustomList = (profileId: string, id: string) =>
  apiRequest<unknown>(`${userPath(profileId)}/custom-lists/${seg(id)}`, { method: 'DELETE' });

/** A 404 (already gone) counts as success, as on native. */
async function ignore404(p: Promise<unknown>) {
  try {
    await p;
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return;
    throw err;
  }
}

export const removeFromWatchlist = (profileId: string, mediaType: string, id: string) =>
  ignore404(
    apiRequest(`${userPath(profileId)}/watchlist/${seg(mediaType)}/${seg(id)}`, {
      method: 'DELETE',
    }),
  );

export const removeFromCustomList = (
  profileId: string,
  listId: string,
  mediaType: string,
  id: string,
) =>
  ignore404(
    apiRequest(
      `${userPath(profileId)}/custom-lists/${seg(listId)}/items/${seg(mediaType)}/${seg(id)}`,
      { method: 'DELETE' },
    ),
  );

export const addToCustomList = (profileId: string, listId: string, upsert: WatchlistUpsert) =>
  apiRequest<WatchlistItem>(`${userPath(profileId)}/custom-lists/${seg(listId)}/items`, {
    method: 'POST',
    body: upsert,
  });

/** The reserved "Set Aside" list (titles parked out of Continue Watching). */
export const isSetAside = (l: CustomList) => l.name.trim().toLowerCase() === 'set aside';
