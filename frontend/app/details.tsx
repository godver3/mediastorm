import type { PlaybackPreference } from '@/components/BackendSettingsContext';
import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useContinueWatching } from '@/components/ContinueWatchingContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import { useLoadingScreen } from '@/components/LoadingScreenContext';
import MobileTabBar from '@/components/MobileTabBar';
import { useToast } from '@/components/ToastContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useWatchlist } from '@/components/WatchlistContext';
import { useWatchStatus } from '@/components/WatchStatusContext';
import EpisodeCard from '@/components/EpisodeCard';
import TVEpisodeStrip from '@/components/TVEpisodeStrip';
import {
  apiService,
  type EpisodeWatchPayload,
  type NZBResult,
  type PrequeueStatusResponse,
  type Rating,
  type SeriesEpisode,
  type SeriesSeason,
  type Title,
  type Trailer,
} from '@/services/api';
import {
  DefaultFocus,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  useSpatialNavigator,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV, getTVScaleMultiplier } from '@/theme/tokens/tvScale';
import { getUnplayableReleases } from '@/hooks/useUnplayableReleases';
import { playbackNavigation } from '@/services/playback-navigation';
import { findAudioTrackByLanguage, findSubtitleTrackByPreference } from '@/app/details/track-selection';
import { Ionicons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import { Stack, useLocalSearchParams, useRouter, usePathname } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Image,
  ImageResizeMode,
  ImageStyle,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  View,
  useWindowDimensions,
} from 'react-native';
import Animated, {
  useAnimatedStyle,
  useSharedValue,
  withTiming,
  Easing,
  cancelAnimation,
} from 'react-native-reanimated';

// Import extracted modules
import { BulkWatchModal } from './details/bulk-watch-modal';
import { ManualSelection, useManualHealthChecks } from './details/manual-selection';
import {
  buildExternalPlayerTargets,
  getHealthFailureReason,
  initiatePlayback,
  isHealthFailureError,
} from './details/playback';
import { ResumePlaybackModal } from './details/resume-modal';
import { SeriesEpisodes } from './details/series-episodes';
import { TrailerModal } from './details/trailer';
import { SeasonSelector } from './details/season-selector';
import { EpisodeSelector } from './details/episode-selector';
import { buildEpisodeQuery, buildSeasonQuery, formatPublishDate, padNumber, toStringParam } from './details/utils';

const SELECTION_TOAST_ID = 'details-nzb-status';

interface EpisodeSearchContext {
  query: string;
  friendlyLabel: string;
  selectionMessage: string;
  episodeCode: string;
}

interface LocalParams extends Record<string, any> {
  title?: string;
  description?: string;
  headerImage?: string;
  titleId?: string;
  mediaType?: string;
  posterUrl?: string;
  backdropUrl?: string;
  tmdbId?: string;
  imdbId?: string;
  tvdbId?: string;
  year?: string;
  initialSeason?: string;
  initialEpisode?: string;
}

// Helper to get rating display configuration with service-specific icons
const getRatingConfig = (
  source: string,
  baseUrl: string,
  value?: number,
  max?: number,
): { label: string; color: string; iconUrl: string | null } => {
  const iconBase = `${baseUrl}/static/rating_icons`;
  switch (source) {
    case 'imdb':
      return { label: 'IMDb', color: '#F5C518', iconUrl: `${iconBase}/imdb.png` };
    case 'tmdb':
      return { label: 'TMDb', color: '#01D277', iconUrl: `${iconBase}/tmdb.png` };
    case 'trakt':
      return { label: 'Trakt', color: '#ED1C24', iconUrl: `${iconBase}/trakt.png` };
    case 'letterboxd':
      return { label: 'Letterboxd', color: '#00E054', iconUrl: `${iconBase}/letterboxd.png` };
    case 'tomatoes': {
      // RT Critics: fresh (>= 60%) vs rotten (< 60%)
      const percent = max === 100 ? value : value !== undefined ? value * 10 : 60;
      const isFresh = (percent ?? 60) >= 60;
      return {
        label: isFresh ? 'Fresh' : 'Rotten',
        color: isFresh ? '#FA320A' : '#6B8E23',
        iconUrl: `${iconBase}/${isFresh ? 'rt_critics' : 'rt_rotten'}.png`,
      };
    }
    case 'audience':
      return { label: 'RT Audience', color: '#FA320A', iconUrl: `${iconBase}/rt_audience.png` };
    case 'metacritic':
      return { label: 'Metacritic', color: '#FFCC34', iconUrl: `${iconBase}/metacritic.png` };
    default:
      return { label: source, color: '#888888', iconUrl: null };
  }
};

// Define the order for rating sources (lower = displayed first)
const RATING_ORDER: Record<string, number> = {
  imdb: 1,
  tmdb: 2,
  trakt: 3,
  tomatoes: 4, // RT Critics before Audience
  audience: 5,
  metacritic: 6,
  letterboxd: 7,
};

// Format rating value based on source and scale
const formatRating = (rating: Rating): string => {
  switch (rating.source) {
    case 'imdb':
      // IMDb: display as decimal (e.g., 7.5)
      return rating.value.toFixed(1);
    case 'letterboxd':
      // Letterboxd: display as decimal stars (e.g., 3.5)
      return rating.value.toFixed(1);
    case 'tmdb':
    case 'trakt':
      // TMDb/Trakt: already percentages
      return `${Math.round(rating.value)}%`;
    case 'tomatoes':
    case 'audience':
    case 'metacritic':
      // Already percentages
      return `${Math.round(rating.value)}%`;
    default:
      if (rating.max === 10) {
        return rating.value.toFixed(1);
      }
      return `${Math.round(rating.value)}%`;
  }
};

// Rating badge component with image fallback (no labels - icons are self-explanatory)
const RatingBadge = ({
  rating,
  config,
  iconSize,
  styles,
}: {
  rating: Rating;
  config: { label: string; color: string; iconUrl: string | null };
  iconSize: number;
  styles: ReturnType<typeof createDetailsStyles>;
}) => {
  const [imageError, setImageError] = useState(false);

  return (
    <View style={styles.ratingBadge}>
      {config.iconUrl && !imageError ? (
        <Image
          source={{ uri: config.iconUrl }}
          style={{ width: iconSize, height: iconSize }}
          resizeMode="contain"
          onError={() => {
            console.warn(`Rating icon failed to load: ${config.iconUrl}`);
            setImageError(true);
          }}
        />
      ) : (
        <Ionicons name="star" size={iconSize} color={config.color} />
      )}
      <Text style={[styles.ratingValue, { color: config.color }]}>{formatRating(rating)}</Text>
    </View>
  );
};

export default function DetailsScreen() {
  const params = useLocalSearchParams<LocalParams>();
  const router = useRouter();
  const pathname = usePathname();
  const theme = useTheme();
  const styles = useMemo(() => createDetailsStyles(theme), [theme]);
  const spatialNavigator = useSpatialNavigator();
  const spatialNavigatorRef = useRef(spatialNavigator);
  spatialNavigatorRef.current = spatialNavigator;
  const isWeb = Platform.OS === 'web';
  const isTV = Platform.isTV;
  const isMobile = !isWeb && !isTV;
  const tvScale = isTV ? getTVScaleMultiplier() : 1;
  const shouldShowDebugPlayerButton = false;
  const { height: windowHeight, width: windowWidth } = useWindowDimensions();
  const overlayGradientColors = useMemo(
    () => ['rgba(0, 0, 0, 0)', theme.colors.overlay.scrim, theme.colors.background.base] as const,
    [theme.colors.overlay.scrim, theme.colors.background.base],
  );
  // Keep the mobile gradient anchored near the content box so the fade sits lower on the hero
  const overlayGradientLocations: readonly [number, number, number] = isMobile
    ? [0, 0.7, 1]
    : isTV
      ? [0, 0.8, 1]
      : [0, 0.45, 1];
  const _isFadeMaskSupported = Platform.OS !== 'web';
  const _contentMaskColors = useMemo(() => ['transparent', '#000', '#000'], []);
  const _contentMaskLocations = useMemo(() => [0, 0.12, 1], []);
  const isCompactBreakpoint = theme.breakpoint === 'compact';
  const _isIos = Platform.OS === 'ios';
  const isIosWeb = useMemo(() => {
    if (!isWeb || typeof navigator === 'undefined') {
      return false;
    }
    const userAgent = navigator.userAgent || '';
    const isiOSDevice = /iPad|iPhone|iPod/i.test(userAgent);
    const isTouchEnabledMac = userAgent.includes('Mac') && typeof window !== 'undefined' && 'ontouchend' in window;
    return isiOSDevice || isTouchEnabledMac;
  }, [isWeb]);
  const isWebTouch = useMemo(() => {
    if (!isWeb || typeof navigator === 'undefined') {
      return false;
    }

    const hasMaxTouchPoints = Number(navigator.maxTouchPoints) > 0;
    const hasStandaloneTouch = typeof window !== 'undefined' && 'ontouchstart' in window;
    const prefersCoarsePointer =
      typeof window !== 'undefined' && typeof window.matchMedia === 'function'
        ? window.matchMedia('(pointer: coarse)').matches
        : false;

    return hasMaxTouchPoints || hasStandaloneTouch || prefersCoarsePointer;
  }, [isWeb]);
  const useCompactActionLayout = isCompactBreakpoint && (isWeb || isMobile);
  const isTouchSeasonLayout = isMobile || isWebTouch;
  const shouldUseSeasonModal = isTouchSeasonLayout && isMobile;
  const shouldAutoPlaySeasonSelection = !isTouchSeasonLayout;
  const shouldUseAdaptiveHeroSizing = isMobile || (isWeb && isWebTouch);
  const isPortraitOrientation = windowHeight >= windowWidth;
  const shouldAnchorHeroToTop = shouldUseAdaptiveHeroSizing && isPortraitOrientation;
  const contentBoxStyle = useMemo(() => {
    if (Platform.isTV) {
      return { height: Math.round(windowHeight * 0.5) };
    }
    return { flex: 1 };
  }, [Platform.isTV, windowHeight]);
  const [headerImageDimensions, setHeaderImageDimensions] = useState<{ width: number; height: number } | null>(null);
  // On tvOS, measure the header image so we can avoid over-zooming portrait artwork
  const shouldMeasureHeaderImage = shouldUseAdaptiveHeroSizing || Platform.isTV;

  const title = toStringParam(params.title);
  const description = toStringParam(params.description);
  const headerImageParam = toStringParam(params.headerImage);
  const titleId = toStringParam(params.titleId);
  const rawMediaType = toStringParam(params.mediaType);
  const mediaType = (rawMediaType || 'movie').toLowerCase();
  const isSeries = mediaType === 'series' || mediaType === 'tv' || mediaType === 'show';
  const posterUrlParam = toStringParam(params.posterUrl) || headerImageParam;
  const backdropUrlParam = toStringParam(params.backdropUrl) || headerImageParam;

  // State to hold fetched details for backdrop updates
  const [seriesDetailsForBackdrop, setSeriesDetailsForBackdrop] = useState<Title | null>(null);
  const [movieDetails, setMovieDetails] = useState<Title | null>(null);

  const tmdbId = toStringParam(params.tmdbId);
  const imdbId = toStringParam(params.imdbId);
  const tvdbId = toStringParam(params.tvdbId);
  const yearParam = toStringParam(params.year);
  const initialSeasonParam = toStringParam(params.initialSeason);
  const initialEpisodeParam = toStringParam(params.initialEpisode);

  // Compute final poster and backdrop URLs, preferring fetched metadata over params
  const posterUrl = useMemo(() => {
    // For movies, use fetched details if available
    if (!isSeries && movieDetails?.poster?.url) {
      console.log('[Details] Using fetched movie poster:', movieDetails.poster.url);
      return movieDetails.poster.url;
    }
    // For series, use fetched details if available
    if (isSeries && seriesDetailsForBackdrop?.poster?.url) {
      console.log('[Details] Using fetched series poster:', seriesDetailsForBackdrop.poster.url);
      return seriesDetailsForBackdrop.poster.url;
    }
    // Fall back to params
    console.log('[Details] Using poster param:', posterUrlParam);
    return posterUrlParam;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop, posterUrlParam]);

  const backdropUrl = useMemo(() => {
    // For movies, use fetched details if available
    if (!isSeries && movieDetails?.backdrop?.url) {
      console.log('[Details] Using fetched movie backdrop:', movieDetails.backdrop.url);
      return movieDetails.backdrop.url;
    }
    // For series, use fetched details if available
    if (isSeries && seriesDetailsForBackdrop?.backdrop?.url) {
      console.log('[Details] Using fetched series backdrop:', seriesDetailsForBackdrop.backdrop.url);
      return seriesDetailsForBackdrop.backdrop.url;
    }
    // Fall back to params
    console.log('[Details] Using backdrop param:', backdropUrlParam);
    return backdropUrlParam;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop, backdropUrlParam]);

  // On mobile, prefer portrait poster for background; on desktop/TV, prefer landscape backdrop
  const headerImage = useMemo(() => {
    const result = shouldUseAdaptiveHeroSizing ? posterUrl || backdropUrl : backdropUrl || posterUrl;
    console.log('[Details] headerImage computed:', {
      shouldUseAdaptiveHeroSizing,
      posterUrl,
      backdropUrl,
      result,
      isTV: Platform.isTV,
    });
    return result;
  }, [shouldUseAdaptiveHeroSizing, posterUrl, backdropUrl]);

  const seriesIdentifier = useMemo(() => {
    const trimmedTitle = title.trim();
    if (titleId) {
      // Strip episode information (e.g., :S01E02) from titleId to get the series ID
      // This prevents infinite loops when navigating from Continue Watching
      return titleId.replace(/:S\d{2}E\d{2}$/i, '');
    }
    if (tvdbId) {
      return `tvdb:${tvdbId}`;
    }
    if (tmdbId) {
      return `tmdb:${tmdbId}`;
    }
    if (trimmedTitle) {
      return `title:${trimmedTitle}`;
    }
    return '';
  }, [title, titleId, tmdbId, tvdbId]);

  const yearNumber = useMemo(() => {
    const parsed = Number(yearParam);
    return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : undefined;
  }, [yearParam]);

  const tmdbIdNumber = useMemo(() => {
    const parsed = Number(tmdbId);
    return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : undefined;
  }, [tmdbId]);

  const tvdbIdNumber = useMemo(() => {
    const parsed = Number(tvdbId);
    return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : undefined;
  }, [tvdbId]);

  const movieDetailsQuery = useMemo(() => {
    if (isSeries) {
      return null;
    }
    const trimmedTitleId = titleId?.trim();
    const trimmedTitleName = title?.trim();
    const trimmedImdbId = imdbId?.trim();
    const query: {
      tmdbId?: number;
      tvdbId?: number;
      titleId?: string;
      name?: string;
      year?: number;
      imdbId?: string;
    } = {};

    if (tmdbIdNumber) {
      query.tmdbId = tmdbIdNumber;
    }
    if (tvdbIdNumber) {
      query.tvdbId = tvdbIdNumber;
    }
    if (trimmedTitleId) {
      query.titleId = trimmedTitleId;
    }
    if (trimmedTitleName) {
      query.name = trimmedTitleName;
    }
    if (typeof yearNumber === 'number') {
      query.year = yearNumber;
    }
    if (trimmedImdbId) {
      query.imdbId = trimmedImdbId;
    }

    if (Object.keys(query).length === 0) {
      return null;
    }

    return query;
  }, [imdbId, isSeries, title, titleId, tmdbIdNumber, tvdbIdNumber, yearNumber]);

  useEffect(() => {
    let cancelled = false;

    if (!headerImage || !shouldMeasureHeaderImage) {
      setHeaderImageDimensions(null);
      return () => {
        cancelled = true;
      };
    }

    Image.getSize(
      headerImage,
      (width, height) => {
        if (cancelled) {
          return;
        }

        if (!width || !height) {
          setHeaderImageDimensions(null);
          return;
        }

        setHeaderImageDimensions({ width, height });
      },
      (error) => {
        if (cancelled) {
          return;
        }

        console.warn('[Details] Unable to measure header image size', error);
        setHeaderImageDimensions(null);
      },
    );

    return () => {
      cancelled = true;
    };
  }, [headerImage, shouldMeasureHeaderImage]);

  const backgroundImageSizingStyle = useMemo<ImageStyle>(() => {
    if (!shouldUseAdaptiveHeroSizing || !headerImageDimensions) {
      return styles.backgroundImageFill;
    }

    const { width, height } = headerImageDimensions;
    if (width <= 0 || height <= 0) {
      return styles.backgroundImageFill;
    }

    const viewportWidth = windowWidth;
    const viewportHeight = windowHeight;
    if (viewportWidth <= 0 || viewportHeight <= 0) {
      return styles.backgroundImageFill;
    }

    const aspectRatio = width / height;
    if (!Number.isFinite(aspectRatio) || aspectRatio <= 0) {
      return styles.backgroundImageFill;
    }

    const isPortraitArtwork = height >= width;

    if (isPortraitArtwork) {
      const desiredHeight = viewportHeight;
      const computedWidth = desiredHeight * aspectRatio;
      if (computedWidth <= viewportWidth) {
        return {
          height: desiredHeight,
          width: computedWidth,
        };
      }

      const scaledHeight = viewportWidth / aspectRatio;
      return {
        width: viewportWidth,
        height: scaledHeight,
      };
    }

    const desiredWidth = viewportWidth;
    const computedHeight = desiredWidth / aspectRatio;
    if (computedHeight <= viewportHeight) {
      return {
        width: desiredWidth,
        height: computedHeight,
      };
    }

    const scaledWidth = viewportHeight * aspectRatio;
    return {
      width: scaledWidth,
      height: viewportHeight,
    };
  }, [headerImageDimensions, shouldUseAdaptiveHeroSizing, styles.backgroundImageFill, windowHeight, windowWidth]);

  const isPortraitArtwork = useMemo(() => {
    if (!headerImageDimensions) return null;
    const { width, height } = headerImageDimensions;
    if (!width || !height) return null;
    return height >= width;
  }, [headerImageDimensions]);

  const backgroundImageResizeMode = useMemo<ImageResizeMode>(() => {
    // On tvOS, avoid zooming portrait posters in the hero by using 'contain'
    if (Platform.isTV && isPortraitArtwork === true) {
      return 'contain';
    }
    return shouldUseAdaptiveHeroSizing ? 'contain' : 'cover';
  }, [isPortraitArtwork, shouldUseAdaptiveHeroSizing]);

  const shouldShowBlurredFill = useMemo(() => Platform.isTV && isPortraitArtwork === true, [isPortraitArtwork]);

  // State to hold next episode info from playback
  const [nextEpisodeFromPlayback, setNextEpisodeFromPlayback] = useState<{
    seasonNumber: number;
    episodeNumber: number;
    autoPlay: boolean;
  } | null>(null);
  const allEpisodesRef = useRef<SeriesEpisode[]>([]);
  const handleEpisodeSelectRef = useRef<((episode: SeriesEpisode) => void) | null>(null);
  const handlePlayEpisodeRef = useRef<((episode: SeriesEpisode) => void) | null>(null);

  // Check for next episode when screen comes into focus
  useFocusEffect(
    useCallback(() => {
      if (titleId) {
        const nextEp = playbackNavigation.consumeNextEpisode(titleId);
        if (nextEp) {
          console.log('[Details] Found next episode from playback:', nextEp, {
            hasPrequeueId: !!nextEp.prequeueId,
            hasPrequeueStatus: !!nextEp.prequeueStatus,
            prequeueStatusReady: nextEp.prequeueStatus ? apiService.isPrequeueReady(nextEp.prequeueStatus.status) : false,
          });
          setNextEpisodeFromPlayback(nextEp);
          // Restore shuffle mode from playback navigation
          setIsShuffleMode(nextEp.shuffleMode);

          // Store prequeue data from navigation if present
          if (nextEp.prequeueId) {
            setPrequeueId(nextEp.prequeueId);
            setPrequeueTargetEpisode({
              seasonNumber: nextEp.seasonNumber,
              episodeNumber: nextEp.episodeNumber,
            });
            // Cache the ready status so resolveAndPlay can use it directly
            if (nextEp.prequeueStatus && apiService.isPrequeueReady(nextEp.prequeueStatus.status)) {
              navigationPrequeueStatusRef.current = nextEp.prequeueStatus;
              setPrequeueReady(true);
              console.log('[Details] Cached navigation prequeue status (ready)');
            }
          }

          // Try to select/play the episode immediately if we have the episodes loaded
          if (allEpisodesRef.current.length > 0) {
            const matchingEpisode = allEpisodesRef.current.find(
              (ep) => ep.seasonNumber === nextEp.seasonNumber && ep.episodeNumber === nextEp.episodeNumber,
            );
            if (matchingEpisode) {
              if (nextEp.autoPlay && handlePlayEpisodeRef.current) {
                console.log('[Details] Auto-playing next episode:', matchingEpisode);
                handlePlayEpisodeRef.current(matchingEpisode);
                // Clear the state since we're handling it now
                setNextEpisodeFromPlayback(null);
              } else if (handleEpisodeSelectRef.current) {
                console.log('[Details] Selecting next episode:', matchingEpisode);
                handleEpisodeSelectRef.current(matchingEpisode);
              }
            }
          }
        }
      }
    }, [titleId]),
  );

  const initialSeasonNumber = useMemo(() => {
    // If we have a next episode from playback, use that
    if (nextEpisodeFromPlayback) {
      return nextEpisodeFromPlayback.seasonNumber;
    }

    // Empty string should be treated as null, not 0
    if (!initialSeasonParam || initialSeasonParam.trim() === '') {
      console.log('[Details] Initial season: empty param, returning null');
      return null;
    }
    const parsed = Number(initialSeasonParam);
    const result = Number.isFinite(parsed) ? Math.trunc(parsed) : null;
    console.log('[Details] Initial season:', { initialSeasonParam, parsed, result });
    return result;
  }, [initialSeasonParam, nextEpisodeFromPlayback]);

  const initialEpisodeNumber = useMemo(() => {
    // If we have a next episode from playback, use that
    if (nextEpisodeFromPlayback) {
      return nextEpisodeFromPlayback.episodeNumber;
    }

    // Empty string should be treated as null, not 0
    if (!initialEpisodeParam || initialEpisodeParam.trim() === '') {
      console.log('[Details] Initial episode: empty param, returning null');
      return null;
    }
    const parsed = Number(initialEpisodeParam);
    const result = Number.isFinite(parsed) ? Math.trunc(parsed) : null;
    console.log('[Details] Initial episode:', { initialEpisodeParam, parsed, result });
    return result;
  }, [initialEpisodeParam, nextEpisodeFromPlayback]);

  const { settings, userSettings } = useBackendSettings();
  const { addToWatchlist, removeFromWatchlist, getItem } = useWatchlist();
  const {
    isWatched: isItemWatched,
    toggleWatchStatus,
    bulkUpdateWatchStatus,
    refresh: refreshWatchStatus,
    items: watchStatusItems,
  } = useWatchStatus();
  const { showToast, hideToast } = useToast();
  const { recordEpisodeWatch } = useContinueWatching();
  const { activeUserId, activeUser } = useUserProfiles();
  const { showLoadingScreen, hideLoadingScreen, setOnCancel } = useLoadingScreen();

  const [isResolving, setIsResolving] = useState(false);
  const [selectionError, setSelectionError] = useState<string | null>(null);
  const [showBlackOverlay, setShowBlackOverlay] = useState(false);

  // Clear black overlay and loading screen when returning to details page
  useFocusEffect(
    useCallback(() => {
      setShowBlackOverlay(false);
      hideLoadingScreen();
    }, [hideLoadingScreen]),
  );
  const [manualVisible, setManualVisible] = useState(false);
  const [manualLoading, setManualLoading] = useState(false);
  const [manualError, setManualError] = useState<string | null>(null);
  // Start loading states as true so page waits for metadata before showing (on TV/mobile)
  const [movieDetailsLoading, setMovieDetailsLoading] = useState(true);
  const [movieDetailsError, setMovieDetailsError] = useState<string | null>(null);
  const [seriesDetailsLoading, setSeriesDetailsLoading] = useState(true);
  const [manualResults, setManualResults] = useState<NZBResult[]>([]);
  const [selectionInfo, setSelectionInfo] = useState<string | null>(null);
  const [, setHasSeriesFocusTarget] = useState(false);
  const [watchlistBusy, setWatchlistBusy] = useState(false);
  const [watchlistError, setWatchlistError] = useState<string | null>(null);
  const [activeEpisode, setActiveEpisode] = useState<SeriesEpisode | null>(null);
  const [trailers, setTrailers] = useState<Trailer[]>([]);
  const [primaryTrailer, setPrimaryTrailer] = useState<Trailer | null>(null);
  const [trailersLoading, setTrailersLoading] = useState(false);
  const [trailersError, setTrailersError] = useState<string | null>(null);
  const [trailerModalVisible, setTrailerModalVisible] = useState(false);
  const [activeTrailer, setActiveTrailer] = useState<Trailer | null>(null);
  const [bulkWatchModalVisible, setBulkWatchModalVisible] = useState(false);
  const [resumeModalVisible, setResumeModalVisible] = useState(false);
  const [seasonSelectorVisible, setSeasonSelectorVisible] = useState(false);
  const [episodeSelectorVisible, setEpisodeSelectorVisible] = useState(false);
  const [currentProgress, setCurrentProgress] = useState<{
    position: number;
    duration: number;
    percentWatched: number;
  } | null>(null);
  const [displayProgress, setDisplayProgress] = useState<number | null>(null);

  const _overlayOpen =
    manualVisible ||
    trailerModalVisible ||
    bulkWatchModalVisible ||
    resumeModalVisible ||
    seasonSelectorVisible ||
    episodeSelectorVisible;
  const [pendingPlaybackAction, setPendingPlaybackAction] = useState<((startOffset?: number) => Promise<void>) | null>(
    null,
  );
  const [isDescriptionExpanded, setIsDescriptionExpanded] = useState(false);
  const [collapsedHeight, setCollapsedHeight] = useState(0);
  const [expandedHeight, setExpandedHeight] = useState(0);
  const descriptionHeight = useSharedValue(0);
  const [nextUpEpisode, setNextUpEpisode] = useState<SeriesEpisode | null>(null);
  const [allEpisodes, setAllEpisodes] = useState<SeriesEpisode[]>([]);
  const [isShuffleMode, setIsShuffleMode] = useState(false);
  const [isEpisodeStripFocused, setIsEpisodeStripFocused] = useState(false);
  const [seasons, setSeasons] = useState<SeriesSeason[]>([]);
  const [selectedSeason, setSelectedSeason] = useState<SeriesSeason | null>(null);
  const [hasWatchedEpisodes, setHasWatchedEpisodes] = useState(false);
  const [episodesLoading, setEpisodesLoading] = useState(true);
  const abortControllerRef = useRef<AbortController | null>(null);

  // Debug logging for navigation
  useEffect(() => {
    if (Platform.isTV) {
      console.log('[Details NAV DEBUG] Component state:', {
        isTV: Platform.isTV,
        hasActiveEpisode: !!activeEpisode,
        activeEpisodeNum: activeEpisode?.episodeNumber,
        isEpisodeStripFocused,
        isSeries,
        episodesLoading,
      });
    }
  }, [activeEpisode, isEpisodeStripFocused, isSeries, episodesLoading]);

  // Prequeue state for pre-loading playback
  const [prequeueId, setPrequeueId] = useState<string | null>(null);
  const [prequeueReady, setPrequeueReady] = useState(false);
  const [prequeueTargetEpisode, setPrequeueTargetEpisode] = useState<{
    seasonNumber: number;
    episodeNumber: number;
  } | null>(null);
  // Track pending prequeue request so play button can wait for it
  // Returns both ID and target episode so we don't have to wait for state updates
  const prequeuePromiseRef = useRef<Promise<{
    id: string;
    targetEpisode: { seasonNumber: number; episodeNumber: number } | null;
  } | null> | null>(null);
  // Cache prequeue status from navigation (player already resolved it)
  const navigationPrequeueStatusRef = useRef<PrequeueStatusResponse | null>(null);

  // Debug: Track resume modal visibility changes
  useEffect(() => {
    console.log('🎬 resumeModalVisible changed to:', resumeModalVisible);
    console.log('🎬 currentProgress:', currentProgress);
  }, [resumeModalVisible, currentProgress]);

  // Reset episodes loading state when titleId changes or when it's not a series
  useEffect(() => {
    if (isSeries) {
      setEpisodesLoading(true);
    } else {
      setEpisodesLoading(false);
    }
  }, [titleId, isSeries]);

  // Clean up black overlay on unmount
  useEffect(() => {
    return () => {
      setShowBlackOverlay(false);
    };
  }, []);

  // Set up cancel handler for loading screen
  useEffect(() => {
    setOnCancel(() => {
      console.log('🚫 Loading screen cancelled by user');
      // Cancel any pending playback
      if (abortControllerRef.current) {
        abortControllerRef.current.abort();
        abortControllerRef.current = null;
      }
      // Clear the black overlay
      setShowBlackOverlay(false);
      // Clear resolving state
      setIsResolving(false);
    });

    return () => {
      setOnCancel(null);
    };
  }, [setOnCancel]);

  // Only activate spatial navigation when we're on the details page (not on player or other pages)
  const isDetailsPageActive = pathname === '/details';

  useEffect(() => {
    if (Platform.isTV) {
      console.log(
        '[Details] Spatial navigation active:',
        isDetailsPageActive &&
          !manualVisible &&
          !trailerModalVisible &&
          !bulkWatchModalVisible &&
          !resumeModalVisible &&
          !seasonSelectorVisible &&
          !episodeSelectorVisible,
        {
          pathname,
          isDetailsPageActive,
          manualVisible,
          trailerModalVisible,
          bulkWatchModalVisible,
          resumeModalVisible,
          seasonSelectorVisible,
          episodeSelectorVisible,
        },
      );
    }
  }, [
    isDetailsPageActive,
    manualVisible,
    trailerModalVisible,
    bulkWatchModalVisible,
    resumeModalVisible,
    seasonSelectorVisible,
    episodeSelectorVisible,
    pathname,
  ]);

  // Cleanup: cancel pending playback on unmount or navigation away
  useEffect(() => {
    return () => {
      if (abortControllerRef.current) {
        console.log('🚫 Cancelling pending playback due to navigation away');
        abortControllerRef.current.abort();
        abortControllerRef.current = null;
      }
    };
  }, []);

  // Fetch watch state to determine next episode
  useEffect(() => {
    if (!isSeries || !seriesIdentifier || !activeUserId || allEpisodes.length === 0) {
      setNextUpEpisode(null);
      setHasWatchedEpisodes(false);
      return;
    }

    let cancelled = false;

    apiService
      .getSeriesWatchState(activeUserId, seriesIdentifier)
      .then((watchState) => {
        if (cancelled) {
          return;
        }

        // Check if there are any watched episodes
        const watchedEpisodesCount = watchState?.watchedEpisodes ? Object.keys(watchState.watchedEpisodes).length : 0;
        setHasWatchedEpisodes(watchedEpisodesCount > 0);

        if (!watchState?.nextEpisode) {
          return;
        }

        // Find the matching episode in our series details
        const matchingEpisode = allEpisodes.find(
          (ep) =>
            ep.seasonNumber === watchState.nextEpisode!.seasonNumber &&
            ep.episodeNumber === watchState.nextEpisode!.episodeNumber,
        );

        if (matchingEpisode) {
          console.log('📺 Found next up episode:', matchingEpisode);
          setNextUpEpisode(matchingEpisode);
        }
      })
      .catch((error) => {
        if (!cancelled) {
          console.log('Unable to fetch watch state (may not exist yet):', error);
          setHasWatchedEpisodes(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [isSeries, seriesIdentifier, activeUserId, allEpisodes]);

  // Prequeue playback when details page loads
  useEffect(() => {
    console.log('[prequeue] useEffect triggered', {
      activeUserId: activeUserId ?? 'null',
      titleId: titleId ?? 'null',
      title: title ? title.substring(0, 30) : 'null',
      isSeries,
    });

    // For series, determine which episode to prequeue
    // Priority: activeEpisode (user-selected) > nextUpEpisode (from watch history)
    const targetEpisode = isSeries ? activeEpisode || nextUpEpisode : null;

    // Clear existing prequeue state immediately when episode changes
    // This ensures we wait for the new prequeue instead of using stale data
    setPrequeueId(null);
    setPrequeueTargetEpisode(null);
    setPrequeueReady(false);

    if (!activeUserId || !titleId || !title) {
      console.log('[prequeue] Skipping prequeue - missing:', {
        activeUserId: !activeUserId,
        titleId: !titleId,
        title: !title,
      });
      prequeuePromiseRef.current = null;
      return;
    }

    // For series, wait until we have episode info before prequeuing
    // This prevents prequeuing the wrong episode
    if (isSeries && !targetEpisode) {
      console.log('[prequeue] Waiting for episode info before prequeuing series');
      return;
    }

    let cancelled = false;

    const initiatePrequeue = async (): Promise<{
      id: string;
      targetEpisode: { seasonNumber: number; episodeNumber: number } | null;
    } | null> => {
      try {
        const episodeInfo = targetEpisode
          ? `S${String(targetEpisode.seasonNumber).padStart(2, '0')}E${String(targetEpisode.episodeNumber).padStart(2, '0')}`
          : '';
        console.log(
          '[prequeue] Initiating prequeue for titleId:',
          titleId,
          'title:',
          title,
          'mediaType:',
          mediaType,
          episodeInfo ? `episode: ${episodeInfo}` : '',
        );
        const response = await apiService.prequeuePlayback({
          titleId,
          titleName: title,
          mediaType: isSeries ? 'series' : 'movie',
          userId: activeUserId,
          imdbId: imdbId || undefined,
          year: yearNumber || undefined,
          seasonNumber: targetEpisode?.seasonNumber,
          episodeNumber: targetEpisode?.episodeNumber,
        });

        if (cancelled) {
          return null;
        }

        console.log('[prequeue] Prequeue initiated:', response.prequeueId, 'targetEpisode:', response.targetEpisode);
        setPrequeueId(response.prequeueId);
        const respTargetEpisode = response.targetEpisode
          ? {
              seasonNumber: response.targetEpisode.seasonNumber,
              episodeNumber: response.targetEpisode.episodeNumber,
            }
          : null;
        if (respTargetEpisode) {
          setPrequeueTargetEpisode(respTargetEpisode);
        }
        return { id: response.prequeueId, targetEpisode: respTargetEpisode };
      } catch (error) {
        // Silently fail - prequeue is an optimization, not required
        if (!cancelled) {
          console.log('[prequeue] Prequeue failed (non-fatal):', error);
          setPrequeueId(null);
          setPrequeueTargetEpisode(null);
        }
        return null;
      }
    };

    // Store the promise so play button can wait for it
    prequeuePromiseRef.current = initiatePrequeue();

    return () => {
      cancelled = true;
      prequeuePromiseRef.current = null;
    };
  }, [titleId, title, mediaType, isSeries, activeUserId, imdbId, yearNumber, activeEpisode, nextUpEpisode]);

  // Poll prequeue status until ready
  useEffect(() => {
    if (!prequeueId) {
      setPrequeueReady(false);
      return;
    }

    let cancelled = false;
    let timeoutId: ReturnType<typeof setTimeout>;

    const pollStatus = async () => {
      try {
        const response = await apiService.getPrequeueStatus(prequeueId);
        if (cancelled) return;

        if (response.status === 'ready') {
          console.log('[prequeue] Prequeue is ready:', prequeueId);
          setPrequeueReady(true);
        } else if (apiService.isPrequeueInProgress(response.status)) {
          // Still in progress, poll again
          timeoutId = setTimeout(pollStatus, 1000);
        } else {
          // Failed or expired
          console.log('[prequeue] Prequeue failed/expired:', prequeueId, response.status);
          setPrequeueReady(false);
        }
      } catch (error) {
        if (!cancelled) {
          console.log('[prequeue] Status poll failed:', error);
          setPrequeueReady(false);
        }
      }
    };

    pollStatus();

    return () => {
      cancelled = true;
      if (timeoutId) clearTimeout(timeoutId);
    };
  }, [prequeueId]);

  // Fetch and display progress indicator for the current item
  useEffect(() => {
    if (!activeUserId) {
      setDisplayProgress(null);
      return;
    }

    let cancelled = false;

    const fetchProgress = async () => {
      try {
        // For movies, use the titleId/seriesIdentifier
        // For series, use activeEpisode if available, otherwise nextUpEpisode
        const episodeToShow = activeEpisode || nextUpEpisode;
        const itemId =
          isSeries && episodeToShow && seriesIdentifier
            ? `${seriesIdentifier}:S${String(episodeToShow.seasonNumber).padStart(2, '0')}E${String(episodeToShow.episodeNumber).padStart(2, '0')}`
            : seriesIdentifier || titleId;

        if (!itemId) {
          setDisplayProgress(null);
          return;
        }

        const mediaType = isSeries && episodeToShow ? 'episode' : 'movie';
        const progress = await apiService.getPlaybackProgress(activeUserId, mediaType, itemId);

        if (cancelled) {
          return;
        }

        // Only show progress if it's meaningful (between 5% and 95%)
        if (progress && progress.percentWatched > 5 && progress.percentWatched < 95) {
          setDisplayProgress(Math.round(progress.percentWatched));
        } else {
          setDisplayProgress(null);
        }
      } catch (error) {
        if (!cancelled) {
          console.log('Unable to fetch progress for display:', error);
          setDisplayProgress(null);
        }
      }
    };

    void fetchProgress();

    return () => {
      cancelled = true;
    };
  }, [activeUserId, isSeries, activeEpisode, nextUpEpisode, seriesIdentifier, titleId, watchStatusItems]);

  useEffect(() => {
    if (!selectionError) {
      return;
    }
    showToast(selectionError, {
      tone: 'danger',
      id: SELECTION_TOAST_ID,
      duration: 7000,
    });
  }, [selectionError, showToast]);

  useEffect(() => {
    if (selectionError) {
      return;
    }
    if (selectionInfo) {
      showToast(selectionInfo, {
        tone: 'info',
        id: SELECTION_TOAST_ID,
        duration: 4000,
      });
    } else {
      hideToast(SELECTION_TOAST_ID);
    }
  }, [selectionError, selectionInfo, showToast, hideToast]);

  useEffect(() => {
    if (!movieDetailsQuery) {
      console.log('[Details] No movie details query, skipping fetch');
      setMovieDetails(null);
      setMovieDetailsLoading(false);
      setMovieDetailsError(null);
      return;
    }

    console.log('[Details] Fetching movie details with query:', movieDetailsQuery);
    let cancelled = false;
    setMovieDetailsLoading(true);
    setMovieDetailsError(null);

    apiService
      .getMovieDetails(movieDetailsQuery)
      .then((details) => {
        if (cancelled) {
          return;
        }
        console.log('[Details] Movie details fetched:', {
          name: details.name,
          hasPoster: !!details.poster?.url,
          hasBackdrop: !!details.backdrop?.url,
          posterUrl: details.poster?.url,
          backdropUrl: details.backdrop?.url,
        });
        setMovieDetails(details);
        setMovieDetailsLoading(false);
      })
      .catch((error) => {
        if (cancelled) {
          return;
        }
        console.warn('[details] movie metadata fetch failed', error);
        setMovieDetails(null);
        setMovieDetailsLoading(false);
        setMovieDetailsError(error instanceof Error ? error.message : 'Unable to load movie metadata.');
      });

    return () => {
      cancelled = true;
    };
  }, [movieDetailsQuery]);

  // Fetch series details for backdrop updates (separate from SeriesEpisodes component)
  useEffect(() => {
    if (!isSeries) {
      setSeriesDetailsForBackdrop(null);
      setSeriesDetailsLoading(false);
      return;
    }

    const normalizedTitle = title?.trim();
    if (!normalizedTitle && !tvdbIdNumber && !titleId) {
      setSeriesDetailsForBackdrop(null);
      setSeriesDetailsLoading(false);
      return;
    }

    let cancelled = false;
    setSeriesDetailsLoading(true);

    apiService
      .getSeriesDetails({
        tvdbId: tvdbIdNumber || undefined,
        titleId: titleId || undefined,
        name: normalizedTitle || undefined,
        year: yearNumber,
        tmdbId: tmdbIdNumber,
      })
      .then((details) => {
        if (cancelled) {
          return;
        }
        setSeriesDetailsForBackdrop(details.title);
        setSeriesDetailsLoading(false);
      })
      .catch((error) => {
        if (cancelled) {
          return;
        }
        console.warn('[details] series metadata fetch for backdrop failed', error);
        setSeriesDetailsForBackdrop(null);
        setSeriesDetailsLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [isSeries, title, titleId, tvdbIdNumber, tmdbIdNumber, yearNumber]);

  useEffect(() => {
    const shouldAttempt = Boolean(tmdbIdNumber || tvdbIdNumber || titleId || title);
    if (!shouldAttempt) {
      setTrailers([]);
      setPrimaryTrailer(null);
      setTrailersError(null);
      setTrailersLoading(false);
      return;
    }

    let cancelled = false;
    setTrailersLoading(true);
    setTrailersError(null);

    apiService
      .getTrailers({
        mediaType,
        titleId: titleId || undefined,
        name: title || undefined,
        year: yearNumber,
        tmdbId: tmdbIdNumber,
        tvdbId: tvdbIdNumber,
        imdbId: imdbId || undefined,
      })
      .then((response) => {
        if (cancelled) {
          return;
        }
        const nextTrailers = response?.trailers ?? [];
        setTrailers(nextTrailers);
        setPrimaryTrailer(response?.primaryTrailer ?? (nextTrailers.length ? nextTrailers[0] : null));
      })
      .catch((error) => {
        if (cancelled) {
          return;
        }
        const message = error instanceof Error ? error.message : 'Unable to load trailers.';
        setTrailersError(message);
        setTrailers([]);
        setPrimaryTrailer(null);
      })
      .finally(() => {
        if (!cancelled) {
          setTrailersLoading(false);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [imdbId, mediaType, title, titleId, tmdbIdNumber, tvdbIdNumber, yearNumber]);

  const findPreviousEpisode = useCallback(
    (episode: SeriesEpisode): SeriesEpisode | null => {
      if (allEpisodes.length === 0) {
        return null;
      }

      // Find the current episode index
      const currentIndex = allEpisodes.findIndex(
        (ep) => ep.seasonNumber === episode.seasonNumber && ep.episodeNumber === episode.episodeNumber,
      );

      // If not found or it's the first episode, return null
      if (currentIndex === -1 || currentIndex === 0) {
        return null;
      }

      // Return the previous episode
      return allEpisodes[currentIndex - 1];
    },
    [allEpisodes],
  );

  const findNextEpisode = useCallback(
    (episode: SeriesEpisode): SeriesEpisode | null => {
      if (allEpisodes.length === 0) {
        return null;
      }

      // Find the current episode index
      const currentIndex = allEpisodes.findIndex(
        (ep) => ep.seasonNumber === episode.seasonNumber && ep.episodeNumber === episode.episodeNumber,
      );

      // If not found or it's the last episode, return null
      if (currentIndex === -1 || currentIndex === allEpisodes.length - 1) {
        return null;
      }

      // Return the next episode
      return allEpisodes[currentIndex + 1];
    },
    [allEpisodes],
  );

  const findFirstEpisodeOfNextSeason = useCallback(
    (seasonNumber: number): SeriesEpisode | null => {
      if (allEpisodes.length === 0) {
        return null;
      }

      // Find the first episode of the next season
      return allEpisodes.find((ep) => ep.seasonNumber === seasonNumber + 1) || null;
    },
    [allEpisodes],
  );

  const findFirstEpisode = useCallback((): SeriesEpisode | null => {
    if (allEpisodes.length === 0) {
      return null;
    }

    // Return the first episode (episodes should be sorted)
    return allEpisodes[0];
  }, [allEpisodes]);

  const toEpisodeReference = useCallback(
    (episode: SeriesEpisode): EpisodeWatchPayload['episode'] => ({
      seasonNumber: episode.seasonNumber,
      episodeNumber: episode.episodeNumber,
      episodeId: episode.id,
      tvdbId: episode.tvdbId ? String(episode.tvdbId) : undefined,
      title: episode.name,
      overview: episode.overview,
      runtimeMinutes: episode.runtimeMinutes,
      airDate: episode.airedDate,
    }),
    [],
  );

  const externalIds = useMemo(() => {
    const ids: Record<string, string> = {};
    if (tmdbId) {
      ids.tmdb = tmdbId;
    }
    if (imdbId) {
      ids.imdb = imdbId;
    }
    if (tvdbId) {
      ids.tvdb = tvdbId;
    }
    return Object.keys(ids).length ? ids : undefined;
  }, [imdbId, tmdbId, tvdbId]);

  const watchlistItem = useMemo(() => {
    if (!titleId) {
      return undefined;
    }
    return getItem(mediaType, titleId);
  }, [getItem, mediaType, titleId]);

  const isWatchlisted = Boolean(watchlistItem);
  // For series, check the current episode's watched status; for movies, check the title
  const currentEpisodeForWatchState = activeEpisode || nextUpEpisode;
  const isWatched = useMemo(() => {
    if (!titleId) {
      return false;
    }
    // For series with a current episode, check the episode's watched status
    if (isSeries && currentEpisodeForWatchState && seriesIdentifier) {
      const episodeId = `${seriesIdentifier}:s${String(currentEpisodeForWatchState.seasonNumber).padStart(2, '0')}e${String(currentEpisodeForWatchState.episodeNumber).padStart(2, '0')}`;
      return isItemWatched('episode', episodeId);
    }
    // For movies or series without a current episode, check the title-level status
    return isItemWatched(mediaType, titleId);
  }, [isItemWatched, mediaType, titleId, isSeries, currentEpisodeForWatchState, seriesIdentifier]);
  const canToggleWatchlist = Boolean(titleId && mediaType);

  const watchlistButtonLabel = isWatchlisted ? 'Remove' : 'Watchlist';
  const watchStateButtonLabel = isSeries ? 'Watch State' : isWatched ? 'Mark as not watched' : 'Mark as watched';
  const watchNowLabel = Platform.isTV
    ? !isSeries || !hasWatchedEpisodes
      ? 'Play'
      : 'Up Next'
    : isResolving
      ? 'Resolving…'
      : !isSeries || !hasWatchedEpisodes
        ? 'Play'
        : 'Up Next';
  const manualSelectLabel = 'Search';
  const manualResultsMaxHeight = useMemo(() => {
    if (!windowHeight || !Number.isFinite(windowHeight)) {
      return isCompactBreakpoint ? 360 : 520;
    }
    if (isCompactBreakpoint) {
      return Math.max(320, windowHeight * 0.8);
    }
    return Math.min(520, windowHeight * 0.7);
  }, [isCompactBreakpoint, windowHeight]);

  const hasAvailableTrailer = useMemo(
    () => Boolean((primaryTrailer && primaryTrailer.url) || (trailers?.length ?? 0) > 0),
    [primaryTrailer, trailers],
  );

  const trailerButtonLabel = useMemo(() => (trailersLoading ? 'Loading trailer…' : 'Watch trailer'), [trailersLoading]);

  const trailerButtonDisabled = trailersLoading || !hasAvailableTrailer;

  const playbackPreference = useMemo<PlaybackPreference>(() => {
    // Prefer user settings, fall back to global settings
    const userPref = userSettings?.playback?.preferredPlayer;
    const globalPref = settings?.playback?.preferredPlayer;
    const value = userPref || globalPref; // Use || to also fallback for empty string
    console.log('[playbackPreference]', { userPref, globalPref, resolved: value, platform: Platform.OS });
    if (value === 'outplayer' || value === 'infuse') {
      // Infuse is only available on iOS/macOS, fallback to native on Android
      if (value === 'infuse' && Platform.OS === 'android') {
        console.log('[playbackPreference] Infuse not available on Android, using native');
        return 'native';
      }
      return value;
    }
    return 'native';
  }, [userSettings?.playback?.preferredPlayer, settings?.playback?.preferredPlayer]);

  const fetchIndexerResults = useCallback(
    async ({ query, limit = 5, categories = [] }: { query?: string; limit?: number; categories?: string[] }) => {
      const searchQuery = (query ?? title ?? '').toString().trim();
      if (!searchQuery) {
        throw new Error('Missing title to search for results.');
      }
      const imdbIdToUse = imdbId;
      console.log('[details] fetchIndexerResults', {
        searchQuery,
        imdbId,
        seriesDetailsImdbId: undefined,
        imdbIdToUse,
        mediaType,
        year: yearNumber,
        userId: activeUserId,
      });
      return apiService.searchIndexer(
        searchQuery,
        limit,
        categories,
        imdbIdToUse,
        mediaType,
        yearNumber,
        activeUserId ?? undefined,
      );
    },
    [title, imdbId, mediaType, yearNumber, activeUserId],
  );

  const getEpisodeSearchContext = useCallback(
    (episode: SeriesEpisode): EpisodeSearchContext | null => {
      const trimmedTitle = title.trim();
      const baseTitle = trimmedTitle || title;
      const query = buildEpisodeQuery(baseTitle, episode.seasonNumber, episode.episodeNumber);
      if (!query) {
        return null;
      }

      const episodeCode = `S${padNumber(episode.seasonNumber)}E${padNumber(episode.episodeNumber)}`;
      const labelSuffix = episode.name ? ` – "${episode.name}"` : '';
      const friendlyLabel = baseTitle ? `${baseTitle} ${episodeCode}${labelSuffix}` : `${episodeCode}${labelSuffix}`;
      const selectionMessage = baseTitle ? `${baseTitle} • ${episodeCode}` : episodeCode;

      return {
        query,
        friendlyLabel,
        selectionMessage,
        episodeCode,
      };
    },
    [title],
  );

  const initiatePlaybackRef = useRef<
    ((result: NZBResult, signal?: AbortSignal, overrides?: { useDebugPlayer?: boolean }) => Promise<void>) | null
  >(null);
  const pendingStartOffsetRef = useRef<number | null>(null);

  const describeRelease = useCallback((release?: Title['homeRelease']) => {
    if (!release?.date) {
      return '';
    }
    const dateLabel = formatPublishDate(release.date) || release.date;
    const parts = [dateLabel];
    if (release.country) {
      parts.push(release.country.toUpperCase());
    }
    if (release.note) {
      parts.push(release.note);
    }
    return parts.filter(Boolean).join(' • ');
  }, []);

  const formatTheatricalLabel = useCallback((release?: Title['theatricalRelease']) => {
    if (release?.type?.toLowerCase() === 'theatricallimited') {
      return 'Theatrical (Limited)';
    }
    if (release?.type?.toLowerCase() === 'premiere') {
      return 'Premiere';
    }
    return 'Theatrical Release';
  }, []);

  const formatHomeLabel = useCallback((release?: Title['homeRelease']) => {
    const type = release?.type?.toLowerCase();
    switch (type) {
      case 'digital':
        return 'Digital Release';
      case 'physical':
        return 'Physical Release';
      case 'tv':
        return 'TV Premiere';
      default:
        return 'Home Release';
    }
  }, []);

  const releaseRows = useMemo(() => {
    if (isSeries || !movieDetails) {
      return [];
    }
    const rows: { key: string; label: string; value: string }[] = [];
    if (movieDetails.theatricalRelease) {
      const value = describeRelease(movieDetails.theatricalRelease);
      if (value) {
        rows.push({
          key: 'theatrical',
          label: formatTheatricalLabel(movieDetails.theatricalRelease),
          value,
        });
      }
    }
    if (movieDetails.homeRelease) {
      const value = describeRelease(movieDetails.homeRelease);
      if (value) {
        rows.push({
          key: 'home',
          label: formatHomeLabel(movieDetails.homeRelease),
          value,
        });
      }
    }
    return rows;
  }, [describeRelease, formatHomeLabel, formatTheatricalLabel, isSeries, movieDetails]);

  const shouldShowReleaseSkeleton = !isSeries && movieDetailsLoading && releaseRows.length === 0;
  const releaseErrorMessage =
    !isSeries && movieDetailsError && !movieDetailsLoading && releaseRows.length === 0 ? movieDetailsError : null;

  // Get ratings from movie or series details, sorted by RATING_ORDER
  const ratings = useMemo(() => {
    const rawRatings = isSeries ? (seriesDetailsForBackdrop?.ratings ?? []) : (movieDetails?.ratings ?? []);
    return [...rawRatings].sort((a, b) => {
      const orderA = RATING_ORDER[a.source] ?? 99;
      const orderB = RATING_ORDER[b.source] ?? 99;
      return orderA - orderB;
    });
  }, [isSeries, movieDetails, seriesDetailsForBackdrop]);

  const handleInitiatePlayback = useCallback(
    async (result: NZBResult, signal?: AbortSignal, overrides?: { useDebugPlayer?: boolean }) => {
      // Note: Loading screen is now shown earlier (in checkAndShowResumeModal or handleResumePlayback/handlePlayFromBeginning)
      // so users see it immediately when they click play, not after the stream resolves

      // Build title with episode code for series episodes
      let displayTitle = title;
      if (activeEpisode?.seasonNumber && activeEpisode?.episodeNumber) {
        const seasonStr = activeEpisode.seasonNumber.toString().padStart(2, '0');
        const episodeStr = activeEpisode.episodeNumber.toString().padStart(2, '0');
        displayTitle = `${title} - S${seasonStr}E${episodeStr}`;
      }

      await initiatePlayback(
        result,
        playbackPreference,
        settings,
        headerImage,
        displayTitle,
        router,
        isIosWeb,
        setSelectionInfo,
        setSelectionError,
        {
          // Use 'episode' if activeEpisode exists OR if we're on a series page (isSeries)
          // This prevents defaulting to 'movie' for TV show episodes
          mediaType: activeEpisode || isSeries ? 'episode' : 'movie',
          // Pass clean series title (without episode code) for metadata lookups
          seriesTitle: activeEpisode || isSeries ? title : undefined,
          year: yearNumber,
          seasonNumber: activeEpisode?.seasonNumber,
          episodeNumber: activeEpisode?.episodeNumber,
          episodeName: activeEpisode?.name,
          signal,
          titleId,
          imdbId,
          tvdbId,
          // Pass startOffset if we have pendingStartOffset (from resume) or currentProgress
          ...(() => {
            const offset = pendingStartOffsetRef.current;
            if (offset !== null) {
              // Clear after consuming so it doesn't persist to future playbacks
              pendingStartOffsetRef.current = null;
              return { startOffset: offset };
            }
            if (currentProgress) {
              return { startOffset: currentProgress.position };
            }
            return {};
          })(),
          ...(overrides?.useDebugPlayer ? { debugPlayer: true } : {}),
          // Hide loading screen when launching external player
          onExternalPlayerLaunch: hideLoadingScreen,
          // Per-user settings override for track selection
          userSettings,
          // Profile info for stream tracking
          profileId: activeUserId ?? undefined,
          profileName: activeUser?.name,
          // Shuffle mode for random episode playback
          shuffleMode: isShuffleMode,
        },
      );
    },
    [
      hideLoadingScreen,
      initiatePlayback,
      playbackPreference,
      settings,
      userSettings,
      activeUserId,
      activeUser,
      isShuffleMode,
      headerImage,
      title,
      router,
      isIosWeb,
      isSeries,
      yearNumber,
      activeEpisode,
      titleId,
      imdbId,
      tvdbId,
      currentProgress,
    ],
  );

  useEffect(() => {
    initiatePlaybackRef.current = handleInitiatePlayback;
  }, [handleInitiatePlayback]);

  // Helper to extract episode info from a query string (e.g., "Show Name S01E02")
  const extractEpisodeFromQuery = useCallback(
    (query: string): { seasonNumber: number; episodeNumber: number } | null => {
      const match = query.match(/S(\d{1,2})E(\d{1,2})/i);
      if (match && match[1] && match[2]) {
        return {
          seasonNumber: parseInt(match[1], 10),
          episodeNumber: parseInt(match[2], 10),
        };
      }
      return null;
    },
    [],
  );

  // Helper to check if prequeue target matches the requested playback
  // Accepts optional pqId and targetEp to use instead of state (for when state hasn't updated yet)
  const doesPrequeueMatch = useCallback(
    (
      query: string,
      pqId?: string | null,
      targetEp?: { seasonNumber: number; episodeNumber: number } | null,
    ): boolean => {
      const effectivePrequeueId = pqId !== undefined ? pqId : prequeueId;
      const effectiveTargetEpisode = targetEp !== undefined ? targetEp : prequeueTargetEpisode;

      if (!effectivePrequeueId) {
        return false;
      }

      // For movies, any prequeue for this title matches
      if (!isSeries) {
        return true;
      }

      // For series, check if episode matches
      const requestedEpisode = extractEpisodeFromQuery(query);
      if (!requestedEpisode || !effectiveTargetEpisode) {
        return false;
      }

      return (
        requestedEpisode.seasonNumber === effectiveTargetEpisode.seasonNumber &&
        requestedEpisode.episodeNumber === effectiveTargetEpisode.episodeNumber
      );
    },
    [prequeueId, isSeries, prequeueTargetEpisode, extractEpisodeFromQuery],
  );

  // Helper to launch playback from prequeue data
  const launchFromPrequeue = useCallback(
    async (prequeueStatus: PrequeueStatusResponse) => {
      if (!prequeueStatus.streamPath) {
        throw new Error('Prequeue is missing stream path');
      }

      // Get start offset from pending ref (for resume playback) - get it early as we may use it for HLS session
      const startOffset = pendingStartOffsetRef.current;
      pendingStartOffsetRef.current = null;

      console.log('[prequeue] launchFromPrequeue called', {
        prequeueId: prequeueStatus.prequeueId,
        streamPath: prequeueStatus.streamPath ? 'set' : 'null',
        hlsPlaylistUrl: prequeueStatus.hlsPlaylistUrl ?? 'null',
        hasDolbyVision: prequeueStatus.hasDolbyVision,
        hasHdr10: prequeueStatus.hasHdr10,
        startOffset: startOffset ?? 'null',
        playbackPreference,
      });

      // Note: Loading screen is now shown earlier (in checkAndShowResumeModal or handleResumePlayback/handlePlayFromBeginning)
      // so users see it immediately when they click play, not after the prequeue resolves

      // Check for external player FIRST - they handle HDR natively and don't need HLS
      const isExternalPlayer = playbackPreference === 'infuse' || playbackPreference === 'outplayer';
      if (isExternalPlayer) {
        console.log('[prequeue] External player selected, skipping HLS creation');
        const label = playbackPreference === 'outplayer' ? 'Outplayer' : 'Infuse';

        // Build backend proxy URL for external player (handles IP-locked debrid URLs)
        // Use manual encoding to ensure semicolons and other special chars are properly encoded
        // URLSearchParams doesn't encode semicolons which breaks some parsers
        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const authToken = apiService.getAuthToken();
        const queryParts: string[] = [];
        queryParts.push(`path=${encodeURIComponent(prequeueStatus.streamPath)}`);
        queryParts.push('transmux=0'); // No transmuxing needed for external players
        if (authToken) {
          queryParts.push(`token=${encodeURIComponent(authToken)}`);
        }
        // Add profile info for stream tracking
        if (activeUserId) {
          queryParts.push(`profileId=${encodeURIComponent(activeUserId)}`);
        }
        if (activeUser?.name) {
          queryParts.push(`profileName=${encodeURIComponent(activeUser.name)}`);
        }
        const directUrl = `${baseUrl}/video/stream?${queryParts.join('&')}`;
        console.log('[prequeue] Using backend proxy URL for external player:', directUrl);

        const externalTargets = buildExternalPlayerTargets(playbackPreference, directUrl, isIosWeb);
        console.log('[prequeue] External player targets:', externalTargets);

        if (externalTargets.length > 0) {
          const { Linking } = require('react-native');

          for (const externalUrl of externalTargets) {
            try {
              const supported = await Linking.canOpenURL(externalUrl);
              if (supported) {
                console.log(`[prequeue] Launching ${label} with URL:`, externalUrl);
                hideLoadingScreen();
                await Linking.openURL(externalUrl);
                return;
              }
            } catch (err) {
              console.error(`[prequeue] Failed to launch ${label}:`, err);
            }
          }

          // External player not available, fall through to native
          console.log(`[prequeue] ${label} not available, falling back to native player`);
          setSelectionError(`${label} is not installed. Using native player.`);
        }
        // Fall through to native player if external player targets not available
      }

      const hasAnyHDR = prequeueStatus.hasDolbyVision || prequeueStatus.hasHdr10;
      const needsHLS = hasAnyHDR || prequeueStatus.needsAudioTranscode;

      // Build stream URL
      let streamUrl: string;
      let hlsDuration: number | undefined;

      // Log the decision factors for HLS path
      console.log('[prequeue] HLS decision factors:', {
        hasAnyHDR,
        needsAudioTranscode: prequeueStatus.needsAudioTranscode ?? false,
        needsHLS,
        hlsPlaylistUrl: prequeueStatus.hlsPlaylistUrl ?? 'null',
        hasStartOffset: typeof startOffset === 'number',
        startOffset,
        platformOS: Platform.OS,
        willUsePrequeueHLS: needsHLS && prequeueStatus.hlsPlaylistUrl && typeof startOffset !== 'number',
        willCreateNewHLS: needsHLS && (!prequeueStatus.hlsPlaylistUrl || typeof startOffset === 'number'),
      });

      if (needsHLS && prequeueStatus.hlsPlaylistUrl && typeof startOffset !== 'number') {
        // HDR/TrueHD content with HLS session already created by backend (no resume position)
        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const authToken = apiService.getAuthToken();
        streamUrl = `${baseUrl}${prequeueStatus.hlsPlaylistUrl}${authToken ? `?token=${encodeURIComponent(authToken)}` : ''}`;
        console.log('[prequeue] ✅ Using PRE-CREATED HLS stream URL:', streamUrl);
      } else if (needsHLS && Platform.OS !== 'web') {
        // HDR/TrueHD content - create HLS session with start offset
        // This happens when: (a) backend didn't create session, or (b) we have a resume position
        // and need to recreate with the correct start offset
        console.log('[prequeue] ⚠️ Creating NEW HLS session (not using prequeue HLS)');
        const reason = typeof startOffset === 'number' ? `resuming at ${startOffset}s` : 'no HLS URL from backend';
        const contentType = prequeueStatus.needsAudioTranscode
          ? 'TrueHD/DTS audio'
          : prequeueStatus.hasDolbyVision
            ? 'Dolby Vision'
            : 'HDR10';
        console.log(`[prequeue] ${contentType} detected, creating HLS session (${reason})...`);
        setSelectionInfo(`Creating HLS session for ${contentType}...`);

        try {
          // Use prequeue-selected tracks if available, otherwise fall back to fetching metadata
          let selectedAudioTrack: number | undefined =
            prequeueStatus.selectedAudioTrack !== undefined && prequeueStatus.selectedAudioTrack >= 0
              ? prequeueStatus.selectedAudioTrack
              : undefined;
          let selectedSubtitleTrack: number | undefined =
            prequeueStatus.selectedSubtitleTrack !== undefined && prequeueStatus.selectedSubtitleTrack >= 0
              ? prequeueStatus.selectedSubtitleTrack
              : undefined;

          // Log if using prequeue-selected tracks
          if (selectedAudioTrack !== undefined || selectedSubtitleTrack !== undefined) {
            console.log(
              `[prequeue] Using prequeue-selected tracks: audio=${selectedAudioTrack}, subtitle=${selectedSubtitleTrack}`,
            );
          }

          // Only fetch metadata if prequeue didn't provide track selection
          if (
            selectedAudioTrack === undefined &&
            selectedSubtitleTrack === undefined &&
            (settings?.playback || userSettings?.playback)
          ) {
            try {
              const metadata = await apiService.getVideoMetadata(prequeueStatus.streamPath);
              if (metadata) {
                const audioLang =
                  userSettings?.playback?.preferredAudioLanguage ?? settings?.playback?.preferredAudioLanguage ?? 'eng';
                const subLang =
                  userSettings?.playback?.preferredSubtitleLanguage ??
                  settings?.playback?.preferredSubtitleLanguage ??
                  'eng';
                const subModeRaw =
                  userSettings?.playback?.preferredSubtitleMode ?? settings?.playback?.preferredSubtitleMode ?? 'off';
                const subMode =
                  subModeRaw === 'on' || subModeRaw === 'off' || subModeRaw === 'forced-only' ? subModeRaw : 'off';

                if (metadata.audioStreams) {
                  const match = findAudioTrackByLanguage(metadata.audioStreams, audioLang);
                  if (match !== null) {
                    selectedAudioTrack = match;
                    console.log(`[prequeue] Selected audio track ${match} for language ${audioLang}`);
                  }
                }

                if (metadata.subtitleStreams) {
                  const match = findSubtitleTrackByPreference(
                    metadata.subtitleStreams,
                    subLang,
                    subMode as 'off' | 'on' | 'forced-only',
                  );
                  if (match !== null) {
                    selectedSubtitleTrack = match;
                    console.log(
                      `[prequeue] Selected subtitle track ${match} for language ${subLang} (mode: ${subMode})`,
                    );
                  }
                }
              }
            } catch (metadataError) {
              console.warn('[prequeue] Failed to fetch metadata for track selection:', metadataError);
            }
          }

          const hlsResponse = await apiService.createHlsSession({
            path: prequeueStatus.streamPath,
            dv: prequeueStatus.hasDolbyVision,
            dvProfile: prequeueStatus.dolbyVisionProfile,
            hdr: prequeueStatus.hasHdr10,
            forceAAC: prequeueStatus.needsAudioTranscode,
            start: typeof startOffset === 'number' ? startOffset : undefined,
            audioTrack: selectedAudioTrack,
            subtitleTrack: selectedSubtitleTrack,
            profileId: activeUserId ?? undefined,
            profileName: activeUser?.name,
          });

          const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
          const authToken = apiService.getAuthToken();
          streamUrl = `${baseUrl}${hlsResponse.playlistUrl}${authToken ? `?token=${encodeURIComponent(authToken)}` : ''}`;
          hlsDuration = hlsResponse.duration;
          console.log('[prequeue] Created HLS session, using URL:', streamUrl);
        } catch (hlsError) {
          console.error('[prequeue] Failed to create HLS session:', hlsError);
          throw new Error(`Failed to create HLS session for ${contentType} content: ${hlsError}`);
        }
      } else {
        // SDR content - build direct stream URL
        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const authToken = apiService.getAuthToken();
        // Build URL manually to ensure proper encoding of special chars like semicolons
        // URLSearchParams doesn't encode semicolons which breaks some parsers
        const queryParts: string[] = [];
        queryParts.push(`path=${encodeURIComponent(prequeueStatus.streamPath)}`);
        if (authToken) {
          queryParts.push(`token=${encodeURIComponent(authToken)}`);
        }
        queryParts.push('transmux=0'); // Let native player handle it
        // Add profile info for stream tracking
        if (activeUserId) {
          queryParts.push(`profileId=${encodeURIComponent(activeUserId)}`);
        }
        if (activeUser?.name) {
          queryParts.push(`profileName=${encodeURIComponent(activeUser.name)}`);
        }
        streamUrl = `${baseUrl}/video/stream?${queryParts.join('&')}`;
        console.log('[prequeue] Using direct stream URL:', streamUrl);
      }

      // Build display title
      let displayTitle = title;
      if (prequeueStatus.targetEpisode) {
        const seasonStr = String(prequeueStatus.targetEpisode.seasonNumber).padStart(2, '0');
        const episodeStr = String(prequeueStatus.targetEpisode.episodeNumber).padStart(2, '0');
        displayTitle = `${title} - S${seasonStr}E${episodeStr}`;
      }

      // Launch native player (external players would have returned early above)
      router.push({
        pathname: '/player',
        params: {
          movie: streamUrl,
          headerImage,
          title: displayTitle,
          ...(isSeries ? { seriesTitle: title } : {}),
          ...(isSeries ? { mediaType: 'episode' } : { mediaType: 'movie' }),
          ...(yearNumber ? { year: String(yearNumber) } : {}),
          ...(prequeueStatus.targetEpisode ? { seasonNumber: String(prequeueStatus.targetEpisode.seasonNumber) } : {}),
          ...(prequeueStatus.targetEpisode
            ? { episodeNumber: String(prequeueStatus.targetEpisode.episodeNumber) }
            : {}),
          sourcePath: encodeURIComponent(prequeueStatus.streamPath),
          ...(prequeueStatus.displayName ? { displayName: prequeueStatus.displayName } : {}),
          ...(prequeueStatus.hasDolbyVision ? { dv: '1' } : {}),
          ...(prequeueStatus.hasHdr10 ? { hdr10: '1' } : {}),
          ...(prequeueStatus.dolbyVisionProfile ? { dvProfile: prequeueStatus.dolbyVisionProfile } : {}),
          ...(prequeueStatus.needsAudioTranscode ? { forceAAC: '1' } : {}),
          ...(typeof startOffset === 'number' ? { startOffset: String(startOffset) } : {}),
          ...(typeof hlsDuration === 'number' ? { durationHint: String(hlsDuration) } : {}),
          ...(titleId ? { titleId } : {}),
          ...(imdbId ? { imdbId } : {}),
          ...(tvdbId ? { tvdbId } : {}),
        },
      });
    },
    [
      title,
      headerImage,
      router,
      isSeries,
      yearNumber,
      titleId,
      imdbId,
      tvdbId,
      setSelectionInfo,
      settings,
      userSettings,
      playbackPreference,
      isIosWeb,
      hideLoadingScreen,
      setSelectionError,
    ],
  );

  // Helper to poll prequeue until ready
  const pollPrequeueUntilReady = useCallback(
    async (pqId: string, signal?: AbortSignal): Promise<PrequeueStatusResponse | null> => {
      const maxWaitMs = 60000; // 60 second timeout
      const pollIntervalMs = 1000;
      const startTime = Date.now();

      while (Date.now() - startTime < maxWaitMs) {
        if (signal?.aborted) {
          return null;
        }

        try {
          const status = await apiService.getPrequeueStatus(pqId);

          if (apiService.isPrequeueReady(status.status)) {
            return status;
          }

          if (!apiService.isPrequeueInProgress(status.status)) {
            // Failed or expired
            console.log('[prequeue] Prequeue no longer in progress:', status.status);
            return null;
          }

          // Update status message
          const statusLabel =
            status.status === 'searching'
              ? 'Searching...'
              : status.status === 'resolving'
                ? 'Preparing stream...'
                : status.status === 'probing'
                  ? 'Detecting video format...'
                  : 'Loading...';
          setSelectionInfo(statusLabel);

          // Wait before next poll
          await new Promise((resolve) => setTimeout(resolve, pollIntervalMs));
        } catch (error) {
          console.log('[prequeue] Poll error:', error);
          return null;
        }
      }

      console.log('[prequeue] Prequeue poll timeout');
      return null;
    },
    [],
  );

  const resolveAndPlay = useCallback(
    async ({
      query,
      friendlyLabel,
      limit = 5,
      selectionMessage,
      useDebugPlayer = false,
      targetEpisode,
    }: {
      query: string;
      friendlyLabel: string;
      limit?: number;
      selectionMessage?: string | null;
      useDebugPlayer?: boolean;
      targetEpisode?: { seasonNumber: number; episodeNumber: number };
    }) => {
      if (isResolving) {
        return;
      }

      console.log('[prequeue] resolveAndPlay called', {
        query,
        prequeueIdState: prequeueId ?? 'null',
        prequeuePromiseExists: !!prequeuePromiseRef.current,
        prequeueTargetEpisode,
        targetEpisode,
      });

      // Check for navigation prequeue first (passed from player's next episode prequeue)
      // This takes priority over any pending prequeue promise from the page's own useEffect
      const navPrequeue = navigationPrequeueStatusRef.current;
      if (navPrequeue && targetEpisode && apiService.isPrequeueReady(navPrequeue.status)) {
        const navTarget = navPrequeue.targetEpisode;
        if (
          navTarget &&
          navTarget.seasonNumber === targetEpisode.seasonNumber &&
          navTarget.episodeNumber === targetEpisode.episodeNumber
        ) {
          console.log('[prequeue] Using navigation prequeue from player:', navPrequeue.prequeueId);
          navigationPrequeueStatusRef.current = null; // Clear after use
          setSelectionInfo(null);
          await launchFromPrequeue(navPrequeue);
          return;
        }
      }

      // Wait for any pending prequeue request to complete first
      let currentPrequeueId = prequeueId;
      let currentTargetEpisode = prequeueTargetEpisode;
      if (!currentPrequeueId && prequeuePromiseRef.current) {
        console.log('[prequeue] Waiting for pending prequeue request...');
        setSelectionInfo('Preparing stream...');
        setIsResolving(true);
        try {
          const result = await prequeuePromiseRef.current;
          if (result) {
            currentPrequeueId = result.id;
            currentTargetEpisode = result.targetEpisode;
          }
          console.log(
            '[prequeue] Pending prequeue completed, id:',
            currentPrequeueId,
            'targetEpisode:',
            currentTargetEpisode,
          );
        } catch (error) {
          console.log('[prequeue] Pending prequeue failed:', error);
        } finally {
          setIsResolving(false);
        }
      }

      // Check if we can use prequeue
      const prequeueMatches = currentPrequeueId
        ? doesPrequeueMatch(query, currentPrequeueId, currentTargetEpisode)
        : false;
      console.log('[prequeue] Prequeue check', {
        currentPrequeueId: currentPrequeueId ?? 'null',
        prequeueMatches,
        isSeries,
      });

      if (currentPrequeueId && prequeueMatches) {
        console.log('[prequeue] Checking prequeue status for:', currentPrequeueId);

        // Create abort controller for prequeue flow
        const abortController = new AbortController();
        abortControllerRef.current = abortController;

        setSelectionError(null);
        setSelectionInfo('Checking pre-loaded stream...');
        setIsResolving(true);

        try {
          // Check if we have a cached ready status from navigation
          let status: PrequeueStatusResponse;
          if (
            navigationPrequeueStatusRef.current &&
            navigationPrequeueStatusRef.current.prequeueId === currentPrequeueId &&
            apiService.isPrequeueReady(navigationPrequeueStatusRef.current.status)
          ) {
            console.log('[prequeue] Using cached navigation prequeue status');
            status = navigationPrequeueStatusRef.current;
            navigationPrequeueStatusRef.current = null; // Clear after use
          } else {
            status = await apiService.getPrequeueStatus(currentPrequeueId);
          }
          console.log('[prequeue] Got prequeue status:', {
            status: status.status,
            streamPath: status.streamPath ? 'set' : 'null',
            hlsPlaylistUrl: status.hlsPlaylistUrl ?? 'null',
            hlsSessionId: status.hlsSessionId ?? 'null',
            hasDolbyVision: status.hasDolbyVision,
            hasHdr10: status.hasHdr10,
          });

          if (abortController.signal.aborted) {
            return;
          }

          if (apiService.isPrequeueReady(status.status)) {
            // Ready to play!
            console.log('[prequeue] Using ready prequeue:', currentPrequeueId);
            // Clear toast before launching player to prevent overlay
            setSelectionInfo(null);
            await launchFromPrequeue(status);
            return;
          }

          if (apiService.isPrequeueInProgress(status.status)) {
            // Still loading, poll until ready
            console.log('[prequeue] Prequeue still loading, polling...');
            const readyStatus = await pollPrequeueUntilReady(currentPrequeueId, abortController.signal);

            if (abortController.signal.aborted) {
              return;
            }

            if (readyStatus) {
              console.log('[prequeue] Prequeue became ready');
              // Clear toast before launching player to prevent overlay
              setSelectionInfo(null);
              await launchFromPrequeue(readyStatus);
              return;
            }
            // Fall through to normal flow
            console.log('[prequeue] Prequeue did not become ready, falling back to normal flow');
          } else {
            console.log('[prequeue] Prequeue not usable (status:', status.status, '), falling back to normal flow');
          }
        } catch (error) {
          console.log('[prequeue] Prequeue check failed, falling back to normal flow:', error);
        } finally {
          setIsResolving(false);
          abortControllerRef.current = null;
        }

        // Clear prequeue state since we're falling through
        setPrequeueId(null);
        setPrequeueTargetEpisode(null);
      } else {
        // Log why we're not using prequeue
        console.log('[prequeue] ⚠️ Skipping prequeue path:', {
          hasPrequeueId: !!currentPrequeueId,
          prequeueMatches,
          reason: !currentPrequeueId ? 'no prequeueId' : 'prequeue does not match query',
        });
      }

      console.log('[prequeue] 📥 Using NORMAL playback flow (not prequeue)');

      // Cancel any pending playback
      if (abortControllerRef.current) {
        console.log('🚫 Cancelling previous playback request');
        abortControllerRef.current.abort();
      }

      // Create new abort controller for this request
      const abortController = new AbortController();
      abortControllerRef.current = abortController;

      const trimmedQuery = query.trim();
      if (!trimmedQuery) {
        setSelectionError(`Missing search query for ${friendlyLabel}.`);
        abortControllerRef.current = null;
        return;
      }

      console.log('🔍 PLAYBACK REQUEST:', {
        query: trimmedQuery,
        friendlyLabel,
        limit,
        titleId,
        title,
      });

      if (selectionMessage !== undefined) {
        setSelectionInfo(selectionMessage);
      } else {
        setSelectionInfo(null);
      }
      setSelectionError(null);
      setIsResolving(true);

      try {
        // Check if aborted before starting
        if (abortController.signal.aborted) {
          console.log('🚫 Playback was cancelled before starting');
          return;
        }
        const results = await fetchIndexerResults({ query: trimmedQuery, limit });
        if (!results || results.length === 0) {
          setSelectionError(`No results returned for ${friendlyLabel}.`);
          return;
        }

        console.log(
          '🔍 RAW RESULTS from search:',
          results.map((r, idx) => ({
            index: idx,
            title: r.title,
            serviceType: r.serviceType,
            indexer: r.indexer,
            titleId: r.attributes?.titleId,
            titleName: r.attributes?.titleName,
          })),
        );

        // Filter results to match the current show by titleId or title name
        const filteredResults = (() => {
          // If we have a titleId (series identifier), filter by it
          // Only filter OUT results that have a titleId but it doesn't match
          // Keep results without titleId (e.g., usenet results)
          if (titleId || imdbId) {
            const seriesIdWithoutEpisode = titleId ? titleId.replace(/:S\d{2}E\d{2}$/i, '') : '';
            console.log(`🔍 Filtering by IDs: titleId="${seriesIdWithoutEpisode}", imdbId="${imdbId || 'none'}"`);
            const matchingResults = results.filter((result) => {
              const resultTitleId = result.attributes?.titleId;
              // If no titleId attribute, keep the result (usenet results don't have this)
              if (!resultTitleId) {
                return true;
              }
              // Compare titleId without episode suffix
              const resultIdWithoutEpisode = resultTitleId.replace(/:S\d{2}E\d{2}$/i, '');

              // Check if it matches our titleId (e.g., TVDB format)
              if (seriesIdWithoutEpisode && resultIdWithoutEpisode === seriesIdWithoutEpisode) {
                return true;
              }

              // Also check if it matches our imdbId (for debrid results that use IMDB IDs)
              if (imdbId && resultIdWithoutEpisode === imdbId) {
                return true;
              }

              return false;
            });

            // If we actually filtered something out, use the filtered results
            if (matchingResults.length > 0 && matchingResults.length < results.length) {
              console.log(`✅ Filtered ${results.length} results to ${matchingResults.length} by titleId/imdbId match`);
              return matchingResults;
            }
          }

          // Fallback: filter by title name similarity
          // Only filter OUT results that have a titleName but it doesn't match
          // Keep results without titleName (e.g., usenet results)
          const searchTitle = title.trim().toLowerCase();
          if (searchTitle) {
            const matchingResults = results.filter((result) => {
              const resultTitleName = result.attributes?.titleName;
              // If no titleName attribute, keep the result (usenet results don't have this)
              if (!resultTitleName) {
                return true;
              }
              const resultNameLower = resultTitleName.trim().toLowerCase();
              // Only keep if the title matches
              return (
                resultNameLower === searchTitle ||
                resultNameLower.includes(searchTitle) ||
                searchTitle.includes(resultNameLower)
              );
            });

            if (matchingResults.length > 0 && matchingResults.length < results.length) {
              console.log(`✅ Filtered ${results.length} results to ${matchingResults.length} by title name match`);
              return matchingResults;
            }
          }

          // If no filtering worked, log warning and return all results
          console.warn(`⚠️ Could not filter results by titleId or title name, using all ${results.length} results`);
          return results;
        })();

        if (filteredResults.length === 0) {
          setSelectionError(`No matching results found for ${friendlyLabel}.`);
          return;
        }

        console.log(
          '🔍 FILTERED RESULTS (after titleId/titleName filtering):',
          filteredResults.map((r, idx) => ({
            index: idx,
            title: r.title,
            serviceType: r.serviceType,
            indexer: r.indexer,
            titleId: r.attributes?.titleId,
            titleName: r.attributes?.titleName,
          })),
        );

        // Filter out releases that have been marked as unplayable
        // Uses exact matching on release filename (without extension)
        const unplayableReleases = await getUnplayableReleases();
        const playableResults = filteredResults.filter((result) => {
          if (!result.title) return true;
          // Normalize: lowercase, trim, remove file extension
          const normalizedTitle = result.title
            .toLowerCase()
            .trim()
            .replace(/\.(mkv|mp4|avi|m4v|webm|ts)$/i, '');
          const isUnplayable = unplayableReleases.some((u) => {
            if (!u.title) return false;
            // Normalize stored title the same way
            const storedTitle = u.title
              .toLowerCase()
              .trim()
              .replace(/\.(mkv|mp4|avi|m4v|webm|ts)$/i, '');
            // Exact match only - release filenames are specific enough
            return normalizedTitle === storedTitle;
          });
          if (isUnplayable) {
            console.log(`🚫 Skipping unplayable release: "${result.title}"`);
          }
          return !isUnplayable;
        });

        if (playableResults.length === 0 && filteredResults.length > 0) {
          setSelectionError(`All matching releases for ${friendlyLabel} have been marked as unplayable.`);
          return;
        }

        // Use filtered results in their original order (as returned from the backend)
        // to match the order shown in manual selection
        const prioritizedResults = playableResults;

        console.log(
          '🎯 ATTEMPTING PLAYBACK with these results in order:',
          prioritizedResults.map((r, idx) => ({
            index: idx,
            title: r.title,
            serviceType: r.serviceType,
            indexer: r.indexer,
          })),
        );
        const playbackHandler = initiatePlaybackRef.current;
        if (!playbackHandler) {
          console.error('⚠️ Playback handler unavailable when attempting to resolve search result.');
          setSelectionError(`Unable to start playback for ${friendlyLabel}.`);
          return;
        }

        let lastHealthFailure = false;
        let lastHealthFailureReason: string | null = null;
        for (let index = 0; index < prioritizedResults.length; index += 1) {
          // Check if aborted during iteration
          if (abortController.signal.aborted) {
            console.log('🚫 Playback was cancelled during resolution');
            return;
          }

          const candidate = prioritizedResults[index];
          console.log(
            `🎬 [${index + 1}/${prioritizedResults.length}] Trying: "${candidate.title}" (${candidate.serviceType}) from ${candidate.indexer}`,
          );
          try {
            await playbackHandler(candidate, abortController.signal, { useDebugPlayer });

            // Check if aborted after successful playback initiation
            if (abortController.signal.aborted) {
              console.log('🚫 Playback was cancelled after initiation');
              return;
            }

            console.log(
              `✅ [${index + 1}/${prioritizedResults.length}] SUCCESS! Playback initiated for "${candidate.title}"`,
            );
            // Clear the abort controller since playback was successful
            if (abortControllerRef.current === abortController) {
              abortControllerRef.current = null;
            }
            return;
          } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            const healthFailure = isHealthFailureError(err);
            const healthReason = healthFailure ? getHealthFailureReason(err) : null;

            console.log(
              `❌ [${index + 1}/${prioritizedResults.length}] FAILED: "${candidate.title}" - Error: ${message} - IsHealthFailure: ${healthFailure} - Reason: ${healthReason || 'none'}`,
            );

            if (healthFailure) {
              lastHealthFailure = true;
              if (healthReason) {
                lastHealthFailureReason = healthReason;
              }
              const nextIndex = index + 1;
              const moreCandidatesRemain = nextIndex < prioritizedResults.length;
              const candidateLabel = candidate.title?.trim() || candidate.guid || 'selected release';
              const indexerLabel = candidate.indexer?.trim() || 'the indexer';

              console.warn('⚠️ Health check failed for auto-selected release; evaluating fallback.', {
                candidateLabel,
                indexer: candidate.indexer,
                error: message,
              });

              if (moreCandidatesRemain) {
                console.log(
                  `⏭️ Health failure, continuing to next candidate (${prioritizedResults.length - nextIndex} remaining)`,
                );
                const failurePrefix = healthReason ? `Health check reported ${healthReason}` : 'Health check failed';
                setSelectionInfo(
                  `${failurePrefix} for "${candidateLabel}" from ${indexerLabel}. Trying another release…`,
                );
                setSelectionError(null);
                continue;
              }

              console.log(`🛑 All ${prioritizedResults.length} candidates failed health checks. Stopping.`);
              const failureSummary = lastHealthFailureReason
                ? `All automatic releases failed health checks (last issue: ${lastHealthFailureReason}). Try manual selection or pick another release.`
                : 'All automatic releases failed health checks. Try manual selection or pick another release.';
              setSelectionError(failureSummary);
              setSelectionInfo(null);
              return;
            }

            console.log(`🛑 Non-health failure error, stopping attempts.`);
            setSelectionInfo(null);
            setSelectionError(message || `Unable to start playback for ${friendlyLabel}.`);
            return;
          }
        }

        if (lastHealthFailure) {
          const failureSummary = lastHealthFailureReason
            ? `All automatic releases failed health checks (last issue: ${lastHealthFailureReason}). Try manual selection or pick another release.`
            : 'All automatic releases failed health checks. Try manual selection or pick another release.';
          setSelectionError(failureSummary);
        } else {
          setSelectionError(`Unable to start playback for ${friendlyLabel}.`);
        }
        setSelectionInfo(null);
      } catch (err) {
        // Don't show error if the operation was aborted
        const isAbortError = err instanceof Error && (err.name === 'AbortError' || err.message?.includes('aborted'));
        if (isAbortError) {
          console.log('🚫 Playback resolution was aborted');
          setSelectionInfo(null);
          setSelectionError(null);
          return;
        }

        const message = err instanceof Error ? err.message : `Failed to resolve search result for ${friendlyLabel}.`;
        console.error(`⚠️ Search result resolve failed for ${friendlyLabel}:`, err);
        setSelectionError(message);
        setSelectionInfo(null);
      } finally {
        // Only clear isResolving if this controller is still the active one
        if (abortControllerRef.current === abortController || !abortController.signal.aborted) {
          setIsResolving(false);
        }
        // Clean up the abort controller reference
        if (abortControllerRef.current === abortController) {
          abortControllerRef.current = null;
        }
      }
    },
    [fetchIndexerResults, isResolving, title, titleId, imdbId, launchFromPrequeue],
  );

  const handlePlaySeason = useCallback(
    async (season: SeriesSeason) => {
      const baseTitle = title.trim() || title;
      const query = buildSeasonQuery(baseTitle, season.number);
      if (!query) {
        setSelectionError('Unable to build a season search query.');
        return;
      }

      const friendlyLabel = `${baseTitle} Season ${season.number}`;
      const selectionMessage = `${baseTitle} • Season ${padNumber(season.number)}`;
      await resolveAndPlay({ query, friendlyLabel, limit: 50, selectionMessage });
    },
    [resolveAndPlay, title],
  );

  const handleSeasonSelect = useCallback(
    (season: SeriesSeason, shouldAutoplay: boolean) => {
      setSelectedSeason(season);
      if (shouldAutoplay) {
        void handlePlaySeason(season);
      }
    },
    [handlePlaySeason],
  );

  const recordEpisodePlayback = useCallback(
    (episode: SeriesEpisode) => {
      if (!isSeries || !seriesIdentifier) {
        return;
      }

      const nextEpisode = findNextEpisode(episode);

      // Build external IDs map
      const externalIds: Record<string, string> = {};
      if (imdbId) externalIds.imdb = imdbId;
      if (tmdbId) externalIds.tmdb = tmdbId;
      if (tvdbId) externalIds.tvdb = tvdbId;

      const payload: EpisodeWatchPayload = {
        seriesId: seriesIdentifier,
        seriesTitle: title,
        posterUrl: posterUrl || undefined,
        backdropUrl: backdropUrl || undefined,
        year: yearNumber,
        externalIds: Object.keys(externalIds).length > 0 ? externalIds : undefined,
        episode: toEpisodeReference(episode),
        nextEpisode: nextEpisode ? toEpisodeReference(nextEpisode) : undefined,
      };

      recordEpisodeWatch(payload).catch((err) => {
        console.warn('⚠️ Unable to record watch history:', err);
      });
    },
    [
      backdropUrl,
      findNextEpisode,
      imdbId,
      isSeries,
      posterUrl,
      recordEpisodeWatch,
      seriesIdentifier,
      title,
      tmdbId,
      toEpisodeReference,
      tvdbId,
      yearNumber,
    ],
  );

  const handleEpisodeFocus = useCallback((episode: SeriesEpisode) => {
    setActiveEpisode(episode);
  }, []);

  const handleRequestFocusShift = useCallback(() => {
    const navigator = spatialNavigatorRef.current;
    if (!isTouchSeasonLayout && navigator && typeof navigator.grabFocus === 'function') {
      try {
        navigator.grabFocus('watch-now');
      } catch (error) {
        console.debug('Unable to shift focus to watch-now button:', error);
      }
    }
  }, [isTouchSeasonLayout]);

  const handleEpisodeSelect = useCallback((episode: SeriesEpisode) => {
    setActiveEpisode(episode);
    setSelectionError(null);
    setSelectionInfo(null);
    // Clear any resume position from previous episode
    setCurrentProgress(null);
  }, []);

  const handlePreviousEpisode = useCallback(() => {
    if (!activeEpisode) return;
    const previousEp = findPreviousEpisode(activeEpisode);
    if (previousEp) {
      handleEpisodeSelect(previousEp);
    }
  }, [activeEpisode, findPreviousEpisode, handleEpisodeSelect]);

  const handleNextEpisode = useCallback(() => {
    if (!activeEpisode) return;
    const nextEp = findNextEpisode(activeEpisode);
    if (nextEp) {
      handleEpisodeSelect(nextEp);
    }
  }, [activeEpisode, findNextEpisode, handleEpisodeSelect]);

  const handleEpisodeStripFocus = useCallback(() => {
    console.log('[Details NAV DEBUG] Episode strip FOCUSED');
    setIsEpisodeStripFocused(true);
  }, []);

  const handleEpisodeStripBlur = useCallback(() => {
    console.log('[Details NAV DEBUG] Episode strip BLURRED');
    setIsEpisodeStripFocused(false);
  }, []);

  const onDirectionHandledWithoutMovement = useCallback(
    (direction: string) => {
      console.log(
        '[Details NAV DEBUG] Direction without movement:',
        direction,
        'isEpisodeStripFocused:',
        isEpisodeStripFocused,
      );
      if (isEpisodeStripFocused && activeEpisode) {
        if (direction === 'right') {
          const nextEp = findNextEpisode(activeEpisode);
          if (nextEp) {
            handleEpisodeSelect(nextEp);
          }
        } else if (direction === 'left') {
          const previousEp = findPreviousEpisode(activeEpisode);
          if (previousEp) {
            handleEpisodeSelect(previousEp);
          }
        }
      }
    },
    [isEpisodeStripFocused, activeEpisode, findNextEpisode, findPreviousEpisode, handleEpisodeSelect],
  );

  // Keep the ref in sync with the callback
  useEffect(() => {
    handleEpisodeSelectRef.current = handleEpisodeSelect;
  }, [handleEpisodeSelect]);

  // Select/play next episode when episodes are loaded and we have a next episode to show
  useEffect(() => {
    if (nextEpisodeFromPlayback && allEpisodes.length > 0) {
      const matchingEpisode = allEpisodes.find(
        (ep) =>
          ep.seasonNumber === nextEpisodeFromPlayback.seasonNumber &&
          ep.episodeNumber === nextEpisodeFromPlayback.episodeNumber,
      );
      if (matchingEpisode) {
        if (nextEpisodeFromPlayback.autoPlay && handlePlayEpisodeRef.current) {
          console.log('[Details] Auto-playing next episode after episodes loaded:', matchingEpisode);
          handlePlayEpisodeRef.current(matchingEpisode);
        } else {
          console.log('[Details] Auto-selecting next episode after episodes loaded:', matchingEpisode);
          handleEpisodeSelect(matchingEpisode);
        }
        // Clear the next episode state after applying it
        setNextEpisodeFromPlayback(null);
      }
    }
  }, [nextEpisodeFromPlayback, allEpisodes, handleEpisodeSelect]);

  const handlePlayEpisode = useCallback(
    async (episode: SeriesEpisode) => {
      setActiveEpisode(episode);
      // Clear any resume position from previous episode
      setCurrentProgress(null);
      const baseTitle = title.trim() || title;
      const query = buildEpisodeQuery(baseTitle, episode.seasonNumber, episode.episodeNumber);
      if (!query) {
        setSelectionError('Unable to build an episode search query.');
        return;
      }

      const episodeCode = `S${padNumber(episode.seasonNumber)}E${padNumber(episode.episodeNumber)}`;
      const friendlyLabel = `${baseTitle} ${episodeCode}${episode.name ? ` – "${episode.name}"` : ''}`;
      const selectionMessage = `${baseTitle} • ${episodeCode}`;
      await resolveAndPlay({
        query,
        friendlyLabel,
        limit: 50,
        selectionMessage,
        targetEpisode: { seasonNumber: episode.seasonNumber, episodeNumber: episode.episodeNumber },
      });
      // Don't automatically record episode playback - let progress tracking handle it
      // recordEpisodePlayback(episode);
    },
    [recordEpisodePlayback, resolveAndPlay, title],
  );

  // Keep the play episode ref in sync with the callback
  useEffect(() => {
    handlePlayEpisodeRef.current = handlePlayEpisode;
  }, [handlePlayEpisode]);

  // Shuffle play - pick a random episode and play it
  const handleShufflePlay = useCallback(() => {
    if (allEpisodes.length === 0) return;
    const randomIndex = Math.floor(Math.random() * allEpisodes.length);
    const randomEpisode = allEpisodes[randomIndex];
    setIsShuffleMode(true);
    handlePlayEpisode(randomEpisode);
  }, [allEpisodes, handlePlayEpisode]);

  const getItemIdForProgress = useCallback((): string | null => {
    // Use activeEpisode (user-selected) if available, otherwise fall back to nextUpEpisode
    const episodeToCheck = activeEpisode || nextUpEpisode;

    if (episodeToCheck && seriesIdentifier) {
      // For episodes, construct the itemId
      return `${seriesIdentifier}:S${String(episodeToCheck.seasonNumber).padStart(2, '0')}E${String(episodeToCheck.episodeNumber).padStart(2, '0')}`;
    }

    if (!isSeries && titleId) {
      // For movies, use the titleId
      return titleId;
    }

    return null;
  }, [nextUpEpisode, activeEpisode, seriesIdentifier, isSeries, titleId]);

  // Helper to show loading screen immediately when playback starts
  const showLoadingScreenIfEnabled = useCallback(async () => {
    const isLoadingScreenEnabled =
      userSettings?.playback?.useLoadingScreen ?? settings?.playback?.useLoadingScreen ?? false;
    if (isLoadingScreenEnabled) {
      setShowBlackOverlay(true);
      await new Promise((resolve) => setTimeout(resolve, 50));
      showLoadingScreen();
    }
  }, [userSettings?.playback?.useLoadingScreen, settings?.playback?.useLoadingScreen, showLoadingScreen]);

  const checkAndShowResumeModal = useCallback(
    async (action: () => Promise<void>) => {
      console.log('🔍 checkAndShowResumeModal called', { activeUserId });

      if (!activeUserId) {
        // No user, just play - show loading screen immediately
        console.log('🔍 No active user, playing immediately');
        await showLoadingScreenIfEnabled();
        await action();
        return;
      }

      const itemId = getItemIdForProgress();
      console.log('🔍 Item ID for progress:', itemId);

      if (!itemId) {
        // No item ID, just play - show loading screen immediately
        console.log('🔍 No item ID, playing immediately');
        await showLoadingScreenIfEnabled();
        await action();
        return;
      }

      try {
        const mediaType = isSeries || activeEpisode || nextUpEpisode ? 'episode' : 'movie';
        console.log('🔍 Fetching progress for:', { mediaType, itemId });
        const progress = await apiService.getPlaybackProgress(activeUserId, mediaType, itemId);
        console.log('🔍 Progress result:', progress);

        if (progress && progress.percentWatched > 5 && progress.percentWatched < 95) {
          // Show resume modal - loading screen will be shown after user makes choice
          console.log('🎬 Showing resume modal with progress:', progress.percentWatched);
          setCurrentProgress(progress);
          // Wrap action to accept startOffset parameter for resume
          setPendingPlaybackAction(() => async (startOffset?: number) => {
            // Store the startOffset in a ref that handleInitiatePlayback can access
            // Note: Don't clear it here - handleInitiatePlayback will consume and clear it
            // This allows manual selection flow to preserve the resume position
            if (startOffset !== undefined) {
              pendingStartOffsetRef.current = startOffset;
            }
            await action();
          });
          setResumeModalVisible(true);
        } else {
          // No meaningful progress, just play - show loading screen immediately
          console.log('🔍 No meaningful progress, playing immediately. Progress:', progress?.percentWatched);
          await showLoadingScreenIfEnabled();
          await action();
        }
      } catch (error) {
        console.warn('Failed to check playback progress:', error);
        // On error, just play - show loading screen immediately
        await showLoadingScreenIfEnabled();
        await action();
      }
    },
    [activeUserId, getItemIdForProgress, isSeries, activeEpisode, nextUpEpisode, showLoadingScreenIfEnabled],
  );

  const handleResumePlayback = useCallback(async () => {
    if (!pendingPlaybackAction || !currentProgress) {
      return;
    }

    // Close the modal immediately
    setResumeModalVisible(false);

    // Show loading screen immediately after user confirms resume
    await showLoadingScreenIfEnabled();

    // Pass the startOffset from currentProgress to the pending action
    await pendingPlaybackAction(currentProgress.position);

    // Clear state after playback starts
    setPendingPlaybackAction(null);
    setCurrentProgress(null);
  }, [pendingPlaybackAction, currentProgress, showLoadingScreenIfEnabled]);

  const handlePlayFromBeginning = useCallback(async () => {
    if (!pendingPlaybackAction) {
      return;
    }

    // Close the modal immediately
    setResumeModalVisible(false);

    // Show loading screen immediately after user confirms play from beginning
    await showLoadingScreenIfEnabled();

    // Clear the progress flag so we start from beginning
    setCurrentProgress(null);
    await pendingPlaybackAction();

    // Clear state after playback starts
    setPendingPlaybackAction(null);
  }, [pendingPlaybackAction, showLoadingScreenIfEnabled]);

  const handleWatchNow = useCallback(async () => {
    const playAction = async () => {
      // Use activeEpisode (user-selected) if available, otherwise fall back to nextUpEpisode
      // This matches the prequeue priority order
      const episodeToPlay = activeEpisode || nextUpEpisode;

      if (episodeToPlay) {
        const context = getEpisodeSearchContext(episodeToPlay);
        if (!context) {
          setSelectionError('Unable to build an episode search query.');
          return;
        }

        console.log('⚡ Auto-select: resolving first viable result for episode', context.episodeCode);
        await resolveAndPlay({
          query: context.query,
          friendlyLabel: context.friendlyLabel,
          limit: 50,
          selectionMessage: context.selectionMessage,
        });
        // Don't automatically record episode playback - let progress tracking handle it
        // recordEpisodePlayback(episodeToPlay);
        return;
      }

      const baseTitle = title.trim();
      console.log('⚡ Auto-select: resolving first viable result', baseTitle ? `for ${baseTitle}` : '');
      await resolveAndPlay({
        query: baseTitle || title,
        friendlyLabel: baseTitle ? `"${baseTitle}"` : 'this title',
        limit: 50,
        selectionMessage: null,
      });
    };

    await checkAndShowResumeModal(playAction);
  }, [
    activeEpisode,
    nextUpEpisode,
    getEpisodeSearchContext,
    recordEpisodePlayback,
    resolveAndPlay,
    title,
    checkAndShowResumeModal,
  ]);

  const handleLaunchDebugPlayer = useCallback(async () => {
    const playAction = async () => {
      // Use activeEpisode (user-selected) if available, otherwise fall back to nextUpEpisode
      // This matches the prequeue priority order
      const episodeToPlay = activeEpisode || nextUpEpisode;

      if (episodeToPlay) {
        const context = getEpisodeSearchContext(episodeToPlay);
        if (!context) {
          setSelectionError('Unable to build an episode search query.');
          return;
        }

        console.log('🛠️ Debug Player: resolving first viable result for episode', context.episodeCode);
        await resolveAndPlay({
          query: context.query,
          friendlyLabel: context.friendlyLabel,
          limit: 50,
          selectionMessage: context.selectionMessage,
          useDebugPlayer: true,
        });
        return;
      }

      const baseTitle = title.trim();
      console.log('🛠️ Debug Player: resolving first viable result', baseTitle ? `for ${baseTitle}` : '');
      await resolveAndPlay({
        query: baseTitle || title,
        friendlyLabel: baseTitle ? `"${baseTitle}"` : 'this title',
        limit: 50,
        selectionMessage: null,
        useDebugPlayer: true,
      });
    };

    await checkAndShowResumeModal(playAction);
  }, [activeEpisode, nextUpEpisode, getEpisodeSearchContext, resolveAndPlay, title, checkAndShowResumeModal]);

  const closeManualPicker = useCallback(() => {
    setManualVisible(false);
    setManualError(null);
    setManualLoading(false);
  }, []);

  const handleManualSelection = useCallback(
    async (result: NZBResult) => {
      if (!result) {
        return;
      }

      // Cancel any pending playback
      if (abortControllerRef.current) {
        console.log('🚫 Cancelling previous playback for manual selection');
        abortControllerRef.current.abort();
      }

      // Create new abort controller for manual selection
      const abortController = new AbortController();
      abortControllerRef.current = abortController;

      setManualVisible(false);
      setManualError(null);
      setSelectionError(null);
      setIsResolving(true);

      // Define the playback action to be wrapped in resume check
      const playAction = async () => {
        // Show loading screen now that user has confirmed playback
        await showLoadingScreenIfEnabled();

        try {
          await handleInitiatePlayback(result, abortController.signal);

          // Check if aborted after playback
          if (abortController.signal.aborted) {
            console.log('🚫 Manual playback was cancelled');
            return;
          }

          // Clear the abort controller since playback was successful
          if (abortControllerRef.current === abortController) {
            abortControllerRef.current = null;
          }
        } catch (err) {
          // Don't show error if the operation was aborted
          const isAbortError = err instanceof Error && (err.name === 'AbortError' || err.message?.includes('aborted'));
          if (isAbortError) {
            console.log('🚫 Manual playback was aborted');
            setSelectionInfo(null);
            setSelectionError(null);
            return;
          }

          // Handle playback errors
          console.error('⚠️ Manual playback failed:', err);
          const message = err instanceof Error ? err.message : 'Playback failed';
          setSelectionError(message);
          setSelectionInfo(null);
          // Clear loading screen and black overlay on error
          hideLoadingScreen();
          setShowBlackOverlay(false);
        } finally {
          setIsResolving(false);
        }
      };

      // Check for resume progress before initiating playback
      await checkAndShowResumeModal(playAction);
    },
    [checkAndShowResumeModal, handleInitiatePlayback, hideLoadingScreen, showLoadingScreenIfEnabled],
  );

  const handleToggleWatchlist = useCallback(async () => {
    if (!canToggleWatchlist || watchlistBusy) {
      return;
    }
    setWatchlistError(null);
    setWatchlistBusy(true);
    try {
      if (isWatchlisted) {
        await removeFromWatchlist(mediaType, titleId);
      } else {
        await addToWatchlist({
          id: titleId,
          mediaType,
          name: title,
          overview: description,
          year: yearNumber,
          posterUrl,
          backdropUrl,
          externalIds,
        });
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unable to update watchlist.';
      setWatchlistError(message);
      console.error('⚠️ Watchlist update failed:', err);
    } finally {
      setWatchlistBusy(false);
    }
  }, [
    addToWatchlist,
    backdropUrl,
    canToggleWatchlist,
    description,
    externalIds,
    isWatchlisted,
    mediaType,
    posterUrl,
    removeFromWatchlist,
    title,
    titleId,
    watchlistBusy,
    yearNumber,
  ]);

  const handleToggleWatched = useCallback(async () => {
    if (!canToggleWatchlist || watchlistBusy) {
      return;
    }

    // For series, show bulk watch options modal
    if (isSeries) {
      setBulkWatchModalVisible(true);
      return;
    }

    // For movies, toggle directly
    setWatchlistError(null);
    setWatchlistBusy(true);
    try {
      await toggleWatchStatus(mediaType, titleId, {
        name: title,
        year: yearNumber,
        externalIds: externalIds,
      });
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unable to update watched state.';
      setWatchlistError(message);
      console.error('⚠️ Unable to update watched state:', err);
    } finally {
      setWatchlistBusy(false);
    }
  }, [
    canToggleWatchlist,
    externalIds,
    isSeries,
    mediaType,
    title,
    titleId,
    toggleWatchStatus,
    watchlistBusy,
    yearNumber,
  ]);

  const handleWatchTrailer = useCallback(() => {
    const nextTrailer = primaryTrailer ?? trailers[0];
    if (!nextTrailer) {
      if (!trailersLoading) {
        setTrailersError((prev) => prev ?? 'Trailer not available yet.');
      }
      return;
    }
    setActiveTrailer(nextTrailer);
    setTrailerModalVisible(true);
  }, [primaryTrailer, trailers, trailersLoading]);

  const handleCloseTrailer = useCallback(() => {
    setTrailerModalVisible(false);
    setActiveTrailer(null);
  }, []);

  const handleCloseResumeModal = useCallback(() => {
    setResumeModalVisible(false);
    // Clear resume state when modal is closed without action
    setCurrentProgress(null);
    setPendingPlaybackAction(null);
  }, []);

  const handleManualSelect = useCallback(async () => {
    if (manualLoading) {
      return;
    }

    // Use activeEpisode (user-selected) if available, otherwise fall back to nextUpEpisode
    const episodeToSelect = activeEpisode || nextUpEpisode;
    const context = episodeToSelect ? getEpisodeSearchContext(episodeToSelect) : null;
    console.log('🛠️ Manual selection: fetching indexer results', context ? `for ${context.episodeCode}` : '');
    // Don't show loading overlay here - it should appear after user selects a release
    setManualVisible(true);
    setManualError(null);
    setManualResults([]);
    setSelectionInfo(context?.selectionMessage ?? null);
    setSelectionError(null);
    setManualLoading(true);
    try {
      const results = await fetchIndexerResults({ limit: 50, query: context?.query });
      setManualResults(results);
      if (!results || results.length === 0) {
        setManualError('No results available yet for manual selection.');
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to load results.';
      console.error('⚠️ Manual fetch failed:', err);
      setManualError(message);
    } finally {
      setManualLoading(false);
    }
  }, [activeEpisode, nextUpEpisode, fetchIndexerResults, getEpisodeSearchContext, manualLoading]);

  const handleEpisodeLongPress = useCallback(
    async (episode: SeriesEpisode) => {
      // Set the episode as active first
      setActiveEpisode(episode);
      setSelectionError(null);
      setSelectionInfo(null);

      // Then trigger manual selection with a slight delay to ensure state updates
      await new Promise((resolve) => setTimeout(resolve, 0));

      if (manualLoading) {
        return;
      }

      const context = getEpisodeSearchContext(episode);
      if (!context) {
        setSelectionError('Unable to build an episode search query.');
        return;
      }

      console.log('🛠️ Manual selection: fetching indexer results for', context.episodeCode);
      setManualVisible(true);
      setManualError(null);
      setManualResults([]);
      setSelectionInfo(context.selectionMessage);
      setSelectionError(null);
      setManualLoading(true);
      try {
        const results = await fetchIndexerResults({ limit: 50, query: context.query });
        setManualResults(results);
        if (!results || results.length === 0) {
          setManualError('No results available yet for manual selection.');
        }
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Failed to load results.';
        console.error('⚠️ Manual fetch failed:', err);
        setManualError(message);
      } finally {
        setManualLoading(false);
      }
    },
    [fetchIndexerResults, getEpisodeSearchContext, manualLoading],
  );

  const { healthChecks: manualHealthChecks, checkHealth: checkManualHealth } = useManualHealthChecks(manualResults);
  const seriesFocusHandlerRef = useRef<(() => boolean) | null>(null);
  const _handleSeriesBlockFocus = useCallback(() => {
    seriesFocusHandlerRef.current?.();
  }, []);

  const handleRegisterSeasonFocusHandler = useCallback((handler: (() => boolean) | null) => {
    seriesFocusHandlerRef.current = handler;
    setHasSeriesFocusTarget(Boolean(handler));
  }, []);

  const handleEpisodesLoaded = useCallback((episodes: SeriesEpisode[]) => {
    setAllEpisodes(episodes);
    allEpisodesRef.current = episodes;
    setEpisodesLoading(false);
  }, []);

  const handleSeasonsLoaded = useCallback((loadedSeasons: SeriesSeason[]) => {
    setSeasons(loadedSeasons);
  }, []);

  const handleMarkAllWatched = useCallback(async () => {
    if (!seriesIdentifier || allEpisodes.length === 0) {
      return;
    }

    setWatchlistBusy(true);
    setWatchlistError(null);

    try {
      const updates = allEpisodes.map((episode) => ({
        mediaType: 'episode',
        itemId: `${seriesIdentifier}:s${String(episode.seasonNumber).padStart(2, '0')}e${String(episode.episodeNumber).padStart(2, '0')}`,
        name: episode.name,
        watched: true,
        seasonNumber: episode.seasonNumber,
        episodeNumber: episode.episodeNumber,
        seriesId: seriesIdentifier,
        seriesName: title,
      }));

      await bulkUpdateWatchStatus(updates);
      await refreshWatchStatus();

      // Auto-select the first episode of the show
      const firstEp = findFirstEpisode();
      if (firstEp) {
        console.log('[Details] Auto-selecting first episode after marking all as watched:', firstEp);
        handleEpisodeSelect(firstEp);
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unable to mark all episodes as watched.';
      setWatchlistError(message);
      console.error('⚠️ Unable to mark all episodes as watched:', err);
    } finally {
      setWatchlistBusy(false);
    }
  }, [
    seriesIdentifier,
    allEpisodes,
    title,
    bulkUpdateWatchStatus,
    refreshWatchStatus,
    findFirstEpisode,
    handleEpisodeSelect,
  ]);

  const handleMarkAllUnwatched = useCallback(async () => {
    if (!seriesIdentifier || allEpisodes.length === 0) {
      return;
    }

    setWatchlistBusy(true);
    setWatchlistError(null);

    try {
      const updates = allEpisodes.map((episode) => ({
        mediaType: 'episode',
        itemId: `${seriesIdentifier}:s${String(episode.seasonNumber).padStart(2, '0')}e${String(episode.episodeNumber).padStart(2, '0')}`,
        name: episode.name,
        watched: false,
        seasonNumber: episode.seasonNumber,
        episodeNumber: episode.episodeNumber,
        seriesId: seriesIdentifier,
        seriesName: title,
      }));

      await bulkUpdateWatchStatus(updates);
      await refreshWatchStatus();
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unable to mark all episodes as unwatched.';
      setWatchlistError(message);
      console.error('⚠️ Unable to mark all episodes as unwatched:', err);
    } finally {
      setWatchlistBusy(false);
    }
  }, [seriesIdentifier, allEpisodes, title, bulkUpdateWatchStatus, refreshWatchStatus]);

  const handleMarkSeasonWatched = useCallback(
    async (season: SeriesSeason) => {
      if (!seriesIdentifier) {
        return;
      }

      setWatchlistBusy(true);
      setWatchlistError(null);

      try {
        const updates = season.episodes.map((episode) => ({
          mediaType: 'episode',
          itemId: `${seriesIdentifier}:s${String(episode.seasonNumber).padStart(2, '0')}e${String(episode.episodeNumber).padStart(2, '0')}`,
          name: episode.name,
          watched: true,
          seasonNumber: episode.seasonNumber,
          episodeNumber: episode.episodeNumber,
          seriesId: seriesIdentifier,
          seriesName: title,
        }));

        await bulkUpdateWatchStatus(updates);
        await refreshWatchStatus();

        // Auto-select the first episode of the next season if available
        const firstEpisodeOfNextSeason = findFirstEpisodeOfNextSeason(season.number);
        if (firstEpisodeOfNextSeason) {
          console.log(
            '[Details] Auto-selecting first episode of next season after marking season as watched:',
            firstEpisodeOfNextSeason,
          );
          handleEpisodeSelect(firstEpisodeOfNextSeason);
        }
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Unable to mark season as watched.';
        setWatchlistError(message);
        console.error('⚠️ Unable to mark season as watched:', err);
      } finally {
        setWatchlistBusy(false);
      }
    },
    [
      seriesIdentifier,
      title,
      bulkUpdateWatchStatus,
      refreshWatchStatus,
      findFirstEpisodeOfNextSeason,
      handleEpisodeSelect,
    ],
  );

  const handleMarkSeasonUnwatched = useCallback(
    async (season: SeriesSeason) => {
      if (!seriesIdentifier) {
        return;
      }

      setWatchlistBusy(true);
      setWatchlistError(null);

      try {
        const updates = season.episodes.map((episode) => ({
          mediaType: 'episode',
          itemId: `${seriesIdentifier}:s${String(episode.seasonNumber).padStart(2, '0')}e${String(episode.episodeNumber).padStart(2, '0')}`,
          name: episode.name,
          watched: false,
          seasonNumber: episode.seasonNumber,
          episodeNumber: episode.episodeNumber,
          seriesId: seriesIdentifier,
          seriesName: title,
        }));

        await bulkUpdateWatchStatus(updates);
        await refreshWatchStatus();
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Unable to mark season as unwatched.';
        setWatchlistError(message);
        console.error('⚠️ Unable to mark season as unwatched:', err);
      } finally {
        setWatchlistBusy(false);
      }
    },
    [seriesIdentifier, title, bulkUpdateWatchStatus, refreshWatchStatus],
  );

  const handleToggleEpisodeWatched = useCallback(
    async (episode: SeriesEpisode) => {
      if (!seriesIdentifier) {
        return;
      }

      try {
        const episodeId = `${seriesIdentifier}:s${String(episode.seasonNumber).padStart(2, '0')}e${String(episode.episodeNumber).padStart(2, '0')}`;

        // Build external IDs for the episode
        const episodeExternalIds: Record<string, string> = {};
        if (imdbId) episodeExternalIds.imdb = imdbId;
        if (tmdbId) episodeExternalIds.tmdb = tmdbId;
        if (tvdbId) episodeExternalIds.tvdb = tvdbId;
        if (titleId) episodeExternalIds.titleId = titleId;

        await toggleWatchStatus('episode', episodeId, {
          name: episode.name,
          year: yearNumber,
          externalIds: Object.keys(episodeExternalIds).length ? episodeExternalIds : undefined,
          seasonNumber: episode.seasonNumber,
          episodeNumber: episode.episodeNumber,
          seriesId: seriesIdentifier,
          seriesName: title,
        });
      } catch (err) {
        console.error('⚠️ Unable to toggle episode watch status:', err);
      }
    },
    [seriesIdentifier, toggleWatchStatus, yearNumber, imdbId, tmdbId, tvdbId, titleId, title],
  );

  const isEpisodeWatched = useCallback(
    (episode: SeriesEpisode): boolean => {
      if (!seriesIdentifier) {
        return false;
      }
      const episodeId = `${seriesIdentifier}:s${String(episode.seasonNumber).padStart(2, '0')}e${String(episode.episodeNumber).padStart(2, '0')}`;
      return isItemWatched('episode', episodeId);
    },
    [seriesIdentifier, isItemWatched],
  );

  const handleMarkEpisodeWatched = useCallback(
    async (episode: SeriesEpisode) => {
      if (!seriesIdentifier) {
        return;
      }

      setWatchlistBusy(true);
      setWatchlistError(null);

      try {
        const updates = [
          {
            mediaType: 'episode' as const,
            itemId: `${seriesIdentifier}:s${String(episode.seasonNumber).padStart(2, '0')}e${String(episode.episodeNumber).padStart(2, '0')}`,
            name: episode.name,
            watched: true,
            seasonNumber: episode.seasonNumber,
            episodeNumber: episode.episodeNumber,
            seriesId: seriesIdentifier,
            seriesName: title,
          },
        ];

        await bulkUpdateWatchStatus(updates);
        await refreshWatchStatus();

        // Record this episode watch for continue watching tracking
        recordEpisodePlayback(episode);

        // If we just marked the active episode as watched, auto-select the next episode
        if (
          activeEpisode &&
          episode.seasonNumber === activeEpisode.seasonNumber &&
          episode.episodeNumber === activeEpisode.episodeNumber
        ) {
          const nextEp = findNextEpisode(episode);
          if (nextEp) {
            console.log('[Details] Auto-selecting next episode after marking current as watched:', nextEp);
            handleEpisodeSelect(nextEp);
          }
        }
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Unable to mark episode as watched.';
        setWatchlistError(message);
        console.error('⚠️ Unable to mark episode as watched:', err);
      } finally {
        setWatchlistBusy(false);
      }
    },
    [
      seriesIdentifier,
      title,
      bulkUpdateWatchStatus,
      refreshWatchStatus,
      recordEpisodePlayback,
      activeEpisode,
      findNextEpisode,
      handleEpisodeSelect,
    ],
  );

  const handleMarkEpisodeUnwatched = useCallback(
    async (episode: SeriesEpisode) => {
      if (!seriesIdentifier) {
        return;
      }

      setWatchlistBusy(true);
      setWatchlistError(null);

      try {
        const updates = [
          {
            mediaType: 'episode' as const,
            itemId: `${seriesIdentifier}:s${String(episode.seasonNumber).padStart(2, '0')}e${String(episode.episodeNumber).padStart(2, '0')}`,
            name: episode.name,
            watched: false,
            seasonNumber: episode.seasonNumber,
            episodeNumber: episode.episodeNumber,
            seriesId: seriesIdentifier,
            seriesName: title,
          },
        ];

        await bulkUpdateWatchStatus(updates);
        await refreshWatchStatus();
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Unable to mark episode as unwatched.';
        setWatchlistError(message);
        console.error('⚠️ Unable to mark episode as unwatched:', err);
      } finally {
        setWatchlistBusy(false);
      }
    },
    [seriesIdentifier, title, bulkUpdateWatchStatus, refreshWatchStatus],
  );

  const handleSeasonSelectorSelect = useCallback((season: SeriesSeason) => {
    setSelectedSeason(season);
    setSeasonSelectorVisible(false);
    setEpisodeSelectorVisible(true);
  }, []);

  const handleEpisodeSelectorSelect = useCallback((episode: SeriesEpisode) => {
    setActiveEpisode(episode);
    setEpisodeSelectorVisible(false);
  }, []);

  const handleEpisodeSelectorBack = useCallback(() => {
    setEpisodeSelectorVisible(false);
    setSeasonSelectorVisible(true);
  }, []);

  const renderDetailsContent = () => (
    <>
      <View style={[styles.topContent, isTV && styles.topContentTV, isMobile && styles.topContentMobile]}>
        <View style={styles.titleRow}>
          <Text style={styles.title}>{title}</Text>
        </View>
        {ratings.length > 0 && (
          <View style={styles.ratingsRow}>
            {ratings.map((rating) => {
              const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
              const config = getRatingConfig(rating.source, baseUrl, rating.value, rating.max);
              const iconSize = Math.round((isTV ? 17 : 14) * tvScale);
              return (
                <RatingBadge
                  key={rating.source}
                  rating={rating}
                  config={config}
                  iconSize={iconSize}
                  styles={styles}
                />
              );
            })}
          </View>
        )}
        {(releaseRows.length > 0 || shouldShowReleaseSkeleton || releaseErrorMessage) && (
          <View style={styles.releaseInfoRow}>
            {releaseRows.map((row) => (
              <View key={row.key} style={styles.releaseInfoItem}>
                <Text style={styles.releaseInfoLabel}>{row.label}</Text>
                <Text style={styles.releaseInfoValue}>{row.value}</Text>
              </View>
            ))}
            {shouldShowReleaseSkeleton && <Text style={styles.releaseInfoLoading}>Fetching release dates…</Text>}
            {releaseErrorMessage && <Text style={styles.releaseInfoError}>{releaseErrorMessage}</Text>}
          </View>
        )}
        {isMobile ? (
          <Pressable
            onPress={() => {
              const targetHeight = isDescriptionExpanded ? collapsedHeight : expandedHeight;
              if (targetHeight > 0) {
                descriptionHeight.value = withTiming(targetHeight, {
                  duration: 300,
                  easing: Easing.bezier(0.25, 0.1, 0.25, 1),
                });
              }
              setIsDescriptionExpanded((prev) => !prev);
            }}>
            <View>
              {/* Hidden text to measure collapsed (4-line) height */}
              <Text
                style={[styles.description, styles.descriptionHidden]}
                numberOfLines={4}
                onLayout={(e) => {
                  const height = e.nativeEvent.layout.height;
                  if (height > 0 && collapsedHeight === 0) {
                    // Add small buffer to prevent line clipping
                    const bufferedHeight = height + 4;
                    setCollapsedHeight(bufferedHeight);
                    descriptionHeight.value = bufferedHeight;
                  }
                }}>
                {description}
              </Text>
              {/* Hidden text to measure full height */}
              <Text
                style={[styles.description, styles.descriptionHidden]}
                onLayout={(e) => {
                  const height = e.nativeEvent.layout.height;
                  if (height > 0 && expandedHeight === 0) {
                    // Add small buffer to prevent line clipping
                    setExpandedHeight(height + 4);
                  }
                }}>
                {description}
              </Text>
              {/* Visible animated container */}
              <Animated.View
                style={[{ overflow: 'hidden' }, collapsedHeight > 0 ? { height: descriptionHeight } : undefined]}>
                <Text
                  style={[styles.description, { marginBottom: 0 }]}
                  numberOfLines={isDescriptionExpanded ? undefined : 4}>
                  {description}
                </Text>
              </Animated.View>
            </View>
            {expandedHeight > collapsedHeight && (
              <Text style={styles.descriptionToggle}>{isDescriptionExpanded ? 'Show less' : 'More'}</Text>
            )}
          </Pressable>
        ) : (
          <Text style={styles.description}>{description}</Text>
        )}
      </View>
      <SpatialNavigationNode
        orientation="vertical"
        focusKey="details-content-column"
        onActive={() => console.log('[Details NAV DEBUG] details-content-column ACTIVE')}
        onInactive={() => console.log('[Details NAV DEBUG] details-content-column INACTIVE')}>
        <View style={[styles.bottomContent, isMobile && styles.mobileBottomContent]}>
          {/* Always render this node on TV for series to ensure correct registration order */}
          {Platform.isTV && isSeries && (
            <SpatialNavigationNode
              orientation="horizontal"
              focusKey="episode-strip-wrapper"
              onActive={() => console.log('[Details NAV DEBUG] episode-strip-wrapper ACTIVE')}
              onInactive={() => console.log('[Details NAV DEBUG] episode-strip-wrapper INACTIVE')}>
              {activeEpisode ? (
                <TVEpisodeStrip
                  activeEpisode={activeEpisode}
                  allEpisodes={allEpisodes}
                  selectedSeason={selectedSeason}
                  percentWatched={displayProgress}
                  onSelect={handleWatchNow}
                  onFocus={handleEpisodeStripFocus}
                  onBlur={handleEpisodeStripBlur}
                />
              ) : (
                <View />
              )}
            </SpatialNavigationNode>
          )}
          <SpatialNavigationNode
            orientation="horizontal"
            focusKey="details-action-row"
            onActive={() => console.log('[Details NAV DEBUG] details-action-row ACTIVE')}
            onInactive={() => console.log('[Details NAV DEBUG] details-action-row INACTIVE')}>
            <View style={[styles.actionRow, useCompactActionLayout && styles.compactActionRow]}>
              <DefaultFocus>
                <FocusablePressable
                  focusKey="watch-now"
                  text={!useCompactActionLayout ? watchNowLabel : undefined}
                  icon={useCompactActionLayout || Platform.isTV ? 'play' : undefined}
                  accessibilityLabel={watchNowLabel}
                  onSelect={handleWatchNow}
                  disabled={isResolving || (isSeries && episodesLoading)}
                  loading={isResolving || (isSeries && episodesLoading)}
                  style={useCompactActionLayout ? styles.iconActionButton : styles.primaryActionButton}
                  showReadyPip={prequeueReady}
                />
              </DefaultFocus>
              <FocusablePressable
                focusKey="manual-select"
                text={!useCompactActionLayout ? manualSelectLabel : undefined}
                icon={useCompactActionLayout || Platform.isTV ? 'search' : undefined}
                accessibilityLabel={manualSelectLabel}
                onSelect={handleManualSelect}
                disabled={isSeries && episodesLoading}
                style={useCompactActionLayout ? styles.iconActionButton : styles.manualActionButton}
              />
              {shouldShowDebugPlayerButton && (
                <FocusablePressable
                  focusKey="debug-player"
                  text={!useCompactActionLayout ? 'Debug Player' : undefined}
                  icon={useCompactActionLayout || Platform.isTV ? 'bug' : undefined}
                  accessibilityLabel="Launch debug player overlay"
                  onSelect={handleLaunchDebugPlayer}
                  disabled={isResolving || (isSeries && episodesLoading)}
                  style={useCompactActionLayout ? styles.iconActionButton : styles.debugActionButton}
                />
              )}
              {isSeries && (
                <FocusablePressable
                  focusKey="select-episode"
                  text={!useCompactActionLayout ? 'Select' : undefined}
                  icon={useCompactActionLayout || Platform.isTV ? 'list' : undefined}
                  accessibilityLabel="Select Episode"
                  onSelect={() => setSeasonSelectorVisible(true)}
                  style={useCompactActionLayout ? styles.iconActionButton : styles.manualActionButton}
                />
              )}
              {isSeries && (
                <FocusablePressable
                  focusKey="shuffle-play"
                  text={!useCompactActionLayout ? 'Shuffle' : undefined}
                  icon={useCompactActionLayout || Platform.isTV ? 'shuffle' : undefined}
                  accessibilityLabel="Shuffle play random episode"
                  onSelect={handleShufflePlay}
                  style={useCompactActionLayout ? styles.iconActionButton : styles.manualActionButton}
                  disabled={episodesLoading || allEpisodes.length === 0}
                />
              )}
              <FocusablePressable
                focusKey="toggle-watchlist"
                text={!useCompactActionLayout ? (watchlistBusy ? 'Saving...' : watchlistButtonLabel) : undefined}
                icon={
                  useCompactActionLayout || Platform.isTV
                    ? isWatchlisted
                      ? 'bookmark'
                      : 'bookmark-outline'
                    : undefined
                }
                accessibilityLabel={watchlistBusy ? 'Saving watchlist change' : watchlistButtonLabel}
                onSelect={handleToggleWatchlist}
                loading={watchlistBusy}
                style={[
                  useCompactActionLayout ? styles.iconActionButton : styles.watchlistActionButton,
                  isWatchlisted && styles.watchlistActionButtonActive,
                ]}
                disabled={!canToggleWatchlist || watchlistBusy}
              />
              <FocusablePressable
                focusKey="toggle-watched"
                text={!useCompactActionLayout ? (watchlistBusy ? 'Saving...' : watchStateButtonLabel) : undefined}
                icon={useCompactActionLayout || Platform.isTV ? (isWatched ? 'eye' : 'eye-outline') : undefined}
                accessibilityLabel={watchlistBusy ? 'Saving watched state' : watchStateButtonLabel}
                onSelect={handleToggleWatched}
                loading={watchlistBusy}
                style={[
                  useCompactActionLayout ? styles.iconActionButton : styles.watchStateButton,
                  isWatched && styles.watchStateButtonActive,
                ]}
                disabled={watchlistBusy}
              />
              {/* Trailer button temporarily disabled */}
              {false && !isSeries && (trailersLoading || hasAvailableTrailer) && (
                <FocusablePressable
                  focusKey="watch-trailer"
                  text={!useCompactActionLayout ? trailerButtonLabel : undefined}
                  icon={useCompactActionLayout || Platform.isTV ? 'videocam' : undefined}
                  accessibilityLabel={trailerButtonLabel}
                  onSelect={handleWatchTrailer}
                  loading={trailersLoading}
                  style={useCompactActionLayout ? styles.iconActionButton : styles.trailerActionButton}
                  disabled={trailerButtonDisabled}
                />
              )}
              {/* Show progress badge in action row only for movies (no episode card) */}
              {displayProgress !== null && displayProgress > 0 && !activeEpisode && (
                <View style={[styles.progressIndicator, useCompactActionLayout && styles.progressIndicatorCompact]}>
                  <Text
                    style={[
                      styles.progressIndicatorText,
                      useCompactActionLayout && styles.progressIndicatorTextCompact,
                    ]}>
                    {`${displayProgress}%`}
                  </Text>
                </View>
              )}
            </View>
          </SpatialNavigationNode>
          {watchlistError && <Text style={styles.watchlistError}>{watchlistError}</Text>}
          {trailersError && <Text style={styles.trailerError}>{trailersError}</Text>}
          {!Platform.isTV && activeEpisode && (
            <View style={styles.episodeCardContainer}>
              <EpisodeCard episode={activeEpisode} percentWatched={displayProgress} />
            </View>
          )}
          {!Platform.isTV && activeEpisode && (
            <View style={styles.mobileEpisodeNavRow}>
              <FocusablePressable
                focusKey="previous-episode-mobile"
                icon="chevron-back"
                accessibilityLabel="Previous Episode"
                onSelect={handlePreviousEpisode}
                disabled={!findPreviousEpisode(activeEpisode)}
                style={styles.mobileEpisodeNavButton}
              />
              <Text style={styles.mobileEpisodeNavLabel}>
                S{activeEpisode.seasonNumber} E{activeEpisode.episodeNumber}
              </Text>
              <FocusablePressable
                focusKey="next-episode-mobile"
                icon="chevron-forward"
                accessibilityLabel="Next Episode"
                onSelect={handleNextEpisode}
                disabled={!findNextEpisode(activeEpisode)}
                style={styles.mobileEpisodeNavButton}
              />
            </View>
          )}
          {/* Hidden SeriesEpisodes component to load data */}
          {isSeries ? (
            <View style={{ position: 'absolute', opacity: 0, pointerEvents: 'none', zIndex: -1 }}>
              <SeriesEpisodes
                isSeries={isSeries}
                title={title}
                tvdbId={tvdbId}
                titleId={titleId}
                yearNumber={yearNumber}
                initialSeasonNumber={initialSeasonNumber}
                initialEpisodeNumber={initialEpisodeNumber}
                isTouchSeasonLayout={isTouchSeasonLayout}
                shouldUseSeasonModal={shouldUseSeasonModal}
                shouldAutoPlaySeasonSelection={shouldAutoPlaySeasonSelection}
                onSeasonSelect={handleSeasonSelect}
                onEpisodeSelect={handleEpisodeSelect}
                onEpisodeFocus={handleEpisodeFocus}
                onPlaySeason={handlePlaySeason}
                onPlayEpisode={handlePlayEpisode}
                onEpisodeLongPress={handleEpisodeLongPress}
                onToggleEpisodeWatched={handleToggleEpisodeWatched}
                isEpisodeWatched={isEpisodeWatched}
                renderContent={!Platform.isTV}
                activeEpisode={activeEpisode}
                isResolving={isResolving}
                theme={theme}
                onRegisterSeasonFocusHandler={handleRegisterSeasonFocusHandler}
                onRequestFocusShift={handleRequestFocusShift}
                onEpisodesLoaded={handleEpisodesLoaded}
                onSeasonsLoaded={handleSeasonsLoaded}
              />
            </View>
          ) : null}
        </View>
      </SpatialNavigationNode>
    </>
  );

  const SafeAreaWrapper = isTV ? View : FixedSafeAreaView;
  const safeAreaProps = isTV ? {} : { edges: ['top'] as ('top' | 'bottom' | 'left' | 'right')[] };

  // On TV/mobile, wait for metadata to load before showing the page to prevent background "pop"
  const isMetadataLoading = isSeries ? seriesDetailsLoading : movieDetailsLoading;
  const shouldHideUntilMetadataReady = (isTV || isMobile) && isMetadataLoading;
  const shouldAnimateBackground = isTV || isMobile;

  // Fade in background when metadata is ready
  const backgroundOpacity = useSharedValue(shouldAnimateBackground ? 0 : 1);
  const backgroundAnimatedStyle = useAnimatedStyle(() => ({
    opacity: backgroundOpacity.value,
  }));

  // Track if we've already triggered the fade-in
  const hasTriggeredFadeIn = useRef(false);

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    if (!shouldHideUntilMetadataReady && shouldAnimateBackground && !hasTriggeredFadeIn.current) {
      console.log('[Details] Triggering background fade-in animation');
      hasTriggeredFadeIn.current = true;
      // Cancel any existing animation and force opacity to 0
      cancelAnimation(backgroundOpacity);
      backgroundOpacity.value = 0;
      // Small timeout to ensure the 0 opacity frame is rendered before animating
      timer = setTimeout(() => {
        backgroundOpacity.value = withTiming(1, { duration: 300, easing: Easing.out(Easing.ease) });
      }, 16); // ~1 frame at 60fps
    }
    return () => {
      if (timer) clearTimeout(timer);
    };
  }, [shouldHideUntilMetadataReady, shouldAnimateBackground, backgroundOpacity]);

  console.log('[Details] Visibility state:', {
    shouldHideUntilMetadataReady,
    shouldAnimateBackground,
    movieDetailsLoading,
    hasTriggeredFadeIn: hasTriggeredFadeIn.current,
  });

  return (
    <>
      <SpatialNavigationRoot
        isActive={
          isDetailsPageActive &&
          !manualVisible &&
          !trailerModalVisible &&
          !bulkWatchModalVisible &&
          !resumeModalVisible &&
          !seasonSelectorVisible &&
          !episodeSelectorVisible
        }
        onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
        <Stack.Screen options={{ headerShown: false }} />
        <SafeAreaWrapper style={styles.safeArea} {...safeAreaProps}>
          <View style={styles.container}>
            {headerImage && !shouldHideUntilMetadataReady ? (
              <Animated.View
                style={[
                  styles.backgroundImageContainer,
                  shouldAnchorHeroToTop && styles.backgroundImageContainerTop,
                  (isTV || isMobile) && backgroundAnimatedStyle,
                ]}
                pointerEvents="none">
                {shouldShowBlurredFill && (
                  <Image
                    source={{ uri: headerImage }}
                    style={styles.backgroundImageBackdrop}
                    resizeMode="cover"
                    blurRadius={20}
                  />
                )}
                <Image
                  source={{ uri: headerImage }}
                  style={[
                    styles.backgroundImage,
                    shouldUseAdaptiveHeroSizing && styles.backgroundImageSharp,
                    backgroundImageSizingStyle,
                  ]}
                  resizeMode={backgroundImageResizeMode}
                />
                <LinearGradient
                  pointerEvents="none"
                  colors={
                    Platform.isTV
                      ? ['rgba(0, 0, 0, 0)', 'rgba(0, 0, 0, 0.6)', 'rgba(0, 0, 0, 0.9)']
                      : ['rgba(0, 0, 0, 0)', 'rgba(0, 0, 0, 0.8)', '#000']
                  }
                  locations={Platform.isTV ? [0, 0.5, 1] : [0, 0.7, 1]}
                  start={{ x: 0.5, y: 0 }}
                  end={{ x: 0.5, y: 1 }}
                  style={styles.heroFadeOverlay}
                />
              </Animated.View>
            ) : null}
            <LinearGradient
              pointerEvents="none"
              colors={overlayGradientColors}
              locations={overlayGradientLocations}
              start={{ x: 0.5, y: 0 }}
              end={{ x: 0.5, y: 1 }}
              style={styles.gradientOverlay}
            />
            <View style={styles.contentOverlay}>
              <View style={[styles.contentBox, contentBoxStyle]}>
                <View style={styles.contentBoxInner}>
                  <View style={[styles.contentContainer, isMobile && styles.mobileContentContainer]}>
                    {renderDetailsContent()}
                  </View>
                </View>
              </View>
            </View>
            {Platform.isTV && posterUrl && (
              <View style={styles.posterContainerTV}>
                <Image source={{ uri: posterUrl }} style={styles.posterImageTV} resizeMode="cover" />
                <LinearGradient
                  colors={['transparent', 'rgba(0, 0, 0, 0.7)']}
                  locations={[0, 1]}
                  style={styles.posterGradientTV}
                />
              </View>
            )}
          </View>
        </SafeAreaWrapper>
        <MobileTabBar />
      </SpatialNavigationRoot>
      <TrailerModal visible={trailerModalVisible} trailer={activeTrailer} onClose={handleCloseTrailer} theme={theme} />
      <ResumePlaybackModal
        visible={resumeModalVisible}
        onClose={handleCloseResumeModal}
        onResume={handleResumePlayback}
        onPlayFromBeginning={handlePlayFromBeginning}
        theme={theme}
        percentWatched={currentProgress?.percentWatched ?? 0}
      />
      <BulkWatchModal
        visible={bulkWatchModalVisible}
        onClose={() => setBulkWatchModalVisible(false)}
        theme={theme}
        seasons={seasons}
        allEpisodes={allEpisodes}
        currentEpisode={activeEpisode}
        onMarkAllWatched={handleMarkAllWatched}
        onMarkAllUnwatched={handleMarkAllUnwatched}
        onMarkSeasonWatched={handleMarkSeasonWatched}
        onMarkSeasonUnwatched={handleMarkSeasonUnwatched}
        onMarkEpisodeWatched={handleMarkEpisodeWatched}
        onMarkEpisodeUnwatched={handleMarkEpisodeUnwatched}
        isEpisodeWatched={isEpisodeWatched}
      />
      <ManualSelection
        visible={manualVisible}
        loading={manualLoading}
        error={manualError}
        results={manualResults}
        healthChecks={manualHealthChecks}
        onClose={closeManualPicker}
        onSelect={handleManualSelection}
        onCheckHealth={checkManualHealth}
        theme={theme}
        isWebTouch={isWebTouch}
        isMobile={isMobile}
        maxHeight={manualResultsMaxHeight}
        demoMode={settings?.demoMode}
      />
      <SeasonSelector
        visible={seasonSelectorVisible}
        onClose={() => setSeasonSelectorVisible(false)}
        seasons={seasons}
        onSeasonSelect={handleSeasonSelectorSelect}
        theme={theme}
      />
      <EpisodeSelector
        visible={episodeSelectorVisible}
        onClose={() => setEpisodeSelectorVisible(false)}
        onBack={handleEpisodeSelectorBack}
        season={selectedSeason}
        onEpisodeSelect={handleEpisodeSelectorSelect}
        isEpisodeWatched={isEpisodeWatched}
        theme={theme}
      />
      {/* Black overlay for smooth transition to player */}
      {showBlackOverlay && (
        <View
          style={{
            position: 'absolute',
            top: 0,
            left: 0,
            right: 0,
            bottom: 0,
            backgroundColor: '#000000',
            zIndex: 9999,
          }}
        />
      )}
    </>
  );
}

const createDetailsStyles = (theme: NovaTheme) => {
  // Unified TV scaling - tvOS is baseline (1.0), Android TV auto-derives
  const tvScale = isTV ? getTVScaleMultiplier() : 1;
  // TV text scale designed for tvOS at 1.375x
  const tvTextScale = isTV ? 1.375 * tvScale : 1;

  return StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    container: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
      position: 'relative',
    },
    backgroundImageContainer: {
      ...StyleSheet.absoluteFillObject,
      alignItems: 'center',
      justifyContent: 'center',
      overflow: 'hidden',
    },
    backgroundImageContainerTop: {
      justifyContent: 'flex-start',
    },
    backgroundImage: {
      opacity: Platform.isTV ? 1 : 0.3,
      zIndex: 1,
    },
    backgroundImageSharp: {
      opacity: 1,
    },
    backgroundImageFill: {
      width: '100%',
      height: '100%',
    },
    // Absolute, full-bleed layer for blurred backdrop fill
    backgroundImageBackdrop: {
      ...StyleSheet.absoluteFillObject,
      zIndex: 0,
    },
    heroFadeOverlay: {
      position: 'absolute',
      left: 0,
      right: 0,
      bottom: 0,
      height: Platform.isTV ? '25%' : '65%',
      zIndex: 3,
    },
    gradientOverlay: {
      ...StyleSheet.absoluteFillObject,
      zIndex: 2,
    },
    contentOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'flex-end',
      zIndex: 4,
    },
    contentBox: {
      width: '100%',
      position: 'relative',
    },
    contentBoxInner: {
      flex: 1,
    },
    contentBoxConfined: {
      flex: 1,
      overflow: 'hidden',
    },
    contentMask: {
      ...StyleSheet.absoluteFillObject,
    },
    contentContainer: {
      flex: 1,
      paddingHorizontal: theme.spacing['3xl'],
      paddingVertical: theme.spacing['3xl'],
      gap: theme.spacing['2xl'],
      ...(Platform.isTV ? { flexDirection: 'column', justifyContent: 'flex-end' } : null),
    },
    mobileContentContainer: {
      justifyContent: 'flex-end',
    },
    touchContentScroll: {
      flex: 1,
    },
    touchContentContainer: {
      paddingHorizontal: theme.spacing['3xl'],
      paddingTop: theme.spacing['3xl'],
      paddingBottom: theme.spacing['3xl'],
      gap: theme.spacing['2xl'],
      minHeight: '100%',
      justifyContent: 'flex-end',
    },
    topContent: {},
    topContentTV: {
      backgroundColor: 'rgba(0, 0, 0, 0.35)',
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.lg,
      borderRadius: theme.radius.md,
      alignSelf: 'flex-start',
    },
    topContentMobile: {
      backgroundColor: 'rgba(0, 0, 0, 0.35)',
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
    },
    bottomContent: {
      ...(Platform.isTV ? { flex: 0 } : null),
      position: 'relative',
    },
    mobileBottomContent: {
      flexDirection: 'column-reverse',
    },
    titleRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.md,
      marginBottom: theme.spacing.lg,
      ...(isTV ? { maxWidth: '70%' } : null),
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      ...(isTV
        ? {
            // Design for tvOS (+8px), Android TV auto-scales, 25% larger
            fontSize: Math.round((theme.typography.title.xl.fontSize + 8) * tvScale * 1.25),
            lineHeight: Math.round((theme.typography.title.xl.lineHeight + 8) * tvScale * 1.25),
          }
        : null),
    },
    ratingsRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.md,
      marginBottom: theme.spacing.md,
    },
    ratingBadge: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: Math.round((isTV ? 7 : 4) * tvScale),
      backgroundColor: 'rgba(255, 255, 255, 0.1)',
      paddingHorizontal: Math.round((isTV ? 11 : 8) * tvScale),
      paddingVertical: Math.round((isTV ? 6 : 4) * tvScale),
      borderRadius: Math.round(6 * tvScale),
    },
    ratingValue: {
      fontSize: Math.round((isTV ? 17 : 14) * tvScale),
      fontWeight: '700',
    },
    ratingLabel: {
      fontSize: Math.round((isTV ? 14 : 12) * tvTextScale),
      color: theme.colors.text.secondary,
    },
    releaseInfoRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      marginBottom: theme.spacing.md,
    },
    releaseInfoItem: {
      marginRight: theme.spacing.lg,
      marginBottom: theme.spacing.sm,
    },
    releaseInfoLabel: {
      color: theme.colors.text.secondary,
      // Design for tvOS, Android TV auto-scales
      fontSize: Math.round(14 * tvTextScale),
      marginBottom: 2,
    },
    releaseInfoValue: {
      color: theme.colors.text.primary,
      // Design for tvOS, Android TV auto-scales
      fontSize: Math.round(16 * tvTextScale),
      fontWeight: '600',
    },
    releaseInfoLoading: {
      color: theme.colors.text.secondary,
      fontSize: Math.round(14 * tvTextScale),
    },
    releaseInfoError: {
      color: theme.colors.status.danger,
      fontSize: Math.round(14 * tvTextScale),
    },
    watchlistEyeIcon: {
      marginTop: theme.spacing.xs,
    },
    description: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
      marginBottom: theme.spacing.sm,
      width: '100%',
      maxWidth: theme.breakpoint === 'compact' ? '100%' : '60%',
      ...(isTV
        ? {
            // Design for tvOS at 1.5x, Android TV at 1.875x (25% larger than before)
            fontSize: Math.round(
              theme.typography.body.lg.fontSize * (Platform.OS === 'android' ? 1.875 : 1.5) * tvScale,
            ),
            lineHeight: Math.round(
              theme.typography.body.lg.lineHeight * (Platform.OS === 'android' ? 1.875 : 1.5) * tvScale,
            ),
          }
        : null),
    },
    descriptionToggle: {
      color: theme.colors.text.muted,
      fontSize: 14,
      marginTop: 4,
    },
    descriptionHidden: {
      position: 'absolute',
      opacity: 0,
      zIndex: -1,
    },
    readMoreButton: {
      alignSelf: 'flex-start',
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md,
      marginBottom: theme.spacing.lg,
      backgroundColor: theme.colors.overlay.button,
      borderRadius: theme.radius.md,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    actionRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.lg,
    },
    compactActionRow: {
      flexWrap: 'nowrap',
      gap: theme.spacing.sm,
      maxWidth: '100%',
    },
    primaryActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
    },
    manualActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    debugActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.status.warning,
    },
    trailerActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    watchlistActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    watchlistActionButtonActive: {
      // No special background when active - let focus state handle styling
    },
    watchStateButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    watchStateButtonActive: {
      // No special background when active - let focus state handle styling
    },
    iconActionButton: {
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.sm,
      minWidth: theme.spacing['2xl'] * 1.5,
    },
    watchlistError: {
      marginTop: theme.spacing.md,
      color: theme.colors.status.danger,
      ...theme.typography.body.sm,
    },
    trailerError: {
      marginTop: theme.spacing.sm,
      color: theme.colors.status.danger,
      ...theme.typography.body.sm,
    },
    episodeNavigationRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.md,
      marginBottom: theme.spacing.md,
    },
    episodeNavButton: {
      // Scale padding for TV - paddingVertical inherited from FocusablePressable for consistent height
      paddingHorizontal: Math.round(theme.spacing['2xl'] * tvTextScale),
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    mobileEpisodeNavRow: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'center',
      alignSelf: 'center',
      gap: theme.spacing.xs,
      marginBottom: theme.spacing.sm,
      backgroundColor: 'rgba(255, 255, 255, 0.15)',
      borderRadius: 24,
      paddingVertical: theme.spacing.xs,
      paddingHorizontal: theme.spacing.xs,
    },
    mobileEpisodeNavButton: {
      width: 36,
      height: 36,
      borderRadius: 18,
      backgroundColor: 'rgba(0, 0, 0, 0.4)',
      justifyContent: 'center',
      alignItems: 'center',
      paddingHorizontal: 0,
      paddingVertical: 0,
      minWidth: 36,
    },
    mobileEpisodeNavLabel: {
      color: '#fff',
      fontSize: 14,
      fontWeight: '600',
      paddingHorizontal: theme.spacing.xs,
    },
    episodeCardContainer: {
      marginBottom: theme.spacing.xl,
    },
    episodeCardWrapperTV: {
      width: '75%',
    },
    posterContainerTV: {
      position: 'absolute',
      right: theme.spacing.xl,
      bottom: theme.spacing.xl,
      width: '20%',
      aspectRatio: 2 / 3,
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
      zIndex: 5,
    },
    posterImageTV: {
      width: '100%',
      height: '100%',
    },
    posterGradientTV: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: '20%',
    },
    progressIndicator: {
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md * tvTextScale,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md * tvTextScale,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.accent.primary,
      justifyContent: 'center',
      alignItems: 'center',
      alignSelf: 'stretch',
    },
    progressIndicatorCompact: {
      paddingHorizontal: theme.spacing.sm * tvTextScale,
      paddingVertical: theme.spacing.sm * tvTextScale,
      minWidth: theme.spacing['2xl'] * 1.5,
      alignSelf: 'stretch',
    },
    progressIndicatorText: {
      ...theme.typography.label.md,
      color: theme.colors.accent.primary,
      fontWeight: '600',
      ...(isTV
        ? {
            // Design for tvOS at 1.375x, Android TV auto-scales
            fontSize: Math.round(theme.typography.label.md.fontSize * tvTextScale),
            lineHeight: Math.round(theme.typography.label.md.lineHeight * tvTextScale),
          }
        : null),
    },
    progressIndicatorTextCompact: {
      ...theme.typography.label.md,
      fontSize: Math.round(theme.typography.label.md.fontSize * tvTextScale),
      lineHeight: Math.round(theme.typography.label.md.lineHeight * tvTextScale),
    },
  });
};
