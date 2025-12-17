import { clearMemoryCache as clearImageMemoryCache } from '@/components/Image';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import LoadingIndicator from '@/components/LoadingIndicator';
import Controls from '@/components/player/Controls';
import ExitButton from '@/components/player/ExitButton';
import TVControlsModal from '@/components/player/TVControlsModal';
import { isMobileWeb } from '@/components/player/isMobileWeb';
import MediaInfoDisplay from '@/components/player/MediaInfoDisplay';
import SubtitleOverlay from '@/components/player/SubtitleOverlay';
import VideoPlayer, {
  VideoPlayerHandle,
  type TrackInfo,
  type VideoImplementation,
  type VideoProgressMeta,
} from '@/components/player/VideoPlayer';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import {
  DefaultFocus,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  useLockSpatialNavigation,
  useSpatialNavigator,
} from '@/services/tv-navigation';
import { LinearGradient } from 'expo-linear-gradient';
import { router, Stack, useLocalSearchParams } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Animated,
  BackHandler,
  Platform,
  ScrollView,
  StyleSheet,
  Text,
  useWindowDimensions,
  View,
} from 'react-native';

// TVMenuControl is available on tvOS but not typed in RN types
// eslint-disable-next-line @typescript-eslint/no-var-requires
const TVMenuControl: { enableTVMenuKey?: () => void; disableTVMenuKey?: () => void } | undefined =
  Platform.isTV ? require('react-native').TVMenuControl : undefined;
import { useSafeAreaInsets } from 'react-native-safe-area-context';


import { detectDolbyVision } from '@/app/details/playback';
import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useLoadingScreen } from '@/components/LoadingScreenContext';
import { useToast } from '@/components/ToastContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { usePlaybackProgress } from '@/hooks/usePlaybackProgress';
import type { AudioStreamMetadata, SubtitleStreamMetadata } from '@/services/api';
import apiService from '@/services/api';
import { playbackNavigation } from '@/services/playback-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';

type ConsoleLevel = 'log' | 'warn' | 'error';

interface DebugLogEntry {
  id: number;
  level: ConsoleLevel;
  message: string;
  timestamp: string;
}

type TrackOption = {
  id: string;
  label: string;
  description?: string;
};

interface PlayerParams extends Record<string, any> {
  movie: string;
  headerImage: string;
  title?: string;
  seriesTitle?: string; // Clean series title without episode code (for metadata lookups)
  debugLogs?: string;
  preferSystemPlayer?: string;
  mediaType?: string;
  year?: string;
  seasonNumber?: string;
  episodeNumber?: string;
  episodeName?: string;
  durationHint?: string;
  sourcePath?: string;
  displayName?: string;
  dv?: string;
  dvProfile?: string;
  hdr10?: string;
  forceAAC?: string;
  startOffset?: string;
  titleId?: string;
  imdbId?: string;
  tvdbId?: string;
}

const parseBooleanParam = (value?: string | string[]): boolean => {
  const raw = Array.isArray(value) ? value[0] : value;
  if (!raw) {
    return false;
  }

  const normalized = raw.toString().trim().toLowerCase();
  return normalized === '1' || normalized === 'true' || normalized === 'yes' || normalized === 'on';
};

const SUBTITLE_OFF_OPTION: TrackOption = { id: 'off', label: 'Off' };

const toTitleCase = (value?: string | null): string | undefined => {
  if (!value) {
    return undefined;
  }
  const trimmed = value.trim();
  if (!trimmed) {
    return undefined;
  }
  return trimmed
    .toLowerCase()
    .split(/\s+/)
    .map((segment) => segment.charAt(0).toUpperCase() + segment.slice(1))
    .join(' ');
};

const stripEpisodeCodeSuffix = (value?: string | null): string | undefined => {
  if (!value) {
    return undefined;
  }
  const trimmed = value.trim();
  if (!trimmed) {
    return undefined;
  }
  const cleaned = trimmed.replace(/\s*[-–:]\s*S\d{2}E\d{2}$/i, '').trim();
  return cleaned || trimmed;
};

const formatLanguage = (code?: string | null): string | undefined => {
  if (!code) {
    return undefined;
  }
  const trimmed = code.trim();
  if (!trimmed) {
    return undefined;
  }
  if (trimmed.length <= 3) {
    return trimmed.toUpperCase();
  }
  return toTitleCase(trimmed) ?? trimmed;
};

const buildAudioTrackOptions = (streams: AudioStreamMetadata[]): TrackOption[] => {
  if (!Array.isArray(streams)) {
    return [];
  }
  return streams.map((stream) => {
    const title = toTitleCase(stream.title);
    const language = formatLanguage(stream.language);
    const channel = stream.channelLayout?.trim() || (stream.channels ? `${stream.channels}ch` : undefined);
    const codec = stream.codecLongName ?? stream.codecName;

    const labelParts: string[] = [];
    if (title) {
      labelParts.push(title);
    }
    if (language) {
      labelParts.push(language);
    }
    if (!labelParts.length) {
      labelParts.push(`Audio ${stream.index}`);
    }

    const descriptionParts: string[] = [];
    if (channel) {
      descriptionParts.push(channel);
    }
    if (codec) {
      descriptionParts.push(codec.toUpperCase());
    }

    return {
      id: String(stream.index),
      label: labelParts.join(' · '),
      description: descriptionParts.length ? descriptionParts.join(' · ') : undefined,
    };
  });
};

const buildSubtitleTrackOptions = (streams: SubtitleStreamMetadata[], selectedIndex?: number | null): TrackOption[] => {
  const options: TrackOption[] = streams.map((stream) => {
    const title = toTitleCase(stream.title);
    const language = formatLanguage(stream.language);
    const labelParts: string[] = [];
    if (title) {
      labelParts.push(title);
    }
    if (language && !labelParts.includes(language)) {
      labelParts.push(language);
    }
    if (!labelParts.length) {
      labelParts.push(`Subtitle ${stream.index}`);
    }

    const descriptorParts: string[] = [];
    // Check disposition for forced/default flags
    const isForced = stream.isForced ?? (stream.disposition?.forced ?? 0) > 0;
    const isDefault = stream.isDefault ?? (stream.disposition?.default ?? 0) > 0;
    if (isForced) {
      descriptorParts.push('Forced');
    }
    if (isDefault) {
      descriptorParts.push('Default');
    }
    const codec = stream.codecLongName ?? stream.codecName;
    if (codec) {
      descriptorParts.push(codec.toUpperCase());
    }

    return {
      id: String(stream.index),
      label: labelParts.join(' · '),
      description: descriptorParts.length ? descriptorParts.join(' · ') : undefined,
    };
  });

  const merged = [SUBTITLE_OFF_OPTION, ...options];
  if (selectedIndex === null || selectedIndex === undefined) {
    return merged;
  }

  // Treat negative indices as "no subtitle" - don't create fake tracks for them
  if (selectedIndex < 0) {
    return merged;
  }

  const hasMatch = merged.some((option) => Number(option.id) === selectedIndex);
  if (!hasMatch) {
    merged.push({ id: String(selectedIndex), label: `Subtitle ${selectedIndex}` });
  }
  return merged;
};

const resolveSelectedTrackId = (
  options: TrackOption[],
  selectedIndex: number | null | undefined,
  fallbackId: string | null = options[0]?.id ?? null,
): string | null => {
  if (!options.length) {
    return null;
  }
  // Treat negative indices as "use fallback" (e.g., for subtitles, this means "off")
  if (typeof selectedIndex === 'number' && Number.isFinite(selectedIndex)) {
    if (selectedIndex < 0) {
      return fallbackId ?? options[0]?.id ?? null;
    }
    const match = options.find((option) => Number(option.id) === selectedIndex);
    if (match) {
      return match.id;
    }
  }
  return fallbackId ?? options[0]?.id ?? null;
};

const normalizeLanguageForMatching = (lang: string): string => {
  return lang.toLowerCase().trim();
};

const findAudioTrackByLanguage = (streams: AudioStreamMetadata[], preferredLanguage: string): number | null => {
  if (!preferredLanguage || !streams?.length) {
    return null;
  }

  const normalizedPref = normalizeLanguageForMatching(preferredLanguage);

  // Try exact match on language code or title
  for (const stream of streams) {
    const language = normalizeLanguageForMatching(stream.language || '');
    const title = normalizeLanguageForMatching(stream.title || '');

    if (language === normalizedPref || title === normalizedPref) {
      return stream.index;
    }
  }

  // Try partial match (e.g., "eng" matches "English")
  for (const stream of streams) {
    const language = normalizeLanguageForMatching(stream.language || '');
    const title = normalizeLanguageForMatching(stream.title || '');

    if (
      language.includes(normalizedPref) ||
      title.includes(normalizedPref) ||
      normalizedPref.includes(language) ||
      normalizedPref.includes(title)
    ) {
      return stream.index;
    }
  }

  return null;
};

const findSubtitleTrackByPreference = (
  streams: SubtitleStreamMetadata[],
  preferredLanguage: string | undefined,
  mode: 'off' | 'on' | 'forced-only' | undefined,
): number | null => {
  if (!streams?.length || mode === 'off') {
    return null;
  }

  const normalizedPref = preferredLanguage ? normalizeLanguageForMatching(preferredLanguage) : null;

  // Filter by mode
  let candidateStreams = streams;
  if (mode === 'forced-only') {
    candidateStreams = streams.filter((s) => s.isForced ?? (s.disposition?.forced ?? 0) > 0);
    if (!candidateStreams.length) {
      // No forced subtitles available, return null (off)
      return null;
    }
  }

  // If language preference is set, try to find a match
  if (normalizedPref) {
    // Try exact match
    for (const stream of candidateStreams) {
      const language = normalizeLanguageForMatching(stream.language || '');
      const title = normalizeLanguageForMatching(stream.title || '');

      if (language === normalizedPref || title === normalizedPref) {
        return stream.index;
      }
    }

    // Try partial match
    for (const stream of candidateStreams) {
      const language = normalizeLanguageForMatching(stream.language || '');
      const title = normalizeLanguageForMatching(stream.title || '');

      if (
        language.includes(normalizedPref) ||
        title.includes(normalizedPref) ||
        normalizedPref.includes(language) ||
        normalizedPref.includes(title)
      ) {
        return stream.index;
      }
    }
  }

  // If mode is 'on' and no language match, return first available
  if (mode === 'on' && candidateStreams.length > 0) {
    return candidateStreams[0].index;
  }

  return null;
};

export default function PlayerScreen() {
  const { settings, userSettings } = useBackendSettings();
  const { hideLoadingScreen } = useLoadingScreen();
  const { showToast } = useToast();
  const {
    movie,
    headerImage,
    title,
    seriesTitle,
    debugLogs: debugLogsParam,
    preferSystemPlayer: preferSystemPlayerParam,
    mediaType: mediaTypeParam,
    year: yearParam,
    seasonNumber: seasonNumberParam,
    episodeNumber: episodeNumberParam,
    episodeName: episodeNameParam,
    durationHint: durationHintParam,
    sourcePath: sourcePathParam,
    displayName: displayNameParam,
    dv: dvFlagParam,
    dvProfile: dvProfileParam,
    hdr10: hdr10FlagParam,
    forceAAC: forceAACParam,
    startOffset: startOffsetParam,
    titleId: titleIdParam,
    imdbId: imdbIdParam,
    tvdbId: tvdbIdParam,
  } = useLocalSearchParams<PlayerParams>();
  const resolvedMovie = useMemo(() => {
    const movieParam = Array.isArray(movie) ? movie[0] : movie;
    if (!movieParam) {
      return movieParam;
    }

    // Expo Router automatically decodes URL params, but we need to re-encode
    // special characters in the path for VLC to work correctly
    try {
      const url = new URL(movieParam);
      // Re-encode path segments while preserving slashes
      const encodedPathname = url.pathname
        .split('/')
        .map((segment) => encodeURIComponent(decodeURIComponent(segment)))
        .join('/');
      url.pathname = encodedPathname;
      return url.toString();
    } catch {
      // If it's not a valid URL, return as-is
      return movieParam;
    }
  }, [movie]);
  const routeHasDolbyVision = useMemo(() => parseBooleanParam(dvFlagParam), [dvFlagParam]);
  const routeHasHDR10 = useMemo(() => parseBooleanParam(hdr10FlagParam), [hdr10FlagParam]);
  const routeHasAnyHDR = routeHasDolbyVision || routeHasHDR10;
  const forceAacFromRoute = useMemo(() => parseBooleanParam(forceAACParam), [forceAACParam]);
  const initialSourcePath = useMemo(() => {
    const raw = Array.isArray(sourcePathParam) ? sourcePathParam[0] : sourcePathParam;
    if (!raw) {
      return undefined;
    }
    try {
      return decodeURIComponent(raw);
    } catch (error) {
      console.debug('[player] unable to decode sourcePath param; using raw value.', error);
      return raw;
    }
  }, [sourcePathParam]);
  const displayName = useMemo(() => {
    const raw = Array.isArray(displayNameParam) ? displayNameParam[0] : displayNameParam;
    return raw || undefined;
  }, [displayNameParam]);
  const routeDvProfile = useMemo(() => {
    const raw = Array.isArray(dvProfileParam) ? dvProfileParam[0] : dvProfileParam;
    return raw ? String(raw) : '';
  }, [dvProfileParam]);
  const initialStartOffset = useMemo(() => {
    const raw = Array.isArray(startOffsetParam) ? startOffsetParam[0] : startOffsetParam;
    if (!raw) {
      return 0;
    }
    const parsed = Number(raw);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : 0;
  }, [startOffsetParam]);
  const [sourcePath, setSourcePath] = useState<string | undefined>(initialSourcePath);
  const safeAreaInsets = useSafeAreaInsets();
  const { width: windowWidth, height: windowHeight } = useWindowDimensions();
  const isPortrait = windowHeight >= windowWidth;
  const shouldPreferSystemPlayer = useMemo(() => parseBooleanParam(preferSystemPlayerParam), [preferSystemPlayerParam]);
  const isLiveTV = useMemo(() => shouldPreferSystemPlayer, [shouldPreferSystemPlayer]);

  // Parse media info parameters
  const mediaType = useMemo(() => {
    const raw = Array.isArray(mediaTypeParam) ? mediaTypeParam[0] : mediaTypeParam;
    return raw?.toLowerCase() || 'movie';
  }, [mediaTypeParam]);

  const year = useMemo(() => {
    const raw = Array.isArray(yearParam) ? yearParam[0] : yearParam;
    const parsed = Number(raw);
    return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : undefined;
  }, [yearParam]);

  const seasonNumber = useMemo(() => {
    const raw = Array.isArray(seasonNumberParam) ? seasonNumberParam[0] : seasonNumberParam;
    const parsed = Number(raw);
    return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : undefined;
  }, [seasonNumberParam]);

  const episodeNumber = useMemo(() => {
    const raw = Array.isArray(episodeNumberParam) ? episodeNumberParam[0] : episodeNumberParam;
    const parsed = Number(raw);
    return Number.isFinite(parsed) && parsed > 0 ? Math.trunc(parsed) : undefined;
  }, [episodeNumberParam]);

  const episodeName = useMemo(() => {
    return Array.isArray(episodeNameParam) ? episodeNameParam[0] : episodeNameParam;
  }, [episodeNameParam]);

  const titleId = useMemo(() => {
    return Array.isArray(titleIdParam) ? titleIdParam[0] : titleIdParam;
  }, [titleIdParam]);

  const imdbId = useMemo(() => {
    return Array.isArray(imdbIdParam) ? imdbIdParam[0] : imdbIdParam;
  }, [imdbIdParam]);

  const tvdbId = useMemo(() => {
    return Array.isArray(tvdbIdParam) ? tvdbIdParam[0] : tvdbIdParam;
  }, [tvdbIdParam]);

  const cleanSeriesTitle = useMemo(() => {
    if (typeof seriesTitle === 'string' && seriesTitle.trim()) {
      return seriesTitle.trim();
    }
    return stripEpisodeCodeSuffix(title);
  }, [seriesTitle, title]);

  const parsedDurationHint = useMemo(() => {
    const raw = Array.isArray(durationHintParam) ? durationHintParam[0] : durationHintParam;
    const parsed = Number(raw);
    return Number.isFinite(parsed) && parsed > 0 ? parsed : undefined;
  }, [durationHintParam]);

  const prefersSystemControls = useMemo(() => {
    const result = (() => {
      if (shouldPreferSystemPlayer && Platform.OS === 'ios') {
        return true;
      }

      if (Platform.OS !== 'web') {
        return false;
      }

      return isMobileWeb();
    })();
    console.log('[player] prefersSystemControls computed', {
      result,
      shouldPreferSystemPlayer,
      platformOS: Platform.OS,
      isMobileWeb: isMobileWeb(),
    });
    return result;
  }, [shouldPreferSystemPlayer]);
  const isTvPlatform = Platform.isTV;
  const [paused, setPaused] = useState<boolean>(false);
  const [controlsVisible, setControlsVisible] = useState<boolean>(
    isTvPlatform || (Platform.OS === 'web' && !prefersSystemControls),
  );
  const controlsVisibleRef = useRef<boolean>(controlsVisible);
  const [isVideoBuffering, setIsVideoBuffering] = useState<boolean>(false);
  const [seekIndicatorAmount, setSeekIndicatorAmount] = useState<number>(0);
  const seekIndicatorTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const seekIndicatorStartTimeRef = useRef<number>(0);
  const seekPressCountRef = useRef<number>(0);
  // Refs for callbacks used in key handler (declared before callback definitions)
  const seekRef = useRef<((time: number, showControlsAfter?: boolean) => void) | null>(null);
  const showControlsRef = useRef<(() => void) | null>(null);
  const hideControlsRef = useRef<((options?: { immediate?: boolean }) => void) | null>(null);
  const togglePausePlayRef = useRef<(() => void) | null>(null);
  const warmStartHlsSessionRef = useRef<((targetTime: number) => Promise<boolean>) | null>(null);
  const [currentTime, setCurrentTime] = useState<number>(0);
  const [duration, setDuration] = useState<number>(0);
  const [metadataDuration, setMetadataDuration] = useState<number>(0);
  const [currentMovieUrl, setCurrentMovieUrl] = useState<string | null>(resolvedMovie ?? null);
  const [volume, setVolume] = useState<number>(1);
  const [isFullscreen, setIsFullscreen] = useState<boolean>(false);
  const [audioTrackOptions, setAudioTrackOptions] = useState<TrackOption[]>([]);
  const [subtitleTrackOptions, setSubtitleTrackOptions] = useState<TrackOption[]>([SUBTITLE_OFF_OPTION]);
  const [selectedAudioTrackId, setSelectedAudioTrackId] = useState<string | null>(null);
  const [selectedSubtitleTrackId, setSelectedSubtitleTrackId] = useState<string | null>(SUBTITLE_OFF_OPTION.id);
  const [debugEntries, setDebugEntries] = useState<DebugLogEntry[]>([]);
  const [isModalOpen, setIsModalOpen] = useState<boolean>(false);
  const isModalOpenRef = useRef(isModalOpen);
  const handleModalStateChange = useCallback((open: boolean) => {
    console.log('[player] handleModalStateChange', { open });
    isModalOpenRef.current = open;
    setIsModalOpen(open);
  }, []);
  const [isSeeking, setIsSeeking] = useState<boolean>(false);
  const [hasStartedPlaying, setHasStartedPlaying] = useState<boolean>(false);
  // For HDR content (Dolby Vision/HDR10), we need to use React Native Video player with HLS
  // VLCKit does not support HDR output - it tone-maps to SDR
  const [hasDolbyVision, setHasDolbyVision] = useState<boolean>(routeHasDolbyVision);
  const [isFilenameDisplayed, setIsFilenameDisplayed] = useState<boolean>(false);
  // Video color metadata for HDR info display
  const [videoColorInfo, setVideoColorInfo] = useState<{
    colorTransfer?: string;
    colorPrimaries?: string;
    colorSpace?: string;
    isHDR10?: boolean;
  } | null>(null);
  // Video dimensions for subtitle positioning (relative to video content, not screen)
  const [videoSize, setVideoSize] = useState<{ width: number; height: number } | null>(null);
  const effectiveMovie = useMemo(() => currentMovieUrl ?? resolvedMovie ?? null, [currentMovieUrl, resolvedMovie]);
  const isHlsStream = useMemo(() => {
    if (!effectiveMovie) {
      return false;
    }
    try {
      const parsed = new URL(String(effectiveMovie));
      return parsed.pathname.includes('/video/hls/');
    } catch {
      return String(effectiveMovie).includes('/video/hls/');
    }
  }, [effectiveMovie]);

  // Derive the sidecar subtitle VTT URL from the HLS playlist URL
  // The backend extracts subtitles to subtitles.vtt alongside the HLS segments for fMP4/HDR content
  const sidecarSubtitleUrl = useMemo(() => {
    if (!isHlsStream || !effectiveMovie) {
      return null;
    }
    // Replace stream.m3u8 with subtitles.vtt in the URL
    return String(effectiveMovie).replace(/stream\.m3u8/, 'subtitles.vtt');
  }, [isHlsStream, effectiveMovie]);

  // Debug logging for sidecar subtitle URL changes
  useEffect(() => {
    console.log('[player] sidecarSubtitleUrl changed', {
      sidecarSubtitleUrl,
      effectiveMovie: effectiveMovie ? String(effectiveMovie).substring(0, 100) : null,
      isHlsStream,
      currentTime: currentTimeRef.current,
    });
  }, [sidecarSubtitleUrl, effectiveMovie, isHlsStream]);

  // Check if the current URL is already an HLS session playlist (not just the /hls/start endpoint)
  // Session playlist URLs look like: /video/hls/{sessionId}/stream.m3u8
  // We should NOT create a new session if we already have a playlist URL
  const isExistingHlsSession = useMemo(() => {
    if (!effectiveMovie) return false;
    try {
      const url = new URL(String(effectiveMovie));
      // Already a playlist URL if it contains /video/hls/ and ends with .m3u8
      return url.pathname.includes('/video/hls/') && url.pathname.endsWith('.m3u8');
    } catch {
      const urlStr = String(effectiveMovie);
      return urlStr.includes('/video/hls/') && urlStr.includes('.m3u8');
    }
  }, [effectiveMovie]);

  // Only warm start if we need to resume AND we don't already have a session playlist URL
  // If the details screen already created a session, we should use it instead of creating another
  const shouldWarmStartResume = useMemo(
    () => initialStartOffset > 0 && isHlsStream && !isExistingHlsSession,
    [initialStartOffset, isHlsStream, isExistingHlsSession],
  );
  const videoRef = useRef<VideoPlayerHandle>(null);
  const hasAutoLaunchedSystemPlayerRef = useRef(false);
  const hideControlsTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  // Track the current HLS session ID for keepalive pings when paused
  const hlsSessionIdRef = useRef<string | null>(null);
  const currentTimeRef = useRef<number>(0);
  const durationRef = useRef<number>(0);
  const playbackOffsetRef = useRef<number>(initialStartOffset);
  const sessionBufferEndRef = useRef<number>(initialStartOffset);
  const warmStartTokenRef = useRef(0);
  const warmStartDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const warmStartDebounceResolveRef = useRef<((value: boolean) => void) | null>(null);
  const pendingSessionSeekRef = useRef<number | null>(null);
  const pendingSeekAttemptRef = useRef<{ attempts: number; lastAttemptMs: number }>({
    attempts: 0,
    lastAttemptMs: 0,
  });
  const hasAttemptedInitialWarmStartRef = useRef(false);
  const hasReceivedPlayerLoadRef = useRef(false);
  const hlsSessionRetryCountRef = useRef(0);
  const isRetryingHlsSessionRef = useRef(false);
  const isSeekingRef = useRef<boolean>(false);
  const pausedForSeekRef = useRef<boolean>(false); // Track if we paused for HLS session seek or track switching
  const controlsOpacity = useRef(new Animated.Value(isTvPlatform ? 1 : 0)).current;
  const canUseNativeDriver = Platform.OS !== 'web';
  const theme = useTheme();
  const debugIdRef = useRef(0);
  const debugQueueRef = useRef<DebugLogEntry[]>([]);
  const debugFlushTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const debugSessionRef = useRef<string | null>(null);
  const implementationRef = useRef<VideoImplementation | 'unknown'>('unknown');
  const debugOverlayEnabled = useMemo(() => {
    if (Platform.OS !== 'web') {
      return false;
    }

    const rawValue = Array.isArray(debugLogsParam) ? debugLogsParam[0] : debugLogsParam;
    if (!rawValue) {
      return false;
    }

    const normalized = rawValue.toString().trim().toLowerCase();
    return normalized === '1' || normalized === 'true' || normalized === 'yes' || normalized === 'on';
  }, [debugLogsParam]);

  // Clear image memory cache on Android TV to free RAM for video decoder
  // This is critical for low-memory devices like Fire Stick playing 4K content
  useEffect(() => {
    if (Platform.OS === 'android' && Platform.isTV) {
      console.log('[PlayerScreen] Clearing image memory cache for Android TV');
      clearImageMemoryCache().catch((err) => {
        console.warn('[PlayerScreen] Failed to clear image cache:', err);
      });
    }
  }, []);

  useEffect(() => {
    setSourcePath(initialSourcePath);
  }, [initialSourcePath]);

  // Setup playback progress tracking
  const { activeUserId } = useUserProfiles();

  // Build media item ID based on type using stable identifiers
  const mediaItemId = useMemo(() => {
    if (mediaType === 'episode') {
      // For episodes, build ID from series identifier and episode numbers
      // Priority: titleId > imdbId > tvdbId
      const seriesId = titleId || imdbId || tvdbId;
      if (seriesId && seasonNumber && episodeNumber) {
        // Format: seriesId:S01E02
        const seasonStr = seasonNumber.toString().padStart(2, '0');
        const episodeStr = episodeNumber.toString().padStart(2, '0');
        return `${seriesId}:S${seasonStr}E${episodeStr}`;
      }
      // Fallback to sourcePath if no stable IDs available
      return sourcePath || title || 'unknown';
    }
    // For movies, use titleId > imdbId > tvdbId > sourcePath
    return titleId || imdbId || tvdbId || sourcePath || title || 'unknown';
  }, [mediaType, titleId, imdbId, tvdbId, seasonNumber, episodeNumber, sourcePath, title]);

  // Memoize media info to prevent hook recreation on every render
  const mediaInfo = useMemo(() => {
    // Build external IDs map
    const externalIds: Record<string, string> = {};
    if (imdbId) externalIds.imdb = imdbId;
    if (tvdbId) externalIds.tvdb = tvdbId;
    if (titleId) externalIds.titleId = titleId;

    return {
      mediaType: (mediaType === 'episode' ? 'episode' : 'movie') as 'episode' | 'movie',
      itemId: mediaItemId,
      seasonNumber,
      episodeNumber,
      seriesId: mediaType === 'episode' ? titleId || imdbId || tvdbId : undefined,
      seriesName: mediaType === 'episode' ? cleanSeriesTitle || title : undefined, // Prefer clean series title, fallback to decorated title
      episodeName: mediaType === 'episode' ? episodeName : undefined,
      movieName: mediaType === 'movie' ? title : undefined,
      year,
      externalIds: Object.keys(externalIds).length > 0 ? externalIds : undefined,
    };
  }, [
    mediaType,
    mediaItemId,
    seasonNumber,
    episodeNumber,
    title,
    cleanSeriesTitle,
    episodeName,
    year,
    titleId,
    imdbId,
    tvdbId,
  ]);

  // Initialize playback progress tracking hook
  const { reportProgress } = usePlaybackProgress(activeUserId || 'default', mediaInfo, {
    updateInterval: 10000, // Update every 10 seconds
    minTimeChange: 5, // Only update if position changed by 5+ seconds
    debug: true, // Enable for debugging
  });

  // Extract HLS session ID from existing playlist URL (for sessions created by details screen)
  useEffect(() => {
    if (!isExistingHlsSession || !effectiveMovie) {
      return;
    }
    // URL format: /video/hls/{sessionId}/stream.m3u8
    const match = String(effectiveMovie).match(/\/video\/hls\/([^/]+)\/stream\.m3u8/);
    if (match && match[1]) {
      hlsSessionIdRef.current = match[1];
      console.log('[player] extracted HLS session ID from URL:', match[1]);
    }
  }, [isExistingHlsSession, effectiveMovie]);

  // Track if we've already shown a fatal error alert (to prevent duplicate alerts)
  const hasShownFatalErrorRef = useRef(false);

  // Send keepalive pings and poll for session status to detect stream errors
  // The backend kills FFmpeg after 30s of no segment requests, but players buffer aggressively
  // so we send keepalives continuously while the player is mounted with an HLS stream
  useEffect(() => {
    if (!isHlsStream) {
      return;
    }

    // Send keepalive and check status every 10 seconds
    // More frequent polling for status to detect errors sooner
    const intervalId = setInterval(async () => {
      const sessionId = hlsSessionIdRef.current;
      if (!sessionId) {
        return;
      }

      // Send keepalive ping
      try {
        await apiService.keepaliveHlsSession(sessionId);
      } catch (error) {
        console.warn('[player] keepalive ping failed:', error);
      }

      // Poll for session status to detect fatal errors
      try {
        const status = await apiService.getHlsSessionStatus(sessionId);

        if (status.status === 'error' && status.fatalError && !hasShownFatalErrorRef.current) {
          hasShownFatalErrorRef.current = true;
          console.warn('[player] HLS session fatal error:', status.fatalError);

          // Show error toast and navigate back
          showToast(`Stream error: ${status.fatalError}`, { tone: 'danger', duration: 5000 });
          router.back();
        }
      } catch (error) {
        // Status check failed - might be session expired, not necessarily an error
        console.debug('[player] status check failed:', error);
      }
    }, 10000);

    console.log('[player] started keepalive/status interval for HLS stream');

    return () => {
      console.log('[player] stopping keepalive/status pings (unmounted or stream changed)');
      clearInterval(intervalId);
      hasShownFatalErrorRef.current = false;
    };
  }, [isHlsStream]);

  useEffect(() => {
    setCurrentMovieUrl(resolvedMovie ?? null);
  }, [resolvedMovie]);

  // Update hasDolbyVision if route params change (e.g., navigation)
  useEffect(() => {
    if (routeHasDolbyVision) {
      setHasDolbyVision(true);
    }
  }, [routeHasDolbyVision]);

  const hasAppliedInitialSeekRef = useRef(false);
  const seekAttemptCountRef = useRef(0);
  const seekRetryTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const hasAppliedInitialTracksRef = useRef(false);

  useEffect(() => {
    console.log('🎬 [player] Initializing with startOffset:', initialStartOffset, {
      isHlsStream,
      isExistingHlsSession,
      shouldWarmStartResume,
    });

    if (initialStartOffset > 0) {
      if (shouldWarmStartResume) {
        pendingSessionSeekRef.current = null;
        console.log('🎬 [player] resume will warm start HLS session instead of client seek', {
          initialStartOffset,
        });
      } else if (isExistingHlsSession) {
        // Existing HLS session from details screen - backend already started transcoding from startOffset
        // No seek needed since player time 0 = absolute time startOffset
        pendingSessionSeekRef.current = null;
        playbackOffsetRef.current = initialStartOffset;
        console.log('🎬 [player] using existing HLS session at offset:', initialStartOffset);
      } else {
        pendingSessionSeekRef.current = initialStartOffset;
        console.log('🎬 [player] Set pendingSessionSeekRef to:', initialStartOffset);
      }
      pendingSeekAttemptRef.current.attempts = 0;
      pendingSeekAttemptRef.current.lastAttemptMs = 0;
    } else {
      pendingSessionSeekRef.current = null;
    }
    hasAttemptedInitialWarmStartRef.current = false;
    hasReceivedPlayerLoadRef.current = false;

    // For existing HLS sessions with a start offset, playbackOffsetRef was already set above
    // For new sessions or existing sessions starting from 0, start at 0
    if (isExistingHlsSession && initialStartOffset > 0) {
      // Existing HLS session starts at the offset (playbackOffsetRef already set above)
      sessionBufferEndRef.current = initialStartOffset;
      currentTimeRef.current = initialStartOffset;
      setCurrentTime(initialStartOffset);
    } else {
      playbackOffsetRef.current = 0;
      sessionBufferEndRef.current = 0;
      currentTimeRef.current = 0;
      setCurrentTime(0);
    }
    hasAppliedInitialSeekRef.current = false;
    hasAppliedInitialTracksRef.current = false;
    seekAttemptCountRef.current = 0;
    pendingSeekAttemptRef.current.attempts = 0;
    pendingSeekAttemptRef.current.lastAttemptMs = 0;
    hasReceivedPlayerLoadRef.current = false;

    // Clear any pending retry timeouts
    if (seekRetryTimeoutRef.current) {
      clearTimeout(seekRetryTimeoutRef.current);
      seekRetryTimeoutRef.current = null;
    }

    return () => {
      // Cleanup on unmount
      if (seekRetryTimeoutRef.current) {
        clearTimeout(seekRetryTimeoutRef.current);
        seekRetryTimeoutRef.current = null;
      }
      if (warmStartDebounceRef.current) {
        clearTimeout(warmStartDebounceRef.current);
        warmStartDebounceRef.current = null;
        warmStartDebounceResolveRef.current = null;
      }
    };
  }, [initialStartOffset, shouldWarmStartResume, isHlsStream, isExistingHlsSession]);

  useEffect(() => {
    if (!shouldWarmStartResume || initialStartOffset <= 0 || !sourcePath) {
      return;
    }

    if (hasAttemptedInitialWarmStartRef.current) {
      return;
    }

    let cancelled = false;
    hasAttemptedInitialWarmStartRef.current = true;

    (async () => {
      console.log('[player] attempting initial warm HLS session for resume', {
        initialStartOffset,
        sourcePath,
      });
      const warmed = await warmStartHlsSessionRef.current?.(initialStartOffset);
      if (cancelled) {
        return;
      }
      if (!warmed) {
        console.warn('[player] warm start resume failed - falling back to client-side seek resume');
        pendingSessionSeekRef.current = initialStartOffset;
        pendingSeekAttemptRef.current.attempts = 0;
        pendingSeekAttemptRef.current.lastAttemptMs = 0;
      }
    })();

    return () => {
      cancelled = true;
    };
  }, [initialStartOffset, shouldWarmStartResume, sourcePath]);

  const formatDebugMessage = useCallback((values: unknown[]): string => {
    return values
      .map((value) => {
        if (typeof value === 'string') {
          return value;
        }
        if (value instanceof Error) {
          return value.stack || `${value.name}: ${value.message}`;
        }
        try {
          return JSON.stringify(value);
        } catch {
          return String(value);
        }
      })
      .join(' ');
  }, []);

  const formatDebugTimestamp = useCallback((value: string): string => {
    if (!value) {
      return '';
    }
    const date = new Date(value);
    if (Number.isNaN(date.getTime())) {
      return value;
    }
    return date.toLocaleTimeString();
  }, []);

  const ensureDebugSession = useCallback(() => {
    if (debugSessionRef.current) {
      return debugSessionRef.current;
    }
    let next = '';
    if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') {
      next = crypto.randomUUID();
    } else {
      next = `debug-${Math.random().toString(36).slice(2)}-${Date.now()}`;
    }
    debugSessionRef.current = next;
    return next;
  }, []);

  const [playerImplementation, setPlayerImplementation] = useState<VideoImplementation | 'unknown'>('unknown');
  const usesSystemManagedControls = useMemo(() => {
    // Never use system controls - we always want custom controls for track selection
    // Previously: prefersSystemControls && playerImplementation !== 'vlc'
    return false;
  }, [prefersSystemControls, playerImplementation]);
  const shouldAutoHideControls = !usesSystemManagedControls;
  const autoHideDurationMs = isTvPlatform ? 3000 : 3000;
  const shouldHideStatusBar = Platform.OS === 'ios' && !Platform.isTV && !usesSystemManagedControls && !controlsVisible;
  const isTouchOverlayToggleSupported = Platform.OS !== 'web' && !Platform.isTV;

  useEffect(() => {
    isSeekingRef.current = isSeeking;
  }, [isSeeking]);

  const flushDebugQueue = useCallback(async () => {
    if (!debugOverlayEnabled) {
      debugQueueRef.current = [];
      return;
    }

    const queue = debugQueueRef.current;
    if (!queue.length) {
      return;
    }

    debugQueueRef.current = [];

    const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
    const endpoint = `${baseUrl}/debug/log`;
    const sessionId = ensureDebugSession();
    const userAgent = typeof navigator !== 'undefined' ? navigator.userAgent : undefined;
    const path = typeof window !== 'undefined' ? window.location?.href : undefined;

    const payload = {
      sessionId,
      userAgent,
      path,
      entries: queue.map((entry) => ({
        level: entry.level,
        message: entry.message,
        timestamp: entry.timestamp,
      })),
    };

    const body = JSON.stringify(payload);
    const headers: Record<string, string> = {
      'Content-Type': 'application/json',
    };

    const appendApiKey = (base: string, key: string) =>
      `${base}${base.includes('?') ? '&' : '?'}apiKey=${encodeURIComponent(key)}`;

    let url = endpoint;
    const apiKey = apiService.getApiKey().trim();
    if (apiKey) {
      headers['X-API-Key'] = apiKey;
      url = appendApiKey(url, apiKey);
    } else if (typeof window !== 'undefined' && window.location?.search) {
      const inlineKey = new URLSearchParams(window.location.search).get('apiKey');
      if (inlineKey) {
        url = appendApiKey(url, inlineKey);
      }
    }

    try {
      const nav =
        typeof navigator !== 'undefined'
          ? (navigator as Navigator & { sendBeacon?: (url: string, data?: BodyInit | null) => boolean })
          : undefined;
      if (nav?.sendBeacon) {
        const blob = new Blob([body], { type: 'application/json' });
        if (nav.sendBeacon(url, blob)) {
          return;
        }
      }

      const response = await fetch(url, {
        method: 'POST',
        headers,
        body,
        keepalive: true,
      });
      if (!response.ok) {
        debugQueueRef.current.unshift(...queue);
        if (!debugFlushTimeoutRef.current) {
          debugFlushTimeoutRef.current = setTimeout(() => {
            debugFlushTimeoutRef.current = null;
            void flushDebugQueue();
          }, 2000);
        }
      }
    } catch {
      debugQueueRef.current.unshift(...queue);
      if (!debugFlushTimeoutRef.current) {
        debugFlushTimeoutRef.current = setTimeout(() => {
          debugFlushTimeoutRef.current = null;
          void flushDebugQueue();
        }, 2000);
      }
    }
  }, [debugOverlayEnabled, ensureDebugSession]);

  const scheduleDebugFlush = useCallback(() => {
    if (debugFlushTimeoutRef.current) {
      return;
    }
    debugFlushTimeoutRef.current = setTimeout(() => {
      debugFlushTimeoutRef.current = null;
      void flushDebugQueue();
    }, 1500);
  }, [flushDebugQueue]);

  useEffect(() => {
    if (!debugOverlayEnabled) {
      return;
    }

    const consoleRef = console as unknown as Record<ConsoleLevel, (...args: unknown[]) => void>;
    const methods: ConsoleLevel[] = ['log', 'warn', 'error'];
    const originals = new Map<ConsoleLevel, (...args: unknown[]) => void>();

    const appendEntry = (level: ConsoleLevel, args: unknown[]) => {
      const nextId = debugIdRef.current + 1;
      debugIdRef.current = nextId;

      const entry: DebugLogEntry = {
        id: nextId,
        level,
        message: formatDebugMessage(args),
        timestamp: new Date().toISOString(),
      };

      debugQueueRef.current.push(entry);

      if (debugQueueRef.current.length >= 50) {
        if (debugFlushTimeoutRef.current) {
          clearTimeout(debugFlushTimeoutRef.current);
          debugFlushTimeoutRef.current = null;
        }
        void flushDebugQueue();
      } else {
        scheduleDebugFlush();
      }

      setDebugEntries((current) => {
        const appended = [...current, entry];
        if (appended.length > 200) {
          return appended.slice(appended.length - 200);
        }
        return appended;
      });
    };

    methods.forEach((method) => {
      const original = consoleRef[method];
      originals.set(method, original);
      consoleRef[method] = (...args: unknown[]) => {
        if (typeof original === 'function') {
          original(...args);
        }
        appendEntry(method, args);
      };
    });

    return () => {
      methods.forEach((method) => {
        const original = originals.get(method);
        if (original) {
          consoleRef[method] = original;
        }
      });
      if (debugFlushTimeoutRef.current) {
        clearTimeout(debugFlushTimeoutRef.current);
        debugFlushTimeoutRef.current = null;
      }
      void flushDebugQueue();
    };
  }, [debugOverlayEnabled, flushDebugQueue, formatDebugMessage, scheduleDebugFlush]);

  useEffect(() => {
    if (!debugOverlayEnabled) {
      setDebugEntries([]);
      debugQueueRef.current = [];
      if (debugFlushTimeoutRef.current) {
        clearTimeout(debugFlushTimeoutRef.current);
        debugFlushTimeoutRef.current = null;
      }
    }
  }, [debugOverlayEnabled]);

  useEffect(() => {
    console.log('🎮 PlayerScreen initialized');
    if (Platform.isTV) {
      console.log('[Player] Spatial navigation active:', Platform.isTV);
    }
    return () => {
      console.log('🎮 PlayerScreen disposed');
    };
  }, []);

  // Enable rotation for video player on mobile devices
  useEffect(() => {
    if (Platform.OS === 'web' || Platform.isTV) {
      return;
    }

    const unlockOrientation = async () => {
      try {
        // Dynamic require to avoid loading native module at parse time
        const ScreenOrientation = require('expo-screen-orientation');
        await ScreenOrientation.unlockAsync();
        console.log('[Player] Screen orientation unlocked for video playback');
      } catch (error) {
        console.warn('[Player] Failed to unlock screen orientation:', error);
      }
    };

    unlockOrientation();

    // Cleanup: lock orientation back to portrait when player closes
    return () => {
      const lockOrientation = async () => {
        try {
          // Dynamic require to avoid loading native module at parse time
          const ScreenOrientation = require('expo-screen-orientation');
          await ScreenOrientation.lockAsync(ScreenOrientation.OrientationLock.PORTRAIT_UP);
          console.log('[Player] Screen orientation locked back to portrait');
        } catch (error) {
          console.warn('[Player] Failed to lock screen orientation:', error);
        }
      };
      lockOrientation();
    };
  }, []);

  const handleImplementationResolved = useCallback((implementation: VideoImplementation) => {
    if (implementationRef.current === implementation) {
      return;
    }

    implementationRef.current = implementation;
    console.info('[player] video implementation resolved:', implementation);
    console.log('[player] Platform:', { OS: Platform.OS, isTV: Platform.isTV });
    setPlayerImplementation(implementation);
  }, []);

  const playerImplementationLabel = useMemo(() => {
    switch (playerImplementation) {
      case 'vlc':
        return 'React Native VLC';
      case 'rnv':
        return 'React Native Video';
      case 'expo':
        return 'Expo Video';
      case 'web':
        return 'Web Player';
      case 'mobile-system':
        return 'System Player';
      default:
        return null;
    }
  }, [playerImplementation]);

  // Build HDR info for display - combines route params with fetched metadata
  const hdrInfo = useMemo(() => {
    const isDolbyVision = routeHasDolbyVision;
    // HDR10 can be from route params or detected from video metadata
    const isHDR10 = routeHasHDR10 || (videoColorInfo?.isHDR10 ?? false);

    // Only return info if we have HDR content or color metadata
    if (!isDolbyVision && !isHDR10 && !videoColorInfo?.colorTransfer) {
      return undefined;
    }

    return {
      isDolbyVision,
      dolbyVisionProfile: routeDvProfile || undefined,
      isHDR10,
      colorTransfer: videoColorInfo?.colorTransfer,
      colorPrimaries: videoColorInfo?.colorPrimaries,
      colorSpace: videoColorInfo?.colorSpace,
    };
  }, [routeHasDolbyVision, routeHasHDR10, routeDvProfile, videoColorInfo]);

  useEffect(() => {
    console.log('🎮 Movie:', effectiveMovie);
    console.log('🎮 Header Image:', headerImage);
    console.log('🎮 Title:', title);
  }, [effectiveMovie, headerImage, title]);

  // Log the final streaming URL that the player will use
  useEffect(() => {
    if (!effectiveMovie) {
      console.log('[player] no streaming url provided');
      return;
    }
    try {
      const u = new URL(String(effectiveMovie));
      console.log('[player] streaming url', {
        href: u.href,
        origin: u.origin,
        path: u.pathname + u.search,
      });
    } catch {
      console.log('[player] streaming url (raw)', effectiveMovie);
    }
  }, [effectiveMovie]);

  // Lock/unlock spatial navigation based on controls visibility
  const { lock: baseLockSpatialNav, unlock: baseUnlockSpatialNav } = useLockSpatialNavigation();
  const spatialNavigator = useSpatialNavigator();

  // Wrap lock/unlock with logging
  const lockSpatialNav = useCallback(() => {
    if (isTvPlatform) {
      return;
    }
    baseLockSpatialNav();
  }, [baseLockSpatialNav, isTvPlatform]);

  const unlockSpatialNav = useCallback(() => {
    if (isTvPlatform) {
      return;
    }
    baseUnlockSpatialNav();
  }, [baseUnlockSpatialNav, isTvPlatform]);

  // Track if we've grabbed focus for this controls visible session
  const hasFocusedRef = useRef(false);
  // Track the last focused element key to restore focus when controls reappear
  const lastFocusedKeyRef = useRef<string>('exit-button');

  // Callback to track focus changes
  const handleFocusChange = useCallback((focusKey: string) => {
    lastFocusedKeyRef.current = focusKey;
  }, []);

  // Ensure RemoteControlManager stays active so key events reach the controls overlay
  useEffect(() => {
    if (isTvPlatform && !usesSystemManagedControls) {
      baseUnlockSpatialNav();
    }
  }, [baseUnlockSpatialNav, isTvPlatform, usesSystemManagedControls]);

  useEffect(() => {
    if (usesSystemManagedControls) {
      return;
    }

    RemoteControlManager.enableTvEventHandling();
  }, [usesSystemManagedControls]);

  useEffect(() => {
    if (usesSystemManagedControls) {
      return;
    }

    if (isTvPlatform) {
      // On TV, controls are in a Modal with their own SpatialNavigationRoot.
      // Don't call grabFocus from the outer context - let DefaultFocus inside
      // the Modal handle initial focus.
      if (!controlsVisible) {
        // Reset focus flag when controls are hidden so we re-grab on next show
        hasFocusedRef.current = false;
      }
      return;
    }

    // Lock/unlock spatial navigation based on controls visibility
    if (controlsVisible) {
      unlockSpatialNav();

      // Grab focus to the last focused element when controls become visible
      // Only do this once per controls visible session
      if (!hasFocusedRef.current) {
        hasFocusedRef.current = true;
        // Use a longer delay to ensure the button is rendered, registered, and nav is unlocked
        setTimeout(() => {
          try {
            const focusKey = lastFocusedKeyRef.current || 'play-pause-button';
            spatialNavigator.grabFocus(focusKey);
          } catch {
            // Focus grab can fail if element not yet registered
          }
        }, 150);
      }
    } else {
      lockSpatialNav();
      // Reset focus flag when controls are hidden
      hasFocusedRef.current = false;
    }
  }, [controlsVisible, usesSystemManagedControls, lockSpatialNav, unlockSpatialNav, spatialNavigator, isTvPlatform]);

  useEffect(() => {
    if (usesSystemManagedControls) {
      return;
    }

    const handleKeyDown = (key: SupportedKeys) => {
      console.log('[player] key pressed:', key, { controlsVisible: controlsVisibleRef.current });

      console.log('[player] handling key:', key);

      switch (key) {
        case SupportedKeys.Left:
          if (!controlsVisibleRef.current) {
            // Progressive seek: increase amount based on consecutive presses
            // 1-2 presses: 30s, 3-4: 60s, 5-6: 90s, etc.
            seekPressCountRef.current += 1;
            const seekAmountBackward = 30 * Math.ceil(seekPressCountRef.current / 2);

            // Update seek indicator and seek using accumulated amount from start time
            // This ensures rapid presses accumulate correctly instead of using stale currentTime
            setSeekIndicatorAmount((prev) => {
              // Store initial time when seeking starts
              let startTime = seekIndicatorStartTimeRef.current;
              if (prev === 0) {
                startTime = currentTimeRef.current;
                seekIndicatorStartTimeRef.current = startTime;
              }
              const newAmount = prev - seekAmountBackward;
              const targetTime = Math.max(0, startTime + newAmount);
              void seekRef.current?.(targetTime, false);
              return newAmount;
            });

            // Make controls visible (with opacity) so seek indicator shows
            controlsOpacity.setValue(1);

            // Clear existing timeout and set new one
            if (seekIndicatorTimeoutRef.current) {
              clearTimeout(seekIndicatorTimeoutRef.current);
            }
            seekIndicatorTimeoutRef.current = setTimeout(() => {
              setSeekIndicatorAmount(0);
              seekIndicatorStartTimeRef.current = 0;
              seekPressCountRef.current = 0;
              // Hide controls again after seeking is done
              controlsOpacity.setValue(0);
            }, 1000);
          } else {
            // When controls are visible, allow spatial navigation
            showControlsRef.current?.();
          }
          break;
        case SupportedKeys.Right:
          if (!controlsVisibleRef.current) {
            // Progressive seek: increase amount based on consecutive presses
            // 1-2 presses: 30s, 3-4: 60s, 5-6: 90s, etc.
            seekPressCountRef.current += 1;
            const seekAmountForward = 30 * Math.ceil(seekPressCountRef.current / 2);

            // Update seek indicator and seek using accumulated amount from start time
            // This ensures rapid presses accumulate correctly instead of using stale currentTime
            setSeekIndicatorAmount((prev) => {
              // Store initial time when seeking starts
              let startTime = seekIndicatorStartTimeRef.current;
              if (prev === 0) {
                startTime = currentTimeRef.current;
                seekIndicatorStartTimeRef.current = startTime;
              }
              const newAmount = prev + seekAmountForward;
              const targetTime = startTime + newAmount;
              void seekRef.current?.(targetTime, false);
              return newAmount;
            });

            // Make controls visible (with opacity) so seek indicator shows
            controlsOpacity.setValue(1);

            // Clear existing timeout and set new one
            if (seekIndicatorTimeoutRef.current) {
              clearTimeout(seekIndicatorTimeoutRef.current);
            }
            seekIndicatorTimeoutRef.current = setTimeout(() => {
              setSeekIndicatorAmount(0);
              seekIndicatorStartTimeRef.current = 0;
              seekPressCountRef.current = 0;
              // Hide controls again after seeking is done
              controlsOpacity.setValue(0);
            }, 1000);
          } else {
            // When controls are visible, allow spatial navigation
            showControlsRef.current?.();
          }
          break;
        case SupportedKeys.FastForward:
          void seekRef.current?.(currentTimeRef.current + 10);
          showControlsRef.current?.();
          break;
        case SupportedKeys.Rewind:
          void seekRef.current?.(currentTimeRef.current - 10);
          showControlsRef.current?.();
          break;
        case SupportedKeys.Back:
          if (controlsVisibleRef.current) {
            hideControlsRef.current?.();
          } else {
            router.back();
          }
          break;
        case SupportedKeys.PlayPause:
          togglePausePlayRef.current?.();
          break;
        default:
          showControlsRef.current?.();
          break;
      }
    };

    const listener = RemoteControlManager.addKeydownListener(handleKeyDown);
    return () => {
      RemoteControlManager.removeKeydownListener(listener);
    };
  }, [usesSystemManagedControls, router]);

  const updateDuration = useCallback((value: number, source: string) => {
    if (!Number.isFinite(value) || value <= 0) {
      return;
    }

    const next = Number(value);
    const current = durationRef.current || 0;

    // Prefer full duration from metadata/hints over buffered/seekable amounts
    // For HLS streams, buffered length < total duration, so don't downgrade
    if (current > 0) {
      const ratio = Math.max(current / next, next / current);
      const normalisationFactors = [60, 100, 600, 1000, 3600, 60000]; // Known unit conversion ratios (mins, percentages, ms, hours)
      const tolerance = 0.01; // Allow 1% drift when comparing potential unit conversions
      const isUnitAdjustment = normalisationFactors.some((factor) => {
        const distance = Math.abs(ratio - factor);
        return distance / factor <= tolerance;
      });

      // Don't replace a longer duration with a shorter one (e.g., don't replace total duration with buffered length)
      // Note: 'player.load' is NOT included here because for HLS streams, the player's initial duration
      // is often incomplete (only based on what's been buffered). We prefer metadata/URL hints.
      if (!isUnitAdjustment && next <= current) {
        // Exception: allow update only from metadata or url-param-hint (authoritative sources)
        if (source !== 'metadata' && source !== 'url-param-hint') {
          return;
        }
      }

      // Additional check: unit normalisation should only apply when the new duration is LARGER (e.g., 60 minutes -> 3600 seconds)
      // If we detect a potential unit conversion but the new duration is shorter, it's likely a false positive
      // (e.g., HLS buffered length that happens to be ~1/60th of total duration)
      if (isUnitAdjustment && next < current) {
        return;
      }
    }
    durationRef.current = next;
    setDuration(next);
  }, []);

  const warmStartHlsSession = useCallback(
    async (targetTime: number) => {
      if (!isHlsStream || !sourcePath) {
        return false;
      }

      const safeTarget = Math.max(0, Number(targetTime) || 0);
      const trimmedPath = sourcePath.trim();
      if (!trimmedPath) {
        return false;
      }

      const token = Date.now();
      warmStartTokenRef.current = token;
      setIsVideoBuffering(true);

      try {
        // Use refs to get current track indices (avoids stale closure values)
        const currentAudioTrack = selectedAudioTrackIndexRef.current;
        const currentSubtitleTrack = selectedSubtitleTrackIndexRef.current;

        console.log('[player] creating HLS session with tracks', {
          audioTrack: currentAudioTrack,
          subtitleTrack: currentSubtitleTrack,
          audioTrackType: typeof currentAudioTrack,
          subtitleTrackType: typeof currentSubtitleTrack,
        });

        const response = await apiService.createHlsSession({
          path: trimmedPath,
          dv: routeHasDolbyVision,
          dvProfile: routeDvProfile || undefined,
          hdr: routeHasHDR10,
          forceAAC: forceAacFromRoute,
          start: safeTarget,
          audioTrack: currentAudioTrack ?? undefined,
          subtitleTrack: currentSubtitleTrack ?? undefined,
        });

        // Store session ID for keepalive pings when paused
        hlsSessionIdRef.current = response.sessionId;

        if (warmStartTokenRef.current !== token) {
          return true;
        }

        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const playlistBase = `${baseUrl}${response.playlistUrl}`;
        const existingKey = (() => {
          try {
            if (!effectiveMovie) {
              return '';
            }
            const currentUrl = new URL(String(effectiveMovie));
            const key = currentUrl.searchParams.get('apiKey');
            return key ? key.trim() : '';
          } catch {
            return '';
          }
        })();
        const authKey = apiService.getApiKey().trim() || existingKey;
        const playlistWithKey = authKey
          ? `${playlistBase}${playlistBase.includes('?') ? '&' : '?'}apiKey=${encodeURIComponent(authKey)}`
          : playlistBase;

        const sessionStart =
          typeof response.startOffset === 'number' && response.startOffset >= 0 ? response.startOffset : safeTarget;
        console.log('[player] warmStartHLS setting offsets', {
          responseStartOffset: response.startOffset,
          sessionStart,
          safeTarget,
          playlistUrl: playlistWithKey,
        });
        playbackOffsetRef.current = sessionStart;
        sessionBufferEndRef.current = sessionStart;
        currentTimeRef.current = sessionStart;
        setCurrentTime(sessionStart);
        const pendingSeek = Math.max(0, safeTarget - sessionStart);
        pendingSessionSeekRef.current = pendingSeek > 0.5 ? pendingSeek : null;
        pendingSeekAttemptRef.current.attempts = 0;
        pendingSeekAttemptRef.current.lastAttemptMs = 0;
        console.log(
          '[player] warmStartHLS set playbackOffsetRef.current =',
          playbackOffsetRef.current,
          'pendingSeek=',
          pendingSessionSeekRef.current,
        );
        console.log('[player] warmStartHLS subtitle transition', {
          newSubtitleTimeOffset: -playbackOffsetRef.current,
          newPlaylistUrl: playlistWithKey.substring(0, 80),
          newSubtitleUrl: playlistWithKey.replace(/stream\.m3u8/, 'subtitles.vtt').substring(0, 80),
        });
        setCurrentMovieUrl(playlistWithKey);
        hasReceivedPlayerLoadRef.current = false;
        setHasStartedPlaying(false);

        if (typeof response.duration === 'number' && response.duration > 0) {
          updateDuration(response.duration, 'hls-session');
        }

        return true;
      } catch (error) {
        console.error('🚨 Failed to warm start HLS session', error);
        if (warmStartTokenRef.current === token) {
          setIsVideoBuffering(false);
        }
        return false;
      }
    },
    [
      effectiveMovie,
      forceAacFromRoute,
      isHlsStream,
      routeDvProfile,
      routeHasDolbyVision,
      routeHasHDR10,
      sourcePath,
      updateDuration,
      // Note: We use refs (selectedAudioTrackIndexRef, selectedSubtitleTrackIndexRef)
      // instead of direct values to avoid stale closure issues during seek
    ],
  );

  // Keep warmStartHlsSessionRef updated
  useEffect(() => {
    warmStartHlsSessionRef.current = warmStartHlsSession;
  }, [warmStartHlsSession]);

  const applyPendingSessionSeek = useCallback((reason: string) => {
    const pendingSeek = pendingSessionSeekRef.current;
    if (pendingSeek === null || pendingSeek < 0) {
      return false;
    }

    if (!hasAppliedInitialTracksRef.current) {
      console.log('[player] 🔍 SEEK BLOCKED: initial tracks not applied yet');
      return false;
    }

    const now = Date.now();
    if (now - pendingSeekAttemptRef.current.lastAttemptMs < 300) {
      return false;
    }

    const playerHandle = videoRef.current;
    if (!playerHandle || typeof playerHandle.seek !== 'function') {
      return false;
    }

    pendingSeekAttemptRef.current.lastAttemptMs = now;
    pendingSeekAttemptRef.current.attempts += 1;

    // For HLS streams, we need to seek using relative time (time since session start)
    // not absolute time. Convert absolute time to relative time.
    const relativeSeekTime = pendingSeek - playbackOffsetRef.current;

    try {
      console.log('[player] ✅ APPLYING PENDING SESSION SEEK', {
        absoluteTime: pendingSeek,
        playbackOffset: playbackOffsetRef.current,
        relativeTime: relativeSeekTime,
        reason,
        attempt: pendingSeekAttemptRef.current.attempts,
      });
      playerHandle.seek(relativeSeekTime);
      return true;
    } catch (error) {
      console.warn('[player] ❌ FAILED to apply pending session seek', error);
      return false;
    }
  }, []);

  // For non-HLS streams or HLS without DV, mark tracks as ready and apply pending seek
  useEffect(() => {
    if (!isHlsStream || !hasDolbyVision) {
      hasAppliedInitialTracksRef.current = true;
      console.log('[player] non-HLS or non-DV stream - tracks marked as ready');

      const pendingSeek = pendingSessionSeekRef.current;
      if (pendingSeek !== null && pendingSeek > 0) {
        console.log('[player] applying pending seek for non-HLS/non-DV stream', { pendingSeek });
        const timeoutId = setTimeout(() => {
          const applied = applyPendingSessionSeek('non-hls-initial');
          if (!applied) {
            console.log('[player] pending seek for non-HLS stream will retry via progress updates');
          }
        }, 200);

        return () => clearTimeout(timeoutId);
      }
    }
  }, [isHlsStream, hasDolbyVision, initialStartOffset, applyPendingSessionSeek]);

  // Debug: track last logged progress time for subtitle debugging
  const lastSubtitleDebugLogRef = useRef<{ time: number; logged: number; postSeekLogCount?: number }>({ time: 0, logged: 0 });

  // Debug: track progress event count for diagnosing start position issues
  const progressEventCountRef = useRef(0);
  const firstProgressValueRef = useRef<{ time: number; absoluteTime: number } | null>(null);

  const handleProgressUpdate = useCallback(
    (time: number, meta?: VideoProgressMeta) => {
      if (!Number.isFinite(time) || time < 0) {
        return;
      }

      const absoluteTime = playbackOffsetRef.current + time;
      currentTimeRef.current = absoluteTime;
      setCurrentTime(absoluteTime);

      // Debug: Log first 5 progress events to diagnose start position issues
      const eventCount = progressEventCountRef.current;
      progressEventCountRef.current = eventCount + 1;
      if (eventCount < 5) {
        if (eventCount === 0) {
          firstProgressValueRef.current = { time, absoluteTime };
          if (time > 1) {
            console.warn('[player] ⚠️ FIRST PROGRESS UPDATE HAS NON-ZERO TIME!', {
              playerTime: time,
              playbackOffset: playbackOffsetRef.current,
              absoluteTime,
              initialStartOffset,
              isHlsStream,
              isExistingHlsSession,
              pendingSeek: pendingSessionSeekRef.current,
            });
          }
        }
        console.log('[player] progress update #' + eventCount, {
          playerTime: time.toFixed(3),
          playbackOffset: playbackOffsetRef.current.toFixed(3),
          absoluteTime: absoluteTime.toFixed(3),
          firstProgressTime: firstProgressValueRef.current?.time.toFixed(3) ?? 'N/A',
          hasStartedPlaying,
          pendingSeek: pendingSessionSeekRef.current,
        });
      }

      // Debug logging for subtitle timing - log every 5 seconds or on significant time jumps
      const now = Date.now();
      const timeDiff = Math.abs(absoluteTime - lastSubtitleDebugLogRef.current.time);
      const isJump = timeDiff > 10;
      const shouldLog = (now - lastSubtitleDebugLogRef.current.logged) > 5000 || isJump;
      if (shouldLog && isHlsStream) {
        console.log('[player] progress update (subtitle debug)', {
          playerTime: time.toFixed(2),
          playbackOffset: playbackOffsetRef.current.toFixed(2),
          absoluteTime: absoluteTime.toFixed(2),
          timeDiff: isJump ? `JUMP ${timeDiff.toFixed(2)}s` : 'normal',
          subtitleTimeOffset: (-playbackOffsetRef.current).toFixed(2),
          expectedAdjustedTime: time.toFixed(2),
        });
        lastSubtitleDebugLogRef.current = { time: absoluteTime, logged: now };
      }
      // Extra logging for first 3 progress updates after a time jump (seek)
      if (isJump) {
        console.log('[player] SEEK DETECTED - next 3 progress updates will be logged for subtitle debugging');
        lastSubtitleDebugLogRef.current = { ...lastSubtitleDebugLogRef.current, postSeekLogCount: 3 };
      }
      const postSeekLogCount = lastSubtitleDebugLogRef.current.postSeekLogCount || 0;
      if (postSeekLogCount > 0 && isHlsStream) {
        console.log('[player] post-seek progress', {
          playerTime: time.toFixed(2),
          playbackOffset: playbackOffsetRef.current.toFixed(2),
          absoluteTime: absoluteTime.toFixed(2),
          subtitleTimeOffset: (-playbackOffsetRef.current).toFixed(2),
          expectedAdjustedTime: time.toFixed(2),
          remainingPostSeekLogs: postSeekLogCount - 1,
        });
        lastSubtitleDebugLogRef.current.postSeekLogCount = postSeekLogCount - 1;
      }

      // Don't apply pending seek here for initial load with DV/HLS - let the track effect handle it
      // This prevents the seek from being cleared before tracks are applied
      const pendingRelativeSeek = pendingSessionSeekRef.current;
      if (pendingRelativeSeek !== null) {
        if (hasAppliedInitialTracksRef.current) {
          const difference = Math.abs(time - pendingRelativeSeek);
          const waitingForPlayerLoad = isHlsStream && hasDolbyVision && !hasReceivedPlayerLoadRef.current;

          if (difference <= 0.25) {
            if (waitingForPlayerLoad) {
              console.log('[player] deferring pending seek clear until player load event confirms resume', {
                difference,
                pendingRelativeSeek,
              });
            } else {
              console.log('[player] clearing pending seek - already at target position', {
                difference,
                target: pendingRelativeSeek,
              });
              pendingSessionSeekRef.current = null;
              pendingSeekAttemptRef.current.attempts = 0;
              pendingSeekAttemptRef.current.lastAttemptMs = 0;
            }
          } else {
            applyPendingSessionSeek('progress-mismatch');
          }
        }
      }

      if (time > 0 && !hasStartedPlaying) {
        setHasStartedPlaying(true);

        // Resume playback if we paused for seeking or track switching
        if (pausedForSeekRef.current) {
          pausedForSeekRef.current = false;
          setPaused(false);
        }

        // Hide loading screen with a small delay to ensure player screen is fully visible
        setTimeout(() => {
          hideLoadingScreen();
        }, 100);
      }

      if (meta?.seekable) {
        const absoluteSeekable = playbackOffsetRef.current + meta.seekable;
        if (absoluteSeekable > sessionBufferEndRef.current) {
          sessionBufferEndRef.current = absoluteSeekable;
        }
        updateDuration(meta.seekable, 'progress.seekable');
      }
      if (meta?.playable) {
        const absolutePlayable = playbackOffsetRef.current + meta.playable;
        if (absolutePlayable > sessionBufferEndRef.current) {
          sessionBufferEndRef.current = absolutePlayable;
        }
        updateDuration(meta.playable, 'progress.playable');
      }

      // Report progress to the backend for tracking
      const currentDuration = durationRef.current;
      if (currentDuration > 0 && absoluteTime >= 0) {
        reportProgress(absoluteTime, currentDuration);
      }
    },
    [
      hasStartedPlaying,
      updateDuration,
      reportProgress,
      initialStartOffset,
      hasDolbyVision,
      isHlsStream,
      applyPendingSessionSeek,
      hideLoadingScreen,
    ],
  );

  const handleVideoLoad = useCallback(
    (value: number) => {
      if (!hasReceivedPlayerLoadRef.current) {
        hasReceivedPlayerLoadRef.current = true;
      }
      updateDuration(value, 'player.load');
      if (pendingSessionSeekRef.current !== null) {
        applyPendingSessionSeek('player-load');
      }
    },
    [applyPendingSessionSeek, updateDuration],
  );

  const handleBufferState = useCallback((isBuffering: boolean) => {
    setIsVideoBuffering(isBuffering);
  }, []);

  const hideControls = useCallback(
    (options: { immediate?: boolean } = {}) => {
      if (usesSystemManagedControls || !shouldAutoHideControls || isModalOpenRef.current) {
        return;
      }

      if (hideControlsTimeoutRef.current) {
        clearTimeout(hideControlsTimeoutRef.current);
        hideControlsTimeoutRef.current = null;
      }

      const finalizeHide = () => {
        setControlsVisible(false);
        controlsVisibleRef.current = false; // Update ref synchronously for event handlers
      };

      if (options.immediate) {
        controlsOpacity.stopAnimation();
        controlsOpacity.setValue(0);
        finalizeHide();
        return;
      }

      Animated.timing(controlsOpacity, {
        toValue: 0,
        duration: isTvPlatform ? 600 : 300,
        useNativeDriver: canUseNativeDriver,
      }).start(() => {
        finalizeHide();
      });
    },
    [canUseNativeDriver, controlsOpacity, shouldAutoHideControls, usesSystemManagedControls],
  );

  // Keep hideControlsRef updated
  useEffect(() => {
    hideControlsRef.current = hideControls;
  }, [hideControls]);

  useEffect(() => {
    if (!isTvPlatform || usesSystemManagedControls || !controlsVisible || isModalOpen) {
      return;
    }

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      hideControls();
      return true;
    });

    return () => {
      removeInterceptor();
    };
  }, [controlsVisible, hideControls, isModalOpen, isTvPlatform, usesSystemManagedControls]);

  useEffect(() => {
    if (!isTvPlatform || usesSystemManagedControls) {
      return;
    }

    const subscription = BackHandler.addEventListener('hardwareBackPress', () => {
      if (controlsVisible && !isModalOpen) {
        hideControls();
        return true;
      }
      return false;
    });

    return () => {
      subscription.remove();
    };
  }, [controlsVisible, hideControls, isModalOpen, isTvPlatform, usesSystemManagedControls]);

  // Cleanup seek indicator timeout on unmount
  useEffect(() => {
    return () => {
      if (seekIndicatorTimeoutRef.current) {
        clearTimeout(seekIndicatorTimeoutRef.current);
      }
    };
  }, []);

  // Check if we should set next episode on unmount (when exiting player)
  useEffect(() => {
    return () => {
      // On unmount, check if we should advance to the next episode
      if (mediaType === 'episode' && seasonNumber && episodeNumber && titleId) {
        const currentPosition = currentTimeRef.current;
        const duration = durationRef.current;

        console.log('[player] Unmounting - checking if should set next episode', {
          mediaType,
          seasonNumber,
          episodeNumber,
          titleId,
          currentPosition,
          duration,
          percentWatched: duration > 0 ? ((currentPosition / duration) * 100).toFixed(2) + '%' : 'N/A',
        });

        if (duration > 0) {
          const percentWatched = (currentPosition / duration) * 100;

          // If watched >= 90%, set the next episode
          if (percentWatched >= 90) {
            const nextEpisode = episodeNumber + 1;
            console.log('[player] ✅ Unmounting at >=90% watched, setting next episode:', {
              currentPosition,
              duration,
              percentWatched: percentWatched.toFixed(2) + '%',
              seasonNumber,
              currentEpisode: episodeNumber,
              nextEpisode,
              titleId,
            });
            playbackNavigation.setNextEpisode(titleId, seasonNumber, nextEpisode);
          } else {
            console.log('[player] ❌ Not setting next episode on unmount - watched less than 90%', {
              percentWatched: percentWatched.toFixed(2) + '%',
            });
          }
        } else {
          console.log('[player] ⚠️ Cannot determine watch percentage on unmount - invalid duration');
        }
      }
    };
  }, [mediaType, seasonNumber, episodeNumber, titleId]);

  // TODO: Menu button handling in player is still broken - menu button doesn't properly
  // hide controls or exit player. TVMenuControl enable/disable logic needs investigation.
  useEffect(() => {
    if (!isTvPlatform || typeof TVMenuControl?.enableTVMenuKey !== 'function') {
      return;
    }

    try {
      // When controls are visible or modal is open, we want to handle the menu button ourselves
      // to hide controls/modal instead of exiting. enableTVMenuKey() adds the gesture recognizer
      // so the app receives menu events. disableTVMenuKey() removes it so system handles (exits).
      if (controlsVisible || isModalOpen) {
        console.log('[player] TVMenuControl: enabling menu key (controls visible or modal open)');
        TVMenuControl.enableTVMenuKey();
      } else {
        console.log('[player] TVMenuControl: disabling menu key (controls hidden)');
        if (typeof TVMenuControl.disableTVMenuKey === 'function') {
          TVMenuControl.disableTVMenuKey();
        }
      }
    } catch (error) {
      console.warn('[player] Failed to toggle TV menu key', error);
    }

    return () => {
      try {
        TVMenuControl?.enableTVMenuKey?.();
      } catch (error) {
        console.warn('[player] Failed to re-enable TV menu key', error);
      }
    };
  }, [controlsVisible, isModalOpen, isTvPlatform]);

  const showControls = useCallback(() => {
    if (usesSystemManagedControls) {
      return;
    }

    controlsOpacity.stopAnimation();
    setControlsVisible(true);
    controlsVisibleRef.current = true; // Update ref synchronously for event handlers
    Animated.timing(controlsOpacity, {
      toValue: 1,
      duration: isTvPlatform ? 350 : 300,
      useNativeDriver: canUseNativeDriver,
    }).start();

    if (hideControlsTimeoutRef.current) {
      clearTimeout(hideControlsTimeoutRef.current);
      hideControlsTimeoutRef.current = null;
    }

    // Don't set hide timeout if auto-hide is disabled, a modal is open, the user is actively scrubbing, or filename is displayed
    if (shouldAutoHideControls && !isModalOpenRef.current && !isSeekingRef.current && !isFilenameDisplayed) {
      hideControlsTimeoutRef.current = setTimeout(() => {
        hideControls();
      }, autoHideDurationMs);
    }
  }, [
    canUseNativeDriver,
    controlsOpacity,
    hideControls,
    usesSystemManagedControls,
    isModalOpen,
    isFilenameDisplayed,
    shouldAutoHideControls,
    autoHideDurationMs,
  ]);

  // Keep showControlsRef updated
  useEffect(() => {
    showControlsRef.current = showControls;
  }, [showControls]);

  // Keep controlsVisibleRef in sync for use in event handlers (avoids stale closure)
  useEffect(() => {
    controlsVisibleRef.current = controlsVisible;
  }, [controlsVisible]);

  useEffect(() => {
    if (isModalOpen) {
      showControls();
    }
  }, [isModalOpen, showControls]);

  const handleSeekBarScrubStart = useCallback(() => {
    if (usesSystemManagedControls) {
      return;
    }
    setIsSeeking(true);
    showControls();
  }, [usesSystemManagedControls, showControls]);

  const handleSeekBarScrubEnd = useCallback(() => {
    if (usesSystemManagedControls) {
      return;
    }
    setIsSeeking(false);
    showControls();
  }, [usesSystemManagedControls, showControls]);

  const handleVideoInteract = useCallback(() => {
    if (usesSystemManagedControls) {
      return;
    }

    if (isTouchOverlayToggleSupported && controlsVisible) {
      hideControls();
      return;
    }

    showControls();
  }, [controlsVisible, hideControls, isTouchOverlayToggleSupported, showControls, usesSystemManagedControls]);

  const seek = useCallback(
    (rawTime: number, shouldShowControls: boolean = true) => {
      void (async () => {
        const numericTime = Number(rawTime);
        if (!Number.isFinite(numericTime)) {
          return;
        }

        let time = numericTime;
        const currentDuration = durationRef.current;
        if (Number.isFinite(currentDuration) && currentDuration > 0) {
          time = Math.min(Math.max(time, 0), currentDuration);
        } else if (time < 0) {
          time = 0;
        }

        console.log('[player] seek requested', {
          time,
          duration: durationRef.current,
          currentTime: currentTimeRef.current,
          playbackOffset: playbackOffsetRef.current,
          sessionBufferEnd: sessionBufferEndRef.current,
          currentSubtitleTimeOffset: -playbackOffsetRef.current,
          newSubtitleTimeOffset: -time,
        });

        let performed = false;

        if (isHlsStream && sourcePath) {
          const sessionStart = playbackOffsetRef.current;
          const sessionEnd = sessionBufferEndRef.current;
          const relativeTime = time - sessionStart;
          const bufferPadding = 0.5;
          const hasBufferedWindow = sessionEnd > sessionStart;
          const withinSession = hasBufferedWindow && relativeTime >= 0 && time <= sessionEnd - bufferPadding;

          if (!withinSession) {
            // Pause playback and update seek bar to target position immediately
            setPaused(true);
            pausedForSeekRef.current = true;
            currentTimeRef.current = time;
            setCurrentTime(time);

            // Cancel any pending debounced session creation
            if (warmStartDebounceRef.current) {
              clearTimeout(warmStartDebounceRef.current);
              warmStartDebounceRef.current = null;
              // Resolve the previous promise as false since we're cancelling it
              if (warmStartDebounceResolveRef.current) {
                warmStartDebounceResolveRef.current(false);
                warmStartDebounceResolveRef.current = null;
              }
            }

            // Debounce the session creation to prevent multiple sessions from rapid seeking
            const warmed = await new Promise<boolean>((resolve) => {
              warmStartDebounceResolveRef.current = resolve;
              warmStartDebounceRef.current = setTimeout(async () => {
                warmStartDebounceRef.current = null;
                warmStartDebounceResolveRef.current = null;
                console.log('[player] debounced warmStartHlsSession executing', { time });
                const result = await warmStartHlsSession(time);
                resolve(result);
              }, 1000);
            });

            if (warmed) {
              performed = true;
            } else {
              const fallbackRelative = Math.max(0, time - playbackOffsetRef.current);
              try {
                console.log('[player] fallback seek within current session', { fallbackRelative });
                videoRef.current?.seek(fallbackRelative);
                performed = true;
              } catch (error) {
                console.warn('[player] fallback seek failed', error);
              }
            }
          } else {
            try {
              const relative = Math.max(0, relativeTime);
              console.log('[player] calling videoRef.seek', {
                time: relative,
                hasVideoRef: !!videoRef.current,
                offset: sessionStart,
              });
              videoRef.current?.seek(relative);
              performed = true;
            } catch (error) {
              console.warn('[player] unable to seek within current HLS session', error);
            }
          }
        } else {
          try {
            console.log('[player] calling videoRef.seek', { time, hasVideoRef: !!videoRef.current });
            videoRef.current?.seek(time);
            performed = true;
          } catch (error) {
            console.warn('[player] seek failed', error);
          }
        }

        if (performed) {
          currentTimeRef.current = time;
          setCurrentTime(time);
        }
        if (shouldShowControls) {
          showControls();
        }
      })();
    },
    [isHlsStream, showControls, sourcePath, warmStartHlsSession],
  );

  // Keep seekRef updated
  useEffect(() => {
    seekRef.current = seek;
  }, [seek]);

  const handleAutoplayBlocked = useCallback(() => {
    console.warn('[player] autoplay blocked by browser policy; awaiting user action');
    if (!usesSystemManagedControls) {
      setPaused(true);
      showControls();
    }
  }, [usesSystemManagedControls, showControls]);

  const handleVolumeChange = useCallback(
    (nextValue: number) => {
      const clamped = Math.min(Math.max(Number(nextValue) || 0, 0), 1);
      setVolume(clamped);
      showControls();
    },
    [showControls],
  );

  const togglePausePlay = () => {
    if (usesSystemManagedControls) {
      try {
        const result = videoRef.current?.play?.();
        if (result && typeof (result as unknown as Promise<void>).catch === 'function') {
          (result as unknown as Promise<void>).catch((error) => {
            console.warn('[player] native play toggle failed', error);
          });
        }
      } catch (error) {
        console.warn('[player] unable to toggle native playback', error);
      }
      return;
    }

    setPaused((previousPaused) => {
      const nextPaused = !previousPaused;

      if (nextPaused) {
        try {
          videoRef.current?.pause?.();
        } catch (error) {
          console.warn('[player] unable to trigger manual pause from toggle', error);
        }
      } else if (Platform.OS === 'web') {
        // Immediate manual play keeps us inside the original gesture on iOS Safari.
        try {
          const result = videoRef.current?.play?.();
          if (result && typeof (result as unknown as Promise<void>).catch === 'function') {
            (result as unknown as Promise<void>).catch((error) => {
              console.warn('[player] manual play from toggle failed', error);
            });
          }
        } catch (error) {
          console.warn('[player] unable to trigger manual play from toggle', error);
        }
      } else {
        try {
          videoRef.current?.play?.();
        } catch (error) {
          console.warn('[player] unable to trigger manual play from toggle', error);
        }
      }

      return nextPaused;
    });

    showControls();
  };

  // Keep togglePausePlayRef updated
  useEffect(() => {
    togglePausePlayRef.current = togglePausePlay;
  });

  const toggleFullscreen = () => {
    if (Platform.OS === 'web') {
      try {
        const doc: any = document as any;
        const root: any = document.getElementById('player-fullscreen-root');
        if (!doc.fullscreenElement) {
          root?.requestFullscreen?.().catch(() => {});
        } else {
          doc.exitFullscreen?.().catch(() => {});
        }
      } catch {}
    } else {
      videoRef.current?.toggleFullscreen?.();
    }
    showControls();
  };

  const handleSkipBackward = useCallback(() => {
    const targetTime = Math.max(0, currentTimeRef.current - 30);
    seek(targetTime);
  }, [seek]);

  const handleSkipForward = useCallback(() => {
    const targetTime = currentTimeRef.current + 30;
    seek(targetTime);
  }, [seek]);

  useEffect(() => {
    if (!usesSystemManagedControls) {
      showControls();
    }
  }, [usesSystemManagedControls, showControls]);

  useEffect(() => {
    if (!shouldAutoHideControls) {
      return;
    }

    if (isModalOpen || isSeeking || isFilenameDisplayed) {
      // Pause auto-hide while a modal is open, the user is actively scrubbing, or filename is displayed
      if (hideControlsTimeoutRef.current) {
        clearTimeout(hideControlsTimeoutRef.current);
        hideControlsTimeoutRef.current = null;
      }
      return;
    }

    if (controlsVisible) {
      // Resume hide timer once interaction completes
      showControls();
    }
  }, [isModalOpen, isSeeking, isFilenameDisplayed, controlsVisible, showControls, shouldAutoHideControls]);

  useEffect(() => {
    hasAutoLaunchedSystemPlayerRef.current = false;
  }, [resolvedMovie]);

  useEffect(() => {
    if (!shouldPreferSystemPlayer) {
      hasAutoLaunchedSystemPlayerRef.current = false;
    }
  }, [shouldPreferSystemPlayer]);

  useEffect(() => {
    if (!shouldPreferSystemPlayer) {
      return;
    }

    if (Platform.OS !== 'ios') {
      return;
    }

    if (!usesSystemManagedControls) {
      return;
    }

    if (hasAutoLaunchedSystemPlayerRef.current) {
      return;
    }

    const timeout = setTimeout(() => {
      const player = videoRef.current;
      if (!player) {
        return;
      }

      try {
        player.play?.();
      } catch (error) {
        console.warn('[player] unable to trigger native player autoplay', error);
      }

      try {
        player.toggleFullscreen?.();
        hasAutoLaunchedSystemPlayerRef.current = true;
      } catch (error) {
        console.warn('[player] unable to open native fullscreen player', error);
      }
    }, 250);

    return () => {
      clearTimeout(timeout);
    };
  }, [shouldPreferSystemPlayer, usesSystemManagedControls]);

  const handleVideoError = useCallback(
    async (error: any) => {
      console.error('🚨 PlayerScreen - Video Error:', error);
      console.error('🚨 PlayerScreen - Error details:', JSON.stringify(error, null, 2));

      // Check if this is a CoreMedia error that might be caused by DV fallback
      // Error -19601 (kCMSampleBufferError_RequiredParameterMissing) occurs when
      // the player has stale init segment data after server-side DV fallback
      const errorCode = error?.error?.code;
      const errorDomain = error?.error?.domain;
      const isCoreMediaError = errorDomain === 'CoreMediaErrorDomain' && errorCode === -19601;

      // Only retry for HLS streams, CoreMedia errors, and if we haven't retried too much
      const maxRetries = 2;
      const canRetry =
        isCoreMediaError &&
        isHlsStream &&
        initialSourcePath &&
        hlsSessionRetryCountRef.current < maxRetries &&
        !isRetryingHlsSessionRef.current;

      if (canRetry) {
        hlsSessionRetryCountRef.current += 1;
        isRetryingHlsSessionRef.current = true;

        console.log(
          `[player] CoreMedia error detected, retrying HLS session (attempt ${hlsSessionRetryCountRef.current}/${maxRetries})`,
        );

        try {
          // Create a fresh HLS session - this will get a new session ID
          // The server may have restarted FFmpeg with different codec parameters (DV fallback)
          const response = await apiService.createHlsSession({
            path: initialSourcePath,
            dv: routeHasDolbyVision,
            dvProfile: routeDvProfile || undefined,
            hdr: routeHasHDR10,
            forceAAC: forceAacFromRoute,
            start: currentTimeRef.current, // Resume from current position
            audioTrack: selectedAudioTrackIndexRef.current ?? undefined,
            subtitleTrack: selectedSubtitleTrackIndexRef.current ?? undefined,
          });

          // Store session ID for keepalive pings when paused
          hlsSessionIdRef.current = response.sessionId;

          const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
          const playlistBase = `${baseUrl}${response.playlistUrl}`;
          const authKey = apiService.getApiKey().trim();
          const playlistWithKey = authKey
            ? `${playlistBase}${playlistBase.includes('?') ? '&' : '?'}apiKey=${encodeURIComponent(authKey)}`
            : playlistBase;

          console.log('[player] created new HLS session for retry', {
            playlistUrl: playlistWithKey,
            startOffset: response.startOffset,
          });

          // Update the video source to the new session
          setCurrentMovieUrl(playlistWithKey);

          // Update playback offset if the session has a start offset
          if (typeof response.startOffset === 'number' && response.startOffset >= 0) {
            playbackOffsetRef.current = response.startOffset;
            sessionBufferEndRef.current = response.startOffset;
          }
          return; // Successfully created retry session
        } catch (retryError) {
          console.error('[player] failed to create retry HLS session:', retryError);
          // Fall through to show error toast
        } finally {
          isRetryingHlsSessionRef.current = false;
        }
      }

      // If we can't retry or retry failed, show error toast and navigate back
      const errorMessage =
        error?.error?.localizedDescription ||
        error?.error?.localizedFailureReason ||
        error?.message ||
        'An error occurred during playback';
      showToast(`Playback error: ${errorMessage}`, { tone: 'danger', duration: 5000 });
      router.back();
    },
    [isHlsStream, initialSourcePath, routeHasDolbyVision, routeDvProfile, routeHasHDR10, forceAacFromRoute, showToast],
  );

  const handleVideoEnd = useCallback(() => {
    console.log('🎬 Video playback ended', {
      mediaType,
      seasonNumber,
      episodeNumber,
      titleId,
    });

    // Only auto-navigate for TV show episodes
    if (mediaType === 'episode' && seasonNumber && episodeNumber) {
      // Calculate the next episode
      const nextEpisode = episodeNumber + 1;

      console.log('🎬 Navigating to details page for next episode', {
        seasonNumber,
        nextEpisode,
      });

      // Store the next episode info so the details page can pick it up
      if (titleId) {
        playbackNavigation.setNextEpisode(titleId, seasonNumber, nextEpisode);
      }

      // Add a small delay to give user feedback that playback completed
      setTimeout(() => {
        // Navigate back - this will close the player and return to the details page
        // The details page will handle showing the next episode via its own logic
        router.back();
      }, 1000); // 1 second delay
    } else {
      // For movies, just go back to the previous screen
      setTimeout(() => {
        router.back();
      }, 1000);
    }
  }, [
    mediaType,
    seasonNumber,
    episodeNumber,
    router,
    cleanSeriesTitle,
    title,
    titleId,
    headerImage,
    imdbId,
    tvdbId,
    yearParam,
  ]);

  const handleTracksAvailable = useCallback(
    (audioTracks: TrackInfo[], subtitleTracks: TrackInfo[]) => {
      console.log('[player] player tracks available (VLC/Expo)', { audioTracks, subtitleTracks });

      // Build track options from player-reported tracks, filtering out "Disable" options
      const playerAudioOptions: TrackOption[] = audioTracks
        .filter((track) => track.id !== -1 && track.name?.toLowerCase() !== 'disable')
        .map((track) => ({
          id: String(track.id),
          label: track.name || `Audio ${track.id}`,
        }));

      const playerSubtitleOptions: TrackOption[] = subtitleTracks
        .filter((track) => track.id !== -1 && track.name?.toLowerCase() !== 'disable')
        .map((track) => ({
          id: String(track.id),
          label: track.name || `Subtitle ${track.id}`,
        }));

      // Only update if we don't already have metadata-based tracks
      if (audioTrackOptions.length === 0 && playerAudioOptions.length > 0) {
        setAudioTrackOptions(playerAudioOptions);
        // TODO: Add settings for default language selection
        // For now, default to first available audio track
        setSelectedAudioTrackId(playerAudioOptions[0]?.id ?? null);
      }

      if (subtitleTrackOptions.length <= 1 && playerSubtitleOptions.length > 0) {
        const mergedSubtitles = [SUBTITLE_OFF_OPTION, ...playerSubtitleOptions];
        setSubtitleTrackOptions(mergedSubtitles);
        // TODO: Add settings for default language selection
        // For now, default to first available subtitle track
        const firstSubtitleId = playerSubtitleOptions[0]?.id ?? SUBTITLE_OFF_OPTION.id;
        setSelectedSubtitleTrackId(firstSubtitleId);
      }
    },
    [audioTrackOptions.length, selectedSubtitleTrackId, subtitleTrackOptions.length],
  );

  const selectedAudioTrackIndex = useMemo(() => {
    if (!selectedAudioTrackId || selectedAudioTrackId === 'off') {
      return null;
    }
    const parsed = Number(selectedAudioTrackId);
    const result = Number.isFinite(parsed) ? parsed : null;
    console.log('[player] selectedAudioTrackIndex computed', {
      selectedAudioTrackId,
      parsed,
      result,
    });
    return result;
  }, [selectedAudioTrackId]);

  const selectedSubtitleTrackIndex = useMemo(() => {
    if (!selectedSubtitleTrackId || selectedSubtitleTrackId === 'off') {
      console.log('[player] selectedSubtitleTrackIndex computed (off)', {
        selectedSubtitleTrackId,
        currentTime: currentTimeRef.current,
        isHlsStream,
      });
      return null;
    }
    const parsed = Number(selectedSubtitleTrackId);
    const result = Number.isFinite(parsed) ? parsed : null;
    console.log('[player] selectedSubtitleTrackIndex computed', {
      selectedSubtitleTrackId,
      parsed,
      result,
      currentTime: currentTimeRef.current,
      isHlsStream,
    });
    return result;
  }, [selectedSubtitleTrackId, isHlsStream]);

  // Keep refs in sync with the computed track indices for use in callbacks
  // This ensures warmStartHlsSession always has the current values
  const selectedAudioTrackIndexRef = useRef<number | null>(null);
  const selectedSubtitleTrackIndexRef = useRef<number | null>(null);

  useEffect(() => {
    selectedAudioTrackIndexRef.current = selectedAudioTrackIndex;
  }, [selectedAudioTrackIndex]);

  useEffect(() => {
    selectedSubtitleTrackIndexRef.current = selectedSubtitleTrackIndex;
  }, [selectedSubtitleTrackIndex]);

  // Recreate HLS session when audio/subtitle tracks change for HLS streams
  const lastHlsTrackSelectionRef = useRef<{ audio: number | null; subtitle: number | null }>({
    audio: null,
    subtitle: null,
  });

  useEffect(() => {
    // Only recreate session for HLS streams (HDR/DV content)
    // Use routeHasDolbyVision (from route params) since hasDolbyVision state is currently disabled
    if (!isHlsStream || !sourcePath || !routeHasDolbyVision) {
      return;
    }

    // Don't recreate if no tracks are selected yet (initial load or before metadata)
    if (audioTrackOptions.length === 0 && subtitleTrackOptions.length <= 1) {
      return;
    }

    // Check if this is the first time we're setting tracks (after metadata loads)
    const isFirstSet =
      lastHlsTrackSelectionRef.current.audio === null && lastHlsTrackSelectionRef.current.subtitle === null;

    // Track whether we need to force a session recreation (e.g., warm start happened before track info was available)
    let forceRecreate = false;

    if (isFirstSet) {
      // Check if warm start already created a session before we had track info
      // If so, we need to recreate the session with the correct tracks
      const warmStartHappenedWithoutTracks = hasAttemptedInitialWarmStartRef.current;
      const hasPreferredTracks = selectedAudioTrackIndex !== null || selectedSubtitleTrackIndex !== null;

      if (warmStartHappenedWithoutTracks && hasPreferredTracks) {
        console.log('[player] warm start happened before track info - will recreate session with correct tracks', {
          audioTrack: selectedAudioTrackIndex,
          subtitleTrack: selectedSubtitleTrackIndex,
        });
        forceRecreate = true;
        // Don't return - fall through to recreation logic
      } else {
        // No warm start happened or no tracks selected - just initialize the ref
        lastHlsTrackSelectionRef.current = { audio: selectedAudioTrackIndex, subtitle: selectedSubtitleTrackIndex };
        console.log('[player] initialized track selection ref for HLS stream', {
          audioTrack: selectedAudioTrackIndex,
          subtitleTrack: selectedSubtitleTrackIndex,
        });

        // Mark that initial tracks have been applied and trigger pending seek
        // Wait a delay to ensure the Expo player has actually applied the tracks
        setTimeout(() => {
          hasAppliedInitialTracksRef.current = true;
          console.log('[player] marked initial tracks as applied');

          // Apply any pending seek now that tracks are ready
          // For HLS, wait a bit longer to ensure the player has started playing
          const pendingSeek = pendingSessionSeekRef.current;
          if (pendingSeek !== null && pendingSeek > 0) {
            const seekDelay = isHlsStream ? 1000 : 0; // Wait 1 second for HLS to start playing
            console.log('[player] will apply pending session seek after delay', {
              pendingSeek,
              seekDelay,
              isHls: isHlsStream,
            });

            setTimeout(() => {
              const applied = applyPendingSessionSeek('tracks-applied-delayed');
              if (!applied) {
                console.log('[player] pending session seek not applied yet - will retry on progress updates');
              }
            }, seekDelay);
          }
        }, 500); // Initial delay to give Expo player time to apply tracks
        return;
      }
    }

    // Check if tracks actually changed (skip check if we're forcing recreation)
    if (!forceRecreate) {
      const audioChanged = lastHlsTrackSelectionRef.current.audio !== selectedAudioTrackIndex;
      const subtitleChanged = lastHlsTrackSelectionRef.current.subtitle !== selectedSubtitleTrackIndex;

      if (!audioChanged && !subtitleChanged) {
        return;
      }

      // If only subtitle changed to disabled (null), don't recreate session
      // The sidecar VTT overlay will just stop rendering - no need for new HLS session
      if (!audioChanged && subtitleChanged && selectedSubtitleTrackIndex === null) {
        console.log('[player] subtitle disabled - skipping session recreation (sidecar overlay will hide)');
        lastHlsTrackSelectionRef.current = { audio: selectedAudioTrackIndex, subtitle: selectedSubtitleTrackIndex };
        return;
      }
    }

    // Update reference
    lastHlsTrackSelectionRef.current = { audio: selectedAudioTrackIndex, subtitle: selectedSubtitleTrackIndex };

    // Recreate HLS session with new track selection
    console.log('[player] track selection changed for HLS stream, recreating session', {
      audioTrack: selectedAudioTrackIndex,
      subtitleTrack: selectedSubtitleTrackIndex,
      currentTime: currentTimeRef.current,
    });

    const recreateSession = async () => {
      const currentPlaybackTime = currentTimeRef.current;
      const safeTarget = Math.max(0, Number(currentPlaybackTime) || 0);
      const trimmedPath = sourcePath.trim();

      if (!trimmedPath) {
        console.warn('[player] cannot recreate HLS session: no source path');
        return;
      }

      // Pause playback while recreating HLS session (same behavior as seeking)
      setPaused(true);
      pausedForSeekRef.current = true;
      setIsVideoBuffering(true);

      try {
        console.log('[player] creating NEW HLS session with selected tracks', {
          audioTrack: selectedAudioTrackIndex,
          subtitleTrack: selectedSubtitleTrackIndex,
          audioTrackType: typeof selectedAudioTrackIndex,
          subtitleTrackType: typeof selectedSubtitleTrackIndex,
        });

        const response = await apiService.createHlsSession({
          path: trimmedPath,
          dv: routeHasDolbyVision,
          dvProfile: routeDvProfile || undefined,
          hdr: routeHasHDR10,
          forceAAC: forceAacFromRoute,
          start: safeTarget,
          audioTrack: selectedAudioTrackIndex ?? undefined,
          subtitleTrack: selectedSubtitleTrackIndex ?? undefined,
        });

        // Store session ID for keepalive pings when paused
        hlsSessionIdRef.current = response.sessionId;

        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const playlistBase = `${baseUrl}${response.playlistUrl}`;
        const authKey = apiService.getApiKey().trim();
        const playlistWithKey = authKey
          ? `${playlistBase}${playlistBase.includes('?') ? '&' : '?'}apiKey=${encodeURIComponent(authKey)}`
          : playlistBase;

        const sessionStart =
          typeof response.startOffset === 'number' && response.startOffset >= 0 ? response.startOffset : safeTarget;

        playbackOffsetRef.current = sessionStart;
        sessionBufferEndRef.current = sessionStart;
        currentTimeRef.current = sessionStart;
        setCurrentTime(sessionStart);
        setCurrentMovieUrl(playlistWithKey);
        hasReceivedPlayerLoadRef.current = false;
        setHasStartedPlaying(false);

        if (typeof response.duration === 'number' && response.duration > 0) {
          updateDuration(response.duration, 'hls-session');
        }

        console.log('[player] successfully recreated HLS session with new track selection', {
          previousPlaybackTime: currentPlaybackTime,
          safeTarget,
          sessionStart,
          responseStartOffset: response.startOffset,
          newPlaylistUrl: playlistWithKey.substring(0, 100),
          subtitleTrackIndex: selectedSubtitleTrackIndex,
          willUseNewVttUrl: true,
        });

        // Mark initial tracks as applied after recreation (important for forceRecreate case)
        hasAppliedInitialTracksRef.current = true;
      } catch (error) {
        console.error('🚨 Failed to recreate HLS session with track selection', error);
        setIsVideoBuffering(false);
        // Resume playback if we paused for track change
        if (pausedForSeekRef.current) {
          pausedForSeekRef.current = false;
          setPaused(false);
        }
      }
    };

    recreateSession();
  }, [
    selectedAudioTrackIndex,
    selectedSubtitleTrackIndex,
    isHlsStream,
    sourcePath,
    routeHasDolbyVision,
    routeHasHDR10,
    audioTrackOptions.length,
    subtitleTrackOptions.length,
    routeDvProfile,
    forceAacFromRoute,
    updateDuration,
    applyPendingSessionSeek,
  ]);

  const styles = useMemo(() => createPlayerStyles(theme), [theme]);
  const ControlsContainerComponent = isTvPlatform ? View : Animated.View;
  const controlsContainerStyle = isTvPlatform
    ? styles.controlsContainer
    : [styles.controlsContainer, { opacity: controlsOpacity }];
  const tvOverlayAnimatedStyle = useMemo(
    () => [{ opacity: controlsOpacity }, styles.overlayAnimatedWrapper],
    [controlsOpacity],
  );

  useEffect(() => {
    console.log('[player] resetting state for new movie', resolvedMovie);
    durationRef.current = 0;
    setDuration(0);
    hasReceivedPlayerLoadRef.current = false;

    // Use parsedDurationHint if available from HLS session
    if (parsedDurationHint && parsedDurationHint > 0) {
      console.log('[player] using duration hint from URL params:', parsedDurationHint);
      setMetadataDuration(parsedDurationHint);
      updateDuration(parsedDurationHint, 'url-param-hint');
    } else {
      setMetadataDuration(0);
    }

    setAudioTrackOptions([]);
    setSubtitleTrackOptions([SUBTITLE_OFF_OPTION]);
    setSelectedAudioTrackId(null);
    setSelectedSubtitleTrackId(SUBTITLE_OFF_OPTION.id);
    setHasStartedPlaying(false);
    if (usesSystemManagedControls) {
      setPaused(false);
    }
  }, [usesSystemManagedControls, resolvedMovie, parsedDurationHint, updateDuration]);

  useEffect(() => {
    let isMounted = true;

    const fetchMetadata = async () => {
      try {
        if (!resolvedMovie) {
          console.log('[player] metadata fetch skipped: missing movie url');
          return;
        }

        const url = new URL(resolvedMovie);
        const movieApiKey = url.searchParams.get('apiKey');
        if (movieApiKey && !apiService.getApiKey().trim()) {
          console.log('[player] hydrating api key from playback url');
          apiService.setApiKey(movieApiKey);
        }

        // Extract path from either query param or WebDAV URL
        let pathParam = url.searchParams.get('path');
        if (!pathParam) {
          // Try to extract from WebDAV URL (e.g., http://user:pass@host/webdav/path/to/file.mkv)
          if (url.pathname && url.pathname.startsWith('/webdav/')) {
            pathParam = url.pathname;
            console.log('[player] extracted path from WebDAV URL:', pathParam);
          } else if (url.pathname && url.pathname.includes('/video/hls/')) {
            // HLS URL - use sourcePath from route params if available
            if (sourcePath) {
              console.log('[player] HLS URL detected - fetching metadata from sourcePath for track info', sourcePath);
              pathParam = sourcePath;
            } else {
              console.log(
                '[player] HLS URL detected - no sourcePath available, skipping metadata fetch',
                resolvedMovie,
              );
              // TEMPORARILY DISABLED - testing VLC for all content
              // setHasDolbyVision(true);
              return;
            }
          } else {
            console.log('[player] metadata fetch skipped: no path param in url', resolvedMovie);
            return;
          }
        }

        console.log('[player] fetching metadata for', pathParam);
        setSourcePath(pathParam);
        const metadata = await apiService.getVideoMetadata(pathParam);
        if (!isMounted) {
          return;
        }

        // Detect Dolby Vision content
        // TEMPORARILY DISABLED - testing VLC for all content
        // const isDV = detectDolbyVision(metadata);
        // setHasDolbyVision(isDV);
        // if (isDV) {
        //   console.log('🎬 Dolby Vision detected - using Expo player for DV support', {
        //     videoStreams: metadata.videoStreams,
        //     primaryStream: metadata.videoStreams?.[0],
        //   });
        // }

        // Extract video color metadata for HDR info display
        if (metadata.videoStreams && metadata.videoStreams.length > 0) {
          const primaryVideo = metadata.videoStreams[0];
          const isPQ = primaryVideo.colorTransfer === 'smpte2084';
          const isBT2020 = primaryVideo.colorPrimaries === 'bt2020';
          const isHDR10 = (isPQ && isBT2020) || primaryVideo.hdrFormat === 'HDR10' || primaryVideo.hdrFormat === 'HDR10+';

          setVideoColorInfo({
            colorTransfer: primaryVideo.colorTransfer,
            colorPrimaries: primaryVideo.colorPrimaries,
            colorSpace: primaryVideo.colorSpace,
            isHDR10,
          });
          console.log('[player] video color metadata', {
            colorTransfer: primaryVideo.colorTransfer,
            colorPrimaries: primaryVideo.colorPrimaries,
            colorSpace: primaryVideo.colorSpace,
            isHDR10,
            hasDolbyVision: primaryVideo.hasDolbyVision,
            dolbyVisionProfile: primaryVideo.dolbyVisionProfile,
          });
        }

        const fullDuration = Number(metadata.durationSeconds) || 0;
        if (fullDuration > 0) {
          console.log('[player] metadata duration', fullDuration);
          setMetadataDuration(fullDuration);
          updateDuration(fullDuration, 'metadata');
        } else {
          console.log('[player] metadata duration unavailable', metadata);
        }

        const audioOptions = buildAudioTrackOptions(metadata.audioStreams ?? []);
        console.log('[player] built audio track options from metadata', {
          audioStreamsCount: metadata.audioStreams?.length ?? 0,
          audioOptionsCount: audioOptions.length,
          audioOptions,
        });
        setAudioTrackOptions(audioOptions);

        // Check for user preference first, then fall back to metadata selection
        let selectedAudioIndex = metadata.selectedAudioIndex;
        const preferredAudioLanguage = userSettings?.playback?.preferredAudioLanguage ?? settings?.playback?.preferredAudioLanguage;
        if (preferredAudioLanguage) {
          const preferredAudioIndex = findAudioTrackByLanguage(metadata.audioStreams ?? [], preferredAudioLanguage);
          if (preferredAudioIndex !== null) {
            selectedAudioIndex = preferredAudioIndex;
            console.log('[player] using preferred audio language', {
              preferredLanguage: preferredAudioLanguage,
              selectedAudioIndex,
            });
          }
        }

        const resolvedAudioSelection = resolveSelectedTrackId(
          audioOptions,
          Number.isFinite(selectedAudioIndex) ? selectedAudioIndex : null,
        );
        console.log('[player] setting initial audio track selection', {
          resolvedAudioSelection,
          metadataSelectedIndex: metadata.selectedAudioIndex,
          preferredAudioIndex: selectedAudioIndex !== metadata.selectedAudioIndex ? selectedAudioIndex : undefined,
        });
        setSelectedAudioTrackId(resolvedAudioSelection);

        const subtitleOptions = buildSubtitleTrackOptions(
          metadata.subtitleStreams ?? [],
          metadata.selectedSubtitleIndex,
        );
        console.log('[player] built subtitle track options from metadata', {
          subtitleStreamsCount: metadata.subtitleStreams?.length ?? 0,
          subtitleOptionsCount: subtitleOptions.length,
          subtitleOptions,
        });
        setSubtitleTrackOptions(subtitleOptions);

        // Check for user preference first, then fall back to metadata selection
        let selectedSubtitleIndex = metadata.selectedSubtitleIndex;
        const preferredSubtitleLanguage = userSettings?.playback?.preferredSubtitleLanguage ?? settings?.playback?.preferredSubtitleLanguage;
        const preferredSubtitleModeRaw = userSettings?.playback?.preferredSubtitleMode ?? settings?.playback?.preferredSubtitleMode;
        const preferredSubtitleMode = (preferredSubtitleModeRaw === 'on' || preferredSubtitleModeRaw === 'off' || preferredSubtitleModeRaw === 'forced-only') ? preferredSubtitleModeRaw : undefined;

        if (preferredSubtitleMode !== undefined) {
          const preferredSubtitleIndex = findSubtitleTrackByPreference(
            metadata.subtitleStreams ?? [],
            preferredSubtitleLanguage,
            preferredSubtitleMode,
          );
          if (preferredSubtitleIndex !== null || preferredSubtitleMode === 'off') {
            selectedSubtitleIndex = preferredSubtitleIndex ?? undefined;
            console.log('[player] using preferred subtitle settings', {
              preferredLanguage: preferredSubtitleLanguage,
              preferredMode: preferredSubtitleMode,
              selectedSubtitleIndex,
            });
          }
        }

        const resolvedSubtitleSelection = resolveSelectedTrackId(
          subtitleOptions,
          selectedSubtitleIndex,
          SUBTITLE_OFF_OPTION.id,
        );
        console.log('[player] setting initial subtitle track selection', {
          resolvedSubtitleSelection,
          metadataSelectedIndex: metadata.selectedSubtitleIndex,
          preferredSubtitleIndex:
            selectedSubtitleIndex !== metadata.selectedSubtitleIndex ? selectedSubtitleIndex : undefined,
        });
        setSelectedSubtitleTrackId(resolvedSubtitleSelection);
      } catch (error) {
        console.warn('[player] failed to preload video metadata', error);
      }
    };

    fetchMetadata();
    return () => {
      isMounted = false;
    };
  }, [resolvedMovie, updateDuration, sourcePath, settings]);

  // Hide loading screen on unmount (e.g., if user navigates back before video loads)
  useEffect(() => {
    return () => {
      hideLoadingScreen();
    };
  }, [hideLoadingScreen]);

  return (
    <SpatialNavigationRoot isActive={Platform.isTV && !isModalOpen && !controlsVisible}>
      <Stack.Screen options={{ headerShown: false }} />
      <StatusBar style="light" hidden={shouldHideStatusBar} animated />
      <FixedSafeAreaView style={styles.safeArea} edges={isPortrait ? ['top'] : []}>
        <View
          style={styles.container}
          nativeID="player-fullscreen-root"
          onLayout={(event) => {
            const { width, height } = event.nativeEvent.layout;
            console.debug('[player] container layout', { width, height });
          }}>
          <View
            style={styles.videoWrapper}
            onLayout={(event) => {
              const { width, height } = event.nativeEvent.layout;
              console.debug('[player] video wrapper layout', { width, height });
            }}>
            <VideoPlayer
              key={effectiveMovie ?? 'novastream-player'}
              ref={videoRef}
              movie={effectiveMovie ?? ''}
              headerImage={headerImage}
              movieTitle={title}
              paused={usesSystemManagedControls ? false : paused}
              controls={usesSystemManagedControls}
              onBuffer={handleBufferState}
              onProgress={handleProgressUpdate}
              onLoad={handleVideoLoad}
              onEnd={() => {
                if (!usesSystemManagedControls) {
                  setPaused(true);
                }
                handleVideoEnd();
              }}
              onError={handleVideoError}
              durationHint={metadataDuration || undefined}
              onInteract={handleVideoInteract}
              onTogglePlay={togglePausePlay}
              volume={volume}
              onAutoplayBlocked={handleAutoplayBlocked}
              onToggleFullscreen={() => setIsFullscreen(!isFullscreen)}
              onImplementationResolved={handleImplementationResolved}
              selectedAudioTrackIndex={isHlsStream ? undefined : selectedAudioTrackIndex}
              selectedSubtitleTrackIndex={isHlsStream ? undefined : selectedSubtitleTrackIndex}
              onTracksAvailable={handleTracksAvailable}
              forceRnvPlayer={routeHasAnyHDR}
              forceNativeFullscreen={Platform.OS !== 'web' && (isHlsStream || routeHasAnyHDR)}
              onVideoSize={(width, height) => setVideoSize({ width, height })}
              nowPlaying={{
                title: episodeName || title || undefined,
                subtitle: seriesTitle || undefined,
                imageUri: headerImage || undefined,
              }}
            />
          </View>

          {/* Black overlay that stays visible until playback starts */}
          {!hasStartedPlaying && (
            <View style={styles.blackOverlay} pointerEvents="none" renderToHardwareTextureAndroid={true} />
          )}

          {/* Loading indicator that stays visible even when controls are hidden */}
          {isVideoBuffering && !usesSystemManagedControls && (
            <View style={styles.loadingOverlay} pointerEvents="none" renderToHardwareTextureAndroid={true}>
              <LoadingIndicator />
            </View>
          )}

          {/* Sidecar subtitle overlay for HLS/fMP4 streams (HDR/DV content) */}
          {/* iOS AVPlayer doesn't expose muxed subtitles in fMP4, so we render them as an overlay */}
          {/* VTT cues are relative to session start, so we need to offset by -playbackOffset */}
          {/* to convert absolute currentTime to session-relative time for cue matching */}
          {isHlsStream && sidecarSubtitleUrl && (
            <SubtitleOverlay
              vttUrl={sidecarSubtitleUrl}
              currentTime={currentTime}
              timeOffset={-playbackOffsetRef.current}
              enabled={selectedSubtitleTrackIndex !== null && selectedSubtitleTrackIndex >= 0}
              videoWidth={videoSize?.width}
              videoHeight={videoSize?.height}
            />
          )}

          {(() => {
            const hasDuration = Number.isFinite(duration) && duration > 0;
            const hasPlaybackContext = hasDuration || currentTime > 0 || isVideoBuffering || paused;
            const isTVSeeking = isTvPlatform && seekIndicatorAmount !== 0;
            const shouldRenderControls =
              !usesSystemManagedControls && (controlsVisible || isModalOpen || isTVSeeking) && (hasPlaybackContext || Platform.isTV);

            return shouldRenderControls ? (
              isTvPlatform ? (
                <TVControlsModal visible={controlsVisible || isTVSeeking} onRequestClose={() => hideControls({ immediate: true })} isChildModalOpen={isModalOpen} isSeeking={isTVSeeking}>
                  <ControlsContainerComponent style={controlsContainerStyle} pointerEvents="box-none">
                    <>
                      {/* Top gradient overlay */}
                      <LinearGradient
                        colors={['rgba(0, 0, 0, 0.7)', 'rgba(0, 0, 0, 0)']}
                        style={styles.topGradient}
                        pointerEvents="none"
                      />
                      {/* Bottom gradient overlay */}
                      <LinearGradient
                        colors={['rgba(0, 0, 0, 0)', 'rgba(0, 0, 0, 0.7)']}
                        style={styles.bottomGradient}
                        pointerEvents="none"
                      />
                    </>
                    <Animated.View style={tvOverlayAnimatedStyle} pointerEvents="box-none" renderToHardwareTextureAndroid={true}>
                      <View style={styles.overlayContent} pointerEvents="box-none" renderToHardwareTextureAndroid={true}>
                        <View style={styles.overlayTopRow} pointerEvents="box-none">
                          <ExitButton onSelect={() => router.back()} onFocus={() => handleFocusChange('exit-button')} />
                          <MediaInfoDisplay
                            mediaType={mediaType}
                            title={title || ''}
                            year={year}
                            seasonNumber={seasonNumber}
                            episodeNumber={episodeNumber}
                            episodeName={episodeName}
                            visible={controlsVisible}
                            sourcePath={sourcePath}
                            displayName={displayName}
                            playerImplementation={playerImplementationLabel}
                            onFilenameDisplayChange={setIsFilenameDisplayed}
                            hdrInfo={hdrInfo}
                            safeAreaInsets={safeAreaInsets}
                          />
                        </View>
                        <View style={styles.overlayControls} pointerEvents="box-none">
                          <SpatialNavigationNode orientation="vertical">
                            <Controls
                              paused={paused}
                              onPlayPause={togglePausePlay}
                              currentTime={currentTime}
                              duration={duration}
                              onSeek={seek}
                              volume={volume}
                              onVolumeChange={handleVolumeChange}
                              isFullscreen={isFullscreen}
                              onToggleFullscreen={toggleFullscreen}
                              audioTracks={audioTrackOptions}
                              selectedAudioTrackId={selectedAudioTrackId}
                              onSelectAudioTrack={(id) => {
                                console.log('[player] user selected audio track', { id, audioTrackOptions });
                                setSelectedAudioTrackId(id);
                              }}
                              subtitleTracks={subtitleTrackOptions}
                              selectedSubtitleTrackId={selectedSubtitleTrackId}
                              onSelectSubtitleTrack={(id) => {
                                console.log('[player] user selected subtitle track', { id, subtitleTrackOptions });
                                setSelectedSubtitleTrackId(id);
                              }}
                              onModalStateChange={handleModalStateChange}
                              onScrubStart={handleSeekBarScrubStart}
                              onScrubEnd={handleSeekBarScrubEnd}
                              isLiveTV={isLiveTV}
                              hasStartedPlaying={hasStartedPlaying}
                              onSkipBackward={handleSkipBackward}
                              onSkipForward={handleSkipForward}
                              onFocusChange={handleFocusChange}
                              seekIndicatorAmount={seekIndicatorAmount}
                              seekIndicatorStartTime={seekIndicatorStartTimeRef.current}
                              isSeeking={isTVSeeking}
                            />
                          </SpatialNavigationNode>
                        </View>
                      </View>
                    </Animated.View>
                  </ControlsContainerComponent>
                </TVControlsModal>
              ) : (
                <ControlsContainerComponent style={controlsContainerStyle} pointerEvents="box-none">
                  <>
                    {/* Top gradient overlay */}
                    <LinearGradient
                      colors={['rgba(0, 0, 0, 0.7)', 'rgba(0, 0, 0, 0)']}
                      style={styles.topGradient}
                      pointerEvents="none"
                    />
                    {/* Bottom gradient overlay */}
                    <LinearGradient
                      colors={['rgba(0, 0, 0, 0)', 'rgba(0, 0, 0, 0.7)']}
                      style={styles.bottomGradient}
                      pointerEvents="none"
                    />
                  </>
                  <View style={styles.overlayContent} pointerEvents="box-none">
                    <View style={styles.overlayTopRow} pointerEvents="box-none">
                      <ExitButton onSelect={() => router.back()} onFocus={() => handleFocusChange('exit-button')} />
                      <MediaInfoDisplay
                        mediaType={mediaType}
                        title={title || ''}
                        year={year}
                        seasonNumber={seasonNumber}
                        episodeNumber={episodeNumber}
                        episodeName={episodeName}
                        visible={controlsVisible}
                        sourcePath={sourcePath}
                        displayName={displayName}
                        playerImplementation={playerImplementationLabel}
                        onFilenameDisplayChange={setIsFilenameDisplayed}
                        hdrInfo={hdrInfo}
                        safeAreaInsets={safeAreaInsets}
                      />
                    </View>
                    <View style={styles.overlayControls} pointerEvents="box-none">
                      <SpatialNavigationNode orientation="vertical">
                        <Controls
                          paused={paused}
                          onPlayPause={togglePausePlay}
                          currentTime={currentTime}
                          duration={duration}
                          onSeek={seek}
                          volume={volume}
                          onVolumeChange={handleVolumeChange}
                          isFullscreen={isFullscreen}
                          onToggleFullscreen={toggleFullscreen}
                          audioTracks={audioTrackOptions}
                          selectedAudioTrackId={selectedAudioTrackId}
                          onSelectAudioTrack={(id) => {
                            console.log('[player] user selected audio track', { id, audioTrackOptions });
                            setSelectedAudioTrackId(id);
                          }}
                          subtitleTracks={subtitleTrackOptions}
                          selectedSubtitleTrackId={selectedSubtitleTrackId}
                          onSelectSubtitleTrack={(id) => {
                            console.log('[player] user selected subtitle track', { id, subtitleTrackOptions });
                            setSelectedSubtitleTrackId(id);
                          }}
                          onModalStateChange={handleModalStateChange}
                          onScrubStart={handleSeekBarScrubStart}
                          onScrubEnd={handleSeekBarScrubEnd}
                          isLiveTV={isLiveTV}
                          hasStartedPlaying={hasStartedPlaying}
                          onSkipBackward={handleSkipBackward}
                          onSkipForward={handleSkipForward}
                          onFocusChange={handleFocusChange}
                        />
                      </SpatialNavigationNode>
                    </View>
                  </View>
                </ControlsContainerComponent>
              )
            ) : null;
          })()}

          {debugOverlayEnabled && (
            <View style={styles.debugOverlay} pointerEvents="box-none">
              <View style={styles.debugCard} pointerEvents="auto">
                <ScrollView
                  style={styles.debugScroll}
                  contentContainerStyle={styles.debugScrollContent}
                  showsVerticalScrollIndicator
                  contentInsetAdjustmentBehavior="never"
                  automaticallyAdjustContentInsets={false}>
                  {debugEntries.length === 0 ? (
                    <Text style={[styles.debugLine, styles.debugInfo]}>Console output will appear here.</Text>
                  ) : (
                    debugEntries.map((entry) => {
                      const lineStyle =
                        entry.level === 'error'
                          ? styles.debugError
                          : entry.level === 'warn'
                            ? styles.debugWarn
                            : styles.debugInfo;

                      return (
                        <Text key={entry.id} style={[styles.debugLine, lineStyle]}>
                          {formatDebugTimestamp(entry.timestamp)} — {entry.message}
                        </Text>
                      );
                    })
                  )}
                </ScrollView>
              </View>
            </View>
          )}
        </View>
      </FixedSafeAreaView>
    </SpatialNavigationRoot>
  );
}

const createPlayerStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: '#000000',
    },
    container: {
      flex: 1,
      backgroundColor: '#000000',
    },
    videoWrapper: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'stretch',
      backgroundColor: '#000000',
    },
    controlsContainer: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'space-between',
      zIndex: 1,
    },
    overlayAnimatedWrapper: {
      flex: 1,
    },
    overlayContent: {
      flex: 1,
      justifyContent: 'space-between',
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.lg,
    },
    overlayTopRow: {
      flexDirection: 'row',
      alignItems: 'flex-start',
      justifyContent: 'space-between',
    },
    overlayControls: {
      flex: 1,
      justifyContent: 'flex-end',
      position: 'relative',
      marginTop: theme.spacing.md,
    },
    topGradient: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      height: '20%',
      zIndex: 0,
    },
    bottomGradient: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: '20%',
      zIndex: 0,
    },
    blackOverlay: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: '#000000',
      zIndex: 10,
    },
    loadingOverlay: {
      ...StyleSheet.absoluteFillObject,
      alignItems: 'center',
      justifyContent: 'center',
      zIndex: 2,
    },
    debugOverlay: {
      ...StyleSheet.absoluteFillObject,
      alignItems: 'stretch',
      justifyContent: 'flex-end',
      zIndex: 3,
    },
    debugCard: {
      alignSelf: 'stretch',
      maxHeight: '60%',
      margin: 12,
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      borderRadius: 8,
    },
    debugScroll: {
      maxHeight: '100%',
      paddingHorizontal: 12,
      paddingVertical: 8,
    },
    debugScrollContent: {
      paddingBottom: 8,
    },
    debugLine: {
      fontSize: 12,
      lineHeight: 16,
      marginBottom: 6,
      fontFamily: Platform.select({ web: 'monospace', default: undefined }) || undefined,
      color: theme.colors.text.primary,
    },
    debugInfo: {
      color: '#9be7ff',
    },
    debugWarn: {
      color: '#ffe57f',
    },
    debugError: {
      color: '#ff8a80',
    },
  });
