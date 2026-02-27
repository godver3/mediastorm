import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import React, { useMemo } from 'react';
import { Platform, StyleSheet, Text, View } from 'react-native';

import { Image } from '@/components/Image';

import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { responsiveSize } from '@/theme/tokens/tvScale';

interface BaseCardProps {
  isFocused?: boolean;
}

interface CollageCardProps extends BaseCardProps {
  variant: 'collage';
  title: string;
  subtitle?: string;
  count?: number;
  posterUrls: string[];
}

interface GradientCardProps extends BaseCardProps {
  variant: 'gradient';
  title: string;
  subtitle?: string;
  iconName?: string;
  iconFamily?: 'Ionicons' | 'MaterialCommunityIcons';
  tintColor?: string;
  aspectRatio?: number;
}

interface BackdropCardProps extends BaseCardProps {
  variant: 'backdrop';
  title: string;
  seedTitle: string;
  backdropUrl?: string;
}

type ListCardProps = CollageCardProps | GradientCardProps | BackdropCardProps;

const CARD_ASPECT_RATIO = 16 / 10;

function CardIcon({ name, family, size }: { name: string; family?: 'Ionicons' | 'MaterialCommunityIcons'; size: number }) {
  if (family === 'MaterialCommunityIcons') {
    return <MaterialCommunityIcons name={name as any} size={size} color="rgba(255,255,255,0.5)" />;
  }
  return <Ionicons name={name as any} size={size} color="rgba(255,255,255,0.5)" />;
}

export function ListCard(props: ListCardProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);

  const focused = props.isFocused ?? false;

  const customAspectRatio = props.variant === 'gradient' ? props.aspectRatio : undefined;
  const cardStyle = [
    styles.card,
    focused && styles.cardFocused,
    customAspectRatio != null && { aspectRatio: customAspectRatio },
  ];

  switch (props.variant) {
    case 'collage':
      return (
        <View style={cardStyle}>
          <View style={styles.collageGrid}>
            {[0, 1, 2, 3].map((i) => (
              <View key={i} style={styles.collageTile}>
                {props.posterUrls[i] ? (
                  <Image
                    source={{ uri: props.posterUrls[i] }}
                    style={StyleSheet.absoluteFill}
                    contentFit="cover"
                    transition={0}
                    cachePolicy="disk"
                  />
                ) : (
                  <View style={[StyleSheet.absoluteFill, styles.collagePlaceholder]} />
                )}
              </View>
            ))}
          </View>
          <LinearGradient
            colors={['transparent', 'rgba(0,0,0,0.85)']}
            style={styles.collageOverlay}
          >
            <Text style={styles.collageTitle} numberOfLines={1}>
              {props.title}
            </Text>
            {(props.subtitle || (props.count !== undefined && props.count > 0)) && (
              <Text style={styles.collageCount}>{props.subtitle ?? `${props.count} items`}</Text>
            )}
          </LinearGradient>
        </View>
      );

    case 'gradient': {
      const gradientStart = props.tintColor ?? 'rgba(255,255,255,0.08)';
      const iconSize = Platform.isTV ? responsiveSize(32, 24) : 28;
      const isFlat = props.aspectRatio != null && props.aspectRatio > CARD_ASPECT_RATIO;
      return (
        <View style={cardStyle}>
          <LinearGradient
            colors={[gradientStart, 'rgba(255,255,255,0.02)']}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 1 }}
            style={[StyleSheet.absoluteFill, styles.gradientBg]}
          />
          {isFlat ? (
            <View style={styles.gradientContentFlat}>
              {props.iconName && (
                <CardIcon name={props.iconName} family={props.iconFamily} size={iconSize} />
              )}
              <View style={{ flex: 1 }}>
                <Text style={styles.gradientTitle} numberOfLines={1}>
                  {props.title}
                </Text>
                {props.subtitle && (
                  <Text style={styles.gradientSubtitle} numberOfLines={1}>
                    {props.subtitle}
                  </Text>
                )}
              </View>
            </View>
          ) : (
            <>
              {props.iconName && (
                <View style={styles.gradientIcon}>
                  <CardIcon name={props.iconName} family={props.iconFamily} size={iconSize} />
                </View>
              )}
              <View style={styles.gradientContent}>
                <Text style={styles.gradientTitle} numberOfLines={2}>
                  {props.title}
                </Text>
                {props.subtitle && (
                  <Text style={styles.gradientSubtitle} numberOfLines={1}>
                    {props.subtitle}
                  </Text>
                )}
              </View>
            </>
          )}
        </View>
      );
    }

    case 'backdrop':
      return (
        <View style={cardStyle}>
          {props.backdropUrl ? (
            <Image
              source={{ uri: props.backdropUrl }}
              style={StyleSheet.absoluteFill}
              contentFit="cover"
              transition={0}
              cachePolicy="disk"
            />
          ) : (
            <View style={[StyleSheet.absoluteFill, styles.collagePlaceholder]} />
          )}
          <LinearGradient
            colors={['transparent', 'rgba(0,0,0,0.85)']}
            style={styles.collageOverlay}
          >
            {(props.title || !props.seedTitle) ? null : (
              <Text style={styles.backdropSeed} numberOfLines={1}>
                Because you watched
              </Text>
            )}
            <Text style={styles.collageTitle} numberOfLines={1}>
              {props.title || props.seedTitle}
            </Text>
          </LinearGradient>
        </View>
      );
  }
}

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    card: {
      aspectRatio: CARD_ASPECT_RATIO,
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
      borderWidth: 3,
      borderColor: 'transparent',
    },
    cardFocused: {
      borderColor: theme.colors.accent.primary,
      transform: [{ scale: 1.05 }],
    },
    // Collage
    collageGrid: {
      flex: 1,
      flexDirection: 'row',
      flexWrap: 'wrap',
    },
    collageTile: {
      width: '50%',
      height: '50%',
    },
    collagePlaceholder: {
      backgroundColor: theme.colors.background.elevated,
    },
    collageOverlay: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      paddingHorizontal: Platform.isTV ? responsiveSize(16, 12) : 12,
      paddingBottom: Platform.isTV ? responsiveSize(14, 10) : 10,
      paddingTop: Platform.isTV ? responsiveSize(32, 24) : 24,
    },
    collageTitle: {
      color: '#fff',
      fontSize: Platform.isTV ? responsiveSize(20, 15) : 14,
      fontWeight: '700',
    },
    collageCount: {
      color: 'rgba(255,255,255,0.7)',
      fontSize: Platform.isTV ? responsiveSize(14, 11) : 11,
      marginTop: 2,
    },
    // Gradient
    gradientBg: {
      borderRadius: theme.radius.lg,
    },
    gradientIcon: {
      position: 'absolute',
      top: Platform.isTV ? responsiveSize(14, 10) : 10,
      right: Platform.isTV ? responsiveSize(14, 10) : 10,
    },
    gradientContent: {
      flex: 1,
      justifyContent: 'flex-end',
      padding: Platform.isTV ? responsiveSize(16, 12) : 12,
    },
    gradientContentFlat: {
      flex: 1,
      flexDirection: 'row',
      alignItems: 'center',
      gap: Platform.isTV ? responsiveSize(12, 8) : 10,
      paddingHorizontal: Platform.isTV ? responsiveSize(12, 8) : 10,
    },
    gradientTitle: {
      color: theme.colors.text.primary,
      fontSize: Platform.isTV ? responsiveSize(18, 14) : 14,
      fontWeight: '600',
    },
    gradientSubtitle: {
      color: theme.colors.text.secondary,
      fontSize: Platform.isTV ? responsiveSize(13, 10) : 11,
      marginTop: 2,
    },
    // Backdrop
    backdropSeed: {
      color: 'rgba(255,255,255,0.6)',
      fontSize: Platform.isTV ? responsiveSize(13, 10) : 10,
      fontWeight: '500',
      marginBottom: 2,
    },
  });

export default ListCard;
