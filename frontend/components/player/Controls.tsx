import FocusablePressable from '@/components/FocusablePressable';
import TVButton from '@/components/player/TVButton';
import SeekBar from '@/components/player/SeekBar';
import { SEGMENT_LABELS } from '@/components/player/SkipSegmentButton';
import VolumeControl from '@/components/player/VolumeControl';
import { TrackSelectionModal } from '@/components/player/TrackSelectionModal';
import { StreamInfoModal, type StreamInfoData } from '@/components/player/StreamInfoModal';
import type { ActiveSegment, SegmentType } from '@/hooks/useIntroSkip';
import { DefaultFocus, SpatialNavigationNode } from '@/services/tv-navigation';
import type { SpatialNavigationNodeRef } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import React, { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Animated, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { isTablet, TV_REFERENCE_HEIGHT } from '@/theme/tokens/tvScale';
import type { EPGProgram } from '@/services/api';

interface ControlsProps {
  paused: boolean;
  onPlayPause: () => void;
  currentTime: number;
  duration: number;
  onSeek: (time: number) => void;
  volume: number;
  onVolumeChange: (value: number) => void;
  isFullscreen?: boolean;
  onToggleFullscreen?: () => void;
  audioTracks?: TrackOption[];
  selectedAudioTrackId?: string | null;
  onSelectAudioTrack?: (id: string) => void;
  subtitleTracks?: TrackOption[];
  selectedSubtitleTrackId?: string | null;
  onSelectSubtitleTrack?: (id: string) => void;
  /** Callback to open subtitle search modal */
  onSearchSubtitles?: () => void;
  onModalStateChange?: (isOpen: boolean) => void;
  onScrubStart?: () => void;
  onScrubEnd?: () => void;
  isLiveTV?: boolean;
  hasStartedPlaying?: boolean;
  onSkipBackward?: () => void;
  onSkipForward?: () => void;
  onFocusChange?: (focusKey: string) => void;
  seekIndicatorAmount?: number;
  seekIndicatorStartTime?: number;
  /** When true, greys out control buttons (during TV D-pad seeking) */
  isSeeking?: boolean;
  /** Stream info for TV info modal */
  streamInfo?: StreamInfoData;
  /** Episode navigation */
  hasPreviousEpisode?: boolean;
  hasNextEpisode?: boolean;
  onPreviousEpisode?: () => void;
  onNextEpisode?: () => void;
  /** Green indicator when next episode is prequeued and ready */
  nextEpisodePrequeueReady?: boolean;
  /** Shuffle mode - disables prev, enables random next */
  shuffleMode?: boolean;
  /** Subtitle offset adjustment (for external/searched subtitles) */
  showSubtitleOffset?: boolean;
  subtitleOffset?: number;
  onSubtitleOffsetEarlier?: () => void;
  onSubtitleOffsetLater?: () => void;
  /** Seek amounts for skip buttons */
  seekBackwardSeconds?: number;
  seekForwardSeconds?: number;
  /** Picture-in-Picture (iOS only) */
  onEnterPip?: () => void;
  /** Flash the skip button on double-tap (mobile only) */
  flashSkipButton?: 'backward' | 'forward' | null;
  /** EPG current program for live TV */
  currentProgram?: EPGProgram;
  /** EPG next program for live TV */
  nextProgram?: EPGProgram;
  /** Ref that parent can call to close the active child modal (used by TVControlsModal onRequestClose) */
  closeModalRef?: React.MutableRefObject<(() => void) | null>;
  activeMenu?: ActiveMenu;
  onActiveMenuChange?: (menu: ActiveMenu) => void;
  /** Current playback speed (1.0 = normal) */
  playbackSpeed?: number;
  /** Callback when user selects a new playback speed */
  onPlaybackSpeedChange?: (speed: number) => void;
  /** Active skip segment (intro/recap/outro) for TV controls */
  skipSegment?: ActiveSegment | null;
  /** Callback when skip segment button is pressed */
  onSkipSegment?: () => void;
  /** When true, hides all controls except the skip segment button (TV skip-only overlay) */
  skipOnlyMode?: boolean;
  /** When true, focus the skip segment button after mount (overrides DefaultFocus on play/pause).
   *  Used during skip-only → full controls transition so focus stays on the skip button. */
  skipButtonRetainFocus?: boolean;
  /** Exit button rendered inside spatial nav tree on TV (so DefaultFocus takes priority) */
  onExit?: () => void;
}

export type TrackOption = {
  id: string;
  label: string;
  description?: string;
};

export type ActiveMenu = 'audio' | 'subtitles' | 'info' | 'speed' | null;

const Controls: React.FC<ControlsProps> = ({
  paused,
  onPlayPause,
  currentTime,
  duration,
  onSeek,
  volume,
  onVolumeChange,
  isFullscreen = false,
  onToggleFullscreen,
  audioTracks = [],
  selectedAudioTrackId,
  onSelectAudioTrack,
  subtitleTracks = [],
  selectedSubtitleTrackId,
  onSelectSubtitleTrack,
  onSearchSubtitles,
  onModalStateChange,
  onScrubStart,
  onScrubEnd,
  isLiveTV = false,
  hasStartedPlaying = false,
  onSkipBackward,
  onSkipForward,
  onFocusChange,
  seekIndicatorAmount = 0,
  seekIndicatorStartTime = 0,
  isSeeking = false,
  streamInfo,
  hasPreviousEpisode = false,
  hasNextEpisode = false,
  onPreviousEpisode,
  onNextEpisode,
  nextEpisodePrequeueReady = false,
  shuffleMode = false,
  showSubtitleOffset = false,
  subtitleOffset = 0,
  onSubtitleOffsetEarlier,
  onSubtitleOffsetLater,
  seekBackwardSeconds = 10,
  seekForwardSeconds = 30,
  onEnterPip,
  flashSkipButton,
  currentProgram,
  nextProgram,
  closeModalRef,
  activeMenu = null,
  onActiveMenuChange,
  playbackSpeed = 1.0,
  onPlaybackSpeedChange,
  skipSegment,
  onSkipSegment,
  skipOnlyMode = false,
  skipButtonRetainFocus = false,
  onExit,
}) => {
  const theme = useTheme();
  const { width, height } = useTVDimensions();
  const styles = useMemo(() => useControlsStyles(theme, width, height), [theme, width, height]);
  const showVolume = Platform.OS === 'web';
  const isTvPlatform = Platform.isTV;
  const isMobile = Platform.OS !== 'web' && !isTvPlatform;
  const allowTrackSelection = true; // Allow track selection on all platforms including tvOS
  const isLandscape = width >= height;
  const _isSeekable = Number.isFinite(duration) && duration > 0;
  const lastFocusedKeyRef = useRef<string | null>(null);
  // Refs for focus restoration after modal close
  // On TV: SpatialNavigationNodeRef (.focus()), on web/mobile: View (.requestTVFocus())
  const audioButtonRef = useRef<SpatialNavigationNodeRef | View>(null);
  const subtitleButtonRef = useRef<SpatialNavigationNodeRef | View>(null);
  const infoButtonRef = useRef<SpatialNavigationNodeRef | View>(null);
  const speedButtonRef = useRef<SpatialNavigationNodeRef | View>(null);
  const skipSegmentButtonRef = useRef<SpatialNavigationNodeRef>(null);
  const lastFocusedRef = useRef<SpatialNavigationNodeRef | View | null>(null);

  // Flash animation for skip buttons (triggered by double-tap on mobile)
  const skipBackwardScale = useRef(new Animated.Value(1)).current;
  const skipForwardScale = useRef(new Animated.Value(1)).current;

  useEffect(() => {
    if (!flashSkipButton) return;

    const scaleValue = flashSkipButton === 'backward' ? skipBackwardScale : skipForwardScale;

    // Quick scale up and down animation
    Animated.sequence([
      Animated.timing(scaleValue, {
        toValue: 1.3,
        duration: 100,
        useNativeDriver: true,
      }),
      Animated.timing(scaleValue, {
        toValue: 1,
        duration: 200,
        useNativeDriver: true,
      }),
    ]).start();
  }, [flashSkipButton, skipBackwardScale, skipForwardScale]);

  const audioSummary = useMemo(() => {
    if (!audioTracks.length) {
      return undefined;
    }
    const fallback = audioTracks[0]?.label;
    if (!selectedAudioTrackId) {
      return fallback;
    }
    return audioTracks.find((track) => track.id === selectedAudioTrackId)?.label ?? fallback;
  }, [audioTracks, selectedAudioTrackId]);

  const subtitleSummary = useMemo(() => {
    // Show "External" when using external/searched subtitles
    if (selectedSubtitleTrackId === 'external') {
      return 'External';
    }
    // For live TV, only show subtitle selection if there are embedded tracks (no external search)
    // For other content, show "Search" if no embedded tracks
    if (!subtitleTracks.length || !subtitleTracks.some((track) => Number.isFinite(Number(track.id)))) {
      return isLiveTV ? undefined : 'Search';
    }
    const fallback = subtitleTracks[0]?.label;
    if (!selectedSubtitleTrackId) {
      return fallback;
    }
    return subtitleTracks.find((track) => track.id === selectedSubtitleTrackId)?.label ?? fallback;
  }, [selectedSubtitleTrackId, subtitleTracks, isLiveTV]);

  // Format subtitle offset for display (e.g., "-0.25s", "+0.50s", "0s")
  const formattedSubtitleOffset = useMemo(() => {
    if (subtitleOffset === 0) return '0s';
    const sign = subtitleOffset > 0 ? '+' : '';
    return `${sign}${subtitleOffset.toFixed(2)}s`;
  }, [subtitleOffset]);

  const speedSummary = useMemo(() => `${playbackSpeed}x`, [playbackSpeed]);
  const hasSpeedSelection = Boolean(onPlaybackSpeedChange);

  // Hide all track selection for live TV
  const hasAudioSelection = allowTrackSelection && Boolean(onSelectAudioTrack) && audioTracks.length > 0 && !isLiveTV;
  const hasSubtitleSelection = allowTrackSelection && Boolean(onSelectSubtitleTrack) && !isLiveTV;
  const showFullscreenButton = Boolean(onToggleFullscreen) && !isMobile && !isLiveTV && !isTvPlatform;
  // PiP button: show on iOS and Android mobile (not TV, not live TV)
  const showPipButton =
    Boolean(onEnterPip) && isMobile && (Platform.OS === 'ios' || Platform.OS === 'android') && !isLiveTV;

  // Compute a key for the secondary row that changes when buttons change,
  // forcing the spatial navigation tree to be regenerated
  const secondaryRowKey = useMemo(() => {
    const parts: string[] = [];
    if (hasAudioSelection) parts.push('audio');
    if (hasSubtitleSelection) parts.push('sub');
    if (hasSpeedSelection) parts.push('speed');
    if (isTvPlatform && onPreviousEpisode) parts.push('prev');
    if (isTvPlatform && onNextEpisode) parts.push('next');
    if (isTvPlatform && showSubtitleOffset) parts.push('offset');
    if (isTvPlatform && streamInfo) parts.push('info');
    if (isTvPlatform && skipSegment) parts.push('skip');
    // Include skipOnlyMode so the spatial nav tree remounts when transitioning
    // from skip-only (only skip button registered) to full controls.
    // Without this, the skip button stays at index 0 (leftmost) because it was
    // registered first, and newly-appearing buttons get appended after it.
    if (skipOnlyMode) parts.push('only');
    const key = `secondary-${parts.join('-')}`;
    if (isTvPlatform) console.log('[Controls][KEY] secondaryRowKey computed:', key, { skipOnlyMode });
    return key;
  }, [
    hasAudioSelection,
    hasSubtitleSelection,
    hasSpeedSelection,
    isTvPlatform,
    onPreviousEpisode,
    onNextEpisode,
    showSubtitleOffset,
    streamInfo,
    skipSegment,
    skipOnlyMode,
  ]);

  const activeMenuRef = useRef<ActiveMenu>(null);
  // Guard to prevent modal from immediately reopening when focus returns to the button on tvOS
  const menuClosingGuardRef = useRef(false);

  // Map focusKey → ref for focus restoration
  const focusKeyToRef: Record<string, React.RefObject<SpatialNavigationNodeRef | View | null>> = useMemo(() => ({
    'audio-track-button': audioButtonRef,
    'subtitle-track-button': subtitleButtonRef,
    'subtitle-track-button-secondary': subtitleButtonRef,
    'info-button': infoButtonRef,
    'speed-button': speedButtonRef,
  }), []);

  const openMenu = useCallback(
    (menu: Exclude<ActiveMenu, null>, focusKey?: string) => {
      // On TV platforms, check if we just closed a menu (prevents focus-return re-triggering)
      if (Platform.isTV && menuClosingGuardRef.current) {
        console.log('[Controls] openMenu blocked by closing guard', { menu });
        return;
      }
      console.log('[Controls] openMenu called', { menu, currentActiveMenu: activeMenu, focusKey });

      if (focusKey) {
        lastFocusedKeyRef.current = focusKey;
        lastFocusedRef.current = focusKeyToRef[focusKey]?.current ?? null;
      }

      onActiveMenuChange?.(menu);
      onModalStateChange?.(true);
    },
    [onModalStateChange, activeMenu, onActiveMenuChange, focusKeyToRef],
  );

  const closeMenu = useCallback(() => {
    console.log('[Controls] closeMenu called', { currentActiveMenu: activeMenu, lastFocus: lastFocusedKeyRef.current });
    // Set guard to prevent immediate re-opening on TV platforms
    if (Platform.isTV) {
      menuClosingGuardRef.current = true;
      setTimeout(() => {
        menuClosingGuardRef.current = false;
      }, 400);
    }
    onActiveMenuChange?.(null);
    onModalStateChange?.(false);

    // Restore focus to the button that opened the menu
    // On TV: use spatial nav .focus(), on web: use requestTVFocus()
    if (Platform.isTV && lastFocusedRef.current) {
      const target = lastFocusedRef.current;
      lastFocusedRef.current = null;
      lastFocusedKeyRef.current = null;
      requestAnimationFrame(() => {
        setTimeout(() => {
          if ('focus' in target && typeof target.focus === 'function') {
            target.focus();
          } else {
            (target as any).requestTVFocus?.();
          }
        }, 50);
      });
    }
  }, [onModalStateChange, activeMenu, onActiveMenuChange]);

  // Expose closeMenu to parent via ref (used by TVControlsModal's onRequestClose)
  useEffect(() => {
    if (closeModalRef) {
      closeModalRef.current = activeMenu !== null ? closeMenu : null;
    }
    return () => {
      if (closeModalRef) {
        closeModalRef.current = null;
      }
    };
  }, [activeMenu, closeMenu, closeModalRef]);

  // Wrapped callback for transitioning to subtitle search modal.
  // On tvOS, we need careful timing to transition focus between modals.
  const handleOpenSubtitleSearch = useCallback(() => {
    // Close this modal first
    onActiveMenuChange?.(null);
    // Open the SubtitleSearchModal immediately (no delay needed since both
    // state updates will be batched by React and rendered together)
    onSearchSubtitles?.();
  }, [onSearchSubtitles, onActiveMenuChange]);

  useEffect(
    () => () => {
      if (activeMenu !== null) {
        onModalStateChange?.(false);
      }
    },
    [onModalStateChange, activeMenu],
  );

  const handleSelectTrack = useCallback(
    (id: string) => {
      console.log('[Controls] handleSelectTrack called', { id, activeMenu });
      if (activeMenu === 'audio' && onSelectAudioTrack) {
        onSelectAudioTrack(id);
      } else if (activeMenu === 'subtitles' && onSelectSubtitleTrack) {
        onSelectSubtitleTrack(id);
      }
      closeMenu();
    },
    [activeMenu, closeMenu, onSelectAudioTrack, onSelectSubtitleTrack],
  );

  const activeOptions = useMemo(() => {
    if (activeMenu === 'audio') {
      return audioTracks;
    }
    if (activeMenu === 'subtitles') {
      return subtitleTracks;
    }
    return [] as TrackOption[];
  }, [activeMenu, audioTracks, subtitleTracks]);

  const selectedTrackId = activeMenu === 'audio' ? selectedAudioTrackId : selectedSubtitleTrackId;
  const trackModalSubtitle = useMemo(() => {
    if (activeMenu === 'audio') {
      return audioSummary ? `Current track: ${audioSummary}` : 'Select an audio track';
    }
    if (activeMenu === 'subtitles') {
      return subtitleSummary ? `Current subtitles: ${subtitleSummary}` : 'Select a subtitle track';
    }
    return undefined;
  }, [activeMenu, audioSummary, subtitleSummary]);

  // Memoize focus handlers to prevent re-renders of FocusablePressable on every Controls render
  // This is critical for Android TV performance where re-creating these functions causes sluggish navigation
  const handlePlayPauseFocus = useCallback(() => {
    if (Platform.isTV) console.log('[Controls][FOCUS] play-pause-button focused', { skipOnlyMode, skipButtonRetainFocus });
    onFocusChange?.('play-pause-button');
  }, [onFocusChange, skipOnlyMode, skipButtonRetainFocus]);
  const handleSkipBackFocus = useCallback(() => {
    if (Platform.isTV) console.log('[Controls][FOCUS] skip-back-button focused');
    onFocusChange?.('skip-back-button');
  }, [onFocusChange]);
  const handleSkipForwardFocus = useCallback(() => {
    if (Platform.isTV) console.log('[Controls][FOCUS] skip-forward-button focused');
    onFocusChange?.('skip-forward-button');
  }, [onFocusChange]);
  const handleFullscreenFocus = useCallback(() => onFocusChange?.('fullscreen-button'), [onFocusChange]);
  const handleAudioTrackFocus = useCallback(() => {
    if (Platform.isTV) console.log('[Controls][FOCUS] audio-track-button focused', { skipOnlyMode, skipButtonRetainFocus });
    onFocusChange?.('audio-track-button');
  }, [onFocusChange, skipOnlyMode, skipButtonRetainFocus]);
  const handleSubtitleTrackFocus = useCallback(() => {
    if (Platform.isTV) console.log('[Controls][FOCUS] subtitle-track-button focused', { skipOnlyMode, skipButtonRetainFocus });
    onFocusChange?.('subtitle-track-button');
  }, [onFocusChange, skipOnlyMode, skipButtonRetainFocus]);
  const handleSubtitleTrackSecondaryFocus = useCallback(
    () => onFocusChange?.('subtitle-track-button-secondary'),
    [onFocusChange],
  );
  const handlePreviousEpisodeFocus = useCallback(() => onFocusChange?.('previous-episode-button'), [onFocusChange]);
  const handleNextEpisodeFocus = useCallback(() => onFocusChange?.('next-episode-button'), [onFocusChange]);
  const handleSubtitleOffsetEarlierFocus = useCallback(
    () => onFocusChange?.('subtitle-offset-earlier'),
    [onFocusChange],
  );
  const handleSubtitleOffsetLaterFocus = useCallback(() => onFocusChange?.('subtitle-offset-later'), [onFocusChange]);
  const handleInfoFocus = useCallback(() => {
    if (Platform.isTV) console.log('[Controls][FOCUS] info-button focused');
    onFocusChange?.('info-button');
  }, [onFocusChange]);
  const handleSpeedFocus = useCallback(() => {
    if (Platform.isTV) console.log('[Controls][FOCUS] speed-button focused');
    onFocusChange?.('speed-button');
  }, [onFocusChange]);
  const handleSkipSegmentFocus = useCallback(() => {
    if (Platform.isTV) console.log('[Controls][FOCUS] skip-segment-button focused', { skipOnlyMode, skipButtonRetainFocus });
    onFocusChange?.('skip-segment-button');
  }, [onFocusChange, skipOnlyMode, skipButtonRetainFocus]);
  const handleExitFocus = useCallback(() => {
    if (Platform.isTV) console.log('[Controls][FOCUS] exit-button focused', { skipOnlyMode, skipButtonRetainFocus });
    onFocusChange?.('exit-button');
  }, [onFocusChange, skipOnlyMode, skipButtonRetainFocus]);

  // Memoize menu openers to stabilize onSelect props
  const handleOpenAudioMenu = useCallback(() => openMenu('audio', 'audio-track-button'), [openMenu]);
  const handleOpenSubtitlesMenu = useCallback(
    () => openMenu('subtitles', hasAudioSelection ? 'subtitle-track-button-secondary' : 'subtitle-track-button'),
    [openMenu, hasAudioSelection],
  );
  const handleOpenInfoMenu = useCallback(() => openMenu('info', 'info-button'), [openMenu]);
  const handleOpenSpeedMenu = useCallback(() => openMenu('speed', 'speed-button'), [openMenu]);

  // Speed button content: icon with overlaid speed value
  const speedIconSize = isTvPlatform ? 28 : 24;
  const speedBadgeFontSize = isTvPlatform ? 9 : 7;
  const speedIcon = (
    <View style={styles.speedIconContainer}>
      <Ionicons name="speedometer-outline" size={speedIconSize} color={theme.colors.text.primary} />
      <View style={[styles.speedBadge, { minWidth: speedBadgeFontSize * 3 }]}>
        <Text style={[styles.speedBadgeText, { fontSize: speedBadgeFontSize }]}>{playbackSpeed !== 1 ? `${playbackSpeed}x` : '1x'}</Text>
      </View>
    </View>
  );

  // Debug: log render-time state for skip focus investigation
  if (isTvPlatform && skipSegment) {
    console.log('[Controls][RENDER]', {
      skipOnlyMode,
      skipButtonRetainFocus,
      hasAudioSelection,
      hasSubtitleSelection,
      isLiveTV,
      secondaryRowKey,
      hasDefaultFocusOnSkip: skipButtonRetainFocus || skipOnlyMode,
      hasDefaultFocusOnPlayPause: !skipButtonRetainFocus,
    });
  }

  return (
    <>
      {/* Mobile center controls */}
      {isMobile && !isLiveTV && (
        <View style={styles.centerControls} pointerEvents="box-none">
          {onPreviousEpisode && (
            <View style={styles.skipButtonContainer}>
              <Pressable
                onPress={hasPreviousEpisode && !shuffleMode ? onPreviousEpisode : undefined}
                style={[styles.episodeButton, (!hasPreviousEpisode || shuffleMode) && styles.episodeButtonDisabled]}
                disabled={!hasPreviousEpisode || shuffleMode}>
                <Ionicons
                  name="play-skip-back"
                  size={24}
                  color={hasPreviousEpisode && !shuffleMode ? theme.colors.text.primary : theme.colors.text.disabled}
                />
              </Pressable>
            </View>
          )}
          {onSkipBackward && (
            <Animated.View style={[styles.skipButtonContainer, { transform: [{ scale: skipBackwardScale }] }]}>
              <Pressable onPress={onSkipBackward} style={styles.skipButton}>
                <View style={styles.skipButtonContent}>
                  <Text style={styles.skipButtonText}>{seekBackwardSeconds}</Text>
                  <Ionicons name="play-back" size={20} color={theme.colors.text.primary} />
                </View>
              </Pressable>
            </Animated.View>
          )}
          <DefaultFocus>
            <Pressable onPress={onPlayPause} style={styles.centerPlayButton}>
              <Ionicons name={paused ? 'play' : 'pause'} size={40} color={theme.colors.text.primary} />
            </Pressable>
          </DefaultFocus>
          {onSkipForward && (
            <Animated.View style={[styles.skipButtonContainer, { transform: [{ scale: skipForwardScale }] }]}>
              <Pressable onPress={onSkipForward} style={styles.skipButton}>
                <View style={styles.skipButtonContent}>
                  <Text style={styles.skipButtonText}>{seekForwardSeconds}</Text>
                  <Ionicons name="play-forward" size={20} color={theme.colors.text.primary} />
                </View>
              </Pressable>
            </Animated.View>
          )}
          {onNextEpisode && (
            <View style={styles.skipButtonContainer}>
              <Pressable
                onPress={hasNextEpisode || shuffleMode ? onNextEpisode : undefined}
                style={[styles.episodeButton, !hasNextEpisode && !shuffleMode && styles.episodeButtonDisabled]}
                disabled={!hasNextEpisode && !shuffleMode}>
                <Ionicons
                  name={shuffleMode ? 'shuffle' : 'play-skip-forward'}
                  size={24}
                  color={hasNextEpisode || shuffleMode ? theme.colors.text.primary : theme.colors.text.disabled}
                />
                {nextEpisodePrequeueReady && <View style={styles.prequeueReadyIndicator} />}
              </Pressable>
            </View>
          )}
        </View>
      )}
      {/* Mobile subtitle offset controls - phones: above play button in landscape, tablets: below play button */}
      {isMobile && showSubtitleOffset && !isLiveTV && (
        <View
          style={[
            styles.subtitleOffsetContainer,
            isLandscape && !isTablet && styles.subtitleOffsetContainerLandscape,
            isLandscape && isTablet && styles.subtitleOffsetContainerTabletLandscape,
          ]}
          pointerEvents="box-none">
          <View style={styles.subtitleOffsetRow}>
            <Pressable onPress={onSubtitleOffsetEarlier} style={styles.subtitleOffsetButton}>
              <Ionicons name="remove" size={18} color={theme.colors.text.primary} />
            </Pressable>
            <View style={styles.subtitleOffsetLabelContainer}>
              <Text style={styles.subtitleOffsetLabel}>Subtitle</Text>
              <Text style={styles.subtitleOffsetValue}>{formattedSubtitleOffset}</Text>
            </View>
            <Pressable onPress={onSubtitleOffsetLater} style={styles.subtitleOffsetButton}>
              <Ionicons name="add" size={18} color={theme.colors.text.primary} />
            </Pressable>
          </View>
        </View>
      )}
      <SpatialNavigationNode key={secondaryRowKey} orientation="vertical">
        {/* Exit button BEFORE bottomControls in spatial nav vertical tree so pressing Up
            from main row reaches it. Visually at top-left via absolute positioning. */}
        {isTvPlatform && onExit && !skipOnlyMode && (
          <TVButton
            icon="arrow-back"
            text="Exit"
            onSelect={onExit}
            onFocus={handleExitFocus}
            disabled={activeMenu !== null}
            style={styles.exitButton}
            textStyle={styles.exitButtonText}
          />
        )}
        <View
          style={[
            styles.bottomControls,
            isMobile && styles.bottomControlsMobile,
            isMobile && isLandscape && styles.bottomControlsMobileLandscape,
            skipOnlyMode && styles.bottomControlsSkipOnly,
          ]}
          pointerEvents={activeMenu !== null ? 'none' : 'auto'}
          renderToHardwareTextureAndroid={isTvPlatform}>
          {!isLiveTV && !skipOnlyMode && (
            <SpatialNavigationNode orientation="horizontal">
              <View style={styles.mainRow} pointerEvents="box-none">
                {!isMobile && (
                  <View style={[styles.buttonGroup, isSeeking && styles.seekingDisabled]}>
                    {skipButtonRetainFocus ? (
                      isTvPlatform ? (
                        <TVButton
                          icon={paused ? 'play' : 'pause'}
                          onSelect={onPlayPause}
                          onFocus={handlePlayPauseFocus}
                          style={styles.controlButton}
                          disabled={isSeeking || activeMenu !== null}
                        />
                      ) : (
                        <FocusablePressable
                          icon={paused ? 'play' : 'pause'}
                          focusKey="play-pause-button"
                          onSelect={onPlayPause}
                          onFocus={handlePlayPauseFocus}
                          style={styles.controlButton}
                          disabled={isSeeking || activeMenu !== null}
                        />
                      )
                    ) : (
                      <DefaultFocus>
                        {isTvPlatform ? (
                          <TVButton
                            icon={paused ? 'play' : 'pause'}
                            onSelect={onPlayPause}
                            onFocus={handlePlayPauseFocus}
                            style={styles.controlButton}
                            disabled={isSeeking || activeMenu !== null}
                          />
                        ) : (
                          <FocusablePressable
                            icon={paused ? 'play' : 'pause'}
                            focusKey="play-pause-button"
                            onSelect={onPlayPause}
                            onFocus={handlePlayPauseFocus}
                            style={styles.controlButton}
                            disabled={isSeeking || activeMenu !== null}
                          />
                        )}
                      </DefaultFocus>
                    )}
                    {onSkipBackward && (
                      <View style={styles.tvSkipButtonContainer}>
                        {isTvPlatform ? (
                          <TVButton
                            icon="play-back"
                            onSelect={onSkipBackward}
                            onFocus={handleSkipBackFocus}
                            style={styles.controlButton}
                            disabled={isSeeking || activeMenu !== null}
                          />
                        ) : (
                          <FocusablePressable
                            icon="play-back"
                            focusKey="skip-back-button"
                            onSelect={onSkipBackward}
                            onFocus={handleSkipBackFocus}
                            style={styles.controlButton}
                            disabled={isSeeking || activeMenu !== null}
                          />
                        )}
                        <Text style={styles.tvSkipLabel}>{seekBackwardSeconds}s</Text>
                      </View>
                    )}
                    {onSkipForward && (
                      <View style={styles.tvSkipButtonContainer}>
                        {isTvPlatform ? (
                          <TVButton
                            icon="play-forward"
                            onSelect={onSkipForward}
                            onFocus={handleSkipForwardFocus}
                            style={styles.controlButton}
                            disabled={isSeeking || activeMenu !== null}
                          />
                        ) : (
                          <FocusablePressable
                            icon="play-forward"
                            focusKey="skip-forward-button"
                            onSelect={onSkipForward}
                            onFocus={handleSkipForwardFocus}
                            style={styles.controlButton}
                            disabled={isSeeking || activeMenu !== null}
                          />
                        )}
                        <Text style={styles.tvSkipLabel}>{seekForwardSeconds}s</Text>
                      </View>
                    )}
                  </View>
                )}
                <View style={[styles.seekContainer, isMobile && styles.seekContainerMobile]} pointerEvents="box-none">
                  <SeekBar
                    currentTime={currentTime}
                    duration={duration}
                    onSeek={onSeek}
                    onScrubStart={onScrubStart}
                    onScrubEnd={onScrubEnd}
                    seekIndicatorAmount={seekIndicatorAmount}
                    seekIndicatorStartTime={seekIndicatorStartTime}
                  />
                </View>
                <View style={[styles.buttonGroup, isSeeking && styles.seekingDisabled]}>
                  {showVolume && <VolumeControl value={volume} onChange={onVolumeChange} />}
                  {showFullscreenButton && onToggleFullscreen && (
                    <FocusablePressable
                      icon={isFullscreen ? 'contract' : 'expand'}
                      focusKey="fullscreen-button"
                      onSelect={onToggleFullscreen}
                      onFocus={handleFullscreenFocus}
                      style={styles.controlButton}
                      disabled={isSeeking || activeMenu !== null}
                    />
                  )}
                </View>
              </View>
            </SpatialNavigationNode>
          )}
          {!skipOnlyMode && isLiveTV && (
            <View style={styles.mainRow} pointerEvents="box-none">
              <View style={[styles.seekContainer, isMobile && styles.seekContainerMobile]} pointerEvents="box-none">
                {hasStartedPlaying && (
                  <View style={styles.liveContainer}>
                    <View style={styles.liveBadge}>
                      <Text style={styles.liveBadgeText}>LIVE</Text>
                    </View>
                    {currentProgram && (
                      <View style={styles.epgContainer}>
                        <Text style={styles.epgTitle} numberOfLines={1}>
                          {currentProgram.title}
                        </Text>
                        {nextProgram && (
                          <Text style={styles.epgNext} numberOfLines={1}>
                            Next: {nextProgram.title}
                          </Text>
                        )}
                      </View>
                    )}
                  </View>
                )}
              </View>
            </View>
          )}
          {/* Secondary row: control buttons below progress bar */}
          {(hasAudioSelection ||
            hasSubtitleSelection ||
            hasSpeedSelection ||
            (isTvPlatform && streamInfo) ||
            (isTvPlatform && (onPreviousEpisode || onNextEpisode)) ||
            (isTvPlatform && showSubtitleOffset) ||
            (isTvPlatform && skipSegment) ||
            showPipButton ||
            skipOnlyMode) && (
              <SpatialNavigationNode orientation="horizontal">
                <View style={[styles.secondaryRow, isSeeking && styles.seekingDisabled]} pointerEvents="box-none">
                  {/* Mobile PiP layout: portrait=stacked, landscape=side-by-side */}
                  {showPipButton ? (
                    isLandscape ? (
                      <View style={styles.mobilePipRow} pointerEvents="box-none">
                        {hasAudioSelection && audioSummary && (
                          <View style={styles.trackButtonGroup} pointerEvents="box-none">
                            <FocusablePressable
                              icon="musical-notes"
                              focusKey="audio-track-button"
                              onSelect={handleOpenAudioMenu}
                              onFocus={handleAudioTrackFocus}
                              style={[styles.controlButton, styles.trackButton]}
                              disabled={isSeeking || activeMenu !== null}
                            />
                            <Text style={styles.trackLabel} numberOfLines={1}>{audioSummary}</Text>
                          </View>
                        )}
                        {hasSubtitleSelection && subtitleSummary && (
                          <View style={styles.trackButtonGroup} pointerEvents="box-none">
                            <FocusablePressable
                              icon="chatbubble-ellipses"
                              focusKey={hasAudioSelection ? 'subtitle-track-button-secondary' : 'subtitle-track-button'}
                              onSelect={handleOpenSubtitlesMenu}
                              onFocus={hasAudioSelection ? handleSubtitleTrackSecondaryFocus : handleSubtitleTrackFocus}
                              style={[styles.controlButton, styles.trackButton]}
                              disabled={isSeeking || activeMenu !== null}
                            />
                            <Text style={styles.trackLabel} numberOfLines={1}>{subtitleSummary}</Text>
                          </View>
                        )}
                        <View style={styles.pipButtonSpacer} />
                        {hasSpeedSelection && (
                          <Pressable
                            onPress={handleOpenSpeedMenu}
                            style={[styles.controlButton, styles.trackButton, styles.pipButton]}>
                            {speedIcon}
                          </Pressable>
                        )}
                        <Pressable
                          onPress={onEnterPip}
                          style={[styles.controlButton, styles.trackButton, styles.pipButton]}>
                          <MaterialCommunityIcons
                            name="picture-in-picture-bottom-right"
                            size={24}
                            color={theme.colors.text.primary}
                          />
                        </Pressable>
                      </View>
                    ) : (
                      <View style={styles.mobilePipContainer} pointerEvents="box-none">
                        {hasAudioSelection && audioSummary && hasSubtitleSelection && subtitleSummary && (
                          <View style={styles.mobilePipRow} pointerEvents="box-none">
                            <View style={styles.trackButtonGroupFlex} pointerEvents="box-none">
                              <FocusablePressable
                                icon="musical-notes"
                                focusKey="audio-track-button"
                                onSelect={handleOpenAudioMenu}
                                onFocus={handleAudioTrackFocus}
                                style={[styles.controlButton, styles.trackButton]}
                                disabled={isSeeking || activeMenu !== null}
                              />
                              <Text style={styles.trackLabel}>{audioSummary}</Text>
                            </View>
                            {hasSpeedSelection && (
                              <Pressable
                                onPress={handleOpenSpeedMenu}
                                style={[styles.controlButton, styles.trackButton, styles.pipButton]}>
                                {speedIcon}
                              </Pressable>
                            )}
                          </View>
                        )}
                        <View style={styles.mobilePipRow} pointerEvents="box-none">
                          {hasSubtitleSelection && subtitleSummary ? (
                            <View style={styles.trackButtonGroupFlex} pointerEvents="box-none">
                              <FocusablePressable
                                icon="chatbubble-ellipses"
                                focusKey={hasAudioSelection ? 'subtitle-track-button-secondary' : 'subtitle-track-button'}
                                onSelect={handleOpenSubtitlesMenu}
                                onFocus={hasAudioSelection ? handleSubtitleTrackSecondaryFocus : handleSubtitleTrackFocus}
                                style={[styles.controlButton, styles.trackButton]}
                                disabled={isSeeking || activeMenu !== null}
                              />
                              <Text style={styles.trackLabel} numberOfLines={1}>{subtitleSummary}</Text>
                            </View>
                          ) : hasAudioSelection && audioSummary ? (
                            <View style={styles.trackButtonGroupFlex} pointerEvents="box-none">
                              <FocusablePressable
                                icon="musical-notes"
                                focusKey="audio-track-button"
                                onSelect={handleOpenAudioMenu}
                                onFocus={handleAudioTrackFocus}
                                style={[styles.controlButton, styles.trackButton]}
                                disabled={isSeeking || activeMenu !== null}
                              />
                              <Text style={styles.trackLabel} numberOfLines={1}>{audioSummary}</Text>
                            </View>
                          ) : null}
                          <Pressable
                            onPress={onEnterPip}
                            style={[styles.controlButton, styles.trackButton, styles.pipButton]}>
                            <MaterialCommunityIcons
                              name="picture-in-picture-bottom-right"
                              size={24}
                              color={theme.colors.text.primary}
                            />
                          </Pressable>
                        </View>
                      </View>
                    )
                  ) : (
                    <>
                      {!skipOnlyMode && hasAudioSelection && audioSummary && (
                        <View style={styles.trackButtonGroup} pointerEvents="box-none">
                          {isLiveTV ? (
                            <DefaultFocus>
                              {isTvPlatform ? (
                                <TVButton
                                  ref={audioButtonRef as React.Ref<SpatialNavigationNodeRef>}
                                  icon="musical-notes"
                                  onSelect={handleOpenAudioMenu}
                                  onFocus={handleAudioTrackFocus}
                                  style={[styles.controlButton, styles.trackButton]}
                                  disabled={isSeeking || activeMenu !== null}
                                />
                              ) : (
                                <FocusablePressable
                                  ref={audioButtonRef as React.Ref<View>}
                                  icon="musical-notes"
                                  focusKey="audio-track-button"
                                  onSelect={handleOpenAudioMenu}
                                  onFocus={handleAudioTrackFocus}
                                  style={[styles.controlButton, styles.trackButton]}
                                  disabled={isSeeking || activeMenu !== null}
                                />
                              )}
                            </DefaultFocus>
                          ) : isTvPlatform ? (
                            <TVButton
                              ref={audioButtonRef as React.Ref<SpatialNavigationNodeRef>}
                              icon="musical-notes"
                              onSelect={handleOpenAudioMenu}
                              onFocus={handleAudioTrackFocus}
                              style={[styles.controlButton, styles.trackButton]}
                              disabled={isSeeking || activeMenu !== null}
                            />
                          ) : (
                            <FocusablePressable
                              ref={audioButtonRef as React.Ref<View>}
                              icon="musical-notes"
                              focusKey="audio-track-button"
                              onSelect={handleOpenAudioMenu}
                              onFocus={handleAudioTrackFocus}
                              style={[styles.controlButton, styles.trackButton]}
                              disabled={isSeeking || activeMenu !== null}
                            />
                          )}
                          <Text style={[styles.trackLabel, styles.tvTrackLabel]} numberOfLines={1}>{audioSummary}</Text>
                        </View>
                      )}
                      {!skipOnlyMode && hasSubtitleSelection && subtitleSummary && !hasAudioSelection && (
                        <View style={styles.trackButtonGroup} pointerEvents="box-none">
                          {isLiveTV ? (
                            <DefaultFocus>
                              {isTvPlatform ? (
                                <TVButton
                                  ref={subtitleButtonRef as React.Ref<SpatialNavigationNodeRef>}
                                  icon="chatbubble-ellipses"
                                  onSelect={handleOpenSubtitlesMenu}
                                  onFocus={handleSubtitleTrackFocus}
                                  style={[styles.controlButton, styles.trackButton]}
                                  disabled={isSeeking || activeMenu !== null}
                                />
                              ) : (
                                <FocusablePressable
                                  ref={subtitleButtonRef as React.Ref<View>}
                                  icon="chatbubble-ellipses"
                                  focusKey="subtitle-track-button"
                                  onSelect={handleOpenSubtitlesMenu}
                                  onFocus={handleSubtitleTrackFocus}
                                  style={[styles.controlButton, styles.trackButton]}
                                  disabled={isSeeking || activeMenu !== null}
                                />
                              )}
                            </DefaultFocus>
                          ) : isTvPlatform ? (
                            <TVButton
                              ref={subtitleButtonRef as React.Ref<SpatialNavigationNodeRef>}
                              icon="chatbubble-ellipses"
                              onSelect={handleOpenSubtitlesMenu}
                              onFocus={handleSubtitleTrackFocus}
                              style={[styles.controlButton, styles.trackButton]}
                              disabled={isSeeking || activeMenu !== null}
                            />
                          ) : (
                            <FocusablePressable
                              ref={subtitleButtonRef as React.Ref<View>}
                              icon="chatbubble-ellipses"
                              focusKey="subtitle-track-button"
                              onSelect={handleOpenSubtitlesMenu}
                              onFocus={handleSubtitleTrackFocus}
                              style={[styles.controlButton, styles.trackButton]}
                              disabled={isSeeking || activeMenu !== null}
                            />
                          )}
                          <Text style={[styles.trackLabel, styles.tvTrackLabel]} numberOfLines={1}>{subtitleSummary}</Text>
                        </View>
                      )}
                      {!skipOnlyMode && hasSubtitleSelection && subtitleSummary && hasAudioSelection && (
                        <View style={styles.trackButtonGroup} pointerEvents="box-none">
                          {isTvPlatform ? (
                            <TVButton
                              ref={subtitleButtonRef as React.Ref<SpatialNavigationNodeRef>}
                              icon="chatbubble-ellipses"
                              onSelect={handleOpenSubtitlesMenu}
                              onFocus={handleSubtitleTrackSecondaryFocus}
                              style={[styles.controlButton, styles.trackButton]}
                              disabled={isSeeking || activeMenu !== null}
                            />
                          ) : (
                            <FocusablePressable
                              ref={subtitleButtonRef as React.Ref<View>}
                              icon="chatbubble-ellipses"
                              focusKey="subtitle-track-button-secondary"
                              onSelect={handleOpenSubtitlesMenu}
                              onFocus={handleSubtitleTrackSecondaryFocus}
                              style={[styles.controlButton, styles.trackButton]}
                              disabled={isSeeking || activeMenu !== null}
                            />
                          )}
                          <Text style={[styles.trackLabel, styles.tvTrackLabel]} numberOfLines={1}>{subtitleSummary}</Text>
                        </View>
                      )}
                    </>
                  )}
                  {/* Episode navigation buttons for TV platforms */}
                  {!skipOnlyMode && isTvPlatform && onPreviousEpisode && (
                    <View
                      style={[
                        styles.trackButtonGroup,
                        (!hasPreviousEpisode || shuffleMode) && styles.episodeButtonGroupDisabled,
                      ]}
                      pointerEvents="box-none">
                      <TVButton
                        icon="chevron-back-circle"
                        onSelect={onPreviousEpisode}
                        onFocus={handlePreviousEpisodeFocus}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null || !hasPreviousEpisode || shuffleMode}
                      />
                      <Text
                        style={[styles.trackLabel, (!hasPreviousEpisode || shuffleMode) && styles.trackLabelDisabled]}>
                        Prev Ep
                      </Text>
                    </View>
                  )}
                  {!skipOnlyMode && isTvPlatform && onNextEpisode && (
                    <View
                      style={[
                        styles.trackButtonGroup,
                        !hasNextEpisode && !shuffleMode && styles.episodeButtonGroupDisabled,
                      ]}
                      pointerEvents="box-none">
                      <View>
                        <TVButton
                          icon={shuffleMode ? 'shuffle' : 'chevron-forward-circle'}
                          onSelect={onNextEpisode}
                          onFocus={handleNextEpisodeFocus}
                          style={[styles.controlButton, styles.trackButton]}
                          disabled={isSeeking || activeMenu !== null || (!hasNextEpisode && !shuffleMode)}
                        />
                        {nextEpisodePrequeueReady && <View style={styles.prequeueReadyIndicatorTv} />}
                      </View>
                      <Text style={[styles.trackLabel, !hasNextEpisode && !shuffleMode && styles.trackLabelDisabled]}>
                        {shuffleMode ? 'Shuffle' : 'Next Ep'}
                      </Text>
                    </View>
                  )}
                  {/* Subtitle offset controls for TV platforms */}
                  {!skipOnlyMode && isTvPlatform && showSubtitleOffset && !isLiveTV && onSubtitleOffsetEarlier && onSubtitleOffsetLater && (
                    <View style={styles.subtitleOffsetTvGroup} pointerEvents="box-none">
                      <TVButton
                        icon="remove-circle-outline"
                        onSelect={onSubtitleOffsetEarlier}
                        onFocus={handleSubtitleOffsetEarlierFocus}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null}
                      />
                      <View style={styles.subtitleOffsetTvDisplay} pointerEvents="box-none">
                        <Text style={styles.subtitleOffsetTvLabel}>Subtitle</Text>
                        <Text style={styles.subtitleOffsetTvValue}>{formattedSubtitleOffset}</Text>
                      </View>
                      <TVButton
                        icon="add-circle-outline"
                        onSelect={onSubtitleOffsetLater}
                        onFocus={handleSubtitleOffsetLaterFocus}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null}
                      />
                    </View>
                  )}
                  {/* Info button for TV platforms (not for live TV) */}
                  {!skipOnlyMode && isTvPlatform && streamInfo && !isLiveTV && (
                    <View style={styles.trackButtonGroup} pointerEvents="box-none">
                      <TVButton
                        ref={infoButtonRef as React.Ref<SpatialNavigationNodeRef>}
                        icon="information-circle"
                        onSelect={handleOpenInfoMenu}
                        onFocus={handleInfoFocus}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null}
                      />
                    </View>
                  )}
                  {/* Speed button for TV platforms - after info button */}
                  {!skipOnlyMode && isTvPlatform && hasSpeedSelection && (
                    <View style={styles.trackButtonGroup} pointerEvents="box-none">
                      <TVButton
                        ref={speedButtonRef as React.Ref<SpatialNavigationNodeRef>}
                        icon="speedometer-outline"
                        onSelect={handleOpenSpeedMenu}
                        onFocus={handleSpeedFocus}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null}
                      />
                      <Text style={[styles.trackLabel, styles.tvTrackLabel]}>{speedSummary}</Text>
                    </View>
                  )}
                  {/* Skip segment button for TV platforms - far right of secondary row */}
                  {isTvPlatform && skipSegment && onSkipSegment && (
                    <View style={[styles.trackButtonGroup, styles.skipSegmentGroup]} pointerEvents="box-none">
                      {skipButtonRetainFocus || skipOnlyMode ? (
                        <DefaultFocus>
                          <TVButton
                            ref={skipSegmentButtonRef}
                            text={SEGMENT_LABELS[skipSegment.type]}
                            icon="play-forward"
                            onSelect={onSkipSegment}
                            onFocus={handleSkipSegmentFocus}
                            style={[styles.controlButton, styles.trackButton]}
                            disabled={isSeeking || activeMenu !== null}
                            variant="primary"
                          />
                        </DefaultFocus>
                      ) : (
                        <TVButton
                          ref={skipSegmentButtonRef}
                          text={SEGMENT_LABELS[skipSegment.type]}
                          icon="play-forward"
                          onSelect={onSkipSegment}
                          onFocus={handleSkipSegmentFocus}
                          style={[styles.controlButton, styles.trackButton]}
                          disabled={isSeeking || activeMenu !== null}
                          variant="primary"
                        />
                      )}
                    </View>
                  )}
                </View>
              </SpatialNavigationNode>
            )}
        </View>
      </SpatialNavigationNode>
      {/* Modal rendering removed - now handled by parent (player.tsx) for better TV focus layering */}
    </>
  );
};

const useControlsStyles = (theme: NovaTheme, screenWidth: number, screenHeight: number) => {
  // Calculate dynamic gap for center controls based on screen width
  // Button widths: play (80) + 2x skip (60) + 2x episode (50) = 300px max
  // We want comfortable spacing that scales down on narrow screens
  const centerControlsGap = Math.max(theme.spacing.sm, Math.min(theme.spacing.xl, (screenWidth - 300) / 6));
  // Viewport-height ratio: 1.0 on tvOS (1080p), ~0.5 on Android TV
  const vh = (screenHeight > 0 ? screenHeight : 1080) / TV_REFERENCE_HEIGHT;
  const controlButtonMinWidth = Math.round(60 * vh);

  return StyleSheet.create({
    centerControls: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'center',
      gap: centerControlsGap,
    },
    skipButtonContainer: {
      flex: 0,
    },
    skipButton: {
      backgroundColor: 'rgba(0, 0, 0, 0.6)',
      borderRadius: 60,
      width: 60,
      height: 60,
      alignItems: 'center',
      justifyContent: 'center',
      borderWidth: 2,
      borderColor: 'rgba(255, 255, 255, 0.3)',
    },
    episodeButton: {
      backgroundColor: 'rgba(0, 0, 0, 0.6)',
      borderRadius: 50,
      width: 50,
      height: 50,
      alignItems: 'center',
      justifyContent: 'center',
      borderWidth: 2,
      borderColor: 'rgba(255, 255, 255, 0.3)',
    },
    episodeButtonDisabled: {
      opacity: 0.4,
      borderColor: 'rgba(255, 255, 255, 0.15)',
    },
    prequeueReadyIndicator: {
      position: 'absolute',
      top: 2,
      right: 2,
      width: 10,
      height: 10,
      borderRadius: 5,
      backgroundColor: '#22c55e', // green-500
      borderWidth: 1,
      borderColor: 'rgba(255, 255, 255, 0.6)',
    },
    prequeueReadyIndicatorTv: {
      position: 'absolute',
      top: 4,
      right: 4,
      width: 12,
      height: 12,
      borderRadius: 6,
      backgroundColor: '#22c55e', // green-500
      borderWidth: 1.5,
      borderColor: 'rgba(255, 255, 255, 0.6)',
    },
    skipButtonContent: {
      alignItems: 'center',
      justifyContent: 'center',
      gap: 2,
    },
    skipButtonText: {
      color: theme.colors.text.primary,
      fontSize: 14,
      fontWeight: '700',
    },
    centerPlayButton: {
      backgroundColor: 'rgba(0, 0, 0, 0.6)',
      borderRadius: 40,
      width: 80,
      height: 80,
      alignItems: 'center',
      justifyContent: 'center',
      borderWidth: 2,
      borderColor: 'rgba(255, 255, 255, 0.3)',
    },
    centerPlayIcon: {
      color: theme.colors.text.primary,
      fontSize: 32,
      lineHeight: 32,
    },
    bottomControls: {
      position: 'absolute',
      bottom: theme.spacing.lg,
      left: theme.spacing.lg,
      right: theme.spacing.lg,
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.md,
      borderRadius: theme.radius.lg,
      backgroundColor: theme.colors.overlay.scrim,
    },
    bottomControlsMobile: {
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.md,
    },
    bottomControlsSkipOnly: {
      backgroundColor: 'transparent',
    },
    bottomControlsMobileLandscape: {
      bottom: theme.spacing.xs,
    },
    mainRow: {
      flexDirection: 'row',
      alignItems: 'center',
    },
    seekContainer: {
      flex: 1,
      marginHorizontal: theme.spacing.md,
    },
    seekContainerMobile: {
      marginHorizontal: theme.spacing.sm,
    },
    liveContainer: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.md,
      minHeight: theme.spacing['2xl'],
    },
    liveBadge: {
      backgroundColor: theme.colors.accent.primary,
      borderRadius: theme.radius.pill,
      paddingHorizontal: theme.spacing.md * 1.5,
      paddingVertical: theme.spacing.xs * 1.5,
    },
    liveBadgeText: {
      ...theme.typography.body.sm,
      fontSize: (theme.typography.body.sm.fontSize || 14) * 1.5,
      color: theme.colors.text.inverse,
      fontWeight: '600',
      letterSpacing: 1.5,
    },
    epgContainer: {
      flex: 1,
      justifyContent: 'center',
    },
    epgTitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      fontWeight: '500',
    },
    epgNext: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      marginTop: 2,
    },
    controlButton: {
      marginRight: theme.spacing.md,
      minWidth: controlButtonMinWidth,
    },
    trackButton: {
      marginRight: 0,
    },
    // Pushes PiP button to far right in landscape row
    pipButtonSpacer: {
      flex: 1,
    },
    // PiP button styled to match FocusablePressable
    pipButton: {
      backgroundColor: theme.colors.overlay.button,
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.sm,
      borderRadius: theme.radius.md,
      alignItems: 'center',
      justifyContent: 'center',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    // Speed icon with overlaid badge
    speedIconContainer: {
      position: 'relative' as const,
      alignItems: 'center',
      justifyContent: 'center',
    },
    speedBadge: {
      position: 'absolute' as const,
      bottom: -2,
      alignSelf: 'center',
      backgroundColor: 'rgba(0,0,0,0.75)',
      borderRadius: 3,
      paddingHorizontal: 2,
      paddingVertical: 0.5,
    },
    speedBadgeText: {
      color: theme.colors.text.primary,
      fontWeight: '700' as const,
      textAlign: 'center' as const,
    },
    // Portrait: stacked layout container
    mobilePipContainer: {
      width: '100%',
    },
    // Row that holds tracks + PiP icon
    mobilePipRow: {
      flexDirection: 'row',
      alignItems: 'center',
      marginBottom: theme.spacing.xs,
    },
    // Track button group that fills available space (for PiP row)
    trackButtonGroupFlex: {
      flexDirection: 'row',
      alignItems: 'center',
      flex: 1,
      marginRight: theme.spacing.sm,
    },
    secondaryRow: {
      marginTop: theme.spacing.sm,
      flexDirection: 'row',
      flexWrap: 'wrap',
      alignItems: 'center',
    },
    trackButtonGroup: {
      flexDirection: 'row',
      alignItems: 'center',
      marginRight: theme.spacing.lg,
      marginBottom: theme.spacing.xs,
    },
    skipSegmentGroup: {
      marginLeft: 'auto',
    },
    exitButton: {
      position: 'absolute' as const,
      top: Math.round(theme.spacing.lg * vh),
      left: Math.round(theme.spacing.lg * vh),
      paddingVertical: Math.round(theme.spacing.md * vh),
      paddingHorizontal: Math.round(theme.spacing.lg * vh),
      marginHorizontal: Math.round(theme.spacing.lg * vh),
    },
    exitButtonText: {
      fontSize: Math.round(16 * vh),
      lineHeight: Math.round(21 * vh),
    },
    trackLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      marginLeft: theme.spacing.sm,
      flexShrink: 1,
    },
    tvTrackLabel: {
      maxWidth: Math.round(200 * vh),
    },
    trackLabelDisabled: {
      color: theme.colors.text.disabled,
    },
    episodeButtonGroupDisabled: {
      opacity: 0.5,
    },
    seekingDisabled: {
      opacity: 0.3,
    },
    buttonGroup: {
      flexDirection: 'row',
      alignItems: 'center',
    },
    // TV skip button with label showing seek amount
    tvSkipButtonContainer: {
      flexDirection: 'row',
      alignItems: 'center',
    },
    tvSkipLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      marginLeft: -Math.round(theme.spacing.sm * vh),
      marginRight: theme.spacing.md,
    },
    // Mobile subtitle offset styles
    subtitleOffsetContainer: {
      position: 'absolute',
      top: '60%',
      left: 0,
      right: 0,
      alignItems: 'center',
      justifyContent: 'center',
    },
    subtitleOffsetContainerLandscape: {
      top: '25%', // Above play button in landscape (with clearance) - phones only
    },
    subtitleOffsetContainerTabletLandscape: {
      top: '65%', // Below play button in landscape - tablets have more vertical space
    },
    subtitleOffsetRow: {
      flexDirection: 'row',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.6)',
      borderRadius: theme.radius.md,
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: theme.spacing.xs,
      gap: theme.spacing.sm,
    },
    subtitleOffsetButton: {
      width: 32,
      height: 32,
      borderRadius: 16,
      backgroundColor: 'rgba(255, 255, 255, 0.15)',
      alignItems: 'center',
      justifyContent: 'center',
    },
    subtitleOffsetLabelContainer: {
      alignItems: 'center',
      minWidth: 60,
    },
    subtitleOffsetLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontSize: 10,
    },
    subtitleOffsetValue: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      fontWeight: '600',
      fontSize: 14,
    },
    // TV subtitle offset styles - Android TV has 30% less padding
    subtitleOffsetTvGroup: {
      flexDirection: 'row',
      alignItems: 'center',
      marginRight: Math.round(theme.spacing.xl * vh),
      marginBottom: Math.round(theme.spacing.xs * vh),
    },
    subtitleOffsetTvDisplay: {
      alignItems: 'center',
      marginHorizontal: Math.round(theme.spacing.sm * vh),
      minWidth: Math.round(60 * vh),
    },
    subtitleOffsetTvLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontSize: 10,
    },
    subtitleOffsetTvValue: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
  });
};

// Memoize Controls to prevent re-renders when only currentTime/duration change
// The SeekBar inside will still update, but the control buttons won't re-render
export default memo(Controls);
