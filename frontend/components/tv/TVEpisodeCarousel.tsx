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
  onEpisodeFocus?: (episode: SeriesEpisode) => void;
  onEpisodePlay?: (episode: SeriesEpisode) => void;
  isEpisodeWatched?: (episode: SeriesEpisode) => boolean;
  getEpisodeProgress?: (episode: SeriesEpisode) => number;
  autoFocusEpisodes?: boolean;
  autoFocusSelectedSeason?: boolean;
  onFocusRowChange?: (area: 'seasons' | 'episodes' | 'actions' | 'cast') => void;
  /** Callback when active episode's native tag changes (for parent focus navigation) */
  onActiveEpisodeTagChange?: (tag: number | undefined) => void;
  /** Callback when selected season's native tag changes (for parent focus navigation) */
  onSelectedSeasonTagChange?: (tag: number | undefined) => void;
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
  onEpisodeFocus,
  onEpisodePlay,
  isEpisodeWatched,
  getEpisodeProgress,
  autoFocusEpisodes = false,
  autoFocusSelectedSeason = false,
  onFocusRowChange,
  onActiveEpisodeTagChange,
  onSelectedSeasonTagChange,
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

  // Track the selected season's native tag for focus navigation (action buttons -> selected season)
  const [selectedSeasonTag, setSelectedSeasonTag] = useState<number | undefined>(undefined);

  // Track focused episode for details panel
  const [focusedEpisode, setFocusedEpisode] = useState<SeriesEpisode | null>(activeEpisode);

  // Track current focus area to avoid redundant onFocusRowChange calls
  const currentFocusAreaRef = useRef<'seasons' | 'episodes' | null>(null);

  // Debounce timer for episode focus updates to parent
  const episodeFocusTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Cleanup debounce timer on unmount
  useEffect(() => {
    return () => {
      if (episodeFocusTimerRef.current) {
        clearTimeout(episodeFocusTimerRef.current);
      }
    };
  }, []);

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

    // Short delay to ensure refs are assigned after render
    const timer = setTimeout(() => {
      const activeRef = episodeCardRefs.current.get(activeIndex);
      if (activeRef) {
        setActiveEpisodeTag(findNodeHandle(activeRef) ?? undefined);
      }
    }, 50);

    return () => clearTimeout(timer);
  }, [activeEpisode, episodes]);

  // Notify parent when active episode tag changes (for action buttons -> episode focus)
  useEffect(() => {
    onActiveEpisodeTagChange?.(activeEpisodeTag);
  }, [activeEpisodeTag, onActiveEpisodeTagChange]);

  // Update selected season tag for focus navigation (allows action buttons to navigate down to selected season)
  useEffect(() => {
    if (!Platform.isTV || !selectedSeason || seasons.length === 0) {
      setSelectedSeasonTag(undefined);
      return;
    }

    // Find the index of the selected season
    const selectedIndex = seasons.findIndex((s) => s.number === selectedSeason.number);

    if (selectedIndex < 0) {
      setSelectedSeasonTag(undefined);
      return;
    }

    // Short delay to ensure refs are assigned after render
    const timer = setTimeout(() => {
      const selectedRef = seasonCardRefs.current.get(selectedIndex);
      if (selectedRef) {
        setSelectedSeasonTag(findNodeHandle(selectedRef) ?? undefined);
      }
    }, 50);

    return () => clearTimeout(timer);
  }, [selectedSeason, seasons]);

  // Notify parent when selected season tag changes (for action buttons -> season focus)
  useEffect(() => {
    onSelectedSeasonTagChange?.(selectedSeasonTag);
  }, [selectedSeasonTag, onSelectedSeasonTagChange]);

  // Calculate item sizes
  const seasonItemSize = SEASON_CHIP_WIDTH + SEASON_CHIP_GAP;
  const episodeItemSize = THUMBNAIL_WIDTH + EPISODE_GAP;

  // Simple scroll handlers using scrollToIndex
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

  // Handle episode selection - play episode directly
  const handleEpisodePress = useCallback(
    (episode: SeriesEpisode) => {
      if (onEpisodePlay) {
        onEpisodePlay(episode);
      }
    },
    [onEpisodePlay]
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
            // Only notify parent of row change when entering seasons row, not on every season
            if (currentFocusAreaRef.current !== 'seasons') {
              currentFocusAreaRef.current = 'seasons';
              onFocusRowChange?.('seasons');
            }
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
      // Use focusedEpisode (local state) for immediate visual feedback
      // This updates instantly on navigation and persists when focus leaves the row
      const isActive =
        focusedEpisode?.seasonNumber === episode.seasonNumber &&
        focusedEpisode?.episodeNumber === episode.episodeNumber;
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
            // Only notify parent of row change when entering episodes row, not on every episode
            if (currentFocusAreaRef.current !== 'episodes') {
              currentFocusAreaRef.current = 'episodes';
              onFocusRowChange?.('episodes');
            }
            // Debounce parent state update to avoid lag when rapidly navigating
            if (episodeFocusTimerRef.current) {
              clearTimeout(episodeFocusTimerRef.current);
            }
            episodeFocusTimerRef.current = setTimeout(() => {
              (onEpisodeFocus ?? onEpisodeSelect)(episode);
            }, 150);
          }}
          hasTVPreferredFocus={shouldAutoFocus}
          tvParallaxProperties={{ enabled: false }}
          nextFocusLeft={isFirst && firstRef ? findNodeHandle(firstRef) ?? undefined : undefined}
          nextFocusRight={isLast && lastRef ? findNodeHandle(lastRef) ?? undefined : undefined}
          nextFocusUp={selectedSeasonTag}
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
      focusedEpisode,
      episodes.length,
      onFocusRowChange,
      autoFocusEpisodes,
      isEpisodeWatched,
      getEpisodeProgress,
      handleEpisodePress,
      scrollToEpisode,
      onEpisodeFocus,
      onEpisodeSelect,
      selectedSeasonTag,
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
      {/* Season Selector Row - render all seasons (no virtualization needed for small lists) */}
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
          extraData={activeEpisodeTag}
        />
      </View>

      {/* Episode Carousel - render all episodes (no virtualization needed for typical season sizes) */}
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
            initialNumToRender={episodes.length}
            removeClippedSubviews={false}
            extraData={`${focusedEpisode?.seasonNumber}-${focusedEpisode?.episodeNumber}-${selectedSeasonTag}`}
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
            initialNumToRender={episodes.length}
            removeClippedSubviews={false}
            extraData={`${focusedEpisode?.seasonNumber}-${focusedEpisode?.episodeNumber}-${selectedSeasonTag}`}
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
