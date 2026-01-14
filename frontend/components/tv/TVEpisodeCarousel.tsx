/**
 * TV Episode Carousel - Full-featured carousel with season selector and episode browser
 * Uses native Pressable focus with FlatList.scrollToOffset (same pattern as home screen shelves)
 */

import React, { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Dimensions,
  FlatList,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  View,
  findNodeHandle,
  // @ts-ignore - TVFocusGuideView is available on TV platforms
  TVFocusGuideView,
} from 'react-native';
import { Image } from '../Image';
import { LinearGradient } from 'expo-linear-gradient';
import type { SeriesEpisode, SeriesSeason } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';
import TVEpisodeThumbnail, { THUMBNAIL_WIDTH, THUMBNAIL_HEIGHT } from './TVEpisodeThumbnail';

const isAppleTV = Platform.isTV && Platform.OS === 'ios';
const isAndroidTV = Platform.isTV && Platform.OS === 'android';

// Season chip dimensions
const SEASON_CHIP_WIDTH = tvScale(120);
const SEASON_CHIP_HEIGHT = tvScale(44);
const SEASON_CHIP_GAP = tvScale(12);

// Episode card spacing
const EPISODE_GAP = tvScale(16);

interface TVEpisodeCarouselProps {
  seasons: SeriesSeason[];
  selectedSeason: SeriesSeason | null;
  episodes: SeriesEpisode[];
  activeEpisode: SeriesEpisode | null;
  onSeasonSelect: (season: SeriesSeason) => void;
  onEpisodeSelect: (episode: SeriesEpisode) => void;
  onEpisodePlay?: (episode: SeriesEpisode) => void;
  isEpisodeWatched?: (episode: SeriesEpisode) => boolean;
  getEpisodeProgress?: (episode: SeriesEpisode) => number;
  autoFocusEpisodes?: boolean;
  autoFocusSelectedSeason?: boolean;
  onFocusRowChange?: (area: 'seasons' | 'episodes' | 'actions' | 'cast') => void;
  /** Callback when active episode's native tag changes (for parent focus navigation) */
  onActiveEpisodeTagChange?: (tag: number | undefined) => void;
}

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

const formatEpisodeCode = (episode: SeriesEpisode): string => {
  const season = String(episode.seasonNumber).padStart(2, '0');
  const episodeNum = String(episode.episodeNumber).padStart(2, '0');
  return `S${season}E${episodeNum}`;
};

const TVEpisodeCarousel = memo(function TVEpisodeCarousel({
  seasons,
  selectedSeason,
  episodes,
  activeEpisode,
  onSeasonSelect,
  onEpisodeSelect,
  onEpisodePlay,
  isEpisodeWatched,
  getEpisodeProgress,
  autoFocusEpisodes = false,
  autoFocusSelectedSeason = false,
  onFocusRowChange,
  onActiveEpisodeTagChange,
}: TVEpisodeCarouselProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);

  // Refs for FlatLists
  const seasonListRef = useRef<FlatList>(null);
  const episodeListRef = useRef<FlatList>(null);

  // Refs for focus containment
  const seasonCardRefs = useRef<Map<number, View | null>>(new Map());
  const episodeCardRefs = useRef<Map<number, View | null>>(new Map());

  // Ref for TVFocusGuideView destinations (first season chip for upward navigation from episodes)
  const [seasonFocusDestinations, setSeasonFocusDestinations] = useState<View[]>([]);

  // Track the active episode's native tag for focus navigation (season chips -> active episode)
  const [activeEpisodeTag, setActiveEpisodeTag] = useState<number | undefined>(undefined);

  // Track focused episode for details panel
  const [focusedEpisode, setFocusedEpisode] = useState<SeriesEpisode | null>(activeEpisode);

  // Update focused episode when active episode changes
  useEffect(() => {
    if (activeEpisode) {
      setFocusedEpisode(activeEpisode);
    }
  }, [activeEpisode]);

  // Update active episode tag for focus navigation (allows season chips to navigate down to active episode)
  useEffect(() => {
    if (!Platform.isTV || !activeEpisode || episodes.length === 0) {
      setActiveEpisodeTag(undefined);
      return;
    }

    // Find the index of the active episode
    const activeIndex = episodes.findIndex(
      (ep) =>
        ep.seasonNumber === activeEpisode.seasonNumber &&
        ep.episodeNumber === activeEpisode.episodeNumber
    );

    if (activeIndex < 0) {
      setActiveEpisodeTag(undefined);
      return;
    }

    // Delay to ensure refs are assigned after render
    const timer = setTimeout(() => {
      const activeRef = episodeCardRefs.current.get(activeIndex);
      if (activeRef) {
        setActiveEpisodeTag(findNodeHandle(activeRef) ?? undefined);
      }
    }, 150);

    return () => clearTimeout(timer);
  }, [activeEpisode, episodes]);

  // Notify parent when active episode tag changes (for action buttons -> episode focus)
  useEffect(() => {
    onActiveEpisodeTagChange?.(activeEpisodeTag);
  }, [activeEpisodeTag, onActiveEpisodeTagChange]);

  // Calculate item sizes
  const seasonItemSize = SEASON_CHIP_WIDTH + SEASON_CHIP_GAP;
  const episodeItemSize = THUMBNAIL_WIDTH + EPISODE_GAP;

  // Scroll handlers using scrollToOffset pattern from VirtualizedShelf
  // Snaps to card boundaries so items are never cut off
  const scrollToSeason = useCallback(
    (index: number) => {
      if (!Platform.isTV || !seasonListRef.current) return;

      const { width: screenWidth } = Dimensions.get('window');
      // Keep 1 full season chip visible to the left
      const targetChipIndex = Math.max(0, index - 1);
      let targetX = targetChipIndex * seasonItemSize;

      const maxScroll = Math.max(0, seasons.length * seasonItemSize - screenWidth);
      targetX = Math.max(0, Math.min(targetX, maxScroll));

      seasonListRef.current.scrollToOffset({ offset: targetX, animated: true });
    },
    [seasons.length, seasonItemSize]
  );

  const scrollToEpisode = useCallback(
    (index: number) => {
      if (!Platform.isTV || !episodeListRef.current) return;

      const { width: screenWidth } = Dimensions.get('window');
      const paddingLeft = tvScale(48);
      const paddingRight = tvScale(48);
      const itemPosition = index * episodeItemSize + paddingLeft;

      // Keep 2 cards visible to the left
      const leftOffset = Math.round(2 * episodeItemSize);
      let targetX = Math.round(itemPosition - leftOffset - paddingLeft);

      // Calculate max scroll - ensure last item + right padding stays on screen
      const totalContentWidth = episodes.length * episodeItemSize + paddingLeft + paddingRight - EPISODE_GAP;
      const maxScroll = Math.max(0, totalContentWidth - screenWidth);
      targetX = Math.max(0, Math.min(targetX, maxScroll));

      episodeListRef.current.scrollToOffset({ offset: targetX, animated: true });
    },
    [episodes.length, episodeItemSize]
  );

  // Scroll to active episode when episodes change
  useEffect(() => {
    if (activeEpisode && episodes.length > 0) {
      const activeIndex = episodes.findIndex(
        (ep) =>
          ep.seasonNumber === activeEpisode.seasonNumber &&
          ep.episodeNumber === activeEpisode.episodeNumber
      );
      if (activeIndex >= 0) {
        // Small delay to ensure FlatList is ready
        setTimeout(() => scrollToEpisode(activeIndex), 100);
      }
    }
  }, [activeEpisode, episodes, scrollToEpisode]);

  // Update TVFocusGuideView destinations when season chips are ready
  // This allows episode cards to navigate up to season chips even when scrolled
  useEffect(() => {
    if (!isAppleTV || seasons.length === 0) return;

    // Delay to ensure refs are assigned after render
    const timer = setTimeout(() => {
      const firstSeasonRef = seasonCardRefs.current.get(0);
      if (firstSeasonRef) {
        setSeasonFocusDestinations([firstSeasonRef]);
      }
    }, 100);

    return () => clearTimeout(timer);
  }, [seasons.length]);

  // Handle season selection
  const handleSeasonSelect = useCallback(
    (season: SeriesSeason) => {
      onSeasonSelect(season);
    },
    [onSeasonSelect]
  );

  // Handle episode selection (tap to select, tap again to play)
  const handleEpisodePress = useCallback(
    (episode: SeriesEpisode) => {
      const isAlreadyActive =
        activeEpisode?.seasonNumber === episode.seasonNumber &&
        activeEpisode?.episodeNumber === episode.episodeNumber;

      if (isAlreadyActive && onEpisodePlay) {
        onEpisodePlay(episode);
      } else {
        onEpisodeSelect(episode);
      }
    },
    [activeEpisode, onEpisodeSelect, onEpisodePlay]
  );

  // Render season chip
  const renderSeasonItem = useCallback(
    ({ item: season, index }: { item: SeriesSeason; index: number }) => {
      const isSelected = selectedSeason?.number === season.number;
      const seasonLabel = season.name || `Season ${season.number}`;
      const isFirst = index === 0;
      const isLast = index === seasons.length - 1;

      // Get refs for focus containment
      const firstRef = seasonCardRefs.current.get(0);
      const lastRef = seasonCardRefs.current.get(seasons.length - 1);

      return (
        <Pressable
          ref={(ref) => { seasonCardRefs.current.set(index, ref); }}
          onPress={() => handleSeasonSelect(season)}
          onFocus={() => {
            scrollToSeason(index);
            onFocusRowChange?.('seasons');
          }}
          hasTVPreferredFocus={isSelected && autoFocusSelectedSeason}
          tvParallaxProperties={{ enabled: false }}
          nextFocusLeft={isFirst && firstRef ? findNodeHandle(firstRef) ?? undefined : undefined}
          nextFocusRight={isLast && lastRef ? findNodeHandle(lastRef) ?? undefined : undefined}
          nextFocusDown={activeEpisodeTag}
          style={({ focused }) => [
            styles.seasonChip,
            isSelected && styles.seasonChipSelected,
            focused && styles.seasonChipFocused,
          ]}
        >
          {({ focused }) => (
            <Text
              style={[
                styles.seasonChipText,
                isSelected && styles.seasonChipTextSelected,
                focused && styles.seasonChipTextFocused,
              ]}
            >
              {seasonLabel}
            </Text>
          )}
        </Pressable>
      );
    },
    [selectedSeason, seasons.length, handleSeasonSelect, scrollToSeason, styles, onFocusRowChange, autoFocusSelectedSeason, activeEpisodeTag]
  );

  // Render episode thumbnail
  const renderEpisodeItem = useCallback(
    ({ item: episode, index }: { item: SeriesEpisode; index: number }) => {
      const isActive =
        activeEpisode?.seasonNumber === episode.seasonNumber &&
        activeEpisode?.episodeNumber === episode.episodeNumber;
      const isWatched = isEpisodeWatched?.(episode) ?? false;
      const progress = getEpisodeProgress?.(episode) ?? 0;
      const isFirst = index === 0;
      const isLast = index === episodes.length - 1;

      // Get refs for focus containment
      const firstRef = episodeCardRefs.current.get(0);
      const lastRef = episodeCardRefs.current.get(episodes.length - 1);

      // Auto focus first episode if this is first render and autoFocus is enabled
      const shouldAutoFocus = autoFocusEpisodes && isFirst;

      return (
        <Pressable
          ref={(ref) => { episodeCardRefs.current.set(index, ref); }}
          onPress={() => handleEpisodePress(episode)}
          onFocus={() => {
            setFocusedEpisode(episode);
            scrollToEpisode(index);
            onFocusRowChange?.('episodes');
          }}
          hasTVPreferredFocus={shouldAutoFocus}
          tvParallaxProperties={{ enabled: false }}
          nextFocusLeft={isFirst && firstRef ? findNodeHandle(firstRef) ?? undefined : undefined}
          nextFocusRight={isLast && lastRef ? findNodeHandle(lastRef) ?? undefined : undefined}
          // @ts-ignore - Android TV performance optimization
          renderToHardwareTextureAndroid={isAndroidTV}
          style={({ focused }) => styles.episodeCard}
        >
          {({ focused }) => (
            <TVEpisodeThumbnail
              episode={episode}
              isActive={isActive}
              isFocused={focused}
              isWatched={isWatched}
              progress={progress}
              theme={theme}
            />
          )}
        </Pressable>
      );
    },
    [
      activeEpisode,
      episodes.length,
      onFocusRowChange,
      autoFocusEpisodes,
      isEpisodeWatched,
      getEpisodeProgress,
      handleEpisodePress,
      scrollToEpisode,
      theme,
      styles,
    ]
  );

  // Episode details panel content
  const detailsContent = useMemo(() => {
    if (!focusedEpisode) return null;

    const episodeCode = formatEpisodeCode(focusedEpisode);
    const airDate = formatAirDate(focusedEpisode.airedDate);

    return {
      code: episodeCode,
      title: focusedEpisode.name || `Episode ${focusedEpisode.episodeNumber}`,
      overview: focusedEpisode.overview,
      airDate,
      runtime: focusedEpisode.runtimeMinutes,
    };
  }, [focusedEpisode]);

  if (!seasons.length) {
    return null;
  }

  return (
    <View style={styles.container}>
      {/* Season Selector Row */}
      <View style={styles.seasonRow}>
        <FlatList
          ref={seasonListRef}
          data={seasons}
          renderItem={renderSeasonItem}
          keyExtractor={(item) => `season-${item.number}`}
          horizontal
          showsHorizontalScrollIndicator={false}
          scrollEnabled={!Platform.isTV}
          getItemLayout={(_, index) => ({
            length: seasonItemSize,
            offset: seasonItemSize * index,
            index,
          })}
          contentContainerStyle={styles.seasonListContent}
          initialNumToRender={10}
          maxToRenderPerBatch={5}
          windowSize={3}
        />
      </View>

      {/* Episode Carousel - wrapped in TVFocusGuideView on tvOS for upward navigation to season chips */}
      {isAppleTV ? (
        <TVFocusGuideView
          style={styles.episodeRow}
          destinations={seasonFocusDestinations}
        >
          <FlatList
            ref={episodeListRef}
            data={episodes}
            renderItem={renderEpisodeItem}
            keyExtractor={(item) => `ep-${item.seasonNumber}-${item.episodeNumber}`}
            horizontal
            showsHorizontalScrollIndicator={false}
            scrollEnabled={false}
            getItemLayout={(_, index) => ({
              length: episodeItemSize,
              offset: episodeItemSize * index,
              index,
            })}
            contentContainerStyle={styles.episodeListContent}
            initialNumToRender={9}
            maxToRenderPerBatch={7}
            windowSize={3}
            removeClippedSubviews={true}
          />
        </TVFocusGuideView>
      ) : (
        <View style={styles.episodeRow}>
          <FlatList
            ref={episodeListRef}
            data={episodes}
            renderItem={renderEpisodeItem}
            keyExtractor={(item) => `ep-${item.seasonNumber}-${item.episodeNumber}`}
            horizontal
            showsHorizontalScrollIndicator={false}
            scrollEnabled={!Platform.isTV}
            getItemLayout={(_, index) => ({
              length: episodeItemSize,
              offset: episodeItemSize * index,
              index,
            })}
            contentContainerStyle={styles.episodeListContent}
            initialNumToRender={isAndroidTV ? 7 : 9}
            maxToRenderPerBatch={isAndroidTV ? 5 : 7}
            windowSize={3}
            removeClippedSubviews={Platform.isTV}
          />
        </View>
      )}

      {/* Episode Details Panel */}
      {detailsContent && (
        <View style={styles.detailsPanel}>
          <View style={styles.detailsHeader}>
            <Text style={styles.detailsCode}>{detailsContent.code}</Text>
            <Text style={styles.detailsTitle} numberOfLines={1}>
              {detailsContent.title}
            </Text>
          </View>
          {detailsContent.overview && (
            <Text style={styles.detailsOverview} numberOfLines={2}>
              {detailsContent.overview}
            </Text>
          )}
          <View style={styles.detailsMeta}>
            {detailsContent.airDate && (
              <Text style={styles.detailsMetaText}>{detailsContent.airDate}</Text>
            )}
            {detailsContent.runtime && (
              <Text style={styles.detailsMetaText}>{detailsContent.runtime} min</Text>
            )}
          </View>
        </View>
      )}
    </View>
  );
});

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    container: {
      marginBottom: tvScale(24),
      width: '100%',
      overflow: 'hidden',
    },

    // Season row
    seasonRow: {
      marginBottom: tvScale(16),
      width: '100%',
    },
    seasonListContent: {
      paddingHorizontal: tvScale(48),
      gap: SEASON_CHIP_GAP,
    },
    seasonChip: {
      width: SEASON_CHIP_WIDTH,
      height: SEASON_CHIP_HEIGHT,
      borderRadius: tvScale(22),
      backgroundColor: theme.colors.overlay.button,
      justifyContent: 'center',
      alignItems: 'center',
      borderWidth: tvScale(3),
      borderColor: 'transparent',
    },
    seasonChipSelected: {
      // Selected but not focused: accent border only
      borderColor: theme.colors.accent.primary,
    },
    seasonChipFocused: {
      // Focused: accent background + accent border, no zoom
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    seasonChipText: {
      fontSize: tvScale(16),
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    seasonChipTextSelected: {
      // No special color when selected - same as normal
    },
    seasonChipTextFocused: {
      color: theme.colors.text.inverse,
    },

    // Episode row
    episodeRow: {
      height: THUMBNAIL_HEIGHT + tvScale(8), // Small buffer, no zoom animation
      width: '100%',
    },
    episodeListContent: {
      paddingLeft: tvScale(48),
      paddingRight: tvScale(48),
      gap: EPISODE_GAP,
      alignItems: 'center',
    },
    episodeCard: {
      // No additional styling - handled by TVEpisodeThumbnail
    },

    // Details panel - width matches show overview (60%), aligned with carousel
    detailsPanel: {
      marginTop: tvScale(16),
      marginLeft: tvScale(48),
      width: '60%',
      // No background - clean look
    },
    detailsHeader: {
      flexDirection: 'row',
      alignItems: 'baseline',
      gap: tvScale(12),
      marginBottom: tvScale(12),
    },
    detailsCode: {
      fontSize: tvScale(24),
      fontWeight: '700',
      color: theme.colors.accent.primary,
    },
    detailsTitle: {
      flex: 1,
      fontSize: tvScale(24),
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    detailsOverview: {
      fontSize: tvScale(20),
      lineHeight: tvScale(28),
      color: theme.colors.text.secondary,
      marginBottom: tvScale(12),
    },
    detailsMeta: {
      flexDirection: 'row',
      gap: tvScale(24),
    },
    detailsMetaText: {
      fontSize: tvScale(18),
      color: theme.colors.text.muted,
    },
  });

export default TVEpisodeCarousel;
