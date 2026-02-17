/**
 * usePlayback -- owns prequeue, track overrides, resume modal, resolving state,
 * pulse animation, and all playback-initiation callbacks for the details page.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Platform } from 'react-native';
import {
  apiService,
  type NZBResult,
  type PrequeueStatusResponse,
  type SeriesEpisode,
} from '@/services/api';
import type { PlaybackPreference } from '@/components/BackendSettingsContext';
import { clearMemoryCache } from '@/components/Image';
import type { ManualTrackOverrides } from '../manual-selection';
import { findAudioTrackByLanguage, findSubtitleTrackByPreference } from '@/app/details/track-selection';
import {
  buildExternalPlayerTargets,
  getHealthFailureReason,
  getTimeoutMessage,
  initiatePlayback,
  isHealthFailureError,
  isTimeoutError,
} from '../playback';
import { buildEpisodeQuery, formatUnreleasedMessage, isEpisodeUnreleased, padNumber } from '../utils';
import { getUnplayableReleases } from '@/hooks/useUnplayableReleases';
import { activateKeepAwakeAsync, deactivateKeepAwake } from 'expo-keep-awake';
import Animated, {
  useAnimatedStyle,
  useSharedValue,
  withTiming,
  withRepeat,
  Easing,
  cancelAnimation,
} from 'react-native-reanimated';
import type { UserProfile } from '@/services/api';

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const SELECTION_TOAST_ID = 'details-nzb-status';

// ---------------------------------------------------------------------------
// Helper: format language code
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

interface EpisodeSearchContext {
  query: string;
  friendlyLabel: string;
  selectionMessage: string;
  episodeCode: string;
}

export interface UsePlaybackParams {
  // Identity
  titleId: string;
  title: string;
  mediaType: string;
  isSeries: boolean;
  activeUserId: string | null;
  imdbId: string;
  tvdbId: string;
  tmdbId: string;
  yearNumber: number | undefined;
  seriesIdentifier: string;

  // Display / layout
  headerImage: string;
  isIosWeb: boolean;
  isSelectBlocked: boolean;
  instanceId: string;

  // Router
  router: any;

  // Settings
  settings: any;
  userSettings: any;
  playbackPreference: PlaybackPreference;

  // From useEpisodeManager
  activeEpisode: SeriesEpisode | null;
  nextUpEpisode: SeriesEpisode | null;
  isShuffleMode: boolean;

  // From useDetailsData
  detailsBundle: import('@/services/api').DetailsBundleData | null;
  bundleReady: boolean;

  // Active user profile
  activeUser: UserProfile | null;

  // Toast
  showToast: (
    message: string,
    opts?: { tone?: 'info' | 'success' | 'danger'; id?: string; duration?: number },
  ) => string;
  hideToast: (id: string) => void;

  // Loading screen
  showLoadingScreen: () => void;
  hideLoadingScreen: () => void;
  setOnCancel: (handler: (() => void) | null) => void;

  // Trailer auto-play
  dismissTrailerAutoPlay: () => void;

  // Whether the details page is the visible route
  isDetailsPageActive: boolean;

  // Progress refresh (for re-fetch on return from player)
  progressRefreshKey: number;
  setProgressRefreshKey: React.Dispatch<React.SetStateAction<number>>;
}

export interface PlaybackResult {
  // Resolving state
  isResolving: boolean;
  setIsResolving: React.Dispatch<React.SetStateAction<boolean>>;
  selectionError: string | null;
  setSelectionError: React.Dispatch<React.SetStateAction<string | null>>;
  selectionInfo: string | null;
  setSelectionInfo: React.Dispatch<React.SetStateAction<string | null>>;
  showBlackOverlay: boolean;
  setShowBlackOverlay: React.Dispatch<React.SetStateAction<boolean>>;

  // Prequeue
  prequeueId: string | null;
  prequeueReady: boolean;
  prequeueDisplayInfo: PrequeueStatusResponse | null;
  prequeueTargetEpisode: { seasonNumber: number; episodeNumber: number } | null;
  prequeuePulseStyle: { opacity: number };

  // Track selection
  trackOverrideAudio: number | null;
  setTrackOverrideAudio: React.Dispatch<React.SetStateAction<number | null>>;
  trackOverrideSubtitle: number | null;
  setTrackOverrideSubtitle: React.Dispatch<React.SetStateAction<number | null>>;
  showAudioTrackModal: boolean;
  setShowAudioTrackModal: React.Dispatch<React.SetStateAction<boolean>>;
  showSubtitleTrackModal: boolean;
  setShowSubtitleTrackModal: React.Dispatch<React.SetStateAction<boolean>>;
  buildPrequeueAudioOptions: () => Array<{ id: string; label: string; description?: string }>;
  buildPrequeueSubtitleOptions: () => Array<{ id: string; label: string; description?: string }>;
  currentAudioTrackId: string | null;
  currentSubtitleTrackId: string | null;

  // Resume modal
  resumeModalVisible: boolean;
  setResumeModalVisible: React.Dispatch<React.SetStateAction<boolean>>;
  currentProgress: { position: number; duration: number; percentWatched: number } | null;
  setCurrentProgress: React.Dispatch<React.SetStateAction<{ position: number; duration: number; percentWatched: number } | null>>;
  pendingPlaybackAction: ((startOffset?: number) => Promise<void>) | null;
  setPendingPlaybackAction: React.Dispatch<React.SetStateAction<((startOffset?: number) => Promise<void>) | null>>;
  handleResumePlayback: () => Promise<void>;
  handlePlayFromBeginning: () => Promise<void>;
  handleCloseResumeModal: () => void;

  // Progress
  displayProgress: number | null;
  episodeProgressMap: Map<string, number>;

  // Actions
  resolveAndPlay: (args: {
    query: string;
    friendlyLabel: string;
    limit?: number;
    selectionMessage?: string | null;
    useDebugPlayer?: boolean;
    targetEpisode?: { seasonNumber: number; episodeNumber: number; airedDate?: string };
  }) => Promise<void>;
  handleWatchNow: () => Promise<void>;
  handleLaunchDebugPlayer: () => Promise<void>;
  handleInitiatePlayback: (
    result: NZBResult,
    signal?: AbortSignal,
    overrides?: { useDebugPlayer?: boolean; trackOverrides?: ManualTrackOverrides },
  ) => Promise<void>;
  fetchIndexerResults: (opts: {
    query?: string;
    limit?: number;
    categories?: string[];
  }) => Promise<NZBResult[]>;
  getEpisodeSearchContext: (
    episode: SeriesEpisode,
  ) => { query: string; friendlyLabel: string; selectionMessage: string; episodeCode: string } | null;
  checkAndShowResumeModal: (action: () => Promise<void>) => Promise<void>;
  showLoadingScreenIfEnabled: () => Promise<void>;

  // Refs for cross-hook communication
  abortControllerRef: React.MutableRefObject<AbortController | null>;
  pendingStartOffsetRef: React.MutableRefObject<number | null>;
  pendingShuffleModeRef: React.MutableRefObject<boolean>;
  resolveAndPlayRef: React.MutableRefObject<((...args: any[]) => Promise<void>) | null>;
  navigationPrequeueIdRef: React.MutableRefObject<{
    prequeueId: string;
    targetEpisode: { seasonNumber: number; episodeNumber: number };
  } | null>;
  navigationPrequeueStatusRef: React.MutableRefObject<PrequeueStatusResponse | null>;
}

// ---------------------------------------------------------------------------
// Hook implementation
// ---------------------------------------------------------------------------

export function usePlayback(params: UsePlaybackParams): PlaybackResult {
  const {
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
    headerImage,
    isIosWeb,
    isSelectBlocked,
    instanceId,
    router,
    settings,
    userSettings,
    playbackPreference,
    activeEpisode,
    nextUpEpisode,
    isShuffleMode,
    detailsBundle,
    bundleReady,
    activeUser,
    showToast,
    hideToast,
    showLoadingScreen,
    hideLoadingScreen,
    setOnCancel,
    dismissTrailerAutoPlay,
    isDetailsPageActive,
    progressRefreshKey,
    setProgressRefreshKey,
  } = params;

  // -------------------------------------------------------------------------
  // Resolving / error state
  // -------------------------------------------------------------------------
  const [isResolving, setIsResolving] = useState(false);
  const [selectionError, setSelectionError] = useState<string | null>(null);
  const [selectionInfo, setSelectionInfo] = useState<string | null>(null);
  const [showBlackOverlay, setShowBlackOverlay] = useState(false);

  // -------------------------------------------------------------------------
  // Prequeue state
  // -------------------------------------------------------------------------
  const [prequeueId, setPrequeueId] = useState<string | null>(null);
  const [prequeueReady, setPrequeueReady] = useState(false);
  const [prequeueDisplayInfo, setPrequeueDisplayInfo] = useState<PrequeueStatusResponse | null>(null);
  const [prequeueTargetEpisode, setPrequeueTargetEpisode] = useState<{
    seasonNumber: number;
    episodeNumber: number;
  } | null>(null);

  // Track pending prequeue request so play button can wait for it.
  // Returns both ID and target episode so we don't have to wait for state updates.
  const prequeuePromiseRef = useRef<Promise<{
    id: string;
    targetEpisode: { seasonNumber: number; episodeNumber: number } | null;
  } | null> | null>(null);

  // Cache prequeue status from navigation (player already resolved it)
  const navigationPrequeueStatusRef = useRef<PrequeueStatusResponse | null>(null);
  // Cache navigation prequeue ID even when not ready yet (to avoid React state timing issues)
  const navigationPrequeueIdRef = useRef<{
    prequeueId: string;
    targetEpisode: { seasonNumber: number; episodeNumber: number };
  } | null>(null);

  // -------------------------------------------------------------------------
  // Track overrides
  // -------------------------------------------------------------------------
  const [trackOverrideAudio, setTrackOverrideAudio] = useState<number | null>(null);
  const [trackOverrideSubtitle, setTrackOverrideSubtitle] = useState<number | null>(null);

  // Modal visibility for track selection
  const [showAudioTrackModal, setShowAudioTrackModal] = useState(false);
  const [showSubtitleTrackModal, setShowSubtitleTrackModal] = useState(false);

  // -------------------------------------------------------------------------
  // Resume modal
  // -------------------------------------------------------------------------
  const [resumeModalVisible, setResumeModalVisible] = useState(false);
  const [currentProgress, setCurrentProgress] = useState<{
    position: number;
    duration: number;
    percentWatched: number;
  } | null>(null);
  const [pendingPlaybackAction, setPendingPlaybackAction] = useState<
    ((startOffset?: number) => Promise<void>) | null
  >(null);

  // -------------------------------------------------------------------------
  // Progress
  // -------------------------------------------------------------------------
  const [displayProgress, setDisplayProgress] = useState<number | null>(null);
  const [episodeProgressMap, setEpisodeProgressMap] = useState<Map<string, number>>(new Map());

  // -------------------------------------------------------------------------
  // Refs
  // -------------------------------------------------------------------------
  const abortControllerRef = useRef<AbortController | null>(null);
  const pendingStartOffsetRef = useRef<number | null>(null);
  const pendingShuffleModeRef = useRef<boolean>(false);
  const resolveAndPlayRef = useRef<((...args: any[]) => Promise<void>) | null>(null);
  const initiatePlaybackRef = useRef<
    ((result: NZBResult, signal?: AbortSignal, overrides?: { useDebugPlayer?: boolean }) => Promise<void>) | null
  >(null);

  // -------------------------------------------------------------------------
  // Pulse animation for prequeue loading state
  // -------------------------------------------------------------------------
  const prequeuePulseOpacity = useSharedValue(1);
  const prequeueFadeIn = useSharedValue(0);
  const prequeuePulseStyle = useAnimatedStyle(() => {
    return {
      opacity: prequeuePulseOpacity.value * prequeueFadeIn.value,
    };
  });

  // Fade-in when prequeue info first appears
  useEffect(() => {
    if (prequeueDisplayInfo) {
      prequeueFadeIn.value = withTiming(1, { duration: 200, easing: Easing.out(Easing.ease) });
    } else {
      prequeueFadeIn.value = 0;
    }
  }, [!!prequeueDisplayInfo]);

  // Start/stop pulse animation based on prequeue status
  useEffect(() => {
    const isLoading = prequeueDisplayInfo && !prequeueReady && prequeueDisplayInfo.status !== 'failed';
    if (isLoading) {
      prequeuePulseOpacity.value = 0.4;
      prequeuePulseOpacity.value = withRepeat(
        withTiming(1, { duration: 600, easing: Easing.inOut(Easing.ease) }),
        -1,
        true,
      );
    } else {
      cancelAnimation(prequeuePulseOpacity);
      prequeuePulseOpacity.value = 1;
    }
  }, [prequeueDisplayInfo?.status, prequeueReady]);

  // -------------------------------------------------------------------------
  // Keep-awake during resolution
  // -------------------------------------------------------------------------
  useEffect(() => {
    if (isResolving) {
      activateKeepAwakeAsync().catch(() => {
        // Ignore errors -- keep-awake may not be available on all platforms
      });
    } else {
      deactivateKeepAwake();
    }
    return () => {
      deactivateKeepAwake();
    };
  }, [isResolving]);

  // -------------------------------------------------------------------------
  // Black overlay cleanup on unmount
  // -------------------------------------------------------------------------
  useEffect(() => {
    return () => {
      setShowBlackOverlay(false);
    };
  }, []);

  // -------------------------------------------------------------------------
  // Cancel handler for loading screen
  // -------------------------------------------------------------------------
  useEffect(() => {
    setOnCancel(() => {
      console.log('[usePlayback] Loading screen cancelled by user');
      if (abortControllerRef.current) {
        abortControllerRef.current.abort();
        abortControllerRef.current = null;
      }
      setShowBlackOverlay(false);
      setIsResolving(false);
    });
    return () => {
      setOnCancel(null);
    };
  }, [setOnCancel]);

  // -------------------------------------------------------------------------
  // Cleanup abort controller on unmount
  // -------------------------------------------------------------------------
  useEffect(() => {
    return () => {
      if (abortControllerRef.current) {
        console.log('[usePlayback] Cancelling pending playback due to navigation away');
        abortControllerRef.current.abort();
        abortControllerRef.current = null;
      }
    };
  }, []);

  // -------------------------------------------------------------------------
  // Selection error toast display
  // -------------------------------------------------------------------------
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

  // -------------------------------------------------------------------------
  // Selection info toast display
  // -------------------------------------------------------------------------
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

  // -------------------------------------------------------------------------
  // Debug: Track resume modal visibility changes
  // -------------------------------------------------------------------------
  useEffect(() => {
    console.log('[usePlayback] resumeModalVisible changed to:', resumeModalVisible);
    console.log('[usePlayback] currentProgress:', currentProgress);
  }, [resumeModalVisible, currentProgress]);

  // -------------------------------------------------------------------------
  // Build audio track options for selection modal
  // -------------------------------------------------------------------------
  const buildPrequeueAudioOptions = useCallback(() => {
    if (!prequeueDisplayInfo?.audioTracks) return [];
    return prequeueDisplayInfo.audioTracks.map((track) => ({
      id: String(track.index),
      label: `${formatLanguage(track.language)}${track.title ? ` - ${track.title}` : ''}`,
      description: track.codec?.toUpperCase(),
    }));
  }, [prequeueDisplayInfo?.audioTracks]);

  // -------------------------------------------------------------------------
  // Build subtitle track options for selection modal
  // -------------------------------------------------------------------------
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

  // -------------------------------------------------------------------------
  // Computed current audio track ID (accounting for overrides)
  // -------------------------------------------------------------------------
  const currentAudioTrackId = useMemo(() => {
    if (trackOverrideAudio !== null) return String(trackOverrideAudio);
    const selected = prequeueDisplayInfo?.selectedAudioTrack;
    return selected !== undefined && selected >= 0
      ? String(selected)
      : prequeueDisplayInfo?.audioTracks?.[0]?.index !== undefined
        ? String(prequeueDisplayInfo.audioTracks[0].index)
        : null;
  }, [trackOverrideAudio, prequeueDisplayInfo?.selectedAudioTrack, prequeueDisplayInfo?.audioTracks]);

  // -------------------------------------------------------------------------
  // Computed current subtitle track ID (accounting for overrides)
  // -------------------------------------------------------------------------
  const currentSubtitleTrackId = useMemo(() => {
    if (trackOverrideSubtitle !== null) return String(trackOverrideSubtitle);
    const selected = prequeueDisplayInfo?.selectedSubtitleTrack;
    return selected !== undefined && selected >= 0 ? String(selected) : '-1';
  }, [trackOverrideSubtitle, prequeueDisplayInfo?.selectedSubtitleTrack]);

  // =========================================================================
  // Prequeue initiation effect (deferred until bundleReady)
  // =========================================================================
  useEffect(() => {
    if (!bundleReady) return;

    console.log('[prequeue] useEffect triggered', {
      activeUserId: activeUserId ?? 'null',
      titleId: titleId ?? 'null',
      title: title ? title.substring(0, 30) : 'null',
      isSeries,
    });

    // For series, determine which episode to prequeue.
    // Priority: activeEpisode (user-selected) > nextUpEpisode (from watch history)
    const targetEpisode = isSeries ? activeEpisode || nextUpEpisode : null;

    // Check if we already have a navigation prequeue for this episode (from player's on-demand prequeue).
    // If so, skip creating a new prequeue -- it would cancel the one we're about to use.
    const navPrequeue = navigationPrequeueIdRef.current;
    if (navPrequeue && targetEpisode) {
      const navTarget = navPrequeue.targetEpisode;
      if (
        navTarget.seasonNumber === targetEpisode.seasonNumber &&
        navTarget.episodeNumber === targetEpisode.episodeNumber
      ) {
        console.log('[prequeue] Skipping new prequeue - using navigation prequeue:', navPrequeue.prequeueId);
        setPrequeueId(navPrequeue.prequeueId);
        setPrequeueTargetEpisode({
          seasonNumber: navTarget.seasonNumber,
          episodeNumber: navTarget.episodeNumber,
        });
        setPrequeueReady(false);
        prequeuePromiseRef.current = null;
        return;
      }
    }

    // Clear existing prequeue state immediately when episode changes
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
          absoluteEpisodeNumber: (targetEpisode as any)?.absoluteEpisodeNumber,
          skipHLS: Platform.OS !== 'web' ? true : undefined,
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
        // Silently fail -- prequeue is an optimization, not required
        if (!cancelled) {
          console.log('[prequeue] Prequeue failed (non-fatal):', error);
          setPrequeueId(null);
          setPrequeueTargetEpisode(null);
        }
        return null;
      }
    };

    // Debounce prequeue for series to avoid rapid requests when user navigates between episodes.
    // Movies start immediately since there's no episode navigation.
    const prequeueDelay = isSeries && targetEpisode ? 500 : 0;

    if (prequeueDelay > 0) {
      console.log('[prequeue] Debouncing prequeue for', prequeueDelay, 'ms');
      debounceTimer = setTimeout(() => {
        if (!cancelled) {
          prequeuePromiseRef.current = initiatePrequeue();
        }
      }, prequeueDelay);
    } else {
      prequeuePromiseRef.current = initiatePrequeue();
    }

    return () => {
      cancelled = true;
      if (debounceTimer) {
        clearTimeout(debounceTimer);
      }
      prequeuePromiseRef.current = null;
    };
  }, [titleId, title, mediaType, isSeries, activeUserId, imdbId, yearNumber, activeEpisode, nextUpEpisode, bundleReady]);

  // =========================================================================
  // Prequeue polling effect (polls status until ready)
  // =========================================================================
  useEffect(() => {
    if (!prequeueId) {
      setPrequeueReady(false);
      setPrequeueDisplayInfo(null);
      return;
    }

    let cancelled = false;
    let timeoutId: ReturnType<typeof setTimeout>;
    let lastStatus = '';

    const pollStatus = async () => {
      try {
        const response = await apiService.getPrequeueStatus(prequeueId);
        if (cancelled) return;

        // Only update state when status actually changes (avoids re-renders while polling)
        const statusChanged = response.status !== lastStatus;
        lastStatus = response.status;

        if (response.status === 'ready') {
          // When ready, compute expected subtitle track based on user preferences for display
          let displayResponse = response;
          if (response.subtitleTracks && response.subtitleTracks.length > 0) {
            if (response.selectedSubtitleTrack === undefined || response.selectedSubtitleTrack < 0) {
              const playbackSettings = userSettings?.playback ?? settings?.playback;
              const subLang = playbackSettings?.preferredSubtitleLanguage ?? 'eng';
              const subModeRaw = playbackSettings?.preferredSubtitleMode ?? 'off';
              const subMode =
                subModeRaw === 'on' || subModeRaw === 'off' || subModeRaw === 'forced-only' ? subModeRaw : 'off';

              const subtitleStreams = response.subtitleTracks.map((t) => ({
                index: t.index,
                language: t.language || '',
                title: t.title,
                isForced: t.forced,
                disposition: t.forced ? { forced: 1 } : undefined,
              }));

              // Get actual audio language for audio-aware subtitle selection
              const audioLang = playbackSettings?.preferredAudioLanguage ?? 'eng';
              const selectedAudioStream = response.audioTracks?.find(
                (t: { index: number; language?: string }) => t.index === response.selectedAudioTrack,
              );
              const actualAudioLang = selectedAudioStream?.language || audioLang;

              const computedSubtitleTrack = findSubtitleTrackByPreference(subtitleStreams, subLang, subMode, actualAudioLang);
              if (computedSubtitleTrack !== null) {
                displayResponse = { ...response, selectedSubtitleTrack: computedSubtitleTrack };
              }
            }
          }

          console.log('[prequeue] Prequeue is ready:', prequeueId);
          setPrequeueDisplayInfo(displayResponse);
          setPrequeueReady(true);
        } else if (apiService.isPrequeueInProgress(response.status)) {
          // Only update display info when status changes to avoid re-renders
          if (statusChanged) {
            setPrequeueDisplayInfo(response);
          }
          timeoutId = setTimeout(pollStatus, 1000);
        } else {
          // Failed or expired
          console.log('[prequeue] Prequeue failed/expired:', prequeueId, response.status);
          setPrequeueDisplayInfo(response);
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

  // =========================================================================
  // Display progress effect
  // For series: derived from episodeProgressMap (no extra API call)
  // For movies: derived from bundle or fetched individually
  // =========================================================================
  useEffect(() => {
    if (!activeUserId) {
      setDisplayProgress(null);
      return;
    }

    // For series, derive from episodeProgressMap to avoid a separate HTTP request
    if (isSeries) {
      const episodeToShow = activeEpisode || nextUpEpisode;
      if (!episodeToShow) {
        setDisplayProgress(null);
        return;
      }
      const key = `${episodeToShow.seasonNumber}-${episodeToShow.episodeNumber}`;
      const percent = episodeProgressMap.get(key);
      setDisplayProgress(percent ?? null);
      return;
    }

    // For movies, try bundle first
    const itemId = seriesIdentifier || titleId;
    if (!itemId) {
      setDisplayProgress(null);
      return;
    }

    // Derive from bundle's playback progress if available (initial load only)
    if (detailsBundle && progressRefreshKey === 0) {
      const bundleProgress = detailsBundle.playbackProgress.find(
        (p) => p.mediaType === 'movie' && p.itemId === itemId,
      );
      if (bundleProgress && bundleProgress.percentWatched > 5 && bundleProgress.percentWatched < 95) {
        setDisplayProgress(Math.round(bundleProgress.percentWatched));
      } else {
        setDisplayProgress(null);
      }
      return;
    }

    // Wait for bundle on initial load
    if (!bundleReady && progressRefreshKey === 0) return;

    // Fallback: fetch individually
    console.log('[usePlayback:Progress] Display progress falling through to individual fetch', {
      bundleReady,
      progressRefreshKey,
      hasBundle: !!detailsBundle,
      itemId,
    });
    let cancelled = false;

    const fetchProgress = async () => {
      try {
        console.log('[usePlayback:Progress] Fetching display progress', {
          mediaType: 'movie',
          itemId,
          source: 'displayProgress',
        });
        const progress = await apiService.getPlaybackProgress(activeUserId, 'movie', itemId);

        if (cancelled) return;

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
    episodeProgressMap,
    seriesIdentifier,
    titleId,
    progressRefreshKey,
    detailsBundle,
    bundleReady,
  ]);

  // =========================================================================
  // Episode progress map fetch
  // =========================================================================
  useEffect(() => {
    if (!activeUserId || !isSeries || !seriesIdentifier) {
      setEpisodeProgressMap(new Map());
      return;
    }

    const buildProgressMap = (progressList: import('@/services/api').PlaybackProgress[]) => {
      const progressMap = new Map<string, number>();
      const itemIdPrefix = `${seriesIdentifier}:`;
      for (const progress of progressList) {
        if (progress.mediaType !== 'episode') continue;
        const matchesSeriesId = progress.seriesId === seriesIdentifier;
        const matchesItemIdPrefix = progress.itemId?.startsWith(itemIdPrefix);
        if (matchesSeriesId || matchesItemIdPrefix) {
          let seasonNum = progress.seasonNumber;
          let episodeNum = progress.episodeNumber;
          if ((!seasonNum || !episodeNum) && progress.itemId) {
            const match = progress.itemId.match(/:S(\d+)E(\d+)$/i);
            if (match) {
              seasonNum = parseInt(match[1], 10);
              episodeNum = parseInt(match[2], 10);
            }
          }
          if (seasonNum && episodeNum) {
            const key = `${seasonNum}-${episodeNum}`;
            if (progress.percentWatched > 5 && progress.percentWatched < 95) {
              progressMap.set(key, Math.round(progress.percentWatched));
            }
          }
        }
      }
      return progressMap;
    };

    // Wait for bundle attempt before falling back (only on initial load)
    if (!bundleReady && progressRefreshKey === 0) return;

    // Already hydrated from bundle on initial load -- skip redundant fetch.
    // The bundle hydration writes to episodeProgressMap in the useDetailsData consolidated effect,
    // so we only need to re-fetch when progressRefreshKey > 0 (return from player).
    if (progressRefreshKey === 0 && detailsBundle) return;

    let cancelled = false;

    console.log('[usePlayback:Progress] Episode progress falling through to individual fetch', {
      bundleReady,
      progressRefreshKey,
      hasBundle: !!detailsBundle,
      seriesIdentifier,
    });
    const fetchAllProgress = async () => {
      try {
        console.log('[usePlayback:Progress] Fetching ALL progress for episode map', {
          seriesIdentifier,
          source: 'episodeProgressMap',
        });
        const progressList = await apiService.listPlaybackProgress(activeUserId);
        if (cancelled) return;
        setEpisodeProgressMap(buildProgressMap(progressList));
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
  }, [activeUserId, isSeries, seriesIdentifier, progressRefreshKey, bundleReady, detailsBundle]);

  // =========================================================================
  // fetchIndexerResults
  // =========================================================================
  const fetchIndexerResults = useCallback(
    async ({ query, limit = 5, categories = [] }: { query?: string; limit?: number; categories?: string[] }) => {
      const searchQuery = (query ?? title ?? '').toString().trim();
      if (!searchQuery) {
        throw new Error('Missing title to search for results.');
      }
      const imdbIdToUse = imdbId;
      console.log('[usePlayback] fetchIndexerResults', {
        searchQuery,
        imdbId,
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

  // =========================================================================
  // getEpisodeSearchContext
  // =========================================================================
  const getEpisodeSearchContext = useCallback(
    (episode: SeriesEpisode): EpisodeSearchContext | null => {
      const trimmedTitle = title.trim();
      const baseTitle = trimmedTitle || title;
      const query = buildEpisodeQuery(baseTitle, episode.seasonNumber, episode.episodeNumber);
      if (!query) {
        return null;
      }

      const episodeCode = `S${padNumber(episode.seasonNumber)}E${padNumber(episode.episodeNumber)}`;
      const labelSuffix = episode.name ? ` -- "${episode.name}"` : '';
      const friendlyLabel = baseTitle ? `${baseTitle} ${episodeCode}${labelSuffix}` : `${episodeCode}${labelSuffix}`;
      const selectionMessage = baseTitle ? `${baseTitle} \u2022 ${episodeCode}` : episodeCode;

      return {
        query,
        friendlyLabel,
        selectionMessage,
        episodeCode,
      };
    },
    [title],
  );

  // =========================================================================
  // handleInitiatePlayback (wraps initiatePlayback from ../playback)
  // =========================================================================
  const handleInitiatePlayback = useCallback(
    async (
      result: NZBResult,
      signal?: AbortSignal,
      overrides?: { useDebugPlayer?: boolean; trackOverrides?: ManualTrackOverrides },
    ) => {
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
          mediaType: activeEpisode || isSeries ? 'episode' : 'movie',
          seriesTitle: activeEpisode || isSeries ? title : undefined,
          year: yearNumber,
          seasonNumber: activeEpisode?.seasonNumber,
          episodeNumber: activeEpisode?.episodeNumber,
          episodeName: activeEpisode?.name,
          signal,
          titleId,
          imdbId,
          tvdbId,
          ...(() => {
            const offset = pendingStartOffsetRef.current;
            if (offset !== null) {
              pendingStartOffsetRef.current = null;
              return { startOffset: offset };
            }
            if (currentProgress) {
              return { startOffset: currentProgress.position };
            }
            return {};
          })(),
          ...(overrides?.useDebugPlayer ? { debugPlayer: true } : {}),
          onExternalPlayerLaunch: hideLoadingScreen,
          userSettings,
          profileId: activeUserId ?? undefined,
          profileName: activeUser?.name,
          shuffleMode: pendingShuffleModeRef.current || isShuffleMode,
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

  // Keep ref in sync
  useEffect(() => {
    initiatePlaybackRef.current = handleInitiatePlayback;
  }, [handleInitiatePlayback]);

  // =========================================================================
  // showLoadingScreenIfEnabled
  // =========================================================================
  const showLoadingScreenIfEnabled = useCallback(async () => {
    const isLoadingScreenEnabled =
      userSettings?.playback?.useLoadingScreen ?? settings?.playback?.useLoadingScreen ?? false;
    if (isLoadingScreenEnabled) {
      setShowBlackOverlay(true);
      await new Promise((resolve) => setTimeout(resolve, 50));
      showLoadingScreen();
    }
  }, [userSettings?.playback?.useLoadingScreen, settings?.playback?.useLoadingScreen, showLoadingScreen]);

  // =========================================================================
  // Helper: extract episode info from a query string
  // =========================================================================
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

  // =========================================================================
  // Helper: check if prequeue target matches the requested playback
  // =========================================================================
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

  // =========================================================================
  // pollPrequeueUntilReady
  // =========================================================================
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

  // =========================================================================
  // launchFromPrequeue (internal -- builds stream URL, routes to player)
  // =========================================================================
  const launchFromPrequeue = useCallback(
    async (prequeueStatus: PrequeueStatusResponse) => {
      if (!prequeueStatus.streamPath) {
        throw new Error('Prequeue is missing stream path');
      }

      // Get start offset from pending ref (for resume playback) -- get it early as we may use it for HLS session
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

      // Check for external player FIRST -- they handle HDR natively and don't need HLS
      const isExternalPlayer = playbackPreference === 'infuse' || playbackPreference === 'outplayer';
      if (isExternalPlayer) {
        console.log('[prequeue] External player selected, skipping HLS creation');
        const label = playbackPreference === 'outplayer' ? 'Outplayer' : 'Infuse';

        // Build backend proxy URL for external player (handles IP-locked debrid URLs)
        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const authToken = apiService.getAuthToken();
        const queryParts: string[] = [];
        queryParts.push(`path=${encodeURIComponent(prequeueStatus.streamPath)}`);
        queryParts.push('transmux=0');
        if (authToken) {
          queryParts.push(`token=${encodeURIComponent(authToken)}`);
        }
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

      // Native platforms use NativePlayer (KSPlayer/MPV) which handles HDR/tracks/seeking natively
      const isNativePlatform = Platform.OS !== 'web';
      const needsHLS = false; // Native platforms use direct streaming, web uses HLS for HDR/TrueHD

      // Build stream URL
      let streamUrl: string;
      let hlsDuration: number | undefined;
      let hlsActualStartOffset: number | undefined;

      // Log the decision factors for HLS path
      const audioOverrideDiffersFromPrequeue =
        trackOverrideAudio !== null && trackOverrideAudio !== prequeueStatus.selectedAudioTrack;
      console.log('[prequeue] HLS decision factors:', {
        hasDolbyVision: prequeueStatus.hasDolbyVision,
        hasHdr10: prequeueStatus.hasHdr10,
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

      // Track selected audio/subtitle tracks for passing to player
      let selectedAudioTrack: number | undefined;
      let selectedSubtitleTrack: number | undefined;

      if (!prequeueUserIdMatches && prequeueStatus.hlsPlaylistUrl) {
        console.log('[prequeue] Pre-created HLS userId mismatch, will create new session', {
          prequeueUserId: prequeueStatus.userId,
          activeUserId,
        });
      }

      if (!audioTrackMatchesPrequeue && prequeueStatus.hlsPlaylistUrl) {
        console.log('[prequeue] Audio track override differs from prequeue, will create new HLS session', {
          trackOverrideAudio,
          prequeueSelectedAudio: prequeueStatus.selectedAudioTrack,
        });
      }

      if (canUsePreCreatedHLS) {
        // HDR/TrueHD content with HLS session already created by backend (no resume position)
        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const authToken = apiService.getAuthToken();
        streamUrl = `${baseUrl}${prequeueStatus.hlsPlaylistUrl}${authToken ? `?token=${encodeURIComponent(authToken)}` : ''}`;
        if (typeof prequeueStatus.duration === 'number' && prequeueStatus.duration > 0) {
          hlsDuration = prequeueStatus.duration;
          console.log('[prequeue] Using duration from prequeue:', hlsDuration);
        }
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
        console.log('[prequeue] Using PRE-CREATED HLS stream URL:', streamUrl, {
          selectedAudioTrack,
          selectedSubtitleTrack,
          trackOverrideAudio,
          trackOverrideSubtitle,
        });
      } else if (needsHLS && Platform.OS !== 'web') {
        // HDR/TrueHD content -- create HLS session with start offset
        console.log('[prequeue] Creating NEW HLS session (not using prequeue HLS)');
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

          if (selectedAudioTrack !== undefined || selectedSubtitleTrack !== undefined) {
            console.log(
              `[prequeue] Using tracks: audio=${selectedAudioTrack}, subtitle=${selectedSubtitleTrack}`,
              { trackOverrideAudio, trackOverrideSubtitle },
            );
          }

          // Fetch metadata if either track still needs selection based on user preferences
          if (
            (selectedAudioTrack === undefined || selectedSubtitleTrack === undefined) &&
            (settings?.playback || userSettings?.playback)
          ) {
            try {
              const audioLang =
                userSettings?.playback?.preferredAudioLanguage ?? settings?.playback?.preferredAudioLanguage ?? 'eng';
              const metadata = await apiService.getVideoMetadata(prequeueStatus.streamPath, { audioLang });
              if (metadata) {
                const subLang =
                  userSettings?.playback?.preferredSubtitleLanguage ??
                  settings?.playback?.preferredSubtitleLanguage ??
                  'eng';
                const subModeRaw =
                  userSettings?.playback?.preferredSubtitleMode ?? settings?.playback?.preferredSubtitleMode ?? 'off';
                const subMode =
                  subModeRaw === 'on' || subModeRaw === 'off' || subModeRaw === 'forced-only' ? subModeRaw : 'off';

                if (selectedAudioTrack === undefined && metadata.audioStreams) {
                  const match = findAudioTrackByLanguage(metadata.audioStreams, audioLang);
                  if (match !== null) {
                    selectedAudioTrack = match;
                    console.log(`[prequeue] Selected audio track ${match} for language ${audioLang}`);
                  }
                }

                if (selectedSubtitleTrack === undefined && metadata.subtitleStreams) {
                  // Get actual audio language for audio-aware subtitle selection
                  const selectedAudioStream = metadata.audioStreams?.find(
                    (s: { index: number; language?: string }) => s.index === selectedAudioTrack,
                  );
                  const actualAudioLang = selectedAudioStream?.language || audioLang;

                  const match = findSubtitleTrackByPreference(
                    metadata.subtitleStreams,
                    subLang,
                    subMode as 'off' | 'on' | 'forced-only',
                    actualAudioLang,
                  );
                  if (match !== null) {
                    selectedSubtitleTrack = match;
                    console.log(
                      `[prequeue] Selected subtitle track ${match} for language ${subLang} (mode: ${subMode}, audioLang: ${actualAudioLang})`,
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
        // Native platforms use direct stream URL (NativePlayer handles HDR/tracks/seeking)
        // Web fallback for SDR content also uses direct streaming
        console.log('[prequeue] Building direct stream URL for native player');

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

        // Fetch metadata if either track still needs selection based on user preferences
        if (
          (selectedAudioTrack === undefined || selectedSubtitleTrack === undefined) &&
          (settings?.playback || userSettings?.playback)
        ) {
          try {
            const audioLang =
              userSettings?.playback?.preferredAudioLanguage ?? settings?.playback?.preferredAudioLanguage ?? 'eng';
            const metadata = await apiService.getVideoMetadata(prequeueStatus.streamPath, { audioLang });
            if (metadata) {
              const subLang =
                userSettings?.playback?.preferredSubtitleLanguage ??
                settings?.playback?.preferredSubtitleLanguage ??
                'eng';
              const subModeRaw =
                userSettings?.playback?.preferredSubtitleMode ?? settings?.playback?.preferredSubtitleMode ?? 'off';
              const subMode =
                subModeRaw === 'on' || subModeRaw === 'off' || subModeRaw === 'forced-only' ? subModeRaw : 'off';

              if (selectedAudioTrack === undefined && metadata.audioStreams) {
                const match = findAudioTrackByLanguage(metadata.audioStreams, audioLang);
                if (match !== null) {
                  selectedAudioTrack = match;
                  console.log(`[prequeue] Native player: selected audio track ${match} for language ${audioLang}`);
                }
              }

              if (selectedSubtitleTrack === undefined && metadata.subtitleStreams) {
                // Get actual audio language for audio-aware subtitle selection
                const selectedAudioStream = metadata.audioStreams?.find(
                  (s: { index: number; language?: string }) => s.index === selectedAudioTrack,
                );
                const actualAudioLang = selectedAudioStream?.language || audioLang;

                const match = findSubtitleTrackByPreference(
                  metadata.subtitleStreams,
                  subLang,
                  subMode as 'off' | 'on' | 'forced-only',
                  actualAudioLang,
                );
                if (match !== null) {
                  selectedSubtitleTrack = match;
                  console.log(
                    `[prequeue] Native player: selected subtitle track ${match} for language ${subLang} (mode: ${subMode}, audioLang: ${actualAudioLang})`,
                  );
                }
              }
            }
          } catch (metadataError) {
            console.warn('[prequeue] Native player: failed to fetch metadata for track selection:', metadataError);
          }
        }

        console.log('[prequeue] Native player preselected tracks:', {
          audio: selectedAudioTrack,
          subtitle: selectedSubtitleTrack,
          trackOverrideAudio,
          trackOverrideSubtitle,
          prequeueAudio: prequeueStatus.selectedAudioTrack,
          prequeueSubtitle: prequeueStatus.selectedSubtitleTrack,
        });

        const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
        const authToken = apiService.getAuthToken();
        const queryParts: string[] = [];
        queryParts.push(`path=${encodeURIComponent(prequeueStatus.streamPath)}`);
        if (authToken) {
          queryParts.push(`token=${encodeURIComponent(authToken)}`);
        }
        queryParts.push('transmux=0');
        if (activeUserId) {
          queryParts.push(`profileId=${encodeURIComponent(activeUserId)}`);
        }
        if (activeUser?.name) {
          queryParts.push(`profileName=${encodeURIComponent(activeUser.name)}`);
        }
        streamUrl = `${baseUrl}/video/stream?${queryParts.join('&')}`;
        if (typeof prequeueStatus.duration === 'number' && prequeueStatus.duration > 0) {
          hlsDuration = prequeueStatus.duration;
          console.log('[prequeue] Using duration from prequeue:', hlsDuration);
        }
        console.log('[prequeue] Using direct stream URL:', streamUrl);

        // SDR path: Start subtitle extraction with correct offset (lazy extraction)
        if (prequeueStatus.prequeueId) {
          try {
            const subtitleResult = await apiService.startPrequeueSubtitles(prequeueStatus.prequeueId, startOffset ?? 0);
            if (subtitleResult.subtitleSessions && Object.keys(subtitleResult.subtitleSessions).length > 0) {
              prequeueStatus.subtitleSessions = subtitleResult.subtitleSessions;
              console.log(
                '[prequeue] Started subtitle extraction for',
                Object.keys(subtitleResult.subtitleSessions).length,
                'tracks at offset',
                startOffset ?? 0,
              );
            }
          } catch (subtitleError) {
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

      // Launch native player
      console.log('[usePlayback] ===== LAUNCHING PLAYER =====', {
        isNativePlatform,
        selectedAudioTrack,
        selectedSubtitleTrack,
        audioTracksFromPrequeue: prequeueStatus.audioTracks?.map((t, i) => ({
          arrayIndex: i,
          streamIndex: t.index,
          language: t.language,
          title: t.title,
        })),
        subtitleTracksFromPrequeue: prequeueStatus.subtitleTracks?.map((t, i) => ({
          arrayIndex: i,
          streamIndex: t.index,
          language: t.language,
          title: t.title,
        })),
        streamUrl: streamUrl.substring(0, 100) + '...',
      });

      // Android TV: free GL-cached browse UI bitmaps before playback allocates video buffers
      if (Platform.isTV && Platform.OS === 'android') {
        await clearMemoryCache();
      }

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
          // Pass prequeue-selected tracks so player knows what's baked into the HLS session.
          // For native player (KSPlayer), convert stream index to relative index (position in track array).
          // KSPlayer uses array indices (0, 1, 2) not ffprobe stream indices (1, 2, 4, etc.)
          ...((() => {
            if (selectedAudioTrack === undefined || selectedAudioTrack < 0) return {};
            const hasTrackArray = isNativePlatform && prequeueStatus.audioTracks && prequeueStatus.audioTracks.length > 0;
            const relativeIndex = hasTrackArray
              ? prequeueStatus.audioTracks!.findIndex((t) => t.index === selectedAudioTrack)
              : selectedAudioTrack;
            console.log('[prequeue] Audio track conversion:', {
              isNativePlatform,
              selectedAudioTrack,
              hasTrackArray,
              audioTracksLength: prequeueStatus.audioTracks?.length,
              relativeIndex,
            });
            return { preselectedAudioTrack: String(relativeIndex) };
          })()),
          ...((() => {
            if (selectedSubtitleTrack === undefined || selectedSubtitleTrack < 0) return {};
            const hasTrackArray = isNativePlatform && prequeueStatus.subtitleTracks && prequeueStatus.subtitleTracks.length > 0;
            const relativeIndex = hasTrackArray
              ? prequeueStatus.subtitleTracks!.findIndex((t) => t.index === selectedSubtitleTrack)
              : selectedSubtitleTrack;
            console.log('[prequeue] Subtitle track conversion:', {
              isNativePlatform,
              selectedSubtitleTrack,
              hasTrackArray,
              subtitleTracksLength: prequeueStatus.subtitleTracks?.length,
              relativeIndex,
            });
            return { preselectedSubtitleTrack: String(relativeIndex) };
          })()),
          // AIOStreams passthrough format data for info modal
          ...(prequeueStatus.passthroughName ? { passthroughName: prequeueStatus.passthroughName } : {}),
          ...(prequeueStatus.passthroughDescription
            ? { passthroughDescription: prequeueStatus.passthroughDescription }
            : {}),
          // Native player flags for direct streaming (bypasses HLS)
          ...(isNativePlatform ? { useNativePlayer: '1' } : {}),
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
      activeUserId,
      activeUser,
    ],
  );

  // =========================================================================
  // resolveAndPlay -- the main "find and play" callback
  // =========================================================================
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

      // 1. Check for navigation prequeue first (passed from player's next episode prequeue)
      const navPrequeue = navigationPrequeueStatusRef.current;
      if (navPrequeue && targetEpisode && apiService.isPrequeueReady(navPrequeue.status)) {
        const navTarget = navPrequeue.targetEpisode;
        if (
          navTarget &&
          navTarget.seasonNumber === targetEpisode.seasonNumber &&
          navTarget.episodeNumber === targetEpisode.episodeNumber
        ) {
          console.log('[prequeue] Using navigation prequeue from player:', navPrequeue.prequeueId);
          navigationPrequeueStatusRef.current = null;
          navigationPrequeueIdRef.current = null;
          setSelectionInfo(null);
          await launchFromPrequeue(navPrequeue);
          return;
        }
      }

      // 2. Check for navigation prequeue ID (may not be ready yet, but we should wait for it)
      const navPrequeueId = navigationPrequeueIdRef.current;
      if (navPrequeueId && targetEpisode) {
        const navTarget = navPrequeueId.targetEpisode;
        if (
          navTarget.seasonNumber === targetEpisode.seasonNumber &&
          navTarget.episodeNumber === targetEpisode.episodeNumber
        ) {
          console.log('[prequeue] Found navigation prequeue ID (may not be ready yet):', navPrequeueId.prequeueId);
          const abortController = new AbortController();
          abortControllerRef.current = abortController;
          setSelectionError(null);
          setSelectionInfo('Waiting for pre-loaded stream...');
          setIsResolving(true);

          try {
            const readyStatus = await pollPrequeueUntilReady(navPrequeueId.prequeueId, abortController.signal);
            if (abortController.signal.aborted) {
              return;
            }
            if (readyStatus) {
              console.log('[prequeue] Navigation prequeue became ready:', navPrequeueId.prequeueId);
              navigationPrequeueIdRef.current = null;
              navigationPrequeueStatusRef.current = null;
              setSelectionInfo(null);
              await launchFromPrequeue(readyStatus);
              return;
            }
            console.error('[prequeue] Navigation prequeue failed to become ready:', navPrequeueId.prequeueId);
            setSelectionError('Failed to prepare next episode. Please try again.');
            navigationPrequeueIdRef.current = null;
            return;
          } catch (error) {
            console.error('[prequeue] Navigation prequeue polling failed:', error);
            setSelectionError('Failed to prepare next episode. Please try again.');
            navigationPrequeueIdRef.current = null;
            return;
          } finally {
            setIsResolving(false);
            abortControllerRef.current = null;
          }
        }
      }

      // 3. Wait for any pending prequeue request to complete first
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

      // 4. Check if we can use prequeue
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
            navigationPrequeueStatusRef.current = null;
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
            console.log('[prequeue] Using ready prequeue:', currentPrequeueId);
            setSelectionInfo(null);
            await launchFromPrequeue(status);
            return;
          }

          if (apiService.isPrequeueInProgress(status.status)) {
            console.log('[prequeue] Prequeue still loading, polling...');
            const readyStatus = await pollPrequeueUntilReady(currentPrequeueId, abortController.signal);

            if (abortController.signal.aborted) {
              return;
            }

            if (readyStatus) {
              console.log('[prequeue] Prequeue became ready');
              setSelectionInfo(null);
              await launchFromPrequeue(readyStatus);
              return;
            }
            // Prequeue failed -- show error instead of falling back to normal flow
            console.error('[prequeue] Prequeue did not become ready:', currentPrequeueId);
            setSelectionError('Failed to prepare stream. Please try again.');
            setPrequeueId(null);
            setPrequeueTargetEpisode(null);
            return;
          } else {
            // Prequeue failed/expired -- show error instead of falling back
            console.error('[prequeue] Prequeue not usable (status:', status.status, '):', currentPrequeueId);
            setSelectionError('Stream preparation failed. Please try again.');
            setPrequeueId(null);
            setPrequeueTargetEpisode(null);
            return;
          }
        } catch (error) {
          console.error('[prequeue] Prequeue check failed:', error);
          setSelectionError('Failed to check stream status. Please try again.');
          setPrequeueId(null);
          setPrequeueTargetEpisode(null);
          return;
        } finally {
          setIsResolving(false);
          abortControllerRef.current = null;
        }
      } else {
        console.log('[prequeue] Skipping prequeue path:', {
          hasPrequeueId: !!currentPrequeueId,
          prequeueMatches,
          reason: !currentPrequeueId ? 'no prequeueId' : 'prequeue does not match query',
        });
      }

      // 5. Normal indexer search flow with health check loop
      console.log('[prequeue] Using NORMAL playback flow (not prequeue)');

      // Cancel any pending playback
      if (abortControllerRef.current) {
        console.log('[usePlayback] Cancelling previous playback request');
        abortControllerRef.current.abort();
      }

      const abortController = new AbortController();
      abortControllerRef.current = abortController;

      const trimmedQuery = query.trim();
      if (!trimmedQuery) {
        setSelectionError(`Missing search query for ${friendlyLabel}.`);
        abortControllerRef.current = null;
        return;
      }

      console.log('[usePlayback] PLAYBACK REQUEST:', {
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
        if (abortController.signal.aborted) {
          console.log('[usePlayback] Playback was cancelled before starting');
          return;
        }
        const results = await fetchIndexerResults({ query: trimmedQuery, limit });
        if (!results || results.length === 0) {
          if (targetEpisode?.airedDate && isEpisodeUnreleased(targetEpisode.airedDate)) {
            setSelectionError(formatUnreleasedMessage(friendlyLabel, targetEpisode.airedDate));
          } else {
            setSelectionError(`No results returned for ${friendlyLabel}.`);
          }
          return;
        }

        console.log(
          '[usePlayback] RAW RESULTS from search:',
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
          if (titleId || imdbId) {
            const seriesIdWithoutEpisode = titleId ? titleId.replace(/:S\d{2}E\d{2}$/i, '') : '';
            console.log(`[usePlayback] Filtering by IDs: titleId="${seriesIdWithoutEpisode}", imdbId="${imdbId || 'none'}"`);
            const matchingResults = results.filter((result) => {
              const resultTitleId = result.attributes?.titleId;
              if (!resultTitleId) {
                return true;
              }
              const resultIdWithoutEpisode = resultTitleId.replace(/:S\d{2}E\d{2}$/i, '');
              if (seriesIdWithoutEpisode && resultIdWithoutEpisode === seriesIdWithoutEpisode) {
                return true;
              }
              if (imdbId && resultIdWithoutEpisode === imdbId) {
                return true;
              }
              return false;
            });

            if (matchingResults.length > 0 && matchingResults.length < results.length) {
              console.log(`[usePlayback] Filtered ${results.length} results to ${matchingResults.length} by titleId/imdbId match`);
              return matchingResults;
            }
          }

          // Fallback: filter by title name similarity
          const searchTitle = title.trim().toLowerCase();
          if (searchTitle) {
            const matchingResults = results.filter((result) => {
              const resultTitleName = result.attributes?.titleName;
              if (!resultTitleName) {
                return true;
              }
              const resultNameLower = resultTitleName.trim().toLowerCase();
              return (
                resultNameLower === searchTitle ||
                resultNameLower.includes(searchTitle) ||
                searchTitle.includes(resultNameLower)
              );
            });

            if (matchingResults.length > 0 && matchingResults.length < results.length) {
              console.log(`[usePlayback] Filtered ${results.length} results to ${matchingResults.length} by title name match`);
              return matchingResults;
            }
          }

          console.warn(`[usePlayback] Could not filter results by titleId or title name, using all ${results.length} results`);
          return results;
        })();

        if (filteredResults.length === 0) {
          setSelectionError(`No matching results found for ${friendlyLabel}.`);
          return;
        }

        console.log(
          '[usePlayback] FILTERED RESULTS (after titleId/titleName filtering):',
          filteredResults.map((r, idx) => ({
            index: idx,
            title: r.title,
            serviceType: r.serviceType,
            indexer: r.indexer,
            titleId: r.attributes?.titleId,
            titleName: r.attributes?.titleName,
          })),
        );

        // 6. Filter out releases that have been marked as unplayable
        const unplayableReleases = await getUnplayableReleases();
        const playableResults = filteredResults.filter((result) => {
          if (!result.title) return true;
          const normalizedTitle = result.title
            .toLowerCase()
            .trim()
            .replace(/\.(mkv|mp4|avi|m4v|webm|ts)$/i, '');
          const isUnplayable = unplayableReleases.some((u) => {
            if (!u.title) return false;
            const storedTitle = u.title
              .toLowerCase()
              .trim()
              .replace(/\.(mkv|mp4|avi|m4v|webm|ts)$/i, '');
            return normalizedTitle === storedTitle;
          });
          if (isUnplayable) {
            console.log(`[usePlayback] Skipping unplayable release: "${result.title}"`);
          }
          return !isUnplayable;
        });

        if (playableResults.length === 0 && filteredResults.length > 0) {
          setSelectionError(`All matching releases for ${friendlyLabel} have been marked as unplayable.`);
          return;
        }

        const prioritizedResults = playableResults;

        console.log(
          '[usePlayback] ATTEMPTING PLAYBACK with these results in order:',
          prioritizedResults.map((r, idx) => ({
            index: idx,
            title: r.title,
            serviceType: r.serviceType,
            indexer: r.indexer,
          })),
        );
        const playbackHandler = initiatePlaybackRef.current;
        if (!playbackHandler) {
          console.error('[usePlayback] Playback handler unavailable when attempting to resolve search result.');
          setSelectionError(`Unable to start playback for ${friendlyLabel}.`);
          return;
        }

        let lastHealthFailure = false;
        let lastHealthFailureReason: string | null = null;
        for (let index = 0; index < prioritizedResults.length; index += 1) {
          if (abortController.signal.aborted) {
            console.log('[usePlayback] Playback was cancelled during resolution');
            return;
          }

          const candidate = prioritizedResults[index];
          console.log(
            `[usePlayback] [${index + 1}/${prioritizedResults.length}] Trying: "${candidate.title}" (${candidate.serviceType}) from ${candidate.indexer}`,
          );
          try {
            await playbackHandler(candidate, abortController.signal, { useDebugPlayer });

            if (abortController.signal.aborted) {
              console.log('[usePlayback] Playback was cancelled after initiation');
              return;
            }

            console.log(
              `[usePlayback] [${index + 1}/${prioritizedResults.length}] SUCCESS! Playback initiated for "${candidate.title}"`,
            );
            if (abortControllerRef.current === abortController) {
              abortControllerRef.current = null;
            }
            return;
          } catch (err) {
            const message = err instanceof Error ? err.message : String(err);
            const healthFailure = isHealthFailureError(err);
            const healthReason = healthFailure ? getHealthFailureReason(err) : null;

            console.log(
              `[usePlayback] [${index + 1}/${prioritizedResults.length}] FAILED: "${candidate.title}" - Error: ${message} - IsHealthFailure: ${healthFailure} - Reason: ${healthReason || 'none'}`,
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

              console.warn('[usePlayback] Health check failed for auto-selected release; evaluating fallback.', {
                candidateLabel,
                indexer: candidate.indexer,
                error: message,
              });

              if (moreCandidatesRemain) {
                console.log(
                  `[usePlayback] Health failure, continuing to next candidate (${prioritizedResults.length - nextIndex} remaining)`,
                );
                const failurePrefix = healthReason ? `Health check reported ${healthReason}` : 'Health check failed';
                setSelectionInfo(
                  `${failurePrefix} for "${candidateLabel}" from ${indexerLabel}. Trying another release...`,
                );
                setSelectionError(null);
                continue;
              }

              console.log(`[usePlayback] All ${prioritizedResults.length} candidates failed health checks. Stopping.`);
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

            console.log(`[usePlayback] Non-health failure error, stopping attempts.`);
            setSelectionInfo(null);
            setSelectionError(message || `Unable to start playback for ${friendlyLabel}.`);
            return;
          }
        }

        // Exhausted all candidates
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
        const isAbortError = err instanceof Error && (err.name === 'AbortError' || err.message?.includes('aborted'));
        if (isAbortError) {
          console.log('[usePlayback] Playback resolution was aborted');
          setSelectionInfo(null);
          setSelectionError(null);
          return;
        }

        if (isTimeoutError(err)) {
          console.error(`[usePlayback] Search timed out for ${friendlyLabel}:`, err);
          setSelectionError(getTimeoutMessage(err));
          setSelectionInfo(null);
          return;
        }

        const message = err instanceof Error ? err.message : `Failed to resolve search result for ${friendlyLabel}.`;
        console.error(`[usePlayback] Search result resolve failed for ${friendlyLabel}:`, err);
        setSelectionError(message);
        setSelectionInfo(null);
      } finally {
        if (abortControllerRef.current === abortController || !abortController.signal.aborted) {
          setIsResolving(false);
        }
        if (abortControllerRef.current === abortController) {
          abortControllerRef.current = null;
        }
      }
    },
    [fetchIndexerResults, isResolving, title, titleId, imdbId, launchFromPrequeue, doesPrequeueMatch, pollPrequeueUntilReady, prequeueId, prequeueTargetEpisode, isSeries],
  );

  // =========================================================================
  // Keep resolveAndPlayRef in sync so useEpisodeManager can call it
  // =========================================================================
  useEffect(() => {
    resolveAndPlayRef.current = resolveAndPlay;
  }, [resolveAndPlay]);

  // =========================================================================
  // getItemIdForProgress (internal helper)
  // =========================================================================
  const getItemIdForProgress = useCallback((): string | null => {
    const episodeToCheck = activeEpisode || nextUpEpisode;

    if (episodeToCheck && seriesIdentifier) {
      return `${seriesIdentifier}:S${String(episodeToCheck.seasonNumber).padStart(2, '0')}E${String(episodeToCheck.episodeNumber).padStart(2, '0')}`;
    }

    if (!isSeries && titleId) {
      return titleId;
    }

    return null;
  }, [nextUpEpisode, activeEpisode, seriesIdentifier, isSeries, titleId]);

  // =========================================================================
  // checkAndShowResumeModal
  // =========================================================================
  const checkAndShowResumeModal = useCallback(
    async (action: () => Promise<void>) => {
      console.log('[usePlayback] checkAndShowResumeModal called', { activeUserId });

      if (!activeUserId) {
        console.log('[usePlayback] No active user, playing immediately');
        await showLoadingScreenIfEnabled();
        await action();
        return;
      }

      const itemId = getItemIdForProgress();
      console.log('[usePlayback] Item ID for progress:', itemId);

      if (!itemId) {
        console.log('[usePlayback] No item ID, playing immediately');
        await showLoadingScreenIfEnabled();
        await action();
        return;
      }

      try {
        const progressMediaType = isSeries || activeEpisode || nextUpEpisode ? 'episode' : 'movie';
        console.log('[usePlayback] Fetching progress for:', { mediaType: progressMediaType, itemId });
        const progress = await apiService.getPlaybackProgress(activeUserId, progressMediaType, itemId);
        console.log('[usePlayback] Progress result:', progress);

        if (progress && progress.percentWatched > 5 && progress.percentWatched < 95) {
          console.log('[usePlayback] Showing resume modal with progress:', progress.percentWatched);
          setCurrentProgress(progress);
          setPendingPlaybackAction(() => async (startOffset?: number) => {
            if (startOffset !== undefined) {
              pendingStartOffsetRef.current = startOffset;
            }
            await action();
          });
          setResumeModalVisible(true);
        } else {
          console.log('[usePlayback] No meaningful progress, playing immediately. Progress:', progress?.percentWatched);
          await showLoadingScreenIfEnabled();
          await action();
        }
      } catch (error) {
        console.warn('Failed to check playback progress:', error);
        await showLoadingScreenIfEnabled();
        await action();
      }
    },
    [activeUserId, getItemIdForProgress, isSeries, activeEpisode, nextUpEpisode, showLoadingScreenIfEnabled],
  );

  // =========================================================================
  // handleResumePlayback
  // =========================================================================
  const handleResumePlayback = useCallback(async () => {
    if (!pendingPlaybackAction || !currentProgress) {
      return;
    }

    setResumeModalVisible(false);
    await showLoadingScreenIfEnabled();

    // Apply rewind-on-playback-start setting
    const rewindAmount =
      userSettings?.playback?.rewindOnPlaybackStart ?? settings?.playback?.rewindOnPlaybackStart ?? 0;
    const resumePosition = Math.max(0, currentProgress.position - rewindAmount);
    await pendingPlaybackAction(resumePosition);

    setPendingPlaybackAction(null);
    setCurrentProgress(null);
  }, [pendingPlaybackAction, currentProgress, showLoadingScreenIfEnabled, userSettings, settings]);

  // =========================================================================
  // handlePlayFromBeginning
  // =========================================================================
  const handlePlayFromBeginning = useCallback(async () => {
    if (!pendingPlaybackAction) {
      return;
    }

    setResumeModalVisible(false);
    await showLoadingScreenIfEnabled();
    setCurrentProgress(null);
    await pendingPlaybackAction();

    setPendingPlaybackAction(null);
  }, [pendingPlaybackAction, showLoadingScreenIfEnabled]);

  // =========================================================================
  // handleCloseResumeModal
  // =========================================================================
  const handleCloseResumeModal = useCallback(() => {
    setResumeModalVisible(false);
    setCurrentProgress(null);
    setPendingPlaybackAction(null);
  }, []);

  // =========================================================================
  // handleWatchNow
  // =========================================================================
  const handleWatchNow = useCallback(async () => {
    if (isSelectBlocked) {
      console.log(`[usePlayback ${instanceId}] handleWatchNow BLOCKED (fromSimilar navigation debounce)`);
      return;
    }
    dismissTrailerAutoPlay();
    console.log(`[usePlayback ${instanceId}] handleWatchNow called - titleId: ${titleId}, title: ${title}`);
    const playAction = async () => {
      console.log(`[usePlayback ${instanceId}] playAction executing - titleId: ${titleId}`);
      const episodeToPlay = activeEpisode || nextUpEpisode;

      if (episodeToPlay) {
        const context = getEpisodeSearchContext(episodeToPlay);
        if (!context) {
          setSelectionError('Unable to build an episode search query.');
          return;
        }

        console.log('[usePlayback] Auto-select: resolving first viable result for episode', context.episodeCode);
        await resolveAndPlay({
          query: context.query,
          friendlyLabel: context.friendlyLabel,
          limit: 50,
          selectionMessage: context.selectionMessage,
          targetEpisode: {
            seasonNumber: episodeToPlay.seasonNumber,
            episodeNumber: episodeToPlay.episodeNumber,
            airedDate: episodeToPlay.airedDate,
          },
        });
        return;
      }

      const baseTitle = title.trim();
      console.log('[usePlayback] Auto-select: resolving first viable result', baseTitle ? `for ${baseTitle}` : '');
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
    resolveAndPlay,
    title,
    checkAndShowResumeModal,
    instanceId,
    titleId,
    isSelectBlocked,
    dismissTrailerAutoPlay,
  ]);

  // =========================================================================
  // handleLaunchDebugPlayer
  // =========================================================================
  const handleLaunchDebugPlayer = useCallback(async () => {
    dismissTrailerAutoPlay();
    const playAction = async () => {
      const episodeToPlay = activeEpisode || nextUpEpisode;

      if (episodeToPlay) {
        const context = getEpisodeSearchContext(episodeToPlay);
        if (!context) {
          setSelectionError('Unable to build an episode search query.');
          return;
        }

        console.log('[usePlayback] Debug Player: resolving first viable result for episode', context.episodeCode);
        await resolveAndPlay({
          query: context.query,
          friendlyLabel: context.friendlyLabel,
          limit: 50,
          selectionMessage: context.selectionMessage,
          useDebugPlayer: true,
          targetEpisode: {
            seasonNumber: episodeToPlay.seasonNumber,
            episodeNumber: episodeToPlay.episodeNumber,
            airedDate: episodeToPlay.airedDate,
          },
        });
        return;
      }

      const baseTitle = title.trim();
      console.log('[usePlayback] Debug Player: resolving first viable result', baseTitle ? `for ${baseTitle}` : '');
      await resolveAndPlay({
        query: baseTitle || title,
        friendlyLabel: baseTitle ? `"${baseTitle}"` : 'this title',
        limit: 50,
        selectionMessage: null,
        useDebugPlayer: true,
      });
    };

    await checkAndShowResumeModal(playAction);
  }, [dismissTrailerAutoPlay, activeEpisode, nextUpEpisode, getEpisodeSearchContext, resolveAndPlay, title, checkAndShowResumeModal]);

  // =========================================================================
  // Return
  // =========================================================================
  return {
    // Resolving state
    isResolving,
    setIsResolving,
    selectionError,
    setSelectionError,
    selectionInfo,
    setSelectionInfo,
    showBlackOverlay,
    setShowBlackOverlay,

    // Prequeue
    prequeueId,
    prequeueReady,
    prequeueDisplayInfo,
    prequeueTargetEpisode,
    prequeuePulseStyle,

    // Track selection
    trackOverrideAudio,
    setTrackOverrideAudio,
    trackOverrideSubtitle,
    setTrackOverrideSubtitle,
    showAudioTrackModal,
    setShowAudioTrackModal,
    showSubtitleTrackModal,
    setShowSubtitleTrackModal,
    buildPrequeueAudioOptions,
    buildPrequeueSubtitleOptions,
    currentAudioTrackId,
    currentSubtitleTrackId,

    // Resume modal
    resumeModalVisible,
    setResumeModalVisible,
    currentProgress,
    setCurrentProgress,
    pendingPlaybackAction,
    setPendingPlaybackAction,
    handleResumePlayback,
    handlePlayFromBeginning,
    handleCloseResumeModal,

    // Progress
    displayProgress,
    episodeProgressMap,

    // Actions
    resolveAndPlay,
    handleWatchNow,
    handleLaunchDebugPlayer,
    handleInitiatePlayback,
    fetchIndexerResults,
    getEpisodeSearchContext,
    checkAndShowResumeModal,
    showLoadingScreenIfEnabled,

    // Refs for cross-hook communication
    abortControllerRef,
    pendingStartOffsetRef,
    pendingShuffleModeRef,
    resolveAndPlayRef,
    navigationPrequeueIdRef,
    navigationPrequeueStatusRef,
  };
}
