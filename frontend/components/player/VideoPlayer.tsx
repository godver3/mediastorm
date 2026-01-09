import type { CSSProperties } from 'react';
import React, { useEffect, useImperativeHandle, useMemo, useRef } from 'react';
import { Platform, View, type ViewStyle } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';

import { isMobileWeb } from './isMobileWeb';
import type { VideoImplementation, VideoPlayerHandle, VideoPlayerProps, VideoProgressMeta } from './types';

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

export type {
  NowPlayingMetadata,
  TrackInfo,
  VideoImplementation,
  VideoPlayerHandle,
  VideoPlayerProps,
  VideoProgressMeta,
} from './types';

const VideoPlayer = React.forwardRef<VideoPlayerHandle, VideoPlayerProps>((props, ref) => {
  const { onImplementationResolved, forceNativeFullscreen, ...rest } = props;

  const implementation = useMemo((): { key: VideoImplementation; Component: PlayerComponent } => {
    // Mobile web uses inline system player
    if (Platform.OS === 'web' && isMobileWeb()) {
      return { key: 'mobile-system', Component: MobileSystemVideoPlayer };
    }

    // Native platforms use react-native-video
    if (RnvVideoPlayer) {
      return { key: 'rnv', Component: RnvVideoPlayer };
    }

    // Fallback for mobile web on non-mobile detection
    return { key: 'mobile-system', Component: MobileSystemVideoPlayer };
  }, []);

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
