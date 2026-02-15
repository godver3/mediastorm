import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useContinueWatching } from '@/components/ContinueWatchingContext';
import { useStartupData } from '@/components/StartupDataContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { markNavStart } from '@/utils/nav-timing';
import { FloatingHero } from '@/components/FloatingHero';
import MediaGrid from '@/components/MediaGrid';
import { getMovieReleaseIcon, type ReleaseIconInfo } from '@/components/MediaItem';
import { useMovieReleases } from '@/components/MovieReleasesContext';
import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { useMenuContext } from '@/components/MenuContext';
import { useToast } from '@/components/ToastContext';
import { TvModal } from '@/components/TvModal';
import FocusablePressable from '@/components/FocusablePressable';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useWatchlist } from '@/components/WatchlistContext';
import { useWatchStatus } from '@/components/WatchStatusContext';
import { useTrendingMovies, useTrendingTVShows } from '@/hooks/useApi';
import { apiService, type Rating, ReleaseWindow, SeriesWatchState, Title, TrendingItem, type WatchlistItem, type WatchStatusItem } from '@/services/api';
import { APP_VERSION } from '@/version';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import { TVFocusGuard, useTVFocusBoundary } from '@/components/tv-focus';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV, isTablet, getTVScaleMultiplier } from '@/theme/tokens/tvScale';
import { useFocusEffect, useIsFocused } from '@react-navigation/native';
import { LinearGradient } from 'expo-linear-gradient';
import { Stack, useRouter } from 'expo-router';
import React, { startTransition, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { LayoutChangeEvent, View as RNView } from 'react-native';
import { Image } from '@/components/Image';
import {
  findNodeHandle,
  FlatList,
  Image as RNImage,
  Modal,
  NativeScrollEvent,
  NativeSyntheticEvent,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { useMemoryMonitor } from '@/hooks/useMemoryMonitor';
import Animated, {
  useAnimatedRef,
  useAnimatedStyle,
  scrollTo as reanimatedScrollTo,
  useSharedValue,
  useAnimatedReaction,
  withTiming,
} from 'react-native-reanimated';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { isEpisodeComingSoon, isEpisodeUnreleased } from '@/app/details/utils';

type CardData = {
  id: string | number;
  title: string;
  description: string;
  headerImage: string;
  cardImage: string;
  mediaType?: string;
  posterUrl?: string;
  backdropUrl?: string;
  tmdbId?: number;
  imdbId?: string;
  tvdbId?: number;
  year?: number | string;
  percentWatched?: number;
  seriesOverview?: string; // For series, store the show overview separately from episode description
  collagePosters?: string[]; // For explore cards: array of 4 poster URLs to display in a grid
  theatricalRelease?: ReleaseWindow; // Movie theatrical release status
  homeRelease?: ReleaseWindow; // Movie home release status
  releaseIcon?: ReleaseIconInfo; // Pre-computed release icon to avoid computation at render time
  watchState?: 'none' | 'partial' | 'complete'; // Watch state for series/movies
  unwatchedCount?: number; // Number of unwatched episodes for series
  isUnreleased?: boolean; // Next episode hasn't aired yet (continue watching)
};

type HeroContent = {
  title: string;
  description: string;
  headerImage: string;
};

const HERO_PLACEHOLDER: HeroContent = {
  title: 'Loading…',
  description: 'Please wait while we load trending content.',
  headerImage: 'https://via.placeholder.com/1920x1080/333/fff?text=Loading...',
};

const EXPLORE_CARD_ID_PREFIX = '__explore__';
const MAX_SHELF_ITEMS_ON_HOME = 20;

// Simple hash function for seeded randomness
function simpleHash(str: string): number {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    const char = str.charCodeAt(i);
    hash = ((hash << 5) - hash) + char;
    hash = hash & hash; // Convert to 32-bit integer
  }
  return Math.abs(hash);
}

// Seeded pseudo-random number generator (mulberry32)
function seededRandom(seed: number): () => number {
  return function() {
    let t = seed += 0x6D2B79F5;
    t = Math.imul(t ^ t >>> 15, t | 1);
    t ^= t + Math.imul(t ^ t >>> 7, t | 61);
    return ((t ^ t >>> 14) >>> 0) / 4294967296;
  };
}

// Helper to pick N random posters from displayed items (deterministic based on content)
function pickRandomPosters<T>(
  items: T[],
  getPoster: (item: T) => string | undefined,
  count: number = 4,
  shelfId: string = ''
): string[] {
  const displayedItems = items.slice(0, MAX_SHELF_ITEMS_ON_HOME);
  const itemsWithPosters = displayedItems.filter((item) => getPoster(item));
  if (itemsWithPosters.length <= count) {
    return itemsWithPosters.map((item) => getPoster(item)!);
  }
  // Create a seed based on shelfId and the posters themselves for stability
  const posterUrls = itemsWithPosters.map((item) => getPoster(item)!);
  const seedString = shelfId + posterUrls.slice(0, 10).join('');
  const seed = simpleHash(seedString);
  const random = seededRandom(seed);

  // Fisher-Yates shuffle with seeded random for deterministic selection
  const shuffled = [...itemsWithPosters];
  for (let i = shuffled.length - 1; i > 0; i--) {
    const j = Math.floor(random() * (i + 1));
    [shuffled[i], shuffled[j]] = [shuffled[j], shuffled[i]];
  }
  return shuffled.slice(0, count).map((item) => getPoster(item)!);
}

// Helper to create explore card with 4-poster collage
// Uses random posters from displayed items on the shelf
// Optional overrideRemainingCount can be passed when we know the total from API
function createExploreCard(shelfId: string, allCards: CardData[], overrideRemainingCount?: number, imageGetter?: (card: CardData) => string | undefined): CardData {
  const remainingCount = overrideRemainingCount ?? allCards.length - MAX_SHELF_ITEMS_ON_HOME;
  const totalCount =
    overrideRemainingCount !== undefined ? overrideRemainingCount + MAX_SHELF_ITEMS_ON_HOME : allCards.length;

  // Pick 4 posters from displayed items (deterministic based on shelfId and content)
  const getImage = imageGetter ?? ((card: CardData) => card.cardImage);
  const collagePosters = pickRandomPosters(allCards, getImage, 4, shelfId);

  return {
    id: `${EXPLORE_CARD_ID_PREFIX}${shelfId}`,
    title: 'Explore',
    year: `+${remainingCount} More`,
    description: `View all ${totalCount} items`,
    headerImage: collagePosters[0] || '',
    cardImage: collagePosters[0] || '',
    collagePosters,
    mediaType: 'explore',
  };
}

const getConnectionStatusMessage = (
  retryCountdown: number | null,
  isReachable: boolean,
  isLoading: boolean,
  hasContent: boolean,
): string => {
  if (isLoading) {
    return 'Connecting to backend…';
  }
  if (!isReachable && retryCountdown !== null) {
    return `Unable to reach backend. Retrying in ${retryCountdown}s…`;
  }
  if (!isReachable) {
    return 'Unable to reach backend. Check your settings.';
  }
  if (isReachable && !hasContent) {
    return 'Connected. Loading content…';
  }
  return 'Please wait while we load trending content.';
};

const MIN_CONTINUE_WATCHING_PERCENT = 5;
const MAX_HERO_ITEMS = 10;

// Memory optimization: cap cache sizes to prevent unbounded growth
const MAX_SERIES_OVERVIEWS_CACHE = 200; // Keep overviews for ~200 series
const MAX_WATCHLIST_YEARS_CACHE = 200; // Keep years for ~200 items
const MAX_HERO_KEYS_CACHE = 100; // Track up to 100 hero item keys
const RECENT_SERIES_WATCH_WINDOW_MS = 365 * 24 * 60 * 60 * 1000;

const AUTH_WARNING_MESSAGE = 'Backend URL/PIN authorization failed. Verify your settings.';

// Helper to cap Map size by removing oldest entries (FIFO eviction)
function capMapSize<K, V>(map: Map<K, V>, maxSize: number): Map<K, V> {
  if (map.size <= maxSize) {
    return map;
  }
  const entries = Array.from(map.entries());
  const trimmed = entries.slice(entries.length - maxSize);
  return new Map(trimmed);
}

// Helper to cap Set size by removing oldest entries (FIFO eviction)
function capSetSize<T>(set: Set<T>, maxSize: number): void {
  if (set.size <= maxSize) {
    return;
  }
  const toRemove = set.size - maxSize;
  let removed = 0;
  for (const item of set) {
    if (removed >= toRemove) break;
    set.delete(item);
    removed++;
  }
}

function isAuthErrorMessage(rawMessage: string | null | undefined): boolean {
  if (!rawMessage) {
    return false;
  }

  const trimmed = rawMessage.trim();
  if (!trimmed) {
    return false;
  }

  if (/invalid pin/i.test(trimmed)) {
    return true;
  }

  if (/api request failed/i.test(trimmed) && /\b401\b/.test(trimmed)) {
    return true;
  }

  return false;
}

function buildWarningMessage(context: string, rawMessage: string | null | undefined): string | null {
  if (!rawMessage) {
    return null;
  }

  const trimmed = rawMessage.trim();
  if (!trimmed) {
    return null;
  }

  if (/^API request failed:\s*/i.test(trimmed)) {
    const simplified = trimmed.replace(/^API request failed:\s*/i, '').trim();
    if (!simplified) {
      return `${context}: Request failed.`;
    }
    return `${context}: ${simplified}`;
  }

  return `${context}: ${trimmed}`;
}

const isAndroidTV = Platform.isTV && Platform.OS === 'android';

// Enrich titles with watch status data for the watchState badge
// Returns isWatched for movies, watchState for series (none/partial/complete)
function enrichWithWatchStatus<T extends { id: string; mediaType: string; percentWatched?: number }>(
  titles: T[],
  isWatched: (mediaType: string, id: string) => boolean,
  watchStatusItems: WatchStatusItem[],
  continueWatchingItems?: SeriesWatchState[],
): (T & { isWatched?: boolean; watchState?: 'none' | 'partial' | 'complete' })[] {
  return titles.map((title) => {
    if (title.mediaType === 'movie') {
      const movieWatched = isWatched('movie', title.id);
      const percentWatched = title.percentWatched ?? 0;
      // Determine watch state: complete if marked watched or >=90%, partial if has progress
      const watchState: 'none' | 'partial' | 'complete' =
        movieWatched || percentWatched >= 90 ? 'complete' : percentWatched > 0 ? 'partial' : 'none';
      return {
        ...title,
        isWatched: movieWatched,
        watchState,
      };
    }
    if (title.mediaType === 'series' || title.mediaType === 'tv') {
      // Check if series itself is marked watched
      const seriesWatched = isWatched('series', title.id);

      // Check for auto-complete using backend-provided episode counts
      const cwItem = continueWatchingItems?.find((cw) => cw.seriesId === title.id);
      const totalEpisodes = cwItem?.totalEpisodeCount ?? 0;
      const watchedEpisodes = cwItem?.watchedEpisodeCount ?? 0;
      const allEpisodesWatched = totalEpisodes > 0 && watchedEpisodes >= totalEpisodes;

      // Check if any non-special episodes (season > 0) of this series are fully watched
      const hasWatchedEpisodes = watchStatusItems.some(
        (item) =>
          item.mediaType === 'episode' &&
          item.seriesId === title.id &&
          item.watched &&
          (item.seasonNumber ?? 0) > 0, // Exclude season 0 (specials)
      );

      // Check if series has partial progress from continue watching (episode in progress)
      const hasPartialProgress =
        cwItem &&
        ((cwItem.percentWatched ?? 0) > 0 || // Has overall progress
          (cwItem.resumePercent ?? 0) > 0 || // Has resume position
          watchedEpisodes > 0 || // Has watched some episodes (from backend)
          (cwItem.watchedEpisodes && Object.keys(cwItem.watchedEpisodes).length > 0)); // Has any watched episodes in map

      // Determine watch state:
      // - complete: series marked watched OR all released episodes watched
      // - partial: has fully watched episodes OR has partial episode progress
      // - none: no watch activity
      const watchState: 'none' | 'partial' | 'complete' =
        seriesWatched || allEpisodesWatched
          ? 'complete'
          : hasWatchedEpisodes || hasPartialProgress
            ? 'partial'
            : 'none';

      // Calculate unwatched count for badge display
      const unwatchedCount = totalEpisodes > 0 ? totalEpisodes - watchedEpisodes : undefined;

      return {
        ...title,
        isWatched: seriesWatched || allEpisodesWatched,
        watchState,
        unwatchedCount,
      };
    }
    return title;
  });
}

// Helper to enrich TrendingItem[] with watch status (enriches the .title property of each item)
function enrichTrendingItemsWithWatchStatus(
  items: TrendingItem[],
  isWatched: (mediaType: string, id: string) => boolean,
  watchStatusItems: WatchStatusItem[],
  continueWatchingItems?: SeriesWatchState[],
): TrendingItem[] {
  const titles = items.map((item) => item.title);
  const enrichedTitles = enrichWithWatchStatus(titles, isWatched, watchStatusItems, continueWatchingItems);
  return items.map((item, index) => ({
    ...item,
    title: enrichedTitles[index] as Title,
  }));
}

// Delay (ms) before firing secondary data fetches (overviews, years, releases, custom shelves).
// On Android TV, animations are disabled so InteractionManager.runAfterInteractions() fires
// instantly — it never actually defers. A real timeout lets the home screen paint and become
// responsive before the batch metadata calls (which take 10-12s) start competing for the JS thread.
const SECONDARY_FETCH_DELAY_MS = Platform.isTV ? 2000 : 0;

// Debug logging for index page render/load analysis
const DEBUG_INDEX_RENDERS = __DEV__ && false; // Set to true for render debugging
const DEBUG_PERF = __DEV__ && false; // Performance profiling
const DEBUG_VNAV = false; // Vertical navigation timing

// === STARTUP DEBUG KILL SWITCHES ===
// Toggle these to isolate what's choking Fire Stick startup.
// Set to true to DISABLE each suspect. Check logs after each change.
const KILL_SERIES_OVERVIEWS = false;
const KILL_WATCHLIST_YEARS = false;
const KILL_MOVIE_RELEASES = false;
const KILL_CUSTOM_SHELVES = false;
const KILL_FOCUS_REFRESH = false;
const KILL_HERO_ENRICHMENT = false;
const DEBUG_MAX_CARDS_PER_SHELF = 0;
const KILL_ALL_SHELVES = false;
// === END KILL SWITCHES ===

let indexRenderCount = 0;
let renderStartTime = 0;

// Enriched hero data fetched from details API for TV hero display
type EnrichedHeroData = {
  logoUrl?: string;
  isLogoDark?: boolean;
  ratings?: Rating[];
  genres?: string[];
  certification?: string;
  backdropUrl?: string; // Alternate backdrop from details API
};

// Rating source display order (lower = first)
const RATING_ORDER: Record<string, number> = {
  imdb: 1,
  tmdb: 2,
  trakt: 3,
  tomatoes: 4,
  audience: 5,
  metacritic: 6,
  letterboxd: 7,
};

const getRatingConfig = (
  source: string,
  baseUrl: string,
  value?: number,
  max?: number,
): { color: string; iconUrl: string | null } => {
  const iconBase = `${baseUrl}/static/rating_icons`;
  switch (source) {
    case 'imdb':
      return { color: '#F5C518', iconUrl: `${iconBase}/imdb.png` };
    case 'tmdb':
      return { color: '#01D277', iconUrl: `${iconBase}/tmdb.png` };
    case 'trakt':
      return { color: '#ED1C24', iconUrl: `${iconBase}/trakt.png` };
    case 'letterboxd':
      return { color: '#00E054', iconUrl: `${iconBase}/letterboxd.png` };
    case 'tomatoes': {
      const percent = max === 100 ? value : value !== undefined ? value * 10 : 60;
      const isFresh = (percent ?? 60) >= 60;
      return {
        color: isFresh ? '#FA320A' : '#6B8E23',
        iconUrl: `${iconBase}/${isFresh ? 'rt_critics' : 'rt_rotten'}.png`,
      };
    }
    case 'audience':
      return { color: '#FA320A', iconUrl: `${iconBase}/rt_audience.png` };
    case 'metacritic':
      return { color: '#FFCC34', iconUrl: `${iconBase}/metacritic.png` };
    default:
      return { color: '#888888', iconUrl: null };
  }
};

const formatRating = (rating: Rating): string => {
  switch (rating.source) {
    case 'imdb':
    case 'letterboxd':
      return rating.value.toFixed(1);
    case 'tmdb':
    case 'trakt':
    case 'tomatoes':
    case 'audience':
    case 'metacritic':
      return `${Math.round(rating.value)}%`;
    default:
      return rating.max === 10 ? rating.value.toFixed(1) : `${Math.round(rating.value)}%`;
  }
};

const MAX_ENRICHED_CACHE = 50;

function IndexScreen() {
  // Memory monitoring - logs every 60s on Android TV
  useMemoryMonitor('IndexPage', 60000, DEBUG_PERF && isAndroidTV);

  // Startup timing — log mount time and kill switch status
  const mountTimeRef = useRef(Date.now());
  useEffect(() => {
    const kills = [
      KILL_SERIES_OVERVIEWS && 'SERIES_OVERVIEWS',
      KILL_WATCHLIST_YEARS && 'WATCHLIST_YEARS',
      KILL_MOVIE_RELEASES && 'MOVIE_RELEASES',
      KILL_CUSTOM_SHELVES && 'CUSTOM_SHELVES',
      KILL_FOCUS_REFRESH && 'FOCUS_REFRESH',
      KILL_HERO_ENRICHMENT && 'HERO_ENRICHMENT',
      KILL_ALL_SHELVES && 'ALL_SHELVES',
    ].filter(Boolean);
    console.log(`[STARTUP] IndexScreen mounted, kill switches: ${kills.length > 0 ? kills.join(', ') : 'NONE'}`);
  }, []);

  // Track render count and timing
  const renderCountRef = useRef(0);
  renderCountRef.current += 1;
  // Log every render with timestamp on TV to find the 9s gap
  if (Platform.isTV && renderCountRef.current <= 30) {
    console.log(`[RENDER] #${renderCountRef.current} at ${new Date().toISOString()}`);
  }

  if (DEBUG_PERF) {
    renderStartTime = performance.now();
  }

  if (DEBUG_INDEX_RENDERS) {
    indexRenderCount += 1;
    console.log(`[IndexPage] Render #${indexRenderCount} (component instance: ${renderCountRef.current})`);
  }

  // Log render completion time
  React.useLayoutEffect(() => {
    if (DEBUG_PERF && renderStartTime) {
      const elapsed = performance.now() - renderStartTime;
      console.log(`[IndexPage:Perf] Render #${indexRenderCount} took ${elapsed.toFixed(1)}ms`);
    }
  });

  const { height: screenHeight, width: screenWidth } = useTVDimensions();
  const theme = useTheme();
  const router = useRouter();
  const focused = useIsFocused();
  const { isOpen: isMenuOpen, openMenu, closeMenu, setFirstContentFocusableTag } = useMenuContext();

  // Backend settings - must come before trending hooks so we can extract hideUnreleased settings
  const {
    loading: settingsLoading,
    error: settingsError,
    settings,
    userSettings,
    lastLoadedAt: settingsLastLoadedAt,
    isBackendReachable,
    retryCountdown,
  } = useBackendSettings();
  const { startupData, ready: startupReady } = useStartupData();

  // Extract hideUnreleased settings for trending shelves from settings
  const trendingMoviesHideUnreleased = useMemo(() => {
    const allShelves = userSettings?.homeShelves?.shelves ?? settings?.homeShelves?.shelves ?? [];
    const shelf = allShelves.find((s) => s.id === 'trending-movies');
    return shelf?.hideUnreleased ?? false;
  }, [userSettings?.homeShelves?.shelves, settings?.homeShelves?.shelves]);

  const trendingTVHideUnreleased = useMemo(() => {
    const allShelves = userSettings?.homeShelves?.shelves ?? settings?.homeShelves?.shelves ?? [];
    const shelf = allShelves.find((s) => s.id === 'trending-tv');
    return shelf?.hideUnreleased ?? false;
  }, [userSettings?.homeShelves?.shelves, settings?.homeShelves?.shelves]);

  // Extract universal hideWatched setting from display settings
  const hideWatched = useMemo(() => {
    return settings?.display?.hideWatched ?? false;
  }, [settings?.display?.hideWatched]);

  const {
    items: watchlistItems,
    loading: watchlistLoading,
    error: watchlistError,
    refresh: refreshWatchlist,
  } = useWatchlist();
  const {
    items: continueWatchingItems,
    loading: continueWatchingLoading,
    error: continueWatchingError,
    refresh: refreshContinueWatching,
    hideFromContinueWatching,
  } = useContinueWatching();
  const { refresh: refreshUserProfiles, activeUserId, activeUser, pendingPinUserId, profileSelectorVisible } =
    useUserProfiles();
  const {
    data: trendingMovies,
    error: trendingMoviesError,
    refetch: refetchTrendingMovies,
  } = useTrendingMovies(activeUserId ?? undefined, true, trendingMoviesHideUnreleased, hideWatched);
  const {
    data: trendingTVShows,
    error: trendingTVShowsError,
    refetch: refetchTrendingTVShows,
  } = useTrendingTVShows(activeUserId ?? undefined, true, trendingTVHideUnreleased, hideWatched);
  const { isWatched, items: watchStatusItems } = useWatchStatus();
  const safeAreaInsets = useSafeAreaInsets();
  // Use Reanimated's animated ref for UI thread scrolling
  const scrollViewRef = useAnimatedRef<Animated.ScrollView>();
  const scrollMetricsRef = React.useRef({ offset: 0, viewportHeight: screenHeight });
  const shelfRefs = React.useRef<{ [key: string]: RNView | null }>({});
  // Cache shelf positions to avoid measureLayout on every shelf change (expensive on Android)
  const shelfPositionsRef = React.useRef<{ [key: string]: number }>({});
  // Store FlatList refs for each shelf for programmatic horizontal scrolling
  const shelfFlatListRefs = React.useRef<{ [key: string]: FlatList | null }>({});
  // Shared value for animated vertical scrolling on TV (allows custom duration)
  const shelfScrollTargetY = useSharedValue(-1); // -1 = no pending scroll

  // Drive vertical scrolling from shared value (TV only, runs on UI thread for smoothness)
  useAnimatedReaction(
    () => shelfScrollTargetY.value,
    (targetY, prevTargetY) => {
      'worklet';
      if (targetY >= 0 && targetY !== prevTargetY) {
        reanimatedScrollTo(scrollViewRef, 0, targetY, true);
      }
    },
    [scrollViewRef],
  );

  const pageRef = React.useRef<RNView | null>(null);
  // Track initial load to skip scroll animations on first render
  const isInitialLoadRef = React.useRef(true);
  // Track focus transitions to detect navigation returns vs initial load vs dep-change re-runs.
  // wasFocusedRef tracks the previous value of `focused` so we only detect actual false→true
  // transitions as "return from navigation," not effect re-runs from dep changes while focused.
  const hasBeenFocusedRef = React.useRef(false);
  const wasFocusedRef = React.useRef(false);
  const { showToast } = useToast();
  const hasAuthFailureRef = React.useRef(false);
  const previousSettingsLoadedAtRef = React.useRef<number | null>(null);

  // Custom list data storage (for MDBList shelves)
  const [customListData, setCustomListData] = useState<Record<string, TrendingItem[]>>({});
  const [customListTotals, setCustomListTotals] = useState<Record<string, number>>({});
  const [customListUnfilteredTotals, setCustomListUnfilteredTotals] = useState<Record<string, number>>({});
  const [customListLoading, setCustomListLoading] = useState<Record<string, boolean>>({});
  const fetchedListUrlsRef = React.useRef<Set<string>>(new Set());

  // Debug: Log data source changes
  useEffect(() => {
    if (!DEBUG_INDEX_RENDERS) return;
    console.log(
      `[IndexPage] Data changed - watchlist: ${watchlistItems?.length ?? 0} items, loading: ${watchlistLoading}`,
    );
  }, [watchlistItems, watchlistLoading]);

  useEffect(() => {
    if (!DEBUG_INDEX_RENDERS) return;
    console.log(
      `[IndexPage] Data changed - continueWatching: ${continueWatchingItems?.length ?? 0} items, loading: ${continueWatchingLoading}`,
    );
  }, [continueWatchingItems, continueWatchingLoading]);

  useEffect(() => {
    if (!DEBUG_INDEX_RENDERS) return;
    console.log(`[IndexPage] Data changed - trendingMovies: ${trendingMovies?.length ?? 0} items`);
  }, [trendingMovies]);

  useEffect(() => {
    if (!DEBUG_INDEX_RENDERS) return;
    console.log(`[IndexPage] Data changed - trendingTVShows: ${trendingTVShows?.length ?? 0} items`);
  }, [trendingTVShows]);

  useEffect(() => {
    if (!DEBUG_INDEX_RENDERS) return;
    console.log(
      `[IndexPage] Settings changed - loading: ${settingsLoading}, reachable: ${isBackendReachable}, lastLoaded: ${settingsLastLoadedAt}`,
    );
  }, [settingsLoading, isBackendReachable, settingsLastLoadedAt]);

  useEffect(() => {
    if (!DEBUG_INDEX_RENDERS) return;
    console.log(`[IndexPage] Focus changed - focused: ${focused}, menuOpen: ${isMenuOpen}`);
  }, [focused, isMenuOpen]);

  // Check if user is in kids curated list mode (only allowed lists, no trending)
  const isKidsCuratedMode = useMemo(() => {
    if (!activeUser?.isKidsProfile) return false;
    return activeUser.kidsMode === 'content_list' || activeUser.kidsMode === 'both';
  }, [activeUser?.isKidsProfile, activeUser?.kidsMode]);

  // Virtual shelves for kids allowed lists (when in curated mode)
  const kidsAllowedShelves = useMemo(() => {
    if (!isKidsCuratedMode || !activeUser?.kidsAllowedLists) return [];
    // Create virtual shelf configs from kidsAllowedLists URLs
    return activeUser.kidsAllowedLists.map((listUrl, index) => {
      // Extract list name from URL for display (e.g., "mdblist.com/lists/user/listname" -> "listname")
      const urlParts = listUrl.split('/');
      const listName = urlParts[urlParts.length - 1] || `Kids List ${index + 1}`;
      // Capitalize and replace hyphens/underscores with spaces
      const displayName = listName
        .split(/[-_]/)
        .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
        .join(' ');
      return {
        id: `kids-list-${index}`,
        name: displayName,
        enabled: true,
        order: 10 + index, // After built-in shelves
        type: 'mdblist' as const,
        listUrl,
        limit: undefined as number | undefined,
        hideUnreleased: undefined as boolean | undefined,
      };
    });
  }, [isKidsCuratedMode, activeUser?.kidsAllowedLists]);

  // Get custom shelves from settings (includes kids allowed lists when in curated mode)
  const customShelves = useMemo(() => {
    const allShelves = userSettings?.homeShelves?.shelves ?? settings?.homeShelves?.shelves ?? [];
    const configuredShelves = allShelves.filter((shelf) => shelf.type === 'mdblist' && shelf.listUrl && shelf.enabled);
    // In kids curated mode, also include the kids allowed lists as virtual shelves
    if (isKidsCuratedMode && kidsAllowedShelves.length > 0) {
      return [...configuredShelves, ...kidsAllowedShelves];
    }
    return configuredShelves;
  }, [userSettings?.homeShelves?.shelves, settings?.homeShelves?.shelves, isKidsCuratedMode, kidsAllowedShelves]);

  // Get explore card position setting (front or end)
  const exploreCardPosition = useMemo(() => {
    return userSettings?.homeShelves?.exploreCardPosition ?? settings?.homeShelves?.exploreCardPosition ?? 'front';
  }, [userSettings?.homeShelves?.exploreCardPosition, settings?.homeShelves?.exploreCardPosition]);

  // Fetch custom list data when custom shelves change
  // Wait for activeUserId to avoid duplicate fetches (once without userId, once with)
  useEffect(() => {
    if (customShelves.length === 0 || !activeUserId) return;
    if (KILL_CUSTOM_SHELVES) { console.log('[KILL] Custom shelves disabled'); return; }
    // Don't fire until startup bundle has settled to avoid duplicate fetches
    if (!startupReady) return;

    // Defer until home screen has painted — see SECONDARY_FETCH_DELAY_MS comment
    const timer = setTimeout(() => {
      const fetchCustomLists = async () => {
        for (const shelf of customShelves) {
          if (!shelf.listUrl) continue;
          // Use shelf's configured limit if set, otherwise use default
          const itemLimit = shelf.limit && shelf.limit > 0 ? shelf.limit : MAX_SHELF_ITEMS_ON_HOME;
          // Create cache key that includes URL, limit, hideUnreleased, hideWatched, and userId so changes trigger re-fetch
          const cacheKey = `${shelf.listUrl}:${itemLimit}:${shelf.hideUnreleased ?? false}:${hideWatched}:${activeUserId ?? ''}`;
          // Skip if we've already fetched this URL with these parameters
          if (fetchedListUrlsRef.current.has(cacheKey)) continue;

          fetchedListUrlsRef.current.add(cacheKey);
          startTransition(() => {
            setCustomListLoading((prev) => ({ ...prev, [shelf.id]: true }));
          });

          try {
            const { items, total, unfilteredTotal } = await apiService.getCustomList(
              shelf.listUrl,
              activeUserId ?? undefined, // userId for hideWatched filtering
              itemLimit,
              undefined, // offset
              shelf.hideUnreleased,
              hideWatched,
            );
            startTransition(() => {
              setCustomListData((prev) => ({ ...prev, [shelf.id]: items }));
              setCustomListTotals((prev) => ({ ...prev, [shelf.id]: total }));
              // Store unfilteredTotal for explore card logic (falls back to total if not filtering)
              setCustomListUnfilteredTotals((prev) => ({ ...prev, [shelf.id]: unfilteredTotal ?? total }));
            });
          } catch (err) {
            console.warn(`Failed to fetch custom list for shelf ${shelf.id}:`, err);
          } finally {
            startTransition(() => {
              setCustomListLoading((prev) => ({ ...prev, [shelf.id]: false }));
            });
          }
        }
      };

      void fetchCustomLists();
    }, SECONDARY_FETCH_DELAY_MS);

    return () => clearTimeout(timer);
  }, [customShelves, activeUserId, hideWatched, startupReady]);

  const backendLoadError = useMemo(() => {
    if (settingsLoading || settingsError) {
      return settingsError;
    }
    return null;
  }, [settingsLoading, settingsError]);

  const errorEntries = useMemo(
    () => [
      { context: 'Backend Settings', message: backendLoadError },
      { context: 'Continue Watching', message: continueWatchingError },
      { context: 'Your Watchlist', message: watchlistError },
      { context: 'Trending Movies', message: trendingMoviesError },
      { context: 'Trending TV Shows', message: trendingTVShowsError },
    ],
    [backendLoadError, continueWatchingError, watchlistError, trendingMoviesError, trendingTVShowsError],
  );

  const hasAuthFailure = useMemo(() => errorEntries.some(({ message }) => isAuthErrorMessage(message)), [errorEntries]);

  // Show errors as toasts
  useEffect(() => {
    if (hasAuthFailure) {
      showToast(AUTH_WARNING_MESSAGE, {
        tone: 'danger',
        id: 'auth-error',
        duration: 7000,
      });
    }

    // Check if backend settings has a network error - if so, only show that one toast
    // since all other API calls will also fail with network errors
    const backendNetworkError = backendLoadError && /network request failed/i.test(backendLoadError);

    for (const { context, message } of errorEntries) {
      if (!message || isAuthErrorMessage(message)) {
        continue;
      }

      // Skip showing individual network errors if backend settings already has one
      if (backendNetworkError && context !== 'Backend Settings' && /network request failed/i.test(message)) {
        continue;
      }

      // Skip showing stale network errors if backend is now reachable
      // (these are leftover errors from before reconnection)
      if (isBackendReachable && /network request failed/i.test(message)) {
        continue;
      }

      const formatted = buildWarningMessage(context, message);
      if (formatted) {
        showToast(formatted, {
          tone: 'danger',
          id: `error-${context}`,
          duration: 7000,
        });
      }
    }
  }, [errorEntries, hasAuthFailure, showToast, backendLoadError, isBackendReachable]);

  // Shelf scrolling with position caching - uses Reanimated shared value for UI-thread animation
  const scrollToShelf = useCallback(
    (shelfKey: string, skipAnimation = false) => {
      if (!Platform.isTV) {
        return;
      }

      const topOffset = 0; // No offset needed with current layout

      const performScroll = (rawY: number) => {
        const targetY = Math.max(0, rawY - topOffset);
        if (skipAnimation || isInitialLoadRef.current) {
          // No animation: instant snap
          scrollViewRef.current?.scrollTo({ y: targetY, animated: false });
        } else {
          // Animated: use shared value for UI-thread smoothness
          shelfScrollTargetY.value = targetY;
        }
      };

      // Check cache first (avoids expensive measureLayout on Android)
      const cachedPosition = shelfPositionsRef.current[shelfKey];
      if (cachedPosition !== undefined) {
        performScroll(cachedPosition);
        return;
      }

      // Fall back to measureLayout for first access, then cache
      const shelfRef = shelfRefs.current[shelfKey];
      const scrollViewNode = scrollViewRef.current;
      if (!shelfRef || !scrollViewNode) {
        return;
      }

      shelfRef.measureLayout(
        scrollViewNode as any,
        (_left, top) => {
          // Cache the raw position (before offset applied)
          shelfPositionsRef.current[shelfKey] = top;
          performScroll(top);
        },
        () => {
          // Silently fail - no console spam
        },
      );
    },
    [scrollViewRef, shelfScrollTargetY],
  );

  const registerShelfRef = useCallback((key: string, ref: RNView | null) => {
    shelfRefs.current[key] = ref;
  }, []);

  const registerShelfFlatListRef = useCallback((key: string, ref: FlatList | null) => {
    shelfFlatListRefs.current[key] = ref;
  }, []);

  const MemoizedDesktopShelf = useMemo(
    () => React.memo(DesktopShelf, areDesktopShelfPropsEqual),
    [DesktopShelf, areDesktopShelfPropsEqual],
  );

  useEffect(() => {
    if (__DEV__ && Platform.OS === 'ios') {
      console.log('[SafeArea] Home screen insets', safeAreaInsets);
    }
  }, [safeAreaInsets]);
  useEffect(() => {
    scrollMetricsRef.current.viewportHeight = screenHeight;
  }, [screenHeight]);

  // Use mobile layout on all non-TV iOS/Android devices (phones, tablets, foldables)
  const isMobileDevice = (Platform.OS === 'ios' || Platform.OS === 'android') && !Platform.isTV;
  const shouldUseMobileLayout = isMobileDevice;

  const triggerReloadAfterAuthFailure = useCallback(() => {
    const callAndReport = (label: string, fn?: () => Promise<unknown> | void) => {
      if (!fn) {
        return;
      }
      try {
        const result = fn();
        if (result && typeof (result as Promise<unknown>).then === 'function') {
          (result as Promise<unknown>).catch(() => {
            // Silently ignore reload failures
          });
        }
      } catch {
        // Silently ignore reload errors
      }
    };

    callAndReport('User Profiles', refreshUserProfiles);
    callAndReport('Continue Watching', refreshContinueWatching);
    callAndReport('Watchlist', refreshWatchlist);
    // Note: Trending movies/TV shows are NOT reloaded here — they self-manage
    // via the startup bundle and their own dependency arrays (hideUnreleased,
    // hideWatched, userId, etc.). Calling refetch() here would defeat startup
    // bundle hydration by resetting the hydration flag and forcing independent
    // HTTP requests.
  }, [refreshUserProfiles, refreshContinueWatching, refreshWatchlist]);

  // Full reload when settings are applied/saved (settingsLastLoadedAt changes)
  useEffect(() => {
    // Skip on initial mount - only reload when settings actually change
    if (previousSettingsLoadedAtRef.current === null) {
      previousSettingsLoadedAtRef.current = settingsLastLoadedAt;
      return;
    }

    // If settingsLastLoadedAt changed, trigger a full reload
    if (settingsLastLoadedAt !== null && settingsLastLoadedAt !== previousSettingsLoadedAtRef.current) {
      previousSettingsLoadedAtRef.current = settingsLastLoadedAt;
      triggerReloadAfterAuthFailure();
    }
  }, [settingsLastLoadedAt, triggerReloadAfterAuthFailure]);

  // Reload content when backend reconnects after going down.
  // Only cascades on actual reconnection (was reachable, went down, came back).
  // Does NOT cascade on the first time becoming reachable (initial login) because
  // context providers already handle initial data loading via the startup bundle.
  const previousBackendReachableRef = React.useRef<boolean | null>(null);
  const hasEverBeenReachableRef = React.useRef(false);
  useEffect(() => {
    // Skip initial mount
    if (previousBackendReachableRef.current === null) {
      previousBackendReachableRef.current = isBackendReachable;
      if (isBackendReachable) {
        hasEverBeenReachableRef.current = true;
      }
      return;
    }

    if (isBackendReachable) {
      // Only reload on reconnection (not first connection)
      if (!previousBackendReachableRef.current && hasEverBeenReachableRef.current) {
        triggerReloadAfterAuthFailure();
      }
      hasEverBeenReachableRef.current = true;
    }
    previousBackendReachableRef.current = isBackendReachable;
  }, [isBackendReachable, triggerReloadAfterAuthFailure]);

  useFocusEffect(
    useCallback(() => {
      if (!hasAuthFailure) {
        return;
      }

      triggerReloadAfterAuthFailure();

      return undefined;
    }, [hasAuthFailure, triggerReloadAfterAuthFailure]),
  );

  // Memory optimization: clear large caches when screen loses focus
  // This reduces memory pressure when navigating to player/details
  useFocusEffect(
    useCallback(() => {
      // Called when screen gains focus - nothing to do
      return () => {
        // Called when screen loses focus - clear caches to free memory
        if (Platform.isTV) {
          // Clear custom list data (can be refetched when returning)
          setCustomListData({});
          setCustomListTotals({});
          setCustomListUnfilteredTotals({});
          fetchedListUrlsRef.current.clear();

          // Clear overview caches (will be refetched as needed)
          setSeriesOverviews(new Map());
          setWatchlistYears(new Map());

          if (__DEV__) {
            console.log('[IndexPage] Cleared caches on blur to free memory');
          }
        }
      };
    }, []),
  );

  // Refs for refresh functions used in the focus effect below.
  // This prevents the effect from re-running when activeUserId changes during
  // startup (which gives refresh a new identity), which would be misinterpreted
  // as a "return from navigation" and trigger redundant fetches before the
  // startup bundle arrives.
  const refreshContinueWatchingRef = useRef(refreshContinueWatching);
  refreshContinueWatchingRef.current = refreshContinueWatching;
  const refreshWatchlistRef = useRef(refreshWatchlist);
  refreshWatchlistRef.current = refreshWatchlist;

  // Reload data when screen becomes visible (including when navigating back from details)
  // Using useEffect with focused instead of useFocusEffect because the screen stays mounted
  // when details page is pushed on top, so useFocusEffect doesn't trigger on navigation back
  useEffect(() => {
    if (!focused) {
      wasFocusedRef.current = false;
      return;
    }

    // Detect actual focus transition (false→true), not dep-change re-runs while focused.
    // isReturnFromNavigation is true ONLY when: we've been focused before AND focused just
    // transitioned from false→true (i.e. user navigated away and came back).
    const isFocusTransition = !wasFocusedRef.current; // false→true transition
    wasFocusedRef.current = true;
    const isReturnFromNavigation = hasBeenFocusedRef.current && isFocusTransition;

    // Clear cached shelf positions on every focus - layout may not be ready yet or may have changed
    if (Platform.isTV) {
      shelfPositionsRef.current = {};
    }

    // Compute shelf config for initial scroll positioning
    // Note: We compute inline here since desktopShelves may not be ready yet
    const shelfConfig = userSettings?.homeShelves?.shelves ??
      settings?.homeShelves?.shelves ?? [
        { id: 'continue-watching', name: 'Continue Watching', enabled: true, order: 0 },
        { id: 'watchlist', name: 'Your Watchlist', enabled: true, order: 1 },
        { id: 'trending-movies', name: 'Trending Movies', enabled: true, order: 2 },
        { id: 'trending-tv', name: 'Trending TV Shows', enabled: true, order: 3 },
      ];

    // Map shelf IDs to their card data for focus computation
    const shelfCardMap: Record<string, CardData[]> = {
      'continue-watching': continueWatchingCards,
      watchlist: watchlistCards,
      'trending-movies': trendingMovieCards,
      'trending-tv': trendingShowCards,
    };

    // Find the first enabled shelf (by user's order) that has cards
    const sortedShelfConfigs = [...shelfConfig].filter((c) => c.enabled).sort((a, b) => a.order - b.order);
    const firstShelfWithCards = sortedShelfConfigs.find((config) => {
      const cards = shelfCardMap[config.id];
      return cards && cards.length > 0;
    });

    if (Platform.isTV && firstShelfWithCards) {
      // Only scroll to first shelf on initial load, not on return from navigation
      // Focus naturally stays on the previously focused item when returning
      if (!isReturnFromNavigation) {
        setTimeout(() => {
          scrollToShelf(firstShelfWithCards.id, true); // Skip animation
        }, 50);
      }

      // Mark initial load as complete
      if (isInitialLoadRef.current) {
        setTimeout(() => {
          isInitialLoadRef.current = false;
        }, 150);
      }
    } else if (Platform.isTV) {
      // No shelves with cards - mark initial load complete anyway
      isInitialLoadRef.current = false;
    }

    // Only refresh if we have backend ready and no auth failure
    if (settingsLoading || hasAuthFailure) {
      return;
    }

    // Mark that we've been focused at least once — AFTER the settingsLoading guard
    // so that when settingsLoading transitions false, we don't misidentify it as
    // a return from navigation
    if (!hasBeenFocusedRef.current) {
      hasBeenFocusedRef.current = true;
    }

    // Only silently refresh when RETURNING from navigation, not on initial load
    // Initial load data is already fresh from the context providers
    if (isReturnFromNavigation && !KILL_FOCUS_REFRESH) {
      // Silently refresh continue watching and watchlist when returning from details
      // Uses refs to avoid re-triggering this effect when function identity changes during startup
      refreshContinueWatchingRef.current?.({ silent: true }).catch(() => {
        // Silent refresh failed - not critical
      });

      refreshWatchlistRef.current?.({ silent: true }).catch(() => {
        // Silent refresh failed - not critical
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- Intentionally omit card arrays, settings, and refresh refs to prevent re-triggering on startup state changes
  }, [focused, settingsLoading, hasAuthFailure, scrollToShelf]);

  useEffect(() => {
    const hadAuthFailure = hasAuthFailureRef.current;
    hasAuthFailureRef.current = hasAuthFailure;

    if (hadAuthFailure && !hasAuthFailure) {
      try {
        const maybePromise = refreshUserProfiles?.();
        if (maybePromise && typeof maybePromise.then === 'function') {
          maybePromise.catch((error) => {
            if (__DEV__) {
              console.warn('[AuthRecovery] User profiles refresh failed', error);
            }
          });
        }
      } catch (error) {
        if (__DEV__) {
          console.warn('[AuthRecovery] User profiles refresh threw', error);
        }
      }
    }
  }, [hasAuthFailure, refreshUserProfiles]);

  const desktopStyles = useMemo(
    () => (!shouldUseMobileLayout ? createDesktopStyles(theme, screenHeight) : undefined),
    [shouldUseMobileLayout, screenHeight, theme],
  );
  const mobileStyles = useMemo(
    () => (shouldUseMobileLayout ? createMobileStyles(theme) : undefined),
    [shouldUseMobileLayout, theme],
  );

  // Memoize badge visibility to prevent prop identity changes on each render
  const badgeVisibility = useMemo(
    () => userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility ?? [],
    [userSettings?.display?.badgeVisibility, settings?.display?.badgeVisibility],
  );

  // Only enrich titles with watch status when the badge is enabled
  const shouldEnrichWatchStatus = useMemo(
    () => badgeVisibility.includes('watchState'),
    [badgeVisibility],
  );

  // Memoize watch state icon style to prevent prop identity changes on each render
  const watchStateIconStyle = useMemo(
    () => userSettings?.display?.watchStateIconStyle ?? settings?.display?.watchStateIconStyle ?? 'colored',
    [userSettings?.display?.watchStateIconStyle, settings?.display?.watchStateIconStyle],
  );

  // Cache series overviews for continue watching items
  const [seriesOverviews, setSeriesOverviews] = useState<Map<string, string>>(new Map());

  // Cache years for watchlist items missing year data
  const [watchlistYears, setWatchlistYears] = useState<Map<string, number>>(new Map());
  // Track which IDs we've already queued for year fetching (prevents re-fetch cascade)
  const fetchedYearIdsRef = useRef<Set<string>>(new Set());

  // Movie release data from context (persists across navigation)
  const { releases: movieReleases, hasRelease: hasMovieRelease, queueReleaseFetch } = useMovieReleases();

  const watchlistCards = useMemo(() => {
    if (DEBUG_INDEX_RENDERS) {
      console.log(
        `[IndexPage] useMemo: watchlistCards recomputing (${watchlistItems?.length ?? 0} items, ${watchlistYears.size} years, ${movieReleases.size} releases)`,
      );
    }
    // Enrich with watch status before mapping to CardData (for TV shelf badges)
    const trendingItems = mapWatchlistToTrendingItems(watchlistItems, watchlistYears);
    const enrichedItems = enrichTrendingItemsWithWatchStatus(trendingItems, isWatched, watchStatusItems, continueWatchingItems);
    // Filter out watched items if hideWatched is enabled
    const filteredItems = hideWatched
      ? enrichedItems.filter((item) => !isWatched(item.title.mediaType, item.title.id))
      : enrichedItems;
    const allCards = mapTrendingToCards(filteredItems, movieReleases);
    const knownTotal = startupData?.watchlistTotal ?? allCards.length;
    if (allCards.length <= MAX_SHELF_ITEMS_ON_HOME && knownTotal <= MAX_SHELF_ITEMS_ON_HOME) {
      return allCards;
    }
    const exploreCard = createExploreCard('watchlist', allCards, knownTotal > allCards.length ? knownTotal - MAX_SHELF_ITEMS_ON_HOME : undefined);
    const limitedCards = allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return exploreCardPosition === 'end' ? [...limitedCards, exploreCard] : [exploreCard, ...limitedCards];
  }, [watchlistItems, watchlistYears, movieReleases, exploreCardPosition, isWatched, watchStatusItems, continueWatchingItems, hideWatched, startupData?.watchlistTotal]);
  const continueWatchingCards = useMemo(() => {
    if (DEBUG_INDEX_RENDERS) {
      console.log(
        `[IndexPage] useMemo: continueWatchingCards recomputing (${continueWatchingItems?.length ?? 0} items, ${seriesOverviews.size} overviews)`,
      );
    }
    const allCards = mapContinueWatchingToCards(continueWatchingItems, seriesOverviews, watchlistItems, movieReleases);
    const knownTotal = startupData?.continueWatchingTotal ?? allCards.length;
    if (allCards.length <= MAX_SHELF_ITEMS_ON_HOME && knownTotal <= MAX_SHELF_ITEMS_ON_HOME) {
      return allCards;
    }
    const exploreCard = createExploreCard('continue-watching', allCards, knownTotal > allCards.length ? knownTotal - MAX_SHELF_ITEMS_ON_HOME : undefined, (card) => card.backdropUrl || card.headerImage || card.cardImage);
    const limitedCards = allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return exploreCardPosition === 'end' ? [...limitedCards, exploreCard] : [exploreCard, ...limitedCards];
  }, [continueWatchingItems, seriesOverviews, watchlistItems, movieReleases, exploreCardPosition, startupData?.continueWatchingTotal]);

  useEffect(() => {
    if (!continueWatchingItems || continueWatchingItems.length === 0) {
      return;
    }
    if (KILL_SERIES_OVERVIEWS) { console.log('[KILL] Series overviews disabled'); return; }
    // Don't fire until startup bundle has settled to avoid duplicate fetches
    if (!startupReady) return;

    // Defer until home screen has painted — see SECONDARY_FETCH_DELAY_MS comment
    const timer = setTimeout(() => {
      const fetchOverviews = async () => {
        const updates = new Map<string, string>();
        const queriesToFetch: Array<{
          seriesId: string;
          tvdbId?: string;
          tmdbId?: string;
          titleId: string;
          name: string;
          year?: number;
        }> = [];

        for (const item of continueWatchingItems) {
          // Skip if we already have the overview
          if (seriesOverviews.has(item.seriesId)) {
            continue;
          }

          // Check if series is in watchlist first (fast path)
          const watchlistItem = watchlistItems?.find((w) => w.id === item.seriesId);
          if (watchlistItem?.overview) {
            updates.set(item.seriesId, watchlistItem.overview);
            continue;
          }

          // Extract identifiers from seriesId
          // Format could be: "tvdb:123456", "tvdb:series:123456", "tmdb:tv:123456", etc.
          const tvdbMatch = item.seriesId.match(/tvdb:(?:series:)?(\d+)/);
          const tmdbMatch = item.seriesId.match(/tmdb:(?:tv:)?(\d+)/);
          const tvdbId = tvdbMatch ? tvdbMatch[1] : undefined;
          const tmdbId = tmdbMatch ? tmdbMatch[1] : undefined;

          if (tvdbId || tmdbId) {
            queriesToFetch.push({
              seriesId: item.seriesId,
              tvdbId,
              tmdbId,
              titleId: item.seriesId,
              name: item.seriesTitle,
              year: item.year,
            });
          }
        }

        // Batch fetch all queries in a single request
        if (queriesToFetch.length > 0) {
          try {
            const batchResponse = await apiService.batchSeriesDetails(
              queriesToFetch.map((q) => ({
                tvdbId: q.tvdbId,
                tmdbId: q.tmdbId,
                titleId: q.titleId,
                name: q.name,
                year: q.year,
              })),
            );

            // Process results
            for (let i = 0; i < batchResponse.results.length; i++) {
              const result = batchResponse.results[i];
              const query = queriesToFetch[i];

              if (result.details?.title.overview) {
                updates.set(query.seriesId, result.details.title.overview);
              } else if (result.error) {
                console.warn(`Failed to fetch overview for ${query.name}:`, result.error);
              }
            }
          } catch (error) {
            console.warn('Failed to batch fetch series overviews:', error);
          }
        }

        if (updates.size > 0) {
          startTransition(() => {
            setSeriesOverviews((prev) => capMapSize(new Map([...prev, ...updates]), MAX_SERIES_OVERVIEWS_CACHE));
          });
        }
      };

      void fetchOverviews();
    }, SECONDARY_FETCH_DELAY_MS);

    return () => clearTimeout(timer);
  }, [continueWatchingItems, watchlistItems, startupReady]);

  // Fetch missing year data for watchlist items
  // Uses ref to track already-fetched IDs to prevent re-fetch cascade
  useEffect(() => {
    if (!watchlistItems || watchlistItems.length === 0) {
      return;
    }
    if (KILL_WATCHLIST_YEARS) { console.log('[KILL] Watchlist years disabled'); return; }
    // Don't fire until startup bundle has settled to avoid duplicate fetches
    if (!startupReady) return;

    // Defer until home screen has painted — see SECONDARY_FETCH_DELAY_MS comment
    const timer = setTimeout(() => {
      const fetchMissingYears = async () => {
        const updates = new Map<string, number>();
        const seriesToFetch: Array<{
          id: string;
          tvdbId?: string;
          tmdbId?: string;
          name: string;
        }> = [];
        const moviesToFetch: Array<{
          id: string;
          imdbId?: string;
          tmdbId?: string;
          name: string;
        }> = [];

        for (const item of watchlistItems) {
          // Skip if we already have the year (either from API or cached)
          if (item.year && item.year > 0) {
            continue;
          }
          // Use ref to check (not state) to prevent re-fetch cascade
          if (fetchedYearIdsRef.current.has(item.id)) {
            continue;
          }

          const isSeries = item.mediaType === 'series' || item.mediaType === 'tv' || item.mediaType === 'show';

          if (isSeries) {
            seriesToFetch.push({
              id: item.id,
              tvdbId: item.externalIds?.tvdb,
              tmdbId: item.externalIds?.tmdb,
              name: item.name,
            });
          } else {
            moviesToFetch.push({
              id: item.id,
              imdbId: item.externalIds?.imdb,
              tmdbId: item.externalIds?.tmdb,
              name: item.name,
            });
          }
        }

        // Mark all IDs as queued BEFORE fetching to prevent duplicate fetches
        for (const series of seriesToFetch) {
          fetchedYearIdsRef.current.add(series.id);
        }
        for (const movie of moviesToFetch) {
          fetchedYearIdsRef.current.add(movie.id);
        }

        if (seriesToFetch.length === 0 && moviesToFetch.length === 0) {
          return;
        }

        if (DEBUG_INDEX_RENDERS) {
          console.log(`[IndexPage] Fetching years for ${seriesToFetch.length} series, ${moviesToFetch.length} movies`);
        }

        // Batch fetch series details
        if (seriesToFetch.length > 0) {
          try {
            const batchResponse = await apiService.batchSeriesDetails(
              seriesToFetch.map((q) => ({
                tvdbId: q.tvdbId,
                tmdbId: q.tmdbId,
                name: q.name,
              })),
            );

            for (let i = 0; i < batchResponse.results.length; i++) {
              const result = batchResponse.results[i];
              const query = seriesToFetch[i];

              if (result.details?.title.year && result.details.title.year > 0) {
                updates.set(query.id, result.details.title.year);
              }
            }
          } catch (error) {
            console.warn('Failed to batch fetch series years:', error);
          }
        }

        // Fetch movie details in parallel (no batch API for movies)
        if (moviesToFetch.length > 0) {
          const movieResults = await Promise.allSettled(
            moviesToFetch.map(async (movie) => {
              const details = await apiService.getMovieDetails({
                imdbId: movie.imdbId,
                tmdbId: movie.tmdbId ? Number(movie.tmdbId) : undefined,
                name: movie.name,
              });
              return { id: movie.id, year: details?.year };
            }),
          );
          for (const settled of movieResults) {
            if (settled.status === 'fulfilled' && settled.value.year && settled.value.year > 0) {
              updates.set(settled.value.id, settled.value.year);
            }
          }
        }

        if (updates.size > 0) {
          startTransition(() => {
            setWatchlistYears((prev) => capMapSize(new Map([...prev, ...updates]), MAX_WATCHLIST_YEARS_CACHE));
          });
        }
      };

      void fetchMissingYears();
    }, SECONDARY_FETCH_DELAY_MS);

    return () => clearTimeout(timer);
  }, [watchlistItems, startupReady]);

  // Queue release data fetches for movies when releaseStatus badge is enabled
  // Uses MovieReleasesContext which handles batching, deduplication, and persistence
  useEffect(() => {
    // Only fetch if releaseStatus badge is enabled
    if (!badgeVisibility.includes('releaseStatus')) {
      return;
    }
    if (KILL_MOVIE_RELEASES) { console.log('[KILL] Movie releases disabled'); return; }
    // Don't fire until startup bundle has settled to avoid duplicate fetches
    if (!startupReady) return;

    // Defer until home screen has painted — see SECONDARY_FETCH_DELAY_MS comment
    const timer = setTimeout(() => {
      // Collect all movies that need release data
      const moviesToFetch: Array<{ id: string; tmdbId?: number; imdbId?: string }> = [];

      // From trending movies
      if (trendingMovies) {
        for (const item of trendingMovies) {
          if (
            item.title.mediaType === 'movie' &&
            (item.title.tmdbId || item.title.imdbId) &&
            !hasMovieRelease(item.title.id) &&
            !item.title.theatricalRelease &&
            !item.title.homeRelease
          ) {
            moviesToFetch.push({ id: item.title.id, tmdbId: item.title.tmdbId, imdbId: item.title.imdbId });
          }
        }
      }

      // From custom lists
      for (const items of Object.values(customListData)) {
        for (const item of items) {
          if (
            item.title.mediaType === 'movie' &&
            (item.title.tmdbId || item.title.imdbId) &&
            !hasMovieRelease(item.title.id) &&
            !item.title.theatricalRelease &&
            !item.title.homeRelease
          ) {
            moviesToFetch.push({ id: item.title.id, tmdbId: item.title.tmdbId, imdbId: item.title.imdbId });
          }
        }
      }

      // From continue watching (movies only - no nextEpisode)
      if (continueWatchingItems) {
        for (const item of continueWatchingItems) {
          const isMovie = !item.nextEpisode;
          const tmdbId = item.externalIds?.tmdb ? Number(item.externalIds.tmdb) : undefined;
          if (isMovie && tmdbId && !hasMovieRelease(item.seriesId)) {
            moviesToFetch.push({ id: item.seriesId, tmdbId });
          }
        }
      }

      // From watchlist (movies only)
      if (watchlistItems) {
        for (const item of watchlistItems) {
          const tmdbId = item.externalIds?.tmdb ? Number(item.externalIds.tmdb) : undefined;
          if (item.mediaType === 'movie' && tmdbId && !hasMovieRelease(item.id)) {
            moviesToFetch.push({ id: item.id, tmdbId });
          }
        }
      }

      if (moviesToFetch.length === 0) {
        return;
      }

      if (DEBUG_INDEX_RENDERS) {
        console.log(`[IndexPage] Queueing release fetch for ${moviesToFetch.length} movies`);
      }

      // Queue for fetching - context handles batching and deduplication
      queueReleaseFetch(moviesToFetch);
    }, SECONDARY_FETCH_DELAY_MS);

    return () => clearTimeout(timer);
  }, [
    trendingMovies,
    customListData,
    continueWatchingItems,
    watchlistItems,
    badgeVisibility,
    hasMovieRelease,
    queueReleaseFetch,
    startupReady,
  ]);

  const trendingMovieCards = useMemo(() => {
    if (DEBUG_INDEX_RENDERS) {
      console.log(`[IndexPage] useMemo: trendingMovieCards recomputing (${trendingMovies?.length ?? 0} items)`);
    }
    const allCards = mapTrendingToCards(trendingMovies ?? undefined, movieReleases);
    if (allCards.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allCards;
    }
    const exploreCard = createExploreCard('trending-movies', allCards);
    const limitedCards = allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return exploreCardPosition === 'end' ? [...limitedCards, exploreCard] : [exploreCard, ...limitedCards];
  }, [trendingMovies, movieReleases, exploreCardPosition]);

  const trendingShowCards = useMemo(() => {
    if (DEBUG_INDEX_RENDERS) {
      console.log(`[IndexPage] useMemo: trendingShowCards recomputing (${trendingTVShows?.length ?? 0} items)`);
    }
    const allCards = mapTrendingToCards(trendingTVShows ?? undefined);
    if (allCards.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allCards;
    }
    const exploreCard = createExploreCard('trending-shows', allCards);
    const limitedCards = allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return exploreCardPosition === 'end' ? [...limitedCards, exploreCard] : [exploreCard, ...limitedCards];
  }, [trendingTVShows, exploreCardPosition]);

  // Generate cards for each custom list shelf
  const customListCards = useMemo(() => {
    const result: Record<string, CardData[]> = {};
    for (const [shelfId, items] of Object.entries(customListData)) {
      const allCards = mapTrendingToCards(items, movieReleases);
      const shelf = customShelves.find((s) => s.id === shelfId);
      // Use filtered total for display, unfiltered total for explore card decision
      const filteredTotal = customListTotals[shelfId] ?? allCards.length;
      const unfilteredTotal = customListUnfilteredTotals[shelfId] ?? filteredTotal;
      // Use shelf's configured limit if set, otherwise use filtered total
      const shelfLimit = shelf?.limit && shelf.limit > 0 ? shelf.limit : filteredTotal;

      // Calculate how many items are displayed and how many more exist
      const displayedCount = Math.min(MAX_SHELF_ITEMS_ON_HOME, allCards.length);
      const remainingCount = Math.max(0, filteredTotal - displayedCount);

      // Show explore card only if:
      // 1. Unfiltered total exceeds home screen limit (there was enough content originally), AND
      // 2. There are actually more filtered items to explore (remainingCount > 0)
      if (unfilteredTotal <= MAX_SHELF_ITEMS_ON_HOME || remainingCount === 0) {
        // Not enough items or all filtered items already fit - no explore card
        result[shelfId] = allCards.slice(0, shelfLimit);
      } else {
        // Show explore card with remaining filtered items count
        const exploreCard = createExploreCard(shelfId, allCards, remainingCount);
        const limitedCards = allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME);
        result[shelfId] =
          exploreCardPosition === 'end' ? [...limitedCards, exploreCard] : [exploreCard, ...limitedCards];
      }
    }
    return result;
  }, [customListData, customListTotals, customListUnfilteredTotals, movieReleases, exploreCardPosition, customShelves]);

  // Generate titles for each custom list shelf (mobile)
  const customListTitles = useMemo(() => {
    const result: Record<string, (Title & { uniqueKey: string; collagePosters?: string[] })[]> = {};
    for (const [shelfId, items] of Object.entries(customListData)) {
      const titlesWithReleases = items.map((item) => {
        // Merge cached release data for movies
        const cachedReleases = item.title.mediaType === 'movie' ? movieReleases.get(item.title.id) : undefined;
        return {
          ...item.title,
          uniqueKey: `custom:${shelfId}:${item.title.id}`,
          theatricalRelease: item.title.theatricalRelease ?? cachedReleases?.theatricalRelease,
          homeRelease: item.title.homeRelease ?? cachedReleases?.homeRelease,
        };
      });
      // Enrich with watch status if badge is enabled
      const allTitles = shouldEnrichWatchStatus
        ? enrichWithWatchStatus(titlesWithReleases, isWatched, watchStatusItems, continueWatchingItems)
        : titlesWithReleases;
      const shelf = customShelves.find((s) => s.id === shelfId);
      // Use filtered total for display, unfiltered total for explore card decision
      const filteredTotal = customListTotals[shelfId] ?? allTitles.length;
      const unfilteredTotal = customListUnfilteredTotals[shelfId] ?? filteredTotal;
      // Use shelf's configured limit if set, otherwise use filtered total
      const shelfLimit = shelf?.limit && shelf.limit > 0 ? shelf.limit : filteredTotal;

      // Calculate how many items are displayed and how many more exist
      const displayedCount = Math.min(MAX_SHELF_ITEMS_ON_HOME, allTitles.length);
      const remainingCount = Math.max(0, filteredTotal - displayedCount);

      // Show explore card only if:
      // 1. Unfiltered total exceeds home screen limit (there was enough content originally), AND
      // 2. There are actually more filtered items to explore (remainingCount > 0)
      if (unfilteredTotal <= MAX_SHELF_ITEMS_ON_HOME || remainingCount === 0) {
        // Not enough items or all filtered items already fit - no explore card
        result[shelfId] = allTitles.slice(0, shelfLimit);
      } else {
        // Show explore card with remaining filtered items count
        // Pick posters from displayed items (deterministic)
        const collagePosters = pickRandomPosters(allTitles, (title) => title?.poster?.url, 4, shelfId);
        const exploreTitle: Title & { uniqueKey: string; collagePosters?: string[] } = {
          id: `${EXPLORE_CARD_ID_PREFIX}${shelfId}`,
          name: 'Explore',
          overview: `View all ${filteredTotal} items`,
          year: remainingCount,
          language: 'en',
          mediaType: 'explore',
          poster: {
            url: collagePosters[0] || '',
            type: 'poster',
            width: 0,
            height: 0,
          },
          uniqueKey: `explore:${shelfId}`,
          collagePosters,
        };
        const limitedTitles = allTitles.slice(0, MAX_SHELF_ITEMS_ON_HOME);
        result[shelfId] =
          exploreCardPosition === 'end' ? [...limitedTitles, exploreTitle] : [exploreTitle, ...limitedTitles];
      }
    }
    return result;
  }, [customListData, customListTotals, customListUnfilteredTotals, movieReleases, exploreCardPosition, customShelves, shouldEnrichWatchStatus, isWatched, watchStatusItems, continueWatchingItems]);

  const watchlistTitles = useMemo(() => {
    const baseTitles = mapWatchlistToTitles(watchlistItems, watchlistYears);
    // Merge cached release data for movies
    const titlesWithReleases = baseTitles.map((title) => {
      if (title.mediaType === 'movie') {
        const cachedReleases = movieReleases.get(title.id);
        if (cachedReleases) {
          return {
            ...title,
            theatricalRelease: title.theatricalRelease ?? cachedReleases.theatricalRelease,
            homeRelease: title.homeRelease ?? cachedReleases.homeRelease,
          };
        }
      }
      return title;
    });
    // Enrich with watch status if badge is enabled
    const enrichedTitles = shouldEnrichWatchStatus
      ? enrichWithWatchStatus(titlesWithReleases, isWatched, watchStatusItems, continueWatchingItems)
      : titlesWithReleases;
    // Filter out watched items if hideWatched is enabled
    const allTitles = hideWatched
      ? enrichedTitles.filter((title) => !isWatched(title.mediaType, title.id))
      : enrichedTitles;
    const knownTotal = startupData?.watchlistTotal ?? allTitles.length;
    if (allTitles.length <= MAX_SHELF_ITEMS_ON_HOME && knownTotal <= MAX_SHELF_ITEMS_ON_HOME) {
      return allTitles;
    }
    const totalCount = Math.max(knownTotal, allTitles.length);
    const remainingCount = totalCount - MAX_SHELF_ITEMS_ON_HOME;
    // Pick posters from displayed items (deterministic)
    const collagePosters = pickRandomPosters(allTitles, (title) => title?.poster?.url, 4, 'watchlist');
    const exploreTitle: Title & { uniqueKey: string; collagePosters?: string[] } = {
      id: `${EXPLORE_CARD_ID_PREFIX}watchlist`,
      name: 'Explore',
      overview: `View all ${totalCount} items`,
      year: remainingCount, // Will be displayed as "+X More"
      language: 'en',
      mediaType: 'explore',
      poster: {
        url: collagePosters[0] || '',
        type: 'poster',
        width: 0,
        height: 0,
      },
      uniqueKey: 'explore:watchlist',
      collagePosters,
    };
    const limitedTitles = allTitles.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return exploreCardPosition === 'end' ? [...limitedTitles, exploreTitle] : [exploreTitle, ...limitedTitles];
  }, [watchlistItems, watchlistYears, movieReleases, exploreCardPosition, shouldEnrichWatchStatus, isWatched, watchStatusItems, continueWatchingItems, hideWatched, startupData?.watchlistTotal]);
  const continueWatchingTitles = useMemo(() => {
    const baseTitles = mapContinueWatchingToTitles(continueWatchingItems, seriesOverviews, watchlistItems);
    // Merge cached release data for movies
    const titlesWithReleases = baseTitles.map((title) => {
      if (title.mediaType === 'movie') {
        const cachedReleases = movieReleases.get(title.id);
        if (cachedReleases) {
          return {
            ...title,
            theatricalRelease: title.theatricalRelease ?? cachedReleases.theatricalRelease,
            homeRelease: title.homeRelease ?? cachedReleases.homeRelease,
          };
        }
      }
      return title;
    });
    // Enrich with watch status if badge is enabled
    const allTitles = shouldEnrichWatchStatus
      ? enrichWithWatchStatus(titlesWithReleases, isWatched, watchStatusItems, continueWatchingItems)
      : titlesWithReleases;
    const knownTotal = startupData?.continueWatchingTotal ?? allTitles.length;
    if (allTitles.length <= MAX_SHELF_ITEMS_ON_HOME && knownTotal <= MAX_SHELF_ITEMS_ON_HOME) {
      return allTitles;
    }
    const totalCount = Math.max(knownTotal, allTitles.length);
    const remainingCount = totalCount - MAX_SHELF_ITEMS_ON_HOME;
    // Pick backdrop images from displayed items for landscape collage (deterministic)
    const collagePosters = pickRandomPosters(allTitles, (title) => title?.backdrop?.url || title?.poster?.url, 4, 'continue-watching');
    const exploreTitle: Title & { uniqueKey: string; collagePosters?: string[] } = {
      id: `${EXPLORE_CARD_ID_PREFIX}continue-watching`,
      name: 'Explore',
      overview: `View all ${totalCount} items`,
      year: remainingCount,
      language: 'en',
      mediaType: 'explore',
      poster: {
        url: collagePosters[0] || '',
        type: 'poster',
        width: 0,
        height: 0,
      },
      uniqueKey: 'explore:continue-watching',
      collagePosters,
    };
    const limitedTitles = allTitles.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return exploreCardPosition === 'end' ? [...limitedTitles, exploreTitle] : [exploreTitle, ...limitedTitles];
  }, [continueWatchingItems, seriesOverviews, watchlistItems, movieReleases, exploreCardPosition, shouldEnrichWatchStatus, isWatched, watchStatusItems, startupData?.continueWatchingTotal]);
  const trendingMovieTitles = useMemo(() => {
    const titlesWithReleases =
      trendingMovies?.map((item) => {
        // Merge cached release data if available
        const cachedReleases = movieReleases.get(item.title.id);
        return {
          ...item.title,
          uniqueKey: `movie:${item.title.id}`,
          theatricalRelease: item.title.theatricalRelease ?? cachedReleases?.theatricalRelease,
          homeRelease: item.title.homeRelease ?? cachedReleases?.homeRelease,
        };
      }) ?? [];
    // Enrich with watch status if badge is enabled
    const allTitles = shouldEnrichWatchStatus
      ? enrichWithWatchStatus(titlesWithReleases, isWatched, watchStatusItems, continueWatchingItems)
      : titlesWithReleases;
    if (allTitles.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allTitles;
    }
    const remainingCount = allTitles.length - MAX_SHELF_ITEMS_ON_HOME;
    // Pick posters from displayed items (deterministic)
    const collagePosters = pickRandomPosters(allTitles, (title) => title?.poster?.url, 4, 'trending-movies');
    const exploreTitle: Title & { uniqueKey: string; collagePosters?: string[]; displayYear?: string } = {
      id: `${EXPLORE_CARD_ID_PREFIX}trending-movies`,
      name: 'Explore',
      overview: `View all ${allTitles.length} items`,
      year: remainingCount,
      language: 'en',
      mediaType: 'explore',
      poster: {
        url: collagePosters[0] || '',
        type: 'poster',
        width: 0,
        height: 0,
      },
      uniqueKey: 'explore:trending-movies',
      collagePosters,
    };
    const limitedTitles = allTitles.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return exploreCardPosition === 'end' ? [...limitedTitles, exploreTitle] : [exploreTitle, ...limitedTitles];
  }, [trendingMovies, movieReleases, exploreCardPosition, shouldEnrichWatchStatus, isWatched, watchStatusItems, continueWatchingItems]);

  const trendingShowTitles = useMemo(() => {
    const baseTitles =
      trendingTVShows?.map((item) => ({
        ...item.title,
        uniqueKey: `show:${item.title.id}`,
      })) ?? [];
    // Enrich with watch status if badge is enabled
    const allTitles = shouldEnrichWatchStatus
      ? enrichWithWatchStatus(baseTitles, isWatched, watchStatusItems, continueWatchingItems)
      : baseTitles;
    if (allTitles.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allTitles;
    }
    const remainingCount = allTitles.length - MAX_SHELF_ITEMS_ON_HOME;
    // Pick posters from displayed items (deterministic)
    const collagePosters = pickRandomPosters(allTitles, (title) => title?.poster?.url, 4, 'trending-shows');
    const exploreTitle: Title & { uniqueKey: string; collagePosters?: string[] } = {
      id: `${EXPLORE_CARD_ID_PREFIX}trending-shows`,
      name: 'Explore',
      overview: `View all ${allTitles.length} items`,
      year: remainingCount,
      language: 'en',
      mediaType: 'explore',
      poster: {
        url: collagePosters[0] || '',
        type: 'poster',
        width: 0,
        height: 0,
      },
      uniqueKey: 'explore:trending-shows',
      collagePosters,
    };
    const limitedTitles = allTitles.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return exploreCardPosition === 'end' ? [...limitedTitles, exploreTitle] : [exploreTitle, ...limitedTitles];
  }, [trendingTVShows, exploreCardPosition, shouldEnrichWatchStatus, isWatched, watchStatusItems, continueWatchingItems]);

  const [focusedDesktopCard, setFocusedDesktopCard] = useState<CardData | null>(null);
  const [enrichedHeroData, setEnrichedHeroData] = useState<EnrichedHeroData | null>(null);
  const enrichedCacheRef = useRef<Map<string, EnrichedHeroData>>(new Map());
  const enrichedAbortRef = useRef<AbortController | null>(null);
  const [enrichedLoading, setEnrichedLoading] = useState(false);
  const heroContentOpacity = useSharedValue(0);
  const heroContentAnimStyle = useAnimatedStyle(() => ({
    opacity: heroContentOpacity.value,
  }));
  const [mobileHeroIndex, setMobileHeroIndex] = useState(0);

  // Ref for hero carousel ScrollView
  const heroScrollRef = useRef<ScrollView>(null);
  // Ref to store stable shuffled hero items (prevents re-shuffling on data load)
  const stableHeroItemsRef = useRef<CardData[]>([]);
  const heroItemKeysRef = useRef<Set<string>>(new Set());
  const isUserScrolling = useRef(false);

  // Use ref instead of state for focus tracking to avoid re-renders on every focus change
  const focusedShelfKeyRef = useRef<string | null>(null);
  // Track shelf card counts for boundary detection (updated via ref to avoid callback recreation)
  const shelfCardCountsRef = useRef<Record<string, number>>({});

  // TV: detect boundary presses (left at first card → open menu)
  const { reportFocusIndex } = useTVFocusBoundary({
    onBoundaryReached: (dir) => {
      if (dir === 'left' && !isMenuOpen && !profileSelectorVisible) openMenu();
    },
  });
  // heroImageDimensions removed — now computed inline in TVHero component

  // Remove from Continue Watching confirmation modal state
  const [isRemoveConfirmVisible, setIsRemoveConfirmVisible] = useState(false);
  const [pendingRemoveItem, setPendingRemoveItem] = useState<{ id: string; name: string } | null>(null);

  // TV focus refs and handles for Remove modal
  const removeCancelButtonRef = useRef<View>(null);
  const removeConfirmButtonRef = useRef<View>(null);
  const [removeCancelHandle, setRemoveCancelHandle] = useState<number | null>(null);
  const [removeConfirmHandle, setRemoveConfirmHandle] = useState<number | null>(null);

  // Version mismatch modal state
  const [isVersionMismatchVisible, setIsVersionMismatchVisible] = useState(false);
  const [backendVersion, setBackendVersion] = useState<string | null>(null);
  const versionCheckDoneRef = React.useRef(false);
  const versionMismatchButtonRef = useRef<View>(null);
  const [versionMismatchButtonHandle, setVersionMismatchButtonHandle] = useState<number | null>(null);

  // Check for version mismatch on app launch
  useEffect(() => {
    // Only check once per app session, and only when backend is reachable
    if (versionCheckDoneRef.current || !isBackendReachable) {
      return;
    }

    const checkVersion = async () => {
      try {
        const result = await apiService.getBackendVersion();
        if (result?.version) {
          setBackendVersion(result.version);
          // Compare versions - show modal if they don't match
          if (result.version !== APP_VERSION) {
            setIsVersionMismatchVisible(true);
          }
          versionCheckDoneRef.current = true;
        }
      } catch (err) {
        // Silently fail - version check is not critical
        console.log('[VersionCheck] Failed to fetch backend version:', err);
      }
    };

    checkVersion();
  }, [isBackendReachable]);

  // Set up focus handles for Remove modal when visible (TV platforms)
  useEffect(() => {
    if (isRemoveConfirmVisible && Platform.isTV) {
      const timer = setTimeout(() => {
        const cancelHandle = removeCancelButtonRef.current ? findNodeHandle(removeCancelButtonRef.current) : null;
        const confirmHandle = removeConfirmButtonRef.current ? findNodeHandle(removeConfirmButtonRef.current) : null;
        setRemoveCancelHandle(cancelHandle);
        setRemoveConfirmHandle(confirmHandle);
      }, 50);
      return () => clearTimeout(timer);
    } else {
      setRemoveCancelHandle(null);
      setRemoveConfirmHandle(null);
    }
  }, [isRemoveConfirmVisible]);

  // Set up focus handle for Version Mismatch modal when visible (TV platforms)
  useEffect(() => {
    if (isVersionMismatchVisible && Platform.isTV) {
      const timer = setTimeout(() => {
        const handle = versionMismatchButtonRef.current ? findNodeHandle(versionMismatchButtonRef.current) : null;
        setVersionMismatchButtonHandle(handle);
      }, 50);
      return () => clearTimeout(timer);
    } else {
      setVersionMismatchButtonHandle(null);
    }
  }, [isVersionMismatchVisible]);

  const handleDismissVersionMismatch = useCallback(() => {
    setIsVersionMismatchVisible(false);
  }, []);

  // heroImageDimensions effect removed — orientation is now computed inline in TVHero

  // Update focused card when cards array changes (e.g., episode progress updates)
  useEffect(() => {
    if (!focusedDesktopCard) {
      return;
    }

    // Find the updated version of the currently focused card
    // Match by stable ID (series ID without episode code)
    const focusedRawId = String(focusedDesktopCard.id);
    const focusedStableId = focusedRawId.includes(':S') ? focusedRawId.split(':S')[0] : focusedRawId;

    const allCards = [...continueWatchingCards, ...watchlistCards, ...trendingMovieCards, ...trendingShowCards];
    const updatedCard = allCards.find((card) => {
      const cardRawId = String(card.id);
      const cardStableId = cardRawId.includes(':S') ? cardRawId.split(':S')[0] : cardRawId;
      return cardStableId === focusedStableId;
    });

    if (updatedCard && updatedCard.description !== focusedDesktopCard.description) {
      // Card data has changed, update the focused card
      setFocusedDesktopCard(updatedCard);
    }
  }, [continueWatchingCards, watchlistCards, trendingMovieCards, trendingShowCards, focusedDesktopCard]);

  // Fetch enriched hero data (logo, ratings, genres) when focused card changes on TV
  useEffect(() => {
    if (!Platform.isTV || !focusedDesktopCard) {
      setEnrichedHeroData(null);
      setEnrichedLoading(false);
      return;
    }

    if (KILL_HERO_ENRICHMENT) { console.log('[KILL] Hero enrichment disabled'); return; }

    // Skip explore cards
    if (String(focusedDesktopCard.id).startsWith(EXPLORE_CARD_ID_PREFIX)) {
      setEnrichedHeroData(null);
      setEnrichedLoading(false);
      return;
    }

    const cacheKey = `${focusedDesktopCard.mediaType}:${focusedDesktopCard.tmdbId ?? focusedDesktopCard.id}`;

    // Check cache first — instant, no loading flash
    const cached = enrichedCacheRef.current.get(cacheKey);
    if (cached) {
      setEnrichedHeroData(cached);
      setEnrichedLoading(false);
      return;
    }

    // Mark loading — hero content hidden until fetch completes
    setEnrichedHeroData(null);
    setEnrichedLoading(true);

    // Abort previous in-flight fetch
    enrichedAbortRef.current?.abort();
    const controller = new AbortController();
    enrichedAbortRef.current = controller;

    const fetchDetails = async () => {
      try {
        const card = focusedDesktopCard;
        const params = {
          tmdbId: card.tmdbId,
          tvdbId: card.tvdbId,
          imdbId: card.imdbId,
          name: card.title,
          year: card.year ? Number(card.year) : undefined,
        };

        let title: Title | undefined;

        if (card.mediaType === 'series' || card.mediaType === 'tv') {
          const result = await apiService.getSeriesDetails(params);
          title = result?.title;
        } else if (card.mediaType === 'movie') {
          title = await apiService.getMovieDetails(params);
        }

        if (controller.signal.aborted || !title) return;

        // Sort ratings by preferred order, take top 3
        const sortedRatings = [...(title.ratings ?? [])].sort((a, b) => {
          return (RATING_ORDER[a.source] ?? 99) - (RATING_ORDER[b.source] ?? 99);
        }).slice(0, 3);

        // Pick an alternate backdrop different from what the card already shows
        const cardBackdrop = focusedDesktopCard.backdropUrl || focusedDesktopCard.headerImage;
        const allBackdrops = [
          ...(title.backdrops ?? []),
          ...(title.backdrop ? [title.backdrop] : []),
        ];
        const alternateBackdrop = allBackdrops.find((b) => b.url !== cardBackdrop)?.url;

        const enriched: EnrichedHeroData = {
          logoUrl: title.logo?.url,
          isLogoDark: title.logo?.is_dark,
          ratings: sortedRatings.length > 0 ? sortedRatings : undefined,
          genres: title.genres?.slice(0, 3),
          certification: title.certification,
          backdropUrl: alternateBackdrop,
        };

        // Store in cache with size cap
        enrichedCacheRef.current.set(cacheKey, enriched);
        if (enrichedCacheRef.current.size > MAX_ENRICHED_CACHE) {
          const firstKey = enrichedCacheRef.current.keys().next().value;
          if (firstKey) enrichedCacheRef.current.delete(firstKey);
        }

        if (!controller.signal.aborted) {
          setEnrichedHeroData(enriched);
          setEnrichedLoading(false);
        }
      } catch (err) {
        // Fetch failed — show fallback title/year/overview
        if (!controller.signal.aborted) {
          console.log('[HeroEnrich] Failed to fetch details:', err);
          setEnrichedLoading(false);
        }
      }
    };

    fetchDetails();

    return () => {
      controller.abort();
    };
  }, [focusedDesktopCard]);

  // Animate hero content opacity — instant hide when loading, smooth fade in when ready
  useEffect(() => {
    if (enrichedLoading || !focusedDesktopCard) {
      heroContentOpacity.value = 0; // Instant hide — no animation delay
    } else {
      heroContentOpacity.value = withTiming(1, { duration: 300 });
    }
  }, [enrichedLoading, focusedDesktopCard]);

  // Debounce ref for hero updates
  const focusDebounceRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);

  // Create array of hero items for mobile rotation
  // Uses stable ordering - only shuffles new items, doesn't reshuffle on data reload
  const mobileHeroItems = useMemo<CardData[]>(() => {
    const newItems: CardData[] = [];

    const addCards = (cards: CardData[]) => {
      for (const card of cards) {
        if (!card.backdropUrl) {
          continue;
        }
        const key = getHeroCardKey(card);
        if (heroItemKeysRef.current.has(key)) {
          continue;
        }
        heroItemKeysRef.current.add(key);
        newItems.push(card);
      }
    };

    addCards(continueWatchingCards);
    addCards(watchlistCards);
    addCards(trendingMovieCards);
    addCards(trendingShowCards);

    // If we have new items, shuffle only them and append to existing stable list
    if (newItems.length > 0) {
      const shuffledNew = shuffleArray(newItems);
      stableHeroItemsRef.current = [...stableHeroItemsRef.current, ...shuffledNew].slice(0, MAX_HERO_ITEMS);
    }

    // Cap the keys set to prevent unbounded memory growth
    capSetSize(heroItemKeysRef.current, MAX_HERO_KEYS_CACHE);

    return stableHeroItemsRef.current;
  }, [continueWatchingCards, watchlistCards, trendingMovieCards, trendingShowCards]);

  const heroSource = useMemo<HeroContent>(() => {
    // For mobile, use rotating hero items
    if (shouldUseMobileLayout && mobileHeroItems.length > 0) {
      const item = mobileHeroItems[mobileHeroIndex % mobileHeroItems.length];
      const description = item.seriesOverview ?? item.description;
      return {
        title: item.title,
        description,
        headerImage: item.backdropUrl ?? item.headerImage,
      };
    }

    // For desktop, use focused card or first available (excluding continue watching)
    const candidate = focusedDesktopCard ?? watchlistCards[0] ?? trendingMovieCards[0] ?? trendingShowCards[0];

    if (candidate) {
      const description = candidate.seriesOverview ?? candidate.description;
      return {
        title: candidate.title,
        description,
        headerImage: candidate.headerImage,
      };
    }

    // Show connection status message when no content is available
    const hasContent = false; // We're in the fallback path, so no content
    const statusMessage = getConnectionStatusMessage(retryCountdown, isBackendReachable, settingsLoading, hasContent);
    return {
      ...HERO_PLACEHOLDER,
      description: statusMessage,
    };
  }, [
    shouldUseMobileLayout,
    mobileHeroItems,
    mobileHeroIndex,
    focusedDesktopCard,
    watchlistCards,
    trendingMovieCards,
    trendingShowCards,
    retryCountdown,
    isBackendReachable,
    settingsLoading,
  ]);

  // Calculate hero width for carousel scrolling
  // Phones: 90% of screen width, Tablets: 70% portrait / 60% landscape
  const heroGap = theme.spacing.md;
  const isLandscape = screenWidth > screenHeight;
  const heroWidthPercent = isTablet ? (isLandscape ? 0.6 : 0.7) : 0.9;
  const heroWidth = Math.round(screenWidth * heroWidthPercent);
  const heroSnapInterval = heroWidth + heroGap;
  // Padding to center the first/last items
  const heroPadding = (screenWidth - heroWidth) / 2;

  // Handle hero carousel scroll
  const handleHeroScroll = useCallback(
    (event: NativeSyntheticEvent<NativeScrollEvent>) => {
      const offsetX = event.nativeEvent.contentOffset.x;
      const newIndex = Math.round(offsetX / heroSnapInterval);
      if (newIndex !== mobileHeroIndex && newIndex >= 0 && newIndex < mobileHeroItems.length) {
        isUserScrolling.current = true;
        setMobileHeroIndex(newIndex);
        // Reset flag after a short delay
        setTimeout(() => {
          isUserScrolling.current = false;
        }, 100);
      }
    },
    [heroSnapInterval, mobileHeroIndex, mobileHeroItems.length],
  );

  // Auto-rotate hero on mobile
  useEffect(() => {
    if (!shouldUseMobileLayout || mobileHeroItems.length <= 1 || !focused) {
      return;
    }

    const interval = setInterval(() => {
      if (isUserScrolling.current) return; // Don't auto-rotate while user is swiping
      setMobileHeroIndex((prev) => {
        const nextIndex = (prev + 1) % mobileHeroItems.length;
        // Scroll to next item
        heroScrollRef.current?.scrollTo({ x: nextIndex * heroSnapInterval, animated: true });
        return nextIndex;
      });
    }, 5000); // Rotate every 5 seconds

    return () => clearInterval(interval);
  }, [shouldUseMobileLayout, mobileHeroItems.length, focused, heroSnapInterval]);

  // Re-align hero scroll position when screen dimensions change (e.g., rotation)
  useEffect(() => {
    if (!shouldUseMobileLayout || mobileHeroItems.length === 0) {
      return;
    }
    // Scroll to current index position with new dimensions (no animation to avoid jarring effect)
    heroScrollRef.current?.scrollTo({ x: mobileHeroIndex * heroSnapInterval, animated: false });
  }, [heroSnapInterval]); // Only trigger on dimension changes, not index changes

  const handleCardSelect = useCallback(
    (card: CardData) => {
      const _navStart = Date.now();
      console.log(`[NAV TIMING] handleCardSelect fired at ${_navStart} for "${card.title}" (${card.id})`);
      // Handle explore cards
      if (typeof card.id === 'string' && card.id.startsWith(EXPLORE_CARD_ID_PREFIX)) {
        const shelfId = card.id.replace(EXPLORE_CARD_ID_PREFIX, '');
        // All explore cards now navigate to watchlist with shelf parameter
        router.push(shelfId === 'watchlist' ? '/watchlist' : `/watchlist?shelf=${shelfId}`);
        return;
      }

      // Check if this is a continue watching item
      // For series: ID format is "tmdb:tv:127235:S03E09" (has episode code)
      // For movies: ID format is just "tmdb:movie:1571470" (no episode code)
      const isContinueWatchingSeries =
        card.mediaType === 'series' && typeof card.id === 'string' && card.id.includes(':S');
      const isContinueWatchingMovie =
        card.mediaType === 'movie' && continueWatchingItems?.some((state) => state.seriesId === String(card.id));
      const isContinueWatching = isContinueWatchingSeries || isContinueWatchingMovie;

      const metadata = isContinueWatchingSeries
        ? continueWatchingItems?.find((state) => {
            // Card ID format: "tmdb:tv:127235:S03E09"
            // Series ID format: "tmdb:tv:127235"
            // Remove the episode code (":S03E09") from the end
            const cardIdWithoutEpisode = String(card.id).replace(/:S\d{2}E\d{2}$/i, '');
            return state.seriesId === cardIdWithoutEpisode;
          })
        : isContinueWatchingMovie
          ? continueWatchingItems?.find((state) => state.seriesId === String(card.id))
          : null;

      // Try to find series overview from card (pre-fetched), cache, watchlist, or fallback to card description
      const seriesId = metadata?.seriesId ?? String(card.id ?? '');
      const cachedOverview = seriesOverviews.get(seriesId);
      const watchlistItem = watchlistItems?.find((item) => item.id === seriesId);
      // Prioritize: card.seriesOverview (pre-fetched) > cached > watchlist > card.description (episode info - least preferred)
      const seriesOverview = card.seriesOverview ?? cachedOverview ?? watchlistItem?.overview;

      // Ensure proper fallback for backdrop (prefer landscape) and poster (prefer portrait)
      const headerImage = card.backdropUrl || card.posterUrl || card.headerImage || '';
      const posterUrl = card.posterUrl || '';
      const backdropUrl = card.backdropUrl || '';

      const params = {
        title: metadata?.seriesTitle ?? card.title,
        titleId: seriesId,
        mediaType: card.mediaType ?? 'movie',
        description: seriesOverview ?? card.description ?? '',
        headerImage,
        posterUrl,
        backdropUrl,
        tmdbId: card.tmdbId ? String(card.tmdbId) : '',
        imdbId: card.imdbId ?? '',
        tvdbId: card.tvdbId ? String(card.tvdbId) : '',
        year: card.year ? String(card.year) : '',
        initialSeason: metadata?.nextEpisode ? String(metadata.nextEpisode.seasonNumber ?? '') : '',
        initialEpisode: metadata?.nextEpisode ? String(metadata.nextEpisode.episodeNumber ?? '') : '',
      };

      console.log(`[NAV TIMING] handleCardSelect calling router.push at +${Date.now() - _navStart}ms`);
      markNavStart();
      router.push({
        pathname: '/details',
        params,
      });
      console.log(`[NAV TIMING] handleCardSelect router.push returned at +${Date.now() - _navStart}ms`);
    },
    [router, continueWatchingItems, watchlistItems, seriesOverviews],
  );

  const handleTitlePress = useCallback(
    (item: Title) => {
      const _navStart = Date.now();
      console.log(`[NAV TIMING] handleTitlePress fired at ${_navStart} for "${item.name}" (${item.id})`);
      // Handle explore cards
      if (typeof item.id === 'string' && item.id.startsWith(EXPLORE_CARD_ID_PREFIX)) {
        const shelfId = item.id.replace(EXPLORE_CARD_ID_PREFIX, '');
        // All explore cards now navigate to watchlist with shelf parameter
        router.push(shelfId === 'watchlist' ? '/watchlist' : `/watchlist?shelf=${shelfId}`);
        return;
      }

      // For TV shows, check if there's continue watching progress to use
      const isTVShow = item.mediaType === 'series' || item.mediaType === 'tv' || item.mediaType === 'show';
      let initialSeason = '';
      let initialEpisode = '';

      if (isTVShow && item.id) {
        // Check if this show has continue watching data
        const continueWatchingMetadata = continueWatchingItems?.find((state) => state.seriesId === item.id);
        if (continueWatchingMetadata?.nextEpisode) {
          initialSeason = String(continueWatchingMetadata.nextEpisode.seasonNumber ?? '');
          initialEpisode = String(continueWatchingMetadata.nextEpisode.episodeNumber ?? '');
        } else {
          // Default to S01E01 for TV shows with no progress
          initialSeason = '1';
          initialEpisode = '1';
        }
      }

      console.log(`[NAV TIMING] handleTitlePress calling router.push at +${Date.now() - _navStart}ms`);
      markNavStart();
      router.push({
        pathname: '/details',
        params: {
          title: item.name,
          titleId: item.id ?? '',
          mediaType: item.mediaType ?? 'movie',
          description: item.overview ?? '',
          headerImage: item.backdrop?.url ?? item.poster?.url ?? '',
          posterUrl: item.poster?.url ?? '',
          backdropUrl: item.backdrop?.url ?? '',
          tmdbId: item.tmdbId ? String(item.tmdbId) : '',
          imdbId: item.imdbId ?? '',
          tvdbId: item.tvdbId ? String(item.tvdbId) : '',
          year: item.year ? String(item.year) : '',
          initialSeason,
          initialEpisode,
        },
      });
      console.log(`[NAV TIMING] handleTitlePress router.push returned at +${Date.now() - _navStart}ms`);
    },
    [router, continueWatchingItems],
  );

  const handleContinueWatchingPress = useCallback(
    (item: Title) => {
      const _navStart = Date.now();
      console.log(`[NAV TIMING] handleContinueWatchingPress fired at ${_navStart} for "${item.name}" (${item.id})`);
      const metadata = continueWatchingItems?.find((state) => state.seriesId === item.id);
      if (!metadata || !metadata.nextEpisode) {
        handleTitlePress(item);
        return;
      }

      // Use the same fallback logic as the card data - prefer backdrop for landscape hero
      const headerImage = metadata.backdropUrl || metadata.posterUrl || '';
      const posterUrl = metadata.posterUrl || '';
      const backdropUrl = metadata.backdropUrl || '';

      console.log(`[NAV TIMING] handleContinueWatchingPress calling router.push at +${Date.now() - _navStart}ms`);
      markNavStart();
      router.push({
        pathname: '/details',
        params: {
          title: metadata.seriesTitle,
          titleId: metadata.seriesId,
          mediaType: 'series',
          description: item.overview ?? '',
          headerImage,
          posterUrl,
          backdropUrl,
          imdbId: item.imdbId ?? '',
          tmdbId: item.tmdbId ? String(item.tmdbId) : '',
          tvdbId: item.tvdbId ? String(item.tvdbId) : '',
          initialSeason: String(metadata.nextEpisode.seasonNumber ?? ''),
          initialEpisode: String(metadata.nextEpisode.episodeNumber ?? ''),
        },
      });
    },
    [continueWatchingItems, handleTitlePress, router],
  );

  const handleContinueWatchingLongPress = useCallback((item: Title) => {
    // Extract the series ID from the item
    // For continue watching items, the id is either the seriesId directly (for movies)
    // or "seriesId:S01E02" format (for series with next episode)
    const seriesId = String(item.id).split(':S')[0];

    // Show confirmation modal instead of immediately removing
    setPendingRemoveItem({ id: seriesId, name: item.name });
    setIsRemoveConfirmVisible(true);
  }, []);

  // Handle confirmation of removal from Continue Watching
  const handleConfirmRemove = useCallback(() => {
    if (!pendingRemoveItem) return;

    hideFromContinueWatching(pendingRemoveItem.id)
      .then(() => {
        showToast('Removed from Continue Watching', {
          tone: 'success',
          duration: 3000,
        });
      })
      .catch(() => {
        showToast('Failed to remove from Continue Watching', {
          tone: 'danger',
          duration: 3000,
        });
      });

    setIsRemoveConfirmVisible(false);
    setPendingRemoveItem(null);
  }, [pendingRemoveItem, hideFromContinueWatching, showToast]);

  // Handle cancellation of removal
  const handleCancelRemove = useCallback(() => {
    setIsRemoveConfirmVisible(false);
    setPendingRemoveItem(null);
  }, []);

  // Handle back button for Remove modal (TV platforms)
  useEffect(() => {
    if (!isRemoveConfirmVisible || !Platform.isTV) return;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      handleCancelRemove();
      return true;
    });

    return () => {
      removeInterceptor();
    };
  }, [isRemoveConfirmVisible, handleCancelRemove]);

  // TV: Listen for LongEnter to remove items from Continue Watching
  useEffect(() => {
    if (!Platform.isTV || !focused) {
      return;
    }

    const handleLongEnter = (key: SupportedKeys) => {
      if (key !== SupportedKeys.LongEnter) {
        return;
      }

      // Only handle when a continue watching item is focused
      if (focusedShelfKeyRef.current !== 'continue-watching' || !focusedDesktopCard) {
        return;
      }

      // Extract the series ID from the card
      // For series: ID format is "tmdb:tv:127235:S03E09" (has episode code)
      // For movies: ID format is just "tmdb:movie:1571470" (no episode code)
      const cardId = String(focusedDesktopCard.id);
      const seriesId = cardId.includes(':S') ? cardId.split(':S')[0] : cardId;

      // Show confirmation modal instead of immediately removing
      setPendingRemoveItem({ id: seriesId, name: focusedDesktopCard.title });
      setIsRemoveConfirmVisible(true);
    };

    RemoteControlManager.addKeydownListener(handleLongEnter);

    return () => {
      RemoteControlManager.removeKeydownListener(handleLongEnter);
    };
  }, [focused, focusedDesktopCard]);

  // Consolidated focus handler - called when any shelf card receives focus
  // Combines: menu close, shelf tracking, vertical scroll, and debounced hero update
  const handleShelfItemFocus = useCallback(
    (card: CardData, shelfKey: string, cardIndex: number): void => {
      const isVertical = focusedShelfKeyRef.current !== shelfKey;

      // Report focus position for boundary detection (left-at-first → open menu)
      const shelfTotal = shelfCardCountsRef.current[shelfKey] ?? 0;
      reportFocusIndex(cardIndex, shelfTotal);

      // Close menu if open (no-op if already closed)
      closeMenu();

      // Only scroll vertically if shelf changed (skip on horizontal navigation)
      const previousShelfKey = focusedShelfKeyRef.current;
      focusedShelfKeyRef.current = shelfKey;
      if (previousShelfKey !== shelfKey) {
        scrollToShelf(shelfKey);
      }

      // Debounced hero update - only update after focus settles
      // Use longer debounce for vertical navigation to avoid hero image decode
      // stalling frames mid-scroll-animation
      if (focusDebounceRef.current) {
        clearTimeout(focusDebounceRef.current);
      }

      const debounceMs = isAndroidTV
        ? (isVertical ? 400 : 100)  // Vertical: defer past scroll animation; Horizontal: quick
        : 350;

      // Fade out over the debounce period — invisible by the time content swaps
      heroContentOpacity.value = withTiming(0, { duration: debounceMs });
      focusDebounceRef.current = setTimeout(() => {
        // Batch both updates in same render so content swaps while invisible
        setEnrichedLoading(true);
        setFocusedDesktopCard(card);
      }, debounceMs);
    },
    [closeMenu, scrollToShelf, heroContentOpacity, reportFocusIndex],
  );

  // These callbacks do actual work in production, not just logging
  const handleDesktopScrollLayout = useCallback(
    (event: LayoutChangeEvent) => {
      if (__DEV__ && Platform.OS === 'ios') {
        console.log('[SafeArea] Home desktop ScrollView layout', event.nativeEvent.layout);
      }
      scrollMetricsRef.current.viewportHeight = event.nativeEvent.layout.height;
    },
    [scrollMetricsRef],
  );

  const lastContentHeightRef = useRef(0);
  const handleDesktopContentSizeChange = useCallback((_width: number, height: number) => {
    // Only invalidate cached shelf positions when content height changes significantly
    // (e.g., a shelf expanding/collapsing). Minor changes from badge updates or text
    // reflow don't meaningfully shift shelf positions and would force expensive
    // measureLayout calls on the next vertical navigation.
    if (Platform.isTV && Math.abs(height - lastContentHeightRef.current) > 100) {
      shelfPositionsRef.current = {};
    }
    lastContentHeightRef.current = height;
  }, []);

  // Desktop shelves configuration - moved before conditional return to satisfy React hooks rules
  const desktopShelves = useMemo(() => {
    // Skip computation for mobile layout
    if (shouldUseMobileLayout) return [];
    if (KILL_ALL_SHELVES) { console.log('[KILL] All shelves disabled'); return []; }
    if (DEBUG_INDEX_RENDERS) {
      console.log(
        `[IndexPage] useMemo: desktopShelves recomputing - CW:${continueWatchingCards.length} WL:${watchlistCards.length} TM:${trendingMovieCards.length} TS:${trendingShowCards.length}`,
      );
    }

    // Get shelf configuration from user settings, fall back to global settings, then default order
    let shelfConfig = userSettings?.homeShelves?.shelves ??
      settings?.homeShelves?.shelves ?? [
        { id: 'continue-watching', name: 'Continue Watching', enabled: true, order: 0 },
        { id: 'watchlist', name: 'Your Watchlist', enabled: true, order: 1 },
        { id: 'trending-movies', name: 'Trending Movies', enabled: true, order: 2 },
        { id: 'trending-tv', name: 'Trending TV Shows', enabled: true, order: 3 },
      ];

    // In kids curated mode, add kids allowed lists and exclude trending shelves
    if (isKidsCuratedMode) {
      // Add kids allowed lists as virtual shelves
      shelfConfig = [...shelfConfig, ...kidsAllowedShelves];
    }

    // Map shelf IDs to their data
    const shelfDataMap: Record<
      string,
      {
        cards: CardData[];
        autoFocus: boolean;
        collapseIfEmpty: boolean;
        showEmptyState: boolean;
        cardLayout?: 'portrait' | 'landscape';
      }
    > = {
      'continue-watching': {
        cards: continueWatchingCards,
        autoFocus: true,
        collapseIfEmpty: true,
        showEmptyState: continueWatchingLoading,
        cardLayout: 'landscape',
      },
      watchlist: {
        cards: watchlistCards,
        autoFocus: false,
        collapseIfEmpty: true,
        showEmptyState: watchlistLoading,
      },
      'trending-movies': {
        cards: trendingMovieCards,
        autoFocus: false,
        collapseIfEmpty: false,
        showEmptyState: true,
      },
      'trending-tv': {
        cards: trendingShowCards,
        autoFocus: false,
        collapseIfEmpty: false,
        showEmptyState: true,
      },
    };

    // Add custom list shelves to the map (include all custom shelves and kids shelves)
    for (const config of shelfConfig) {
      if (config.type === 'mdblist' && config.listUrl) {
        shelfDataMap[config.id] = {
          cards: customListCards[config.id] ?? [],
          autoFocus: false,
          collapseIfEmpty: true,
          showEmptyState: customListLoading[config.id] ?? true,
        };
      }
    }

    // Build shelves based on configuration
    const shelves = shelfConfig
      .filter((config) => {
        if (!config.enabled) return false;
        // In kids curated mode, exclude trending shelves (unrestricted content)
        if (isKidsCuratedMode && (config.id === 'trending-movies' || config.id === 'trending-tv')) {
          return false;
        }
        return true;
      })
      .sort((a, b) => a.order - b.order)
      .map((config, index) => {
        const data = shelfDataMap[config.id];
        if (!data) {
          return null;
        }
        const cards = DEBUG_MAX_CARDS_PER_SHELF > 0 ? data.cards.slice(0, DEBUG_MAX_CARDS_PER_SHELF) : data.cards;
        return {
          key: config.id,
          title: config.name,
          cards,
          autoFocus: index === 0 && cards.length > 0,
          collapseIfEmpty: data.collapseIfEmpty,
          showEmptyState: data.showEmptyState,
          cardLayout: data.cardLayout,
        };
      })
      .filter((shelf): shelf is NonNullable<typeof shelf> => shelf !== null);

    if (DEBUG_MAX_CARDS_PER_SHELF > 0) {
      console.log(`[DEBUG] Card cap: ${DEBUG_MAX_CARDS_PER_SHELF}/shelf, shelves: ${shelves.map(s => `${s.key}(${s.cards.length})`).join(', ')}`);
    }
    return shelves;
  }, [
    shouldUseMobileLayout,
    settings,
    userSettings,
    continueWatchingCards,
    continueWatchingLoading,
    trendingMovieCards,
    trendingShowCards,
    watchlistCards,
    watchlistLoading,
    customListCards,
    customListLoading,
    isKidsCuratedMode,
    kidsAllowedShelves,
  ]);

  // Keep shelf card counts in sync for boundary detection (ref avoids callback recreation)
  useEffect(() => {
    const counts: Record<string, number> = {};
    for (const shelf of desktopShelves) {
      counts[shelf.key] = shelf.cards.length;
    }
    shelfCardCountsRef.current = counts;
  }, [desktopShelves]);

  // Track navigation structure changes - only based on which shelves exist, NOT card counts
  // Using card counts in the key was causing full remounts on every data load
  const navigationKey = useMemo(() => {
    if (!desktopShelves || desktopShelves.length === 0) return 'shelves-empty';
    // Only include shelf keys, not card counts - data changes shouldn't cause remounts
    return `shelves-${desktopShelves.map((s) => s.key).join('-')}`;
  }, [desktopShelves]);

  // Set initial focused card for TV hero display on first load
  // hasTVPreferredFocus grants focus but doesn't trigger onFocus callback, so we set it manually
  useEffect(() => {
    if (!Platform.isTV || focusedDesktopCard) {
      return;
    }
    // Find the first shelf with autoFocus (first shelf with cards)
    const autoFocusShelf = desktopShelves.find((shelf) => shelf.autoFocus && shelf.cards.length > 0);
    if (autoFocusShelf) {
      setFocusedDesktopCard(autoFocusShelf.cards[0]);
      focusedShelfKeyRef.current = autoFocusShelf.key;
    }
  }, [desktopShelves, focusedDesktopCard]);

  // Note: focusedShelfIndex computation removed - was unused (_shouldShowTopGradient)
  // The ref focusedShelfKeyRef.current can be read synchronously if needed

  if (shouldUseMobileLayout) {
    if (!mobileStyles) {
      return <View style={{ flex: 1, backgroundColor: theme.colors.background.base }} />;
    }

    // Get shelf configuration from user settings, fall back to global settings, then default order
    let mobileShelfConfig = userSettings?.homeShelves?.shelves ??
      settings?.homeShelves?.shelves ?? [
        { id: 'continue-watching', name: 'Continue Watching', enabled: true, order: 0 },
        { id: 'watchlist', name: 'Your Watchlist', enabled: true, order: 1 },
        { id: 'trending-movies', name: 'Trending Movies', enabled: true, order: 2 },
        { id: 'trending-tv', name: 'Trending TV Shows', enabled: true, order: 3 },
      ];

    // In kids curated mode, add kids allowed lists
    if (isKidsCuratedMode && kidsAllowedShelves.length > 0) {
      mobileShelfConfig = [...mobileShelfConfig, ...kidsAllowedShelves];
    }

    // Map shelf IDs to their data for mobile
    const mobileShelfDataMap: Record<
      string,
      {
        titles: Array<Title & { uniqueKey: string }>;
        loading?: boolean;
        onItemPress: (item: Title) => void;
        onItemLongPress?: (item: Title) => void;
        cardLayout?: 'portrait' | 'landscape';
      }
    > = {
      'continue-watching': {
        titles: continueWatchingTitles,
        loading: continueWatchingLoading,
        onItemPress: handleContinueWatchingPress,
        onItemLongPress: handleContinueWatchingLongPress,
        cardLayout: 'landscape',
      },
      watchlist: {
        titles: watchlistTitles,
        loading: watchlistLoading,
        onItemPress: handleTitlePress,
      },
      'trending-movies': {
        titles: trendingMovieTitles,
        onItemPress: handleTitlePress,
      },
      'trending-tv': {
        titles: trendingShowTitles,
        onItemPress: handleTitlePress,
      },
    };

    // Add custom list shelves to the mobile map (include all custom shelves and kids shelves)
    for (const config of mobileShelfConfig) {
      if (config.type === 'mdblist' && config.listUrl) {
        mobileShelfDataMap[config.id] = {
          titles: customListTitles[config.id] ?? [],
          loading: customListLoading[config.id],
          onItemPress: handleTitlePress,
        };
      }
    }

    return (
      <>
        <Stack.Screen options={{ headerShown: false }} />
        <FixedSafeAreaView style={mobileStyles.safeArea} edges={['top']}>
          <ScrollView
            style={mobileStyles.container}
            contentContainerStyle={mobileStyles.content}
            contentInsetAdjustmentBehavior="never"
            automaticallyAdjustContentInsets={false}
            removeClippedSubviews={Platform.OS === 'android'}>
            <View style={mobileStyles.heroContainer}>
              {mobileHeroItems.length > 1 ? (
                <>
                  <ScrollView
                    ref={heroScrollRef}
                    horizontal
                    showsHorizontalScrollIndicator={false}
                    onMomentumScrollEnd={handleHeroScroll}
                    scrollEventThrottle={16}
                    snapToInterval={heroSnapInterval}
                    snapToAlignment="start"
                    decelerationRate="fast"
                    removeClippedSubviews={Platform.OS === 'android'}
                    contentContainerStyle={{ paddingHorizontal: heroPadding }}>
                    {mobileHeroItems.map((item, index) => (
                      <Pressable
                        key={`hero-${item.id}-${index}`}
                        style={[mobileStyles.hero, { width: heroWidth, marginRight: heroGap }]}
                        onPress={() => handleCardSelect(item)}>
                        <Image
                          source={item.backdropUrl || item.headerImage}
                          style={mobileStyles.heroImage}
                          contentFit="cover"
                        />
                        <LinearGradient
                          colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.6)', 'rgba(0,0,0,0.95)']}
                          locations={[0, 0.6, 1]}
                          start={{ x: 0, y: 0 }}
                          end={{ x: 0, y: 1 }}
                          style={mobileStyles.heroGradient}
                        />
                        <View style={mobileStyles.heroTextContainer}>
                          <Text style={mobileStyles.heroTitle} numberOfLines={2}>
                            {item.title}
                          </Text>
                          <Text style={mobileStyles.heroDescription} numberOfLines={3}>
                            {item.seriesOverview ?? item.description}
                          </Text>
                        </View>
                      </Pressable>
                    ))}
                  </ScrollView>
                  {/* Pagination dots */}
                  <View style={mobileStyles.heroPagination}>
                    {mobileHeroItems.map((_, index) => (
                      <Pressable
                        key={index}
                        style={[mobileStyles.heroDot, index === mobileHeroIndex && mobileStyles.heroDotActive]}
                        onPress={() => {
                          setMobileHeroIndex(index);
                          heroScrollRef.current?.scrollTo({ x: index * heroSnapInterval, animated: true });
                        }}
                        hitSlop={{ top: 10, bottom: 10, left: 5, right: 5 }}
                      />
                    ))}
                  </View>
                </>
              ) : (
                // Fallback for 0 or 1 hero items - show single hero without carousel
                <Pressable
                  style={[mobileStyles.hero, mobileStyles.heroSingle]}
                  onPress={() => {
                    const currentHeroItem = mobileHeroItems[0];
                    if (currentHeroItem) {
                      handleCardSelect(currentHeroItem);
                    }
                  }}>
                  <Image source={heroSource.headerImage} style={mobileStyles.heroImage} contentFit="cover" />
                  <LinearGradient
                    colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.6)', 'rgba(0,0,0,0.95)']}
                    locations={[0, 0.6, 1]}
                    start={{ x: 0, y: 0 }}
                    end={{ x: 0, y: 1 }}
                    style={mobileStyles.heroGradient}
                  />
                  <View style={mobileStyles.heroTextContainer}>
                    <Text style={mobileStyles.heroTitle} numberOfLines={2}>
                      {heroSource.title}
                    </Text>
                    <Text style={mobileStyles.heroDescription} numberOfLines={3}>
                      {heroSource.description}
                    </Text>
                  </View>
                </Pressable>
              )}
            </View>

            {mobileShelfConfig
              .filter((config) => {
                if (!config.enabled) return false;
                // In kids curated mode, exclude trending shelves (unrestricted content)
                if (isKidsCuratedMode && (config.id === 'trending-movies' || config.id === 'trending-tv')) {
                  return false;
                }
                return true;
              })
              .sort((a, b) => a.order - b.order)
              .map((config) => {
                const data = mobileShelfDataMap[config.id];
                if (!data) {
                  return null;
                }
                const shouldShow = data.loading || data.titles.length > 0;
                if (!shouldShow) {
                  return null;
                }
                return (
                  <View key={config.id} style={mobileStyles.section}>
                    <MediaGrid
                      title={config.name}
                      items={data.titles}
                      loading={data.loading}
                      onItemPress={data.onItemPress}
                      onItemLongPress={data.onItemLongPress}
                      badgeVisibility={badgeVisibility}
                      watchStateIconStyle={watchStateIconStyle}
                      cardLayout={data.cardLayout}
                    />
                  </View>
                );
              })}
          </ScrollView>
        </FixedSafeAreaView>

        {/* Remove from Continue Watching Confirmation Modal (Mobile) */}
        <Modal
          visible={isRemoveConfirmVisible}
          transparent={true}
          animationType="fade"
          onRequestClose={handleCancelRemove}>
          <View style={mobileStyles.modalOverlay}>
            <View style={mobileStyles.modalContainer}>
              <Text style={mobileStyles.modalTitle}>Remove from Continue Watching?</Text>
              <Text style={mobileStyles.modalSubtitle}>
                Are you sure you want to remove "{pendingRemoveItem?.name}" from Continue Watching?
              </Text>
              <View style={mobileStyles.modalActions}>
                <Pressable onPress={handleCancelRemove} style={mobileStyles.modalButton}>
                  <Text style={mobileStyles.modalButtonText}>Cancel</Text>
                </Pressable>
                <Pressable
                  onPress={handleConfirmRemove}
                  style={[mobileStyles.modalButton, mobileStyles.modalButtonDanger]}>
                  <Text style={[mobileStyles.modalButtonText, mobileStyles.modalButtonDangerText]}>Remove</Text>
                </Pressable>
              </View>
            </View>
          </View>
        </Modal>

        {/* Version Mismatch Warning Modal (Mobile) */}
        <Modal
          visible={isVersionMismatchVisible}
          transparent={true}
          animationType="fade"
          onRequestClose={handleDismissVersionMismatch}>
          <View style={mobileStyles.modalOverlay}>
            <View style={mobileStyles.modalContainer}>
              <Text style={mobileStyles.modalTitle}>Version Mismatch</Text>
              <Text style={mobileStyles.modalSubtitle}>
                Frontend version ({APP_VERSION}) does not match backend version ({backendVersion ?? 'unknown'}). You may
                experience unexpected behavior. Consider updating.
              </Text>
              <View style={mobileStyles.modalActions}>
                <Pressable onPress={handleDismissVersionMismatch} style={mobileStyles.modalButton}>
                  <Text style={mobileStyles.modalButtonText}>OK</Text>
                </Pressable>
              </View>
            </View>
          </View>
        </Modal>
      </>
    );
  }

  if (!desktopStyles) {
    return <View style={{ flex: 1, backgroundColor: theme.colors.background.base }} />;
  }

  const desktopContent = (
    <View
      ref={pageRef}
      style={desktopStyles?.styles.page}
      accessibilityElementsHidden={isMenuOpen}
      importantForAccessibility={isMenuOpen ? 'no-hide-descendants' : 'auto'}>
      {/* Full-screen background art for TV — crossfades as user navigates between items */}
      {Platform.isTV && focusedDesktopCard && (
        <Animated.View style={[desktopStyles?.styles.tvBackgroundArt, heroContentAnimStyle]} pointerEvents="none">
          <Image
            source={enrichedHeroData?.backdropUrl || focusedDesktopCard.backdropUrl || focusedDesktopCard.headerImage}
            style={StyleSheet.absoluteFill}
            contentFit="cover"
            transition={500}
          />
          {/* Bottom fade to background base */}
          <LinearGradient
            colors={['transparent', 'rgba(11,11,15,0.1)', 'rgba(11,11,15,0.45)', '#0b0b0f']}
            locations={[0, 0.6, 0.85, 1]}
            start={{ x: 0, y: 0 }}
            end={{ x: 0, y: 1 }}
            style={desktopStyles?.styles.tvBackgroundOverlay}
          />
          {/* Left edge fade to background base */}
          <LinearGradient
            colors={['#0b0b0f', 'rgba(11,11,15,0.3)', 'rgba(11,11,15,0.1)', 'transparent']}
            locations={[0, 0.08, 0.25, 0.5]}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 0 }}
            style={desktopStyles?.styles.tvBackgroundOverlay}
          />
        </Animated.View>
      )}
      {Platform.isTV && (
        <View style={desktopStyles?.styles.topSpacer} pointerEvents="none" renderToHardwareTextureAndroid={isAndroidTV}>
          {focusedDesktopCard && (
            <Animated.View style={[desktopStyles?.styles.tvHeroOverview, heroContentAnimStyle]}>
              <View style={desktopStyles?.styles.tvHeroOverviewBg}>
                {/* Logo or title text */}
                {enrichedHeroData?.logoUrl ? (
                  <RNImage
                    source={{ uri: enrichedHeroData.logoUrl }}
                    style={[
                      desktopStyles?.styles.tvHeroLogo,
                      enrichedHeroData.isLogoDark ? { tintColor: 'white' } : undefined,
                    ]}
                    resizeMode="contain"
                  />
                ) : (
                  <Text style={desktopStyles?.styles.tvHeroTitle} numberOfLines={2}>
                    {focusedDesktopCard.title}
                  </Text>
                )}
                {/* Ratings row */}
                {enrichedHeroData?.ratings && enrichedHeroData.ratings.length > 0 && (
                  <View style={desktopStyles?.styles.tvHeroRatingsRow}>
                    {enrichedHeroData.ratings.map((rating) => {
                      const ratingBaseUrl = apiService.getBaseUrl().replace(/\/$/, '');
                      const config = getRatingConfig(rating.source, ratingBaseUrl, rating.value, rating.max);
                      return (
                        <View key={rating.source} style={desktopStyles?.styles.tvHeroRatingBadge}>
                          {config.iconUrl && (
                            <Image
                              source={config.iconUrl}
                              style={desktopStyles?.styles.tvHeroRatingIcon}
                              contentFit="contain"
                            />
                          )}
                          <Text style={[desktopStyles?.styles.tvHeroRatingValue, { color: config.color }]}>
                            {formatRating(rating)}
                          </Text>
                        </View>
                      );
                    })}
                  </View>
                )}
                {/* Genres row */}
                {enrichedHeroData?.genres && enrichedHeroData.genres.length > 0 && (
                  <View style={desktopStyles?.styles.tvHeroGenresRow}>
                    {enrichedHeroData.genres.map((genre) => (
                      <View key={genre} style={desktopStyles?.styles.tvHeroGenreBadge}>
                        <Text style={desktopStyles?.styles.tvHeroGenreText}>{genre}</Text>
                      </View>
                    ))}
                  </View>
                )}
                {focusedDesktopCard.year != null && Number(focusedDesktopCard.year) > 0 && (
                  <Text style={desktopStyles?.styles.tvHeroYear}>{focusedDesktopCard.year}</Text>
                )}
                <Text style={desktopStyles?.styles.tvHeroDescription} numberOfLines={6}>
                  {focusedDesktopCard.seriesOverview ?? focusedDesktopCard.description}
                </Text>
              </View>
            </Animated.View>
          )}
        </View>
      )}

      <Animated.ScrollView
        ref={scrollViewRef}
        style={desktopStyles?.styles.pageScroll}
        contentContainerStyle={desktopStyles?.styles.pageScrollContent}
        bounces={false}
        overScrollMode="never"
        decelerationRate="fast"
        showsVerticalScrollIndicator={false}
        scrollEnabled={false}
        contentInsetAdjustmentBehavior="never"
        automaticallyAdjustContentInsets={false}
        removeClippedSubviews={Platform.isTV}
        scrollEventThrottle={16}
        onLayout={handleDesktopScrollLayout}
        onContentSizeChange={handleDesktopContentSizeChange}>
        {!Platform.isTV && (
          <View style={desktopStyles?.styles.hero}>
            <Image source={heroSource.headerImage} style={desktopStyles?.styles.heroImage} contentFit="cover" />
            <LinearGradient
              colors={['rgba(0,0,0,0.9)', 'rgba(0,0,0,0.6)', 'rgba(0,0,0,0.2)', 'transparent']}
              start={{ x: 0, y: 0 }}
              end={{ x: 1, y: 0 }}
              style={desktopStyles?.styles.heroGradient}
            />
            <View style={desktopStyles?.styles.heroTextContainer}>
              <Text style={desktopStyles?.styles.heroTitle} numberOfLines={2}>
                {heroSource.title}
              </Text>
              <Text style={desktopStyles?.styles.heroDescription} numberOfLines={3}>
                {heroSource.description}
              </Text>
            </View>
          </View>
        )}

        <View key={navigationKey}>
          {desktopShelves.map((shelf, shelfIndex) => {
            const isLandscape = shelf.cardLayout === 'landscape';
            return (
              <MemoizedDesktopShelf
                key={shelf.key}
                title={shelf.title}
                cards={shelf.cards}
                styles={desktopStyles!.styles}
                cardWidth={isLandscape ? desktopStyles!.landscapeCardWidth : desktopStyles!.cardWidth}
                cardHeight={isLandscape ? desktopStyles!.landscapeCardHeight : desktopStyles!.cardHeight}
                cardSpacing={desktopStyles!.cardSpacing}
                shelfPadding={desktopStyles!.shelfPadding}
                onCardSelect={handleCardSelect}
                onShelfItemFocus={handleShelfItemFocus}
                autoFocus={shelf.autoFocus && shelf.cards.length > 0}
                collapseIfEmpty={shelf.collapseIfEmpty}
                showEmptyState={shelf.showEmptyState}
                shelfKey={shelf.key}
                shelfIndex={shelfIndex}
                registerShelfRef={registerShelfRef}
                registerShelfFlatListRef={registerShelfFlatListRef}
                isInitialLoad={isInitialLoadRef.current}
                badgeVisibility={badgeVisibility}
                watchStateIconStyle={watchStateIconStyle}
                onFirstItemTagChange={shelf.autoFocus ? setFirstContentFocusableTag : undefined}
                cardLayout={shelf.cardLayout}
              />
            );
          })}
        </View>
      </Animated.ScrollView>
      {!Platform.isTV && (
        <FloatingHero
          data={
            focusedDesktopCard
              ? {
                  title: focusedDesktopCard.title,
                  description: focusedDesktopCard.description,
                  headerImage: focusedDesktopCard.headerImage,
                  year: focusedDesktopCard.year,
                  mediaType: focusedDesktopCard.mediaType,
                }
              : null
          }
        />
      )}

      {/* Remove from Continue Watching Confirmation Modal (TV) */}
      {isRemoveConfirmVisible && (
        <View style={desktopStyles.styles.tvModalOverlay} focusable={false}>
          <Pressable
            style={desktopStyles.styles.tvModalBackdrop}
            onPress={handleCancelRemove}
            tvParallaxProperties={{ enabled: false }}
          />
          <View style={desktopStyles.styles.tvModalContainer} focusable={false}>
            <Text style={desktopStyles.styles.tvModalTitle}>Remove from Continue Watching?</Text>
            <Text style={desktopStyles.styles.tvModalSubtitle}>
              Are you sure you want to remove "{pendingRemoveItem?.name}" from Continue Watching?
            </Text>
            <View style={desktopStyles.styles.tvModalActions} focusable={false}>
              <Pressable
                ref={removeCancelButtonRef}
                onPress={handleCancelRemove}
                hasTVPreferredFocus={true}
                tvParallaxProperties={{ enabled: false }}
                nextFocusUp={removeCancelHandle}
                nextFocusDown={removeCancelHandle}
                nextFocusLeft={removeCancelHandle}
                nextFocusRight={removeConfirmHandle}
                style={({ focused }) => [
                  desktopStyles.styles.tvModalButton,
                  focused && desktopStyles.styles.tvModalButtonFocused,
                ]}>
                {({ focused }) => (
                  <Text
                    style={[
                      desktopStyles.styles.tvModalButtonText,
                      focused && desktopStyles.styles.tvModalButtonTextFocused,
                    ]}>
                    Cancel
                  </Text>
                )}
              </Pressable>
              <Pressable
                ref={removeConfirmButtonRef}
                onPress={handleConfirmRemove}
                tvParallaxProperties={{ enabled: false }}
                nextFocusUp={removeConfirmHandle}
                nextFocusDown={removeConfirmHandle}
                nextFocusLeft={removeCancelHandle}
                nextFocusRight={removeConfirmHandle}
                style={({ focused }) => [
                  desktopStyles.styles.tvModalButton,
                  desktopStyles.styles.tvModalButtonDanger,
                  focused && desktopStyles.styles.tvModalButtonFocused,
                  focused && desktopStyles.styles.tvModalButtonDangerFocused,
                ]}>
                {({ focused }) => (
                  <Text
                    style={[
                      desktopStyles.styles.tvModalButtonText,
                      desktopStyles.styles.tvModalButtonDangerText,
                      focused && desktopStyles.styles.tvModalButtonTextFocused,
                      focused && desktopStyles.styles.tvModalButtonDangerTextFocused,
                    ]}>
                    Remove
                  </Text>
                )}
              </Pressable>
            </View>
          </View>
        </View>
      )}

      {/* Version Mismatch Warning Modal (TV) */}
      <TvModal visible={isVersionMismatchVisible} onRequestClose={handleDismissVersionMismatch}>
        <View style={desktopStyles.styles.tvModalContainer} focusable={false}>
          <Text style={desktopStyles.styles.tvModalTitle}>Version Mismatch</Text>
          <Text style={desktopStyles.styles.tvModalSubtitle}>
            Frontend version ({APP_VERSION}) does not match backend version ({backendVersion ?? 'unknown'}). You may
            experience unexpected behavior. Consider updating.
          </Text>
          <View style={desktopStyles.styles.tvModalActions} focusable={false}>
            <FocusablePressable
              ref={versionMismatchButtonRef}
              autoFocus
              text="OK"
              onSelect={handleDismissVersionMismatch}
              style={desktopStyles.styles.tvModalButton}
              focusedStyle={desktopStyles.styles.tvModalButtonFocused}
              textStyle={desktopStyles.styles.tvModalButtonText}
              focusedTextStyle={desktopStyles.styles.tvModalButtonTextFocused}
              nextFocusUp={versionMismatchButtonHandle ?? undefined}
              nextFocusDown={versionMismatchButtonHandle ?? undefined}
              nextFocusLeft={versionMismatchButtonHandle ?? undefined}
              nextFocusRight={versionMismatchButtonHandle ?? undefined}
            />
          </View>
        </View>
      </TvModal>
    </View>
  );

  return (
    <>
      <Stack.Screen options={{ headerShown: false }} />
      {desktopContent}
    </>
  );
}

// Pre-computed gradient props - avoids creating new arrays on every render
const GRADIENT_COLORS_ANDROID_TV = ['transparent', 'rgba(0,0,0,0.8)', 'rgba(0,0,0,1)'] as const;
const GRADIENT_COLORS_DEFAULT = ['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)'] as const;
const GRADIENT_LOCATIONS_ANDROID_TV = [0, 0.5, 1] as const;
const GRADIENT_LOCATIONS_DEFAULT = [0, 0.6, 1] as const;
const GRADIENT_START = { x: 0.5, y: 0 } as const;
const GRADIENT_END = { x: 0.5, y: 1 } as const;

// Memoized shelf card content - prevents re-renders when card data hasn't changed
type ShelfCardContentProps = {
  card: CardData;
  cardKey: string;
  isFocused: boolean;
  isLastItem: boolean;
  showReleaseStatus: boolean;
  showWatchState: boolean;
  showUnwatchedCount: boolean;
  watchStateIconStyle: 'colored' | 'white';
  styles: ReturnType<typeof createDesktopStyles>['styles'];
  cardLayout?: 'portrait' | 'landscape';
};

// Memoized TV hero component — isolates re-renders from the main page tree.
// When focusedDesktopCard changes (debounced), only this component re-renders
// instead of the entire IndexScreen (which contains all shelves, ScrollView, etc.).
type TVHeroProps = {
  card: CardData;
  styles: ReturnType<typeof createDesktopStyles>['styles'];
};

const TVHero = React.memo(function TVHero({ card, styles }: TVHeroProps) {
  const imageUrl = card.backdropUrl || card.headerImage;
  if (!imageUrl) return null;

  // Compute image orientation inline (avoids separate state + effect cycle)
  const isPosterUrl =
    /\/w(?:185|342|500)\//i.test(imageUrl) ||
    (!card.backdropUrl && card.posterUrl === imageUrl);
  const isPortrait = isPosterUrl;

  if (isAndroidTV) {
    return (
      <View style={styles.topContent}>
        <View style={styles.topHeroContainer}>
          <Image source={imageUrl} style={styles.topHeroImage} contentFit="cover" transition={0} />
        </View>
        <View style={styles.topTextContainer}>
          <Text style={styles.topTitle} numberOfLines={2}>
            {card.title}
          </Text>
          {card.year != null && Number(card.year) > 0 && (
            <Text style={styles.topYearScaled}>{card.year}</Text>
          )}
          <Text style={styles.topDescriptionScaled} numberOfLines={4}>
            {card.description}
          </Text>
        </View>
      </View>
    );
  }

  return (
    <View style={styles.topContent}>
      <View style={styles.topHeroContainer}>
        {isPortrait ? (
          <>
            <Image source={imageUrl} style={[StyleSheet.absoluteFillObject]} contentFit="cover" blurRadius={50} />
            <Image source={imageUrl} style={styles.topHeroImage} contentFit="contain" />
          </>
        ) : (
          <Image source={imageUrl} style={styles.topHeroImage} contentFit="contain" />
        )}
      </View>
      <View style={styles.topTextContainer}>
        <Text style={styles.topTitle} numberOfLines={2}>
          {card.title}
        </Text>
        {card.year != null && Number(card.year) > 0 && (
          <Text style={styles.topYear}>{card.year}</Text>
        )}
        <Text style={styles.topDescription} numberOfLines={6}>
          {card.description}
        </Text>
      </View>
    </View>
  );
});

const ShelfCardContent = React.memo(
  function ShelfCardContent({ card, cardKey, isFocused, isLastItem, showReleaseStatus, showWatchState, showUnwatchedCount, watchStateIconStyle, styles, cardLayout = 'portrait' }: ShelfCardContentProps) {
    const isExploreCard = card.mediaType === 'explore' && card.collagePosters && card.collagePosters.length >= 4;
    const isLandscape = cardLayout === 'landscape';

    // Compute watch state icon (using MaterialCommunityIcons names)
    const getWatchStateIcon = () => {
      if (!card.watchState || card.watchState === 'none') return null;
      const useWhite = watchStateIconStyle === 'white';
      const greenColor = useWhite ? '#ffffff' : '#4ade80';
      const yellowColor = useWhite ? '#ffffff' : '#facc15';
      if (card.watchState === 'complete') {
        return { icon: 'circle' as const, color: greenColor };
      }
      if (card.watchState === 'partial') {
        return { icon: 'circle-half-full' as const, color: yellowColor };
      }
      return null;
    };

    const watchStateData = showWatchState ? getWatchStateIcon() : null;
    const hasUnwatchedCount = showUnwatchedCount &&
      (card.mediaType === 'series' || card.mediaType === 'tv') &&
      card.unwatchedCount !== undefined &&
      card.unwatchedCount > 0;

    // Determine if we need the top-left badge container (combines watch state + release status + unwatched count)
    const hasTopLeftBadge = (showReleaseStatus && card.releaseIcon) || watchStateData || hasUnwatchedCount;

    // Calculate progress bar visibility (only show if >5% progress)
    const showProgressBar = isLandscape && card.percentWatched !== undefined && card.percentWatched >= MIN_CONTINUE_WATCHING_PERCENT && card.percentWatched < 100;

    // For landscape cards, prefer backdrop image over poster
    const landscapeImage = card.backdropUrl || card.headerImage || card.cardImage;

    return (
      <View
        style={[
          isLandscape ? styles.landscapeCard : styles.card,
          isFocused && styles.cardFocused,
          !isLastItem && styles.cardSpacing,
        ]}
        // @ts-ignore - Android TV performance optimization
        renderToHardwareTextureAndroid={isAndroidTV}>
        {isExploreCard ? (
          <>
            <View style={{ flexDirection: 'row', flexWrap: 'wrap', width: '100%', height: '100%' }}>
              {card.collagePosters!.slice(0, 4).map((poster, i) => (
                <Image
                  key={`collage-${i}`}
                  source={poster}
                  style={{ width: '50%', height: '50%' }}
                  contentFit="cover"
                  transition={0}
                />
              ))}
            </View>
            <View style={isLandscape ? styles.landscapeCardTextContainer : [styles.cardTextContainer, { position: 'absolute', bottom: 0, left: 0, right: 0 }]}>
              <LinearGradient
                pointerEvents="none"
                colors={isAndroidTV ? GRADIENT_COLORS_ANDROID_TV : GRADIENT_COLORS_DEFAULT}
                locations={isAndroidTV ? GRADIENT_LOCATIONS_ANDROID_TV : GRADIENT_LOCATIONS_DEFAULT}
                start={GRADIENT_START}
                end={GRADIENT_END}
                style={styles.cardTextGradient}
              />
              <Text style={isLandscape
                ? (isAndroidTV ? styles.landscapeCardTitleAndroidTV : styles.landscapeCardTitle)
                : (isAndroidTV ? styles.cardTitleAndroidTV : styles.cardTitle)} numberOfLines={1}>
                {card.title}
              </Text>
              {card.year ? (
                <Text style={isLandscape
                  ? (isAndroidTV ? styles.landscapeCardMetaAndroidTV : styles.landscapeCardMeta)
                  : (isAndroidTV ? styles.cardMetaAndroidTV : styles.cardMeta)}>{card.year}</Text>
              ) : null}
            </View>
          </>
        ) : isLandscape ? (
          <>
            {/* Landscape card layout with progress bar */}
            <Image
              key={`img-${cardKey}`}
              source={landscapeImage}
              style={styles.cardImage}
              contentFit="cover"
              transition={0}
              cachePolicy="disk"
              recyclingKey={cardKey}
            />
            {/* Desaturation overlay for unreleased episodes */}
            {card.isUnreleased && !isFocused && (
              <View style={styles.unreleasedOverlay} pointerEvents="none" />
            )}
            <View style={styles.landscapeCardTextContainer}>
              <LinearGradient
                pointerEvents="none"
                colors={isAndroidTV ? GRADIENT_COLORS_ANDROID_TV : GRADIENT_COLORS_DEFAULT}
                locations={isAndroidTV ? GRADIENT_LOCATIONS_ANDROID_TV : GRADIENT_LOCATIONS_DEFAULT}
                start={GRADIENT_START}
                end={GRADIENT_END}
                style={styles.cardTextGradient}
              />
              <Text style={isAndroidTV ? styles.landscapeCardTitleAndroidTV : styles.landscapeCardTitle} numberOfLines={1}>
                {card.title}
              </Text>
              {card.year ? (
                <Text style={isAndroidTV ? styles.landscapeCardMetaAndroidTV : styles.landscapeCardMeta}>{card.year}</Text>
              ) : null}
            </View>
            {/* Coming soon badge (top-right) */}
            {card.isUnreleased && (
              <View style={styles.unreleasedBadge}>
                <Ionicons name="calendar-outline" size={isAndroidTV ? 12 : isTV ? 14 : 10} color="#000000" />
                <Text style={styles.unreleasedBadgeText}>Soon</Text>
              </View>
            )}
            {/* Progress bar at bottom */}
            {showProgressBar && (
              <View style={styles.progressBarContainer}>
                <View style={styles.progressBarBackground} />
                <View style={[styles.progressBarFill, { width: `${card.percentWatched ?? 0}%` }]} />
              </View>
            )}
          </>
        ) : (
          <>
            <Image
              key={`img-${cardKey}`}
              source={card.cardImage}
              style={styles.cardImage}
              contentFit="cover"
              transition={0}
              cachePolicy="disk"
              recyclingKey={cardKey}
            />
            {/* Top-left badge container (watch state + unwatched count + release status) */}
            {hasTopLeftBadge && (
              <View style={isAndroidTV ? styles.topLeftBadgeContainerAndroidTV : styles.topLeftBadgeContainer}>
                {/* Watch state and unwatched count badge */}
                {(watchStateData || hasUnwatchedCount) && (
                  <View style={isAndroidTV ? styles.topLeftBadgeItemAndroidTV : styles.topLeftBadgeItem}>
                    {watchStateData && (
                      <MaterialCommunityIcons
                        name={watchStateData.icon}
                        size={isAndroidTV ? styles.watchStateIconAndroidTV.fontSize : styles.watchStateIcon.fontSize}
                        color={watchStateData.color}
                      />
                    )}
                    {hasUnwatchedCount && (
                      <Text style={isAndroidTV ? styles.unwatchedCountTextAndroidTV : styles.unwatchedCountText}>
                        {card.unwatchedCount}
                      </Text>
                    )}
                  </View>
                )}
                {/* Release status badge */}
                {showReleaseStatus && card.releaseIcon && (
                  <View style={isAndroidTV ? styles.topLeftBadgeItemAndroidTV : styles.topLeftBadgeItem}>
                    <MaterialCommunityIcons
                      name={card.releaseIcon.name}
                      size={isAndroidTV ? styles.releaseStatusIconAndroidTV.fontSize : styles.releaseStatusIcon.fontSize}
                      color={watchStateIconStyle === 'white' ? '#ffffff' : card.releaseIcon.color}
                    />
                  </View>
                )}
              </View>
            )}
            {card.percentWatched !== undefined && card.percentWatched >= MIN_CONTINUE_WATCHING_PERCENT && (
              <View style={isAndroidTV ? styles.progressBadgeAndroidTV : styles.progressBadge}>
                <Text style={isAndroidTV ? styles.progressBadgeTextAndroidTV : styles.progressBadgeText}>
                  {Math.round(card.percentWatched)}%
                </Text>
              </View>
            )}
            <View style={styles.cardTextContainer}>
              <LinearGradient
                pointerEvents="none"
                colors={isAndroidTV ? GRADIENT_COLORS_ANDROID_TV : GRADIENT_COLORS_DEFAULT}
                locations={isAndroidTV ? GRADIENT_LOCATIONS_ANDROID_TV : GRADIENT_LOCATIONS_DEFAULT}
                start={GRADIENT_START}
                end={GRADIENT_END}
                style={styles.cardTextGradient}
              />
              <Text style={isAndroidTV ? styles.cardTitleAndroidTV : styles.cardTitle} numberOfLines={2}>
                {card.title}
              </Text>
              {card.year ? (
                <Text style={isAndroidTV ? styles.cardMetaAndroidTV : styles.cardMeta}>{card.year}</Text>
              ) : null}
            </View>
          </>
        )}
      </View>
    );
  },
  (prev, next) => {
    // Custom comparison - only re-render if card data or visual state changes
    return (
      prev.card.id === next.card.id &&
      prev.card.title === next.card.title &&
      prev.card.cardImage === next.card.cardImage &&
      prev.card.backdropUrl === next.card.backdropUrl &&
      prev.card.percentWatched === next.card.percentWatched &&
      prev.card.releaseIcon === next.card.releaseIcon &&
      prev.card.year === next.card.year &&
      prev.card.watchState === next.card.watchState &&
      prev.card.unwatchedCount === next.card.unwatchedCount &&
      prev.card.isUnreleased === next.card.isUnreleased &&
      prev.cardKey === next.cardKey &&
      prev.isFocused === next.isFocused &&
      prev.isLastItem === next.isLastItem &&
      prev.showReleaseStatus === next.showReleaseStatus &&
      prev.showWatchState === next.showWatchState &&
      prev.showUnwatchedCount === next.showUnwatchedCount &&
      prev.watchStateIconStyle === next.watchStateIconStyle &&
      prev.cardLayout === next.cardLayout
    );
  },
);

type VirtualizedShelfProps = {
  title: string;
  cards: CardData[];
  styles: ReturnType<typeof createDesktopStyles>['styles'];
  onCardSelect: (card: CardData) => void;
  onShelfItemFocus: (card: CardData, shelfKey: string, cardIndex: number) => void;
  autoFocus?: boolean;
  collapseIfEmpty?: boolean;
  showEmptyState?: boolean;
  shelfKey: string;
  shelfIndex: number;
  registerShelfRef: (key: string, ref: RNView | null) => void;
  registerShelfFlatListRef: (key: string, ref: FlatList | null) => void;
  isInitialLoad?: boolean;
  cardWidth: number;
  cardHeight: number;
  cardSpacing: number;
  shelfPadding: number;
  badgeVisibility?: string[]; // Which badges to show: watchProgress, releaseStatus
  watchStateIconStyle?: 'colored' | 'white'; // Icon color style for watch state badges
  onFirstItemTagChange?: (tag: number | null) => void; // Report first card's native tag (for drawer focus)
  cardLayout?: 'portrait' | 'landscape'; // Card layout style (default: portrait)
};

// Alias for backwards compatibility
type DesktopShelfProps = VirtualizedShelfProps;

// Type for shelf card handlers passed through context
type ShelfCardHandlers = {
  onSelect: (cardId: string | number) => void;
  onFocus: (cardId: string | number, index: number) => void;
};

function VirtualizedShelf({
  title,
  cards,
  styles,
  onCardSelect,
  onShelfItemFocus,
  autoFocus,
  collapseIfEmpty,
  showEmptyState,
  shelfKey,
  shelfIndex: _shelfIndex,
  registerShelfRef,
  registerShelfFlatListRef,
  cardWidth,
  cardHeight,
  cardSpacing,
  shelfPadding,
  badgeVisibility,
  watchStateIconStyle = 'colored',
  onFirstItemTagChange,
  cardLayout = 'portrait',
}: VirtualizedShelfProps) {
  if (DEBUG_INDEX_RENDERS) {
    console.log(`[IndexPage] VirtualizedShelf render: ${shelfKey} (${cards.length} cards)`);
  }

  const { profileSelectorVisible } = useUserProfiles();
  // Compute which badges to show
  const showWatchState = badgeVisibility?.includes('watchState') ?? false;
  const showUnwatchedCount = badgeVisibility?.includes('unwatchedCount') ?? false;
  const containerRef = React.useRef<RNView | null>(null);
  const flatListRef = React.useRef<FlatList>(null);
  const isEmpty = cards.length === 0;
  const shouldCollapse = Boolean(collapseIfEmpty && isEmpty);
  // Track refs for non-TV platforms only
  const cardRefsMap = React.useRef<Map<number, View | null>>(new Map());
  const lastItemIndexRef = React.useRef<number>(cards.length - 1);
  lastItemIndexRef.current = cards.length - 1;

  // Store card lookup map in ref for O(1) access by ID without causing callback recreation
  // Using ref instead of useMemo so shelfHandlers doesn't need to depend on cardMap
  const cardMapRef = React.useRef(new Map<string | number, CardData>());
  React.useEffect(() => {
    cardMapRef.current.clear();
    cards.forEach((card) => cardMapRef.current.set(card.id, card));
  }, [cards]);

  // Store callbacks in refs to avoid recreating renderItem
  const callbacksRef = React.useRef({ onCardSelect, onShelfItemFocus });
  callbacksRef.current = { onCardSelect, onShelfItemFocus };

  // Set the ref for the parent component
  React.useEffect(() => {
    registerShelfRef(shelfKey, containerRef.current);
    return () => {
      registerShelfRef(shelfKey, null);
    };
  }, [registerShelfRef, shelfKey]);

  // Register FlatList ref for programmatic horizontal scrolling
  React.useEffect(() => {
    registerShelfFlatListRef(shelfKey, flatListRef.current);
    return () => {
      registerShelfFlatListRef(shelfKey, null);
    };
  }, [registerShelfFlatListRef, shelfKey]);

  // Calculate item size for virtualized list (card width + spacing)
  const itemSize = cardWidth + cardSpacing;

  // TV scroll handler - snaps scroll position to card boundaries so cards are never cut off
  const scrollToFocusedItem = React.useCallback(
    (index: number) => {
      if (!Platform.isTV || !flatListRef.current) return;

      // Number of full cards to keep visible to the left of the focused item
      const cardsToLeft = 2;

      // Calculate target scroll position that aligns to card boundaries
      const targetCardIndex = Math.max(0, index - cardsToLeft);
      // Also cap so we don't scroll past where the last card would be at cardsToLeft position
      const maxCardIndex = Math.max(0, cards.length - 1 - cardsToLeft);
      const clampedIndex = Math.min(targetCardIndex, maxCardIndex);

      const targetX = clampedIndex * itemSize;

      flatListRef.current.scrollToOffset({
        offset: targetX,
        animated: true,
      });
    },
    [cards.length, itemSize],
  );

  // Store scrollToFocusedItem in ref for stable access
  const scrollToFocusedItemRef = React.useRef(scrollToFocusedItem);
  scrollToFocusedItemRef.current = scrollToFocusedItem;

  // Stable context handlers that use refs - never recreated when cards change
  // Uses cardMapRef (ref) instead of cardMap (useMemo) to avoid recreation on card updates
  const shelfHandlers = useMemo<ShelfCardHandlers>(
    () => ({
      onSelect: (cardId: string | number) => {
        const card = cardMapRef.current.get(cardId);
        if (card) callbacksRef.current.onCardSelect(card);
      },
      onFocus: (cardId: string | number, index: number) => {
        const card = cardMapRef.current.get(cardId);
        if (card) {
          // Single consolidated callback handles: menu close, shelf tracking, scroll, hero update (debounced)
          callbacksRef.current.onShelfItemFocus(card, shelfKey, index);
        }
        scrollToFocusedItemRef.current(index);
      },
    }),
    [shelfKey],
  );

  // Render item callback for FlatList - uses stable handlers via shelfHandlers
  // Uses memoized ShelfCardContent to prevent unnecessary re-renders
  const renderItem = useCallback(
    ({ item, index }: { item: CardData; index: number }) => {
      const card = item;
      // Use stable key: for series with episode codes, use just the series ID
      const rawId = String(card.id ?? index);
      const cardKey = rawId.includes(':S') ? rawId.split(':S')[0] : rawId;
      // First item gets autoFocus if shelf has autoFocus enabled (skip when profile selector is up)
      const shouldAutoFocus = autoFocus && index === 0 && !profileSelectorVisible;
      // Check if this is the last item for spacing
      const isLastItem = index === lastItemIndexRef.current;
      // Use pre-computed release icon from card data
      const showReleaseStatus = Boolean(badgeVisibility?.includes('releaseStatus') && card.releaseIcon);

      return (
        <Pressable
          ref={(ref) => {
            cardRefsMap.current.set(index, ref);
            // Report first card's native tag for drawer focus restoration
            if (index === 0 && ref && onFirstItemTagChange) {
              const tag = findNodeHandle(ref);
              if (tag) onFirstItemTagChange(tag);
            }
          }}
          onPress={() => shelfHandlers.onSelect(card.id)}
          onFocus={() => shelfHandlers.onFocus(card.id, index)}
          hasTVPreferredFocus={shouldAutoFocus}
          tvParallaxProperties={{ enabled: false }}>
          {({ focused, pressed }) => (
            <ShelfCardContent
              card={card}
              cardKey={cardKey}
              isFocused={Platform.isTV ? focused : pressed}
              isLastItem={isLastItem}
              showReleaseStatus={showReleaseStatus}
              showWatchState={showWatchState}
              showUnwatchedCount={showUnwatchedCount}
              watchStateIconStyle={watchStateIconStyle}
              styles={styles}
              cardLayout={cardLayout}
            />
          )}
        </Pressable>
      );
    },
    [autoFocus, shelfHandlers, styles, badgeVisibility, shelfKey, showWatchState, showUnwatchedCount, watchStateIconStyle, cardLayout, onFirstItemTagChange, profileSelectorVisible],
  );

  // Calculate row height for the virtualized list container
  const rowHeight = cardHeight + cardSpacing;

  // Early return for collapsed shelves - must be after all hooks
  if (shouldCollapse) {
    return (
      <View ref={containerRef} style={[styles.shelf, styles.shelfCollapsed]} accessibilityElementsHidden>
        {/* Empty collapsed shelf */}
      </View>
    );
  }

  const shouldShowEmptyState = Boolean(showEmptyState && isEmpty);

  return (
    <View ref={containerRef} style={styles.shelf} renderToHardwareTextureAndroid={isAndroidTV}>
      <View style={styles.shelfTitleWrapper}>
        <Text style={styles.shelfTitle}>{title}</Text>
      </View>
      {shouldShowEmptyState ? (
        <View style={styles.emptyCard}>
          <Text style={styles.emptyCardText}>Nothing to show yet</Text>
        </View>
      ) : (
        <TVFocusGuard trapFocus={['left', 'right']} style={{ height: rowHeight }}>
          <FlatList
            ref={flatListRef}
            data={cards}
            renderItem={renderItem}
            keyExtractor={(item, index) => String(item.id ?? index)}
            horizontal
            showsHorizontalScrollIndicator={false}
            scrollEnabled={true}
            contentContainerStyle={{ paddingRight: shelfPadding }}
            getItemLayout={(_, index) => ({
              length: itemSize,
              offset: itemSize * index,
              index,
            })}
            initialNumToRender={13}
            maxToRenderPerBatch={7}
            windowSize={3}
          />
        </TVFocusGuard>
      )}
    </View>
  );
}

// Alias for backwards compatibility
const DesktopShelf = VirtualizedShelf;

function areDesktopShelfPropsEqual(prev: DesktopShelfProps, next: DesktopShelfProps) {
  return (
    prev.title === next.title &&
    prev.cards === next.cards &&
    prev.styles === next.styles &&
    prev.onCardSelect === next.onCardSelect &&
    prev.onShelfItemFocus === next.onShelfItemFocus &&
    prev.autoFocus === next.autoFocus &&
    prev.collapseIfEmpty === next.collapseIfEmpty &&
    prev.showEmptyState === next.showEmptyState &&
    prev.shelfKey === next.shelfKey &&
    prev.shelfIndex === next.shelfIndex &&
    prev.registerShelfRef === next.registerShelfRef &&
    prev.registerShelfFlatListRef === next.registerShelfFlatListRef &&
    prev.isInitialLoad === next.isInitialLoad &&
    prev.cardWidth === next.cardWidth &&
    prev.cardHeight === next.cardHeight &&
    prev.cardSpacing === next.cardSpacing &&
    prev.shelfPadding === next.shelfPadding &&
    prev.badgeVisibility === next.badgeVisibility &&
    prev.onFirstItemTagChange === next.onFirstItemTagChange &&
    prev.cardLayout === next.cardLayout
  );
}

function createDesktopStyles(theme: NovaTheme, screenHeight: number) {
  const heroMin = 280;
  const heroMax = Math.round(screenHeight * 0.5);
  const heroHeight = Math.min(Math.max(Math.round(screenHeight * 0.38), heroMin), heroMax);

  const verticalPadding = theme.spacing['2xl'] * 2 + theme.spacing['3xl'];
  const availableBelowHero = Math.max(screenHeight - heroHeight - verticalPadding, theme.spacing['2xl']);

  // Calculate minimum text container height requirements
  const titleLineHeight = theme.typography.label.md.lineHeight;
  const yearLineHeight = theme.typography.caption.sm.lineHeight;
  const textPadding = theme.spacing.md * 2; // top and bottom padding
  const textGap = theme.spacing.xs;
  const minTextHeight = titleLineHeight * 2 + yearLineHeight + textPadding + textGap; // 2 lines for title + year + padding

  // Unified TV scaling - tvOS is baseline (1.0), Android TV auto-derives
  const tvScale = isTV ? getTVScaleMultiplier() : 1;
  const isTVOS = isTV && Platform.OS === 'ios';
  const isAndroidTV = isTV && Platform.OS === 'android';
  // tvOS: 1.4x, Android TV: smaller to fit ~6.5 posters
  const tvCardScaleFactor = isTVOS ? 1.4 : isAndroidTV ? 0.8 : 1.0;

  // Ensure minimum card height accommodates text properly but isn't too large
  const minCardHeight = Math.max(250, minTextHeight * 3.5); // Reduced from 5x to 3.5x for better balance
  const baseCardHeight = 300;
  const targetCardHeight = Math.min(baseCardHeight, Math.round(availableBelowHero * 0.65));
  const calculatedCardHeight = Math.max(minCardHeight, targetCardHeight);
  const cardHeight = Math.round(calculatedCardHeight * tvCardScaleFactor);
  const cardWidth = Math.round(cardHeight * (2 / 3)); // Standard 2:3 poster ratio

  // Landscape card dimensions (16:9 ratio) for continue watching shelf
  // Height is ~60% of portrait card height to keep similar visual weight
  const landscapeCardHeight = Math.round(cardHeight * 0.6);
  const landscapeCardWidth = Math.round(landscapeCardHeight * (16 / 9)); // 16:9 aspect ratio

  // Progress badge scaling - designed for tvOS at 1.25x, Android TV at 1.3x
  const badgeScale = isTV ? 1.25 * tvScale : 1.0;
  const androidTVBadgeScale = 1.3; // Badge for Android TV
  const androidTVTitleScale = 0.74; // Title text scale for Android TV
  const androidTVMetaScale = 0.9; // Year text scale for Android TV
  const badgePaddingH = Math.round(theme.spacing.sm * badgeScale);
  const badgePaddingV = Math.round(theme.spacing.xs * badgeScale);
  const badgeRadius = Math.round(theme.radius.sm * badgeScale);
  const badgeTextFontSize = Math.round(theme.typography.caption.sm.fontSize * badgeScale);
  const badgeTextLineHeight = Math.round(theme.typography.caption.sm.lineHeight * badgeScale);
  // Android TV specific badge sizing (2x larger)
  const androidTVBadgePaddingH = Math.round(theme.spacing.sm * androidTVBadgeScale);
  const androidTVBadgePaddingV = Math.round(theme.spacing.xs * androidTVBadgeScale);
  const androidTVBadgeRadius = Math.round(theme.radius.sm * androidTVBadgeScale);
  const androidTVBadgeTextFontSize = Math.round(theme.typography.caption.sm.fontSize * androidTVBadgeScale);
  const androidTVBadgeTextLineHeight = Math.round(theme.typography.caption.sm.lineHeight * androidTVBadgeScale);

  const cardSpacing = theme.spacing.lg;

  const styles = StyleSheet.create({
    page: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
    },
    topSpacer: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      height: screenHeight * 0.53, // 53% of screen height — shelves start just past midpoint
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      zIndex: 1,
    },
    tvBackgroundArt: {
      position: 'absolute',
      top: 0,
      right: 0,
      width: '85%',
      height: '85%',
      zIndex: 0,
    },
    tvBackgroundOverlay: {
      ...StyleSheet.absoluteFillObject,
    },
    tvHeroOverview: {
      position: 'absolute',
      bottom: theme.spacing.lg,
      left: theme.spacing['3xl'],
      right: '60%', // Constrain to left ~40% of screen
    },
    tvHeroOverviewBg: {
      backgroundColor: 'rgba(0, 0, 0, 0.45)',
      borderRadius: theme.radius.lg,
      padding: theme.spacing['xl'],
      gap: theme.spacing.sm,
    },
    tvHeroTitle: {
      ...theme.typography.title.xl,
      fontSize: Math.round(theme.typography.title.xl.fontSize * (isAndroidTV ? 2.2 : 1.8) * tvScale),
      lineHeight: Math.round(theme.typography.title.xl.lineHeight * (isAndroidTV ? 2.2 : 1.8) * tvScale),
      color: theme.colors.text.primary,
      fontWeight: '700',
      textShadowColor: 'rgba(0, 0, 0, 0.8)',
      textShadowOffset: { width: 0, height: 2 },
      textShadowRadius: 8,
    },
    tvHeroYear: {
      ...theme.typography.body.lg,
      fontSize: Math.round(theme.typography.body.lg.fontSize * (isAndroidTV ? 1.4 : 1.15) * tvScale),
      lineHeight: Math.round(theme.typography.body.lg.lineHeight * (isAndroidTV ? 1.4 : 1.15) * tvScale),
      color: theme.colors.text.muted,
      fontStyle: 'italic',
      textShadowColor: 'rgba(0, 0, 0, 0.8)',
      textShadowOffset: { width: 0, height: 1 },
      textShadowRadius: 6,
    },
    tvHeroDescription: {
      ...theme.typography.body.lg,
      fontSize: Math.round(theme.typography.body.lg.fontSize * (isAndroidTV ? 1.6 : 1.3) * tvScale),
      lineHeight: Math.round(theme.typography.body.lg.lineHeight * (isAndroidTV ? 1.6 : 1.3) * tvScale),
      color: theme.colors.text.secondary,
      textShadowColor: 'rgba(0, 0, 0, 0.8)',
      textShadowOffset: { width: 0, height: 1 },
      textShadowRadius: 6,
    },
    tvHeroLogo: {
      width: '80%',
      height: Math.round(140 * tvScale),
      alignSelf: 'flex-start' as const,
      marginBottom: theme.spacing.md,
    },
    tvHeroRatingsRow: {
      flexDirection: 'row' as const,
      alignItems: 'center' as const,
      gap: Math.round(8 * tvScale),
    },
    tvHeroRatingBadge: {
      flexDirection: 'row' as const,
      alignItems: 'center' as const,
      gap: Math.round(4 * tvScale),
      backgroundColor: 'rgba(255, 255, 255, 0.1)',
      paddingHorizontal: Math.round(8 * tvScale),
      paddingVertical: Math.round(4 * tvScale),
      borderRadius: Math.round(6 * tvScale),
    },
    tvHeroRatingIcon: {
      width: Math.round(17 * tvScale),
      height: Math.round(17 * tvScale),
    },
    tvHeroRatingValue: {
      fontSize: Math.round(14 * tvScale),
      fontWeight: '700' as const,
    },
    tvHeroGenresRow: {
      flexDirection: 'row' as const,
      flexWrap: 'wrap' as const,
      gap: Math.round(8 * tvScale),
    },
    tvHeroGenreBadge: {
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      paddingHorizontal: Math.round(12 * tvScale),
      paddingVertical: Math.round(6 * tvScale),
      borderRadius: Math.round(16 * tvScale),
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: 'rgba(255, 255, 255, 0.15)',
    },
    tvHeroGenreText: {
      fontSize: Math.round(12 * tvScale),
      fontWeight: '500' as const,
      color: theme.colors.text.secondary,
    },
    bottomFadeGradient: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: '6%',
      zIndex: 5,
    },
    topContent: {
      flex: 1,
      flexDirection: 'row',
      padding: theme.spacing['2xl'],
      gap: theme.spacing['2xl'],
    },
    topHeroContainer: {
      aspectRatio: 16 / 9,
      height: '100%',
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
    },
    topHeroImage: {
      width: '100%',
      height: '100%',
    },
    topTextContainer: {
      flex: 1,
      justifyContent: 'center',
      gap: theme.spacing.md,
    },
    topTitle: {
      ...theme.typography.title.xl,
      // Design for tvOS at 1.5x, Android TV at 1.8x (20% larger)
      fontSize: Math.round(theme.typography.title.xl.fontSize * (isAndroidTV ? 1.8 : 1.5) * tvScale),
      lineHeight: Math.round(theme.typography.title.xl.lineHeight * (isAndroidTV ? 1.8 : 1.5) * tvScale),
      color: theme.colors.text.primary,
      fontWeight: '700',
    },
    topYear: {
      ...theme.typography.body.lg,
      // Design for tvOS at 1.75x, Android TV at 2.1x (20% larger)
      fontSize: Math.round(theme.typography.body.lg.fontSize * (isAndroidTV ? 2.1 : 1.75) * tvScale),
      lineHeight: Math.round(theme.typography.body.lg.lineHeight * (isAndroidTV ? 2.1 : 1.75) * tvScale),
      color: theme.colors.text.secondary,
    },
    topDescription: {
      ...theme.typography.body.lg,
      // Design for tvOS at 1.5x, Android TV at 1.8x (20% larger)
      fontSize: Math.round(theme.typography.body.lg.fontSize * (isAndroidTV ? 1.8 : 1.5) * tvScale),
      lineHeight: Math.round(theme.typography.body.lg.lineHeight * (isAndroidTV ? 1.8 : 1.5) * tvScale),
      color: theme.colors.text.secondary,
    },
    // Pre-scaled variants for Android TV hero (avoids creating inline style objects on every render)
    topYearScaled: {
      ...theme.typography.body.lg,
      fontSize: Math.round(theme.typography.body.lg.fontSize * (isAndroidTV ? 2.1 : 1.75) * tvScale * 1.25),
      lineHeight: Math.round(theme.typography.body.lg.lineHeight * (isAndroidTV ? 2.1 : 1.75) * tvScale * 1.25),
      color: theme.colors.text.secondary,
    },
    topDescriptionScaled: {
      ...theme.typography.body.lg,
      fontSize: Math.round(theme.typography.body.lg.fontSize * (isAndroidTV ? 1.8 : 1.5) * tvScale * 1.25),
      lineHeight: Math.round(theme.typography.body.lg.lineHeight * (isAndroidTV ? 1.8 : 1.5) * tvScale * 1.25),
      color: theme.colors.text.secondary,
    },
    heroContainer: {
      marginTop: theme.spacing['2xl'],
    },
    heroScrollContent: {
      paddingHorizontal: theme.spacing['2xl'],
    },
    hero: {
      height: heroHeight,
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      position: 'relative',
    },
    heroSingle: {
      marginHorizontal: theme.spacing['2xl'],
    },
    heroImage: {
      width: '100%',
      height: '100%',
    },
    heroPagination: {
      flexDirection: 'row',
      justifyContent: 'center',
      alignItems: 'center',
      gap: theme.spacing.sm,
      marginTop: theme.spacing.md,
    },
    heroDot: {
      width: 8,
      height: 8,
      borderRadius: 4,
      backgroundColor: 'rgba(255, 255, 255, 0.4)',
    },
    heroDotActive: {
      backgroundColor: theme.colors.accent.primary,
      width: 10,
      height: 10,
      borderRadius: 5,
    },
    heroGradient: {
      ...StyleSheet.absoluteFillObject,
    },
    heroTextContainer: {
      position: 'absolute',
      left: theme.spacing['3xl'],
      right: theme.spacing['3xl'],
      bottom: theme.spacing['2xl'],
      gap: theme.spacing.md,
    },
    heroTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    heroDescription: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
    },
    pageScroll: {
      flex: 1,
      marginTop: Platform.isTV ? screenHeight * 0.53 : 0,
    },
    pageScrollContent: {
      // Add extra bottom padding on TV to ensure the last shelf can scroll properly
      // This prevents shelf titles from appearing above the bottom-most items
      paddingBottom: Platform.isTV ? screenHeight * 0.5 + 15 : theme.spacing['3xl'],
      paddingTop: theme.spacing['2xl'],
      gap: theme.spacing['3xl'],
    },
    shelf: {
      gap: theme.spacing.lg,
      paddingHorizontal: theme.spacing['2xl'],
      paddingTop: 10,
      zIndex: 10,
      position: 'relative',
      elevation: 10,
    },
    shelfCollapsed: {
      paddingHorizontal: theme.spacing['2xl'],
      paddingTop: 0,
      height: 0,
      marginBottom: 0,
      opacity: 0,
      overflow: 'hidden',
    },
    shelfTitleWrapper: {
      zIndex: 10,
      position: 'relative',
      backgroundColor: 'transparent',
      // On TV, lift titles above the top fade gradient (which has zIndex 5)
      ...(Platform.isTV ? { zIndex: 7 } : {}),
    },
    shelfTitle: {
      ...theme.typography.shelf.title,
      color: theme.colors.text.primary,
    },
    shelfRow: {
      gap: theme.spacing.lg,
      paddingBottom: theme.spacing.sm,
    },
    card: {
      width: cardWidth,
      height: cardHeight,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
      borderWidth: 3,
      borderColor: 'transparent',
    },
    cardFocused: {
      borderColor: theme.colors.accent.primary,
      // Keep borderWidth constant to prevent layout shift
      // Only color changes for better performance
    },
    cardSpacing: {
      marginRight: theme.spacing.lg,
    },
    cardImage: {
      width: '100%',
      height: '100%',
    },
    cardTextContainer: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      padding: theme.spacing.md,
      gap: theme.spacing.xs,
      alignItems: 'center',
      justifyContent: 'flex-end',
      minHeight: '40%',
    },
    cardTextGradient: {
      ...StyleSheet.absoluteFillObject,
    },
    cardTitle: {
      ...(isTV ? theme.typography.body.md : theme.typography.body.lg),
      ...(isTV
        ? {
            // Design for tvOS at 1.5x, Android TV auto-scales
            fontSize: Math.round(theme.typography.body.md.fontSize * 1.5 * tvScale),
            lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.5 * tvScale),
          }
        : null),
      color: theme.colors.text.primary,
      textAlign: 'center',
      zIndex: 1,
      fontWeight: '600',
    },
    // Android TV specific title style (smaller)
    cardTitleAndroidTV: {
      ...theme.typography.body.md,
      fontSize: Math.round(theme.typography.body.md.fontSize * 1.5 * androidTVTitleScale),
      lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.5 * androidTVTitleScale),
      color: theme.colors.text.primary,
      textAlign: 'center',
      zIndex: 1,
      fontWeight: '600',
    },
    cardMeta: {
      ...(isTV ? theme.typography.body.sm : theme.typography.caption.sm),
      ...(isTV
        ? {
            // Design for tvOS at 1.25x, Android TV auto-scales
            fontSize: Math.round(theme.typography.body.sm.fontSize * 1.25 * tvScale),
            lineHeight: Math.round(theme.typography.body.sm.lineHeight * 1.25 * tvScale),
          }
        : null),
      color: theme.colors.text.secondary,
      textAlign: 'center',
      zIndex: 1,
    },
    // Android TV specific meta style
    cardMetaAndroidTV: {
      ...theme.typography.body.sm,
      fontSize: Math.round(theme.typography.body.sm.fontSize * 1.25 * androidTVMetaScale),
      lineHeight: Math.round(theme.typography.body.sm.lineHeight * 1.25 * androidTVMetaScale),
      color: theme.colors.text.secondary,
      textAlign: 'center',
      zIndex: 1,
    },
    emptyCard: {
      width: cardWidth,
      height: cardHeight,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      alignItems: 'center',
      justifyContent: 'center',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    emptyCardText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      textAlign: 'center',
      paddingHorizontal: theme.spacing.md,
    },
    // Landscape card styles (for continue watching shelf)
    landscapeCard: {
      width: landscapeCardWidth,
      height: landscapeCardHeight,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
      borderWidth: 3,
      borderColor: 'transparent',
    },
    unreleasedOverlay: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(0, 0, 0, 0.55)',
      zIndex: 2,
    },
    landscapeCardTextContainer: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      padding: theme.spacing.md,
      paddingBottom: theme.spacing.md + 4, // Extra padding for progress bar
      gap: theme.spacing.xs,
      alignItems: 'flex-start',
      justifyContent: 'flex-end',
      minHeight: '50%',
      zIndex: 3, // Above unreleased overlay (zIndex 2)
    },
    landscapeCardTitle: {
      ...(isTV ? theme.typography.body.md : theme.typography.body.md),
      ...(isTV
        ? {
            fontSize: Math.round(theme.typography.body.md.fontSize * 1.4 * tvScale),
            lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.4 * tvScale),
          }
        : null),
      color: theme.colors.text.primary,
      textAlign: 'left',
      zIndex: 1,
      fontWeight: '600',
    },
    landscapeCardTitleAndroidTV: {
      ...theme.typography.body.md,
      fontSize: Math.round(theme.typography.body.md.fontSize * 1.4 * androidTVTitleScale),
      lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.4 * androidTVTitleScale),
      color: theme.colors.text.primary,
      textAlign: 'left',
      zIndex: 1,
      fontWeight: '600',
    },
    landscapeCardMeta: {
      ...(isTV ? theme.typography.body.sm : theme.typography.caption.sm),
      ...(isTV
        ? {
            fontSize: Math.round(theme.typography.body.sm.fontSize * 1.2 * tvScale),
            lineHeight: Math.round(theme.typography.body.sm.lineHeight * 1.2 * tvScale),
          }
        : null),
      color: theme.colors.text.secondary,
      textAlign: 'left',
      zIndex: 1,
    },
    landscapeCardMetaAndroidTV: {
      ...theme.typography.body.sm,
      fontSize: Math.round(theme.typography.body.sm.fontSize * 1.2 * androidTVMetaScale),
      lineHeight: Math.round(theme.typography.body.sm.lineHeight * 1.2 * androidTVMetaScale),
      color: theme.colors.text.secondary,
      textAlign: 'left',
      zIndex: 1,
    },
    // Progress bar styles (at bottom of landscape card)
    unreleasedBadge: {
      position: 'absolute',
      top: isTV ? theme.spacing.sm : theme.spacing.xs,
      right: isTV ? theme.spacing.sm : theme.spacing.xs,
      flexDirection: 'row',
      alignItems: 'center',
      gap: isTV ? 4 : 3,
      paddingHorizontal: isTV ? 8 : 6,
      paddingVertical: isTV ? 4 : 3,
      borderRadius: 12,
      backgroundColor: theme.colors.status.warning,
      zIndex: 5,
    },
    unreleasedBadgeText: {
      fontSize: isAndroidTV ? 10 : isTV ? 12 : 9,
      fontWeight: '700',
      color: '#000000',
    },
    progressBarContainer: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: 4,
      zIndex: 3,
    },
    progressBarBackground: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(255, 255, 255, 0.4)',
    },
    progressBarFill: {
      position: 'absolute',
      top: 0,
      left: 0,
      bottom: 0,
      backgroundColor: theme.colors.accent.primary,
    },
    progressBadge: {
      position: 'absolute',
      top: theme.spacing.md,
      right: theme.spacing.md,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: badgePaddingH,
      paddingVertical: badgePaddingV,
      borderRadius: badgeRadius,
      zIndex: 2,
    },
    progressBadgeText: {
      ...theme.typography.caption.sm,
      fontSize: badgeTextFontSize,
      lineHeight: badgeTextLineHeight,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    // Android TV specific badge styles (2x larger)
    progressBadgeAndroidTV: {
      position: 'absolute',
      top: theme.spacing.md,
      right: theme.spacing.md,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: androidTVBadgePaddingH,
      paddingVertical: androidTVBadgePaddingV,
      borderRadius: androidTVBadgeRadius,
      zIndex: 2,
    },
    progressBadgeTextAndroidTV: {
      ...theme.typography.caption.sm,
      fontSize: androidTVBadgeTextFontSize,
      lineHeight: androidTVBadgeTextLineHeight,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    // Release status badge (top-left) - matches MediaItem.tsx styling
    releaseStatusBadge: {
      position: 'absolute',
      top: theme.spacing.md,
      left: theme.spacing.md,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: badgePaddingH,
      paddingVertical: badgePaddingV,
      borderRadius: badgeRadius,
      zIndex: 2,
    },
    releaseStatusIcon: {
      fontSize: Math.round(14 * badgeScale),
    },
    // Android TV release status badge (matches MediaItem.tsx sizing)
    releaseStatusBadgeAndroidTV: {
      position: 'absolute',
      top: theme.spacing.md,
      left: theme.spacing.md,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: badgePaddingH,
      paddingVertical: badgePaddingV,
      borderRadius: badgeRadius,
      zIndex: 2,
    },
    releaseStatusIconAndroidTV: {
      fontSize: Math.round(14 * badgeScale),
    },
    // Top-left badge container (stacks watch state and release status badges)
    topLeftBadgeContainer: {
      position: 'absolute',
      top: theme.spacing.md,
      left: theme.spacing.md,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: badgePaddingH,
      paddingVertical: badgePaddingV,
      borderRadius: badgeRadius,
      zIndex: 2,
      flexDirection: 'column',
      alignItems: 'center',
      gap: Math.round(2 * badgeScale),
    },
    topLeftBadgeContainerAndroidTV: {
      position: 'absolute',
      top: theme.spacing.md,
      left: theme.spacing.md,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: androidTVBadgePaddingH,
      paddingVertical: androidTVBadgePaddingV,
      borderRadius: androidTVBadgeRadius,
      zIndex: 2,
      flexDirection: 'column',
      alignItems: 'center',
      gap: Math.round(2 * androidTVBadgeScale),
    },
    topLeftBadgeItem: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: Math.round(4 * badgeScale),
    },
    topLeftBadgeItemAndroidTV: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: Math.round(4 * androidTVBadgeScale),
    },
    watchStateIcon: {
      fontSize: Math.round(14 * badgeScale),
    },
    watchStateIconAndroidTV: {
      fontSize: Math.round(14 * badgeScale),
    },
    unwatchedCountText: {
      fontSize: badgeTextFontSize,
      lineHeight: badgeTextLineHeight,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    unwatchedCountTextAndroidTV: {
      fontSize: androidTVBadgeTextFontSize,
      lineHeight: androidTVBadgeTextLineHeight,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    // TV Modal styles
    tvModalOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      zIndex: 1000,
    },
    tvModalBackdrop: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
    },
    tvModalContainer: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.lg,
      padding: theme.spacing['2xl'],
      minWidth: Math.round(400 * tvScale),
      maxWidth: Math.round(600 * tvScale),
      gap: theme.spacing.xl,
      alignItems: 'center',
    },
    tvModalTitle: {
      ...theme.typography.title.lg,
      fontSize: Math.round(theme.typography.title.lg.fontSize * 1.5),
      lineHeight: Math.round(theme.typography.title.lg.lineHeight * 1.5),
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    tvModalSubtitle: {
      ...theme.typography.body.md,
      fontSize: Math.round(theme.typography.body.md.fontSize * 1.25),
      lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.25),
      color: theme.colors.text.secondary,
      textAlign: 'center',
    },
    tvModalActions: {
      flexDirection: 'row',
      justifyContent: 'center',
      gap: theme.spacing.xl,
    },
    tvModalButton: {
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing['2xl'],
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: 3,
      borderColor: 'transparent',
    },
    tvModalButtonFocused: {
      borderColor: theme.colors.accent.primary,
    },
    tvModalButtonDanger: {
      backgroundColor: theme.colors.status.danger,
    },
    tvModalButtonDangerFocused: {
      borderColor: theme.colors.text.primary,
    },
    tvModalButtonText: {
      ...theme.typography.body.md,
      fontSize: Math.round(theme.typography.body.md.fontSize * 1.25),
      lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.25),
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    tvModalButtonTextFocused: {
      color: theme.colors.text.primary,
    },
    tvModalButtonDangerText: {
      color: theme.colors.text.primary,
    },
    tvModalButtonDangerTextFocused: {
      color: theme.colors.text.primary,
    },
  });

  const shelfPadding = theme.spacing['2xl'];

  return {
    styles,
    cardWidth,
    cardHeight,
    landscapeCardWidth,
    landscapeCardHeight,
    cardSpacing,
    shelfPadding,
  };
}

function createMobileStyles(theme: NovaTheme) {
  return StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    container: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    content: {
      paddingBottom: theme.spacing['3xl'],
      gap: theme.spacing['2xl'],
    },
    heroContainer: {
      marginTop: theme.spacing.lg,
    },
    heroScrollContent: {
      paddingHorizontal: theme.spacing.lg,
    },
    hero: {
      overflow: 'hidden',
      aspectRatio: 16 / 9,
      position: 'relative',
      borderRadius: theme.radius.lg,
    },
    heroSingle: {
      marginHorizontal: theme.spacing.lg,
    },
    heroImage: {
      width: '100%',
      height: '100%',
    },
    heroGradient: {
      ...StyleSheet.absoluteFillObject,
    },
    heroTextContainer: {
      position: 'absolute',
      left: theme.spacing.lg,
      right: theme.spacing.lg,
      bottom: theme.spacing.lg,
      gap: theme.spacing.xs,
    },
    heroTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    heroDescription: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    heroPagination: {
      flexDirection: 'row',
      justifyContent: 'center',
      alignItems: 'center',
      gap: theme.spacing.sm,
      marginTop: theme.spacing.md,
      paddingHorizontal: theme.spacing.lg,
    },
    heroDot: {
      width: 8,
      height: 8,
      borderRadius: 4,
      backgroundColor: 'rgba(255, 255, 255, 0.4)',
    },
    heroDotActive: {
      backgroundColor: theme.colors.accent.primary,
      width: 10,
      height: 10,
      borderRadius: 5,
    },
    section: {
      gap: theme.spacing.md,
    },
    // Modal styles for mobile
    modalOverlay: {
      flex: 1,
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      justifyContent: 'center',
      alignItems: 'center',
      padding: theme.spacing.xl,
    },
    modalContainer: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.lg,
      padding: theme.spacing.xl,
      width: '100%',
      maxWidth: 400,
      gap: theme.spacing.lg,
    },
    modalTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    modalSubtitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      textAlign: 'center',
    },
    modalActions: {
      flexDirection: 'row',
      justifyContent: 'center',
      gap: theme.spacing.md,
    },
    modalButton: {
      flex: 1,
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.lg,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      alignItems: 'center',
    },
    modalButtonDanger: {
      backgroundColor: theme.colors.status.danger,
    },
    modalButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    modalButtonDangerText: {
      color: theme.colors.text.primary,
    },
  });
}

function getHeroCardKey(card: CardData): string {
  const tmdbKey = card.tmdbId ? `tmdb:${card.tmdbId}` : null;
  const tvdbKey = card.tvdbId ? `tvdb:${card.tvdbId}` : null;
  const imdbKey = card.imdbId ? `imdb:${card.imdbId}` : null;
  const fallbackId =
    typeof card.id === 'string' ? card.id.replace(/:S\d{2}E\d{2}$/i, '') : String(card.id ?? card.title ?? '');
  return tmdbKey ?? tvdbKey ?? imdbKey ?? `${card.mediaType ?? 'unknown'}:${fallbackId}`;
}

function shuffleArray<T>(input: T[]): T[] {
  const array = [...input];
  for (let i = array.length - 1; i > 0; i -= 1) {
    const j = Math.floor(Math.random() * (i + 1));
    [array[i], array[j]] = [array[j], array[i]];
  }
  return array;
}

function mapTrendingToCards(
  items?: TrendingItem[],
  cachedReleases?: Map<string, { theatricalRelease?: Title['theatricalRelease']; homeRelease?: Title['homeRelease'] }>,
): CardData[] {
  if (!items) {
    return [];
  }

  return items.map((item) => {
    // Merge cached release data for movies
    const cached = item.title.mediaType === 'movie' && cachedReleases ? cachedReleases.get(item.title.id) : undefined;
    const theatricalRelease = item.title.theatricalRelease ?? cached?.theatricalRelease;
    const homeRelease = item.title.homeRelease ?? cached?.homeRelease;

    // Pre-compute release icon for movies to avoid computation at render time
    const releaseIcon =
      item.title.mediaType === 'movie'
        ? getMovieReleaseIcon({
            ...item.title,
            theatricalRelease,
            homeRelease,
          })
        : undefined;

    // Extract watchState and unwatchedCount if they exist (from enriched titles)
    const enrichedTitle = item.title as Title & { watchState?: 'none' | 'partial' | 'complete'; unwatchedCount?: number };

    return {
      id: item.title.id,
      title: item.title.name,
      description: item.title.overview || 'No description available',
      headerImage:
        item.title.backdrop?.url ||
        item.title.poster?.url ||
        'https://via.placeholder.com/1920x1080/333/fff?text=No+Image',
      cardImage:
        item.title.poster?.url ||
        item.title.backdrop?.url ||
        'https://via.placeholder.com/600x900/333/fff?text=No+Image',
      mediaType: item.title.mediaType,
      posterUrl: item.title.poster?.url,
      backdropUrl: item.title.backdrop?.url,
      tmdbId: item.title.tmdbId,
      imdbId: item.title.imdbId,
      tvdbId: item.title.tvdbId,
      year: item.title.year,
      theatricalRelease,
      homeRelease,
      releaseIcon,
      watchState: enrichedTitle.watchState,
      unwatchedCount: enrichedTitle.unwatchedCount,
    };
  });
}

function mapWatchlistToTrendingItems(items?: WatchlistItem[], cachedYears?: Map<string, number>) {
  if (!items) {
    return [];
  }

  return items.map((item) => ({
    rank: 0,
    title: {
      id: item.id,
      name: item.name,
      overview: item.overview ?? '',
      mediaType: item.mediaType,
      poster: item.posterUrl ? { url: item.posterUrl, type: 'poster', width: 0, height: 0 } : undefined,
      backdrop: item.backdropUrl ? { url: item.backdropUrl, type: 'backdrop', width: 0, height: 0 } : undefined,
      imdbId: item.externalIds?.imdb,
      tmdbId: item.externalIds?.tmdb ? Number(item.externalIds.tmdb) : undefined,
      tvdbId: item.externalIds?.tvdb ? Number(item.externalIds.tvdb) : undefined,
      year: item.year && item.year > 0 ? item.year : (cachedYears?.get(item.id) ?? 0),
    } as Title,
  }));
}

function formatEpisodeCode(seasonNumber?: number, episodeNumber?: number) {
  if (typeof seasonNumber !== 'number' || typeof episodeNumber !== 'number') {
    return 'Next episode';
  }
  const pad = (value: number) => value.toString().padStart(2, '0');
  return `S${pad(seasonNumber)}E${pad(episodeNumber)}`;
}

function wasLastEpisodeWatchedRecently(item: SeriesWatchState): boolean {
  const parseTimestamp = (value?: string) => {
    if (!value) {
      return null;
    }
    const parsed = Date.parse(value);
    return Number.isNaN(parsed) ? null : parsed;
  };

  const now = Date.now();
  const isWithinWindow = (timestamp: number | null) => {
    if (timestamp === null) {
      return false;
    }
    return now - timestamp <= RECENT_SERIES_WATCH_WINDOW_MS;
  };

  const watchedAtTimestamp = parseTimestamp(item.lastWatched?.watchedAt);
  if (isWithinWindow(watchedAtTimestamp)) {
    return true;
  }

  return isWithinWindow(parseTimestamp(item.updatedAt));
}

function mapContinueWatchingToCards(
  items?: SeriesWatchState[],
  seriesOverviews?: Map<string, string>,
  watchlistItems?: WatchlistItem[],
  cachedReleases?: Map<string, { theatricalRelease?: Title['theatricalRelease']; homeRelease?: Title['homeRelease'] }>,
): CardData[] {
  if (!items) {
    return [];
  }

  const parseNumeric = (value?: string) => {
    if (!value) {
      return undefined;
    }
    const parsed = Number(value);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
  };

  return items
    .map<CardData | null>((item) => {
      const basePercent = item.percentWatched ?? 0;
      const next = item.nextEpisode;
      const isMovie = !next;
      const hasMeaningfulProgress = basePercent > MIN_CONTINUE_WATCHING_PERCENT;
      const wasRecent = wasLastEpisodeWatchedRecently(item);
      // For series: show if it has meaningful progress OR was watched recently OR has a valid next episode
      // The backend tracks next episodes, so if there's a next episode, we should show it
      const includeSeries = !isMovie && (hasMeaningfulProgress || wasRecent || !!next);
      const includeMovie = isMovie && hasMeaningfulProgress;

      if (!includeMovie && !includeSeries) {
        return null;
      }
      const displayPercent = Math.max(0, Math.round(item.resumePercent ?? basePercent));

      const headerImage =
        item.backdropUrl || item.posterUrl || 'https://via.placeholder.com/1920x1080/333/fff?text=No+Image';
      const cardImage =
        item.posterUrl || item.backdropUrl || 'https://via.placeholder.com/600x900/333/fff?text=No+Image';

      // Check if this is a movie (no nextEpisode) or a series
      if (isMovie) {
        // Movie resume - use overview from API response, falling back to lastWatched.overview
        const movieOverview = item.overview || item.lastWatched?.overview || '';
        // Get cached release data for movie
        const cached = cachedReleases?.get(item.seriesId);
        const theatricalRelease = cached?.theatricalRelease;
        const homeRelease = cached?.homeRelease;

        // Pre-compute release icon to avoid computation at render time
        const releaseIcon = getMovieReleaseIcon({
          id: item.seriesId,
          name: item.seriesTitle,
          overview: movieOverview,
          year: item.year ?? 0,
          language: 'en',
          mediaType: 'movie',
          theatricalRelease,
          homeRelease,
        });

        // Compute movie watch state: partial if has progress but not complete
        const movieWatchState: 'none' | 'partial' | 'complete' =
          displayPercent >= 90 ? 'complete' : displayPercent > 0 ? 'partial' : 'none';

        return {
          id: item.seriesId,
          title: item.seriesTitle,
          description: movieOverview || 'Resume watching',
          headerImage,
          cardImage,
          mediaType: 'movie',
          posterUrl: item.posterUrl,
          backdropUrl: item.backdropUrl,
          tmdbId: parseNumeric(item.externalIds?.tmdb),
          imdbId: item.externalIds?.imdb,
          tvdbId: parseNumeric(item.externalIds?.tvdb),
          year: item.year,
          percentWatched: displayPercent,
          seriesOverview: movieOverview, // Store for details page
          theatricalRelease,
          homeRelease,
          releaseIcon,
          watchState: movieWatchState,
        };
      }

      // Series - show next episode info
      const code = formatEpisodeCode(next?.seasonNumber, next?.episodeNumber);
      const baseSeriesId = item.seriesId;
      const watchlistItem = watchlistItems?.find((w) => w.id === baseSeriesId);
      // Prefer overview from API response, then async-fetched cache, then watchlist
      const seriesOverview = item.overview || seriesOverviews?.get(baseSeriesId) || watchlistItem?.overview || '';

      // Hide unreleased episodes that are too far out; show "coming soon" for those within the window
      const unreleased = next?.airDate ? isEpisodeUnreleased(next.airDate) : false;
      const comingSoon = unreleased && next?.airDate ? isEpisodeComingSoon(next.airDate) : false;
      if (unreleased && !comingSoon) {
        return null; // Too far out — hide from continue watching
      }

      // Compute series watch state and unwatched count
      const totalEpisodes = item.totalEpisodeCount ?? 0;
      const watchedEpisodes = item.watchedEpisodeCount ?? 0;
      const allEpisodesWatched = totalEpisodes > 0 && watchedEpisodes >= totalEpisodes;
      const seriesWatchState: 'none' | 'partial' | 'complete' =
        allEpisodesWatched ? 'complete' : watchedEpisodes > 0 || displayPercent > 0 ? 'partial' : 'none';
      const seriesUnwatchedCount = totalEpisodes > 0 ? totalEpisodes - watchedEpisodes : undefined;

      return {
        id: `${item.seriesId}:${code}`,
        title: item.seriesTitle,
        description: next?.title ? `Next: ${code} • ${next.title}` : `Next: ${code}`,
        headerImage,
        cardImage,
        mediaType: 'series',
        posterUrl: item.posterUrl,
        backdropUrl: item.backdropUrl,
        tmdbId: parseNumeric(item.externalIds?.tmdb),
        imdbId: item.externalIds?.imdb,
        tvdbId: parseNumeric(item.externalIds?.tvdb),
        year: item.year,
        percentWatched: displayPercent,
        seriesOverview,
        watchState: seriesWatchState,
        unwatchedCount: seriesUnwatchedCount,
        isUnreleased: comingSoon,
      } as CardData;
    })
    .filter((card): card is CardData => card !== null);
}

function mapWatchlistToTitles(
  items?: WatchlistItem[],
  cachedYears?: Map<string, number>,
): Array<Title & { uniqueKey: string }> {
  if (!items) {
    return [];
  }

  const parseNumeric = (value?: string) => {
    if (!value) {
      return undefined;
    }
    const parsed = Number(value);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
  };

  return items.map((item) => {
    const title: Title = {
      id: item.id,
      name: item.name,
      overview: item.overview ?? '',
      year: item.year && item.year > 0 ? item.year : (cachedYears?.get(item.id) ?? 0),
      language: 'en',
      mediaType: item.mediaType,
      poster: item.posterUrl ? { url: item.posterUrl, type: 'poster', width: 0, height: 0 } : undefined,
      backdrop: item.backdropUrl ? { url: item.backdropUrl, type: 'backdrop', width: 0, height: 0 } : undefined,
      imdbId: item.externalIds?.imdb,
      tmdbId: parseNumeric(item.externalIds?.tmdb),
      tvdbId: parseNumeric(item.externalIds?.tvdb),
      popularity: undefined,
      network: undefined,
    };

    return { ...title, uniqueKey: `${item.mediaType}:${item.id}` };
  });
}

function mapContinueWatchingToTitles(
  items?: SeriesWatchState[],
  seriesOverviews?: Map<string, string>,
  watchlistItems?: WatchlistItem[],
): Array<Title & { uniqueKey: string; percentWatched?: number }> {
  if (!items) {
    return [];
  }

  const parseNumeric = (value?: string) => {
    if (!value) {
      return undefined;
    }
    const parsed = Number(value);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
  };

  return items
    .map<(Title & { uniqueKey: string; percentWatched?: number }) | null>((item) => {
      const basePercent = item.percentWatched ?? 0;
      const next = item.nextEpisode;
      const isMovie = !next;
      const hasMeaningfulProgress = basePercent > MIN_CONTINUE_WATCHING_PERCENT;
      // For series: show if it has meaningful progress OR was watched recently OR has a valid next episode
      // The backend tracks next episodes, so if there's a next episode, we should show it
      const includeSeries = !isMovie && (hasMeaningfulProgress || wasLastEpisodeWatchedRecently(item) || !!next);
      const includeMovie = isMovie && hasMeaningfulProgress;
      if (!includeMovie && !includeSeries) {
        return null;
      }
      const displayPercent = Math.max(0, Math.round(item.resumePercent ?? basePercent));

      // Check if this is a movie (no nextEpisode) or a series
      if (isMovie) {
        // Movie resume - use progress info from lastWatched.overview
        const progressText = item.lastWatched?.overview || 'Resume watching';
        const title: Title = {
          id: item.seriesId,
          name: item.seriesTitle,
          overview: progressText,
          year: item.year ?? 0,
          language: 'en',
          mediaType: 'movie',
          poster: item.posterUrl ? { url: item.posterUrl, type: 'poster', width: 0, height: 0 } : undefined,
          backdrop: item.backdropUrl ? { url: item.backdropUrl, type: 'backdrop', width: 0, height: 0 } : undefined,
          imdbId: item.externalIds?.imdb,
          tmdbId: parseNumeric(item.externalIds?.tmdb),
          tvdbId: parseNumeric(item.externalIds?.tvdb),
        };

        return { ...title, uniqueKey: `movie:${item.seriesId}`, percentWatched: displayPercent };
      }

      // Series - show next episode info in the name, but use series overview
      const code = formatEpisodeCode(next?.seasonNumber, next?.episodeNumber);
      const label = next?.title ? `${item.seriesTitle} • ${code} – ${next.title}` : `${item.seriesTitle} • ${code}`;

      // Hide unreleased episodes that are too far out; show "coming soon" for those within the window
      const unreleased = next?.airDate ? isEpisodeUnreleased(next.airDate) : false;
      const comingSoon = unreleased && next?.airDate ? isEpisodeComingSoon(next.airDate) : false;
      if (unreleased && !comingSoon) {
        return null; // Too far out — hide from continue watching
      }

      // Try to get series overview from cache, watchlist, or fall back to empty string
      const cachedOverview = seriesOverviews?.get(item.seriesId);
      const watchlistItem = watchlistItems?.find((w) => w.id === item.seriesId);
      const seriesOverview = cachedOverview ?? watchlistItem?.overview ?? '';

      const title: Title = {
        id: item.seriesId,
        name: label,
        overview: seriesOverview,
        year: item.year ?? 0,
        language: 'en',
        mediaType: 'series',
        poster: item.posterUrl ? { url: item.posterUrl, type: 'poster', width: 0, height: 0 } : undefined,
        backdrop: item.backdropUrl ? { url: item.backdropUrl, type: 'backdrop', width: 0, height: 0 } : undefined,
        imdbId: item.externalIds?.imdb,
        tmdbId: parseNumeric(item.externalIds?.tmdb),
        tvdbId: parseNumeric(item.externalIds?.tvdb),
      };

      return { ...title, uniqueKey: `${item.seriesId}:${code}`, percentWatched: displayPercent, isUnreleased: comingSoon };
    })
    .filter((title): title is Title & { uniqueKey: string; percentWatched?: number; isUnreleased?: boolean } => title !== null);
}

export default React.memo(IndexScreen);
