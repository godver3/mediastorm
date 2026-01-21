/**
 * MarqueeText - Animated scrolling text for truncated content
 * Scrolls horizontally when focused to reveal full text
 */

import React, { memo, useCallback, useEffect, useState } from 'react';
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
  style?: TextStyle | TextStyle[];
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
  const [containerWidth, setContainerWidth] = useState(0);
  const [fullTextWidth, setFullTextWidth] = useState(0);
  const translateX = useSharedValue(0);

  // Check if truncated
  const isTruncated = fullTextWidth > containerWidth + 2 && containerWidth > 0;
  const scrollDistance = Math.max(0, fullTextWidth - containerWidth + 10);

  // Handle container layout
  const onContainerLayout = useCallback((e: { nativeEvent: { layout: { width: number } } }) => {
    setContainerWidth(e.nativeEvent.layout.width);
  }, []);

  // Measure the full text width from the unconstrained hidden text
  const onMeasureLayout = useCallback((e: { nativeEvent: { layout: { width: number } } }) => {
    setFullTextWidth(e.nativeEvent.layout.width);
  }, []);

  // Start/stop animation based on focus and truncation
  useEffect(() => {
    if (focused && isTruncated && scrollDistance > 0) {
      const duration = (scrollDistance / speed) * 1000;

      translateX.value = withDelay(
        delay,
        withRepeat(
          withSequence(
            // Scroll to end
            withTiming(-scrollDistance, {
              duration,
              easing: Easing.linear,
            }),
            // Pause at end
            withTiming(-scrollDistance, { duration: pauseDuration }),
            // Scroll back to start
            withTiming(0, {
              duration,
              easing: Easing.linear,
            }),
            // Pause at start
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

    return () => {
      cancelAnimation(translateX);
    };
  }, [focused, isTruncated, scrollDistance, speed, delay, pauseDuration, translateX]);

  const animatedStyle = useAnimatedStyle(() => ({
    transform: [{ translateX: translateX.value }],
  }));

  // Flatten style array if needed
  const flatStyle = Array.isArray(style) ? Object.assign({}, ...style.filter(Boolean)) : style;

  return (
    <View style={[styles.container, containerStyle]} onLayout={onContainerLayout}>
      {/* Visible animated text - single line, scrolls horizontally when truncated */}
      <Animated.Text style={[style, animatedStyle, { width: fullTextWidth || undefined }]} numberOfLines={1}>{children}</Animated.Text>
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
