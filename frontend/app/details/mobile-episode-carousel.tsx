/**
 * Netflix-style episode carousel for mobile devices
 * Shows horizontal season selector + horizontal scrolling episode cards with thumbnails
 */

import React, { memo, useMemo, useRef, useCallback, useEffect, useState } from 'react';
import { View, Text, Pressable, StyleSheet, ActivityIndicator, useWindowDimensions, ScrollView } from 'react-native';
import Animated, { useAnimatedScrollHandler, useSharedValue } from 'react-native-reanimated';
import { Image } from '@/components/Image';
import { Ionicons } from '@expo/vector-icons';
import type { NovaTheme } from '@/theme';
import type { SeriesEpisode, SeriesSeason } from '@/services/api';
import MarqueeText from '@/components/tv/MarqueeText';
import { getSeasonLabel } from './utils';

interface MobileEpisodeCarouselProps {
  seasons: SeriesSeason[];
  selectedSeason: SeriesSeason | null;
  episodes: SeriesEpisode[];
  activeEpisode: SeriesEpisode | null;
  isLoading?: boolean;
  onSeasonSelect: (season: SeriesSeason) => void;
  onEpisodeSelect: (episode: SeriesEpisode) => void;
  onEpisodePlay?: (episode: SeriesEpisode) => void;
  onEpisodeLongPress?: (episode: SeriesEpisode) => void;
  isEpisodeWatched?: (episode: SeriesEpisode) => boolean;
  getEpisodeProgress?: (episode: SeriesEpisode) => number;
  theme: NovaTheme;
}

// Card dimensions (56% larger than original 155x87)
const CARD_WIDTH = 242;
const CARD_HEIGHT = 136; // 16:9 aspect ratio
const CARD_GAP = 12;
const SEASON_CHIP_GAP = 10;
const SEASON_CHIP_MIN_WIDTH = 90; // Minimum width for consistent snapping

const MobileEpisodeCarousel = memo(function MobileEpisodeCarousel({
  seasons,
  selectedSeason,
  episodes,
  activeEpisode,
  isLoading,
  onSeasonSelect,
  onEpisodeSelect,
  onEpisodePlay,
  onEpisodeLongPress,
  isEpisodeWatched,
  getEpisodeProgress,
  theme,
}: MobileEpisodeCarouselProps) {
  const { width: screenWidth } = useWindowDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth), [theme, screenWidth]);
  const episodeScrollRef = useRef<Animated.ScrollView>(null);
  const seasonScrollRef = useRef<ScrollView>(null);
  const scrollX = useSharedValue(0);

  // Track which episode was just toggled for visual feedback
  const [flashingEpisode, setFlashingEpisode] = useState<string | null>(null);

  const scrollHandler = useAnimatedScrollHandler({
    onScroll: (event) => {
      scrollX.value = event.contentOffset.x;
    },
  });

  const handleSeasonPress = useCallback(
    (season: SeriesSeason) => {
      onSeasonSelect(season);
    },
    [onSeasonSelect],
  );

  const handleEpisodePress = useCallback(
    (episode: SeriesEpisode) => {
      // If this episode is already selected, play it
      const isAlreadyActive =
        activeEpisode?.seasonNumber === episode.seasonNumber && activeEpisode?.episodeNumber === episode.episodeNumber;

      if (isAlreadyActive && onEpisodePlay) {
        onEpisodePlay(episode);
      } else {
        onEpisodeSelect(episode);
      }
    },
    [activeEpisode, onEpisodeSelect, onEpisodePlay],
  );

  const handleEpisodeLongPress = useCallback(
    (episode: SeriesEpisode) => {
      const episodeKey = `${episode.seasonNumber}-${episode.episodeNumber}`;
      setFlashingEpisode(episodeKey);
      // Clear flash after animation
      setTimeout(() => setFlashingEpisode(null), 600);
      onEpisodeLongPress?.(episode);
    },
    [onEpisodeLongPress],
  );

  // Scroll to active episode when it changes or on initial load
  useEffect(() => {
    if (!activeEpisode || episodes.length === 0 || !episodeScrollRef.current) {
      return;
    }

    const activeIndex = episodes.findIndex(
      (ep) => ep.seasonNumber === activeEpisode.seasonNumber && ep.episodeNumber === activeEpisode.episodeNumber,
    );

    if (activeIndex > 0) {
      // Calculate scroll position to center the active episode
      const scrollPosition = activeIndex * (CARD_WIDTH + CARD_GAP);
      // Small delay to ensure the scroll view is ready
      setTimeout(() => {
        episodeScrollRef.current?.scrollTo({ x: scrollPosition, animated: true });
      }, 100);
    }
  }, [activeEpisode, episodes]);

  // Reset episode scroll when season changes
  useEffect(() => {
    if (episodeScrollRef.current) {
      episodeScrollRef.current.scrollTo({ x: 0, animated: false });
    }
  }, [selectedSeason?.number]);

  // Track if we've done the initial scroll
  const hasInitializedSeasonScroll = useRef(false);
  const seasonScrollReadyRef = useRef(false);

  // Scroll season selector to show selected season at left, or default to season 1
  const scrollToTargetSeason = useCallback(() => {
    if (!seasonScrollRef.current || seasons.length === 0) {
      return;
    }

    // Determine target season: selected season, or season 1 if available, or first season
    const targetSeasonNumber = selectedSeason?.number ?? (seasons.some((s) => s.number === 1) ? 1 : seasons[0]?.number);
    if (targetSeasonNumber === undefined) return;

    // Find the index of target season in sorted seasons array
    const targetIndex = seasons.findIndex((s) => s.number === targetSeasonNumber);
    if (targetIndex < 0) return;

    // Calculate scroll position - use snap interval for alignment
    const chipInterval = SEASON_CHIP_MIN_WIDTH + SEASON_CHIP_GAP;
    const scrollX = targetIndex * chipInterval;

    seasonScrollRef.current?.scrollTo({ x: scrollX, animated: hasInitializedSeasonScroll.current });
    hasInitializedSeasonScroll.current = true;
  }, [selectedSeason?.number, seasons]);

  // Trigger scroll when selection changes or after layout
  useEffect(() => {
    if (seasonScrollReadyRef.current) {
      // Layout already done, scroll immediately (with small delay for state updates)
      setTimeout(scrollToTargetSeason, 50);
    }
  }, [scrollToTargetSeason]);

  const handleSeasonScrollLayout = useCallback(() => {
    seasonScrollReadyRef.current = true;
    // Scroll after layout with a delay to ensure content is rendered
    setTimeout(scrollToTargetSeason, 100);
  }, [scrollToTargetSeason]);

  if (!seasons.length) {
    return null;
  }

  // Calculate snap interval (card width + gap)
  const snapInterval = CARD_WIDTH + CARD_GAP;

  // Calculate snap offsets for season chips - one offset per chip position
  const seasonSnapOffsets = useMemo(() => {
    const chipInterval = SEASON_CHIP_MIN_WIDTH + SEASON_CHIP_GAP;
    return seasons.map((_, index) => index * chipInterval);
  }, [seasons]);

  return (
    <View style={styles.container}>
      {/* Horizontal Season Selector */}
      <View style={styles.seasonCarouselWrapper}>
        <ScrollView
          ref={seasonScrollRef}
          horizontal
          showsHorizontalScrollIndicator={false}
          contentContainerStyle={styles.seasonScrollContent}
          style={styles.seasonScroll}
          snapToOffsets={seasonSnapOffsets}
          decelerationRate="fast"
          onLayout={handleSeasonScrollLayout}>
          {seasons.map((season) => {
            const isSelected = selectedSeason?.number === season.number;
            const seasonLabel = getSeasonLabel(season.number, season.name);
            return (
              <Pressable
                key={season.number}
                style={[styles.seasonChip, isSelected && styles.seasonChipSelected]}
                onPress={() => handleSeasonPress(season)}>
                <MarqueeText
                  style={[styles.seasonChipText, isSelected ? styles.seasonChipTextSelected : undefined]}
                  containerStyle={styles.seasonChipTextContainer}
                  focused={isSelected}
                  speed={25}
                  delay={500}>
                  {seasonLabel}
                </MarqueeText>
              </Pressable>
            );
          })}
        </ScrollView>
      </View>

      {/* Episode Cards */}
      {isLoading ? (
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="small" color={theme.colors.text.muted} />
        </View>
      ) : (
        <View style={styles.carouselWrapper}>
          <Animated.ScrollView
            ref={episodeScrollRef}
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={styles.episodeScrollContent}
            style={styles.episodeScroll}
            onScroll={scrollHandler}
            scrollEventThrottle={16}
            snapToInterval={snapInterval}
            decelerationRate="fast"
            snapToAlignment="start">
            {episodes.map((episode, _index) => {
              const episodeKey = `${episode.seasonNumber}-${episode.episodeNumber}`;
              const isActive =
                activeEpisode?.seasonNumber === episode.seasonNumber &&
                activeEpisode?.episodeNumber === episode.episodeNumber;
              const isWatched = isEpisodeWatched?.(episode) ?? false;
              const progress = getEpisodeProgress?.(episode) ?? 0;
              const isFlashing = flashingEpisode === episodeKey;

              return (
                <Pressable
                  key={episodeKey}
                  style={[styles.episodeCard, isActive && styles.episodeCardActive]}
                  onPress={() => handleEpisodePress(episode)}
                  onLongPress={() => handleEpisodeLongPress(episode)}
                  delayLongPress={500}>
                  {/* Thumbnail */}
                  <View style={[
                    styles.thumbnailContainer,
                    isActive && styles.thumbnailContainerActive,
                    isFlashing && styles.thumbnailContainerFlashing,
                  ]}>
                    {episode.image?.url ? (
                      <Image source={{ uri: episode.image.url }} style={styles.episodeThumbnail} contentFit="cover" />
                    ) : (
                      <View style={[styles.episodeThumbnail, styles.thumbnailPlaceholder]}>
                        <Ionicons name="film-outline" size={32} color={theme.colors.text.muted} />
                      </View>
                    )}

                    {/* Episode number badge */}
                    <View style={styles.episodeNumberBadge}>
                      <Text style={styles.episodeNumberText}>{episode.episodeNumber}</Text>
                    </View>

                    {/* Progress percentage or watched checkmark */}
                    {progress > 0 && progress < 100 ? (
                      <View style={styles.progressBadge}>
                        <Text style={styles.progressBadgeText}>{Math.round(progress)}%</Text>
                      </View>
                    ) : (isWatched || isFlashing) ? (
                      <View style={[styles.watchedBadge, isFlashing && styles.watchedBadgeFlashing]}>
                        <Ionicons name={isWatched ? 'checkmark' : 'eye-outline'} size={14} color="#fff" />
                      </View>
                    ) : null}

                    {/* Progress bar - raised slightly to avoid border overlap */}
                    {progress > 0 && progress < 100 && (
                      <View style={styles.progressBarContainer}>
                        <View style={styles.progressBarBackground} />
                        <View style={[styles.progressBarFill, { width: `${progress}%` }]} />
                      </View>
                    )}

                    {/* Selected indicator border */}
                    {isActive && <View style={styles.selectedBorder} />}
                    {/* Flash border */}
                    {isFlashing && <View style={styles.flashBorder} />}
                  </View>

                  {/* Episode title */}
                  <Text style={[styles.episodeTitle, isWatched && styles.episodeTitleWatched]} numberOfLines={2}>
                    {episode.name || `Episode ${episode.episodeNumber}`}
                  </Text>
                </Pressable>
              );
            })}
          </Animated.ScrollView>
        </View>
      )}
    </View>
  );
});

const createStyles = (theme: NovaTheme, _screenWidth: number) =>
  StyleSheet.create({
    container: {
      marginTop: theme.spacing.lg,
    },
    seasonCarouselWrapper: {
      position: 'relative',
      marginBottom: theme.spacing.md,
    },
    seasonScroll: {
      marginHorizontal: -theme.spacing['3xl'],
    },
    seasonScrollContent: {
      paddingHorizontal: theme.spacing['3xl'],
      gap: SEASON_CHIP_GAP,
    },
    seasonChip: {
      width: SEASON_CHIP_MIN_WIDTH,
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.pill,
      backgroundColor: theme.colors.background.surface,
      borderWidth: 1,
      borderColor: 'transparent',
      alignItems: 'center',
      justifyContent: 'center',
    },
    seasonChipSelected: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    seasonChipTextContainer: {
      maxWidth: 70, // Truncate long season names, marquee scrolls when selected
    },
    seasonChipText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontWeight: '500',
    },
    seasonChipTextSelected: {
      color: '#fff',
      fontWeight: '600',
    },
    loadingContainer: {
      height: CARD_HEIGHT + 40,
      justifyContent: 'center',
      alignItems: 'center',
    },
    carouselWrapper: {
      position: 'relative',
    },
    episodeScroll: {
      marginHorizontal: -theme.spacing['3xl'],
    },
    episodeScrollContent: {
      paddingHorizontal: theme.spacing['3xl'],
      gap: CARD_GAP,
    },
    episodeCard: {
      width: CARD_WIDTH,
    },
    episodeCardActive: {
      // Active styling handled by border
    },
    thumbnailContainer: {
      position: 'relative',
      width: CARD_WIDTH,
      height: CARD_HEIGHT,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
    },
    thumbnailContainerActive: {
      // Border applied via selectedBorder overlay
    },
    episodeThumbnail: {
      width: '100%',
      height: '100%',
    },
    thumbnailPlaceholder: {
      justifyContent: 'center',
      alignItems: 'center',
    },
    episodeNumberBadge: {
      position: 'absolute',
      top: theme.spacing.xs,
      left: theme.spacing.xs,
      backgroundColor: 'rgba(0, 0, 0, 0.7)',
      paddingHorizontal: 8,
      paddingVertical: 3,
      borderRadius: 4,
    },
    episodeNumberText: {
      color: '#fff',
      fontSize: 12,
      fontWeight: '600',
    },
    watchedBadge: {
      position: 'absolute',
      top: theme.spacing.xs,
      right: theme.spacing.xs,
      width: 24,
      height: 24,
      borderRadius: 12,
      backgroundColor: theme.colors.accent.primary,
      justifyContent: 'center',
      alignItems: 'center',
    },
    watchedBadgeFlashing: {
      backgroundColor: theme.colors.status.success,
      transform: [{ scale: 1.2 }],
    },
    progressBadge: {
      position: 'absolute',
      top: theme.spacing.xs,
      right: theme.spacing.xs,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: 6,
      paddingVertical: 2,
      borderRadius: 4,
    },
    progressBadgeText: {
      color: theme.colors.accent.primary,
      fontSize: 11,
      fontWeight: '700',
    },
    progressBarContainer: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: 6, // 4px bar + 2px padding, fills down to bottom
    },
    progressBarBackground: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(255, 255, 255, 0.5)',
    },
    progressBarFill: {
      position: 'absolute',
      top: 0,
      left: 0,
      bottom: 0,
      backgroundColor: theme.colors.accent.primary,
    },
    selectedBorder: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      borderWidth: 2,
      borderColor: theme.colors.accent.primary,
      borderRadius: theme.radius.md,
    },
    thumbnailContainerFlashing: {
      opacity: 0.85,
    },
    flashBorder: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      borderWidth: 3,
      borderColor: theme.colors.status.success,
      borderRadius: theme.radius.md,
    },
    episodeTitle: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.xs,
    },
    episodeTitleWatched: {
      color: theme.colors.text.secondary,
    },
  });

export default MobileEpisodeCarousel;
