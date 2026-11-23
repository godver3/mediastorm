/**
 * Home: the profile's server-configured shelves and the feeds behind them.
 * Mirrors native-tvos StrmrTV/API/APIClient+Home.swift, +ContinueWatching.swift,
 * +ListShelves.swift, +Browse.swift and Home/HomeSource.swift / PlannedShelf.swift.
 */
import { apiRequest, type QueryValue } from './client';
import type { ImageRef, Title } from './titles';

const seg = encodeURIComponent;
const profilePath = (profileId: string) => `/users/${seg(profileId)}`;

// ---------------------------------------------------------------------------
// Types (backend models/user_settings.go ShelfConfig, handlers display-list,
// history/continue; native Models.swift)
// ---------------------------------------------------------------------------

export interface CalendarSources {
  watchlist?: boolean;
  history?: boolean;
  trending?: boolean;
  topTrending?: boolean;
  mdblists?: boolean;
}

export interface StreamingServiceList {
  key?: string;
  title?: string;
  url: string;
}

export interface StreamingService {
  id: string;
  name: string;
  enabled?: boolean;
  order?: number;
  logoUrl?: string;
  lists: StreamingServiceList[];
}

export interface ShelfConfig {
  id: string;
  name?: string;
  enabled?: boolean;
  order?: number;
  type?: string;
  limit?: number;
  activityWindowDays?: number;
  minimumProfiles?: number;
  maxItemsPerProfile?: number;
  calendarSources?: CalendarSources;
  listUrl?: string;
  libraryId?: string;
  addonManifestUrl?: string;
  addonCatalogType?: string;
  addonCatalogId?: string;
  tmdbSourceType?: string;
  tmdbSourceId?: string;
  tmdbMediaType?: string;
  tmdbDiscoverQuery?: string;
  sort?: string;
  traktAccountId?: string;
  traktListType?: string;
  traktListId?: string;
  publicMetaDBAccountId?: string;
  publicMetaDBListId?: string;
  simklAccountId?: string;
  simklListType?: string;
  simklMediaType?: string;
  letterboxdListId?: string;
  letterboxdListUrl?: string;
  hideUnreleased?: boolean;
  streamingServices?: StreamingService[];
  /** collection-hub shelves: cards that open another shelf's list. */
  collectionItems?: CollectionLink[];
}

/** A card of a collection hub shelf (native CollectionLink). */
export interface CollectionLink {
  id: string;
  name?: string;
  enabled?: boolean;
  order?: number;
  sourceShelfId?: string;
  logoUrl?: string;
  heroArtUrl?: string;
}

/** One `homeShelves.views` entry (`config.HomeViewSettings`; native HomeViewDefinition). */
export interface HomeViewDefinition {
  name?: string;
  icon?: string;
  /** all | movies | shows */
  mediaFilter?: string;
  deleted?: boolean;
  /** custom: the page has its own layout; otherwise it follows Home. */
  mode?: string;
  shelves?: ShelfConfig[];
  itemCap?: number;
}

export type HomeMediaFilter = 'all' | 'movies' | 'shows';

export interface HomeShelvesConfig {
  shelves?: ShelfConfig[];
  views?: Record<string, HomeViewDefinition>;
  itemCap?: number;
  exploreCardPosition?: string;
  excludeUpcomingFromContinue?: boolean;
  tvTopShelfMode?: string;
  tvTopShelfSourceId?: string;
}

export interface HomeSettings {
  shelves: ShelfConfig[];
  itemCap: number;
  hideWatched: boolean;
  excludeUpcomingFromContinue: boolean;
  topShelfMode: 'default' | 'disabled' | 'shelf';
  topShelfSourceId?: string;
  /** The profile's Home view definitions (server's merged in). */
  views: Record<string, HomeViewDefinition>;
  /** The Home view these settings are resolved for (`all` = Home itself). */
  view: string;
  filter: HomeMediaFilter;
}

export interface ResumeState {
  position: number;
  duration: number;
  percent: number;
  eligible: boolean;
}

export interface EpisodeRef {
  seasonNumber: number;
  episodeNumber: number;
  title?: string;
  overview?: string;
  image?: ImageRef;
  airDate?: string;
  airDateTimeUTC?: string;
  itemId?: string;
}

export interface ContinueWatchingItem {
  seriesId: string;
  seriesTitle: string;
  overview?: string;
  posterUrl?: string;
  textPosterUrl?: string;
  backdropUrl?: string;
  logoUrl?: string;
  year?: number;
  externalIds?: Record<string, string>;
  nextEpisode?: EpisodeRef;
  percentWatched?: number;
  resumePercent?: number;
  resume?: ResumeState;
  status?: string;
  totalEpisodeCount?: number;
  watchedEpisodeCount?: number;
}

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
  externalIds?: Record<string, string>;
  watchState?: string;
  unwatchedCount?: number;
  status?: string;
}

export interface TrendingItem {
  rank: number;
  title: Title;
}

export interface DisplayListPage<T> {
  items: T[] | null;
  total: number;
  sourceTotal?: number;
}

export interface CalendarItem {
  title: string;
  episodeTitle?: string;
  episodeOverview?: string;
  mediaType: string;
  seasonNumber?: number;
  episodeNumber?: number;
  airDate: string;
  airTime?: string;
  network?: string;
  releaseType?: string;
  posterUrl?: string;
  textPosterUrl?: string;
  backdropUrl?: string;
  overview?: string;
  logo?: ImageRef;
  year?: number;
  externalIds?: Record<string, string>;
  source?: string;
}

export interface DashboardStream {
  id: string;
  profileNames?: string[];
  percentWatched?: number;
  isPaused?: boolean;
  mediaType?: string;
  title?: string;
  year?: number;
  seasonNumber?: number;
  episodeNumber?: number;
  episodeName?: string;
  externalIds?: Record<string, string>;
  posterUrl?: string;
  backdropUrl?: string;
}

// ---------------------------------------------------------------------------
// Query keys
// ---------------------------------------------------------------------------

export const homeKeys = {
  all: ['home'] as const,
  settings: (profileId: string) => [...homeKeys.all, profileId, 'settings'] as const,
  continueWatching: (profileId: string) => [...homeKeys.all, profileId, 'continue'] as const,
  calendar: (profileId: string) => [...homeKeys.all, profileId, 'calendar'] as const,
  shelf: (profileId: string, shelfId: string, query: Record<string, string>) =>
    [...homeKeys.all, profileId, 'shelf', shelfId, query] as const,
  dashboard: (profileId: string) => [...homeKeys.all, profileId, 'dashboard'] as const,
};

// ---------------------------------------------------------------------------
// Fetchers
// ---------------------------------------------------------------------------

interface SettingsResponse {
  homeShelves?: HomeShelvesConfig;
  display?: { hideWatched?: boolean };
}

export function toHomeSettings(raw: SettingsResponse | null | undefined): HomeSettings {
  const home = raw?.homeShelves ?? {};
  const mode = home.tvTopShelfMode;
  return {
    shelves: [...(home.shelves ?? [])].sort((a, b) => (a.order ?? 0) - (b.order ?? 0)),
    itemCap: home.itemCap && home.itemCap > 0 ? home.itemCap : 20,
    hideWatched: raw?.display?.hideWatched ?? false,
    excludeUpcomingFromContinue: home.excludeUpcomingFromContinue ?? true,
    topShelfMode: mode === 'disabled' || mode === 'shelf' ? mode : 'default',
    topShelfSourceId: home.tvTopShelfSourceId || undefined,
    views: home.views ?? {},
    view: 'all',
    filter: 'all',
  };
}

// ---------------------------------------------------------------------------
// Home views (native Home/HomeViews.swift, Expo utils/home-views.ts)
// ---------------------------------------------------------------------------

export const isCustomHomeView = (id: string) => /^page-[a-zA-Z0-9-]{1,80}$/.test(id);

export function homeViewLabel(view: string, views?: Record<string, HomeViewDefinition>) {
  if (view === 'all') return 'Home';
  if (view === 'movies') return 'Movies';
  if (view === 'shows') return 'Shows';
  return views?.[view]?.name || 'Home page';
}

export function homeViewFilter(
  view: string,
  views?: Record<string, HomeViewDefinition>,
): HomeMediaFilter {
  if (view === 'movies') return 'movies';
  if (view === 'shows') return 'shows';
  const d = views?.[view];
  if (!d || d.deleted) return 'all';
  return d.mediaFilter === 'movies' ? 'movies' : d.mediaFilter === 'shows' ? 'shows' : 'all';
}

/** All, Movies, Shows, then the custom pages not deleted, by name (`homeViewIds`). */
export function homeViewIds(views?: Record<string, HomeViewDefinition>): string[] {
  const pages = Object.entries(views ?? {})
    .filter(([id, d]) => isCustomHomeView(id) && !d.deleted)
    .map(([id]) => id)
    .sort(
      (a, b) => homeViewLabel(a, views).localeCompare(homeViewLabel(b, views)) || (a < b ? -1 : 1),
    );
  return ['all', 'movies', 'shows', ...pages];
}

/** Whether an item of `mediaType` belongs on a view showing `filter` (`matchesHomeView`). */
export function matchesHomeFilter(mediaType: string | undefined, filter: HomeMediaFilter) {
  const t = mediaType?.toLowerCase() ?? '';
  if (filter === 'all') return true;
  if (filter === 'movies') return ['movie', 'movies', 'film', 'films'].includes(t);
  return ['tv', 'series', 'show', 'shows', 'episode'].includes(t);
}

/** Whether a shelf can show on a view (`homeViewShelfAllowed`). */
export function homeShelfAllowed(c: ShelfConfig, filter: HomeMediaFilter) {
  if (filter === 'all') return true;
  if (['live-favorites', 'dashboard', 'tonight'].includes(c.id)) return false;
  if (filter === 'movies' && ['trending-tv', 'my-upcoming', 'my-recently-aired'].includes(c.id))
    return false;
  if (filter === 'shows' && c.id === 'trending-movies') return false;
  const type =
    [c.addonCatalogType, c.tmdbMediaType, c.simklMediaType].find((t) => t && t !== '') ??
    /(?:genre-\d+|decade-\d+)-(movie|tv)$/.exec(c.id)?.[1];
  if (!type || type === 'all') return true;
  return matchesHomeFilter(type, filter);
}

/**
 * Home as `view` lays it out (`HomeViews.resolve`, simplified to what the web shows): All is
 * Home as configured; a custom page with its own shelves uses them; then shelves the view's
 * media filter can't show are dropped.
 */
export function resolveHomeView(settings: HomeSettings, view: string): HomeSettings {
  const ids = homeViewIds(settings.views);
  const v = ids.includes(view) ? view : 'all';
  if (v === 'all') return { ...settings, view: 'all', filter: 'all' };
  const filter = homeViewFilter(v, settings.views);
  const d = settings.views[v];
  const own = d && !d.deleted && d.mode === 'custom' && d.shelves ? d.shelves : undefined;
  const shelves = [...(own ?? settings.shelves)]
    .filter((c) => homeShelfAllowed(c, filter))
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  return {
    ...settings,
    shelves,
    itemCap: own && d?.itemCap && d.itemCap > 0 ? d.itemCap : settings.itemCap,
    // An intentionally empty custom page has no top shelf either.
    topShelfMode: own && own.length === 0 ? 'disabled' : settings.topShelfMode,
    view: v,
    filter,
  };
}

/** The `mediaType` the server's surprise / list filters take. */
export const filterMediaType = (f: HomeMediaFilter) =>
  f === 'movies' ? 'movie' : f === 'shows' ? 'series' : undefined;

/** GET /users/{id}/settings (server shelves already merged in). */
export async function getHomeSettings(profileId: string, signal?: AbortSignal) {
  const raw = await apiRequest<SettingsResponse>(`${profilePath(profileId)}/settings`, { signal });
  return toHomeSettings(raw);
}

interface CustomListSummary {
  id: string;
  name: string;
}
interface ListedTitle {
  id: string;
  mediaType: string;
  externalIds?: Record<string, string>;
}

export const isSetAsideList = (l: CustomListSummary) => l.name.trim().toLowerCase() === 'set aside';

export const isMovieId = (seriesId: string) => seriesId.includes(':movie:');

/** Same kind, and the same ID or any shared provider ID (native ListedTitle.matches). */
export function matchesListed(listed: ListedTitle, item: ContinueWatchingItem): boolean {
  if ((listed.mediaType === 'movie') !== isMovieId(item.seriesId)) return false;
  if (listed.id.toLowerCase() === item.seriesId.toLowerCase()) return true;
  const theirs = item.externalIds ?? {};
  return Object.entries(listed.externalIds ?? {}).some(([k, v]) => {
    const a = v.trim().toLowerCase();
    return a !== '' && a === theirs[k]?.trim().toLowerCase();
  });
}

async function setAsideItems(profileId: string, signal?: AbortSignal): Promise<ListedTitle[]> {
  try {
    const lists = await apiRequest<CustomListSummary[]>(`${profilePath(profileId)}/custom-lists`, {
      signal,
    });
    const list = lists?.find(isSetAsideList);
    if (!list) return [];
    return (
      (await apiRequest<ListedTitle[]>(
        `${profilePath(profileId)}/custom-lists/${seg(list.id)}/items`,
        { signal },
      )) ?? []
    );
  } catch {
    return [];
  }
}

/** Continue Watching minus the "Set Aside" list (native continueWatching()). */
export async function getContinueWatching(profileId: string, signal?: AbortSignal) {
  const [rows, aside] = await Promise.all([
    apiRequest<ContinueWatchingItem[] | null>(`${profilePath(profileId)}/history/continue`, {
      signal,
    }),
    setAsideItems(profileId, signal),
  ]);
  const items = (rows ?? []).filter((r) => r && r.seriesId);
  return aside.length ? items.filter((i) => !aside.some((a) => matchesListed(a, i))) : items;
}

export async function getDisplayList<T>(
  profileId: string,
  query: Record<string, QueryValue>,
  signal?: AbortSignal,
) {
  const page = await apiRequest<DisplayListPage<T>>(`${profilePath(profileId)}/display-list`, {
    query,
    signal,
  });
  return { items: page?.items ?? [], total: Math.max(page?.sourceTotal ?? page?.total ?? 0, 0) };
}

export async function getHomeCalendar(profileId: string, limit: number, signal?: AbortSignal) {
  const res = await apiRequest<{ items?: CalendarItem[] | null; pending?: boolean }>(
    `${profilePath(profileId)}/calendar`,
    {
      query: {
        days: 14,
        recentDays: 90,
        compact: 1,
        home: 1,
        limit,
        tz: Intl.DateTimeFormat().resolvedOptions().timeZone,
      },
      signal,
    },
  );
  return (res?.items ?? []).filter(Boolean);
}

export async function getDashboardStreams(signal?: AbortSignal) {
  const res = await apiRequest<{ streams?: DashboardStream[] | null }>('/dashboard/shelf', {
    signal,
  });
  return res?.streams ?? [];
}

/** Random pick for Watch Something's Surprise Me (needs an AI key on the server). */
export async function getSurprise(
  profileId: string,
  signal?: AbortSignal,
  filter: HomeMediaFilter = 'all',
) {
  const mediaType = filterMediaType(filter);
  const res = await apiRequest<{ title: Title }>('/recommendations/surprise', {
    query: { userId: profileId, ...(mediaType ? { mediaType } : {}) },
    signal,
  });
  return res.title;
}

// ---------------------------------------------------------------------------
// Continue Watching / hidden titles (native APIClient+ContinueWatching, +TitleState)
// ---------------------------------------------------------------------------

/** A title hidden from the profile (`/users/{id}/hidden-items`). */
export interface HiddenItem {
  id: string;
  mediaType: string;
  name?: string;
  year?: number;
  posterUrl?: string;
  backdropUrl?: string;
  externalIds?: Record<string, string>;
}

export const hiddenKeys = {
  all: ['hidden'] as const,
  list: (profileId: string) => [...hiddenKeys.all, profileId] as const,
};

/** Drops a title from Continue Watching until it is played again. */
export const hideFromContinueWatching = (profileId: string, seriesId: string) =>
  apiRequest<unknown>(`${profilePath(profileId)}/history/continue/hide`, {
    method: 'POST',
    body: { seriesId },
  });

export const unhideFromContinueWatching = (profileId: string, seriesId: string) =>
  apiRequest<unknown>(`${profilePath(profileId)}/history/continue/unhide`, {
    method: 'POST',
    body: { seriesId },
  });

export async function getHiddenItems(profileId: string, signal?: AbortSignal) {
  const rows = await apiRequest<HiddenItem[] | null>(`${profilePath(profileId)}/hidden-items`, {
    signal,
  });
  return (rows ?? []).filter((r) => r && r.id);
}

/** Hides a title from the profile; the server drops it from Home rows and lists. */
export const hideTitle = (profileId: string, item: HiddenItem) =>
  apiRequest<HiddenItem>(`${profilePath(profileId)}/hidden-items`, {
    method: 'POST',
    body: item,
  });

export const unhideTitle = (profileId: string, mediaType: string, id: string) =>
  apiRequest<unknown>(`${profilePath(profileId)}/hidden-items/${seg(mediaType)}/${seg(id)}`, {
    method: 'DELETE',
  });

/** The hidden-items body for a Continue Watching item (native APIClient.hide). */
export function hiddenItemFor(item: ContinueWatchingItem): HiddenItem {
  return {
    id: item.seriesId,
    mediaType: isMovieId(item.seriesId) ? 'movie' : 'series',
    name: item.seriesTitle,
    ...(item.year ? { year: item.year } : {}),
    ...(item.posterUrl ? { posterUrl: item.posterUrl } : {}),
    ...(item.backdropUrl ? { backdropUrl: item.backdropUrl } : {}),
    ...(item.externalIds && Object.keys(item.externalIds).length
      ? { externalIds: item.externalIds }
      : {}),
  };
}

// ---------------------------------------------------------------------------
// Shelf planning (HomeSource / PlannedShelf)
// ---------------------------------------------------------------------------

export type ShelfSource =
  | { kind: 'continueWatching' }
  | { kind: 'upcoming' }
  | { kind: 'watchlist' }
  | { kind: 'list'; query: Record<string, string>; ranked: boolean; shape: 'ranked' | 'item' }
  | { kind: 'calendar' }
  | { kind: 'recentlyAired'; sources?: CalendarSources }
  | { kind: 'dashboard' }
  | { kind: 'watchSomething' }
  | { kind: 'services'; services: StreamingService[] };

export interface PlannedShelf {
  id: string;
  title: string;
  source: ShelfSource;
  cap: number;
  landscape: boolean;
  /** A per-shelf limit below the item cap: the row is the whole thing, no See all. */
  limited: boolean;
  /** `/lists/$listKey` key of the full list behind the row (native HomeShelf.list). */
  seeAll?: string;
}

/** The list a row's See all opens (native ShelfConfig.browseList + HomeSource). */
export function shelfListKey(c: ShelfConfig, source: ShelfSource): string | undefined {
  if (source.kind === 'continueWatching') return 'continue-watching';
  if (source.kind !== 'list') return undefined;
  switch (c.id) {
    case 'top-ten':
      return 'top-ten';
    case 'my-recommended':
    case 'gemini-recs':
      return 'personalized';
    case 'trending-movies':
      return 'trending.movie';
    case 'trending-tv':
      return 'trending.series';
    case 'permanent-prequeue':
      return 'permanent-prequeue';
  }
  const q = source.query;
  if (q.source === 'genre') return `genre.${q.mediaType}.${q.genreId}`;
  if (q.source === 'decade') return `decade.${q.mediaType}.${q.decade}`;
  // Remote lists and the server's activity rows keep their own query.
  return `shelf.${c.id}`;
}

/** Shelves the native/Expo home draws 16:9 (ShelfStyle.isLandscapeByDefault). */
const LANDSCAPE_IDS = new Set([
  'continue-watching',
  'my-upcoming',
  'watch-something',
  'dashboard',
  'streaming-services',
  'recently-watched',
  'watch-together',
  'tonight',
  'live-favorites',
]);

const clean = (s?: string) => {
  const t = s?.trim() ?? '';
  return t === '' ? undefined : t;
};

const shelfTitle = (c: ShelfConfig) => (c.name ? c.name : c.id);

/** The display-list query for a remote list shelf (ShelfConfig.remoteQuery). */
export function remoteQuery(c: ShelfConfig): Record<string, string> | undefined {
  const q: Record<string, string> = { name: shelfTitle(c) };
  if (c.hideUnreleased) q.hideUnreleased = 'true';
  switch (c.type?.toLowerCase()) {
    case 'mdblist': {
      const url = clean(c.listUrl);
      if (!url) return undefined;
      return { ...q, source: 'mdblist', url };
    }
    case 'stremio': {
      const m = clean(c.addonManifestUrl);
      const t = clean(c.addonCatalogType);
      const id = clean(c.addonCatalogId);
      if (!m || !t || !id) return undefined;
      return { ...q, source: 'stremio', manifestUrl: m, catalogType: t, catalogId: id };
    }
    case 'tmdb': {
      const st = clean(c.tmdbSourceType);
      if (!st) return undefined;
      return {
        ...q,
        source: 'tmdb-list',
        sourceType: st,
        sourceId: clean(c.tmdbSourceId) ?? '',
        mediaType: clean(c.tmdbMediaType) ?? '',
        sort: clean(c.sort) ?? '',
        discoverQuery: clean(c.tmdbDiscoverQuery) ?? '',
      };
    }
    case 'trakt': {
      const a = clean(c.traktAccountId);
      const lt = clean(c.traktListType);
      if (!a || !lt) return undefined;
      const r: Record<string, string> = { ...q, source: 'trakt-list', accountId: a, listType: lt };
      const l = clean(c.traktListId);
      if (l) r.listId = l;
      return r;
    }
    case 'publicmetadb': {
      const a = clean(c.publicMetaDBAccountId);
      const l = clean(c.publicMetaDBListId);
      if (!a || !l) return undefined;
      return { ...q, source: 'publicmetadb-list', accountId: a, listId: l };
    }
    case 'simkl': {
      const a = clean(c.simklAccountId);
      const m = clean(c.simklMediaType);
      if (!a || !m) return undefined;
      const r: Record<string, string> = { ...q, source: 'simkl-list', accountId: a, mediaType: m };
      const lt = clean(c.simklListType);
      if (lt) r.listType = lt;
      return r;
    }
    case 'letterboxd': {
      const id = clean(c.letterboxdListId);
      const url = clean(c.letterboxdListUrl);
      if (!id && !url) return undefined;
      const r: Record<string, string> = { ...q, source: 'letterboxd-list' };
      if (id) r.listId = id;
      if (url) r.listUrl = url;
      return r;
    }
    default:
      return undefined;
  }
}

/** The display-list query for a built-in list shelf (BrowseList.query). */
function builtinListQuery(c: ShelfConfig): Record<string, string> | undefined {
  switch (c.id) {
    case 'trending-movies':
      return { source: 'trending', mediaType: 'movie' };
    case 'trending-tv':
      return { source: 'trending', mediaType: 'series' };
    case 'permanent-prequeue':
      return { source: 'permanent-prequeue' };
  }
  const parts = c.id.split('-');
  if (parts.length === 3 && /^\d+$/.test(parts[1]!)) {
    const mediaType = parts[2] === 'movie' ? 'movie' : 'series';
    if (parts[0] === 'genre') return { source: 'genre', genreId: parts[1]!, mediaType };
    if (parts[0] === 'decade') return { source: 'decade', decade: parts[1]!, mediaType };
  }
  return undefined;
}

export function shelfSource(c: ShelfConfig): ShelfSource | undefined {
  const title = shelfTitle(c);
  switch (c.id) {
    case 'continue-watching':
      return { kind: 'continueWatching' };
    case 'my-upcoming':
      return { kind: 'upcoming' };
    case 'watchlist':
      return { kind: 'watchlist' };
    case 'top-ten':
      return { kind: 'list', query: { source: 'top-ten' }, ranked: true, shape: 'ranked' };
    case 'my-recommended':
    case 'gemini-recs':
      return { kind: 'list', query: { source: 'personalized' }, ranked: false, shape: 'ranked' };
    case 'calendar':
      return { kind: 'calendar' };
    case 'my-recently-aired':
      return { kind: 'recentlyAired', sources: c.calendarSources };
    case 'dashboard':
      return { kind: 'dashboard' };
    case 'watch-something':
      return { kind: 'watchSomething' };
    case 'streaming-services':
      return { kind: 'services', services: resolveServices(c.streamingServices) };
    case 'popular-on-server':
    case 'recently-watched': {
      const query: Record<string, string> = { source: c.id, name: title };
      if (c.activityWindowDays && c.activityWindowDays > 0)
        query.activityWindowDays = String(c.activityWindowDays);
      if (c.minimumProfiles && c.minimumProfiles > 0)
        query.minimumProfiles = String(c.minimumProfiles);
      if (c.maxItemsPerProfile && c.maxItemsPerProfile > 0)
        query.maxItemsPerProfile = String(c.maxItemsPerProfile);
      return { kind: 'list', query, ranked: false, shape: 'ranked' };
    }
  }
  const remote = remoteQuery(c);
  if (remote) return { kind: 'list', query: remote, ranked: false, shape: 'ranked' };
  const builtin = builtinListQuery(c);
  if (builtin)
    return {
      kind: 'list',
      query: builtin,
      ranked: false,
      shape: c.id === 'permanent-prequeue' ? 'item' : 'ranked',
    };
  return undefined;
}

const NOT_FOR_CURATED_KIDS = new Set([
  'trending-movies',
  'trending-tv',
  'top-ten',
  'streaming-services',
]);

/** Kids content_list profiles get one MDBList row per allowed list (`kids-list-N`). */
function kidsListShelves(urls: string[]): ShelfConfig[] {
  return urls.map((url, index) => {
    const slug = url.split('/').filter(Boolean).pop() ?? '';
    const name = slug
      ? slug
          .split(/[-_]/)
          .filter(Boolean)
          .map((w) => w[0]!.toUpperCase() + w.slice(1))
          .join(' ')
      : `Kids List ${index + 1}`;
    return {
      id: `kids-list-${index}`,
      type: 'mdblist',
      listUrl: url,
      name,
      enabled: true,
      order: 10 + index,
    };
  });
}

/**
 * Enabled shelves in order, minus what the web can't show (watch rooms, live
 * favourites, libraries, collection hubs) and what a kids profile doesn't get.
 */
export function planShelves(
  settings: HomeSettings,
  profile?: { isKidsProfile?: boolean; kidsMode?: string; kidsAllowedLists?: string[] } | null,
): PlannedShelf[] {
  const isKids = profile?.isKidsProfile === true;
  const curated = isKids && profile?.kidsMode === 'content_list';
  let all = settings.shelves;
  if (settings.topShelfMode === 'disabled') all = all.filter((s) => s.id !== 'top-ten');
  if (settings.topShelfMode === 'shelf' && settings.topShelfSourceId) {
    const source = all.find((s) => s.id === settings.topShelfSourceId);
    if (source && source.id !== 'top-ten') {
      const order =
        all.find((s) => s.id === 'top-ten')?.order ?? Math.min(0, ...all.map((s) => s.order ?? 0));
      all = [
        { ...source, enabled: true, order },
        ...all.filter((s) => s.id !== source.id && s.id !== 'top-ten'),
      ];
    }
  }
  if (curated) {
    all = all.filter((s) => !NOT_FOR_CURATED_KIDS.has(s.id));
    all = [...all, ...kidsListShelves(profile?.kidsAllowedLists ?? [])];
  }
  all = [...all].sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
  const seen = new Set<string>();
  const out: PlannedShelf[] = [];
  for (const c of all) {
    if (!c.enabled || seen.has(c.id)) continue;
    if (isKids && c.id === 'watch-something') continue;
    const source = shelfSource(c);
    if (!source) continue;
    seen.add(c.id);
    const limit = c.limit ?? 0;
    const limited = limit > 0 && limit < settings.itemCap;
    out.push({
      id: c.id,
      title: shelfTitle(c),
      source,
      cap: limited ? limit : settings.itemCap,
      landscape: LANDSCAPE_IDS.has(c.id) || c.type === 'collection-hub',
      limited,
      seeAll: limited ? undefined : shelfListKey(c, source),
    });
  }
  return out;
}

/** Rows the hero carousel takes by default (HomeCarouselSource.defaultRows). */
export const HERO_ROWS = ['top-ten', 'trending-movies', 'trending-tv'];
export const HERO_MAX_SLIDES = 10;

export function heroShelf(plan: PlannedShelf[]): PlannedShelf | undefined {
  for (const id of HERO_ROWS) {
    const s = plan.find((p) => p.id === id);
    if (s) return s;
  }
  return plan.find((p) => p.source.kind === 'list');
}

// ---------------------------------------------------------------------------
// Streaming services (StreamingService.resolved + defaults)
// ---------------------------------------------------------------------------

const svc = (id: string, name: string, logoUrl: string, movies: string, shows: string) => ({
  id,
  name,
  logoUrl,
  lists: [
    { key: `${id}-movies`, title: 'Movies', url: movies },
    { key: `${id}-shows`, title: 'Shows', url: shows },
  ],
});
const MDB = 'https://mdblist.com/lists/';
const TMDB_LOGO = 'https://image.tmdb.org/t/p/original/';

export const DEFAULT_STREAMING_SERVICES: StreamingService[] = [
  svc(
    'netflix',
    'Netflix',
    `${TMDB_LOGO}wwemzKWzjKYJFfCeiB57q3r4Bcm.png`,
    `${MDB}snoak/netflix-top-10-movies/json`,
    `${MDB}snoak/netflix-top-10-shows/json`,
  ),
  svc(
    'disney',
    'Disney+',
    `${TMDB_LOGO}1edZOYAfoyZyZ3rklNSiUpXX30Q.png`,
    `${MDB}snoak/disney-plus-top-10-movies/json`,
    `${MDB}snoak/disney-plus-top-10-tv-shows/json`,
  ),
  svc(
    'amazon',
    'Amazon Prime',
    `${TMDB_LOGO}w7HfLNm9CWwRmAMU58udl2L7We7.png`,
    `${MDB}snoak/amazon-prime-top-10-shows/json`,
    `${MDB}snoak/amazon-prime-top-10-tv-shows/json`,
  ),
  svc(
    'appletv',
    'Apple TV+',
    'https://image.tmdb.org/t/p/w780_filter(negate,000,fff)/bngHRFi794mnMq34gfVcm9nDxN1.png',
    `${MDB}snoak/apple-tv-top-10-movies/json`,
    `${MDB}snoak/apple-tv-top-10-tv-shows/json`,
  ),
  svc(
    'paramount',
    'Paramount+',
    `${TMDB_LOGO}fi83B1oztoS47xxcemFdPMhIzK.png`,
    `${MDB}snoak/paramount-plus-top-10-movies/json`,
    `${MDB}snoak/paramount-plus-top-10-tv-shows/json`,
  ),
  svc(
    'hbomax',
    'HBO Max',
    'https://cdn.prod.website-files.com/6911e6b4bcc8af690d496a26/6911e6b4bcc8af690d496fed_nav-hbo-max.png',
    `${MDB}snoak/hbo-top-10-movies-2/json`,
    `${MDB}snoak/hbo-top-10-tv-shows/json`,
  ),
  svc(
    'hulu',
    'Hulu',
    `${TMDB_LOGO}pqUTCleNUiTLAVlelGxUgWn1ELh.png`,
    `${MDB}azodath/top-hulu-movies/json`,
    `${MDB}azodath/top-hulu-shows/json`,
  ),
  svc(
    'crunchyroll',
    'Crunchyroll',
    `${TMDB_LOGO}qqyXcZlJQKlRmAD1TCKV7mGLQlt.png`,
    `${MDB}snoak/trending-anime-movies/json`,
    `${MDB}snoak/trending-anime-shows/json`,
  ),
];

const RETIRED: Record<string, string> = {
  'https://mdblist.com/lists/snoak/top-hulu-movies': `${MDB}azodath/top-hulu-movies/json`,
  'https://mdblist.com/lists/snoak/top-tv-shows-hulu': `${MDB}azodath/top-hulu-shows/json`,
};

export function resolveServices(configured?: StreamingService[]): StreamingService[] {
  if (!configured || configured.length === 0) return DEFAULT_STREAMING_SERVICES;
  return configured
    .map((s, index) => ({ s, index }))
    .filter(({ s }) => s.enabled !== false && s.name && s.logoUrl)
    .map(({ s, index }) => {
      const seen = new Set<string>();
      const lists = (s.lists ?? []).flatMap((l) => {
        const trimmed = l.url.trim();
        const norm = trimmed.replace(/\/+$/, '').replace(/\/json$/, '');
        const url = RETIRED[norm] ?? trimmed;
        if (!url || seen.has(url)) return [];
        seen.add(url);
        return [{ ...l, url }];
      });
      return { ...s, enabled: true, order: s.order ?? index, lists };
    })
    .filter((s) => s.lists.length > 0)
    .sort((a, b) => (a.order ?? 0) - (b.order ?? 0));
}

// ---------------------------------------------------------------------------
// Cards (HomeCard)
// ---------------------------------------------------------------------------

/** Everything a Home card or the hero needs, whatever the source shape. */
export interface HomeCardModel {
  id: string;
  title: string;
  subtitle?: string;
  overview?: string;
  posterUrl?: string;
  /** Poster without title text, when the backend found one (hero on phones). */
  textlessPosterUrl?: string;
  /** 16:9 art for landscape cards (episode still or backdrop). */
  landscapeUrl?: string;
  backdropUrl?: string;
  logoUrl?: string;
  progress?: number;
  badge?: string;
  /** For detailsLink(); undefined = card isn't linkable. */
  link?: Pick<Title, 'id' | 'name' | 'mediaType' | 'year' | 'tmdbId' | 'tvdbId' | 'imdbId'>;
  season?: number;
  episode?: number;
}

const numId = (v?: string) => {
  const n = v ? Number(v) : NaN;
  return Number.isFinite(n) && n > 0 ? n : undefined;
};

function linkFromIds(
  id: string,
  name: string,
  mediaType: string,
  year: number | undefined,
  ids: Record<string, string> | undefined,
): HomeCardModel['link'] {
  return {
    id,
    name,
    mediaType: mediaType === 'movie' ? 'movie' : 'series',
    year,
    tmdbId: numId(ids?.tmdb),
    tvdbId: numId(ids?.tvdb),
    imdbId: ids?.imdb || undefined,
  };
}

export const episodeCode = (s: number, e: number) =>
  `S${String(s).padStart(2, '0')}E${String(e).padStart(2, '0')}`;

/** Next-episode air badge ("Tomorrow", "Fri", "Dec 3") when it hasn't aired yet. */
export function airBadge(ep: Pick<EpisodeRef, 'airDate' | 'airDateTimeUTC'>, now = new Date()) {
  const when = airsAt(ep);
  if (!when || when <= now) return undefined;
  const day = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();
  const diff = Math.round((day(when) - day(now)) / 86_400_000);
  if (diff === 0) return 'Today';
  if (diff === 1) return 'Tomorrow';
  if (diff < 7) return when.toLocaleDateString(undefined, { weekday: 'short' });
  return when.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

export function airsAt(ep: Pick<EpisodeRef, 'airDate' | 'airDateTimeUTC'>): Date | undefined {
  if (ep.airDateTimeUTC) {
    const d = new Date(ep.airDateTimeUTC);
    if (!Number.isNaN(d.getTime())) return d;
  }
  if (ep.airDate && /^\d{4}-\d{2}-\d{2}/.test(ep.airDate)) {
    const [y, m, d] = ep.airDate.slice(0, 10).split('-').map(Number);
    return new Date(y!, m! - 1, d!);
  }
  return undefined;
}

export const isUpcoming = (item: ContinueWatchingItem, now = new Date()) => {
  if (isMovieId(item.seriesId) || !item.nextEpisode) return false;
  const at = airsAt(item.nextEpisode);
  return !!at && at > now;
};

export function continueWatchingCard(item: ContinueWatchingItem): HomeCardModel {
  const movie = isMovieId(item.seriesId);
  const ep = item.nextEpisode;
  let progress: number;
  if (item.resume)
    progress = item.resume.eligible ? Math.min(Math.max(item.resume.percent / 100, 0), 1) : 0;
  else
    progress = Math.min(
      Math.max((movie ? item.percentWatched : item.resumePercent) ?? 0, 0) / 100,
      1,
    );
  return {
    id: item.seriesId,
    title: item.seriesTitle,
    subtitle: movie
      ? item.year
        ? String(item.year)
        : 'Movie'
      : ep
        ? `${episodeCode(ep.seasonNumber, ep.episodeNumber)}${ep.title ? ` · ${ep.title}` : ''}`
        : 'Series',
    overview: ep?.overview ?? item.overview,
    posterUrl: item.textPosterUrl ?? item.posterUrl,
    textlessPosterUrl: item.textPosterUrl ? item.posterUrl : undefined,
    landscapeUrl: ep?.image?.url ?? item.backdropUrl,
    backdropUrl: item.backdropUrl,
    logoUrl: item.logoUrl,
    progress,
    badge: !movie && ep ? airBadge(ep) : undefined,
    link: linkFromIds(
      item.seriesId,
      item.seriesTitle,
      movie ? 'movie' : 'series',
      item.year,
      item.externalIds,
    ),
    season: !movie ? ep?.seasonNumber : undefined,
    episode: !movie ? ep?.episodeNumber : undefined,
  };
}

const titleSubtitle = (t: Title) =>
  t.cardSubtitle ??
  [t.year, t.mediaType === 'movie' ? 'Movie' : 'Series'].filter(Boolean).join(' · ');

export function titleCard(t: Title, rank?: number): HomeCardModel {
  return {
    id: t.id,
    title: t.name,
    subtitle: titleSubtitle(t),
    overview: t.overview,
    posterUrl: t.textPoster?.url ?? t.poster?.url,
    textlessPosterUrl: t.textPoster ? t.poster?.url : undefined,
    landscapeUrl: t.cardImage?.url ?? t.backdrop?.url,
    backdropUrl: t.backdrop?.url,
    logoUrl: t.logo?.url,
    badge: rank ? `#${rank}` : undefined,
    link: t,
  };
}

export function watchlistCard(item: WatchlistItem): HomeCardModel {
  return {
    id: item.id,
    title: item.name,
    subtitle: [item.year, item.mediaType === 'movie' ? 'Movie' : 'Series']
      .filter(Boolean)
      .join(' · '),
    overview: item.overview,
    posterUrl: item.textPosterUrl ?? item.posterUrl,
    textlessPosterUrl: item.textPosterUrl ? item.posterUrl : undefined,
    landscapeUrl: item.backdropUrl,
    backdropUrl: item.backdropUrl,
    logoUrl: item.logoUrl,
    badge: item.unwatchedCount && item.unwatchedCount > 0 ? String(item.unwatchedCount) : undefined,
    link: linkFromIds(item.id, item.name, item.mediaType, item.year, item.externalIds),
  };
}

/** The details id, built as native CalendarItem.titleID does. */
export function calendarTitleId(item: CalendarItem): string {
  const ids = item.externalIds ?? {};
  if (item.mediaType === 'movie') {
    if (ids.tmdb) return `tmdb:movie:${ids.tmdb}`;
    if (ids.imdb) return ids.imdb;
    if (ids.tvdb) return `tvdb:movie:${ids.tvdb}`;
  } else {
    if (ids.tmdb) return `tmdb:tv:${ids.tmdb}`;
    if (ids.tvdb) return `tvdb:${ids.tvdb}`;
    if (ids.imdb) return ids.imdb;
  }
  return item.title;
}

export function calendarAirs(item: CalendarItem): Date | undefined {
  const base = airsAt({ airDate: item.airDate });
  if (!base) return undefined;
  if (item.airTime && /^\d{1,2}:\d{2}/.test(item.airTime)) {
    const [h, m] = item.airTime.split(':').map(Number);
    base.setHours(h!, m!);
  }
  return base;
}

export function calendarCard(item: CalendarItem, now = new Date()): HomeCardModel {
  const id = calendarTitleId(item);
  const at = calendarAirs(item);
  const movie = item.mediaType === 'movie';
  const code =
    !movie && item.seasonNumber != null && item.episodeNumber != null
      ? episodeCode(item.seasonNumber, item.episodeNumber)
      : undefined;
  return {
    id: `${id}:${item.seasonNumber ?? ''}:${item.episodeNumber ?? ''}:${item.airDate}`,
    title: item.title,
    subtitle: [code, item.episodeTitle ?? (movie ? item.releaseType : undefined)]
      .filter(Boolean)
      .join(' · '),
    overview: item.episodeOverview ?? item.overview,
    posterUrl: item.textPosterUrl ?? item.posterUrl,
    textlessPosterUrl: item.textPosterUrl ? item.posterUrl : undefined,
    landscapeUrl: item.backdropUrl,
    backdropUrl: item.backdropUrl,
    logoUrl: item.logo?.url,
    badge: at ? (airBadge({ airDateTimeUTC: at.toISOString() }, now) ?? undefined) : undefined,
    link: linkFromIds(id, item.title, item.mediaType, item.year, item.externalIds),
    season: code ? item.seasonNumber : undefined,
    episode: code ? item.episodeNumber : undefined,
  };
}

const SOURCE_KEYS: Record<string, keyof CalendarSources> = {
  watchlist: 'watchlist',
  history: 'history',
  trending: 'trending',
  'top-trending': 'topTrending',
  mdblist: 'mdblists',
};

/** Calendar source filter for My Recently Aired (watchlist only when unset). */
export function matchesCalendarSources(item: CalendarItem, sources?: CalendarSources) {
  const s = sources ?? { watchlist: true };
  const key = SOURCE_KEYS[item.source ?? 'watchlist'];
  return key ? s[key] === true : false;
}

/** One card per title, earliest release first, from today on (Coming Up). */
export function comingUp(items: CalendarItem[], limit: number, now = new Date()): HomeCardModel[] {
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate());
  const upcoming = items
    .map((i) => ({ i, at: calendarAirs(i) }))
    .filter((x) => x.at && x.at >= today)
    .sort((a, b) => a.at!.getTime() - b.at!.getTime());
  const seen = new Set<string>();
  const out: HomeCardModel[] = [];
  for (const { i } of upcoming) {
    const id = calendarTitleId(i);
    if (seen.has(id)) continue;
    seen.add(id);
    out.push(calendarCard(i, now));
    if (out.length >= limit) break;
  }
  return out;
}

/** Aired episodes from the chosen sources, newest first, one per series. */
export function recentlyAired(
  items: CalendarItem[],
  sources: CalendarSources | undefined,
  limit: number,
  now = new Date(),
): HomeCardModel[] {
  const aired = items
    .filter((i) => i.mediaType !== 'movie' && matchesCalendarSources(i, sources))
    .map((i) => ({ i, at: calendarAirs(i) }))
    .filter((x) => x.at && x.at <= now)
    .sort((a, b) => b.at!.getTime() - a.at!.getTime());
  const seen = new Set<string>();
  const out: HomeCardModel[] = [];
  for (const { i } of aired) {
    const id = calendarTitleId(i);
    if (seen.has(id)) continue;
    seen.add(id);
    out.push(calendarCard(i, now));
    if (out.length >= limit) break;
  }
  return out;
}

export function dashboardCard(s: DashboardStream): HomeCardModel | undefined {
  if (!s.title) return undefined;
  const episode = s.mediaType === 'episode';
  const ids = s.externalIds ?? {};
  const id =
    (episode
      ? ids.tmdb
        ? `tmdb:tv:${ids.tmdb}`
        : ids.tvdb
          ? `tvdb:${ids.tvdb}`
          : ids.imdb
      : ids.tmdb
        ? `tmdb:movie:${ids.tmdb}`
        : ids.imdb) ?? undefined;
  const code =
    episode && s.seasonNumber != null && s.episodeNumber != null
      ? episodeCode(s.seasonNumber, s.episodeNumber)
      : undefined;
  return {
    id: s.id,
    title: s.title,
    subtitle: [s.profileNames?.join(', '), code, s.isPaused ? 'Paused' : undefined]
      .filter(Boolean)
      .join(' · '),
    posterUrl: s.posterUrl,
    landscapeUrl: s.backdropUrl,
    backdropUrl: s.backdropUrl,
    progress: s.percentWatched ? s.percentWatched / 100 : undefined,
    link: id ? linkFromIds(id, s.title, episode ? 'series' : 'movie', s.year, ids) : undefined,
    season: code ? s.seasonNumber : undefined,
    episode: code ? s.episodeNumber : undefined,
  };
}

/** Query for a list shelf, with Home's filters (PlannedShelf.load). */
export function listShelfQuery(
  shelf: PlannedShelf,
  settings: Pick<HomeSettings, 'hideWatched'> & Partial<Pick<HomeSettings, 'view'>>,
): Record<string, string> {
  if (shelf.source.kind !== 'list') return {};
  const q: Record<string, string> = { name: shelf.title, ...shelf.source.query };
  if (settings.hideWatched) q.hideWatched = 'true';
  // The server filters the list to the Home view's media type.
  if (settings.view && settings.view !== 'all') q.homeView = settings.view;
  q.limit = String(shelf.cap);
  q.offset = '0';
  return q;
}
