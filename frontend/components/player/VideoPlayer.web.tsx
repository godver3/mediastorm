import type { CSSProperties } from 'react';
import React, { useCallback, useEffect, useImperativeHandle, useMemo, useRef } from 'react';
import { View, type ViewStyle } from 'react-native';

import type { VideoPlayerHandle, VideoPlayerProps, VideoProgressMeta } from './types';

import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';

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

const getRangeEnd = (range?: TimeRanges): number | undefined => {
  if (!range || range.length === 0) {
    return undefined;
  }
  try {
    return range.end(range.length - 1);
  } catch (error) {
    console.warn('[VideoPlayer.web] unable to read range end', error);
    return undefined;
  }
};

const isAutoplayError = (error: unknown): boolean => {
  if (!error) {
    return false;
  }
  if (typeof error === 'object') {
    const domError = error as { name?: string; message?: string; code?: number };
    const name = domError.name?.toLowerCase() ?? '';
    const message = domError.message?.toLowerCase() ?? '';
    if (domError.code === 0) {
      return true;
    }
    if (name.includes('notallowed') || name.includes('aborted')) {
      return true;
    }
    if (message.includes('autoplay') || message.includes('user gesture') || message.includes('gesture required')) {
      return true;
    }
  }
  if (typeof error === 'string' && error.toLowerCase().includes('autoplay')) {
    return true;
  }
  return false;
};

const useVideoStyles = (theme: NovaTheme, resizeMode: 'cover' | 'contain' = 'cover'): { container: ViewStyle; video: CSSProperties } => {
  return useMemo(() => {
    const backgroundColor = theme.colors?.background?.base ?? 'black';
    return {
      container: {
        // Use both flex: 1 and explicit 100% dimensions for compatibility
        // with both flex and absolutely positioned parents
        flex: 1,
        width: '100%',
        height: '100%',
        alignItems: 'stretch',
        justifyContent: 'center',
        backgroundColor,
      },
      video: {
        width: '100%',
        height: '100%',
        objectFit: resizeMode,
        objectPosition: 'center center',
        backgroundColor: 'black',
      },
    };
  }, [theme, resizeMode]);
};

const VideoPlayer = React.forwardRef<VideoPlayerHandle, VideoPlayerProps>((props, ref) => {
  const {
    movie,
    headerImage,
    movieTitle,
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
    onAutoplayBlocked,
    onToggleFullscreen,
    resizeMode = 'cover',
  } = props;

  const theme = useTheme();
  const styles = useVideoStyles(theme, resizeMode);
  const videoRef = useRef<HTMLVideoElement | null>(null);
  const lastDurationRef = useRef(0);
  const isBufferingRef = useRef(false);
  const lastMovieRef = useRef<string | null>(null);

  const reportDuration = useCallback(
    (value?: number | null, source?: string) => {
      if (value === undefined || value === null) {
        return;
      }
      const numeric = Number(value);
      if (!Number.isFinite(numeric) || numeric <= 0) {
        return;
      }
      if (numeric <= lastDurationRef.current) {
        return;
      }
      lastDurationRef.current = numeric;
      onLoad(numeric);
      console.debug('[VideoPlayer.web] duration reported', { numeric, source });
    },
    [onLoad],
  );

  const updateBuffering = useCallback(
    (buffering: boolean) => {
      if (isBufferingRef.current === buffering) {
        return;
      }
      isBufferingRef.current = buffering;
      onBuffer(buffering);
    },
    [onBuffer],
  );

  useEffect(() => {
    updateBuffering(false);
  }, [updateBuffering]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) {
      return;
    }
    const handleLoadedMetadata = () => {
      reportDuration(video.duration, 'loadedmetadata');
    };
    const handleDurationChange = () => {
      reportDuration(video.duration, 'durationchange');
    };
    const handleProgress = () => {
      const playable = getRangeEnd(video.buffered);
      const seekable = getRangeEnd(video.seekable);
      const meta: VideoProgressMeta = {};
      if (Number.isFinite(playable)) {
        meta.playable = playable as number;
      }
      if (Number.isFinite(seekable)) {
        meta.seekable = seekable as number;
      }
      onProgress(video.currentTime, meta);
    };
    const handleTimeUpdate = () => {
      const playable = getRangeEnd(video.buffered);
      const seekable = getRangeEnd(video.seekable);
      const meta: VideoProgressMeta = {};
      if (Number.isFinite(playable)) {
        meta.playable = playable as number;
      }
      if (Number.isFinite(seekable)) {
        meta.seekable = seekable as number;
      }
      onProgress(video.currentTime, meta);
    };
    const handleWaiting = () => updateBuffering(true);
    const handlePlaying = () => updateBuffering(false);
    const handleEnded = () => {
      updateBuffering(false);
      onEnd();
    };
    const handleError = (event: Event) => {
      updateBuffering(false);
      onError?.(event);
    };
    const handleFullscreenChange = () => {
      onToggleFullscreen?.();
    };
    const handlePointerDown = () => {
      onInteract?.();
    };

    video.addEventListener('loadedmetadata', handleLoadedMetadata);
    video.addEventListener('durationchange', handleDurationChange);
    video.addEventListener('progress', handleProgress);
    video.addEventListener('timeupdate', handleTimeUpdate);
    video.addEventListener('waiting', handleWaiting);
    video.addEventListener('playing', handlePlaying);
    video.addEventListener('ended', handleEnded);
    video.addEventListener('error', handleError);
    video.addEventListener('pointerdown', handlePointerDown);
    document.addEventListener('fullscreenchange', handleFullscreenChange);

    return () => {
      video.removeEventListener('loadedmetadata', handleLoadedMetadata);
      video.removeEventListener('durationchange', handleDurationChange);
      video.removeEventListener('progress', handleProgress);
      video.removeEventListener('timeupdate', handleTimeUpdate);
      video.removeEventListener('waiting', handleWaiting);
      video.removeEventListener('playing', handlePlaying);
      video.removeEventListener('ended', handleEnded);
      video.removeEventListener('error', handleError);
      video.removeEventListener('pointerdown', handlePointerDown);
      document.removeEventListener('fullscreenchange', handleFullscreenChange);
    };
  }, [onEnd, onError, onInteract, onProgress, onToggleFullscreen, reportDuration, updateBuffering]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) {
      return;
    }
    if (!movie) {
      video.removeAttribute('src');
      video.load();
      updateBuffering(false);
      lastMovieRef.current = null;
      return;
    }
    if (lastMovieRef.current === movie) {
      return;
    }
    lastMovieRef.current = movie;
    updateBuffering(true);
    video.src = movie;
    video.load();
  }, [movie, updateBuffering]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) {
      return;
    }
    const volumeValue = clampVolume(volume);
    video.volume = volumeValue;
    video.muted = volumeValue === 0;
  }, [volume]);

  useEffect(() => {
    const video = videoRef.current;
    if (!video) {
      return;
    }
    if (paused) {
      video.pause();
      return;
    }
    const result = video.play();
    if (result && typeof (result as Promise<void>).catch === 'function') {
      (result as Promise<void>).catch((error) => {
        if (isAutoplayError(error)) {
          onAutoplayBlocked?.();
          return;
        }
        onError?.(error);
      });
    }
  }, [onAutoplayBlocked, onError, paused]);

  useEffect(() => {
    reportDuration(durationHint, 'durationHint');
  }, [durationHint, reportDuration]);

  useImperativeHandle(
    ref,
    () => ({
      seek: (seconds: number) => {
        const video = videoRef.current;
        if (!video) {
          return;
        }
        try {
          video.currentTime = Number.isFinite(seconds) && seconds >= 0 ? seconds : 0;
        } catch (error) {
          console.warn('[VideoPlayer.web] unable to seek', error);
        }
      },
      pause: () => {
        const element = videoRef.current;
        if (!element) {
          return;
        }

        try {
          element.pause();
        } catch (error) {
          console.warn('[VideoPlayer.web] unable to trigger pause()', error);
        }
      },
      toggleFullscreen: () => {
        const element = videoRef.current;
        if (!element) {
          return;
        }
        try {
          if (document.fullscreenElement) {
            document.exitFullscreen?.();
          } else {
            element.requestFullscreen?.().catch((error) => {
              console.warn('[VideoPlayer.web] requestFullscreen failed', error);
            });
          }
        } catch (error) {
          console.warn('[VideoPlayer.web] fullscreen toggle failed', error);
        }
      },
      play: () => {
        const element = videoRef.current;
        if (!element) {
          return;
        }
        const playPromise = element.play();
        if (playPromise && typeof (playPromise as Promise<void>).catch === 'function') {
          (playPromise as Promise<void>).catch((error) => {
            if (isAutoplayError(error)) {
              onAutoplayBlocked?.();
              return;
            }
            onError?.(error);
          });
        }
      },
    }),
    [onAutoplayBlocked, onError],
  );

  return (
    <View style={styles.container}>
      <video
        ref={videoRef}
        key={movie}
        controls={controls}
        poster={headerImage}
        aria-label={movieTitle}
        playsInline
        preload="auto"
        style={styles.video}
        src={movie}
        crossOrigin="anonymous"
      />
    </View>
  );
});

VideoPlayer.displayName = 'VideoPlayer.web';

export default VideoPlayer;
