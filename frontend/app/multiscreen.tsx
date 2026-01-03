import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { Stack, useLocalSearchParams, useRouter } from 'expo-router';
import { LinearGradient } from 'expo-linear-gradient';
import * as KeepAwake from 'expo-keep-awake';
import { Ionicons } from '@expo/vector-icons';

import { Image } from '@/components/Image';
import { useMultiscreen } from '@/components/MultiscreenContext';
import VideoPlayer from '@/components/player/VideoPlayer';
import type { VideoPlayerHandle } from '@/components/player/types';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import type { MultiscreenChannel } from '@/services/api';

interface LayoutPosition {
  width: number;
  height: number;
  left: number;
  top: number;
  zIndex?: number;
}

// Calculate layout positions for 2-5 screens (normal grid layout)
const getNormalLayoutPositions = (count: number, screenWidth: number, screenHeight: number): LayoutPosition[] => {
  const positions: LayoutPosition[] = [];

  switch (count) {
    case 2:
      // Side-by-side 50/50
      for (let i = 0; i < 2; i++) {
        positions.push({
          width: screenWidth / 2,
          height: screenHeight,
          left: i * (screenWidth / 2),
          top: 0,
        });
      }
      break;

    case 3:
      // 2 on top (60% height), 1 full-width on bottom (40%)
      for (let i = 0; i < 2; i++) {
        positions.push({
          width: screenWidth / 2,
          height: screenHeight * 0.6,
          left: i * (screenWidth / 2),
          top: 0,
        });
      }
      positions.push({
        width: screenWidth,
        height: screenHeight * 0.4,
        left: 0,
        top: screenHeight * 0.6,
      });
      break;

    case 4:
      // 2x2 grid
      for (let i = 0; i < 4; i++) {
        positions.push({
          width: screenWidth / 2,
          height: screenHeight / 2,
          left: (i % 2) * (screenWidth / 2),
          top: Math.floor(i / 2) * (screenHeight / 2),
        });
      }
      break;

    case 5:
      // 3 on top (55% height), 2 on bottom (45%)
      for (let i = 0; i < 3; i++) {
        positions.push({
          width: screenWidth / 3,
          height: screenHeight * 0.55,
          left: i * (screenWidth / 3),
          top: 0,
        });
      }
      for (let i = 0; i < 2; i++) {
        positions.push({
          width: screenWidth / 2,
          height: screenHeight * 0.45,
          left: i * (screenWidth / 2),
          top: screenHeight * 0.55,
        });
      }
      break;

    default:
      // Fallback: single full screen
      positions.push({
        width: screenWidth,
        height: screenHeight,
        left: 0,
        top: 0,
      });
  }

  return positions;
};

// Calculate layout positions with one screen expanded to 80%
const getExpandedLayoutPositions = (
  count: number,
  expandedIndex: number,
  screenWidth: number,
  screenHeight: number,
): LayoutPosition[] => {
  const positions: LayoutPosition[] = [];

  // Expanded screen takes 80% of width and height, centered
  const expandedWidth = screenWidth * 0.8;
  const expandedHeight = screenHeight * 0.8;
  const expandedLeft = (screenWidth - expandedWidth) / 2;
  const expandedTop = (screenHeight - expandedHeight) / 2;

  // Thumbnail dimensions for non-expanded screens
  const thumbWidth = screenWidth * 0.15;
  const thumbHeight = screenHeight * 0.15;
  const thumbPadding = 8;

  // Calculate positions for all screens
  let thumbIndex = 0;
  for (let i = 0; i < count; i++) {
    if (i === expandedIndex) {
      // Expanded screen - centered and large
      positions.push({
        width: expandedWidth,
        height: expandedHeight,
        left: expandedLeft,
        top: expandedTop,
        zIndex: 10,
      });
    } else {
      // Thumbnail - arranged at the bottom
      const thumbLeft = thumbPadding + thumbIndex * (thumbWidth + thumbPadding);
      positions.push({
        width: thumbWidth,
        height: thumbHeight,
        left: thumbLeft,
        top: screenHeight - thumbHeight - thumbPadding,
        zIndex: 20, // Above the expanded screen so they're clickable
      });
      thumbIndex++;
    }
  }

  return positions;
};

// Get layout positions based on whether a screen is expanded
const getLayoutPositions = (
  count: number,
  screenWidth: number,
  screenHeight: number,
  expandedIndex: number | null = null,
): LayoutPosition[] => {
  if (expandedIndex !== null && expandedIndex >= 0 && expandedIndex < count) {
    return getExpandedLayoutPositions(count, expandedIndex, screenWidth, screenHeight);
  }
  return getNormalLayoutPositions(count, screenWidth, screenHeight);
};

export default function MultiscreenPage() {
  const theme = useTheme();
  const router = useRouter();
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const { channels: channelsParam } = useLocalSearchParams<{ channels: string }>();
  const { setActiveAudioIndex } = useMultiscreen();

  // Parse channels from params
  const channels = useMemo<MultiscreenChannel[]>(() => {
    if (!channelsParam) return [];
    try {
      return JSON.parse(channelsParam);
    } catch {
      console.warn('[multiscreen] Failed to parse channels param');
      return [];
    }
  }, [channelsParam]);

  const [activeIndex, setActiveIndex] = useState(0);
  const [focusedIndex, setFocusedIndex] = useState(0);
  const [expandedIndex, setExpandedIndex] = useState<number | null>(null);
  const [showOverlay, setShowOverlay] = useState(true);
  const overlayTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const videoRefs = useRef<(VideoPlayerHandle | null)[]>([]);

  // Keep screen awake during multiscreen playback
  useEffect(() => {
    KeepAwake.activateKeepAwakeAsync();
    return () => {
      KeepAwake.deactivateKeepAwake();
    };
  }, []);

  // Calculate layout positions
  const layoutPositions = useMemo(
    () => getLayoutPositions(channels.length, screenWidth, screenHeight, expandedIndex),
    [channels.length, screenWidth, screenHeight, expandedIndex],
  );

  // Group channels into rows based on CURRENT layout positions for navigation
  // But we use a stable key based on channel index to prevent remounting
  const channelRows = useMemo(() => {
    const rowMap = new Map<number, { channel: MultiscreenChannel; index: number; position: LayoutPosition }[]>();

    channels.forEach((channel, index) => {
      const position = layoutPositions[index];
      const rowKey = Math.round(position.top); // Round to handle floating point

      if (!rowMap.has(rowKey)) {
        rowMap.set(rowKey, []);
      }
      rowMap.get(rowKey)!.push({ channel, index, position });
    });

    // Sort rows by top position and return as array
    return Array.from(rowMap.entries())
      .sort(([a], [b]) => a - b)
      .map(([, items]) => items.sort((a, b) => a.position.left - b.position.left));
  }, [channels, layoutPositions]);

  // Auto-hide overlay after 3 seconds
  const resetOverlayTimeout = useCallback(() => {
    if (overlayTimeoutRef.current) {
      clearTimeout(overlayTimeoutRef.current);
    }
    setShowOverlay(true);
    overlayTimeoutRef.current = setTimeout(() => {
      setShowOverlay(false);
    }, 3000);
  }, []);

  useEffect(() => {
    resetOverlayTimeout();
    return () => {
      if (overlayTimeoutRef.current) {
        clearTimeout(overlayTimeoutRef.current);
      }
    };
  }, [resetOverlayTimeout]);

  // Handle focus change - always update focus visuals
  const handleFocusChange = useCallback(
    (index: number) => {
      setFocusedIndex(index);
      resetOverlayTimeout();
      // When expanded, only change audio if focusing the expanded screen
      if (expandedIndex !== null && index !== expandedIndex) {
        // Just show overlay, don't change audio
        return;
      }
      setActiveIndex(index);
      setActiveAudioIndex(index);
    },
    [setActiveAudioIndex, resetOverlayTimeout, expandedIndex],
  );

  // Handle active index change (update audio) - used by select handlers
  const handleActiveChange = useCallback(
    (index: number) => {
      setActiveIndex(index);
      setActiveAudioIndex(index);
      resetOverlayTimeout();
    },
    [setActiveAudioIndex, resetOverlayTimeout],
  );

  // Handle back button to exit multiscreen
  useEffect(() => {
    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      console.log('[multiscreen] Back pressed, exiting multiscreen');
      router.back();
      return true;
    });

    return () => {
      removeInterceptor();
    };
  }, [router]);

  // Handle screen tap (mobile) - toggle expand on tap
  const handleScreenTap = useCallback(
    (index: number) => {
      // Toggle expand state
      if (expandedIndex === index) {
        // Collapsing - keep audio on current
        setExpandedIndex(null);
      } else {
        // Expanding or switching - switch audio to this screen
        handleActiveChange(index);
        setExpandedIndex(index);
      }
    },
    [handleActiveChange, expandedIndex],
  );

  // Handle screen select (TV) - toggle expand on select press
  const handleScreenSelect = useCallback(
    (index: number) => {
      resetOverlayTimeout();
      // Toggle expand state
      setExpandedIndex((prev) => {
        if (prev === index) {
          // Collapsing - keep audio on current
          return null;
        } else {
          // Expanding - switch audio to this screen
          handleActiveChange(index);
          return index;
        }
      });
    },
    [resetOverlayTimeout, handleActiveChange],
  );

  // Handle exit button tap
  const handleExit = useCallback(() => {
    router.back();
  }, [router]);

  // Handle video player events (minimal - just prevent errors)
  const handleBuffer = useCallback(() => {}, []);
  const handleProgress = useCallback(() => {}, []);
  const handleLoad = useCallback(() => {}, []);
  const handleEnd = useCallback(() => {}, []);
  const handleError = useCallback((error: unknown) => {
    console.warn('[multiscreen] Video error:', error);
  }, []);

  // Log warning for heavy resource usage
  useEffect(() => {
    if (channels.length >= 4) {
      console.warn(
        `[multiscreen] Running ${channels.length} concurrent video streams. This may cause performance issues on low-end devices.`,
      );
    }
  }, [channels.length]);

  if (channels.length < 2) {
    return (
      <View style={styles.container}>
        <Stack.Screen options={{ headerShown: false }} />
        <Text style={styles.errorText}>Not enough channels for multiscreen</Text>
      </View>
    );
  }

  return (
    <SpatialNavigationRoot isActive={Platform.isTV}>
      <Stack.Screen options={{ headerShown: false }} />
      <View style={styles.container}>
        {/* TV: Separate video layer from navigation layer to prevent remounting */}
        {Platform.isTV ? (
          <>
            {/* Video layer - stable, never remounts */}
            {channels.map((channel, index) => {
              const position = layoutPositions[index];
              const isActive = index === activeIndex;
              const isFocused = index === focusedIndex;
              const isExpanded = index === expandedIndex;
              return (
                <View
                  key={`video-${channel.id}`}
                  style={[
                    styles.playerContainer,
                    {
                      width: position.width,
                      height: position.height,
                      left: position.left,
                      top: position.top,
                      zIndex: position.zIndex ?? 1,
                    },
                    isFocused && styles.playerContainerFocused,
                    isActive && styles.playerContainerActive,
                    isExpanded && styles.playerContainerExpanded,
                  ]}
                >
                  <View style={styles.playerWrapper}>
                    <View style={styles.videoContainer}>
                      <VideoPlayer
                        ref={(ref) => {
                          videoRefs.current[index] = ref;
                        }}
                        movie={channel.streamUrl}
                        headerImage={channel.logo ?? ''}
                        movieTitle={channel.name}
                        paused={false}
                        controls={false}
                        volume={isActive ? 1 : 0}
                        onBuffer={handleBuffer}
                        onProgress={handleProgress}
                        onLoad={handleLoad}
                        onEnd={handleEnd}
                        onError={handleError}
                        mediaType="channel"
                        resizeMode="contain"
                      />
                    </View>
                    {/* Channel name overlay */}
                    {showOverlay && (
                      <View style={styles.overlayContainer}>
                        <LinearGradient colors={['transparent', 'rgba(0,0,0,0.8)']} style={styles.overlayGradient} />
                        <View style={styles.overlayContent}>
                          {channel.logo && (
                            <Image source={{ uri: channel.logo }} style={styles.overlayLogo} contentFit="contain" />
                          )}
                          <Text style={styles.overlayTitle} numberOfLines={1}>
                            {channel.name}
                          </Text>
                          {isActive && (
                            <View style={styles.audioIndicator}>
                              <Text style={styles.audioIndicatorText}>AUDIO</Text>
                            </View>
                          )}
                        </View>
                      </View>
                    )}
                  </View>
                </View>
              );
            })}
            {/* Navigation layer - handles focus, positioned over videos */}
            {/* Key forces re-layout when expanded state changes */}
            <SpatialNavigationNode
              key={`nav-layout-${expandedIndex ?? 'normal'}`}
              orientation="vertical"
              alignInGrid
              style={StyleSheet.absoluteFill}
            >
              {channelRows.map((row, rowIndex) => (
                <SpatialNavigationNode key={`nav-row-${rowIndex}`} orientation="horizontal">
                  {row.map(({ channel, index, position }) => {
                    // Default focus: expanded item when expanded, first item when not
                    const shouldDefaultFocus = expandedIndex !== null ? index === expandedIndex : index === 0;
                    const navElement = (
                      <SpatialNavigationFocusableView
                        key={`nav-${channel.id}`}
                        focusKey={`multiscreen-${index}`}
                        onFocus={() => handleFocusChange(index)}
                        onSelect={() => handleScreenSelect(index)}
                        style={[
                          styles.navOverlay,
                          {
                            width: position.width,
                            height: position.height,
                            left: position.left,
                            top: position.top,
                            zIndex: (position.zIndex ?? 1) + 100,
                          },
                        ]}
                      >
                        {() => <View style={styles.navOverlayInner} />}
                      </SpatialNavigationFocusableView>
                    );
                    // Wrap with DefaultFocus to set/restore focus after layout change
                    return shouldDefaultFocus ? (
                      <DefaultFocus key={`nav-${channel.id}`}>{navElement}</DefaultFocus>
                    ) : (
                      navElement
                    );
                  })}
                </SpatialNavigationNode>
              ))}
            </SpatialNavigationNode>
          </>
        ) : (
          /* Mobile: Simple map without navigation nodes */
          channels.map((channel, index) => {
            const position = layoutPositions[index];
            const isActive = index === activeIndex;
            const isExpanded = index === expandedIndex;
            return (
              <View
                key={channel.id}
                style={[
                  styles.playerContainer,
                  {
                    width: position.width,
                    height: position.height,
                    left: position.left,
                    top: position.top,
                    zIndex: position.zIndex ?? 1,
                  },
                  isActive && styles.playerContainerActive,
                  isExpanded && styles.playerContainerExpanded,
                ]}
              >
                <View style={styles.playerWrapper}>
                  <View style={styles.videoContainer}>
                    <VideoPlayer
                      ref={(ref) => {
                        videoRefs.current[index] = ref;
                      }}
                      movie={channel.streamUrl}
                      headerImage={channel.logo ?? ''}
                      movieTitle={channel.name}
                      paused={false}
                      controls={false}
                      volume={isActive ? 1 : 0}
                      onBuffer={handleBuffer}
                      onProgress={handleProgress}
                      onLoad={handleLoad}
                      onEnd={handleEnd}
                      onError={handleError}
                      mediaType="channel"
                      resizeMode="contain"
                    />
                  </View>
                  {/* Touchable overlay for mobile - captures taps to switch audio */}
                  <Pressable style={styles.touchOverlay} onPress={() => handleScreenTap(index)}>
                    {/* Channel name overlay */}
                    {showOverlay && (
                      <View style={styles.overlayContainer}>
                        <LinearGradient colors={['transparent', 'rgba(0,0,0,0.8)']} style={styles.overlayGradient} />
                        <View style={styles.overlayContent}>
                          {channel.logo && (
                            <Image source={{ uri: channel.logo }} style={styles.overlayLogo} contentFit="contain" />
                          )}
                          <Text style={styles.overlayTitle} numberOfLines={1}>
                            {channel.name}
                          </Text>
                          {isActive && (
                            <View style={styles.audioIndicator}>
                              <Text style={styles.audioIndicatorText}>AUDIO</Text>
                            </View>
                          )}
                        </View>
                      </View>
                    )}
                  </Pressable>
                </View>
              </View>
            );
          })
        )}

        {/* Exit button for mobile */}
        {!Platform.isTV && showOverlay && (
          <Pressable style={styles.exitButton} onPress={handleExit}>
            <Ionicons name="close" size={28} color="#fff" />
          </Pressable>
        )}
      </View>
    </SpatialNavigationRoot>
  );
}

const createStyles = (theme: NovaTheme) => {
  const isTV = Platform.isTV;

  return StyleSheet.create({
    container: {
      flex: 1,
      backgroundColor: '#000',
    },
    playerContainer: {
      position: 'absolute',
      overflow: 'hidden',
      // Use consistent border width to prevent resizing on focus
      borderWidth: isTV ? 4 : 2,
      borderColor: 'transparent',
    },
    playerContainerFocused: {
      borderColor: theme.colors.text.primary,
    },
    playerContainerActive: {
      borderColor: theme.colors.accent.primary,
    },
    playerContainerExpanded: {
      borderColor: theme.colors.accent.primary,
      borderWidth: isTV ? 6 : 3,
    },
    playerWrapper: {
      flex: 1,
      backgroundColor: '#000',
    },
    playerWrapperFocused: {
      // Additional focus styling handled by container
    },
    navOverlay: {
      position: 'absolute',
    },
    navOverlayInner: {
      flex: 1,
    },
    videoContainer: {
      // Use absolute positioning to ensure video has concrete bounds for objectFit: contain
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: '#000',
    },
    touchOverlay: {
      ...StyleSheet.absoluteFillObject,
      zIndex: 10,
    },
    overlayContainer: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: isTV ? 80 : 50,
      zIndex: 20,
    },
    overlayGradient: {
      ...StyleSheet.absoluteFillObject,
    },
    overlayContent: {
      flex: 1,
      flexDirection: 'row',
      alignItems: 'center',
      paddingHorizontal: isTV ? theme.spacing.lg : theme.spacing.sm,
      gap: isTV ? theme.spacing.md : theme.spacing.sm,
    },
    overlayLogo: {
      width: isTV ? 40 : 24,
      height: isTV ? 40 : 24,
      borderRadius: theme.radius.sm,
    },
    overlayTitle: {
      ...(isTV ? theme.typography.body.lg : theme.typography.caption.sm),
      color: theme.colors.text.primary,
      flex: 1,
    },
    audioIndicator: {
      backgroundColor: theme.colors.accent.primary,
      paddingHorizontal: isTV ? theme.spacing.md : theme.spacing.sm,
      paddingVertical: isTV ? theme.spacing.xs : 2,
      borderRadius: theme.radius.sm,
    },
    audioIndicatorText: {
      ...(isTV ? theme.typography.label.md : theme.typography.caption.sm),
      color: theme.colors.text.inverse,
      fontWeight: '700',
      fontSize: isTV ? 14 : 10,
    },
    exitButton: {
      position: 'absolute',
      top: 50,
      right: 16,
      width: 44,
      height: 44,
      borderRadius: 22,
      backgroundColor: 'rgba(0, 0, 0, 0.7)',
      alignItems: 'center',
      justifyContent: 'center',
      zIndex: 100,
    },
    errorText: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
      textAlign: 'center',
      marginTop: 100,
    },
  });
};
