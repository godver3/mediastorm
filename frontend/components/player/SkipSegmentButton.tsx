import React, { useEffect, useMemo, useRef } from 'react';
import { Animated, Platform, Pressable, StyleSheet } from 'react-native';
import FocusablePressable from '@/components/FocusablePressable';
import type { SegmentType } from '@/hooks/useIntroSkip';
import { useTheme } from '@/theme';
import type { NovaTheme } from '@/theme';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { TV_REFERENCE_HEIGHT } from '@/theme/tokens/tvScale';

const SEGMENT_LABELS: Record<SegmentType, string> = {
  intro: 'Skip Intro',
  recap: 'Skip Recap',
  outro: 'Skip Credits',
};

interface SkipSegmentButtonProps {
  segmentType: SegmentType;
  onSkip: () => void;
  visible: boolean;
}

const SkipSegmentButton: React.FC<SkipSegmentButtonProps> = ({ segmentType, onSkip, visible }) => {
  const theme = useTheme();
  const { height } = useTVDimensions();
  const vh = (height > 0 ? height : 1080) / TV_REFERENCE_HEIGHT;
  const styles = useMemo(() => createStyles(theme, vh), [theme, vh]);
  const opacity = useRef(new Animated.Value(0)).current;

  useEffect(() => {
    Animated.timing(opacity, {
      toValue: visible ? 1 : 0,
      duration: 200,
      useNativeDriver: true,
    }).start();
  }, [visible, opacity]);

  const label = SEGMENT_LABELS[segmentType];

  if (Platform.isTV) {
    return (
      <Animated.View style={[styles.container, { opacity }]} pointerEvents={visible ? 'auto' : 'none'}>
        <FocusablePressable
          text={label}
          icon="play-forward"
          onSelect={onSkip}
          style={styles.button}
          textStyle={styles.text}
          focusedTextStyle={styles.text}
          variant="primary"
        />
      </Animated.View>
    );
  }

  return (
    <Animated.View style={[styles.container, { opacity }]} pointerEvents={visible ? 'auto' : 'none'}>
      <Pressable onPress={onSkip} style={styles.button}>
        <Animated.Text style={styles.text}>{label}</Animated.Text>
      </Pressable>
    </Animated.View>
  );
};

const createStyles = (theme: NovaTheme, vh: number) => {
  const isTV = Platform.isTV;
  return StyleSheet.create({
    container: {
      position: 'absolute',
      bottom: isTV ? Math.round(160 * vh) : 120,
      right: isTV ? Math.round(48 * vh) : 24,
      zIndex: 50,
    },
    button: {
      backgroundColor: 'rgba(0, 0, 0, 0.7)',
      borderWidth: 1,
      borderColor: 'rgba(255, 255, 255, 0.3)',
      borderRadius: isTV ? Math.round(8 * vh) : 8,
      paddingVertical: isTV ? Math.round(12 * vh) : 10,
      paddingHorizontal: isTV ? Math.round(24 * vh) : 20,
    },
    text: {
      color: '#FFFFFF',
      fontSize: isTV ? Math.round(18 * vh) : 16,
      fontWeight: '600',
    },
  });
};

export default SkipSegmentButton;
