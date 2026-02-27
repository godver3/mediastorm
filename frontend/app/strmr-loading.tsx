import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { useTheme } from '@/theme';
import { Stack } from 'expo-router';
import { LinearGradient } from 'expo-linear-gradient';
import React, { useEffect, useMemo, useRef } from 'react';
import { Animated, Easing, Platform, StyleSheet, View } from 'react-native';

const STRMR_LETTERS = ['s', 't', 'r', 'm', 'r'];
const LETTER_PULSE_DURATION_MS = 520;
const LETTER_PHASE_OFFSET_MS = LETTER_PULSE_DURATION_MS / 2;
const WAVE_REST_MS = 900;
const HIGHLIGHT_SWEEP_DURATION_MS = 4200;
const CIRCLE_PULSE_DURATION_MS = 3200;
const BACKGROUND_WAVE_DURATION_MS = 7000;

export default function StrmrLoadingScreen() {
  const theme = useTheme();
  const letterAnimations = useMemo(() => STRMR_LETTERS.map(() => new Animated.Value(0)), []);
  const highlightRotation = useRef(new Animated.Value(0)).current;
  const circlePulse = useRef(new Animated.Value(0)).current;
  const backgroundShift = useRef(new Animated.Value(0)).current;

  useEffect(() => {
    highlightRotation.setValue(0);
    const loop = Animated.loop(
      Animated.timing(highlightRotation, {
        toValue: 1,
        duration: HIGHLIGHT_SWEEP_DURATION_MS,
        easing: Easing.linear,
        useNativeDriver: true,
      }),
    );
    loop.start();

    return () => {
      loop.stop();
    };
  }, [highlightRotation]);

  useEffect(() => {
    const sequences = letterAnimations.map((value) => {
      value.setValue(0);
      return Animated.sequence([
        Animated.timing(value, {
          toValue: 1,
          duration: LETTER_PULSE_DURATION_MS,
          easing: Easing.out(Easing.quad),
          useNativeDriver: true,
        }),
        Animated.timing(value, {
          toValue: 0,
          duration: LETTER_PULSE_DURATION_MS,
          easing: Easing.in(Easing.quad),
          useNativeDriver: true,
        }),
      ]);
    });

    const cycle = Animated.loop(
      Animated.sequence([Animated.stagger(LETTER_PHASE_OFFSET_MS, sequences), Animated.delay(WAVE_REST_MS)]),
    );

    cycle.start();

    return () => {
      cycle.stop();
    };
  }, [letterAnimations]);

  useEffect(() => {
    circlePulse.setValue(0);
    const loop = Animated.loop(
      Animated.sequence([
        Animated.timing(circlePulse, {
          toValue: 1,
          duration: CIRCLE_PULSE_DURATION_MS,
          easing: Easing.inOut(Easing.quad),
          useNativeDriver: true,
        }),
        Animated.timing(circlePulse, {
          toValue: 0,
          duration: CIRCLE_PULSE_DURATION_MS,
          easing: Easing.inOut(Easing.quad),
          useNativeDriver: true,
        }),
      ]),
    );
    loop.start();

    return () => {
      loop.stop();
    };
  }, [circlePulse]);

  useEffect(() => {
    backgroundShift.setValue(0);
    const loop = Animated.loop(
      Animated.timing(backgroundShift, {
        toValue: 1,
        duration: BACKGROUND_WAVE_DURATION_MS,
        easing: Easing.inOut(Easing.quad),
        useNativeDriver: true,
      }),
    );
    loop.start();

    return () => {
      loop.stop();
    };
  }, [backgroundShift]);

  return (
    <FixedSafeAreaView style={[styles.safeArea, { backgroundColor: theme.colors.background.base }]}>
      <Stack.Screen
        options={{
          title: 'mediastorm loading',
          headerShown: false,
        }}
      />
      <View style={styles.gradientContainer}>
        <Animated.View
          pointerEvents="none"
          style={[
            styles.gradientLayer,
            {
              opacity: 1,
            },
          ]}>
          <LinearGradient
            colors={['#1b0c32', '#270947', theme.colors.background.base]}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 0.85 }}
            style={StyleSheet.absoluteFill}
          />
        </Animated.View>
        <Animated.View
          pointerEvents="none"
          style={[
            styles.gradientLayer,
            {
              opacity: 0.75,
            },
          ]}>
          <LinearGradient
            colors={['rgba(232, 238, 255, 0.55)', 'rgba(40, 44, 54, 0.08)', 'rgba(210, 222, 255, 0.32)']}
            start={{ x: 0, y: 0 }}
            end={{ x: 0.95, y: 1 }}
            style={StyleSheet.absoluteFill}
          />
        </Animated.View>
        <View style={styles.center}>
          <Animated.View
            pointerEvents="none"
            style={[
              styles.glow,
              {
                backgroundColor: 'transparent',
                transform: [
                  {
                    scale: circlePulse.interpolate({
                      inputRange: [0, 1],
                      outputRange: [1, 1.01],
                    }),
                  },
                ],
                opacity: circlePulse.interpolate({
                  inputRange: [0, 1],
                  outputRange: [0.6, 0.8],
                }),
              },
            ]}
          />
          <Animated.View
            pointerEvents="none"
            style={[
              styles.radialBlur,
              {
                transform: [
                  {
                    scale: circlePulse.interpolate({
                      inputRange: [0, 1],
                      outputRange: [1.01, 1.08],
                    }),
                  },
                ],
                opacity: circlePulse.interpolate({
                  inputRange: [0, 1],
                  outputRange: [0.2, 0.4],
                }),
              },
            ]}>
            <LinearGradient
              colors={[`${theme.colors.accent.primary}30`, `${theme.colors.accent.primary}00`]}
              start={{ x: 0.5, y: 0 }}
              end={{ x: 0.5, y: 1 }}
              style={StyleSheet.absoluteFill}
            />
            <LinearGradient
              colors={[`${theme.colors.accent.primary}20`, `${theme.colors.accent.primary}00`]}
              start={{ x: 0, y: 0.5 }}
              end={{ x: 1, y: 0.5 }}
              style={[StyleSheet.absoluteFill, { transform: [{ rotate: '45deg' }] }]}
            />
          </Animated.View>
          <Animated.View
            pointerEvents="none"
            style={[
              styles.highlightArc,
              {
                borderTopColor: circlePulse.interpolate({
                  inputRange: [0, 1],
                  outputRange: [`${theme.colors.accent.primary}cc`, `${theme.colors.accent.primary}88`],
                }) as any,
                borderRightColor: `${theme.colors.accent.primary}00`,
                borderBottomColor: `${theme.colors.accent.primary}00`,
                borderLeftColor: `${theme.colors.accent.primary}00`,
                transform: [
                  {
                    rotate: highlightRotation.interpolate({
                      inputRange: [0, 1],
                      outputRange: ['0deg', '360deg'],
                    }),
                  },
                  {
                    scale: circlePulse.interpolate({
                      inputRange: [0, 1],
                      outputRange: [1, 1.03],
                    }),
                  },
                ],
                opacity: circlePulse.interpolate({
                  inputRange: [0, 1],
                  outputRange: [0.7, 1],
                }),
              },
            ]}
          />
          <View style={styles.letterRow}>
            {letterAnimations.map((value, index) => {
              const letter = STRMR_LETTERS[index];
              const scale = value.interpolate({
                inputRange: [0, 1],
                outputRange: [1, Platform.isTV ? 1.22 : 1.1],
              });
              const opacity = value.interpolate({
                inputRange: [0, 1],
                outputRange: [0.35, 1],
              });

              return (
                <Animated.Text
                  key={`${letter}-${index}`}
                  style={[
                    styles.letter,
                    {
                      color: theme.colors.accent.primary,
                      opacity,
                      transform: [{ scale }],
                    },
                  ]}>
                  {letter}
                </Animated.Text>
              );
            })}
          </View>
        </View>
      </View>
    </FixedSafeAreaView>
  );
}

const styles = StyleSheet.create({
  safeArea: {
    flex: 1,
    paddingTop: Platform.isTV ? 0 : undefined,
  },
  gradientContainer: {
    flex: 1,
    position: 'relative',
    marginHorizontal: Platform.isTV ? -60 : -20,
    marginTop: Platform.isTV ? -40 : -16,
  },
  gradientLayer: {
    ...StyleSheet.absoluteFillObject,
    top: Platform.isTV ? -80 : -40,
    bottom: Platform.isTV ? -80 : -40,
    left: Platform.isTV ? -80 : -40,
    right: Platform.isTV ? -80 : -40,
  },
  center: {
    flex: 1,
    paddingHorizontal: Platform.isTV ? 120 : 24,
    paddingVertical: Platform.isTV ? 80 : 24,
    justifyContent: 'center',
    alignItems: 'center',
    width: '100%',
  },
  letterRow: {
    flexDirection: 'row',
    alignItems: 'flex-end',
    justifyContent: 'center',
    gap: Platform.isTV ? 6 : 4,
  },
  letter: {
    fontSize: Platform.isTV ? 140 : 72,
    fontWeight: '700',
    textTransform: 'lowercase',
    textAlign: 'center',
    letterSpacing: Platform.isTV ? 2 : 1,
    textShadowColor: 'rgba(0, 0, 0, 0.35)',
    textShadowOffset: { width: 0, height: Platform.isTV ? 10 : 4 },
    textShadowRadius: Platform.isTV ? 30 : 12,
  },
  glow: {
    position: 'absolute',
    width: Platform.isTV ? 640 : 280,
    height: Platform.isTV ? 640 : 280,
    borderRadius: 999,
  },
  radialBlur: {
    position: 'absolute',
    width: Platform.isTV ? 760 : 340,
    height: Platform.isTV ? 760 : 340,
    borderRadius: 999,
  },
  highlightArc: {
    position: 'absolute',
    width: Platform.isTV ? 560 : 240,
    height: Platform.isTV ? 560 : 240,
    borderRadius: 999,
    borderWidth: Platform.isTV ? 10 : 6,
  },
});
