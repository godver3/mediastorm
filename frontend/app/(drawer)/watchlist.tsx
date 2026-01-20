import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useContinueWatching } from '@/components/ContinueWatchingContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import MediaGrid from '@/components/MediaGrid';
import { useMenuContext } from '@/components/MenuContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useWatchlist } from '@/components/WatchlistContext';
import { apiService, type Title, type TrendingItem } from '@/services/api';
import { mapWatchlistToTitles } from '@/services/watchlist';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Direction } from '@bam.tech/lrud';
import { Ionicons } from '@expo/vector-icons';
import { useIsFocused } from '@react-navigation/native';
import { Stack, useLocalSearchParams, useNavigation, useRouter } from 'expo-router';
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { Platform, StyleSheet, Text, View } from 'react-native';
import { isTablet, responsiveSize, tvScale } from '@/theme/tokens/tvScale';
import { useTVDimensions } from '@/hooks/useTVDimensions';

type WatchlistTitle = Title & { uniqueKey?: string };

// Number of items to load per batch for progressive loading
const INITIAL_LOAD_COUNT = 30;
const LOAD_MORE_COUNT = 30;

// Spatial navigation filter button - uses SpatialNavigationFocusableView for D-pad navigation
// Styled to match TVActionButton for visual consistency
const SpatialFilterButton = ({
  label,
  icon,
  isActive,
  onSelect,
  theme,
}: {
  label: string;
  icon: keyof typeof Ionicons.glyphMap;
  isActive: boolean;
  onSelect: () => void;
  theme: NovaTheme;
}) => {
  // Match TVActionButton sizing exactly
  const scale = tvScale(1.375, 1);
  const baseIconSize = 24;
  const scaledIconSize = tvScale(baseIconSize * 1.375, baseIconSize); // Same as TVActionButton
  const paddingH = theme.spacing.md * scale;
  const paddingV = theme.spacing.sm * scale;
  const borderRadius = theme.radius.md * scale;
  const fontSize = theme.typography.label.md.fontSize * scale;
  const lineHeight = theme.typography.label.md.lineHeight * scale;
  const gap = theme.spacing.sm * scale;

  // Consistent border width to prevent button resizing when active state changes
  const borderWidth = 2 * scale;

  return (
    <SpatialNavigationFocusableView onSelect={onSelect}>
      {({ isFocused }: { isFocused: boolean }) => (
        <View
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            gap,
            paddingHorizontal: paddingH,
            paddingVertical: paddingV,
            borderRadius,
            backgroundColor: isFocused
              ? theme.colors.accent.primary
              : isActive
                ? 'transparent'
                : theme.colors.overlay.button,
            borderWidth,
            borderColor: isFocused || isActive ? theme.colors.accent.primary : 'transparent',
          }}>
          <Ionicons
            name={icon}
            size={scaledIconSize}
            color={isFocused ? theme.colors.text.inverse : isActive ? theme.colors.accent.primary : theme.colors.text.primary}
          />
          <Text
            style={{
              color: isFocused ? theme.colors.text.inverse : isActive ? theme.colors.accent.primary : theme.colors.text.primary,
              fontSize,
              lineHeight,
              fontWeight: '500',
            }}>
            {label}
          </Text>
        </View>
      )}
    </SpatialNavigationFocusableView>
  );
};

export default function WatchlistScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const router = useRouter();
  const { activeUserId, pendingPinUserId } = useUserProfiles();
  const { settings, userSettings } = useBackendSettings();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const isFocused = useIsFocused();
  const isActive = isFocused && !isMenuOpen && !pendingPinUserId;

  // Handle left navigation at edge to open menu
  const onDirectionHandledWithoutMovement = useCallback(
    (direction: Direction) => {
      if (direction === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

  // Get shelf and collection parameters - if present, we're exploring a non-watchlist shelf
  const { shelf: shelfId, collection: collectionId, collectionName } = useLocalSearchParams<{
    shelf?: string;
    collection?: string;
    collectionName?: string;
  }>();
  const isExploreMode = !!shelfId || !!collectionId;
  const isCollectionMode = !!collectionId;

  // Get shelf configuration for custom lists
  const shelfConfig = useMemo(() => {
    if (!shelfId) return null;
    const allShelves = userSettings?.homeShelves?.shelves ?? settings?.homeShelves?.shelves ?? [];
    return allShelves.find((s) => s.id === shelfId) ?? null;
  }, [userSettings?.homeShelves?.shelves, settings?.homeShelves?.shelves, shelfId]);

  // Watchlist data
  const { items, loading: watchlistLoading, error: watchlistError } = useWatchlist();

  // Continue watching data
  const { items: continueWatchingItems, loading: continueWatchingLoading } = useContinueWatching();

  // Progressive loading state for Explore mode (trending movies, trending TV, custom lists)
  const [exploreItems, setExploreItems] = useState<TrendingItem[]>([]);
  const [exploreTotal, setExploreTotal] = useState(0);
  const [exploreLoading, setExploreLoading] = useState(false);
  const [exploreLoadingMore, setExploreLoadingMore] = useState(false);
  const [exploreError, setExploreError] = useState<string | null>(null);
  const loadedOffsetRef = useRef(0);
  const isLoadingMoreRef = useRef(false);

  // Collection mode state
  const [collectionItems, setCollectionItems] = useState<Title[]>([]);
  const [collectionLoading, setCollectionLoading] = useState(false);
  const [collectionError, setCollectionError] = useState<string | null>(null);

  // Fetch collection data when in collection mode
  useEffect(() => {
    if (!isCollectionMode || !collectionId) {
      setCollectionItems([]);
      return;
    }

    const fetchCollection = async () => {
      setCollectionLoading(true);
      setCollectionError(null);
      try {
        const collectionIdNum = parseInt(collectionId, 10);
        if (isNaN(collectionIdNum)) {
          throw new Error('Invalid collection ID');
        }
        const details = await apiService.getCollectionDetails(collectionIdNum);
        setCollectionItems(details.movies);
      } catch (err) {
        setCollectionError(err instanceof Error ? err.message : 'Failed to load collection');
      } finally {
        setCollectionLoading(false);
      }
    };

    void fetchCollection();
  }, [isCollectionMode, collectionId]);

  // Cache for movie release data
  const [movieReleases, setMovieReleases] = useState<
    Map<string, { theatricalRelease?: Title['theatricalRelease']; homeRelease?: Title['homeRelease'] }>
  >(new Map());

  const isCustomList = shelfConfig?.type === 'mdblist' && !!shelfConfig?.listUrl;
  const isTrendingMovies = shelfId === 'trending-movies';
  const isTrendingTV = shelfId === 'trending-tv' || shelfId === 'trending-shows';
  const needsProgressiveLoading = isTrendingMovies || isTrendingTV || isCustomList;

  // Fetch explore data with progressive loading
  const fetchExploreData = useCallback(
    async (offset: number, limit: number, isInitial: boolean) => {
      if (!needsProgressiveLoading) return;

      if (isInitial) {
        setExploreLoading(true);
        setExploreError(null);
        setExploreItems([]);
        loadedOffsetRef.current = 0;
      } else {
        setExploreLoadingMore(true);
      }

      try {
        let items: TrendingItem[] = [];
        let total = 0;

        if (isTrendingMovies) {
          const response = await apiService.getTrendingMovies(
            activeUserId ?? undefined,
            limit,
            offset,
            shelfConfig?.hideUnreleased,
          );
          if ('items' in response) {
            items = response.items;
            total = response.total;
          }
        } else if (isTrendingTV) {
          const response = await apiService.getTrendingTVShows(
            activeUserId ?? undefined,
            limit,
            offset,
            shelfConfig?.hideUnreleased,
          );
          if ('items' in response) {
            items = response.items;
            total = response.total;
          }
        } else if (isCustomList && shelfConfig?.listUrl) {
          const response = await apiService.getCustomList(
            shelfConfig.listUrl,
            limit,
            offset,
            shelfConfig?.hideUnreleased,
          );
          items = response.items;
          total = response.total;
        }

        if (isInitial) {
          setExploreItems(items);
        } else {
          setExploreItems((prev) => [...prev, ...items]);
        }
        setExploreTotal(total);
        loadedOffsetRef.current = offset + items.length;
      } catch (err) {
        setExploreError(err instanceof Error ? err.message : 'Failed to load items');
      } finally {
        setExploreLoading(false);
        setExploreLoadingMore(false);
        isLoadingMoreRef.current = false;
      }
    },
    [
      needsProgressiveLoading,
      isTrendingMovies,
      isTrendingTV,
      isCustomList,
      shelfConfig?.listUrl,
      shelfConfig?.hideUnreleased,
      activeUserId,
    ],
  );

  // Initial fetch when explore mode changes
  useEffect(() => {
    if (!needsProgressiveLoading) {
      setExploreItems([]);
      setExploreTotal(0);
      return;
    }

    void fetchExploreData(0, INITIAL_LOAD_COUNT, true);
  }, [needsProgressiveLoading, shelfId, shelfConfig?.listUrl, activeUserId, fetchExploreData]);

  // Load more items when user scrolls near the end
  const handleLoadMore = useCallback(() => {
    if (!needsProgressiveLoading) return;
    if (isLoadingMoreRef.current) return;
    if (exploreLoading || exploreLoadingMore) return;
    if (loadedOffsetRef.current >= exploreTotal) return; // All items loaded

    isLoadingMoreRef.current = true;
    void fetchExploreData(loadedOffsetRef.current, LOAD_MORE_COUNT, false);
  }, [needsProgressiveLoading, exploreLoading, exploreLoadingMore, exploreTotal, fetchExploreData]);

  // Check if there are more items to load
  const hasMoreItems = needsProgressiveLoading && loadedOffsetRef.current < exploreTotal;

  // Determine current loading state (initial loading only, not load more)
  const loading = useMemo(() => {
    if (!isExploreMode) return watchlistLoading;
    if (isCollectionMode) return collectionLoading;
    if (shelfId === 'continue-watching') return continueWatchingLoading;
    if (needsProgressiveLoading) return exploreLoading;
    return false;
  }, [isExploreMode, isCollectionMode, shelfId, watchlistLoading, collectionLoading, continueWatchingLoading, needsProgressiveLoading, exploreLoading]);

  // Determine current error state
  const error = useMemo(() => {
    if (!isExploreMode) return watchlistError;
    if (isCollectionMode) return collectionError;
    if (needsProgressiveLoading) return exploreError;
    return null;
  }, [isExploreMode, isCollectionMode, watchlistError, collectionError, needsProgressiveLoading, exploreError]);

  // Cache years for watchlist items missing year data
  const [watchlistYears, setWatchlistYears] = useState<Map<string, number>>(new Map());
  // Track which IDs we've already queued for year fetching (prevents re-fetch cascade)
  const fetchedYearIdsRef = useRef<Set<string>>(new Set());

  // Fetch missing year data for watchlist items
  useEffect(() => {
    if (!items || items.length === 0) {
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

      for (const item of items) {
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
        } catch (fetchError) {
          console.warn('Failed to batch fetch series years:', fetchError);
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
        } catch (fetchError) {
          console.warn(`Failed to fetch movie year for ${movie.name}:`, fetchError);
        }
      }

      if (updates.size > 0) {
        setWatchlistYears((prev) => new Map([...prev, ...updates]));
      }
    };

    void fetchMissingYears();
  }, [items]); // Removed watchlistYears from deps - using ref instead

  // Track which movie IDs we've already fetched releases for (avoids re-fetching on re-renders)
  const fetchedReleaseIdsRef = useRef<Set<string>>(new Set());

  // Fetch release data for movies when releaseStatus badge is enabled
  useEffect(() => {
    const badgeVisibility = userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility ?? [];
    if (!badgeVisibility.includes('releaseStatus')) {
      return;
    }

    const moviesToFetch: Array<{ id: string; tmdbId?: number; imdbId?: string }> = [];

    // From watchlist
    if (items) {
      for (const item of items) {
        const tmdbId = item.externalIds?.tmdb ? Number(item.externalIds.tmdb) : undefined;
        const imdbId = item.externalIds?.imdb;
        if (item.mediaType === 'movie' && (tmdbId || imdbId) && !fetchedReleaseIdsRef.current.has(item.id)) {
          moviesToFetch.push({ id: item.id, tmdbId, imdbId });
        }
      }
    }

    // From continue watching (movies only - no nextEpisode)
    if (continueWatchingItems) {
      for (const item of continueWatchingItems) {
        const isMovie = !item.nextEpisode;
        const tmdbId = item.externalIds?.tmdb ? Number(item.externalIds.tmdb) : undefined;
        const imdbId = item.externalIds?.imdb;
        if (isMovie && (tmdbId || imdbId) && !fetchedReleaseIdsRef.current.has(item.seriesId)) {
          moviesToFetch.push({ id: item.seriesId, tmdbId, imdbId });
        }
      }
    }

    // From explore items (trending movies, trending TV, custom lists)
    for (const item of exploreItems) {
      if (
        item.title.mediaType === 'movie' &&
        (item.title.tmdbId || item.title.imdbId) &&
        !fetchedReleaseIdsRef.current.has(item.title.id) &&
        !item.title.theatricalRelease &&
        !item.title.homeRelease
      ) {
        moviesToFetch.push({ id: item.title.id, tmdbId: item.title.tmdbId, imdbId: item.title.imdbId });
      }
    }

    if (moviesToFetch.length === 0) {
      return;
    }

    // Mark these IDs as being fetched (before async to prevent duplicate fetches)
    for (const movie of moviesToFetch) {
      fetchedReleaseIdsRef.current.add(movie.id);
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
  }, [
    items,
    continueWatchingItems,
    exploreItems,
    userSettings?.display?.badgeVisibility,
    settings?.display?.badgeVisibility,
    // Removed movieReleases from deps - using ref to track fetched IDs instead
  ]);

  const watchlistTitles = useMemo(() => {
    const baseTitles = mapWatchlistToTitles(items, watchlistYears);
    // Merge cached release data for movies
    return baseTitles.map((title) => {
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
  }, [items, watchlistYears, movieReleases]);

  // Map continue watching items to titles
  const continueWatchingTitles = useMemo((): WatchlistTitle[] => {
    if (!continueWatchingItems) return [];
    return continueWatchingItems.map((item) => {
      // Determine media type: if there's a nextEpisode, it's a series; otherwise it's a movie
      const isMovie = !item.nextEpisode;
      const cachedReleases = isMovie ? movieReleases.get(item.seriesId) : undefined;
      return {
        id: item.seriesId,
        name: item.seriesTitle,
        overview: item.overview ?? '',
        year: item.year ?? 0,
        language: 'en',
        mediaType: isMovie ? 'movie' : 'series',
        poster: item.posterUrl ? { url: item.posterUrl, type: 'poster' as const, width: 0, height: 0 } : undefined,
        backdrop: item.backdropUrl
          ? { url: item.backdropUrl, type: 'backdrop' as const, width: 0, height: 0 }
          : undefined,
        uniqueKey: `cw:${item.seriesId}`,
        theatricalRelease: cachedReleases?.theatricalRelease,
        homeRelease: cachedReleases?.homeRelease,
      };
    });
  }, [continueWatchingItems, movieReleases]);

  // Map explore items (trending movies, trending TV, custom lists) to titles
  const exploreTitles = useMemo((): WatchlistTitle[] => {
    if (!needsProgressiveLoading || exploreItems.length === 0) return [];

    // Determine prefix based on shelf type
    const prefix = isTrendingMovies ? 'tm' : isTrendingTV ? 'ttv' : 'cl';

    return exploreItems.map((item, index) => {
      const cachedReleases = item.title.mediaType === 'movie' ? movieReleases.get(item.title.id) : undefined;
      return {
        ...item.title,
        uniqueKey: `${prefix}:${item.title.id}-${index}`,
        theatricalRelease: item.title.theatricalRelease ?? cachedReleases?.theatricalRelease,
        homeRelease: item.title.homeRelease ?? cachedReleases?.homeRelease,
      };
    });
  }, [needsProgressiveLoading, exploreItems, isTrendingMovies, isTrendingTV, movieReleases]);

  // Map collection items to titles
  const collectionTitles = useMemo((): WatchlistTitle[] => {
    if (!isCollectionMode || collectionItems.length === 0) return [];

    return collectionItems.map((item, index) => {
      const cachedReleases = movieReleases.get(item.id);
      return {
        ...item,
        uniqueKey: `col:${item.id}-${index}`,
        theatricalRelease: item.theatricalRelease ?? cachedReleases?.theatricalRelease,
        homeRelease: item.homeRelease ?? cachedReleases?.homeRelease,
      };
    });
  }, [isCollectionMode, collectionItems, movieReleases]);

  // Select the appropriate titles based on mode
  const allTitles = useMemo((): WatchlistTitle[] => {
    if (!isExploreMode) return watchlistTitles;
    if (isCollectionMode) return collectionTitles;
    if (shelfId === 'continue-watching') return continueWatchingTitles;
    if (needsProgressiveLoading) return exploreTitles;
    return [];
  }, [isExploreMode, isCollectionMode, shelfId, watchlistTitles, collectionTitles, continueWatchingTitles, needsProgressiveLoading, exploreTitles]);

  // Page title based on mode
  const pageTitle = useMemo(() => {
    if (!isExploreMode) return 'Your Watchlist';
    if (isCollectionMode && collectionName) return decodeURIComponent(collectionName);
    if (shelfConfig?.name) return shelfConfig.name;
    if (shelfId === 'continue-watching') return 'Continue Watching';
    if (shelfId === 'trending-movies') return 'Trending Movies';
    if (shelfId === 'trending-tv' || shelfId === 'trending-shows') return 'Trending TV Shows';
    return 'Explore';
  }, [isExploreMode, isCollectionMode, collectionName, shelfConfig?.name, shelfId]);

  // Tab title - show "Explore" when in explore mode, otherwise "Watchlist"
  const tabTitle = isExploreMode ? 'Explore' : 'Watchlist';

  // Update the tab/navigation title dynamically
  const navigation = useNavigation();
  useLayoutEffect(() => {
    navigation.setOptions({ title: tabTitle });
  }, [navigation, tabTitle]);

  const [filter, setFilter] = useState<'all' | 'movie' | 'series'>('all');

  const filteredTitles = useMemo(() => {
    if (filter === 'all') return allTitles;
    return allTitles.filter((title) => title.mediaType === filter);
  }, [filter, allTitles]);

  const filterOptions: Array<{ key: 'all' | 'movie' | 'series'; label: string; icon: keyof typeof Ionicons.glyphMap }> =
    [
      { key: 'all', label: 'All', icon: 'grid-outline' },
      { key: 'movie', label: 'Movies', icon: 'film-outline' },
      { key: 'series', label: 'TV Shows', icon: 'tv-outline' },
    ];

  const handleTitlePress = useCallback(
    (title: WatchlistTitle) => {
      router.push({
        pathname: '/details',
        params: {
          title: title.name,
          titleId: title.id ?? '',
          mediaType: title.mediaType ?? 'movie',
          description: title.overview ?? '',
          headerImage: title.backdrop?.url ?? title.poster?.url ?? '',
          posterUrl: title.poster?.url ?? '',
          backdropUrl: title.backdrop?.url ?? '',
          tmdbId: title.tmdbId ? String(title.tmdbId) : '',
          imdbId: title.imdbId ?? '',
          tvdbId: title.tvdbId ? String(title.tvdbId) : '',
          year: title.year ? String(title.year) : '',
        },
      });
    },
    [router],
  );

  const emptyMessage = useMemo(() => {
    if (allTitles.length === 0) {
      if (isExploreMode) {
        return `No items in ${pageTitle}`;
      }
      return 'Your watchlist is empty';
    }
    if (filter === 'movie') {
      return isExploreMode ? 'No movies in this list' : 'No movies in your watchlist';
    }
    if (filter === 'series') {
      return isExploreMode ? 'No TV shows in this list' : 'No TV shows in your watchlist';
    }
    return isExploreMode ? 'No items in this list' : 'Your watchlist is empty';
  }, [filter, allTitles.length, isExploreMode, pageTitle]);

  // Number of columns based on device type and orientation
  // Mobile: 2, Tablet portrait: 4, Tablet landscape: 6, TV: 6
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const isLandscape = screenWidth > screenHeight;
  const numColumns = useMemo(() => {
    if (Platform.isTV) return 6;
    if (isTablet) return isLandscape ? 6 : 4;
    return 2; // Mobile
  }, [isLandscape]);

  return (
    <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          <SpatialNavigationNode orientation="vertical">
            {/* Page title */}
            <View style={styles.titleRow}>
              <Text style={styles.title}>{pageTitle}</Text>
            </View>

            {/* Filter buttons row - hidden in collection mode since collections are all movies */}
            {!isCollectionMode && (
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.filtersRow}>
                  {filterOptions.map((option, index) => {
                    const button = (
                      <SpatialFilterButton
                        key={option.key}
                        label={option.label}
                        icon={option.icon}
                        isActive={filter === option.key}
                        onSelect={() => setFilter(option.key)}
                        theme={theme}
                      />
                    );
                    // Give first filter button default focus
                    return index === 0 ? <DefaultFocus key={option.key}>{button}</DefaultFocus> : button;
                  })}
                </View>
              </SpatialNavigationNode>
            )}

            {/* Grid content - hide title in collection mode since page title already shows it */}
            <MediaGrid
              title={isCollectionMode ? '' : pageTitle}
              items={filteredTitles}
              loading={loading}
              error={error}
              onItemPress={handleTitlePress}
              layout="grid"
              numColumns={numColumns}
              defaultFocusFirstItem={false}
              badgeVisibility={userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility}
              emptyMessage={emptyMessage}
              onEndReached={handleLoadMore}
              loadingMore={exploreLoadingMore}
              hasMoreItems={hasMoreItems}
            />
          </SpatialNavigationNode>
        </View>
      </FixedSafeAreaView>
    </SpatialNavigationRoot>
  );
}

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
    },
    container: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: theme.spacing.xl,
      paddingTop: theme.spacing.xl,
    },
    titleRow: {
      marginBottom: theme.spacing.lg,
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    filtersRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.lg,
      marginBottom: theme.spacing.lg,
    },
  });
