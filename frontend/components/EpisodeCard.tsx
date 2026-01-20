import React, { memo, useMemo, useState, useEffect } from 'react';
import { Image } from './Image';
import { StyleSheet, Text, View, Platform, Pressable } from 'react-native';
import Animated, { useSharedValue, withTiming, Easing } from 'react-native-reanimated';

import type { SeriesEpisode } from '../services/api';
import type { NovaTheme } from '../theme';
import { useTheme } from '../theme';
import { tvScale, isTV, getTVScaleMultiplier } from '../theme/tokens/tvScale';

interface EpisodeCardProps {
  episode: SeriesEpisode;
  percentWatched?: number | null;
}

const formatAirDate = (dateString?: string): string | null => {
  if (!dateString) return null;

  try {
    // Parse the date string (format: YYYY-MM-DD)
    const date = new Date(dateString + 'T00:00:00');
    if (isNaN(date.getTime())) {
      console.log('[EpisodeCard] Invalid date format:', dateString);
      return null;
    }

    const formatted = date.toLocaleDateString('en-US', {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
    });

    return formatted;
  } catch (error) {
    console.log('[EpisodeCard] Error formatting date:', error);
    return null;
  }
};

// Number of lines to show when collapsed
const COLLAPSED_LINES = 3;

const createStyles = (theme: NovaTheme) => {
  const isMobile = !isTV;
  // Unified TV scaling - tvOS is baseline, Android TV auto-derives
  const tvTextScale = isTV ? getTVScaleMultiplier() : 1;

  const styleSheet = StyleSheet.create({
    container: {
      backgroundColor: `${theme.colors.background.surface}B3`, // 70% opacity (30% transparent)
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      flexDirection: 'column',
      alignItems: 'stretch',
    },
    topRow: {
      flexDirection: 'row',
      height: tvScale(343, 172),
    },
    imageContainer: {
      width: tvScale(499, 250),
      height: tvScale(343, 172),
      backgroundColor: theme.colors.background.elevated,
      position: 'relative',
    },
    episodeImage: {
      width: '100%',
      height: '100%',
    },
    imagePlaceholder: {
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
    releaseDateOverlay: {
      position: 'absolute',
      bottom: isTV ? theme.spacing.sm : theme.spacing.xs,
      left: isTV ? theme.spacing.sm : theme.spacing.xs,
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      paddingHorizontal: isTV ? theme.spacing.md : theme.spacing.sm,
      paddingVertical: isTV ? theme.spacing.sm : theme.spacing.xs,
      borderRadius: theme.radius.sm,
      zIndex: 10,
    },
    releaseDateText: {
      ...theme.typography.caption.sm,
      ...(isTV
        ? {
            // Design for tvOS at 1.4x, Android TV auto-scales
            fontSize: Math.round(theme.typography.caption.sm.fontSize * 1.4 * tvTextScale),
            lineHeight: Math.round(theme.typography.caption.sm.lineHeight * 1.4 * tvTextScale),
          }
        : {
            fontSize: 11,
            lineHeight: 14,
          }),
      color: theme.colors.text.primary,
      fontWeight: '700',
    },
    progressCorner: {
      position: 'absolute',
      top: 0,
      right: 0,
      width: 80,
      height: 80,
      overflow: 'hidden',
      zIndex: 10,
    },
    progressTriangle: {
      position: 'absolute',
      top: -40,
      right: -40,
      width: 80,
      height: 80,
      backgroundColor: theme.colors.accent.primary,
      transform: [{ rotate: '45deg' }],
    },
    progressBadgeTextWrapper: {
      position: 'absolute',
      top: 15,
      right: 5,
      transform: [{ rotate: '45deg' }],
    },
    progressBadgeText: {
      fontSize: 12,
      lineHeight: 14,
      color: '#FFFFFF',
      fontWeight: '700',
    },
    contentContainer: {
      flex: 1,
      padding: isTV ? theme.spacing.lg : theme.spacing.sm,
      justifyContent: 'center',
    },
    topContent: {
      gap: isTV ? theme.spacing.sm : theme.spacing.xs,
    },
    overviewSection: {
      paddingHorizontal: isMobile ? theme.spacing.sm : theme.spacing.lg,
      paddingBottom: isMobile ? theme.spacing.sm : theme.spacing.lg,
      paddingTop: isMobile ? theme.spacing.xs : theme.spacing.sm,
    },
    overviewHidden: {
      position: 'absolute',
      opacity: 0,
    },
    overviewToggle: {
      color: theme.colors.text.muted,
      fontSize: 14,
      marginTop: 4,
    },
    episodeCode: {
      ...theme.typography.body.sm,
      ...(isTV
        ? {
            // Design for tvOS at 1.3x, Android TV auto-scales
            fontSize: Math.round(theme.typography.body.md.fontSize * 1.3 * tvTextScale),
            lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.3 * tvTextScale),
          }
        : null),
      color: theme.colors.accent.primary,
      fontWeight: '700',
      letterSpacing: 0.5,
    },
    episodeTitle: {
      ...theme.typography.body.lg,
      ...(isTV
        ? {
            // Design for tvOS at 1.2x, Android TV auto-scales
            fontSize: Math.round(theme.typography.title.md.fontSize * 1.2 * tvTextScale),
            lineHeight: Math.round(theme.typography.title.md.lineHeight * 1.2 * tvTextScale),
          }
        : null),
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    episodeOverview: {
      ...theme.typography.body.sm,
      ...(isTV
        ? {
            // Design for tvOS at 1.3x, Android TV auto-scales
            fontSize: Math.round(theme.typography.body.sm.fontSize * 1.3 * tvTextScale),
            lineHeight: Math.round(theme.typography.body.sm.lineHeight * 1.3 * tvTextScale),
          }
        : null),
      color: theme.colors.text.secondary,
    },
    bottomContent: {
      flexDirection: 'row',
      gap: theme.spacing.sm,
      alignItems: 'center',
      marginTop: theme.spacing.sm,
    },
    metadataText: {
      ...theme.typography.caption.sm,
      ...(isTV
        ? {
            // Design for tvOS at 1.4x, Android TV auto-scales
            fontSize: Math.round(theme.typography.caption.sm.fontSize * 1.4 * tvTextScale),
            lineHeight: Math.round(theme.typography.caption.sm.lineHeight * 1.4 * tvTextScale),
          }
        : null),
      color: theme.colors.text.muted,
    },
  });

  return {
    ...styleSheet,
    // Computed value for minimum collapsed height (not a style)
    minCollapsedHeight: theme.typography.body.sm.lineHeight * COLLAPSED_LINES,
  };
};

const EpisodeCard = memo(function EpisodeCard({ episode, percentWatched }: EpisodeCardProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const [isOverviewExpanded, setIsOverviewExpanded] = useState(false);
  const [collapsedHeight, setCollapsedHeight] = useState(0);
  const [expandedHeight, setExpandedHeight] = useState(0);
  const overviewHeight = useSharedValue(styles.minCollapsedHeight + 4);

  // Reset state when episode changes
  useEffect(() => {
    setIsOverviewExpanded(false);
    setCollapsedHeight(0);
    setExpandedHeight(0);
    // Set initial height to minimum to avoid visual jump
    overviewHeight.value = styles.minCollapsedHeight + 4;
  }, [episode.id, styles.minCollapsedHeight]);

  const episodeCode = useMemo(() => {
    const season = String(episode.seasonNumber).padStart(2, '0');
    const episodeNum = String(episode.episodeNumber).padStart(2, '0');
    return `S${season}E${episodeNum}`;
  }, [episode.seasonNumber, episode.episodeNumber]);

  const airDate = useMemo(() => {
    console.log('[EpisodeCard] Episode data:', {
      name: episode.name,
      airedDate: episode.airedDate,
      seasonNumber: episode.seasonNumber,
      episodeNumber: episode.episodeNumber,
    });
    const formatted = formatAirDate(episode.airedDate);
    console.log('[EpisodeCard] Formatted air date:', formatted);
    return formatted;
  }, [episode.airedDate, episode.name, episode.seasonNumber, episode.episodeNumber]);

  const isMobile = !Platform.isTV;

  return (
    <View style={styles.container}>
      <View style={[styles.progressCorner, { opacity: percentWatched != null && percentWatched > 0 ? 1 : 0 }]}>
        <View style={styles.progressTriangle} />
        <View style={styles.progressBadgeTextWrapper}>
          <Text style={styles.progressBadgeText}>{`${percentWatched ?? 0}%`}</Text>
        </View>
      </View>
      <View style={styles.topRow}>
        <View style={styles.imageContainer}>
          {episode.image?.url ? (
            <Image source={episode.image.url} style={styles.episodeImage} contentFit="cover" />
          ) : (
            <View style={styles.imagePlaceholder}>
              <Text style={styles.placeholderText}>No image available</Text>
            </View>
          )}
          {airDate ? (
            <View style={styles.releaseDateOverlay}>
              <Text style={styles.releaseDateText}>{airDate}</Text>
            </View>
          ) : (
            (console.log('[EpisodeCard] No air date to display - airDate is:', airDate), null)
          )}
        </View>

        <View style={styles.contentContainer}>
          <View style={styles.topContent}>
            <Text style={styles.episodeCode}>{episodeCode}</Text>
            <Text style={styles.episodeTitle} numberOfLines={2}>
              {episode.name}
            </Text>
            {!isMobile && episode.overview && (
              <Text style={styles.episodeOverview} numberOfLines={2}>
                {episode.overview}
              </Text>
            )}
          </View>

          {episode.runtimeMinutes && (
            <View style={styles.bottomContent}>
              <Text style={styles.metadataText}>{episode.runtimeMinutes} minutes</Text>
            </View>
          )}
        </View>
      </View>

      {isMobile && episode.overview && (
        <View style={styles.overviewSection}>
          <Pressable
            onPress={() => {
              const targetHeight = isOverviewExpanded ? collapsedHeight : expandedHeight;
              if (targetHeight > 0) {
                overviewHeight.value = withTiming(targetHeight, {
                  duration: 300,
                  easing: Easing.bezier(0.25, 0.1, 0.25, 1),
                });
              }
              setIsOverviewExpanded((prev) => !prev);
            }}>
            <View>
              {/* Hidden text to measure collapsed height */}
              <Text
                style={[styles.episodeOverview, styles.overviewHidden]}
                numberOfLines={COLLAPSED_LINES}
                onLayout={(e) => {
                  const height = e.nativeEvent.layout.height;
                  if (height > 0 && collapsedHeight === 0) {
                    const bufferedHeight = height + 4;
                    setCollapsedHeight(bufferedHeight);
                    overviewHeight.value = bufferedHeight;
                  }
                }}>
                {episode.overview}
              </Text>
              {/* Hidden text to measure full height */}
              <Text
                style={[styles.episodeOverview, styles.overviewHidden]}
                onLayout={(e) => {
                  const height = e.nativeEvent.layout.height;
                  if (height > 0 && expandedHeight === 0) {
                    setExpandedHeight(height + 4);
                  }
                }}>
                {episode.overview}
              </Text>
              {/* Visible animated container */}
              <Animated.View
                style={[{ overflow: 'hidden' }, collapsedHeight > 0 ? { height: overviewHeight } : undefined]}>
                <Text
                  style={[styles.episodeOverview, { marginBottom: 0 }]}
                  numberOfLines={isOverviewExpanded ? undefined : COLLAPSED_LINES}>
                  {episode.overview}
                </Text>
              </Animated.View>
            </View>
            {expandedHeight > collapsedHeight && (
              <Text style={styles.overviewToggle}>{isOverviewExpanded ? 'Show less' : 'More'}</Text>
            )}
          </Pressable>
        </View>
      )}
    </View>
  );
});

export default EpisodeCard;
