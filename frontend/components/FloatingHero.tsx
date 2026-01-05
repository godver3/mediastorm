import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Image } from './Image';
import { LinearGradient } from 'expo-linear-gradient';
import React, { useEffect, useState } from 'react';
import { Animated, Platform, StyleSheet, Text, View } from 'react-native';

type FloatingHeroData = {
  title: string;
  description: string;
  headerImage: string;
  year?: number | string; // Can be number for actual year or string for explore card "+X More"
  mediaType?: string;
};

interface FloatingHeroProps {
  data: FloatingHeroData | null;
}

export const FloatingHero = React.memo(
  function FloatingHero({ data }: FloatingHeroProps) {
    const theme = useTheme();
    const styles = React.useMemo(() => createStyles(theme), [theme]);
    const [fadeAnim] = useState(new Animated.Value(0));
    const [scaleAnim] = useState(new Animated.Value(0.95));

    // TODO: Re-enable animations after performance testing
    useEffect(() => {
      if (data) {
        // Instant show (no animation for testing)
        fadeAnim.setValue(1);
        scaleAnim.setValue(1);
      } else {
        // Instant hide
        fadeAnim.setValue(0);
        scaleAnim.setValue(0.95);
      }
    }, [data, fadeAnim, scaleAnim]);

    // Don't render on non-TV platforms
    if (!Platform.isTV) {
      return null;
    }

    // Don't render if no data
    if (!data) {
      return null;
    }

    return (
      <Animated.View
        renderToHardwareTextureAndroid={true}
        style={[
          styles.container,
          {
            opacity: fadeAnim,
            transform: [{ scale: scaleAnim }],
          },
        ]}
        pointerEvents="none">
        <View style={styles.card}>
          <Image source={data.headerImage} style={styles.image} contentFit="cover" />
          <LinearGradient
            colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
            locations={[0.7, 0.9, 1]}
            start={{ x: 0, y: 0 }}
            end={{ x: 0, y: 1 }}
            style={styles.gradient}
          />
          <View style={styles.content}>
            <Text style={styles.title} numberOfLines={2}>
              {data.title}
            </Text>
            {data.year && (
              <Text style={styles.meta}>
                {data.year}
                {data.mediaType && ` • ${data.mediaType === 'series' ? 'TV Series' : 'Movie'}`}
              </Text>
            )}
            <Text style={styles.description} numberOfLines={3}>
              {data.description}
            </Text>
          </View>
        </View>
      </Animated.View>
    );
  },
  (prevProps, nextProps) => {
    // Only re-render if data actually changed
    if (prevProps.data === null && nextProps.data === null) return true;
    if (prevProps.data === null || nextProps.data === null) return false;

    return (
      prevProps.data.title === nextProps.data.title &&
      prevProps.data.description === nextProps.data.description &&
      prevProps.data.headerImage === nextProps.data.headerImage &&
      prevProps.data.year === nextProps.data.year &&
      prevProps.data.mediaType === nextProps.data.mediaType
    );
  },
);

const createStyles = (theme: NovaTheme) => {
  return StyleSheet.create({
    container: {
      position: 'absolute',
      bottom: theme.spacing['3xl'],
      right: theme.spacing['3xl'],
      zIndex: 100,
      // Ensure it's above other content but doesn't interfere with interactions
      elevation: 10,
    },
    card: {
      width: 462, // 420 * 1.1
      height: 264, // 240 * 1.1
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
      borderWidth: 2,
      borderColor: theme.colors.accent.primary,
      shadowColor: '#000',
      shadowOffset: { width: 0, height: 8 },
      shadowOpacity: 0.6,
      shadowRadius: 16,
      elevation: 12,
    },
    image: {
      width: '100%',
      height: '100%',
      position: 'absolute',
    },
    gradient: {
      ...StyleSheet.absoluteFillObject,
    },
    content: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      padding: theme.spacing.lg,
      gap: theme.spacing.xs,
    },
    title: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      fontWeight: '700',
    },
    meta: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      fontWeight: '600',
    },
    description: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      lineHeight: theme.typography.body.sm.lineHeight * 0.95,
    },
  });
};
