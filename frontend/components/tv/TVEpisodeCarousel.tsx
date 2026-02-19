/**
 * TV Episode Carousel - Simplified carousel with season selector and episode browser
 * Uses SpatialNavigationVirtualizedList for proper spatial navigation integration
 * First press selects episode, second press plays
 */

import React, { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Platform, StyleSheet, Text, View } from 'react-native';
import type { SeriesEpisode, SeriesSeason } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';
import TVEpisodeThumbnail, { THUMBNAIL_WIDTH, THUMBNAIL_HEIGHT } from './TVEpisodeThumbnail';
import { getSeasonLabel, isEpisodeUnreleased } from '@/app/details/utils';
import {
  SpatialNavigationFocusableView,
  SpatialNavigationVirtualizedList,
  DefaultFocus,
  SpatialNavigationNode,
} from '@/services/tv-navigation';

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

// Calculate how many items fit on screen
const SEASON_ITEMS_VISIBLE = Math.max(1, Math.floor(1920 / (SEASON_CHIP_WIDTH + SEASON_CHIP_GAP)));
const EPISODE_ITEMS_VISIBLE = Math.max(1, Math.floor(1920 / (THUMBNAIL_WIDTH + EPISODE_GAP)));

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

  // Track focused episode for details panel display
  const [focusedEpisode, setFocusedEpisode] = useState<SeriesEpisode | null>(activeEpisode);

  // Debounce ref for focus updates - prevents re-renders during rapid navigation
  const focusDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Store frequently-changing props in refs to keep render callbacks stable
  // This prevents recreating callbacks when activeEpisode changes, which would cause all items to re-render
  const activeEpisodeRef = useRef(activeEpisode);
  activeEpisodeRef.current = activeEpisode;

  const isEpisodeWatchedRef = useRef(isEpisodeWatched);
  isEpisodeWatchedRef.current = isEpisodeWatched;

  const getEpisodeProgressRef = useRef(getEpisodeProgress);
  getEpisodeProgressRef.current = getEpisodeProgress;

  // Update focused episode when active episode changes
  useEffect(() => {
    if (activeEpisode) {
      setFocusedEpisode(activeEpisode);
    }
  }, [activeEpisode]);

  // Cleanup debounce timeout on unmount
  useEffect(() => {
    return () => {
      if (focusDebounceRef.current) {
        clearTimeout(focusDebounceRef.current);
      }
    };
  }, []);

  // Handle episode press - first press selects, second press plays
  // Uses ref to avoid recreating callback when activeEpisode changes
  const handleEpisodePress = useCallback(
    (episode: SeriesEpisode) => {
      const currentActive = activeEpisodeRef.current;
      const isAlreadySelected =
        currentActive?.seasonNumber === episode.seasonNumber && currentActive?.episodeNumber === episode.episodeNumber;

      if (isAlreadySelected) {
        // Second press - play
        onEpisodePlay?.(episode);
      } else {
        // First press - select
        onEpisodeSelect(episode);
      }
    },
    [onEpisodeSelect, onEpisodePlay],
  );

  // Focus handler for season chips — always notify parent so scroll adjusts
  // even when returning from a different section (cast/similar)
  const handleSeasonFocus = useCallback(
    () => {
      onFocusRowChange?.('seasons');
    },
    [onFocusRowChange],
  );

  // Focus handler for episode items — always notify parent
  const handleEpisodeFocus = useCallback(
    (episode: SeriesEpisode) => {
      // Debounce focus updates to prevent re-renders during rapid navigation
      if (focusDebounceRef.current) {
        clearTimeout(focusDebounceRef.current);
      }
      focusDebounceRef.current = setTimeout(() => {
        setFocusedEpisode(episode);
      }, 100);

      onFocusRowChange?.('episodes');
    },
    [onFocusRowChange],
  );

  // Render season chip for SpatialNavigationVirtualizedList
  const renderSeasonItem = useCallback(
    ({ item: season, index }: { item: SeriesSeason; index: number }) => {
      const isSelected = selectedSeason?.number === season.number;
      const seasonLabel = getSeasonLabel(season.number, season.name);

      const focusableView = (
        <SpatialNavigationFocusableView
          onSelect={() => onSeasonSelect(season)}
          onFocus={handleSeasonFocus}>
          {({ isFocused }: { isFocused: boolean }) => (
            <View
              style={[
                styles.seasonChip,
                isSelected && styles.seasonChipSelected,
                isFocused && styles.seasonChipFocused,
              ]}>
              <Text
                style={[
                  styles.seasonChipText,
                  isSelected && styles.seasonChipTextSelected,
                  isFocused && styles.seasonChipTextFocused,
                ]}
                numberOfLines={1}
                ellipsizeMode="tail">
                {seasonLabel}
              </Text>
            </View>
          )}
        </SpatialNavigationFocusableView>
      );

      return index === 0 ? <DefaultFocus>{focusableView}</DefaultFocus> : focusableView;
    },
    [selectedSeason, onSeasonSelect, handleSeasonFocus, styles],
  );

  // Render episode thumbnail for SpatialNavigationVirtualizedList
  const renderEpisodeItem = useCallback(
    ({ item: episode }: { item: SeriesEpisode }) => {
      const isSelected =
        activeEpisode?.seasonNumber === episode.seasonNumber && activeEpisode?.episodeNumber === episode.episodeNumber;
      const isWatched = isEpisodeWatchedRef.current?.(episode) ?? false;
      const progress = getEpisodeProgressRef.current?.(episode) ?? 0;

      return (
        <SpatialNavigationFocusableView
          onSelect={() => handleEpisodePress(episode)}
          onFocus={() => handleEpisodeFocus(episode)}>
          {({ isFocused }: { isFocused: boolean }) => (
            <TVEpisodeThumbnail
              episode={episode}
              isActive={isSelected}
              isFocused={isFocused}
              isWatched={isWatched}
              isUnreleased={isEpisodeUnreleased(episode.airedDate)}
              progress={progress}
              theme={theme}
              showSelectedBadge={isSelected}
            />
          )}
        </SpatialNavigationFocusableView>
      );
    },
    // Note: activeEpisode in deps is intentional - it recreates the callback to trigger list re-render
    // when selection changes, updating the "Selected" badge on episode thumbnails
    [handleEpisodePress, handleEpisodeFocus, theme, activeEpisode],
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
      <SpatialNavigationNode orientation="horizontal">
        <View style={styles.seasonRow}>
          <SpatialNavigationVirtualizedList
            data={seasons}
            renderItem={renderSeasonItem}
            itemSize={SEASON_CHIP_WIDTH + SEASON_CHIP_GAP}
            numberOfRenderedItems={SEASON_ITEMS_VISIBLE + 4}
            numberOfItemsVisibleOnScreen={SEASON_ITEMS_VISIBLE}
            orientation="horizontal"
            scrollDuration={300}
          />
        </View>
      </SpatialNavigationNode>

      {/* Episode Carousel */}
      <SpatialNavigationNode orientation="horizontal">
        <View style={styles.episodeRow}>
          <SpatialNavigationVirtualizedList
            data={episodes}
            renderItem={renderEpisodeItem}
            itemSize={THUMBNAIL_WIDTH + EPISODE_GAP}
            numberOfRenderedItems={EPISODE_ITEMS_VISIBLE + 4}
            numberOfItemsVisibleOnScreen={EPISODE_ITEMS_VISIBLE}
            orientation="horizontal"
            scrollDuration={300}
          />
        </View>
      </SpatialNavigationNode>

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
            {detailsContent.airDate && <Text style={styles.detailsMetaText}>{detailsContent.airDate}</Text>}
            {detailsContent.runtime && <Text style={styles.detailsMetaText}>{detailsContent.runtime} minutes</Text>}
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
      height: SEASON_CHIP_HEIGHT + tvScale(8),
      paddingLeft: tvScale(48),
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
      // Selected season uses subtle background tint instead of accent border
      // to avoid confusion with focus indicator
      backgroundColor: theme.colors.overlay.medium,
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
      paddingLeft: tvScale(48),
    },

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
