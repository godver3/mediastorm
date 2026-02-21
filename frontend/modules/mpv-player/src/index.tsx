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

export interface MpvPlayerSource {
  uri: string;
  headers?: Record<string, string>;
}

export interface DebugLogEvent {
  message: string;
}

export interface SubtitleStyle {
  fontSize?: number;
  textColor?: string;
  backgroundColor?: string;
  bottomMargin?: number;
}

export interface SubtitleTextEvent {
  text: string;
}

export interface MpvPlayerProps {
  source?: MpvPlayerSource;
  paused?: boolean;
  volume?: number;
  rate?: number;
  audioTrack?: number;
  subtitleTrack?: number;
  subtitleSize?: number;
  subtitleColor?: string;
  subtitlePosition?: number;
  subtitleStyle?: SubtitleStyle;
  controlsVisible?: boolean;
  externalSubtitleUrl?: string;
  isHDR?: boolean;
  style?: StyleProp<ViewStyle>;
  onLoad?: (data: LoadEvent) => void;
  onProgress?: (data: ProgressEvent) => void;
  onEnd?: () => void;
  onError?: (error: ErrorEvent) => void;
  onTracksChanged?: (data: TracksEvent) => void;
  onBuffering?: (buffering: boolean) => void;
  onDebugLog?: (data: DebugLogEvent) => void;
  onSubtitleText?: (data: SubtitleTextEvent) => void;
}

export interface MpvPlayerRef {
  seek: (time: number) => void;
  setAudioTrack: (trackId: number) => void;
  setSubtitleTrack: (trackId: number) => void;
}

// Native component interface
interface NativeMpvPlayerProps {
  source?: MpvPlayerSource;
  paused?: boolean;
  volume?: number;
  rate?: number;
  audioTrack?: number;
  subtitleTrack?: number;
  subtitleSize?: number;
  subtitleColor?: string;
  subtitlePosition?: number;
  subtitleStyle?: SubtitleStyle;
  controlsVisible?: boolean;
  externalSubtitleUrl?: string;
  isHDR?: boolean;
  style?: StyleProp<ViewStyle>;
  onLoad?: (event: NativeSyntheticEvent<LoadEvent>) => void;
  onProgress?: (event: NativeSyntheticEvent<ProgressEvent>) => void;
  onEnd?: (event: NativeSyntheticEvent<{ ended: boolean }>) => void;
  onError?: (event: NativeSyntheticEvent<ErrorEvent>) => void;
  onTracksChanged?: (event: NativeSyntheticEvent<TracksEvent>) => void;
  onBuffering?: (event: NativeSyntheticEvent<BufferingEvent>) => void;
  onDebugLog?: (event: NativeSyntheticEvent<DebugLogEvent>) => void;
  onSubtitleText?: (event: NativeSyntheticEvent<SubtitleTextEvent>) => void;
}

// Only load native component on Android - cache to prevent double registration on hot reload
let NativeMpvPlayerView: ReturnType<typeof requireNativeComponent<NativeMpvPlayerProps>> | null = null;

if (Platform.OS === 'android') {
  try {
    NativeMpvPlayerView = requireNativeComponent<NativeMpvPlayerProps>('MpvPlayer');
  } catch (e) {
    // Already registered (hot reload)
    console.log('[MpvPlayer] Using cached native component');
  }
}

// Helper to dispatch commands to the native view.
// Uses string command names for Fabric (new architecture) compatibility —
// UIManager.getViewManagerConfig() returns undefined under Fabric interop,
// but dispatchViewManagerCommand accepts string names that map to receiveCommand().
const dispatchCommand = (ref: any, command: string, args: any[]) => {
  const handle = findNodeHandle(ref);
  if (handle != null) {
    UIManager.dispatchViewManagerCommand(handle, command, args);
  }
};

export const MpvPlayer = forwardRef<MpvPlayerRef, MpvPlayerProps>((props, ref) => {
  const {
    source,
    paused = true,
    volume = 1,
    rate = 1,
    audioTrack,
    subtitleTrack,
    subtitleSize,
    subtitleColor,
    subtitlePosition,
    subtitleStyle,
    controlsVisible,
    externalSubtitleUrl,
    isHDR,
    style,
    onLoad,
    onProgress,
    onEnd,
    onError,
    onTracksChanged,
    onBuffering,
    onDebugLog,
    onSubtitleText,
  } = props;

  const nativeRef = useRef<any>(null);

  useImperativeHandle(ref, () => ({
    seek: (time: number) => {
      dispatchCommand(nativeRef.current, 'seek', [time]);
    },
    setAudioTrack: (trackId: number) => {
      dispatchCommand(nativeRef.current, 'setAudioTrack', [trackId]);
    },
    setSubtitleTrack: (trackId: number) => {
      dispatchCommand(nativeRef.current, 'setSubtitleTrack', [trackId]);
    },
  }));

  // Event handlers that extract data from native events
  const handleLoad = useCallback(
    (event: NativeSyntheticEvent<LoadEvent>) => {
      console.log('[MpvPlayer] onLoad:', event.nativeEvent);
      onLoad?.(event.nativeEvent);
    },
    [onLoad]
  );

  const handleProgress = useCallback(
    (event: NativeSyntheticEvent<ProgressEvent>) => {
      if (!event?.nativeEvent) {
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
      console.log('[MpvPlayer] onEnd');
      onEnd?.();
    },
    [onEnd]
  );

  const handleError = useCallback(
    (event: NativeSyntheticEvent<ErrorEvent>) => {
      console.error('[MpvPlayer] onError:', event.nativeEvent);
      onError?.(event.nativeEvent);
    },
    [onError]
  );

  const handleTracksChanged = useCallback(
    (event: NativeSyntheticEvent<TracksEvent>) => {
      console.log('[MpvPlayer] onTracksChanged:', event.nativeEvent);
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

  const handleDebugLog = useCallback(
    (event: NativeSyntheticEvent<DebugLogEvent>) => {
      console.log('[MpvPlayer]', event.nativeEvent.message);
      onDebugLog?.(event.nativeEvent);
    },
    [onDebugLog]
  );

  const handleSubtitleText = useCallback(
    (event: NativeSyntheticEvent<SubtitleTextEvent>) => {
      onSubtitleText?.(event.nativeEvent);
    },
    [onSubtitleText]
  );

  if (!NativeMpvPlayerView) {
    console.log('[MpvPlayer] Native view not available on this platform');
    return null;
  }

  return (
    <NativeMpvPlayerView
      ref={nativeRef}
      source={source}
      paused={paused}
      volume={volume}
      rate={rate}
      audioTrack={audioTrack}
      subtitleTrack={subtitleTrack}
      subtitleSize={subtitleSize}
      subtitleColor={subtitleColor}
      subtitlePosition={subtitlePosition}
      subtitleStyle={subtitleStyle}
      controlsVisible={controlsVisible}
      externalSubtitleUrl={externalSubtitleUrl}
      isHDR={isHDR}
      style={style}
      onLoad={handleLoad}
      onProgress={handleProgress}
      onEnd={handleEnd}
      onError={handleError}
      onTracksChanged={handleTracksChanged}
      onBuffering={handleBuffering}
      onDebugLog={handleDebugLog}
      onSubtitleText={handleSubtitleText}
    />
  );
});

MpvPlayer.displayName = 'MpvPlayer';

export default MpvPlayer;
