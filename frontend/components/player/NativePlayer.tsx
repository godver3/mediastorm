import React, { forwardRef, useImperativeHandle, useRef, useCallback } from 'react';
import { Platform, View, StyleProp, ViewStyle, Text, StyleSheet } from 'react-native';

// Load pip-manager for Android programmatic PiP entry
let pipEnterPip: (() => boolean) | null = null;
if (Platform.OS === 'android' && !Platform.isTV) {
  try {
    const pipManager = require('pip-manager');
    pipEnterPip = pipManager.enterPip;
  } catch {
    // pip-manager not available
  }
}

// Types for track information
export interface Track {
  id: number;
  type: string;
  title: string;
  language: string;
  codec: string;
  selected: boolean;
}

export interface LoadEvent {
  duration: number;
  width: number;
  height: number;
}

export interface ProgressEvent {
  currentTime: number;
  duration: number;
}

export interface TracksEvent {
  audioTracks: Track[];
  subtitleTracks: Track[];
}

export interface ErrorEvent {
  error: string;
}

export interface BufferingEvent {
  buffering: boolean;
}

export interface NativePlayerSource {
  uri: string;
  headers?: Record<string, string>;
}

// Subtitle styling configuration for native players (KSPlayer/MPV)
export interface SubtitleStyle {
  // Font size multiplier (1.0 = default, 1.5 = 150% size)
  fontSize?: number;
  // Text color as hex string (e.g., '#FFFFFF')
  textColor?: string;
  // Background color as hex string with alpha (e.g., '#00000080' for 50% black)
  backgroundColor?: string;
  // Bottom margin in points (distance from bottom of video)
  bottomMargin?: number;
}

export interface NativePlayerProps {
  source?: NativePlayerSource;
  paused?: boolean;
  volume?: number;
  rate?: number;
  audioTrack?: number;
  subtitleTrack?: number;
  // Subtitle styling (font size, color, background, position)
  subtitleStyle?: SubtitleStyle;
  // When true, subtitles move up to avoid being hidden by controls
  controlsVisible?: boolean;
  style?: StyleProp<ViewStyle>;
  onLoad?: (data: LoadEvent) => void;
  onProgress?: (data: ProgressEvent) => void;
  onEnd?: () => void;
  onError?: (error: ErrorEvent) => void;
  onTracksChanged?: (data: TracksEvent) => void;
  onBuffering?: (buffering: boolean) => void;
  onPipStatusChanged?: (isActive: boolean, paused?: boolean) => void;
}

export interface NativePlayerRef {
  seek: (time: number) => void;
  setAudioTrack: (trackId: number) => void;
  setSubtitleTrack: (trackId: number) => void;
  enterPip: (forBackground?: boolean) => void;
}

// Lazy load platform-specific players
let MpvPlayer: any = null;
let KSPlayer: any = null;

// Try to load MPV player for Android
if (Platform.OS === 'android') {
  try {
    MpvPlayer = require('mpv-player').MpvPlayer;
    console.log('[NativePlayer] MPV player loaded successfully');
  } catch (e) {
    console.log('[NativePlayer] MPV player not available:', e);
  }
}

// Try to load KSPlayer for iOS and tvOS
if (Platform.OS === 'ios' || (Platform.OS as string) === 'tvos') {
  try {
    KSPlayer = require('ksplayer').KSPlayer;
    console.log('[NativePlayer] KSPlayer loaded successfully for', Platform.OS);
  } catch (e) {
    console.log('[NativePlayer] KSPlayer not available:', e);
  }
}

export const NativePlayer = forwardRef<NativePlayerRef, NativePlayerProps>((props, ref) => {
  const {
    source,
    paused = true,
    volume = 1,
    rate = 1,
    audioTrack,
    subtitleTrack,
    subtitleStyle,
    controlsVisible,
    style,
    onLoad,
    onProgress,
    onEnd,
    onError,
    onTracksChanged,
    onBuffering,
    onPipStatusChanged,
  } = props;

  const playerRef = useRef<any>(null);
  const dvLogShown = useRef(false);

  // Forward native debug logs to Metro console (for DV color debugging)
  const handleDebugLog = useCallback((data: { message: string }) => {
    // Only log DV-related messages and deduplicate (fires per frame)
    if (data.message?.includes('[KSPlayer-DV]') && !dvLogShown.current) {
      console.log('[NativePlayer]', data.message);
      dvLogShown.current = true;
    }
  }, []);

  useImperativeHandle(ref, () => ({
    seek: (time: number) => {
      playerRef.current?.seek(time);
    },
    setAudioTrack: (trackId: number) => {
      playerRef.current?.setAudioTrack(trackId);
    },
    setSubtitleTrack: (trackId: number) => {
      playerRef.current?.setSubtitleTrack(trackId);
    },
    enterPip: (forBackground?: boolean) => {
      if (Platform.OS === 'android' && pipEnterPip) {
        // On Android, PiP is Activity-level — use pip-manager module
        pipEnterPip();
      } else {
        // On iOS, delegate to the native player view (KSPlayer)
        playerRef.current?.enterPip(forBackground);
      }
    },
  }));

  // Event handlers - KSPlayer/MpvPlayer already extract nativeEvent, so we pass through directly
  const handleLoad = useCallback((data: LoadEvent) => {
    console.log('[NativePlayer] onLoad:', data);
    onLoad?.(data);
  }, [onLoad]);

  const handleProgress = useCallback((data: ProgressEvent) => {
    onProgress?.(data);
  }, [onProgress]);

  const handleEnd = useCallback(() => {
    console.log('[NativePlayer] onEnd');
    onEnd?.();
  }, [onEnd]);

  const handleError = useCallback((data: ErrorEvent) => {
    console.error('[NativePlayer] onError:', data);
    onError?.(data);
  }, [onError]);

  const handleTracksChanged = useCallback((data: TracksEvent) => {
    console.log('[NativePlayer] onTracksChanged:', data);
    onTracksChanged?.(data);
  }, [onTracksChanged]);

  const handleBuffering = useCallback((buffering: boolean) => {
    onBuffering?.(buffering);
  }, [onBuffering]);

  // Select player based on platform
  // iOS and tvOS use KSPlayer, Android uses MPV
  const PlayerComponent = Platform.OS === 'android' ? MpvPlayer : KSPlayer;

  if (!PlayerComponent) {
    // Fallback for platforms without native player
    return (
      <View style={[styles.fallback, style]}>
        <Text style={styles.fallbackText}>
          Native player not available on this platform
        </Text>
        <Text style={styles.fallbackSubtext}>
          {Platform.OS === 'android' ? 'MPV player module not loaded' :
           (Platform.OS === 'ios' || (Platform.OS as string) === 'tvos') ? 'KSPlayer module not loaded' :
           `Unsupported platform: ${Platform.OS}`}
        </Text>
      </View>
    );
  }

  return (
    <PlayerComponent
      ref={playerRef}
      source={source}
      paused={paused}
      volume={volume}
      rate={rate}
      audioTrack={audioTrack}
      subtitleTrack={subtitleTrack}
      subtitleStyle={subtitleStyle}
      controlsVisible={controlsVisible}
      style={[styles.player, style]}
      onLoad={handleLoad}
      onProgress={handleProgress}
      onEnd={handleEnd}
      onError={handleError}
      onTracksChanged={handleTracksChanged}
      onBuffering={handleBuffering}
      onDebugLog={handleDebugLog}
      onPipStatusChanged={onPipStatusChanged}
    />
  );
});

NativePlayer.displayName = 'NativePlayer';

const styles = StyleSheet.create({
  player: {
    flex: 1,
  },
  fallback: {
    flex: 1,
    backgroundColor: '#000',
    justifyContent: 'center',
    alignItems: 'center',
    padding: 20,
  },
  fallbackText: {
    color: '#fff',
    fontSize: 18,
    textAlign: 'center',
    marginBottom: 10,
  },
  fallbackSubtext: {
    color: '#888',
    fontSize: 14,
    textAlign: 'center',
  },
});

export default NativePlayer;
