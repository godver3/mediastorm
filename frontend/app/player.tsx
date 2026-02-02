import { clearMemoryCache as clearImageMemoryCache } from '@/components/Image';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import LoadingIndicator from '@/components/LoadingIndicator';
import Controls from '@/components/player/Controls';
import ExitButton from '@/components/player/ExitButton';
import TVControlsModal from '@/components/player/TVControlsModal';
import { isMobileWeb } from '@/components/player/isMobileWeb';
import MediaInfoDisplay from '@/components/player/MediaInfoDisplay';
import { StreamInfoModal } from '@/components/player/StreamInfoModal';
import SubtitleOverlay, { SubtitleCuesRange, SubtitleDebugInfo } from '@/components/player/SubtitleOverlay';
import { SubtitleSearchModal } from '@/components/player/SubtitleSearchModal';
import { SubtitleStatusOverlay, type AutoSubtitleStatus } from '@/components/player/SubtitleStatusOverlay';
import type { SubtitleSearchResult } from '@/services/api';
import VideoPlayer, {
  VideoPlayerHandle,
  type TrackInfo,
  type VideoImplementation,
  type VideoProgressMeta,
} from '@/components/player/VideoPlayer';
import { createPlayerStyles } from '@/styles/player-styles';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import Constants from 'expo-constants';
import { activateKeepAwakeAsync, deactivateKeepAwake } from 'expo-keep-awake';
import { LinearGradient } from 'expo-linear-gradient';
import { router, Stack, useLocalSearchParams } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Animated, AppState, BackHandler, Image, Platform, Pressable, ScrollView, Text, View } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { useChannelEPG } from '@/hooks/useChannelEPG';
import { isTablet } from '@/theme/tokens/tvScale';

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
  ContentPreference,
} from '@/services/api';
import apiService from '@/services/api';
import { playbackNavigation } from '@/services/playback-navigation';
import { findAudioTrackByLanguage, findSubtitleTrackByPreference } from '@/app/details/track-selection';
import { useHlsSession, type HlsSessionResponse } from '@/hooks/useHlsSession';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { updateNowPlaying, updatePlaybackPosition, clearNowPlaying, setupRemoteCommands } from 'now-playing-manager';

// Safe import for pip-manager (Android mobile only, may not be available in dev builds)
let enableAutoPip: (() => void) | null = null;
let disableAutoPip: (() => void) | null = null;
if (Platform.OS === 'android' && !Platform.isTV) {
  try {
    // eslint-disable-next-line @typescript-eslint/no-var-requires
    const pipManager = require('pip-manager');
    console.log('[PiP Debug] pip-manager module loaded:', pipManager);
    console.log('[PiP Debug] enableAutoPip:', typeof pipManager.enableAutoPip);
    console.log('[PiP Debug] disableAutoPip:', typeof pipManager.disableAutoPip);
    enableAutoPip = pipManager.enableAutoPip;
    disableAutoPip = pipManager.disableAutoPip;
  } catch (e) {
    console.log('[PiP Debug] Failed to load pip-manager:', e);
    // Module not available - auto-PiP will be disabled
  }
}
import {
  type ConsoleLevel,
  type DebugLogEntry,
  type TrackOption,
  type PlayerParams,
  parseBooleanParam,
  SUBTITLE_OFF_OPTION,
  toTitleCase,
  stripEpisodeCodeSuffix,
  formatLanguage,
  buildAudioTrackOptions,
  buildSubtitleTrackOptions,
  resolveSelectedTrackId,
} from '@/utils/player-helpers';
import { selectBestSubtitle } from '@/utils/subtitle-helpers';

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
    actualStartOffset: actualStartOffsetParam,
    titleId: titleIdParam,
    imdbId: imdbIdParam,
    tvdbId: tvdbIdParam,
    shuffleMode: shuffleModeParam,
    preExtractedSubtitles: preExtractedSubtitlesParam,
    subtitleDebug: subtitleDebugParam,
    preselectedAudioTrack: preselectedAudioTrackParam,
    preselectedSubtitleTrack: preselectedSubtitleTrackParam,
    passthroughName: passthroughNameParam,
    passthroughDescription: passthroughDescriptionParam,
    tvgId: tvgIdParam,
  } = useLocalSearchParams<PlayerParams>();
  const resolvedMovie = useMemo(() => {
    const movieParam = Array.isArray(movie) ? movie[0] : movie;
    if (!movieParam) {
      return movieParam;
    }

    // Expo Router automatically decodes URL params, but we need to re-encode
    // special characters for proper URL parsing.
    // URLSearchParams doesn't encode semicolons which breaks some URL parsers.
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
  // Note: hasAnyHDR (below) also includes detected HDR from video metadata for player routing
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
  const passthroughName = useMemo(() => {
    const raw = Array.isArray(passthroughNameParam) ? passthroughNameParam[0] : passthroughNameParam;
    return raw || undefined;
  }, [passthroughNameParam]);
  const passthroughDescription = useMemo(() => {
    const raw = Array.isArray(passthroughDescriptionParam)
      ? passthroughDescriptionParam[0]
      : passthroughDescriptionParam;
    return raw || undefined;
  }, [passthroughDescriptionParam]);
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
  // Keyframe-aligned start offset for subtitle sync (passed from prequeue HLS session creation)
  const initialActualStartOffset = useMemo(() => {
    const raw = Array.isArray(actualStartOffsetParam) ? actualStartOffsetParam[0] : actualStartOffsetParam;
    if (!raw) {
      return undefined; // undefined means not provided - use initialStartOffset
    }
    const parsed = Number(raw);
    return Number.isFinite(parsed) && parsed >= 0 ? parsed : undefined;
  }, [actualStartOffsetParam]);

  // Parse pre-extracted subtitle sessions from route params
  const preExtractedSubtitles = useMemo((): SubtitleSessionInfo[] | null => {
    const raw = Array.isArray(preExtractedSubtitlesParam) ? preExtractedSubtitlesParam[0] : preExtractedSubtitlesParam;
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

  // Parse preselected track indices (tracks already baked into HLS session by prequeue)
  const preselectedAudioTrack = useMemo(() => {
    const raw = Array.isArray(preselectedAudioTrackParam) ? preselectedAudioTrackParam[0] : preselectedAudioTrackParam;
    if (!raw) {
      return undefined;
    }
    const parsed = Number(raw);
    return Number.isFinite(parsed) && parsed >= 0 ? parsed : undefined;
  }, [preselectedAudioTrackParam]);

  const preselectedSubtitleTrack = useMemo(() => {
    const raw = Array.isArray(preselectedSubtitleTrackParam)
      ? preselectedSubtitleTrackParam[0]
      : preselectedSubtitleTrackParam;
    if (!raw) {
      return undefined;
    }
    const parsed = Number(raw);
    return Number.isFinite(parsed) && parsed >= 0 ? parsed : undefined;
  }, [preselectedSubtitleTrackParam]);

  const [sourcePath, setSourcePath] = useState<string | undefined>(initialSourcePath);
  const safeAreaInsets = useSafeAreaInsets();
  const { width: windowWidth, height: windowHeight } = useTVDimensions();
  const isPortrait = windowHeight >= windowWidth;

  const shouldPreferSystemPlayer = useMemo(() => parseBooleanParam(preferSystemPlayerParam), [preferSystemPlayerParam]);
  const isLiveTV = useMemo(() => shouldPreferSystemPlayer, [shouldPreferSystemPlayer]);

  // Parse tvgId for EPG lookup (live TV only)
  const tvgId = useMemo(() => {
    const raw = Array.isArray(tvgIdParam) ? tvgIdParam[0] : tvgIdParam;
    return raw?.trim() || undefined;
  }, [tvgIdParam]);

  // Fetch EPG data for live TV channels
  const { getProgram, fetchEPGForChannels, isEnabled: isEPGEnabled } = useChannelEPG();
  const epgData = useMemo(() => (tvgId ? getProgram(tvgId) : undefined), [tvgId, getProgram]);
  const currentProgram = epgData?.current;
  const nextProgram = epgData?.next;

  // Fetch EPG when player loads with a tvgId
  useEffect(() => {
    if (isLiveTV && tvgId && isEPGEnabled) {
      void fetchEPGForChannels([tvgId]);
    }
  }, [isLiveTV, tvgId, isEPGEnabled, fetchEPGForChannels]);

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
  const pausedRef = useRef<boolean>(paused);
  const [isPipActive, setIsPipActive] = useState<boolean>(false);
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
  // Double-tap gesture handling for mobile skip forward/backward
  const lastTapRef = useRef<{ time: number; side: 'left' | 'right' } | null>(null);
  const [flashSkipButton, setFlashSkipButton] = useState<'backward' | 'forward' | null>(null);
  const DOUBLE_TAP_DELAY = 300; // ms
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
  const externalSubtitleUrlRef = useRef<string | null>(null);
  const [subtitleSearchResults, setSubtitleSearchResults] = useState<SubtitleSearchResult[]>([]);
  const [subtitleSearchLoading, setSubtitleSearchLoading] = useState<boolean>(false);
  const [subtitleSearchError, setSubtitleSearchError] = useState<string | null>(null);
  const [subtitleSearchLanguage, setSubtitleSearchLanguage] = useState<string>('en');
  const subtitleSearchLanguageInitializedRef = useRef(false);
  // Track the language of the current search request to ignore stale results
  const currentSubtitleSearchLanguageRef = useRef<string | null>(null);

  // Initialize subtitle search language from user's preferred subtitle language
  // Settings use ISO 639-2 (3-letter) codes, but subtitle search uses ISO 639-1 (2-letter) codes
  useEffect(() => {
    if (subtitleSearchLanguageInitializedRef.current) return;
    const preferredLang =
      userSettings?.playback?.preferredSubtitleLanguage || settings?.playback?.preferredSubtitleLanguage;
    if (preferredLang) {
      // Map ISO 639-2 (3-letter) to ISO 639-1 (2-letter) codes
      const iso639_2_to_1: Record<string, string> = {
        eng: 'en',
        spa: 'es',
        fra: 'fr',
        fre: 'fr',
        deu: 'de',
        ger: 'de',
        ita: 'it',
        por: 'pt',
        nld: 'nl',
        dut: 'nl',
        pol: 'pl',
        rus: 'ru',
        jpn: 'ja',
        kor: 'ko',
        zho: 'zh',
        chi: 'zh',
        ara: 'ar',
        heb: 'he',
        swe: 'sv',
        nor: 'no',
        nob: 'no',
        nno: 'no',
        dan: 'da',
        fin: 'fi',
        hrv: 'hr',
        srp: 'sr',
        bos: 'bs',
      };
      const twoLetterCode = iso639_2_to_1[preferredLang.toLowerCase()] || preferredLang;
      setSubtitleSearchLanguage(twoLetterCode);
      subtitleSearchLanguageInitializedRef.current = true;
    }
  }, [userSettings?.playback?.preferredSubtitleLanguage, settings?.playback?.preferredSubtitleLanguage]);

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
  // Debug info from SubtitleOverlay for troubleshooting sync issues
  const [subtitleDebugInfo, setSubtitleDebugInfo] = useState<SubtitleDebugInfo | null>(null);
  // Backend-probed subtitle tracks (used for non-HLS streams to get accurate track indices)
  const [backendSubtitleTracks, setBackendSubtitleTracks] = useState<Array<{
    index: number;
    language: string;
    title: string;
    codec: string;
    forced: boolean;
  }> | null>(null);
  // Raw audio/subtitle stream metadata (for per-content language preference saving)
  const [audioStreamMetadata, setAudioStreamMetadata] = useState<AudioStreamMetadata[] | null>(null);
  const [subtitleStreamMetadata, setSubtitleStreamMetadata] = useState<SubtitleStreamMetadata[] | null>(null);
  // Per-content language preference (loaded from backend, overrides user settings)
  const [contentPreference, setContentPreference] = useState<ContentPreference | null>(null);
  const [debugEntries, setDebugEntries] = useState<DebugLogEntry[]>([]);
  // Auto-subtitle search state (automatic subtitle search when no embedded tracks match preference)
  const [autoSubtitleStatus, setAutoSubtitleStatus] = useState<AutoSubtitleStatus>('idle');
  const [autoSubtitleMessage, setAutoSubtitleMessage] = useState<string | null>(null);
  const autoSubtitleTriggeredRef = useRef(false);
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
      // Track this search request to ignore stale results if user switches language mid-search
      currentSubtitleSearchLanguageRef.current = language;
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
        // Only update results if this is still the current search (user hasn't switched language)
        if (currentSubtitleSearchLanguageRef.current === language) {
          console.log('[player] subtitle search results:', results.length);
          setSubtitleSearchResults(results);
          setSubtitleSearchLoading(false);
        } else {
          console.log('[player] ignoring stale subtitle search results for', language);
        }
      } catch (error) {
        // Only update error if this is still the current search
        if (currentSubtitleSearchLanguageRef.current === language) {
          console.error('[player] subtitle search error:', error);
          setSubtitleSearchError('Failed to search for subtitles');
          setSubtitleSearchResults([]);
          setSubtitleSearchLoading(false);
        }
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
      // Update ref synchronously before setting state to prevent race conditions
      externalSubtitleUrlRef.current = url;
      setExternalSubtitleUrl(url);
      // Set subtitle track to a special external ID
      setSelectedSubtitleTrackId('external');
      handleCloseSubtitleSearch();
    },
    [imdbId, title, seriesTitle, year, seasonNumber, episodeNumber, subtitleSearchLanguage, handleCloseSubtitleSearch],
  );

  // Helper to check if a language code is English
  const isEnglishLanguage = useCallback((lang: string) => {
    const normalized = lang.toLowerCase();
    return ['en', 'eng', 'english'].includes(normalized);
  }, []);

  // Auto-subtitle search: automatically search for subtitles when no embedded tracks match preference
  // Fallback order: preferred language online -> English embedded -> none
  const performAutoSubtitleSearch = useCallback(
    async (language: string) => {
      console.log('[player] starting auto-subtitle search for language:', language);
      setAutoSubtitleStatus('searching');
      setAutoSubtitleMessage('Searching for subtitles...');

      try {
        const results = await apiService.searchSubtitles({
          imdbId: imdbId || undefined,
          title: title || seriesTitle || undefined,
          year: year || undefined,
          season: seasonNumber,
          episode: episodeNumber,
          language,
        });

        console.log('[player] auto-subtitle search returned', results.length, 'results');

        if (results.length === 0) {
          // If no results and preferred language is not English, try English embedded track
          if (!isEnglishLanguage(language)) {
            console.log('[player] no results for preferred language, trying English embedded fallback');

            if (subtitleStreamMetadata && subtitleStreamMetadata.length > 0) {
              const englishEmbeddedIndex = findSubtitleTrackByPreference(subtitleStreamMetadata, 'eng', 'on');
              if (englishEmbeddedIndex !== null) {
                console.log('[player] found English embedded track:', englishEmbeddedIndex);
                setSelectedSubtitleTrackId(String(englishEmbeddedIndex));
                setAutoSubtitleStatus('ready');
                setAutoSubtitleMessage('Using English subtitles');
                setTimeout(() => setAutoSubtitleMessage(null), 2000);
                return;
              }
            }
          }

          setAutoSubtitleStatus('no-results');
          setAutoSubtitleMessage('No subtitles found');
          setTimeout(() => setAutoSubtitleMessage(null), 3000);
          return;
        }

        setAutoSubtitleStatus('downloading');
        setAutoSubtitleMessage('Downloading subtitles...');

        const bestMatch = selectBestSubtitle(results, releaseName);
        console.log('[player] auto-subtitle selected best match:', bestMatch.release);

        const url = apiService.getSubtitleDownloadUrl({
          subtitleId: bestMatch.id,
          provider: bestMatch.provider,
          imdbId: imdbId || undefined,
          title: title || seriesTitle || undefined,
          year: year || undefined,
          season: seasonNumber,
          episode: episodeNumber,
          language,
        });

        // Update ref synchronously before setting state to prevent race conditions
        externalSubtitleUrlRef.current = url;
        setExternalSubtitleUrl(url);
        setSelectedSubtitleTrackId('external');
        setAutoSubtitleStatus('ready');
        setAutoSubtitleMessage('Subtitles ready');
        console.log('[player] external subtitle URL set:', url);
        setTimeout(() => setAutoSubtitleMessage(null), 2000);
      } catch (error) {
        console.error('[player] auto-subtitle search error:', error);

        // If search failed and preferred language is not English, try English embedded track
        if (!isEnglishLanguage(language)) {
          console.log('[player] search failed, trying English embedded fallback');

          if (subtitleStreamMetadata && subtitleStreamMetadata.length > 0) {
            const englishEmbeddedIndex = findSubtitleTrackByPreference(subtitleStreamMetadata, 'eng', 'on');
            if (englishEmbeddedIndex !== null) {
              console.log('[player] found English embedded track:', englishEmbeddedIndex);
              setSelectedSubtitleTrackId(String(englishEmbeddedIndex));
              setAutoSubtitleStatus('ready');
              setAutoSubtitleMessage('Using English subtitles');
              setTimeout(() => setAutoSubtitleMessage(null), 2000);
              return;
            }
          }
        }

        setAutoSubtitleStatus('failed');
        setAutoSubtitleMessage('Subtitle search failed');
        setTimeout(() => setAutoSubtitleMessage(null), 3000);
      }
    },
    [
      imdbId,
      title,
      seriesTitle,
      year,
      seasonNumber,
      episodeNumber,
      releaseName,
      isEnglishLanguage,
      subtitleStreamMetadata,
    ],
  );

  // Check conditions and trigger auto-subtitle search if needed
  const triggerAutoSubtitleSearchIfNeeded = useCallback(() => {
    if (autoSubtitleTriggeredRef.current) {
      console.log('[player] auto-subtitle search already triggered, skipping');
      return;
    }

    const subtitleMode = userSettings?.playback?.preferredSubtitleMode ?? settings?.playback?.preferredSubtitleMode;
    const subtitleLang =
      userSettings?.playback?.preferredSubtitleLanguage ?? settings?.playback?.preferredSubtitleLanguage;

    // Don't auto-search if user has subtitles disabled
    if (subtitleMode === 'off') {
      console.log('[player] auto-subtitle search skipped: subtitle mode is off');
      return;
    }

    // Don't auto-search if no preferred language is set
    if (!subtitleLang) {
      console.log('[player] auto-subtitle search skipped: no preferred language set');
      return;
    }

    // Check if OpenSubtitles credentials are configured
    const hasCredentials = settings?.subtitles?.openSubtitlesUsername && settings?.subtitles?.openSubtitlesPassword;
    if (!hasCredentials) {
      console.log('[player] auto-subtitle search skipped: no OpenSubtitles credentials configured');
      return;
    }

    console.log('[player] triggering auto-subtitle search for language:', subtitleLang);
    autoSubtitleTriggeredRef.current = true;
    performAutoSubtitleSearch(subtitleLang);
  }, [settings, userSettings, performAutoSubtitleSearch]);

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

  // Keep ref in sync with state for use in closures
  useEffect(() => {
    externalSubtitleUrlRef.current = externalSubtitleUrl;
  }, [externalSubtitleUrl]);

  const [isSeeking, setIsSeeking] = useState<boolean>(false);
  const [hasStartedPlaying, setHasStartedPlaying] = useState<boolean>(false);
  // Cache-busting counter for subtitle URLs - increments after seek to force refetch
  const [subtitleCacheBust, setSubtitleCacheBust] = useState<number>(0);
  // Track HDR content type (Dolby Vision/HDR10) for proper player configuration
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

  // Calculate letterbox bottom height for accurate subtitle positioning
  // Uses actual screen dimensions and video natural size for precise placement
  const letterboxBottom = useMemo(() => {
    if (!videoSize?.width || !videoSize?.height) return undefined;

    const videoAspectRatio = videoSize.width / videoSize.height;
    const screenAspectRatio = windowWidth / windowHeight;

    // Video is wider than screen: letterboxing on top/bottom
    if (videoAspectRatio > screenAspectRatio) {
      const actualVideoHeight = windowWidth / videoAspectRatio;
      return (windowHeight - actualVideoHeight) / 2;
    }

    // No letterboxing (video fills screen height or is pillarboxed)
    return 0;
  }, [videoSize?.width, videoSize?.height, windowWidth, windowHeight]);

  // Next episode prequeue state (triggered at 90% of current episode)
  const [nextEpisodePrequeueReady, setNextEpisodePrequeueReady] = useState(false);
  const nextEpisodePrequeueRef = useRef<{
    prequeueId: string | null;
    targetEpisode: { seasonNumber: number; episodeNumber: number; absoluteEpisodeNumber?: number } | null;
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

  // Combined HDR detection: route params OR detected from video metadata
  // Used for player routing - ensures RNV is used when HDR is detected even if route params are missing
  const hasAnyHDR = useMemo(
    () => routeHasAnyHDR || videoColorInfo?.isHDR10 || false,
    [routeHasAnyHDR, videoColorInfo?.isHDR10],
  );

  // Refresh settings on playback start to ensure latest values (e.g., subtitle size)
  useEffect(() => {
    refreshSettings().catch((err) => {
      console.warn('[player] Failed to refresh settings on mount:', err);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []); // Only run on mount

  // Prevent screen saver / display sleep while video is playing
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

  // Enable/disable Android auto-PiP based on playback state
  // This controls whether the native onUserLeaveHint() should trigger PiP
  useEffect(() => {
    console.log('[PiP Debug] useEffect running, paused:', paused, 'enableAutoPip:', !!enableAutoPip, 'disableAutoPip:', !!disableAutoPip);
    if (enableAutoPip && disableAutoPip) {
      if (paused) {
        console.log('[PiP Debug] Calling disableAutoPip()');
        disableAutoPip();
      } else {
        console.log('[PiP Debug] Calling enableAutoPip()');
        enableAutoPip();
      }
    } else {
      console.log('[PiP Debug] Functions not available, skipping');
    }

    return () => {
      // Ensure auto-PiP is disabled when player unmounts
      console.log('[PiP Debug] Cleanup: disabling auto-PiP');
      disableAutoPip?.();
    };
  }, [paused]);

  // Auto-pause when app is backgrounded (mobile and TV)
  // Skip auto-pause when in PiP mode - video should keep playing
  const wasPlayingBeforeBackgroundRef = useRef(false);
  const isPipActiveRef = useRef(isPipActive);
  isPipActiveRef.current = isPipActive;
  // Track when we entered background (for detecting screensaver/long pause)
  const backgroundedAtRef = useRef<number | null>(null);
  // Threshold for considering session potentially expired (slightly less than backend's 60s idle timeout)
  const SESSION_EXPIRY_THRESHOLD_MS = 50000;

  // Pause teardown effect: Prevents AVPlayer HLS timeout (-11866) by tearing down
  // the player after extended pause. When paused for 20+ seconds on an HLS stream,
  // we save the current time, clear the video source, and show a poster overlay.
  // This prevents AVPlayer from throwing "stream ended unexpectedly" errors.
  // On resume, we create a new HLS session starting from the saved time.
  useEffect(() => {
    // TEMPORARILY DISABLED: Pause teardown causing issues, disabling for debugging
    return;
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
    } else if (!paused && pauseTeardownTimerRef.current !== null) {
      // Clear timer if we unpause before teardown
      console.log('[player] pause teardown: clearing timer (unpaused)');
      clearTimeout(pauseTeardownTimerRef.current!);
      pauseTeardownTimerRef.current = null;
    }

    return () => {
      const timerId = pauseTeardownTimerRef.current;
      if (timerId !== null) {
        clearTimeout(timerId);
        pauseTeardownTimerRef.current = null;
      }
    };
  }, [paused, isHlsStream, currentMovieUrl, pauseTeardownActive]);

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
  // Note: hlsSessionIdRef is now provided by useHlsSession hook
  const currentTimeRef = useRef<number>(0);
  const durationRef = useRef<number>(0);
  const nowPlayingLastUpdateRef = useRef<number>(0); // Throttle iOS Now Playing updates
  const playbackOffsetRef = useRef<number>(initialStartOffset);
  // Use initialActualStartOffset if provided (from prequeue HLS session), otherwise fall back to initialStartOffset
  const actualPlaybackOffsetRef = useRef<number>(initialActualStartOffset ?? initialStartOffset); // Keyframe-aligned start for subtitle sync
  // Keyframe delta: difference between actual keyframe and requested position (negative = keyframe earlier)
  const keyframeDeltaRef = useRef<number>(0);
  // Track when keyframe sync was last applied (for debug display)
  const [lastSyncTime, setLastSyncTime] = useState<string | null>(null);
  // Track if we've done a warm seek (session recreation) since initial load
  // Initial warm start doesn't need delta, but subsequent warm seeks do
  const hasWarmSeekedRef = useRef<boolean>(false);
  // Note: sessionBufferEndRef is now provided by useHlsSession hook
  const warmStartTokenRef = useRef(0);
  const warmStartDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const videoEndedNaturallyRef = useRef(false); // Track if video ended naturally (vs user exit)
  const userNavigatedToEpisodeRef = useRef(false); // Track if user explicitly navigated (via Next/Prev button)
  // Stall detection refs - track when playback stops progressing with exhausted buffer
  const lastProgressTimeRef = useRef<number>(0); // Last time currentTime increased
  const lastCurrentTimeRef = useRef<number>(0); // Last recorded currentTime
  const lastPlayableDurationRef = useRef<number>(0); // Last recorded playableDuration
  const lastBufferGrowthTimeRef = useRef<number>(0); // Last time playableDuration increased (buffer growing)
  const stallDetectedAtRef = useRef<number | null>(null); // When stall was first detected
  const isRecoveringFromStallRef = useRef(false); // Prevent multiple recovery attempts
  const warmStartDebounceResolveRef = useRef<((value: boolean) => void) | null>(null);
  // Note: pendingSessionSeekRef is now provided by useHlsSession hook
  const pendingSeekAttemptRef = useRef<{ attempts: number; lastAttemptMs: number }>({
    attempts: 0,
    lastAttemptMs: 0,
  });
  const hasAttemptedInitialWarmStartRef = useRef(false);
  const hasReceivedPlayerLoadRef = useRef(false);
  const hlsSessionRetryCountRef = useRef(0);
  // Note: isRecreatingHlsSessionRef and skipTrackPreferencesRef are now provided by useHlsSession hook
  const isRetryingHlsSessionRef = useRef(false);
  const isSeekingRef = useRef<boolean>(false);
  const pausedForSeekRef = useRef<boolean>(false); // Track if we paused for HLS session seek or track switching
  // Track skip button seek target to prevent stale progress events from flickering the time display
  // reachedAt tracks when we first got close to target, to continue protecting for a window afterward
  const skipSeekTargetRef = useRef<{ target: number; timestamp: number; reachedAt?: number } | null>(null);
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

  // Subtitle sync debug overlay - set to true to debug subtitle timing
  const subtitleDebugEnabled = false;
  // const subtitleDebugEnabled = useMemo(() => {
  //   const rawValue = Array.isArray(subtitleDebugParam) ? subtitleDebugParam[0] : subtitleDebugParam;
  //   if (!rawValue) {
  //     return false;
  //   }
  //   const normalized = rawValue.toString().trim().toLowerCase();
  //   return normalized === '1' || normalized === 'true' || normalized === 'yes' || normalized === 'on';
  // }, [subtitleDebugParam]);

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
  const { activeUserId, activeUser } = useUserProfiles();

  // Fetch per-content language preference (if exists) to override user settings
  useEffect(() => {
    if (!titleId || !activeUserId) {
      setContentPreference(null);
      return;
    }

    let isMounted = true;
    const fetchContentPreference = async () => {
      try {
        const pref = await apiService.getContentPreference(activeUserId, titleId);
        if (isMounted) {
          setContentPreference(pref);
          if (pref) {
            console.log('[player] loaded per-content language preference', pref);
          }
        }
      } catch (error) {
        console.warn('[player] failed to fetch content preference', error);
        if (isMounted) {
          setContentPreference(null);
        }
      }
    };

    fetchContentPreference();
    return () => {
      isMounted = false;
    };
  }, [titleId, activeUserId]);

  // Refs for HLS session callbacks (populated later, accessed by callbacks)
  const playbackOffsetCallbackRef = useRef<((offset: number) => void) | null>(null);
  const fatalErrorCallbackRef = useRef<((error: string) => void) | null>(null);

  // Initialize HLS session hook for managing session lifecycle
  const [hlsSessionState, hlsSessionActions, hlsSessionRefs] = useHlsSession({
    sourcePath: initialSourcePath || '',
    initialPlaylistUrl: resolvedMovie && String(resolvedMovie).includes('/video/hls/') ? resolvedMovie : undefined,
    initialStartOffset,
    hasDolbyVision: routeHasDolbyVision,
    dolbyVisionProfile: routeDvProfile || undefined,
    hasHDR10: routeHasHDR10,
    forceAAC: forceAacFromRoute,
    profileId: activeUserId ?? undefined,
    profileName: activeUser?.name,
    onOffsetCorrection: useCallback((serverOffset: number) => {
      playbackOffsetCallbackRef.current?.(serverOffset);
    }, []),
    onFatalError: useCallback((error: string) => {
      fatalErrorCallbackRef.current?.(error);
    }, []),
  });

  // Destructure commonly used refs from the hook for easier access
  const {
    sessionIdRef: hlsSessionIdRef,
    sessionBufferEndRef,
    isRecreatingRef: isRecreatingHlsSessionRef,
    skipTrackPreferencesRef,
    pendingSeekRef: pendingSessionSeekRef,
  } = hlsSessionRefs;

  // Pause teardown restoration: When user resumes after teardown, create a new HLS session
  // Note: This effect must be after useHlsSession initialization as it uses hlsSessionActions
  useEffect(() => {
    if (!paused && pauseTeardownActive && initialSourcePath) {
      console.log('[player] pause teardown: resuming from teardown, creating new HLS session', {
        savedTime: pauseTeardownTimeRef.current,
      });

      // Create new HLS session starting from saved time
      const restoreSession = async () => {
        try {
          // Update hook's track refs with current player selections
          hlsSessionRefs.audioTrackRef.current = selectedAudioTrackIndexRef.current ?? undefined;
          hlsSessionRefs.subtitleTrackRef.current = selectedSubtitleTrackIndexRef.current ?? undefined;

          // Use the hook's createSession for consistent session management
          const response = await hlsSessionActions.createSession(pauseTeardownTimeRef.current, {
            audioTrack: selectedAudioTrackIndexRef.current ?? undefined,
            subtitleTrack: selectedSubtitleTrackIndexRef.current ?? undefined,
          });

          if (!response) {
            throw new Error('Failed to create session');
          }

          console.log('[player] pause teardown: restored HLS session', {
            playlistUrl: response.playlistUrl,
            startOffset: response.startOffset,
            actualStartOffset: response.actualStartOffset,
          });

          // Update playback offset if the session has a start offset
          if (typeof response.startOffset === 'number' && response.startOffset >= 0) {
            playbackOffsetRef.current = response.startOffset;
            // Use actualStartOffset for subtitle sync (keyframe-aligned)
            actualPlaybackOffsetRef.current =
              typeof response.actualStartOffset === 'number' && response.actualStartOffset >= 0
                ? response.actualStartOffset
                : response.startOffset;
          }
          // Mark that we've done a warm seek (restoration is like a seek)
          hasWarmSeekedRef.current = true;

          // Restore the video URL - this will trigger the video to load
          setCurrentMovieUrl(response.playlistUrl);
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
  }, [paused, pauseTeardownActive, initialSourcePath, showToast, hlsSessionActions, hlsSessionRefs]);

  // Auto-pause when app is backgrounded (mobile and TV)
  // Skip auto-pause when in PiP mode - video should keep playing
  // Note: This effect must be after useHlsSession initialization as it uses hlsSessionActions
  useEffect(() => {
    const subscription = AppState.addEventListener('change', async (nextAppState) => {
      if (nextAppState === 'background' || nextAppState === 'inactive') {
        // Record when we went to background
        backgroundedAtRef.current = Date.now();
        console.log('[player] App entering background, recording time');

        // Don't pause if in PiP mode - the video should keep playing
        if (isPipActiveRef.current) {
          console.log('[player] Skipping auto-pause: PiP mode active');
          return;
        }
        // On iOS/Android mobile, auto-enter PiP when backgrounding during playback
        if ((Platform.OS === 'ios' || Platform.OS === 'android') && !Platform.isTV && !paused) {
          console.log('[player] Auto-entering PiP on background');
          isPipActiveRef.current = true;
          videoRef.current?.enterPip?.();
          return;
        }
        // App is being minimized - pause if currently playing
        if (!paused) {
          wasPlayingBeforeBackgroundRef.current = true;
          setPaused(true);
          console.log('[player] Auto-paused: app went to background');
        }
      } else if (nextAppState === 'active') {
        // App came back to foreground
        const backgroundDuration = backgroundedAtRef.current ? Date.now() - backgroundedAtRef.current : 0;
        backgroundedAtRef.current = null;
        wasPlayingBeforeBackgroundRef.current = false;

        // If we were backgrounded for a long time with an HLS stream, validate session is still alive
        if (isHlsStream && backgroundDuration > SESSION_EXPIRY_THRESHOLD_MS) {
          console.log('[player] Returning from long background, validating HLS session', {
            backgroundDurationMs: backgroundDuration,
            thresholdMs: SESSION_EXPIRY_THRESHOLD_MS,
          });

          // Check if session is still valid
          const status = await hlsSessionActions.getStatus();
          if (!status) {
            console.log('[player] HLS session expired during background, destroying player');
            showToast('Stream session expired. Please restart playback.', { tone: 'danger', duration: 4000 });
            router.back();
            return;
          }
          console.log('[player] HLS session still valid after background');
        }
      }
    });

    return () => {
      subscription.remove();
    };
  }, [paused, isHlsStream, hlsSessionActions, showToast, router]);

  // Save per-content language preference when user manually changes audio or subtitle track
  // If the user selects their global default language, clear the content preference instead
  const saveContentLanguagePreference = useCallback(
    async (options: { audioTrackId?: string; subtitleTrackId?: string }) => {
      // Skip if we don't have required info
      if (!titleId || !activeUserId) {
        console.log('[player] skipping content preference save - missing titleId or userId');
        return;
      }

      // Get user's global preferred languages
      const globalAudioLanguage =
        userSettings?.playback?.preferredAudioLanguage || settings?.playback?.preferredAudioLanguage;
      const globalSubtitleLanguage =
        userSettings?.playback?.preferredSubtitleLanguage || settings?.playback?.preferredSubtitleLanguage;
      const globalSubtitleMode =
        userSettings?.playback?.preferredSubtitleMode || settings?.playback?.preferredSubtitleMode;

      // Determine content type based on mediaType
      const isSeries = mediaType === 'episode' || mediaType === 'series' || mediaType === 'tv' || mediaType === 'show';
      const contentType: 'series' | 'movie' = isSeries ? 'series' : 'movie';

      // Build preference object with current values
      let audioLanguage: string | undefined;
      let subtitleLanguage: string | undefined;
      let subtitleMode: 'off' | 'on' | 'forced-only' | undefined;

      // Get audio language from the track selection
      if (options.audioTrackId && audioStreamMetadata) {
        const trackIndex = parseInt(options.audioTrackId, 10);
        const audioStream = audioStreamMetadata.find((s) => s.index === trackIndex);
        if (audioStream?.language) {
          audioLanguage = audioStream.language;
        }
      }

      // Get subtitle language/mode from the track selection
      if (options.subtitleTrackId !== undefined) {
        if (options.subtitleTrackId === 'off' || options.subtitleTrackId === SUBTITLE_OFF_OPTION.id) {
          subtitleMode = 'off';
          subtitleLanguage = undefined;
        } else if (options.subtitleTrackId === 'external') {
          // External subtitles - don't save as preference
          return;
        } else if (subtitleStreamMetadata) {
          const trackIndex = parseInt(options.subtitleTrackId, 10);
          const subtitleStream = subtitleStreamMetadata.find((s) => s.index === trackIndex);
          if (subtitleStream) {
            subtitleLanguage = subtitleStream.language || undefined;
            // Check if it's a forced subtitle
            subtitleMode = subtitleStream.isForced ? 'forced-only' : 'on';
          }
        }
      }

      // Don't save if no language info available
      if (!audioLanguage && !subtitleLanguage && subtitleMode !== 'off') {
        console.log('[player] skipping content preference save - no language info');
        return;
      }

      try {
        // Get existing preference to preserve settings not being changed
        const existingPref = await apiService.getContentPreference(activeUserId, titleId);

        // Determine final values (new values or preserve existing)
        const finalAudioLanguage = audioLanguage ?? existingPref?.audioLanguage;
        const finalSubtitleLanguage = subtitleLanguage ?? existingPref?.subtitleLanguage;
        const finalSubtitleMode = subtitleMode ?? existingPref?.subtitleMode;

        // Only save values that DIFFER from global settings
        // If a value matches global, it should be undefined (no override needed)
        const audioOverride =
          finalAudioLanguage && finalAudioLanguage !== globalAudioLanguage ? finalAudioLanguage : undefined;
        const subtitleLangOverride =
          finalSubtitleLanguage && finalSubtitleLanguage !== globalSubtitleLanguage ? finalSubtitleLanguage : undefined;
        const subtitleModeOverride =
          finalSubtitleMode && finalSubtitleMode !== globalSubtitleMode ? finalSubtitleMode : undefined;

        // If no actual overrides remain, delete the preference entirely
        if (!audioOverride && !subtitleLangOverride && !subtitleModeOverride) {
          if (existingPref) {
            console.log('[player] clearing content preference - all values match global settings');
            await apiService.deleteContentPreference(activeUserId, titleId);
            setContentPreference(null);
          }
          return;
        }

        const preference: ContentPreference = {
          contentId: titleId,
          contentType,
          audioLanguage: audioOverride,
          subtitleLanguage: subtitleLangOverride,
          subtitleMode: subtitleModeOverride,
        };

        console.log('[player] saving content language preference', preference);
        await apiService.setContentPreference(activeUserId, preference);
        setContentPreference(preference);
      } catch (error) {
        console.error('[player] failed to save content language preference', error);
      }
    },
    [titleId, activeUserId, mediaType, audioStreamMetadata, subtitleStreamMetadata, userSettings, settings],
  );

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
  // Note: The useHlsSession hook handles initial extraction, this syncs runtime changes
  useEffect(() => {
    if (!isExistingHlsSession || !effectiveMovie) {
      return;
    }
    // URL format: /video/hls/{sessionId}/stream.m3u8
    const match = String(effectiveMovie).match(/\/video\/hls\/([^/]+)\/stream\.m3u8/);
    if (match && match[1] && hlsSessionIdRef.current !== match[1]) {
      hlsSessionIdRef.current = match[1];
      console.log('[player] extracted HLS session ID from URL:', match[1]);
    }
  }, [isExistingHlsSession, effectiveMovie, hlsSessionIdRef]);

  // Track if we've already shown a fatal error alert (to prevent duplicate alerts)
  const hasShownFatalErrorRef = useRef(false);

  // Set up callback refs for HLS session hook
  useEffect(() => {
    playbackOffsetCallbackRef.current = (serverOffset: number) => {
      playbackOffsetRef.current = serverOffset;
    };
    fatalErrorCallbackRef.current = (error: string) => {
      if (!hasShownFatalErrorRef.current) {
        hasShownFatalErrorRef.current = true;
        console.warn('[player] HLS session fatal error:', error);
        showToast(`Stream error: ${error}`, { tone: 'danger', duration: 5000 });
        router.back();
      }
    };
  }, [showToast, router]);

  // Track keyframeDelta from hook state for subtitle sync
  // keyframeDelta is the offset between actual keyframe and requested position
  // NOTE: Video seeks to keyframe but subtitles are extracted from requested position,
  // so we need keyframeDelta to correct the desync
  useEffect(() => {
    const newKeyframeDelta = hlsSessionState.keyframeDelta;
    console.log('[player] keyframeDelta effect triggered', {
      hookKeyframeDelta: newKeyframeDelta,
      refKeyframeDelta: keyframeDeltaRef.current,
      hookActualStartOffset: hlsSessionState.actualStartOffset,
    });
    if (typeof newKeyframeDelta === 'number' && newKeyframeDelta !== keyframeDeltaRef.current) {
      const oldDelta = keyframeDeltaRef.current;
      console.log('[player] syncing keyframeDeltaRef from hook state', {
        old: oldDelta,
        new: newKeyframeDelta,
      });
      keyframeDeltaRef.current = newKeyframeDelta;

      // Show notification when background keyframe probe completes with meaningful delta
      if (Math.abs(newKeyframeDelta) > 0.1) {
        const deltaMs = Math.round(newKeyframeDelta * 1000);
        const sign = deltaMs >= 0 ? '+' : '';
        const syncMsg = `${sign}${deltaMs}ms`;
        console.log('[player] keyframe sync applied', { deltaMs, oldDelta, newKeyframeDelta, hasWarmSeeked: hasWarmSeekedRef.current });
        setLastSyncTime(syncMsg);
        showToast(`Keyframe sync: ${syncMsg}`, { tone: 'neutral', duration: 2000 });
      } else if (Math.abs(newKeyframeDelta) <= 0.1 && oldDelta !== 0) {
        // Seek happened, delta reset to ~0, clear sync indicator until new probe completes
        console.log('[player] keyframe delta reset (new seek), waiting for probe', { oldDelta, newKeyframeDelta });
        setLastSyncTime(null);
      }
    }
    // Also sync actualStartOffset
    if (
      typeof hlsSessionState.actualStartOffset === 'number' &&
      hlsSessionState.actualStartOffset >= 0 &&
      hlsSessionState.actualStartOffset !== actualPlaybackOffsetRef.current
    ) {
      actualPlaybackOffsetRef.current = hlsSessionState.actualStartOffset;
    }
  }, [hlsSessionState.keyframeDelta, hlsSessionState.actualStartOffset, showToast]);

  // Send keepalive pings and poll for session status to detect stream errors
  // The backend kills FFmpeg after 60s of no segment requests, but players buffer aggressively
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

      // Send keepalive ping using the hook's method (includes offset validation)
      const keepaliveResponse = await hlsSessionActions.keepalive(currentTimeRef.current, currentTimeRef.current);

      // Log periodically to confirm sync is working (every 60s worth of keepalives)
      if (keepaliveResponse?.startOffset !== undefined) {
        if (Math.floor(currentTimeRef.current / 60) !== Math.floor((currentTimeRef.current - 10) / 60)) {
          console.log('[player] keepalive: segment timing in sync', {
            startOffset: keepaliveResponse.startOffset,
            segmentDuration: keepaliveResponse.segmentDuration,
            currentTime: currentTimeRef.current,
          });
        }
      }

      // Poll for session status to detect fatal errors
      // Note: Fatal errors are also handled by the hook's onFatalError callback
      const status = await hlsSessionActions.getStatus();
      if (status?.status === 'error' && status.fatalError && !hasShownFatalErrorRef.current) {
        hasShownFatalErrorRef.current = true;
        console.warn('[player] HLS session fatal error:', status.fatalError);
        showToast(`Stream error: ${status.fatalError}`, { tone: 'danger', duration: 5000 });
        router.back();
      }
    }, 10000);

    console.log('[player] started keepalive/status interval for HLS stream');

    return () => {
      console.log('[player] stopping keepalive/status pings (unmounted or stream changed)');
      clearInterval(intervalId);
      hasShownFatalErrorRef.current = false;
    };
  }, [isHlsStream, hlsSessionActions, hlsSessionIdRef, showToast, router]);

  // Stall detection and recovery for HLS streams
  // Detects when playback has stalled (buffer exhausted, no progress) and recreates the session
  useEffect(() => {
    if (!isHlsStream || !initialSourcePath) {
      return;
    }

    const STALL_DETECTION_INTERVAL = 1000; // Check every 1 second
    const STALL_THRESHOLD = 3000; // Trigger recovery after 3 seconds of stall
    const BUFFER_EXHAUSTED_THRESHOLD = 1.0; // Consider buffer exhausted if < 1 second ahead
    const END_OF_VIDEO_THRESHOLD = 10; // Don't trigger stall recovery if within 10 seconds of end

    const intervalId = setInterval(async () => {
      // Skip if paused, recovering, or no session
      if (pausedRef.current || isRecoveringFromStallRef.current || !hlsSessionIdRef.current) {
        return;
      }

      const now = Date.now();
      const timeSinceProgress = now - lastProgressTimeRef.current;
      const timeSinceBufferGrowth = now - lastBufferGrowthTimeRef.current;
      const currentTime = lastCurrentTimeRef.current;
      const playableDuration = lastPlayableDurationRef.current;
      const bufferRemaining = playableDuration - currentTime;
      const totalDuration = durationRef.current;

      // Skip stall detection if we're near the end of the video
      // At the end, buffer naturally stops growing because there's no more content - this isn't a stall
      const isNearEndOfVideo =
        totalDuration > 0 && currentTime >= totalDuration - END_OF_VIDEO_THRESHOLD;
      if (isNearEndOfVideo) {
        // Clear any stall detection state when approaching end of video
        if (stallDetectedAtRef.current !== null) {
          stallDetectedAtRef.current = null;
        }
        return;
      }

      // Detect stall: buffer is exhausted AND playback hasn't progressed AND buffer isn't growing
      // If buffer is still growing, we're just buffering normally (segments downloading) - don't trigger recovery
      const isBufferGrowing = timeSinceBufferGrowth < STALL_THRESHOLD;
      const isStalled =
        bufferRemaining < BUFFER_EXHAUSTED_THRESHOLD &&
        timeSinceProgress > STALL_DETECTION_INTERVAL &&
        !isBufferGrowing &&
        currentTime > 0 &&
        playableDuration > 0;

      if (isStalled) {
        if (stallDetectedAtRef.current === null) {
          // First detection of stall
          stallDetectedAtRef.current = now;
          console.warn('[player] stall detection: buffer exhausted and not growing, monitoring...', {
            currentTime,
            playableDuration,
            bufferRemaining,
            timeSinceProgress,
            timeSinceBufferGrowth,
          });
        } else if (now - stallDetectedAtRef.current > STALL_THRESHOLD) {
          // Stall persisted beyond threshold - trigger recovery
          console.warn('[player] stall recovery: triggering session recreation', {
            currentTime,
            playableDuration,
            stallDuration: now - stallDetectedAtRef.current,
            timeSinceBufferGrowth,
          });

          isRecoveringFromStallRef.current = true;
          stallDetectedAtRef.current = null;

          try {
            // Get current track selections
            const audioTrack = selectedAudioTrackIndexRef.current ?? undefined;
            const subtitleTrack = selectedSubtitleTrackIndexRef.current ?? undefined;

            // Show toast to inform user
            showToast('Reconnecting stream...', { tone: 'info', duration: 3000 });

            // Recreate session from current position
            const response = await hlsSessionActions.createSession(currentTime, {
              audioTrack,
              subtitleTrack,
            });

            if (response) {
              console.log('[player] stall recovery: session recreated successfully', {
                newSessionId: response.sessionId,
                startOffset: response.startOffset,
              });

              // Update the video source to use new playlist
              setCurrentMovieUrl(response.playlistUrl);

              // Reset stall tracking
              lastProgressTimeRef.current = Date.now();
              lastPlayableDurationRef.current = 0;
            } else {
              console.error('[player] stall recovery: session creation failed');
              showToast('Failed to reconnect stream', { tone: 'danger', duration: 3000 });
            }
          } catch (error) {
            console.error('[player] stall recovery: error during session recreation', error);
            showToast('Failed to reconnect stream', { tone: 'danger', duration: 3000 });
          } finally {
            // Allow future recovery attempts after a delay
            setTimeout(() => {
              isRecoveringFromStallRef.current = false;
            }, 5000);
          }
        }
      } else {
        // Not stalled - clear detection
        if (stallDetectedAtRef.current !== null) {
          stallDetectedAtRef.current = null;
        }
      }
    }, STALL_DETECTION_INTERVAL);

    // Initialize tracking refs
    lastProgressTimeRef.current = Date.now();
    lastBufferGrowthTimeRef.current = Date.now();
    lastCurrentTimeRef.current = 0;
    lastPlayableDurationRef.current = 0;
    stallDetectedAtRef.current = null;
    isRecoveringFromStallRef.current = false;

    return () => {
      clearInterval(intervalId);
    };
  }, [isHlsStream, initialSourcePath, hlsSessionActions, hlsSessionIdRef, showToast]);

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
  // Track which path we've already fetched metadata for to prevent repeated fetches
  const lastFetchedMetadataPathRef = useRef<string | null>(null);

  useEffect(() => {
    // IMPORTANT: Skip entire effect during HLS seek transition
    // warmStartHlsSession already set the correct offsets and we don't want to overwrite them
    // with stale route params (initialStartOffset is from route params and doesn't update on seek)
    if (pausedForSeekRef.current || isRecreatingHlsSessionRef.current) {
      console.log('🎬 [player] skipping initialization during seek transition');
      return;
    }

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
        // Use actualStartOffset from prequeue if provided (keyframe-aligned for subtitle sync)
        if (typeof initialActualStartOffset === 'number') {
          actualPlaybackOffsetRef.current = initialActualStartOffset;
        }
        console.log(
          '🎬 [player] using existing HLS session at offset:',
          initialStartOffset,
          'actualStartOffset:',
          actualPlaybackOffsetRef.current,
        );
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
    // Reset warm seek flag - initial playback doesn't need keyframeDelta
    hasWarmSeekedRef.current = false;

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
  }, [initialStartOffset, initialActualStartOffset, shouldWarmStartResume, isHlsStream, isExistingHlsSession]);

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

    // Cleanup: lock orientation back to portrait when player closes (phones only)
    // Tablets should retain free orientation
    return () => {
      if (isTablet) {
        console.log('[Player] Skipping orientation lock on tablet');
        return;
      }
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
      case 'rnv':
        return 'React Native Video';
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
      passthroughName,
      passthroughDescription,
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
    passthroughName,
    passthroughDescription,
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

  // Track focus state for controls (native focus doesn't need lock/unlock)

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
    if (usesSystemManagedControls) {
      return;
    }

    RemoteControlManager.enableTvEventHandling();
  }, [usesSystemManagedControls]);

  useEffect(() => {
    if (usesSystemManagedControls) {
      return;
    }

    // Reset focus flag when controls visibility changes
    if (!controlsVisible) {
      hasFocusedRef.current = false;
    } else if (!hasFocusedRef.current) {
      hasFocusedRef.current = true;
    }
  }, [controlsVisible, usesSystemManagedControls]);

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
        case SupportedKeys.Enter:
          // On TV platforms, Enter key should toggle play/pause when controls are hidden
          if (!controlsVisibleRef.current) {
            console.log('[player] Enter key toggling play/pause (controls hidden)');
            togglePausePlayRef.current?.();
          } else {
            // When controls are visible, Enter activates focused element (spatial navigation)
            showControlsRef.current?.();
          }
          break;
        case SupportedKeys.PlayPause: {
          console.log('[player] PlayPause key received, paused:', pausedRef.current, 'lastToggle:', lastPlayPauseToggleRef.current);
          // TODO: tvOS sends duplicate events - debounce to ignore rapid duplicates
          const now = Date.now();
          const debounceMs = Platform.OS === 'ios' && Platform.isTV ? 300 : 0;
          const timeSinceLastToggle = now - lastPlayPauseToggleRef.current;
          console.log('[player] PlayPause debounce check:', { now, debounceMs, timeSinceLastToggle, willToggle: timeSinceLastToggle > debounceMs });
          if (timeSinceLastToggle > debounceMs) {
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
        // CRITICAL: Clear the video source BEFORE calling seek API.
        // This stops iOS AVPlayer from trying to access segments that the backend
        // is about to delete. Without this, the player errors with -12312 because
        // it tries to prefetch segments from the old (now-deleted) playlist.
        // Use empty string (not null) to prevent fallback to resolvedMovie.
        if (hlsSessionIdRef.current) {
          console.log('[player] clearing video source before seek to prevent race condition');
          // Mark recreation flags BEFORE clearing URL to prevent race condition where
          // isHlsStream becomes false and triggers subtitle probe effect for SDR content
          hlsSessionActions.setRecreating(true);
          hlsSessionActions.setSkipTrackPreferences(true);
          setCurrentMovieUrl('');
        }

        // Use refs to get current track indices (avoids stale closure values)
        const currentAudioTrack = selectedAudioTrackIndexRef.current ?? undefined;
        const currentSubtitleTrack = selectedSubtitleTrackIndexRef.current ?? undefined;

        // Update the hook's track refs before seeking
        hlsSessionRefs.audioTrackRef.current = currentAudioTrack;
        hlsSessionRefs.subtitleTrackRef.current = currentSubtitleTrack;

        // Use the hook's seek method which handles API calls and session management
        const response = await hlsSessionActions.seek(safeTarget);

        if (!response) {
          console.error('🚨 Failed to warm start HLS session: no response');
          if (warmStartTokenRef.current === token) {
            setIsVideoBuffering(false);
          }
          return false;
        }

        if (warmStartTokenRef.current !== token) {
          return true;
        }

        const sessionStart = response.startOffset ?? safeTarget;
        const actualSessionStart = response.actualStartOffset ?? sessionStart;

        console.log('[player] warmStartHLS setting offsets', {
          responseStartOffset: response.startOffset,
          responseActualStartOffset: response.actualStartOffset,
          sessionStart,
          actualSessionStart,
          safeTarget,
          playlistUrl: response.playlistUrl?.substring(0, 80),
        });

        // Update player-specific offset refs (not managed by hook)
        playbackOffsetRef.current = sessionStart;
        actualPlaybackOffsetRef.current = actualSessionStart; // For subtitle sync
        currentTimeRef.current = sessionStart;
        setCurrentTime(sessionStart);

        // Sync keyframeDelta for subtitle timing correction
        if (typeof response.keyframeDelta === 'number') {
          keyframeDeltaRef.current = response.keyframeDelta;
        }

        // Reset pending seek attempt tracking
        pendingSeekAttemptRef.current.attempts = 0;
        pendingSeekAttemptRef.current.lastAttemptMs = 0;
        hasWarmSeekedRef.current = true;

        console.log('[player] warmStartHLS offsets set', {
          playbackOffset: playbackOffsetRef.current,
          actualPlaybackOffset: actualPlaybackOffsetRef.current,
          keyframeDelta: keyframeDeltaRef.current,
          responseStartOffset: response.startOffset,
          responseActualStartOffset: response.actualStartOffset,
          responseKeyframeDelta: response.keyframeDelta,
          sessionStart,
          actualSessionStart,
          subtitleTimeOffset: -actualPlaybackOffsetRef.current,
        });

        // Mark that we're recreating HLS session to preserve track selections
        hlsSessionActions.setRecreating(true);
        hlsSessionActions.setSkipTrackPreferences(true);
        setCurrentMovieUrl(response.playlistUrl);
        hasReceivedPlayerLoadRef.current = false;
        setHasStartedPlaying(false);
        // Increment cache bust to force subtitle refetch after seek
        setSubtitleCacheBust((prev) => prev + 1);
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
      isHlsStream,
      sourcePath,
      updateDuration,
      hlsSessionActions,
      hlsSessionRefs,
      hlsSessionIdRef,
      pendingSessionSeekRef,
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
        // Check if we're expecting a skip button seek to complete
        const skipTarget = skipSeekTargetRef.current;
        if (skipTarget) {
          const now = Date.now();
          const timeSinceSkip = now - skipTarget.timestamp;
          const distanceFromTarget = Math.abs(absoluteTime - skipTarget.target);
          const isCloseToTarget = distanceFromTarget < 2;

          // Total protection timeout - after this, accept any progress
          if (timeSinceSkip > 1500) {
            console.log('[player] skip seek timeout, accepting progress', { absoluteTime, target: skipTarget.target });
            skipSeekTargetRef.current = null;
            currentTimeRef.current = absoluteTime;
            setCurrentTime(absoluteTime);
          } else if (isCloseToTarget) {
            // We've reached the target - mark when and accept this update
            if (!skipTarget.reachedAt) {
              console.log('[player] skip seek reached target', { absoluteTime, target: skipTarget.target });
              skipTarget.reachedAt = now;
            }
            currentTimeRef.current = absoluteTime;
            setCurrentTime(absoluteTime);
          } else if (skipTarget.reachedAt) {
            // We already reached target but this event is far from it (stale)
            // Only accept if we've been at target long enough (500ms buffer for late events)
            const timeSinceReached = now - skipTarget.reachedAt;
            if (timeSinceReached > 500) {
              console.log('[player] skip seek protection ended, accepting progress', {
                absoluteTime,
                target: skipTarget.target,
              });
              skipSeekTargetRef.current = null;
              currentTimeRef.current = absoluteTime;
              setCurrentTime(absoluteTime);
            } else {
              console.log('[player] skip seek ignoring stale event after reaching target', {
                absoluteTime,
                target: skipTarget.target,
                timeSinceReached,
              });
            }
          } else {
            console.log('[player] skip seek ignoring stale event before reaching target', {
              absoluteTime,
              target: skipTarget.target,
              timeSinceSkip,
            });
          }
        } else {
          currentTimeRef.current = absoluteTime;
          setCurrentTime(absoluteTime);
        }
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

        // Prep iOS PiP controller by calling enterPip once - this initializes the
        // AVPictureInPictureController so future PiP requests work on first try
        if (Platform.OS === 'ios' && !Platform.isTV) {
          console.log('[player] Prepping PiP controller on playback start');
          videoRef.current?.enterPip?.();
        }

        // Hide loading screen with a small delay to ensure player screen is fully visible
        setTimeout(() => {
          hideLoadingScreen();
        }, 100);
      }

      if (meta?.seekable) {
        // For HLS streams, seekableDuration reports the full video duration (from the playlist),
        // not the actual transcoded content. Only use it for duration display, not buffer tracking.
        if (!isHlsStream) {
          const absoluteSeekable = playbackOffsetRef.current + meta.seekable;
          if (absoluteSeekable > sessionBufferEndRef.current) {
            sessionBufferEndRef.current = absoluteSeekable;
          }
        }
        updateDuration(meta.seekable, 'progress.seekable');
      }
      if (meta?.playable) {
        // playableDuration reflects actual buffered/transcoded content - use this for HLS buffer tracking
        const absolutePlayable = playbackOffsetRef.current + meta.playable;
        if (absolutePlayable > sessionBufferEndRef.current) {
          sessionBufferEndRef.current = absolutePlayable;
        }
        updateDuration(meta.playable, 'progress.playable');

        // Update stall detection tracking for HLS streams
        if (isHlsStream && !paused) {
          const now = Date.now();
          // Track if currentTime is progressing
          if (absoluteTime > lastCurrentTimeRef.current + 0.1) {
            lastProgressTimeRef.current = now;
            lastCurrentTimeRef.current = absoluteTime;
            // Clear stall detection if we're making progress
            if (stallDetectedAtRef.current !== null) {
              console.log('[player] stall recovery: playback resumed, clearing stall detection');
              stallDetectedAtRef.current = null;
            }
          }
          // Track if buffer is growing (segments still downloading)
          if (absolutePlayable > lastPlayableDurationRef.current + 0.1) {
            lastPlayableDurationRef.current = absolutePlayable;
            lastBufferGrowthTimeRef.current = now;
          }
        }
      }

      // Report progress to the backend for tracking (skip for live TV)
      const currentDuration = durationRef.current;
      if (!isLiveTV && currentDuration > 0 && absoluteTime >= 0) {
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
      // Clear recreation flags now that the new source has loaded
      if (isRecreatingHlsSessionRef.current) {
        isRecreatingHlsSessionRef.current = false;
      }
      if (skipTrackPreferencesRef.current) {
        skipTrackPreferencesRef.current = false;
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
      // Shuffle mode: pick a random episode (different from current, excludes season 0/specials)
      const shuffleableEpisodes = allEpisodes.filter((ep) => ep.seasonNumber !== 0);
      if (shuffleableEpisodes.length > 0) {
        let randomIndex: number;
        do {
          randomIndex = Math.floor(Math.random() * shuffleableEpisodes.length);
        } while (
          shuffleableEpisodes.length > 1 &&
          shuffleableEpisodes[randomIndex].seasonNumber === seasonNumber &&
          shuffleableEpisodes[randomIndex].episodeNumber === episodeNumber
        );
        nextEpisode = shuffleableEpisodes[randomIndex];
        isShuffleEpisode = true;
      }
    } else if (currentIndex >= 0 && currentIndex < allEpisodes.length - 1) {
      // Sequential mode: get next episode
      nextEpisode = allEpisodes[currentIndex + 1];
    }

    if (!nextEpisode) return;

    // Skip prequeue if no active user - don't create "default" prequeue entries
    if (!activeUserId) {
      console.log('[player] Skipping next episode prequeue - no active user');
      return;
    }

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
        userId: activeUserId,
        seasonNumber: nextEpisode.seasonNumber,
        episodeNumber: nextEpisode.episodeNumber,
        absoluteEpisodeNumber: nextEpisode.absoluteEpisodeNumber,
      });

      nextEpisodePrequeueRef.current = {
        prequeueId: response.prequeueId,
        targetEpisode: {
          seasonNumber: nextEpisode.seasonNumber,
          episodeNumber: nextEpisode.episodeNumber,
          absoluteEpisodeNumber: nextEpisode.absoluteEpisodeNumber,
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

  // Keep pausedRef in sync for use in event handlers (avoids stale closure)
  useEffect(() => {
    pausedRef.current = paused;
  }, [paused]);

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
          currentKeyframeOffset: actualPlaybackOffsetRef.current,
          sessionBufferEnd: sessionBufferEndRef.current,
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
          const isBackwardsSeek = time < currentTimeRef.current;
          // Force session recreation for any backwards seek to ensure clean segment boundaries
          const withinSession = hasBufferedWindow && relativeTime >= 0 && time <= sessionEnd - bufferPadding && !isBackwardsSeek;

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
              console.log('[player] calling videoRef.seek (in-session)', {
                time: relative,
                hasVideoRef: !!videoRef.current,
                offset: sessionStart,
                keyframeDelta: keyframeDeltaRef.current,
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
          // Only update time if this seek's target is still the active target
          // This prevents cancelled/stale seeks from overwriting newer skip targets
          const activeSkipTarget = skipSeekTargetRef.current;
          const isStaleSeek = activeSkipTarget && Math.abs(activeSkipTarget.target - time) > 1;
          if (!isStaleSeek) {
            currentTimeRef.current = time;
            setCurrentTime(time);
          }
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
    console.log('[player] togglePausePlay called, current paused:', pausedRef.current);
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
      console.log('[player] setPaused toggling:', previousPaused, '->', nextPaused);

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

    // Only reset auto-hide timer if controls are already visible
    // When controls are hidden, play/pause should just toggle playback without showing controls
    if (controlsVisibleRef.current) {
      showControls();
    }
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

  const handleEnterPip = useCallback(() => {
    // Call enterPip FIRST, then update state. If we update state before calling
    // enterPip, the re-render can interfere with the button press handling,
    // causing the first tap to silently fail.
    videoRef.current?.enterPip?.();
    // Set the ref immediately after to prevent race condition
    // with AppState listener (which would pause on background before the
    // onPictureInPictureStatusChanged callback fires)
    isPipActiveRef.current = true;
    setIsPipActive(true);
  }, []);

  const handlePictureInPictureStatusChanged = useCallback((isActive: boolean) => {
    if (isActive) {
      // Entering PiP - already handled in handleEnterPip, but update state to be safe
      isPipActiveRef.current = true;
      setIsPipActive(true);
    } else {
      // Exiting PiP - update state immediately (for Controls layout remount),
      // but delay clearing the ref to prevent AppState transitions from pausing
      setIsPipActive(false);
      setTimeout(() => {
        isPipActiveRef.current = false;
      }, 500);
    }
    console.log('[player] PiP status changed:', isActive);
  }, []);

  // Sync paused state with native playback state (for TV platforms where native
  // media controls can toggle playback directly via MPRemoteCommandCenter)
  const handleNativePlaybackStateChanged = useCallback((isPlaying: boolean) => {
    const shouldBePaused = !isPlaying;
    // Only sync if state differs to avoid unnecessary re-renders
    if (pausedRef.current !== shouldBePaused) {
      console.log('[player] syncing paused state from native:', pausedRef.current, '->', shouldBePaused);
      setPaused(shouldBePaused);
    }
  }, []);

  const seekBackwardSeconds = settings?.playback?.seekBackwardSeconds ?? 10;
  const seekForwardSeconds = settings?.playback?.seekForwardSeconds ?? 30;

  const handleSkipBackward = useCallback(() => {
    // Use the pending skip target if one exists and is recent, otherwise use currentTimeRef
    // This prevents async operations (HLS session recreation, progress events) from
    // resetting the position between rapid skip button presses
    const skipTarget = skipSeekTargetRef.current;
    const baseTime =
      skipTarget && Date.now() - skipTarget.timestamp < 2000 ? skipTarget.target : currentTimeRef.current;
    const targetTime = Math.max(0, baseTime - seekBackwardSeconds);
    console.log('[player] skip backward', {
      from: baseTime,
      to: targetTime,
      skipAmount: seekBackwardSeconds,
      usedPendingTarget: !!skipTarget,
    });
    skipSeekTargetRef.current = { target: targetTime, timestamp: Date.now() };
    seek(targetTime);
  }, [seek, seekBackwardSeconds]);

  const handleSkipForward = useCallback(() => {
    // Use the pending skip target if one exists and is recent, otherwise use currentTimeRef
    // This prevents async operations (HLS session recreation, progress events) from
    // resetting the position between rapid skip button presses
    const skipTarget = skipSeekTargetRef.current;
    const baseTime =
      skipTarget && Date.now() - skipTarget.timestamp < 2000 ? skipTarget.target : currentTimeRef.current;
    const targetTime = baseTime + seekForwardSeconds;
    console.log('[player] skip forward', {
      from: baseTime,
      to: targetTime,
      skipAmount: seekForwardSeconds,
      usedPendingTarget: !!skipTarget,
    });
    skipSeekTargetRef.current = { target: targetTime, timestamp: Date.now() };
    seek(targetTime);
  }, [seek, seekForwardSeconds]);

  // Double-tap handler for mobile skip forward/backward
  const isMobilePlatform = Platform.OS !== 'web' && !Platform.isTV;
  const handleDoubleTapSeek = useCallback(
    (event: { nativeEvent: { locationX: number } }) => {
      if (!isMobilePlatform || isLiveTV || usesSystemManagedControls) {
        handleVideoInteract();
        return;
      }

      const { locationX } = event.nativeEvent;
      const screenMidpoint = windowWidth / 2;
      const side: 'left' | 'right' = locationX < screenMidpoint ? 'left' : 'right';
      const now = Date.now();

      if (lastTapRef.current && now - lastTapRef.current.time < DOUBLE_TAP_DELAY && lastTapRef.current.side === side) {
        // Double-tap detected on same side
        lastTapRef.current = null;

        if (side === 'left') {
          handleSkipBackward();
          setFlashSkipButton('backward');
        } else {
          handleSkipForward();
          setFlashSkipButton('forward');
        }

        // Clear flash after animation
        setTimeout(() => setFlashSkipButton(null), 400);
      } else {
        // First tap - record it and show/hide controls after a short delay if no second tap
        lastTapRef.current = { time: now, side };
        setTimeout(() => {
          // If no second tap occurred, treat as single tap
          if (lastTapRef.current && lastTapRef.current.time === now) {
            lastTapRef.current = null;
            handleVideoInteract();
          }
        }, DOUBLE_TAP_DELAY);
      }
    },
    [
      isMobilePlatform,
      isLiveTV,
      usesSystemManagedControls,
      windowWidth,
      handleSkipBackward,
      handleSkipForward,
      handleVideoInteract,
      DOUBLE_TAP_DELAY,
    ],
  );

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
          // Update hook's track refs with current player selections
          hlsSessionRefs.audioTrackRef.current = selectedAudioTrackIndexRef.current ?? undefined;
          hlsSessionRefs.subtitleTrackRef.current = selectedSubtitleTrackIndexRef.current ?? undefined;

          // Create a fresh HLS session using the hook - this will get a new session ID
          // The server may have restarted FFmpeg with different codec parameters (DV fallback)
          const response = await hlsSessionActions.createSession(currentTimeRef.current, {
            audioTrack: selectedAudioTrackIndexRef.current ?? undefined,
            subtitleTrack: selectedSubtitleTrackIndexRef.current ?? undefined,
          });

          if (!response) {
            throw new Error('Failed to create retry session');
          }

          console.log('[player] created new HLS session for retry', {
            playlistUrl: response.playlistUrl,
            startOffset: response.startOffset,
            actualStartOffset: response.actualStartOffset,
          });

          // Update the video source to the new session
          setCurrentMovieUrl(response.playlistUrl);

          // Update playback offset if the session has a start offset
          if (typeof response.startOffset === 'number' && response.startOffset >= 0) {
            playbackOffsetRef.current = response.startOffset;
            // Use actualStartOffset for subtitle sync (keyframe-aligned)
            actualPlaybackOffsetRef.current =
              typeof response.actualStartOffset === 'number' && response.actualStartOffset >= 0
                ? response.actualStartOffset
                : response.startOffset;
          }
          // Mark that we've done a warm seek (retry is like a seek)
          hasWarmSeekedRef.current = true;
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
    [isHlsStream, initialSourcePath, showToast, hlsSessionActions, hlsSessionRefs],
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
      console.log('[player] player tracks available', { audioTracks, subtitleTracks });

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

      // Only use player-reported subtitle tracks if we don't have backend-probed tracks
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
    }

    // Add cache-busting parameter to force refetch after seek
    // Without this, the browser may serve a cached VTT from before the seek
    if (subtitleCacheBust > 0) {
      const separator = baseUrl.includes('?') ? '&' : '?';
      baseUrl = `${baseUrl}${separator}_cb=${subtitleCacheBust}`;
    }

    console.log('[player] sidecarSubtitleUrl: built URL', {
      selectedSubtitleTrackIndex,
      subtitleCacheBust,
      finalUrl: baseUrl,
    });

    return baseUrl;
  }, [isHlsStream, effectiveMovie, selectedSubtitleTrackIndex, subtitleCacheBust]);

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

    // Skip during HLS seek transitions - isHlsStream temporarily becomes false when URL is cleared
    // but we're still in an HLS playback context and shouldn't reset subtitle tracks
    if (isRecreatingHlsSessionRef.current || pausedForSeekRef.current) {
      console.log('[player] skipping subtitle probe during HLS seek transition');
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
        if (
          session.language &&
          session.title &&
          !session.title.toLowerCase().includes(session.language.toLowerCase())
        ) {
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
      const preferredMode = userSettings?.playback?.preferredSubtitleMode || settings?.playback?.preferredSubtitleMode;

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

      const selectedIndex = findSubtitleTrackByPreference(streamsForSelection, preferredLang || undefined, validMode);

      // Check if external subtitles are already active
      if (externalSubtitleUrlRef.current) {
        console.log('[player] preserving external subtitle selection');
        return;
      }

      if (selectedIndex !== null) {
        setSelectedSubtitleTrackId(String(selectedIndex));
      } else if (validMode === 'off') {
        setSelectedSubtitleTrackId('off');
      } else {
        // Preferred language not found - trigger auto-search
        setSelectedSubtitleTrackId('off');
        triggerAutoSubtitleSearchIfNeeded();
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

          // Check if external subtitles are already active
          if (externalSubtitleUrlRef.current) {
            console.log('[player] preserving external subtitle selection');
            return;
          }

          if (selectedIndex !== null) {
            setSelectedSubtitleTrackId(String(selectedIndex));
          } else if (validMode === 'off') {
            setSelectedSubtitleTrackId('off');
          } else {
            // Preferred language not found - trigger auto-search
            setSelectedSubtitleTrackId('off');
            triggerAutoSubtitleSearchIfNeeded();
          }
        } else {
          // No text-based subtitle tracks found (e.g., only PGS bitmap subs)
          // Check if external subtitles are already active
          if (externalSubtitleUrlRef.current) {
            console.log('[player] preserving external subtitle selection');
            return;
          }
          // Clear any player-reported tracks and show only "Off" option
          console.log('[player] no text-based subtitle tracks found, clearing player-reported tracks');
          setSubtitleTrackOptions([SUBTITLE_OFF_OPTION]);
          setSelectedSubtitleTrackId('off');
          // No embedded tracks available - trigger auto-search
          triggerAutoSubtitleSearchIfNeeded();
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
    // This prevents race condition where the player reports incorrect track indices
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
    // This prevents race condition where the player reports absolute stream indices (e.g., 14)
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

  // Callback to handle subtitle debug info for troubleshooting
  const handleSubtitleDebugInfo = useCallback((info: SubtitleDebugInfo) => {
    setSubtitleDebugInfo(info);
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
    // Only recreate session for HLS streams
    // TESTING: Removed HDR/DV check since SDR now also uses HLS
    // Original: if (!isHlsStream || !sourcePath || !(routeHasDolbyVision || routeHasHDR10)) {
    if (!isHlsStream || !sourcePath) {
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
      // Check if prequeue already baked in the correct tracks
      // If preselected tracks match user preferences, skip recreation entirely
      const audioMatchesPreselected =
        preselectedAudioTrack !== undefined && preselectedAudioTrack === selectedAudioTrackIndex;
      const subtitleMatchesPreselected =
        preselectedSubtitleTrack !== undefined && preselectedSubtitleTrack === selectedSubtitleTrackIndex;
      // Consider subtitle "matching" if preselected is undefined and selected is null/undefined (both mean no subtitle)
      const subtitleEffectivelyMatches =
        subtitleMatchesPreselected ||
        (preselectedSubtitleTrack === undefined &&
          (selectedSubtitleTrackIndex === null || selectedSubtitleTrackIndex === undefined));

      if (audioMatchesPreselected || (preselectedAudioTrack !== undefined && subtitleEffectivelyMatches)) {
        console.log('[player] prequeue tracks match preferences - skipping HLS session recreation', {
          preselectedAudio: preselectedAudioTrack,
          preselectedSubtitle: preselectedSubtitleTrack,
          preferredAudio: selectedAudioTrackIndex,
          preferredSubtitle: selectedSubtitleTrackIndex,
          audioMatches: audioMatchesPreselected,
          subtitleMatches: subtitleEffectivelyMatches,
        });
        // Initialize refs with preselected values and skip recreation
        lastHlsTrackSelectionRef.current = { audio: preselectedAudioTrack, subtitle: preselectedSubtitleTrack ?? null };
        setTimeout(() => {
          hasAppliedInitialTracksRef.current = true;
          console.log('[player] marked initial tracks as applied (preselected)');
          const pendingSeek = pendingSessionSeekRef.current;
          if (pendingSeek !== null && pendingSeek > 0) {
            setTimeout(
              () => {
                applyPendingSessionSeek('preselected-tracks-applied');
              },
              isHlsStream ? 1000 : 0,
            );
          }
        }, 500);
        return;
      }

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

      // CRITICAL: Clear the video source BEFORE calling the API.
      // This ensures the old player is fully destroyed before the new one starts,
      // preventing potential dual-player audio issues on Android TV.
      console.log('[player] clearing video source before track recreation to prevent dual players');
      hlsSessionActions.setRecreating(true);
      hlsSessionActions.setSkipTrackPreferences(true);
      setCurrentMovieUrl('');

      try {
        console.log('[player] creating NEW HLS session with selected tracks', {
          audioTrack: selectedAudioTrackIndex,
          subtitleTrack: selectedSubtitleTrackIndex,
          audioTrackType: typeof selectedAudioTrackIndex,
          subtitleTrackType: typeof selectedSubtitleTrackIndex,
        });

        // Use hook's createSession for consistent session management
        const response = await hlsSessionActions.createSession(safeTarget, {
          audioTrack: selectedAudioTrackIndex ?? undefined,
          subtitleTrack: selectedSubtitleTrackIndex ?? undefined,
          trackSwitch: true, // Skip waiting for first segment for faster track switching
        });

        if (!response) {
          throw new Error('Failed to create session');
        }

        const sessionStart = response.startOffset ?? safeTarget;
        const actualSessionStart = response.actualStartOffset ?? sessionStart;

        playbackOffsetRef.current = sessionStart;
        actualPlaybackOffsetRef.current = actualSessionStart; // For subtitle sync
        currentTimeRef.current = sessionStart;
        setCurrentTime(sessionStart);
        // Mark that we've done a warm seek (track switch recreates session)
        hasWarmSeekedRef.current = true;
        // Set the new movie URL (recreating flags already set before API call)
        setCurrentMovieUrl(response.playlistUrl);
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
          newPlaylistUrl: response.playlistUrl?.substring(0, 100),
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
    audioTrackOptions.length,
    subtitleTrackOptions.length,
    updateDuration,
    applyPendingSessionSeek,
    hlsSessionActions,
    preselectedAudioTrack,
    preselectedSubtitleTrack,
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
    lastFetchedMetadataPathRef.current = null; // Allow metadata fetch for new video

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
    // Capture the skip flag at effect start, before async operations
    // This prevents race conditions where handleLoad clears the flag before the async fetch completes
    const shouldSkipPreferencesAtStart = skipTrackPreferencesRef.current;

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
              return;
            }
          } else {
            console.log('[player] metadata fetch skipped: no path param in url', resolvedMovie);
            return;
          }
        }

        // Skip if we've already successfully fetched metadata for this exact path
        // This prevents repeated fetches when other dependencies change but the path is the same
        if (lastFetchedMetadataPathRef.current === pathParam) {
          console.log('[player] metadata fetch skipped: already fetched for this path');
          return;
        }

        console.log('[player] fetching metadata for', pathParam);
        setSourcePath(pathParam);
        const metadata = await apiService.getVideoMetadata(pathParam);
        if (!isMounted) {
          return;
        }

        // Only mark as fetched AFTER successful completion and isMounted check
        // This prevents the race condition where the ref is set before the fetch completes,
        // then the effect re-runs (setting isMounted=false), and metadata is never processed
        lastFetchedMetadataPathRef.current = pathParam;

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

          // Calculate video/audio bitrate with fallback to format-level bitrate
          // FFprobe often doesn't report per-stream bitrates, only the total file bitrate
          let videoBitrate = primaryVideo.bitRate;
          let audioBitrate = primaryAudio?.bitRate;
          const formatBitrate = metadata.formatBitRate || 0;

          if (!videoBitrate && formatBitrate) {
            // Use format bitrate minus total audio bitrate as an estimate
            const totalAudioBitrate = metadata.audioStreams?.reduce((sum, a) => sum + (a.bitRate || 0), 0) || 0;
            videoBitrate = formatBitrate - totalAudioBitrate;
            // If audio bitrate is also unknown, just use format bitrate (video is the bulk of it)
            if (videoBitrate <= 0) {
              videoBitrate = formatBitrate;
            }
          }

          // Estimate audio bitrate if video bitrate is known but audio isn't
          if (!audioBitrate && formatBitrate && videoBitrate) {
            const estimatedAudio = formatBitrate - videoBitrate;
            // Only use if it's a reasonable audio bitrate (> 32kbps, < 2Mbps)
            if (estimatedAudio > 32000 && estimatedAudio < 2000000) {
              audioBitrate = estimatedAudio;
            }
          }

          setStreamInfo({
            resolution,
            videoBitrate,
            videoCodec: primaryVideo.codecLongName || primaryVideo.codecName,
            frameRate,
            audioCodec: primaryAudio?.codecLongName || primaryAudio?.codecName,
            audioChannels,
            audioBitrate,
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
        // Use the captured value from effect start to handle async timing - by the time this runs,
        // handleLoad may have already cleared skipTrackPreferencesRef, but we captured it earlier.
        // Also skip if initial tracks have already been applied (prevents race condition when
        // contentPreference loads and triggers effect re-run after user has changed tracks).
        if (shouldSkipPreferencesAtStart || hasAppliedInitialTracksRef.current) {
          console.log('[player] HLS session recreation - skipping track preference application', {
            shouldSkipPreferencesAtStart,
            hasAppliedInitialTracks: hasAppliedInitialTracksRef.current,
          });
          // Still build and set audio options so the track menu shows correctly
          const audioOptions = buildAudioTrackOptions(metadata.audioStreams ?? []);
          setAudioTrackOptions(audioOptions);
          // Store raw metadata for per-content preference saving
          setAudioStreamMetadata(metadata.audioStreams ?? null);
          setSubtitleStreamMetadata(metadata.subtitleStreams ?? null);
          // Also set subtitle options for the menu
          const subtitleOptions = buildSubtitleTrackOptions(
            metadata.subtitleStreams ?? [],
            metadata.selectedSubtitleIndex,
          );
          setSubtitleTrackOptions(subtitleOptions);

          // Set the UI track state so the menu shows the correct selection
          // Priority: lastHlsTrackSelectionRef (has user's current selection after track changes) > preselected (initial from prequeue)
          // This prevents preselected from overwriting user's manual track selection
          if (lastHlsTrackSelectionRef.current.audio !== null) {
            const audioId = resolveSelectedTrackId(audioOptions, lastHlsTrackSelectionRef.current.audio);
            setSelectedAudioTrackId(audioId);
            console.log('[player] set audio track UI state from lastHlsTrackSelection', {
              audio: lastHlsTrackSelectionRef.current.audio,
              audioId,
            });
          } else if (preselectedAudioTrack !== undefined) {
            const audioId = resolveSelectedTrackId(audioOptions, preselectedAudioTrack);
            setSelectedAudioTrackId(audioId);
            console.log('[player] set audio track UI state from preselected', { preselectedAudioTrack, audioId });
          }

          if (lastHlsTrackSelectionRef.current.subtitle !== null) {
            const subtitleId = resolveSelectedTrackId(subtitleOptions, lastHlsTrackSelectionRef.current.subtitle);
            setSelectedSubtitleTrackId(subtitleId);
            console.log('[player] set subtitle track UI state from lastHlsTrackSelection', {
              subtitle: lastHlsTrackSelectionRef.current.subtitle,
              subtitleId,
            });
          } else if (preselectedSubtitleTrack !== undefined) {
            const subtitleId = resolveSelectedTrackId(subtitleOptions, preselectedSubtitleTrack);
            setSelectedSubtitleTrackId(subtitleId);
            console.log('[player] set subtitle track UI state from preselected', {
              preselectedSubtitleTrack,
              subtitleId,
            });
          }

          // Even in the early return path, check if auto-search is needed for files with no embedded subtitles
          // This handles the case where hasAppliedInitialTracksRef was set before settings loaded
          const noEmbeddedSubs = !metadata.subtitleStreams || metadata.subtitleStreams.length === 0;
          const currentSubtitleSelection =
            lastHlsTrackSelectionRef.current.subtitle !== null
              ? String(lastHlsTrackSelectionRef.current.subtitle)
              : preselectedSubtitleTrack !== undefined
                ? String(preselectedSubtitleTrack)
                : 'off';
          if (noEmbeddedSubs && currentSubtitleSelection === 'off') {
            console.log('[player] no embedded subtitles in early return path, triggering auto-search');
            triggerAutoSubtitleSearchIfNeeded();
          }

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
        // Store raw metadata for per-content preference saving
        setAudioStreamMetadata(metadata.audioStreams ?? null);
        setSubtitleStreamMetadata(metadata.subtitleStreams ?? null);

        // Priority: preselected (one-time override from prequeue) > per-content preference > user settings > global settings > metadata
        let selectedAudioIndex = metadata.selectedAudioIndex;
        let audioSelectionSource = 'metadata';

        // First check for preselected track (user explicitly selected in prequeue modal)
        if (preselectedAudioTrack !== undefined) {
          selectedAudioIndex = preselectedAudioTrack;
          audioSelectionSource = 'preselected-override';
          console.log('[player] using preselected audio track (user override from prequeue)', {
            preselectedAudioTrack,
            selectedAudioIndex,
          });
        } else {
          // Fall back to preference chain
          const preferredAudioLanguage =
            contentPreference?.audioLanguage ||
            userSettings?.playback?.preferredAudioLanguage ||
            settings?.playback?.preferredAudioLanguage;
          if (preferredAudioLanguage) {
            const preferredAudioIndex = findAudioTrackByLanguage(metadata.audioStreams ?? [], preferredAudioLanguage);
            if (preferredAudioIndex !== null) {
              selectedAudioIndex = preferredAudioIndex;
              audioSelectionSource = contentPreference?.audioLanguage ? 'content-preference' : 'user-settings';
              console.log('[player] using preferred audio language', {
                preferredLanguage: preferredAudioLanguage,
                source: audioSelectionSource,
                selectedAudioIndex,
              });
            }
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
          source: audioSelectionSource,
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

          // Priority: preselected (one-time override from prequeue) > per-content preference > user settings > global settings > metadata
          let selectedSubtitleIndex = metadata.selectedSubtitleIndex;
          let subtitleSelectionSource = 'metadata';
          let shouldTriggerAutoSearch = false;

          // First check for preselected track (user explicitly selected in prequeue modal)
          // Note: preselectedSubtitleTrack of -1 means "Off" was explicitly selected
          if (preselectedSubtitleTrack !== undefined) {
            if (preselectedSubtitleTrack === -1) {
              selectedSubtitleIndex = undefined;
              subtitleSelectionSource = 'preselected-override-off';
              console.log('[player] using preselected subtitle track OFF (user override from prequeue)');
            } else {
              selectedSubtitleIndex = preselectedSubtitleTrack;
              subtitleSelectionSource = 'preselected-override';
              console.log('[player] using preselected subtitle track (user override from prequeue)', {
                preselectedSubtitleTrack,
                selectedSubtitleIndex,
              });
            }
          } else {
            // Fall back to preference chain
            const preferredSubtitleLanguage =
              contentPreference?.subtitleLanguage ||
              userSettings?.playback?.preferredSubtitleLanguage ||
              settings?.playback?.preferredSubtitleLanguage;
            const preferredSubtitleModeRaw =
              contentPreference?.subtitleMode ||
              userSettings?.playback?.preferredSubtitleMode ||
              settings?.playback?.preferredSubtitleMode;
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
              if (preferredSubtitleIndex !== null) {
                selectedSubtitleIndex = preferredSubtitleIndex;
                subtitleSelectionSource = contentPreference?.subtitleLanguage ? 'content-preference' : 'user-settings';
              } else if (preferredSubtitleMode === 'off') {
                selectedSubtitleIndex = undefined;
                subtitleSelectionSource = 'user-settings-off';
              } else {
                // Preferred language not found - set to off and trigger auto-search
                selectedSubtitleIndex = undefined;
                subtitleSelectionSource = 'user-settings-fallback';
                shouldTriggerAutoSearch = true;
              }
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
            source: subtitleSelectionSource,
          });

          // Don't overwrite external subtitle selection if auto-search already found subtitles
          if (externalSubtitleUrlRef.current) {
            console.log('[player] preserving external subtitle selection');
          } else {
            setSelectedSubtitleTrackId(resolvedSubtitleSelection);

            // Trigger auto-search if no suitable embedded track was found (only for preference fallback, not preselected override)
            if (resolvedSubtitleSelection === 'off' && shouldTriggerAutoSearch) {
              triggerAutoSubtitleSearchIfNeeded();
            }
          }
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
  }, [
    resolvedMovie,
    updateDuration,
    sourcePath,
    settings,
    userSettings,
    isHlsStream,
    routeHasAnyHDR,
    triggerAutoSubtitleSearchIfNeeded,
    contentPreference,
    preselectedAudioTrack,
    preselectedSubtitleTrack,
  ]);

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

      // Reset auto-subtitle state for the new episode
      autoSubtitleTriggeredRef.current = false;
      setAutoSubtitleStatus('idle');
      setAutoSubtitleMessage(null);

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

  const handleNextEpisode = useCallback(async () => {
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

    // No prequeue exists - trigger one immediately and pass to details page
    // Details page will poll/wait for it to be ready
    const seriesId = titleId || imdbId || tvdbId;
    if (!seriesId || !activeUserId) {
      console.warn('[player] Cannot trigger on-demand prequeue - missing seriesId or userId');
      // Fall back to normal navigation without prequeue
      if (shuffleMode && allEpisodes.length > 1) {
        const shuffleableEpisodes = allEpisodes.filter((ep) => ep.seasonNumber !== 0);
        if (shuffleableEpisodes.length > 0) {
          let randomIndex: number;
          let nextEp: SeriesEpisode;
          do {
            randomIndex = Math.floor(Math.random() * shuffleableEpisodes.length);
            nextEp = shuffleableEpisodes[randomIndex];
          } while (
            shuffleableEpisodes.length > 1 &&
            nextEp.seasonNumber === seasonNumber &&
            nextEp.episodeNumber === episodeNumber
          );
          navigateToEpisode(nextEp);
          return;
        }
      }
      if (hasNextEpisode) {
        const nextEp = allEpisodes[currentEpisodeIndex + 1];
        if (nextEp) navigateToEpisode(nextEp);
      }
      return;
    }

    // Determine the next episode
    let nextEpisode: SeriesEpisode | undefined;
    let isShuffleEpisode = false;

    if (shuffleMode && allEpisodes.length > 1) {
      // Shuffle mode: pick a random episode (different from current, excludes season 0/specials)
      const shuffleableEpisodes = allEpisodes.filter((ep) => ep.seasonNumber !== 0);
      if (shuffleableEpisodes.length > 0) {
        let randomIndex: number;
        do {
          randomIndex = Math.floor(Math.random() * shuffleableEpisodes.length);
        } while (
          shuffleableEpisodes.length > 1 &&
          shuffleableEpisodes[randomIndex].seasonNumber === seasonNumber &&
          shuffleableEpisodes[randomIndex].episodeNumber === episodeNumber
        );
        nextEpisode = shuffleableEpisodes[randomIndex];
        isShuffleEpisode = true;
      }
    } else if (hasNextEpisode) {
      // Sequential mode: get next episode
      nextEpisode = allEpisodes[currentEpisodeIndex + 1];
    }

    if (!nextEpisode) return;

    console.log('[player] Triggering on-demand prequeue for next episode:', {
      season: nextEpisode.seasonNumber,
      episode: nextEpisode.episodeNumber,
      shuffleMode: isShuffleEpisode,
    });

    try {
      // Trigger prequeue and get ID
      const response = await apiService.prequeuePlayback({
        titleId: seriesId,
        titleName: cleanSeriesTitle || title || '',
        mediaType: 'series',
        userId: activeUserId,
        seasonNumber: nextEpisode.seasonNumber,
        episodeNumber: nextEpisode.episodeNumber,
        absoluteEpisodeNumber: nextEpisode.absoluteEpisodeNumber,
      });

      // Store prequeue data in ref so navigateToEpisode can use it
      nextEpisodePrequeueRef.current = {
        prequeueId: response.prequeueId,
        targetEpisode: {
          seasonNumber: nextEpisode.seasonNumber,
          episodeNumber: nextEpisode.episodeNumber,
          absoluteEpisodeNumber: nextEpisode.absoluteEpisodeNumber,
        },
        statusResponse: null, // Not ready yet, details page will poll
        isShuffleEpisode,
      };

      console.log('[player] On-demand prequeue initiated:', response.prequeueId);

      // Navigate with prequeue flag - details page will poll for ready status
      navigateToEpisode(nextEpisode, true);
    } catch (error) {
      console.warn('[player] On-demand prequeue failed, falling back to normal navigation:', error);
      // Fall back to normal navigation without prequeue
      navigateToEpisode(nextEpisode);
    }
  }, [
    hasNextEpisode,
    allEpisodes,
    currentEpisodeIndex,
    navigateToEpisode,
    shuffleMode,
    seasonNumber,
    episodeNumber,
    titleId,
    imdbId,
    tvdbId,
    activeUserId,
    cleanSeriesTitle,
    title,
  ]);

  // Hide loading screen on unmount (e.g., if user navigates back before video loads)
  useEffect(() => {
    return () => {
      hideLoadingScreen();
    };
  }, [hideLoadingScreen]);

  return (
    <>
      <Stack.Screen options={{ headerShown: false }} />
      <StatusBar style="light" hidden={shouldHideStatusBar} animated />
      {/* On Android, don't apply top safe area to the container (which would shift the video when status bar toggles).
          Instead, safe area is applied to the controls overlay only. On iOS, apply top edge in portrait. */}
      <FixedSafeAreaView
        style={styles.safeArea}
        edges={Platform.OS === 'android' && !Platform.isTV ? [] : isPortrait ? ['top'] : []}>
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
              // Always disable player's built-in subtitles - we use SubtitleOverlay for consistent sizing
              selectedSubtitleTrackIndex={undefined}
              onTracksAvailable={handleTracksAvailable}
              // Force react-native-video for all content (exception: Live TV uses system player)
              forceRnvPlayer={!isLiveTV}
              forceNativeFullscreen={Platform.OS !== 'web' && hasAnyHDR}
              onVideoSize={(width, height) => setVideoSize({ width, height })}
              nowPlaying={{
                title: episodeName || title || undefined,
                subtitle: seriesTitle || undefined,
                imageUri: headerImage || undefined,
              }}
              subtitleSize={userSettings?.playback?.subtitleSize ?? settings?.playback?.subtitleSize ?? 1.0}
              mediaType={mediaType}
              onPictureInPictureStatusChanged={handlePictureInPictureStatusChanged}
              onPlaybackStateChanged={handleNativePlaybackStateChanged}
            />
          </View>

          {/* Pause teardown overlay - shows last frame (or poster) when player is torn down after extended pause */}
          {/* This prevents AVPlayer HLS timeout by proactively stopping the player */}
          {/* Tapping the overlay shows controls so user can press play to resume */}
          {pauseTeardownActive && (
            <Pressable style={styles.pauseTeardownOverlay} onPress={handleVideoInteract}>
              {pauseTeardownFrame ? (
                <Image source={{ uri: pauseTeardownFrame }} style={styles.pauseTeardownImage} resizeMode="cover" />
              ) : headerImage ? (
                <Image source={{ uri: headerImage }} style={styles.pauseTeardownImage} resizeMode="contain" />
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
          {/* VTT is extracted with -start_at_zero, player time is relative to playbackOffset */}
          {/* Use -playbackOffset to convert currentTime to VTT-relative time */}
          {isHlsStream && sidecarSubtitleUrl && hasStartedPlaying && !isPipActive && (
            <SubtitleOverlay
              vttUrl={sidecarSubtitleUrl}
              currentTime={currentTime}
              currentTimeRef={currentTimeRef}
              timeOffset={-playbackOffsetRef.current + (hlsSessionState.keyframeDelta ?? 0) - subtitleOffset}
              enabled={selectedSubtitleTrackIndex !== null && selectedSubtitleTrackIndex >= 0}
              videoWidth={videoSize?.width}
              videoHeight={videoSize?.height}
              sizeScale={userSettings?.playback?.subtitleSize ?? settings?.playback?.subtitleSize ?? 1.0}
              controlsVisible={controlsVisible}
              isHDRContent={!!hdrInfo?.isDolbyVision || !!hdrInfo?.isHDR10}
              onDebugInfo={subtitleDebugEnabled ? handleSubtitleDebugInfo : undefined}
              letterboxBottom={letterboxBottom}
            />
          )}

          {/* Extracted subtitle overlay for non-HLS streams (direct playback) */}
          {/* Uses standalone subtitle extraction endpoint to convert embedded subs to VTT */}
          {/* Use sdrFirstCueTimeRef for sync (mirrors actualPlaybackOffsetRef for HLS) */}
          {/* timeOffset: -firstCueTime to align VTT cues with player position, -subtitleOffset for user adjustment */}
          {!isHlsStream &&
            extractedSubtitleUrl &&
            selectedSubtitleTrackId !== 'external' &&
            hasStartedPlaying &&
            !isPipActive && (
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
                isHDRContent={!!hdrInfo?.isDolbyVision || !!hdrInfo?.isHDR10}
                letterboxBottom={letterboxBottom}
              />
            )}

          {/* External subtitle overlay from OpenSubtitles/Subliminal search */}
          {/* timeOffset is negated: positive user offset = later subtitles = decrease adjustedTime */}
          {externalSubtitleUrl && selectedSubtitleTrackId === 'external' && hasStartedPlaying && !isPipActive && (
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
              isHDRContent={!!hdrInfo?.isDolbyVision || !!hdrInfo?.isHDR10}
              letterboxBottom={letterboxBottom}
            />
          )}

          {/* Auto-subtitle search status overlay */}
          {autoSubtitleMessage && !isPipActive && (
            <SubtitleStatusOverlay status={autoSubtitleStatus} message={autoSubtitleMessage} />
          )}

          {/* Double-tap overlay for mobile skip forward/backward */}
          {isMobilePlatform && !isLiveTV && !usesSystemManagedControls && (
            <Pressable style={styles.doubleTapOverlay} onPress={handleDoubleTapSeek} />
          )}

          {(() => {
            const hasDuration = Number.isFinite(duration) && duration > 0;
            const hasPlaybackContext = hasDuration || currentTime > 0 || isVideoBuffering || paused || isLiveTV;
            const isTVSeeking = isTvPlatform && seekIndicatorAmount !== 0;
            const shouldRenderControls =
              !usesSystemManagedControls &&
              !isPipActive &&
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
                  isSeeking={isTVSeeking}>
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
                      renderToHardwareTextureAndroid={true}>
                      <View
                        style={styles.overlayContent}
                        pointerEvents="box-none"
                        renderToHardwareTextureAndroid={true}>
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
                            resolution={streamInfo?.resolution}
                            safeAreaInsets={safeAreaInsets}
                          />
                        </View>
                        <View style={styles.overlayControls} pointerEvents="box-none">
                          <Controls
                            key={`controls-tv-${isPipActive}`}
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
                              // Save per-content language preference
                              saveContentLanguagePreference({ audioTrackId: id });
                            }}
                            subtitleTracks={subtitleTrackOptions}
                            selectedSubtitleTrackId={selectedSubtitleTrackId}
                            onSelectSubtitleTrack={(id) => {
                              console.log('[player] user selected subtitle track', { id, subtitleTrackOptions });
                              setSelectedSubtitleTrackId(id);
                              // Clear external subtitle when switching to embedded track
                              if (id !== 'external') {
                                externalSubtitleUrlRef.current = null;
                                setExternalSubtitleUrl(null);
                              }
                              // Save per-content language preference
                              saveContentLanguagePreference({ subtitleTrackId: id });
                            }}
                            onSearchSubtitles={isLiveTV ? undefined : handleOpenSubtitleSearch}
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
                            onEnterPip={handleEnterPip}
                            currentProgram={currentProgram}
                            nextProgram={nextProgram}
                          />
                        </View>
                      </View>
                    </Animated.View>
                  </ControlsContainerComponent>
                  {/* TV: Render subtitle search modal inside TVControlsModal for proper layering */}
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
                      // so the video doesn't shift when status bar toggles.
                      // Use Constants.statusBarHeight (stable) instead of safeAreaInsets.top (dynamic)
                      // to prevent the controls from "bumping down" when the status bar appears.
                      Platform.OS === 'android' &&
                        !Platform.isTV &&
                        isPortrait && { paddingTop: Constants.statusBarHeight ?? 0 },
                    ]}
                    pointerEvents="box-none">
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
                        resolution={streamInfo?.resolution}
                        safeAreaInsets={safeAreaInsets}
                      />
                    </View>
                    <View style={styles.overlayControls} pointerEvents="box-none">
                      <Controls
                        key={`controls-mobile-${isPipActive}`}
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
                          // Save per-content language preference
                          saveContentLanguagePreference({ audioTrackId: id });
                        }}
                        subtitleTracks={subtitleTrackOptions}
                        selectedSubtitleTrackId={selectedSubtitleTrackId}
                        onSelectSubtitleTrack={(id) => {
                          console.log('[player] user selected subtitle track', { id, subtitleTrackOptions });
                          setSelectedSubtitleTrackId(id);
                          // Clear external subtitle when switching to embedded track
                          if (id !== 'external') {
                            externalSubtitleUrlRef.current = null;
                            setExternalSubtitleUrl(null);
                          }
                          // Save per-content language preference
                          saveContentLanguagePreference({ subtitleTrackId: id });
                        }}
                        onSearchSubtitles={isLiveTV ? undefined : handleOpenSubtitleSearch}
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
                        onEnterPip={handleEnterPip}
                        flashSkipButton={flashSkipButton}
                        currentProgram={currentProgram}
                        nextProgram={nextProgram}
                      />
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

          {/* Mobile: External subtitle search modal (TV renders inside TVControlsModal) */}
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

          {/* Subtitle sync debug overlay - shows timing values for debugging sync issues */}
          {subtitleDebugEnabled && (
            <View
              style={{
                position: 'absolute',
                top: Platform.isTV ? 120 : 60,
                left: Platform.isTV ? 24 : 12,
                backgroundColor: 'rgba(0, 0, 0, 0.85)',
                padding: Platform.isTV ? 24 : 12,
                borderRadius: Platform.isTV ? 16 : 8,
                minWidth: Platform.isTV ? 560 : 280,
                zIndex: 100,
              }}
              pointerEvents="none">
              <Text
                style={{
                  color: '#00ff00',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 22 : 11,
                  fontWeight: 'bold',
                  marginBottom: Platform.isTV ? 16 : 8,
                }}>
                SUBTITLE SYNC DEBUG
              </Text>
              <Text
                style={{
                  color: '#fff',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 8 : 4,
                }}>
                Current Time (abs): {currentTime.toFixed(3)}s
              </Text>
              <Text
                style={{
                  color: '#fff',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 8 : 4,
                }}>
                Player Relative: {(currentTime - playbackOffsetRef.current).toFixed(3)}s
              </Text>
              <Text
                style={{
                  color: '#0ff',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 8 : 4,
                }}>
                VTT Lookup Time:{' '}
                {(currentTime - playbackOffsetRef.current + (hlsSessionState.keyframeDelta ?? 0) - subtitleOffset).toFixed(3)}s
              </Text>
              <Text
                style={{
                  color: '#ff0',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 12 : 6,
                }}>
                Manual Offset: {subtitleOffset.toFixed(3)}s {subtitleOffset !== 0 ? '(USER ADJUSTED)' : ''}
              </Text>
              <Text
                style={{
                  color: '#aaa',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 18 : 9,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                --- OFFSETS ---
              </Text>
              <Text
                style={{
                  color: '#f0f',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                playbackOffset: {playbackOffsetRef.current.toFixed(3)}s (requested)
              </Text>
              <Text
                style={{
                  color: '#f0f',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                actualPlaybackOffset: {actualPlaybackOffsetRef.current.toFixed(3)}s (keyframe)
              </Text>
              <Text
                style={{
                  color: '#0ff',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                keyframeDelta: {(hlsSessionState.keyframeDelta ?? 0).toFixed(3)}s
              </Text>
              {lastSyncTime && (
                <Text
                  style={{
                    color: '#0f0',
                    fontFamily: 'monospace',
                    fontSize: Platform.isTV ? 20 : 10,
                    marginBottom: Platform.isTV ? 4 : 2,
                    fontWeight: 'bold',
                  }}>
                  ✓ SYNCED: {lastSyncTime}
                </Text>
              )}
              <Text
                style={{
                  color: '#aaa',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 18 : 9,
                  marginBottom: Platform.isTV ? 4 : 2,
                  marginTop: Platform.isTV ? 8 : 4,
                }}>
                --- FORMULA ---
              </Text>
              <Text
                style={{
                  color: '#0f0',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                timeOffset = -{playbackOffsetRef.current.toFixed(1)} + {(hlsSessionState.keyframeDelta ?? 0).toFixed(1)} -{' '}
                {subtitleOffset.toFixed(1)}
              </Text>
              <Text
                style={{
                  color: '#0f0',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 8 : 4,
                }}>
                = {(-playbackOffsetRef.current + (hlsSessionState.keyframeDelta ?? 0) - subtitleOffset).toFixed(3)}s
              </Text>
              <Text
                style={{
                  color: '#aaa',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 18 : 9,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                --- STATE ---
              </Text>
              <Text
                style={{
                  color: hasWarmSeekedRef.current ? '#9f9' : '#f90',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                hasWarmSeeked: {hasWarmSeekedRef.current ? 'TRUE' : 'FALSE'}
              </Text>
              <Text
                style={{
                  color: '#aaa',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                sessionBufferEnd: {sessionBufferEndRef.current.toFixed(3)}s
              </Text>
              <Text
                style={{
                  color: '#aaa',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                initialStartOffset: {initialStartOffset.toFixed(3)}s
              </Text>
              <Text
                style={{
                  color: '#aaa',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 18 : 9,
                  marginBottom: Platform.isTV ? 4 : 2,
                  marginTop: Platform.isTV ? 8 : 4,
                }}>
                --- VTT CUES ---
              </Text>
              <Text
                style={{
                  color: '#ff0',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                Total Cues: {subtitleDebugInfo?.totalCues ?? 'N/A'}
              </Text>
              <Text
                style={{
                  color: '#ff0',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                First Cue Start: {subtitleDebugInfo?.firstCueStart?.toFixed(3) ?? 'N/A'}s
              </Text>
              <Text
                style={{
                  color: '#0ff',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                VTT Adjusted Time: {subtitleDebugInfo?.adjustedTime?.toFixed(3) ?? 'N/A'}s
              </Text>
              <Text
                style={{
                  color: subtitleDebugInfo && subtitleDebugInfo.activeCueStart !== null ? '#0f0' : '#f00',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                Active Cue:{' '}
                {subtitleDebugInfo && subtitleDebugInfo.activeCueStart !== null
                  ? `${subtitleDebugInfo.activeCueStart.toFixed(3)}-${subtitleDebugInfo.activeCueEnd?.toFixed(3)}s`
                  : 'NONE'}
              </Text>
              {subtitleDebugInfo?.activeCueText && (
                <Text
                  style={{
                    color: '#9f9',
                    fontFamily: 'monospace',
                    fontSize: Platform.isTV ? 18 : 9,
                    marginBottom: Platform.isTV ? 4 : 2,
                  }}>
                  "{subtitleDebugInfo.activeCueText}"
                </Text>
              )}
              <Text
                style={{
                  color: '#aaa',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 18 : 9,
                  marginBottom: Platform.isTV ? 4 : 2,
                  marginTop: Platform.isTV ? 8 : 4,
                }}>
                --- SYNC CHECK ---
              </Text>
              <Text
                style={{
                  color: '#ff0',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                If subs late: decrease manual offset
              </Text>
              <Text
                style={{
                  color: '#ff0',
                  fontFamily: 'monospace',
                  fontSize: Platform.isTV ? 20 : 10,
                  marginBottom: Platform.isTV ? 4 : 2,
                }}>
                If subs early: increase manual offset
              </Text>
            </View>
          )}

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
    </>
  );
}
