/**
 * useTrailers — owns trailer prequeue, auto-play, immersive mode, and remote control
 */

import { useCallback, useEffect, useRef, useState } from 'react';
import { Platform } from 'react-native';
import { apiService, type TrailerPrequeueStatus, type Trailer } from '@/services/api';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import Animated, {
  useAnimatedStyle,
  useSharedValue,
  withTiming,
} from 'react-native-reanimated';

interface UseTrailersParams {
  primaryTrailer: Trailer | null;
  autoPlayTrailersTV: boolean;
  isDetailsPageActive: boolean;
  prequeueId: string | null; // content prequeue (not trailer)
}

export interface TrailersResult {
  // Trailer prequeue
  trailerStreamUrl: string | null;
  trailerPrequeueId: string | null;
  trailerPrequeueStatus: TrailerPrequeueStatus | null;

  // Auto-play state
  isBackdropTrailerPlaying: boolean;
  setIsBackdropTrailerPlaying: React.Dispatch<React.SetStateAction<boolean>>;
  isTrailerImmersiveMode: boolean;
  setIsTrailerImmersiveMode: React.Dispatch<React.SetStateAction<boolean>>;
  trailerAutoPlayDismissed: boolean;

  // Actions
  dismissTrailerAutoPlay: () => void;

  // Animated style for immersive fade
  immersiveContentStyle: ReturnType<typeof useAnimatedStyle>;
  immersiveContentOpacity: Animated.SharedValue<number>;
}

export function useTrailers(params: UseTrailersParams): TrailersResult {
  const {
    primaryTrailer,
    autoPlayTrailersTV,
    isDetailsPageActive,
    prequeueId,
  } = params;

  // Trailer prequeue state
  const [trailerPrequeueId, setTrailerPrequeueId] = useState<string | null>(null);
  const [trailerPrequeueStatus, setTrailerPrequeueStatus] = useState<TrailerPrequeueStatus | null>(null);
  const [trailerStreamUrl, setTrailerStreamUrl] = useState<string | null>(null);

  // Auto-play state
  const [isBackdropTrailerPlaying, setIsBackdropTrailerPlaying] = useState(false);
  const [isTrailerImmersiveMode, setIsTrailerImmersiveMode] = useState(false);
  const [trailerAutoPlayDismissed, setTrailerAutoPlayDismissed] = useState(false);

  const dismissTrailerAutoPlay = useCallback(() => {
    if (autoPlayTrailersTV) {
      setTrailerAutoPlayDismissed(true);
      setIsBackdropTrailerPlaying(false);
    }
  }, [autoPlayTrailersTV]);

  // Prequeue YouTube trailers for 1080p playback on mobile
  useEffect(() => {
    if (Platform.OS === 'web') {
      setTrailerStreamUrl(null);
      setTrailerPrequeueId(null);
      setTrailerPrequeueStatus(null);
      return;
    }
    const trailerUrl = primaryTrailer?.url;
    if (!trailerUrl) {
      setTrailerStreamUrl(null);
      setTrailerPrequeueId(null);
      setTrailerPrequeueStatus(null);
      return;
    }
    const isYouTube = trailerUrl.includes('youtube.com') || trailerUrl.includes('youtu.be');
    if (!isYouTube) {
      setTrailerStreamUrl(trailerUrl);
      setTrailerPrequeueId(null);
      setTrailerPrequeueStatus(null);
      return;
    }
    let cancelled = false;
    setTrailerPrequeueStatus('pending');
    setTrailerStreamUrl(null);
    apiService
      .prequeueTrailer(trailerUrl)
      .then((response) => {
        if (cancelled) return;
        setTrailerPrequeueId(response.id);
        setTrailerPrequeueStatus(response.status);
      })
      .catch((err) => {
        if (cancelled) return;
        console.warn('[trailer-prequeue] failed to start prequeue:', err);
        setTrailerPrequeueStatus('failed');
      });
    return () => { cancelled = true; };
  }, [primaryTrailer?.url]);

  // Poll for trailer prequeue status
  useEffect(() => {
    if (!trailerPrequeueId || trailerPrequeueStatus === 'ready' || trailerPrequeueStatus === 'failed') {
      return;
    }
    let cancelled = false;
    let lastStatus = trailerPrequeueStatus;
    const pollInterval = setInterval(async () => {
      if (cancelled) return;
      try {
        const status = await apiService.getTrailerPrequeueStatus(trailerPrequeueId);
        if (cancelled) return;
        if (status.status === 'ready') {
          setTrailerPrequeueStatus('ready');
          const serveUrl = apiService.getTrailerPrequeueServeUrl(trailerPrequeueId);
          setTrailerStreamUrl(serveUrl);
          clearInterval(pollInterval);
        } else if (status.status === 'failed') {
          setTrailerPrequeueStatus('failed');
          console.warn('[trailer-prequeue] download failed:', status.error);
          clearInterval(pollInterval);
        } else if (status.status !== lastStatus) {
          lastStatus = status.status;
          setTrailerPrequeueStatus(status.status);
        }
      } catch (err) {
        if (cancelled) return;
        console.warn('[trailer-prequeue] status check failed:', err);
      }
    }, 1000);
    return () => {
      cancelled = true;
      clearInterval(pollInterval);
    };
  }, [trailerPrequeueId, trailerPrequeueStatus]);

  // Auto-start backdrop trailer
  useEffect(() => {
    if (autoPlayTrailersTV && trailerPrequeueStatus === 'ready' && trailerStreamUrl && !trailerAutoPlayDismissed && !prequeueId) {
      setIsBackdropTrailerPlaying(true);
    }
  }, [autoPlayTrailersTV, trailerPrequeueStatus, trailerStreamUrl, trailerAutoPlayDismissed, prequeueId]);

  // Exit immersive mode when trailer stops
  useEffect(() => {
    if (!isBackdropTrailerPlaying) {
      setIsTrailerImmersiveMode(false);
    }
  }, [isBackdropTrailerPlaying]);

  // Remote control listener for trailer playback (non-immersive)
  useEffect(() => {
    if (!Platform.isTV || !autoPlayTrailersTV || !trailerStreamUrl || !isDetailsPageActive) return;
    const removeListener = RemoteControlManager.addKeydownListener((key) => {
      if (key === SupportedKeys.PlayPause) {
        setIsBackdropTrailerPlaying((prev) => !prev);
        return;
      }
    });
    return () => { removeListener(); };
  }, [autoPlayTrailersTV, trailerStreamUrl, isDetailsPageActive]);

  // Key interceptor for immersive mode: consumes ALL keys to exit immersive mode
  useEffect(() => {
    if (isTrailerImmersiveMode) {
      const removeInterceptor = RemoteControlManager.pushKeyInterceptor((key) => {
        if (key === SupportedKeys.PlayPause) {
          setIsBackdropTrailerPlaying((prev) => !prev);
          setIsTrailerImmersiveMode(false);
          return true; // handled
        }
        // Any other key also exits immersive mode and is consumed to prevent it from reaching the details page
        setIsTrailerImmersiveMode(false);
        return true; // handled
      });
      return () => removeInterceptor();
    }
  }, [isTrailerImmersiveMode]);

  // Stop trailer when navigating away or content prequeue starts
  useEffect(() => {
    if ((!isDetailsPageActive || prequeueId) && isBackdropTrailerPlaying) {
      setIsBackdropTrailerPlaying(false);
      setIsTrailerImmersiveMode(false);
    }
  }, [isDetailsPageActive, isBackdropTrailerPlaying, prequeueId]);

  // Immersive mode opacity animation
  const immersiveContentOpacity = useSharedValue(1);
  useEffect(() => {
    immersiveContentOpacity.value = withTiming(isTrailerImmersiveMode ? 0 : 1, { duration: 400 });
  }, [isTrailerImmersiveMode]);
  const immersiveContentStyle = useAnimatedStyle(() => ({
    opacity: immersiveContentOpacity.value,
  }));

  return {
    trailerStreamUrl,
    trailerPrequeueId,
    trailerPrequeueStatus,
    isBackdropTrailerPlaying,
    setIsBackdropTrailerPlaying,
    isTrailerImmersiveMode,
    setIsTrailerImmersiveMode,
    trailerAutoPlayDismissed,
    dismissTrailerAutoPlay,
    immersiveContentStyle,
    immersiveContentOpacity,
  };
}
