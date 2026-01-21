// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: MIT-0

import { useBackendSettings } from '@/components/BackendSettingsContext';
import {
  QUALITY_OPTIONS,
  YEAR_RANGE_OPTIONS,
  matchesYearRange,
  useDiscoveryPreferences,
} from '@/hooks/useDiscoveryPreferences';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV, responsiveSize } from '@/theme/tokens/tvScale';
import { Stack, useRouter } from 'expo-router';
import { useCallback, useMemo } from 'react';
import { StyleSheet, Text, View } from 'react-native';
import MediaGrid from '../../components/MediaGrid';
import { Title } from '../../services/api';
import { DefaultFocus, SpatialNavigationFocusableView, SpatialNavigationNode } from '@/services/tv-navigation';

// Preference button using spatial navigation for TV platforms
const PreferenceButton = ({
  label,
  isActive,
  onPress,
  autoFocus,
  theme,
}: {
  label: string;
  isActive: boolean;
  onPress: () => void;
  autoFocus?: boolean;
  theme: NovaTheme;
}) => {
  const paddingH = responsiveSize(24, 12);
  const paddingV = responsiveSize(12, 8);
  const fontSize = responsiveSize(22, 14);
  const borderRadius = responsiveSize(8, 6);

  const content = (
    <SpatialNavigationFocusableView onSelect={onPress}>
      {({ isFocused }: { isFocused: boolean }) => (
        <View
          style={{
            paddingHorizontal: paddingH,
            paddingVertical: paddingV,
            borderRadius,
            minWidth: responsiveSize(120, 80),
            backgroundColor: isFocused
              ? theme.colors.accent.primary
              : isActive
                ? theme.colors.accent.secondary
                : theme.colors.overlay.button,
            borderWidth: StyleSheet.hairlineWidth,
            borderColor: isFocused
              ? theme.colors.accent.primary
              : isActive
                ? theme.colors.accent.secondary
                : theme.colors.border.subtle,
          }}>
          <Text
            style={{
              fontSize,
              fontWeight: '500',
              textAlign: 'center',
              color: isFocused ? theme.colors.text.inverse : theme.colors.text.primary,
            }}>
            {label}
          </Text>
        </View>
      )}
    </SpatialNavigationFocusableView>
  );

  if (autoFocus) {
    return <DefaultFocus>{content}</DefaultFocus>;
  }

  return content;
};

export default function TVShowsScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const { settings, userSettings } = useBackendSettings();
  const router = useRouter();

  const { preferences, updatePreferences, qualityLabel } = useDiscoveryPreferences();

  // Fetch trending TV shows
  const { data: trendingTVShows, loading, error } = useTrendingTVShows();

  const filteredShows = useMemo(() => {
    const titles = trendingTVShows?.map((item) => item.title) ?? [];
    return titles.filter((item) => matchesYearRange(item.year, preferences.yearRange));
  }, [preferences.yearRange, trendingTVShows]);

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
    <>
      <Stack.Screen options={{ headerShown: false }} />
      <View style={styles.container}>
        <Text style={styles.title}>TV Shows</Text>

        <SpatialNavigationNode orientation="vertical">
          <View style={styles.preferenceSection}>
            <Text style={styles.sectionLabel}>Quality</Text>
            <SpatialNavigationNode orientation="horizontal">
              <View style={styles.preferenceRow}>
                {QUALITY_OPTIONS.map((option, index) => (
                  <PreferenceButton
                    key={option.value}
                    label={option.label}
                    isActive={preferences.quality === option.value}
                    onPress={() => updatePreferences({ quality: option.value })}
                    autoFocus={index === 0}
                    theme={theme}
                  />
                ))}
              </View>
            </SpatialNavigationNode>

            <Text style={styles.sectionLabel}>Era</Text>
            <SpatialNavigationNode orientation="horizontal">
              <View style={styles.preferenceRow}>
                {YEAR_RANGE_OPTIONS.map((option) => (
                  <PreferenceButton
                    key={option.value}
                    label={option.label}
                    isActive={preferences.yearRange === option.value}
                    onPress={() => updatePreferences({ yearRange: option.value })}
                    theme={theme}
                  />
                ))}
              </View>
            </SpatialNavigationNode>
          </View>
        </SpatialNavigationNode>

        <MediaGrid
          title={`Trending TV Shows • ${qualityLabel}`}
          items={filteredShows}
          loading={loading}
          error={error}
          onItemPress={handleTVShowPress}
          badgeVisibility={userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility}
          watchStateIconStyle={userSettings?.display?.watchStateIconStyle ?? settings?.display?.watchStateIconStyle ?? 'colored'}
          useNativeFocus={false}
        />
      </View>
    </>
  );
}

// Import the hook that was used
import { useTrendingTVShows } from '../../hooks/useApi';

const createStyles = (theme: NovaTheme) => {
  const titleSize = responsiveSize(36, 24);
  const sectionLabelSize = responsiveSize(20, 14);

  return StyleSheet.create({
    container: {
      flex: 1,
      backgroundColor: isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: theme.spacing.xl,
      paddingTop: theme.spacing.xl,
    },
    title: {
      fontSize: titleSize,
      fontWeight: '700',
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
      fontSize: sectionLabelSize,
      color: theme.colors.text.secondary,
    },
    preferenceRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm,
    },
  });
};
