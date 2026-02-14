import React, { forwardRef, useImperativeHandle, useRef, useCallback } from 'react';
import {
  requireNativeComponent,
  Platform,
  UIManager,
  findNodeHandle,
  NativeModules,
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
  style?: StyleProp<ViewStyle>;
  onLoad?: (data: LoadEvent) => void;
  onProgress?: (data: ProgressEvent) => void;
  onEnd?: () => void;
  onError?: (error: ErrorEvent) => void;
  onTracksChanged?: (data: TracksEvent) => void;
  onBuffering?: (buffering: boolean) => void;
  onDebugLog?: (data: DebugLogEvent) => void;
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
  style?: StyleProp<ViewStyle>;
  onLoad?: (event: NativeSyntheticEvent<LoadEvent>) => void;
  onProgress?: (event: NativeSyntheticEvent<ProgressEvent>) => void;
  onEnd?: (event: NativeSyntheticEvent<{ ended: boolean }>) => void;
  onError?: (event: NativeSyntheticEvent<ErrorEvent>) => void;
  onTracksChanged?: (event: NativeSyntheticEvent<TracksEvent>) => void;
  onBuffering?: (event: NativeSyntheticEvent<BufferingEvent>) => void;
  onDebugLog?: (event: NativeSyntheticEvent<DebugLogEvent>) => void;
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

// Get the view manager for imperative commands
const MpvPlayerViewManager =
  Platform.OS === 'android' ? UIManager.getViewManagerConfig('MpvPlayer') : null;

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
    style,
    onLoad,
    onProgress,
    onEnd,
    onError,
    onTracksChanged,
    onBuffering,
    onDebugLog,
  } = props;

  const nativeRef = useRef<any>(null);

  useImperativeHandle(ref, () => ({
    seek: (time: number) => {
      const handle = findNodeHandle(nativeRef.current);
      if (handle && MpvPlayerViewManager?.Commands) {
        UIManager.dispatchViewManagerCommand(
          handle,
          MpvPlayerViewManager.Commands.seek,
          [time]
        );
      }
    },
    setAudioTrack: (trackId: number) => {
      const handle = findNodeHandle(nativeRef.current);
      if (handle && MpvPlayerViewManager?.Commands) {
        UIManager.dispatchViewManagerCommand(
          handle,
          MpvPlayerViewManager.Commands.setAudioTrack,
          [trackId]
        );
      }
    },
    setSubtitleTrack: (trackId: number) => {
      const handle = findNodeHandle(nativeRef.current);
      if (handle && MpvPlayerViewManager?.Commands) {
        UIManager.dispatchViewManagerCommand(
          handle,
          MpvPlayerViewManager.Commands.setSubtitleTrack,
          [trackId]
        );
      }
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
      style={style}
      onLoad={handleLoad}
      onProgress={handleProgress}
      onEnd={handleEnd}
      onError={handleError}
      onTracksChanged={handleTracksChanged}
      onBuffering={handleBuffering}
      onDebugLog={handleDebugLog}
    />
  );
});

MpvPlayer.displayName = 'MpvPlayer';

// === Native PlayerActivity launcher (Android TV only) ===

export interface PlayerLaunchParams {
  streamUrl: string;
  title: string;
  authToken: string;
  userId: string;
  mediaType: string;
  itemId: string;
  backendUrl: string;
  startOffset?: number;
  durationHint?: number;
  preselectedAudioTrack?: number;
  preselectedSubtitleTrack?: number;
  seasonNumber?: number;
  episodeNumber?: number;
  seriesId?: string;
  seriesName?: string;
  episodeName?: string;
  titleId?: string;
  imdbId?: string;
  tvdbId?: string;
  isHDR?: boolean;
  isDolbyVision?: boolean;
}

export interface PlayerResult {
  lastPosition: number;
  completed: boolean;
  percentWatched: number;
}

/**
 * Launch the native PlayerActivity (Android TV only).
 * Returns playback result when the Activity finishes.
 */
export async function launchPlayer(params: PlayerLaunchParams): Promise<PlayerResult> {
  const { PlayerLauncherModule } = NativeModules;
  if (!PlayerLauncherModule) {
    throw new Error('PlayerLauncherModule is not available');
  }
  return PlayerLauncherModule.launch(params);
}

export default MpvPlayer;
