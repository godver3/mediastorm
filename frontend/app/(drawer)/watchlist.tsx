import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import MediaGrid from '@/components/MediaGrid';
import { useMenuContext } from '@/components/MenuContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useWatchlist } from '@/components/WatchlistContext';
import type { Title } from '@/services/api';
import {
  DefaultFocus,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  useSpatialNavigator,
} from '@/services/tv-navigation';
import { mapWatchlistToTitles } from '@/services/watchlist';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { LinearGradient } from 'expo-linear-gradient';
import { Stack, useRouter } from 'expo-router';
import { useCallback, useMemo, useState } from 'react';
import { Platform, StyleSheet, View } from 'react-native';

type WatchlistTitle = Title & { uniqueKey?: string };

export default function WatchlistScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const router = useRouter();
  const isFocused = useIsFocused();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const { pendingPinUserId } = useUserProfiles();
  const isActive = isFocused && !isMenuOpen && !pendingPinUserId;

  const { items, loading, error } = useWatchlist();
  const watchlistTitles = useMemo(() => mapWatchlistToTitles(items), [items]);
  const [filter, setFilter] = useState<'all' | 'movie' | 'series'>('all');
  const [focusedFilterIndex, setFocusedFilterIndex] = useState<number | null>(null);
  const navigator = useSpatialNavigator();

  const filteredWatchlistTitles = useMemo(() => {
    if (filter === 'all') {
      return watchlistTitles;
    }

    return watchlistTitles.filter((title) => title.mediaType === filter);
  }, [filter, watchlistTitles]);

  const filterOptions: Array<{ key: 'all' | 'movie' | 'series'; label: string; icon: keyof typeof Ionicons.glyphMap }> =
    [
      { key: 'all', label: 'All', icon: 'grid-outline' },
      { key: 'movie', label: 'Movies', icon: 'film-outline' },
      { key: 'series', label: 'TV Shows', icon: 'tv-outline' },
    ];

  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      // Enable horizontal step within the filter row when no movement occurred
      if ((movement === 'right' || movement === 'left') && focusedFilterIndex !== null) {
        const delta = movement === 'right' ? 1 : -1;
        const nextIndex = focusedFilterIndex + delta;
        if (nextIndex >= 0 && nextIndex < filterOptions.length) {
          navigator.grabFocus(`watchlist-filter-${filterOptions[nextIndex].key}`);
          return;
        }
      }

      if (movement === 'left') {
        openMenu();
      }
    },
    [filterOptions, focusedFilterIndex, navigator, openMenu],
  );

  const handleTitlePress = useCallback(
    (title: WatchlistTitle) => {
      router.push({
        pathname: '/details',
        params: {
          title: title.name,
          titleId: title.id ?? '',
          mediaType: title.mediaType ?? 'movie',
          description: title.overview ?? '',
          headerImage: title.backdrop?.url ?? title.poster?.url ?? '',
          posterUrl: title.poster?.url ?? '',
          backdropUrl: title.backdrop?.url ?? '',
          tmdbId: title.tmdbId ? String(title.tmdbId) : '',
          imdbId: title.imdbId ?? '',
          tvdbId: title.tvdbId ? String(title.tvdbId) : '',
          year: title.year ? String(title.year) : '',
        },
      });
    },
    [router],
  );

  const filterLabel = filter === 'movie' ? 'Movies' : filter === 'series' ? 'TV Shows' : 'All Titles';

  return (
    <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          {/* Arrange filters and grid vertically for predictable TV navigation */}
          <SpatialNavigationNode orientation="vertical">
            <View style={styles.controlsRow}>
              {/* Make filters a vertical list on TV for Up/Down navigation */}
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.filtersRow}>
                  {filterOptions.map((option, index) => {
                    const isActive = filter === option.key;
                    const isFirst = index === 0;
                    return isFirst ? (
                      <DefaultFocus key={option.key}>
                        <FocusablePressable
                          focusKey={`watchlist-filter-${option.key}`}
                          text={option.label}
                          icon={option.icon}
                          onFocus={() => setFocusedFilterIndex(index)}
                          onSelect={() => setFilter(option.key)}
                          style={[styles.filterButton, isActive && styles.filterButtonActive]}
                        />
                      </DefaultFocus>
                    ) : (
                      <FocusablePressable
                        key={option.key}
                        focusKey={`watchlist-filter-${option.key}`}
                        text={option.label}
                        icon={option.icon}
                        onFocus={() => setFocusedFilterIndex(index)}
                        onSelect={() => setFilter(option.key)}
                        style={[styles.filterButton, isActive && styles.filterButtonActive]}
                      />
                    );
                  })}
                </View>
              </SpatialNavigationNode>
            </View>

            <MediaGrid
              title={`Your Watchlist · ${filterLabel}`}
              items={filteredWatchlistTitles}
              loading={loading}
              error={error}
              onItemPress={handleTitlePress}
              layout="grid"
              numColumns={Platform.isTV ? 6 : 7}
              defaultFocusFirstItem={!theme.breakpoint || theme.breakpoint !== 'compact'}
            />
          </SpatialNavigationNode>

          {Platform.isTV && (
            <LinearGradient
              pointerEvents="none"
              colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.8)']}
              locations={[0, 1]}
              start={{ x: 0.5, y: 0.6 }}
              end={{ x: 0.5, y: 1 }}
              style={styles.bottomGradient}
            />
          )}
        </View>
      </FixedSafeAreaView>
    </SpatialNavigationRoot>
  );
}

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
    },
    container: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: theme.spacing.xl,
      paddingTop: theme.spacing.xl,
    },
    controlsRow: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      flexWrap: 'wrap',
      marginBottom: theme.spacing.sm,
    },
    filtersRow: {
      flexDirection: 'row',
      gap: theme.spacing.sm,
      marginBottom: theme.spacing.sm,
    },
    filterButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    filterButtonActive: {
      borderColor: theme.colors.accent.primary,
    },
    bottomGradient: {
      position: 'absolute',
      left: 0,
      right: 0,
      bottom: 0,
      height: '40%',
    },
  });
