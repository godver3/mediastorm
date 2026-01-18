/**
 * TV Episode Thumbnail - Individual focusable episode card for TV carousel
 * Uses native Pressable focus with visual states for focused/active/watched
 */

import React, { memo, useMemo } from 'react';
import { StyleSheet, Text, View } from 'react-native';
import { Image } from '../Image';
import { LinearGradient } from 'expo-linear-gradient';
import type { SeriesEpisode } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';

// Pre-computed gradient props - avoids creating new arrays on every render
const GRADIENT_COLORS = ['transparent', 'rgba(0, 0, 0, 0.85)'] as const;
const GRADIENT_LOCATIONS = [0.3, 1] as const;

// Card dimensions - design for tvOS, Android TV auto-scales (50% larger for TV viewing)
const THUMBNAIL_WIDTH = tvScale(360);
const THUMBNAIL_HEIGHT = tvScale(202); // 16:9 aspect ratio

export { THUMBNAIL_WIDTH, THUMBNAIL_HEIGHT };

interface TVEpisodeThumbnailProps {
  episode: SeriesEpisode;
  isActive: boolean; // Currently selected episode for playback
  isFocused: boolean; // Has D-pad focus (from style function)
  isWatched: boolean;
  progress: number; // 0-100 percent watched
  theme: NovaTheme;
  showSelectedBadge?: boolean; // Show "Selected - Press to play" indicator
}

const formatEpisodeCode = (episode: SeriesEpisode): string => {
  const season = String(episode.seasonNumber).padStart(2, '0');
  const episodeNum = String(episode.episodeNumber).padStart(2, '0');
  return `S${season}E${episodeNum}`;
};

const TVEpisodeThumbnail = memo(
  function TVEpisodeThumbnail({
    episode,
    isActive,
    isFocused,
    isWatched,
    progress,
    theme,
    showSelectedBadge = false,
  }: TVEpisodeThumbnailProps) {
    // Memoize styles to prevent recreation on every render
    const styles = useMemo(() => createStyles(theme), [theme]);
    const episodeCode = formatEpisodeCode(episode);
    const showProgress = progress > 0 && progress < 100;

    return (
      <View style={[styles.container, isFocused && styles.containerFocused, isActive && styles.containerActive]}>
        {/* Thumbnail Image */}
        {episode.image?.url ? (
          <Image source={episode.image.url} style={styles.image} contentFit="cover" transition={0} />
        ) : (
          <View style={styles.placeholder} />
        )}

        {/* Dark overlay for text readability */}
        <View style={styles.darkOverlay} />

        {/* Episode number badge (top-left) */}
        <View style={styles.episodeBadge}>
          <Text style={styles.episodeBadgeText}>{episode.episodeNumber}</Text>
        </View>

        {/* Progress percentage or watched checkmark (top-right) */}
        {showProgress ? (
          <View style={styles.progressBadge}>
            <Text style={styles.progressBadgeText}>{Math.round(progress)}%</Text>
          </View>
        ) : isWatched ? (
          <View style={styles.watchedBadge}>
            <Text style={styles.watchedCheckmark}>✓</Text>
          </View>
        ) : null}

        {/* Episode code overlay at bottom */}
        <LinearGradient colors={GRADIENT_COLORS} locations={GRADIENT_LOCATIONS} style={styles.bottomGradient}>
          <Text style={styles.episodeCode}>{episodeCode}</Text>
        </LinearGradient>

        {/* Progress bar at bottom */}
        {showProgress && (
          <View style={styles.progressBarContainer}>
            <View style={styles.progressBarBackground} />
            <View style={[styles.progressBarFill, { width: `${progress}%` }]} />
          </View>
        )}

        {/* Selected badge - bottom left indicator */}
        {showSelectedBadge && (
          <View style={styles.selectedBadge}>
            <Text style={styles.selectedBadgeText}>Selected</Text>
          </View>
        )}
      </View>
    );
  },
  // Custom comparison - only re-render when visual state actually changes
  (prev, next) => {
    return (
      prev.episode.seasonNumber === next.episode.seasonNumber &&
      prev.episode.episodeNumber === next.episode.episodeNumber &&
      prev.episode.image?.url === next.episode.image?.url &&
      prev.isActive === next.isActive &&
      prev.isFocused === next.isFocused &&
      prev.isWatched === next.isWatched &&
      prev.progress === next.progress &&
      prev.showSelectedBadge === next.showSelectedBadge &&
      prev.theme === next.theme
    );
  },
);

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    container: {
      width: THUMBNAIL_WIDTH,
      height: THUMBNAIL_HEIGHT,
      borderRadius: tvScale(8),
      overflow: 'hidden',
      backgroundColor: theme.colors.background.elevated,
      borderWidth: tvScale(3),
      borderColor: 'transparent',
    },
    containerFocused: {
      // No zoom on focus - just border highlight
      borderColor: theme.colors.accent.primary,
    },
    containerActive: {
      borderColor: theme.colors.accent.primary,
    },
    image: {
      width: '100%',
      height: '100%',
    },
    placeholder: {
      flex: 1,
      backgroundColor: theme.colors.background.elevated,
    },
    darkOverlay: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(0, 0, 0, 0.3)',
    },
    episodeBadge: {
      position: 'absolute',
      top: tvScale(8),
      left: tvScale(8),
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: tvScale(10),
      paddingVertical: tvScale(4),
      borderRadius: tvScale(4),
    },
    episodeBadgeText: {
      color: '#fff',
      fontSize: tvScale(14),
      fontWeight: '700',
    },
    progressBadge: {
      position: 'absolute',
      top: tvScale(8),
      right: tvScale(8),
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: tvScale(8),
      paddingVertical: tvScale(4),
      borderRadius: tvScale(4),
    },
    progressBadgeText: {
      color: theme.colors.accent.primary,
      fontSize: tvScale(12),
      fontWeight: '700',
    },
    watchedBadge: {
      position: 'absolute',
      top: tvScale(8),
      right: tvScale(8),
      width: tvScale(28),
      height: tvScale(28),
      borderRadius: tvScale(14),
      backgroundColor: theme.colors.accent.primary,
      justifyContent: 'center',
      alignItems: 'center',
    },
    watchedCheckmark: {
      color: '#fff',
      fontSize: tvScale(16),
      fontWeight: '700',
    },
    bottomGradient: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      paddingTop: tvScale(24),
      paddingBottom: tvScale(8),
      paddingHorizontal: tvScale(8),
      alignItems: 'center',
    },
    episodeCode: {
      color: '#fff',
      fontSize: tvScale(14),
      fontWeight: '700',
      textShadowColor: 'rgba(0, 0, 0, 0.8)',
      textShadowOffset: { width: 0, height: 1 },
      textShadowRadius: 2,
    },
    progressBarContainer: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: tvScale(4),
    },
    progressBarBackground: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(255, 255, 255, 0.4)',
    },
    progressBarFill: {
      position: 'absolute',
      top: 0,
      left: 0,
      bottom: 0,
      backgroundColor: theme.colors.accent.primary,
    },
    selectedBadge: {
      position: 'absolute',
      bottom: tvScale(8),
      left: tvScale(8),
      backgroundColor: theme.colors.accent.primary,
      paddingHorizontal: tvScale(8),
      paddingVertical: tvScale(3),
      borderRadius: tvScale(4),
    },
    selectedBadgeText: {
      color: theme.colors.text.inverse,
      fontSize: tvScale(11),
      fontWeight: '600',
    },
  });

export default TVEpisodeThumbnail;
