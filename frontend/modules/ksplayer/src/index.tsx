import React, { forwardRef, useImperativeHandle, useRef, useCallback } from 'react';
import {
  requireNativeComponent,
  Platform,
  UIManager,
  findNodeHandle,
  NativeSyntheticEvent,
  StyleProp,
  ViewStyle,
} from 'react-native';

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

export interface VideoInfoEvent {
  frameRate: number;
  dynamicRange: 'SDR' | 'HDR10' | 'HLG' | 'DolbyVision';
  codec: string;
  hdrActive: boolean;
}

export interface DebugLogEvent {
  message: string;
}

export interface KSPlayerSource {
  uri: string;
  headers?: Record<string, string>;
}

// Subtitle styling configuration
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

export interface KSPlayerProps {
  source?: KSPlayerSource;
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
  onVideoInfo?: (data: VideoInfoEvent) => void;
  onDebugLog?: (data: DebugLogEvent) => void;
}

export interface KSPlayerRef {
  seek: (time: number) => void;
  setAudioTrack: (trackId: number) => void;
  setSubtitleTrack: (trackId: number) => void;
  getTracks: () => Promise<TracksEvent>;
}

// Native component interface
interface NativeKSPlayerProps {
  source?: KSPlayerSource;
  paused?: boolean;
  volume?: number;
  rate?: number;
  audioTrack?: number;
  subtitleTrack?: number;
  subtitleStyle?: SubtitleStyle;
  controlsVisible?: boolean;
  style?: StyleProp<ViewStyle>;
  onLoad?: (event: NativeSyntheticEvent<LoadEvent>) => void;
  onProgress?: (event: NativeSyntheticEvent<ProgressEvent>) => void;
  onEnd?: (event: NativeSyntheticEvent<{ ended: boolean }>) => void;
  onError?: (event: NativeSyntheticEvent<ErrorEvent>) => void;
  onTracksChanged?: (event: NativeSyntheticEvent<TracksEvent>) => void;
  onBuffering?: (event: NativeSyntheticEvent<BufferingEvent>) => void;
  onVideoInfo?: (event: NativeSyntheticEvent<VideoInfoEvent>) => void;
  onDebugLog?: (event: NativeSyntheticEvent<DebugLogEvent>) => void;
}

// Only load native component on iOS - cache to prevent double registration on hot reload
let NativeKSPlayerView: ReturnType<typeof requireNativeComponent<NativeKSPlayerProps>> | null = null;

if (Platform.OS === 'ios') {
  try {
    NativeKSPlayerView = requireNativeComponent<NativeKSPlayerProps>('KSPlayerView');
  } catch (e) {
    // Already registered (hot reload)
    console.log('[KSPlayer] Using cached native component');
  }
}

// Get the view manager for imperative commands
const KSPlayerViewManager =
  Platform.OS === 'ios' ? UIManager.getViewManagerConfig('KSPlayerView') : null;

export const KSPlayer = forwardRef<KSPlayerRef, KSPlayerProps>((props, ref) => {
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
    onVideoInfo,
    onDebugLog,
  } = props;

  const nativeRef = useRef<any>(null);

  useImperativeHandle(ref, () => ({
    seek: (time: number) => {
      const handle = findNodeHandle(nativeRef.current);
      if (handle && KSPlayerViewManager) {
        UIManager.dispatchViewManagerCommand(
          handle,
          UIManager.getViewManagerConfig('KSPlayerView').Commands.seek,
          [time]
        );
      }
    },
    setAudioTrack: (trackId: number) => {
      const handle = findNodeHandle(nativeRef.current);
      if (handle && KSPlayerViewManager) {
        UIManager.dispatchViewManagerCommand(
          handle,
          UIManager.getViewManagerConfig('KSPlayerView').Commands.setAudioTrack,
          [trackId]
        );
      }
    },
    setSubtitleTrack: (trackId: number) => {
      const handle = findNodeHandle(nativeRef.current);
      if (handle && KSPlayerViewManager) {
        UIManager.dispatchViewManagerCommand(
          handle,
          UIManager.getViewManagerConfig('KSPlayerView').Commands.setSubtitleTrack,
          [trackId]
        );
      }
    },
    getTracks: (): Promise<TracksEvent> => {
      return new Promise((resolve, reject) => {
        const handle = findNodeHandle(nativeRef.current);
        if (handle && KSPlayerViewManager) {
          UIManager.dispatchViewManagerCommand(
            handle,
            UIManager.getViewManagerConfig('KSPlayerView').Commands.getTracks,
            []
          );
          // Note: For proper promise support, we'd need NativeModules
          // For now, use onTracksChanged callback
          reject(new Error('Use onTracksChanged callback instead'));
        } else {
          reject(new Error('KSPlayer not available'));
        }
      });
    },
  }));

  // Event handlers that extract data from native events
  const handleLoad = useCallback(
    (event: NativeSyntheticEvent<LoadEvent>) => {
      console.log('[KSPlayer] onLoad:', event.nativeEvent);
      onLoad?.(event.nativeEvent);
    },
    [onLoad]
  );

  const handleProgress = useCallback(
    (event: NativeSyntheticEvent<ProgressEvent>) => {
      // Debug: log the raw event structure
      if (!event?.nativeEvent) {
        console.log('[KSPlayer] onProgress received invalid event:', JSON.stringify(event));
        return;
      }
      const data = event.nativeEvent;
      if (typeof data.currentTime === 'number') {
        onProgress?.(data);
      }
    },
    [onProgress]
  );

  const handleEnd = useCallback(
    (event: NativeSyntheticEvent<{ ended: boolean }>) => {
      console.log('[KSPlayer] onEnd');
      onEnd?.();
    },
    [onEnd]
  );

  const handleError = useCallback(
    (event: NativeSyntheticEvent<ErrorEvent>) => {
      console.error('[KSPlayer] onError:', event.nativeEvent);
      onError?.(event.nativeEvent);
    },
    [onError]
  );

  const handleTracksChanged = useCallback(
    (event: NativeSyntheticEvent<TracksEvent>) => {
      console.log('[KSPlayer] onTracksChanged:', event.nativeEvent);
      onTracksChanged?.(event.nativeEvent);
    },
    [onTracksChanged]
  );

  const handleBuffering = useCallback(
    (event: NativeSyntheticEvent<BufferingEvent>) => {
      onBuffering?.(event.nativeEvent.buffering);
    },
    [onBuffering]
  );

  const handleVideoInfo = useCallback(
    (event: NativeSyntheticEvent<VideoInfoEvent>) => {
      console.log('[KSPlayer] onVideoInfo:', event.nativeEvent);
      onVideoInfo?.(event.nativeEvent);
    },
    [onVideoInfo]
  );

  const handleDebugLog = useCallback(
    (event: NativeSyntheticEvent<DebugLogEvent>) => {
      console.log('[KSPlayer:Swift]', event.nativeEvent.message);
      onDebugLog?.(event.nativeEvent);
    },
    [onDebugLog]
  );

  if (!NativeKSPlayerView) {
    console.log('[KSPlayer] Native view not available on this platform');
    return null;
  }

  return (
    <NativeKSPlayerView
      ref={nativeRef}
      source={source}
      paused={paused}
      volume={volume}
      rate={rate}
      audioTrack={audioTrack}
      subtitleTrack={subtitleTrack}
      subtitleStyle={subtitleStyle}
      controlsVisible={controlsVisible}
      style={style}
      onLoad={handleLoad}
      onProgress={handleProgress}
      onEnd={handleEnd}
      onError={handleError}
      onTracksChanged={handleTracksChanged}
      onBuffering={handleBuffering}
      onVideoInfo={handleVideoInfo}
      onDebugLog={handleDebugLog}
    />
  );
});

KSPlayer.displayName = 'KSPlayer';

export default KSPlayer;
