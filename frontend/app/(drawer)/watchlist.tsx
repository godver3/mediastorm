import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useContinueWatching } from '@/components/ContinueWatchingContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import MediaGrid from '@/components/MediaGrid';
import { useMenuContext } from '@/components/MenuContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useWatchlist } from '@/components/WatchlistContext';
import { useTrendingMovies, useTrendingTVShows } from '@/hooks/useApi';
import { apiService, type Title, type TrendingItem, type SeriesWatchState } from '@/services/api';
import {
  DefaultFocus,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  useSpatialNavigator,
} from '@/services/tv-navigation';
import { mapWatchlistToTitles } from '@/services/watchlist';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { Stack, useLocalSearchParams, useNavigation, useRouter } from 'expo-router';
import { useCallback, useEffect, useLayoutEffect, useMemo, useState } from 'react';
import { Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { isAndroidTV } from '@/theme/tokens/tvScale';

type WatchlistTitle = Title & { uniqueKey?: string };

// Native filter button for Android TV - uses Pressable with style function (no re-renders)
const NativeFilterButton = ({
  label,
  icon,
  isActive,
  onPress,
  autoFocus,
  theme,
}: {
  label: string;
  icon: keyof typeof Ionicons.glyphMap;
  isActive: boolean;
  onPress: () => void;
  autoFocus?: boolean;
  theme: NovaTheme;
}) => (
  <Pressable
    onPress={onPress}
    hasTVPreferredFocus={autoFocus}
    style={({ focused }) => [
      {
        flexDirection: 'row',
        alignItems: 'center',
        gap: theme.spacing.sm,
        paddingHorizontal: theme.spacing['2xl'],
        paddingVertical: theme.spacing.md,
        borderRadius: theme.radius.md,
        backgroundColor: theme.colors.background.surface,
        borderWidth: focused ? 2 : StyleSheet.hairlineWidth,
        borderColor: focused
          ? theme.colors.text.primary
          : isActive
            ? theme.colors.accent.primary
            : theme.colors.border.subtle,
      },
    ]}
  >
    <Ionicons name={icon} size={20} color={theme.colors.text.primary} />
    <Text style={{ color: theme.colors.text.primary, fontSize: 16, fontWeight: '500' }}>{label}</Text>
  </Pressable>
);

export default function WatchlistScreen() {
  const theme = useTheme();
  const { width: screenWidth } = useTVDimensions();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const router = useRouter();
  const isFocused = useIsFocused();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const { pendingPinUserId, activeUserId } = useUserProfiles();
  const { settings, userSettings } = useBackendSettings();
  const isActive = isFocused && !isMenuOpen && !pendingPinUserId;

  // Get shelf parameter - if present, we're exploring a non-watchlist shelf
  const { shelf: shelfId } = useLocalSearchParams<{ shelf?: string }>();
  const isExploreMode = !!shelfId;

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

  // Trending data
  const {
    data: trendingMovies,
    error: trendingMoviesError,
    loading: trendingMoviesLoading,
  } = useTrendingMovies(activeUserId ?? undefined);
  const {
    data: trendingTVShows,
    error: trendingTVShowsError,
    loading: trendingTVShowsLoading,
  } = useTrendingTVShows(activeUserId ?? undefined);

  // Custom list data
  const [customListItems, setCustomListItems] = useState<TrendingItem[]>([]);
  const [customListLoading, setCustomListLoading] = useState(false);
  const [customListError, setCustomListError] = useState<string | null>(null);

  // Cache for movie release data
  const [movieReleases, setMovieReleases] = useState<
    Map<string, { theatricalRelease?: Title['theatricalRelease']; homeRelease?: Title['homeRelease'] }>
  >(new Map());

  const isCustomList = shelfConfig?.type === 'mdblist' && !!shelfConfig?.listUrl;

  // Fetch custom list data when needed
  useEffect(() => {
    if (!isCustomList || !shelfConfig?.listUrl) {
      return;
    }

    const fetchCustomList = async () => {
      setCustomListLoading(true);
      setCustomListError(null);
      try {
        // Fetch all items (no limit) for the explore page
        const { items: fetchedItems } = await apiService.getCustomList(shelfConfig.listUrl!);
        setCustomListItems(fetchedItems);
      } catch (err) {
        setCustomListError(err instanceof Error ? err.message : 'Failed to load list');
      } finally {
        setCustomListLoading(false);
      }
    };

    void fetchCustomList();
  }, [isCustomList, shelfConfig?.listUrl]);

  // Determine current loading state
  const loading = useMemo(() => {
    if (!isExploreMode) return watchlistLoading;
    if (shelfId === 'continue-watching') return continueWatchingLoading;
    if (shelfId === 'trending-movies') return trendingMoviesLoading;
    if (shelfId === 'trending-tv' || shelfId === 'trending-shows') return trendingTVShowsLoading;
    if (isCustomList) return customListLoading;
    return false;
  }, [
    isExploreMode,
    shelfId,
    watchlistLoading,
    continueWatchingLoading,
    trendingMoviesLoading,
    trendingTVShowsLoading,
    isCustomList,
    customListLoading,
  ]);

  // Determine current error state
  const error = useMemo(() => {
    if (!isExploreMode) return watchlistError;
    if (shelfId === 'trending-movies') return trendingMoviesError ?? null;
    if (shelfId === 'trending-tv' || shelfId === 'trending-shows') return trendingTVShowsError ?? null;
    if (isCustomList) return customListError;
    return null;
  }, [isExploreMode, shelfId, watchlistError, trendingMoviesError, trendingTVShowsError, isCustomList, customListError]);

  // Cache years for watchlist items missing year data
  const [watchlistYears, setWatchlistYears] = useState<Map<string, number>>(new Map());

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
        if (watchlistYears.has(item.id)) {
          continue;
        }

        const isSeries =
          item.mediaType === 'series' || item.mediaType === 'tv' || item.mediaType === 'show';

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
  }, [items, watchlistYears]);

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
        if (item.mediaType === 'movie' && (tmdbId || imdbId) && !movieReleases.has(item.id)) {
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
        if (isMovie && (tmdbId || imdbId) && !movieReleases.has(item.seriesId)) {
          moviesToFetch.push({ id: item.seriesId, tmdbId, imdbId });
        }
      }
    }

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

    // From custom list
    for (const item of customListItems) {
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
  }, [items, continueWatchingItems, trendingMovies, customListItems, userSettings?.display?.badgeVisibility, settings?.display?.badgeVisibility, movieReleases]);

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
        backdrop: item.backdropUrl ? { url: item.backdropUrl, type: 'backdrop' as const, width: 0, height: 0 } : undefined,
        uniqueKey: `cw:${item.seriesId}`,
        theatricalRelease: cachedReleases?.theatricalRelease,
        homeRelease: cachedReleases?.homeRelease,
      };
    });
  }, [continueWatchingItems, movieReleases]);

  // Map trending items to titles
  const trendingMovieTitles = useMemo((): WatchlistTitle[] => {
    if (!trendingMovies) return [];
    return trendingMovies.map((item) => {
      const cachedReleases = movieReleases.get(item.title.id);
      return {
        ...item.title,
        uniqueKey: `tm:${item.title.id}`,
        theatricalRelease: item.title.theatricalRelease ?? cachedReleases?.theatricalRelease,
        homeRelease: item.title.homeRelease ?? cachedReleases?.homeRelease,
      };
    });
  }, [trendingMovies, movieReleases]);

  const trendingTVTitles = useMemo((): WatchlistTitle[] => {
    if (!trendingTVShows) return [];
    return trendingTVShows.map((item) => ({
      ...item.title,
      uniqueKey: `ttv:${item.title.id}`,
    }));
  }, [trendingTVShows]);

  // Map custom list items to titles
  const customListTitles = useMemo((): WatchlistTitle[] => {
    return customListItems.map((item, index) => {
      const cachedReleases = item.title.mediaType === 'movie' ? movieReleases.get(item.title.id) : undefined;
      return {
        ...item.title,
        uniqueKey: `cl:${item.title.id}-${index}`,
        theatricalRelease: item.title.theatricalRelease ?? cachedReleases?.theatricalRelease,
        homeRelease: item.title.homeRelease ?? cachedReleases?.homeRelease,
      };
    });
  }, [customListItems, movieReleases]);

  // Select the appropriate titles based on mode
  const allTitles = useMemo((): WatchlistTitle[] => {
    if (!isExploreMode) return watchlistTitles;
    if (shelfId === 'continue-watching') return continueWatchingTitles;
    if (shelfId === 'trending-movies') return trendingMovieTitles;
    if (shelfId === 'trending-tv' || shelfId === 'trending-shows') return trendingTVTitles;
    if (isCustomList) return customListTitles;
    return [];
  }, [
    isExploreMode,
    shelfId,
    watchlistTitles,
    continueWatchingTitles,
    trendingMovieTitles,
    trendingTVTitles,
    isCustomList,
    customListTitles,
  ]);

  // Page title based on mode
  const pageTitle = useMemo(() => {
    if (!isExploreMode) return 'Your Watchlist';
    if (shelfConfig?.name) return shelfConfig.name;
    if (shelfId === 'continue-watching') return 'Continue Watching';
    if (shelfId === 'trending-movies') return 'Trending Movies';
    if (shelfId === 'trending-tv' || shelfId === 'trending-shows') return 'Trending TV Shows';
    return 'Explore';
  }, [isExploreMode, shelfConfig?.name, shelfId]);

  // Tab title - show "Explore" when in explore mode, otherwise "Watchlist"
  const tabTitle = isExploreMode ? 'Explore' : 'Watchlist';

  // Update the tab/navigation title dynamically
  const navigation = useNavigation();
  useLayoutEffect(() => {
    navigation.setOptions({ title: tabTitle });
  }, [navigation, tabTitle]);

  const [filter, setFilter] = useState<'all' | 'movie' | 'series'>('all');
  const [focusedFilterIndex, setFocusedFilterIndex] = useState<number | null>(null);
  const navigator = useSpatialNavigator();

  const filteredTitles = useMemo(() => {
    if (filter === 'all') {
      return allTitles;
    }
    return allTitles.filter((title) => title.mediaType === filter);
  }, [filter, allTitles]);

  const filterOptions: Array<{ key: 'all' | 'movie' | 'series'; label: string; icon: keyof typeof Ionicons.glyphMap }> =
    [
      { key: 'all', label: 'All', icon: 'grid-outline' },
      { key: 'movie', label: 'Movies', icon: 'film-outline' },
      { key: 'series', label: 'TV Shows', icon: 'tv-outline' },
    ];

  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      // Enable horizontal step within the filter row when no movement occurred
      if ((movement === 'right' || movement === 'left') && focusedFilterIndex !== null) {
        const delta = movement === 'right' ? 1 : -1;
        const nextIndex = focusedFilterIndex + delta;
        if (nextIndex >= 0 && nextIndex < filterOptions.length) {
          navigator.grabFocus(`watchlist-filter-${filterOptions[nextIndex].key}`);
          return;
        }
      }

      if (movement === 'left') {
        openMenu();
      }
    },
    [filterOptions, focusedFilterIndex, navigator, openMenu],
  );

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

  const filterLabel = filter === 'movie' ? 'Movies' : filter === 'series' ? 'TV Shows' : 'All';

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

  // Android TV: Use fully native focus (no SpatialNavigationRoot)
  if (isAndroidTV) {
    return (
      <>
        <Stack.Screen options={{ headerShown: false }} />
        <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
          <View style={styles.container}>
            <View style={styles.controlsRow}>
              <View style={styles.filtersRow}>
                {filterOptions.map((option, index) => (
                  <NativeFilterButton
                    key={option.key}
                    label={option.label}
                    icon={option.icon}
                    isActive={filter === option.key}
                    onPress={() => setFilter(option.key)}
                    autoFocus={index === 0}
                    theme={theme}
                  />
                ))}
              </View>
            </View>

            <MediaGrid
              title={`${pageTitle} · ${filterLabel}`}
              items={filteredTitles}
              loading={loading}
              error={error}
              onItemPress={handleTitlePress}
              layout="grid"
              numColumns={6}
              defaultFocusFirstItem={false}
              badgeVisibility={userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility}
              emptyMessage={emptyMessage}
              useNativeFocus={true}
              useMinimalCards={true}
            />
          </View>
        </FixedSafeAreaView>
      </>
    );
  }

  // tvOS and other platforms: Use SpatialNavigation
  return (
    <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          {/* Arrange filters and grid vertically for predictable TV navigation */}
          <SpatialNavigationNode orientation="vertical">
            <View style={styles.controlsRow}>
              {/* Make filters a vertical list on TV for Up/Down navigation */}
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.filtersRow}>
                  {filterOptions.map((option, index) => {
                    const isActiveFilter = filter === option.key;
                    const isFirst = index === 0;
                    return isFirst ? (
                      <DefaultFocus key={option.key}>
                        <FocusablePressable
                          focusKey={`watchlist-filter-${option.key}`}
                          text={option.label}
                          icon={option.icon}
                          onFocus={() => setFocusedFilterIndex(index)}
                          onSelect={() => setFilter(option.key)}
                          style={[styles.filterButton, isActiveFilter && styles.filterButtonActive]}
                        />
                      </DefaultFocus>
                    ) : (
                      <FocusablePressable
                        key={option.key}
                        focusKey={`watchlist-filter-${option.key}`}
                        text={option.label}
                        icon={option.icon}
                        onFocus={() => setFocusedFilterIndex(index)}
                        onSelect={() => setFilter(option.key)}
                        style={[styles.filterButton, isActiveFilter && styles.filterButtonActive]}
                      />
                    );
                  })}
                </View>
              </SpatialNavigationNode>
            </View>

            <MediaGrid
              title={`${pageTitle} · ${filterLabel}`}
              items={filteredTitles}
              loading={loading}
              error={error}
              onItemPress={handleTitlePress}
              layout="grid"
              numColumns={6}
              defaultFocusFirstItem={!theme.breakpoint || theme.breakpoint !== 'compact'}
              badgeVisibility={userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility}
              emptyMessage={emptyMessage}
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
    controlsRow: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      flexWrap: 'wrap',
      marginBottom: theme.spacing.sm,
    },
    filtersRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm,
      marginBottom: theme.spacing.sm,
    },
    filterButton: {
      paddingHorizontal: Platform.isTV ? theme.spacing['2xl'] : theme.spacing.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    filterButtonActive: {
      borderColor: theme.colors.accent.primary,
    },
  });
