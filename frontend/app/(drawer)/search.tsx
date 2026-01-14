import { useBackendSettings } from '@/components/BackendSettingsContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import MediaGrid from '@/components/MediaGrid';
import { useMenuContext } from '@/components/MenuContext';
import { useSearchTitles } from '@/hooks/useApi';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import { Title } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV, responsiveSize } from '@/theme/tokens/tvScale';
import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import { useIsFocused } from '@react-navigation/native';
import { Stack, useRouter } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Keyboard, Platform, Pressable, StyleSheet, Text, TextInput, View } from 'react-native';

// Native filter button - uses Pressable with style function (no re-renders)
// Uses responsiveSize() for unified scaling across tvOS and Android TV
const NativeFilterButton = ({
  label,
  icon,
  isActive,
  onPress,
  autoFocus,
  theme,
}: {
  label: string;
  icon: keyof typeof Ionicons.glyphMap;
  isActive: boolean;
  onPress: () => void;
  autoFocus?: boolean;
  theme: NovaTheme;
}) => {
  // Unified responsive sizing - design for 1920px width, scales automatically
  const iconSize = responsiveSize(36, 20);
  const paddingH = responsiveSize(28, 14);
  const paddingV = responsiveSize(16, 8);
  const borderRadius = responsiveSize(12, 6);
  const fontSize = responsiveSize(24, 14);
  const lineHeight = responsiveSize(32, 18);
  const gap = responsiveSize(12, 6);

  return (
    <Pressable
      onPress={onPress}
      hasTVPreferredFocus={autoFocus}
      tvParallaxProperties={{ enabled: false }}
      style={({ focused }) => [
        {
          flexDirection: 'row',
          alignItems: 'center',
          gap,
          paddingHorizontal: paddingH,
          paddingVertical: paddingV,
          borderRadius,
          backgroundColor: focused ? theme.colors.accent.primary : theme.colors.overlay.button,
          borderWidth: responsiveSize(6, 2),
          borderColor: focused
            ? theme.colors.accent.primary
            : isActive
              ? theme.colors.accent.primary
              : 'transparent',
        },
      ]}
    >
      {({ focused }) => (
        <>
          <Ionicons
            name={icon}
            size={iconSize}
            color={focused ? theme.colors.text.inverse : theme.colors.text.primary}
          />
          <Text
            style={{
              color: focused ? theme.colors.text.inverse : theme.colors.text.primary,
              fontSize,
              lineHeight,
              fontWeight: '500',
            }}
          >
            {label}
          </Text>
        </>
      )}
    </Pressable>
  );
};

// Calculate similarity score between search query and title name
function calculateSimilarity(query: string, title: string): number {
  const queryLower = query.toLowerCase().trim();
  const titleLower = title.toLowerCase().trim();

  // Exact match gets highest score
  if (titleLower === queryLower) return 1000;

  // Starts with query gets very high score
  if (titleLower.startsWith(queryLower)) return 900;

  // Contains query as whole word gets high score
  const queryWords = queryLower.split(/\s+/);
  const titleWords = titleLower.split(/\s+/);

  // Count matching words
  let matchingWords = 0;
  for (const qWord of queryWords) {
    if (titleWords.some((tWord) => tWord === qWord)) {
      matchingWords++;
    }
  }

  if (matchingWords === queryWords.length) return 800;

  // Contains query substring gets medium score
  if (titleLower.includes(queryLower)) return 700;

  // Partial word matches
  let partialMatches = 0;
  for (const qWord of queryWords) {
    if (titleWords.some((tWord) => tWord.includes(qWord) || qWord.includes(tWord))) {
      partialMatches++;
    }
  }

  // Score based on percentage of matching words
  const wordMatchScore = (partialMatches / queryWords.length) * 600;

  // Bonus for title containing all query characters in order
  let charIndex = 0;
  for (let i = 0; i < titleLower.length && charIndex < queryLower.length; i++) {
    if (titleLower[i] === queryLower[charIndex]) {
      charIndex++;
    }
  }
  const sequenceBonus = (charIndex / queryLower.length) * 100;

  return wordMatchScore + sequenceBonus;
}

export default function SearchScreen() {
  const theme = useTheme();
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth, screenHeight), [theme, screenWidth, screenHeight]);
  const inputRef = useRef<TextInput>(null);
  const router = useRouter();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const isFocused = useIsFocused();
  const { settings, userSettings } = useBackendSettings();

  // Reset navigation flag when screen becomes focused again
  // And clean up keyboard overlay when screen loses focus or unmounts
  React.useEffect(() => {
    if (isFocused) {
      console.log('[SEARCH DEBUG] Screen focused, resetting isNavigatingRef');
      isNavigatingRef.current = false;
    } else {
      // When screen loses focus (user navigates away), forcibly clean up text entry overlay
      if (Platform.isTV) {
        console.log('[SEARCH DEBUG] Screen unfocused, cleaning up text entry overlay');
        inputRef.current?.blur();
        Keyboard.dismiss();
      }
    }

    // Cleanup function that runs when component unmounts or before next effect
    return () => {
      if (Platform.isTV) {
        console.log('[SEARCH DEBUG] Screen cleanup/unmount, dismissing keyboard');
        inputRef.current?.blur();
        Keyboard.dismiss();
      }
    };
  }, [isFocused]);

  // Dismiss keyboard when menu opens
  React.useEffect(() => {
    if (Platform.isTV && isMenuOpen) {
      console.log('[SEARCH DEBUG] Menu opened, dismissing keyboard');
      inputRef.current?.blur();
      Keyboard.dismiss();
    }
  }, [isMenuOpen]);
  const [query, setQuery] = useState('');
  const [submittedQuery, setSubmittedQuery] = useState('');
  const tempQueryRef = useRef('');
  const isNavigatingRef = useRef(false);
  const [filter, setFilter] = useState<'all' | 'movie' | 'series'>('all');
  const [isInputFocused, setIsInputFocused] = useState(false);

  const filterOptions: Array<{ key: 'all' | 'movie' | 'series'; label: string; icon: keyof typeof Ionicons.glyphMap }> =
    [
      { key: 'all', label: 'All', icon: 'grid-outline' },
      { key: 'movie', label: 'Movies', icon: 'film-outline' },
      { key: 'series', label: 'TV Shows', icon: 'tv-outline' },
    ];

  const { data: searchResults, loading, error } = useSearchTitles(submittedQuery);
  const items = useMemo(() => {
    const seen = new Map<string, number>();
    const titlesWithKeys =
      searchResults?.map((result, index) => {
        const title = result.title;
        const baseKey = title.id || (title.name ? `${title.name}-${title.year ?? 'unknown'}` : `result-${index}`);
        const count = seen.get(baseKey) ?? 0;
        seen.set(baseKey, count + 1);
        const uniqueKey = count === 0 ? baseKey : `${baseKey}-${count}`;
        return { ...title, uniqueKey };
      }) ?? [];

    // Sort by similarity to search query, with items without posters at the bottom
    const searchQuery = submittedQuery.trim();
    if (searchQuery && titlesWithKeys.length > 0) {
      // Helper to check if poster is a real image (not a placeholder)
      const hasRealPoster = (posterUrl?: string) => {
        if (!posterUrl) return false;
        // Check for placeholder/missing image URLs
        return !posterUrl.includes('/missing/');
      };

      const sorted = titlesWithKeys.sort((a, b) => {
        const hasPosterA = hasRealPoster(a.poster?.url);
        const hasPosterB = hasRealPoster(b.poster?.url);

        // First priority: items with real posters come before items without
        if (hasPosterA && !hasPosterB) return -1;
        if (!hasPosterA && hasPosterB) return 1;

        // Second priority: sort by similarity score
        const scoreA = calculateSimilarity(searchQuery, a.name || '');
        const scoreB = calculateSimilarity(searchQuery, b.name || '');
        return scoreB - scoreA; // Higher scores first
      });

      // Debug logging
      if (__DEV__) {
        console.log('Search results sorted:');
        sorted.slice(0, 10).forEach((item, idx) => {
          console.log(
            `  ${idx + 1}. ${item.name} (${item.mediaType}) - Real poster: ${hasRealPoster(item.poster?.url)}, URL: ${item.poster?.url || 'N/A'}`,
          );
        });
      }

      return sorted;
    }

    return titlesWithKeys;
  }, [searchResults, submittedQuery]);

  const filteredItems = useMemo(() => {
    if (filter === 'all') {
      return items;
    }
    return items.filter((item) => item.mediaType === filter);
  }, [items, filter]);

  // TV: Handle left key to open menu when not typing
  useEffect(() => {
    if (!isTV || isMenuOpen || isInputFocused) return;

    const handleKeyDown = (key: SupportedKeys) => {
      if (key === SupportedKeys.Left) {
        openMenu();
      }
    };

    RemoteControlManager.addKeydownListener(handleKeyDown);
    return () => {
      RemoteControlManager.removeKeydownListener(handleKeyDown);
    };
  }, [isMenuOpen, isInputFocused, openMenu]);

  const handleResultPress = useCallback(
    (title: Title) => {
      console.log('[SEARCH DEBUG] handleResultPress called for:', title.name);
      console.log('[SEARCH DEBUG] Input focused before blur:', inputRef.current?.isFocused());

      // Set flag to prevent TextInput from accepting focus during navigation
      isNavigatingRef.current = true;

      // Ensure keyboard is dismissed before navigation on tvOS
      if (Platform.isTV) {
        inputRef.current?.blur();
        Keyboard.dismiss();
        console.log('[SEARCH DEBUG] Called blur and dismiss');
      }

      console.log('[SEARCH DEBUG] Navigating to details page');
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

  const handleFocus = useCallback(() => {
    console.log('[SEARCH DEBUG] TextInput onFocus called');
    console.log('[SEARCH DEBUG] isNavigatingRef.current:', isNavigatingRef.current);

    // If we're navigating away, immediately blur
    if (isNavigatingRef.current) {
      console.log('[SEARCH DEBUG] Blocking focus during navigation, blurring immediately');
      inputRef.current?.blur();
      return;
    }

    setIsInputFocused(true);
  }, []);

  const handleBlur = useCallback(() => {
    console.log('[SEARCH DEBUG] TextInput onBlur called');
    setIsInputFocused(false);
    // Trigger search when keyboard closes
    if (Platform.isTV) {
      const finalQuery = tempQueryRef.current.trim();
      if (finalQuery) {
        setQuery(finalQuery);
        setSubmittedQuery(finalQuery);
      }
    }
  }, []);

  const handleSubmit = useCallback(() => {
    // Trigger search on submit
    const finalQuery = Platform.isTV ? tempQueryRef.current : query;
    setQuery(finalQuery);
    setSubmittedQuery(finalQuery);
    inputRef.current?.blur();
  }, [query]);

  const handleChangeText = useCallback((text: string) => {
    if (Platform.isTV) {
      // On tvOS, store in ref to avoid controlled input issues
      tempQueryRef.current = text;
    } else {
      // On mobile, use normal controlled input
      setQuery(text);
    }
  }, []);

  const handleClearSearch = useCallback(() => {
    setQuery('');
    setSubmittedQuery('');
    tempQueryRef.current = '';
    if (!Platform.isTV) {
      inputRef.current?.blur();
      Keyboard.dismiss();
    }
  }, [setQuery, setSubmittedQuery]);

  const hasQuery = submittedQuery.trim().length > 0;
  // Use mobile layout on phones, but switch to grid layout on wider screens (tablets, foldables)
  const isMobileDevice = (Platform.OS === 'ios' || Platform.OS === 'android') && !Platform.isTV;
  const isWideScreen = screenWidth >= 600;
  const isCompact = (isMobileDevice && !isWideScreen) || theme.breakpoint === 'compact';
  const showClearButton = isCompact && query.trim().length > 0;

  const filterLabel = filter === 'movie' ? 'Movies' : filter === 'series' ? 'TV Shows' : 'All';
  const emptyMessage = useMemo(() => {
    const type = filter === 'movie' ? 'movies' : filter === 'series' ? 'TV shows' : 'results';
    return `No ${type} found for "${submittedQuery}"`;
  }, [filter, submittedQuery]);

  const renderContent = () => {
    // Show placeholder when no query has been submitted
    if (!hasQuery) {
      return (
        <View style={styles.placeholderContainer}>
          <Text style={styles.placeholderText}>Enter a title to see search results.</Text>
        </View>
      );
    }

    // Use MediaGrid for consistent styling with watchlist
    return (
      <MediaGrid
        title={`Search Results · ${filterLabel}`}
        items={filteredItems}
        loading={loading}
        error={error ?? undefined}
        onItemPress={handleResultPress}
        layout="grid"
        numColumns={6}
        defaultFocusFirstItem={true}
        badgeVisibility={userSettings?.display?.badgeVisibility ?? settings?.display?.badgeVisibility}
        emptyMessage={emptyMessage}
        useNativeFocus={true}
      />
    );
  };

  return (
    <>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          <View style={styles.headerRow}>
            {/* Search input - on TV, wrapping in Pressable allows focus */}
            {Platform.isTV ? (
              <Pressable
                onPress={() => inputRef.current?.focus()}
                hasTVPreferredFocus={!hasQuery}
                tvParallaxProperties={{ enabled: false }}
                style={({ focused }) => [
                  styles.searchInputWrapper,
                  focused && styles.searchInputWrapperFocused,
                ]}
              >
                {({ focused }) => (
                  <View style={styles.searchInputContent} pointerEvents={isMenuOpen ? 'none' : 'auto'}>
                    {!isCompact && (
                      <MaterialCommunityIcons
                        name="magnify"
                        style={styles.searchIcon}
                        size={isCompact ? 20 : 28}
                      />
                    )}
                    <TextInput
                      ref={inputRef}
                      style={[styles.searchInput, (focused || isInputFocused) && styles.searchInputFocused]}
                      placeholder="Search for movies or TV shows"
                      placeholderTextColor={theme.colors.text.muted}
                      defaultValue={query}
                      onChangeText={handleChangeText}
                      onFocus={handleFocus}
                      onBlur={handleBlur}
                      returnKeyType="search"
                      onSubmitEditing={handleSubmit}
                      autoCorrect={false}
                      autoCapitalize="none"
                      autoComplete="off"
                      textContentType="none"
                      spellCheck={false}
                      clearButtonMode="never"
                      enablesReturnKeyAutomatically={false}
                      multiline={false}
                      numberOfLines={1}
                      underlineColorAndroid="transparent"
                      importantForAutofill="no"
                      disableFullscreenUI={true}
                      editable={(focused || isInputFocused) && !isMenuOpen}
                      {...(Platform.OS === 'ios' && { keyboardAppearance: 'dark' })}
                    />
                  </View>
                )}
              </Pressable>
            ) : (
              <View style={styles.searchInputWrapper} pointerEvents={isMenuOpen ? 'none' : 'auto'}>
                <View style={styles.searchInputContent}>
                  <TextInput
                    ref={inputRef}
                    style={styles.searchInput}
                    placeholder="Search for movies or TV shows"
                    placeholderTextColor={theme.colors.text.muted}
                    value={query}
                    onChangeText={handleChangeText}
                    onFocus={handleFocus}
                    onBlur={handleBlur}
                    returnKeyType="search"
                    onSubmitEditing={handleSubmit}
                    autoCorrect={false}
                    autoCapitalize="none"
                    autoComplete="off"
                    textContentType="none"
                    spellCheck={false}
                    clearButtonMode="never"
                    enablesReturnKeyAutomatically={false}
                    multiline={false}
                    numberOfLines={1}
                    underlineColorAndroid="transparent"
                    importantForAutofill="no"
                    editable={!isMenuOpen}
                  />
                  {showClearButton ? (
                    <Pressable
                      accessibilityHint="Clears the current search"
                      accessibilityLabel="Clear search"
                      hitSlop={10}
                      onPress={handleClearSearch}
                      style={({ pressed }) => [styles.clearButton, pressed && styles.clearButtonPressed]}
                    >
                      <MaterialCommunityIcons
                        name="close"
                        style={styles.clearButtonIcon}
                        size={isCompact ? 22 : 26}
                      />
                    </Pressable>
                  ) : null}
                </View>
              </View>
            )}

            {/* Filter buttons */}
            <View style={styles.filtersRow}>
              {filterOptions.map((option) => (
                <NativeFilterButton
                  key={option.key}
                  label={option.label}
                  icon={option.icon}
                  isActive={filter === option.key}
                  onPress={() => setFilter(option.key)}
                  theme={theme}
                />
              ))}
            </View>
          </View>

          {renderContent()}

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
    </>
  );
}

const createStyles = (theme: NovaTheme, _screenWidth: number, _screenHeight: number) => {
  const isCompact = theme.breakpoint === 'compact';

  return StyleSheet.create({
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
    headerRow: {
      flexDirection: 'column',
      gap: theme.spacing.md,
      marginBottom: theme.spacing.md,
    },
    filtersRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm,
      marginBottom: theme.spacing.sm,
    },
    searchInputWrapper: {
      justifyContent: 'center',
    },
    searchInputContent: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: isCompact ? theme.spacing.sm : theme.spacing.lg,
      backgroundColor: 'transparent',
    },
    searchInputWrapperFocused: {
      // No additional styling needed - focus handled by TextInput
    },
    searchIcon: {
      color: theme.colors.text.secondary,
      opacity: 0.9,
    },
    searchInput: {
      flex: 1,
      // Android TV renders larger than tvOS, so use smaller font size
      fontSize: isCompact ? theme.typography.body.lg.fontSize : Platform.OS === 'android' ? 16 : 32,
      color: theme.colors.text.primary,
      paddingHorizontal: isCompact ? theme.spacing.md : Platform.OS === 'android' ? theme.spacing.sm : theme.spacing.lg,
      paddingVertical: isCompact ? theme.spacing.sm : Platform.OS === 'android' ? theme.spacing.xs : theme.spacing.md,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      // Invisible border same size as focused to prevent layout shift
      borderWidth: 3,
      borderColor: 'transparent',
      // Android TV renders larger than tvOS, so use smaller minHeight
      minHeight: isCompact ? 44 : Platform.OS === 'android' ? 36 : 60,
    },
    searchInputFocused: {
      borderColor: theme.colors.accent.primary,
      ...(Platform.isTV && Platform.OS === 'ios'
        ? {
            shadowColor: theme.colors.accent.primary,
            shadowOpacity: 0.4,
            shadowOffset: { width: 0, height: 4 },
            shadowRadius: 12,
          }
        : null),
      ...(Platform.isTV && Platform.OS === 'android'
        ? {
            elevation: 8,
          }
        : null),
    },
    clearButton: {
      width: isCompact ? 34 : 40,
      height: isCompact ? 34 : 40,
      borderRadius: 999,
      backgroundColor: theme.colors.background.base,
      alignItems: 'center',
      justifyContent: 'center',
    },
    clearButtonPressed: {
      opacity: 0.7,
    },
    clearButtonIcon: {
      color: theme.colors.text.secondary,
    },
    placeholderContainer: {
      flex: 1,
      alignItems: 'center',
      justifyContent: 'center',
      paddingHorizontal: theme.spacing.xl,
    },
    placeholderText: {
      ...(Platform.isTV ? theme.typography.title.xl : theme.typography.body.md),
      color: theme.colors.text.muted,
      textAlign: 'center',
    },
    bottomGradient: {
      position: 'absolute',
      left: 0,
      right: 0,
      bottom: 0,
      height: '25%',
    },
  });
};
