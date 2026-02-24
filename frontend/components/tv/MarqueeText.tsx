/**
 * MarqueeText - Animated scrolling text for truncated content
 * Scrolls horizontally when focused to reveal full text
 *
 * Uses refs for layout measurement to avoid React commits.
 * Only the shared values (translateX, measuredTextWidth) drive animation
 * on the UI thread via Reanimated.
 */

import React, { memo, useCallback, useEffect, useRef } from 'react';
import { StyleSheet, Text, View, type TextStyle, type ViewStyle } from 'react-native';
import Animated, {
  useAnimatedStyle,
  useSharedValue,
  withDelay,
  withRepeat,
  withSequence,
  withTiming,
  cancelAnimation,
  Easing,
} from 'react-native-reanimated';

interface MarqueeTextProps {
  children: string;
  style?: TextStyle | (TextStyle | undefined)[];
  containerStyle?: ViewStyle;
  focused?: boolean;
  /** Delay before starting animation (ms) */
  delay?: number;
  /** Speed of scroll in pixels per second */
  speed?: number;
  /** Pause duration at start/end of scroll (ms) */
  pauseDuration?: number;
}

const MarqueeText = memo(function MarqueeText({
  children,
  style,
  containerStyle,
  focused = false,
  delay = 600,
  speed = 25,
  pauseDuration = 800,
}: MarqueeTextProps) {
  // Refs for layout measurements — no React commits
  const containerWidthRef = useRef(0);
  const fullTextWidthRef = useRef(0);
  const focusedRef = useRef(focused);
  focusedRef.current = focused;

  // Shared values drive Animated.Text on the UI thread
  const translateX = useSharedValue(0);
  const measuredTextWidth = useSharedValue(0);

  // Reads refs + focusedRef, starts/stops animation directly
  const updateAnimation = useCallback(() => {
    const cw = containerWidthRef.current;
    const tw = fullTextWidthRef.current;
    const isTruncated = tw > cw + 2 && cw > 0;
    const scrollDistance = Math.max(0, tw - cw + 10);

    if (focusedRef.current && isTruncated && scrollDistance > 0) {
      const duration = (scrollDistance / speed) * 1000;

      translateX.value = withDelay(
        delay,
        withRepeat(
          withSequence(
            withTiming(-scrollDistance, {
              duration,
              easing: Easing.linear,
            }),
            withTiming(-scrollDistance, { duration: pauseDuration }),
            withTiming(0, {
              duration,
              easing: Easing.linear,
            }),
            withTiming(0, { duration: pauseDuration }),
          ),
          -1,
          false,
        ),
      );
    } else {
      cancelAnimation(translateX);
      translateX.value = withTiming(0, { duration: 100 });
    }
  }, [speed, delay, pauseDuration, translateX]);

  // Handle container layout — write ref, trigger animation recalc
  const onContainerLayout = useCallback((e: { nativeEvent: { layout: { width: number } } }) => {
    containerWidthRef.current = e.nativeEvent.layout.width;
    updateAnimation();
  }, [updateAnimation]);

  // Measure full text width — write ref + shared value, trigger animation recalc
  const onMeasureLayout = useCallback((e: { nativeEvent: { layout: { width: number } } }) => {
    fullTextWidthRef.current = e.nativeEvent.layout.width;
    measuredTextWidth.value = e.nativeEvent.layout.width;
    updateAnimation();
  }, [updateAnimation, measuredTextWidth]);

  // Re-run animation when focus changes
  useEffect(() => {
    updateAnimation();
    return () => {
      cancelAnimation(translateX);
    };
  }, [focused, updateAnimation, translateX]);

  // Single animated style combining transform + width
  const animatedStyle = useAnimatedStyle(() => ({
    transform: [{ translateX: translateX.value }],
    width: measuredTextWidth.value || undefined,
  }));

  // Flatten style array if needed
  const flatStyle = Array.isArray(style) ? Object.assign({}, ...style.filter(Boolean)) : style;

  return (
    <View style={[styles.container, containerStyle]} onLayout={onContainerLayout}>
      {/* Visible animated text - single line, scrolls horizontally when truncated */}
      <Animated.Text style={[style, animatedStyle]} numberOfLines={1}>{children}</Animated.Text>
      {/* Measurement wrapper - positioned off screen with no width constraint */}
      <View style={styles.measureWrapper} pointerEvents="none">
        <Text style={[flatStyle, styles.measureText]} onLayout={onMeasureLayout}>
          {children}
        </Text>
      </View>
    </View>
  );
});

const styles = StyleSheet.create({
  container: {
    overflow: 'hidden',
  },
  measureWrapper: {
    position: 'absolute',
    top: -9999,
    left: 0,
    width: 9999, // Large width to allow text to expand
    flexDirection: 'row',
    opacity: 0,
  },
  measureText: {
    // No width constraints
  },
});

export default MarqueeText;
