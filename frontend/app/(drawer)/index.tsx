import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useContinueWatching } from '@/components/ContinueWatchingContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { FloatingHero } from '@/components/FloatingHero';
import MediaGrid from '@/components/MediaGrid';
import { getMovieReleaseIcon } from '@/components/MediaItem';
import { MaterialCommunityIcons } from '@expo/vector-icons';
import { useMenuContext } from '@/components/MenuContext';
import { useToast } from '@/components/ToastContext';
import { TvModal } from '@/components/TvModal';
import FocusablePressable from '@/components/FocusablePressable';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useWatchlist } from '@/components/WatchlistContext';
import { useTrendingMovies, useTrendingTVShows } from '@/hooks/useApi';
import { useMemoryMonitor } from '@/hooks/useMemoryMonitor';
import { apiService, SeriesWatchState, Title, TrendingItem, type WatchlistItem } from '@/services/api';
import { APP_VERSION } from '@/version';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  SpatialNavigationVirtualizedList,
  useSpatialNavigator,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV, getTVScaleMultiplier } from '@/theme/tokens/tvScale';
import { Direction } from '@bam.tech/lrud';
import { useFocusEffect, useIsFocused } from '@react-navigation/native';
import { LinearGradient } from 'expo-linear-gradient';
import { Stack, useRouter } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { LayoutChangeEvent, View as RNView } from 'react-native';
import { Image } from '@/components/Image';
import {
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
import Animated, {
  useAnimatedRef,
  scrollTo as reanimatedScrollTo,
  useSharedValue,
  useAnimatedReaction,
  withTiming,
  Easing,
} from 'react-native-reanimated';

// Scroll animation duration for vertical scrolling between shelves (milliseconds)
// Library default for horizontal is 200ms. Using 450ms for vertical for a smoother feel.
const TV_SCROLL_DURATION_MS = 450;
import { useSafeAreaInsets } from 'react-native-safe-area-context';

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
  theatricalRelease?: { released: boolean; date?: string }; // Movie theatrical release status
  homeRelease?: { released: boolean; date?: string }; // Movie home release status
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

// Helper to pick N random posters from displayed items
function pickRandomPosters<T>(items: T[], getPoster: (item: T) => string | undefined, count: number = 4): string[] {
  const displayedItems = items.slice(0, MAX_SHELF_ITEMS_ON_HOME);
  const itemsWithPosters = displayedItems.filter((item) => getPoster(item));
  if (itemsWithPosters.length <= count) {
    return itemsWithPosters.map((item) => getPoster(item)!);
  }
  // Fisher-Yates shuffle to pick random items
  const shuffled = [...itemsWithPosters];
  for (let i = shuffled.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [shuffled[i], shuffled[j]] = [shuffled[j], shuffled[i]];
  }
  return shuffled.slice(0, count).map((item) => getPoster(item)!);
}

// Helper to create explore card with 4-poster collage
// Uses random posters from displayed items on the shelf
// Optional overrideRemainingCount can be passed when we know the total from API
function createExploreCard(shelfId: string, allCards: CardData[], overrideRemainingCount?: number): CardData {
  const remainingCount = overrideRemainingCount ?? allCards.length - MAX_SHELF_ITEMS_ON_HOME;
  const totalCount =
    overrideRemainingCount !== undefined ? overrideRemainingCount + MAX_SHELF_ITEMS_ON_HOME : allCards.length;

  // Pick 4 random posters from displayed items
  const collagePosters = pickRandomPosters(allCards, (card) => card.cardImage, 4);

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
const RECENT_SERIES_WATCH_WINDOW_MS = 365 * 24 * 60 * 60 * 1000;

const AUTH_WARNING_MESSAGE = 'Backend URL/PIN authorization failed. Verify your settings.';

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

function IndexScreen() {
  // Memory monitoring disabled - was causing lag due to native bridge calls
  // Uncomment to debug memory issues:
  // useMemoryMonitor('HomePage', 60000, isAndroidTV);

  const { height: screenHeight, width: screenWidth } = useTVDimensions();
  const theme = useTheme();
  const router = useRouter();
  const isFocused = useIsFocused();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const spatialNavigator = useSpatialNavigator();
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
  const { refresh: refreshUserProfiles, activeUserId, pendingPinUserId } = useUserProfiles();
  const {
    data: trendingMovies,
    error: trendingMoviesError,
    refetch: refetchTrendingMovies,
  } = useTrendingMovies(activeUserId ?? undefined);
  const {
    data: trendingTVShows,
    error: trendingTVShowsError,
    refetch: refetchTrendingTVShows,
  } = useTrendingTVShows(activeUserId ?? undefined);
  const safeAreaInsets = useSafeAreaInsets();
  // Use Reanimated's animated ref for UI thread scrolling
  const scrollViewRef = useAnimatedRef<Animated.ScrollView>();
  const scrollMetricsRef = React.useRef({ offset: 0, viewportHeight: screenHeight });
  const shelfRefs = React.useRef<{ [key: string]: RNView | null }>({});
  // Cache shelf positions to avoid measureLayout on every shelf change (expensive on Android)
  const shelfPositionsRef = React.useRef<{ [key: string]: number }>({});
  // Shared value for animated vertical scrolling on TV (allows custom duration)
  const shelfScrollTargetY = useSharedValue(-1); // -1 = no pending scroll

  // Drive vertical scrolling from shared value (TV only, Android TV uses faster custom animation)
  useAnimatedReaction(
    () => shelfScrollTargetY.value,
    (targetY, prevTargetY) => {
      'worklet';
      if (targetY >= 0 && targetY !== prevTargetY) {
        reanimatedScrollTo(scrollViewRef, 0, targetY, false);
      }
    },
    [scrollViewRef],
  );

  const pageRef = React.useRef<RNView | null>(null);
  // Track initial load to skip scroll animations on first render
  const isInitialLoadRef = React.useRef(true);
  // Track if we've been focused before (to detect navigation returns vs initial load)
  const hasBeenFocusedRef = React.useRef(false);
  const {
    loading: settingsLoading,
    error: settingsError,
    settings,
    userSettings,
    lastLoadedAt: settingsLastLoadedAt,
    isBackendReachable,
    retryCountdown,
  } = useBackendSettings();
  const { showToast } = useToast();
  const hasAuthFailureRef = React.useRef(false);
  const previousSettingsLoadedAtRef = React.useRef<number | null>(null);

  // Custom list data storage (for MDBList shelves)
  const [customListData, setCustomListData] = useState<Record<string, TrendingItem[]>>({});
  const [customListTotals, setCustomListTotals] = useState<Record<string, number>>({});
  const [customListLoading, setCustomListLoading] = useState<Record<string, boolean>>({});
  const fetchedListUrlsRef = React.useRef<Set<string>>(new Set());

  // Get custom shelves from settings
  const customShelves = useMemo(() => {
    const allShelves = userSettings?.homeShelves?.shelves ?? settings?.homeShelves?.shelves ?? [];
    return allShelves.filter((shelf) => shelf.type === 'mdblist' && shelf.listUrl && shelf.enabled);
  }, [userSettings?.homeShelves?.shelves, settings?.homeShelves?.shelves]);

  // Fetch custom list data when custom shelves change
  useEffect(() => {
    if (customShelves.length === 0) return;

    const fetchCustomLists = async () => {
      for (const shelf of customShelves) {
        if (!shelf.listUrl) continue;
        // Skip if we've already fetched this URL
        if (fetchedListUrlsRef.current.has(shelf.listUrl)) continue;

        fetchedListUrlsRef.current.add(shelf.listUrl);
        setCustomListLoading((prev) => ({ ...prev, [shelf.id]: true }));

        try {
          // Only fetch limited items for home page display
          const { items, total } = await apiService.getCustomList(shelf.listUrl, MAX_SHELF_ITEMS_ON_HOME);
          setCustomListData((prev) => ({ ...prev, [shelf.id]: items }));
          setCustomListTotals((prev) => ({ ...prev, [shelf.id]: total }));
        } catch (err) {
          console.warn(`Failed to fetch custom list for shelf ${shelf.id}:`, err);
        } finally {
          setCustomListLoading((prev) => ({ ...prev, [shelf.id]: false }));
        }
      }
    };

    void fetchCustomLists();
  }, [customShelves]);

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

  // Shelf scrolling with position caching - uses Reanimated shared value for fast custom animation on Android TV
  const scrollToShelf = useCallback(
    (shelfKey: string, skipAnimation = false) => {
      if (!Platform.isTV) {
        return;
      }

      const shouldAnimate = !skipAnimation && !isInitialLoadRef.current;

      const performScroll = (targetY: number) => {
        scrollViewRef.current?.scrollTo({ y: targetY, animated: shouldAnimate });
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
          const targetY = Math.max(0, top);
          // Cache the position for future use
          shelfPositionsRef.current[shelfKey] = targetY;
          performScroll(targetY);
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
    console.log('[Homepage] Triggering full API reload');
    const callAndReport = (label: string, fn?: () => Promise<unknown> | void) => {
      if (!fn) {
        console.warn(`[Homepage] No reload function for ${label}`);
        return;
      }
      console.log(`[Homepage] Reloading ${label}...`);
      try {
        const result = fn();
        if (result && typeof (result as Promise<unknown>).then === 'function') {
          (result as Promise<unknown>)
            .then(() => {
              console.log(`[Homepage] ${label} reload succeeded`);
            })
            .catch((error) => {
              console.warn(`[Homepage] ${label} reload failed`, error);
            });
        }
      } catch (error) {
        console.warn(`[Homepage] ${label} reload threw`, error);
      }
    };

    callAndReport('User Profiles', refreshUserProfiles);
    callAndReport('Continue Watching', refreshContinueWatching);
    callAndReport('Watchlist', refreshWatchlist);
    callAndReport('Trending Movies', refetchTrendingMovies);
    callAndReport('Trending TV Shows', refetchTrendingTVShows);
  }, [refreshUserProfiles, refreshContinueWatching, refreshWatchlist, refetchTrendingMovies, refetchTrendingTVShows]);

  // Full reload when settings are applied/saved (settingsLastLoadedAt changes)
  useEffect(() => {
    // Skip on initial mount - only reload when settings actually change
    if (previousSettingsLoadedAtRef.current === null) {
      previousSettingsLoadedAtRef.current = settingsLastLoadedAt;
      return;
    }

    // If settingsLastLoadedAt changed, trigger a full reload
    if (settingsLastLoadedAt !== null && settingsLastLoadedAt !== previousSettingsLoadedAtRef.current) {
      console.log('[Homepage] Settings changed, triggering full reload');
      previousSettingsLoadedAtRef.current = settingsLastLoadedAt;
      triggerReloadAfterAuthFailure();
    }
  }, [settingsLastLoadedAt, triggerReloadAfterAuthFailure]);

  // Reload content when backend becomes reachable after being unreachable
  const previousBackendReachableRef = React.useRef<boolean | null>(null);
  useEffect(() => {
    // Skip initial mount
    if (previousBackendReachableRef.current === null) {
      previousBackendReachableRef.current = isBackendReachable;
      return;
    }

    // If backend just became reachable, trigger a full reload
    if (isBackendReachable && !previousBackendReachableRef.current) {
      console.log('[Homepage] Backend became reachable, triggering full reload');
      triggerReloadAfterAuthFailure();
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

  // Reload data when screen becomes visible (including when navigating back from details)
  // Using useEffect with isFocused instead of useFocusEffect because the screen stays mounted
  // when details page is pushed on top, so useFocusEffect doesn't trigger on navigation back
  useEffect(() => {
    if (!isFocused) {
      return;
    }

    // Determine if this is a return from navigation (not initial load)
    const isReturnFromNavigation = hasBeenFocusedRef.current;

    // Mark that we've been focused at least once
    if (!hasBeenFocusedRef.current) {
      hasBeenFocusedRef.current = true;
    }

    // Clear cached shelf positions on every focus - layout may not be ready yet or may have changed
    if (Platform.isTV) {
      shelfPositionsRef.current = {};
    }

    // Only reset shelf focus when RETURNING from navigation (e.g., details page)
    // Not on initial load - that causes unnecessary remounts
    // Skip on Android TV - the key-based remount causes IllegalStateException crashes
    if (isReturnFromNavigation && Platform.isTV && !isAndroidTV) {
      setShelfResetCounter((prev) => prev + 1);
    }

    // Programmatically grab focus to the first item of the first shelf and scroll to show it
    // Note: We compute the first shelf inline here since desktopShelves may not be ready yet
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
      const cards = shelfCardMap[firstShelfWithCards.id];
      const firstCard = cards[0];
      const rawId = String(firstCard.id ?? 0);
      const cardKey = rawId.includes(':S') ? rawId.split(':S')[0] : rawId;
      const focusId = `${firstShelfWithCards.id}-card-${cardKey}`;

      // Scroll to the first shelf position
      // On initial load: skip animation, on return: also skip to avoid jarring scroll
      // This ensures the view is in the right position before/with focus grab
      setTimeout(() => {
        scrollToShelf(firstShelfWithCards.id, true); // Skip animation
      }, 50);

      // Grab focus to the first card
      setTimeout(() => {
        spatialNavigator.grabFocus(focusId);
      }, 100);

      // Mark initial load as complete after first focus
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

    // Only silently refresh when RETURNING from navigation, not on initial load
    // Initial load data is already fresh from the context providers
    if (isReturnFromNavigation) {
      // Silently refresh continue watching and watchlist when returning from details
      refreshContinueWatching?.({ silent: true }).catch(() => {
        // Silent refresh failed - not critical
      });

      refreshWatchlist?.({ silent: true }).catch(() => {
        // Silent refresh failed - not critical
      });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- Intentionally omit card arrays and settings to prevent focus grab on every data update
  }, [isFocused, settingsLoading, hasAuthFailure, refreshContinueWatching, refreshWatchlist, scrollToShelf]);

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

  // Cache series overviews for continue watching items
  const [seriesOverviews, setSeriesOverviews] = useState<Map<string, string>>(new Map());

  // Cache years for watchlist items missing year data
  const [watchlistYears, setWatchlistYears] = useState<Map<string, number>>(new Map());

  // Cache movie release data for displaying release status badges
  const [movieReleases, setMovieReleases] = useState<
    Map<string, { theatricalRelease?: Title['theatricalRelease']; homeRelease?: Title['homeRelease'] }>
  >(new Map());

  const watchlistCards = useMemo(() => {
    const allCards = mapTrendingToCards(mapWatchlistToTrendingItems(watchlistItems, watchlistYears), movieReleases);
    if (allCards.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allCards;
    }
    // Create explore card and put it at the front
    const exploreCard = createExploreCard('watchlist', allCards);
    const limitedCards = allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return [exploreCard, ...limitedCards];
  }, [watchlistItems, watchlistYears, movieReleases]);
  const continueWatchingCards = useMemo(() => {
    const allCards = mapContinueWatchingToCards(continueWatchingItems, seriesOverviews, watchlistItems, movieReleases);
    if (allCards.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allCards;
    }
    const exploreCard = createExploreCard('continue-watching', allCards);
    const limitedCards = allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return [exploreCard, ...limitedCards];
  }, [continueWatchingItems, seriesOverviews, watchlistItems, movieReleases]);

  useEffect(() => {
    if (!continueWatchingItems || continueWatchingItems.length === 0) {
      return;
    }

    // Fetch overviews for items that don't have them yet
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
        setSeriesOverviews((prev) => new Map([...prev, ...updates]));
      }
    };

    void fetchOverviews();
  }, [continueWatchingItems, watchlistItems]);

  // Fetch missing year data for watchlist items
  useEffect(() => {
    if (!watchlistItems || watchlistItems.length === 0) {
      return;
    }

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
        if (watchlistYears.has(item.id)) {
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

      // Fetch movie details individually (no batch API for movies)
      for (const movie of moviesToFetch) {
        try {
          const details = await apiService.getMovieDetails({
            imdbId: movie.imdbId,
            tmdbId: movie.tmdbId ? Number(movie.tmdbId) : undefined,
            name: movie.name,
          });
          if (details?.year && details.year > 0) {
            updates.set(movie.id, details.year);
          }
        } catch (error) {
          console.warn(`Failed to fetch movie year for ${movie.name}:`, error);
        }
      }

      if (updates.size > 0) {
        setWatchlistYears((prev) => new Map([...prev, ...updates]));
      }
    };

    void fetchMissingYears();
  }, [watchlistItems, watchlistYears]);

  // Fetch release data for movies when releaseStatus badge is enabled
  useEffect(() => {
    // Only fetch if releaseStatus badge is enabled
    const badgeVisibility = userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility ?? [];
    if (!badgeVisibility.includes('releaseStatus')) {
      return;
    }

    // Collect all movies that need release data
    const moviesToFetch: Array<{ id: string; tmdbId?: number; imdbId?: string }> = [];

    // From trending movies
    if (trendingMovies) {
      for (const item of trendingMovies) {
        if (
          item.title.mediaType === 'movie' &&
          (item.title.tmdbId || item.title.imdbId) &&
          !movieReleases.has(item.title.id) &&
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
          !movieReleases.has(item.title.id) &&
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
        if (
          isMovie &&
          tmdbId &&
          !movieReleases.has(item.seriesId)
        ) {
          moviesToFetch.push({ id: item.seriesId, tmdbId });
        }
      }
    }

    // From watchlist (movies only)
    if (watchlistItems) {
      for (const item of watchlistItems) {
        const tmdbId = item.externalIds?.tmdb ? Number(item.externalIds.tmdb) : undefined;
        if (
          item.mediaType === 'movie' &&
          tmdbId &&
          !movieReleases.has(item.id)
        ) {
          moviesToFetch.push({ id: item.id, tmdbId });
        }
      }
    }

    if (moviesToFetch.length === 0) {
      return;
    }

    const fetchReleases = async () => {
      try {
        const batchResponse = await apiService.batchMovieReleases(
          moviesToFetch.map((m) => ({ titleId: m.id, tmdbId: m.tmdbId, imdbId: m.imdbId })),
        );

        const updates = new Map<
          string,
          { theatricalRelease?: Title['theatricalRelease']; homeRelease?: Title['homeRelease'] }
        >();

        for (let i = 0; i < batchResponse.results.length; i++) {
          const result = batchResponse.results[i];
          const movie = moviesToFetch[i];

          if (!result.error) {
            updates.set(movie.id, {
              theatricalRelease: result.theatricalRelease,
              homeRelease: result.homeRelease,
            });
          }
        }

        if (updates.size > 0) {
          setMovieReleases((prev) => new Map([...prev, ...updates]));
        }
      } catch (error) {
        console.warn('Failed to batch fetch movie releases:', error);
      }
    };

    void fetchReleases();
  }, [trendingMovies, customListData, continueWatchingItems, watchlistItems, userSettings?.display?.badgeVisibility, settings?.display?.badgeVisibility, movieReleases]);

  const trendingMovieCards = useMemo(() => {
    const allCards = mapTrendingToCards(trendingMovies ?? undefined, movieReleases);
    if (allCards.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allCards;
    }
    const exploreCard = createExploreCard('trending-movies', allCards);
    return [exploreCard, ...allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME)];
  }, [trendingMovies, movieReleases]);

  const trendingShowCards = useMemo(() => {
    const allCards = mapTrendingToCards(trendingTVShows ?? undefined);
    if (allCards.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allCards;
    }
    const exploreCard = createExploreCard('trending-shows', allCards);
    return [exploreCard, ...allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME)];
  }, [trendingTVShows]);

  // Generate cards for each custom list shelf
  const customListCards = useMemo(() => {
    const result: Record<string, CardData[]> = {};
    for (const [shelfId, items] of Object.entries(customListData)) {
      const allCards = mapTrendingToCards(items, movieReleases);
      const totalCount = customListTotals[shelfId] ?? allCards.length;
      if (totalCount <= MAX_SHELF_ITEMS_ON_HOME) {
        result[shelfId] = allCards;
      } else {
        // Create explore card showing remaining count based on total
        const remainingCount = totalCount - MAX_SHELF_ITEMS_ON_HOME;
        const exploreCard = createExploreCard(shelfId, allCards, remainingCount);
        result[shelfId] = [exploreCard, ...allCards.slice(0, MAX_SHELF_ITEMS_ON_HOME)];
      }
    }
    return result;
  }, [customListData, customListTotals, movieReleases]);

  // Generate titles for each custom list shelf (mobile)
  const customListTitles = useMemo(() => {
    const result: Record<string, (Title & { uniqueKey: string; collagePosters?: string[] })[]> = {};
    for (const [shelfId, items] of Object.entries(customListData)) {
      const allTitles = items.map((item) => {
        // Merge cached release data for movies
        const cachedReleases = item.title.mediaType === 'movie' ? movieReleases.get(item.title.id) : undefined;
        return {
          ...item.title,
          uniqueKey: `custom:${shelfId}:${item.title.id}`,
          theatricalRelease: item.title.theatricalRelease ?? cachedReleases?.theatricalRelease,
          homeRelease: item.title.homeRelease ?? cachedReleases?.homeRelease,
        };
      });
      const totalCount = customListTotals[shelfId] ?? allTitles.length;
      if (totalCount <= MAX_SHELF_ITEMS_ON_HOME) {
        result[shelfId] = allTitles;
      } else {
        const remainingCount = totalCount - MAX_SHELF_ITEMS_ON_HOME;
        // Pick random posters from displayed items
        const collagePosters = pickRandomPosters(allTitles, (title) => title?.poster?.url, 4);
        const exploreTitle: Title & { uniqueKey: string; collagePosters?: string[] } = {
          id: `${EXPLORE_CARD_ID_PREFIX}${shelfId}`,
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
          uniqueKey: `explore:${shelfId}`,
          collagePosters,
        };
        const limitedTitles = allTitles.slice(0, MAX_SHELF_ITEMS_ON_HOME);
        result[shelfId] = [exploreTitle, ...limitedTitles];
      }
    }
    return result;
  }, [customListData, customListTotals, movieReleases]);

  const watchlistTitles = useMemo(() => {
    const baseTitles = mapWatchlistToTitles(watchlistItems, watchlistYears);
    // Merge cached release data for movies
    const allTitles = baseTitles.map((title) => {
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
    if (allTitles.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allTitles;
    }
    const remainingCount = allTitles.length - MAX_SHELF_ITEMS_ON_HOME;
    // Pick random posters from displayed items
    const collagePosters = pickRandomPosters(allTitles, (title) => title?.poster?.url, 4);
    const exploreTitle: Title & { uniqueKey: string; collagePosters?: string[] } = {
      id: `${EXPLORE_CARD_ID_PREFIX}watchlist`,
      name: 'Explore',
      overview: `View all ${allTitles.length} items`,
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
    return [exploreTitle, ...limitedTitles];
  }, [watchlistItems, watchlistYears, movieReleases]);
  const continueWatchingTitles = useMemo(() => {
    const baseTitles = mapContinueWatchingToTitles(continueWatchingItems, seriesOverviews, watchlistItems);
    // Merge cached release data for movies
    const allTitles = baseTitles.map((title) => {
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
    if (allTitles.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allTitles;
    }
    const remainingCount = allTitles.length - MAX_SHELF_ITEMS_ON_HOME;
    // Pick random posters from displayed items
    const collagePosters = pickRandomPosters(allTitles, (title) => title?.poster?.url, 4);
    const exploreTitle: Title & { uniqueKey: string; collagePosters?: string[] } = {
      id: `${EXPLORE_CARD_ID_PREFIX}continue-watching`,
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
      uniqueKey: 'explore:continue-watching',
      collagePosters,
    };
    const limitedTitles = allTitles.slice(0, MAX_SHELF_ITEMS_ON_HOME);
    return [exploreTitle, ...limitedTitles];
  }, [continueWatchingItems, seriesOverviews, watchlistItems, movieReleases]);
  const trendingMovieTitles = useMemo(() => {
    const allTitles =
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
    if (allTitles.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allTitles;
    }
    const remainingCount = allTitles.length - MAX_SHELF_ITEMS_ON_HOME;
    // Pick random posters from displayed items
    const collagePosters = pickRandomPosters(allTitles, (title) => title?.poster?.url, 4);
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
    return [exploreTitle, ...limitedTitles];
  }, [trendingMovies, movieReleases]);

  const trendingShowTitles = useMemo(() => {
    const allTitles =
      trendingTVShows?.map((item) => ({
        ...item.title,
        uniqueKey: `show:${item.title.id}`,
      })) ?? [];
    if (allTitles.length <= MAX_SHELF_ITEMS_ON_HOME) {
      return allTitles;
    }
    const remainingCount = allTitles.length - MAX_SHELF_ITEMS_ON_HOME;
    // Pick random posters from displayed items
    const collagePosters = pickRandomPosters(allTitles, (title) => title?.poster?.url, 4);
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
    return [exploreTitle, ...limitedTitles];
  }, [trendingTVShows]);

  const [focusedDesktopCard, setFocusedDesktopCard] = useState<CardData | null>(null);
  const [mobileHeroIndex, setMobileHeroIndex] = useState(0);

  // Ref for hero carousel ScrollView
  const heroScrollRef = useRef<ScrollView>(null);
  // Ref to store stable shuffled hero items (prevents re-shuffling on data load)
  const stableHeroItemsRef = useRef<CardData[]>([]);
  const heroItemKeysRef = useRef<Set<string>>(new Set());
  const isUserScrolling = useRef(false);

  const [focusedShelfKey, setFocusedShelfKey] = useState<string | null>(null);
  const [heroImageDimensions, setHeroImageDimensions] = useState<{ width: number; height: number } | null>(null);
  const [shelfResetCounter, setShelfResetCounter] = useState(0);

  // Remove from Continue Watching confirmation modal state
  const [isRemoveConfirmVisible, setIsRemoveConfirmVisible] = useState(false);
  const [pendingRemoveItem, setPendingRemoveItem] = useState<{ id: string; name: string } | null>(null);

  // Version mismatch modal state
  const [isVersionMismatchVisible, setIsVersionMismatchVisible] = useState(false);
  const [backendVersion, setBackendVersion] = useState<string | null>(null);
  const versionCheckDoneRef = React.useRef(false);

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

  const handleDismissVersionMismatch = useCallback(() => {
    setIsVersionMismatchVisible(false);
  }, []);

  // Detect image orientation from URL pattern (avoids expensive Image.getSize network call)
  // backdropUrl = landscape, posterUrl = portrait, headerImage = check for poster patterns
  useEffect(() => {
    if (!focusedDesktopCard || !Platform.isTV) {
      setHeroImageDimensions(null);
      return;
    }

    const imageUrl = focusedDesktopCard.backdropUrl || focusedDesktopCard.headerImage;
    if (!imageUrl) {
      setHeroImageDimensions(null);
      return;
    }

    // Detect portrait from URL pattern instead of fetching image
    // Poster URLs typically contain width indicators (w185, w342, w500, w780) for portrait images
    // Backdrop URLs use larger widths (w780, w1280, original) for landscape
    const isPosterUrl =
      /\/w(?:185|342|500)\//i.test(imageUrl) ||
      (!focusedDesktopCard.backdropUrl && focusedDesktopCard.posterUrl === imageUrl);

    if (isPosterUrl) {
      // Portrait aspect ratio (2:3)
      setHeroImageDimensions({ width: 500, height: 750 });
    } else {
      // Landscape aspect ratio (16:9)
      setHeroImageDimensions({ width: 1920, height: 1080 });
    }
  }, [focusedDesktopCard]);

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

  // Debounce hero updates - only update after focus settles
  const focusDebounceRef = React.useRef<ReturnType<typeof setTimeout> | null>(null);
  const handleCardFocus = useCallback((card: CardData) => {
    // Clear any pending update
    if (focusDebounceRef.current) {
      clearTimeout(focusDebounceRef.current);
    }
    // Wait for focus to settle before updating hero
    // Use shorter debounce on Android TV for snappier feel
    const debounceMs = isAndroidTV ? 150 : 500;
    focusDebounceRef.current = setTimeout(() => {
      setFocusedDesktopCard(card);
    }, debounceMs);
  }, []);

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

  // Calculate hero width for carousel scrolling (90% of screen width with spacing between items)
  const heroGap = theme.spacing.md;
  const heroWidth = Math.round(screenWidth * 0.9);
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
    if (!shouldUseMobileLayout || mobileHeroItems.length <= 1 || !isFocused) {
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
  }, [shouldUseMobileLayout, mobileHeroItems.length, isFocused, heroSnapInterval]);

  const handleCardSelect = useCallback(
    (card: CardData) => {
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
            console.log('[ContinueWatching] Metadata lookup:', {
              cardId: card.id,
              cardIdWithoutEpisode,
              seriesId: state.seriesId,
              matches: state.seriesId === cardIdWithoutEpisode,
            });
            return state.seriesId === cardIdWithoutEpisode;
          })
        : isContinueWatchingMovie
          ? continueWatchingItems?.find((state) => state.seriesId === String(card.id))
          : null;

      console.log('[ContinueWatching] Card select:', {
        cardId: card.id,
        cardMediaType: card.mediaType,
        isContinueWatching,
        hasMetadata: !!metadata,
        nextEpisode: metadata?.nextEpisode,
        continueWatchingItemsCount: continueWatchingItems?.length ?? 0,
      });

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

      console.log('[ContinueWatching] Navigation params:', params);

      router.push({
        pathname: '/details',
        params,
      });
    },
    [router, continueWatchingItems, watchlistItems, seriesOverviews],
  );

  const handleTitlePress = useCallback(
    (item: Title) => {
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
    },
    [router, continueWatchingItems],
  );

  const handleContinueWatchingPress = useCallback(
    (item: Title) => {
      const metadata = continueWatchingItems?.find((state) => state.seriesId === item.id);
      if (!metadata || !metadata.nextEpisode) {
        handleTitlePress(item);
        return;
      }

      // Use the same fallback logic as the card data - prefer backdrop for landscape hero
      const headerImage = metadata.backdropUrl || metadata.posterUrl || '';
      const posterUrl = metadata.posterUrl || '';
      const backdropUrl = metadata.backdropUrl || '';

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

  // TV: Listen for LongEnter to remove items from Continue Watching
  useEffect(() => {
    if (!Platform.isTV || !isFocused) {
      return;
    }

    const handleLongEnter = (key: SupportedKeys) => {
      if (key !== SupportedKeys.LongEnter) {
        return;
      }

      // Only handle when a continue watching item is focused
      if (focusedShelfKey !== 'continue-watching' || !focusedDesktopCard) {
        console.log('[Homepage] LongEnter ignored - not on continue watching shelf or no focused card');
        return;
      }

      console.log('[Homepage] LongEnter detected on continue watching item:', focusedDesktopCard.id);

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
  }, [isFocused, focusedShelfKey, focusedDesktopCard]);

  // Optimized: Direct shelf scrolling without title parameter
  // Track last focused shelf to avoid unnecessary state updates
  const lastFocusedShelfKeyRef = React.useRef<string | null>(null);
  const handleRowFocus = useCallback(
    (shelfKey: string) => {
      // Only update state if shelf actually changed (avoid re-renders)
      if (lastFocusedShelfKeyRef.current !== shelfKey) {
        lastFocusedShelfKeyRef.current = shelfKey;
        setFocusedShelfKey(shelfKey);
      }
      scrollToShelf(shelfKey);
    },
    [scrollToShelf],
  );

  const handleSafeAreaLayout = useCallback((event: LayoutChangeEvent) => {
    if (__DEV__ && Platform.OS === 'ios') {
      console.log('[SafeArea] Home SafeAreaView layout', event.nativeEvent.layout);
    }
  }, []);

  const handleMobileScrollLayout = useCallback((event: LayoutChangeEvent) => {
    if (__DEV__ && Platform.OS === 'ios') {
      console.log('[SafeArea] Home mobile ScrollView layout', event.nativeEvent.layout);
    }
  }, []);

  const handleMobileContentSizeChange = useCallback((width: number, height: number) => {
    if (__DEV__ && Platform.OS === 'ios') {
      console.log('[SafeArea] Home mobile ScrollView content size', { width, height });
    }
  }, []);

  const handleDesktopLayout = useCallback((event: LayoutChangeEvent) => {
    if (__DEV__ && Platform.OS === 'ios') {
      console.log('[SafeArea] Home desktop layout', event.nativeEvent.layout);
    }
  }, []);

  const handleDesktopScrollLayout = useCallback(
    (event: LayoutChangeEvent) => {
      if (__DEV__ && Platform.OS === 'ios') {
        console.log('[SafeArea] Home desktop ScrollView layout', event.nativeEvent.layout);
      }
      scrollMetricsRef.current.viewportHeight = event.nativeEvent.layout.height;
    },
    [scrollMetricsRef],
  );

  const handleDesktopContentSizeChange = useCallback((width: number, height: number) => {
    if (__DEV__ && Platform.OS === 'ios') {
      console.log('[SafeArea] Home desktop ScrollView content size', { width, height });
    }
    // Clear cached shelf positions when content size changes - positions are now stale
    if (Platform.isTV) {
      shelfPositionsRef.current = {};
    }
  }, []);

  // Desktop shelves configuration - moved before conditional return to satisfy React hooks rules
  const desktopShelves = useMemo(() => {
    // Skip computation for mobile layout
    if (shouldUseMobileLayout) return [];

    // Get shelf configuration from user settings, fall back to global settings, then default order
    const shelfConfig = userSettings?.homeShelves?.shelves ??
      settings?.homeShelves?.shelves ?? [
        { id: 'continue-watching', name: 'Continue Watching', enabled: true, order: 0 },
        { id: 'watchlist', name: 'Your Watchlist', enabled: true, order: 1 },
        { id: 'trending-movies', name: 'Trending Movies', enabled: true, order: 2 },
        { id: 'trending-tv', name: 'Trending TV Shows', enabled: true, order: 3 },
      ];

    // Map shelf IDs to their data
    const shelfDataMap: Record<
      string,
      {
        cards: CardData[];
        autoFocus: boolean;
        collapseIfEmpty: boolean;
        showEmptyState: boolean;
      }
    > = {
      'continue-watching': {
        cards: continueWatchingCards,
        autoFocus: true,
        collapseIfEmpty: true,
        showEmptyState: continueWatchingLoading,
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

    // Add custom list shelves to the map (include all custom shelves, even if data not loaded yet)
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
      .filter((config) => config.enabled)
      .sort((a, b) => a.order - b.order)
      .map((config, index) => {
        const data = shelfDataMap[config.id];
        if (!data) {
          return null;
        }
        return {
          key: config.id,
          title: config.name,
          cards: data.cards,
          autoFocus: index === 0 && data.cards.length > 0,
          collapseIfEmpty: data.collapseIfEmpty,
          showEmptyState: data.showEmptyState,
        };
      })
      .filter((shelf): shelf is NonNullable<typeof shelf> => shelf !== null);

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
  ]);

  // Track navigation structure changes for debugging
  const navigationKey = useMemo(() => {
    if (!desktopShelves || desktopShelves.length === 0) return `shelves-empty-${shelfResetCounter}`;
    return `shelves-${desktopShelves.map((s) => `${s.key}-${s.cards.length}`).join('-')}-reset-${shelfResetCounter}`;
  }, [desktopShelves, shelfResetCounter]);

  // Determine if we should show the fade gradient (any row except the first is focused)
  const focusedShelfIndex = useMemo(() => {
    if (!focusedShelfKey || !desktopShelves || desktopShelves.length === 0) return -1;
    return desktopShelves.findIndex((shelf) => shelf.key === focusedShelfKey);
  }, [focusedShelfKey, desktopShelves]);

  const _shouldShowTopGradient = focusedShelfIndex > 0;

  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      if (movement === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

  if (shouldUseMobileLayout) {
    if (!mobileStyles) {
      return <View style={{ flex: 1, backgroundColor: theme.colors.background.base }} />;
    }

    // Get shelf configuration from user settings, fall back to global settings, then default order
    const mobileShelfConfig = userSettings?.homeShelves?.shelves ??
      settings?.homeShelves?.shelves ?? [
        { id: 'continue-watching', name: 'Continue Watching', enabled: true, order: 0 },
        { id: 'watchlist', name: 'Your Watchlist', enabled: true, order: 1 },
        { id: 'trending-movies', name: 'Trending Movies', enabled: true, order: 2 },
        { id: 'trending-tv', name: 'Trending TV Shows', enabled: true, order: 3 },
      ];

    // Map shelf IDs to their data for mobile
    const mobileShelfDataMap: Record<
      string,
      {
        titles: Array<Title & { uniqueKey: string }>;
        loading?: boolean;
        onItemPress: (item: Title) => void;
        onItemLongPress?: (item: Title) => void;
      }
    > = {
      'continue-watching': {
        titles: continueWatchingTitles,
        loading: continueWatchingLoading,
        onItemPress: handleContinueWatchingPress,
        onItemLongPress: handleContinueWatchingLongPress,
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

    // Add custom list shelves to the mobile map (include all custom shelves, even if data not loaded yet)
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
        <FixedSafeAreaView style={mobileStyles.safeArea} edges={['top']} onLayout={handleSafeAreaLayout}>
          <ScrollView
            style={mobileStyles.container}
            contentContainerStyle={mobileStyles.content}
            contentInsetAdjustmentBehavior="never"
            automaticallyAdjustContentInsets={false}
            onLayout={handleMobileScrollLayout}
            onContentSizeChange={handleMobileContentSizeChange}
          >
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
                    contentContainerStyle={{ paddingHorizontal: heroPadding }}
                  >
                    {mobileHeroItems.map((item, index) => (
                      <Pressable
                        key={`hero-${item.id}-${index}`}
                        style={[mobileStyles.hero, { width: heroWidth, marginRight: heroGap }]}
                        onPress={() => handleCardSelect(item)}
                      >
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
                  }}
                >
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
              .filter((config) => config.enabled)
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
                      badgeVisibility={userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility}
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
          onRequestClose={handleCancelRemove}
        >
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
                  style={[mobileStyles.modalButton, mobileStyles.modalButtonDanger]}
                >
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
          onRequestClose={handleDismissVersionMismatch}
        >
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

  return (
    <SpatialNavigationRoot
      isActive={isFocused && !isMenuOpen && !pendingPinUserId && !isRemoveConfirmVisible && !isVersionMismatchVisible}
      onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}
    >
      <Stack.Screen options={{ headerShown: false }} />
      <View ref={pageRef} style={desktopStyles?.styles.page} onLayout={handleDesktopLayout}>
        {Platform.isTV && (
          <View
            style={desktopStyles?.styles.topSpacer}
            pointerEvents="none"
            renderToHardwareTextureAndroid={isAndroidTV}
          >
            {focusedDesktopCard &&
              heroImageDimensions &&
              (() => {
                const imageUrl = focusedDesktopCard.backdropUrl || focusedDesktopCard.headerImage;
                const isPortrait = heroImageDimensions.height > heroImageDimensions.width;

                // Android TV: use cover fit and no blur for performance
                if (isAndroidTV) {
                  return (
                    <View style={desktopStyles?.styles.topContent}>
                      <View style={desktopStyles?.styles.topHeroContainer}>
                        <Image
                          source={imageUrl}
                          style={desktopStyles?.styles.topHeroImage}
                          contentFit="cover"
                          transition={0}
                        />
                      </View>
                      <View style={desktopStyles?.styles.topTextContainer}>
                        <Text style={desktopStyles?.styles.topTitle} numberOfLines={2}>
                          {focusedDesktopCard.title}
                        </Text>
                        {focusedDesktopCard.year && (
                          <Text
                            style={[
                              desktopStyles?.styles.topYear,
                              {
                                fontSize: desktopStyles?.styles.topYear.fontSize * 1.25,
                                lineHeight: desktopStyles?.styles.topYear.lineHeight * 1.25,
                              },
                            ]}
                          >
                            {focusedDesktopCard.year}
                          </Text>
                        )}
                        <Text
                          style={[
                            desktopStyles?.styles.topDescription,
                            {
                              fontSize: desktopStyles?.styles.topDescription.fontSize * 1.25,
                              lineHeight: desktopStyles?.styles.topDescription.lineHeight * 1.25,
                            },
                          ]}
                          numberOfLines={4}
                        >
                          {focusedDesktopCard.description}
                        </Text>
                      </View>
                    </View>
                  );
                }

                return (
                  <View style={desktopStyles?.styles.topContent}>
                    <View style={desktopStyles?.styles.topHeroContainer}>
                      {isPortrait ? (
                        <>
                          <Image
                            source={imageUrl}
                            style={[StyleSheet.absoluteFillObject]}
                            contentFit="cover"
                            blurRadius={50}
                          />
                          <Image source={imageUrl} style={desktopStyles?.styles.topHeroImage} contentFit="contain" />
                        </>
                      ) : (
                        <Image source={imageUrl} style={desktopStyles?.styles.topHeroImage} contentFit="contain" />
                      )}
                    </View>
                    <View style={desktopStyles?.styles.topTextContainer}>
                      <Text style={desktopStyles?.styles.topTitle} numberOfLines={2}>
                        {focusedDesktopCard.title}
                      </Text>
                      {focusedDesktopCard.year && (
                        <Text style={desktopStyles?.styles.topYear}>{focusedDesktopCard.year}</Text>
                      )}
                      <Text style={desktopStyles?.styles.topDescription} numberOfLines={6}>
                        {focusedDesktopCard.description}
                      </Text>
                    </View>
                  </View>
                );
              })()}
          </View>
        )}
        {/* Bottom gradient fade for visual polish on TV - disabled on Android TV for performance */}
        {Platform.isTV && !isAndroidTV && (
          <LinearGradient
            colors={[
              'transparent',
              `${theme.colors.background.base}40`,
              `${theme.colors.background.base}B3`,
              theme.colors.background.base,
            ]}
            locations={[0, 0.3, 0.7, 1]}
            start={{ x: 0, y: 0 }}
            end={{ x: 0, y: 1 }}
            style={desktopStyles?.styles.bottomFadeGradient}
            pointerEvents="none"
          />
        )}

        <SpatialNavigationNode orientation="vertical">
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
            onContentSizeChange={handleDesktopContentSizeChange}
          >
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
              {desktopShelves.map((shelf) => (
                <MemoizedDesktopShelf
                  key={shelf.key}
                  title={shelf.title}
                  cards={shelf.cards}
                  styles={desktopStyles!.styles}
                  cardWidth={desktopStyles!.cardWidth}
                  cardHeight={desktopStyles!.cardHeight}
                  cardSpacing={desktopStyles!.cardSpacing}
                  onCardSelect={handleCardSelect}
                  onCardFocus={handleCardFocus}
                  onRowFocus={handleRowFocus}
                  autoFocus={shelf.autoFocus && shelf.cards.length > 0}
                  collapseIfEmpty={shelf.collapseIfEmpty}
                  showEmptyState={shelf.showEmptyState}
                  shelfKey={shelf.key}
                  registerShelfRef={registerShelfRef}
                  isInitialLoad={isInitialLoadRef.current}
                  badgeVisibility={userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility}
                />
              ))}
            </View>
          </Animated.ScrollView>
        </SpatialNavigationNode>
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
        <TvModal visible={isRemoveConfirmVisible} onRequestClose={handleCancelRemove}>
          <View style={desktopStyles.styles.tvModalContainer}>
            <Text style={desktopStyles.styles.tvModalTitle}>Remove from Continue Watching?</Text>
            <Text style={desktopStyles.styles.tvModalSubtitle}>
              Are you sure you want to remove "{pendingRemoveItem?.name}" from Continue Watching?
            </Text>
            <SpatialNavigationNode orientation="horizontal">
              <View style={desktopStyles.styles.tvModalActions}>
                <DefaultFocus>
                  <FocusablePressable
                    focusKey="remove-confirm-cancel"
                    text="Cancel"
                    onSelect={handleCancelRemove}
                    style={desktopStyles.styles.tvModalButton}
                    focusedStyle={desktopStyles.styles.tvModalButtonFocused}
                    textStyle={desktopStyles.styles.tvModalButtonText}
                    focusedTextStyle={desktopStyles.styles.tvModalButtonTextFocused}
                  />
                </DefaultFocus>
                <FocusablePressable
                  focusKey="remove-confirm-remove"
                  text="Remove"
                  onSelect={handleConfirmRemove}
                  style={[desktopStyles.styles.tvModalButton, desktopStyles.styles.tvModalButtonDanger]}
                  focusedStyle={[
                    desktopStyles.styles.tvModalButtonFocused,
                    desktopStyles.styles.tvModalButtonDangerFocused,
                  ]}
                  textStyle={[desktopStyles.styles.tvModalButtonText, desktopStyles.styles.tvModalButtonDangerText]}
                  focusedTextStyle={[
                    desktopStyles.styles.tvModalButtonTextFocused,
                    desktopStyles.styles.tvModalButtonDangerTextFocused,
                  ]}
                />
              </View>
            </SpatialNavigationNode>
          </View>
        </TvModal>

        {/* Version Mismatch Warning Modal (TV) */}
        <TvModal visible={isVersionMismatchVisible} onRequestClose={handleDismissVersionMismatch}>
          <View style={desktopStyles.styles.tvModalContainer}>
            <Text style={desktopStyles.styles.tvModalTitle}>Version Mismatch</Text>
            <Text style={desktopStyles.styles.tvModalSubtitle}>
              Frontend version ({APP_VERSION}) does not match backend version ({backendVersion ?? 'unknown'}). You may
              experience unexpected behavior. Consider updating.
            </Text>
            <SpatialNavigationNode orientation="horizontal">
              <View style={desktopStyles.styles.tvModalActions}>
                <DefaultFocus>
                  <FocusablePressable
                    focusKey="version-mismatch-ok"
                    text="OK"
                    onSelect={handleDismissVersionMismatch}
                    style={desktopStyles.styles.tvModalButton}
                    focusedStyle={desktopStyles.styles.tvModalButtonFocused}
                    textStyle={desktopStyles.styles.tvModalButtonText}
                    focusedTextStyle={desktopStyles.styles.tvModalButtonTextFocused}
                  />
                </DefaultFocus>
              </View>
            </SpatialNavigationNode>
          </View>
        </TvModal>
      </View>
    </SpatialNavigationRoot>
  );
}

type VirtualizedShelfProps = {
  title: string;
  cards: CardData[];
  styles: ReturnType<typeof createDesktopStyles>['styles'];
  onCardSelect: (card: CardData) => void;
  onCardFocus: (card: CardData) => void;
  onRowFocus: (shelfKey: string) => void;
  autoFocus?: boolean;
  collapseIfEmpty?: boolean;
  showEmptyState?: boolean;
  shelfKey: string;
  registerShelfRef: (key: string, ref: RNView | null) => void;
  isInitialLoad?: boolean;
  cardWidth: number;
  cardHeight: number;
  cardSpacing: number;
  badgeVisibility?: string[]; // Which badges to show: watchProgress, releaseStatus
};

// Alias for backwards compatibility
type DesktopShelfProps = VirtualizedShelfProps;

function VirtualizedShelf({
  title,
  cards,
  styles,
  onCardSelect,
  onCardFocus,
  onRowFocus,
  autoFocus,
  collapseIfEmpty,
  showEmptyState,
  shelfKey,
  registerShelfRef,
  cardWidth,
  cardHeight,
  cardSpacing,
  badgeVisibility,
}: VirtualizedShelfProps) {
  const containerRef = React.useRef<RNView | null>(null);
  const isEmpty = cards.length === 0;
  const shouldCollapse = Boolean(collapseIfEmpty && isEmpty);
  const lastFocusTimeRef = React.useRef<number>(0);

  // Set the ref for the parent component
  React.useEffect(() => {
    registerShelfRef(shelfKey, containerRef.current);
    return () => {
      registerShelfRef(shelfKey, null);
    };
  }, [registerShelfRef, shelfKey]);

  // Calculate item size for virtualized list (card width + spacing)
  const itemSize = cardWidth + cardSpacing;

  // Render item callback for SpatialNavigationVirtualizedList
  const renderItem = useCallback(
    ({ item, index }: { item: CardData; index: number }) => {
      const card = item;
      // Use stable key: for series with episode codes, use just the series ID
      const rawId = String(card.id ?? index);
      const cardKey = rawId.includes(':S') ? rawId.split(':S')[0] : rawId;
      // Create a stable focusId for programmatic focus control
      const focusId = `${shelfKey}-card-${cardKey}`;

      return (
        <SpatialNavigationFocusableView
          onSelect={() => onCardSelect(card)}
          focusKey={focusId}
          onFocus={() => {
            const now = Date.now();
            const timeSinceLastFocus = now - lastFocusTimeRef.current;

            // Prevent rapid focus changes that can cause wrap-around bugs
            if (Platform.isTV && timeSinceLastFocus < 50) {
              return;
            }

            lastFocusTimeRef.current = now;
            onCardFocus(card);
            onRowFocus(shelfKey);
          }}
        >
          {({ isFocused }: { isFocused: boolean }) => {
            // Android TV rendering with 2x badge and full content
            if (isAndroidTV) {
              // Explore card collage for Android TV
              if (card.mediaType === 'explore' && card.collagePosters && card.collagePosters.length >= 4) {
                return (
                  <Pressable
                    style={[styles.card, isFocused && styles.cardFocused]}
                    renderToHardwareTextureAndroid
                    tvParallaxProperties={{ enabled: false }}
                  >
                    <View style={{ flexDirection: 'row', flexWrap: 'wrap', width: '100%', height: '100%' }}>
                      {card.collagePosters.slice(0, 4).map((poster, i) => (
                        <Image
                          key={`collage-${i}`}
                          source={poster}
                          style={{ width: '50%', height: '50%' }}
                          contentFit="cover"
                          transition={0}
                        />
                      ))}
                    </View>
                    <View style={[styles.cardTextContainer, { position: 'absolute', bottom: 0, left: 0, right: 0 }]}>
                      <LinearGradient
                        pointerEvents="none"
                        colors={['transparent', 'rgba(0,0,0,0.8)', 'rgba(0,0,0,1)']}
                        locations={[0, 0.5, 1]}
                        start={{ x: 0.5, y: 0 }}
                        end={{ x: 0.5, y: 1 }}
                        style={styles.cardTextGradient}
                      />
                      <Text style={styles.cardTitleAndroidTV} numberOfLines={1}>
                        {card.title}
                      </Text>
                      {card.year ? <Text style={styles.cardMetaAndroidTV}>{card.year}</Text> : null}
                    </View>
                  </Pressable>
                );
              }
              // Compute release icon for movies
              const releaseIcon = card.mediaType === 'movie' ? getMovieReleaseIcon({
                id: String(card.id),
                name: card.title,
                overview: card.description,
                year: typeof card.year === 'number' ? card.year : parseInt(String(card.year)) || 0,
                language: 'en',
                homeRelease: card.homeRelease,
                theatricalRelease: card.theatricalRelease,
              }) : null;
              const showReleaseStatus = badgeVisibility?.includes('releaseStatus') && releaseIcon;
              return (
                <Pressable
                  style={[styles.card, isFocused && styles.cardFocused]}
                  renderToHardwareTextureAndroid
                  tvParallaxProperties={{ enabled: false }}
                >
                  <Image source={card.cardImage} style={styles.cardImage} contentFit="cover" transition={0} />
                  {showReleaseStatus && (
                    <View style={styles.releaseStatusBadgeAndroidTV}>
                      <MaterialCommunityIcons
                        name={releaseIcon.name}
                        size={styles.releaseStatusIconAndroidTV.fontSize}
                        color={releaseIcon.color}
                      />
                    </View>
                  )}
                  {card.percentWatched !== undefined && card.percentWatched >= MIN_CONTINUE_WATCHING_PERCENT && (
                    <View style={styles.progressBadgeAndroidTV}>
                      <Text style={styles.progressBadgeTextAndroidTV}>{Math.round(card.percentWatched)}%</Text>
                    </View>
                  )}
                  <View style={styles.cardTextContainer}>
                    <LinearGradient
                      pointerEvents="none"
                      colors={['transparent', 'rgba(0,0,0,0.8)', 'rgba(0,0,0,1)']}
                      locations={[0, 0.5, 1]}
                      start={{ x: 0.5, y: 0 }}
                      end={{ x: 0.5, y: 1 }}
                      style={styles.cardTextGradient}
                    />
                    <Text style={styles.cardTitleAndroidTV} numberOfLines={2}>
                      {card.title}
                    </Text>
                    {card.year ? <Text style={styles.cardMetaAndroidTV}>{card.year}</Text> : null}
                  </View>
                </Pressable>
              );
            }
            // Full rendering for other platforms
            // Special rendering for explore cards with collage
            if (card.mediaType === 'explore' && card.collagePosters && card.collagePosters.length >= 4) {
              return (
                <Pressable
                  style={[styles.card, isFocused && styles.cardFocused]}
                  tvParallaxProperties={{ enabled: false }}
                >
                  <View style={{ flexDirection: 'row', flexWrap: 'wrap', width: '100%', height: '100%' }}>
                    {card.collagePosters.slice(0, 4).map((poster, i) => (
                      <Image
                        key={`collage-${i}`}
                        source={poster}
                        style={{ width: '50%', height: '50%' }}
                        contentFit="cover"
                        transition={0}
                      />
                    ))}
                  </View>
                  <View style={[styles.cardTextContainer, { position: 'absolute', bottom: 0, left: 0, right: 0 }]}>
                    <LinearGradient
                      pointerEvents="none"
                      colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.8)', 'rgba(0,0,0,1)']}
                      locations={[0, 0.5, 1]}
                      start={{ x: 0.5, y: 0 }}
                      end={{ x: 0.5, y: 1 }}
                      style={styles.cardTextGradient}
                    />
                    <Text style={styles.cardTitle} numberOfLines={1}>
                      {card.title}
                    </Text>
                    {card.year ? <Text style={styles.cardMeta}>{card.year}</Text> : null}
                  </View>
                </Pressable>
              );
            }
            // Compute release icon for movies (non-Android TV platforms)
            const releaseIconRegular = card.mediaType === 'movie' ? getMovieReleaseIcon({
              id: String(card.id),
              name: card.title,
              overview: card.description,
              year: typeof card.year === 'number' ? card.year : parseInt(String(card.year)) || 0,
              language: 'en',
              homeRelease: card.homeRelease,
              theatricalRelease: card.theatricalRelease,
            }) : null;
            const showReleaseStatusRegular = badgeVisibility?.includes('releaseStatus') && releaseIconRegular;
            return (
              <Pressable
                style={[styles.card, isFocused && styles.cardFocused]}
                tvParallaxProperties={{ enabled: false }}
              >
                <Image
                  key={`img-${cardKey}`}
                  source={card.cardImage}
                  style={styles.cardImage}
                  contentFit="cover"
                  transition={0}
                  cachePolicy={Platform.isTV ? 'disk' : 'memory'}
                  recyclingKey={cardKey}
                />
                {showReleaseStatusRegular && (
                  <View style={styles.releaseStatusBadge}>
                    <MaterialCommunityIcons
                      name={releaseIconRegular.name}
                      size={styles.releaseStatusIcon.fontSize}
                      color={releaseIconRegular.color}
                    />
                  </View>
                )}
                {card.percentWatched !== undefined && card.percentWatched >= MIN_CONTINUE_WATCHING_PERCENT && (
                  <View style={styles.progressBadge}>
                    <Text style={styles.progressBadgeText}>{Math.round(card.percentWatched)}%</Text>
                  </View>
                )}
                <View style={styles.cardTextContainer}>
                  <LinearGradient
                    pointerEvents="none"
                    colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
                    locations={[0, 0.6, 1]}
                    start={{ x: 0.5, y: 0 }}
                    end={{ x: 0.5, y: 1 }}
                    style={styles.cardTextGradient}
                  />
                  <Text style={styles.cardTitle} numberOfLines={2}>
                    {card.title}
                  </Text>
                  {card.year ? <Text style={styles.cardMeta}>{card.year}</Text> : null}
                </View>
              </Pressable>
            );
          }}
        </SpatialNavigationFocusableView>
      );
    },
    [onCardFocus, onCardSelect, onRowFocus, shelfKey, styles, badgeVisibility],
  );

  if (shouldCollapse) {
    return (
      <View ref={containerRef} style={[styles.shelf, styles.shelfCollapsed]} accessibilityElementsHidden>
        {/* Don't include SpatialNavigationNode for collapsed shelves */}
      </View>
    );
  }

  const shouldShowEmptyState = Boolean(showEmptyState && isEmpty);

  // Calculate row height for the virtualized list container
  const rowHeight = cardHeight + cardSpacing;

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
        <SpatialNavigationNode orientation="horizontal">
          <View style={{ height: rowHeight }} renderToHardwareTextureAndroid={isAndroidTV}>
            {autoFocus ? (
              <DefaultFocus>
                <SpatialNavigationVirtualizedList
                  data={cards}
                  renderItem={renderItem}
                  itemSize={itemSize}
                  orientation="horizontal"
                  numberOfRenderedItems={isAndroidTV ? 9 : 13}
                  numberOfItemsVisibleOnScreen={isAndroidTV ? 5 : 7}
                  onEndReachedThresholdItemsNumber={3}
                />
              </DefaultFocus>
            ) : (
              <SpatialNavigationVirtualizedList
                data={cards}
                renderItem={renderItem}
                itemSize={itemSize}
                orientation="horizontal"
                numberOfRenderedItems={isAndroidTV ? 9 : 13}
                numberOfItemsVisibleOnScreen={isAndroidTV ? 5 : 7}
                onEndReachedThresholdItemsNumber={3}
              />
            )}
          </View>
        </SpatialNavigationNode>
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
    prev.onCardFocus === next.onCardFocus &&
    prev.onRowFocus === next.onRowFocus &&
    prev.autoFocus === next.autoFocus &&
    prev.collapseIfEmpty === next.collapseIfEmpty &&
    prev.showEmptyState === next.showEmptyState &&
    prev.shelfKey === next.shelfKey &&
    prev.registerShelfRef === next.registerShelfRef &&
    prev.isInitialLoad === next.isInitialLoad &&
    prev.cardWidth === next.cardWidth &&
    prev.cardHeight === next.cardHeight &&
    prev.cardSpacing === next.cardSpacing &&
    prev.badgeVisibility === next.badgeVisibility
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
      height: screenHeight * 0.4, // 40% of screen height for tvOS
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      zIndex: 1,
    },
    topFadeGradient: {
      position: 'absolute',
      top: '40%', // Start at the bottom of the top section
      left: 0,
      right: 0,
      height: '9%', // 15% of the lower 60% = 0.15 * 0.6 = 0.09 = 9% of total screen
      zIndex: 5,
    },
    bottomFadeGradient: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: '10%', // Bottom 10% of screen
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
      marginTop: Platform.isTV ? screenHeight * 0.4 : 0,
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
    // Android TV release status badge (scaled up)
    releaseStatusBadgeAndroidTV: {
      position: 'absolute',
      top: theme.spacing.md,
      left: theme.spacing.md,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: androidTVBadgePaddingH,
      paddingVertical: androidTVBadgePaddingV,
      borderRadius: androidTVBadgeRadius,
      zIndex: 2,
    },
    releaseStatusIconAndroidTV: {
      fontSize: Math.round(14 * androidTVBadgeScale),
    },
    // TV Modal styles
    tvModalContainer: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.lg,
      padding: theme.spacing['2xl'],
      minWidth: 400,
      maxWidth: 600,
      gap: theme.spacing.xl,
      alignItems: 'center',
    },
    tvModalTitle: {
      ...theme.typography.title.lg,
      fontSize: Math.round(theme.typography.title.lg.fontSize * 1.5 * tvScale),
      lineHeight: Math.round(theme.typography.title.lg.lineHeight * 1.5 * tvScale),
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    tvModalSubtitle: {
      ...theme.typography.body.md,
      fontSize: Math.round(theme.typography.body.md.fontSize * 1.25 * tvScale),
      lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.25 * tvScale),
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
      fontSize: Math.round(theme.typography.body.md.fontSize * 1.25 * tvScale),
      lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.25 * tvScale),
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

  return {
    styles,
    cardWidth,
    cardHeight,
    cardSpacing,
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
    return {
      id: item.title.id,
      title: item.title.name,
      description: item.title.overview || 'No description available',
      headerImage:
        item.title.backdrop?.url ||
        item.title.poster?.url ||
        'https://via.placeholder.com/1920x1080/333/fff?text=No+Image',
      cardImage:
        item.title.poster?.url || item.title.backdrop?.url || 'https://via.placeholder.com/600x900/333/fff?text=No+Image',
      mediaType: item.title.mediaType,
      posterUrl: item.title.poster?.url,
      backdropUrl: item.title.backdrop?.url,
      tmdbId: item.title.tmdbId,
      imdbId: item.title.imdbId,
      tvdbId: item.title.tvdbId,
      year: item.title.year,
      theatricalRelease: item.title.theatricalRelease ?? cached?.theatricalRelease,
      homeRelease: item.title.homeRelease ?? cached?.homeRelease,
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
          theatricalRelease: cached?.theatricalRelease,
          homeRelease: cached?.homeRelease,
        };
      }

      // Series - show next episode info
      const code = formatEpisodeCode(next?.seasonNumber, next?.episodeNumber);
      const baseSeriesId = item.seriesId;
      const watchlistItem = watchlistItems?.find((w) => w.id === baseSeriesId);
      // Prefer overview from API response, then async-fetched cache, then watchlist
      const seriesOverview = item.overview || seriesOverviews?.get(baseSeriesId) || watchlistItem?.overview || '';
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

      return { ...title, uniqueKey: `${item.seriesId}:${code}`, percentWatched: displayPercent };
    })
    .filter((title): title is Title & { uniqueKey: string; percentWatched?: number } => title !== null);
}

export default React.memo(IndexScreen);
