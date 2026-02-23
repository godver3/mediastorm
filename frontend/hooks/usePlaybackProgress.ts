import { useCallback, useRef, useEffect } from 'react';
import { AppState } from 'react-native';
import apiService, { PlaybackProgressUpdate } from '../services/api';

export interface PlaybackProgressOptions {
  /**
   * How often to send progress updates (in milliseconds)
   * Default: 10000 (10 seconds)
   */
  updateInterval?: number;

  /**
   * Minimum time change required to trigger an update (in seconds)
   * Prevents excessive updates when user is paused
   * Default: 5 seconds
   */
  minTimeChange?: number;

  /**
   * When true, progress updates continue even when app is backgrounded (e.g. PiP mode)
   * Default: false
   */
  isPipActive?: boolean;

  /**
   * Enable debug logging
   * Default: false
   */
  debug?: boolean;
}

export interface UsePlaybackProgressReturn {
  /**
   * Report current playback position
   */
  reportProgress: (position: number, duration: number, isPaused?: boolean) => void;

  /**
   * Clear the progress tracking interval
   */
  stopTracking: () => void;

  /**
   * Force an immediate progress update
   */
  forceUpdate: (position: number, duration: number, isPaused?: boolean) => Promise<void>;
}

/**
 * Hook for tracking playback progress and automatically sending updates to the backend
 */
export function usePlaybackProgress(
  userId: string,
  mediaInfo: {
    mediaType: 'movie' | 'episode';
    itemId: string;
    // Episode-specific
    seasonNumber?: number;
    episodeNumber?: number;
    seriesId?: string;
    seriesName?: string;
    episodeName?: string;
    // Movie-specific
    movieName?: string;
    year?: number;
    externalIds?: Record<string, string>;
  },
  options: PlaybackProgressOptions = {},
): UsePlaybackProgressReturn {
  const { updateInterval = 10000, minTimeChange = 5, isPipActive = false, debug = false } = options;

  const lastUpdateTimeRef = useRef<number>(0);
  const lastPositionRef = useRef<number>(0);
  const lastSentPausedRef = useRef<boolean>(false);
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const currentPositionRef = useRef<number>(0);
  const currentDurationRef = useRef<number>(0);
  const isUnmountedRef = useRef<boolean>(false);
  const isBackgroundedRef = useRef<boolean>(false);

  const log = useCallback(
    (...args: unknown[]) => {
      if (debug) {
        console.log('[usePlaybackProgress]', ...args);
      }
    },
    [debug],
  );

  const sendUpdate = useCallback(
    async (position: number, duration: number) => {
      if (isUnmountedRef.current || (isBackgroundedRef.current && !isPipActive)) {
        return;
      }

      // Validate inputs
      if (!Number.isFinite(position) || position < 0) {
        log('Invalid position:', position);
        return;
      }

      if (!Number.isFinite(duration) || duration <= 0) {
        log('Invalid duration:', duration);
        return;
      }

      // Skip if position hasn't changed enough (but always send when pause state changes)
      const timeSinceLastUpdate = Date.now() - lastUpdateTimeRef.current;
      const positionDiff = Math.abs(position - lastPositionRef.current);
      const pauseStateChanged = lastSentPausedRef.current !== isPausedRef.current;

      if (!pauseStateChanged && timeSinceLastUpdate < updateInterval && positionDiff < minTimeChange) {
        log('Skipping update - insufficient change', { timeSinceLastUpdate, positionDiff });
        return;
      }

      const update: PlaybackProgressUpdate = {
        mediaType: mediaInfo.mediaType,
        itemId: mediaInfo.itemId,
        position,
        duration,
        isPaused: isPausedRef.current,
        externalIds: mediaInfo.externalIds,
        seasonNumber: mediaInfo.seasonNumber,
        episodeNumber: mediaInfo.episodeNumber,
        seriesId: mediaInfo.seriesId,
        seriesName: mediaInfo.seriesName,
        episodeName: mediaInfo.episodeName,
        movieName: mediaInfo.movieName,
        year: mediaInfo.year,
      };

      try {
        log('Sending progress update', {
          position,
          duration,
          percentWatched: ((position / duration) * 100).toFixed(2) + '%',
        });

        const result = await apiService.updatePlaybackProgress(userId, update);

        lastUpdateTimeRef.current = Date.now();
        lastPositionRef.current = position;
        lastSentPausedRef.current = isPausedRef.current;

        log('Progress update sent successfully', result);
      } catch (error) {
        console.error('[usePlaybackProgress] Failed to send progress update:', error);
        console.error('[usePlaybackProgress] Update payload was:', update);
      }
    },
    [userId, mediaInfo, updateInterval, minTimeChange, isPipActive, log],
  );

  const isPausedRef = useRef<boolean>(false);

  const reportProgress = useCallback((position: number, duration: number, isPaused?: boolean) => {
    currentPositionRef.current = position;
    currentDurationRef.current = duration;
    if (isPaused !== undefined) {
      isPausedRef.current = isPaused;
    }
  }, []);

  const forceUpdate = useCallback(
    async (position: number, duration: number, isPaused?: boolean) => {
      if (isPaused !== undefined) isPausedRef.current = isPaused;
      // Temporarily clear the throttling
      lastUpdateTimeRef.current = 0;
      await sendUpdate(position, duration);
    },
    [sendUpdate],
  );

  const stopTracking = useCallback(() => {
    if (intervalRef.current) {
      clearInterval(intervalRef.current);
      intervalRef.current = null;
      log('Stopped tracking');
    }
  }, [log]);

  // Track app state to avoid sending updates when backgrounded
  useEffect(() => {
    const subscription = AppState.addEventListener('change', (nextAppState) => {
      isBackgroundedRef.current = nextAppState !== 'active';
      if (isBackgroundedRef.current) {
        log('App backgrounded - pausing progress updates');
      } else {
        log('App active - resuming progress updates');
      }
    });

    return () => {
      subscription.remove();
    };
  }, [log]);

  // Set up periodic progress reporting
  useEffect(() => {
    log('Starting progress tracking', { updateInterval });

    intervalRef.current = setInterval(() => {
      const position = currentPositionRef.current;
      const duration = currentDurationRef.current;

      if (position > 0 && duration > 0) {
        sendUpdate(position, duration);
      }
    }, updateInterval);

    return () => {
      stopTracking();
    };
  }, [sendUpdate, stopTracking, updateInterval, log]);

  // Send final update on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;

      // Send one final update with the last known position
      const position = currentPositionRef.current;
      const duration = currentDurationRef.current;

      if (position > 0 && duration > 0) {
        // Don't await this - fire and forget on unmount
        apiService
          .updatePlaybackProgress(userId, {
            mediaType: mediaInfo.mediaType,
            itemId: mediaInfo.itemId,
            position,
            duration,
            isPaused: true, // Unmounting = playback ending
            externalIds: mediaInfo.externalIds,
            seasonNumber: mediaInfo.seasonNumber,
            episodeNumber: mediaInfo.episodeNumber,
            seriesId: mediaInfo.seriesId,
            seriesName: mediaInfo.seriesName,
            episodeName: mediaInfo.episodeName,
            movieName: mediaInfo.movieName,
            year: mediaInfo.year,
          })
          .catch((error) => {
            console.warn('[usePlaybackProgress] Failed to send final progress update:', error);
          });
      }
    };
  }, [userId, mediaInfo]);

  return {
    reportProgress,
    stopTracking,
    forceUpdate,
  };
}
