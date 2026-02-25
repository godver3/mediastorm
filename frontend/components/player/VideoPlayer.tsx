import type { CSSProperties } from 'react';
import React, { useCallback, useEffect, useImperativeHandle, useMemo, useRef } from 'react';
import { Platform, View, type ViewStyle, StyleSheet } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';

import { isMobileWeb } from './isMobileWeb';
import type { VideoImplementation, VideoPlayerHandle, VideoPlayerProps, VideoProgressMeta } from './types';
import { NativePlayer, type NativePlayerRef, type LoadEvent, type ProgressEvent, type ErrorEvent, type TracksEvent } from './NativePlayer';

type PlayerComponent = React.ForwardRefExoticComponent<VideoPlayerProps & React.RefAttributes<VideoPlayerHandle>>;

// Only load RNV player on native platforms
const loadRnvPlayer = (): PlayerComponent | null => {
  if (Platform.OS === 'web') {
    return null;
  }

  try {
    return require('./VideoPlayer.rnv').default as PlayerComponent;
  } catch (error) {
    console.warn('Warning: unable to load RNV player implementation', error);
    return null;
  }
};

const RnvVideoPlayer = loadRnvPlayer();

// Check if native player modules are available
const isNativePlayerAvailable = (): boolean => {
  if (Platform.OS === 'android') {
    try {
      require('mpv-player');
      return true;
    } catch {
      return false;
    }
  }
  if (Platform.OS === 'ios' || (Platform.OS as string) === 'tvos') {
    try {
      require('ksplayer');
      return true;
    } catch {
      return false;
    }
  }
  return false;
};

const nativePlayerAvailable = Platform.OS !== 'web' && isNativePlayerAvailable();

export type {
  NowPlayingMetadata,
  TrackInfo,
  VideoImplementation,
  VideoPlayerHandle,
  VideoPlayerProps,
  VideoProgressMeta,
} from './types';

/**
 * NativePlayerAdapter - Adapts VideoPlayerProps to NativePlayerProps
 * Used to integrate NativePlayer (KSPlayer/MPV) with the existing player interface
 */
const NativePlayerAdapter = React.forwardRef<VideoPlayerHandle, VideoPlayerProps>((props, ref) => {
  const {
    movie,
    paused,
    onBuffer,
    onProgress,
    onLoad,
    onEnd,
    onError,
    durationHint,
    volume = 1,
    selectedAudioTrackIndex,
    selectedSubtitleTrackIndex,
    onVideoSize,
    onTracksAvailable,
    subtitleSize = 1.0,
    controlsVisible,
    onPictureInPictureStatusChanged,
    externalSubtitleUrl,
    isHDR,
    isDV,
  } = props;

  const playerRef = useRef<NativePlayerRef>(null);
  const durationRef = useRef<number>(0);

  useImperativeHandle(ref, () => ({
    seek: (time: number) => {
      playerRef.current?.seek(time);
    },
    play: () => {
      // NativePlayer doesn't have separate play/pause methods - controlled via paused prop
    },
    pause: () => {
      // NativePlayer doesn't have separate play/pause methods - controlled via paused prop
    },
    enterPip: (forBackground?: boolean) => {
      playerRef.current?.enterPip(forBackground);
    },
  }), []);

  // Forward declaration for applyInitialTracks - will be assigned after its definition
  const applyInitialTracksRef = useRef<(() => void) | null>(null);

  const handleLoad = useCallback((data: LoadEvent) => {
    console.log('[NativePlayerAdapter] onLoad:', data);
    durationRef.current = data.duration;
    onLoad?.(data.duration);
    if (data.width && data.height) {
      onVideoSize?.(data.width, data.height);
    }
    // Try to apply tracks after load as backup (in case onTracksChanged doesn't fire or fires too early)
    // Use a longer delay to give KSPlayer time to initialize tracks
    setTimeout(() => {
      console.log('[NativePlayerAdapter] onLoad: attempting track application after delay');
      applyInitialTracksRef.current?.();
    }, 500);
  }, [onLoad, onVideoSize]);

  const handleProgress = useCallback((data: ProgressEvent) => {
    const meta: VideoProgressMeta = {
      playable: data.duration,
      seekable: data.duration,
    };
    onProgress(data.currentTime, meta);
  }, [onProgress]);

  const handleEnd = useCallback(() => {
    console.log('[NativePlayerAdapter] onEnd');
    onEnd?.();
  }, [onEnd]);

  const handleError = useCallback((data: ErrorEvent) => {
    console.error('[NativePlayerAdapter] onError:', data);
    onError?.(new Error(data.error));
  }, [onError]);

  const handleBuffering = useCallback((buffering: boolean) => {
    console.log('[NativePlayerAdapter] onBuffering:', buffering);
    onBuffer?.(buffering);
  }, [onBuffer]);

  // Convert NativePlayer track format to VideoPlayer TrackInfo format
  const handleTracksChanged = useCallback((data: TracksEvent) => {
    if (!onTracksAvailable) return;

    // Convert to TrackInfo format (id + name + optional language/codec)
    const audioTracks: TrackInfo[] = data.audioTracks.map((t) => ({
      id: t.id,
      name: t.title || t.language || '',
      language: t.language,
      codec: t.codec,
    }));
    const subtitleTracks: TrackInfo[] = data.subtitleTracks.map((t) => ({
      id: t.id,
      name: t.title || t.language || `Subtitle ${t.id}`,
      language: t.language,
    }));

    console.log('[NativePlayerAdapter] forwarding tracks to onTracksAvailable', {
      audioCount: audioTracks.length,
      subtitleCount: subtitleTracks.length,
    });
    onTracksAvailable(audioTracks, subtitleTracks);
  }, [onTracksAvailable]);

  // Report duration hint early if available
  useEffect(() => {
    if (durationHint && durationHint > 0 && onLoad) {
      onLoad(durationHint);
    }
  }, [durationHint, onLoad]);

  // Track whether initial tracks have been applied (only apply once per source)
  const initialTracksAppliedRef = useRef(false);
  const lastAppliedAudioRef = useRef<number | undefined>(undefined);
  const lastAppliedSubtitleRef = useRef<number | undefined>(undefined);
  const tracksReadyRef = useRef(false);

  // Reset refs when source changes
  useEffect(() => {
    initialTracksAppliedRef.current = false;
    lastAppliedAudioRef.current = undefined;
    lastAppliedSubtitleRef.current = undefined;
    tracksReadyRef.current = false;
  }, [movie]);

  // Handle track changes - applies tracks imperatively when they change
  useEffect(() => {
    if (selectedAudioTrackIndex === undefined || selectedAudioTrackIndex === null) return;

    // Skip if same as last applied
    if (selectedAudioTrackIndex === lastAppliedAudioRef.current) return;

    if (tracksReadyRef.current && playerRef.current) {
      console.log('[NativePlayerAdapter] applying audio track:', selectedAudioTrackIndex);
      playerRef.current.setAudioTrack(selectedAudioTrackIndex);
      lastAppliedAudioRef.current = selectedAudioTrackIndex;
    } else {
      console.log('[NativePlayerAdapter] audio track pending (tracks not ready):', selectedAudioTrackIndex);
    }
  }, [selectedAudioTrackIndex]);

  useEffect(() => {
    if (selectedSubtitleTrackIndex === undefined || selectedSubtitleTrackIndex === null) return;

    // Skip if same as last applied
    if (selectedSubtitleTrackIndex === lastAppliedSubtitleRef.current) return;

    if (tracksReadyRef.current && playerRef.current) {
      console.log('[NativePlayerAdapter] applying subtitle track:', selectedSubtitleTrackIndex);
      playerRef.current.setSubtitleTrack(selectedSubtitleTrackIndex);
      lastAppliedSubtitleRef.current = selectedSubtitleTrackIndex;
    } else {
      console.log('[NativePlayerAdapter] subtitle track pending (tracks not ready):', selectedSubtitleTrackIndex);
    }
  }, [selectedSubtitleTrackIndex]);

  // Store the latest track indices in refs to avoid stale closure issues
  const pendingAudioRef = useRef<number | undefined>(selectedAudioTrackIndex);
  const pendingSubtitleRef = useRef<number | undefined>(selectedSubtitleTrackIndex);

  // Keep refs in sync with props
  useEffect(() => {
    pendingAudioRef.current = selectedAudioTrackIndex;
    pendingSubtitleRef.current = selectedSubtitleTrackIndex;
    console.log('[NativePlayerAdapter] track refs updated:', {
      audio: selectedAudioTrackIndex,
      subtitle: selectedSubtitleTrackIndex,
    });
  }, [selectedAudioTrackIndex, selectedSubtitleTrackIndex]);

  // Apply tracks imperatively (used by both onTracksChanged and onLoad)
  const applyInitialTracks = useCallback(() => {
    if (initialTracksAppliedRef.current) {
      console.log('[NativePlayerAdapter] applyInitialTracks: already applied, skipping');
      return;
    }

    const audioToApply = pendingAudioRef.current;
    const subtitleToApply = pendingSubtitleRef.current;

    console.log('[NativePlayerAdapter] applyInitialTracks called:', {
      audioToApply,
      subtitleToApply,
      hasPlayerRef: !!playerRef.current,
    });

    if (!playerRef.current) {
      console.log('[NativePlayerAdapter] applyInitialTracks: no player ref yet');
      return;
    }

    initialTracksAppliedRef.current = true;
    tracksReadyRef.current = true;

    // Apply after a short delay to let KSPlayer stabilize
    setTimeout(() => {
      const audio = pendingAudioRef.current;
      const subtitle = pendingSubtitleRef.current;

      console.log('[NativePlayerAdapter] setTimeout applying tracks:', { audio, subtitle });

      if (audio !== undefined && audio !== null && playerRef.current) {
        console.log('[NativePlayerAdapter] applying initial audio track:', audio);
        playerRef.current.setAudioTrack(audio);
        lastAppliedAudioRef.current = audio;
      }
      if (subtitle !== undefined && subtitle !== null && subtitle >= 0 && playerRef.current) {
        console.log('[NativePlayerAdapter] applying initial subtitle track:', subtitle);
        playerRef.current.setSubtitleTrack(subtitle);
        lastAppliedSubtitleRef.current = subtitle;
      }
    }, 100);
  }, []);

  // Update ref so handleLoad can call applyInitialTracks
  applyInitialTracksRef.current = applyInitialTracks;

  // When tracks become available, apply any pending selections
  const originalHandleTracksChanged = handleTracksChanged;
  const handleTracksChangedWithPending = useCallback((data: { audioTracks: Array<{ id: number; title: string; language: string }>; subtitleTracks: Array<{ id: number; title: string; language: string }> }) => {
    console.log('[NativePlayerAdapter] onTracksChanged fired, track count:', {
      audio: data.audioTracks.length,
      subtitle: data.subtitleTracks.length,
    });
    originalHandleTracksChanged(data);
    applyInitialTracks();

    // Force-apply current pending tracks now that KSPlayer reports tracks are available.
    // applyInitialTracks may have already been marked as applied (from an earlier call when
    // KSPlayer tracks weren't ready yet), so we need to directly apply here as a fallback.
    const audio = pendingAudioRef.current;
    const subtitle = pendingSubtitleRef.current;
    if (audio !== undefined && audio !== null && playerRef.current) {
      console.log('[NativePlayerAdapter] onTracksChanged: force-applying audio track:', audio);
      playerRef.current.setAudioTrack(audio);
      lastAppliedAudioRef.current = audio;
    }
    if (subtitle !== undefined && subtitle !== null && subtitle >= 0 && playerRef.current) {
      console.log('[NativePlayerAdapter] onTracksChanged: force-applying subtitle track:', subtitle);
      playerRef.current.setSubtitleTrack(subtitle);
      lastAppliedSubtitleRef.current = subtitle;
    }
    tracksReadyRef.current = true;
  }, [originalHandleTracksChanged, applyInitialTracks]);

  // Build subtitle style to match our SubtitleOverlay appearance
  const subtitleStyle = useMemo(() => ({
    fontSize: subtitleSize,
    textColor: '#FFFFFF',
    backgroundColor: '#00000099', // 60% black background (matching SubtitleOverlay)
    bottomMargin: 50, // Match SubtitleOverlay positioning
  }), [subtitleSize]);

  // Log the track indices being passed to native player
  console.log('[NativePlayerAdapter] ===== RENDER =====', {
    audioTrack: selectedAudioTrackIndex ?? 'undefined',
    subtitleTrack: selectedSubtitleTrackIndex ?? -1,
    externalSubtitleUrl: externalSubtitleUrl ?? 'undefined',
    isHDR: isHDR ?? 'undefined',
    isDV: isDV ?? 'undefined',
  });

  return (
    <NativePlayer
      ref={playerRef}
      source={{ uri: movie }}
      paused={paused}
      volume={volume}
      audioTrack={selectedAudioTrackIndex ?? undefined}
      subtitleTrack={selectedSubtitleTrackIndex ?? -1}
      subtitleStyle={subtitleStyle}
      controlsVisible={controlsVisible}
      externalSubtitleUrl={externalSubtitleUrl}
      isHDR={isHDR}
      isDV={isDV}
      style={nativePlayerStyles.player}
      onLoad={handleLoad}
      onProgress={handleProgress}
      onEnd={handleEnd}
      onError={handleError}
      onBuffering={handleBuffering}
      onTracksChanged={handleTracksChangedWithPending}
      onPipStatusChanged={onPictureInPictureStatusChanged}
    />
  );
});

NativePlayerAdapter.displayName = 'NativePlayerAdapter';

const nativePlayerStyles = StyleSheet.create({
  player: {
    flex: 1,
  },
});

const VideoPlayer = React.forwardRef<VideoPlayerHandle, VideoPlayerProps>((props, ref) => {
  const { onImplementationResolved, forceNativeFullscreen, forceRnvPlayer, ...rest } = props;

  const implementation = useMemo((): { key: VideoImplementation; Component: PlayerComponent } => {
    // Mobile web uses inline system player
    if (Platform.OS === 'web' && isMobileWeb()) {
      return { key: 'mobile-system', Component: MobileSystemVideoPlayer };
    }

    // Native platforms (iOS/tvOS/Android) - use NativePlayer if available and not forcing RNV
    // NativePlayer uses KSPlayer on iOS/tvOS and MPV on Android for direct stream playback
    if (!forceRnvPlayer && nativePlayerAvailable) {
      console.log('[VideoPlayer] Using NativePlayer for', Platform.OS);
      return { key: 'native', Component: NativePlayerAdapter as PlayerComponent };
    }

    // Fallback to react-native-video (RNV) for HLS streams or when native player unavailable
    if (RnvVideoPlayer) {
      return { key: 'rnv', Component: RnvVideoPlayer };
    }

    // Fallback for mobile web on non-mobile detection
    return { key: 'mobile-system', Component: MobileSystemVideoPlayer };
  }, [forceRnvPlayer]);

  const lastImplementationRef = useRef<VideoImplementation | null>(null);
  useEffect(() => {
    if (!onImplementationResolved) {
      return;
    }

    if (lastImplementationRef.current === implementation.key) {
      return;
    }

    lastImplementationRef.current = implementation.key;
    onImplementationResolved(implementation.key);
  }, [implementation.key, onImplementationResolved]);

  const ImplementationComponent = implementation.Component;
  return <ImplementationComponent {...rest} forceNativeFullscreen={forceNativeFullscreen} ref={ref} />;
});

VideoPlayer.displayName = 'VideoPlayer';

export default VideoPlayer;

const getRangeEnd = (range?: TimeRanges): number | undefined => {
  if (!range || range.length === 0) {
    return undefined;
  }

  try {
    return range.end(range.length - 1);
  } catch {
    return undefined;
  }
};

const clampVolume = (value: number): number => {
  if (!Number.isFinite(value)) {
    return 1;
  }

  if (value <= 0) {
    return 0;
  }

  if (value >= 1) {
    return 1;
  }

  return value;
};

const MobileSystemVideoPlayer = React.forwardRef<VideoPlayerHandle, VideoPlayerProps>((props, ref) => {
  const {
    movie,
    headerImage,
    paused,
    controls,
    onBuffer,
    onProgress,
    onLoad,
    onEnd,
    onError,
    durationHint,
    onInteract,
    volume = 1,
    onToggleFullscreen,
  } = props;

  const videoRef = useRef<HTMLVideoElement | null>(null);
  const { width, height } = useTVDimensions();
  const styles = useMemo(() => createSystemPlayerStyles({ width, height }), [height, width]);
  const resolvedVolume = clampVolume(volume);

  useImperativeHandle(
    ref,
    () => ({
      seek: (seconds: number) => {
        if (!videoRef.current) {
          return;
        }
        try {
          videoRef.current.currentTime = seconds;
        } catch (error) {
          console.warn('[MobileSystemVideoPlayer] unable to seek', error);
        }
      },
      toggleFullscreen: () => {
        const element = videoRef.current;
        if (!element) {
          return;
        }

        try {
          if (document.fullscreenElement) {
            document.exitFullscreen?.().catch(() => {});
            onToggleFullscreen?.();
            return;
          }

          const request = element.requestFullscreen?.();
          if (typeof request?.then === 'function') {
            request
              .then(() => {
                onToggleFullscreen?.();
              })
              .catch((error) => {
                console.warn('[VideoPlayer.system] requestFullscreen failed', error);
                attemptMobileFullscreen(element, onToggleFullscreen);
              });
            return;
          }

          if (request === undefined) {
            attemptMobileFullscreen(element, onToggleFullscreen);
          }
        } catch (error) {
          console.warn('[VideoPlayer.system] toggleFullscreen failed', error);
          attemptMobileFullscreen(element, onToggleFullscreen);
        }
      },
      play: () => {
        if (!videoRef.current) {
          return;
        }

        try {
          const result = videoRef.current.play?.();
          if (typeof result?.catch === 'function') {
            result.catch((error) => {
              console.warn('[VideoPlayer.system] play failed', error);
            });
          }
        } catch (error) {
          console.warn('[VideoPlayer.system] unable to trigger play', error);
        }
      },
      pause: () => {
        if (!videoRef.current) {
          return;
        }

        try {
          videoRef.current.pause?.();
        } catch (error) {
          console.warn('[VideoPlayer.system] unable to trigger pause', error);
        }
      },
    }),
    [onToggleFullscreen],
  );

  React.useEffect(() => {
    const element = videoRef.current;
    if (!element) {
      return;
    }

    try {
      element.volume = resolvedVolume;
      element.muted = resolvedVolume <= 0;
    } catch (error) {
      console.warn('[VideoPlayer.system] failed to sync volume', error);
    }
  }, [resolvedVolume]);

  React.useEffect(() => {
    if (!durationHint || !onLoad) {
      return;
    }
    if (Number.isFinite(durationHint) && durationHint > 0) {
      onLoad(durationHint);
    }
  }, [durationHint, onLoad]);

  const emitProgress = (element: HTMLVideoElement) => {
    const meta = {
      playable: getRangeEnd(element.buffered),
      seekable: getRangeEnd(element.seekable),
    } as VideoProgressMeta;

    onProgress(element.currentTime ?? 0, meta);
  };

  const handleLoadedMetadata: React.ReactEventHandler<HTMLVideoElement> = (event) => {
    const element = event.currentTarget;
    onLoad?.(Number(element.duration) || 0);
    emitProgress(element);
    onBuffer?.(false);
  };

  const handleTimeUpdate: React.ReactEventHandler<HTMLVideoElement> = (event) => {
    emitProgress(event.currentTarget);
  };

  const handleWaiting = () => {
    onBuffer?.(true);
  };

  const handlePlaying = () => {
    onBuffer?.(false);
  };

  const handleTouchStart: React.TouchEventHandler<HTMLVideoElement> = () => {
    onInteract?.();
  };

  const handleEnded = () => {
    onBuffer?.(false);
    onEnd?.();
  };

  const handleError: React.ReactEventHandler<HTMLVideoElement> = (event) => {
    const element = event.currentTarget;
    const { error } = element;
    const detail = error
      ? {
          code: error.code,
          message: error.message,
        }
      : new Error('Unknown HTMLVideoElement error');
    onError?.(detail);
  };

  return (
    <View style={styles.wrapper}>
      <video
        ref={videoRef}
        style={styles.video}
        poster={headerImage || undefined}
        src={movie || undefined}
        controls={controls ?? true}
        playsInline
        preload="auto"
        autoPlay={!paused}
        muted={resolvedVolume <= 0}
        onLoadedMetadata={handleLoadedMetadata}
        onTimeUpdate={handleTimeUpdate}
        onProgress={handleTimeUpdate}
        onWaiting={handleWaiting}
        onPlaying={handlePlaying}
        onTouchStart={handleTouchStart}
        onEnded={handleEnded}
        onError={handleError}
      />
    </View>
  );
});

MobileSystemVideoPlayer.displayName = 'VideoPlayer.system-web';

const createSystemPlayerStyles = ({ width: _width, height: _height }: { width: number; height: number }) => {
  const wrapper: ViewStyle = {
    flex: 1,
    width: '100%',
    height: '100%',
    alignItems: 'stretch',
    justifyContent: 'center',
    backgroundColor: 'black',
  };

  const video: CSSProperties = {
    width: '100%',
    height: '100%',
    maxWidth: '100%',
    maxHeight: '100%',
    backgroundColor: 'black',
    display: 'block',
    objectFit: 'cover',
    objectPosition: 'center center',
  };

  return { wrapper, video };
};

const attemptMobileFullscreen = (element: HTMLVideoElement, onToggleFullscreen?: () => void) => {
  const anyElement = element as any;
  if (typeof anyElement?.webkitEnterFullscreen === 'function') {
    try {
      anyElement.webkitEnterFullscreen();
      onToggleFullscreen?.();
    } catch (error) {
      console.warn('[VideoPlayer.system] webkitEnterFullscreen failed', error);
    }
  }
};
