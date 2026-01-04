import { clearMemoryCache as clearImageMemoryCache } from '@/components/Image';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import LoadingIndicator from '@/components/LoadingIndicator';
import Controls from '@/components/player/Controls';
import ExitButton from '@/components/player/ExitButton';
import TVControlsModal from '@/components/player/TVControlsModal';
import { isMobileWeb } from '@/components/player/isMobileWeb';
import MediaInfoDisplay from '@/components/player/MediaInfoDisplay';
import { StreamInfoModal } from '@/components/player/StreamInfoModal';
import SubtitleOverlay, { SubtitleCuesRange } from '@/components/player/SubtitleOverlay';
import { SubtitleSearchModal } from '@/components/player/SubtitleSearchModal';
import type { SubtitleSearchResult } from '@/services/api';
import VideoPlayer, {
  VideoPlayerHandle,
  type TrackInfo,
  type VideoImplementation,
  type VideoProgressMeta,
} from '@/components/player/VideoPlayer';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import {
  SpatialNavigationNode,
  SpatialNavigationRoot,
  useLockSpatialNavigation,
  useSpatialNavigator,
} from '@/services/tv-navigation';
import { activateKeepAwakeAsync, deactivateKeepAwake } from 'expo-keep-awake';
import { LinearGradient } from 'expo-linear-gradient';
import { router, Stack, useLocalSearchParams } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Animated,
  AppState,
  BackHandler,
  Image,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';

// TVMenuControl is available on tvOS but not typed in RN types
// eslint-disable-next-line @typescript-eslint/no-var-requires
const TVMenuControl: { enableTVMenuKey?: () => void; disableTVMenuKey?: () => void } | undefined = Platform.isTV
  ? require('react-native').TVMenuControl
  : undefined;

// Safe import for react-native-view-shot (may not be available in older builds)
// Used to capture the last video frame before pause teardown
let captureRef: ((view: View, options?: object) => Promise<string>) | null = null;
try {
  // eslint-disable-next-line @typescript-eslint/no-var-requires
  captureRef = require('react-native-view-shot').captureRef;
} catch {
  // Module not available - will fall back to poster image for pause teardown
}

import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useLoadingScreen } from '@/components/LoadingScreenContext';
import { useToast } from '@/components/ToastContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { usePlaybackProgress } from '@/hooks/usePlaybackProgress';
import type {
  AudioStreamMetadata,
  SubtitleStreamMetadata,
  SeriesEpisode,
  PrequeueStatusResponse,
  SubtitleSessionInfo,
} from '@/services/api';
import apiService from '@/services/api';
import { playbackNavigation } from '@/services/playback-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { updateNowPlaying, updatePlaybackPosition, clearNowPlaying, setupRemoteCommands } from 'now-playing-manager';

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
  releaseName?: string;
  dv?: string;
  dvProfile?: string;
  hdr10?: string;
  forceAAC?: string;
  startOffset?: string;
  titleId?: string;
  imdbId?: string;
  tvdbId?: string;
  preExtractedSubtitles?: string; // JSON stringified SubtitleSessionInfo[]
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

const isStreamForced = (stream: SubtitleStreamMetadata): boolean => {
  // Check isForced flag, disposition.forced, or title containing "forced"
  if (stream.isForced) return true;
  if ((stream.disposition?.forced ?? 0) > 0) return true;
  if (stream.title?.toLowerCase().includes('forced')) return true;
  return false;
};

const isStreamSDH = (stream: SubtitleStreamMetadata): boolean => {
  const title = stream.title?.toLowerCase() || '';
  return title.includes('sdh') || title.includes('hearing impaired') || title.includes('cc');
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

  // Helper to check if stream matches the preferred language
  const matchesLanguage = (stream: SubtitleStreamMetadata): boolean => {
    if (!normalizedPref) return true; // No preference means any language matches
    const language = normalizeLanguageForMatching(stream.language || '');
    const title = normalizeLanguageForMatching(stream.title || '');
    // Exact or partial match
    return (
      language === normalizedPref ||
      title === normalizedPref ||
      language.includes(normalizedPref) ||
      normalizedPref.includes(language)
    );
  };

  // For forced-only mode: only consider forced tracks
  if (mode === 'forced-only') {
    const forcedStreams = streams.filter((s) => isStreamForced(s) && matchesLanguage(s));
    if (forcedStreams.length > 0) {
      return forcedStreams[0].index;
    }
    return null;
  }

  // For 'on' mode: prefer SDH > no title/plain > anything else, exclude forced
  if (mode === 'on') {
    // Get all non-forced streams matching the language
    const nonForcedMatches = streams.filter((s) => !isStreamForced(s) && matchesLanguage(s));

    if (nonForcedMatches.length > 0) {
      // Priority 1: SDH subtitles
      const sdhMatch = nonForcedMatches.find((s) => isStreamSDH(s));
      if (sdhMatch) {
        return sdhMatch.index;
      }

      // Priority 2: No title (plain/full subtitles)
      const plainMatch = nonForcedMatches.find((s) => !s.title || s.title.trim() === '');
      if (plainMatch) {
        return plainMatch.index;
      }

      // Priority 3: Any non-forced match
      return nonForcedMatches[0].index;
    }

    // Fallback: if no non-forced matches, try any stream matching language (including forced)
    const anyMatch = streams.filter((s) => matchesLanguage(s));
    if (anyMatch.length > 0) {
      return anyMatch[0].index;
    }

    // Last resort: first available stream
    return streams[0].index;
  }

  return null;
};

export default function PlayerScreen() {
  const { settings, userSettings, refreshSettings } = useBackendSettings();
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
    releaseName: releaseNameParam,
    dv: dvFlagParam,
    dvProfile: dvProfileParam,
    hdr10: hdr10FlagParam,
    forceAAC: forceAACParam,
    startOffset: startOffsetParam,
    titleId: titleIdParam,
    imdbId: imdbIdParam,
    tvdbId: tvdbIdParam,
    shuffleMode: shuffleModeParam,
    preExtractedSubtitles: preExtractedSubtitlesParam,
  } = useLocalSearchParams<PlayerParams>();
  const resolvedMovie = useMemo(() => {
    const movieParam = Array.isArray(movie) ? movie[0] : movie;
    if (!movieParam) {
      return movieParam;
    }

    // Expo Router automatically decodes URL params, but we need to re-encode
    // special characters for VLC to work correctly.
    // URLSearchParams doesn't encode semicolons which breaks VLC's URL parsing.
    try {
      const url = new URL(movieParam);
      // Re-encode path segments while preserving slashes
      const encodedPathname = url.pathname
        .split('/')
        .map((segment) => encodeURIComponent(decodeURIComponent(segment)))
        .join('/');
      url.pathname = encodedPathname;

      // Also re-encode the 'path' query parameter if present
      // URLSearchParams doesn't encode semicolons, so we need to do it manually
      const pathParam = url.searchParams.get('path');
      if (pathParam) {
        // Remove the old path param and rebuild query string manually
        // to ensure proper encoding of semicolons and other special chars
        const otherParams: string[] = [];
        url.searchParams.forEach((value, key) => {
          if (key !== 'path') {
            otherParams.push(`${encodeURIComponent(key)}=${encodeURIComponent(value)}`);
          }
        });
        // Add path first, properly encoded
        const allParams = [`path=${encodeURIComponent(pathParam)}`, ...otherParams];
        return `${url.origin}${url.pathname}?${allParams.join('&')}`;
      }

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
  const releaseName = useMemo(() => {
    const raw = Array.isArray(releaseNameParam) ? releaseNameParam[0] : releaseNameParam;
    return raw || undefined;
  }, [releaseNameParam]);
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

  // Parse pre-extracted subtitle sessions from route params
  const preExtractedSubtitles = useMemo((): SubtitleSessionInfo[] | null => {
    const raw = Array.isArray(preExtractedSubtitlesParam)
      ? preExtractedSubtitlesParam[0]
      : preExtractedSubtitlesParam;
    if (!raw) {
      return null;
    }
    try {
      const parsed = JSON.parse(raw);
      if (Array.isArray(parsed) && parsed.length > 0) {
        console.log('[player] Loaded pre-extracted subtitles:', parsed.length, 'tracks');
        return parsed as SubtitleSessionInfo[];
      }
    } catch (err) {
      console.warn('[player] Failed to parse preExtractedSubtitles:', err);
    }
    return null;
  }, [preExtractedSubtitlesParam]);

  const [sourcePath, setSourcePath] = useState<string | undefined>(initialSourcePath);
  const safeAreaInsets = useSafeAreaInsets();
  const { width: windowWidth, height: windowHeight } = useTVDimensions();
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

  const shuffleMode = useMemo(() => {
    const raw = Array.isArray(shuffleModeParam) ? shuffleModeParam[0] : shuffleModeParam;
    return raw === '1';
  }, [shuffleModeParam]);

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
  // Pause teardown state: Prevents AVPlayer HLS timeout (-11866) by tearing down
  // the player after extended pause and showing a poster overlay instead
  const [pauseTeardownActive, setPauseTeardownActive] = useState<boolean>(false);
  const [pauseTeardownFrame, setPauseTeardownFrame] = useState<string | null>(null); // Captured frame URI
  const pauseTeardownTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pauseTeardownTimeRef = useRef<number>(0); // Playback time when we tore down
  const pauseTeardownUrlRef = useRef<string | null>(null); // URL to restore
  const videoWrapperRef = useRef<View>(null); // For capturing last frame before teardown
  const PAUSE_TEARDOWN_DELAY = 20000; // 20 seconds before teardown
  const [controlsVisible, setControlsVisible] = useState<boolean>(
    isTvPlatform || (Platform.OS === 'web' && !prefersSystemControls),
  );
  const controlsVisibleRef = useRef<boolean>(controlsVisible);
  const [isVideoBuffering, setIsVideoBuffering] = useState<boolean>(false);
  const [seekIndicatorAmount, setSeekIndicatorAmount] = useState<number>(0);
  const seekIndicatorTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const seekIndicatorStartTimeRef = useRef<number>(0);
  const lastPlayPauseToggleRef = useRef<number>(0); // For debouncing tvOS duplicate events
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
  // External subtitle search state
  const [subtitleSearchModalVisible, setSubtitleSearchModalVisible] = useState<boolean>(false);
  const [externalSubtitleUrl, setExternalSubtitleUrl] = useState<string | null>(null);
  const [subtitleSearchResults, setSubtitleSearchResults] = useState<SubtitleSearchResult[]>([]);
  const [subtitleSearchLoading, setSubtitleSearchLoading] = useState<boolean>(false);
  const [subtitleSearchError, setSubtitleSearchError] = useState<string | null>(null);
  const [subtitleSearchLanguage, setSubtitleSearchLanguage] = useState<string>('en');
  // Subtitle timing offset (positive = subtitles appear later, negative = earlier)
  // Applies to all sidecar subtitles (HLS, extracted, and external)
  const [subtitleOffset, setSubtitleOffset] = useState<number>(0);
  // Extracted subtitle VTT URL for non-HLS streams (using standalone subtitle extraction endpoint)
  const [extractedSubtitleUrl, setExtractedSubtitleUrl] = useState<string | null>(null);
  const [extractedSubtitleSessionId, setExtractedSubtitleSessionId] = useState<string | null>(null);
  // First cue time for SDR subtitle sync (mirrors actualPlaybackOffsetRef for HLS)
  // This is the time of the first extracted cue, used to offset subtitle timing
  const sdrFirstCueTimeRef = useRef<number>(0);
  // Track available subtitle cue range for seek detection (re-extract if seeking outside range)
  const subtitleCuesRangeRef = useRef<SubtitleCuesRange | null>(null);
  // Backend-probed subtitle tracks (used for non-HLS streams to get accurate track indices)
  const [backendSubtitleTracks, setBackendSubtitleTracks] = useState<Array<{
    index: number;
    language: string;
    title: string;
    codec: string;
    forced: boolean;
  }> | null>(null);
  const [debugEntries, setDebugEntries] = useState<DebugLogEntry[]>([]);
  const [isModalOpen, setIsModalOpen] = useState<boolean>(false);
  const isModalOpenRef = useRef(isModalOpen);
  const handleModalStateChange = useCallback((open: boolean) => {
    console.log('[player] handleModalStateChange', { open });
    isModalOpenRef.current = open;
    setIsModalOpen(open);
  }, []);

  // Track if video was paused before opening subtitle search (to avoid resuming if user had paused)
  const wasPlayingBeforeSubtitleSearchRef = useRef(false);

  // Subtitle search handlers
  const handleOpenSubtitleSearch = useCallback(() => {
    // Pause playback while searching for subtitles
    wasPlayingBeforeSubtitleSearchRef.current = !paused;
    if (!paused) {
      setPaused(true);
    }
    setSubtitleSearchModalVisible(true);
    handleModalStateChange(true);
  }, [handleModalStateChange, paused]);

  const handleCloseSubtitleSearch = useCallback(() => {
    setSubtitleSearchModalVisible(false);
    handleModalStateChange(false);
    // Resume playback if it was playing before opening the modal
    if (wasPlayingBeforeSubtitleSearchRef.current) {
      setPaused(false);
    }
  }, [handleModalStateChange]);

  const handleSubtitleSearch = useCallback(
    async (language: string) => {
      setSubtitleSearchLanguage(language);
      setSubtitleSearchLoading(true);
      setSubtitleSearchError(null);

      try {
        const results = await apiService.searchSubtitles({
          imdbId: imdbId || undefined,
          title: title || seriesTitle || undefined,
          year: year || undefined,
          season: seasonNumber,
          episode: episodeNumber,
          language,
        });
        console.log('[player] subtitle search results:', results.length);
        setSubtitleSearchResults(results);
      } catch (error) {
        console.error('[player] subtitle search error:', error);
        setSubtitleSearchError('Failed to search for subtitles');
        setSubtitleSearchResults([]);
      } finally {
        setSubtitleSearchLoading(false);
      }
    },
    [imdbId, title, seriesTitle, year, seasonNumber, episodeNumber],
  );

  const handleSelectExternalSubtitle = useCallback(
    (subtitle: SubtitleSearchResult) => {
      console.log('[player] selected external subtitle:', subtitle);
      const url = apiService.getSubtitleDownloadUrl({
        subtitleId: subtitle.id,
        provider: subtitle.provider,
        imdbId: imdbId || undefined,
        title: title || seriesTitle || undefined,
        year: year || undefined,
        season: seasonNumber,
        episode: episodeNumber,
        language: subtitleSearchLanguage,
      });
      console.log('[player] external subtitle URL:', url);
      setExternalSubtitleUrl(url);
      // Set subtitle track to a special external ID
      setSelectedSubtitleTrackId('external');
      handleCloseSubtitleSearch();
    },
    [imdbId, title, seriesTitle, year, seasonNumber, episodeNumber, subtitleSearchLanguage, handleCloseSubtitleSearch],
  );

  // Subtitle timing adjustment handlers (0.25s increments)
  // Uses refs to access extendControlsVisibility which is defined later in the component
  const SUBTITLE_OFFSET_STEP = 0.25;
  const handleSubtitleOffsetEarlier = useCallback(() => {
    setSubtitleOffset((prev) => {
      const newOffset = prev - SUBTITLE_OFFSET_STEP;
      console.log('[player] subtitle offset adjusted earlier:', newOffset);
      return newOffset;
    });
    // Extend the auto-hide timer by 5 seconds when adjusting offset
    extendControlsVisibilityRef.current?.();
  }, []);

  const handleSubtitleOffsetLater = useCallback(() => {
    setSubtitleOffset((prev) => {
      const newOffset = prev + SUBTITLE_OFFSET_STEP;
      console.log('[player] subtitle offset adjusted later:', newOffset);
      return newOffset;
    });
    // Extend the auto-hide timer by 5 seconds when adjusting offset
    extendControlsVisibilityRef.current?.();
  }, []);

  // Check if external subtitles are active
  const isUsingExternalSubtitles = selectedSubtitleTrackId === 'external' && externalSubtitleUrl !== null;

  const [isSeeking, setIsSeeking] = useState<boolean>(false);
  const [hasStartedPlaying, setHasStartedPlaying] = useState<boolean>(false);
  // For HDR content (Dolby Vision/HDR10), we need to use React Native Video player with HLS
  // VLCKit does not support HDR output - it tone-maps to SDR
  const [hasDolbyVision, setHasDolbyVision] = useState<boolean>(routeHasDolbyVision);
  const [isFilenameDisplayed, setIsFilenameDisplayed] = useState<boolean>(false);
  // Mobile stream info modal (on TV, this is handled in Controls component)
  const [mobileStreamInfoVisible, setMobileStreamInfoVisible] = useState<boolean>(false);
  // Video color metadata for HDR info display
  const [videoColorInfo, setVideoColorInfo] = useState<{
    colorTransfer?: string;
    colorPrimaries?: string;
    colorSpace?: string;
    isHDR10?: boolean;
  } | null>(null);
  // Video/audio stream info for info modal (TV platforms)
  const [streamInfo, setStreamInfo] = useState<{
    resolution?: string;
    videoBitrate?: number;
    videoCodec?: string;
    frameRate?: string;
    audioCodec?: string;
    audioChannels?: string;
    audioBitrate?: number;
  } | null>(null);
  // All episodes for navigation (series content only)
  const [allEpisodes, setAllEpisodes] = useState<SeriesEpisode[]>([]);
  // Video dimensions for subtitle positioning (relative to video content, not screen)
  const [videoSize, setVideoSize] = useState<{ width: number; height: number } | null>(null);
  // Next episode prequeue state (triggered at 90% of current episode)
  const [nextEpisodePrequeueReady, setNextEpisodePrequeueReady] = useState(false);
  const nextEpisodePrequeueRef = useRef<{
    prequeueId: string | null;
    targetEpisode: { seasonNumber: number; episodeNumber: number } | null;
    statusResponse: PrequeueStatusResponse | null;
    isShuffleEpisode: boolean;
  }>({ prequeueId: null, targetEpisode: null, statusResponse: null, isShuffleEpisode: false });
  const hasTriggeredNextEpisodePrequeue = useRef(false);
  const nextEpisodePrequeuePollingRef = useRef<boolean>(false);
  // Refs to allow access to values/functions before they're declared in component order
  const hasNextEpisodeRef = useRef(false);
  const triggerNextEpisodePrequeueRef = useRef<(() => Promise<void>) | null>(null);
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

  // Refresh settings on playback start to ensure latest values (e.g., subtitle size)
  useEffect(() => {
    refreshSettings().catch((err) => {
      console.warn('[player] Failed to refresh settings on mount:', err);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []); // Only run on mount

  // Prevent screen saver / display sleep while video is playing
  // This is needed because VLC player on Android doesn't handle this automatically
  useEffect(() => {
    if (paused) {
      deactivateKeepAwake();
    } else {
      activateKeepAwakeAsync().catch(() => {
        // Ignore errors - keep-awake may not be available on all platforms
      });
    }

    return () => {
      deactivateKeepAwake();
    };
  }, [paused]);

  // Auto-pause when app is backgrounded (mobile and TV)
  const wasPlayingBeforeBackgroundRef = useRef(false);
  useEffect(() => {
    const subscription = AppState.addEventListener('change', (nextAppState) => {
      if (nextAppState === 'background' || nextAppState === 'inactive') {
        // App is being minimized - pause if currently playing
        if (!paused) {
          wasPlayingBeforeBackgroundRef.current = true;
          setPaused(true);
          console.log('[player] Auto-paused: app went to background');
        }
      } else if (nextAppState === 'active') {
        // App came back to foreground - we don't auto-resume, user can manually resume
        wasPlayingBeforeBackgroundRef.current = false;
      }
    });

    return () => {
      subscription.remove();
    };
  }, [paused]);

  // Pause teardown effect: Prevents AVPlayer HLS timeout (-11866) by tearing down
  // the player after extended pause. When paused for 20+ seconds on an HLS stream,
  // we save the current time, clear the video source, and show a poster overlay.
  // This prevents AVPlayer from throwing "stream ended unexpectedly" errors.
  // On resume, we create a new HLS session starting from the saved time.
  useEffect(() => {
    // Only apply to HLS streams on native platforms (where AVPlayer is used)
    const shouldUsePauseTeardown = isHlsStream && Platform.OS !== 'web';

    if (paused && shouldUsePauseTeardown && currentMovieUrl && !pauseTeardownActive) {
      // Start the teardown timer when paused
      console.log('[player] pause teardown: starting 20s timer');
      pauseTeardownTimerRef.current = setTimeout(async () => {
        console.log('[player] pause teardown: timer fired, tearing down player');
        // Save current state for restoration
        pauseTeardownTimeRef.current = currentTimeRef.current;
        pauseTeardownUrlRef.current = currentMovieUrl;

        // Try to capture the current video frame before teardown
        if (captureRef && videoWrapperRef.current) {
          try {
            console.log('[player] pause teardown: capturing video frame');
            const uri = await captureRef(videoWrapperRef.current, {
              format: 'jpg',
              quality: 0.8,
            });
            console.log('[player] pause teardown: frame captured', { uri });
            setPauseTeardownFrame(uri);
          } catch (error) {
            console.log('[player] pause teardown: frame capture failed, using fallback', error);
            setPauseTeardownFrame(null);
          }
        } else {
          console.log('[player] pause teardown: captureRef not available, using fallback');
          setPauseTeardownFrame(null);
        }

        // Tear down the player by clearing the URL
        setCurrentMovieUrl(null);
        setPauseTeardownActive(true);
      }, PAUSE_TEARDOWN_DELAY);
    } else if (!paused && pauseTeardownTimerRef.current) {
      // Clear timer if we unpause before teardown
      console.log('[player] pause teardown: clearing timer (unpaused)');
      clearTimeout(pauseTeardownTimerRef.current);
      pauseTeardownTimerRef.current = null;
    }

    return () => {
      if (pauseTeardownTimerRef.current) {
        clearTimeout(pauseTeardownTimerRef.current);
        pauseTeardownTimerRef.current = null;
      }
    };
  }, [paused, isHlsStream, currentMovieUrl, pauseTeardownActive]);

  // Pause teardown restoration: When user resumes after teardown, create a new HLS session
  useEffect(() => {
    if (!paused && pauseTeardownActive && initialSourcePath) {
      console.log('[player] pause teardown: resuming from teardown, creating new HLS session', {
        savedTime: pauseTeardownTimeRef.current,
      });

      // Create new HLS session starting from saved time
      const restoreSession = async () => {
        try {
          const response = await apiService.createHlsSession({
            path: initialSourcePath,
            dv: routeHasDolbyVision,
            dvProfile: routeDvProfile || undefined,
            hdr: routeHasHDR10,
            forceAAC: forceAacFromRoute,
            start: pauseTeardownTimeRef.current,
            audioTrack: selectedAudioTrackIndexRef.current ?? undefined,
            subtitleTrack: selectedSubtitleTrackIndexRef.current ?? undefined,
          });

          hlsSessionIdRef.current = response.sessionId;

          const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
          const playlistBase = `${baseUrl}${response.playlistUrl}`;
          const authToken = apiService.getAuthToken();
          const playlistWithKey = authToken
            ? `${playlistBase}${playlistBase.includes('?') ? '&' : '?'}token=${encodeURIComponent(authToken)}`
            : playlistBase;

          console.log('[player] pause teardown: restored HLS session', {
            playlistUrl: playlistWithKey,
            startOffset: response.startOffset,
            actualStartOffset: response.actualStartOffset,
          });

          // Update playback offset if the session has a start offset
          if (typeof response.startOffset === 'number' && response.startOffset >= 0) {
            playbackOffsetRef.current = response.startOffset;
            sessionBufferEndRef.current = response.startOffset;
            // Use actualStartOffset for subtitle sync (keyframe-aligned)
            actualPlaybackOffsetRef.current =
              typeof response.actualStartOffset === 'number' && response.actualStartOffset >= 0
                ? response.actualStartOffset
                : response.startOffset;
          }

          // Restore the video URL - this will trigger the video to load
          setCurrentMovieUrl(playlistWithKey);
          setPauseTeardownActive(false);
          setPauseTeardownFrame(null);
        } catch (error) {
          console.error('[player] pause teardown: failed to restore session', error);
          setPauseTeardownActive(false);
          setPauseTeardownFrame(null);
          showToast('Failed to resume playback', { tone: 'danger' });
        }
      };

      restoreSession();
    }
  }, [paused, pauseTeardownActive, initialSourcePath, routeHasDolbyVision, routeDvProfile, routeHasHDR10, forceAacFromRoute, showToast]);

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
  // Ref for extending controls visibility (used by subtitle offset buttons)
  const extendControlsVisibilityRef = useRef<(() => void) | null>(null);
  // Track the current HLS session ID for keepalive pings when paused
  const hlsSessionIdRef = useRef<string | null>(null);
  const currentTimeRef = useRef<number>(0);
  const durationRef = useRef<number>(0);
  const nowPlayingLastUpdateRef = useRef<number>(0); // Throttle iOS Now Playing updates
  const playbackOffsetRef = useRef<number>(initialStartOffset);
  const actualPlaybackOffsetRef = useRef<number>(initialStartOffset); // Keyframe-aligned start for subtitle sync
  const sessionBufferEndRef = useRef<number>(initialStartOffset);
  const warmStartTokenRef = useRef(0);
  const warmStartDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const videoEndedNaturallyRef = useRef(false); // Track if video ended naturally (vs user exit)
  const userNavigatedToEpisodeRef = useRef(false); // Track if user explicitly navigated (via Next/Prev button)
  const warmStartDebounceResolveRef = useRef<((value: boolean) => void) | null>(null);
  const pendingSessionSeekRef = useRef<number | null>(null);
  const pendingSeekAttemptRef = useRef<{ attempts: number; lastAttemptMs: number }>({
    attempts: 0,
    lastAttemptMs: 0,
  });
  const hasAttemptedInitialWarmStartRef = useRef(false);
  const hasReceivedPlayerLoadRef = useRef(false);
  const hlsSessionRetryCountRef = useRef(0);
  // Track when we're recreating HLS sessions (for track change or seek) vs loading a new video
  // When true, the reset effect should preserve current track selections
  const isRecreatingHlsSessionRef = useRef(false);
  // Track when we should skip re-applying language preferences in metadata effect
  // Set when recreating HLS sessions, cleared after metadata effect runs
  const skipTrackPreferencesRef = useRef(false);
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

      // Send keepalive ping with current playback time for rate limiting
      // Also send bufferStart (using currentTime as approximation since players buffer from current position forward)
      // Backend uses this to avoid deleting segments the player still needs
      // Response includes segment timing info for accurate subtitle sync
      try {
        const response = await apiService.keepaliveHlsSession(
          sessionId,
          currentTimeRef.current,
          currentTimeRef.current,
        );
        // Validate playback offset matches server's startOffset
        // This catches any desync between frontend and backend
        if (response.startOffset !== undefined) {
          const offsetDelta = Math.abs(response.startOffset - playbackOffsetRef.current);
          if (offsetDelta > 0.5) {
            console.warn('[player] keepalive: playback offset mismatch, correcting', {
              serverStartOffset: response.startOffset,
              clientPlaybackOffset: playbackOffsetRef.current,
              segmentDuration: response.segmentDuration,
              delta: offsetDelta,
            });
            playbackOffsetRef.current = response.startOffset;
          } else {
            // Log periodically to confirm sync is working (every 60s worth of keepalives)
            if (Math.floor(currentTimeRef.current / 60) !== Math.floor((currentTimeRef.current - 10) / 60)) {
              console.log('[player] keepalive: segment timing in sync', {
                startOffset: response.startOffset,
                segmentDuration: response.segmentDuration,
                currentTime: currentTimeRef.current,
              });
            }
          }
        }
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
    // IMPORTANT: Skip resetting during HLS seek transition - warmStartHlsSession already set the correct offset
    if (pausedForSeekRef.current) {
      console.log('🎬 [player] skipping offset reset during seek transition');
    } else if (isExistingHlsSession && initialStartOffset > 0) {
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
  // Hide status bar on mobile devices (iOS and Android) when controls are hidden for immersive experience
  const shouldHideStatusBar =
    (Platform.OS === 'ios' || Platform.OS === 'android') &&
    !Platform.isTV &&
    !usesSystemManagedControls &&
    !controlsVisible;
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

    const appendToken = (base: string, token: string) =>
      `${base}${base.includes('?') ? '&' : '?'}token=${encodeURIComponent(token)}`;

    let url = endpoint;
    const authToken = apiService.getAuthToken();
    if (authToken) {
      headers['Authorization'] = `Bearer ${authToken}`;
      url = appendToken(url, authToken);
    } else if (typeof window !== 'undefined' && window.location?.search) {
      const inlineToken = new URLSearchParams(window.location.search).get('token');
      if (inlineToken) {
        url = appendToken(url, inlineToken);
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

  // Hide Android navigation bar for immersive video experience
  useEffect(() => {
    if (Platform.OS !== 'android' || Platform.isTV) {
      return;
    }

    const hideNavigationBar = async () => {
      try {
        // Dynamic require to avoid loading native module at parse time on other platforms
        const NavigationBar = require('expo-navigation-bar');
        await NavigationBar.setVisibilityAsync('hidden');
        await NavigationBar.setBehaviorAsync('overlay-swipe');
        console.log('[Player] Android navigation bar hidden');
      } catch (error) {
        console.warn('[Player] Failed to hide Android navigation bar:', error);
      }
    };

    hideNavigationBar();

    // Cleanup: show navigation bar when player closes
    return () => {
      const showNavigationBar = async () => {
        try {
          const NavigationBar = require('expo-navigation-bar');
          await NavigationBar.setVisibilityAsync('visible');
          console.log('[Player] Android navigation bar restored');
        } catch (error) {
          console.warn('[Player] Failed to restore Android navigation bar:', error);
        }
      };
      showNavigationBar();
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

  // Build full stream info for TV info modal - combines media info, stream info, and HDR info
  const fullStreamInfo = useMemo(() => {
    // Build episode code if applicable
    const episodeCode =
      (mediaType === 'episode' || mediaType === 'series' || mediaType === 'tv' || mediaType === 'show') &&
      seasonNumber &&
      episodeNumber
        ? `S${seasonNumber.toString().padStart(2, '0')}E${episodeNumber.toString().padStart(2, '0')}`
        : undefined;

    // Build HDR format string
    let hdrFormatStr: string | undefined;
    if (routeHasDolbyVision) {
      hdrFormatStr = routeDvProfile ? `Dolby Vision Profile ${routeDvProfile}` : 'Dolby Vision';
    } else if (routeHasHDR10 || videoColorInfo?.isHDR10) {
      hdrFormatStr = 'HDR10';
    }

    // Extract filename from source path
    const extractFilename = (path?: string | null) => {
      if (!path) return undefined;
      try {
        const url = new URL(path);
        const pathname = url.pathname;
        const filename = pathname.split('/').pop();
        return filename ? decodeURIComponent(filename) : undefined;
      } catch {
        const filename = path.split('/').pop();
        return filename || undefined;
      }
    };

    return {
      title: title || undefined,
      episodeCode,
      episodeName: episodeName || undefined,
      year: year || undefined,
      filename: displayName || extractFilename(sourcePath),
      resolution: streamInfo?.resolution,
      videoBitrate: streamInfo?.videoBitrate,
      videoCodec: streamInfo?.videoCodec,
      frameRate: streamInfo?.frameRate,
      audioCodec: streamInfo?.audioCodec,
      audioChannels: streamInfo?.audioChannels,
      audioBitrate: streamInfo?.audioBitrate,
      colorSpace: videoColorInfo?.colorSpace,
      colorPrimaries: videoColorInfo?.colorPrimaries,
      colorTransfer: videoColorInfo?.colorTransfer,
      hdrFormat: hdrFormatStr,
      playerImplementation: playerImplementationLabel || undefined,
    };
  }, [
    title,
    mediaType,
    seasonNumber,
    episodeNumber,
    episodeName,
    year,
    displayName,
    sourcePath,
    streamInfo,
    videoColorInfo,
    routeHasDolbyVision,
    routeHasHDR10,
    routeDvProfile,
    playerImplementationLabel,
  ]);

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
      switch (key) {
        case SupportedKeys.Left:
          if (!controlsVisibleRef.current) {
            // TODO: tvOS sends duplicate events per button press (likely MPRemoteCommandCenter interference).
            // Halve the skip amount as a workaround until root cause is properly fixed.
            const baseBackward = settings?.playback?.seekBackwardSeconds ?? 10;
            const backwardAmount = Platform.OS === 'ios' && Platform.isTV ? baseBackward / 2 : baseBackward;

            // Update seek indicator and seek using accumulated amount from start time
            // This ensures rapid presses accumulate correctly instead of using stale currentTime
            setSeekIndicatorAmount((prev) => {
              // Store initial time when seeking starts
              let startTime = seekIndicatorStartTimeRef.current;
              if (prev === 0) {
                startTime = currentTimeRef.current;
                seekIndicatorStartTimeRef.current = startTime;
              }
              const newAmount = prev - backwardAmount;
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
            // TODO: tvOS sends duplicate events per button press (likely MPRemoteCommandCenter interference).
            // Halve the skip amount as a workaround until root cause is properly fixed.
            const baseForward = settings?.playback?.seekForwardSeconds ?? 30;
            const forwardAmount = Platform.OS === 'ios' && Platform.isTV ? baseForward / 2 : baseForward;

            // Update seek indicator and seek using accumulated amount from start time
            // This ensures rapid presses accumulate correctly instead of using stale currentTime
            setSeekIndicatorAmount((prev) => {
              // Store initial time when seeking starts
              let startTime = seekIndicatorStartTimeRef.current;
              if (prev === 0) {
                startTime = currentTimeRef.current;
                seekIndicatorStartTimeRef.current = startTime;
              }
              const newAmount = prev + forwardAmount;
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
              // Hide controls again after seeking is done
              controlsOpacity.setValue(0);
            }, 1000);
          } else {
            // When controls are visible, allow spatial navigation
            showControlsRef.current?.();
          }
          break;
        case SupportedKeys.FastForward: {
          // TODO: tvOS sends duplicate events - halve as workaround
          const ffAmount = settings?.playback?.seekForwardSeconds ?? 30;
          const ffAdjusted = Platform.OS === 'ios' && Platform.isTV ? ffAmount / 2 : ffAmount;
          void seekRef.current?.(currentTimeRef.current + ffAdjusted);
          showControlsRef.current?.();
          break;
        }
        case SupportedKeys.Rewind: {
          // TODO: tvOS sends duplicate events - halve as workaround
          const rwAmount = settings?.playback?.seekBackwardSeconds ?? 10;
          const rwAdjusted = Platform.OS === 'ios' && Platform.isTV ? rwAmount / 2 : rwAmount;
          void seekRef.current?.(currentTimeRef.current - rwAdjusted);
          showControlsRef.current?.();
          break;
        }
        case SupportedKeys.Back:
          if (controlsVisibleRef.current) {
            hideControlsRef.current?.();
          } else {
            router.back();
          }
          break;
        case SupportedKeys.PlayPause: {
          // TODO: tvOS sends duplicate events - debounce to ignore rapid duplicates
          const now = Date.now();
          const debounceMs = Platform.OS === 'ios' && Platform.isTV ? 300 : 0;
          if (now - lastPlayPauseToggleRef.current > debounceMs) {
            lastPlayPauseToggleRef.current = now;
            togglePausePlayRef.current?.();
          }
          break;
        }
        default:
          showControlsRef.current?.();
          break;
      }
    };

    const listener = RemoteControlManager.addKeydownListener(handleKeyDown);
    return () => {
      RemoteControlManager.removeKeydownListener(listener);
    };
  }, [usesSystemManagedControls, router, settings]);

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

        // Check if we have an existing session - use seek endpoint for faster response
        const existingSessionId = hlsSessionIdRef.current;
        let response: { sessionId: string; playlistUrl: string; duration?: number; startOffset?: number };

        if (existingSessionId) {
          console.log('[player] seeking within existing HLS session', {
            sessionId: existingSessionId,
            targetTime: safeTarget,
          });

          // CRITICAL: Clear the video source BEFORE calling seek API.
          // This stops iOS AVPlayer from trying to access segments that the backend
          // is about to delete. Without this, the player errors with -12312 because
          // it tries to prefetch segments from the old (now-deleted) playlist.
          // Use empty string (not null) to prevent fallback to resolvedMovie.
          console.log('[player] clearing video source before seek to prevent race condition');
          setCurrentMovieUrl('');

          try {
            const seekResponse = await apiService.seekHlsSession(existingSessionId, safeTarget);
            response = {
              sessionId: seekResponse.sessionId,
              playlistUrl: seekResponse.playlistUrl,
              duration: seekResponse.duration,
              startOffset: seekResponse.startOffset,
            };
            console.log('[player] HLS seek completed', { response });
          } catch (seekError) {
            console.warn('[player] HLS seek failed, falling back to new session', seekError);
            // Fall through to create new session
            const newSessionResponse = await apiService.createHlsSession({
              path: trimmedPath,
              dv: routeHasDolbyVision,
              dvProfile: routeDvProfile || undefined,
              hdr: routeHasHDR10,
              forceAAC: forceAacFromRoute,
              start: safeTarget,
              audioTrack: currentAudioTrack ?? undefined,
              subtitleTrack: currentSubtitleTrack ?? undefined,
            });
            response = newSessionResponse;
            hlsSessionIdRef.current = response.sessionId;
          }
        } else {
          console.log('[player] creating new HLS session with tracks', {
            audioTrack: currentAudioTrack,
            subtitleTrack: currentSubtitleTrack,
            audioTrackType: typeof currentAudioTrack,
            subtitleTrackType: typeof currentSubtitleTrack,
          });

          response = await apiService.createHlsSession({
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
        }

        if (warmStartTokenRef.current !== token) {
          return true;
        }

        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const playlistBase = `${baseUrl}${response.playlistUrl}`;
        const existingToken = (() => {
          try {
            if (!effectiveMovie) {
              return '';
            }
            const currentUrl = new URL(String(effectiveMovie));
            const token = currentUrl.searchParams.get('token');
            return token ? token.trim() : '';
          } catch {
            return '';
          }
        })();
        const authToken = apiService.getAuthToken() || existingToken;
        // Add cache-busting parameter to force player to reload playlist after seek
        // iOS AVPlayer caches HLS playlists and won't reload if URL is identical
        const cacheBuster = `_t=${Date.now()}`;
        const playlistWithKey = authToken
          ? `${playlistBase}?token=${encodeURIComponent(authToken)}&${cacheBuster}`
          : `${playlistBase}?${cacheBuster}`;

        const sessionStart =
          typeof response.startOffset === 'number' && response.startOffset >= 0 ? response.startOffset : safeTarget;
        // Use actualStartOffset for subtitle sync (keyframe-aligned), fallback to sessionStart
        const actualSessionStart =
          typeof response.actualStartOffset === 'number' && response.actualStartOffset >= 0
            ? response.actualStartOffset
            : sessionStart;
        console.log('[player] warmStartHLS setting offsets', {
          responseStartOffset: response.startOffset,
          responseActualStartOffset: response.actualStartOffset,
          sessionStart,
          actualSessionStart,
          safeTarget,
          playlistUrl: playlistWithKey,
        });
        playbackOffsetRef.current = sessionStart;
        actualPlaybackOffsetRef.current = actualSessionStart; // For subtitle sync
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
          'actualPlaybackOffsetRef.current =',
          actualPlaybackOffsetRef.current,
          'pendingSeek=',
          pendingSessionSeekRef.current,
        );
        console.log('[player] warmStartHLS subtitle transition', {
          newSubtitleTimeOffset: -actualPlaybackOffsetRef.current,
          newPlaylistUrl: playlistWithKey.substring(0, 80),
          newSubtitleUrl: playlistWithKey.replace(/stream\.m3u8/, 'subtitles.vtt').substring(0, 80),
        });
        // Mark that we're recreating HLS session to preserve track selections
        isRecreatingHlsSessionRef.current = true;
        skipTrackPreferencesRef.current = true;
        setCurrentMovieUrl(playlistWithKey);
        hasReceivedPlayerLoadRef.current = false;
        setHasStartedPlaying(false);
        // Reset progress event counter for fresh debug logs after seek
        progressEventCountRef.current = 0;
        firstProgressValueRef.current = null;

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
  const lastSubtitleDebugLogRef = useRef<{ time: number; logged: number; postSeekLogCount?: number }>({
    time: 0,
    logged: 0,
  });

  // Debug: track progress event count for diagnosing start position issues
  const progressEventCountRef = useRef(0);
  const firstProgressValueRef = useRef<{ time: number; absoluteTime: number } | null>(null);

  const handleProgressUpdate = useCallback(
    (time: number, meta?: VideoProgressMeta) => {
      if (!Number.isFinite(time) || time < 0) {
        return;
      }

      const absoluteTime = playbackOffsetRef.current + time;

      // Skip updating currentTime during HLS seek transition to prevent
      // stale progress events from overwriting the correct seek position.
      // We still continue processing the rest of the function (resume logic, etc.)
      if (!pausedForSeekRef.current) {
        currentTimeRef.current = absoluteTime;
        setCurrentTime(absoluteTime);
      }

      // Track first progress event for diagnostics
      const eventCount = progressEventCountRef.current;
      progressEventCountRef.current = eventCount + 1;
      if (eventCount === 0) {
        firstProgressValueRef.current = { time, absoluteTime };
      }

      // Track time jumps (seeks) for subtitle timing state
      const timeDiff = Math.abs(absoluteTime - lastSubtitleDebugLogRef.current.time);
      const isJump = timeDiff > 10;
      if (isJump) {
        lastSubtitleDebugLogRef.current = { time: absoluteTime, logged: Date.now(), postSeekLogCount: 3 };
      } else {
        const postSeekLogCount = lastSubtitleDebugLogRef.current.postSeekLogCount || 0;
        if (postSeekLogCount > 0) {
          lastSubtitleDebugLogRef.current.postSeekLogCount = postSeekLogCount - 1;
        }
        lastSubtitleDebugLogRef.current.time = absoluteTime;
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

      // For live streams (isLiveTV), trigger hasStartedPlaying immediately since time may stay at 0
      // For regular content, wait for time > 0 to avoid flashing before actual playback
      if (!hasStartedPlaying && (time > 0 || isLiveTV)) {
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

      // Update iOS Now Playing position (throttled to every 10 seconds)
      // Skip on tvOS - setting nowPlayingInfo causes MPRemoteCommandCenter to intercept Siri Remote buttons
      if (Platform.OS === 'ios' && !Platform.isTV && hasStartedPlaying && currentDuration > 0) {
        const now = Date.now();
        if (now - nowPlayingLastUpdateRef.current >= 10000) {
          nowPlayingLastUpdateRef.current = now;
          updatePlaybackPosition(absoluteTime, currentDuration, paused ? 0 : 1).catch(() => {
            // Silently ignore errors
          });
        }
      }

      // Trigger prequeue for next episode when at 90% of current episode
      if (
        mediaType === 'episode' &&
        currentDuration > 0 &&
        !hasTriggeredNextEpisodePrequeue.current &&
        (hasNextEpisodeRef.current || shuffleMode)
      ) {
        const percentWatched = (absoluteTime / currentDuration) * 100;
        if (percentWatched >= 90) {
          hasTriggeredNextEpisodePrequeue.current = true;
          triggerNextEpisodePrequeueRef.current?.();
        }
      }
    },
    [
      hasStartedPlaying,
      updateDuration,
      reportProgress,
      initialStartOffset,
      hasDolbyVision,
      isHlsStream,
      isLiveTV,
      applyPendingSessionSeek,
      hideLoadingScreen,
      paused,
      mediaType,
      shuffleMode,
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

      // Resume playback if we paused for seek or audio track change.
      // On Android, ExoPlayer doesn't emit progress events when paused,
      // so we can't rely on handleProgressUpdate to resume playback.
      // Instead, resume here as soon as the video loads.
      if (pausedForSeekRef.current) {
        console.log('[player] resuming playback after load (paused for seek/track change)');
        pausedForSeekRef.current = false;
        setIsVideoBuffering(false);
        setPaused(false);
      }
    },
    [applyPendingSessionSeek, updateDuration],
  );

  const handleBufferState = useCallback((isBuffering: boolean) => {
    setIsVideoBuffering(isBuffering);
  }, []);

  // Poll for next episode prequeue status
  const pollNextEpisodePrequeue = useCallback(async (prequeueId: string) => {
    if (nextEpisodePrequeuePollingRef.current) return;
    nextEpisodePrequeuePollingRef.current = true;

    const maxPolls = 20; // ~30 seconds at 1.5s interval
    let polls = 0;

    while (polls < maxPolls) {
      await new Promise((resolve) => setTimeout(resolve, 1500));
      polls++;

      // Stop if prequeue was cleared or component unmounted
      if (nextEpisodePrequeueRef.current?.prequeueId !== prequeueId) {
        nextEpisodePrequeuePollingRef.current = false;
        return;
      }

      try {
        const status = await apiService.getPrequeueStatus(prequeueId);

        if (apiService.isPrequeueReady(status.status)) {
          nextEpisodePrequeueRef.current.statusResponse = status;
          setNextEpisodePrequeueReady(true);
          console.log('[player] Next episode prequeue ready:', prequeueId);
          nextEpisodePrequeuePollingRef.current = false;
          return;
        }

        if (status.status === 'failed' || status.status === 'expired') {
          console.log('[player] Next episode prequeue failed/expired:', status.status);
          nextEpisodePrequeuePollingRef.current = false;
          return;
        }
      } catch (error) {
        console.warn('[player] Next episode prequeue poll error:', error);
        nextEpisodePrequeuePollingRef.current = false;
        return;
      }
    }

    nextEpisodePrequeuePollingRef.current = false;
  }, []);

  // Trigger prequeue for next episode when at 90% of current episode
  const triggerNextEpisodePrequeue = useCallback(async () => {
    const seriesId = titleId || imdbId || tvdbId;
    if (!seriesId || allEpisodes.length === 0) return;

    // Find the current episode index
    const currentIndex = allEpisodes.findIndex(
      (ep) => ep.seasonNumber === seasonNumber && ep.episodeNumber === episodeNumber,
    );

    let nextEpisode: SeriesEpisode | undefined;
    let isShuffleEpisode = false;

    if (shuffleMode && allEpisodes.length > 1) {
      // Shuffle mode: pick a random episode (different from current)
      let randomIndex: number;
      do {
        randomIndex = Math.floor(Math.random() * allEpisodes.length);
      } while (
        allEpisodes[randomIndex].seasonNumber === seasonNumber &&
        allEpisodes[randomIndex].episodeNumber === episodeNumber
      );
      nextEpisode = allEpisodes[randomIndex];
      isShuffleEpisode = true;
    } else if (currentIndex >= 0 && currentIndex < allEpisodes.length - 1) {
      // Sequential mode: get next episode
      nextEpisode = allEpisodes[currentIndex + 1];
    }

    if (!nextEpisode) return;

    console.log('[player] Triggering prequeue for next episode:', {
      season: nextEpisode.seasonNumber,
      episode: nextEpisode.episodeNumber,
      shuffleMode: isShuffleEpisode,
    });

    try {
      const response = await apiService.prequeuePlayback({
        titleId: seriesId,
        titleName: cleanSeriesTitle || title || '',
        mediaType: 'series',
        userId: activeUserId || 'default',
        seasonNumber: nextEpisode.seasonNumber,
        episodeNumber: nextEpisode.episodeNumber,
      });

      nextEpisodePrequeueRef.current = {
        prequeueId: response.prequeueId,
        targetEpisode: {
          seasonNumber: nextEpisode.seasonNumber,
          episodeNumber: nextEpisode.episodeNumber,
        },
        statusResponse: null,
        isShuffleEpisode,
      };

      // Start polling for ready status
      pollNextEpisodePrequeue(response.prequeueId);
    } catch (error) {
      console.warn('[player] Next episode prequeue failed (non-fatal):', error);
    }
  }, [
    titleId,
    imdbId,
    tvdbId,
    allEpisodes,
    seasonNumber,
    episodeNumber,
    shuffleMode,
    cleanSeriesTitle,
    title,
    activeUserId,
    pollNextEpisodePrequeue,
  ]);

  // Keep ref updated for use in callbacks defined earlier in the component
  triggerNextEpisodePrequeueRef.current = triggerNextEpisodePrequeue;

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
    [canUseNativeDriver, controlsOpacity, shouldAutoHideControls, usesSystemManagedControls, pauseTeardownActive],
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
      // If subtitle search modal is open, close it
      if (subtitleSearchModalVisible) {
        handleCloseSubtitleSearch();
        return true;
      }
      if (controlsVisible && !isModalOpen) {
        hideControls();
        return true;
      }
      return false;
    });

    return () => {
      subscription.remove();
    };
  }, [
    controlsVisible,
    hideControls,
    isModalOpen,
    isTvPlatform,
    usesSystemManagedControls,
    subtitleSearchModalVisible,
    handleCloseSubtitleSearch,
  ]);

  // Cleanup seek indicator timeout on unmount
  useEffect(() => {
    return () => {
      if (seekIndicatorTimeoutRef.current) {
        clearTimeout(seekIndicatorTimeoutRef.current);
      }
    };
  }, []);

  // iOS Now Playing integration - setup remote commands on mount, clear on unmount
  // Note: Skip entirely on tvOS because setting nowPlayingInfo and enabling MPRemoteCommandCenter
  // causes Siri Remote button presses to be intercepted/duplicated, breaking play/pause and skip
  useEffect(() => {
    if (Platform.OS === 'ios' && !Platform.isTV) {
      setupRemoteCommands().catch((err) => {
        console.warn('[player] Failed to setup remote commands:', err);
      });
    }
    return () => {
      if (Platform.OS === 'ios' && !Platform.isTV) {
        clearNowPlaying().catch((err) => {
          console.warn('[player] Failed to clear Now Playing:', err);
        });
      }
    };
  }, []);

  // iOS Now Playing - update info when playback starts
  // Skip on tvOS - setting nowPlayingInfo causes MPRemoteCommandCenter to intercept Siri Remote buttons
  useEffect(() => {
    if (Platform.OS !== 'ios' || Platform.isTV || !hasStartedPlaying) {
      return;
    }

    // Build Now Playing info
    const nowPlayingTitle = mediaType === 'episode' && seriesTitle ? seriesTitle : (displayName ?? title ?? 'Unknown');
    let nowPlayingSubtitle: string | undefined;
    if (mediaType === 'episode' && seasonNumber && episodeNumber) {
      const episodeCode = `S${String(seasonNumber).padStart(2, '0')}E${String(episodeNumber).padStart(2, '0')}`;
      nowPlayingSubtitle = episodeName ? `${episodeCode} - ${episodeName}` : episodeCode;
    } else if (year) {
      nowPlayingSubtitle = String(year);
    }

    const imageUri = Array.isArray(headerImage) ? headerImage[0] : headerImage;

    console.log('[player] Updating iOS Now Playing info:', {
      title: nowPlayingTitle,
      subtitle: nowPlayingSubtitle,
      imageUri: imageUri?.substring(0, 50),
      duration: durationRef.current,
    });

    updateNowPlaying({
      title: nowPlayingTitle,
      subtitle: nowPlayingSubtitle,
      duration: durationRef.current > 0 ? durationRef.current : undefined,
      currentTime: currentTimeRef.current,
      playbackRate: paused ? 0 : 1,
      imageUri,
    }).catch((err) => {
      console.warn('[player] Failed to update Now Playing:', err);
    });
  }, [
    hasStartedPlaying,
    mediaType,
    seriesTitle,
    displayName,
    title,
    seasonNumber,
    episodeNumber,
    episodeName,
    year,
    headerImage,
    paused,
  ]);

  // Check if we should set next episode on unmount (when exiting player early but close to end)
  useEffect(() => {
    return () => {
      // Skip if video ended naturally - handleVideoEnd already handled autoPlay
      if (videoEndedNaturallyRef.current) {
        console.log('[player] Skipping unmount next episode logic - video ended naturally');
        return;
      }

      // Skip if user explicitly navigated via Next/Prev button - navigateToEpisode already set it
      if (userNavigatedToEpisodeRef.current) {
        console.log('[player] Skipping unmount next episode logic - user explicitly navigated');
        return;
      }

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

          // If watched >= 90%, set the next episode (but not autoPlay - user exited manually)
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
        TVMenuControl.enableTVMenuKey();
      } else {
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

  // Extended visibility for subtitle offset adjustments (5 seconds extra = 8 seconds total)
  const EXTENDED_HIDE_DURATION_MS = autoHideDurationMs + 5000;
  const extendControlsVisibility = useCallback(() => {
    if (usesSystemManagedControls) {
      return;
    }
    // Show controls first to ensure they're visible
    showControls();
    // Then clear the default timeout and set an extended one
    if (hideControlsTimeoutRef.current) {
      clearTimeout(hideControlsTimeoutRef.current);
      hideControlsTimeoutRef.current = null;
    }
    if (shouldAutoHideControls && !isModalOpenRef.current && !isSeekingRef.current && !isFilenameDisplayed) {
      hideControlsTimeoutRef.current = setTimeout(() => {
        hideControls();
      }, EXTENDED_HIDE_DURATION_MS);
    }
  }, [
    showControls,
    hideControls,
    usesSystemManagedControls,
    shouldAutoHideControls,
    isFilenameDisplayed,
    EXTENDED_HIDE_DURATION_MS,
  ]);

  // Keep extendControlsVisibilityRef updated
  useEffect(() => {
    extendControlsVisibilityRef.current = extendControlsVisibility;
  }, [extendControlsVisibility]);

  // Keep controlsVisibleRef in sync for use in event handlers (avoids stale closure)
  useEffect(() => {
    controlsVisibleRef.current = controlsVisible;
  }, [controlsVisible]);

  // Track previous modal state to detect modal close
  const prevIsModalOpenRef = useRef(isModalOpen);
  useEffect(() => {
    const wasModalOpen = prevIsModalOpenRef.current;
    prevIsModalOpenRef.current = isModalOpen;

    if (isModalOpen) {
      // Modal just opened - show controls (auto-hide timer won't be set since modal is open)
      showControls();
    } else if (wasModalOpen && controlsVisible) {
      // Modal just closed but controls are still visible - restart auto-hide timer
      showControls();
    }
  }, [isModalOpen, showControls, controlsVisible]);

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
  }, [
    controlsVisible,
    hideControls,
    isTouchOverlayToggleSupported,
    isTvPlatform,
    showControls,
    usesSystemManagedControls,
  ]);

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
          pauseTeardownActive,
        });

        // If player was torn down due to extended pause, update the saved time
        // and trigger restoration - the restoration effect will create a new
        // HLS session starting from this seek position
        if (pauseTeardownActive) {
          console.log('[player] seek during pause teardown, updating saved time and resuming', { time });
          pauseTeardownTimeRef.current = time;
          currentTimeRef.current = time;
          setCurrentTime(time);
          setPaused(false); // Triggers restoration effect
          if (shouldShowControls) {
            showControls();
          }
          return;
        }

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
          // Check if we need to re-extract subtitles for the new position
          triggerSubtitleReExtractionRef.current?.(time);
        }
        if (shouldShowControls) {
          showControls();
        }
      })();
    },
    [isHlsStream, pauseTeardownActive, showControls, sourcePath, warmStartHlsSession],
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

  const seekBackwardSeconds = settings?.playback?.seekBackwardSeconds ?? 10;
  const seekForwardSeconds = settings?.playback?.seekForwardSeconds ?? 30;

  const handleSkipBackward = useCallback(() => {
    const targetTime = Math.max(0, currentTimeRef.current - seekBackwardSeconds);
    seek(targetTime);
  }, [seek, seekBackwardSeconds]);

  const handleSkipForward = useCallback(() => {
    const targetTime = currentTimeRef.current + seekForwardSeconds;
    seek(targetTime);
  }, [seek, seekForwardSeconds]);

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

      // Suppress "resource unavailable" errors during HLS session seek transitions
      // When seeking outside the current HLS session, we pause playback and create a new session.
      // During the debounce window, iOS AVPlayer may throw NSURLErrorResourceUnavailable (-1008)
      // because segments for the target position don't exist yet. This is expected and should be ignored.
      const errorCode = error?.error?.code;
      const errorDomain = error?.error?.domain;
      if (pausedForSeekRef.current && errorDomain === 'NSURLErrorDomain' && errorCode === -1008) {
        console.log('[player] suppressing resource unavailable error during HLS seek transition');
        return;
      }

      // Check if this is a CoreMedia error that might be caused by DV fallback
      // Error -19601 (kCMSampleBufferError_RequiredParameterMissing) occurs when
      // the player has stale init segment data after server-side DV fallback
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
          const authToken = apiService.getAuthToken();
          const playlistWithKey = authToken
            ? `${playlistBase}${playlistBase.includes('?') ? '&' : '?'}token=${encodeURIComponent(authToken)}`
            : playlistBase;

          console.log('[player] created new HLS session for retry', {
            playlistUrl: playlistWithKey,
            startOffset: response.startOffset,
            actualStartOffset: response.actualStartOffset,
          });

          // Update the video source to the new session
          setCurrentMovieUrl(playlistWithKey);

          // Update playback offset if the session has a start offset
          if (typeof response.startOffset === 'number' && response.startOffset >= 0) {
            playbackOffsetRef.current = response.startOffset;
            sessionBufferEndRef.current = response.startOffset;
            // Use actualStartOffset for subtitle sync (keyframe-aligned)
            actualPlaybackOffsetRef.current =
              typeof response.actualStartOffset === 'number' && response.actualStartOffset >= 0
                ? response.actualStartOffset
                : response.startOffset;
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
      shuffleMode,
    });

    // Mark that video ended naturally - prevents unmount cleanup from overwriting autoPlay
    videoEndedNaturallyRef.current = true;

    // Only auto-navigate for TV show episodes
    if (mediaType === 'episode' && seasonNumber && episodeNumber) {
      const seriesId = titleId || imdbId || tvdbId;

      // Check if we have a prequeued episode to use (works for both shuffle and sequential)
      const prequeueData = nextEpisodePrequeueRef.current;
      if (prequeueData?.prequeueId && prequeueData.targetEpisode && seriesId) {
        console.log('🎬 Using prequeued episode', {
          season: prequeueData.targetEpisode.seasonNumber,
          episode: prequeueData.targetEpisode.episodeNumber,
          isShuffleEpisode: prequeueData.isShuffleEpisode,
        });

        playbackNavigation.setNextEpisode(
          seriesId,
          prequeueData.targetEpisode.seasonNumber,
          prequeueData.targetEpisode.episodeNumber,
          true,
          prequeueData.isShuffleEpisode,
          prequeueData.prequeueId,
          prequeueData.statusResponse ?? undefined,
        );

        setTimeout(() => {
          router.back();
        }, 500);
        return;
      }

      // No prequeue - fall back to existing logic
      // Shuffle mode: pick a random episode (different from current)
      if (shuffleMode && allEpisodes.length > 1) {
        let randomIndex: number;
        let nextEp: SeriesEpisode;
        do {
          randomIndex = Math.floor(Math.random() * allEpisodes.length);
          nextEp = allEpisodes[randomIndex];
        } while (nextEp.seasonNumber === seasonNumber && nextEp.episodeNumber === episodeNumber);

        console.log('🔀 Shuffle: playing random episode', {
          season: nextEp.seasonNumber,
          episode: nextEp.episodeNumber,
        });

        if (seriesId) {
          playbackNavigation.setNextEpisode(seriesId, nextEp.seasonNumber, nextEp.episodeNumber, true, true);
        }

        setTimeout(() => {
          router.back();
        }, 500);
        return;
      }

      // Sequential mode: find next episode in order
      const currentIndex = allEpisodes.findIndex(
        (ep) => ep.seasonNumber === seasonNumber && ep.episodeNumber === episodeNumber,
      );
      const hasNext = currentIndex >= 0 && currentIndex < allEpisodes.length - 1;
      const nextEp = hasNext ? allEpisodes[currentIndex + 1] : null;

      if (nextEp) {
        console.log('🎬 Auto-playing next episode', {
          season: nextEp.seasonNumber,
          episode: nextEp.episodeNumber,
        });

        if (seriesId) {
          // Set next episode with autoPlay=true so details page auto-plays
          playbackNavigation.setNextEpisode(seriesId, nextEp.seasonNumber, nextEp.episodeNumber, true, false);
        }

        // Small delay for smooth transition, then navigate back to trigger autoplay
        setTimeout(() => {
          router.back();
        }, 500);
      } else {
        console.log('🎬 No next episode available, returning to details');
        // No next episode, just go back after a delay
        setTimeout(() => {
          router.back();
        }, 1000);
      }
    } else {
      // For movies, just go back to the previous screen
      setTimeout(() => {
        router.back();
      }, 1000);
    }
  }, [mediaType, seasonNumber, episodeNumber, router, titleId, imdbId, tvdbId, allEpisodes, shuffleMode]);

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

      // Only use VLC's subtitle tracks if we don't have backend-probed tracks
      // For non-HLS streams, backend tracks are more reliable (correct indices)
      if (subtitleTrackOptions.length <= 1 && playerSubtitleOptions.length > 0 && !backendSubtitleTracks) {
        const mergedSubtitles = [SUBTITLE_OFF_OPTION, ...playerSubtitleOptions];
        setSubtitleTrackOptions(mergedSubtitles);
        // TODO: Add settings for default language selection
        // For now, default to first available subtitle track
        const firstSubtitleId = playerSubtitleOptions[0]?.id ?? SUBTITLE_OFF_OPTION.id;
        setSelectedSubtitleTrackId(firstSubtitleId);
      }
    },
    [audioTrackOptions.length, selectedSubtitleTrackId, subtitleTrackOptions.length, backendSubtitleTracks],
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

  // Derive the sidecar subtitle VTT URL from the HLS playlist URL
  // The backend extracts subtitles to subtitles.vtt alongside the HLS segments for fMP4/HDR content
  // Supports ?track=N parameter to switch subtitle tracks without recreating HLS session
  const sidecarSubtitleUrl = useMemo(() => {
    if (!isHlsStream || !effectiveMovie) {
      return null;
    }

    // Replace stream.m3u8 with subtitles.vtt in the URL
    let baseUrl = String(effectiveMovie).replace(/stream\.m3u8/, 'subtitles.vtt');

    // Add track parameter if a subtitle track is selected
    if (selectedSubtitleTrackIndex !== null && selectedSubtitleTrackIndex >= 0) {
      const separator = baseUrl.includes('?') ? '&' : '?';
      baseUrl = `${baseUrl}${separator}track=${selectedSubtitleTrackIndex}`;
      console.log('[player] sidecarSubtitleUrl: adding track parameter', {
        selectedSubtitleTrackIndex,
        finalUrl: baseUrl,
      });
    } else {
      console.log('[player] sidecarSubtitleUrl: no track selected', {
        selectedSubtitleTrackIndex,
        finalUrl: baseUrl,
      });
    }

    return baseUrl;
  }, [isHlsStream, effectiveMovie, selectedSubtitleTrackIndex]);

  // Debug logging for sidecar subtitle URL changes
  useEffect(() => {
    console.log('[player] sidecarSubtitleUrl changed', {
      sidecarSubtitleUrl,
      selectedSubtitleTrackIndex,
      effectiveMovie: effectiveMovie ? String(effectiveMovie).substring(0, 100) : null,
      isHlsStream,
      currentTime: currentTimeRef.current,
    });
  }, [sidecarSubtitleUrl, effectiveMovie, isHlsStream, selectedSubtitleTrackIndex]);

  // Check if any sidecar subtitles are active (for showing offset controls)
  const isUsingSidecarSubtitles = useMemo(() => {
    const hasActiveTrack = selectedSubtitleTrackIndex !== null && selectedSubtitleTrackIndex >= 0;
    if (isUsingExternalSubtitles) return true;
    if (isHlsStream && sidecarSubtitleUrl && hasActiveTrack) return true;
    if (!isHlsStream && extractedSubtitleUrl && selectedSubtitleTrackId !== 'external' && hasActiveTrack) return true;
    return false;
  }, [
    isUsingExternalSubtitles,
    isHlsStream,
    sidecarSubtitleUrl,
    extractedSubtitleUrl,
    selectedSubtitleTrackId,
    selectedSubtitleTrackIndex,
  ]);

  // Probe subtitle tracks from backend for non-HLS streams
  // This gives us accurate track indices and metadata for subtitle selection
  // If pre-extracted subtitles are available, use them instead of probing
  useEffect(() => {
    // Skip for HLS/HDR streams - they handle subtitles through the HLS session
    if (isHlsStream || routeHasAnyHDR) {
      setBackendSubtitleTracks(null);
      return;
    }

    // If we have pre-extracted subtitles, use them instead of probing
    if (preExtractedSubtitles && preExtractedSubtitles.length > 0) {
      console.log('[player] using pre-extracted subtitle tracks:', preExtractedSubtitles.length);
      const tracks = preExtractedSubtitles.map((session) => ({
        index: session.trackIndex,
        language: session.language,
        title: session.title,
        codec: session.codec,
        forced: session.isForced,
      }));
      setBackendSubtitleTracks(tracks);

      // Build track options from pre-extracted sessions
      const subtitleOptions: TrackOption[] = preExtractedSubtitles.map((session) => {
        let label = session.title || session.language || `Track ${session.trackIndex + 1}`;
        if (session.language && session.title && !session.title.toLowerCase().includes(session.language.toLowerCase())) {
          label = `${session.language.toUpperCase()} - ${session.title}`;
        }
        if (session.isForced) {
          label += ' (Forced)';
        }
        return {
          id: String(session.trackIndex),
          label,
        };
      });

      setSubtitleTrackOptions([SUBTITLE_OFF_OPTION, ...subtitleOptions]);

      // Auto-select based on user preference (same logic as probe path)
      const preferredLang = (
        userSettings?.playback?.preferredSubtitleLanguage ||
        settings?.playback?.preferredSubtitleLanguage ||
        ''
      ).toLowerCase();
      const preferredMode =
        userSettings?.playback?.preferredSubtitleMode || settings?.playback?.preferredSubtitleMode;

      // Convert to SubtitleStreamMetadata-like format for selection logic
      const streamsForSelection: SubtitleStreamMetadata[] = preExtractedSubtitles.map((s) => ({
        index: s.trackIndex,
        language: s.language,
        title: s.title,
        isForced: s.isForced,
        codec: s.codec || '',
      }));

      const validMode =
        preferredMode === 'on' || preferredMode === 'off' || preferredMode === 'forced-only' ? preferredMode : 'on';

      const selectedIndex = findSubtitleTrackByPreference(
        streamsForSelection,
        preferredLang || undefined,
        validMode,
      );

      if (selectedIndex !== null) {
        setSelectedSubtitleTrackId(String(selectedIndex));
      } else {
        setSelectedSubtitleTrackId('off');
      }
      return;
    }

    // Need source path to probe
    if (!sourcePath) {
      return;
    }

    console.log('[player] probing subtitle tracks from backend', { sourcePath });

    let cancelled = false;
    apiService
      .probeSubtitleTracks(sourcePath)
      .then((response) => {
        if (cancelled) return;
        console.log('[player] backend subtitle tracks:', response.tracks);
        setBackendSubtitleTracks(response.tracks);

        // Build track options from backend response
        if (response.tracks.length > 0) {
          const backendSubtitleOptions: TrackOption[] = response.tracks.map((track) => {
            // Build a descriptive label
            let label = track.title || track.language || `Track ${track.index + 1}`;
            if (track.language && track.title && !track.title.toLowerCase().includes(track.language.toLowerCase())) {
              label = `${track.language.toUpperCase()} - ${track.title}`;
            }
            if (track.forced) {
              label += ' (Forced)';
            }
            return {
              id: String(track.index),
              label,
            };
          });

          // Merge with existing options (keep Off option, add backend tracks)
          const newTrackOptions = [SUBTITLE_OFF_OPTION, ...backendSubtitleOptions];
          setSubtitleTrackOptions(newTrackOptions);
          // Get valid track ids from backend response
          const validTrackIds = new Set(['off', ...response.tracks.map((t) => String(t.index))]);

          // Auto-select based on user preference
          const preferredLang = (
            userSettings?.playback?.preferredSubtitleLanguage ||
            settings?.playback?.preferredSubtitleLanguage ||
            ''
          ).toLowerCase();
          const preferredMode =
            userSettings?.playback?.preferredSubtitleMode || settings?.playback?.preferredSubtitleMode;

          // Convert backend tracks to SubtitleStreamMetadata-like format for reuse of selection logic
          const streamsForSelection: SubtitleStreamMetadata[] = response.tracks.map((t) => ({
            index: t.index,
            language: t.language,
            title: t.title,
            isForced: t.forced,
            codec: t.codec || '',
          }));

          // Use the same selection logic as metadata path
          const validMode =
            preferredMode === 'on' || preferredMode === 'off' || preferredMode === 'forced-only' ? preferredMode : 'on'; // Default to 'on' if not set

          const selectedIndex = findSubtitleTrackByPreference(
            streamsForSelection,
            preferredLang || undefined,
            validMode,
          );

          if (selectedIndex !== null) {
            setSelectedSubtitleTrackId(String(selectedIndex));
          } else if (validMode === 'off') {
            setSelectedSubtitleTrackId('off');
          } else {
            // No preferred language set - check if current selection is valid
            // This handles the race condition where VLC set an invalid track id
            setSelectedSubtitleTrackId((current) => {
              if (current && validTrackIds.has(current)) {
                return current; // Current selection is valid, keep it
              }
              // Current selection is invalid (from VLC), default to first track or off
              const firstTrackId = response.tracks[0] ? String(response.tracks[0].index) : 'off';
              return firstTrackId;
            });
          }
        } else {
          // No text-based subtitle tracks found (e.g., only PGS bitmap subs)
          // Clear any VLC-reported tracks and show only "Off" option
          console.log('[player] no text-based subtitle tracks found, clearing VLC-reported tracks');
          setSubtitleTrackOptions([SUBTITLE_OFF_OPTION]);
          setSelectedSubtitleTrackId('off');
        }
      })
      .catch((error) => {
        if (cancelled) return;
        console.error('[player] failed to probe subtitle tracks:', error);
        setBackendSubtitleTracks(null);
      });

    return () => {
      cancelled = true;
    };
  }, [isHlsStream, routeHasAnyHDR, sourcePath, userSettings, settings, preExtractedSubtitles]);

  // Start subtitle extraction for non-HLS streams when a subtitle track is selected
  // This uses the standalone subtitle extraction endpoint to generate VTT
  // If pre-extracted subtitles are available, use them instead of on-demand extraction
  useEffect(() => {
    // Only for non-HLS streams with an embedded subtitle track selected
    // Skip for HLS streams (they use sidecar subtitles from the HLS session)
    // Also skip for DV/HDR content which will become HLS streams
    if (isHlsStream || routeHasAnyHDR) {
      // HLS/DV/HDR streams use sidecar subtitles from the HLS session
      return;
    }

    // Wait for backend probe to complete before starting extraction
    // This prevents race condition where VLC reports incorrect track indices
    if (backendSubtitleTracks === null) {
      console.log('[player] subtitle extraction waiting for backend probe');
      return;
    }

    // If no subtitle track selected or external subtitles are in use, clear extracted URL
    if (
      selectedSubtitleTrackIndex === null ||
      selectedSubtitleTrackIndex < 0 ||
      selectedSubtitleTrackId === 'external'
    ) {
      setExtractedSubtitleUrl(null);
      setExtractedSubtitleSessionId(null);
      return;
    }

    // Validate track index is within range of backend-probed tracks
    // This prevents race condition where VLC reports absolute stream indices (e.g., 14)
    // before the backend probe completes and sets correct relative indices (0-4)
    if (selectedSubtitleTrackIndex >= backendSubtitleTracks.length) {
      console.log('[player] subtitle extraction skipped - track index out of range', {
        selectedSubtitleTrackIndex,
        backendTracksCount: backendSubtitleTracks.length,
      });
      return;
    }

    // Check if we have a pre-extracted session for this track
    if (preExtractedSubtitles) {
      const preExtractedSession = preExtractedSubtitles.find(
        (session) => session.trackIndex === selectedSubtitleTrackIndex,
      );
      if (preExtractedSession) {
        console.log('[player] using pre-extracted subtitle for track', selectedSubtitleTrackIndex, {
          sessionId: preExtractedSession.sessionId,
          isExtracting: preExtractedSession.isExtracting,
          firstCueTime: preExtractedSession.firstCueTime,
        });
        // Build full URL from relative path
        const fullUrl = apiService.getFullUrl(preExtractedSession.vttUrl);
        setExtractedSubtitleUrl(fullUrl);
        setExtractedSubtitleSessionId(preExtractedSession.sessionId);
        // Set first cue time for subtitle sync (mirrors actualPlaybackOffsetRef for HLS)
        sdrFirstCueTimeRef.current = preExtractedSession.firstCueTime ?? 0;
        return;
      }
    }

    // Need source path to start extraction (fallback to on-demand)
    if (!sourcePath) {
      console.log('[player] subtitle extraction skipped - no sourcePath');
      return;
    }

    // Start subtitle extraction (on-demand fallback)
    console.log('[player] starting on-demand subtitle extraction', {
      sourcePath,
      subtitleTrack: selectedSubtitleTrackIndex,
      backendTracks: backendSubtitleTracks?.length,
      startOffset: initialStartOffset,
    });

    let cancelled = false;
    apiService
      .startSubtitleExtract(sourcePath, selectedSubtitleTrackIndex, initialStartOffset)
      .then((response) => {
        if (cancelled) return;
        console.log('[player] subtitle extraction started', response);
        // Build full URL from relative path
        const fullUrl = apiService.getFullUrl(response.subtitleUrl);
        setExtractedSubtitleUrl(fullUrl);
        setExtractedSubtitleSessionId(response.sessionId);
        // Set first cue time for subtitle sync (mirrors actualPlaybackOffsetRef for HLS)
        sdrFirstCueTimeRef.current = response.firstCueTime ?? 0;
      })
      .catch((error) => {
        if (cancelled) return;
        console.error('[player] subtitle extraction failed', error);
        setExtractedSubtitleUrl(null);
        setExtractedSubtitleSessionId(null);
      });

    return () => {
      cancelled = true;
    };
  }, [
    isHlsStream,
    routeHasAnyHDR,
    selectedSubtitleTrackIndex,
    selectedSubtitleTrackId,
    sourcePath,
    backendSubtitleTracks,
    preExtractedSubtitles,
    initialStartOffset,
  ]);

  // Callback to handle subtitle cues range changes from SubtitleOverlay
  const handleSubtitleCuesRangeChange = useCallback((range: SubtitleCuesRange | null) => {
    subtitleCuesRangeRef.current = range;
  }, []);

  // Re-extract subtitles when seeking outside the available cue range
  const triggerSubtitleReExtraction = useCallback(
    (seekPosition: number) => {
      // Only for non-HLS streams with active subtitle extraction
      if (isHlsStream || !sourcePath || selectedSubtitleTrackIndex === null || selectedSubtitleTrackIndex < 0) {
        return;
      }

      const range = subtitleCuesRangeRef.current;
      // If no range yet (extraction just started) or position is within range, no need to re-extract
      if (!range) {
        console.log('[player] subtitle re-extraction skipped - no cue range yet');
        return;
      }

      // Check if seek position is outside available cue range
      // Add a small buffer (10s) to avoid unnecessary re-extractions for small seeks
      const buffer = 10;
      const isOutsideRange = seekPosition < range.minTime - buffer || seekPosition > range.maxTime + buffer;

      if (!isOutsideRange) {
        console.log('[player] seek within subtitle cue range, no re-extraction needed', {
          seekPosition,
          range,
        });
        return;
      }

      console.log('[player] seek outside subtitle cue range, triggering re-extraction', {
        seekPosition,
        range,
        selectedSubtitleTrackIndex,
      });

      // Clear current extraction state
      setExtractedSubtitleUrl(null);
      setExtractedSubtitleSessionId(null);
      subtitleCuesRangeRef.current = null;

      // Start new extraction from seek position
      apiService
        .startSubtitleExtract(sourcePath, selectedSubtitleTrackIndex, seekPosition)
        .then((response) => {
          console.log('[player] subtitle re-extraction started', response);
          const fullUrl = apiService.getFullUrl(response.subtitleUrl);
          setExtractedSubtitleUrl(fullUrl);
          setExtractedSubtitleSessionId(response.sessionId);
          // Update first cue time for subtitle sync after seek
          sdrFirstCueTimeRef.current = response.firstCueTime ?? 0;
        })
        .catch((error) => {
          console.error('[player] subtitle re-extraction failed', error);
        });
    },
    [isHlsStream, sourcePath, selectedSubtitleTrackIndex],
  );

  // Keep ref updated for use in seek handler
  const triggerSubtitleReExtractionRef = useRef(triggerSubtitleReExtraction);
  useEffect(() => {
    triggerSubtitleReExtractionRef.current = triggerSubtitleReExtraction;
  }, [triggerSubtitleReExtraction]);

  // Recreate HLS session when audio/subtitle tracks change for HLS streams
  const lastHlsTrackSelectionRef = useRef<{ audio: number | null; subtitle: number | null }>({
    audio: null,
    subtitle: null,
  });

  useEffect(() => {
    // Only recreate session for HLS streams (HDR/DV content)
    // Check for either DV or HDR10 content since both use HLS streaming
    if (!isHlsStream || !sourcePath || !(routeHasDolbyVision || routeHasHDR10)) {
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

      // If only subtitle changed (not audio), don't recreate session
      // The sidecar VTT URL includes ?track=N parameter, so the SubtitleOverlay will fetch the new track
      // This avoids re-transcoding the entire video just to switch subtitle tracks
      if (!audioChanged && subtitleChanged) {
        console.log('[player] subtitle track changed - skipping session recreation (sidecar will load new track)', {
          from: lastHlsTrackSelectionRef.current.subtitle,
          to: selectedSubtitleTrackIndex,
        });
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
        const authToken = apiService.getAuthToken();
        const playlistWithKey = authToken
          ? `${playlistBase}${playlistBase.includes('?') ? '&' : '?'}token=${encodeURIComponent(authToken)}`
          : playlistBase;

        const sessionStart =
          typeof response.startOffset === 'number' && response.startOffset >= 0 ? response.startOffset : safeTarget;
        // Use actualStartOffset for subtitle sync (keyframe-aligned), fallback to sessionStart
        const actualSessionStart =
          typeof response.actualStartOffset === 'number' && response.actualStartOffset >= 0
            ? response.actualStartOffset
            : sessionStart;

        playbackOffsetRef.current = sessionStart;
        actualPlaybackOffsetRef.current = actualSessionStart; // For subtitle sync
        sessionBufferEndRef.current = sessionStart;
        currentTimeRef.current = sessionStart;
        setCurrentTime(sessionStart);
        // Mark that we're recreating HLS session to preserve track selections
        isRecreatingHlsSessionRef.current = true;
        skipTrackPreferencesRef.current = true;
        setCurrentMovieUrl(playlistWithKey);
        hasReceivedPlayerLoadRef.current = false;
        setHasStartedPlaying(false);
        // Reset progress event counter for fresh debug logs after track change
        progressEventCountRef.current = 0;
        firstProgressValueRef.current = null;

        if (typeof response.duration === 'number' && response.duration > 0) {
          updateDuration(response.duration, 'hls-session');
        }

        console.log('[player] successfully recreated HLS session with new track selection', {
          previousPlaybackTime: currentPlaybackTime,
          safeTarget,
          sessionStart,
          actualSessionStart,
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
    // When recreating HLS sessions (for track change or seek), preserve track selections
    // Only reset tracks when loading a completely different video
    const isRecreating = isRecreatingHlsSessionRef.current;
    if (isRecreating) {
      console.log('[player] HLS session recreation - preserving track selections', resolvedMovie);
      isRecreatingHlsSessionRef.current = false;
      // Still reset some state but preserve track selections
      durationRef.current = 0;
      setDuration(0);
      hasReceivedPlayerLoadRef.current = false;
      if (parsedDurationHint && parsedDurationHint > 0) {
        console.log('[player] using duration hint from URL params:', parsedDurationHint);
        setMetadataDuration(parsedDurationHint);
        updateDuration(parsedDurationHint, 'url-param-hint');
      } else {
        setMetadataDuration(0);
      }
      setHasStartedPlaying(false);
      if (usesSystemManagedControls) {
        setPaused(false);
      }
      return;
    }

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
        const movieToken = url.searchParams.get('token');
        if (movieToken && !apiService.getAuthToken()) {
          console.log('[player] hydrating auth token from playback url');
          apiService.setAuthToken(movieToken);
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
          const isHDR10 =
            (isPQ && isBT2020) || primaryVideo.hdrFormat === 'HDR10' || primaryVideo.hdrFormat === 'HDR10+';

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

          // Extract video/audio stream info for info modal
          const primaryAudio = metadata.audioStreams?.[0];
          const resolution =
            primaryVideo.width && primaryVideo.height ? `${primaryVideo.width}x${primaryVideo.height}` : undefined;
          const frameRate = primaryVideo.avgFrameRate
            ? (() => {
                // Parse frame rate like "24000/1001" or "24"
                const match = primaryVideo.avgFrameRate.match(/^(\d+)\/(\d+)$/);
                if (match) {
                  const fps = parseInt(match[1], 10) / parseInt(match[2], 10);
                  return `${fps.toFixed(3)} fps`;
                }
                const num = parseFloat(primaryVideo.avgFrameRate);
                return !isNaN(num) ? `${num.toFixed(3)} fps` : undefined;
              })()
            : undefined;
          const audioChannels =
            primaryAudio?.channelLayout || (primaryAudio?.channels ? `${primaryAudio.channels}ch` : undefined);
          setStreamInfo({
            resolution,
            videoBitrate: primaryVideo.bitRate,
            videoCodec: primaryVideo.codecLongName || primaryVideo.codecName,
            frameRate,
            audioCodec: primaryAudio?.codecLongName || primaryAudio?.codecName,
            audioChannels,
            audioBitrate: primaryAudio?.bitRate,
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

        // When recreating HLS sessions (seek/track change), skip re-applying language preferences
        // The current track selections should be preserved from the previous session
        const shouldSkipTrackPreferences = skipTrackPreferencesRef.current;
        if (shouldSkipTrackPreferences) {
          console.log('[player] HLS session recreation - skipping track preference application');
          skipTrackPreferencesRef.current = false;
          // Still build and set audio options so the track menu shows correctly
          const audioOptions = buildAudioTrackOptions(metadata.audioStreams ?? []);
          setAudioTrackOptions(audioOptions);
          // Also set subtitle options for the menu
          const subtitleOptions = buildSubtitleTrackOptions(
            metadata.subtitleStreams ?? [],
            metadata.selectedSubtitleIndex,
          );
          setSubtitleTrackOptions(subtitleOptions);
          // Skip the rest - track selections are already preserved from reset effect
          hasAppliedInitialTracksRef.current = true;
          return;
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
        const preferredAudioLanguage =
          userSettings?.playback?.preferredAudioLanguage ?? settings?.playback?.preferredAudioLanguage;
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

        // For non-HLS/SDR streams, skip setting subtitle options here - the backend probe effect
        // will set them with correct relative indices (0, 1, 2, ...) instead of absolute ffprobe
        // stream indices (11, 12, 13, ...). This prevents a race condition where metadata preload
        // overwrites the backend probe's correct indices.
        const shouldSetSubtitleOptions = isHlsStream || routeHasAnyHDR;

        if (shouldSetSubtitleOptions) {
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
          const preferredSubtitleLanguage =
            userSettings?.playback?.preferredSubtitleLanguage ?? settings?.playback?.preferredSubtitleLanguage;
          const preferredSubtitleModeRaw =
            userSettings?.playback?.preferredSubtitleMode ?? settings?.playback?.preferredSubtitleMode;
          const preferredSubtitleMode =
            preferredSubtitleModeRaw === 'on' ||
            preferredSubtitleModeRaw === 'off' ||
            preferredSubtitleModeRaw === 'forced-only'
              ? preferredSubtitleModeRaw
              : undefined;

          if (preferredSubtitleMode !== undefined) {
            const preferredSubtitleIndex = findSubtitleTrackByPreference(
              metadata.subtitleStreams ?? [],
              preferredSubtitleLanguage,
              preferredSubtitleMode,
            );
            if (preferredSubtitleIndex !== null || preferredSubtitleMode === 'off') {
              selectedSubtitleIndex = preferredSubtitleIndex ?? undefined;
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
        } else {
          console.log('[player] skipping subtitle options from metadata - backend probe will set correct indices');
        }
      } catch (error) {
        console.warn('[player] failed to preload video metadata', error);
      }
    };

    fetchMetadata();
    return () => {
      isMounted = false;
    };
  }, [resolvedMovie, updateDuration, sourcePath, settings, isHlsStream, routeHasAnyHDR]);

  // Fetch series details for episode navigation (only for series content)
  useEffect(() => {
    const isSeries = mediaType === 'episode' || mediaType === 'series' || mediaType === 'tv' || mediaType === 'show';
    if (!isSeries) {
      setAllEpisodes([]);
      return;
    }

    // Need at least one identifier to fetch series
    if (!titleId && !tvdbId && !imdbId) {
      console.log('[player] no series identifier available for episode navigation');
      return;
    }

    let isMounted = true;

    const fetchSeriesEpisodes = async () => {
      try {
        console.log('[player] fetching series details for episode navigation', {
          titleId,
          tvdbId,
          imdbId,
          name: cleanSeriesTitle,
        });
        const details = await apiService.getSeriesDetails({
          titleId: titleId || undefined,
          tvdbId: tvdbId || undefined,
          name: cleanSeriesTitle || undefined,
          // imdbId is not supported by getSeriesDetails, but titleId should cover most cases
        });

        if (!isMounted) return;

        // Flatten all episodes from all seasons
        const episodes: SeriesEpisode[] = [];
        for (const season of details.seasons || []) {
          for (const episode of season.episodes || []) {
            episodes.push(episode);
          }
        }

        // Sort by season and episode number
        episodes.sort((a, b) => {
          if (a.seasonNumber !== b.seasonNumber) {
            return a.seasonNumber - b.seasonNumber;
          }
          return a.episodeNumber - b.episodeNumber;
        });

        console.log('[player] loaded episodes for navigation', { count: episodes.length });
        setAllEpisodes(episodes);
      } catch (error) {
        console.warn('[player] failed to fetch series details for episode navigation', error);
      }
    };

    fetchSeriesEpisodes();

    return () => {
      isMounted = false;
    };
  }, [mediaType, titleId, tvdbId, imdbId, cleanSeriesTitle]);

  // Find current episode index and determine if prev/next exist
  const currentEpisodeIndex = useMemo(() => {
    if (!allEpisodes.length || !seasonNumber || !episodeNumber) return -1;
    return allEpisodes.findIndex((ep) => ep.seasonNumber === seasonNumber && ep.episodeNumber === episodeNumber);
  }, [allEpisodes, seasonNumber, episodeNumber]);

  const hasPreviousEpisode = currentEpisodeIndex > 0;
  const hasNextEpisode = currentEpisodeIndex >= 0 && currentEpisodeIndex < allEpisodes.length - 1;

  // Keep ref updated for use in callbacks defined earlier in the component
  hasNextEpisodeRef.current = hasNextEpisode;

  // Reset prequeue state when episode changes
  useEffect(() => {
    hasTriggeredNextEpisodePrequeue.current = false;
    nextEpisodePrequeueRef.current = {
      prequeueId: null,
      targetEpisode: null,
      statusResponse: null,
      isShuffleEpisode: false,
    };
    setNextEpisodePrequeueReady(false);
    nextEpisodePrequeuePollingRef.current = false;
  }, [seasonNumber, episodeNumber]);

  // Navigate to previous/next episode
  const navigateToEpisode = useCallback(
    (episode: SeriesEpisode, usePrequeue: boolean = false) => {
      const seriesId = titleId || imdbId || tvdbId;
      if (!seriesId) {
        console.warn('[player] no series identifier for episode navigation');
        return;
      }

      console.log('[player] navigating to episode', {
        season: episode.seasonNumber,
        episode: episode.episodeNumber,
        shuffleMode,
        usePrequeue,
      });

      // Check if we have prequeue data for this episode
      const prequeueData = nextEpisodePrequeueRef.current;
      const hasMatchingPrequeue =
        usePrequeue &&
        prequeueData?.prequeueId &&
        prequeueData.targetEpisode?.seasonNumber === episode.seasonNumber &&
        prequeueData.targetEpisode?.episodeNumber === episode.episodeNumber;

      if (hasMatchingPrequeue) {
        // Pass prequeue data to the navigation
        playbackNavigation.setNextEpisode(
          seriesId,
          episode.seasonNumber,
          episode.episodeNumber,
          true,
          prequeueData.isShuffleEpisode ? true : shuffleMode,
          prequeueData.prequeueId ?? undefined,
          prequeueData.statusResponse ?? undefined,
        );
      } else {
        // No prequeue data, normal navigation
        playbackNavigation.setNextEpisode(seriesId, episode.seasonNumber, episode.episodeNumber, true, shuffleMode);
      }

      // Mark that user explicitly navigated to prevent unmount cleanup from overwriting
      userNavigatedToEpisodeRef.current = true;

      // Navigate back to details page - it will auto-play the episode
      router.back();
    },
    [titleId, imdbId, tvdbId, shuffleMode],
  );

  const handlePreviousEpisode = useCallback(() => {
    if (!hasPreviousEpisode) return;
    const previousEpisode = allEpisodes[currentEpisodeIndex - 1];
    if (previousEpisode) {
      navigateToEpisode(previousEpisode);
    }
  }, [hasPreviousEpisode, allEpisodes, currentEpisodeIndex, navigateToEpisode]);

  const handleNextEpisode = useCallback(() => {
    // Check if we have a prequeued episode to use
    const prequeueData = nextEpisodePrequeueRef.current;
    if (prequeueData?.prequeueId && prequeueData.targetEpisode) {
      // Use the prequeued episode (works for both shuffle and sequential)
      const prequeuedEpisode: SeriesEpisode = {
        id: '',
        seasonNumber: prequeueData.targetEpisode.seasonNumber,
        episodeNumber: prequeueData.targetEpisode.episodeNumber,
        name: '',
        overview: '',
      };
      navigateToEpisode(prequeuedEpisode, true);
      return;
    }

    // No prequeue - fall back to existing logic
    // Shuffle mode: pick a random episode (different from current)
    if (shuffleMode && allEpisodes.length > 1) {
      let randomIndex: number;
      let nextEpisode: SeriesEpisode;
      do {
        randomIndex = Math.floor(Math.random() * allEpisodes.length);
        nextEpisode = allEpisodes[randomIndex];
      } while (nextEpisode.seasonNumber === seasonNumber && nextEpisode.episodeNumber === episodeNumber);
      navigateToEpisode(nextEpisode);
      return;
    }
    // Sequential mode
    if (!hasNextEpisode) return;
    const nextEpisode = allEpisodes[currentEpisodeIndex + 1];
    if (nextEpisode) {
      navigateToEpisode(nextEpisode);
    }
  }, [hasNextEpisode, allEpisodes, currentEpisodeIndex, navigateToEpisode, shuffleMode, seasonNumber, episodeNumber]);

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
      {/* On Android, don't apply top safe area to the container (which would shift the video when status bar toggles).
          Instead, safe area is applied to the controls overlay only. On iOS, apply top edge in portrait. */}
      <FixedSafeAreaView
        style={styles.safeArea}
        edges={Platform.OS === 'android' && !Platform.isTV ? [] : isPortrait ? ['top'] : []}
      >
        <View style={styles.container} nativeID="player-fullscreen-root">
          <View ref={videoWrapperRef} style={styles.videoWrapper}>
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
              // Always disable VLC's built-in subtitles - we use SubtitleOverlay for consistent sizing
              selectedSubtitleTrackIndex={undefined}
              onTracksAvailable={handleTracksAvailable}
              // Use react-native-video for HDR content (VLC tone-maps to SDR)
              forceRnvPlayer={routeHasAnyHDR}
              forceNativeFullscreen={Platform.OS !== 'web' && routeHasAnyHDR}
              onVideoSize={(width, height) => setVideoSize({ width, height })}
              nowPlaying={{
                title: episodeName || title || undefined,
                subtitle: seriesTitle || undefined,
                imageUri: headerImage || undefined,
              }}
              subtitleSize={userSettings?.playback?.subtitleSize ?? settings?.playback?.subtitleSize ?? 1.0}
              mediaType={mediaType}
            />
          </View>

          {/* Black overlay that stays visible until playback starts */}
          {!hasStartedPlaying && (
            <View style={styles.blackOverlay} pointerEvents="none" renderToHardwareTextureAndroid={true} />
          )}

          {/* Pause teardown overlay - shows last frame (or poster) when player is torn down after extended pause */}
          {/* This prevents AVPlayer HLS timeout by proactively stopping the player */}
          {/* Tapping the overlay shows controls so user can press play to resume */}
          {pauseTeardownActive && (
            <Pressable
              style={styles.pauseTeardownOverlay}
              onPress={handleVideoInteract}
            >
              {pauseTeardownFrame ? (
                <Image
                  source={{ uri: pauseTeardownFrame }}
                  style={styles.pauseTeardownImage}
                  resizeMode="cover"
                />
              ) : headerImage ? (
                <Image
                  source={{ uri: headerImage }}
                  style={styles.pauseTeardownImage}
                  resizeMode="contain"
                />
              ) : (
                <View style={styles.pauseTeardownPlaceholder} />
              )}
            </Pressable>
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
          {/* Use actualPlaybackOffsetRef (keyframe-aligned) for accurate subtitle sync after seeks */}
          {/* User offset is also applied (negated: positive = later subtitles = decrease adjustedTime) */}
          {isHlsStream && sidecarSubtitleUrl && (
            <SubtitleOverlay
              vttUrl={sidecarSubtitleUrl}
              currentTime={currentTime}
              currentTimeRef={currentTimeRef}
              timeOffset={-actualPlaybackOffsetRef.current - subtitleOffset}
              enabled={selectedSubtitleTrackIndex !== null && selectedSubtitleTrackIndex >= 0}
              videoWidth={videoSize?.width}
              videoHeight={videoSize?.height}
              sizeScale={userSettings?.playback?.subtitleSize ?? settings?.playback?.subtitleSize ?? 1.0}
              controlsVisible={controlsVisible}
            />
          )}

          {/* Extracted subtitle overlay for non-HLS streams (VLC direct playback) */}
          {/* Uses standalone subtitle extraction endpoint to convert embedded subs to VTT */}
          {/* Use sdrFirstCueTimeRef for sync (mirrors actualPlaybackOffsetRef for HLS) */}
          {/* timeOffset: -firstCueTime to align VTT cues with VLC position, -subtitleOffset for user adjustment */}
          {!isHlsStream && extractedSubtitleUrl && selectedSubtitleTrackId !== 'external' && (
            <SubtitleOverlay
              vttUrl={extractedSubtitleUrl}
              currentTime={currentTime}
              currentTimeRef={currentTimeRef}
              timeOffset={-sdrFirstCueTimeRef.current - subtitleOffset}
              enabled={selectedSubtitleTrackIndex !== null && selectedSubtitleTrackIndex >= 0}
              videoWidth={videoSize?.width}
              videoHeight={videoSize?.height}
              sizeScale={userSettings?.playback?.subtitleSize ?? settings?.playback?.subtitleSize ?? 1.0}
              controlsVisible={controlsVisible}
              onCuesRangeChange={handleSubtitleCuesRangeChange}
            />
          )}

          {/* External subtitle overlay from OpenSubtitles/Subliminal search */}
          {/* timeOffset is negated: positive user offset = later subtitles = decrease adjustedTime */}
          {externalSubtitleUrl && selectedSubtitleTrackId === 'external' && (
            <SubtitleOverlay
              vttUrl={externalSubtitleUrl}
              currentTime={currentTime}
              currentTimeRef={currentTimeRef}
              timeOffset={-subtitleOffset}
              enabled={true}
              videoWidth={videoSize?.width}
              videoHeight={videoSize?.height}
              sizeScale={userSettings?.playback?.subtitleSize ?? settings?.playback?.subtitleSize ?? 1.0}
              controlsVisible={controlsVisible}
            />
          )}

          {(() => {
            const hasDuration = Number.isFinite(duration) && duration > 0;
            const hasPlaybackContext = hasDuration || currentTime > 0 || isVideoBuffering || paused;
            const isTVSeeking = isTvPlatform && seekIndicatorAmount !== 0;
            const shouldRenderControls =
              !usesSystemManagedControls &&
              (controlsVisible || isModalOpen || isTVSeeking) &&
              (hasPlaybackContext || Platform.isTV);

            return shouldRenderControls ? (
              isTvPlatform ? (
                <TVControlsModal
                  visible={controlsVisible || isTVSeeking}
                  onRequestClose={() => {
                    // Close subtitle search modal first if open, otherwise hide controls
                    if (subtitleSearchModalVisible) {
                      handleCloseSubtitleSearch();
                    } else {
                      hideControls({ immediate: true });
                    }
                  }}
                  isChildModalOpen={isModalOpen}
                  isSeeking={isTVSeeking}
                >
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
                    <Animated.View
                      style={tvOverlayAnimatedStyle}
                      pointerEvents="box-none"
                      renderToHardwareTextureAndroid={true}
                    >
                      <View
                        style={styles.overlayContent}
                        pointerEvents="box-none"
                        renderToHardwareTextureAndroid={true}
                      >
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
                                // Clear external subtitle when switching to embedded track
                                if (id !== 'external') {
                                  setExternalSubtitleUrl(null);
                                }
                              }}
                              onSearchSubtitles={handleOpenSubtitleSearch}
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
                              streamInfo={fullStreamInfo}
                              hasPreviousEpisode={mediaType === 'episode' ? hasPreviousEpisode : undefined}
                              hasNextEpisode={mediaType === 'episode' ? hasNextEpisode : undefined}
                              onPreviousEpisode={mediaType === 'episode' ? handlePreviousEpisode : undefined}
                              onNextEpisode={mediaType === 'episode' ? handleNextEpisode : undefined}
                              nextEpisodePrequeueReady={mediaType === 'episode' ? nextEpisodePrequeueReady : undefined}
                              showSubtitleOffset={isUsingSidecarSubtitles}
                              subtitleOffset={subtitleOffset}
                              onSubtitleOffsetEarlier={handleSubtitleOffsetEarlier}
                              onSubtitleOffsetLater={handleSubtitleOffsetLater}
                              seekBackwardSeconds={seekBackwardSeconds}
                              seekForwardSeconds={seekForwardSeconds}
                              shuffleMode={shuffleMode}
                            />
                          </SpatialNavigationNode>
                        </View>
                      </View>
                    </Animated.View>
                  </ControlsContainerComponent>
                  {/* Render SubtitleSearchModal inside TVControlsModal on TV for proper modal stacking */}
                  {subtitleSearchModalVisible && (
                    <SubtitleSearchModal
                      visible={subtitleSearchModalVisible}
                      onClose={handleCloseSubtitleSearch}
                      onSelectSubtitle={handleSelectExternalSubtitle}
                      searchResults={subtitleSearchResults}
                      isLoading={subtitleSearchLoading}
                      error={subtitleSearchError}
                      onSearch={handleSubtitleSearch}
                      currentLanguage={subtitleSearchLanguage}
                      mediaReleaseName={releaseName}
                    />
                  )}
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
                  <View
                    style={[
                      styles.overlayContent,
                      // On Android mobile, apply safe area padding to controls overlay (not the container)
                      // so the video doesn't shift when status bar toggles
                      Platform.OS === 'android' && !Platform.isTV && isPortrait && { paddingTop: safeAreaInsets.top },
                    ]}
                    pointerEvents="box-none"
                  >
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
                        onShowStreamInfo={() => setMobileStreamInfoVisible(true)}
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
                            // Clear external subtitle when switching to embedded track
                            if (id !== 'external') {
                              setExternalSubtitleUrl(null);
                            }
                          }}
                          onSearchSubtitles={handleOpenSubtitleSearch}
                          onModalStateChange={handleModalStateChange}
                          onScrubStart={handleSeekBarScrubStart}
                          onScrubEnd={handleSeekBarScrubEnd}
                          isLiveTV={isLiveTV}
                          hasStartedPlaying={hasStartedPlaying}
                          onSkipBackward={handleSkipBackward}
                          onSkipForward={handleSkipForward}
                          onFocusChange={handleFocusChange}
                          streamInfo={fullStreamInfo}
                          hasPreviousEpisode={mediaType === 'episode' ? hasPreviousEpisode : undefined}
                          hasNextEpisode={mediaType === 'episode' ? hasNextEpisode : undefined}
                          onPreviousEpisode={mediaType === 'episode' ? handlePreviousEpisode : undefined}
                          onNextEpisode={mediaType === 'episode' ? handleNextEpisode : undefined}
                          nextEpisodePrequeueReady={mediaType === 'episode' ? nextEpisodePrequeueReady : undefined}
                          showSubtitleOffset={isUsingSidecarSubtitles}
                          subtitleOffset={subtitleOffset}
                          onSubtitleOffsetEarlier={handleSubtitleOffsetEarlier}
                          onSubtitleOffsetLater={handleSubtitleOffsetLater}
                          seekBackwardSeconds={seekBackwardSeconds}
                          seekForwardSeconds={seekForwardSeconds}
                          shuffleMode={shuffleMode}
                        />
                      </SpatialNavigationNode>
                    </View>
                  </View>
                </ControlsContainerComponent>
              )
            ) : null;
          })()}

          {/* Mobile stream info modal (TV platforms use the modal in Controls component) */}
          {!isTvPlatform && fullStreamInfo && (
            <StreamInfoModal
              visible={mobileStreamInfoVisible}
              info={fullStreamInfo}
              onClose={() => setMobileStreamInfoVisible(false)}
            />
          )}

          {/* External subtitle search modal - only render here on non-TV (TV renders inside TVControlsModal) */}
          {!isTvPlatform && (
            <SubtitleSearchModal
              visible={subtitleSearchModalVisible}
              onClose={handleCloseSubtitleSearch}
              onSelectSubtitle={handleSelectExternalSubtitle}
              searchResults={subtitleSearchResults}
              isLoading={subtitleSearchLoading}
              error={subtitleSearchError}
              onSearch={handleSubtitleSearch}
              currentLanguage={subtitleSearchLanguage}
              mediaReleaseName={releaseName}
            />
          )}

          {debugOverlayEnabled && (
            <View style={styles.debugOverlay} pointerEvents="box-none">
              <View style={styles.debugCard} pointerEvents="auto">
                <ScrollView
                  style={styles.debugScroll}
                  contentContainerStyle={styles.debugScrollContent}
                  showsVerticalScrollIndicator
                  contentInsetAdjustmentBehavior="never"
                  automaticallyAdjustContentInsets={false}
                >
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
    pauseTeardownOverlay: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: '#000000',
      alignItems: 'center',
      justifyContent: 'center',
      zIndex: 0, // Below controls (zIndex: 1) so controls are visible and interactive
    },
    pauseTeardownImage: {
      width: '100%',
      height: '100%',
    },
    pauseTeardownPlaceholder: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: '#000000',
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
