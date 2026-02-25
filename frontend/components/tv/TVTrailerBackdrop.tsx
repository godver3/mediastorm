/**
 * TV Trailer Backdrop - Plays trailer video in backdrop area on TV details pages
 * Replaces static backdrop image with auto-playing trailer when enabled
 */

import React, { memo, useCallback, useEffect, useRef, useState } from 'react';
import { Platform, StyleSheet, View } from 'react-native';
import { Image as ExpoImage } from 'expo-image';
import { LinearGradient } from 'expo-linear-gradient';
import Video, { type BufferConfig, type VideoRef } from 'react-native-video';
import Animated, { useSharedValue, useAnimatedStyle, withTiming } from 'react-native-reanimated';
import { isAndroidTV } from '@/theme/tokens/tvScale';

interface TVTrailerBackdropProps {
  backdropUrl: string | null;
  trailerStreamUrl: string | null;
  isPlaying: boolean;
  isImmersive?: boolean; // Hide gradient and show video at full opacity when in immersive mode
  onEnd: () => void;
  onError?: () => void;
}

const AnimatedLinearGradient = Animated.createAnimatedComponent(LinearGradient);

const TVTrailerBackdrop = memo(function TVTrailerBackdrop({
  backdropUrl,
  trailerStreamUrl,
  isPlaying,
  isImmersive = false,
  onEnd,
  onError,
}: TVTrailerBackdropProps) {
  const videoRef = useRef<VideoRef>(null);
  const [videoReady, setVideoReady] = useState(false);
  const [videoError, setVideoError] = useState(false);

  // Keep video mounted briefly after pausing for fade-out transition
  const [keepMounted, setKeepMounted] = useState(false);
  const unmountTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Animate gradient opacity based on immersive mode
  const gradientOpacity = useSharedValue(1);
  useEffect(() => {
    gradientOpacity.value = withTiming(isImmersive ? 0 : 1, { duration: 400 });
  }, [isImmersive, gradientOpacity]);

  const gradientAnimatedStyle = useAnimatedStyle(() => ({
    opacity: gradientOpacity.value,
  }));

  // Target opacity: 1 when playing and ready, 0 otherwise
  const shouldShowVideo = isPlaying && videoReady && !videoError;

  // Animate video opacity
  const videoOpacity = useSharedValue(0);
  useEffect(() => {
    videoOpacity.value = withTiming(shouldShowVideo ? 1 : 0, { duration: 400 });
  }, [shouldShowVideo, videoOpacity]);

  const videoAnimatedStyle = useAnimatedStyle(() => ({
    opacity: videoOpacity.value,
  }));

  // Animate backdrop opacity - inverse of video
  const backdropOpacity = useSharedValue(1);
  useEffect(() => {
    backdropOpacity.value = withTiming(shouldShowVideo ? 0 : 1, { duration: 400 });
  }, [shouldShowVideo, backdropOpacity]);

  const backdropAnimatedStyle = useAnimatedStyle(() => ({
    opacity: backdropOpacity.value,
  }));

  // When playback stops, delay unmount to allow the opacity fade-out (400ms)
  useEffect(() => {
    if (isPlaying) {
      if (unmountTimerRef.current) clearTimeout(unmountTimerRef.current);
      setKeepMounted(true);
    } else {
      unmountTimerRef.current = setTimeout(() => setKeepMounted(false), 500);
    }
    return () => { if (unmountTimerRef.current) clearTimeout(unmountTimerRef.current); };
  }, [isPlaying]);

  const handleLoad = useCallback(() => {
    setVideoReady(true);
    setVideoError(false);
  }, []);

  const handleError = useCallback(() => {
    console.warn('[TVTrailerBackdrop] Video playback error');
    setVideoError(true);
    setVideoReady(false);
    onError?.();
  }, [onError]);

  const handleEnd = useCallback(() => {
    setVideoReady(false);
    onEnd();
  }, [onEnd]);

  // Render video when we have a URL, no error, and either playing or fading out
  const shouldRenderVideo = trailerStreamUrl && !videoError && (isPlaying || keepMounted);

  return (
    <View style={styles.container} pointerEvents="none">
      {/* Static backdrop image - always rendered, opacity animated */}
      {backdropUrl && (
        <Animated.View style={[styles.absoluteFill, backdropAnimatedStyle]}>
          <ExpoImage
            source={{ uri: backdropUrl }}
            style={styles.backdropImage}
            contentFit="cover"
            transition={0}
          />
        </Animated.View>
      )}

      {/* Video player - rendered when playing or fading out */}
      {shouldRenderVideo && (
        <Animated.View style={[styles.absoluteFill, videoAnimatedStyle]}>
          <Video
            ref={videoRef}
            source={{ uri: trailerStreamUrl }}
            style={styles.video}
            resizeMode="cover"
            paused={!isPlaying}
            muted={false}
            repeat={false}
            onLoad={handleLoad}
            onEnd={handleEnd}
            onError={handleError}
            controls={false}
            playInBackground={false}
            playWhenInactive={false}
            preventDisplayModeSwitch
            // TextureView for backdrop — SurfaceView creates a separate window layer
            // that can steal focus/input on Android TV
            useTextureView
            // Trailers don't need progress tracking — minimize bridge traffic
            progressUpdateInterval={1000}
            bufferConfig={
              Platform.OS === 'android'
                ? ({
                    minBufferMs: isAndroidTV ? 10000 : 5000,
                    maxBufferMs: isAndroidTV ? 20000 : 10000,
                    bufferForPlaybackMs: isAndroidTV ? 5000 : 2000,
                    bufferForPlaybackAfterRebufferMs: isAndroidTV ? 10000 : 5000,
                    backBufferDurationMs: 0,
                  } as BufferConfig)
                : undefined
            }
          />
        </Animated.View>
      )}

      {/* Gradient overlay - fades out in immersive mode */}
      <AnimatedLinearGradient
        colors={['rgba(0, 0, 0, 0)', 'rgba(0, 0, 0, 0.6)', 'rgba(0, 0, 0, 0.9)']}
        locations={[0, 0.5, 1]}
        start={{ x: 0.5, y: 0 }}
        end={{ x: 0.5, y: 1 }}
        style={[styles.heroFadeOverlay, gradientAnimatedStyle]}
        pointerEvents="none"
      />
    </View>
  );
});

const styles = StyleSheet.create({
  container: {
    ...StyleSheet.absoluteFillObject,
    alignItems: 'center',
    justifyContent: 'center',
    overflow: 'hidden',
  },
  absoluteFill: {
    ...StyleSheet.absoluteFillObject,
  },
  backdropImage: {
    ...StyleSheet.absoluteFillObject,
  },
  video: {
    ...StyleSheet.absoluteFillObject,
  },
  heroFadeOverlay: {
    position: 'absolute',
    left: 0,
    right: 0,
    bottom: 0,
    height: '25%',
  },
});

export default TVTrailerBackdrop;
