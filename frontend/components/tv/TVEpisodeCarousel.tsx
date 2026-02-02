/**
 * TV Episode Carousel - Simplified carousel with season selector and episode browser
 * Uses spatial navigation for proper D-pad navigation between rows
 * First press selects episode, second press plays
 */

import React, { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Platform, StyleSheet, Text, View } from 'react-native';
import type { SeriesEpisode, SeriesSeason } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';
import {
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationVirtualizedList,
} from '@/services/tv-navigation';
import TVEpisodeThumbnail, { THUMBNAIL_WIDTH, THUMBNAIL_HEIGHT } from './TVEpisodeThumbnail';
import { getSeasonLabel } from '@/app/details/utils';

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

  // Track current focus area to avoid redundant callbacks
  const currentFocusAreaRef = useRef<'seasons' | 'episodes' | null>(null);

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

  // Calculate item sizes for virtualized lists
  const seasonItemSize = SEASON_CHIP_WIDTH + SEASON_CHIP_GAP;
  const episodeItemSize = THUMBNAIL_WIDTH + EPISODE_GAP;

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

  // Render season chip using SpatialNavigationFocusableView
  const renderSeasonItem = useCallback(
    ({ item: season }: { item: SeriesSeason }) => {
      const isSelected = selectedSeason?.number === season.number;
      const seasonLabel = getSeasonLabel(season.number, season.name);

      return (
        <SpatialNavigationFocusableView
          onSelect={() => onSeasonSelect(season)}
          onFocus={() => {
            if (currentFocusAreaRef.current !== 'seasons') {
              currentFocusAreaRef.current = 'seasons';
              onFocusRowChange?.('seasons');
            }
          }}>
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
    },
    [selectedSeason, onSeasonSelect, onFocusRowChange, styles],
  );

  // Render episode thumbnail using SpatialNavigationFocusableView
  const renderEpisodeItem = useCallback(
    ({ item: episode }: { item: SeriesEpisode }) => {
      const isSelected =
        activeEpisode?.seasonNumber === episode.seasonNumber && activeEpisode?.episodeNumber === episode.episodeNumber;
      const isWatched = isEpisodeWatchedRef.current?.(episode) ?? false;
      const progress = getEpisodeProgressRef.current?.(episode) ?? 0;

      return (
        <SpatialNavigationFocusableView
          onSelect={() => handleEpisodePress(episode)}
          onFocus={() => {
            // Debounce focus updates to prevent re-renders during rapid navigation
            if (focusDebounceRef.current) {
              clearTimeout(focusDebounceRef.current);
            }
            focusDebounceRef.current = setTimeout(() => {
              setFocusedEpisode(episode);
            }, 100);

            if (currentFocusAreaRef.current !== 'episodes') {
              currentFocusAreaRef.current = 'episodes';
              onFocusRowChange?.('episodes');
            }
          }}>
          {({ isFocused }: { isFocused: boolean }) => (
            <TVEpisodeThumbnail
              episode={episode}
              isActive={isSelected}
              isFocused={isFocused}
              isWatched={isWatched}
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
    [handleEpisodePress, onFocusRowChange, theme, activeEpisode],
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

  // SpatialNavigationVirtualizedList requires numberOfRenderedItems >= numberOfItemsVisibleOnScreen + 2
  // For small lists (< 3 items), render directly without virtualization
  const useVirtualizedSeasons = seasons.length >= 3;
  const useVirtualizedEpisodes = episodes.length >= 3;

  return (
    <View style={styles.container}>
      {/* Season Selector Row - wrapped in SpatialNavigationNode for vertical nav */}
      <SpatialNavigationNode orientation="horizontal">
        <View style={styles.seasonRow}>
          {useVirtualizedSeasons ? (
            <SpatialNavigationVirtualizedList
              data={seasons}
              renderItem={renderSeasonItem}
              itemSize={seasonItemSize}
              orientation="horizontal"
              numberOfRenderedItems={seasons.length}
              numberOfItemsVisibleOnScreen={Math.max(1, Math.min(seasons.length - 2, isAndroidTV ? 6 : 8))}
            />
          ) : (
            <View style={styles.smallListRow}>
              {seasons.map((season) => (
                <React.Fragment key={`season-${season.number}`}>
                  {renderSeasonItem({ item: season })}
                </React.Fragment>
              ))}
            </View>
          )}
        </View>
      </SpatialNavigationNode>

      {/* Episode Carousel - wrapped in SpatialNavigationNode for vertical nav */}
      <SpatialNavigationNode orientation="horizontal">
        <View style={styles.episodeRow}>
          {useVirtualizedEpisodes ? (
            <SpatialNavigationVirtualizedList
              data={episodes}
              renderItem={renderEpisodeItem}
              itemSize={episodeItemSize}
              orientation="horizontal"
              numberOfRenderedItems={episodes.length}
              numberOfItemsVisibleOnScreen={Math.max(1, Math.min(episodes.length - 2, isAndroidTV ? 4 : 5))}
            />
          ) : (
            <View style={styles.smallListRow}>
              {episodes.map((episode) => (
                <React.Fragment key={`ep-${episode.seasonNumber}-${episode.episodeNumber}`}>
                  {renderEpisodeItem({ item: episode })}
                </React.Fragment>
              ))}
            </View>
          )}
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
    smallListRow: {
      flexDirection: 'row',
      gap: tvScale(12),
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
