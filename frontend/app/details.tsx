import type { PlaybackPreference } from '@/components/BackendSettingsContext';
import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useContinueWatching } from '@/components/ContinueWatchingContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import { useLoadingScreen } from '@/components/LoadingScreenContext';
import MobileTabBar from '@/components/MobileTabBar';
import { useToast } from '@/components/ToastContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { TvModal } from '@/components/TvModal';
import { useWatchlist } from '@/components/WatchlistContext';
import { useWatchStatus } from '@/components/WatchStatusContext';
import EpisodeCard from '@/components/EpisodeCard';
import TVEpisodeStrip from '@/components/TVEpisodeStrip';

// Safely import new TV components - fallback to TVEpisodeStrip if unavailable
let TVEpisodeCarousel: typeof import('@/components/tv').TVEpisodeCarousel | null = null;
let TVCastSection: typeof import('@/components/tv').TVCastSection | null = null;
let TVMoreLikeThisSection: typeof import('@/components/tv').TVMoreLikeThisSection | null = null;
let TVTrailerBackdrop: typeof import('@/components/tv').TVTrailerBackdrop | null = null;
try {
  const tvComponents = require('@/components/tv');
  TVEpisodeCarousel = tvComponents.TVEpisodeCarousel;
  TVCastSection = tvComponents.TVCastSection;
  TVMoreLikeThisSection = tvComponents.TVMoreLikeThisSection;
  TVTrailerBackdrop = tvComponents.TVTrailerBackdrop;
} catch {
  // TV components not available, will use fallbacks
}
import {
  apiService,
  type CastMember,
  type Rating,
  type SeriesEpisode,
  type SeriesSeason,
  type Title,
  type Trailer,
} from '@/services/api';
import { useTheme } from '@/theme';
import { getTVScaleMultiplier, isTablet, isAndroidTV as isAndroidTVPlatform } from '@/theme/tokens/tvScale';
import { playbackNavigation } from '@/services/playback-navigation';
import { Ionicons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import { Stack, useLocalSearchParams, useRouter, usePathname } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Image as RNImage, ImageResizeMode, ImageStyle, InteractionManager, Platform, Pressable, Text, View, unstable_batchedUpdates } from 'react-native';
import { Image as ProxiedImage } from '@/components/Image';
import { createDetailsStyles } from '@/styles/details-styles';
import { SpatialNavigationRoot, SpatialNavigationNode, SpatialNavigationFocusableView, DefaultFocus } from '@/services/tv-navigation';
import TVActionButton from '@/components/tv/TVActionButton';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import Animated, {
  useAnimatedStyle,
  useAnimatedScrollHandler,
  useSharedValue,
  withTiming,
  Easing,
  cancelAnimation,
} from 'react-native-reanimated';

// Import extracted modules
import { BulkWatchModal } from './details/bulk-watch-modal';
import { ManualSelection, useManualHealthChecks } from './details/manual-selection';
import { TrackSelectionModal } from '@/components/player/TrackSelectionModal';
import { ResumePlaybackModal } from './details/resume-modal';
import { SeriesEpisodes } from './details/series-episodes';
import { TrailerModal } from './details/trailer';
import { SeasonSelector } from './details/season-selector';
import { EpisodeSelector } from './details/episode-selector';
import { buildSeasonQuery, formatPublishDate, formatUnreleasedMessage, isEpisodeUnreleased, isMovieUnreleased, toStringParam } from './details/utils';
import MobileParallaxContainer from './details/mobile-parallax-container';
import MobileEpisodeCarousel from './details/mobile-episode-carousel';
import CastSection from '@/components/CastSection';
import MoreLikeThisSection from '@/components/MoreLikeThisSection';

// Import extracted hooks
import { useDetailsData } from './details/hooks/useDetailsData';
import { useTrailers } from './details/hooks/useTrailers';
import { usePlayback } from './details/hooks/usePlayback';
import { useEpisodeManager } from './details/hooks/useEpisodeManager';
import { useWatchActions } from './details/hooks/useWatchActions';
import { useManualSelectFlow } from './details/hooks/useManualSelectFlow';
import { useDownloads } from '@/components/DownloadsContext';

const SELECTION_TOAST_ID = 'details-nzb-status';

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
  /** When navigating from "More Like This", delay auto-focus to prevent enter key propagation */
  fromSimilar?: string;
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

// Helper to get certification (content rating) icon URL and aspect ratio
const getCertificationConfig = (certification: string, baseUrl: string): { iconUrl: string; aspectRatio: number } | null => {
  const iconBase = `${baseUrl}/static/rating_icons`;
  // Normalize the certification string for matching
  const normalized = certification.toLowerCase().replace(/\s+/g, '-');

  // Map certification strings to icon file names and aspect ratios (width/height)
  const certificationIcons: Record<string, { file: string; aspectRatio: number }> = {
    // MPAA movie ratings (varying aspect ratios)
    'g': { file: 'g.png', aspectRatio: 1.15 },
    'pg': { file: 'pg.png', aspectRatio: 1.45 },
    'pg-13': { file: 'pg-13.png', aspectRatio: 2.53 },
    'r': { file: 'r.png', aspectRatio: 1.15 },
    'nc-17': { file: 'nc-17.png', aspectRatio: 2.94 },
    // TV ratings (square icons)
    'tv-y': { file: 'tv-y.png', aspectRatio: 1.0 },
    'tv-y7': { file: 'tv-y7.png', aspectRatio: 1.0 },
    'tv-y7-fv': { file: 'tv-y7.png', aspectRatio: 1.0 },
    'tv-g': { file: 'tv-g.png', aspectRatio: 1.0 },
    'tv-pg': { file: 'tv-pg.png', aspectRatio: 1.0 },
    'tv-14': { file: 'tv-14.png', aspectRatio: 1.0 },
    'tv-ma': { file: 'tv-ma.png', aspectRatio: 1.0 },
  };

  const config = certificationIcons[normalized];
  return config ? { iconUrl: `${iconBase}/${config.file}`, aspectRatio: config.aspectRatio } : null;
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

// Certification badge component with image and text fallback
const CertificationBadge = ({
  certification,
  iconUrl,
  iconSize,
  aspectRatio,
  styles,
}: {
  certification: string;
  iconUrl: string | null;
  iconSize: number;
  aspectRatio: number;
  styles: ReturnType<typeof createDetailsStyles>;
}) => {
  const [imageError, setImageError] = useState(false);

  // If we have an icon URL and no error, show the image
  if (iconUrl && !imageError) {
    return (
      <ProxiedImage
        source={{ uri: iconUrl }}
        style={{ width: iconSize * aspectRatio, height: iconSize }}
        contentFit="contain"
        onError={() => {
          console.warn(`Certification icon failed to load: ${iconUrl}`);
          setImageError(true);
        }}
      />
    );
  }

  // Fallback to text badge
  return (
    <View style={styles.genreBadge}>
      <Text style={styles.genreText}>{certification}</Text>
    </View>
  );
};

// Memoized ratings row — data rarely changes mid-session, prevents re-renders from parent state churn
const MemoizedRatingsRow = memo(function MemoizedRatingsRow({
  ratings,
  shouldShowSkeleton,
  iconSize,
  baseUrl,
  styles,
}: {
  ratings: Rating[];
  shouldShowSkeleton: boolean;
  iconSize: number;
  baseUrl: string;
  styles: ReturnType<typeof createDetailsStyles>;
}) {
  if (ratings.length === 0 && !shouldShowSkeleton) return null;
  return (
    <View style={styles.ratingsRow}>
      {ratings.length > 0 ? (
        ratings.map((rating) => {
          const config = getRatingConfig(rating.source, baseUrl, rating.value, rating.max);
          return (
            <RatingBadge key={rating.source} rating={rating} config={config} iconSize={iconSize} styles={styles} />
          );
        })
      ) : (
        <Text style={styles.ratingValue}>{'\u2014'}</Text>
      )}
    </View>
  );
});

// Memoized genres row — data rarely changes mid-session
const MemoizedGenresRow = memo(function MemoizedGenresRow({
  certification,
  genres,
  tvScale,
  separatorColor,
  styles,
}: {
  certification: string | null | undefined;
  genres: string[];
  tvScale: number;
  separatorColor: string;
  styles: ReturnType<typeof createDetailsStyles>;
}) {
  if (!certification && genres.length === 0) return null;
  return (
    <View style={styles.genresRow}>
      {certification && (
        <View style={[styles.genreBadge, { backgroundColor: 'rgba(70, 130, 180, 0.35)', borderColor: 'rgba(100, 160, 210, 0.5)' }]}>
          <Text style={styles.genreText}>{certification}</Text>
        </View>
      )}
      {certification && genres.length > 0 && (
        <Text style={{ color: separatorColor, fontSize: 14 * tvScale, marginHorizontal: 0, fontWeight: '900' }}>|</Text>
      )}
      {genres.map((genre) => (
        <View key={genre} style={styles.genreBadge}>
          <Text style={styles.genreText}>{genre}</Text>
        </View>
      ))}
    </View>
  );
});

// Memoized release info row — data rarely changes mid-session
const MemoizedReleaseInfo = memo(function MemoizedReleaseInfo({
  releaseRows,
  releaseSkeletonRows,
  shouldShowSkeleton,
  errorMessage,
  isSeries,
  runtimeMinutes,
  iconSize,
  iconColor,
  styles,
}: {
  releaseRows: { key: string; icon: keyof typeof Ionicons.glyphMap; value: string }[];
  releaseSkeletonRows: { key: string; icon: keyof typeof Ionicons.glyphMap; value: string }[];
  shouldShowSkeleton: boolean;
  errorMessage: string | null;
  isSeries: boolean;
  runtimeMinutes: number | undefined;
  iconSize: number;
  iconColor: string;
  styles: ReturnType<typeof createDetailsStyles>;
}) {
  if (releaseRows.length === 0 && !shouldShowSkeleton && !errorMessage && (isSeries || !runtimeMinutes)) return null;
  return (
    <View style={styles.releaseInfoRow}>
      {(releaseRows.length > 0 ? releaseRows : releaseSkeletonRows).map((row) => (
        <View key={row.key} style={styles.releaseInfoItem}>
          <Ionicons name={row.icon} size={iconSize} color={iconColor} style={styles.releaseInfoIcon} />
          <Text style={styles.releaseInfoValue}>{row.value}</Text>
        </View>
      ))}
      {!isSeries && runtimeMinutes && (
        <View style={styles.releaseInfoItem}>
          <Ionicons name="time-outline" size={iconSize} color={iconColor} style={styles.releaseInfoIcon} />
          <Text style={styles.releaseInfoValue}>{runtimeMinutes} min</Text>
        </View>
      )}
      {errorMessage && <Text style={styles.releaseInfoError}>{errorMessage}</Text>}
    </View>
  );
});

export default function DetailsScreen() {
  const params = useLocalSearchParams<LocalParams>();
  const router = useRouter();
  const pathname = usePathname();

  const theme = useTheme();
  const styles = useMemo(() => createDetailsStyles(theme), [theme]);
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
  const isCompactBreakpoint = theme.breakpoint === 'compact';
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

  const tmdbId = toStringParam(params.tmdbId);
  const imdbId = toStringParam(params.imdbId);
  const tvdbId = toStringParam(params.tvdbId);
  const yearParam = toStringParam(params.year);
  const initialSeasonParam = toStringParam(params.initialSeason);
  const initialEpisodeParam = toStringParam(params.initialEpisode);
  const fromSimilarParam = toStringParam(params.fromSimilar);

  // When navigating from "More Like This", temporarily block select actions to prevent
  // the enter key that triggered navigation from also triggering play on the new page.
  const [isSelectBlocked, setIsSelectBlocked] = useState(!!fromSimilarParam);
  useEffect(() => {
    if (fromSimilarParam) {
      const timer = setTimeout(() => {
        setIsSelectBlocked(false);
      }, 300); // 300ms delay to let the enter key event fully propagate
      return () => clearTimeout(timer);
    }
  }, [fromSimilarParam]);

  const seriesIdentifier = useMemo(() => {
    const trimmedTitle = title.trim();
    if (titleId) {
      // Strip episode information (e.g., :S01E02) from titleId to get the series ID
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

  // ===== Context Hooks =====
  const { settings, userSettings } = useBackendSettings();
  const { addToWatchlist, removeFromWatchlist, getItem } = useWatchlist();
  const {
    isWatched: isItemWatched,
    toggleWatchStatus,
    bulkUpdateWatchStatus,
    refresh: refreshWatchStatus,
  } = useWatchStatus();
  const { showToast, hideToast } = useToast();
  const { recordEpisodeWatch, items: continueWatchingItems, hideFromContinueWatching } = useContinueWatching();
  const { activeUserId, activeUser } = useUserProfiles();
  const { showLoadingScreen, hideLoadingScreen, setOnCancel } = useLoadingScreen();

  // Kids profiles have restricted navigation - disable cast/crew and similar content links
  const isKidsProfile = activeUser?.isKidsProfile ?? false;

  // Check if this title is in the continue watching list
  const isInContinueWatching = useMemo(() => {
    if (!seriesIdentifier) return false;
    return continueWatchingItems.some(item => item.seriesId === seriesIdentifier);
  }, [continueWatchingItems, seriesIdentifier]);

  // ===== Lifted Episode State (shared between usePlayback and useEpisodeManager) =====
  const [activeEpisode, setActiveEpisode] = useState<SeriesEpisode | null>(null);
  const [nextUpEpisode, setNextUpEpisode] = useState<SeriesEpisode | null>(null);
  const [isShuffleMode, setIsShuffleMode] = useState(false);
  const [progressRefreshKey, setProgressRefreshKey] = useState(0);

  // State for next episode from player navigation
  const [nextEpisodeFromPlayback, setNextEpisodeFromPlayback] = useState<{
    seasonNumber: number;
    episodeNumber: number;
    autoPlay?: boolean;
  } | null>(null);

  const isDetailsPageActive = pathname === '/details';
  const autoPlayTrailersTV = Platform.isTV && settings?.playback?.autoPlayTrailersTV;

  // Modal state
  const [trailerModalVisible, setTrailerModalVisible] = useState(false);
  const [activeTrailer, setActiveTrailer] = useState<Trailer | null>(null);
  const [seasonSelectorVisible, setSeasonSelectorVisible] = useState(false);
  const [episodeSelectorVisible, setEpisodeSelectorVisible] = useState(false);
  const [moreOptionsVisible, setMoreOptionsVisible] = useState(false);
  const [manualSelectDownloadOnly, setManualSelectDownloadOnly] = useState(false);
  const [seasonDownloadMode, setSeasonDownloadMode] = useState(false);
  const [seasonDownloadTarget, setSeasonDownloadTarget] = useState<SeriesSeason | null>(null);
  const [isDescriptionExpanded, setIsDescriptionExpanded] = useState(false);
  const [collapsedHeight, setCollapsedHeight] = useState(0);
  const [expandedHeight, setExpandedHeight] = useState(0);
  const descriptionHeight = useSharedValue(0);

  // ===== Hook 1: useDetailsData =====
  const detailsData = useDetailsData({
    titleId,
    title,
    isSeries,
    mediaType,
    seriesIdentifier,
    yearNumber,
    tmdbIdNumber,
    tvdbIdNumber,
    imdbId,
    activeUserId,
    selectedSeasonNumber: undefined, // Will be updated when selectedSeason changes
  });

  const {
    seriesDetailsData,
    movieDetails,
    detailsBundle,
    bundleReady,
    similarContent,
    similarLoading,
    trailers,
    primaryTrailer,
    trailersLoading,
    contentPreference,
    // episodeProgressMap and displayProgress from useDetailsData are unused —
    // the component uses playback.episodeProgressMap and playback.displayProgress instead.
    movieDetailsLoading,
    movieDetailsError,
    seriesDetailsLoading,
    credits: detailsCredits,
    ratings: detailsRatings,
    genres: detailsGenres,
    certification: detailsCertification,
    isMetadataLoadingForSkeleton,
    hydratedFromBundle,
    bundleTrailerSeasonRef,
  } = detailsData;

  // Derive the title from series details for poster/backdrop
  const seriesDetailsForBackdrop = seriesDetailsData?.title ?? null;

  // Compute final poster URL - prefer fetched metadata for textless posters
  const posterUrl = useMemo(() => {
    if (!isSeries && movieDetails?.poster?.url) {
      return movieDetails.poster.url;
    }
    if (isSeries && seriesDetailsForBackdrop?.poster?.url) {
      return seriesDetailsForBackdrop.poster.url;
    }
    return posterUrlParam;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop, posterUrlParam]);

  const backdropUrl = useMemo(() => {
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

  // Check if logo has dark pixels (black text on transparent background)
  const isLogoDark = useMemo(() => {
    if (!isSeries && movieDetails?.logo?.is_dark) return true;
    if (isSeries && seriesDetailsForBackdrop?.logo?.is_dark) return true;
    return false;
  }, [isSeries, movieDetails, seriesDetailsForBackdrop]);

  // Measure logo dimensions to calculate proper sizing within bounding box
  const [logoDimensions, setLogoDimensions] = useState<{ width: number; height: number } | null>(null);
  const [logoLoadFailed, setLogoLoadFailed] = useState(false);
  useEffect(() => {
    if (!logoUrl) {
      setLogoDimensions(null);
      setLogoLoadFailed(false);
      return;
    }
    setLogoLoadFailed(false);
    // Defer image measurement until after navigation animation completes
    const task = InteractionManager.runAfterInteractions(() => {
      RNImage.getSize(
        logoUrl,
        (width, height) => {
          setLogoDimensions({ width, height });
        },
        () => {
          setLogoDimensions(null);
          setLogoLoadFailed(true);
        }
      );
    });
    return () => task.cancel();
  }, [logoUrl]);

  // Title area starts invisible. Wait for metadata + logo resolution before showing anything.
  // Don't decide until metadata has loaded (logoUrl is derived from metadata, so it's null before that).
  const metadataLoaded = isSeries ? !seriesDetailsLoading : !movieDetailsLoading;
  const logoReady = !!(logoUrl && logoDimensions);
  const logoResolved = logoReady || (metadataLoaded && (!logoUrl || logoLoadFailed));
  const titleTextOpacity = useSharedValue(0);
  const logoOpacity = useSharedValue(0);
  // logoFadeReady is set to true once the page-level content fade has started,
  // so the logo/title don't fade in while the page is still invisible.
  const [logoFadeReady, setLogoFadeReady] = useState(false);
  useEffect(() => {
    if (!logoFadeReady) return;
    if (logoReady) {
      logoOpacity.value = withTiming(1, { duration: 300, easing: Easing.out(Easing.ease) });
    } else if (logoResolved) {
      // Metadata loaded but no logo — show title text
      titleTextOpacity.value = withTiming(1, { duration: 300, easing: Easing.out(Easing.ease) });
    }
  }, [logoReady, logoResolved, logoFadeReady]);
  const titleTextAnimatedStyle = useAnimatedStyle(() => ({ opacity: titleTextOpacity.value }));
  const logoAnimatedStyle = useAnimatedStyle(() => ({ opacity: logoOpacity.value }));

  // Preload poster/backdrop image so it's ready when page displays
  const [isPosterPreloaded, setIsPosterPreloaded] = useState(false);
  const posterPreloadedOnceRef = useRef(false);
  const posterToPreload = posterUrl || backdropUrl;
  useEffect(() => {
    if (!posterToPreload) {
      setIsPosterPreloaded(true);
      posterPreloadedOnceRef.current = true;
      return;
    }
    if (posterPreloadedOnceRef.current) {
      RNImage.prefetch(posterToPreload).catch(() => {});
      return;
    }
    setIsPosterPreloaded(false);
    RNImage.prefetch(posterToPreload)
      .then(() => {
        setIsPosterPreloaded(true);
        posterPreloadedOnceRef.current = true;
      })
      .catch(() => {
        setIsPosterPreloaded(true);
        posterPreloadedOnceRef.current = true;
      }); // Still show page on error
  }, [posterToPreload]);

  // Calculate logo style to maintain constant area across different aspect ratios
  const logoStyle = useMemo(() => {
    if (!logoDimensions) return styles.titleLogo;

    const { width: imgWidth, height: imgHeight } = logoDimensions;
    const aspectRatio = imgWidth / imgHeight;

    // Fixed target area in square pixels
    const baseTargetArea = isTV ? (tvScale * 120) * (tvScale * 120) * 3.4 : 80 * 80 * 2.1;

    // Perceptual boost for squarish logos
    const referenceAspectRatio = 5;
    const perceptualBoost = aspectRatio < referenceAspectRatio
      ? Math.pow(referenceAspectRatio / aspectRatio, 0.25)
      : 1;
    const targetArea = baseTargetArea * perceptualBoost;

    let finalWidth = Math.sqrt(targetArea * aspectRatio);
    let finalHeight = Math.sqrt(targetArea / aspectRatio);

    // Bounding box constraints
    const maxWidth = windowWidth * (isTV ? 0.3 : 0.8);
    const maxHeight = isTV ? tvScale * 216 : 120;

    if (finalWidth > maxWidth || finalHeight > maxHeight) {
      const scaleX = finalWidth > maxWidth ? maxWidth / finalWidth : 1;
      const scaleY = finalHeight > maxHeight ? maxHeight / finalHeight : 1;
      const scaleApplied = Math.min(scaleX, scaleY);
      finalWidth *= scaleApplied;
      finalHeight *= scaleApplied;
    }

    return {
      width: finalWidth,
      height: finalHeight,
    };
  }, [logoDimensions, windowWidth, isTV, tvScale, styles.titleLogo]);

  // Shadow/glow style for logo wrapper (iOS only)
  const logoGlowStyle = Platform.OS === 'ios' || Platform.OS === 'macos' ? {
    shadowColor: 'rgba(255, 255, 255, 0.2)',
    shadowOffset: { width: 0, height: 0 },
    shadowOpacity: 1,
    shadowRadius: 6,
  } : {};

  // Compute final description/overview, preferring params but falling back to fetched metadata
  const displayDescription = useMemo(() => {
    if (description) {
      return description;
    }
    if (isSeries && seriesDetailsForBackdrop?.overview) {
      return seriesDetailsForBackdrop.overview;
    }
    if (!isSeries && movieDetails?.overview) {
      return movieDetails.overview;
    }
    return '';
  }, [description, isSeries, seriesDetailsForBackdrop, movieDetails]);

  // Reset description height measurements when displayDescription changes
  useEffect(() => {
    setCollapsedHeight(0);
    setExpandedHeight(0);
    descriptionHeight.value = 0;
    setIsDescriptionExpanded(false);
  }, [displayDescription]);

  // On mobile, prefer portrait poster for background; on desktop/TV, prefer landscape backdrop
  const headerImage = useMemo(() => {
    return shouldUseAdaptiveHeroSizing ? posterUrl || backdropUrl : backdropUrl || posterUrl;
  }, [shouldUseAdaptiveHeroSizing, posterUrl, backdropUrl]);

  useEffect(() => {
    let cancelled = false;

    if (!headerImage || !shouldMeasureHeaderImage) {
      setHeaderImageDimensions(null);
      return () => {
        cancelled = true;
      };
    }

    // Defer image measurement until after navigation animation completes
    const task = InteractionManager.runAfterInteractions(() => {
      RNImage.getSize(
        headerImage,
        (width, height) => {
          if (cancelled) return;
          if (!width || !height) {
            setHeaderImageDimensions(null);
            return;
          }
          setHeaderImageDimensions({ width, height });
        },
        (error) => {
          if (cancelled) return;
          console.warn('[Details] Unable to measure header image size', error);
          setHeaderImageDimensions(null);
        },
      );
    });

    return () => {
      cancelled = true;
      task.cancel();
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

    const isPortraitArt = height >= width;

    if (isPortraitArt) {
      const desiredHeight = viewportHeight;
      const computedWidth = desiredHeight * aspectRatio;
      if (computedWidth <= viewportWidth) {
        return { height: desiredHeight, width: computedWidth };
      }
      const scaledHeight = viewportWidth / aspectRatio;
      return { width: viewportWidth, height: scaledHeight };
    }

    const desiredWidth = viewportWidth;
    const computedHeight = desiredWidth / aspectRatio;
    if (computedHeight <= viewportHeight) {
      return { width: desiredWidth, height: computedHeight };
    }
    const scaledWidth = viewportHeight * aspectRatio;
    return { width: scaledWidth, height: viewportHeight };
  }, [headerImageDimensions, shouldUseAdaptiveHeroSizing, styles.backgroundImageFill, windowHeight, windowWidth]);

  const isPortraitArtwork = useMemo(() => {
    if (!headerImageDimensions) return null;
    const { width, height } = headerImageDimensions;
    if (!width || !height) return null;
    return height >= width;
  }, [headerImageDimensions]);

  const backgroundImageResizeMode = useMemo<ImageResizeMode>(() => {
    if (Platform.isTV && isPortraitArtwork === true) {
      return 'contain';
    }
    return shouldUseAdaptiveHeroSizing ? 'contain' : 'cover';
  }, [isPortraitArtwork, shouldUseAdaptiveHeroSizing]);

  const shouldShowBlurredFill = useMemo(() => Platform.isTV && isPortraitArtwork === true, [isPortraitArtwork]);

  // ===== Hook 2: useTrailers (called before usePlayback; prequeueId bridged via effect) =====
  const trailersHook = useTrailers({
    primaryTrailer,
    autoPlayTrailersTV: autoPlayTrailersTV ?? false,
    isDetailsPageActive,
    prequeueId: null, // Bridged via effect below
  });

  // ===== Hook 3: usePlayback =====
  const playbackPreference = useMemo<PlaybackPreference>(() => {
    const userPref = userSettings?.playback?.preferredPlayer;
    const globalPref = settings?.playback?.preferredPlayer;
    const value = userPref || globalPref;
    if (value === 'outplayer' || value === 'infuse') {
      if (value === 'infuse' && Platform.OS === 'android') {
        return 'native';
      }
      return value;
    }
    return 'native';
  }, [userSettings?.playback?.preferredPlayer, settings?.playback?.preferredPlayer]);

  const playback = usePlayback({
    titleId,
    title,
    mediaType,
    isSeries,
    activeUserId,
    imdbId,
    tvdbId,
    tmdbId,
    yearNumber,
    seriesIdentifier,
    headerImage: headerImage || '',
    isIosWeb,
    isSelectBlocked,
    instanceId: '',
    router,
    settings,
    userSettings,
    playbackPreference,
    activeEpisode,
    nextUpEpisode,
    isShuffleMode,
    detailsBundle,
    bundleReady,
    activeUser: activeUser ?? null,
    showToast,
    hideToast,
    showLoadingScreen,
    hideLoadingScreen,
    setOnCancel,
    dismissTrailerAutoPlay: trailersHook.dismissTrailerAutoPlay,
    isDetailsPageActive,
    progressRefreshKey,
    setProgressRefreshKey,
  });

  // Bridge: stop trailer when content prequeue starts
  useEffect(() => {
    if (playback.prequeueId && trailersHook.isBackdropTrailerPlaying) {
      trailersHook.setIsBackdropTrailerPlaying(false);
      trailersHook.setIsTrailerImmersiveMode(false);
    }
  }, [playback.prequeueId]);

  // Bridge: don't auto-start trailer when content prequeue is active
  useEffect(() => {
    if (
      autoPlayTrailersTV &&
      trailersHook.trailerPrequeueStatus === 'ready' &&
      trailersHook.trailerStreamUrl &&
      !trailersHook.trailerAutoPlayDismissed &&
      playback.prequeueId
    ) {
      // Content prequeue is active, don't auto-start trailer
      return;
    }
  }, [autoPlayTrailersTV, trailersHook.trailerPrequeueStatus, trailersHook.trailerStreamUrl, trailersHook.trailerAutoPlayDismissed, playback.prequeueId]);

  // Parse initial season/episode from route params (must be computed before useEpisodeManager)
  const initialSeasonNumber = useMemo(() => {
    if (nextEpisodeFromPlayback) {
      return nextEpisodeFromPlayback.seasonNumber;
    }
    if (!initialSeasonParam || initialSeasonParam.trim() === '') {
      return null;
    }
    const parsed = Number(initialSeasonParam);
    return Number.isFinite(parsed) ? Math.trunc(parsed) : null;
  }, [initialSeasonParam, nextEpisodeFromPlayback]);

  const initialEpisodeNumber = useMemo(() => {
    if (nextEpisodeFromPlayback) {
      return nextEpisodeFromPlayback.episodeNumber;
    }
    if (!initialEpisodeParam || initialEpisodeParam.trim() === '') {
      return null;
    }
    const parsed = Number(initialEpisodeParam);
    return Number.isFinite(parsed) ? Math.trunc(parsed) : null;
  }, [initialEpisodeParam, nextEpisodeFromPlayback]);

  // ===== Hook 4: useEpisodeManager =====
  const episodeManager = useEpisodeManager({
    isSeries,
    seriesIdentifier,
    title,
    activeUserId,
    detailsBundle,
    bundleReady,
    resolveAndPlayRef: playback.resolveAndPlayRef,
    initialSeasonNumber,
    initialEpisodeNumber,
    dismissTrailerAutoPlay: trailersHook.dismissTrailerAutoPlay,
    showLoadingScreenIfEnabled: playback.showLoadingScreenIfEnabled,
    pendingShuffleModeRef: playback.pendingShuffleModeRef,
    nextEpisodeFromPlayback,
    setNextEpisodeFromPlayback,
    setCurrentProgress: playback.setCurrentProgress,
    setPendingPlaybackAction: playback.setPendingPlaybackAction,
    setResumeModalVisible: playback.setResumeModalVisible,
    pendingStartOffsetRef: playback.pendingStartOffsetRef,
    setSelectionError: playback.setSelectionError,
    setSelectionInfo: playback.setSelectionInfo,
    imdbId,
    tmdbId,
    tvdbId,
    activeEpisode,
    setActiveEpisode,
    nextUpEpisode,
    setNextUpEpisode,
    isShuffleMode,
    setIsShuffleMode,
  });

  // ===== Hook 5: useWatchActions =====
  const externalIds = useMemo(() => {
    const ids: Record<string, string> = {};
    if (tmdbId) ids.tmdb = tmdbId;
    if (imdbId) ids.imdb = imdbId;
    if (tvdbId) ids.tvdb = tvdbId;
    return Object.keys(ids).length ? ids : undefined;
  }, [imdbId, tmdbId, tvdbId]);

  const watchActions = useWatchActions({
    titleId,
    title,
    description,
    mediaType,
    isSeries,
    seriesIdentifier,
    yearNumber,
    posterUrl: posterUrl || '',
    backdropUrl: backdropUrl || '',
    externalIds,
    genres: detailsGenres,
    runtimeMinutes: movieDetails?.runtimeMinutes,
    activeUserId,
    addToWatchlist,
    removeFromWatchlist,
    getItem,
    isItemWatched,
    toggleWatchStatus,
    bulkUpdateWatchStatus,
    refreshWatchStatus,
    recordEpisodeWatch,
    allEpisodes: episodeManager.allEpisodes,
    activeEpisode,
    nextUpEpisode,
    findFirstEpisode: episodeManager.findFirstEpisode,
    findFirstEpisodeOfNextSeason: episodeManager.findFirstEpisodeOfNextSeason,
    findNextEpisode: episodeManager.findNextEpisode,
    handleEpisodeSelect: episodeManager.handleEpisodeSelect,
    toEpisodeReference: episodeManager.toEpisodeReference,
    dismissTrailerAutoPlay: trailersHook.dismissTrailerAutoPlay,
  });

  // ===== Hook 6: useManualSelectFlow =====
  const manualSelect = useManualSelectFlow({
    title,
    activeEpisode,
    nextUpEpisode,
    fetchIndexerResults: playback.fetchIndexerResults,
    getEpisodeSearchContext: playback.getEpisodeSearchContext,
    handleInitiatePlayback: playback.handleInitiatePlayback,
    checkAndShowResumeModal: playback.checkAndShowResumeModal,
    showLoadingScreenIfEnabled: playback.showLoadingScreenIfEnabled,
    hideLoadingScreen,
    setSelectionInfo: playback.setSelectionInfo,
    setSelectionError: playback.setSelectionError,
    setIsResolving: playback.setIsResolving,
    setShowBlackOverlay: playback.setShowBlackOverlay,
    dismissTrailerAutoPlay: trailersHook.dismissTrailerAutoPlay,
    abortControllerRef: playback.abortControllerRef,
  });

  // ===== Downloads =====
  const { startDownload } = useDownloads();

  const handleDownloadResult = useCallback(async (result: import('@/services/api').NZBResult) => {
    if (!titleId) return;
    manualSelect.closeManualPicker();
    showToast('Resolving stream for download...', { tone: 'info', duration: 3000 });
    try {
      // Inject episode context for debrid pack selection (packs have targetSeason but no targetEpisode)
      const ep = activeEpisode || nextUpEpisode;
      const resolveResult = ep ? {
        ...result,
        attributes: {
          ...(result.attributes || {}),
          targetSeason: result.attributes?.targetSeason || String(ep.seasonNumber),
          targetEpisode: result.attributes?.targetEpisode || String(ep.episodeNumber),
          targetEpisodeCode: result.attributes?.targetEpisodeCode ||
            `S${String(ep.seasonNumber).padStart(2, '0')}E${String(ep.episodeNumber).padStart(2, '0')}`,
        },
      } : result;
      const resolution = await apiService.resolvePlayback(resolveResult);
      if (!resolution.webdavPath) {
        showToast('Could not resolve stream path', { tone: 'danger', duration: 3000 });
        return;
      }
      await startDownload({
        titleId,
        mediaType: ep ? 'episode' : 'movie',
        title: ep ? `${title} S${String(ep.seasonNumber).padStart(2, '0')}E${String(ep.episodeNumber).padStart(2, '0')}` : title,
        posterUrl: headerImage,
        streamPath: resolution.webdavPath,
        fileSize: result.serviceType === 'debrid'
          ? (resolution.fileSize || result.sizeBytes || 0)   // debrid: provider knows actual file size
          : (result.sizeBytes || resolution.fileSize || 0),  // usenet: indexer size > NZB-parsed size
        seriesTitle: isSeries ? title : undefined,
        seasonNumber: ep?.seasonNumber,
        episodeNumber: ep?.episodeNumber,
        episodeName: ep?.name,
        imdbId: imdbId || undefined,
        tvdbId: tvdbId || undefined,
        seriesIdentifier: seriesIdentifier || undefined,
      });
      showToast('Download started', { tone: 'success', duration: 2000 });
    } catch (err: any) {
      showToast(err?.message || 'Download failed', { tone: 'danger', duration: 3000 });
    }
  }, [titleId, activeEpisode, nextUpEpisode, title, headerImage, isSeries, startDownload, showToast, manualSelect, imdbId, tvdbId, seriesIdentifier]);

  const handleMenuDownload = useCallback(() => {
    setMoreOptionsVisible(false);
    setManualSelectDownloadOnly(true);
    manualSelect.handleManualSelect();
  }, [manualSelect]);

  const handleMenuDownloadSeason = useCallback(() => {
    setMoreOptionsVisible(false);
    setSeasonDownloadMode(true);
    setSeasonSelectorVisible(true);
  }, []);

  const handleSeasonDownloadSearch = useCallback(async (season: SeriesSeason) => {
    setSeasonSelectorVisible(false);
    setSeasonDownloadTarget(season);

    // Open manual selection in loading state
    manualSelect.setManualVisible(true);
    manualSelect.setManualError(null);
    manualSelect.setManualResults([]);
    manualSelect.setManualLoading(true);
    setManualSelectDownloadOnly(true);

    try {
      const query = buildSeasonQuery(title, season.number);
      const results = await playback.fetchIndexerResults({ query, limit: 50 });
      manualSelect.setManualResults(results);
      if (!results || results.length === 0) {
        manualSelect.setManualError('No results found for this season.');
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to search for season packs.';
      manualSelect.setManualError(message);
    } finally {
      manualSelect.setManualLoading(false);
    }
  }, [title, manualSelect, playback]);

  const handleSeasonDownloadResult = useCallback(async (result: import('@/services/api').NZBResult) => {
    if (!titleId || !seasonDownloadTarget) return;
    manualSelect.closeManualPicker();

    const episodes = seasonDownloadTarget.episodes;
    if (!episodes || episodes.length === 0) {
      showToast('No episodes found for this season', { tone: 'danger', duration: 3000 });
      return;
    }

    showToast(`Queuing ${episodes.length} episodes for download...`, { tone: 'info', duration: 3000 });

    let successCount = 0;
    let failCount = 0;

    // Use batch resolve for debrid results (single API round-trip for all episodes)
    if (result.serviceType === 'debrid') {
      try {
        const targets: import('@/services/api').BatchEpisodeTarget[] = episodes.map((ep) => ({
          seasonNumber: ep.seasonNumber,
          episodeNumber: ep.episodeNumber,
          episodeCode: `S${String(ep.seasonNumber).padStart(2, '0')}E${String(ep.episodeNumber).padStart(2, '0')}`,
          absoluteEpisodeNumber: ep.absoluteEpisodeNumber,
          airDate: ep.airedDate,
        }));

        const batchResp = await apiService.resolvePlaybackBatch(result, targets);

        for (const epResult of batchResp.results) {
          const ep = episodes.find(
            (e) => e.seasonNumber === epResult.seasonNumber && e.episodeNumber === epResult.episodeNumber,
          );
          const epCode = epResult.episodeCode || `S${String(epResult.seasonNumber).padStart(2, '0')}E${String(epResult.episodeNumber).padStart(2, '0')}`;

          if (!epResult.resolution?.webdavPath) {
            failCount++;
            continue;
          }
          try {
            await startDownload({
              titleId,
              mediaType: 'episode',
              title: `${title} ${epCode}`,
              posterUrl: headerImage,
              streamPath: epResult.resolution.webdavPath,
              fileSize: epResult.resolution.fileSize || result.sizeBytes || 0,
              seriesTitle: title,
              seasonNumber: epResult.seasonNumber,
              episodeNumber: epResult.episodeNumber,
              episodeName: ep?.name,
              imdbId: imdbId || undefined,
              tvdbId: tvdbId || undefined,
              seriesIdentifier: seriesIdentifier || undefined,
            });
            successCount++;
          } catch {
            failCount++;
          }
        }
      } catch {
        // Batch call failed entirely — fall back to sequential resolve
        for (const ep of episodes) {
          try {
            const epCode = `S${String(ep.seasonNumber).padStart(2, '0')}E${String(ep.episodeNumber).padStart(2, '0')}`;
            const resolveResult = {
              ...result,
              attributes: {
                ...(result.attributes || {}),
                targetSeason: String(ep.seasonNumber),
                targetEpisode: String(ep.episodeNumber),
                targetEpisodeCode: epCode,
              },
            };
            const resolution = await apiService.resolvePlayback(resolveResult);
            if (!resolution.webdavPath) {
              failCount++;
              continue;
            }
            await startDownload({
              titleId,
              mediaType: 'episode',
              title: `${title} ${epCode}`,
              posterUrl: headerImage,
              streamPath: resolution.webdavPath,
              fileSize: resolution.fileSize || result.sizeBytes || 0,
              seriesTitle: title,
              seasonNumber: ep.seasonNumber,
              episodeNumber: ep.episodeNumber,
              episodeName: ep.name,
              imdbId: imdbId || undefined,
              tvdbId: tvdbId || undefined,
              seriesIdentifier: seriesIdentifier || undefined,
            });
            successCount++;
          } catch {
            failCount++;
          }
        }
      }
    } else {
      // Non-debrid: keep existing sequential resolve
      for (const ep of episodes) {
        try {
          const epCode = `S${String(ep.seasonNumber).padStart(2, '0')}E${String(ep.episodeNumber).padStart(2, '0')}`;
          const resolveResult = {
            ...result,
            attributes: {
              ...(result.attributes || {}),
              targetSeason: String(ep.seasonNumber),
              targetEpisode: String(ep.episodeNumber),
              targetEpisodeCode: epCode,
            },
          };
          const resolution = await apiService.resolvePlayback(resolveResult);
          if (!resolution.webdavPath) {
            failCount++;
            continue;
          }
          await startDownload({
            titleId,
            mediaType: 'episode',
            title: `${title} ${epCode}`,
            posterUrl: headerImage,
            streamPath: resolution.webdavPath,
            fileSize: resolution.fileSize || result.sizeBytes || 0,
            seriesTitle: title,
            seasonNumber: ep.seasonNumber,
            episodeNumber: ep.episodeNumber,
            episodeName: ep.name,
            imdbId: imdbId || undefined,
            tvdbId: tvdbId || undefined,
            seriesIdentifier: seriesIdentifier || undefined,
          });
          successCount++;
        } catch {
          failCount++;
        }
      }
    }

    if (failCount === 0) {
      showToast(`${successCount} episodes queued for download`, { tone: 'success', duration: 3000 });
    } else {
      showToast(`${successCount} queued, ${failCount} failed`, { tone: successCount > 0 ? 'info' : 'danger', duration: 4000 });
    }
  }, [titleId, seasonDownloadTarget, title, headerImage, startDownload, showToast, manualSelect, imdbId, tvdbId, seriesIdentifier]);

  // ===== Focus effect: consume next episode from playback navigation =====
  useFocusEffect(
    useCallback(() => {
      if (titleId) {
        const nextEp = playbackNavigation.consumeNextEpisode(titleId);
        if (nextEp) {
          // Batch all setState calls to prevent multiple renders from focus effect
          unstable_batchedUpdates(() => {
            setNextEpisodeFromPlayback(nextEp);
            setIsShuffleMode(nextEp.shuffleMode);
          });
          playback.pendingShuffleModeRef.current = nextEp.shuffleMode;

          // Store prequeue data from navigation if present
          if (nextEp.prequeueId) {
            playback.navigationPrequeueIdRef.current = {
              prequeueId: nextEp.prequeueId,
              targetEpisode: {
                seasonNumber: nextEp.seasonNumber,
                episodeNumber: nextEp.episodeNumber,
              },
            };
            if (nextEp.prequeueStatus && apiService.isPrequeueReady(nextEp.prequeueStatus.status)) {
              playback.navigationPrequeueStatusRef.current = nextEp.prequeueStatus;
            }
          }

          // Try to select/play the episode immediately if episodes are loaded
          if (episodeManager.allEpisodesRef.current.length > 0) {
            const matchingEpisode = episodeManager.allEpisodesRef.current.find(
              (ep) => ep.seasonNumber === nextEp.seasonNumber && ep.episodeNumber === nextEp.episodeNumber,
            );
            if (matchingEpisode) {
              if (nextEp.autoPlay && episodeManager.handlePlayEpisodeRef.current) {
                episodeManager.handlePlayEpisodeRef.current(matchingEpisode);
                setNextEpisodeFromPlayback(null);
              } else if (episodeManager.handleEpisodeSelectRef.current) {
                episodeManager.handleEpisodeSelectRef.current(matchingEpisode);
              }
            }
          }
        }
      }
    }, [titleId]),
  );

  // ===== Derived display values =====
  // credits, ratings, genres, certification are pre-computed by useDetailsData
  const credits = detailsCredits;
  const ratings = detailsRatings;
  const genres = detailsGenres;
  const certification = detailsCertification;

  // Stable baseUrl for rating icons — only changes if backend URL changes
  const ratingBaseUrl = useMemo(() => apiService.getBaseUrl().replace(/\/$/, ''), []);

  const shouldShowRatingsSkeleton = isMetadataLoadingForSkeleton && ratings.length === 0;

  const describeRelease = useCallback((release?: Title['homeRelease']) => {
    if (!release?.date) return '';
    const dateLabel = formatPublishDate(release.date) || release.date;
    const parts = [dateLabel];
    if (release.country) {
      parts.push(release.country.toUpperCase());
    }
    return parts.filter(Boolean).join(' . ');
  }, []);

  const getHomeReleaseIcon = useCallback((release?: Title['homeRelease']): keyof typeof Ionicons.glyphMap => {
    const type = release?.type?.toLowerCase();
    switch (type) {
      case 'digital': return 'cloud-outline';
      case 'physical': return 'disc-outline';
      case 'tv': return 'tv-outline';
      default: return 'home-outline';
    }
  }, []);

  const releaseRows = useMemo(() => {
    if (isSeries || !movieDetails) return [];
    const rows: { key: string; icon: keyof typeof Ionicons.glyphMap; value: string }[] = [];
    if (movieDetails.theatricalRelease) {
      const value = describeRelease(movieDetails.theatricalRelease);
      if (value) rows.push({ key: 'theatrical', icon: 'film-outline', value });
    }
    if (movieDetails.homeRelease) {
      const value = describeRelease(movieDetails.homeRelease);
      if (value) rows.push({ key: 'home', icon: getHomeReleaseIcon(movieDetails.homeRelease), value });
    }
    return rows;
  }, [describeRelease, getHomeReleaseIcon, isSeries, movieDetails]);

  const shouldShowReleaseSkeleton = !isSeries && movieDetailsLoading && releaseRows.length === 0;
  const releaseErrorMessage =
    !isSeries && movieDetailsError && !movieDetailsLoading && releaseRows.length === 0 ? movieDetailsError : null;

  const releaseSkeletonRows = useMemo(() => {
    if (isSeries || !shouldShowReleaseSkeleton) return [];
    return [
      { key: 'theatrical-skeleton', icon: 'film-outline' as keyof typeof Ionicons.glyphMap, value: '\u2014' },
      { key: 'home-skeleton', icon: 'home-outline' as keyof typeof Ionicons.glyphMap, value: '\u2014' },
    ];
  }, [isSeries, shouldShowReleaseSkeleton]);

  const episodeToPlayCode = episodeManager.episodeToPlayCode;
  const watchNowLabel = Platform.isTV
    ? isSeries && episodeToPlayCode
      ? `${!episodeManager.hasWatchedEpisodes ? 'Play' : 'Up Next'} ${episodeToPlayCode}`
      : !isSeries || !episodeManager.hasWatchedEpisodes
        ? 'Play'
        : 'Up Next'
    : playback.isResolving
      ? 'Resolving\u2026'
      : !isSeries || !episodeManager.hasWatchedEpisodes
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

  const trailerButtonLabel = useMemo(() => (trailersLoading ? 'Loading trailer\u2026' : 'Watch trailer'), [trailersLoading]);
  const trailerButtonDisabled = trailersLoading || !hasAvailableTrailer;

  const displayProgress = playback.displayProgress;
  const episodeProgressMap = playback.episodeProgressMap;

  // ===== Handlers =====
  const handleWatchTrailer = useCallback(() => {
    if (autoPlayTrailersTV && trailersHook.trailerStreamUrl) {
      trailersHook.setIsBackdropTrailerPlaying((prev) => !prev);
      trailersHook.setIsTrailerImmersiveMode(false);
      return;
    }
    trailersHook.dismissTrailerAutoPlay();
    const nextTrailer = primaryTrailer ?? trailers[0];
    if (!nextTrailer) return;
    setActiveTrailer(nextTrailer);
    setTrailerModalVisible(true);
  }, [autoPlayTrailersTV, trailersHook, primaryTrailer, trailers]);

  const handleViewCollection = useCallback(() => {
    if (!movieDetails?.collection) return;
    trailersHook.dismissTrailerAutoPlay();
    router.push({
      pathname: '/watchlist',
      params: {
        collection: String(movieDetails.collection.id),
        collectionName: encodeURIComponent(movieDetails.collection.name),
      },
    });
  }, [movieDetails?.collection, trailersHook, router]);

  // More Options menu handlers
  const handleMenuShuffleShow = useCallback(() => {
    setMoreOptionsVisible(false);
    episodeManager.handleShufflePlay();
  }, [episodeManager]);

  const handleMenuShuffleSeason = useCallback(() => {
    setMoreOptionsVisible(false);
    episodeManager.handleShuffleSeasonPlay();
  }, [episodeManager]);

  const handleRemoveFromContinueWatching = useCallback(() => {
    if (!seriesIdentifier) return;
    setMoreOptionsVisible(false);
    hideFromContinueWatching(seriesIdentifier)
      .then(() => {
        showToast('Removed from Continue Watching', { tone: 'success', duration: 3000 });
      })
      .catch(() => {
        showToast('Failed to remove from Continue Watching', { tone: 'danger', duration: 3000 });
      });
  }, [seriesIdentifier, hideFromContinueWatching, showToast]);

  const handleSimilarTitlePress = useCallback(
    (item: Title) => {
      router.replace({
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
          fromSimilar: '1',
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

  const handleSeasonSelectorSelect = useCallback((season: SeriesSeason) => {
    episodeManager.setSelectedSeason(season);
    setSeasonSelectorVisible(false);
    setEpisodeSelectorVisible(true);
  }, [episodeManager]);

  const handleMobileSeasonSelect = useCallback((season: SeriesSeason) => {
    episodeManager.setSelectedSeason(season);
    setSeasonSelectorVisible(false);
  }, [episodeManager]);

  const handleEpisodeSelectorSelect = useCallback((episode: SeriesEpisode) => {
    setActiveEpisode(episode);
    setEpisodeSelectorVisible(false);
  }, []);

  const handleEpisodeSelectorBack = useCallback(() => {
    setEpisodeSelectorVisible(false);
    setSeasonSelectorVisible(true);
  }, []);

  const handleRegisterSeasonFocusHandler = useCallback((_handler: (() => boolean) | null) => {
    // No-op: spatial navigation removed
  }, []);

  const handleRequestFocusShift = useCallback(() => {
    // No-op: spatial navigation removed
  }, []);

  const { healthChecks: manualHealthChecks, checkHealth: checkManualHealth } = useManualHealthChecks(manualSelect.manualResults);

  // ===== TV scroll and animation =====
  const tvScrollViewRef = useRef<Animated.ScrollView>(null);
  const currentTVFocusAreaRef = useRef<string | null>(null);
  const actionRowRef = useRef<View>(null);
  // Section refs for scroll-to-section on TV (measureLayout targets)
  const sectionRefs = useRef<Record<string, View | null>>({});
  // Cache measured positions to avoid expensive measureLayout calls on Android
  const sectionPositionsRef = useRef<Record<string, number>>({});
  const showTrailerFullscreen = Platform.isTV && autoPlayTrailersTV && trailersHook.isBackdropTrailerPlaying && !trailersHook.isTrailerImmersiveMode;
  const tvScrollY = useSharedValue(0);

  const scrollToSection = useCallback(
    (sectionKey: string, animated = true) => {
      if (!Platform.isTV) return;

      // Per-section viewport offset: how far from the TOP of the screen the section should appear.
      // 'actions' sits near the bottom so the backdrop art stays visible above.
      // Other sections sit near the top with a small inset.
      const getSectionViewportOffset = (key: string): number => {
        if (key === 'actions') {
          // Place action row near the bottom of the screen (higher value = lower on screen)
          const actionsRatio = isAndroidTVPlatform ? 0.85 : 0.88;
          return Math.round(windowHeight * actionsRatio) + (isAndroidTVPlatform ? 10 : -30);
        }
        // Episodes, cast, similar — small offset from top so heading is visible
        return Math.round(windowHeight * 0.12);
      };

      const performScroll = (rawY: number, key: string) => {
        const viewportOffset = getSectionViewportOffset(key);
        const targetY = Math.max(0, rawY - viewportOffset);
        tvScrollViewRef.current?.scrollTo({ y: targetY, animated });
      };

      // Check cache first (raw measured position)
      const cachedPosition = sectionPositionsRef.current[sectionKey];
      if (cachedPosition !== undefined) {
        performScroll(cachedPosition, sectionKey);
        return;
      }

      const tryMeasure = () => {
        const sectionRef = sectionRefs.current[sectionKey];
        const scrollViewNode = tvScrollViewRef.current;
        if (!sectionRef || !scrollViewNode) return false;

        sectionRef.measureLayout(
          scrollViewNode as any,
          (_left, top) => {
            sectionPositionsRef.current[sectionKey] = top;
            performScroll(top, sectionKey);
          },
          () => { /* silently fail */ },
        );
        return true;
      };

      // Measure layout on first access, then cache.
      // If ref isn't ready yet (initial render), retry after next frame.
      if (!tryMeasure()) {
        requestAnimationFrame(() => tryMeasure());
      }
    },
    [windowHeight],
  );

  const handleTVFocusAreaChange = useCallback(
    (area: 'seasons' | 'episodes' | 'actions' | 'cast' | 'similar') => {
      if (!Platform.isTV) return;
      if (currentTVFocusAreaRef.current === area) return;
      // Capture the actions scroll position before leaving
      if (currentTVFocusAreaRef.current === 'actions' && area !== 'actions') {
        trailersHook.dismissTrailerAutoPlay();
      }
      currentTVFocusAreaRef.current = area;
      if (area === 'actions') {
        scrollToSection('actions');
      } else {
        scrollToSection(area);
      }
    },
    [trailersHook.dismissTrailerAutoPlay, scrollToSection],
  );

  // On Android TV (low-RAM devices), unmount heavy content when the player is active
  const isAndroidTV = Platform.OS === 'android' && Platform.isTV;

  // ===== Visibility gate =====
  const hasBeenDisplayedRef = useRef(false);
  const isMetadataLoading = isSeries ? seriesDetailsLoading : movieDetailsLoading;
  const isPosterReady = isPosterPreloaded;
  // On Android TV, bypass the gate when nav params provide enough content to render immediately
  // (title text fallback + backdrop from params). This eliminates ~3s blank screen on Fire Stick.
  const hasNavParamContent = !!title && !!(headerImageParam || posterUrlParam || backdropUrlParam);
  const shouldHideUntilMetadataReady = (isTV || isMobile) && !hasBeenDisplayedRef.current &&
    !(isAndroidTV && hasNavParamContent) &&
    (isMetadataLoading || !isPosterReady);
  if (!shouldHideUntilMetadataReady && (isTV || isMobile)) {
    hasBeenDisplayedRef.current = true;
  }

  // Defer the hidden SeriesEpisodes pre-mount until after the first paint,
  // so it doesn't compete with the critical render path for JS thread time.
  const [deferredSeriesReady, setDeferredSeriesReady] = useState(false);
  useEffect(() => {
    if (!shouldHideUntilMetadataReady && isSeries && isTV) {
      const id = requestAnimationFrame(() => setDeferredSeriesReady(true));
      return () => cancelAnimationFrame(id);
    }
  }, [shouldHideUntilMetadataReady, isSeries, isTV]);

  // Fade in background and content on all platforms
  const backgroundOpacity = useSharedValue(0);
  const contentOpacity = useSharedValue(0);
  const backgroundAnimatedStyle = useAnimatedStyle(() => ({
    opacity: backgroundOpacity.value,
    ...(Platform.isTV ? { transform: [{ translateY: -tvScrollY.value * 0.4 }] } : {}),
  }));
  const contentAnimatedStyle = useAnimatedStyle(() => ({
    opacity: contentOpacity.value,
  }));
  const tvScrollHandler = useAnimatedScrollHandler({
    onScroll: (event) => {
      tvScrollY.value = event.contentOffset.y;
    },
  });

  // Invalidate section position cache when content height changes significantly
  const lastTVContentHeightRef = useRef(0);
  const handleTVContentSizeChange = useCallback((_width: number, height: number) => {
    if (Platform.isTV && height !== lastTVContentHeightRef.current) {
      sectionPositionsRef.current = {};
    }
    lastTVContentHeightRef.current = height;
  }, []);

  // TV spacer — fixed height (pushes content below the hero image)
  // Android TV has roughly half the dp coordinate space of tvOS (due to ~2x density),
  // so we use a smaller ratio to keep the action row visible on screen
  const tvSpacerHeight = useMemo(() => {
    if (!Platform.isTV) return 0;
    const ratio = isAndroidTVPlatform ? 0.45 : 0.7;
    return Math.round(windowHeight * ratio);
  }, [windowHeight]);

  // Track if we've already triggered the fade-in
  const hasTriggeredFadeIn = useRef(false);

  // On TV/mobile the visibility gate delays rendering, so trigger fade when it opens.
  // On web/desktop there's no gate — trigger fade once the poster image has preloaded.
  const readyToFadeIn = (isTV || isMobile)
    ? !shouldHideUntilMetadataReady
    : isPosterPreloaded;

  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    if (readyToFadeIn && !hasTriggeredFadeIn.current) {
      hasTriggeredFadeIn.current = true;
      cancelAnimation(backgroundOpacity);
      cancelAnimation(contentOpacity);
      backgroundOpacity.value = 0;
      contentOpacity.value = 0;
      timer = setTimeout(() => {
        backgroundOpacity.value = withTiming(1, { duration: 400, easing: Easing.out(Easing.ease) });
        contentOpacity.value = withTiming(1, { duration: 350, easing: Easing.out(Easing.ease) });
        // Allow logo/title to begin their own fade now that content is becoming visible
        setLogoFadeReady(true);
      }, 16);
    }
    return () => {
      if (timer) clearTimeout(timer);
    };
  }, [readyToFadeIn, backgroundOpacity, contentOpacity]);

  if (isAndroidTV && !isDetailsPageActive) {
    return (
      <>
        <Stack.Screen options={{ headerShown: false }} />
        <View style={{ flex: 1, backgroundColor: '#0b0b0f' }} />
      </>
    );
  }

  // Spatial navigation: disable when any modal with its own SpatialNavigationRoot is open
  const isSpatialNavActive = isDetailsPageActive && !seasonSelectorVisible && !trailerModalVisible
    && !playback.resumeModalVisible && !watchActions.bulkWatchModalVisible
    && !manualSelect.manualVisible && !episodeSelectorVisible && !moreOptionsVisible;

  // Force spatial nav tree to rebuild when track selectors appear.
  // Track selectors mount late (after prequeue resolves) and would register
  // after episodes/cast/similar in the LRUD tree, breaking focus order.
  // Changing the key causes a one-time remount that re-registers all nodes in DOM order.
  const spatialNavKey = (playback.prequeueDisplayInfo?.audioTracks?.length ?? 0) > 0 ||
    (playback.prequeueDisplayInfo?.subtitleTracks?.length ?? 0) > 0 ? 'with-tracks' : 'base';

  // ===== Render helpers =====
  const renderDetailsContent = () => (
    <>
      <View style={[styles.topContent, isTV && styles.topContentTV, isMobile && styles.topContentMobile]}>
        <View style={[styles.titleRow, { overflow: 'visible' }]}>
          {/* Title text always renders to reserve layout height */}
          <Animated.Text style={[styles.title, titleTextAnimatedStyle]}>{title}</Animated.Text>
          {/* Logo anchored at bottom of title row, grows upward */}
          {logoUrl && logoDimensions && (
            <Animated.View style={[{ position: 'absolute', bottom: 0, left: isTV ? 0 : -12, padding: 12, overflow: 'visible' }, logoGlowStyle, logoAnimatedStyle]}>
              <RNImage
                source={{ uri: logoUrl }}
                style={[logoStyle, isLogoDark ? { tintColor: 'white' } : undefined]}
                resizeMode="contain"
              />
            </Animated.View>
          )}
        </View>
        <MemoizedRatingsRow
          ratings={ratings}
          shouldShowSkeleton={shouldShowRatingsSkeleton}
          iconSize={Math.round((isTV ? 17 : 14) * tvScale)}
          baseUrl={ratingBaseUrl}
          styles={styles}
        />
        <MemoizedGenresRow
          certification={certification}
          genres={genres}
          tvScale={tvScale}
          separatorColor={theme.colors.text.secondary}
          styles={styles}
        />
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
                <Ionicons name="volume-high" size={14 * tvScale} color={theme.colors.text.secondary} style={{ marginRight: 4 * tvScale }} />
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
                <Ionicons name="text" size={14 * tvScale} color={theme.colors.text.secondary} style={{ marginRight: 4 * tvScale }} />
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
                <Ionicons name="text" size={14 * tvScale} color={theme.colors.text.secondary} style={{ marginRight: 4 * tvScale }} />
                <Text style={{ color: theme.colors.text.secondary, fontSize: 12 * tvScale }}>OFF</Text>
              </View>
            )}
          </View>
        )}
        <MemoizedReleaseInfo
          releaseRows={releaseRows}
          releaseSkeletonRows={releaseSkeletonRows}
          shouldShowSkeleton={shouldShowReleaseSkeleton}
          errorMessage={releaseErrorMessage}
          isSeries={isSeries}
          runtimeMinutes={movieDetails?.runtimeMinutes}
          iconSize={Math.round(14 * tvScale)}
          iconColor={theme.colors.text.secondary}
          styles={styles}
        />
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
              <Text
                style={[styles.description, styles.descriptionHidden]}
                numberOfLines={4}
                onLayout={(e) => {
                  const height = e.nativeEvent.layout.height;
                  if (height > 0 && collapsedHeight === 0) {
                    const bufferedHeight = height + 4;
                    setCollapsedHeight(bufferedHeight);
                    descriptionHeight.value = bufferedHeight;
                  }
                }}>
                {displayDescription}
              </Text>
              <Text
                style={[styles.description, styles.descriptionHidden]}
                onLayout={(e) => {
                  const height = e.nativeEvent.layout.height;
                  if (height > 0 && expandedHeight === 0) {
                    setExpandedHeight(height + 4);
                  }
                }}>
                {displayDescription}
              </Text>
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
          <Text
            style={[styles.description, !displayDescription && isMetadataLoadingForSkeleton && { minHeight: tvScale * 60 }]}
          >{displayDescription}</Text>
        )}
      </View>
      <View style={[styles.bottomContent, isMobile && styles.mobileBottomContent]}>
        {/* Action Row */}
        {Platform.isTV ? (
          <SpatialNavigationNode orientation="horizontal">
            <View ref={(ref) => { sectionRefs.current['actions'] = ref; }} style={styles.actionRow}>
              <TVActionButton
                icon="play"
                onSelect={playback.handleWatchNow}
                onFocus={() => handleTVFocusAreaChange('actions')}
                disabled={playback.isResolving || (isSeries && episodeManager.episodesLoading)}
                loading={playback.isResolving || (isSeries && episodeManager.episodesLoading)}
                showReadyPip={playback.prequeueReady}
                badge={(() => {
                  if (isSeries) {
                    return isEpisodeUnreleased((activeEpisode || nextUpEpisode)?.airedDate, (activeEpisode || nextUpEpisode)?.airedDateTimeUTC) ? 'unreleased' : undefined;
                  }
                  return isMovieUnreleased(movieDetails?.homeRelease, movieDetails?.theatricalRelease) ? 'unreleased' : undefined;
                })()}
                variant="primary"
                autoFocus
              />
              <TVActionButton
                icon="search"
                onSelect={manualSelect.handleManualSelect}
                onFocus={() => handleTVFocusAreaChange('actions')}
                disabled={isSeries && episodeManager.episodesLoading}
              />
              {shouldShowDebugPlayerButton && (
                <TVActionButton
                  icon="bug"
                  onSelect={playback.handleLaunchDebugPlayer}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  disabled={playback.isResolving || (isSeries && episodeManager.episodesLoading)}
                />
              )}
              {isSeries && (
                <TVActionButton
                  icon="list"
                  onSelect={() => {
                    trailersHook.dismissTrailerAutoPlay();
                    setSeasonSelectorVisible(true);
                  }}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                />
              )}
              {(isSeries || isInContinueWatching) && (
                <TVActionButton
                  icon="ellipsis-vertical"
                  onSelect={() => setMoreOptionsVisible(true)}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                />
              )}
              <TVActionButton
                icon={watchActions.isWatchlisted ? 'bookmark' : 'bookmark-outline'}
                onSelect={watchActions.handleToggleWatchlist}
                onFocus={() => handleTVFocusAreaChange('actions')}
                loading={watchActions.watchlistBusy}
                disabled={!watchActions.canToggleWatchlist || watchActions.watchlistBusy}
              />
              <TVActionButton
                icon={watchActions.isWatched ? 'eye' : 'eye-outline'}
                onSelect={watchActions.handleToggleWatched}
                onFocus={() => handleTVFocusAreaChange('actions')}
                loading={watchActions.watchlistBusy}
                disabled={watchActions.watchlistBusy}
              />
              {(trailersLoading || hasAvailableTrailer) && (
                <TVActionButton
                  icon="videocam"
                  onSelect={handleWatchTrailer}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                  loading={trailersLoading}
                  disabled={trailerButtonDisabled}
                />
              )}
              {!isSeries && movieDetails?.collection && (
                <TVActionButton
                  icon="albums"
                  onSelect={handleViewCollection}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                />
              )}
              {showTrailerFullscreen && (
                <TVActionButton
                  icon="expand"
                  onSelect={() => trailersHook.setIsTrailerImmersiveMode(true)}
                  onFocus={() => handleTVFocusAreaChange('actions')}
                />
              )}
              {displayProgress !== null && displayProgress > 0 && !activeEpisode && (
                <View style={styles.progressIndicator}>
                  <Text style={styles.progressIndicatorText}>
                    {`${displayProgress}%`}
                  </Text>
                </View>
              )}
            </View>
          </SpatialNavigationNode>
        ) : (
          <View ref={actionRowRef} style={[styles.actionRow, useCompactActionLayout && styles.compactActionRow]}>
            <FocusablePressable
              focusKey="watch-now"
              text={!useCompactActionLayout ? watchNowLabel : undefined}
              icon={useCompactActionLayout ? 'play' : undefined}
              accessibilityLabel={watchNowLabel}
              onSelect={playback.handleWatchNow}
              disabled={playback.isResolving || (isSeries && episodeManager.episodesLoading)}
              loading={playback.isResolving || (isSeries && episodeManager.episodesLoading)}
              style={useCompactActionLayout ? styles.iconActionButton : styles.primaryActionButton}
              showReadyPip={playback.prequeueReady}
              badge={(() => {
                if (isSeries) {
                  return isEpisodeUnreleased((activeEpisode || nextUpEpisode)?.airedDate, (activeEpisode || nextUpEpisode)?.airedDateTimeUTC) ? 'unreleased' : undefined;
                }
                return isMovieUnreleased(movieDetails?.homeRelease, movieDetails?.theatricalRelease) ? 'unreleased' : undefined;
              })()}
            />
            <FocusablePressable
              focusKey="manual-select"
              text={!useCompactActionLayout ? manualSelectLabel : undefined}
              icon={useCompactActionLayout ? 'search' : undefined}
              accessibilityLabel={manualSelectLabel}
              onSelect={manualSelect.handleManualSelect}
              disabled={isSeries && episodeManager.episodesLoading}
              style={useCompactActionLayout ? styles.iconActionButton : styles.manualActionButton}
            />
            {shouldShowDebugPlayerButton && (
              <FocusablePressable
                focusKey="debug-player"
                text={!useCompactActionLayout ? 'Debug Player' : undefined}
                icon={useCompactActionLayout ? 'bug' : undefined}
                accessibilityLabel="Launch debug player overlay"
                onSelect={playback.handleLaunchDebugPlayer}
                disabled={playback.isResolving || (isSeries && episodeManager.episodesLoading)}
                style={useCompactActionLayout ? styles.iconActionButton : styles.debugActionButton}
              />
            )}
            {isSeries && (
              <FocusablePressable
                focusKey="select-episode"
                text={!useCompactActionLayout ? 'Select' : undefined}
                icon={useCompactActionLayout ? 'list' : undefined}
                accessibilityLabel="Select Episode"
                onSelect={() => {
                  trailersHook.dismissTrailerAutoPlay();
                  setSeasonSelectorVisible(true);
                }}
                style={useCompactActionLayout ? styles.iconActionButton : styles.manualActionButton}
              />
            )}
            {(isSeries || isInContinueWatching) && (
              <FocusablePressable
                focusKey="more-options"
                text={!useCompactActionLayout ? 'More' : undefined}
                icon="ellipsis-vertical"
                accessibilityLabel="More options"
                onSelect={() => setMoreOptionsVisible(true)}
                style={useCompactActionLayout ? styles.iconActionButton : styles.manualActionButton}
              />
            )}
            <FocusablePressable
              focusKey="toggle-watchlist"
              text={!useCompactActionLayout ? (watchActions.watchlistBusy ? 'Saving...' : watchActions.watchlistButtonLabel) : undefined}
              icon={
                useCompactActionLayout
                  ? watchActions.isWatchlisted
                    ? 'bookmark'
                    : 'bookmark-outline'
                  : undefined
              }
              accessibilityLabel={watchActions.watchlistBusy ? 'Saving watchlist change' : watchActions.watchlistButtonLabel}
              onSelect={watchActions.handleToggleWatchlist}
              loading={watchActions.watchlistBusy}
              style={[
                useCompactActionLayout ? styles.iconActionButton : styles.watchlistActionButton,
                watchActions.isWatchlisted && styles.watchlistActionButtonActive,
              ]}
              disabled={!watchActions.canToggleWatchlist || watchActions.watchlistBusy}
            />
            <FocusablePressable
              focusKey="toggle-watched"
              text={!useCompactActionLayout ? (watchActions.watchlistBusy ? 'Saving...' : watchActions.watchStateButtonLabel) : undefined}
              icon={useCompactActionLayout ? (watchActions.isWatched ? 'eye' : 'eye-outline') : undefined}
              accessibilityLabel={watchActions.watchlistBusy ? 'Saving watched state' : watchActions.watchStateButtonLabel}
              onSelect={watchActions.handleToggleWatched}
              loading={watchActions.watchlistBusy}
              style={[
                useCompactActionLayout ? styles.iconActionButton : styles.watchStateButton,
                watchActions.isWatched && styles.watchStateButtonActive,
              ]}
              disabled={watchActions.watchlistBusy}
            />
            {(trailersLoading || hasAvailableTrailer) && (
              <FocusablePressable
                focusKey="watch-trailer"
                text={!useCompactActionLayout ? trailerButtonLabel : undefined}
                icon={useCompactActionLayout ? 'videocam' : undefined}
                accessibilityLabel={trailerButtonLabel}
                onSelect={handleWatchTrailer}
                loading={trailersLoading}
                style={useCompactActionLayout ? styles.iconActionButton : styles.trailerActionButton}
                disabled={trailerButtonDisabled}
              />
            )}
            {!isSeries && movieDetails?.collection && (
              <FocusablePressable
                focusKey="view-collection"
                text={!useCompactActionLayout ? movieDetails.collection.name : undefined}
                icon={useCompactActionLayout ? 'albums' : undefined}
                accessibilityLabel={`View ${movieDetails.collection.name}`}
                onSelect={handleViewCollection}
                style={useCompactActionLayout ? styles.iconActionButton : styles.trailerActionButton}
              />
            )}
            {showTrailerFullscreen && (
              <FocusablePressable
                focusKey="trailer-fullscreen"
                icon="expand"
                accessibilityLabel="Watch trailer fullscreen"
                onSelect={() => trailersHook.setIsTrailerImmersiveMode(true)}
                style={useCompactActionLayout ? styles.iconActionButton : styles.trailerActionButton}
              />
            )}
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
        )}
        {watchActions.watchlistError && <Text style={styles.watchlistError}>{watchActions.watchlistError}</Text>}
        {/* Prequeue stream info display */}
        <Animated.View style={[styles.prequeueInfoContainer, styles.prequeueInfoMinHeight, playback.prequeuePulseStyle]}>
          {playback.prequeueDisplayInfo && (
            <>
              {(playback.prequeueDisplayInfo.status === 'queued' || playback.prequeueDisplayInfo.status === 'searching') && (
                <Text style={styles.prequeueFilename}>
                  {playback.prequeueDisplayInfo.status === 'queued' && 'Queued...'}
                  {playback.prequeueDisplayInfo.status === 'searching' && 'Searching for streams...'}
                </Text>
              )}
              {playback.prequeueDisplayInfo.status === 'failed' && (
                <Text style={styles.prequeueFilename}>
                  {(() => {
                    const error = playback.prequeueDisplayInfo.error || 'Unknown error';
                    const targetEp = activeEpisode || nextUpEpisode;
                    const errorLower = error.toLowerCase();
                    const isNoUsableResultsError = errorLower === 'no results found' || errorLower.includes('does not match target');
                    if (isSeries && targetEp && isNoUsableResultsError && isEpisodeUnreleased(targetEp.airedDate, targetEp.airedDateTimeUTC)) {
                      const episodeLabel = `S${String(targetEp.seasonNumber).padStart(2, '0')}E${String(targetEp.episodeNumber).padStart(2, '0')}`;
                      return formatUnreleasedMessage(episodeLabel, targetEp.airedDate);
                    }
                    return `Failed: ${error}`;
                  })()}
                </Text>
              )}
              {(playback.prequeueDisplayInfo.status === 'resolving' || playback.prequeueDisplayInfo.status === 'probing' || playback.prequeueDisplayInfo.status === 'ready') && (
                <>
                  <Text style={styles.prequeueFilename} numberOfLines={1} ellipsizeMode="middle">
                    {playback.prequeueDisplayInfo.displayName ||
                      playback.prequeueDisplayInfo.passthroughName ||
                      (playback.prequeueDisplayInfo.streamPath?.split('/').pop()) ||
                      (playback.prequeueDisplayInfo.status === 'resolving' ? 'Resolving stream...' : 'Analyzing media...')}
                  </Text>
                  {(playback.prequeueDisplayInfo.status === 'probing' || playback.prequeueDisplayInfo.status === 'resolving') &&
                   !playback.prequeueDisplayInfo.audioTracks?.length && (
                    <Text style={styles.prequeueLoadingText}>Analyzing tracks...</Text>
                  )}
                  {(playback.prequeueDisplayInfo.audioTracks?.length || playback.prequeueDisplayInfo.subtitleTracks?.length) ? (
                    <View style={styles.prequeueTrackRow}>
                      {playback.prequeueDisplayInfo.audioTracks && playback.prequeueDisplayInfo.audioTracks.length > 0 && (
                        Platform.isTV ? (
                          <SpatialNavigationFocusableView
                            onSelect={() => playback.setShowAudioTrackModal(true)}
                            onFocus={() => { handleTVFocusAreaChange('actions'); trailersHook.dismissTrailerAutoPlay(); }}>
                            {({ isFocused }: { isFocused: boolean }) => (
                              <View style={[styles.prequeueTrackPressable, isFocused && styles.prequeueTrackFocused]}>
                                <Ionicons name="volume-high" size={16 * tvScale} color={isFocused ? theme.colors.text.inverse : theme.colors.text.secondary} />
                                <Text style={[styles.prequeueTrackValue, isFocused && styles.prequeueTrackValueFocused]} numberOfLines={1}>
                                  {(() => {
                                    const selectedIdx = playback.trackOverrideAudio ?? playback.prequeueDisplayInfo?.selectedAudioTrack;
                                    const track = selectedIdx !== undefined && selectedIdx >= 0
                                      ? playback.prequeueDisplayInfo?.audioTracks?.find((t) => t.index === selectedIdx)
                                      : playback.prequeueDisplayInfo?.audioTracks?.[0];
                                    if (!track) return 'Default';
                                    return `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`;
                                  })()}
                                </Text>
                                {(() => {
                                  const selectedIdx = playback.trackOverrideAudio ?? playback.prequeueDisplayInfo?.selectedAudioTrack;
                                  const track = selectedIdx !== undefined && selectedIdx >= 0
                                    ? playback.prequeueDisplayInfo?.audioTracks?.find((t) => t.index === selectedIdx)
                                    : playback.prequeueDisplayInfo?.audioTracks?.[0];
                                  if (track?.codec) {
                                    return (
                                      <Text style={[styles.prequeueTrackBadge, styles.prequeueTrackCodecBadge]}>
                                        {track.codec.toUpperCase()}
                                      </Text>
                                    );
                                  }
                                  return null;
                                })()}
                                {playback.prequeueDisplayInfo!.audioTracks!.length > 1 && (
                                  <Ionicons name="chevron-forward" size={12 * tvScale} color={isFocused ? theme.colors.text.inverse : theme.colors.text.muted} />
                                )}
                              </View>
                            )}
                          </SpatialNavigationFocusableView>
                        ) : (
                          <Pressable
                            onPress={() => playback.setShowAudioTrackModal(true)}
                            onFocus={() => trailersHook.dismissTrailerAutoPlay()}
                            disabled={playback.prequeueDisplayInfo.audioTracks.length <= 1}
                            style={styles.prequeueTrackPressable}
                          >
                            <Ionicons name="volume-high" size={16 * tvScale} color={theme.colors.text.secondary} />
                            <Text style={styles.prequeueTrackValue} numberOfLines={1}>
                              {(() => {
                                const selectedIdx = playback.trackOverrideAudio ?? playback.prequeueDisplayInfo?.selectedAudioTrack;
                                const track = selectedIdx !== undefined && selectedIdx >= 0
                                  ? playback.prequeueDisplayInfo?.audioTracks?.find((t) => t.index === selectedIdx)
                                  : playback.prequeueDisplayInfo?.audioTracks?.[0];
                                if (!track) return 'Default';
                                return `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`;
                              })()}
                            </Text>
                            {(() => {
                              const selectedIdx = playback.trackOverrideAudio ?? playback.prequeueDisplayInfo?.selectedAudioTrack;
                              const track = selectedIdx !== undefined && selectedIdx >= 0
                                ? playback.prequeueDisplayInfo?.audioTracks?.find((t) => t.index === selectedIdx)
                                : playback.prequeueDisplayInfo?.audioTracks?.[0];
                              if (track?.codec) {
                                return (
                                  <Text style={[styles.prequeueTrackBadge, styles.prequeueTrackCodecBadge]}>
                                    {track.codec.toUpperCase()}
                                  </Text>
                                );
                              }
                              return null;
                            })()}
                            {playback.prequeueDisplayInfo.audioTracks.length > 1 && (
                              <Ionicons name="chevron-forward" size={12 * tvScale} color={theme.colors.text.muted} />
                            )}
                          </Pressable>
                        )
                      )}
                      {(playback.prequeueDisplayInfo.audioTracks?.length ?? 0) > 0 && (playback.prequeueDisplayInfo.subtitleTracks?.length ?? 0) > 0 && (
                        <Text style={styles.prequeueTrackSeparator}>{'\u2022'}</Text>
                      )}
                      {playback.prequeueDisplayInfo.subtitleTracks && playback.prequeueDisplayInfo.subtitleTracks.length > 0 && (
                        Platform.isTV ? (
                          <SpatialNavigationFocusableView
                            onSelect={() => playback.setShowSubtitleTrackModal(true)}
                            onFocus={() => { handleTVFocusAreaChange('actions'); trailersHook.dismissTrailerAutoPlay(); }}>
                            {({ isFocused }: { isFocused: boolean }) => (
                              <View style={[styles.prequeueTrackPressable, isFocused && styles.prequeueTrackFocused]}>
                                <Ionicons name="text" size={16 * tvScale} color={isFocused ? theme.colors.text.inverse : theme.colors.text.secondary} />
                                <Text style={[styles.prequeueTrackValue, isFocused && styles.prequeueTrackValueFocused]} numberOfLines={1}>
                                  {(() => {
                                    const selectedIdx = playback.trackOverrideSubtitle ?? playback.prequeueDisplayInfo?.selectedSubtitleTrack;
                                    if (selectedIdx === undefined || selectedIdx < 0) return 'Off';
                                    const track = playback.prequeueDisplayInfo?.subtitleTracks?.find((t) => t.index === selectedIdx);
                                    if (!track) return 'Off';
                                    return `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`;
                                  })()}
                                </Text>
                                <Ionicons name="chevron-forward" size={12 * tvScale} color={isFocused ? theme.colors.text.inverse : theme.colors.text.muted} />
                              </View>
                            )}
                          </SpatialNavigationFocusableView>
                        ) : (
                          <Pressable
                            onPress={() => playback.setShowSubtitleTrackModal(true)}
                            onFocus={() => trailersHook.dismissTrailerAutoPlay()}
                            style={styles.prequeueTrackPressable}
                          >
                            <Ionicons name="text" size={16 * tvScale} color={theme.colors.text.secondary} />
                            <Text style={styles.prequeueTrackValue} numberOfLines={1}>
                              {(() => {
                                const selectedIdx = playback.trackOverrideSubtitle ?? playback.prequeueDisplayInfo?.selectedSubtitleTrack;
                                if (selectedIdx === undefined || selectedIdx < 0) return 'Off';
                                const track = playback.prequeueDisplayInfo?.subtitleTracks?.find((t) => t.index === selectedIdx);
                                if (!track) return 'Off';
                                return `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`;
                              })()}
                            </Text>
                            <Ionicons name="chevron-forward" size={12 * tvScale} color={theme.colors.text.muted} />
                          </Pressable>
                        )
                      )}
                    </View>
                  ) : null}
                </>
              )}
            </>
          )}
        </Animated.View>
        {/* TV Episode Carousel */}
        {Platform.isTV && isSeries && (
          <View ref={(ref) => { sectionRefs.current['episodes'] = ref; sectionRefs.current['seasons'] = ref; }} style={{ minHeight: Math.round(tvScale * 416) }}>
            {episodeManager.seasons.length > 0 && TVEpisodeCarousel ? (
              <TVEpisodeCarousel
                seasons={episodeManager.seasons}
                selectedSeason={episodeManager.selectedSeason}
                episodes={episodeManager.selectedSeason?.episodes ?? []}
                activeEpisode={activeEpisode}
                onSeasonSelect={(season: SeriesSeason) => episodeManager.handleSeasonSelect(season, false)}
                onEpisodeSelect={episodeManager.handleEpisodeSelect}
                onEpisodePlay={episodeManager.handlePlayEpisode}
                isEpisodeWatched={watchActions.isEpisodeWatched}
                getEpisodeProgress={(episode: SeriesEpisode) => {
                  const key = `${episode.seasonNumber}-${episode.episodeNumber}`;
                  return episodeProgressMap.get(key) ?? 0;
                }}
                onFocusRowChange={handleTVFocusAreaChange}
              />
            ) : activeEpisode ? (
              <TVEpisodeStrip
                activeEpisode={activeEpisode}
                allEpisodes={episodeManager.allEpisodes}
                selectedSeason={episodeManager.selectedSeason}
                percentWatched={displayProgress}
                onSelect={playback.handleWatchNow}
                onFocus={episodeManager.handleEpisodeStripFocus}
                onBlur={episodeManager.handleEpisodeStripBlur}
              />
            ) : (
              <View style={{ minHeight: Math.round(tvScale * 416) }} />
            )}
          </View>
        )}
        {/* TV Cast Section */}
        {Platform.isTV && TVCastSection && (
          <View ref={(ref) => { sectionRefs.current['cast'] = ref; }}>
            <TVCastSection
              credits={credits}
              isLoading={isSeries ? seriesDetailsLoading : movieDetailsLoading}
              maxCast={10}
              onFocus={() => handleTVFocusAreaChange('cast')}
              compactMargin
              onCastMemberPress={isKidsProfile ? undefined : handleCastMemberPress}
            />
          </View>
        )}
        {/* TV More Like This Section */}
        {Platform.isTV && TVMoreLikeThisSection && (
          <View ref={(ref) => { sectionRefs.current['similar'] = ref; }}>
            <TVMoreLikeThisSection
              titles={similarContent}
              isLoading={similarLoading}
              maxTitles={20}
              onFocus={() => handleTVFocusAreaChange('similar')}
              onTitlePress={isKidsProfile ? undefined : handleSimilarTitlePress}
            />
          </View>
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
              onSelect={episodeManager.handlePreviousEpisode}
              disabled={!episodeManager.findPreviousEpisode(activeEpisode)}
              style={styles.mobileEpisodeNavButton}
            />
            <Text style={styles.mobileEpisodeNavLabel}>
              S{activeEpisode.seasonNumber} E{activeEpisode.episodeNumber}
            </Text>
            <FocusablePressable
              focusKey="next-episode-mobile"
              icon="chevron-forward"
              accessibilityLabel="Next Episode"
              onSelect={episodeManager.handleNextEpisode}
              disabled={!episodeManager.findNextEpisode(activeEpisode)}
              style={styles.mobileEpisodeNavButton}
            />
          </View>
        )}
        {/* Hidden SeriesEpisodes component to load data (non-TV) */}
        {isSeries && !isTV ? (
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
              onSeasonSelect={episodeManager.handleSeasonSelect}
              onEpisodeSelect={episodeManager.handleEpisodeSelect}
              onEpisodeFocus={episodeManager.handleEpisodeFocus}
              onPlaySeason={episodeManager.handlePlaySeason}
              onPlayEpisode={episodeManager.handlePlayEpisode}
              onEpisodeLongPress={watchActions.handleToggleEpisodeWatched}
              onToggleEpisodeWatched={watchActions.handleToggleEpisodeWatched}
              isEpisodeWatched={watchActions.isEpisodeWatched}
              renderContent={!Platform.isTV}
              activeEpisode={activeEpisode}
              isResolving={playback.isResolving}
              theme={theme}
              onRegisterSeasonFocusHandler={handleRegisterSeasonFocusHandler}
              onRequestFocusShift={handleRequestFocusShift}
              onEpisodesLoaded={episodeManager.handleEpisodesLoaded}
              onSeasonsLoaded={episodeManager.handleSeasonsLoaded}
            />
          </View>
        ) : null}
      </View>
    </>
  );

  // Mobile content rendering with parallax
  const renderMobileContent = () => (
    <MobileParallaxContainer posterUrl={posterUrl} backdropUrl={backdropUrl} theme={theme} contentAnimatedStyle={contentAnimatedStyle} backgroundAnimatedStyle={backgroundAnimatedStyle}>
      <View style={[styles.topContent, { overflow: 'visible' }]}>
        <View style={[styles.titleRow, { overflow: 'visible', marginLeft: -12 }]}>
          <Animated.Text style={[styles.title, titleTextAnimatedStyle]}>{title}</Animated.Text>
          {logoUrl && logoDimensions && (
            <Animated.View style={[{ position: 'absolute', bottom: 0, left: 0, padding: 12, overflow: 'visible' }, logoGlowStyle, logoAnimatedStyle]}>
              <RNImage
                source={{ uri: logoUrl }}
                style={[logoStyle, isLogoDark ? { tintColor: 'white' } : undefined]}
                resizeMode="contain"
              />
            </Animated.View>
          )}
        </View>
        <MemoizedRatingsRow
          ratings={ratings}
          shouldShowSkeleton={shouldShowRatingsSkeleton}
          iconSize={14}
          baseUrl={ratingBaseUrl}
          styles={styles}
        />
        <MemoizedGenresRow
          certification={certification}
          genres={genres}
          tvScale={1}
          separatorColor={theme.colors.text.secondary}
          styles={styles}
        />
        {contentPreference && (contentPreference.audioLanguage || contentPreference.subtitleLanguage) && (
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: 8, marginTop: 8, marginBottom: 8 }}>
            {contentPreference.audioLanguage && (
              <View style={{ flexDirection: 'row', alignItems: 'center', backgroundColor: theme.colors.background.elevated, paddingHorizontal: 10, paddingVertical: 4, borderRadius: 4 }}>
                <Ionicons name="volume-high" size={14} color={theme.colors.text.secondary} style={{ marginRight: 4 }} />
                <Text style={{ color: theme.colors.text.secondary, fontSize: 12 }}>{contentPreference.audioLanguage.toUpperCase()}</Text>
              </View>
            )}
            {contentPreference.subtitleLanguage && (
              <View style={{ flexDirection: 'row', alignItems: 'center', backgroundColor: theme.colors.background.elevated, paddingHorizontal: 10, paddingVertical: 4, borderRadius: 4 }}>
                <Ionicons name="text" size={14} color={theme.colors.text.secondary} style={{ marginRight: 4 }} />
                <Text style={{ color: theme.colors.text.secondary, fontSize: 12 }}>{contentPreference.subtitleLanguage.toUpperCase()}</Text>
              </View>
            )}
          </View>
        )}
        <MemoizedReleaseInfo
          releaseRows={releaseRows}
          releaseSkeletonRows={releaseSkeletonRows}
          shouldShowSkeleton={shouldShowReleaseSkeleton}
          errorMessage={releaseErrorMessage}
          isSeries={isSeries}
          runtimeMinutes={movieDetails?.runtimeMinutes}
          iconSize={14}
          iconColor={theme.colors.text.secondary}
          styles={styles}
        />
        <Text style={[styles.description, { maxWidth: '100%' }]}>{displayDescription}</Text>
      </View>

      {/* Mobile action buttons */}
      <View style={[styles.actionRow, styles.compactActionRow, { marginTop: theme.spacing.lg }]}>
        <FocusablePressable focusKey="watch-now-mobile" icon="play" onSelect={playback.handleWatchNow} style={styles.iconActionButton} loading={playback.isResolving || (isSeries && episodeManager.episodesLoading)} disabled={playback.isResolving || (isSeries && episodeManager.episodesLoading)} showReadyPip={playback.prequeueReady} badge={(() => { if (isSeries) return isEpisodeUnreleased((activeEpisode || nextUpEpisode)?.airedDate, (activeEpisode || nextUpEpisode)?.airedDateTimeUTC) ? 'unreleased' : undefined; return isMovieUnreleased(movieDetails?.homeRelease, movieDetails?.theatricalRelease) ? 'unreleased' : undefined; })()} />
        <FocusablePressable focusKey="manual-selection-mobile" icon="search" onSelect={manualSelect.handleManualSelect} style={styles.iconActionButton} disabled={playback.isResolving || (isSeries && episodeManager.episodesLoading)} />
        {isSeries && <FocusablePressable focusKey="watch-management-mobile" icon="checkmark-done" onSelect={() => watchActions.setBulkWatchModalVisible(true)} style={styles.iconActionButton} />}
        {(isMobile || isSeries || isInContinueWatching) && <FocusablePressable focusKey="more-options-mobile" icon="ellipsis-vertical" accessibilityLabel="More options" onSelect={() => setMoreOptionsVisible(true)} style={styles.iconActionButton} />}
        <FocusablePressable focusKey="watchlist-toggle-mobile" icon={watchActions.isWatchlisted ? 'bookmark' : 'bookmark-outline'} onSelect={watchActions.handleToggleWatchlist} loading={watchActions.watchlistBusy} style={[styles.iconActionButton, watchActions.isWatchlisted && styles.watchlistActionButtonActive]} />
        {!isSeries && <FocusablePressable focusKey="watch-state-toggle-mobile" icon={watchActions.isWatched ? 'eye' : 'eye-outline'} accessibilityLabel={watchActions.watchStateButtonLabel} onSelect={watchActions.handleToggleWatched} loading={watchActions.watchlistBusy} style={[styles.iconActionButton, watchActions.isWatched && styles.watchStateButtonActive]} disabled={watchActions.watchlistBusy} />}
        {(trailersLoading || hasAvailableTrailer) && <FocusablePressable focusKey="watch-trailer-mobile" icon="videocam" accessibilityLabel={trailerButtonLabel} onSelect={handleWatchTrailer} loading={trailersLoading} style={styles.iconActionButton} disabled={trailerButtonDisabled} />}
        {!isSeries && movieDetails?.collection && <FocusablePressable focusKey="view-collection-mobile" icon="albums" accessibilityLabel={`View ${movieDetails.collection.name}`} onSelect={handleViewCollection} style={styles.iconActionButton} />}
      </View>

      {/* Mobile prequeue info */}
      <Animated.View style={[styles.prequeueInfoContainer, styles.prequeueInfoMinHeight, playback.prequeuePulseStyle]}>
        {playback.prequeueDisplayInfo && (
          <>
            {(playback.prequeueDisplayInfo.status === 'queued' || playback.prequeueDisplayInfo.status === 'searching') && (
              <Text style={styles.prequeueFilename}>
                {playback.prequeueDisplayInfo.status === 'queued' && 'Queued...'}
                {playback.prequeueDisplayInfo.status === 'searching' && 'Searching for streams...'}
              </Text>
            )}
            {playback.prequeueDisplayInfo.status === 'failed' && (
              <Text style={styles.prequeueFilename}>
                {(() => {
                  const error = playback.prequeueDisplayInfo.error || 'Unknown error';
                  const targetEp = activeEpisode || nextUpEpisode;
                  const errorLower = error.toLowerCase();
                  const isNoUsableResultsError = errorLower === 'no results found' || errorLower.includes('does not match target');
                  if (isSeries && targetEp && isNoUsableResultsError && isEpisodeUnreleased(targetEp.airedDate, targetEp.airedDateTimeUTC)) {
                    const episodeLabel = `S${String(targetEp.seasonNumber).padStart(2, '0')}E${String(targetEp.episodeNumber).padStart(2, '0')}`;
                    return formatUnreleasedMessage(episodeLabel, targetEp.airedDate);
                  }
                  return `Failed: ${error}`;
                })()}
              </Text>
            )}
            {(playback.prequeueDisplayInfo.status === 'resolving' || playback.prequeueDisplayInfo.status === 'probing' || playback.prequeueDisplayInfo.status === 'ready') && (
              <>
                <Text style={styles.prequeueFilename} numberOfLines={1} ellipsizeMode="middle">
                  {playback.prequeueDisplayInfo.displayName || playback.prequeueDisplayInfo.passthroughName || (playback.prequeueDisplayInfo.streamPath?.split('/').pop()) || (playback.prequeueDisplayInfo.status === 'resolving' ? 'Resolving stream...' : 'Analyzing media...')}
                </Text>
                {(playback.prequeueDisplayInfo.status === 'probing' || playback.prequeueDisplayInfo.status === 'resolving') && !playback.prequeueDisplayInfo.audioTracks?.length && (
                  <Text style={styles.prequeueLoadingText}>Analyzing tracks...</Text>
                )}
                {(playback.prequeueDisplayInfo.audioTracks?.length || playback.prequeueDisplayInfo.subtitleTracks?.length) ? (
                  <View style={styles.prequeueTrackRow}>
                    {playback.prequeueDisplayInfo.audioTracks && playback.prequeueDisplayInfo.audioTracks.length > 0 && (
                      <Pressable onPress={() => playback.setShowAudioTrackModal(true)} disabled={playback.prequeueDisplayInfo.audioTracks.length <= 1} style={styles.prequeueTrackPressable}>
                        <Ionicons name="volume-high" size={14} color={theme.colors.text.secondary} />
                        <Text style={styles.prequeueTrackValue} numberOfLines={1}>
                          {(() => { const idx = playback.trackOverrideAudio ?? playback.prequeueDisplayInfo?.selectedAudioTrack; const t = idx !== undefined && idx >= 0 ? playback.prequeueDisplayInfo?.audioTracks?.find((x) => x.index === idx) : playback.prequeueDisplayInfo?.audioTracks?.[0]; if (!t) return 'Default'; return `${formatLanguage(t.language)}${t.title ? ` - ${t.title}` : ''}`; })()}
                        </Text>
                        {playback.prequeueDisplayInfo.audioTracks.length > 1 && <Ionicons name="chevron-forward" size={10} color={theme.colors.text.muted} />}
                      </Pressable>
                    )}
                    {(playback.prequeueDisplayInfo.audioTracks?.length ?? 0) > 0 && (playback.prequeueDisplayInfo.subtitleTracks?.length ?? 0) > 0 && <Text style={styles.prequeueTrackSeparator}>{'\u2022'}</Text>}
                    {playback.prequeueDisplayInfo.subtitleTracks && playback.prequeueDisplayInfo.subtitleTracks.length > 0 && (
                      <Pressable onPress={() => playback.setShowSubtitleTrackModal(true)} style={styles.prequeueTrackPressable}>
                        <Ionicons name="text" size={14} color={theme.colors.text.secondary} />
                        <Text style={styles.prequeueTrackValue} numberOfLines={1}>
                          {(() => { const idx = playback.trackOverrideSubtitle ?? playback.prequeueDisplayInfo?.selectedSubtitleTrack; if (idx === undefined || idx < 0) return 'Off'; const t = playback.prequeueDisplayInfo?.subtitleTracks?.find((x) => x.index === idx); if (!t) return 'Off'; return `${formatLanguage(t.language)}${t.title ? ` - ${t.title}` : ''}`; })()}
                        </Text>
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
      {isSeries && episodeManager.seasons.length > 0 && (
        <MobileEpisodeCarousel
          seasons={episodeManager.seasons}
          selectedSeason={episodeManager.selectedSeason}
          episodes={episodeManager.selectedSeason?.episodes ?? []}
          activeEpisode={activeEpisode}
          isLoading={seriesDetailsLoading}
          onSeasonSelect={(season) => episodeManager.handleSeasonSelect(season, false)}
          onEpisodeSelect={episodeManager.handleEpisodeSelect}
          onEpisodePlay={episodeManager.handlePlayEpisode}
          onEpisodeLongPress={watchActions.handleToggleEpisodeWatched}
          isEpisodeWatched={watchActions.isEpisodeWatched}
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
            <Text style={[styles.episodeOverviewText, { color: theme.colors.text.secondary }]}>{activeEpisode.overview}</Text>
          ) : null}
          {activeEpisode.airedDate && (
            <Text style={[styles.episodeOverviewMeta, { color: theme.colors.text.muted }]}>
              {formatPublishDate(activeEpisode.airedDate)}
              {activeEpisode.runtimeMinutes ? ` \u2022 ${activeEpisode.runtimeMinutes} minutes` : ''}
            </Text>
          )}
        </View>
      )}

      {/* Cast section */}
      <CastSection credits={credits} isLoading={isSeries ? seriesDetailsLoading : movieDetailsLoading} theme={theme} onCastMemberPress={isKidsProfile ? undefined : handleCastMemberPress} />

      {/* More Like This section */}
      <MoreLikeThisSection titles={similarContent} isLoading={similarLoading} theme={theme} onTitlePress={isKidsProfile ? undefined : handleSimilarTitlePress} />

      {/* Hidden SeriesEpisodes component to load data */}
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
            onSeasonSelect={episodeManager.handleSeasonSelect}
            onEpisodeSelect={episodeManager.handleEpisodeSelect}
            onEpisodeFocus={episodeManager.handleEpisodeFocus}
            onPlaySeason={episodeManager.handlePlaySeason}
            onPlayEpisode={episodeManager.handlePlayEpisode}
            onEpisodeLongPress={watchActions.handleToggleEpisodeWatched}
            onToggleEpisodeWatched={watchActions.handleToggleEpisodeWatched}
            isEpisodeWatched={watchActions.isEpisodeWatched}
            renderContent={false}
            activeEpisode={activeEpisode}
            isResolving={playback.isResolving}
            theme={theme}
            onRegisterSeasonFocusHandler={handleRegisterSeasonFocusHandler}
            onRequestFocusShift={handleRequestFocusShift}
            onEpisodesLoaded={episodeManager.handleEpisodesLoaded}
            onSeasonsLoaded={episodeManager.handleSeasonsLoaded}
          />
        </View>
      )}
    </MobileParallaxContainer>
  );

  const SafeAreaWrapper = isTV ? View : FixedSafeAreaView;
  const safeAreaProps = isTV ? {} : { edges: ['top'] as ('top' | 'bottom' | 'left' | 'right')[] };

  const detailsContent = (
    <>
      <SafeAreaWrapper style={styles.safeArea} {...safeAreaProps}>
        <View style={styles.container}>
          {/* Pre-mount hidden SeriesEpisodes OUTSIDE the visibility gate (TV) — deferred until after first paint.
              Skip entirely when bundle hydration already populated seasons (its callbacks would no-op anyway). */}
          {isTV && isSeries && deferredSeriesReady && episodeManager.seasons.length === 0 && (
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
                onSeasonSelect={episodeManager.handleSeasonSelect}
                onEpisodeSelect={episodeManager.handleEpisodeSelect}
                onEpisodeFocus={episodeManager.handleEpisodeFocus}
                onPlaySeason={episodeManager.handlePlaySeason}
                onPlayEpisode={episodeManager.handlePlayEpisode}
                onEpisodeLongPress={watchActions.handleToggleEpisodeWatched}
                onToggleEpisodeWatched={watchActions.handleToggleEpisodeWatched}
                isEpisodeWatched={watchActions.isEpisodeWatched}
                renderContent={false}
                activeEpisode={activeEpisode}
                isResolving={playback.isResolving}
                theme={theme}
                onRegisterSeasonFocusHandler={handleRegisterSeasonFocusHandler}
                onRequestFocusShift={handleRequestFocusShift}
                onEpisodesLoaded={episodeManager.handleEpisodesLoaded}
                onSeasonsLoaded={episodeManager.handleSeasonsLoaded}
              />
            </View>
          )}
          {/* Hide all content until metadata (and logo) is ready */}
          {shouldHideUntilMetadataReady ? null : (
            <>
              {/* Mobile uses the new parallax scrollable container */}
              {isMobile ? (
                renderMobileContent()
              ) : (
                <>
                  {headerImage ? (
                    autoPlayTrailersTV && TVTrailerBackdrop ? (
                      <TVTrailerBackdrop
                        backdropUrl={headerImage}
                        trailerStreamUrl={trailersHook.trailerStreamUrl}
                        isPlaying={trailersHook.isBackdropTrailerPlaying}
                        isImmersive={trailersHook.isTrailerImmersiveMode}
                        onEnd={() => {
                          trailersHook.setIsBackdropTrailerPlaying(false);
                          trailersHook.setIsTrailerImmersiveMode(false);
                        }}
                        onError={() => {
                          trailersHook.setIsBackdropTrailerPlaying(false);
                          trailersHook.setIsTrailerImmersiveMode(false);
                        }}
                      />
                    ) : (
                      <Animated.View
                        style={[
                          styles.backgroundImageContainer,
                          shouldAnchorHeroToTop && styles.backgroundImageContainerTop,
                          backgroundAnimatedStyle,
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
                    )
                  ) : null}
                  {/* Content + overlay fade in together */}
                  <Animated.View style={[{ flex: 1 }, contentAnimatedStyle]}>
                    {/* Hide overlay gradient when TVTrailerBackdrop is active */}
                    {!(autoPlayTrailersTV && TVTrailerBackdrop) && (
                      <LinearGradient
                        pointerEvents="none"
                        colors={overlayGradientColors}
                        locations={overlayGradientLocations}
                        start={{ x: 0.5, y: 0 }}
                        end={{ x: 0.5, y: 1 }}
                        style={styles.gradientOverlay}
                      />
                    )}
                    {Platform.isTV ? (
                      <>
                        <Animated.ScrollView
                          ref={tvScrollViewRef}
                          style={styles.tvScrollContainer}
                          contentContainerStyle={styles.tvScrollContent}
                          showsVerticalScrollIndicator={false}
                          onScroll={tvScrollHandler}
                          onContentSizeChange={handleTVContentSizeChange}
                          scrollEventThrottle={16}
                          scrollEnabled={false}
                          bounces={false}
                          overScrollMode="never"
                          decelerationRate="fast">
                          {/* Fixed height spacer */}
                          <View style={{ height: tvSpacerHeight }} />
                          {/* Content area with gradient background */}
                          <Animated.View style={autoPlayTrailersTV ? trailersHook.immersiveContentStyle as any : undefined}>
                            <LinearGradient
                              colors={[
                                'transparent',
                                'rgba(0, 0, 0, 0.6)',
                                'rgba(0, 0, 0, 0.85)',
                                theme.colors.background.base,
                              ]}
                              locations={[0, 0.1, 0.25, 0.45]}
                              style={styles.tvContentGradient}>
                              <View style={styles.tvContentInner}>
                                {renderDetailsContent()}
                              </View>
                            </LinearGradient>
                          </Animated.View>
                        </Animated.ScrollView>
                      </>
                    ) : (
                      <View style={styles.contentOverlay}>
                        <View style={[styles.contentBox, contentBoxStyle]}>
                          <View style={styles.contentBoxInner}>
                            <View style={styles.contentContainer}>{renderDetailsContent()}</View>
                          </View>
                        </View>
                      </View>
                    )}
                  </Animated.View>
                </>
              )}
            </>
          )}
        </View>
      </SafeAreaWrapper>
      <MobileTabBar />
    </>
  );

  return (
    <>
      <Stack.Screen options={{ headerShown: false }} />
      {Platform.isTV ? (
        <SpatialNavigationRoot isActive={isSpatialNavActive} key={spatialNavKey}>
          {detailsContent}
        </SpatialNavigationRoot>
      ) : (
        detailsContent
      )}
      <TrailerModal
        visible={trailerModalVisible}
        trailer={activeTrailer}
        onClose={handleCloseTrailer}
        theme={theme}
        preloadedStreamUrl={trailersHook.trailerStreamUrl}
        isDownloading={trailersHook.trailerPrequeueStatus === 'pending' || trailersHook.trailerPrequeueStatus === 'downloading'}
      />
      <ResumePlaybackModal
        visible={playback.resumeModalVisible}
        onClose={playback.handleCloseResumeModal}
        onResume={playback.handleResumePlayback}
        onPlayFromBeginning={playback.handlePlayFromBeginning}
        theme={theme}
        percentWatched={playback.currentProgress?.percentWatched ?? 0}
      />
      <BulkWatchModal
        visible={watchActions.bulkWatchModalVisible}
        onClose={() => watchActions.setBulkWatchModalVisible(false)}
        theme={theme}
        seasons={episodeManager.seasons}
        allEpisodes={episodeManager.allEpisodes}
        currentEpisode={activeEpisode}
        onMarkAllWatched={watchActions.handleMarkAllWatched}
        onMarkAllUnwatched={watchActions.handleMarkAllUnwatched}
        onMarkSeasonWatched={watchActions.handleMarkSeasonWatched}
        onMarkSeasonUnwatched={watchActions.handleMarkSeasonUnwatched}
        onMarkEpisodeWatched={watchActions.handleToggleEpisodeWatched}
        onMarkEpisodeUnwatched={watchActions.handleToggleEpisodeWatched}
        isEpisodeWatched={watchActions.isEpisodeWatched}
      />
      <ManualSelection
        visible={manualSelect.manualVisible}
        loading={manualSelect.manualLoading}
        error={manualSelect.manualError}
        results={manualSelect.manualResults}
        healthChecks={manualHealthChecks}
        onClose={() => { setManualSelectDownloadOnly(false); setSeasonDownloadMode(false); setSeasonDownloadTarget(null); manualSelect.closeManualPicker(); }}
        onSelect={manualSelect.handleManualSelection}
        onCheckHealth={checkManualHealth}
        onDownload={isMobile ? (seasonDownloadMode ? handleSeasonDownloadResult : handleDownloadResult) : undefined}
        downloadOnly={manualSelectDownloadOnly}
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
        onClose={() => { setSeasonDownloadMode(false); setSeasonDownloadTarget(null); setSeasonSelectorVisible(false); }}
        seasons={seasonDownloadMode ? episodeManager.seasons.filter(s => s.number > 0) : episodeManager.seasons}
        onSeasonSelect={seasonDownloadMode ? handleSeasonDownloadSearch : (isMobile ? handleMobileSeasonSelect : handleSeasonSelectorSelect)}
        theme={theme}
      />
      {/* More Options Menu */}
      <TvModal visible={moreOptionsVisible} onRequestClose={() => setMoreOptionsVisible(false)}>
        <SpatialNavigationRoot isActive={moreOptionsVisible}>
          <View style={styles.moreOptionsModal}>
            <View style={styles.moreOptionsHeader}>
              <Text style={styles.moreOptionsTitle}>More Options</Text>
            </View>
            <SpatialNavigationNode orientation="vertical">
              <View style={styles.moreOptionsContent}>
                {isSeries && (
                  <DefaultFocus>
                    <SpatialNavigationFocusableView
                      onSelect={handleMenuShuffleShow}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <View style={[styles.moreOptionsItem, isFocused && styles.moreOptionsItemFocused]}>
                          <View>
                            <Ionicons name="shuffle" size={16} color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary} style={{ position: 'absolute', top: -4, left: -4, opacity: 0.35 }} />
                            <Ionicons name="shuffle" size={20} color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary} />
                          </View>
                          <Text style={[styles.moreOptionsTitle, { fontSize: theme.typography.body.md.fontSize }, isFocused && { color: theme.colors.text.inverse }]}>Shuffle Show</Text>
                        </View>
                      )}
                    </SpatialNavigationFocusableView>
                  </DefaultFocus>
                )}
                {isSeries && (
                  <SpatialNavigationFocusableView
                    onSelect={handleMenuShuffleSeason}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <View style={[styles.moreOptionsItem, isFocused && styles.moreOptionsItemFocused]}>
                        <Ionicons name="shuffle" size={20} color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary} />
                        <Text style={[styles.moreOptionsTitle, { fontSize: theme.typography.body.md.fontSize }, isFocused && { color: theme.colors.text.inverse }]}>
                          {`Shuffle Season${episodeManager.selectedSeason ? ` ${episodeManager.selectedSeason.number}` : ''}`}
                        </Text>
                      </View>
                    )}
                  </SpatialNavigationFocusableView>
                )}
                {isInContinueWatching && (
                  (() => {
                    const removeCWItem = (
                      <SpatialNavigationFocusableView
                        onSelect={handleRemoveFromContinueWatching}>
                        {({ isFocused }: { isFocused: boolean }) => (
                          <View style={[styles.moreOptionsItem, isFocused && styles.moreOptionsItemFocused]}>
                            <Ionicons name="eye-off-outline" size={20} color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary} />
                            <Text style={[styles.moreOptionsTitle, { fontSize: theme.typography.body.md.fontSize }, isFocused && { color: theme.colors.text.inverse }]}>Remove from Continue Watching</Text>
                          </View>
                        )}
                      </SpatialNavigationFocusableView>
                    );
                    return !isSeries ? <DefaultFocus>{removeCWItem}</DefaultFocus> : removeCWItem;
                  })()
                )}
                {isMobile && (
                  <SpatialNavigationFocusableView
                    onSelect={handleMenuDownload}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <View style={[styles.moreOptionsItem, isFocused && styles.moreOptionsItemFocused]}>
                        <Ionicons name="download-outline" size={20} color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary} />
                        <Text style={[styles.moreOptionsTitle, { fontSize: theme.typography.body.md.fontSize }, isFocused && { color: theme.colors.text.inverse }]}>Download Selected</Text>
                      </View>
                    )}
                  </SpatialNavigationFocusableView>
                )}
                {isMobile && isSeries && (
                  <SpatialNavigationFocusableView
                    onSelect={handleMenuDownloadSeason}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <View style={[styles.moreOptionsItem, isFocused && styles.moreOptionsItemFocused]}>
                          <View>
                            <Ionicons name="download-outline" size={16} color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary} style={{ position: 'absolute', top: -4, left: -4, opacity: 0.35 }} />
                            <Ionicons name="download-outline" size={20} color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary} />
                          </View>
                        <Text style={[styles.moreOptionsTitle, { fontSize: theme.typography.body.md.fontSize }, isFocused && { color: theme.colors.text.inverse }]}>Download Season</Text>
                      </View>
                    )}
                  </SpatialNavigationFocusableView>
                )}
                <SpatialNavigationFocusableView
                  onSelect={() => setMoreOptionsVisible(false)}>
                  {({ isFocused }: { isFocused: boolean }) => (
                    <View style={[styles.moreOptionsCancelButton, isFocused && styles.moreOptionsCancelFocused]}>
                      <Text style={[styles.moreOptionsCancelButtonText, isFocused && { color: theme.colors.text.inverse }]}>Cancel</Text>
                    </View>
                  )}
                </SpatialNavigationFocusableView>
              </View>
            </SpatialNavigationNode>
          </View>
        </SpatialNavigationRoot>
      </TvModal>
      <EpisodeSelector
        visible={episodeSelectorVisible}
        onClose={() => setEpisodeSelectorVisible(false)}
        onBack={handleEpisodeSelectorBack}
        season={episodeManager.selectedSeason}
        onEpisodeSelect={handleEpisodeSelectorSelect}
        isEpisodeWatched={watchActions.isEpisodeWatched}
        theme={theme}
      />
      {/* Audio Track Selection Modal */}
      <TrackSelectionModal
        visible={playback.showAudioTrackModal}
        title="Audio Track"
        options={playback.buildPrequeueAudioOptions()}
        selectedId={playback.currentAudioTrackId}
        onSelect={(id) => {
          playback.setTrackOverrideAudio(parseInt(id, 10));
          playback.setShowAudioTrackModal(false);
        }}
        onClose={() => playback.setShowAudioTrackModal(false)}
      />
      {/* Subtitle Track Selection Modal */}
      <TrackSelectionModal
        visible={playback.showSubtitleTrackModal}
        title="Subtitles"
        options={playback.buildPrequeueSubtitleOptions()}
        selectedId={playback.currentSubtitleTrackId}
        onSelect={(id) => {
          playback.setTrackOverrideSubtitle(parseInt(id, 10));
          playback.setShowSubtitleTrackModal(false);
        }}
        onClose={() => playback.setShowSubtitleTrackModal(false)}
      />
      {/* Black overlay for smooth transition to player */}
      {playback.showBlackOverlay && (
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
