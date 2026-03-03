import React, { useEffect, useMemo, useRef } from 'react';
import { Animated, Pressable, StyleSheet } from 'react-native';
import type { SegmentType } from '@/hooks/useIntroSkip';
import { useTheme } from '@/theme';
import type { NovaTheme } from '@/theme';

export const SEGMENT_LABELS: Record<SegmentType, string> = {
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
  const styles = useMemo(() => createStyles(theme), [theme]);
  const opacity = useRef(new Animated.Value(0)).current;

  useEffect(() => {
    Animated.timing(opacity, {
      toValue: visible ? 1 : 0,
      duration: 200,
      useNativeDriver: true,
    }).start();
  }, [visible, opacity]);

  const label = SEGMENT_LABELS[segmentType];

  return (
    <Animated.View style={[styles.container, { opacity }]} pointerEvents={visible ? 'auto' : 'none'}>
      <Pressable onPress={onSkip} style={styles.button}>
        <Animated.Text style={styles.text}>{label}</Animated.Text>
      </Pressable>
    </Animated.View>
  );
};

const createStyles = (theme: NovaTheme) => {
  return StyleSheet.create({
    container: {
      position: 'absolute',
      bottom: 120,
      right: 24,
      zIndex: 50,
    },
    button: {
      backgroundColor: 'rgba(0, 0, 0, 0.7)',
      borderWidth: 1,
      borderColor: 'rgba(255, 255, 255, 0.3)',
      borderRadius: 8,
      paddingVertical: 10,
      paddingHorizontal: 20,
    },
    text: {
      color: '#FFFFFF',
      fontSize: 16,
      fontWeight: '600',
    },
  });
};

export default SkipSegmentButton;
