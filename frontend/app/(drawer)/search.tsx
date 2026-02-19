import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { Image } from '@/components/Image';
import { useMenuContext } from '@/components/MenuContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useSearchTitles } from '@/hooks/useApi';
import { Title } from '@/services/api';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Direction } from '@bam.tech/lrud';
import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';
import { useIsFocused } from '@react-navigation/native';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { LinearGradient } from 'expo-linear-gradient';
import { Stack, useRouter } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Keyboard,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { responsiveSize, tvScale } from '@/theme/tokens/tvScale';

type ResultTitle = Title & { uniqueKey: string };

const MAX_RECENT_SEARCHES = 5;

// Calculate similarity score between search query and a single title string
function calculateSingleSimilarity(query: string, title: string): number {
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

// Calculate similarity score checking name, originalName, and alternateTitles
// Returns the best (highest) similarity score found
function calculateSimilarity(
  query: string,
  name: string,
  originalName?: string,
  alternateTitles?: string[],
): number {
  let bestScore = calculateSingleSimilarity(query, name);

  // Check originalName (e.g., "Moana" when display name is "Vaiana")
  if (originalName) {
    const originalScore = calculateSingleSimilarity(query, originalName);
    if (originalScore > bestScore) {
      bestScore = originalScore;
    }
  }

  // Check alternate titles (all translations)
  if (alternateTitles && alternateTitles.length > 0) {
    for (const alt of alternateTitles) {
      const altScore = calculateSingleSimilarity(query, alt);
      if (altScore > bestScore) {
        bestScore = altScore;
      }
    }
  }

  return bestScore;
}

export default function SearchScreen() {
  const theme = useTheme();
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth, screenHeight), [theme, screenWidth, screenHeight]);
  const inputRef = useRef<TextInput>(null);
  const router = useRouter();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const { pendingPinUserId, activeUser } = useUserProfiles();

  // Check if user is in kids curated list mode (content restricted to allowed lists)
  const isKidsCuratedMode =
    activeUser?.isKidsProfile && activeUser.kidsMode === 'content_list';
  const isFocused = useIsFocused();
  const isActive = isFocused && !isMenuOpen && !pendingPinUserId;

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
  const [recentSearches, setRecentSearches] = useState<string[]>([]);
  const tempQueryRef = useRef('');
  const rowRefs = useRef<{ [key: string]: View | null }>({});
  const rowPositionsRef = useRef<{ [key: string]: number }>({});
  const mainScrollViewRef = useRef<any>(null);
  const isNavigatingRef = useRef(false);
  const [filter, setFilter] = useState<'all' | 'movie' | 'series'>('all');

  // Per-user AsyncStorage key for recent searches
  const recentSearchesKey = activeUser?.id ? `strmr.recentSearches.${activeUser.id}` : null;

  // Load recent searches from storage; reload when profile changes
  useEffect(() => {
    setRecentSearches([]);
    if (!recentSearchesKey) return;
    const loadRecentSearches = async () => {
      try {
        const stored = await AsyncStorage.getItem(recentSearchesKey);
        if (stored) {
          const parsed = JSON.parse(stored);
          if (Array.isArray(parsed)) {
            setRecentSearches(parsed.slice(0, MAX_RECENT_SEARCHES));
          }
        }
      } catch (error) {
        console.warn('Failed to load recent searches:', error);
      }
    };
    loadRecentSearches();
  }, [recentSearchesKey]);

  // Save a new search to recent searches
  const saveRecentSearch = useCallback(async (searchQuery: string) => {
    const trimmed = searchQuery.trim();
    if (!trimmed || !recentSearchesKey) return;

    setRecentSearches((prev) => {
      // Remove if already exists, then add to front
      const filtered = prev.filter((s) => s.toLowerCase() !== trimmed.toLowerCase());
      const updated = [trimmed, ...filtered].slice(0, MAX_RECENT_SEARCHES);

      // Persist to storage
      AsyncStorage.setItem(recentSearchesKey, JSON.stringify(updated)).catch((error) => {
        console.warn('Failed to save recent searches:', error);
      });

      return updated;
    });
  }, [recentSearchesKey]);

  const filterOptions: Array<{ key: 'all' | 'movie' | 'series'; label: string; icon: keyof typeof Ionicons.glyphMap }> =
    [
      { key: 'all', label: 'All', icon: 'grid-outline' },
      { key: 'movie', label: 'Movies', icon: 'film-outline' },
      { key: 'series', label: 'TV Shows', icon: 'tv-outline' },
    ];

  const { data: searchResults, loading, error } = useSearchTitles(submittedQuery, activeUser?.id);
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

        // Second priority: sort by similarity score (checks name, originalName, and alternateTitles)
        const scoreA = calculateSimilarity(searchQuery, a.name || '', a.originalName, a.alternateTitles);
        const scoreB = calculateSimilarity(searchQuery, b.name || '', b.originalName, b.alternateTitles);
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

  // Clear row position cache when results change (positions will be different)
  React.useEffect(() => {
    rowPositionsRef.current = {};
  }, [filteredItems]);

  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      // Open menu when pressing left at the edge
      if (movement === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

  const handleResultPress = useCallback(
    (title: ResultTitle) => {
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
  }, []);

  const handleBlur = useCallback(() => {
    console.log('[SEARCH DEBUG] TextInput onBlur called');
    // Trigger search when keyboard closes
    if (Platform.isTV) {
      const finalQuery = tempQueryRef.current.trim();
      if (finalQuery) {
        setQuery(finalQuery);
        setSubmittedQuery(finalQuery);
        saveRecentSearch(finalQuery);
      }
    }
  }, [saveRecentSearch]);

  const handleSubmit = useCallback(() => {
    // Trigger search on submit
    const finalQuery = Platform.isTV ? tempQueryRef.current : query;
    setQuery(finalQuery);
    setSubmittedQuery(finalQuery);
    if (finalQuery.trim()) {
      saveRecentSearch(finalQuery);
    }
    inputRef.current?.blur();
  }, [query, saveRecentSearch]);

  const handleSelectRecentSearch = useCallback((searchTerm: string) => {
    setQuery(searchTerm);
    setSubmittedQuery(searchTerm);
    tempQueryRef.current = searchTerm;
    // Don't save again since it's already in recent searches
  }, []);

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

  // Scroll to row when it receives focus (for TV navigation) — match home index behavior
  // Uses position caching to avoid expensive measureLayout calls on Android TV
  const scrollToRow = useCallback((rowKey: string) => {
    if (!Platform.isTV || !mainScrollViewRef.current) {
      return;
    }

    const scrollView = mainScrollViewRef.current;

    const performScroll = (targetY: number) => {
      scrollView?.scrollTo({ y: targetY, animated: true });
    };

    // Check cache first (avoids expensive measureLayout on Android)
    const cachedPosition = rowPositionsRef.current[rowKey];
    if (cachedPosition !== undefined) {
      performScroll(cachedPosition);
      return;
    }

    // Fall back to measureLayout for first access, then cache
    const rowRef = rowRefs.current[rowKey];
    if (!rowRef) {
      return;
    }

    rowRef.measureLayout(
      scrollView as any,
      (_left, top) => {
        const targetY = Math.max(0, top - 20);
        // Cache the position for future use
        rowPositionsRef.current[rowKey] = targetY;
        performScroll(targetY);
      },
      () => {
        // silently ignore failures
      },
    );
  }, []);

  const handleClearRecentSearch = useCallback(async (searchTerm: string) => {
    if (!recentSearchesKey) return;
    setRecentSearches((prev) => {
      const updated = prev.filter((s) => s !== searchTerm);
      AsyncStorage.setItem(recentSearchesKey, JSON.stringify(updated)).catch((error) => {
        console.warn('Failed to save recent searches:', error);
      });
      return updated;
    });
  }, [recentSearchesKey]);

  const renderContent = () => {
    if (!hasQuery) {
      // Show recent searches if available
      if (recentSearches.length > 0) {
        return (
          <View style={styles.recentSearchesContainer}>
            <Text style={styles.recentSearchesTitle}>Recent Searches</Text>
            {isCompact ? (
              // Mobile layout - simple list
              <View style={styles.recentSearchesList}>
                {recentSearches.map((term, index) => (
                  <Pressable
                    key={`recent-${index}`}
                    style={({ pressed }) => [
                      styles.recentSearchItem,
                      pressed && styles.recentSearchItemPressed,
                    ]}
                    onPress={() => handleSelectRecentSearch(term)}>
                    <MaterialCommunityIcons
                      name="history"
                      size={20}
                      color={theme.colors.text.secondary}
                      style={styles.recentSearchIcon}
                    />
                    <Text style={styles.recentSearchText} numberOfLines={1}>
                      {term}
                    </Text>
                    <Pressable
                      hitSlop={8}
                      onPress={(e) => {
                        e.stopPropagation();
                        handleClearRecentSearch(term);
                      }}
                      style={styles.recentSearchClearButton}>
                      <MaterialCommunityIcons
                        name="close"
                        size={18}
                        color={theme.colors.text.muted}
                      />
                    </Pressable>
                  </Pressable>
                ))}
              </View>
            ) : (
              // TV layout - focusable items
              <SpatialNavigationNode orientation="vertical">
                <View style={styles.recentSearchesList}>
                  {recentSearches.map((term, index) => (
                    <SpatialNavigationFocusableView
                      key={`recent-${index}`}
                      onSelect={() => handleSelectRecentSearch(term)}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <View
                          style={[
                            styles.recentSearchItemTV,
                            isFocused && styles.recentSearchItemTVFocused,
                          ]}>
                          <MaterialCommunityIcons
                            name="history"
                            size={24}
                            color={isFocused ? theme.colors.text.inverse : theme.colors.text.secondary}
                            style={styles.recentSearchIcon}
                          />
                          <Text
                            style={[
                              styles.recentSearchTextTV,
                              isFocused && styles.recentSearchTextTVFocused,
                            ]}
                            numberOfLines={1}>
                            {term}
                          </Text>
                        </View>
                      )}
                    </SpatialNavigationFocusableView>
                  ))}
                </View>
              </SpatialNavigationNode>
            )}
          </View>
        );
      }

      return (
        <View style={styles.placeholderContainer}>
          <Text style={styles.placeholderText}>Enter a title to see search results.</Text>
        </View>
      );
    }

    // Only show full loading state if we have no results yet
    if (loading && (!filteredItems || filteredItems.length === 0)) {
      return (
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="large" color={theme.colors.accent.primary} />
          {!Platform.isTV && <Text style={styles.loadingText}>Searching...</Text>}
        </View>
      );
    }

    if (error && (!filteredItems || filteredItems.length === 0)) {
      return (
        <View style={styles.errorContainer}>
          <Text style={styles.errorText}>Error: {error}</Text>
        </View>
      );
    }

    if (!filteredItems || filteredItems.length === 0) {
      const filterLabel = filter === 'movie' ? 'movies' : filter === 'series' ? 'TV shows' : 'results';
      return (
        <View style={styles.emptyContainer}>
          <Text style={styles.emptyText}>
            No {filterLabel} found for "{submittedQuery}"
          </Text>
        </View>
      );
    }

    // For mobile/compact view, use a proper interactive grid
    if (isCompact) {
      return (
        <ScrollView
          style={styles.scrollView}
          contentContainerStyle={styles.gridContent}
          showsVerticalScrollIndicator={false}>
          {!Platform.isTV && <Text style={styles.resultsTitle}>Search Results</Text>}
          <View style={styles.grid}>
            {filteredItems.map((item, index) => (
              <Pressable
                key={item.uniqueKey || `item-${index}`}
                style={styles.compactCard}
                onPress={() => handleResultPress(item)}
                android_ripple={{ color: theme.colors.accent.primary + '30' }}>
                <View style={styles.cardImageContainer}>
                  {item.poster?.url ? (
                    <Image
                      source={{ uri: item.poster.url }}
                      style={styles.cardImage}
                      contentFit="cover"
                      transition={0}
                      cachePolicy={Platform.isTV ? 'disk' : 'memory'}
                    />
                  ) : (
                    <View style={styles.placeholder}>
                      <Text style={styles.placeholderImageText}>No Image</Text>
                    </View>
                  )}
                  {item.mediaType && (
                    <View style={styles.badge}>
                      <Text style={styles.badgeText}>{item.mediaType === 'series' ? 'TV' : 'MOVIE'}</Text>
                    </View>
                  )}
                  <View style={styles.cardTextContainer}>
                    <LinearGradient
                      pointerEvents="none"
                      colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
                      locations={[0, 0.6, 1]}
                      start={{ x: 0.5, y: 0 }}
                      end={{ x: 0.5, y: 1 }}
                      style={styles.cardTextGradient}
                    />
                    <Text style={styles.cardTitle} numberOfLines={2}>
                      {item.name}
                    </Text>
                    {item.year ? <Text style={styles.cardYear}>{item.year}</Text> : null}
                  </View>
                </View>
              </Pressable>
            ))}
          </View>
          {loading && (
            <View style={styles.loadingMoreContainer}>
              <ActivityIndicator size="small" color={theme.colors.accent.primary} />
            </View>
          )}
        </ScrollView>
      );
    }

    // For TV/desktop/wide tablets, use focusable grid with spatial navigation
    // Split items into rows for proper grid navigation
    // Use 6 columns on TV, otherwise base on screen width
    const columnsPerRow = Platform.isTV
      ? 6
      : screenWidth >= 1200
        ? 7
        : screenWidth >= 900
          ? 6
          : screenWidth >= 600
            ? 5
            : 4;
    const rows: ResultTitle[][] = [];
    for (let i = 0; i < filteredItems.length; i += columnsPerRow) {
      rows.push(filteredItems.slice(i, i + columnsPerRow));
    }

    // Key changes when row count changes, forcing grid recalculation
    const gridKey = `grid-${rows.length}`;

    return (
      <ScrollView
        ref={mainScrollViewRef}
        style={styles.scrollView}
        bounces={false}
        showsVerticalScrollIndicator={false}
        // TV: disable native scroll - use programmatic scrolling only to prevent
        // native focus from moving the grid when drawer is open
        scrollEnabled={!Platform.isTV}
        contentInsetAdjustmentBehavior="never"
        automaticallyAdjustContentInsets={false}
        removeClippedSubviews={Platform.isTV}
        scrollEventThrottle={16}
        // Android TV: prevent native focus-based scrolling
        focusable={false}
        // @ts-ignore - TV-specific prop
        isTVSelectable={false}>
        <View style={styles.gridContent}>
          {!Platform.isTV && <Text style={styles.resultsTitle}>Search Results</Text>}
          <SpatialNavigationNode key={gridKey} orientation="vertical" alignInGrid>
            {rows.map((row, rowIndex) => {
              const rowKey = `row-${rowIndex}`;

              return (
                <View
                  key={rowKey}
                  ref={(ref) => {
                    rowRefs.current[rowKey] = ref;
                  }}
                  style={styles.rowContainer}>
                  <SpatialNavigationNode orientation="horizontal">
                    <View style={styles.gridRow}>
                      {row.map((item, colIndex) => {
                        const cardKey = item.uniqueKey || `item-${rowIndex}-${colIndex}`;

                        const focusable = (
                          <SpatialNavigationFocusableView
                            onSelect={() => handleResultPress(item)}
                            onFocus={() => scrollToRow(rowKey)}>
                            {({ isFocused }: { isFocused: boolean }) => (
                              <View style={[styles.card, isFocused && styles.cardFocused]}>
                                <View style={styles.cardImageContainer}>
                                  {item.poster?.url ? (
                                    <Image
                                      key={`img-${cardKey}`}
                                      source={{ uri: item.poster.url }}
                                      style={styles.cardImage}
                                      contentFit="cover"
                                      transition={0}
                                      cachePolicy={Platform.isTV ? 'disk' : 'memory'}
                                      recyclingKey={cardKey}
                                    />
                                  ) : (
                                    <View style={styles.placeholder}>
                                      <Text style={styles.placeholderText}>No Image</Text>
                                    </View>
                                  )}
                                  {item.mediaType && (
                                    <View style={styles.badge}>
                                      <Text style={styles.badgeText}>
                                        {item.mediaType === 'series' ? 'TV' : 'MOVIE'}
                                      </Text>
                                    </View>
                                  )}
                                </View>
                                <View style={styles.cardTextContainer}>
                                  <LinearGradient
                                    pointerEvents="none"
                                    colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
                                    locations={[0, 0.6, 1]}
                                    start={{ x: 0.5, y: 0 }}
                                    end={{ x: 0.5, y: 1 }}
                                    style={styles.cardTextGradient}
                                  />
                                  <Text style={styles.cardTitle} numberOfLines={2}>
                                    {item.name}
                                  </Text>
                                  {item.year ? <Text style={styles.cardYear}>{item.year}</Text> : null}
                                </View>
                              </View>
                            )}
                          </SpatialNavigationFocusableView>
                        );

                        return <React.Fragment key={cardKey}>{focusable}</React.Fragment>;
                      })}
                    </View>
                  </SpatialNavigationNode>
                </View>
              );
            })}
          </SpatialNavigationNode>
          {loading && (
            <View style={styles.loadingMoreContainer}>
              <ActivityIndicator size="small" color={theme.colors.accent.primary} />
            </View>
          )}
        </View>
      </ScrollView>
    );
  };

  return (
    <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          <SpatialNavigationNode orientation="vertical">
            <View style={styles.headerRow}>
              <DefaultFocus>
                <SpatialNavigationFocusableView
                  onSelect={() => {
                    // Programmatically focus the TextInput to show keyboard on TV
                    inputRef.current?.focus();
                  }}
                  onBlur={() => {
                    // Blur the TextInput when spatial navigation moves away
                    inputRef.current?.blur();
                  }}>
                  {({ isFocused: textInputFocused }: { isFocused: boolean }) => (
                    <View
                      style={[styles.searchInputWrapper, textInputFocused && styles.searchInputWrapperFocused]}
                      pointerEvents={isMenuOpen ? 'none' : 'auto'}>
                      <Pressable
                        android_disableSound
                        tvParallaxProperties={{ enabled: false }}
                        style={[styles.searchInputBox, textInputFocused && styles.searchInputBoxFocused]}>
                      <View style={styles.searchInputContent}>
                        {!isCompact && (
                          <MaterialCommunityIcons
                            name="magnify"
                            style={styles.searchIcon}
                            size={isCompact ? 20 : 28}
                          />
                        )}
                        <TextInput
                          ref={inputRef}
                          style={[styles.searchInput, textInputFocused && styles.searchInputFocused]}
                          placeholder="Search for movies or TV shows"
                          placeholderTextColor={theme.colors.text.muted}
                          {...(Platform.isTV ? { defaultValue: query } : { value: query })}
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
                          editable={Platform.isTV ? textInputFocused && !isMenuOpen : !isMenuOpen}
                          {...(Platform.OS === 'ios' &&
                            Platform.isTV && {
                              keyboardAppearance: 'dark',
                            })}
                        />
                        {showClearButton ? (
                          <Pressable
                            accessibilityHint="Clears the current search"
                            accessibilityLabel="Clear search"
                            hitSlop={10}
                            onPress={handleClearSearch}
                            style={({ pressed }) => [styles.clearButton, pressed && styles.clearButtonPressed]}>
                            <MaterialCommunityIcons
                              name="close"
                              style={styles.clearButtonIcon}
                              size={isCompact ? 22 : 26}
                            />
                          </Pressable>
                        ) : null}
                      </View>
                      </Pressable>
                    </View>
                  )}
                </SpatialNavigationFocusableView>
              </DefaultFocus>

              {/* Filter buttons - styled to match TVActionButton */}
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.filtersRow}>
                  {filterOptions.map((option) => {
                    const isFilterActive = filter === option.key;
                    // Match TVActionButton sizing exactly
                    const scale = tvScale(1.375, 1);
                    const baseIconSize = 24;
                    const scaledIconSize = tvScale(baseIconSize * 1.375, baseIconSize);
                    const paddingH = theme.spacing.md * scale;
                    const paddingV = theme.spacing.sm * scale;
                    const borderRadius = theme.radius.md * scale;
                    const fontSize = theme.typography.label.md.fontSize * scale;
                    const lineHeight = theme.typography.label.md.lineHeight * scale;
                    const gap = theme.spacing.sm * scale;
                    // Consistent border width to prevent button resizing when active state changes
                    const borderWidth = 2 * scale;

                    return (
                      <SpatialNavigationFocusableView key={option.key} onSelect={() => setFilter(option.key)}>
                        {({ isFocused }: { isFocused: boolean }) => (
                          <View
                            style={{
                              flexDirection: 'row',
                              alignItems: 'center',
                              gap,
                              paddingHorizontal: paddingH,
                              paddingVertical: paddingV,
                              borderRadius,
                              backgroundColor: isFocused
                                ? theme.colors.accent.primary
                                : isFilterActive
                                  ? 'transparent'
                                  : theme.colors.overlay.button,
                              borderWidth,
                              borderColor: isFocused || isFilterActive ? theme.colors.accent.primary : 'transparent',
                            }}>
                            <Ionicons
                              name={option.icon}
                              size={scaledIconSize}
                              color={isFocused ? theme.colors.text.inverse : isFilterActive ? theme.colors.accent.primary : theme.colors.text.primary}
                            />
                            <Text
                              style={{
                                color: isFocused ? theme.colors.text.inverse : isFilterActive ? theme.colors.accent.primary : theme.colors.text.primary,
                                fontSize,
                                lineHeight,
                                fontWeight: '500',
                              }}>
                              {option.label}
                            </Text>
                          </View>
                        )}
                      </SpatialNavigationFocusableView>
                    );
                  })}
                </View>
              </SpatialNavigationNode>
            </View>

            {/* Kids mode notice - search disabled for curated-list profiles */}
            {isKidsCuratedMode && (
              <View style={styles.kidsNotice}>
                <MaterialCommunityIcons
                  name="information-outline"
                  size={isCompact ? 16 : 20}
                  color={theme.colors.warning.text}
                />
                <Text style={styles.kidsNoticeText}>
                  Search is not available on this profile. Content is limited to curated lists.
                </Text>
              </View>
            )}

            {renderContent()}
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

const createStyles = (theme: NovaTheme, screenWidth: number, _screenHeight: number) => {
  // Calculate card dimensions for proper grid layout
  const isCompact = theme.breakpoint === 'compact';

  // Grid configuration - use 4 columns on wide mobile screens (foldables, tablets)
  const isWideCompact = isCompact && screenWidth >= 600;
  const columnsCount = isCompact ? (isWideCompact ? 4 : 2) : Platform.isTV ? 6 : 7;
  const gap = isCompact ? theme.spacing.md : theme.spacing.lg;
  const horizontalPadding = isCompact ? theme.spacing.lg : theme.spacing['2xl'];

  // Calculate card width based on screen width to fill available space
  // Available width = screen width - (horizontal padding * 2) - (gaps between cards)
  const availableWidth = screenWidth - horizontalPadding * 2;
  const totalGapWidth = gap * (columnsCount - 1);
  const cardWidth = Math.floor((availableWidth - totalGapWidth) / columnsCount);

  // Card height maintains portrait poster aspect ratio (2:3)
  const cardHeight = Math.round(cardWidth * (3 / 2));

  return StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
    },
    container: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: horizontalPadding,
      paddingTop: theme.spacing.xl,
    },
    headerRow: {
      flexDirection: 'column',
      gap: theme.spacing.md,
      marginBottom: theme.spacing.md,
    },
    filtersRow: {
      flexDirection: 'row',
      gap: theme.spacing.lg, // Match details page actionRow gap
      flexShrink: 0,
    },
    searchInputWrapper: {
      justifyContent: 'center',
    },
    searchInputWrapperFocused: {
      // No additional styling needed - focus handled by searchInputBox
    },
    searchInputBox: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.md,
      borderWidth: isCompact ? 2 : 3,
      borderColor: 'transparent',
    },
    searchInputBoxFocused: {
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
    searchInputContent: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: isCompact ? theme.spacing.sm : theme.spacing.lg,
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
      backgroundColor: 'transparent',
      // Android TV renders larger than tvOS, so use smaller minHeight
      minHeight: isCompact ? 44 : Platform.OS === 'android' ? 36 : 60,
    },
    searchInputFocused: {
      // Focus styling handled by searchInputBox
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
    scrollView: {
      flex: 1,
    },
    gridContent: {
      paddingBottom: theme.spacing['3xl'],
    },
    resultsTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.lg,
    },
    grid: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: gap,
      justifyContent: 'flex-start',
    },
    rowContainer: {
      marginBottom: gap,
    },
    gridRow: {
      flexDirection: 'row',
      gap: gap,
      flexWrap: 'wrap',
    },
    card: {
      width: cardWidth,
      height: cardHeight,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
      borderWidth: 3,
      borderColor: 'transparent',
    },
    cardFocused: {
      borderColor: theme.colors.accent.primary,
      // Keep borderWidth constant to prevent layout shift
      // Only color changes for better performance
    },
    compactCard: {
      width: cardWidth,
      height: cardHeight,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      borderWidth: 3,
      borderColor: 'transparent',
    },
    cardImageContainer: {
      width: '100%',
      height: '100%',
      backgroundColor: theme.colors.background.elevated,
      position: 'relative',
    },
    cardImage: {
      width: '100%',
      height: '100%',
    },
    badge: {
      position: 'absolute',
      top: isCompact ? theme.spacing.xs : Platform.OS === 'android' ? theme.spacing.xs : theme.spacing.sm,
      right: isCompact ? theme.spacing.xs : Platform.OS === 'android' ? theme.spacing.xs : theme.spacing.sm,
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      paddingHorizontal: isCompact ? theme.spacing.sm : Platform.OS === 'android' ? theme.spacing.xs : theme.spacing.md,
      paddingVertical: isCompact ? 2 : Platform.OS === 'android' ? 1 : theme.spacing.xs,
      borderRadius: theme.radius.sm,
      borderWidth: isCompact ? 1 : Platform.OS === 'android' ? 1 : 2,
      borderColor: theme.colors.accent.primary,
    },
    badgeText: {
      ...theme.typography.caption.sm,
      color: theme.colors.accent.primary,
      fontWeight: '700',
      // Android TV renders larger, use smaller font
      fontSize: isCompact ? 10 : Platform.OS === 'android' ? 10 : 16,
      letterSpacing: 0.5,
    },
    cardTextContainer: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      padding: isCompact ? theme.spacing.sm : theme.spacing.md,
      gap: theme.spacing.xs,
      alignItems: 'center',
      justifyContent: 'flex-end',
      minHeight: '40%',
    },
    cardTextGradient: {
      ...StyleSheet.absoluteFillObject,
    },
    cardTitle: {
      ...(Platform.isTV ? theme.typography.body.lg : theme.typography.body.md),
      ...(Platform.isTV && Platform.OS === 'ios'
        ? {
            fontSize: Math.round(theme.typography.body.lg.fontSize * 1.5),
            lineHeight: Math.round(theme.typography.body.lg.lineHeight * 1.5),
          }
        : null),
      color: theme.colors.text.primary,
      textAlign: 'center',
      zIndex: 1,
      fontWeight: '600',
    },
    cardYear: {
      ...(Platform.isTV ? theme.typography.body.md : theme.typography.caption.sm),
      ...(Platform.isTV && Platform.OS === 'ios'
        ? {
            fontSize: Math.round(theme.typography.body.md.fontSize * 1.25),
            lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.25),
          }
        : null),
      color: theme.colors.text.secondary,
      textAlign: 'center',
      zIndex: 1,
    },
    cardInfo: {
      padding: theme.spacing.sm,
      gap: 2,
      justifyContent: 'center',
    },
    placeholder: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: theme.colors.background.elevated,
    },
    placeholderImageText: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      textAlign: 'center',
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
    loadingContainer: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      paddingVertical: theme.spacing['3xl'],
    },
    loadingText: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.md,
    },
    loadingMoreContainer: {
      alignItems: 'center',
      paddingVertical: theme.spacing.xl,
    },
    errorContainer: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      paddingVertical: theme.spacing['3xl'],
    },
    errorText: {
      ...theme.typography.body.md,
      color: theme.colors.status.danger,
      textAlign: 'center',
    },
    emptyContainer: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      paddingVertical: theme.spacing['3xl'],
    },
    emptyText: {
      ...theme.typography.body.md,
      color: theme.colors.text.muted,
      textAlign: 'center',
    },
    recentSearchesContainer: {
      paddingTop: theme.spacing.lg,
    },
    recentSearchesTitle: {
      ...(Platform.isTV ? theme.typography.title.lg : theme.typography.title.md),
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.md,
    },
    recentSearchesList: {
      gap: isCompact ? theme.spacing.xs : theme.spacing.sm,
    },
    recentSearchItem: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.md,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
    },
    recentSearchItemPressed: {
      opacity: 0.7,
    },
    recentSearchItemTV: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingVertical: theme.spacing.lg,
      paddingHorizontal: theme.spacing.xl,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      borderWidth: 3,
      borderColor: 'transparent',
    },
    recentSearchItemTVFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    recentSearchIcon: {
      marginRight: theme.spacing.md,
    },
    recentSearchText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      flex: 1,
    },
    recentSearchTextTV: {
      ...theme.typography.body.lg,
      fontSize: responsiveSize(theme.typography.body.lg.fontSize * 1.25, screenWidth),
      color: theme.colors.text.primary,
      flex: 1,
    },
    recentSearchTextTVFocused: {
      color: theme.colors.text.inverse,
    },
    recentSearchClearButton: {
      padding: theme.spacing.xs,
    },
    bottomGradient: {
      position: 'absolute',
      left: 0,
      right: 0,
      bottom: 0,
      height: '25%',
    },
    kidsNotice: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
      backgroundColor: theme.colors.warning.muted,
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.md,
      marginBottom: theme.spacing.md,
    },
    kidsNoticeText: {
      ...theme.typography.body.sm,
      color: theme.colors.warning.text,
      flex: 1,
    },
  });
};
