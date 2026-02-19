import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useContinueWatching } from '@/components/ContinueWatchingContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import MediaGrid, { type MediaGridHandle } from '@/components/MediaGrid';
import { useMenuContext } from '@/components/MenuContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useWatchlist } from '@/components/WatchlistContext';
import { useWatchStatus } from '@/components/WatchStatusContext';
import { apiService, type Title, type TrendingItem, type PersonDetails, type WatchStatusItem, type SeriesWatchState } from '@/services/api';
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
import { Image, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
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

export default function WatchlistScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const router = useRouter();
  const { activeUserId, pendingPinUserId } = useUserProfiles();
  const { settings, userSettings } = useBackendSettings();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const isFocused = useIsFocused();
  const isActive = isFocused && !isMenuOpen && !pendingPinUserId;

  // Ref for MediaGrid to control scrolling from header
  const mediaGridRef = useRef<MediaGridHandle>(null);

  // Handle left navigation at edge to open menu
  const onDirectionHandledWithoutMovement = useCallback(
    (direction: Direction) => {
      if (direction === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

  // Get shelf, collection, and person parameters - if present, we're exploring a non-watchlist shelf
  const { shelf: shelfId, collection: collectionId, collectionName, person: personId, personName } = useLocalSearchParams<{
    shelf?: string;
    collection?: string;
    collectionName?: string;
    person?: string;
    personName?: string;
  }>();
  const isExploreMode = !!shelfId || !!collectionId || !!personId;
  const isCollectionMode = !!collectionId;
  const isPersonMode = !!personId;

  // Get shelf configuration for custom lists
  const shelfConfig = useMemo(() => {
    if (!shelfId) return null;
    const allShelves = userSettings?.homeShelves?.shelves ?? settings?.homeShelves?.shelves ?? [];
    return allShelves.find((s) => s.id === shelfId) ?? null;
  }, [userSettings?.homeShelves?.shelves, settings?.homeShelves?.shelves, shelfId]);

  // Watchlist data
  const { items, loading: watchlistLoading, error: watchlistError, refresh: refreshWatchlist } = useWatchlist();

  // Continue watching data
  const { items: continueWatchingItems, loading: continueWatchingLoading, refresh: refreshContinueWatching } = useContinueWatching();

  // The startup bundle caps watchlist/continue-watching to 20 items.
  // When this page needs the full list, silently refresh the context.
  useEffect(() => {
    if (shelfId === 'continue-watching') {
      void refreshContinueWatching({ silent: true });
    } else if (!isExploreMode) {
      void refreshWatchlist({ silent: true });
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- only on mount
  }, []);

  // Watch status data for watchState badge
  const { isWatched, items: watchStatusItems } = useWatchStatus();

  // Badge visibility settings
  const badgeVisibility = useMemo(
    () => userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility ?? [],
    [userSettings?.display?.badgeVisibility, settings?.display?.badgeVisibility],
  );

  // Only enrich titles with watch status when the badge is enabled
  const shouldEnrichWatchStatus = useMemo(() => badgeVisibility.includes('watchState'), [badgeVisibility]);

  // Memoize watch state icon style to prevent prop identity changes on each render
  const watchStateIconStyle = useMemo(
    () => userSettings?.display?.watchStateIconStyle ?? settings?.display?.watchStateIconStyle ?? 'colored',
    [userSettings?.display?.watchStateIconStyle, settings?.display?.watchStateIconStyle],
  );

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

  // Person mode state
  const [personDetails, setPersonDetails] = useState<PersonDetails | null>(null);
  const [personLoading, setPersonLoading] = useState(false);
  const [personError, setPersonError] = useState<string | null>(null);
  const [bioModalVisible, setBioModalVisible] = useState(false);
  const [filmographySort, setFilmographySort] = useState<'popular' | 'chronological'>('popular');

  // Fetch person data when in person mode
  useEffect(() => {
    if (!isPersonMode || !personId) {
      setPersonDetails(null);
      return;
    }

    const fetchPerson = async () => {
      setPersonLoading(true);
      setPersonError(null);
      try {
        const personIdNum = parseInt(personId, 10);
        if (isNaN(personIdNum)) {
          throw new Error('Invalid person ID');
        }
        const details = await apiService.getPersonDetails(personIdNum);
        setPersonDetails(details);
      } catch (err) {
        setPersonError(err instanceof Error ? err.message : 'Failed to load person details');
      } finally {
        setPersonLoading(false);
      }
    };

    void fetchPerson();
  }, [isPersonMode, personId]);

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

  // Universal hideWatched setting from display settings
  const hideWatched = settings?.display?.hideWatched ?? false;

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
            hideWatched,
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
            hideWatched,
          );
          if ('items' in response) {
            items = response.items;
            total = response.total;
          }
        } else if (isCustomList && shelfConfig?.listUrl) {
          const response = await apiService.getCustomList(
            shelfConfig.listUrl,
            activeUserId ?? undefined,
            limit,
            offset,
            shelfConfig?.hideUnreleased,
            hideWatched,
            shelfConfig?.name,
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
      hideWatched,
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
    if (isPersonMode) return personLoading;
    if (isCollectionMode) return collectionLoading;
    if (shelfId === 'continue-watching') return continueWatchingLoading;
    if (needsProgressiveLoading) return exploreLoading;
    return false;
  }, [isExploreMode, isPersonMode, isCollectionMode, shelfId, watchlistLoading, personLoading, collectionLoading, continueWatchingLoading, needsProgressiveLoading, exploreLoading]);

  // Determine current error state
  const error = useMemo(() => {
    if (!isExploreMode) return watchlistError;
    if (isPersonMode) return personError;
    if (isCollectionMode) return collectionError;
    if (needsProgressiveLoading) return exploreError;
    return null;
  }, [isExploreMode, isPersonMode, isCollectionMode, watchlistError, personError, collectionError, needsProgressiveLoading, exploreError]);

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
    return shouldEnrichWatchStatus
      ? enrichWithWatchStatus(titlesWithReleases, isWatched, watchStatusItems, continueWatchingItems)
      : titlesWithReleases;
  }, [items, watchlistYears, movieReleases, shouldEnrichWatchStatus, isWatched, watchStatusItems, continueWatchingItems]);

  // Map continue watching items to titles
  const continueWatchingTitles = useMemo((): WatchlistTitle[] => {
    if (!continueWatchingItems) return [];
    const baseTitles = continueWatchingItems.map((item) => {
      // Determine media type: if there's a nextEpisode, it's a series; otherwise it's a movie
      const isMovieType = !item.nextEpisode;
      const cachedReleases = isMovieType ? movieReleases.get(item.seriesId) : undefined;
      // Calculate display percent for watch progress
      const displayPercent = Math.max(0, Math.round(item.resumePercent ?? item.percentWatched ?? 0));
      return {
        id: item.seriesId,
        name: item.seriesTitle,
        overview: item.overview ?? '',
        year: item.year ?? 0,
        language: 'en',
        mediaType: isMovieType ? 'movie' : 'series',
        poster: item.posterUrl ? { url: item.posterUrl, type: 'poster' as const, width: 0, height: 0 } : undefined,
        backdrop: item.backdropUrl
          ? { url: item.backdropUrl, type: 'backdrop' as const, width: 0, height: 0 }
          : undefined,
        uniqueKey: `cw:${item.seriesId}`,
        theatricalRelease: cachedReleases?.theatricalRelease,
        homeRelease: cachedReleases?.homeRelease,
        percentWatched: displayPercent,
      };
    });
    // Enrich with watch status if badge is enabled
    return shouldEnrichWatchStatus
      ? enrichWithWatchStatus(baseTitles, isWatched, watchStatusItems, continueWatchingItems)
      : baseTitles;
  }, [continueWatchingItems, movieReleases, shouldEnrichWatchStatus, isWatched, watchStatusItems]);

  // Map explore items (trending movies, trending TV, custom lists) to titles
  const exploreTitles = useMemo((): WatchlistTitle[] => {
    if (!needsProgressiveLoading || exploreItems.length === 0) return [];

    // Determine prefix based on shelf type
    const prefix = isTrendingMovies ? 'tm' : isTrendingTV ? 'ttv' : 'cl';

    const baseTitles = exploreItems.map((item, index) => {
      const cachedReleases = item.title.mediaType === 'movie' ? movieReleases.get(item.title.id) : undefined;
      return {
        ...item.title,
        uniqueKey: `${prefix}:${item.title.id}-${index}`,
        theatricalRelease: item.title.theatricalRelease ?? cachedReleases?.theatricalRelease,
        homeRelease: item.title.homeRelease ?? cachedReleases?.homeRelease,
      };
    });
    // Enrich with watch status if badge is enabled
    return shouldEnrichWatchStatus
      ? enrichWithWatchStatus(baseTitles, isWatched, watchStatusItems, continueWatchingItems)
      : baseTitles;
  }, [needsProgressiveLoading, exploreItems, isTrendingMovies, isTrendingTV, movieReleases, shouldEnrichWatchStatus, isWatched, watchStatusItems, continueWatchingItems]);

  // Map collection items to titles
  const collectionTitles = useMemo((): WatchlistTitle[] => {
    if (!isCollectionMode || collectionItems.length === 0) return [];

    const baseTitles = collectionItems.map((item, index) => {
      const cachedReleases = movieReleases.get(item.id);
      return {
        ...item,
        uniqueKey: `col:${item.id}-${index}`,
        theatricalRelease: item.theatricalRelease ?? cachedReleases?.theatricalRelease,
        homeRelease: item.homeRelease ?? cachedReleases?.homeRelease,
      };
    });
    // Enrich with watch status if badge is enabled
    return shouldEnrichWatchStatus
      ? enrichWithWatchStatus(baseTitles, isWatched, watchStatusItems, continueWatchingItems)
      : baseTitles;
  }, [isCollectionMode, collectionItems, movieReleases, shouldEnrichWatchStatus, isWatched, watchStatusItems, continueWatchingItems]);

  // Map person filmography to titles, sorted based on user preference
  const personTitles = useMemo((): WatchlistTitle[] => {
    if (!isPersonMode || !personDetails?.filmography?.length) return [];

    let sorted = [...personDetails.filmography];

    if (filmographySort === 'chronological') {
      // Sort by year ascending (oldest first), items without year go to the end
      sorted.sort((a, b) => {
        if (!a.year && !b.year) return 0;
        if (!a.year) return 1;
        if (!b.year) return -1;
        return a.year - b.year;
      });
    }
    // 'popular' keeps the original order from TMDB (already sorted by popularity)

    const baseTitles = sorted.map((item, index) => {
      const cachedReleases = item.mediaType === 'movie' ? movieReleases.get(item.id) : undefined;
      return {
        ...item,
        uniqueKey: `person:${item.id}-${index}`,
        theatricalRelease: item.theatricalRelease ?? cachedReleases?.theatricalRelease,
        homeRelease: item.homeRelease ?? cachedReleases?.homeRelease,
      };
    });
    // Enrich with watch status if badge is enabled
    return shouldEnrichWatchStatus
      ? enrichWithWatchStatus(baseTitles, isWatched, watchStatusItems, continueWatchingItems)
      : baseTitles;
  }, [isPersonMode, personDetails, movieReleases, filmographySort, shouldEnrichWatchStatus, isWatched, watchStatusItems, continueWatchingItems]);

  // Select the appropriate titles based on mode
  const allTitles = useMemo((): WatchlistTitle[] => {
    if (!isExploreMode) {
      // Personal watchlist - apply hideWatched filter
      if (hideWatched) {
        return watchlistTitles.filter((title) => !isWatched(title.mediaType, title.id));
      }
      return watchlistTitles;
    }
    if (isPersonMode) return personTitles; // Don't filter bio mode
    if (isCollectionMode) return collectionTitles;
    if (shelfId === 'continue-watching') return continueWatchingTitles;
    if (needsProgressiveLoading) return exploreTitles;
    return [];
  }, [isExploreMode, isPersonMode, isCollectionMode, shelfId, watchlistTitles, personTitles, collectionTitles, continueWatchingTitles, needsProgressiveLoading, exploreTitles, hideWatched, isWatched]);

  // Page title based on mode
  const pageTitle = useMemo(() => {
    if (!isExploreMode) return 'Your Watchlist';
    if (isPersonMode) {
      // Use person details name if loaded, otherwise decode URL param
      return personDetails?.person.name ?? (personName ? decodeURIComponent(personName) : 'Actor');
    }
    if (isCollectionMode && collectionName) return decodeURIComponent(collectionName);
    if (shelfConfig?.name) return shelfConfig.name;
    if (shelfId === 'continue-watching') return 'Continue Watching';
    if (shelfId === 'trending-movies') return 'Trending Movies';
    if (shelfId === 'trending-tv' || shelfId === 'trending-shows') return 'Trending TV Shows';
    return 'Explore';
  }, [isExploreMode, isPersonMode, personDetails?.person.name, personName, isCollectionMode, collectionName, shelfConfig?.name, shelfId]);

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

  // Person header component for ListHeaderComponent (scrolls with grid)
  const personHeaderComponent = useMemo(() => {
    if (!isPersonMode || !personDetails) return null;

    // TV sort button icon size - scaled for platform (1.5x tvOS, ~1.05x Android TV)
    const isNonTvosTV = Platform.isTV && Platform.OS !== 'ios';
    const atvScale = isNonTvosTV ? 0.7 : 1;
    const sortIconSize = Platform.isTV ? Math.round(27 * atvScale) : 18;

    // Bio content - wrap in SpatialNavigationFocusableView on TV for D-pad navigation
    // Android TV gets more lines since line height is smaller (and user wants it taller)
    const bioNumberOfLines = Platform.isTV ? (isNonTvosTV ? 10 : 5) : 5;
    const bioContent = personDetails.person.biography ? (
      Platform.isTV ? (
        <SpatialNavigationFocusableView
          onSelect={() => setBioModalVisible(true)}
          onFocus={() => mediaGridRef.current?.scrollToTop()}>
          {({ isFocused }: { isFocused: boolean }) => (
            <View style={[styles.bioPressable, isFocused && styles.bioPressableFocused]}>
              <Text style={styles.personBioTop} numberOfLines={bioNumberOfLines}>
                {personDetails.person.biography}
              </Text>
              <Text style={styles.bioReadMore}>Select to read more</Text>
            </View>
          )}
        </SpatialNavigationFocusableView>
      ) : (
        <Pressable onPress={() => setBioModalVisible(true)}>
          <Text style={styles.personBioTop} numberOfLines={bioNumberOfLines}>
            {personDetails.person.biography}
          </Text>
          <Text style={styles.bioReadMore}>Tap to read more</Text>
        </Pressable>
      )
    ) : null;

    // Sort button - TV version with spatial navigation
    const renderSortButton = (
      sortType: 'popular' | 'chronological',
      icon: 'flame' | 'calendar',
      label: string,
    ) => {
      const isActive = filmographySort === sortType;
      if (Platform.isTV) {
        return (
          <SpatialNavigationFocusableView
            onSelect={() => setFilmographySort(sortType)}
            onFocus={() => mediaGridRef.current?.scrollToTop()}>
            {({ isFocused }: { isFocused: boolean }) => (
              <View
                style={[
                  styles.sortButton,
                  isActive && styles.sortButtonActive,
                  isFocused && styles.sortButtonFocused,
                ]}>
                <Ionicons
                  name={icon}
                  size={sortIconSize}
                  color={isFocused ? theme.colors.text.inverse : isActive ? theme.colors.accent.primary : theme.colors.text.muted}
                />
                <Text
                  style={[
                    styles.sortButtonText,
                    isActive && styles.sortButtonTextActive,
                    isFocused && styles.sortButtonTextFocused,
                  ]}>
                  {label}
                </Text>
              </View>
            )}
          </SpatialNavigationFocusableView>
        );
      }
      return (
        <Pressable
          style={[styles.sortButton, isActive && styles.sortButtonActive]}
          onPress={() => setFilmographySort(sortType)}>
          <Ionicons
            name={icon}
            size={18}
            color={isActive ? theme.colors.accent.primary : theme.colors.text.muted}
          />
          <Text style={[styles.sortButtonText, isActive && styles.sortButtonTextActive]}>
            {label}
          </Text>
        </Pressable>
      );
    };

    // Bio row content
    const bioRow = (
      <View style={styles.personTopRow}>
        {personDetails.person.profileUrl && (
          <Image
            source={{ uri: personDetails.person.profileUrl }}
            style={styles.personPhoto}
            resizeMode="cover"
          />
        )}
        <View style={styles.personBioWrap}>
          {personDetails.person.knownFor && (
            <Text style={styles.personRole}>{personDetails.person.knownFor}</Text>
          )}
          {bioContent}
        </View>
      </View>
    );

    // Sort buttons row content
    const sortRow = (
      <View style={styles.sortToggleRow}>
        <Text style={styles.sortLabel}>Sort by:</Text>
        <View style={styles.sortButtons}>
          {renderSortButton('popular', 'flame', 'Popular')}
          {renderSortButton('chronological', 'calendar', 'Year')}
        </View>
      </View>
    );

    // On TV, wrap each row in horizontal navigation node for proper left/right navigation.
    // Don't wrap in outer vertical node - let the horizontal nodes be direct children of
    // MediaGrid's vertical node so navigation UP from grid goes to sort buttons (not bio).
    return Platform.isTV ? (
      <View style={styles.personHeader}>
        <SpatialNavigationNode orientation="horizontal">
          {bioRow}
        </SpatialNavigationNode>
        <SpatialNavigationNode orientation="horizontal">
          {sortRow}
        </SpatialNavigationNode>
      </View>
    ) : (
      <View style={styles.personHeader}>
        {bioRow}
        {sortRow}
      </View>
    );
  }, [isPersonMode, personDetails, styles, filmographySort, theme.colors]);

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
    <SpatialNavigationRoot isActive={isActive && !bioModalVisible} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          <SpatialNavigationNode orientation="vertical">
            {/* Page title */}
            <View style={styles.titleRow}>
              <Text style={styles.title}>{pageTitle}</Text>
            </View>

            {/* Filter buttons row - hidden in collection mode and person mode */}
            {!isCollectionMode && !isPersonMode && (
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

            {/* Grid content - hide title in collection/person mode since page title already shows it */}
            <MediaGrid
              ref={mediaGridRef}
              title={isCollectionMode || isPersonMode ? '' : pageTitle}
              items={filteredTitles}
              loading={loading}
              error={error}
              onItemPress={handleTitlePress}
              layout="grid"
              numColumns={numColumns}
              defaultFocusFirstItem={false}
              badgeVisibility={userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility}
              watchStateIconStyle={watchStateIconStyle}
              emptyMessage={emptyMessage}
              onEndReached={handleLoadMore}
              loadingMore={exploreLoadingMore}
              hasMoreItems={hasMoreItems}
              ListHeaderComponent={personHeaderComponent}
              listKey={isPersonMode ? filmographySort : undefined}
            />
          </SpatialNavigationNode>
        </View>
      </FixedSafeAreaView>

      {/* Biography Modal - scrollable content with close button */}
      {bioModalVisible && (
        <Modal
          visible={bioModalVisible}
          transparent
          animationType="fade"
          onRequestClose={() => setBioModalVisible(false)}>
          <SpatialNavigationRoot isActive={bioModalVisible}>
            <View style={styles.modalOverlay}>
              <View style={styles.modalContent}>
                {Platform.isTV ? (
                  <SpatialNavigationFocusableView onSelect={() => setBioModalVisible(false)}>
                    {({ isFocused }: { isFocused: boolean }) => {
                      // Reduce close button size on Android TV
                      const isAndroidTV = Platform.OS === 'android';
                      const closeIconSize = isAndroidTV ? 24 : 48;
                      return (
                        <View style={[styles.modalCloseButton, isFocused && styles.modalCloseButtonFocused]}>
                          <Ionicons
                            name="close-circle"
                            size={closeIconSize}
                            color={isFocused ? theme.colors.accent.primary : theme.colors.text.secondary}
                          />
                        </View>
                      );
                    }}
                  </SpatialNavigationFocusableView>
                ) : (
                  <Pressable style={styles.modalCloseButton} onPress={() => setBioModalVisible(false)}>
                    <Ionicons name="close-circle" size={32} color={theme.colors.text.secondary} />
                  </Pressable>
                )}
                <ScrollView
                  contentContainerStyle={styles.modalScrollContent}
                  showsVerticalScrollIndicator={true}>
                  <Text style={styles.modalBioText}>
                    {personDetails?.person.biography ?? 'No biography available.'}
                  </Text>
                </ScrollView>
              </View>
            </View>
          </SpatialNavigationRoot>
        </Modal>
      )}
    </SpatialNavigationRoot>
  );
}

const createStyles = (theme: NovaTheme) => {
  // Non-tvOS TV platforms (Android TV, Fire TV, etc.) need smaller scaling
  const isNonTvosTV = Platform.isTV && Platform.OS !== 'ios';
  // Scale factor for non-tvOS TV - reduce sizes by 30% compared to tvOS
  const atvScale = isNonTvosTV ? 0.7 : 1;
  // TV scale multiplier (1.5x for tvOS, ~1.05x for Android TV)
  const tvScale = Platform.isTV ? 1.5 * atvScale : 1;

  return StyleSheet.create({
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
    personHeader: {
      marginBottom: Platform.isTV ? theme.spacing.lg * 1.5 : theme.spacing.lg,
    },
    personTopRow: {
      flexDirection: 'row',
    },
    personPhoto: {
      width: Platform.isTV ? 225 : responsiveSize(120, 100),
      height: Platform.isTV ? 338 : responsiveSize(180, 150),
      borderRadius: Platform.isTV ? theme.radius.md * 1.5 : theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      marginRight: Platform.isTV ? theme.spacing.lg * 1.5 : theme.spacing.lg,
    },
    personBioWrap: {
      flex: 1,
      paddingTop: Platform.isTV ? theme.spacing.xs * 1.5 : theme.spacing.xs,
      // Add right padding on Android TV to prevent text going off screen edge
      paddingRight: isNonTvosTV ? theme.spacing.xl : 0,
    },
    personRole: {
      ...theme.typography.label.md,
      fontSize: Platform.isTV ? theme.typography.label.md.fontSize * 1.5 : theme.typography.label.md.fontSize,
      lineHeight: Platform.isTV ? theme.typography.label.md.lineHeight * 1.5 : theme.typography.label.md.lineHeight,
      color: theme.colors.text.secondary,
      marginBottom: Platform.isTV ? theme.spacing.sm * 1.5 : theme.spacing.sm,
    },
    bioPressable: {
      borderRadius: Platform.isTV ? theme.radius.sm * 1.5 : theme.radius.sm,
      borderWidth: Platform.isTV ? 3 : 2,
      borderColor: 'transparent',
      padding: Platform.isTV ? theme.spacing.sm : 0,
      margin: Platform.isTV ? -theme.spacing.sm : 0,
    },
    bioPressableFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.surface,
    },
    personBioTop: {
      ...theme.typography.body.sm,
      fontSize: Platform.isTV ? theme.typography.body.sm.fontSize * 1.5 : theme.typography.body.sm.fontSize,
      color: theme.colors.text.secondary,
      lineHeight: Math.round(22 * tvScale),
    },
    bioReadMore: {
      ...theme.typography.label.md,
      fontSize: Platform.isTV ? theme.typography.label.md.fontSize * 1.5 : theme.typography.label.md.fontSize,
      color: theme.colors.accent.primary,
      marginTop: Platform.isTV ? theme.spacing.sm : theme.spacing.xs,
    },
    sortToggleRow: {
      flexDirection: 'row',
      alignItems: 'center',
      marginTop: Platform.isTV ? theme.spacing.lg * tvScale : theme.spacing.lg,
      paddingTop: Platform.isTV ? theme.spacing.md * tvScale : theme.spacing.md,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
    },
    sortLabel: {
      ...theme.typography.label.md,
      fontSize: theme.typography.label.md.fontSize * tvScale,
      lineHeight: theme.typography.label.md.lineHeight * tvScale,
      color: theme.colors.text.muted,
      marginRight: Platform.isTV ? theme.spacing.md * tvScale : theme.spacing.md,
    },
    sortButtons: {
      flexDirection: 'row',
      gap: Platform.isTV ? theme.spacing.md * atvScale : theme.spacing.sm,
    },
    sortButton: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: Platform.isTV ? theme.spacing.sm * atvScale : theme.spacing.xs,
      paddingVertical: Platform.isTV ? theme.spacing.md * atvScale : theme.spacing.xs,
      paddingHorizontal: Platform.isTV ? theme.spacing.lg * atvScale : theme.spacing.sm,
      borderRadius: Platform.isTV ? theme.radius.md * atvScale : theme.radius.sm,
      backgroundColor: theme.colors.background.surface,
      borderWidth: Platform.isTV ? Math.round(3 * atvScale) : 2,
      borderColor: 'transparent',
    },
    sortButtonActive: {
      backgroundColor: theme.colors.accent.primary + '20',
    },
    sortButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    sortButtonText: {
      ...theme.typography.label.md,
      fontSize: theme.typography.label.md.fontSize * tvScale,
      lineHeight: theme.typography.label.md.lineHeight * tvScale,
      color: theme.colors.text.muted,
    },
    sortButtonTextActive: {
      color: theme.colors.accent.primary,
    },
    sortButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    modalOverlay: {
      flex: 1,
      backgroundColor: 'rgba(0, 0, 0, 0.9)',
      justifyContent: 'center',
      alignItems: 'center',
      padding: Platform.isTV ? theme.spacing.xl * 1.5 : theme.spacing.xl,
    },
    modalContent: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: Platform.isTV ? theme.radius.lg * 1.5 : theme.radius.lg,
      maxWidth: Platform.isTV ? 900 : 600,
      maxHeight: '85%',
      width: '100%',
    },
    modalCloseButton: {
      position: 'absolute',
      // Android TV: shift up and right (smaller values)
      top: Platform.isTV ? (isNonTvosTV ? theme.spacing.sm : theme.spacing.lg) : theme.spacing.sm,
      right: Platform.isTV ? (isNonTvosTV ? theme.spacing.sm : theme.spacing.lg) : theme.spacing.sm,
      zIndex: 10,
      padding: Platform.isTV ? (isNonTvosTV ? theme.spacing.xs : theme.spacing.md) : theme.spacing.xs,
      backgroundColor: theme.colors.background.elevated,
      borderRadius: Platform.isTV ? (isNonTvosTV ? 15 : 30) : 20,
      borderWidth: Platform.isTV ? (isNonTvosTV ? 2 : 3) : 2,
      borderColor: 'transparent',
    },
    modalCloseButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.surface,
    },
    modalScrollContent: {
      padding: Platform.isTV ? theme.spacing.xl * 1.5 : theme.spacing.xl,
      paddingTop: Platform.isTV ? theme.spacing['3xl'] * 1.5 : theme.spacing['3xl'],
      paddingBottom: Platform.isTV ? theme.spacing['2xl'] * 1.5 : theme.spacing['2xl'],
    },
    modalBioText: {
      ...theme.typography.body.md,
      fontSize: Platform.isTV ? theme.typography.body.md.fontSize * 1.5 : theme.typography.body.md.fontSize,
      color: theme.colors.text.primary,
      lineHeight: Math.round(26 * tvScale),
    },
  });
};
