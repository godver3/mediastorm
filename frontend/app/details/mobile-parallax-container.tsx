/**
 * Mobile parallax container for details page
 * Provides scrollable content with parallax poster fade effect
 */

import React, { memo, useMemo, type ReactNode } from 'react';
import { View, StyleSheet, useWindowDimensions } from 'react-native';
import { Image } from 'expo-image';
import { LinearGradient } from 'expo-linear-gradient';
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
const POSTER_TRANSITION_DISTANCE = 300;
// Minimum opacity for poster when scrolled (never fully fades)
const POSTER_MIN_OPACITY = 0.45;
// How far down the screen the content starts (as percentage of screen height)
const CONTENT_START_PERCENT = 0.62;

const MobileParallaxContainer = memo(function MobileParallaxContainer({
  posterUrl,
  backdropUrl,
  theme,
  children,
}: MobileParallaxContainerProps) {
  const { height: windowHeight, width: windowWidth } = useWindowDimensions();
  const styles = useMemo(() => createStyles(theme, windowHeight, windowWidth), [theme, windowHeight, windowWidth]);

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
      Extrapolation.CLAMP
    );
    return { opacity };
  });

  const imageUrl = posterUrl || backdropUrl;

  return (
    <View style={styles.container}>
      {/* Fixed background poster/backdrop - persists with parallax */}
      {imageUrl && (
        <Animated.View style={[styles.posterContainer, posterAnimatedStyle]}>
          <Image
            source={{ uri: imageUrl }}
            style={styles.posterImage}
            contentFit="cover"
          />
          {/* Lower third gradient to darken bottom of poster */}
          <LinearGradient
            colors={['transparent', 'rgba(0, 0, 0, 0.5)', theme.colors.background.base]}
            locations={[0, 0.5, 1]}
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
        contentInsetAdjustmentBehavior="never"
      >
        {/* Spacer to push content below the poster initially */}
        <View style={styles.contentSpacer} />

        {/* Content card - gradient from transparent to opaque */}
        <LinearGradient
          colors={['transparent', `${theme.colors.background.base}40`, `${theme.colors.background.base}B0`, theme.colors.background.base]}
          locations={[0, 0.05, 0.15, 0.3]}
          style={styles.contentCard}
        >
          {children}
        </LinearGradient>
      </Animated.ScrollView>
    </View>
  );
});

const createStyles = (theme: NovaTheme, windowHeight: number, windowWidth: number) => {
  const contentStartOffset = windowHeight * CONTENT_START_PERCENT;

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
    },
    posterImage: {
      width: '100%',
      height: '100%',
    },
    posterBottomGradient: {
      position: 'absolute',
      left: 0,
      right: 0,
      bottom: 0,
      height: '40%', // Lower third gradient
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
    },
  });
};

export default MobileParallaxContainer;
