/**
 * React Native Video player implementation for HDR content on tvOS
 * Uses react-native-video with fullscreen presentation for proper HDR rendering
 */
import React, { useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import { Platform, Pressable, StyleSheet, View } from 'react-native';
import Video, {
  type OnLoadData,
  type OnProgressData,
  type VideoRef,
  type SelectedTrack,
  SelectedTrackType,
} from 'react-native-video';

import type { VideoPlayerHandle, VideoPlayerProps, VideoProgressMeta } from './types';

const RNVideoPlayer = React.forwardRef<VideoPlayerHandle, VideoPlayerProps>(
  (
    {
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
      selectedAudioTrackIndex,
      selectedSubtitleTrackIndex,
      onTracksAvailable,
      forceNativeFullscreen,
      onNativeFullscreenExit,
      nowPlaying,
      onVideoSize,
    },
    ref,
  ) => {
    const videoRef = useRef<VideoRef>(null);
    const [isFullscreen, setIsFullscreen] = useState(false);
    const lastDurationRef = useRef<number>(0);

    // Stuck playback detection - reload if:
    // 1. Playing but currentTime stays at 0, OR
    // 2. Time advances but video never renders (onReadyForDisplay never fires)
    const [sourceKey, setSourceKey] = useState(0);
    const playbackStartedAtRef = useRef<number | null>(null);
    const hasAdvancedRef = useRef(false);
    const hasRenderedFrameRef = useRef(false);
    const reloadAttemptRef = useRef(0);
    const MAX_RELOAD_ATTEMPTS = 2;
    const STUCK_THRESHOLD_MS = 4000; // 4 seconds before considering stuck (time at 0)
    const NO_RENDER_THRESHOLD_MS = 3000; // 3 seconds of playback without video frame

    const resolvedVolume = useMemo(() => {
      const numericVolume = Number(volume);
      if (!Number.isFinite(numericVolume)) return 1;
      if (numericVolume <= 0) return 0;
      if (numericVolume >= 1) return 1;
      return numericVolume;
    }, [volume]);

    // Note: We intentionally do NOT call onLoad with durationHint here.
    // The onLoad callback should only fire when the video actually loads,
    // otherwise it misleads the player into thinking playback is ready.
    // The parent component (player.tsx) handles durationHint separately.

    // Track first few progress events for debugging start position issues
    const progressEventCountRef = useRef(0);
    const firstProgressTimeRef = useRef<number | null>(null);

    // Reset stuck detection state when source changes
    useEffect(() => {
      playbackStartedAtRef.current = null;
      hasAdvancedRef.current = false;
      hasRenderedFrameRef.current = false;
      progressEventCountRef.current = 0;
      firstProgressTimeRef.current = null;
      reloadAttemptRef.current = 0; // Reset reload attempts for new source
      setSourceKey(0); // Reset source key for new source
    }, [movie]);

    const handleLoad = useCallback(
      (data: OnLoadData) => {
        // Clear buffering state when the source loads (matches Expo player behavior)
        onBuffer(false);

        // Log full OnLoadData to diagnose start position issues
        console.log('[RNVideoPlayer] onLoad event', {
          duration: data.duration,
          currentTime: data.currentTime,
          naturalSize: data.naturalSize,
          audioTracks: data.audioTracks?.length ?? 0,
          textTracks: data.textTracks?.length ?? 0,
        });

        // Log full data for debugging black screen issues
        console.log('[RNVideoPlayer] onLoad FULL DATA', JSON.stringify(data, null, 2));

        if (data.duration && data.duration > 0) {
          lastDurationRef.current = data.duration;
          onLoad?.(data.duration);
        }

        // Report video dimensions for subtitle positioning
        if (data.naturalSize?.width && data.naturalSize?.height) {
          onVideoSize?.(data.naturalSize.width, data.naturalSize.height);
        }

        // Report tracks if available
        if (onTracksAvailable) {
          const audioTracks = (data.audioTracks || []).map((track, index) => ({
            id: index,
            name: track.title || track.language || `Audio ${index}`,
          }));
          const textTracks = (data.textTracks || []).map((track, index) => ({
            id: index,
            name: track.title || track.language || `Subtitle ${index}`,
          }));
          if (audioTracks.length > 0 || textTracks.length > 0) {
            onTracksAvailable(audioTracks, textTracks);
          }
        }

        // Note: We no longer auto-enter native fullscreen for HDR on tvOS
        // This allows our custom controls overlay and subtitle overlay to remain visible
        // The video still renders HDR correctly in the inline player
      },
      [onBuffer, onLoad, onTracksAvailable, onVideoSize],
    );

    const handleProgress = useCallback(
      (data: OnProgressData) => {
        const eventCount = progressEventCountRef.current;
        progressEventCountRef.current = eventCount + 1;

        // Log first 5 progress events and flag if first event starts non-zero
        if (eventCount < 5) {
          const isFirstEvent = eventCount === 0;
          if (isFirstEvent) {
            firstProgressTimeRef.current = data.currentTime;
            if (data.currentTime > 1) {
              console.warn('[RNVideoPlayer] ⚠️ FIRST PROGRESS EVENT IS NON-ZERO!', {
                currentTime: data.currentTime,
                playableDuration: data.playableDuration,
                seekableDuration: data.seekableDuration,
              });
            }
          }
          console.log('[RNVideoPlayer] progress event #' + eventCount, {
            currentTime: data.currentTime,
            playableDuration: data.playableDuration,
            seekableDuration: data.seekableDuration,
            firstProgressTime: firstProgressTimeRef.current,
          });
        }

        // Log every 10th event to track if currentTime ever advances (debugging black screen)
        if (eventCount > 0 && eventCount % 10 === 0) {
          console.log('[RNVideoPlayer] progress event #' + eventCount + ' (periodic)', {
            currentTime: data.currentTime,
            playableDuration: data.playableDuration,
            seekableDuration: data.seekableDuration,
          });
        }

        // Stuck playback detection: if currentTime advances, mark as not stuck
        if (data.currentTime > 0.1) {
          if (!hasAdvancedRef.current) {
            console.log('[RNVideoPlayer] playback advancing normally, clearing stuck detection');
            hasAdvancedRef.current = true;
          }

          // Check for "no render" condition: time is advancing but no video frame rendered
          // This catches the case where audio plays but video shows black
          if (
            !hasRenderedFrameRef.current &&
            playbackStartedAtRef.current !== null &&
            reloadAttemptRef.current < MAX_RELOAD_ATTEMPTS
          ) {
            const playingDuration = Date.now() - playbackStartedAtRef.current;
            if (playingDuration > NO_RENDER_THRESHOLD_MS) {
              console.warn('[RNVideoPlayer] ⚠️ NO VIDEO RENDER DETECTED - audio playing but no frames', {
                playingDuration,
                attempt: reloadAttemptRef.current + 1,
                maxAttempts: MAX_RELOAD_ATTEMPTS,
                currentTime: data.currentTime,
                hasRenderedFrame: hasRenderedFrameRef.current,
              });

              // Reset state and trigger reload
              reloadAttemptRef.current += 1;
              playbackStartedAtRef.current = null;
              hasAdvancedRef.current = false;
              hasRenderedFrameRef.current = false;
              progressEventCountRef.current = 0;
              firstProgressTimeRef.current = null;
              setSourceKey((prev) => prev + 1);
              return; // Don't emit progress for this event
            }
          } else if (hasRenderedFrameRef.current && reloadAttemptRef.current > 0) {
            // Video is rendering, reset reload attempts
            reloadAttemptRef.current = 0;
          }
        } else if (playbackStartedAtRef.current !== null && !hasAdvancedRef.current) {
          // Check if we've been stuck at time 0 for too long
          const stuckDuration = Date.now() - playbackStartedAtRef.current;
          if (stuckDuration > STUCK_THRESHOLD_MS && reloadAttemptRef.current < MAX_RELOAD_ATTEMPTS) {
            console.warn('[RNVideoPlayer] ⚠️ STUCK PLAYBACK DETECTED - time not advancing', {
              stuckDuration,
              attempt: reloadAttemptRef.current + 1,
              maxAttempts: MAX_RELOAD_ATTEMPTS,
              currentTime: data.currentTime,
              playableDuration: data.playableDuration,
            });

            // Reset state and trigger reload
            reloadAttemptRef.current += 1;
            playbackStartedAtRef.current = null;
            hasRenderedFrameRef.current = false;
            progressEventCountRef.current = 0;
            firstProgressTimeRef.current = null;
            setSourceKey((prev) => prev + 1);
            return; // Don't emit progress for this stuck event
          }
        }

        const meta: VideoProgressMeta = {
          playable: data.playableDuration,
          seekable: data.seekableDuration,
        };
        onProgress(data.currentTime, meta);
      },
      [onProgress],
    );

    const handleBuffer = useCallback(
      ({ isBuffering }: { isBuffering: boolean }) => {
        onBuffer(isBuffering);
      },
      [onBuffer],
    );

    const handleReadyForDisplay = useCallback(() => {
      console.log('[RNVideoPlayer] onReadyForDisplay - video frame rendered');
      hasRenderedFrameRef.current = true;
      // Ensure buffering is cleared when video is ready to display
      onBuffer(false);
    }, [onBuffer]);

    const handlePlaybackStateChanged = useCallback((state: { isPlaying: boolean; isSeeking: boolean }) => {
      console.log('[RNVideoPlayer] playback state changed', state);

      // Track when playback starts for stuck detection
      if (state.isPlaying && !state.isSeeking && playbackStartedAtRef.current === null) {
        playbackStartedAtRef.current = Date.now();
        console.log('[RNVideoPlayer] playback started, monitoring for stuck state');
      }
    }, []);

    const handleVideoTracks = useCallback((data: { videoTracks: any[] }) => {
      console.log('[RNVideoPlayer] video tracks available', {
        count: data.videoTracks?.length ?? 0,
        tracks: data.videoTracks,
      });
    }, []);

    const handleAspectRatio = useCallback((data: { width: number; height: number }) => {
      console.log('[RNVideoPlayer] aspect ratio', data);
    }, []);

    const handlePlaybackRateChange = useCallback((data: { playbackRate: number }) => {
      console.log('[RNVideoPlayer] playback rate changed', data);
    }, []);

    const handleReceiveAdEvent = useCallback((data: any) => {
      console.log('[RNVideoPlayer] received ad event', data);
    }, []);

    const handleEnd = useCallback(() => {
      onEnd();
    }, [onEnd]);

    const handleError = useCallback(
      (error: any) => {
        console.error('[RNVideoPlayer] error:', error);
        onError?.(error);
      },
      [onError],
    );

    const handleFullscreenPlayerWillPresent = useCallback(() => {
      setIsFullscreen(true);
      onToggleFullscreen?.();
    }, [onToggleFullscreen]);

    const handleFullscreenPlayerDidPresent = useCallback(() => {}, []);

    const handleFullscreenPlayerWillDismiss = useCallback(() => {}, []);

    const handleFullscreenPlayerDidDismiss = useCallback(() => {
      setIsFullscreen(false);
      onToggleFullscreen?.();
      // Notify parent when HDR fullscreen is exited
      if (forceNativeFullscreen && onNativeFullscreenExit) {
        onNativeFullscreenExit();
      }
    }, [forceNativeFullscreen, onNativeFullscreenExit, onToggleFullscreen]);

    useImperativeHandle(
      ref,
      () => ({
        seek: (seconds: number) => {
          videoRef.current?.seek(seconds);
        },
        play: () => {},
        pause: () => {},
        toggleFullscreen: () => {
          if (isFullscreen) {
            videoRef.current?.dismissFullscreenPlayer();
          } else {
            videoRef.current?.presentFullscreenPlayer();
          }
        },
      }),
      [isFullscreen],
    );

    if (!movie) {
      return <Pressable style={styles.container} onPress={onInteract} tvParallaxProperties={{ enabled: false }} />;
    }

    // Build source object - AVPlayer on iOS/tvOS auto-detects HLS from .m3u8 extension
    // Don't specify type as it can cause issues with react-native-video
    const source = {
      uri: movie,
      metadata: nowPlaying
        ? {
            title: nowPlaying.title,
            subtitle: nowPlaying.subtitle,
            artist: nowPlaying.artist,
            imageUri: nowPlaying.imageUri,
          }
        : undefined,
    };

    // Build selected tracks
    const selectedAudioTrack: SelectedTrack | undefined =
      typeof selectedAudioTrackIndex === 'number'
        ? { type: SelectedTrackType.INDEX, value: selectedAudioTrackIndex }
        : undefined;

    const selectedTextTrack: SelectedTrack =
      typeof selectedSubtitleTrackIndex === 'number'
        ? { type: SelectedTrackType.INDEX, value: selectedSubtitleTrackIndex }
        : { type: SelectedTrackType.DISABLED, value: undefined };

    return (
      <Pressable style={styles.container} onPress={onInteract} tvParallaxProperties={{ enabled: false }}>
        <View style={styles.videoContainer} pointerEvents="box-none">
          <Video
            key={`rnv-player-${sourceKey}`}
            ref={videoRef}
            source={source}
            style={styles.video}
            paused={paused}
            volume={resolvedVolume}
            controls={controls}
            resizeMode="contain"
            progressUpdateInterval={250}
            onLoad={handleLoad}
            onProgress={handleProgress}
            onBuffer={handleBuffer}
            onEnd={handleEnd}
            onError={handleError}
            onFullscreenPlayerWillPresent={handleFullscreenPlayerWillPresent}
            onFullscreenPlayerDidPresent={handleFullscreenPlayerDidPresent}
            onFullscreenPlayerWillDismiss={handleFullscreenPlayerWillDismiss}
            onFullscreenPlayerDidDismiss={handleFullscreenPlayerDidDismiss}
            onReadyForDisplay={handleReadyForDisplay}
            onPlaybackStateChanged={handlePlaybackStateChanged}
            onVideoTracks={handleVideoTracks}
            onAspectRatio={handleAspectRatio}
            onPlaybackRateChange={handlePlaybackRateChange}
            selectedAudioTrack={selectedAudioTrack}
            selectedTextTrack={selectedTextTrack}
            // HDR-related props
            allowsExternalPlayback={true}
            automaticallyWaitsToMinimizeStalling={true}
            preferredForwardBufferDuration={30}
            // Now Playing / Control Center integration
            showNotificationControls={true}
            playInBackground={true}
            playWhenInactive={true}
            // Suppress "LIVE" indicator on Android TV for HLS streams
            controlsStyles={{ liveLabel: '' }}
          />
        </View>
      </Pressable>
    );
  },
);

RNVideoPlayer.displayName = 'RNVideoPlayer';

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#000',
  },
  videoContainer: {
    flex: 1,
  },
  video: {
    position: 'absolute',
    top: 0,
    left: 0,
    bottom: 0,
    right: 0,
    backgroundColor: '#000',
  },
});

export default RNVideoPlayer;
