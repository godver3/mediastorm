import FocusablePressable from '@/components/FocusablePressable';
import SeekBar from '@/components/player/SeekBar';
import VolumeControl from '@/components/player/VolumeControl';
import { TrackSelectionModal } from '@/components/player/TrackSelectionModal';
import { StreamInfoModal, type StreamInfoData } from '@/components/player/StreamInfoModal';
import { DefaultFocus, SpatialNavigationNode } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import React, { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';

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
}

type TrackOption = {
  id: string;
  label: string;
  description?: string;
};

type ActiveMenu = 'audio' | 'subtitles' | 'info' | null;

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
}) => {
  const theme = useTheme();
  const { width, height } = useTVDimensions();
  const styles = useMemo(() => useControlsStyles(theme, width), [theme, width]);
  const showVolume = Platform.OS === 'web';
  const isTvPlatform = Platform.isTV;
  const isMobile = Platform.OS !== 'web' && !isTvPlatform;
  const allowTrackSelection = true; // Allow track selection on all platforms including tvOS
  const isLandscape = width >= height;
  const isSeekable = Number.isFinite(duration) && duration > 0;
  const [activeMenu, setActiveMenu] = useState<ActiveMenu>(null);

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

  const hasAudioSelection = allowTrackSelection && Boolean(onSelectAudioTrack) && audioTracks.length > 0;
  // Always show subtitle button when onSelectSubtitleTrack is provided (for external subtitle search)
  const hasSubtitleSelection = allowTrackSelection && Boolean(onSelectSubtitleTrack);
  const showFullscreenButton = Boolean(onToggleFullscreen) && !isMobile && !isLiveTV && !isTvPlatform;
  // PiP button: only show on iOS mobile (not TV, not live TV)
  const showPipButton = Boolean(onEnterPip) && isMobile && Platform.OS === 'ios' && !isLiveTV;

  // Compute a key for the secondary row that changes when buttons change,
  // forcing the spatial navigation tree to be regenerated
  const secondaryRowKey = useMemo(() => {
    const parts: string[] = [];
    if (hasAudioSelection) parts.push('audio');
    if (hasSubtitleSelection) parts.push('sub');
    if (isTvPlatform && onPreviousEpisode) parts.push('prev');
    if (isTvPlatform && onNextEpisode) parts.push('next');
    if (isTvPlatform && showSubtitleOffset) parts.push('offset');
    if (isTvPlatform && streamInfo) parts.push('info');
    return `secondary-${parts.join('-')}`;
  }, [hasAudioSelection, hasSubtitleSelection, isTvPlatform, onPreviousEpisode, onNextEpisode, showSubtitleOffset, streamInfo]);

  const activeMenuRef = useRef<ActiveMenu>(null);
  // Guard to prevent modal from immediately reopening when focus returns to the button on tvOS
  const menuClosingGuardRef = useRef(false);

  useEffect(() => {
    activeMenuRef.current = activeMenu;
  }, [activeMenu]);

  const openMenu = useCallback(
    (menu: Exclude<ActiveMenu, null>) => {
      // On TV platforms, check if we just closed a menu (prevents focus-return re-triggering)
      if (Platform.isTV && menuClosingGuardRef.current) {
        console.log('[Controls] openMenu blocked by closing guard', { menu });
        return;
      }
      console.log('[Controls] openMenu called', { menu, currentActiveMenu: activeMenuRef.current });
      setActiveMenu(menu);
      onModalStateChange?.(true);
    },
    [onModalStateChange],
  );

  const closeMenu = useCallback(() => {
    console.log('[Controls] closeMenu called', { currentActiveMenu: activeMenuRef.current });
    // Set guard to prevent immediate re-opening on TV platforms
    if (Platform.isTV) {
      menuClosingGuardRef.current = true;
      setTimeout(() => {
        menuClosingGuardRef.current = false;
      }, 400);
    }
    setActiveMenu(null);
    onModalStateChange?.(false);
  }, [onModalStateChange]);

  // Wrapped callback for transitioning to subtitle search modal.
  // On tvOS, we need careful timing to transition focus between modals.
  const handleOpenSubtitleSearch = useCallback(() => {
    // Close this modal first
    setActiveMenu(null);
    // Open the SubtitleSearchModal immediately (no delay needed since both
    // state updates will be batched by React and rendered together)
    onSearchSubtitles?.();
  }, [onSearchSubtitles]);

  useEffect(
    () => () => {
      if (activeMenuRef.current !== null) {
        onModalStateChange?.(false);
      }
    },
    [onModalStateChange],
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
  const handlePlayPauseFocus = useCallback(() => onFocusChange?.('play-pause-button'), [onFocusChange]);
  const handleSkipBackFocus = useCallback(() => onFocusChange?.('skip-back-button'), [onFocusChange]);
  const handleSkipForwardFocus = useCallback(() => onFocusChange?.('skip-forward-button'), [onFocusChange]);
  const handleFullscreenFocus = useCallback(() => onFocusChange?.('fullscreen-button'), [onFocusChange]);
  const handleAudioTrackFocus = useCallback(() => onFocusChange?.('audio-track-button'), [onFocusChange]);
  const handleSubtitleTrackFocus = useCallback(() => onFocusChange?.('subtitle-track-button'), [onFocusChange]);
  const handleSubtitleTrackSecondaryFocus = useCallback(() => onFocusChange?.('subtitle-track-button-secondary'), [onFocusChange]);
  const handlePreviousEpisodeFocus = useCallback(() => onFocusChange?.('previous-episode-button'), [onFocusChange]);
  const handleNextEpisodeFocus = useCallback(() => onFocusChange?.('next-episode-button'), [onFocusChange]);
  const handleSubtitleOffsetEarlierFocus = useCallback(() => onFocusChange?.('subtitle-offset-earlier'), [onFocusChange]);
  const handleSubtitleOffsetLaterFocus = useCallback(() => onFocusChange?.('subtitle-offset-later'), [onFocusChange]);
  const handleInfoFocus = useCallback(() => onFocusChange?.('info-button'), [onFocusChange]);

  // Memoize menu openers to stabilize onSelect props
  const handleOpenAudioMenu = useCallback(() => openMenu('audio'), [openMenu]);
  const handleOpenSubtitlesMenu = useCallback(() => openMenu('subtitles'), [openMenu]);
  const handleOpenInfoMenu = useCallback(() => openMenu('info'), [openMenu]);

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
            <View style={styles.skipButtonContainer}>
              <Pressable onPress={onSkipBackward} style={styles.skipButton}>
                <View style={styles.skipButtonContent}>
                  <Text style={styles.skipButtonText}>{seekBackwardSeconds}</Text>
                  <Ionicons name="play-back" size={20} color={theme.colors.text.primary} />
                </View>
              </Pressable>
            </View>
          )}
          <DefaultFocus>
            <Pressable onPress={onPlayPause} style={styles.centerPlayButton}>
              <Ionicons name={paused ? 'play' : 'pause'} size={40} color={theme.colors.text.primary} />
            </Pressable>
          </DefaultFocus>
          {onSkipForward && (
            <View style={styles.skipButtonContainer}>
              <Pressable onPress={onSkipForward} style={styles.skipButton}>
                <View style={styles.skipButtonContent}>
                  <Text style={styles.skipButtonText}>{seekForwardSeconds}</Text>
                  <Ionicons name="play-forward" size={20} color={theme.colors.text.primary} />
                </View>
              </Pressable>
            </View>
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
                {nextEpisodePrequeueReady && (
                  <View style={styles.prequeueReadyIndicator} />
                )}
              </Pressable>
            </View>
          )}
        </View>
      )}
      {/* Mobile subtitle offset controls - above play button in landscape, below in portrait */}
      {isMobile && showSubtitleOffset && (
        <View style={[styles.subtitleOffsetContainer, isLandscape && styles.subtitleOffsetContainerLandscape]} pointerEvents="box-none">
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
      <SpatialNavigationNode orientation="vertical">
        <View
          style={[
            styles.bottomControls,
            isMobile && styles.bottomControlsMobile,
            isMobile && isLandscape && styles.bottomControlsMobileLandscape,
          ]}
          renderToHardwareTextureAndroid={isTvPlatform}>
          {!isLiveTV && (
            <SpatialNavigationNode orientation="horizontal">
              <View style={styles.mainRow} pointerEvents="box-none">
                {!isMobile && (
                  <View style={[styles.buttonGroup, isSeeking && styles.seekingDisabled]}>
                    <DefaultFocus>
                      <FocusablePressable
                        icon={paused ? 'play' : 'pause'}
                        focusKey="play-pause-button"
                        onSelect={onPlayPause}
                        onFocus={handlePlayPauseFocus}
                        style={styles.controlButton}
                        disabled={isSeeking}
                      />
                    </DefaultFocus>
                    {onSkipBackward && (
                      <View style={styles.tvSkipButtonContainer}>
                        <FocusablePressable
                          icon="play-back"
                          focusKey="skip-back-button"
                          onSelect={onSkipBackward}
                          onFocus={handleSkipBackFocus}
                          style={styles.controlButton}
                          disabled={isSeeking}
                        />
                        <Text style={styles.tvSkipLabel}>{seekBackwardSeconds}s</Text>
                      </View>
                    )}
                    {onSkipForward && (
                      <View style={styles.tvSkipButtonContainer}>
                        <FocusablePressable
                          icon="play-forward"
                          focusKey="skip-forward-button"
                          onSelect={onSkipForward}
                          onFocus={handleSkipForwardFocus}
                          style={styles.controlButton}
                          disabled={isSeeking}
                        />
                        <Text style={styles.tvSkipLabel}>{seekForwardSeconds}s</Text>
                      </View>
                    )}
                  </View>
                )}
                {/* Mobile landscape: track selection and PiP in main row */}
                {isMobile && isLandscape && (hasAudioSelection || hasSubtitleSelection || showPipButton) && (
                  <View style={styles.mobileTrackGroup}>
                    {hasAudioSelection && audioSummary && (
                      <Pressable onPress={handleOpenAudioMenu} style={styles.mobileTrackButton}>
                        <Ionicons name="musical-notes" size={18} color={theme.colors.text.primary} />
                        <Text style={styles.mobileTrackLabel}>{audioSummary}</Text>
                      </Pressable>
                    )}
                    {hasSubtitleSelection && subtitleSummary && (
                      <Pressable onPress={handleOpenSubtitlesMenu} style={styles.mobileTrackButton}>
                        <Ionicons name="chatbubble-ellipses" size={18} color={theme.colors.text.primary} />
                        <Text style={styles.mobileTrackLabel}>{subtitleSummary}</Text>
                      </Pressable>
                    )}
                    {showPipButton && (
                      <Pressable onPress={onEnterPip} style={styles.mobileTrackButton}>
                        <Ionicons name="browsers-outline" size={18} color={theme.colors.text.primary} />
                        <Text style={styles.mobileTrackLabel}>PiP</Text>
                      </Pressable>
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
                      disabled={isSeeking}
                    />
                  )}
                </View>
              </View>
            </SpatialNavigationNode>
          )}
          {isLiveTV && (
            <View style={styles.mainRow} pointerEvents="box-none">
              <View style={[styles.seekContainer, isMobile && styles.seekContainerMobile]} pointerEvents="box-none">
                {hasStartedPlaying && (
                  <View style={styles.liveContainer}>
                    <View style={styles.liveBadge}>
                      <Text style={styles.liveBadgeText}>LIVE</Text>
                    </View>
                  </View>
                )}
              </View>
            </View>
          )}
          {/* Secondary row: hidden in mobile landscape (track selection moved to main row) */}
          {!(isMobile && isLandscape) && (hasAudioSelection || hasSubtitleSelection || (isTvPlatform && streamInfo) || (isTvPlatform && (onPreviousEpisode || onNextEpisode)) || (isTvPlatform && showSubtitleOffset) || (showPipButton && !isLandscape)) && (
            <SpatialNavigationNode key={secondaryRowKey} orientation="horizontal">
              <View style={[styles.secondaryRow, isSeeking && styles.seekingDisabled]} pointerEvents="box-none">
                {hasAudioSelection && audioSummary && (
                  <View style={styles.trackButtonGroup} pointerEvents="box-none">
                    {isLiveTV ? (
                      <DefaultFocus>
                        <FocusablePressable
                          icon="musical-notes"
                          focusKey="audio-track-button"
                          onSelect={handleOpenAudioMenu}
                          onFocus={handleAudioTrackFocus}
                          style={[styles.controlButton, styles.trackButton]}
                          disabled={isSeeking || activeMenu !== null}
                        />
                      </DefaultFocus>
                    ) : (
                      <FocusablePressable
                        icon="musical-notes"
                        focusKey="audio-track-button"
                        onSelect={handleOpenAudioMenu}
                        onFocus={handleAudioTrackFocus}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null}
                      />
                    )}
                    <Text style={styles.trackLabel}>{audioSummary}</Text>
                  </View>
                )}
                {hasSubtitleSelection && subtitleSummary && !hasAudioSelection && (
                  <View style={styles.trackButtonGroup} pointerEvents="box-none">
                    {isLiveTV ? (
                      <DefaultFocus>
                        <FocusablePressable
                          icon="chatbubble-ellipses"
                          focusKey="subtitle-track-button"
                          onSelect={handleOpenSubtitlesMenu}
                          onFocus={handleSubtitleTrackFocus}
                          style={[styles.controlButton, styles.trackButton]}
                          disabled={isSeeking || activeMenu !== null}
                        />
                      </DefaultFocus>
                    ) : (
                      <FocusablePressable
                        icon="chatbubble-ellipses"
                        focusKey="subtitle-track-button"
                        onSelect={handleOpenSubtitlesMenu}
                        onFocus={handleSubtitleTrackFocus}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null}
                      />
                    )}
                    <Text style={styles.trackLabel}>{subtitleSummary}</Text>
                  </View>
                )}
                {hasSubtitleSelection && subtitleSummary && hasAudioSelection && (
                  <View style={styles.trackButtonGroup} pointerEvents="box-none">
                    <FocusablePressable
                      icon="chatbubble-ellipses"
                      focusKey="subtitle-track-button-secondary"
                      onSelect={handleOpenSubtitlesMenu}
                      onFocus={handleSubtitleTrackSecondaryFocus}
                      style={[styles.controlButton, styles.trackButton]}
                      disabled={isSeeking || activeMenu !== null}
                    />
                    <Text style={styles.trackLabel}>{subtitleSummary}</Text>
                  </View>
                )}
                {/* PiP button for mobile portrait */}
                {showPipButton && !isLandscape && (
                  <View style={styles.trackButtonGroup} pointerEvents="box-none">
                    <Pressable
                      onPress={onEnterPip}
                      style={[styles.controlButton, styles.trackButton, styles.pipButton]}>
                      <Ionicons name="browsers-outline" size={24} color={theme.colors.text.primary} />
                    </Pressable>
                    <Text style={styles.trackLabel}>PiP</Text>
                  </View>
                )}
                {/* Episode navigation buttons for TV platforms */}
                {isTvPlatform && onPreviousEpisode && (
                  <View style={[styles.trackButtonGroup, (!hasPreviousEpisode || shuffleMode) && styles.episodeButtonGroupDisabled]} pointerEvents="box-none">
                    <FocusablePressable
                      icon="chevron-back-circle"
                      focusKey="previous-episode-button"
                      onSelect={onPreviousEpisode}
                      onFocus={handlePreviousEpisodeFocus}
                      style={[styles.controlButton, styles.trackButton]}
                      disabled={isSeeking || activeMenu !== null || !hasPreviousEpisode || shuffleMode}
                    />
                    <Text style={[styles.trackLabel, (!hasPreviousEpisode || shuffleMode) && styles.trackLabelDisabled]}>Prev Ep</Text>
                  </View>
                )}
                {isTvPlatform && onNextEpisode && (
                  <View style={[styles.trackButtonGroup, !hasNextEpisode && !shuffleMode && styles.episodeButtonGroupDisabled]} pointerEvents="box-none">
                    <View>
                      <FocusablePressable
                        icon={shuffleMode ? 'shuffle' : 'chevron-forward-circle'}
                        focusKey="next-episode-button"
                        onSelect={onNextEpisode}
                        onFocus={handleNextEpisodeFocus}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null || (!hasNextEpisode && !shuffleMode)}
                      />
                      {nextEpisodePrequeueReady && (
                        <View style={styles.prequeueReadyIndicatorTv} />
                      )}
                    </View>
                    <Text style={[styles.trackLabel, !hasNextEpisode && !shuffleMode && styles.trackLabelDisabled]}>
                      {shuffleMode ? 'Shuffle' : 'Next Ep'}
                    </Text>
                  </View>
                )}
                {/* Subtitle offset controls for TV platforms */}
                {isTvPlatform && showSubtitleOffset && onSubtitleOffsetEarlier && onSubtitleOffsetLater && (
                  <View style={styles.subtitleOffsetTvGroup} pointerEvents="box-none">
                    <FocusablePressable
                      icon="remove-circle-outline"
                      focusKey="subtitle-offset-earlier"
                      onSelect={onSubtitleOffsetEarlier}
                      onFocus={handleSubtitleOffsetEarlierFocus}
                      style={[styles.controlButton, styles.trackButton]}
                      disabled={isSeeking || activeMenu !== null}
                    />
                    <View style={styles.subtitleOffsetTvDisplay} pointerEvents="box-none">
                      <Text style={styles.subtitleOffsetTvLabel}>Subtitle</Text>
                      <Text style={styles.subtitleOffsetTvValue}>{formattedSubtitleOffset}</Text>
                    </View>
                    <FocusablePressable
                      icon="add-circle-outline"
                      focusKey="subtitle-offset-later"
                      onSelect={onSubtitleOffsetLater}
                      onFocus={handleSubtitleOffsetLaterFocus}
                      style={[styles.controlButton, styles.trackButton]}
                      disabled={isSeeking || activeMenu !== null}
                    />
                  </View>
                )}
                {/* Info button for TV platforms (not for live TV) */}
                {isTvPlatform && streamInfo && !isLiveTV && (
                  <FocusablePressable
                    icon="information-circle"
                    focusKey="info-button"
                    onSelect={handleOpenInfoMenu}
                    onFocus={handleInfoFocus}
                    style={[styles.controlButton, styles.trackButton]}
                    disabled={isSeeking || activeMenu !== null}
                  />
                )}
              </View>
            </SpatialNavigationNode>
          )}
        </View>
      </SpatialNavigationNode>
      {activeMenu === 'audio' || activeMenu === 'subtitles' ? (
        <TrackSelectionModal
          visible={true}
          title={activeMenu === 'audio' ? 'Audio Tracks' : 'Subtitles'}
          subtitle={trackModalSubtitle}
          options={activeOptions}
          selectedId={selectedTrackId}
          onSelect={handleSelectTrack}
          onClose={closeMenu}
          focusKeyPrefix={activeMenu}
          onSearchSubtitles={activeMenu === 'subtitles' && !isLiveTV ? handleOpenSubtitleSearch : undefined}
        />
      ) : null}
      {activeMenu === 'info' && streamInfo ? (
        <StreamInfoModal visible={true} info={streamInfo} onClose={closeMenu} />
      ) : null}
    </>
  );
};

const useControlsStyles = (theme: NovaTheme, screenWidth: number) => {
  // Calculate dynamic gap for center controls based on screen width
  // Button widths: play (80) + 2x skip (60) + 2x episode (50) = 300px max
  // We want comfortable spacing that scales down on narrow screens
  const centerControlsGap = Math.max(theme.spacing.sm, Math.min(theme.spacing.xl, (screenWidth - 300) / 6));
  const isAndroidTV = Platform.isTV && Platform.OS === 'android';
  // Android TV control buttons are 50% smaller (40% + 10%)
  const controlButtonMinWidth = isAndroidTV ? 32 : 60;

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
      height: theme.spacing['2xl'],
      justifyContent: 'center',
      alignItems: 'flex-start',
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
    controlButton: {
      marginRight: theme.spacing.md,
      minWidth: controlButtonMinWidth,
    },
    trackButton: {
      marginRight: 0,
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
    trackLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      marginLeft: theme.spacing.sm,
      flexShrink: 1,
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
    // Mobile landscape: compact track selection in main row
    mobileTrackGroup: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
      marginRight: theme.spacing.md,
      marginTop: -2, // lift buttons slightly
    },
    mobileTrackButton: {
      flexDirection: 'row',
      alignItems: 'center',
      backgroundColor: 'rgba(255, 255, 255, 0.15)',
      borderRadius: theme.radius.sm,
      paddingVertical: theme.spacing.xs,
      paddingHorizontal: theme.spacing.sm,
      gap: theme.spacing.xs,
    },
    mobileTrackLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      fontSize: 12,
      flexShrink: 1,
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
      marginLeft: isAndroidTV ? -theme.spacing.xs : -theme.spacing.sm,
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
      top: '25%', // Above play button in landscape (with clearance)
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
      marginRight: isAndroidTV ? theme.spacing.xl * 0.7 : theme.spacing.xl,
      marginBottom: isAndroidTV ? theme.spacing.xs * 0.7 : theme.spacing.xs,
    },
    subtitleOffsetTvDisplay: {
      alignItems: 'center',
      marginHorizontal: isAndroidTV ? theme.spacing.sm * 0.7 : theme.spacing.sm,
      minWidth: isAndroidTV ? 42 : 60,
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
