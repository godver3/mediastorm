import { memo, useEffect, useMemo } from 'react';
import { Image } from './Image';
import { Platform, StyleSheet, Text, View } from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';

import type { SeriesEpisode } from '../services/api';
import type { NovaTheme } from '../theme';
import { useTheme } from '../theme';
import { SpatialNavigationFocusableView, SpatialNavigationNode } from '@/services/tv-navigation';

interface TVEpisodeStripProps {
  activeEpisode: SeriesEpisode;
  allEpisodes: SeriesEpisode[];
  selectedSeason: { number: number } | null;
  percentWatched?: number | null;
  onSelect?: () => void;
  onFocus?: () => void;
  onBlur?: () => void;
}

// Apple TV needs larger dimensions due to different pixel density handling
const isAppleTV = Platform.isTV && Platform.OS === 'ios';
const TV_SCALE = isAppleTV ? 1.0 : 0.5; // Android TV renders at roughly 2x, so scale down

// 56% larger than original (200, 120, 240)
const STRIP_HEIGHT = Math.round(312 * TV_SCALE);
const THUMBNAIL_WIDTH = Math.round(187 * TV_SCALE);
const SELECTED_IMAGE_WIDTH = Math.round(374 * TV_SCALE);

const formatEpisodeCode = (episode: SeriesEpisode): string => {
  const season = String(episode.seasonNumber).padStart(2, '0');
  const episodeNum = String(episode.episodeNumber).padStart(2, '0');
  return `S${season}E${episodeNum}`;
};

const formatAirDate = (dateString?: string): string | null => {
  if (!dateString) return null;

  try {
    const date = new Date(dateString + 'T00:00:00');
    if (isNaN(date.getTime())) return null;

    return date.toLocaleDateString('en-US', {
      month: 'short',
      day: 'numeric',
      year: 'numeric',
    });
  } catch {
    return null;
  }
};

const createStyles = (theme: NovaTheme) => {
  return StyleSheet.create({
    container: {
      flexDirection: 'row',
      gap: theme.spacing.sm,
      marginBottom: theme.spacing.xl,
      height: STRIP_HEIGHT,
      alignItems: 'stretch',
    },
    // Episode strip section (left or right)
    episodeStripSection: {
      flexDirection: 'row',
      gap: theme.spacing.sm,
    },
    // Wrapper for focusable selected episode
    selectedEpisodeWrapper: {
      width: '40%',
      height: STRIP_HEIGHT,
    },
    // Selected episode card
    selectedEpisodeContainer: {
      width: '100%',
      height: STRIP_HEIGHT,
      backgroundColor: `${theme.colors.background.surface}B3`,
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      flexDirection: 'row',
      borderWidth: isAppleTV ? 3 : 2,
      borderColor: theme.colors.accent.primary,
    },
    selectedEpisodeContainerFocused: {
      borderColor: theme.colors.text.primary,
    },
    selectedImageContainer: {
      width: SELECTED_IMAGE_WIDTH,
      height: STRIP_HEIGHT,
      backgroundColor: theme.colors.background.elevated,
      position: 'relative',
    },
    selectedEpisodeImage: {
      width: '100%',
      height: '100%',
    },
    selectedImagePlaceholder: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: theme.colors.background.elevated,
    },
    placeholderText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.muted,
      textAlign: 'center',
    },
    releaseDateOverlay: {
      position: 'absolute',
      bottom: theme.spacing.sm,
      left: theme.spacing.sm,
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.sm,
      zIndex: 10,
    },
    releaseDateText: {
      fontSize: Math.round(theme.typography.caption.sm.fontSize * (isAppleTV ? 1.3 : 1.0)),
      lineHeight: Math.round(theme.typography.caption.sm.lineHeight * (isAppleTV ? 1.3 : 1.0)),
      color: theme.colors.text.primary,
      fontWeight: '700',
    },
    selectedContentContainer: {
      flex: 1,
      padding: theme.spacing.lg,
      justifyContent: 'space-between',
    },
    selectedTopContent: {
      gap: theme.spacing.sm,
      flex: 1,
    },
    selectedTitleRow: {
      flexDirection: 'row',
      alignItems: 'baseline',
      gap: theme.spacing.sm,
      flexWrap: 'wrap',
    },
    selectedEpisodeCode: {
      fontSize: Math.round(theme.typography.body.md.fontSize * (isAppleTV ? 1.2 : 1.0)),
      lineHeight: Math.round(theme.typography.body.md.lineHeight * (isAppleTV ? 1.2 : 1.0)),
      color: theme.colors.accent.primary,
      fontWeight: '700',
      letterSpacing: 0.5,
    },
    selectedEpisodeTitle: {
      fontSize: Math.round(theme.typography.body.md.fontSize * (isAppleTV ? 1.2 : 1.0)),
      lineHeight: Math.round(theme.typography.body.md.lineHeight * (isAppleTV ? 1.2 : 1.0)),
      color: theme.colors.text.primary,
      fontWeight: '700',
      flex: 1,
    },
    selectedEpisodeOverview: {
      fontSize: Math.round(theme.typography.body.sm.fontSize * (isAppleTV ? 1.2 : 1.0)),
      lineHeight: Math.round(theme.typography.body.sm.lineHeight * (isAppleTV ? 1.2 : 1.0)),
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.xs,
    },
    selectedMetadataText: {
      // Android TV: reduced slightly (1.0 -> 0.95)
      fontSize: Math.round(theme.typography.caption.sm.fontSize * (isAppleTV ? 1.3 : 0.95)),
      lineHeight: Math.round(theme.typography.caption.sm.lineHeight * (isAppleTV ? 1.3 : 0.95)),
      color: theme.colors.text.muted,
      marginTop: theme.spacing.sm,
    },
    // Individual episode thumbnail - full height
    episodeThumbnail: {
      width: THUMBNAIL_WIDTH,
      height: STRIP_HEIGHT,
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.elevated,
    },
    episodeThumbnailFocused: {
      transform: [{ scale: 1.08 }],
      borderWidth: isAppleTV ? 3 : 2,
      borderColor: theme.colors.accent.primary,
    },
    thumbnailImage: {
      width: '100%',
      height: '100%',
    },
    thumbnailPlaceholder: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: theme.colors.background.elevated,
    },
    thumbnailOverlay: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.xs,
      alignItems: 'center',
    },
    thumbnailCode: {
      // Android TV: reduced 30% (12 -> 8)
      fontSize: isAppleTV ? 14 : 8,
      fontWeight: '700',
      color: theme.colors.text.primary,
      textShadowColor: 'rgba(0, 0, 0, 0.8)',
      textShadowOffset: { width: 0, height: 1 },
      textShadowRadius: 2,
    },
    thumbnailDarkOverlay: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
    },
    thumbnailEdgeFade: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
    },
    progressCorner: {
      position: 'absolute',
      top: 0,
      right: 0,
      width: Math.round(80 * TV_SCALE),
      height: Math.round(80 * TV_SCALE),
      overflow: 'hidden',
      zIndex: 10,
    },
    progressTriangle: {
      position: 'absolute',
      top: Math.round(-40 * TV_SCALE),
      right: Math.round(-40 * TV_SCALE),
      width: Math.round(80 * TV_SCALE),
      height: Math.round(80 * TV_SCALE),
      backgroundColor: theme.colors.accent.primary,
      transform: [{ rotate: '45deg' }],
    },
    progressBadgeTextWrapper: {
      position: 'absolute',
      top: Math.round(15 * TV_SCALE),
      right: Math.round(5 * TV_SCALE),
      transform: [{ rotate: '45deg' }],
    },
    progressBadgeText: {
      fontSize: Math.round(12 * TV_SCALE),
      lineHeight: Math.round(14 * TV_SCALE),
      color: '#FFFFFF',
      fontWeight: '700',
    },
  });
};

interface EpisodeThumbnailProps {
  episode: SeriesEpisode;
  styles: ReturnType<typeof createStyles>;
  fadeEdge?: 'left' | 'right';
}

const EpisodeThumbnail = memo(function EpisodeThumbnail({ episode, styles, fadeEdge }: EpisodeThumbnailProps) {
  return (
    <View>
      <View style={styles.episodeThumbnail}>
        {episode.image?.url ? (
          <Image source={episode.image.url} style={styles.thumbnailImage} contentFit="cover" />
        ) : (
          <View style={styles.thumbnailPlaceholder} />
        )}
        <View style={styles.thumbnailDarkOverlay} />
        {fadeEdge && (
          <LinearGradient
            colors={['black', 'transparent']}
            locations={[0, 0.6]}
            start={{ x: fadeEdge === 'left' ? 0 : 1, y: 0.5 }}
            end={{ x: fadeEdge === 'left' ? 1 : 0, y: 0.5 }}
            style={styles.thumbnailEdgeFade}
          />
        )}
        <LinearGradient
          colors={['transparent', 'rgba(0, 0, 0, 0.85)']}
          locations={[0.4, 1]}
          style={styles.thumbnailOverlay}>
          <Text style={styles.thumbnailCode}>{formatEpisodeCode(episode)}</Text>
        </LinearGradient>
      </View>
    </View>
  );
});

const TVEpisodeStrip = memo(function TVEpisodeStrip({
  activeEpisode,
  allEpisodes,
  selectedSeason,
  percentWatched,
  onSelect,
  onFocus,
  onBlur,
}: TVEpisodeStripProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);

  // Track mount/unmount for debugging
  useEffect(() => {
    console.log('[TVEpisodeStrip NAV DEBUG] ========== MOUNTED ==========');
    console.log('[TVEpisodeStrip NAV DEBUG] activeEpisode:', activeEpisode?.episodeNumber);
    return () => {
      console.log('[TVEpisodeStrip NAV DEBUG] ========== UNMOUNTED ==========');
    };
  }, []);

  console.log('[TVEpisodeStrip NAV DEBUG] Rendering, activeEpisode:', activeEpisode?.episodeNumber);

  // Get episodes for the current season, split into before and after active episode
  const { episodesBefore, episodesAfter } = useMemo(() => {
    const seasonNumber = selectedSeason?.number ?? activeEpisode.seasonNumber;
    const seasonEpisodes = allEpisodes
      .filter((ep) => ep.seasonNumber === seasonNumber)
      .sort((a, b) => a.episodeNumber - b.episodeNumber);

    const before: SeriesEpisode[] = [];
    const after: SeriesEpisode[] = [];

    for (const ep of seasonEpisodes) {
      if (ep.episodeNumber < activeEpisode.episodeNumber) {
        before.push(ep);
      } else if (ep.episodeNumber > activeEpisode.episodeNumber) {
        after.push(ep);
      }
    }

    // Limit total to 5 other episodes (6 total including selected)
    // Show up to 2 before and up to 3 after, adjusting if one side has fewer
    let maxBefore = 2;
    let maxAfter = 3;

    if (before.length < maxBefore) {
      maxAfter = Math.min(after.length, 5 - before.length);
    } else if (after.length < maxAfter) {
      maxBefore = Math.min(before.length, 5 - after.length);
    }

    return {
      episodesBefore: before.slice(-maxBefore), // Take last N (closest to current)
      episodesAfter: after.slice(0, maxAfter), // Take first N (closest to current)
    };
  }, [allEpisodes, selectedSeason, activeEpisode]);

  const episodeCode = formatEpisodeCode(activeEpisode);
  const airDate = formatAirDate(activeEpisode.airedDate);

  return (
    <SpatialNavigationNode
      orientation="horizontal"
      focusKey="episode-strip-row"
      onActive={() => console.log('[TVEpisodeStrip NAV DEBUG] episode-strip-row ACTIVE')}
      onInactive={() => console.log('[TVEpisodeStrip NAV DEBUG] episode-strip-row INACTIVE')}>
      <View style={styles.container}>
        {/* Episodes before (left side) */}
        {episodesBefore.length > 0 && (
          <View style={styles.episodeStripSection}>
            {episodesBefore.map((episode, index) => (
              <EpisodeThumbnail
                key={`${episode.seasonNumber}-${episode.episodeNumber}`}
                episode={episode}
                styles={styles}
                fadeEdge={index === 0 ? 'left' : undefined}
              />
            ))}
          </View>
        )}

        {/* Selected episode - center, focusable */}
        <View style={styles.selectedEpisodeWrapper}>
          <SpatialNavigationFocusableView
            focusKey={`selected-episode-${activeEpisode.seasonNumber}-${activeEpisode.episodeNumber}`}
            onSelect={onSelect}
            onFocus={onFocus}
            onBlur={onBlur}>
            {({ isFocused }: { isFocused: boolean }) => (
              <View
                style={[styles.selectedEpisodeContainer, isFocused && styles.selectedEpisodeContainerFocused]}>
                <View style={styles.selectedImageContainer}>
                  <Image
                    source={activeEpisode.image?.url || ''}
                    style={styles.selectedEpisodeImage}
                    contentFit="cover"
                  />
                  {airDate && (
                    <View style={styles.releaseDateOverlay}>
                      <Text style={styles.releaseDateText}>{airDate}</Text>
                    </View>
                  )}
                </View>

                <View style={styles.selectedContentContainer}>
                  <View style={styles.selectedTopContent}>
                    <View style={styles.selectedTitleRow}>
                      <Text style={styles.selectedEpisodeCode}>{episodeCode}</Text>
                      <Text style={styles.selectedEpisodeTitle} numberOfLines={1}>
                        {activeEpisode.name}
                      </Text>
                    </View>
                    {activeEpisode.overview && (
                      <Text style={styles.selectedEpisodeOverview} numberOfLines={3}>
                        {activeEpisode.overview}
                      </Text>
                    )}
                  </View>
                  {activeEpisode.runtimeMinutes && (
                    <Text style={styles.selectedMetadataText}>{activeEpisode.runtimeMinutes} minutes</Text>
                  )}
                </View>
                <View
                  style={[styles.progressCorner, { opacity: percentWatched != null && percentWatched > 0 ? 1 : 0 }]}>
                  <View style={styles.progressTriangle} />
                  <View style={styles.progressBadgeTextWrapper}>
                    <Text style={styles.progressBadgeText}>{`${percentWatched ?? 0}%`}</Text>
                  </View>
                </View>
              </View>
            )}
          </SpatialNavigationFocusableView>
        </View>

        {/* Episodes after (right side) */}
        {episodesAfter.length > 0 && (
          <View style={styles.episodeStripSection}>
            {episodesAfter.map((episode, index) => (
              <EpisodeThumbnail
                key={`${episode.seasonNumber}-${episode.episodeNumber}`}
                episode={episode}
                styles={styles}
                fadeEdge={index === episodesAfter.length - 1 ? 'right' : undefined}
              />
            ))}
          </View>
        )}
      </View>
    </SpatialNavigationNode>
  );
});

export default TVEpisodeStrip;
