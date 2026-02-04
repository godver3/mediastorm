import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Platform,
  StatusBar,
  StyleSheet,
  Text,
  TextInput,
  View,
  Pressable,
  ScrollView,
} from 'react-native';
import { router, Stack } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';

import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import LoadingIndicator from '@/components/LoadingIndicator';
import { NativePlayer, type NativePlayerRef, type Track } from '@/components/player/NativePlayer';
import { DefaultFocus, SpatialNavigationNode, SpatialNavigationRoot } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';

// Sample test URLs
const SAMPLE_URLS = [
  // Standard test streams
  {
    label: 'Big Buck Bunny (HLS)',
    url: 'https://test-streams.mux.dev/x36xhzz/x36xhzz.m3u8',
  },
  {
    label: 'Sintel (MKV)',
    url: 'https://download.blender.org/demo/movies/Sintel.2010.720p.mkv',
  },
  {
    label: 'Tears of Steel (HLS)',
    url: 'https://demo.unified-streaming.com/k8s/features/stable/video/tears-of-steel/tears-of-steel.mp4/.m3u8',
  },
  // HDR10 test streams (Jellyfin)
  {
    label: 'HDR10 1080p HEVC 10M',
    url: 'https://repo.jellyfin.org/test-videos/HDR/HDR10/HEVC/Test%20Jellyfin%201080p%20HEVC%20HDR10%2010M.mp4',
  },
  {
    label: 'HDR10 4K HEVC 40M',
    url: 'https://repo.jellyfin.org/test-videos/HDR/HDR10/HEVC/Test%20Jellyfin%204K%20HEVC%20HDR10%2040M.mp4',
  },
  {
    label: 'HDR10 4K AV1 50M',
    url: 'https://repo.jellyfin.org/test-videos/HDR/HDR10/AV1/Test%20Jellyfin%204K%20AV1%20HDR10%2050M.mp4',
  },
  // Dolby Vision test streams (Jellyfin)
  {
    label: 'Dolby Vision 1080p P5',
    url: 'https://repo.jellyfin.org/test-videos/HDR/Dolby%20Vision/Test%20Jellyfin%201080p%20DV%20P5.mp4',
  },
  {
    label: 'Dolby Vision 4K P8.1',
    url: 'https://repo.jellyfin.org/test-videos/HDR/Dolby%20Vision/Test%20Jellyfin%204K%20DV%20P8.1.mp4',
  },
  {
    label: 'Dolby Vision 4K P5',
    url: 'https://repo.jellyfin.org/test-videos/HDR/Dolby%20Vision/Test%20Jellyfin%204K%20DV%20P5.mp4',
  },
];

// Fallback theme values for when context isn't ready
const fallbackTheme = {
  colors: {
    background: { base: '#0b0b0f', elevated: '#1a1a1f', surface: '#141418' },
    text: { primary: '#ffffff', secondary: '#a0a0a0', muted: '#666666', inverse: '#000000' },
    border: { subtle: '#2a2a2f' },
    brand: { primary: '#3f66ff' },
    status: { danger: '#ff4444' },
  },
  spacing: { xs: 4, sm: 8, md: 12, lg: 16, xl: 24 },
  radius: { sm: 4, md: 8, lg: 12 },
};

export default function NativePlayerDebugScreen() {
  const contextTheme = useTheme();
  // Use fallback if theme context isn't ready (can happen during hot reload)
  const theme = contextTheme?.colors?.brand?.primary ? contextTheme : fallbackTheme as any;
  const styles = useMemo(() => createStyles(theme), [theme]);

  const playerRef = useRef<NativePlayerRef>(null);
  const [url, setUrl] = useState('');
  const [isPlaying, setIsPlaying] = useState(false);
  const [paused, setPaused] = useState(true);
  const [isBuffering, setIsBuffering] = useState(false);
  const [currentTime, setCurrentTime] = useState(0);
  const [duration, setDuration] = useState(0);
  const [audioTracks, setAudioTracks] = useState<Track[]>([]);
  const [subtitleTracks, setSubtitleTracks] = useState<Track[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [logs, setLogs] = useState<string[]>([]);
  const [showControls, setShowControls] = useState(true);
  const [isFullscreen, setIsFullscreen] = useState(false);
  const [hdrHint, setHdrHint] = useState<'HDR10' | 'DolbyVision' | 'HLG' | undefined>(undefined);
  const controlsTimeoutRef = useRef<NodeJS.Timeout | null>(null);

  // Auto-hide controls after 4 seconds
  const resetControlsTimeout = useCallback(() => {
    if (controlsTimeoutRef.current) {
      clearTimeout(controlsTimeoutRef.current);
    }
    controlsTimeoutRef.current = setTimeout(() => {
      setShowControls(false);
    }, 4000);
  }, []);

  const toggleFullscreen = useCallback(() => {
    setIsFullscreen(prev => !prev);
    resetControlsTimeout();
  }, [resetControlsTimeout]);

  const toggleControls = useCallback(() => {
    setShowControls(prev => {
      if (!prev) {
        resetControlsTimeout();
      }
      return !prev;
    });
  }, [resetControlsTimeout]);

  // Reset timeout when controls are shown
  useEffect(() => {
    if (showControls && isPlaying) {
      resetControlsTimeout();
    }
    return () => {
      if (controlsTimeoutRef.current) {
        clearTimeout(controlsTimeoutRef.current);
      }
    };
  }, [showControls, isPlaying, resetControlsTimeout]);

  const addLog = useCallback((message: string) => {
    const timestamp = new Date().toLocaleTimeString();
    setLogs(prev => [`[${timestamp}] ${message}`, ...prev.slice(0, 49)]);
  }, []);

  const handleLaunch = useCallback(() => {
    if (!url.trim()) {
      addLog('Error: Please enter a URL');
      return;
    }

    addLog(`Launching player with URL: ${url}${hdrHint ? ` (HDR hint: ${hdrHint})` : ''}`);
    setIsPlaying(true);
    setPaused(false);
    setError(null);
    setCurrentTime(0);
    setDuration(0);
    setAudioTracks([]);
    setSubtitleTracks([]);
  }, [url, hdrHint, addLog]);

  const handleStop = useCallback(() => {
    addLog('Stopping playback');
    setIsPlaying(false);
    setPaused(true);
  }, [addLog]);

  const handleLoad = useCallback((data: { duration: number; width: number; height: number }) => {
    addLog(`Loaded: duration=${data.duration.toFixed(1)}s, size=${data.width}x${data.height}`);
    setDuration(data.duration);
    setIsBuffering(false);
  }, [addLog]);

  const handleProgress = useCallback((data: { currentTime: number; duration: number }) => {
    if (!data || typeof data.currentTime !== 'number') {
      return;
    }
    setCurrentTime(data.currentTime);
    if (data.duration > 0) {
      setDuration(data.duration);
    }
  }, []);

  const handleEnd = useCallback(() => {
    addLog('Playback ended');
    setPaused(true);
  }, [addLog]);

  const handleError = useCallback((err: { error: string }) => {
    addLog(`Error: ${err.error}`);
    setError(err.error);
  }, [addLog]);

  const handleTracksChanged = useCallback((data: { audioTracks: Track[]; subtitleTracks: Track[] }) => {
    addLog(`Tracks: ${data.audioTracks.length} audio, ${data.subtitleTracks.length} subtitle`);
    setAudioTracks(data.audioTracks);
    setSubtitleTracks(data.subtitleTracks);
  }, [addLog]);

  const handleBuffering = useCallback((buffering: boolean) => {
    if (buffering) {
      addLog('Buffering...');
    }
    setIsBuffering(buffering);
  }, [addLog]);

  const handleSeek = useCallback((time: number) => {
    addLog(`Seeking to ${time.toFixed(1)}s`);
    playerRef.current?.seek(time);
  }, [addLog]);

  const togglePlayPause = useCallback(() => {
    setPaused(prev => {
      addLog(prev ? 'Play' : 'Pause');
      return !prev;
    });
  }, [addLog]);

  const formatTime = (seconds: number): string => {
    const mins = Math.floor(seconds / 60);
    const secs = Math.floor(seconds % 60);
    return `${mins}:${secs.toString().padStart(2, '0')}`;
  };

  const platformLabel = Platform.OS === 'android' ? 'MPV' : Platform.OS === 'ios' ? 'KSPlayer' : 'N/A';

  const ContainerComponent = isFullscreen && isPlaying ? View : FixedSafeAreaView;

  return (
    <>
      <Stack.Screen options={{ headerShown: false }} />
      <StatusBar hidden={isFullscreen && isPlaying} />
      <SpatialNavigationRoot isActive={Platform.isTV}>
        <ContainerComponent style={[styles.safeArea, isFullscreen && isPlaying && styles.fullscreenContainer]}>
          <View style={[styles.container, isFullscreen && isPlaying && styles.fullscreenInner]}>
            {/* Header - hidden in fullscreen */}
            {!(isFullscreen && isPlaying) && (
              <View style={styles.header}>
                <View style={styles.headerLeft}>
                  <FocusablePressable
                    text="Back"
                    onSelect={() => router.back()}
                    style={styles.backButton}
                  />
                </View>
                <View style={styles.headerCenter}>
                  <Text style={styles.title}>Native Player Debug</Text>
                  <Text style={styles.subtitle}>
                    Testing {platformLabel} on {Platform.OS}
                    {Platform.isTV ? ' (TV)' : ''}
                  </Text>
                </View>
                <View style={styles.headerRight} />
              </View>
            )}

            {!isPlaying ? (
              /* URL Input Screen */
              <ScrollView style={styles.inputContainer} contentContainerStyle={styles.inputContent}>
                <View style={styles.urlSection}>
                  <Text style={styles.sectionTitle}>Stream URL</Text>
                  <TextInput
                    style={styles.urlInput}
                    value={url}
                    onChangeText={setUrl}
                    placeholder="Enter video URL (HLS, MP4, MKV, etc.)"
                    placeholderTextColor={theme.colors.text.muted}
                    autoCapitalize="none"
                    autoCorrect={false}
                    keyboardType="url"
                  />
                  <Text style={styles.sectionTitle}>HDR Hint (pre-configure renderer)</Text>
                  <View style={styles.hdrHintRow}>
                    <FocusablePressable
                      text="Auto"
                      onSelect={() => setHdrHint(undefined)}
                      style={[styles.hdrHintButton, !hdrHint && styles.hdrHintButtonActive]}
                    />
                    <FocusablePressable
                      text="HDR10"
                      onSelect={() => setHdrHint('HDR10')}
                      style={[styles.hdrHintButton, hdrHint === 'HDR10' && styles.hdrHintButtonActive]}
                    />
                    <FocusablePressable
                      text="Dolby Vision"
                      onSelect={() => setHdrHint('DolbyVision')}
                      style={[styles.hdrHintButton, hdrHint === 'DolbyVision' && styles.hdrHintButtonActive]}
                    />
                    <FocusablePressable
                      text="HLG"
                      onSelect={() => setHdrHint('HLG')}
                      style={[styles.hdrHintButton, hdrHint === 'HLG' && styles.hdrHintButtonActive]}
                    />
                  </View>
                  <DefaultFocus>
                    <FocusablePressable
                      text="Launch Player"
                      onSelect={handleLaunch}
                      style={styles.launchButton}
                      textStyle={styles.launchButtonText}
                    />
                  </DefaultFocus>
                </View>

                <View style={styles.samplesSection}>
                  <Text style={styles.sectionTitle}>Sample Streams</Text>
                  {SAMPLE_URLS.map((sample, index) => (
                    <FocusablePressable
                      key={index}
                      text={sample.label}
                      onSelect={() => setUrl(sample.url)}
                      style={styles.sampleButton}
                    />
                  ))}
                </View>

                <View style={styles.logsSection}>
                  <Text style={styles.sectionTitle}>Debug Logs</Text>
                  <ScrollView style={styles.logsContainer}>
                    {logs.length === 0 ? (
                      <Text style={styles.logPlaceholder}>No logs yet. Launch a stream to see activity.</Text>
                    ) : (
                      logs.map((log, index) => (
                        <Text key={index} style={styles.logEntry}>{log}</Text>
                      ))
                    )}
                  </ScrollView>
                </View>
              </ScrollView>
            ) : (
              /* Player Screen - Full screen with overlay controls */
              <Pressable style={styles.playerContainer} onPress={toggleControls}>
                <View style={[styles.playerWrapper, isFullscreen && styles.fullscreenPlayer]}>
                  <NativePlayer
                    ref={playerRef}
                    source={{ uri: url, hdrHint }}
                    paused={paused}
                    volume={1}
                    rate={1}
                    style={styles.player}
                    onLoad={handleLoad}
                    onProgress={handleProgress}
                    onEnd={handleEnd}
                    onError={handleError}
                    onTracksChanged={handleTracksChanged}
                    onBuffering={handleBuffering}
                  />

                  {isBuffering && (
                    <View style={styles.bufferingOverlay}>
                      <LoadingIndicator />
                    </View>
                  )}

                  {error && (
                    <View style={styles.errorOverlay}>
                      <Ionicons name="alert-circle" size={48} color={theme.colors.status.danger} />
                      <Text style={styles.errorText}>{error}</Text>
                    </View>
                  )}

                  {/* Overlay Controls */}
                  {showControls && (
                    <View style={styles.controlsOverlay}>
                      {/* Top bar with back button */}
                      <View style={styles.overlayTop}>
                        <FocusablePressable
                          text="Back"
                          onSelect={handleStop}
                          style={styles.overlayBackButton}
                        />
                        <View style={styles.overlayTopRight}>
                          <Text style={styles.overlayTrackLabel}>
                            Audio: {audioTracks.length} | Subs: {subtitleTracks.length}
                          </Text>
                          <Pressable onPress={toggleFullscreen} style={styles.fullscreenButton}>
                            <Ionicons
                              name={isFullscreen ? 'contract' : 'expand'}
                              size={24}
                              color="#fff"
                            />
                          </Pressable>
                        </View>
                      </View>

                      {/* Center play/pause */}
                      <View style={styles.overlayCenter}>
                        <Pressable onPress={togglePlayPause} style={styles.centerPlayButton}>
                          <Ionicons
                            name={paused ? 'play' : 'pause'}
                            size={64}
                            color="#fff"
                          />
                        </Pressable>
                      </View>

                      {/* Bottom controls */}
                      <View style={styles.overlayBottom}>
                        <View style={styles.progressContainer}>
                          <Text style={styles.overlayTimeText}>{formatTime(currentTime)}</Text>
                          <View style={styles.progressBar}>
                            <View
                              style={[
                                styles.progressFill,
                                { width: `${duration > 0 ? (currentTime / duration) * 100 : 0}%` }
                              ]}
                            />
                          </View>
                          <Text style={styles.overlayTimeText}>{formatTime(duration)}</Text>
                        </View>

                        <View style={styles.controlsRow}>
                          <FocusablePressable
                            text="-30s"
                            onSelect={() => {
                              handleSeek(Math.max(0, currentTime - 30));
                              resetControlsTimeout();
                            }}
                            style={styles.overlayButton}
                          />
                          <FocusablePressable
                            text={paused ? 'Play' : 'Pause'}
                            onSelect={() => {
                              togglePlayPause();
                              resetControlsTimeout();
                            }}
                            style={styles.overlayButton}
                          />
                          <FocusablePressable
                            text="+30s"
                            onSelect={() => {
                              handleSeek(currentTime + 30);
                              resetControlsTimeout();
                            }}
                            style={styles.overlayButton}
                          />
                        </View>
                      </View>
                    </View>
                  )}
                </View>

                {/* Logs - shown below player when controls visible */}
                {showControls && (
                  <View style={styles.playerLogsSection}>
                    <ScrollView style={styles.playerLogsContainer} horizontal={false}>
                      {logs.slice(0, 5).map((log, index) => (
                        <Text key={index} style={styles.logEntry}>{log}</Text>
                      ))}
                    </ScrollView>
                  </View>
                )}
              </Pressable>
            )}
          </View>
        </ContainerComponent>
      </SpatialNavigationRoot>
    </>
  );
}

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    fullscreenContainer: {
      backgroundColor: '#000',
    },
    fullscreenInner: {
      padding: 0,
    },
    container: {
      flex: 1,
      padding: theme.spacing.lg,
    },
    header: {
      flexDirection: 'row',
      alignItems: 'center',
      marginBottom: theme.spacing.lg,
    },
    headerLeft: {
      width: 100,
    },
    headerCenter: {
      flex: 1,
      alignItems: 'center',
    },
    headerRight: {
      width: 100,
    },
    backButton: {
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.sm,
    },
    title: {
      fontSize: 24,
      fontWeight: '700',
      color: theme.colors.text.primary,
    },
    subtitle: {
      fontSize: 14,
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.xs,
    },
    inputContainer: {
      flex: 1,
    },
    inputContent: {
      gap: theme.spacing.xl,
    },
    urlSection: {
      gap: theme.spacing.md,
    },
    sectionTitle: {
      fontSize: 18,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    urlInput: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.md,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      padding: theme.spacing.md,
      fontSize: 16,
      color: theme.colors.text.primary,
    },
    launchButton: {
      backgroundColor: theme.colors.brand.primary,
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.xl,
      borderRadius: theme.radius.md,
      alignItems: 'center',
    },
    launchButtonText: {
      color: theme.colors.text.inverse,
      fontWeight: '600',
      fontSize: 16,
    },
    hdrHintRow: {
      flexDirection: 'row',
      gap: theme.spacing.sm,
      flexWrap: 'wrap',
    },
    hdrHintButton: {
      backgroundColor: theme.colors.background.surface,
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.md,
      borderRadius: theme.radius.sm,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
    },
    hdrHintButtonActive: {
      backgroundColor: theme.colors.brand.primary,
      borderColor: theme.colors.brand.primary,
    },
    samplesSection: {
      gap: theme.spacing.sm,
    },
    sampleButton: {
      backgroundColor: theme.colors.background.surface,
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.md,
      borderRadius: theme.radius.sm,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
    },
    logsSection: {
      flex: 1,
      minHeight: 200,
      gap: theme.spacing.sm,
    },
    logsContainer: {
      flex: 1,
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.md,
      padding: theme.spacing.md,
    },
    logPlaceholder: {
      color: theme.colors.text.muted,
      fontStyle: 'italic',
    },
    logEntry: {
      color: theme.colors.text.secondary,
      fontSize: 12,
      fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
      marginBottom: 4,
    },
    playerContainer: {
      flex: 1,
    },
    playerWrapper: {
      flex: 1,
      backgroundColor: '#000',
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
    },
    fullscreenPlayer: {
      borderRadius: 0,
    },
    player: {
      flex: 1,
    },
    bufferingOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
    },
    errorOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      gap: theme.spacing.md,
    },
    errorText: {
      color: theme.colors.status.danger,
      fontSize: 16,
      textAlign: 'center',
      paddingHorizontal: theme.spacing.lg,
    },
    controlsOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'space-between',
      backgroundColor: 'rgba(0, 0, 0, 0.4)',
    },
    overlayTop: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      padding: theme.spacing.md,
      paddingTop: theme.spacing.lg,
    },
    overlayBackButton: {
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.md,
      borderRadius: theme.radius.sm,
    },
    overlayTopRight: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
    },
    fullscreenButton: {
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
      padding: theme.spacing.sm,
      borderRadius: theme.radius.sm,
    },
    overlayTrackLabel: {
      color: '#fff',
      fontSize: 12,
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
      paddingVertical: theme.spacing.xs,
      paddingHorizontal: theme.spacing.sm,
      borderRadius: theme.radius.sm,
    },
    overlayCenter: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
    },
    centerPlayButton: {
      width: 100,
      height: 100,
      borderRadius: 50,
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
      justifyContent: 'center',
      alignItems: 'center',
    },
    overlayBottom: {
      padding: theme.spacing.md,
      paddingBottom: theme.spacing.lg,
      gap: theme.spacing.md,
    },
    overlayButton: {
      backgroundColor: 'rgba(255, 255, 255, 0.2)',
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.md,
      borderRadius: theme.radius.sm,
      minWidth: 70,
      alignItems: 'center',
    },
    overlayTimeText: {
      color: '#fff',
      fontSize: 12,
      fontVariant: ['tabular-nums'],
      minWidth: 45,
    },
    controlsContainer: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      padding: theme.spacing.md,
      gap: theme.spacing.md,
    },
    controlsRow: {
      flexDirection: 'row',
      justifyContent: 'center',
      gap: theme.spacing.sm,
    },
    controlButton: {
      backgroundColor: theme.colors.background.elevated,
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.md,
      borderRadius: theme.radius.sm,
      minWidth: 80,
      alignItems: 'center',
    },
    progressContainer: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
    },
    timeText: {
      color: theme.colors.text.secondary,
      fontSize: 12,
      fontVariant: ['tabular-nums'],
      minWidth: 45,
    },
    progressBar: {
      flex: 1,
      height: 6,
      backgroundColor: theme.colors.background.elevated,
      borderRadius: 3,
      overflow: 'hidden',
    },
    progressFill: {
      height: '100%',
      backgroundColor: theme.colors.brand.primary,
    },
    trackInfo: {
      gap: theme.spacing.xs,
    },
    trackLabel: {
      color: theme.colors.text.muted,
      fontSize: 12,
    },
    playerLogsSection: {
      maxHeight: 80,
      marginTop: theme.spacing.sm,
    },
    playerLogsContainer: {
      backgroundColor: 'rgba(0, 0, 0, 0.7)',
      borderRadius: theme.radius.sm,
      padding: theme.spacing.sm,
    },
  });
