/**
 * TV Episode Carousel - Simplified carousel with season selector and episode browser
 * First press selects episode, second press plays
 */

import React, { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  FlatList,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import type { SeriesEpisode, SeriesSeason } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';
import TVEpisodeThumbnail, { THUMBNAIL_WIDTH, THUMBNAIL_HEIGHT } from './TVEpisodeThumbnail';

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
  onFocusRowChange?: (area: 'seasons' | 'episodes') => void;
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
  onFocusRowChange,
}: TVEpisodeCarouselProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);

  // Refs for FlatLists
  const seasonListRef = useRef<FlatList>(null);
  const episodeListRef = useRef<FlatList>(null);

  // Track current focus area to avoid redundant callbacks
  const currentFocusAreaRef = useRef<'seasons' | 'episodes' | null>(null);

  // Track focused episode for details panel display
  const [focusedEpisode, setFocusedEpisode] = useState<SeriesEpisode | null>(activeEpisode);

  // Update focused episode when active episode changes
  useEffect(() => {
    if (activeEpisode) {
      setFocusedEpisode(activeEpisode);
    }
  }, [activeEpisode]);

  // Calculate item sizes
  const seasonItemSize = SEASON_CHIP_WIDTH + SEASON_CHIP_GAP;
  const episodeItemSize = THUMBNAIL_WIDTH + EPISODE_GAP;

  // Simple scroll handlers
  const scrollToSeason = useCallback(
    (index: number) => {
      if (!Platform.isTV || !seasonListRef.current) return;
      seasonListRef.current.scrollToIndex({ index, animated: true, viewPosition: 0.3 });
    },
    []
  );

  const scrollToEpisode = useCallback(
    (index: number) => {
      if (!Platform.isTV || !episodeListRef.current) return;
      episodeListRef.current.scrollToIndex({ index, animated: true, viewPosition: 0.3 });
    },
    []
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
        setTimeout(() => scrollToEpisode(activeIndex), 100);
      }
    }
  }, [activeEpisode, episodes, scrollToEpisode]);

  // Handle episode press - first press selects, second press plays
  const handleEpisodePress = useCallback(
    (episode: SeriesEpisode) => {
      const isAlreadySelected =
        activeEpisode?.seasonNumber === episode.seasonNumber &&
        activeEpisode?.episodeNumber === episode.episodeNumber;

      if (isAlreadySelected) {
        // Second press - play
        onEpisodePlay?.(episode);
      } else {
        // First press - select
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

      return (
        <Pressable
          onPress={() => onSeasonSelect(season)}
          onFocus={() => {
            scrollToSeason(index);
            if (currentFocusAreaRef.current !== 'seasons') {
              currentFocusAreaRef.current = 'seasons';
              onFocusRowChange?.('seasons');
            }
          }}
          tvParallaxProperties={{ enabled: false }}
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
    [selectedSeason, onSeasonSelect, scrollToSeason, onFocusRowChange, styles]
  );

  // Render episode thumbnail
  const renderEpisodeItem = useCallback(
    ({ item: episode, index }: { item: SeriesEpisode; index: number }) => {
      const isSelected =
        activeEpisode?.seasonNumber === episode.seasonNumber &&
        activeEpisode?.episodeNumber === episode.episodeNumber;
      const isWatched = isEpisodeWatched?.(episode) ?? false;
      const progress = getEpisodeProgress?.(episode) ?? 0;

      return (
        <Pressable
          onPress={() => handleEpisodePress(episode)}
          onFocus={() => {
            setFocusedEpisode(episode);
            scrollToEpisode(index);
            if (currentFocusAreaRef.current !== 'episodes') {
              currentFocusAreaRef.current = 'episodes';
              onFocusRowChange?.('episodes');
            }
          }}
          tvParallaxProperties={{ enabled: false }}
          // @ts-ignore - Android TV performance optimization
          renderToHardwareTextureAndroid={isAndroidTV}
          style={({ focused }) => styles.episodeCard}
        >
          {({ focused }) => (
            <TVEpisodeThumbnail
              episode={episode}
              isActive={isSelected}
              isFocused={focused}
              isWatched={isWatched}
              progress={progress}
              theme={theme}
              showSelectedBadge={isSelected}
            />
          )}
        </Pressable>
      );
    },
    [
      activeEpisode,
      isEpisodeWatched,
      getEpisodeProgress,
      handleEpisodePress,
      scrollToEpisode,
      onFocusRowChange,
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
          initialNumToRender={seasons.length}
          removeClippedSubviews={false}
        />
      </View>

      {/* Episode Carousel */}
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
          initialNumToRender={episodes.length}
          removeClippedSubviews={false}
          extraData={activeEpisode}
        />
      </View>

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
      borderColor: theme.colors.accent.primary,
    },
    seasonChipFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    seasonChipText: {
      fontSize: tvScale(16),
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    seasonChipTextSelected: {},
    seasonChipTextFocused: {
      color: theme.colors.text.inverse,
    },

    // Episode row
    episodeRow: {
      height: THUMBNAIL_HEIGHT + tvScale(8),
      width: '100%',
    },
    episodeListContent: {
      paddingLeft: tvScale(48),
      paddingRight: tvScale(48),
      gap: EPISODE_GAP,
      alignItems: 'center',
    },
    episodeCard: {},

    // Details panel
    detailsPanel: {
      marginTop: tvScale(16),
      marginLeft: tvScale(48),
      width: '60%',
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
