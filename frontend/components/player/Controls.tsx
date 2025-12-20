import FocusablePressable from '@/components/FocusablePressable';
import SeekBar from '@/components/player/SeekBar';
import VolumeControl from '@/components/player/VolumeControl';
import { TrackSelectionModal } from '@/components/player/TrackSelectionModal';
import { StreamInfoModal, type StreamInfoData } from '@/components/player/StreamInfoModal';
import { DefaultFocus, SpatialNavigationNode } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Platform, Pressable, StyleSheet, Text, View, useWindowDimensions } from 'react-native';

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
}) => {
  const theme = useTheme();
  const { width, height } = useWindowDimensions();
  const styles = useMemo(() => useControlsStyles(theme), [theme]);
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
    if (!subtitleTracks.length) {
      return undefined;
    }
    const fallback = subtitleTracks[0]?.label;
    if (!selectedSubtitleTrackId) {
      return fallback;
    }
    return subtitleTracks.find((track) => track.id === selectedSubtitleTrackId)?.label ?? fallback;
  }, [selectedSubtitleTrackId, subtitleTracks]);

  const hasAudioSelection = allowTrackSelection && Boolean(onSelectAudioTrack) && audioTracks.length > 0;
  const hasSubtitleSelection =
    allowTrackSelection &&
    Boolean(onSelectSubtitleTrack) &&
    subtitleTracks.some((track) => Number.isFinite(Number(track.id)));
  const showFullscreenButton = Boolean(onToggleFullscreen) && !isMobile && !isLiveTV && !isTvPlatform;

  const activeMenuRef = useRef<ActiveMenu>(null);

  useEffect(() => {
    activeMenuRef.current = activeMenu;
  }, [activeMenu]);

  const openMenu = useCallback(
    (menu: Exclude<ActiveMenu, null>) => {
      setActiveMenu(menu);
      onModalStateChange?.(true);
    },
    [onModalStateChange],
  );

  const closeMenu = useCallback(() => {
    setActiveMenu(null);
    onModalStateChange?.(false);
  }, [onModalStateChange]);

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

  return (
    <>
      {/* Mobile center controls */}
      {isMobile && !isLiveTV && (
        <View style={styles.centerControls} pointerEvents="box-none">
          {onSkipBackward && (
            <View style={styles.skipButtonContainer}>
              <Pressable onPress={onSkipBackward} style={styles.skipButton}>
                <View style={styles.skipButtonContent}>
                  <Text style={styles.skipButtonText}>30</Text>
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
                  <Text style={styles.skipButtonText}>30</Text>
                  <Ionicons name="play-forward" size={20} color={theme.colors.text.primary} />
                </View>
              </Pressable>
            </View>
          )}
        </View>
      )}
      <SpatialNavigationNode orientation="vertical">
        <View
          style={[
            styles.bottomControls,
            isMobile && styles.bottomControlsMobile,
            isMobile && isLandscape && styles.bottomControlsMobileLandscape,
          ]}>
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
                        onFocus={() => onFocusChange?.('play-pause-button')}
                        style={styles.controlButton}
                        disabled={isSeeking}
                      />
                    </DefaultFocus>
                    {onSkipBackward && (
                      <FocusablePressable
                        icon="play-skip-back"
                        focusKey="skip-back-button"
                        onSelect={onSkipBackward}
                        onFocus={() => onFocusChange?.('skip-back-button')}
                        style={styles.controlButton}
                        disabled={isSeeking}
                      />
                    )}
                    {onSkipForward && (
                      <FocusablePressable
                        icon="play-skip-forward"
                        focusKey="skip-forward-button"
                        onSelect={onSkipForward}
                        onFocus={() => onFocusChange?.('skip-forward-button')}
                        style={styles.controlButton}
                        disabled={isSeeking}
                      />
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
                      onFocus={() => onFocusChange?.('fullscreen-button')}
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
          {(hasAudioSelection || hasSubtitleSelection || (isTvPlatform && streamInfo)) && (
            <SpatialNavigationNode orientation="horizontal">
              <View style={[styles.secondaryRow, isSeeking && styles.seekingDisabled]} pointerEvents="box-none">
                {hasAudioSelection && audioSummary && (
                  <View style={styles.trackButtonGroup} pointerEvents="box-none">
                    {isLiveTV ? (
                      <DefaultFocus>
                        <FocusablePressable
                          icon="musical-notes"
                          focusKey="audio-track-button"
                          onSelect={() => openMenu('audio')}
                          onFocus={() => onFocusChange?.('audio-track-button')}
                          style={[styles.controlButton, styles.trackButton]}
                          disabled={isSeeking || activeMenu !== null}
                        />
                      </DefaultFocus>
                    ) : (
                      <FocusablePressable
                        icon="musical-notes"
                        focusKey="audio-track-button"
                        onSelect={() => openMenu('audio')}
                        onFocus={() => onFocusChange?.('audio-track-button')}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null}
                      />
                    )}
                    <Text style={styles.trackLabel} numberOfLines={1}>
                      {audioSummary}
                    </Text>
                  </View>
                )}
                {hasSubtitleSelection && subtitleSummary && !hasAudioSelection && (
                  <View style={styles.trackButtonGroup} pointerEvents="box-none">
                    {isLiveTV ? (
                      <DefaultFocus>
                        <FocusablePressable
                          icon="chatbubble-ellipses"
                          focusKey="subtitle-track-button"
                          onSelect={() => openMenu('subtitles')}
                          onFocus={() => onFocusChange?.('subtitle-track-button')}
                          style={[styles.controlButton, styles.trackButton]}
                          disabled={isSeeking || activeMenu !== null}
                        />
                      </DefaultFocus>
                    ) : (
                      <FocusablePressable
                        icon="chatbubble-ellipses"
                        focusKey="subtitle-track-button"
                        onSelect={() => openMenu('subtitles')}
                        onFocus={() => onFocusChange?.('subtitle-track-button')}
                        style={[styles.controlButton, styles.trackButton]}
                        disabled={isSeeking || activeMenu !== null}
                      />
                    )}
                    <Text style={styles.trackLabel} numberOfLines={1}>
                      {subtitleSummary}
                    </Text>
                  </View>
                )}
                {hasSubtitleSelection && subtitleSummary && hasAudioSelection && (
                  <View style={styles.trackButtonGroup} pointerEvents="box-none">
                    <FocusablePressable
                      icon="chatbubble-ellipses"
                      focusKey="subtitle-track-button-secondary"
                      onSelect={() => openMenu('subtitles')}
                      onFocus={() => onFocusChange?.('subtitle-track-button-secondary')}
                      style={[styles.controlButton, styles.trackButton]}
                      disabled={isSeeking || activeMenu !== null}
                    />
                    <Text style={styles.trackLabel} numberOfLines={1}>
                      {subtitleSummary}
                    </Text>
                  </View>
                )}
                {/* Info button for TV platforms */}
                {isTvPlatform && streamInfo && (
                  <View style={styles.trackButtonGroup} pointerEvents="box-none">
                    <FocusablePressable
                      icon="information-circle"
                      focusKey="info-button"
                      onSelect={() => openMenu('info')}
                      onFocus={() => onFocusChange?.('info-button')}
                      style={[styles.controlButton, styles.trackButton]}
                      disabled={isSeeking || activeMenu !== null}
                    />
                    <Text style={styles.trackLabel} numberOfLines={1}>
                      Info
                    </Text>
                  </View>
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
        />
      ) : null}
      {activeMenu === 'info' && streamInfo ? (
        <StreamInfoModal visible={true} info={streamInfo} onClose={closeMenu} />
      ) : null}
    </>
  );
};

const useControlsStyles = (theme: NovaTheme) => {
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
      gap: theme.spacing.xl,
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
      bottom: theme.spacing.md,
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
      minWidth: 60,
    },
    trackButton: {
      marginRight: 0,
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
      maxWidth: 200,
    },
    seekingDisabled: {
      opacity: 0.3,
    },
    buttonGroup: {
      flexDirection: 'row',
      alignItems: 'center',
    },
  });
};

export default Controls;
