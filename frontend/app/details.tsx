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

// Safely import new TV components - fallback to TVEpisodeStrip if unavailable
let TVActionButton: typeof import('@/components/tv').TVActionButton | null = null;
let TVEpisodeCarousel: typeof import('@/components/tv').TVEpisodeCarousel | null = null;
let TVCastSection: typeof import('@/components/tv').TVCastSection | null = null;
let TVMoreLikeThisSection: typeof import('@/components/tv').TVMoreLikeThisSection | null = null;
try {
  const tvComponents = require('@/components/tv');
  TVActionButton = tvComponents.TVActionButton;
  TVEpisodeCarousel = tvComponents.TVEpisodeCarousel;
  TVCastSection = tvComponents.TVCastSection;
  TVMoreLikeThisSection = tvComponents.TVMoreLikeThisSection;
} catch {
  // TV components not available, will use fallbacks
}
import {
  apiService,
  type AudioTrackInfo,
  type CastMember,
  type ContentPreference,
  type EpisodeWatchPayload,
  type NZBResult,
  type PrequeueStatusResponse,
  type Rating,
  type SeriesDetails,
  type SeriesEpisode,
  type SeriesSeason,
  type SubtitleTrackInfo,
  type Title,
  type Trailer,
  type TrailerPrequeueStatus,
} from '@/services/api';
import { SpatialNavigationNode, SpatialNavigationRoot, useSpatialNavigator } from '@/services/tv-navigation';
import { useTheme } from '@/theme';
import { getTVScaleMultiplier, isTablet } from '@/theme/tokens/tvScale';
import { getUnplayableReleases } from '@/hooks/useUnplayableReleases';
import { playbackNavigation } from '@/services/playback-navigation';
import { findAudioTrackByLanguage, findSubtitleTrackByPreference } from '@/app/details/track-selection';
import { Ionicons } from '@expo/vector-icons';
import { activateKeepAwakeAsync, deactivateKeepAwake } from 'expo-keep-awake';
import { LinearGradient } from 'expo-linear-gradient';
import { Stack, useLocalSearchParams, useRouter, usePathname } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Image as RNImage, ImageResizeMode, ImageStyle, Platform, Pressable, Text, View } from 'react-native';
import { Image as ProxiedImage } from '@/components/Image';
import { createDetailsStyles } from '@/styles/details-styles';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import Animated, {
  useAnimatedStyle,
  useAnimatedScrollHandler,
  useSharedValue,
  withTiming,
  withRepeat,
  Easing,
  cancelAnimation,
} from 'react-native-reanimated';

// Import extracted modules
import { BulkWatchModal } from './details/bulk-watch-modal';
import { ManualSelection, useManualHealthChecks, type ManualTrackOverrides } from './details/manual-selection';
import { TrackSelectionModal } from '@/components/player/TrackSelectionModal';
import {
  buildExternalPlayerTargets,
  getHealthFailureReason,
  getTimeoutMessage,
  initiatePlayback,
  isHealthFailureError,
  isTimeoutError,
} from './details/playback';
import { ResumePlaybackModal } from './details/resume-modal';
import { SeriesEpisodes } from './details/series-episodes';
import { TrailerModal } from './details/trailer';
import { SeasonSelector } from './details/season-selector';
import { EpisodeSelector } from './details/episode-selector';
import { buildEpisodeQuery, buildSeasonQuery, formatPublishDate, formatUnreleasedMessage, isEpisodeUnreleased, padNumber, toStringParam } from './details/utils';
import MobileParallaxContainer from './details/mobile-parallax-container';
import MobileEpisodeCarousel from './details/mobile-episode-carousel';
import CastSection from '@/components/CastSection';
import MoreLikeThisSection from '@/components/MoreLikeThisSection';

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

// Format language code to display name
const formatLanguage = (lang: string | undefined): string => {
  if (!lang) return 'Unknown';
  const langMap: Record<string, string> = {
    eng: 'English',
    en: 'English',
    jpn: 'Japanese',
    ja: 'Japanese',
    spa: 'Spanish',
    es: 'Spanish',
    fre: 'French',
    fra: 'French',
    fr: 'French',
    ger: 'German',
    deu: 'German',
    de: 'German',
    ita: 'Italian',
    it: 'Italian',
    por: 'Portuguese',
    pt: 'Portuguese',
    rus: 'Russian',
    ru: 'Russian',
    chi: 'Chinese',
    zho: 'Chinese',
    zh: 'Chinese',
    kor: 'Korean',
    ko: 'Korean',
    ara: 'Arabic',
    ar: 'Arabic',
    hin: 'Hindi',
    hi: 'Hindi',
    und: 'Unknown',
  };
  return langMap[lang.toLowerCase()] || lang.toUpperCase();
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
        <RNImage
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
  const { height: windowHeight, width: windowWidth } = useTVDimensions();
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
  // Tablets always anchor hero to top (grow from top down); phones only in portrait
  const shouldAnchorHeroToTop = isTablet || (shouldUseAdaptiveHeroSizing && isPortraitOrientation);
  // Compute media type early for content box sizing
  const rawMediaTypeForLayout = toStringParam(params.mediaType);
  const mediaTypeForLayout = (rawMediaTypeForLayout || 'movie').toLowerCase();
  const isSeriesLayout =
    mediaTypeForLayout === 'series' || mediaTypeForLayout === 'tv' || mediaTypeForLayout === 'show';

  const contentBoxStyle = useMemo(() => {
    if (Platform.isTV) {
      // Series need more space for episode carousel + cast, movies need less
      const heightRatio = isSeriesLayout ? 0.55 : 0.4;
      return { height: Math.round(windowHeight * heightRatio) };
    }
    return { flex: 1 };
  }, [Platform.isTV, windowHeight, isSeriesLayout]);
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

  // State to hold fetched details for backdrop updates and episodes
  const [seriesDetailsData, setSeriesDetailsData] = useState<SeriesDetails | null>(null);
  const [movieDetails, setMovieDetails] = useState<Title | null>(null);

  // Derive the title from series details for poster/backdrop
  const seriesDetailsForBackdrop = seriesDetailsData?.title ?? null;

  const tmdbId = toStringParam(params.tmdbId);
  const imdbId = toStringParam(params.imdbId);
  const tvdbId = toStringParam(params.tvdbId);
  const yearParam = toStringParam(params.year);
  const initialSeasonParam = toStringParam(params.initialSeason);
  const initialEpisodeParam = toStringParam(params.initialEpisode);

  // Compute final poster URL - prefer fetched metadata for textless posters
  const posterUrl = useMemo(() => {
    // Prefer fetched details (may have textless poster)
    if (!isSeries && movieDetails?.poster?.url) {
      return movieDetails.poster.url;
    }
    if (isSeries && seriesDetailsForBackdrop?.poster?.url) {
      return seriesDetailsForBackdrop.poster.url;
    }
    // Fall back to params
    return posterUrlParam;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop, posterUrlParam]);

  const backdropUrl = useMemo(() => {
    // Backdrop can update from fetched data since it's less prominent
    if (!isSeries && movieDetails?.backdrop?.url) {
      return movieDetails.backdrop.url;
    }
    if (isSeries && seriesDetailsForBackdrop?.backdrop?.url) {
      return seriesDetailsForBackdrop.backdrop.url;
    }
    return backdropUrlParam;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop, backdropUrlParam]);

  // Compute logo URL from fetched metadata
  const logoUrl = useMemo(() => {
    if (!isSeries && movieDetails?.logo?.url) {
      return movieDetails.logo.url;
    }
    if (isSeries && seriesDetailsForBackdrop?.logo?.url) {
      return seriesDetailsForBackdrop.logo.url;
    }
    return null;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop]);

  // Measure logo dimensions to calculate proper sizing within bounding box
  const [logoDimensions, setLogoDimensions] = useState<{ width: number; height: number } | null>(null);
  useEffect(() => {
    if (!logoUrl) {
      setLogoDimensions(null);
      return;
    }
    RNImage.getSize(
      logoUrl,
      (width, height) => setLogoDimensions({ width, height }),
      () => setLogoDimensions(null)
    );
  }, [logoUrl]);

  // Preload poster/backdrop image so it's ready when page displays
  const [isPosterPreloaded, setIsPosterPreloaded] = useState(false);
  const posterToPreload = posterUrl || backdropUrl;
  useEffect(() => {
    if (!posterToPreload) {
      setIsPosterPreloaded(true);
      return;
    }
    setIsPosterPreloaded(false);
    RNImage.prefetch(posterToPreload)
      .then(() => setIsPosterPreloaded(true))
      .catch(() => setIsPosterPreloaded(true)); // Still show page on error
  }, [posterToPreload]);

  // Calculate logo style to maintain constant area across different aspect ratios
  // All logos will have the same width * height regardless of their shape
  const logoStyle = useMemo(() => {
    if (!logoDimensions) return styles.titleLogo;

    const { width: imgWidth, height: imgHeight } = logoDimensions;
    const aspectRatio = imgWidth / imgHeight;

    // Fixed target area in square pixels - all logos will have this exact area
    // TV: 1.8x larger than original, Mobile: reduced by 30% from original
    const baseTargetArea = isTV ? (tvScale * 120) * (tvScale * 120) * 3.4 : 80 * 80 * 2.1;

    // Perceptual boost for squarish logos - wide logos look bigger than their area suggests
    // Boost area for logos with aspect ratio below reference so they have similar visual presence
    const referenceAspectRatio = 5;
    const perceptualBoost = aspectRatio < referenceAspectRatio
      ? Math.pow(referenceAspectRatio / aspectRatio, 0.25)
      : 1;
    const targetArea = baseTargetArea * perceptualBoost;

    // Calculate dimensions that give targetArea while preserving aspect ratio
    // area = width * height, aspectRatio = width / height
    // Solving: width = sqrt(area * aspectRatio), height = sqrt(area / aspectRatio)
    let finalWidth = Math.sqrt(targetArea * aspectRatio);
    let finalHeight = Math.sqrt(targetArea / aspectRatio);

    const preScaleWidth = finalWidth;
    const preScaleHeight = finalHeight;
    const preScaleArea = finalWidth * finalHeight;
    let wasScaled = false;
    let scaleApplied = 1;

    // Bounding box constraints - scale down if needed to fit on screen
    // maxHeight needs to be generous enough for square-ish logos to achieve target area
    // TV: 1.8x larger to match the 1.8x logo size increase
    const maxWidth = windowWidth * (isTV ? 0.3 : 0.8);
    const maxHeight = isTV ? tvScale * 216 : 120;

    if (finalWidth > maxWidth || finalHeight > maxHeight) {
      const scaleX = finalWidth > maxWidth ? maxWidth / finalWidth : 1;
      const scaleY = finalHeight > maxHeight ? maxHeight / finalHeight : 1;
      scaleApplied = Math.min(scaleX, scaleY);
      finalWidth *= scaleApplied;
      finalHeight *= scaleApplied;
      wasScaled = true;
    }

    const finalArea = finalWidth * finalHeight;

    console.log('[LOGO DEBUG]', {
      imgDimensions: { width: imgWidth, height: imgHeight },
      aspectRatio: aspectRatio.toFixed(2),
      perceptualBoost: perceptualBoost.toFixed(2),
      targetArea: Math.round(targetArea),
      preScale: {
        width: Math.round(preScaleWidth),
        height: Math.round(preScaleHeight),
        area: Math.round(preScaleArea),
      },
      bounds: { maxWidth: Math.round(maxWidth), maxHeight: Math.round(maxHeight) },
      wasScaled,
      scaleApplied: scaleApplied.toFixed(3),
      final: {
        width: Math.round(finalWidth),
        height: Math.round(finalHeight),
        area: Math.round(finalArea),
      },
    });

    return {
      width: finalWidth,
      height: finalHeight,
    };
  }, [logoDimensions, windowWidth, isTV, tvScale, styles.titleLogo]);

  // Shadow style for logo wrapper - applied to View, not Image, to avoid clipping
  const logoGlowStyle = {
    shadowColor: 'rgba(255, 255, 255, 0.2)',
    shadowOffset: { width: 0, height: 0 },
    shadowOpacity: 1,
    shadowRadius: 6,
    elevation: 2,
  };

  // Compute final description/overview, preferring params but falling back to fetched metadata
  const displayDescription = useMemo(() => {
    // If we have a description from params, use it
    if (description) {
      return description;
    }
    // For series, fall back to fetched series details
    if (isSeries && seriesDetailsForBackdrop?.overview) {
      return seriesDetailsForBackdrop.overview;
    }
    // For movies, fall back to fetched movie details
    if (!isSeries && movieDetails?.overview) {
      return movieDetails.overview;
    }
    return '';
  }, [description, isSeries, seriesDetailsForBackdrop, movieDetails]);

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

    RNImage.getSize(
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
  // Ref to pass shuffle mode synchronously to playback (state updates are async)
  const pendingShuffleModeRef = useRef<boolean>(false);

  // Check for next episode when screen comes into focus
  useFocusEffect(
    useCallback(() => {
      if (titleId) {
        const nextEp = playbackNavigation.consumeNextEpisode(titleId);
        if (nextEp) {
          console.log('[Details] Found next episode from playback:', nextEp, {
            hasPrequeueId: !!nextEp.prequeueId,
            hasPrequeueStatus: !!nextEp.prequeueStatus,
            prequeueStatusReady: nextEp.prequeueStatus
              ? apiService.isPrequeueReady(nextEp.prequeueStatus.status)
              : false,
          });
          setNextEpisodeFromPlayback(nextEp);
          // Restore shuffle mode from playback navigation (set both state and ref for synchronous access)
          setIsShuffleMode(nextEp.shuffleMode);
          pendingShuffleModeRef.current = nextEp.shuffleMode;

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

  // Kids profiles have restricted navigation - disable cast/crew and similar content links
  const isKidsProfile = activeUser?.isKidsProfile ?? false;

  const [isResolving, setIsResolving] = useState(false);
  const [selectionError, setSelectionError] = useState<string | null>(null);
  const [showBlackOverlay, setShowBlackOverlay] = useState(false);

  // Clear black overlay, loading screen, and refresh progress when returning to details page
  useFocusEffect(
    useCallback(() => {
      setShowBlackOverlay(false);
      hideLoadingScreen();
      // Trigger progress refresh when returning from playback
      setProgressRefreshKey((k) => k + 1);
    }, [hideLoadingScreen]),
  );

  // Prevent screen timeout during playback resolution (auto-play and manual selection)
  useEffect(() => {
    if (isResolving) {
      activateKeepAwakeAsync().catch(() => {
        // Ignore errors - keep-awake may not be available on all platforms
      });
    } else {
      deactivateKeepAwake();
    }

    return () => {
      deactivateKeepAwake();
    };
  }, [isResolving]);

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
  // Pre-loaded trailer stream URL (served from backend prequeue for YouTube trailers)
  const [trailerStreamUrl, setTrailerStreamUrl] = useState<string | null>(null);
  // Trailer prequeue state for 1080p YouTube trailers
  const [trailerPrequeueId, setTrailerPrequeueId] = useState<string | null>(null);
  const [trailerPrequeueStatus, setTrailerPrequeueStatus] = useState<TrailerPrequeueStatus | null>(null);

  // Similar content ("More Like This") state
  const [similarContent, setSimilarContent] = useState<Title[]>([]);
  const [similarLoading, setSimilarLoading] = useState(false);

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
  const [progressRefreshKey, setProgressRefreshKey] = useState(0);
  const [contentPreference, setContentPreference] = useState<ContentPreference | null>(null);
  const [episodeProgressMap, setEpisodeProgressMap] = useState<Map<string, number>>(new Map());

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
  // TV: Track topContent height for dynamic spacer sizing
  const [tvTopContentHeight, setTvTopContentHeight] = useState(0);

  // Reset description height measurements when displayDescription changes
  // This ensures the container re-measures when overview loads asynchronously
  useEffect(() => {
    setCollapsedHeight(0);
    setExpandedHeight(0);
    descriptionHeight.value = 0;
    setIsDescriptionExpanded(false);
  }, [displayDescription]);

  const [nextUpEpisode, setNextUpEpisode] = useState<SeriesEpisode | null>(null);
  const [allEpisodes, setAllEpisodes] = useState<SeriesEpisode[]>([]);
  const [isShuffleMode, setIsShuffleMode] = useState(false);
  // Keep pendingShuffleModeRef in sync with state for subsequent playbacks
  useEffect(() => {
    pendingShuffleModeRef.current = isShuffleMode;
  }, [isShuffleMode]);
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
  const [prequeueDisplayInfo, setPrequeueDisplayInfo] = useState<PrequeueStatusResponse | null>(null);
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

  // Track override state - user's manual selection that differs from prequeue
  const [trackOverrideAudio, setTrackOverrideAudio] = useState<number | null>(null);
  const [trackOverrideSubtitle, setTrackOverrideSubtitle] = useState<number | null>(null);

  // Modal visibility for track selection
  const [showAudioTrackModal, setShowAudioTrackModal] = useState(false);
  const [showSubtitleTrackModal, setShowSubtitleTrackModal] = useState(false);

  // Pulse animation for prequeue loading state
  const prequeuePulseOpacity = useSharedValue(1);
  // Fade-in animation for when prequeue content first appears
  const prequeueFadeIn = useSharedValue(0);
  const prequeuePulseStyle = useAnimatedStyle(() => {
    return {
      opacity: prequeuePulseOpacity.value * prequeueFadeIn.value,
    };
  });

  // Handle fade-in when prequeue info first appears
  useEffect(() => {
    if (prequeueDisplayInfo) {
      // Fade in the content
      prequeueFadeIn.value = withTiming(1, { duration: 200, easing: Easing.out(Easing.ease) });
    } else {
      // Reset fade for next appearance
      prequeueFadeIn.value = 0;
    }
  }, [!!prequeueDisplayInfo]);

  // Start/stop pulse animation based on prequeue status
  useEffect(() => {
    const isLoading = prequeueDisplayInfo && !prequeueReady && prequeueDisplayInfo.status !== 'failed';
    if (isLoading) {
      // Start pulsing - animate between 0.4 and 1
      prequeuePulseOpacity.value = 0.4;
      prequeuePulseOpacity.value = withRepeat(
        withTiming(1, { duration: 600, easing: Easing.inOut(Easing.ease) }),
        -1,
        true
      );
    } else {
      // Stop pulsing, set to full opacity
      cancelAnimation(prequeuePulseOpacity);
      prequeuePulseOpacity.value = 1;
    }
  }, [prequeueDisplayInfo?.status, prequeueReady]);

  // Build audio track options for selection modal
  const buildPrequeueAudioOptions = useCallback(() => {
    if (!prequeueDisplayInfo?.audioTracks) return [];
    return prequeueDisplayInfo.audioTracks.map((track) => ({
      id: String(track.index),
      label: `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`,
      description: track.codec?.toUpperCase(),
    }));
  }, [prequeueDisplayInfo?.audioTracks]);

  // Build subtitle track options for selection modal
  const buildPrequeueSubtitleOptions = useCallback(() => {
    if (!prequeueDisplayInfo?.subtitleTracks) return [];
    const options = [{ id: '-1', label: 'Off' }];
    options.push(
      ...prequeueDisplayInfo.subtitleTracks.map((track) => ({
        id: String(track.index),
        label: `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`,
        description: track.forced ? 'FORCED' : undefined,
      })),
    );
    return options;
  }, [prequeueDisplayInfo?.subtitleTracks]);

  // Computed current audio track ID (accounting for overrides)
  const currentAudioTrackId = useMemo(() => {
    if (trackOverrideAudio !== null) return String(trackOverrideAudio);
    const selected = prequeueDisplayInfo?.selectedAudioTrack;
    return selected !== undefined && selected >= 0
      ? String(selected)
      : prequeueDisplayInfo?.audioTracks?.[0]?.index !== undefined
        ? String(prequeueDisplayInfo.audioTracks[0].index)
        : null;
  }, [trackOverrideAudio, prequeueDisplayInfo?.selectedAudioTrack, prequeueDisplayInfo?.audioTracks]);

  // Computed current subtitle track ID (accounting for overrides)
  const currentSubtitleTrackId = useMemo(() => {
    if (trackOverrideSubtitle !== null) return String(trackOverrideSubtitle);
    const selected = prequeueDisplayInfo?.selectedSubtitleTrack;
    return selected !== undefined && selected >= 0 ? String(selected) : '-1';
  }, [trackOverrideSubtitle, prequeueDisplayInfo?.selectedSubtitleTrack]);

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

  // Fetch content preference for language override indicator
  useEffect(() => {
    // Use series identifier for series, or titleId for movies
    const contentId = isSeries ? seriesIdentifier : titleId;
    if (!activeUserId || !contentId) {
      setContentPreference(null);
      return;
    }

    let cancelled = false;

    apiService
      .getContentPreference(activeUserId, contentId)
      .then((pref) => {
        if (!cancelled) {
          setContentPreference(pref);
        }
      })
      .catch((error) => {
        if (!cancelled) {
          console.log('Unable to fetch content preference:', error);
          setContentPreference(null);
        }
      });

    return () => {
      cancelled = true;
    };
  }, [activeUserId, isSeries, seriesIdentifier, titleId]);

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
    // Reset track overrides when prequeue changes
    setTrackOverrideAudio(null);
    setTrackOverrideSubtitle(null);

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
    let debounceTimer: ReturnType<typeof setTimeout> | null = null;

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
          absoluteEpisodeNumber: targetEpisode?.absoluteEpisodeNumber,
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

    // Debounce prequeue for series to avoid rapid requests when user navigates between episodes
    // Movies start immediately since there's no episode navigation
    const prequeueDelay = isSeries && targetEpisode ? 500 : 0;

    if (prequeueDelay > 0) {
      console.log('[prequeue] Debouncing prequeue for', prequeueDelay, 'ms');
      debounceTimer = setTimeout(() => {
        if (!cancelled) {
          prequeuePromiseRef.current = initiatePrequeue();
        }
      }, prequeueDelay);
    } else {
      // Store the promise so play button can wait for it
      prequeuePromiseRef.current = initiatePrequeue();
    }

    return () => {
      cancelled = true;
      if (debounceTimer) {
        clearTimeout(debounceTimer);
      }
      prequeuePromiseRef.current = null;
    };
  }, [titleId, title, mediaType, isSeries, activeUserId, imdbId, yearNumber, activeEpisode, nextUpEpisode]);

  // Poll prequeue status until ready
  useEffect(() => {
    if (!prequeueId) {
      setPrequeueReady(false);
      setPrequeueDisplayInfo(null);
      return;
    }

    let cancelled = false;
    let timeoutId: ReturnType<typeof setTimeout>;

    const pollStatus = async () => {
      try {
        const response = await apiService.getPrequeueStatus(prequeueId);
        if (cancelled) return;

        // Always update display info to show current status
        setPrequeueDisplayInfo(response);

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
          setPrequeueDisplayInfo(null);
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
  }, [
    activeUserId,
    isSeries,
    activeEpisode,
    nextUpEpisode,
    seriesIdentifier,
    titleId,
    watchStatusItems,
    progressRefreshKey,
  ]);

  // Fetch progress for all episodes when series loads
  useEffect(() => {
    if (!activeUserId || !isSeries || !seriesIdentifier) {
      setEpisodeProgressMap(new Map());
      return;
    }

    let cancelled = false;

    const fetchAllProgress = async () => {
      try {
        const progressList = await apiService.listPlaybackProgress(activeUserId);
        if (cancelled) return;

        const progressMap = new Map<string, number>();
        // Match progress by seriesId OR by itemId prefix (more reliable)
        const itemIdPrefix = `${seriesIdentifier}:`;
        for (const progress of progressList) {
          if (progress.mediaType !== 'episode') continue;

          // Check if this progress belongs to this series
          const matchesSeriesId = progress.seriesId === seriesIdentifier;
          const matchesItemIdPrefix = progress.itemId?.startsWith(itemIdPrefix);

          if (matchesSeriesId || matchesItemIdPrefix) {
            // Get season/episode from progress fields or parse from itemId
            let seasonNum = progress.seasonNumber;
            let episodeNum = progress.episodeNumber;

            if ((!seasonNum || !episodeNum) && progress.itemId) {
              // Parse from itemId format: "seriesId:S01E02"
              const match = progress.itemId.match(/:S(\d+)E(\d+)$/i);
              if (match) {
                seasonNum = parseInt(match[1], 10);
                episodeNum = parseInt(match[2], 10);
              }
            }

            if (seasonNum && episodeNum) {
              const key = `${seasonNum}-${episodeNum}`;
              // Only include meaningful progress (between 5% and 95%)
              if (progress.percentWatched > 5 && progress.percentWatched < 95) {
                progressMap.set(key, Math.round(progress.percentWatched));
              }
            }
          }
        }
        setEpisodeProgressMap(progressMap);
      } catch (error) {
        if (!cancelled) {
          console.log('Unable to fetch episode progress:', error);
        }
      }
    };

    void fetchAllProgress();

    return () => {
      cancelled = true;
    };
  }, [activeUserId, isSeries, seriesIdentifier, progressRefreshKey]);

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

  // Fetch similar content ("More Like This") when TMDB ID is available
  useEffect(() => {
    if (!tmdbIdNumber) {
      setSimilarContent([]);
      setSimilarLoading(false);
      return;
    }

    let cancelled = false;
    setSimilarLoading(true);

    const fetchMediaType = isSeries ? 'series' : 'movie';
    apiService
      .getSimilarContent(fetchMediaType, tmdbIdNumber)
      .then((titles) => {
        if (cancelled) return;
        setSimilarContent(titles);
        setSimilarLoading(false);
      })
      .catch((error) => {
        if (cancelled) return;
        console.warn('[details] similar content fetch failed', error);
        setSimilarContent([]);
        setSimilarLoading(false);
      });

    return () => {
      cancelled = true;
    };
  }, [tmdbIdNumber, isSeries]);

  // Fetch series details for backdrop updates AND episodes (shared with SeriesEpisodes)
  useEffect(() => {
    if (!isSeries) {
      setSeriesDetailsData(null);
      setSeriesDetailsLoading(false);
      return;
    }

    const normalizedTitle = title?.trim();
    if (!normalizedTitle && !tvdbIdNumber && !titleId) {
      setSeriesDetailsData(null);
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
        // Store full SeriesDetails for sharing with SeriesEpisodes
        setSeriesDetailsData(details);
        setSeriesDetailsLoading(false);
      })
      .catch((error) => {
        if (cancelled) {
          return;
        }
        console.warn('[details] series metadata fetch failed', error);
        setSeriesDetailsData(null);
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

    // For series, pass the selected season number to get season-specific trailers
    const seasonNumber = isSeries && selectedSeason?.number ? selectedSeason.number : undefined;

    apiService
      .getTrailers({
        mediaType,
        titleId: titleId || undefined,
        name: title || undefined,
        year: yearNumber,
        tmdbId: tmdbIdNumber,
        tvdbId: tvdbIdNumber,
        imdbId: imdbId || undefined,
        season: seasonNumber,
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
  }, [imdbId, isSeries, mediaType, selectedSeason?.number, title, titleId, tmdbIdNumber, tvdbIdNumber, yearNumber]);

  // Prequeue YouTube trailers for 1080p playback on mobile
  useEffect(() => {
    // Only needed on mobile platforms
    if (Platform.OS === 'web') {
      setTrailerStreamUrl(null);
      setTrailerPrequeueId(null);
      setTrailerPrequeueStatus(null);
      return;
    }

    const trailerUrl = primaryTrailer?.url;
    if (!trailerUrl) {
      setTrailerStreamUrl(null);
      setTrailerPrequeueId(null);
      setTrailerPrequeueStatus(null);
      return;
    }

    // Only prequeue YouTube URLs (direct media URLs can be played directly)
    const isYouTube = trailerUrl.includes('youtube.com') || trailerUrl.includes('youtu.be');
    if (!isYouTube) {
      // Use direct URL for non-YouTube trailers
      setTrailerStreamUrl(trailerUrl);
      setTrailerPrequeueId(null);
      setTrailerPrequeueStatus(null);
      return;
    }

    // Start prequeue download for YouTube trailer (1080p merged video+audio)
    let cancelled = false;
    setTrailerPrequeueStatus('pending');
    setTrailerStreamUrl(null);

    apiService
      .prequeueTrailer(trailerUrl)
      .then((response) => {
        if (cancelled) return;
        setTrailerPrequeueId(response.id);
        setTrailerPrequeueStatus(response.status);
      })
      .catch((err) => {
        if (cancelled) return;
        console.warn('[trailer-prequeue] failed to start prequeue:', err);
        setTrailerPrequeueStatus('failed');
      });

    return () => {
      cancelled = true;
    };
  }, [primaryTrailer?.url]);

  // Poll for prequeue status until ready or failed
  useEffect(() => {
    if (!trailerPrequeueId || trailerPrequeueStatus === 'ready' || trailerPrequeueStatus === 'failed') {
      return;
    }

    let cancelled = false;
    const pollInterval = setInterval(async () => {
      if (cancelled) return;

      try {
        const status = await apiService.getTrailerPrequeueStatus(trailerPrequeueId);
        if (cancelled) return;

        setTrailerPrequeueStatus(status.status);

        if (status.status === 'ready') {
          // Trailer is ready - set the serve URL
          const serveUrl = apiService.getTrailerPrequeueServeUrl(trailerPrequeueId);
          setTrailerStreamUrl(serveUrl);
          clearInterval(pollInterval);
        } else if (status.status === 'failed') {
          console.warn('[trailer-prequeue] download failed:', status.error);
          clearInterval(pollInterval);
        }
      } catch (err) {
        if (cancelled) return;
        console.warn('[trailer-prequeue] status check failed:', err);
      }
    }, 1000); // Poll every second

    return () => {
      cancelled = true;
      clearInterval(pollInterval);
    };
  }, [trailerPrequeueId, trailerPrequeueStatus]);

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
  // Compute episode code for the episode that will be played (for TV series)
  const episodeToPlayCode = useMemo(() => {
    const episode = activeEpisode || nextUpEpisode;
    if (!isSeries || !episode) return null;
    const seasonStr = String(episode.seasonNumber).padStart(2, '0');
    const episodeStr = String(episode.episodeNumber).padStart(2, '0');
    return `S${seasonStr}E${episodeStr}`;
  }, [isSeries, activeEpisode, nextUpEpisode]);
  const watchNowLabel = Platform.isTV
    ? isSeries && episodeToPlayCode
      ? `${!hasWatchedEpisodes ? 'Play' : 'Up Next'} ${episodeToPlayCode}`
      : !isSeries || !hasWatchedEpisodes
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
    return parts.filter(Boolean).join(' • ');
  }, []);

  const getHomeReleaseIcon = useCallback((release?: Title['homeRelease']): keyof typeof Ionicons.glyphMap => {
    const type = release?.type?.toLowerCase();
    switch (type) {
      case 'digital':
        return 'cloud-outline';
      case 'physical':
        return 'disc-outline';
      case 'tv':
        return 'tv-outline';
      default:
        return 'home-outline';
    }
  }, []);

  const releaseRows = useMemo(() => {
    if (isSeries || !movieDetails) {
      return [];
    }
    const rows: { key: string; icon: keyof typeof Ionicons.glyphMap; value: string }[] = [];
    if (movieDetails.theatricalRelease) {
      const value = describeRelease(movieDetails.theatricalRelease);
      if (value) {
        rows.push({
          key: 'theatrical',
          icon: 'film-outline',
          value,
        });
      }
    }
    if (movieDetails.homeRelease) {
      const value = describeRelease(movieDetails.homeRelease);
      if (value) {
        rows.push({
          key: 'home',
          icon: getHomeReleaseIcon(movieDetails.homeRelease),
          value,
        });
      }
    }
    return rows;
  }, [describeRelease, getHomeReleaseIcon, isSeries, movieDetails]);

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

  // Show ratings skeleton while loading to prevent layout shift
  const isMetadataLoadingForSkeleton = isSeries ? seriesDetailsLoading : movieDetailsLoading;
  const shouldShowRatingsSkeleton = isMetadataLoadingForSkeleton && ratings.length === 0;

  // Get top 3 genres from movie or series details
  const genres = useMemo(() => {
    const rawGenres = isSeries ? (seriesDetailsForBackdrop?.genres ?? []) : (movieDetails?.genres ?? []);
    return rawGenres.slice(0, 3);
  }, [isSeries, movieDetails, seriesDetailsForBackdrop]);

  // Get certification (content rating) from movie or series details
  const certification = useMemo(() => {
    return isSeries ? seriesDetailsForBackdrop?.certification : movieDetails?.certification;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop]);

  // Placeholder release rows while loading (movies only)
  const releaseSkeletonRows = useMemo(() => {
    if (isSeries || !shouldShowReleaseSkeleton) return [];
    return [
      { key: 'theatrical-skeleton', icon: 'film-outline' as keyof typeof Ionicons.glyphMap, value: '—' },
      { key: 'home-skeleton', icon: 'home-outline' as keyof typeof Ionicons.glyphMap, value: '—' },
    ];
  }, [isSeries, shouldShowReleaseSkeleton]);

  const handleInitiatePlayback = useCallback(
    async (
      result: NZBResult,
      signal?: AbortSignal,
      overrides?: { useDebugPlayer?: boolean; trackOverrides?: ManualTrackOverrides },
    ) => {
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
          // Shuffle mode for random episode playback (use ref for synchronous access)
          shuffleMode: pendingShuffleModeRef.current || isShuffleMode,
          // Manual track selection override from manual selection modal
          trackOverrides: overrides?.trackOverrides,
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
      // TESTING: Force HLS for all native content (normally only HDR/TrueHD uses HLS)
      const needsHLS = Platform.OS !== 'web'; // hasAnyHDR || prequeueStatus.needsAudioTranscode;

      // Build stream URL
      let streamUrl: string;
      let hlsDuration: number | undefined;
      let hlsActualStartOffset: number | undefined;

      // Log the decision factors for HLS path
      const audioOverrideDiffersFromPrequeue =
        trackOverrideAudio !== null && trackOverrideAudio !== prequeueStatus.selectedAudioTrack;
      console.log('[prequeue] HLS decision factors:', {
        hasAnyHDR,
        needsAudioTranscode: prequeueStatus.needsAudioTranscode ?? false,
        needsHLS,
        hlsPlaylistUrl: prequeueStatus.hlsPlaylistUrl ?? 'null',
        hasStartOffset: typeof startOffset === 'number',
        startOffset,
        platformOS: Platform.OS,
        trackOverrideAudio,
        prequeueSelectedAudio: prequeueStatus.selectedAudioTrack,
        audioOverrideDiffersFromPrequeue,
        willUsePrequeueHLS:
          needsHLS &&
          prequeueStatus.hlsPlaylistUrl &&
          typeof startOffset !== 'number' &&
          !audioOverrideDiffersFromPrequeue,
        willCreateNewHLS:
          needsHLS &&
          (!prequeueStatus.hlsPlaylistUrl || typeof startOffset === 'number' || audioOverrideDiffersFromPrequeue),
      });

      // Check if we can use the pre-created HLS session
      // Skip if: no HLS URL, need resume offset, prequeue userId doesn't match current user,
      // or user has overridden the audio track to something different than what's baked into the HLS session
      const prequeueUserIdMatches = !prequeueStatus.userId || prequeueStatus.userId === activeUserId;
      const audioTrackMatchesPrequeue =
        trackOverrideAudio === null ||
        trackOverrideAudio === prequeueStatus.selectedAudioTrack;
      const canUsePreCreatedHLS =
        needsHLS &&
        prequeueStatus.hlsPlaylistUrl &&
        typeof startOffset !== 'number' &&
        prequeueUserIdMatches &&
        audioTrackMatchesPrequeue;

      // Track selected audio/subtitle tracks for passing to player (declared here so accessible in router.push)
      let selectedAudioTrack: number | undefined;
      let selectedSubtitleTrack: number | undefined;

      if (!prequeueUserIdMatches && prequeueStatus.hlsPlaylistUrl) {
        console.log('[prequeue] ⚠️ Pre-created HLS userId mismatch, will create new session', {
          prequeueUserId: prequeueStatus.userId,
          activeUserId,
        });
      }

      if (!audioTrackMatchesPrequeue && prequeueStatus.hlsPlaylistUrl) {
        console.log('[prequeue] ⚠️ Audio track override differs from prequeue, will create new HLS session', {
          trackOverrideAudio,
          prequeueSelectedAudio: prequeueStatus.selectedAudioTrack,
        });
      }

      if (canUsePreCreatedHLS) {
        // HDR/TrueHD content with HLS session already created by backend (no resume position)
        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const authToken = apiService.getAuthToken();
        streamUrl = `${baseUrl}${prequeueStatus.hlsPlaylistUrl}${authToken ? `?token=${encodeURIComponent(authToken)}` : ''}`;
        // Use duration from prequeue (extracted via ffprobe during prequeue processing)
        if (typeof prequeueStatus.duration === 'number' && prequeueStatus.duration > 0) {
          hlsDuration = prequeueStatus.duration;
          console.log('[prequeue] Using duration from prequeue:', hlsDuration);
        }
        // Extract prequeue-selected tracks so player knows what's baked into the HLS session
        // Use user override if set, otherwise fall back to prequeue-selected tracks
        selectedAudioTrack =
          trackOverrideAudio !== null
            ? trackOverrideAudio
            : prequeueStatus.selectedAudioTrack !== undefined && prequeueStatus.selectedAudioTrack >= 0
              ? prequeueStatus.selectedAudioTrack
              : undefined;
        selectedSubtitleTrack =
          trackOverrideSubtitle !== null
            ? trackOverrideSubtitle
            : prequeueStatus.selectedSubtitleTrack !== undefined && prequeueStatus.selectedSubtitleTrack >= 0
              ? prequeueStatus.selectedSubtitleTrack
              : undefined;
        console.log('[prequeue] ✅ Using PRE-CREATED HLS stream URL:', streamUrl, {
          selectedAudioTrack,
          selectedSubtitleTrack,
          trackOverrideAudio,
          trackOverrideSubtitle,
        });
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
            : prequeueStatus.hasHdr10
              ? 'HDR10'
              : 'SDR (testing)';
        console.log(`[prequeue] ${contentType} detected, creating HLS session (${reason})...`);
        setSelectionInfo(`Creating HLS session for ${contentType}...`);

        try {
          // Use user override if set, otherwise fall back to prequeue-selected tracks
          selectedAudioTrack =
            trackOverrideAudio !== null
              ? trackOverrideAudio
              : prequeueStatus.selectedAudioTrack !== undefined && prequeueStatus.selectedAudioTrack >= 0
                ? prequeueStatus.selectedAudioTrack
                : undefined;
          selectedSubtitleTrack =
            trackOverrideSubtitle !== null
              ? trackOverrideSubtitle
              : prequeueStatus.selectedSubtitleTrack !== undefined && prequeueStatus.selectedSubtitleTrack >= 0
                ? prequeueStatus.selectedSubtitleTrack
                : undefined;

          // Log if using override or prequeue-selected tracks
          if (selectedAudioTrack !== undefined || selectedSubtitleTrack !== undefined) {
            console.log(
              `[prequeue] Using tracks: audio=${selectedAudioTrack}, subtitle=${selectedSubtitleTrack}`,
              { trackOverrideAudio, trackOverrideSubtitle },
            );
          }

          // Only fetch metadata if neither override nor prequeue provided track selection
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
          hlsActualStartOffset = hlsResponse.actualStartOffset;
          console.log(
            '[prequeue] Created HLS session, using URL:',
            streamUrl,
            'actualStartOffset:',
            hlsActualStartOffset,
          );
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
        // Use duration from prequeue (extracted via ffprobe during prequeue processing)
        if (typeof prequeueStatus.duration === 'number' && prequeueStatus.duration > 0) {
          hlsDuration = prequeueStatus.duration;
          console.log('[prequeue] Using duration from prequeue:', hlsDuration);
        }
        console.log('[prequeue] Using direct stream URL:', streamUrl);

        // SDR path: Start subtitle extraction with correct offset (lazy extraction)
        // This is called now that we know the user's resume position
        if (prequeueStatus.prequeueId) {
          try {
            const subtitleResult = await apiService.startPrequeueSubtitles(prequeueStatus.prequeueId, startOffset ?? 0);
            if (subtitleResult.subtitleSessions && Object.keys(subtitleResult.subtitleSessions).length > 0) {
              // Update prequeueStatus with the new subtitle sessions
              prequeueStatus.subtitleSessions = subtitleResult.subtitleSessions;
              console.log(
                '[prequeue] Started subtitle extraction for',
                Object.keys(subtitleResult.subtitleSessions).length,
                'tracks at offset',
                startOffset ?? 0,
              );
            }
          } catch (subtitleError) {
            // Non-fatal - subtitles will just not be available
            console.warn('[prequeue] Failed to start subtitle extraction:', subtitleError);
          }
        }
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
          ...(typeof hlsActualStartOffset === 'number' ? { actualStartOffset: String(hlsActualStartOffset) } : {}),
          ...(typeof hlsDuration === 'number' ? { durationHint: String(hlsDuration) } : {}),
          ...(titleId ? { titleId } : {}),
          ...(imdbId ? { imdbId } : {}),
          ...(tvdbId ? { tvdbId } : {}),
          // Pass pre-extracted subtitle sessions for SDR content (VLC path)
          ...(prequeueStatus.subtitleSessions && Object.keys(prequeueStatus.subtitleSessions).length > 0
            ? { preExtractedSubtitles: JSON.stringify(Object.values(prequeueStatus.subtitleSessions)) }
            : {}),
          // Shuffle mode for random episode playback (use ref for synchronous access)
          ...(pendingShuffleModeRef.current || isShuffleMode ? { shuffleMode: '1' } : {}),
          // Pass prequeue-selected tracks so player knows what's baked into the HLS session
          ...(selectedAudioTrack !== undefined && selectedAudioTrack >= 0
            ? { preselectedAudioTrack: String(selectedAudioTrack) }
            : {}),
          ...(selectedSubtitleTrack !== undefined && selectedSubtitleTrack >= 0
            ? { preselectedSubtitleTrack: String(selectedSubtitleTrack) }
            : {}),
          // AIOStreams passthrough format data for info modal
          ...(prequeueStatus.passthroughName ? { passthroughName: prequeueStatus.passthroughName } : {}),
          ...(prequeueStatus.passthroughDescription
            ? { passthroughDescription: prequeueStatus.passthroughDescription }
            : {}),
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
      isShuffleMode,
      trackOverrideAudio,
      trackOverrideSubtitle,
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
      targetEpisode?: { seasonNumber: number; episodeNumber: number; airedDate?: string };
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
          // Check if episode hasn't aired yet and show a friendlier message
          if (targetEpisode?.airedDate && isEpisodeUnreleased(targetEpisode.airedDate)) {
            setSelectionError(formatUnreleasedMessage(friendlyLabel, targetEpisode.airedDate));
          } else {
            setSelectionError(`No results returned for ${friendlyLabel}.`);
          }
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
              // Check if episode hasn't aired yet and show a friendlier message
              if (targetEpisode?.airedDate && isEpisodeUnreleased(targetEpisode.airedDate)) {
                setSelectionError(formatUnreleasedMessage(friendlyLabel, targetEpisode.airedDate));
              } else {
                const failureSummary = lastHealthFailureReason
                  ? `All automatic releases failed health checks (last issue: ${lastHealthFailureReason}). Try manual selection or pick another release.`
                  : 'All automatic releases failed health checks. Try manual selection or pick another release.';
                setSelectionError(failureSummary);
              }
              setSelectionInfo(null);
              return;
            }

            console.log(`🛑 Non-health failure error, stopping attempts.`);
            setSelectionInfo(null);
            setSelectionError(message || `Unable to start playback for ${friendlyLabel}.`);
            return;
          }
        }

        // Check if episode hasn't aired yet and show a friendlier message
        if (targetEpisode?.airedDate && isEpisodeUnreleased(targetEpisode.airedDate)) {
          setSelectionError(formatUnreleasedMessage(friendlyLabel, targetEpisode.airedDate));
        } else if (lastHealthFailure) {
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

        // Check for timeout errors and show a helpful message
        if (isTimeoutError(err)) {
          console.error(`⚠️ Search timed out for ${friendlyLabel}:`, err);
          setSelectionError(getTimeoutMessage(err));
          setSelectionInfo(null);
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

  const handleEpisodeSelect = useCallback(
    (episode: SeriesEpisode) => {
      setActiveEpisode(episode);
      setSelectionError(null);
      setSelectionInfo(null);
      // Clear any resume position from previous episode
      setCurrentProgress(null);
      // Update selected season if episode is from a different season
      setSelectedSeason((currentSeason) => {
        if (currentSeason?.number !== episode.seasonNumber) {
          const matchingSeason = seasons.find((s) => s.number === episode.seasonNumber);
          return matchingSeason ?? currentSeason;
        }
        return currentSeason;
      });
    },
    [seasons],
  );

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

  const handlePlayEpisode = useCallback(
    async (episode: SeriesEpisode) => {
      setActiveEpisode(episode);

      const playAction = async () => {
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
          targetEpisode: { seasonNumber: episode.seasonNumber, episodeNumber: episode.episodeNumber, airedDate: episode.airedDate },
        });
      };

      // Skip resume check for shuffle mode - always start from beginning
      const isShuffling = pendingShuffleModeRef.current;

      // Check for resume progress directly using the episode's itemId
      if (!isShuffling && activeUserId && seriesIdentifier) {
        const episodeItemId = `${seriesIdentifier}:S${String(episode.seasonNumber).padStart(2, '0')}E${String(episode.episodeNumber).padStart(2, '0')}`;
        try {
          const progress = await apiService.getPlaybackProgress(activeUserId, 'episode', episodeItemId);
          if (progress && progress.percentWatched > 5 && progress.percentWatched < 95) {
            // Show resume modal
            setCurrentProgress(progress);
            setPendingPlaybackAction(() => async (startOffset?: number) => {
              if (startOffset !== undefined) {
                pendingStartOffsetRef.current = startOffset;
              }
              await playAction();
            });
            setResumeModalVisible(true);
            return;
          }
        } catch (error) {
          console.warn('Failed to check playback progress:', error);
        }
      }

      // No resume needed (or shuffle mode), play directly
      await showLoadingScreenIfEnabled();
      await playAction();
    },
    [resolveAndPlay, title, activeUserId, seriesIdentifier, showLoadingScreenIfEnabled],
  );

  // Keep the play episode ref in sync with the callback
  useEffect(() => {
    handlePlayEpisodeRef.current = handlePlayEpisode;
  }, [handlePlayEpisode]);

  // Shuffle play - pick a random episode and play it (excludes season 0/specials)
  const handleShufflePlay = useCallback(() => {
    // Filter out season 0 (specials) from shuffle
    const shuffleableEpisodes = allEpisodes.filter((ep) => ep.seasonNumber !== 0);
    if (shuffleableEpisodes.length === 0) return;
    const randomIndex = Math.floor(Math.random() * shuffleableEpisodes.length);
    const randomEpisode = shuffleableEpisodes[randomIndex];
    // Set both state (for persistence) and ref (for synchronous access)
    setIsShuffleMode(true);
    pendingShuffleModeRef.current = true;
    // Select the season containing the random episode
    const matchingSeason = seasons.find((s) => s.number === randomEpisode.seasonNumber);
    if (matchingSeason) {
      setSelectedSeason(matchingSeason);
    }
    setActiveEpisode(randomEpisode);
    handlePlayEpisode(randomEpisode);
  }, [allEpisodes, seasons, handlePlayEpisode]);

  // Shuffle play current season only - pick a random episode from selected season
  const handleShuffleSeasonPlay = useCallback(() => {
    const seasonEpisodes = selectedSeason?.episodes ?? [];
    if (seasonEpisodes.length === 0) return;
    const randomIndex = Math.floor(Math.random() * seasonEpisodes.length);
    const randomEpisode = seasonEpisodes[randomIndex];
    // Set both state (for persistence) and ref (for synchronous access)
    setIsShuffleMode(true);
    pendingShuffleModeRef.current = true;
    setActiveEpisode(randomEpisode);
    handlePlayEpisode(randomEpisode);
  }, [selectedSeason?.episodes, handlePlayEpisode]);

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
          targetEpisode: { seasonNumber: episodeToPlay.seasonNumber, episodeNumber: episodeToPlay.episodeNumber, airedDate: episodeToPlay.airedDate },
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
          targetEpisode: { seasonNumber: episodeToPlay.seasonNumber, episodeNumber: episodeToPlay.episodeNumber, airedDate: episodeToPlay.airedDate },
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
    async (result: NZBResult, trackOverrides?: ManualTrackOverrides) => {
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

      // Log track overrides if provided
      if (trackOverrides) {
        console.log('[manual] Using track overrides:', trackOverrides);
      }

      // Define the playback action to be wrapped in resume check
      const playAction = async () => {
        // Show loading screen now that user has confirmed playback
        await showLoadingScreenIfEnabled();

        try {
          await handleInitiatePlayback(result, abortController.signal, { trackOverrides });

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

  const handleViewCollection = useCallback(() => {
    if (!movieDetails?.collection) return;
    router.push({
      pathname: '/watchlist',
      params: {
        collection: String(movieDetails.collection.id),
        collectionName: encodeURIComponent(movieDetails.collection.name),
      },
    });
  }, [movieDetails?.collection, router]);

  const handleSimilarTitlePress = useCallback(
    (item: Title) => {
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
          year: item.year ? String(item.year) : '',
        },
      });
    },
    [router],
  );

  const handleCastMemberPress = useCallback(
    (actor: CastMember) => {
      router.push({
        pathname: '/watchlist',
        params: {
          person: String(actor.id),
          personName: encodeURIComponent(actor.name),
        },
      });
    },
    [router],
  );

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
        // Check if episode hasn't aired yet and show a friendlier message
        if (episodeToSelect && isEpisodeUnreleased(episodeToSelect.airedDate)) {
          const baseTitle = title.trim() || title;
          const episodeCode = `S${padNumber(episodeToSelect.seasonNumber)}E${padNumber(episodeToSelect.episodeNumber)}`;
          const episodeLabel = `${baseTitle} ${episodeCode}`;
          setManualError(formatUnreleasedMessage(episodeLabel, episodeToSelect.airedDate));
        } else {
          setManualError('No results available yet for manual selection.');
        }
      }
    } catch (err) {
      // Check for timeout errors and show a helpful message
      if (isTimeoutError(err)) {
        console.error('⚠️ Manual fetch timed out:', err);
        setManualError(getTimeoutMessage(err));
      } else {
        const message = err instanceof Error ? err.message : 'Failed to load results.';
        console.error('⚠️ Manual fetch failed:', err);
        setManualError(message);
      }
    } finally {
      setManualLoading(false);
    }
  }, [activeEpisode, nextUpEpisode, fetchIndexerResults, getEpisodeSearchContext, manualLoading, title]);

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
          // Check if episode hasn't aired yet and show a friendlier message
          if (isEpisodeUnreleased(episode.airedDate)) {
            const baseTitle = title.trim() || title;
            const episodeCode = `S${padNumber(episode.seasonNumber)}E${padNumber(episode.episodeNumber)}`;
            const episodeLabel = `${baseTitle} ${episodeCode}`;
            setManualError(formatUnreleasedMessage(episodeLabel, episode.airedDate));
          } else {
            setManualError('No results available yet for manual selection.');
          }
        }
      } catch (err) {
        // Check for timeout errors and show a helpful message
        if (isTimeoutError(err)) {
          console.error('⚠️ Manual fetch timed out:', err);
          setManualError(getTimeoutMessage(err));
        } else {
          const message = err instanceof Error ? err.message : 'Failed to load results.';
          console.error('⚠️ Manual fetch failed:', err);
          setManualError(message);
        }
      } finally {
        setManualLoading(false);
      }
    },
    [fetchIndexerResults, getEpisodeSearchContext, manualLoading, title],
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

  // Mobile-specific season select: just update the season without opening episode modal
  const handleMobileSeasonSelect = useCallback((season: SeriesSeason) => {
    setSelectedSeason(season);
    setSeasonSelectorVisible(false);
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
      <View
        style={[styles.topContent, isTV && styles.topContentTV, isMobile && styles.topContentMobile]}
        onLayout={
          isTV
            ? (e) => {
                const height = e.nativeEvent.layout.height;
                if (height > 0 && height !== tvTopContentHeight) {
                  setTvTopContentHeight(height);
                }
              }
            : undefined
        }>
        <View style={[styles.titleRow, { overflow: 'visible' }]}>
          {logoUrl && logoDimensions ? (
            <View style={[{ padding: 12, marginLeft: -12, overflow: 'visible' }, logoGlowStyle]}>
              <RNImage
                source={{ uri: logoUrl }}
                style={logoStyle}
                resizeMode="contain"
              />
            </View>
          ) : (
            <Text style={styles.title}>{title}</Text>
          )}
        </View>
        {(ratings.length > 0 || shouldShowRatingsSkeleton) && (
          <View style={styles.ratingsRow}>
            {ratings.length > 0 ? (
              ratings.map((rating) => {
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
              })
            ) : (
              <Text style={styles.ratingValue}>—</Text>
            )}
          </View>
        )}
        {(certification || genres.length > 0) && (
          <View style={styles.genresRow}>
            {certification && (
              <View style={styles.genreBadge}>
                <Text style={styles.genreText}>{certification}</Text>
              </View>
            )}
            {certification && genres.length > 0 && (
              <Text style={[styles.genreText, { alignSelf: 'center', fontWeight: '700', fontSize: 16 * tvScale }]}>|</Text>
            )}
            {genres.map((genre) => (
              <View key={genre} style={styles.genreBadge}>
                <Text style={styles.genreText}>{genre}</Text>
              </View>
            ))}
          </View>
        )}
        {contentPreference && (contentPreference.audioLanguage || contentPreference.subtitleLanguage) && (
          <View
            style={{
              flexDirection: 'row',
              flexWrap: 'wrap',
              gap: 8 * tvScale,
              marginTop: 8 * tvScale,
              marginBottom: 8 * tvScale,
              marginLeft: tvScale * 48,
            }}>
            {contentPreference.audioLanguage && (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  backgroundColor: theme.colors.background.elevated,
                  paddingHorizontal: 10 * tvScale,
                  paddingVertical: 4 * tvScale,
                  borderRadius: 4 * tvScale,
                }}>
                <Ionicons
                  name="volume-high"
                  size={14 * tvScale}
                  color={theme.colors.text.secondary}
                  style={{ marginRight: 4 * tvScale }}
                />
                <Text style={{ color: theme.colors.text.secondary, fontSize: 12 * tvScale }}>
                  {contentPreference.audioLanguage.toUpperCase()}
                </Text>
              </View>
            )}
            {contentPreference.subtitleLanguage && (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  backgroundColor: theme.colors.background.elevated,
                  paddingHorizontal: 10 * tvScale,
                  paddingVertical: 4 * tvScale,
                  borderRadius: 4 * tvScale,
                }}>
                <Ionicons
                  name="text"
                  size={14 * tvScale}
                  color={theme.colors.text.secondary}
                  style={{ marginRight: 4 * tvScale }}
                />
                <Text style={{ color: theme.colors.text.secondary, fontSize: 12 * tvScale }}>
                  {contentPreference.subtitleLanguage.toUpperCase()}
                </Text>
              </View>
            )}
            {contentPreference.subtitleMode === 'off' && !contentPreference.subtitleLanguage && (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  backgroundColor: theme.colors.background.elevated,
                  paddingHorizontal: 10 * tvScale,
                  paddingVertical: 4 * tvScale,
                  borderRadius: 4 * tvScale,
                }}>
                <Ionicons
                  name="text"
                  size={14 * tvScale}
                  color={theme.colors.text.secondary}
                  style={{ marginRight: 4 * tvScale }}
                />
                <Text style={{ color: theme.colors.text.secondary, fontSize: 12 * tvScale }}>OFF</Text>
              </View>
            )}
          </View>
        )}
        {(releaseRows.length > 0 || shouldShowReleaseSkeleton || releaseErrorMessage || (!isSeries && movieDetails?.runtimeMinutes)) && (
          <View style={styles.releaseInfoRow}>
            {(releaseRows.length > 0 ? releaseRows : releaseSkeletonRows).map((row) => (
              <View key={row.key} style={styles.releaseInfoItem}>
                <Ionicons name={row.icon} size={14 * tvScale} color={theme.colors.text.secondary} style={styles.releaseInfoIcon} />
                <Text style={styles.releaseInfoValue}>{row.value}</Text>
              </View>
            ))}
            {!isSeries && movieDetails?.runtimeMinutes && (
              <View style={styles.releaseInfoItem}>
                <Ionicons name="time-outline" size={14 * tvScale} color={theme.colors.text.secondary} style={styles.releaseInfoIcon} />
                <Text style={styles.releaseInfoValue}>{movieDetails.runtimeMinutes} min</Text>
              </View>
            )}
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
                {displayDescription}
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
                {displayDescription}
              </Text>
              {/* Visible animated container */}
              <Animated.View
                style={[{ overflow: 'hidden' }, collapsedHeight > 0 ? { height: descriptionHeight } : undefined]}>
                <Text
                  style={[styles.description, { marginBottom: 0 }]}
                  numberOfLines={isDescriptionExpanded ? undefined : 4}>
                  {displayDescription}
                </Text>
              </Animated.View>
            </View>
            {expandedHeight > collapsedHeight && (
              <Text style={styles.descriptionToggle}>{isDescriptionExpanded ? 'Show less' : 'More'}</Text>
            )}
          </Pressable>
        ) : (
          <Text style={styles.description}>{displayDescription}</Text>
        )}
      </View>
      <SpatialNavigationNode
        orientation="vertical"
        focusKey="details-content-column"
        onActive={() => console.log('[Details NAV DEBUG] details-content-column ACTIVE')}
        onInactive={() => console.log('[Details NAV DEBUG] details-content-column INACTIVE')}>
        <View style={[styles.bottomContent, isMobile && styles.mobileBottomContent]}>
          {/* Action Row - moved above episode carousel for TV */}
          <SpatialNavigationNode
            orientation="horizontal"
            focusKey="details-action-row"
            onActive={() => {
              console.log('[Details NAV DEBUG] details-action-row ACTIVE');
              handleTVFocusAreaChange('actions');
            }}
            onInactive={() => console.log('[Details NAV DEBUG] details-action-row INACTIVE')}>
            <View style={[styles.actionRow, useCompactActionLayout && styles.compactActionRow]}>
              {Platform.isTV && TVActionButton ? (
                <TVActionButton
                  text={watchNowLabel}
                  icon="play"
                  onSelect={handleWatchNow}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  disabled={isResolving || (isSeries && episodesLoading)}
                  loading={isResolving || (isSeries && episodesLoading)}
                  showReadyPip={prequeueReady}
                  autoFocus
                />
              ) : (
                <FocusablePressable
                  focusKey="watch-now"
                  text={!useCompactActionLayout ? watchNowLabel : undefined}
                  icon={useCompactActionLayout || Platform.isTV ? 'play' : undefined}
                  accessibilityLabel={watchNowLabel}
                  onSelect={handleWatchNow}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  disabled={isResolving || (isSeries && episodesLoading)}
                  loading={isResolving || (isSeries && episodesLoading)}
                  style={useCompactActionLayout ? styles.iconActionButton : styles.primaryActionButton}
                  showReadyPip={prequeueReady}
                  autoFocus={Platform.isTV}
                />
              )}
              {Platform.isTV && TVActionButton ? (
                <TVActionButton
                  text={manualSelectLabel}
                  icon="search"
                  onSelect={handleManualSelect}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  disabled={isSeries && episodesLoading}
                />
              ) : (
                <FocusablePressable
                  focusKey="manual-select"
                  text={!useCompactActionLayout ? manualSelectLabel : undefined}
                  icon={useCompactActionLayout || Platform.isTV ? 'search' : undefined}
                  accessibilityLabel={manualSelectLabel}
                  onSelect={handleManualSelect}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  disabled={isSeries && episodesLoading}
                  style={useCompactActionLayout ? styles.iconActionButton : styles.manualActionButton}
                />
              )}
              {shouldShowDebugPlayerButton &&
                (Platform.isTV && TVActionButton ? (
                  <TVActionButton
                    text="Debug Player"
                    icon="bug"
                    onSelect={handleLaunchDebugPlayer}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                    disabled={isResolving || (isSeries && episodesLoading)}
                  />
                ) : (
                  <FocusablePressable
                    focusKey="debug-player"
                    text={!useCompactActionLayout ? 'Debug Player' : undefined}
                    icon={useCompactActionLayout || Platform.isTV ? 'bug' : undefined}
                    accessibilityLabel="Launch debug player overlay"
                    onSelect={handleLaunchDebugPlayer}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                    disabled={isResolving || (isSeries && episodesLoading)}
                    style={useCompactActionLayout ? styles.iconActionButton : styles.debugActionButton}
                  />
                ))}
              {isSeries &&
                (Platform.isTV && TVActionButton ? (
                  <TVActionButton
                    text="Select"
                    icon="list"
                    onSelect={() => setSeasonSelectorVisible(true)}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                  />
                ) : (
                  <FocusablePressable
                    focusKey="select-episode"
                    text={!useCompactActionLayout ? 'Select' : undefined}
                    icon={useCompactActionLayout || Platform.isTV ? 'list' : undefined}
                    accessibilityLabel="Select Episode"
                    onSelect={() => setSeasonSelectorVisible(true)}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                    style={useCompactActionLayout ? styles.iconActionButton : styles.manualActionButton}
                  />
                ))}
              {isSeries &&
                (Platform.isTV && TVActionButton ? (
                  <TVActionButton
                    text="Shuffle"
                    icon="shuffle"
                    onSelect={handleShufflePlay}
                    onLongSelect={handleShuffleSeasonPlay}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                    disabled={episodesLoading || allEpisodes.length === 0}
                  />
                ) : (
                  <FocusablePressable
                    focusKey="shuffle-play"
                    text={!useCompactActionLayout ? 'Shuffle' : undefined}
                    icon={useCompactActionLayout || Platform.isTV ? 'shuffle' : undefined}
                    accessibilityLabel="Shuffle play random episode"
                    onSelect={handleShufflePlay}
                    onLongPress={handleShuffleSeasonPlay}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                    style={useCompactActionLayout ? styles.iconActionButton : styles.manualActionButton}
                    disabled={episodesLoading || allEpisodes.length === 0}
                  />
                ))}
              {Platform.isTV && TVActionButton ? (
                <TVActionButton
                  text={watchlistBusy ? 'Saving...' : watchlistButtonLabel}
                  icon={isWatchlisted ? 'bookmark' : 'bookmark-outline'}
                  onSelect={handleToggleWatchlist}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  loading={watchlistBusy}
                  disabled={!canToggleWatchlist || watchlistBusy}
                />
              ) : (
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
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  loading={watchlistBusy}
                  style={[
                    useCompactActionLayout ? styles.iconActionButton : styles.watchlistActionButton,
                    isWatchlisted && styles.watchlistActionButtonActive,
                  ]}
                  disabled={!canToggleWatchlist || watchlistBusy}
                />
              )}
              {Platform.isTV && TVActionButton ? (
                <TVActionButton
                  text={watchlistBusy ? 'Saving...' : watchStateButtonLabel}
                  icon={isWatched ? 'eye' : 'eye-outline'}
                  onSelect={handleToggleWatched}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  loading={watchlistBusy}
                  disabled={watchlistBusy}
                />
              ) : (
                <FocusablePressable
                  focusKey="toggle-watched"
                  text={!useCompactActionLayout ? (watchlistBusy ? 'Saving...' : watchStateButtonLabel) : undefined}
                  icon={useCompactActionLayout || Platform.isTV ? (isWatched ? 'eye' : 'eye-outline') : undefined}
                  accessibilityLabel={watchlistBusy ? 'Saving watched state' : watchStateButtonLabel}
                  onSelect={handleToggleWatched}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  loading={watchlistBusy}
                  style={[
                    useCompactActionLayout ? styles.iconActionButton : styles.watchStateButton,
                    isWatched && styles.watchStateButtonActive,
                  ]}
                  disabled={watchlistBusy}
                />
              )}
              {/* Trailer button */}
              {(trailersLoading || hasAvailableTrailer) &&
                (Platform.isTV && TVActionButton ? (
                  <TVActionButton
                    text={trailerButtonLabel}
                    icon="videocam"
                    onSelect={handleWatchTrailer}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                    loading={trailersLoading}
                    disabled={trailerButtonDisabled}
                  />
                ) : (
                  <FocusablePressable
                    focusKey="watch-trailer"
                    text={!useCompactActionLayout ? trailerButtonLabel : undefined}
                    icon={useCompactActionLayout || Platform.isTV ? 'videocam' : undefined}
                    accessibilityLabel={trailerButtonLabel}
                    onSelect={handleWatchTrailer}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                    loading={trailersLoading}
                    style={useCompactActionLayout ? styles.iconActionButton : styles.trailerActionButton}
                    disabled={trailerButtonDisabled}
                  />
                ))}
              {/* Collection button - show only for movies that are part of a collection */}
              {!isSeries && movieDetails?.collection &&
                (Platform.isTV && TVActionButton ? (
                  <TVActionButton
                    text={movieDetails.collection.name}
                    icon="albums"
                    onSelect={handleViewCollection}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                  />
                ) : (
                  <FocusablePressable
                    focusKey="view-collection"
                    text={!useCompactActionLayout ? movieDetails.collection.name : undefined}
                    icon={useCompactActionLayout || Platform.isTV ? 'albums' : undefined}
                    accessibilityLabel={`View ${movieDetails.collection.name}`}
                    onSelect={handleViewCollection}
                    onFocus={() => handleTVFocusAreaChange('actions')}
                    style={useCompactActionLayout ? styles.iconActionButton : styles.trailerActionButton}
                  />
                ))}
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
          {/* Prequeue stream info display - always render container to reserve space */}
          <Animated.View style={[styles.prequeueInfoContainer, styles.prequeueInfoMinHeight, prequeuePulseStyle]}>
            {prequeueDisplayInfo && (
              <>
                {/* Status message for early stages */}
                {(prequeueDisplayInfo.status === 'queued' || prequeueDisplayInfo.status === 'searching') && (
                  <Text style={styles.prequeueFilename}>
                    {prequeueDisplayInfo.status === 'queued' && 'Queued...'}
                    {prequeueDisplayInfo.status === 'searching' && 'Searching for streams...'}
                  </Text>
                )}
                {/* Failed status */}
                {prequeueDisplayInfo.status === 'failed' && (
                  <Text style={styles.prequeueFilename}>
                    Failed: {prequeueDisplayInfo.error || 'Unknown error'}
                  </Text>
                )}
                {/* Show filename once available (resolving, probing, or ready) */}
                {(prequeueDisplayInfo.status === 'resolving' || prequeueDisplayInfo.status === 'probing' || prequeueDisplayInfo.status === 'ready') && (
                  <>
                    <Text style={styles.prequeueFilename} numberOfLines={1} ellipsizeMode="middle">
                      {prequeueDisplayInfo.displayName ||
                        prequeueDisplayInfo.passthroughName ||
                        (prequeueDisplayInfo.streamPath?.split('/').pop()) ||
                        (prequeueDisplayInfo.status === 'resolving' ? 'Resolving stream...' : 'Analyzing media...')}
                    </Text>
                    {/* Show loading state for tracks during probing */}
                    {(prequeueDisplayInfo.status === 'probing' || prequeueDisplayInfo.status === 'resolving') &&
                     !prequeueDisplayInfo.audioTracks?.length && (
                      <Text style={styles.prequeueLoadingText}>Analyzing tracks...</Text>
                    )}
                    {/* Audio & Subtitle tracks on one line */}
                    {(prequeueDisplayInfo.audioTracks?.length || prequeueDisplayInfo.subtitleTracks?.length) ? (
                      <View style={styles.prequeueTrackRow}>
                        {/* Audio track - tappable when multiple tracks available */}
                        {prequeueDisplayInfo.audioTracks && prequeueDisplayInfo.audioTracks.length > 0 && (
                          <Pressable
                            onPress={() => setShowAudioTrackModal(true)}
                            disabled={prequeueDisplayInfo.audioTracks.length <= 1}
                            style={styles.prequeueTrackPressable}
                          >
                            <Ionicons name="volume-high" size={16 * tvScale} color={theme.colors.text.secondary} />
                            <Text style={styles.prequeueTrackValue} numberOfLines={1}>
                              {(() => {
                                const selectedIdx = trackOverrideAudio ?? prequeueDisplayInfo.selectedAudioTrack;
                                const track = selectedIdx !== undefined && selectedIdx >= 0
                                  ? prequeueDisplayInfo.audioTracks?.find((t) => t.index === selectedIdx)
                                  : prequeueDisplayInfo.audioTracks?.[0];
                                if (!track) return 'Default';
                                return `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`;
                              })()}
                            </Text>
                            {(() => {
                              const selectedIdx = trackOverrideAudio ?? prequeueDisplayInfo.selectedAudioTrack;
                              const track = selectedIdx !== undefined && selectedIdx >= 0
                                ? prequeueDisplayInfo.audioTracks?.find((t) => t.index === selectedIdx)
                                : prequeueDisplayInfo.audioTracks?.[0];
                              if (track?.codec) {
                                return (
                                  <Text style={[styles.prequeueTrackBadge, styles.prequeueTrackCodecBadge]}>
                                    {track.codec.toUpperCase()}
                                  </Text>
                                );
                              }
                              return null;
                            })()}
                            {prequeueDisplayInfo.audioTracks.length > 1 && (
                              <Ionicons name="chevron-forward" size={12 * tvScale} color={theme.colors.text.muted} />
                            )}
                          </Pressable>
                        )}
                        {/* Separator */}
                        {(prequeueDisplayInfo.audioTracks?.length ?? 0) > 0 && (prequeueDisplayInfo.subtitleTracks?.length ?? 0) > 0 && (
                          <Text style={styles.prequeueTrackSeparator}>•</Text>
                        )}
                        {/* Subtitle track - tappable when subtitles available */}
                        {prequeueDisplayInfo.subtitleTracks && prequeueDisplayInfo.subtitleTracks.length > 0 && (
                          <Pressable
                            onPress={() => setShowSubtitleTrackModal(true)}
                            style={styles.prequeueTrackPressable}
                          >
                            <Ionicons name="text" size={16 * tvScale} color={theme.colors.text.secondary} />
                            <Text style={styles.prequeueTrackValue} numberOfLines={1}>
                              {(() => {
                                const selectedIdx = trackOverrideSubtitle ?? prequeueDisplayInfo.selectedSubtitleTrack;
                                if (selectedIdx === undefined || selectedIdx < 0) return 'Off';
                                const track = prequeueDisplayInfo.subtitleTracks?.find((t) => t.index === selectedIdx);
                                if (!track) return 'Off';
                                return `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`;
                              })()}
                            </Text>
                            {(() => {
                              const selectedIdx = trackOverrideSubtitle ?? prequeueDisplayInfo.selectedSubtitleTrack;
                              if (selectedIdx === undefined || selectedIdx < 0) return null;
                              const track = prequeueDisplayInfo.subtitleTracks?.find((t) => t.index === selectedIdx);
                              if (!track) return null;
                              if (track.forced) {
                                return (
                                  <Text style={[styles.prequeueTrackBadge, styles.prequeueTrackForcedBadge]}>
                                    FORCED
                                  </Text>
                                );
                              }
                              if (track.title?.toLowerCase().includes('sdh') || track.title?.toLowerCase().includes('hearing')) {
                                return (
                                  <Text style={[styles.prequeueTrackBadge, styles.prequeueTrackSDHBadge]}>
                                    SDH
                                  </Text>
                                );
                              }
                              return null;
                            })()}
                            <Ionicons name="chevron-forward" size={12 * tvScale} color={theme.colors.text.muted} />
                          </Pressable>
                        )}
                      </View>
                    ) : null}
                </>
              )}
            </>
          )}
          </Animated.View>
          {/* TV Episode Carousel - always render wrapper node for series to maintain navigation order
              (nodes register in DOM order, so late-loading content would otherwise end up at the end) */}
          {Platform.isTV && isSeries && (
            <SpatialNavigationNode
              orientation="vertical"
              focusKey="episode-section-wrapper"
              onActive={() => console.log('[Details NAV DEBUG] episode-section-wrapper ACTIVE')}
              onInactive={() => console.log('[Details NAV DEBUG] episode-section-wrapper INACTIVE')}>
              {seasons.length > 0 && TVEpisodeCarousel ? (
                <TVEpisodeCarousel
                  seasons={seasons}
                  selectedSeason={selectedSeason}
                  episodes={selectedSeason?.episodes ?? []}
                  activeEpisode={activeEpisode}
                  onSeasonSelect={(season: SeriesSeason) => handleSeasonSelect(season, false)}
                  onEpisodeSelect={handleEpisodeSelect}
                  onEpisodePlay={handlePlayEpisode}
                  isEpisodeWatched={isEpisodeWatched}
                  getEpisodeProgress={(episode: SeriesEpisode) => {
                    const key = `${episode.seasonNumber}-${episode.episodeNumber}`;
                    return episodeProgressMap.get(key) ?? 0;
                  }}
                  onFocusRowChange={handleTVFocusAreaChange}
                />
              ) : activeEpisode ? (
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
          {/* TV Cast Section - always render wrapper node to maintain navigation order
              (nodes register in DOM order, so late-loading content would otherwise end up at wrong position)
              Navigation disabled for kids profiles */}
          {Platform.isTV && TVCastSection && (
            <SpatialNavigationNode orientation="horizontal" focusKey="details-cast-section">
              {(isMetadataLoadingForSkeleton || credits) ? (
                <TVCastSection
                  credits={credits}
                  isLoading={isSeries ? seriesDetailsLoading : movieDetailsLoading}
                  maxCast={10}
                  onFocus={() => handleTVFocusAreaChange('cast')}
                  compactMargin
                  onCastMemberPress={isKidsProfile ? undefined : handleCastMemberPress}
                />
              ) : (
                <View />
              )}
            </SpatialNavigationNode>
          )}
          {/* TV More Like This Section - always render wrapper node to maintain navigation order
              Navigation disabled for kids profiles */}
          {Platform.isTV && TVMoreLikeThisSection && (
            <SpatialNavigationNode orientation="horizontal" focusKey="details-similar-section">
              {(similarLoading || similarContent.length > 0) ? (
                <TVMoreLikeThisSection
                  titles={similarContent}
                  isLoading={similarLoading}
                  maxTitles={20}
                  onFocus={() => handleTVFocusAreaChange('similar')}
                  onTitlePress={isKidsProfile ? undefined : handleSimilarTitlePress}
                />
              ) : (
                <View />
              )}
            </SpatialNavigationNode>
          )}
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
                seriesDetails={seriesDetailsData}
                seriesDetailsLoading={seriesDetailsLoading}
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
                onEpisodeLongPress={handleToggleEpisodeWatched}
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

  // Get credits for cast section
  const credits = useMemo(() => {
    if (isSeries) {
      return seriesDetailsData?.title?.credits ?? null;
    }
    return movieDetails?.credits ?? null;
  }, [isSeries, seriesDetailsData, movieDetails]);

  // Mobile content rendering with parallax and new components
  const renderMobileContent = () => (
    <MobileParallaxContainer posterUrl={posterUrl} backdropUrl={backdropUrl} theme={theme}>
      {/* Title and metadata section */}
      <View style={[styles.topContent, { overflow: 'visible' }]}>
        <View style={[styles.titleRow, { overflow: 'visible', marginLeft: -12 }]}>
          {logoUrl && logoDimensions ? (
            <View style={[{ padding: 12, overflow: 'visible' }, logoGlowStyle]}>
              <RNImage
                source={{ uri: logoUrl }}
                style={logoStyle}
                resizeMode="contain"
              />
            </View>
          ) : (
            <Text style={styles.title}>{title}</Text>
          )}
        </View>
        {(ratings.length > 0 || shouldShowRatingsSkeleton) && (
          <View style={styles.ratingsRow}>
            {ratings.length > 0 ? (
              ratings.map((rating) => {
                const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
                const config = getRatingConfig(rating.source, baseUrl, rating.value, rating.max);
                const iconSize = 14;
                return (
                  <RatingBadge
                    key={rating.source}
                    rating={rating}
                    config={config}
                    iconSize={iconSize}
                    styles={styles}
                  />
                );
              })
            ) : (
              <Text style={styles.ratingValue}>—</Text>
            )}
          </View>
        )}
        {(certification || genres.length > 0) && (
          <View style={styles.genresRow}>
            {certification && (
              <View style={styles.genreBadge}>
                <Text style={styles.genreText}>{certification}</Text>
              </View>
            )}
            {certification && genres.length > 0 && (
              <Text style={[styles.genreText, { alignSelf: 'center', fontWeight: '700', fontSize: 16 }]}>|</Text>
            )}
            {genres.map((genre) => (
              <View key={genre} style={styles.genreBadge}>
                <Text style={styles.genreText}>{genre}</Text>
              </View>
            ))}
          </View>
        )}
        {contentPreference && (contentPreference.audioLanguage || contentPreference.subtitleLanguage) && (
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 8, marginTop: 8, marginBottom: 8 }}>
            {contentPreference.audioLanguage && (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  backgroundColor: theme.colors.background.elevated,
                  paddingHorizontal: 10,
                  paddingVertical: 4,
                  borderRadius: 4,
                }}>
                <Ionicons name="volume-high" size={14} color={theme.colors.text.secondary} style={{ marginRight: 4 }} />
                <Text style={{ color: theme.colors.text.secondary, fontSize: 12 }}>
                  {contentPreference.audioLanguage.toUpperCase()}
                </Text>
              </View>
            )}
            {contentPreference.subtitleLanguage && (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  backgroundColor: theme.colors.background.elevated,
                  paddingHorizontal: 10,
                  paddingVertical: 4,
                  borderRadius: 4,
                }}>
                <Ionicons name="text" size={14} color={theme.colors.text.secondary} style={{ marginRight: 4 }} />
                <Text style={{ color: theme.colors.text.secondary, fontSize: 12 }}>
                  {contentPreference.subtitleLanguage.toUpperCase()}
                </Text>
              </View>
            )}
          </View>
        )}
        {(releaseRows.length > 0 || shouldShowReleaseSkeleton || releaseErrorMessage || (!isSeries && movieDetails?.runtimeMinutes)) && (
          <View style={styles.releaseInfoRow}>
            {(releaseRows.length > 0 ? releaseRows : releaseSkeletonRows).map((row) => (
              <View key={row.key} style={styles.releaseInfoItem}>
                <Ionicons name={row.icon} size={14} color={theme.colors.text.secondary} style={styles.releaseInfoIcon} />
                <Text style={styles.releaseInfoValue}>{row.value}</Text>
              </View>
            ))}
            {!isSeries && movieDetails?.runtimeMinutes && (
              <View style={styles.releaseInfoItem}>
                <Ionicons name="time-outline" size={14} color={theme.colors.text.secondary} style={styles.releaseInfoIcon} />
                <Text style={styles.releaseInfoValue}>{movieDetails.runtimeMinutes} min</Text>
              </View>
            )}
            {releaseErrorMessage && <Text style={styles.releaseInfoError}>{releaseErrorMessage}</Text>}
          </View>
        )}
        <Text style={[styles.description, { maxWidth: '100%' }]}>
          {displayDescription}
        </Text>
      </View>

      {/* Action buttons - icon only for mobile */}
      <View style={[styles.actionRow, styles.compactActionRow, { marginTop: theme.spacing.lg }]}>
        <FocusablePressable
          focusKey="watch-now-mobile"
          icon="play"
          onSelect={handleWatchNow}
          style={styles.iconActionButton}
          loading={isResolving || (isSeries && episodesLoading)}
          disabled={isResolving || (isSeries && episodesLoading)}
          showReadyPip={prequeueReady}
        />
        <FocusablePressable
          focusKey="manual-selection-mobile"
          icon="search"
          onSelect={handleManualSelect}
          style={styles.iconActionButton}
          disabled={isResolving || (isSeries && episodesLoading)}
        />
        {isSeries && (
          <FocusablePressable
            focusKey="watch-management-mobile"
            icon="checkmark-done"
            onSelect={() => setBulkWatchModalVisible(true)}
            style={styles.iconActionButton}
          />
        )}
        {isSeries && (
          <FocusablePressable
            focusKey="shuffle-play-mobile"
            icon="shuffle"
            accessibilityLabel="Shuffle play random episode"
            onSelect={handleShufflePlay}
            onLongPress={handleShuffleSeasonPlay}
            style={styles.iconActionButton}
            disabled={episodesLoading || allEpisodes.length === 0}
          />
        )}
        <FocusablePressable
          focusKey="watchlist-toggle-mobile"
          icon={isWatchlisted ? 'bookmark' : 'bookmark-outline'}
          onSelect={handleToggleWatchlist}
          loading={watchlistBusy}
          style={[styles.iconActionButton, isWatchlisted && styles.watchlistActionButtonActive]}
        />
        {!isSeries && (
          <FocusablePressable
            focusKey="watch-state-toggle-mobile"
            icon={isWatched ? 'eye' : 'eye-outline'}
            accessibilityLabel={watchStateButtonLabel}
            onSelect={handleToggleWatched}
            loading={watchlistBusy}
            style={[styles.iconActionButton, isWatched && styles.watchStateButtonActive]}
            disabled={watchlistBusy}
          />
        )}
        {(trailersLoading || hasAvailableTrailer) && (
          <FocusablePressable
            focusKey="watch-trailer-mobile"
            icon="videocam"
            accessibilityLabel={trailerButtonLabel}
            onSelect={handleWatchTrailer}
            loading={trailersLoading}
            style={styles.iconActionButton}
            disabled={trailerButtonDisabled}
          />
        )}
        {!isSeries && movieDetails?.collection && (
          <FocusablePressable
            focusKey="view-collection-mobile"
            icon="albums"
            accessibilityLabel={`View ${movieDetails.collection.name}`}
            onSelect={handleViewCollection}
            style={styles.iconActionButton}
          />
        )}
      </View>

      {/* Prequeue stream info display (mobile) - always render container to reserve space */}
      <Animated.View style={[styles.prequeueInfoContainer, styles.prequeueInfoMinHeight, prequeuePulseStyle]}>
        {prequeueDisplayInfo && (
          <>
            {/* Status message for early stages */}
            {(prequeueDisplayInfo.status === 'queued' || prequeueDisplayInfo.status === 'searching') && (
              <Text style={styles.prequeueFilename}>
                {prequeueDisplayInfo.status === 'queued' && 'Queued...'}
                {prequeueDisplayInfo.status === 'searching' && 'Searching for streams...'}
              </Text>
            )}
            {/* Failed status */}
            {prequeueDisplayInfo.status === 'failed' && (
              <Text style={styles.prequeueFilename}>
                Failed: {prequeueDisplayInfo.error || 'Unknown error'}
              </Text>
            )}
            {/* Show filename once available (resolving, probing, or ready) */}
            {(prequeueDisplayInfo.status === 'resolving' || prequeueDisplayInfo.status === 'probing' || prequeueDisplayInfo.status === 'ready') && (
              <>
                <Text style={styles.prequeueFilename} numberOfLines={1} ellipsizeMode="middle">
                  {prequeueDisplayInfo.displayName ||
                    prequeueDisplayInfo.passthroughName ||
                    (prequeueDisplayInfo.streamPath?.split('/').pop()) ||
                    (prequeueDisplayInfo.status === 'resolving' ? 'Resolving stream...' : 'Analyzing media...')}
                </Text>
                {/* Show loading state for tracks during probing */}
                {(prequeueDisplayInfo.status === 'probing' || prequeueDisplayInfo.status === 'resolving') &&
                 !prequeueDisplayInfo.audioTracks?.length && (
                  <Text style={styles.prequeueLoadingText}>Analyzing tracks...</Text>
                )}
                {/* Audio & Subtitle tracks on one line */}
                {(prequeueDisplayInfo.audioTracks?.length || prequeueDisplayInfo.subtitleTracks?.length) ? (
                  <View style={styles.prequeueTrackRow}>
                    {/* Audio track - tappable when multiple tracks available */}
                    {prequeueDisplayInfo.audioTracks && prequeueDisplayInfo.audioTracks.length > 0 && (
                      <Pressable
                        onPress={() => setShowAudioTrackModal(true)}
                        disabled={prequeueDisplayInfo.audioTracks.length <= 1}
                        style={styles.prequeueTrackPressable}
                      >
                        <Ionicons name="volume-high" size={14} color={theme.colors.text.secondary} />
                        <Text style={styles.prequeueTrackValue} numberOfLines={1}>
                          {(() => {
                            const selectedIdx = trackOverrideAudio ?? prequeueDisplayInfo.selectedAudioTrack;
                            const track = selectedIdx !== undefined && selectedIdx >= 0
                              ? prequeueDisplayInfo.audioTracks?.find((t) => t.index === selectedIdx)
                              : prequeueDisplayInfo.audioTracks?.[0];
                            if (!track) return 'Default';
                            return `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`;
                          })()}
                        </Text>
                        {(() => {
                          const selectedIdx = trackOverrideAudio ?? prequeueDisplayInfo.selectedAudioTrack;
                          const track = selectedIdx !== undefined && selectedIdx >= 0
                            ? prequeueDisplayInfo.audioTracks?.find((t) => t.index === selectedIdx)
                            : prequeueDisplayInfo.audioTracks?.[0];
                          if (track?.codec) {
                            return (
                              <Text style={[styles.prequeueTrackBadge, styles.prequeueTrackCodecBadge]}>
                                {track.codec.toUpperCase()}
                              </Text>
                            );
                          }
                          return null;
                        })()}
                        {prequeueDisplayInfo.audioTracks.length > 1 && (
                          <Ionicons name="chevron-forward" size={10} color={theme.colors.text.muted} />
                        )}
                      </Pressable>
                    )}
                    {/* Separator */}
                    {(prequeueDisplayInfo.audioTracks?.length ?? 0) > 0 && (prequeueDisplayInfo.subtitleTracks?.length ?? 0) > 0 && (
                      <Text style={styles.prequeueTrackSeparator}>•</Text>
                    )}
                    {/* Subtitle track - tappable when subtitles available */}
                    {prequeueDisplayInfo.subtitleTracks && prequeueDisplayInfo.subtitleTracks.length > 0 && (
                      <Pressable
                        onPress={() => setShowSubtitleTrackModal(true)}
                        style={styles.prequeueTrackPressable}
                      >
                        <Ionicons name="text" size={14} color={theme.colors.text.secondary} />
                        <Text style={styles.prequeueTrackValue} numberOfLines={1}>
                          {(() => {
                            const selectedIdx = trackOverrideSubtitle ?? prequeueDisplayInfo.selectedSubtitleTrack;
                            if (selectedIdx === undefined || selectedIdx < 0) return 'Off';
                            const track = prequeueDisplayInfo.subtitleTracks?.find((t) => t.index === selectedIdx);
                            if (!track) return 'Off';
                            return `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`;
                          })()}
                        </Text>
                        {(() => {
                          const selectedIdx = trackOverrideSubtitle ?? prequeueDisplayInfo.selectedSubtitleTrack;
                          if (selectedIdx === undefined || selectedIdx < 0) return null;
                          const track = prequeueDisplayInfo.subtitleTracks?.find((t) => t.index === selectedIdx);
                          if (!track) return null;
                          if (track.forced) {
                            return (
                              <Text style={[styles.prequeueTrackBadge, styles.prequeueTrackForcedBadge]}>
                                FORCED
                              </Text>
                            );
                          }
                          if (track.title?.toLowerCase().includes('sdh') || track.title?.toLowerCase().includes('hearing')) {
                            return (
                              <Text style={[styles.prequeueTrackBadge, styles.prequeueTrackSDHBadge]}>
                                SDH
                              </Text>
                            );
                          }
                          return null;
                        })()}
                        <Ionicons name="chevron-forward" size={10} color={theme.colors.text.muted} />
                      </Pressable>
                    )}
                  </View>
                ) : null}
              </>
            )}
          </>
        )}
      </Animated.View>

      {/* Episode carousel for series */}
      {isSeries && seasons.length > 0 && (
        <MobileEpisodeCarousel
          seasons={seasons}
          selectedSeason={selectedSeason}
          episodes={selectedSeason?.episodes ?? []}
          activeEpisode={activeEpisode}
          isLoading={seriesDetailsLoading}
          onSeasonSelect={(season) => handleSeasonSelect(season, false)}
          onEpisodeSelect={handleEpisodeSelect}
          onEpisodePlay={handlePlayEpisode}
          onEpisodeLongPress={handleToggleEpisodeWatched}
          isEpisodeWatched={isEpisodeWatched}
          getEpisodeProgress={(episode) => {
            const key = `${episode.seasonNumber}-${episode.episodeNumber}`;
            return episodeProgressMap.get(key) ?? 0;
          }}
          theme={theme}
        />
      )}

      {/* Episode overview when episode is selected */}
      {isSeries && activeEpisode && (
        <View style={{ marginTop: theme.spacing.lg }}>
          <Text style={[styles.episodeOverviewTitle, { color: theme.colors.text.primary }]}>
            {`S${activeEpisode.seasonNumber}:E${activeEpisode.episodeNumber} - ${activeEpisode.name || `Episode ${activeEpisode.episodeNumber}`}`}
          </Text>
          {activeEpisode.overview ? (
            <Text style={[styles.episodeOverviewText, { color: theme.colors.text.secondary }]}>
              {activeEpisode.overview}
            </Text>
          ) : null}
          {activeEpisode.airedDate && (
            <Text style={[styles.episodeOverviewMeta, { color: theme.colors.text.muted }]}>
              {formatPublishDate(activeEpisode.airedDate)}
              {activeEpisode.runtimeMinutes ? ` • ${activeEpisode.runtimeMinutes} minutes` : ''}
            </Text>
          )}
        </View>
      )}

      {/* Cast section - disable navigation for kids profiles */}
      <CastSection credits={credits} isLoading={isSeries ? seriesDetailsLoading : movieDetailsLoading} theme={theme} onCastMemberPress={isKidsProfile ? undefined : handleCastMemberPress} />

      {/* More Like This section - disable navigation for kids profiles */}
      <MoreLikeThisSection
        titles={similarContent}
        isLoading={similarLoading}
        theme={theme}
        onTitlePress={isKidsProfile ? undefined : handleSimilarTitlePress}
      />

      {/* Hidden SeriesEpisodes component to load data (same as in renderDetailsContent) */}
      {isSeries && (
        <View style={{ position: 'absolute', opacity: 0, pointerEvents: 'none', zIndex: -1 }}>
          <SeriesEpisodes
            isSeries={isSeries}
            title={title}
            tvdbId={tvdbId}
            titleId={titleId}
            yearNumber={yearNumber}
            seriesDetails={seriesDetailsData}
            seriesDetailsLoading={seriesDetailsLoading}
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
            onEpisodeLongPress={handleToggleEpisodeWatched}
            onToggleEpisodeWatched={handleToggleEpisodeWatched}
            isEpisodeWatched={isEpisodeWatched}
            renderContent={false}
            activeEpisode={activeEpisode}
            isResolving={isResolving}
            theme={theme}
            onRegisterSeasonFocusHandler={handleRegisterSeasonFocusHandler}
            onRequestFocusShift={handleRequestFocusShift}
            onEpisodesLoaded={handleEpisodesLoaded}
            onSeasonsLoaded={handleSeasonsLoaded}
          />
        </View>
      )}
    </MobileParallaxContainer>
  );

  const SafeAreaWrapper = isTV ? View : FixedSafeAreaView;
  const safeAreaProps = isTV ? {} : { edges: ['top'] as ('top' | 'bottom' | 'left' | 'right')[] };

  // On TV/mobile, wait for metadata, logo, and poster to load before showing the page to prevent pop-in
  const isMetadataLoading = isSeries ? seriesDetailsLoading : movieDetailsLoading;
  const isLogoReady = !logoUrl || logoDimensions !== null;
  const isPosterReady = isPosterPreloaded;
  const shouldHideUntilMetadataReady = (isTV || isMobile) && (isMetadataLoading || !isLogoReady || !isPosterReady);
  const shouldAnimateBackground = isTV || isMobile;

  // Fade in background when metadata is ready
  const backgroundOpacity = useSharedValue(shouldAnimateBackground ? 0 : 1);
  // TV parallax scroll - background moves at 0.4x rate of content
  const tvScrollY = useSharedValue(0);
  const backgroundAnimatedStyle = useAnimatedStyle(() => ({
    opacity: backgroundOpacity.value,
    ...(Platform.isTV ? { transform: [{ translateY: -tvScrollY.value * 0.4 }] } : {}),
  }));
  const tvScrollHandler = useAnimatedScrollHandler({
    onScroll: (event) => {
      tvScrollY.value = event.contentOffset.y;
    },
  });

  // Ref for TV scroll view to programmatically scroll
  const tvScrollViewRef = useRef<Animated.ScrollView>(null);

  // Handle focus area change - scroll to appropriate position for each focus area
  const handleTVFocusAreaChange = useCallback(
    (area: 'seasons' | 'episodes' | 'actions' | 'cast' | 'similar') => {
      if (!Platform.isTV || !tvScrollViewRef.current) return;

      // Scroll positions based on focus area:
      // Layout order (top to bottom): artwork -> action row -> seasons -> episodes -> cast -> similar
      // Higher value = more scroll = content raised higher in viewport
      const scrollPositions = {
        actions: Math.round(windowHeight * 0.15), // Show artwork with action row visible
        seasons: Math.round(windowHeight * 0.25), // Show action row + season selector
        episodes: Math.round(windowHeight * 0.5), // Show seasons + episode carousel (raised higher)
        cast: Math.round(windowHeight * 1.0), // Show cast section fully visible
        similar: Math.round(windowHeight * 2), // Large value to scroll to bottom (clamped by ScrollView)
      };
      const targetY = scrollPositions[area];
      tvScrollViewRef.current.scrollTo({ y: targetY, animated: true });
    },
    [windowHeight],
  );

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
    isMetadataLoading,
    isLogoReady,
    isPosterReady,
    logoUrl: !!logoUrl,
    logoDimensions: !!logoDimensions,
    posterToPreload: !!posterToPreload,
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
            {/* Hide all content until metadata (and logo) is ready to prevent progressive loading */}
            {shouldHideUntilMetadataReady ? null : (
              <>
                {/* Mobile uses the new parallax scrollable container */}
                {isMobile ? (
                  renderMobileContent()
                ) : (
                  <>
                    {headerImage ? (
                  <Animated.View
                    style={[
                      styles.backgroundImageContainer,
                      shouldAnchorHeroToTop && styles.backgroundImageContainerTop,
                      isTV && backgroundAnimatedStyle,
                    ]}
                    pointerEvents="none">
                    {shouldShowBlurredFill && (
                      <RNImage
                        source={{ uri: headerImage }}
                        style={styles.backgroundImageBackdrop}
                        resizeMode="cover"
                        blurRadius={20}
                      />
                    )}
                    <RNImage
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
                {Platform.isTV ? (
                  <Animated.ScrollView
                    ref={tvScrollViewRef}
                    style={styles.tvScrollContainer}
                    contentContainerStyle={styles.tvScrollContent}
                    showsVerticalScrollIndicator={false}
                    onScroll={tvScrollHandler}
                    scrollEventThrottle={16}
                    // Disable native scroll-to-focus - we control scroll programmatically
                    scrollEnabled={false}>
                    {/* Transparent spacer - shrinks when topContent is taller to keep action row at consistent position */}
                    <View
                      style={{
                        height: Math.round(
                          Math.max(
                            windowHeight * 0.35, // Minimum 35% backdrop visible
                            windowHeight * 0.65 - Math.max(0, tvTopContentHeight - 200 * tvScale) // Shrink for taller content
                          )
                        ),
                      }}
                    />
                    {/* Content area with gradient background - starts higher with softer transition */}
                    <LinearGradient
                      colors={[
                        'transparent',
                        'rgba(0, 0, 0, 0.6)',
                        'rgba(0, 0, 0, 0.85)',
                        theme.colors.background.base,
                      ]}
                      locations={[0, 0.1, 0.25, 0.45]}
                      style={styles.tvContentGradient}>
                      <View style={styles.tvContentInner}>{renderDetailsContent()}</View>
                    </LinearGradient>
                  </Animated.ScrollView>
                ) : (
                  <View style={styles.contentOverlay}>
                    <View style={[styles.contentBox, contentBoxStyle]}>
                      <View style={styles.contentBoxInner}>
                        <View style={styles.contentContainer}>{renderDetailsContent()}</View>
                      </View>
                    </View>
                  </View>
                )}
                </>
              )}
            </>
          )}
          {/* Corner poster removed - was covering backdrop art. Plex style shows full backdrop instead */}
          </View>
        </SafeAreaWrapper>
        <MobileTabBar />
      </SpatialNavigationRoot>
      <TrailerModal
        visible={trailerModalVisible}
        trailer={activeTrailer}
        onClose={handleCloseTrailer}
        theme={theme}
        preloadedStreamUrl={trailerStreamUrl}
        isDownloading={trailerPrequeueStatus === 'pending' || trailerPrequeueStatus === 'downloading'}
      />
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
        userSettings={userSettings ?? undefined}
        contentPreference={contentPreference}
      />
      <SeasonSelector
        visible={seasonSelectorVisible}
        onClose={() => setSeasonSelectorVisible(false)}
        seasons={seasons}
        onSeasonSelect={isMobile ? handleMobileSeasonSelect : handleSeasonSelectorSelect}
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
      {/* Audio Track Selection Modal for prequeue override */}
      <TrackSelectionModal
        visible={showAudioTrackModal}
        title="Audio Track"
        options={buildPrequeueAudioOptions()}
        selectedId={currentAudioTrackId}
        onSelect={(id) => {
          setTrackOverrideAudio(parseInt(id, 10));
          setShowAudioTrackModal(false);
        }}
        onClose={() => setShowAudioTrackModal(false)}
      />
      {/* Subtitle Track Selection Modal for prequeue override */}
      <TrackSelectionModal
        visible={showSubtitleTrackModal}
        title="Subtitles"
        options={buildPrequeueSubtitleOptions()}
        selectedId={currentSubtitleTrackId}
        onSelect={(id) => {
          setTrackOverrideSubtitle(parseInt(id, 10));
          setShowSubtitleTrackModal(false);
        }}
        onClose={() => setShowSubtitleTrackModal(false)}
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
