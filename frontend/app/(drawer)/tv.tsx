// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT-0

import { useBackendSettings } from '@/components/BackendSettingsContext';
import FocusablePressable from '@/components/FocusablePressable';
import {
  QUALITY_OPTIONS,
  YEAR_RANGE_OPTIONS,
  matchesYearRange,
  useDiscoveryPreferences,
} from '@/hooks/useDiscoveryPreferences';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { Stack, useRouter } from 'expo-router';
import { useCallback, useMemo } from 'react';
import { Platform, StyleSheet, Text, View } from 'react-native';
import MediaGrid from '../../components/MediaGrid';
import { useMenuContext } from '../../components/MenuContext';
import { useUserProfiles } from '../../components/UserProfilesContext';
import { useTrendingTVShows } from '../../hooks/useApi';
import { Title } from '../../services/api';

export default function TVShowsScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const { pendingPinUserId, userSettings } = useUserProfiles();
  const { settings } = useBackendSettings();
  const isFocused = useIsFocused();
  const isActive = isFocused && !isMenuOpen && !pendingPinUserId;
  const focusedIndex = 0;
  const router = useRouter();

  const { preferences, updatePreferences, qualityLabel } = useDiscoveryPreferences();

  // Fetch trending TV shows
  const { data: trendingTVShows, loading, error } = useTrendingTVShows();

  const filteredShows = useMemo(() => {
    const titles = trendingTVShows?.map((item) => item.title) ?? [];
    return titles.filter((item) => matchesYearRange(item.year, preferences.yearRange));
  }, [preferences.yearRange, trendingTVShows]);

  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      if (movement === 'left' && focusedIndex === 0) {
        openMenu();
      }
    },
    [openMenu, focusedIndex],
  );

  const handleTVShowPress = useCallback(
    (title: Title) => {
      router.push({
        pathname: '/details',
        params: {
          title: title.name,
          titleId: title.id ?? '',
          mediaType: title.mediaType ?? 'series',
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

  return (
    <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
      <Stack.Screen options={{ headerShown: false }} />
      <View style={styles.container}>
        <DefaultFocus>
          <SpatialNavigationFocusableView>
            <Text style={styles.title}>TV Shows</Text>
          </SpatialNavigationFocusableView>
        </DefaultFocus>

        <View style={styles.preferenceSection}>
          <Text style={styles.sectionLabel}>Quality</Text>
          <SpatialNavigationNode orientation="horizontal">
            <View style={styles.preferenceRow}>
              {QUALITY_OPTIONS.map((option) => (
                <FocusablePressable
                  key={option.value}
                  focusKey={`quality-${option.value}`}
                  text={option.label}
                  onSelect={() => updatePreferences({ quality: option.value })}
                  style={[
                    styles.preferenceButton,
                    preferences.quality === option.value && styles.preferenceButtonActive,
                  ]}
                />
              ))}
            </View>
          </SpatialNavigationNode>

          <Text style={styles.sectionLabel}>Era</Text>
          <SpatialNavigationNode orientation="horizontal">
            <View style={styles.preferenceRow}>
              {YEAR_RANGE_OPTIONS.map((option) => (
                <FocusablePressable
                  key={option.value}
                  focusKey={`year-${option.value}`}
                  text={option.label}
                  onSelect={() => updatePreferences({ yearRange: option.value })}
                  style={[
                    styles.preferenceButton,
                    preferences.yearRange === option.value && styles.preferenceButtonActive,
                  ]}
                />
              ))}
            </View>
          </SpatialNavigationNode>
        </View>

        <MediaGrid
          title={`Trending TV Shows • ${qualityLabel}`}
          items={filteredShows}
          loading={loading}
          error={error}
          onItemPress={handleTVShowPress}
          badgeVisibility={userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility}
        />
      </View>
    </SpatialNavigationRoot>
  );
}

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    container: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: theme.spacing.xl,
      paddingTop: theme.spacing.xl,
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.lg,
      textAlign: 'left',
      alignSelf: 'flex-start',
    },
    preferenceSection: {
      gap: theme.spacing.md,
      marginBottom: theme.spacing['2xl'],
    },
    sectionLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    preferenceRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm,
    },
    preferenceButton: {
      minWidth: 96,
    },
    preferenceButtonActive: {
      backgroundColor: theme.colors.accent.secondary,
      borderColor: theme.colors.accent.secondary,
    },
  });
