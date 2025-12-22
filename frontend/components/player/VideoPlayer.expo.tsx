import {
  type ComponentRef,
  forwardRef,
  useCallback,
  useEffect,
  useImperativeHandle,
  useMemo,
  useRef,
  useState,
} from 'react';
import { Image, Platform, Pressable, StyleSheet, useWindowDimensions, View } from 'react-native';

import { type VideoPlayer as ExpoVideoPlayerInstance, useVideoPlayer, VideoView } from 'expo-video';

import type { VideoPlayerHandle, VideoPlayerProps, VideoProgressMeta } from './types';

const ExpoVideoPlayer = forwardRef<VideoPlayerHandle, VideoPlayerProps>(
  (
    {
      movie,
      headerImage,
      movieTitle: _movieTitle,
      paused,
      controls,
      onBuffer,
      onProgress,
      onLoad,
      onEnd,
      onError,
      durationHint: _durationHint,
      onInteract,
      volume = 1,
      onAutoplayBlocked: _onAutoplayBlocked,
      onToggleFullscreen,
      selectedAudioTrackIndex,
      selectedSubtitleTrackIndex,
      onTracksAvailable,
      mediaType,
    },
    ref,
  ) => {
    const { width, height } = useWindowDimensions();
    const styles = useVideoPlayerStyles(width, height, mediaType);
    const videoRef = useRef<ComponentRef<typeof VideoView>>(null);
    const lastDurationRef = useRef<number>(0);
    const rawDurationRef = useRef<number>(0);
    const timeScaleRef = useRef<1 | 0.001>(1);
    const scaleDetectionRef = useRef({
      detected: false,
      mode: 1 as 1 | 0.001,
      lastRawTime: null as number | null,
      largeDeltaCount: 0,
      smallDeltaCount: 0,
    });
    const hasFinishedRef = useRef<boolean>(false);
    const [isFullscreen, setIsFullscreen] = useState(false);
    const [hasRenderedFirstFrame, setHasRenderedFirstFrame] = useState(false);
    const normaliseTimeValue = useCallback((value: number | null | undefined): number => {
      const numeric = Number(value);
      if (!Number.isFinite(numeric) || numeric < 0) {
        return 0;
      }
      return numeric * timeScaleRef.current;
    }, []);

    const emitDurationIfNeeded = useCallback(
      (rawValue?: number) => {
        if (!onLoad) {
          return;
        }

        const candidate = typeof rawValue === 'number' ? rawValue : rawDurationRef.current;
        if (!Number.isFinite(candidate) || candidate <= 0) {
          return;
        }

        rawDurationRef.current = candidate;

        const normalised = normaliseTimeValue(candidate);
        if (!Number.isFinite(normalised) || normalised <= 0) {
          return;
        }

        if (Math.abs(normalised - lastDurationRef.current) < 0.001) {
          return;
        }

        lastDurationRef.current = normalised;
        onLoad(normalised);
      },
      [normaliseTimeValue, onLoad],
    );

    const handleTimeScaleDetection = useCallback(
      (rawTime: number | null | undefined) => {
        const numeric = Number(rawTime);
        if (!Number.isFinite(numeric) || numeric < 0) {
          return;
        }

        const state = scaleDetectionRef.current;

        if (!state.detected) {
          if (state.lastRawTime !== null) {
            const delta = Math.abs(numeric - state.lastRawTime);
            if (delta >= 50 && delta <= 4000) {
              state.largeDeltaCount += 1;
              state.smallDeltaCount = 0;
              if (state.largeDeltaCount >= 3) {
                state.detected = true;
                state.mode = 0.001;
                timeScaleRef.current = state.mode;
                console.debug('🎬 ExpoVideoPlayer - detected millisecond time scale');
                lastDurationRef.current = 0;
                emitDurationIfNeeded();
              }
            } else if (delta > 0 && delta <= 1.5) {
              state.smallDeltaCount += 1;
              state.largeDeltaCount = 0;
              if (state.smallDeltaCount >= 3) {
                state.detected = true;
                state.mode = 1;
                timeScaleRef.current = state.mode;
                lastDurationRef.current = 0;
                emitDurationIfNeeded();
              }
            } else if (delta > 0) {
              state.largeDeltaCount = 0;
              state.smallDeltaCount = 0;
            }
          }
          state.lastRawTime = numeric;
        } else {
          state.lastRawTime = numeric;
          if (timeScaleRef.current !== state.mode) {
            timeScaleRef.current = state.mode;
          }
        }
      },
      [emitDurationIfNeeded],
    );
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

    const videoSource = useMemo(() => {
      if (!movie) {
        return null;
      }

      const sourceString = String(movie).trim();
      if (!sourceString) {
        return null;
      }

      try {
        const parsedUrl = new URL(sourceString);
        console.log('🎬 ExpoVideoPlayer - resolved URL', {
          href: parsedUrl.href,
          origin: parsedUrl.origin,
          path: parsedUrl.pathname + parsedUrl.search,
        });

        // Check if this is an HLS stream
        const isHls =
          parsedUrl.pathname.includes('/video/hls/') ||
          parsedUrl.pathname.endsWith('.m3u8') ||
          parsedUrl.pathname.endsWith('.m3u');

        if (isHls) {
          console.log('🎬 ExpoVideoPlayer - detected HLS stream, setting contentType');
          // For HLS streams, use VideoSource object with contentType
          // This ensures proper track detection on iOS
          return {
            uri: sourceString,
            contentType: 'hls' as const,
          };
        }

        return sourceString;
      } catch (error) {
        console.warn('🎬 ExpoVideoPlayer - invalid URL provided', error);
        return null;
      }
    }, [movie]);

    useEffect(() => {
      if (!videoSource) {
        console.warn('🎬 ExpoVideoPlayer - no playable movie source provided');
        onBuffer(false);
      }
    }, [onBuffer, videoSource]);

    useEffect(() => {
      lastDurationRef.current = 0;
      rawDurationRef.current = 0;
      timeScaleRef.current = 1;
      scaleDetectionRef.current = {
        detected: false,
        mode: 1 as 1 | 0.001,
        lastRawTime: null,
        largeDeltaCount: 0,
        smallDeltaCount: 0,
      };
      hasFinishedRef.current = false;
      setHasRenderedFirstFrame(false);
    }, [movie, videoSource]);

    const setupVideoPlayer = useCallback(
      (createdPlayer: ExpoVideoPlayerInstance) => {
        createdPlayer.timeUpdateEventInterval = 0.1;
        try {
          createdPlayer.volume = resolvedVolume;
          createdPlayer.muted = resolvedVolume <= 0;
        } catch (error) {
          console.warn('Warning: unable to set initial Expo player volume state', error);
        }

        // Note: Initial pause state is handled by the useEffect below (lines 331-345)
        // to avoid recreating the player instance on every pause/resume toggle
      },
      [resolvedVolume],
    );

    // Only create player when we have a valid video source
    // Passing null to useVideoPlayer on Android causes: "createMediaSource(...) must not be null"
    const player = useVideoPlayer(videoSource, setupVideoPlayer);

    useEffect(() => {
      if (!player) {
        return;
      }

      try {
        player.volume = resolvedVolume;
        player.muted = resolvedVolume <= 0;
      } catch (error) {
        console.warn('Warning: unable to update Expo player volume state', error);
      }
    }, [player, resolvedVolume]);

    const toggleFullscreen = useCallback(async () => {
      const view = videoRef.current;
      if (!view) {
        return;
      }

      try {
        if (!isFullscreen && typeof view.enterFullscreen === 'function') {
          await view.enterFullscreen();
        } else if (isFullscreen && typeof view.exitFullscreen === 'function') {
          await view.exitFullscreen();
        }
      } catch (error) {
        console.warn('Warning: unable to toggle Expo player fullscreen', error);
      }
    }, [isFullscreen]);

    useImperativeHandle(
      ref,
      () => ({
        seek: (seconds: number) => {
          console.log('[ExpoVideoPlayer] seek called', {
            seconds,
            hasPlayer: !!player,
            currentTime: player?.currentTime,
            duration: player?.duration,
            timeScale: timeScaleRef.current,
          });
          if (!player) {
            console.warn('[ExpoVideoPlayer] seek called but player is null');
            return;
          }
          if (!Number.isFinite(seconds)) {
            console.warn('[ExpoVideoPlayer] seek called with non-finite time', { seconds });
            return;
          }
          const clampedSeconds = Math.max(0, seconds);
          const scale = timeScaleRef.current > 0 ? timeScaleRef.current : 1;
          const targetRawTime = clampedSeconds / scale;
          const rawDuration = Number(player.duration);
          const clampedRawTime =
            Number.isFinite(rawDuration) && rawDuration > 0 ? Math.min(targetRawTime, rawDuration) : targetRawTime;
          console.log('[ExpoVideoPlayer] attempting to set currentTime', {
            from: player.currentTime,
            to: clampedRawTime,
          });
          try {
            player.currentTime = clampedRawTime;
            console.log('[ExpoVideoPlayer] currentTime set successfully', { newCurrentTime: player.currentTime });
          } catch (error) {
            console.error('🚨 [ExpoVideoPlayer] Failed to seek video:', error);
          }
        },
        play: () => {
          if (!player) {
            return;
          }

          try {
            player.play();
          } catch (error) {
            console.warn('Warning: unable to trigger Expo player play()', error);
          }
        },
        pause: () => {
          if (!player) {
            return;
          }

          try {
            player.pause();
          } catch (error) {
            console.warn('Warning: unable to trigger Expo player pause()', error);
          }
        },
        toggleFullscreen,
      }),
      [player, toggleFullscreen],
    );

    useEffect(() => {
      if (!player) {
        return;
      }

      try {
        if (paused) {
          player.pause();
        } else {
          player.play();
        }
      } catch (error) {
        console.error('🚨 Failed to toggle playback:', error);
      }
    }, [paused, player]);

    const handleVideoError = useCallback(
      (error: unknown) => {
        console.error('🚨 ExpoVideoPlayer - Video Error:', error);
        console.error('🚨 Video source that failed:', videoSource);

        if (error && typeof error === 'object') {
          console.error('🚨 Error details:', JSON.stringify(error));
        }

        onError?.(error);
      },
      [onError, videoSource],
    );

    useEffect(() => {
      if (!player || !videoSource) {
        return;
      }

      const subscriptions = [
        player.addListener('statusChange', ({ status, error }) => {
          onBuffer(status === 'loading');

          if (status === 'error' && error) {
            handleVideoError(error);
          }

          if (status !== 'error') {
            hasFinishedRef.current = false;
          }

          // Hide poster when video is ready to play (fallback for HLS streams)
          if (status === 'readyToPlay' && !hasRenderedFirstFrame) {
            console.log('🎬 Video ready to play - hiding poster');
            setHasRenderedFirstFrame(true);
          }

          // Notify parent when player is ready (important for initial seeking)
          if (status === 'readyToPlay') {
            console.log('🎬 [ExpoVideoPlayer] readyToPlay status - player is ready for seeks');
          }
        }),
        player.addListener('timeUpdate', ({ currentTime, bufferedPosition }) => {
          handleTimeScaleDetection(currentTime);

          // Hide poster on first time update (fallback for HLS/streaming)
          if (currentTime > 0 && !hasRenderedFirstFrame) {
            console.log('🎬 First time update received - hiding poster');
            setHasRenderedFirstFrame(true);
          }

          const meta: VideoProgressMeta = {};
          const normalisedCurrentTime = normaliseTimeValue(currentTime);

          if (Number.isFinite(bufferedPosition) && bufferedPosition >= 0) {
            const normalisedBuffered = normaliseTimeValue(bufferedPosition);
            if (normalisedBuffered > 0) {
              meta.playable = normalisedBuffered;
              meta.seekable = normalisedBuffered;
            }
          }

          const normalisedDuration = normaliseTimeValue(player.duration);
          if (normalisedDuration > 0) {
            meta.seekable = normalisedDuration;
            if (normalisedDuration - normalisedCurrentTime > 0.75) {
              hasFinishedRef.current = false;
            }
          }

          onProgress(normalisedCurrentTime, Object.keys(meta).length ? meta : undefined);
        }),
        player.addListener('sourceLoad', ({ duration }) => {
          const numericDuration = Number(duration) || 0;
          rawDurationRef.current = numericDuration;
          if (numericDuration > 0) {
            emitDurationIfNeeded(numericDuration);
          }
          hasFinishedRef.current = false;
          onBuffer(false);

          // Report available audio and subtitle tracks
          console.log('🎵 [ExpoVideoPlayer] sourceLoad - checking for tracks', {
            hasOnTracksAvailable: !!onTracksAvailable,
            playerExists: !!player,
          });

          if (onTracksAvailable) {
            const checkAndReportTracks = () => {
              const audioTracks = player.availableAudioTracks || [];
              const subtitleTracks = player.availableSubtitleTracks || [];

              console.log('🎵 [ExpoVideoPlayer] track arrays from player', {
                audioTracksLength: audioTracks.length,
                subtitleTracksLength: subtitleTracks.length,
                audioTracks: JSON.stringify(audioTracks),
                subtitleTracks: JSON.stringify(subtitleTracks),
              });

              if (audioTracks.length > 0 || subtitleTracks.length > 0) {
                console.log('🎵 Expo Video tracks available', {
                  audio: audioTracks.length,
                  subtitles: subtitleTracks.length,
                  audioTracks,
                  subtitleTracks,
                });

                // Convert Expo tracks to our TrackInfo format
                const audioTrackInfos = audioTracks.map((track, index) => ({
                  id: index,
                  name: track.label || track.language || `Audio ${index}`,
                }));

                const subtitleTrackInfos = subtitleTracks.map((track, index) => ({
                  id: index,
                  name: track.label || track.language || `Subtitle ${index}`,
                }));

                onTracksAvailable(audioTrackInfos, subtitleTrackInfos);
                return true;
              } else {
                console.log('⚠️ [ExpoVideoPlayer] No tracks available from player yet');
                return false;
              }
            };

            // Check immediately
            const foundTracks = checkAndReportTracks();

            // If no tracks found, try again after a delay (tracks might load asynchronously)
            if (!foundTracks) {
              setTimeout(() => {
                console.log('🎵 [ExpoVideoPlayer] Delayed track check...');
                checkAndReportTracks();
              }, 500);

              setTimeout(() => {
                console.log('🎵 [ExpoVideoPlayer] Second delayed track check...');
                checkAndReportTracks();
              }, 1500);
            }
          }
        }),
        player.addListener('playToEnd', () => {
          if (!hasFinishedRef.current) {
            hasFinishedRef.current = true;
            console.log('🏁 Video ended');
            onEnd();
          }
        }),
      ];

      return () => {
        subscriptions.forEach((subscription) => {
          try {
            subscription.remove();
          } catch {
            // no-op
          }
        });
      };
    }, [handleVideoError, onBuffer, onEnd, onLoad, onProgress, player, videoSource, onTracksAvailable]);

    // Apply audio track selection
    useEffect(() => {
      if (!player) {
        return;
      }

      const audioTracks = player.availableAudioTracks || [];
      if (audioTracks.length === 0) {
        return;
      }

      // If selectedAudioTrackIndex is provided, apply it
      if (
        typeof selectedAudioTrackIndex === 'number' &&
        selectedAudioTrackIndex >= 0 &&
        selectedAudioTrackIndex < audioTracks.length
      ) {
        const selectedTrack = audioTracks[selectedAudioTrackIndex];
        if (selectedTrack && player.audioTrack !== selectedTrack) {
          console.log('[ExpoVideoPlayer] setting audio track', {
            index: selectedAudioTrackIndex,
            track: selectedTrack,
          });
          try {
            player.audioTrack = selectedTrack;
          } catch (error) {
            console.warn('[ExpoVideoPlayer] failed to set audio track', error);
          }
        }
      }
    }, [player, selectedAudioTrackIndex]);

    // Apply subtitle track selection
    useEffect(() => {
      if (!player) {
        return;
      }

      const subtitleTracks = player.availableSubtitleTracks || [];

      // If selectedSubtitleTrackIndex is null, disable subtitles
      if (selectedSubtitleTrackIndex === null || selectedSubtitleTrackIndex === undefined) {
        console.log('[ExpoVideoPlayer] disabling subtitles');
        try {
          player.subtitleTrack = null;
        } catch (error) {
          console.warn('[ExpoVideoPlayer] failed to disable subtitles', error);
        }
        return;
      }

      if (subtitleTracks.length === 0) {
        return;
      }

      // Apply subtitle track selection
      if (
        typeof selectedSubtitleTrackIndex === 'number' &&
        selectedSubtitleTrackIndex >= 0 &&
        selectedSubtitleTrackIndex < subtitleTracks.length
      ) {
        const selectedTrack = subtitleTracks[selectedSubtitleTrackIndex];
        if (selectedTrack && player.subtitleTrack !== selectedTrack) {
          console.log('[ExpoVideoPlayer] setting subtitle track', {
            index: selectedSubtitleTrackIndex,
            track: selectedTrack,
          });
          try {
            player.subtitleTrack = selectedTrack;
          } catch (error) {
            console.warn('[ExpoVideoPlayer] failed to set subtitle track', error);
          }
        }
      }
    }, [player, selectedSubtitleTrackIndex]);

    if (!videoSource) {
      return (
        <Pressable onPress={onInteract} style={styles.root} tvParallaxProperties={{ enabled: false }}>
          {headerImage ? (
            <View style={styles.poster}>
              <Image source={{ uri: headerImage }} style={styles.posterImage} resizeMode="contain" />
            </View>
          ) : (
            <View style={styles.placeholder} />
          )}
        </Pressable>
      );
    }

    return (
      <Pressable onPress={onInteract} style={styles.root} tvParallaxProperties={{ enabled: false }}>
        <View style={styles.videoContainer} pointerEvents="box-none">
          <VideoView
            ref={videoRef}
            player={player}
            style={styles.video}
            nativeControls={controls}
            contentFit="contain"
            onLayout={(event) => {
              const { width, height } = event.nativeEvent.layout;
              console.debug('🎬 [expo VideoView layout]', { width, height });
            }}
            onFullscreenEnter={() => {
              setIsFullscreen(true);
              onToggleFullscreen?.();
            }}
            onFullscreenExit={() => {
              setIsFullscreen(false);
              onToggleFullscreen?.();
            }}
            onFirstFrameRender={() => {
              setHasRenderedFirstFrame(true);
            }}
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
  },
);

ExpoVideoPlayer.displayName = 'ExpoVideoPlayer';

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
      video: {
        flex: 1,
        alignSelf: 'stretch',
      },
      videoContainer: {
        flex: 1,
        width: '100%',
        height: '100%',
        alignSelf: 'stretch',
        backgroundColor: '#000',
        position: 'relative',
        overflow: Platform.OS === 'ios' ? 'hidden' : 'visible',
        justifyContent: 'center',
        alignItems: 'stretch',
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
  }, [screenHeight, screenWidth]);
};

export default ExpoVideoPlayer;
