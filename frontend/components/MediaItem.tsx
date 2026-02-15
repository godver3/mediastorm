import { memo, useMemo, useRef, useEffect, useState } from 'react';

import { SpatialNavigationFocusableView } from '@/services/tv-navigation';
import { Image } from './Image';
import { Pressable, StyleProp, StyleSheet, Text, View, ViewStyle, findNodeHandle } from 'react-native';
import { MaterialCommunityIcons } from '@expo/vector-icons';

import { Title } from '../services/api';
import type { NovaTheme } from '../theme';
import { useTheme } from '../theme';
import { isTV, isAndroidTV, getTVScaleMultiplier } from '../theme/tokens/tvScale';
import { LinearGradient } from 'expo-linear-gradient';

interface MediaItemProps {
  title: Title & {
    percentWatched?: number;
    isWatched?: boolean; // Movie: watched or not
    watchState?: 'none' | 'partial' | 'complete'; // Series: no episodes, some, all (excluding specials)
    unwatchedCount?: number; // Number of unwatched episodes
    collagePosters?: string[]; // For explore cards - 4 posters for 2x2 collage
  };
  onPress?: () => void;
  onLongPress?: () => void;
  onFocus?: () => void;
  style?: StyleProp<ViewStyle>;
  badgeVisibility?: string[]; // Which badges to show: watchProgress, releaseStatus, watchState, unwatchedCount
  watchStateIconStyle?: 'colored' | 'white'; // Icon color style: colored (green/yellow) or white
  useNativeFocus?: boolean; // Use native Pressable focus instead of SpatialNavigationFocusableView (faster on Android TV)
  autoFocus?: boolean; // Give this item initial focus (only works with useNativeFocus)
  nextFocusLeft?: number; // Native tag for left focus navigation (TV) - set to self to trap focus
  trapLeftFocus?: boolean; // Prevent left focus navigation by setting nextFocusLeft to self (TV)
}

// Default badges to show when visibility array is not provided
// Note: Only watchProgress is currently implemented. Other badges are coming soon.
const DEFAULT_BADGE_VISIBILITY = ['watchProgress'];

// Check if a specific badge type should be shown
const shouldShowBadge = (badgeType: string, visibility?: string[]): boolean => {
  // Only allow implemented badge types
  const implementedBadges = ['watchProgress', 'releaseStatus', 'watchState', 'unwatchedCount'];
  if (!implementedBadges.includes(badgeType)) {
    return false;
  }
  const badges = visibility ?? DEFAULT_BADGE_VISIBILITY;
  return badges.includes(badgeType);
};

// Release status icon info for movies
export type ReleaseIconInfo = {
  name: keyof typeof MaterialCommunityIcons.glyphMap;
  color: string;
  label: string;
} | null;

// Get release status icon for movies
export const getMovieReleaseIcon = (title: Title): ReleaseIconInfo => {
  // No release data available - don't show any icon
  if (!title.homeRelease && !title.theatricalRelease) {
    return null;
  }
  // Home release available (digital/physical/streaming)
  if (title.homeRelease?.released) {
    return { name: 'home', color: '#4ade80', label: 'HOME' }; // Green - available at home
  }
  // In theaters — but if theatrical release is older than 12 months, assume home release exists
  if (title.theatricalRelease?.released) {
    if (title.theatricalRelease.date) {
      const theatricalDate = new Date(title.theatricalRelease.date);
      const twelveMonthsAgo = new Date();
      twelveMonthsAgo.setFullYear(twelveMonthsAgo.getFullYear() - 1);
      if (theatricalDate <= twelveMonthsAgo) {
        return { name: 'home', color: '#4ade80', label: 'HOME' }; // Green - assumed home release
      }
    }
    return { name: 'filmstrip', color: '#facc15', label: 'THEATER' }; // Yellow - in theaters
  }
  // Has release data but not yet released
  return { name: 'clock-outline', color: '#9ca3af', label: 'SOON' }; // Gray - coming soon
};

// Get status icon for series
const getSeriesStatusIcon = (status?: string): string | null => {
  if (!status) return null;
  const lower = status.toLowerCase();
  if (lower === 'continuing' || lower === 'returning series') {
    return '\uD83D\uDD34'; // Red circle - airing
  }
  if (lower === 'ended') {
    return '\u2B1B'; // Black square - ended
  }
  if (lower === 'upcoming' || lower === 'in production') {
    return '\u23F3'; // Hourglass - upcoming
  }
  return null;
};

// Get watch state icon
// Visual design: ● (full circle) = fully watched, ◐ (half circle) = partial
// Note: Unwatched items (empty circle) are disabled - only show watched/partial states
const getWatchStateIcon = (
  isWatched?: boolean,
  watchState?: 'none' | 'partial' | 'complete',
  mediaType?: string,
  iconStyle?: 'colored' | 'white',
): { icon: string; color: string } | null => {
  const useWhite = iconStyle === 'white';
  const greenColor = useWhite ? '#ffffff' : '#4ade80';
  const yellowColor = useWhite ? '#ffffff' : '#facc15';

  // Use watchState for both movies and series when available
  if (watchState) {
    switch (watchState) {
      case 'complete':
        return { icon: '\u25CF', color: greenColor }; // Full circle - fully watched
      case 'partial':
        return { icon: '\u25D0', color: yellowColor }; // Half circle - partially watched
      case 'none':
        return null; // Don't show badge for unwatched
      default:
        return null;
    }
  }
  // Fallback for movies without watchState (legacy: only isWatched boolean)
  if (mediaType === 'movie' && isWatched !== undefined) {
    return isWatched ? { icon: '\u25CF', color: greenColor } : null;
  }
  return null;
};

const createStyles = (theme: NovaTheme) => {
  const titleLineHeight = theme.typography.title.md.lineHeight;
  const yearLineHeight = theme.typography.caption.sm.lineHeight;
  // Unified TV scaling - tvOS is baseline, Android TV auto-derives
  const tvTextScale = isTV ? getTVScaleMultiplier() : 1;

  const titleMinHeight = titleLineHeight * 2;
  const infoMinHeight = titleMinHeight + yearLineHeight + theme.spacing.md * 2 + theme.spacing.xs;

  // Use 2:3 aspect ratio (portrait poster) consistently
  const compactPosterWidth = 160;
  const compactPosterHeight = Math.round(compactPosterWidth * 1.5); // 2:3 ratio = 240

  return StyleSheet.create({
    container: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      borderWidth: 3,
      borderColor: 'transparent',
    },
    containerFocused: {
      borderColor: theme.colors.accent.primary,
    },
    containerCompact: {
      width: compactPosterWidth,
      height: compactPosterHeight,
    },
    imageContainer: {
      width: '100%',
      aspectRatio: 2 / 3,
      backgroundColor: theme.colors.background.elevated,
      position: 'relative',
    },
    imageContainerCompact: {
      flex: 1, // Fill parent container (dimensions come from style prop or containerCompact)
    },
    poster: {
      width: '100%',
      height: '100%',
    },
    placeholder: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: theme.colors.background.elevated,
      paddingHorizontal: theme.spacing.md,
    },
    placeholderText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.muted,
      textAlign: 'center',
    },
    // Desktop/TV info below image (legacy) - keep for reference
    info: {
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.md,
      minHeight: infoMinHeight,
      justifyContent: 'flex-start',
      gap: theme.spacing.xs,
    },
    // Overlay info used on mobile (already) and now TV to match Search styling
    infoCompact: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.md,
      gap: theme.spacing.xs,
      alignItems: 'center',
      justifyContent: 'flex-end',
      minHeight: '40%',
    },
    textGradient: {
      ...StyleSheet.absoluteFillObject,
    },
    title: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
      minHeight: titleMinHeight,
      textAlign: 'center',
      zIndex: 1,
    },
    // TV title to match index page card styling
    titleTV: {
      ...theme.typography.body.md,
      ...(isTV
        ? {
            // Design for tvOS at 1.5x, Android TV auto-scales
            fontSize: Math.round(theme.typography.body.md.fontSize * 1.5 * tvTextScale),
            lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.5 * tvTextScale),
          }
        : null),
      color: theme.colors.text.primary,
      textAlign: 'center',
      zIndex: 1,
      fontWeight: '600',
    },
    titleCompact: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      textAlign: 'center',
      zIndex: 1,
      fontWeight: '600',
    },
    year: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      textAlign: 'center',
      zIndex: 1,
    },
    // TV year to match index page card styling
    yearTV: {
      ...theme.typography.body.sm,
      ...(isTV
        ? {
            // Design for tvOS at 1.25x, Android TV auto-scales
            fontSize: Math.round(theme.typography.body.sm.fontSize * 1.25 * tvTextScale),
            lineHeight: Math.round(theme.typography.body.sm.lineHeight * 1.25 * tvTextScale),
          }
        : null),
      color: theme.colors.text.secondary,
      textAlign: 'center',
      zIndex: 1,
    },
    yearCompact: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      textAlign: 'center',
      zIndex: 1,
    },
    yearPlaceholder: {
      height: yearLineHeight,
      width: '100%',
    },
    // Media type badge (TV/MOVIE) to match Search page card styling
    // Android TV: reduce size by 30% (multiply by 0.7)
    badge: {
      position: 'absolute',
      top: Math.round(theme.spacing.sm * (isAndroidTV ? 0.7 : 1)),
      right: Math.round(theme.spacing.sm * (isAndroidTV ? 0.7 : 1)),
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      paddingHorizontal: Math.round(theme.spacing.md * (isAndroidTV ? 0.7 : 1)),
      paddingVertical: Math.round(theme.spacing.xs * (isAndroidTV ? 0.7 : 1)),
      borderRadius: Math.round(theme.radius.sm * (isAndroidTV ? 0.7 : 1)),
      borderWidth: isAndroidTV ? 1 : 2,
      borderColor: theme.colors.accent.primary,
      zIndex: 2,
    },
    badgeText: {
      ...theme.typography.caption.sm,
      color: theme.colors.accent.primary,
      fontWeight: '700',
      fontSize: Math.round(16 * (isAndroidTV ? 0.7 : 1)),
      letterSpacing: 0.5,
    },
    progressBadge: {
      position: 'absolute',
      top: theme.spacing.sm,
      right: theme.spacing.sm,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      // Design badge for tvOS at 1.25x scale, Android TV auto-derives
      paddingHorizontal: Math.round(theme.spacing.sm * (isTV ? 1.25 * tvTextScale : 1)),
      paddingVertical: Math.round(theme.spacing.xs * (isTV ? 1.25 * tvTextScale : 1)),
      borderRadius: Math.round(theme.radius.sm * (isTV ? 1.25 * tvTextScale : 1)),
      zIndex: 2,
    },
    progressBadgeText: {
      ...theme.typography.caption.sm,
      // Design text for tvOS at 1.25x scale, Android TV auto-derives
      fontSize: Math.round(theme.typography.caption.sm.fontSize * (isTV ? 1.25 * tvTextScale : 1)),
      lineHeight: Math.round(theme.typography.caption.sm.lineHeight * (isTV ? 1.25 * tvTextScale : 1)),
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    // Top-left badge container (stacks release status and watch state badges vertically)
    topLeftBadgeContainer: {
      position: 'absolute',
      top: Math.round(theme.spacing.sm * (isAndroidTV ? 0.7 : 1)),
      left: Math.round(theme.spacing.sm * (isAndroidTV ? 0.7 : 1)),
      zIndex: 2,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: Math.round(theme.spacing.sm * (isTV ? 1.25 * tvTextScale : 1)),
      paddingVertical: Math.round(theme.spacing.xs * (isTV ? 1.25 * tvTextScale : 1)),
      borderRadius: Math.round(theme.radius.sm * (isTV ? 1.25 * tvTextScale : 1)),
      flexDirection: 'column',
      alignItems: 'center',
      gap: Math.round(2 * (isTV ? 1.25 * tvTextScale : 1)),
    },
    // Individual badge item style (no background, just layout)
    topLeftBadgeItem: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: Math.round(4 * (isTV ? 1.25 * tvTextScale : 1)),
    },
    // Release status badge (legacy - kept for backwards compatibility, now uses topLeftBadge)
    releaseStatusBadge: {
      position: 'absolute',
      top: Math.round(theme.spacing.sm * (isAndroidTV ? 0.7 : 1)),
      left: Math.round(theme.spacing.sm * (isAndroidTV ? 0.7 : 1)),
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: Math.round(theme.spacing.sm * (isTV ? 1.25 * tvTextScale : 1)),
      paddingVertical: Math.round(theme.spacing.xs * (isTV ? 1.25 * tvTextScale : 1)),
      borderRadius: Math.round(theme.radius.sm * (isTV ? 1.25 * tvTextScale : 1)),
      zIndex: 2,
    },
    releaseStatusIcon: {
      fontSize: Math.round(14 * (isTV ? 1.25 * tvTextScale : 1)),
    },
    // Watch state icon style
    watchStateIcon: {
      fontSize: Math.round(14 * (isTV ? 1.25 * tvTextScale : 1)),
    },
    unwatchedCountText: {
      ...theme.typography.caption.sm,
      fontSize: Math.round(theme.typography.caption.sm.fontSize * (isTV ? 1.25 * tvTextScale : 1)),
      lineHeight: Math.round(theme.typography.caption.sm.lineHeight * (isTV ? 1.25 * tvTextScale : 1)),
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    // "More" card styles for watchlist overflow
    moreCard: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      paddingHorizontal: theme.spacing.md,
    },
    moreCardText: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      fontWeight: '700',
      textAlign: 'center',
      fontSize: isTV ? 28 * tvTextScale : 22,
    },
    moreCardSubtext: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      textAlign: 'center',
      marginTop: theme.spacing.xs,
    },
    // Explore card collage styles (2x2 poster grid)
    collageContainer: {
      flex: 1,
      flexDirection: 'row',
      flexWrap: 'wrap',
    },
    collageQuadrant: {
      width: '50%',
      height: '50%',
      padding: 1,
    },
    collageImage: {
      width: '100%',
      height: '100%',
    },
  });
};

// Memoize the component to prevent unnecessary re-renders
const MediaItem = memo(function MediaItem({
  title,
  onPress,
  onLongPress,
  onFocus,
  style,
  badgeVisibility,
  watchStateIconStyle,
  useNativeFocus = false,
  autoFocus = false,
  nextFocusLeft,
  trapLeftFocus = false,
}: MediaItemProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const isCompact = theme.breakpoint === 'compact';

  // Ref and state for trapping left focus (getting own native tag)
  const pressableRef = useRef<View>(null);
  const [selfTag, setSelfTag] = useState<number | null>(null);

  // Get own native tag for trapping left focus
  useEffect(() => {
    if (!trapLeftFocus || !useNativeFocus) return;
    // Small delay to ensure ref is assigned
    const timer = setTimeout(() => {
      if (pressableRef.current) {
        const tag = findNodeHandle(pressableRef.current);
        setSelfTag(tag);
      }
    }, 50);
    return () => clearTimeout(timer);
  }, [trapLeftFocus, useNativeFocus]);

  // Determine the nextFocusLeft value - explicit prop takes precedence, then selfTag if trapping
  const effectiveNextFocusLeft = nextFocusLeft ?? (trapLeftFocus ? selfTag : undefined) ?? undefined;

  const handlePress = () => {
    onPress?.();
  };

  const handleLongPress = () => {
    onLongPress?.();
  };

  const handleFocus = () => {
    onFocus?.();
  };

  // Compute badge data
  const releaseIcon = title.mediaType === 'movie' ? getMovieReleaseIcon(title) : getSeriesStatusIcon(title.status);
  const watchStateData = getWatchStateIcon(title.isWatched, title.watchState, title.mediaType, watchStateIconStyle);

  if (isCompact) {
    return (
      <Pressable
        onPress={handlePress}
        onLongPress={handleLongPress}
        delayLongPress={500}
        style={[styles.container, styles.containerCompact, style]}
        accessibilityRole="button">
        <View style={[styles.imageContainer, styles.imageContainerCompact]}>
          {title.mediaType === 'more' ? (
            <LinearGradient
              colors={['#2a1245', '#3d1a5c', '#1a1a2e']}
              start={{ x: 0, y: 0 }}
              end={{ x: 1, y: 1 }}
              style={styles.moreCard}>
              <Text style={styles.moreCardText}>{title.name}</Text>
              <Text style={styles.moreCardSubtext}>View all</Text>
            </LinearGradient>
          ) : title.mediaType === 'explore' && title.collagePosters && title.collagePosters.length >= 4 ? (
            <View style={styles.collageContainer}>
              {title.collagePosters.slice(0, 4).map((posterUrl, index) => (
                <View key={index} style={styles.collageQuadrant}>
                  <Image source={posterUrl} style={styles.collageImage} contentFit="cover" />
                </View>
              ))}
              <View style={[styles.info, styles.infoCompact]}>
                <LinearGradient
                  pointerEvents="none"
                  colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
                  locations={[0, 0.6, 1]}
                  start={{ x: 0.5, y: 0 }}
                  end={{ x: 0.5, y: 1 }}
                  style={styles.textGradient}
                />
                <Text style={styles.titleCompact} numberOfLines={2}>
                  {title.name}
                </Text>
                <Text style={styles.yearCompact}>+{title.year} More</Text>
              </View>
            </View>
          ) : title.poster?.url ? (
            <Image source={title.poster.url} style={styles.poster} contentFit="cover" />
          ) : (
            <View style={styles.placeholder}>
              <Text style={styles.placeholderText}>No artwork available</Text>
            </View>
          )}
          {/* Top-left badges container (watch state + release status, stacked vertically) - hide for special card types */}
          {title.mediaType !== 'more' &&
            title.mediaType !== 'explore' &&
            ((shouldShowBadge('releaseStatus', badgeVisibility) && releaseIcon && typeof releaseIcon === 'object') ||
              ((shouldShowBadge('watchState', badgeVisibility) || shouldShowBadge('unwatchedCount', badgeVisibility)) &&
                (watchStateData || (title.unwatchedCount !== undefined && title.unwatchedCount > 0)))) && (
              <View style={styles.topLeftBadgeContainer}>
                {/* Watch state badge */}
                {(shouldShowBadge('watchState', badgeVisibility) || shouldShowBadge('unwatchedCount', badgeVisibility)) &&
                  (watchStateData || (title.unwatchedCount !== undefined && title.unwatchedCount > 0)) && (
                    <View style={styles.topLeftBadgeItem}>
                      {shouldShowBadge('watchState', badgeVisibility) && watchStateData && (
                        <Text style={[styles.watchStateIcon, { color: watchStateData.color }]}>{watchStateData.icon}</Text>
                      )}
                      {shouldShowBadge('unwatchedCount', badgeVisibility) &&
                        (title.mediaType === 'series' || title.mediaType === 'tv') &&
                        title.unwatchedCount !== undefined &&
                        title.unwatchedCount > 0 && <Text style={styles.unwatchedCountText}>{title.unwatchedCount}</Text>}
                    </View>
                  )}
                {/* Release status badge */}
                {shouldShowBadge('releaseStatus', badgeVisibility) && releaseIcon && typeof releaseIcon === 'object' && (
                  <View style={styles.topLeftBadgeItem}>
                    <MaterialCommunityIcons
                      name={releaseIcon.name}
                      size={styles.releaseStatusIcon.fontSize}
                      color={watchStateIconStyle === 'white' ? '#ffffff' : releaseIcon.color}
                    />
                  </View>
                )}
              </View>
            )}
          {/* Progress badge (top-right) - hide if less than 5% */}
          {title.mediaType !== 'more' &&
            title.mediaType !== 'explore' &&
            shouldShowBadge('watchProgress', badgeVisibility) &&
            title.percentWatched !== undefined &&
            title.percentWatched >= 5 && (
              <View style={styles.progressBadge}>
                <Text style={styles.progressBadgeText}>{Math.round(title.percentWatched)}%</Text>
              </View>
            )}
          {/* Info overlay - hide for special card types (they have their own overlay) */}
          {title.mediaType !== 'more' && title.mediaType !== 'explore' && (
            <View style={[styles.info, styles.infoCompact]}>
              <LinearGradient
                pointerEvents="none"
                colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
                locations={[0, 0.6, 1]}
                start={{ x: 0.5, y: 0 }}
                end={{ x: 0.5, y: 1 }}
                style={styles.textGradient}
              />
              <Text style={styles.titleCompact} numberOfLines={2}>
                {title.name}
              </Text>
              {title.year ? (
                <Text style={styles.yearCompact}>{title.year}</Text>
              ) : (
                <View style={styles.yearPlaceholder} />
              )}
            </View>
          )}
        </View>
      </Pressable>
    );
  }

  // Shared content renderer for TV - used by both native and spatial nav modes
  const renderTVContent = (_isFocused: boolean) => (
    <View style={styles.imageContainer}>
      {title.mediaType === 'more' ? (
        <LinearGradient
          colors={['#2a1245', '#3d1a5c', '#1a1a2e']}
          start={{ x: 0, y: 0 }}
          end={{ x: 1, y: 1 }}
          style={styles.moreCard}>
          <Text style={styles.moreCardText}>{title.name}</Text>
          <Text style={styles.moreCardSubtext}>View all</Text>
        </LinearGradient>
      ) : title.mediaType === 'explore' && title.collagePosters && title.collagePosters.length >= 4 ? (
        <View style={styles.collageContainer}>
          {title.collagePosters.slice(0, 4).map((posterUrl, index) => (
            <View key={index} style={styles.collageQuadrant}>
              <Image source={posterUrl} style={styles.collageImage} contentFit="cover" transition={0} />
            </View>
          ))}
          <View style={[styles.infoCompact]}>
            <LinearGradient
              pointerEvents="none"
              colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
              locations={[0, 0.6, 1]}
              start={{ x: 0.5, y: 0 }}
              end={{ x: 0.5, y: 1 }}
              style={styles.textGradient}
            />
            <Text style={styles.titleTV} numberOfLines={2}>
              {title.name}
            </Text>
            <Text style={styles.yearTV}>+{title.year} More</Text>
          </View>
        </View>
      ) : title.poster?.url ? (
        <Image source={title.poster.url} style={styles.poster} contentFit="cover" transition={0} />
      ) : (
        <View style={styles.placeholder}>
          <Text style={styles.placeholderText}>No artwork available</Text>
        </View>
      )}
      {/* Top-left badges container (watch state + release status, stacked vertically) - hide for special card types */}
      {title.mediaType !== 'more' &&
        title.mediaType !== 'explore' &&
        ((shouldShowBadge('releaseStatus', badgeVisibility) && releaseIcon && typeof releaseIcon === 'object') ||
          ((shouldShowBadge('watchState', badgeVisibility) || shouldShowBadge('unwatchedCount', badgeVisibility)) &&
            (watchStateData || (title.unwatchedCount !== undefined && title.unwatchedCount > 0)))) && (
          <View style={styles.topLeftBadgeContainer}>
            {/* Watch state badge */}
            {(shouldShowBadge('watchState', badgeVisibility) || shouldShowBadge('unwatchedCount', badgeVisibility)) &&
              (watchStateData || (title.unwatchedCount !== undefined && title.unwatchedCount > 0)) && (
                <View style={styles.topLeftBadgeItem}>
                  {shouldShowBadge('watchState', badgeVisibility) && watchStateData && (
                    <Text style={[styles.watchStateIcon, { color: watchStateData.color }]}>{watchStateData.icon}</Text>
                  )}
                  {shouldShowBadge('unwatchedCount', badgeVisibility) &&
                    (title.mediaType === 'series' || title.mediaType === 'tv') &&
                    title.unwatchedCount !== undefined &&
                    title.unwatchedCount > 0 && <Text style={styles.unwatchedCountText}>{title.unwatchedCount}</Text>}
                </View>
              )}
            {/* Release status badge */}
            {shouldShowBadge('releaseStatus', badgeVisibility) && releaseIcon && typeof releaseIcon === 'object' && (
              <View style={styles.topLeftBadgeItem}>
                <MaterialCommunityIcons
                  name={releaseIcon.name}
                  size={styles.releaseStatusIcon.fontSize}
                  color={watchStateIconStyle === 'white' ? '#ffffff' : releaseIcon.color}
                />
              </View>
            )}
          </View>
        )}
      {/* Media type badge (top-right) - hide for special card types */}
      {title.mediaType && title.mediaType !== 'more' && title.mediaType !== 'explore' && (
        <View style={styles.badge}>
          <Text style={styles.badgeText}>{title.mediaType === 'series' ? 'TV' : 'MOVIE'}</Text>
        </View>
      )}
      {/* Progress badge - hide if less than 5% */}
      {title.mediaType !== 'more' &&
        title.mediaType !== 'explore' &&
        shouldShowBadge('watchProgress', badgeVisibility) &&
        title.percentWatched !== undefined &&
        title.percentWatched >= 5 && (
          <View style={styles.progressBadge}>
            <Text style={styles.progressBadgeText}>{Math.round(title.percentWatched)}%</Text>
          </View>
        )}
      {/* Overlay info to match Search page - hide for special card types */}
      {title.mediaType !== 'more' && title.mediaType !== 'explore' && (
        <View style={[styles.infoCompact]}>
          <LinearGradient
            pointerEvents="none"
            colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
            locations={[0, 0.6, 1]}
            start={{ x: 0.5, y: 0 }}
            end={{ x: 0.5, y: 1 }}
            style={styles.textGradient}
          />
          <Text style={styles.titleTV} numberOfLines={2}>
            {title.name}
          </Text>
          {title.year ? <Text style={styles.yearTV}>{title.year}</Text> : <View style={styles.yearPlaceholder} />}
        </View>
      )}
    </View>
  );

  // TV/Desktop with native Pressable focus (faster on Android TV - no JS re-renders)
  if (useNativeFocus) {
    return (
      <Pressable
        ref={pressableRef}
        onPress={handlePress}
        onFocus={handleFocus}
        hasTVPreferredFocus={autoFocus}
        nextFocusLeft={effectiveNextFocusLeft}
        style={({ focused }) => [styles.container, style, focused && styles.containerFocused]}
        accessibilityRole="button">
        {/* Note: We pass false for isFocused since native handles visual focus via style prop */}
        {renderTVContent(false)}
      </Pressable>
    );
  }

  // TV/Desktop with SpatialNavigationFocusableView (standard mode)
  return (
    <SpatialNavigationFocusableView onSelect={handlePress} onFocus={handleFocus}>
      {({ isFocused }: { isFocused: boolean }) => (
        <View
          style={[styles.container, style, isFocused && styles.containerFocused]}
          renderToHardwareTextureAndroid={isAndroidTV}>
          {renderTVContent(isFocused)}
        </View>
      )}
    </SpatialNavigationFocusableView>
  );
});

export default MediaItem;
