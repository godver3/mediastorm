/**
 * Mobile parallax container for details page
 * Provides scrollable content with parallax poster fade effect
 */

import React, { memo, useMemo, type ReactNode } from 'react';
import { View, StyleSheet, useWindowDimensions } from 'react-native';
import { Image } from '@/components/Image';
import { LinearGradient } from 'expo-linear-gradient';
import { isTablet } from '@/theme/tokens/tvScale';
import Animated, {
  useAnimatedScrollHandler,
  useSharedValue,
  useAnimatedStyle,
  interpolate,
  Extrapolation,
} from 'react-native-reanimated';
import type { NovaTheme } from '@/theme';

interface MobileParallaxContainerProps {
  posterUrl: string | null;
  backdropUrl: string | null;
  theme: NovaTheme;
  children: ReactNode;
}

// Distance in pixels for the poster transition effect
const POSTER_TRANSITION_DISTANCE = 500;
// Minimum opacity for poster when scrolled (never fully fades)
const POSTER_MIN_OPACITY = 0.6;
// How far down the screen the content starts (as percentage of screen height)
const CONTENT_START_PERCENT = 0.62;

const MobileParallaxContainer = memo(function MobileParallaxContainer({
  posterUrl,
  backdropUrl,
  theme,
  children,
}: MobileParallaxContainerProps) {
  const { height: windowHeight, width: windowWidth } = useWindowDimensions();
  const isLandscape = windowWidth > windowHeight;
  // Tablets in landscape use backdrop (like TV), otherwise prefer poster
  const useBackdrop = isTablet && isLandscape;
  const styles = useMemo(
    () => createStyles(theme, windowHeight, windowWidth, useBackdrop),
    [theme, windowHeight, windowWidth, useBackdrop]
  );

  const scrollY = useSharedValue(0);

  const scrollHandler = useAnimatedScrollHandler({
    onScroll: (event) => {
      scrollY.value = event.contentOffset.y;
    },
  });

  // Animated style for the poster/backdrop - fades partially as user scrolls
  const posterAnimatedStyle = useAnimatedStyle(() => {
    // Fade from full opacity to minimum (never fully disappears)
    const opacity = interpolate(
      scrollY.value,
      [0, POSTER_TRANSITION_DISTANCE],
      [1, POSTER_MIN_OPACITY],
      Extrapolation.CLAMP,
    );
    return { opacity };
  });

  // Tablets in landscape use backdrop (like TV), phones/portrait use poster
  const imageUrl = useBackdrop
    ? (backdropUrl || posterUrl)
    : (posterUrl || backdropUrl);

  return (
    <View style={styles.container}>
      {/* Fixed background poster/backdrop - persists with parallax */}
      {imageUrl && (
        <Animated.View style={[styles.posterContainer, posterAnimatedStyle]}>
          <View style={styles.posterImageWrapper}>
            <Image
              source={{ uri: imageUrl }}
              style={styles.posterImage}
              contentFit="fill"
              transition={300}
            />
          </View>
          {/* Bottom gradient to fade poster to background color - 40% height, solid by 15% from bottom */}
          <LinearGradient
            colors={['transparent', theme.colors.background.base, theme.colors.background.base]}
            locations={[0, 0.625, 1]}
            style={styles.posterBottomGradient}
            pointerEvents="none"
          />
        </Animated.View>
      )}

      {/* Scrollable content */}
      <Animated.ScrollView
        onScroll={scrollHandler}
        scrollEventThrottle={16}
        showsVerticalScrollIndicator={false}
        contentContainerStyle={styles.scrollContentContainer}
        style={styles.scrollView}
        contentInsetAdjustmentBehavior="never">
        {/* Spacer to push content below the poster initially */}
        <View style={styles.contentSpacer} />

        {/* Content card - gradient is a background layer so it doesn't clip children */}
        <View style={styles.contentCard}>
          <LinearGradient
            colors={[
              'transparent',
              `${theme.colors.background.base}40`,
              `${theme.colors.background.base}B0`,
              theme.colors.background.base,
            ]}
            locations={[0, 0.05, 0.15, 0.3]}
            style={StyleSheet.absoluteFill}
            pointerEvents="none"
          />
          {children}
        </View>
      </Animated.ScrollView>
    </View>
  );
});

const createStyles = (theme: NovaTheme, windowHeight: number, _windowWidth: number, useBackdrop: boolean) => {
  const contentStartOffset = windowHeight * CONTENT_START_PERCENT;
  // Backdrop is 16:9 landscape, poster is 2:3 portrait
  const imageAspectRatio = useBackdrop ? 16 / 9 : 2 / 3;

  return StyleSheet.create({
    container: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    posterContainer: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      height: windowHeight * 0.75,
      zIndex: 0,
      overflow: 'hidden',
    },
    posterImageWrapper: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
    },
    posterImage: {
      position: 'absolute',
      top: 0,
      left: 0,
      width: '100%',
      // Backdrop (16:9) or poster (2:3) aspect ratio
      // This forces the image to size based on width and overflow at the bottom
      aspectRatio: imageAspectRatio,
    },
    posterBottomGradient: {
      position: 'absolute',
      left: 0,
      right: 0,
      bottom: 0,
      height: '40%', // Gradient starts 40% up, reaches solid background at 15% from bottom
    },
    scrollView: {
      flex: 1,
      zIndex: 1,
    },
    scrollContentContainer: {
      minHeight: windowHeight + 100, // Extra space to scroll past safe area
    },
    contentSpacer: {
      height: contentStartOffset,
    },
    contentCard: {
      paddingHorizontal: theme.spacing['3xl'],
      paddingTop: theme.spacing.lg,
      paddingBottom: theme.spacing['3xl'],
      minHeight: windowHeight - contentStartOffset + 100,
      overflow: 'visible',
    },
  });
};

export default MobileParallaxContainer;
