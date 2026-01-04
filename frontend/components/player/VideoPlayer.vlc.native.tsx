import React, { useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react';
import { Image, Platform, Pressable, StyleSheet, View } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';
// @ts-ignore - VLC player is only available on native platforms
import { VLCPlayer, type VideoInfo, type VLCPlayerSource } from 'react-native-vlc-media-player';

import type { VideoPlayerHandle, VideoPlayerProps } from './types';

const VlcVideoPlayerInner = (
  {
    movie,
    headerImage,
    movieTitle,
    paused,
    controls: _controls,
    onBuffer,
    onProgress,
    onLoad,
    onEnd,
    onError,
    durationHint,
    onInteract,
    volume = 1,
    onAutoplayBlocked: _onAutoplayBlocked,
    onToggleFullscreen: _onToggleFullscreen,
    selectedAudioTrackIndex,
    selectedSubtitleTrackIndex: _selectedSubtitleTrackIndex,
    onTracksAvailable,
    onVideoSize,
    mediaType,
    nowPlaying,
    subtitleSize = 1.0,
    resizeMode = 'cover',
  }: VideoPlayerProps,
  ref: React.ForwardedRef<VideoPlayerHandle>,
) => {
  const { width, height } = useTVDimensions();
  const styles = useVideoPlayerStyles(width, height, mediaType);
  const videoRef = useRef<any>(null);
  const lastDurationRef = useRef<number>(0);
  const mediaDurationRef = useRef<number>(0);

  const lastProgressLogRef = useRef<{
    currentBucket: number | null;
    parsedDuration: number | null;
    rawDuration: number | null;
    rawCurrentTime: number | null;
  }>({
    currentBucket: null,
    parsedDuration: null,
    rawDuration: null,
    rawCurrentTime: null,
  });

  // Debug: track progress event count for diagnosing start position issues
  const progressEventCountRef = useRef(0);
  const firstProgressTimeRef = useRef<number | null>(null);
  // Track last valid time to detect VLC's bogus time reports during HLS buffering
  const lastValidTimeRef = useRef<{ time: number; realTime: number } | null>(null);
  const hasFinishedRef = useRef<boolean>(false);
  const normalizationLogRef = useRef({
    load: false,
    progressDuration: false,
    progressCurrentTime: false,
  });
  const [hasRenderedFirstFrame, setHasRenderedFirstFrame] = useState(false);
  const [videoAspectRatio, setVideoAspectRatio] = useState<number | null>(null);
  const [isVideoLandscape, setIsVideoLandscape] = useState<boolean | null>(null);
  const [tracksLoaded, setTracksLoaded] = useState(false);
  const [appliedAudioTrack, setAppliedAudioTrack] = useState<number | undefined>(undefined);
  const [isPlaying, setIsPlaying] = useState(false);
  // Counter to force re-applying textTrack=-1 when tracks become available
  // React won't re-send the same prop value, so we toggle to undefined then back to -1
  const [subtitleDisableCounter, setSubtitleDisableCounter] = useState(0);
  const [effectiveTextTrack, setEffectiveTextTrack] = useState<number | undefined>(-1);
  const resolvedVolume = useMemo(() => {
    const numericVolume = Number(volume);
    if (!Number.isFinite(numericVolume)) {
      return 1;
    }
    if (numericVolume <= 0) {
      return 0;
    }
    if (numericVolume >= 1) {
      return 1;
    }
    return numericVolume;
  }, [volume]);

  // Use resizeMode prop (defaults to 'cover' for fullscreen, 'contain' for multiscreen)
  const optimalResizeMode = resizeMode;

  // Don't use aspect ratio override - let cover mode scale properly
  const optimalAspectRatio = undefined;

  const resolvedMovie = movie;

  // Use ref to store mutable source object (VLC native code mutates it)
  const videoSourceRef = useRef<VLCPlayerSource>({ uri: '' });

  const nextVideoSource = useMemo<VLCPlayerSource>(() => {
    // Use higher buffer values for TV devices (Fire Stick, Apple TV) to reduce jitter
    // Android TV needs larger buffers (8s) compared to Apple TV (4s) for smooth playback
    const isAndroidTV = Platform.isTV && Platform.OS === 'android';
    const cachingValue = isAndroidTV ? '8000' : Platform.isTV ? '4000' : '2000';
    const initOptions = [
      '--http-reconnect',
      `--network-caching=${cachingValue}`,
      `--file-caching=${cachingValue}`,
      // Disable VLC's built-in subtitles by default - we use SubtitleOverlay for consistent sizing
      '--sub-track=-1',
    ];

    // Configure subtitle size with user scaling
    // Platform base values: tvOS = 60, others = 100 (VLC default)
    const baseScale = Platform.isTV && Platform.OS === 'ios' ? 60 : 100;
    const scaledValue = Math.round(baseScale * subtitleSize);
    initOptions.push(`--sub-text-scale=${scaledValue}`);

    // For tvOS, also set relative font size (scales with subtitleSize from base of 10)
    if (Platform.isTV && Platform.OS === 'ios') {
      const baseFreetypeSize = 10;
      const scaledFreetypeSize = Math.round(baseFreetypeSize * subtitleSize);
      initOptions.push(`--freetype-rel-fontsize=${scaledFreetypeSize}`);
    }

    // Note: VLC cannot override ASS/SSA embedded font sizes - only SRT and plain text formats
    // are affected by sub-text-scale and freetype-rel-fontsize options

    return {
      uri: resolvedMovie ?? '',
      // Use initType 2 to ensure initOptions are applied (initType 1 ignores initOptions in native code)
      initType: 2 as 2,
      initOptions,
    };
  }, [resolvedMovie, subtitleSize]);

  // Update mutable source ref synchronously so the latest URI is used during render
  videoSourceRef.current = nextVideoSource;

  useEffect(() => {
    lastDurationRef.current = 0;
    mediaDurationRef.current = 0;
    hasFinishedRef.current = false;
    normalizationLogRef.current = {
      load: false,
      progressDuration: false,
      progressCurrentTime: false,
    };
    // Reset progress event tracking for start position debugging
    progressEventCountRef.current = 0;
    firstProgressTimeRef.current = null;
    lastValidTimeRef.current = null;
    setHasRenderedFirstFrame(false);
    setTracksLoaded(false);
    setAppliedAudioTrack(undefined);
    setIsPlaying(false);
    // Reset subtitle disable state - will be re-applied when new tracks load
    setSubtitleDisableCounter(0);
    setEffectiveTextTrack(-1);
    onBuffer(true);

    // Use duration hint if provided (from API metadata, more reliable for HLS streams)
    if (durationHint && Number.isFinite(durationHint) && durationHint > 0) {
      mediaDurationRef.current = durationHint;
      lastDurationRef.current = durationHint;
      onLoad(durationHint);
    }
  }, [movie, durationHint, onLoad]);

  // Build Now Playing info object to pass to VLCPlayer
  const nowPlayingInfo = useMemo(() => {
    if (!nowPlaying) {
      return undefined;
    }
    return {
      title: nowPlaying.title,
      subtitle: nowPlaying.subtitle,
      artist: nowPlaying.artist,
      imageUri: nowPlaying.imageUri,
    };
  }, [nowPlaying]);

  useImperativeHandle(
    ref,
    () => ({
      seek: (seconds: number) => {
        if (!videoRef.current || !Number.isFinite(mediaDurationRef.current) || mediaDurationRef.current <= 0) {
          return;
        }
        const clampedSeconds = Math.max(0, Math.min(seconds, mediaDurationRef.current));
        const progress = clampedSeconds / mediaDurationRef.current;
        videoRef.current.seek(progress);
      },
      play: () => {
        if (!videoRef.current) {
          return;
        }
        try {
          if (typeof videoRef.current.resume === 'function') {
            videoRef.current.resume();
          } else if (typeof videoRef.current.play === 'function') {
            videoRef.current.play();
          }
        } catch {
          // Silently ignore play failures
        }
      },
      pause: () => {
        if (!videoRef.current) {
          return;
        }
        try {
          if (typeof videoRef.current.pause === 'function') {
            videoRef.current.pause();
          }
        } catch {
          // Silently ignore pause failures
        }
      },
    }),
    [],
  );

  const handleVideoError = useCallback(
    (error: unknown) => {
      console.error('🚨 VLCVideoPlayer - Video Error:', error);
      console.error('🚨 Video source that failed:', videoSourceRef.current);

      if (typeof error === 'object' && error) {
        console.error('🚨 Error details:', JSON.stringify(error, null, 2));
      }

      onError?.(error);
    },
    [onError],
  );

  const normalizeVlcTime = useCallback((value: number) => {
    if (!Number.isFinite(value) || value <= 0) {
      return 0;
    }

    const maximumReasonableSeconds = 24 * 60 * 60; // 24 hours
    if (value > maximumReasonableSeconds && value / 1000 <= maximumReasonableSeconds) {
      return value / 1000;
    }

    return value;
  }, []);

  const handleLoad = useCallback(
    (info: VideoInfo) => {
      const rawDuration = Number(info.duration) || 0;
      const durationSeconds = normalizeVlcTime(rawDuration);

      // Calculate video aspect ratio and orientation
      const videoWidth = info.videoSize?.width;
      const videoHeight = info.videoSize?.height;
      const aspectRatio = videoWidth && videoHeight ? videoWidth / videoHeight : null;
      const isVideoLandscape = aspectRatio ? aspectRatio > 1 : null;
      const isScreenPortrait = height >= width;

      // Always use cover mode
      const optimalResizeMode = 'cover';

      // Store video info for use in render
      setVideoAspectRatio(aspectRatio);
      setIsVideoLandscape(isVideoLandscape);

      // Report video dimensions for subtitle positioning
      if (videoWidth && videoHeight) {
        onVideoSize?.(videoWidth, videoHeight);
      }

      // Calculate expected scaling behavior
      const screenAspectRatio = width / height;
      const videoAspectRatioValue = aspectRatio || 0;
      const scalingAnalysis = {
        videoFitsWidth: videoAspectRatioValue <= screenAspectRatio,
        videoFitsHeight: videoAspectRatioValue >= screenAspectRatio,
        expectedBlackBars: {
          horizontal: videoAspectRatioValue > screenAspectRatio ? 'top/bottom' : 'none',
          vertical: videoAspectRatioValue < screenAspectRatio ? 'left/right' : 'none',
        },
        withCoverMode: {
          willCrop: videoAspectRatioValue !== screenAspectRatio,
          cropDirection: videoAspectRatioValue > screenAspectRatio ? 'top/bottom' : 'left/right',
        },
      };

      if (!Number.isFinite(durationSeconds) || durationSeconds <= 0) {
        // Even if VLC reports invalid duration, we can still use durationHint if available
        if (durationHint && Number.isFinite(durationHint) && durationHint > 0) {
          mediaDurationRef.current = durationHint;
          if (durationHint !== lastDurationRef.current) {
            lastDurationRef.current = durationHint;
            onLoad(durationHint);
          }
        }
        return;
      }

      // Always prefer durationHint from API metadata - it's authoritative.
      // VLC's reported duration for HLS streams can fluctuate as content buffers.
      const effectiveDuration =
        durationHint && Number.isFinite(durationHint) && durationHint > 0 ? durationHint : durationSeconds;

      mediaDurationRef.current = effectiveDuration;

      if (effectiveDuration !== lastDurationRef.current) {
        if (durationSeconds !== rawDuration) {
          normalizationLogRef.current.load = true;
        }
        lastDurationRef.current = effectiveDuration;
        onLoad(effectiveDuration);
      }

      // Report available audio and subtitle tracks
      if (onTracksAvailable && (info.audioTracks?.length > 0 || info.textTracks?.length > 0)) {
        onTracksAvailable(info.audioTracks || [], info.textTracks || []);
        setTracksLoaded(true);
        // Force re-apply textTrack=-1 after tracks load to ensure VLC subtitles are disabled
        // VLC may auto-select a subtitle track when loading, so we need to explicitly disable again
        setSubtitleDisableCounter((c) => c + 1);
      }
    },
    [normalizeVlcTime, onLoad, onTracksAvailable, onVideoSize, width, height, durationHint],
  );

  const handleProgress = useCallback(
    (event: { currentTime: number; duration: number }) => {
      const rawCurrentTime = Number(event.currentTime) || 0;
      const rawDuration = Number(event.duration) || 0;
      const durationSeconds = normalizeVlcTime(rawDuration);
      const durationWasNormalizedFromMilliseconds = rawDuration > 0 && durationSeconds !== rawDuration;

      let currentTimeSeconds = normalizeVlcTime(rawCurrentTime);

      if (durationWasNormalizedFromMilliseconds && currentTimeSeconds === rawCurrentTime) {
        currentTimeSeconds = rawCurrentTime / 1000;
      }

      // Use durationHint to detect milliseconds when VLC reports duration as 0
      // If currentTime exceeds the known duration but dividing by 1000 gives
      // a reasonable value, it's likely VLC is reporting in milliseconds
      if (
        durationHint &&
        durationHint > 0 &&
        currentTimeSeconds === rawCurrentTime && // wasn't normalized yet
        currentTimeSeconds > durationHint && // past expected duration (impossible if seconds)
        rawCurrentTime / 1000 <= durationHint // but ms conversion is reasonable
      ) {
        currentTimeSeconds = rawCurrentTime / 1000;
      }

      // VLC can report bogus time values during HLS initial buffering (jumping hundreds of seconds).
      // Detect and ignore impossible time jumps: if playback time increases faster than 5x real time,
      // it's clearly wrong. Use last valid time instead.
      const now = Date.now();
      const lastValid = lastValidTimeRef.current;
      if (lastValid !== null && currentTimeSeconds > 0) {
        const realElapsed = (now - lastValid.realTime) / 1000; // seconds of real time
        const playbackElapsed = currentTimeSeconds - lastValid.time; // seconds of playback time
        // Allow up to 5x playback speed (very generous) or small absolute jumps (< 2s)
        const maxReasonableJump = Math.max(realElapsed * 5, 2);
        if (playbackElapsed > maxReasonableJump) {
          // Bogus value - ignore this update entirely
          return;
        }
      }
      // Update last valid time
      lastValidTimeRef.current = { time: currentTimeSeconds, realTime: now };

      const progressBucket = Math.floor(currentTimeSeconds / 10);
      const previousProgressLog = lastProgressLogRef.current;
      if (
        progressBucket !== previousProgressLog.currentBucket ||
        durationSeconds !== previousProgressLog.parsedDuration ||
        rawDuration !== previousProgressLog.rawDuration ||
        rawCurrentTime !== previousProgressLog.rawCurrentTime
      ) {
        lastProgressLogRef.current = {
          currentBucket: progressBucket,
          parsedDuration: durationSeconds,
          rawDuration,
          rawCurrentTime,
        };

        if (durationSeconds !== rawDuration && !normalizationLogRef.current.progressDuration) {
          normalizationLogRef.current.progressDuration = true;
        }
        if (currentTimeSeconds !== rawCurrentTime && !normalizationLogRef.current.progressCurrentTime) {
          normalizationLogRef.current.progressCurrentTime = true;
        }
      }

      if (Number.isFinite(durationSeconds) && durationSeconds > 0) {
        // Always prefer durationHint from API metadata - it's authoritative.
        // VLC's reported duration for HLS streams can fluctuate as content buffers,
        // causing the seek bar to "rapidly grow". Only fall back to VLC's value
        // if we don't have a hint.
        const effectiveDuration =
          durationHint && Number.isFinite(durationHint) && durationHint > 0 ? durationHint : durationSeconds;

        mediaDurationRef.current = effectiveDuration;

        if (effectiveDuration !== lastDurationRef.current) {
          lastDurationRef.current = effectiveDuration;
          if (effectiveDuration > 3 * 60 * 60) {
            console.warn('⏱️ VLC duration unusually long, check metadata:', effectiveDuration);
          }
          onLoad(effectiveDuration);
        }
      }

      onBuffer(false);

      // Track first progress event for diagnostics
      const eventCount = progressEventCountRef.current;
      progressEventCountRef.current = eventCount + 1;
      if (eventCount === 0) {
        firstProgressTimeRef.current = currentTimeSeconds;
      }

      onProgress(currentTimeSeconds);
    },
    [normalizeVlcTime, onBuffer, onProgress, onLoad, durationHint],
  );

  const handleBuffering = useCallback(() => {
    onBuffer(true);
  }, [onBuffer]);

  const handlePlaying = useCallback(() => {
    hasFinishedRef.current = false;
    setIsPlaying(true);
    onBuffer(false);
  }, [onBuffer]);

  const handleEnd = useCallback(() => {
    if (hasFinishedRef.current) {
      return;
    }

    hasFinishedRef.current = true;
    onEnd();
  }, [onEnd]);

  // Force re-apply textTrack=-1 when subtitleDisableCounter changes
  // This ensures VLC subtitles are disabled even if VLC auto-selected one on load
  useEffect(() => {
    if (subtitleDisableCounter === 0) {
      return; // Skip initial render
    }
    // Briefly set to undefined to force React to see a change, then back to -1
    setEffectiveTextTrack(undefined);
    const timer = setTimeout(() => {
      setEffectiveTextTrack(-1);
    }, 50);
    return () => clearTimeout(timer);
  }, [subtitleDisableCounter]);

  // Apply audio track selection after tracks are loaded AND playback has started
  // Note: VLC subtitles are always disabled (textTrack={-1}) - we use SubtitleOverlay instead
  useEffect(() => {
    if (!tracksLoaded || !isPlaying) {
      return;
    }

    const audioNeedsUpdate = appliedAudioTrack !== selectedAudioTrackIndex;

    if (audioNeedsUpdate) {
      // Delay track application to let VLC stabilize playback first
      const timer = setTimeout(() => {
        setAppliedAudioTrack(selectedAudioTrackIndex ?? undefined);
      }, 500); // Wait 500ms after playback starts before applying tracks

      return () => clearTimeout(timer);
    }
  }, [
    tracksLoaded,
    isPlaying,
    selectedAudioTrackIndex,
    appliedAudioTrack,
  ]);

  return (
    <Pressable onPress={onInteract} style={styles.root} tvParallaxProperties={{ enabled: false }}>
      <View style={styles.videoContainer} pointerEvents="box-none">
        <VLCPlayer
          key={`vlc-player-${resolvedMovie}`}
          ref={videoRef}
          source={videoSourceRef.current}
          style={styles.video}
          paused={paused}
          autoplay={true}
          muted={resolvedVolume <= 0}
          volume={Math.round(resolvedVolume * 200)}
          autoAspectRatio={false}
          videoAspectRatio={optimalAspectRatio}
          resizeMode={optimalResizeMode}
          audioTrack={appliedAudioTrack}
          // Always disable VLC subtitles - we use SubtitleOverlay for consistent sizing across platforms
          // effectiveTextTrack toggles to force re-apply when tracks become available
          textTrack={effectiveTextTrack}
          // @ts-ignore - nowPlayingInfo is added via patch
          nowPlayingInfo={nowPlayingInfo}
          onError={handleVideoError}
          onLoad={(info) => {
            setHasRenderedFirstFrame(true);
            handleLoad(info);
          }}
          onProgress={(event) => {
            setHasRenderedFirstFrame(true);
            handleProgress(event);
          }}
          onBuffering={handleBuffering}
          onPlaying={() => {
            setHasRenderedFirstFrame(true);
            handlePlaying();
          }}
          onEnd={handleEnd}
        />
        {!hasRenderedFirstFrame &&
          (headerImage ? (
            <View pointerEvents="none" style={styles.poster}>
              <Image source={{ uri: headerImage }} style={styles.posterImage} resizeMode="contain" />
            </View>
          ) : (
            <View pointerEvents="none" style={styles.placeholder} />
          ))}
      </View>
    </Pressable>
  );
};

const VlcVideoPlayer = React.forwardRef(VlcVideoPlayerInner) as React.ForwardRefExoticComponent<
  VideoPlayerProps & React.RefAttributes<VideoPlayerHandle>
>;

VlcVideoPlayer.displayName = 'VlcVideoPlayer';

const useVideoPlayerStyles = (screenWidth: number, screenHeight: number, mediaType?: string) => {
  return useMemo(() => {
    const isLiveChannel = mediaType === 'channel';

    return StyleSheet.create({
      root: {
        flex: 1,
        alignSelf: 'stretch',
        justifyContent: 'center',
        alignItems: 'stretch',
        backgroundColor: '#000',
      },
      videoContainer: {
        flex: 1,
        backgroundColor: '#000',
        position: 'relative',
        overflow: Platform.OS === 'ios' ? 'hidden' : 'visible',
      },
      video: {
        // Use absolute positioning to fill container - works correctly with resizeMode
        // This allows the video to properly scale within multiscreen containers
        position: 'absolute',
        top: 0,
        left: 0,
        bottom: 0,
        right: 0,
        // Note: backgroundColor not supported by RCTVLCPlayer on Android
        // Parent containers (root, videoContainer) provide black background
      },
      poster: {
        ...StyleSheet.absoluteFillObject,
        justifyContent: 'center',
        alignItems: 'center',
      },
      posterImage: {
        width: isLiveChannel ? '75%' : '100%',
        height: isLiveChannel ? '75%' : '100%',
      },
      placeholder: {
        ...StyleSheet.absoluteFillObject,
        backgroundColor: '#000',
      },
    });
  }, [mediaType]);
};

export default VlcVideoPlayer;
