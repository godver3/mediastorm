import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useContinueWatching } from '@/components/ContinueWatchingContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { ListCard } from '@/components/ListCard';
import { useMenuContext } from '@/components/MenuContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useWatchlist } from '@/components/WatchlistContext';
import { MOVIE_GENRES, TV_GENRES, type GenreConfig } from '@/constants/genres';
import { getActiveSeasonalLists } from '@/constants/seasonal';
import { apiService, type TrendingItem } from '@/services/api';
import { useTrendingMovies, useTrendingTVShows } from '@/hooks/useApi';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  SpatialNavigationVirtualizedList,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { focusTextInputTV, prefocusTextInputTV } from '@/utils/tv-text-input';
import { isTablet, responsiveSize } from '@/theme/tokens/tvScale';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { Stack, useRouter } from 'expo-router';
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { View as RNView } from 'react-native';
import { Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';
import { KeyboardAwareScrollView } from 'react-native-keyboard-aware-scroll-view';
import Animated, {
  useAnimatedRef,
  scrollTo as reanimatedScrollTo,
  useSharedValue,
  useAnimatedReaction,
} from 'react-native-reanimated';
import { useTVDimensions } from '@/hooks/useTVDimensions';

// Card data for a shelf item
type ListCardData = {
  key: string;
  content: React.ReactElement;
  onPress: () => void;
};

// Section definition
type SectionDef = {
  title: string;
  key: string;
  cards: ListCardData[];
  customRender?: React.ReactNode;
  cardWidthOverride?: number;
  aspectRatio?: number; // aspect ratio for cards in this section
  layout?: 'shelf' | 'grid'; // shelf = horizontal scroll, grid = wrapped rows
  gridGroups?: GridGroup[]; // for grid layout: multiple groups with sub-headings
};

// TV shelf for a single horizontal row of ListCards
function ListShelf({
  title,
  cards,
  cardWidth,
  cardSpacing,
  autoFocus,
  shelfKey,
  cardAspectRatio,
  registerShelfRef,
  onShelfFocus,
  styles,
}: {
  title: string;
  cards: ListCardData[];
  cardWidth: number;
  cardSpacing: number;
  autoFocus: boolean;
  shelfKey: string;
  cardAspectRatio: number;
  registerShelfRef: (key: string, ref: RNView | null) => void;
  onShelfFocus: (shelfKey: string) => void;
  styles: ReturnType<typeof createStyles>;
}) {
  const containerRef = useRef<RNView | null>(null);
  const itemSize = cardWidth + cardSpacing;

  const cardHeight = cardWidth / cardAspectRatio;
  // Extra padding for the 1.05 scale transform on focus so cards don't clip
  const scaleOverflow = cardHeight * 0.05;
  const rowHeight = cardHeight + scaleOverflow + cardSpacing;

  // Register ref for scroll-to-section
  React.useEffect(() => {
    registerShelfRef(shelfKey, containerRef.current);
    return () => registerShelfRef(shelfKey, null);
  }, [registerShelfRef, shelfKey]);

  const renderItem = useCallback(
    ({ item, index }: { item: ListCardData; index: number }) => {
      const focusable = (
        <SpatialNavigationFocusableView
          onSelect={item.onPress}
          onFocus={() => onShelfFocus(shelfKey)}
        >
          {({ isFocused }: { isFocused: boolean }) => (
            <View style={{ width: cardWidth }}>
              {React.cloneElement(item.content as React.ReactElement<any>, { isFocused })}
            </View>
          )}
        </SpatialNavigationFocusableView>
      );

      if (autoFocus && index === 0) {
        return <DefaultFocus>{focusable}</DefaultFocus>;
      }
      return focusable;
    },
    [cardWidth, autoFocus, shelfKey, onShelfFocus],
  );

  return (
    <View ref={containerRef} style={styles.section}>
      <Text style={styles.sectionTitle}>{title}</Text>
      <View style={{ height: rowHeight, overflow: 'visible' }}>
        <SpatialNavigationVirtualizedList
          data={cards}
          renderItem={renderItem}
          itemSize={itemSize}
          additionalItemsRendered={4}
          orientation="horizontal"
          scrollDuration={300}
        />
      </View>
    </View>
  );
}

// A group of cards within a combined grid (each group gets its own sub-heading)
type GridGroup = {
  title: string;
  cards: ListCardData[];
};

// TV grid layout — one unified spatial nav grid across multiple groups (e.g. Movie + TV genres)
function ListGrid({
  groups,
  cardWidth,
  cardSpacing,
  columnsPerRow,
  shelfKey,
  registerShelfRef,
  onShelfFocus,
  styles,
}: {
  groups: GridGroup[];
  cardWidth: number;
  cardSpacing: number;
  columnsPerRow: number;
  shelfKey: string;
  registerShelfRef: (key: string, ref: RNView | null) => void;
  onShelfFocus: (shelfKey: string) => void;
  styles: ReturnType<typeof createStyles>;
}) {
  const containerRef = useRef<RNView | null>(null);

  React.useEffect(() => {
    registerShelfRef(shelfKey, containerRef.current);
    return () => registerShelfRef(shelfKey, null);
  }, [registerShelfRef, shelfKey]);

  // Build a flat list of renderable items: either a heading or a row of cards
  type RowItem = { type: 'heading'; title: string } | { type: 'row'; cards: ListCardData[] };
  const items = useMemo(() => {
    const result: RowItem[] = [];
    for (const group of groups) {
      result.push({ type: 'heading', title: group.title });
      for (let i = 0; i < group.cards.length; i += columnsPerRow) {
        result.push({ type: 'row', cards: group.cards.slice(i, i + columnsPerRow) });
      }
    }
    return result;
  }, [groups, columnsPerRow]);

  return (
    <View ref={containerRef} style={styles.section}>
      <SpatialNavigationNode orientation="vertical" alignInGrid>
        {items.map((item, idx) => {
          if (item.type === 'heading') {
            return (
              <Text key={`heading-${idx}`} style={styles.sectionTitle}>
                {item.title}
              </Text>
            );
          }
          return (
            <SpatialNavigationNode key={`${shelfKey}-row-${idx}`} orientation="horizontal">
              <View style={[styles.gridRow, { gap: cardSpacing }]}>
                {item.cards.map((card) => (
                  <SpatialNavigationFocusableView
                    key={card.key}
                    onSelect={card.onPress}
                    onFocus={() => onShelfFocus(shelfKey)}
                  >
                    {({ isFocused }: { isFocused: boolean }) => (
                      <View style={{ width: cardWidth }}>
                        {React.cloneElement(card.content as React.ReactElement<any>, { isFocused })}
                      </View>
                    )}
                  </SpatialNavigationFocusableView>
                ))}
              </View>
            </SpatialNavigationNode>
          );
        })}
      </SpatialNavigationNode>
    </View>
  );
}

// TV custom section with spatial-navigation-aware focusable elements
function TVAISection({
  aiQuery,
  setAiQuery,
  handleSurprise,
  surpriseLoading,
  selectedMediaType,
  setSelectedMediaType,
  selectedDecade,
  setSelectedDecade,
  shelfKey,
  registerShelfRef,
  onShelfFocus,
  styles,
  router,
}: {
  aiQuery: string;
  setAiQuery: (q: string) => void;
  handleSurprise: () => void;
  surpriseLoading: boolean;
  selectedMediaType: 'any' | 'movie' | 'show';
  setSelectedMediaType: (t: 'any' | 'movie' | 'show') => void;
  selectedDecade: string | null;
  setSelectedDecade: (d: string | null) => void;
  shelfKey: string;
  registerShelfRef: (key: string, ref: RNView | null) => void;
  onShelfFocus: (shelfKey: string) => void;
  styles: ReturnType<typeof createStyles>;
  router: ReturnType<typeof useRouter>;
}) {
  const theme = useTheme();
  const containerRef = useRef<RNView | null>(null);
  const inputRef = useRef<TextInput>(null);

  React.useEffect(() => {
    registerShelfRef(shelfKey, containerRef.current);
    return () => registerShelfRef(shelfKey, null);
  }, [registerShelfRef, shelfKey]);

  return (
    <View ref={containerRef} style={styles.section}>
      <Text style={styles.sectionTitle}>Ask AI</Text>
      <SpatialNavigationNode orientation="vertical">
        {/* Search input — mirrors search page pattern: parallax disabled, Pressable wrapper */}
        <SpatialNavigationFocusableView
          onSelect={() => {
            focusTextInputTV(inputRef);
          }}
          onBlur={() => {
            inputRef.current?.blur();
          }}
          onFocus={() => { onShelfFocus(shelfKey); prefocusTextInputTV(inputRef); }}
        >
          {({ isFocused }: { isFocused: boolean }) => (
            <View style={styles.aiSearchInputWrapper}>
              <Pressable
                android_disableSound
                tvParallaxProperties={{ enabled: false }}
                style={[
                  styles.aiSearchInputBox,
                  isFocused && styles.aiSearchInputBoxFocused,
                ]}>
                <TextInput
                  ref={inputRef}
                  style={styles.aiSearchInput}
                  placeholder={'Ask for recommendations... e.g. "feel-good comedies from the 90s"'}
                  placeholderTextColor={theme.colors.text.muted}
                  {...(Platform.isTV ? { defaultValue: aiQuery } : { value: aiQuery })}
                  onChangeText={setAiQuery}
                  onSubmitEditing={() => {
                    const q = aiQuery.trim();
                    if (q) {
                      router.push({
                        pathname: '/(drawer)/watchlist',
                        params: { shelf: 'custom-ai', aiQuery: q },
                      } as any);
                      setAiQuery('');
                    }
                  }}
                  returnKeyType="search"
                  {...(Platform.OS === 'android' && Platform.isTV && { caretHidden: true })}
                />
              </Pressable>
            </View>
          )}
        </SpatialNavigationFocusableView>

        {/* Surprise + media type chips row */}
        <SpatialNavigationNode orientation="horizontal">
          <View style={styles.surpriseRow}>
            <SpatialNavigationFocusableView
              onSelect={handleSurprise}
              onFocus={() => onShelfFocus(shelfKey)}
            >
              {({ isFocused }: { isFocused: boolean }) => (
                <View
                  style={[
                    styles.surpriseButton,
                    surpriseLoading && styles.surpriseButtonLoading,
                    isFocused && styles.chipFocused,
                  ]}
                >
                  <Text style={styles.surpriseButtonText}>
                    {surpriseLoading ? 'Loading...' : 'Surprise Me'}
                  </Text>
                </View>
              )}
            </SpatialNavigationFocusableView>
            {(['any', 'movie', 'show'] as const).map((t) => {
              const label = t === 'any' ? 'Any' : t === 'movie' ? 'Movie' : 'Show';
              const isSelected = selectedMediaType === t;
              return (
                <SpatialNavigationFocusableView
                  key={t}
                  onSelect={() => setSelectedMediaType(t)}
                  onFocus={() => onShelfFocus(shelfKey)}
                >
                  {({ isFocused }: { isFocused: boolean }) => (
                    <View
                      style={[
                        styles.decadeChip,
                        isSelected && styles.decadeChipSelected,
                        isFocused && styles.chipFocused,
                      ]}
                    >
                      <Text
                        style={[
                          styles.decadeChipText,
                          isSelected && styles.decadeChipTextSelected,
                          isFocused && styles.chipTextFocused,
                        ]}
                      >
                        {label}
                      </Text>
                    </View>
                  )}
                </SpatialNavigationFocusableView>
              );
            })}
          </View>
        </SpatialNavigationNode>

        {/* Decade chips row */}
        <SpatialNavigationNode orientation="horizontal">
          <View style={[styles.decadeChips, styles.decadeRow]}>
            {['Any', '1960s', '1970s', '1980s', '1990s', '2000s', '2010s', '2020s'].map((d) => {
              const isAny = d === 'Any';
              const isSelected = isAny ? selectedDecade === null : selectedDecade === d;
              return (
                <SpatialNavigationFocusableView
                  key={d}
                  onSelect={() => setSelectedDecade(isAny ? null : d)}
                  onFocus={() => onShelfFocus(shelfKey)}
                >
                  {({ isFocused }: { isFocused: boolean }) => (
                    <View
                      style={[
                        styles.decadeChip,
                        isSelected && styles.decadeChipSelected,
                        isFocused && styles.chipFocused,
                      ]}
                    >
                      <Text
                        style={[
                          styles.decadeChipText,
                          isSelected && styles.decadeChipTextSelected,
                          isFocused && styles.chipTextFocused,
                        ]}
                      >
                        {d}
                      </Text>
                    </View>
                  )}
                </SpatialNavigationFocusableView>
              );
            })}
          </View>
        </SpatialNavigationNode>
      </SpatialNavigationNode>
    </View>
  );
}

// Horizontal section of cards (mobile only)
function CardSection({
  title,
  children,
  styles,
}: {
  title: string;
  children: React.ReactNode;
  styles: ReturnType<typeof createStyles>;
}) {
  return (
    <View style={styles.section}>
      <Text style={styles.sectionTitle}>{title}</Text>
      <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.cardRow}>
        {children}
      </ScrollView>
    </View>
  );
}

export default function ListsScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const router = useRouter();
  const { pendingPinUserId, activeUser } = useUserProfiles();
  const { settings, userSettings } = useBackendSettings();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const isFocused = useIsFocused();
  const isActive = isFocused && !isMenuOpen && !pendingPinUserId;

  const { items: continueWatchingItems } = useContinueWatching();
  const { items: watchlistItems } = useWatchlist();
  const activeUserId = activeUser?.id;
  const { data: trendingMovies } = useTrendingMovies(activeUserId ?? undefined);
  const { data: trendingTVShows } = useTrendingTVShows(activeUserId ?? undefined);
  const [aiQuery, setAiQuery] = useState('');
  const [surpriseLoading, setSurpriseLoading] = useState(false);
  const [selectedDecade, setSelectedDecade] = useState<string | null>(null);
  const [selectedMediaType, setSelectedMediaType] = useState<'any' | 'movie' | 'show'>('any');

  // Get custom shelves from settings
  const customShelves = useMemo(() => {
    const allShelves = userSettings?.homeShelves?.shelves ?? settings?.homeShelves?.shelves ?? [];
    return allShelves.filter((s) => s.type === 'mdblist' && s.enabled && s.listUrl);
  }, [userSettings?.homeShelves?.shelves, settings?.homeShelves?.shelves]);

  // Seasonal lists active right now
  const seasonalLists = useMemo(() => getActiveSeasonalLists(), []);

  // Fetch a backdrop preview for custom mdblist shelves and seasonal lists
  const [listBackdrops, setListBackdrops] = useState<Record<string, string>>({});
  useEffect(() => {
    const listUrls: { id: string; url: string }[] = [
      ...customShelves.map((s) => ({ id: s.id, url: s.listUrl! })),
      ...seasonalLists.map((s) => ({ id: `seasonal-${s.id}`, url: s.mdblistUrl })),
    ];
    if (listUrls.length === 0) return;
    let cancelled = false;
    const fetchBackdrops = async () => {
      const results: Record<string, string> = {};
      const dayOfYear = Math.floor(Date.now() / 86400000);
      await Promise.all(
        listUrls.map(async ({ id, url }) => {
          try {
            const { items } = await apiService.getCustomList(url, activeUserId, 5);
            const withBackdrop = items.filter((i) => i.title.backdrop?.url);
            if (withBackdrop.length > 0) {
              results[id] = withBackdrop[dayOfYear % withBackdrop.length].title.backdrop!.url;
            }
          } catch {
            // Silently fail — card will show without backdrop
          }
        }),
      );
      if (!cancelled) setListBackdrops(results);
    };
    fetchBackdrops();
    return () => { cancelled = true; };
  }, [customShelves, seasonalLists, activeUserId]);

  // TV vertical scrolling infrastructure (modelled after index page)
  const scrollViewRef = useAnimatedRef<Animated.ScrollView>();
  const shelfRefs = useRef<{ [key: string]: RNView | null }>({});
  const shelfPositionsRef = useRef<{ [key: string]: number }>({});
  const shelfScrollTargetY = useSharedValue(-1);
  const focusedShelfKeyRef = useRef<string | null>(null);

  // Drive vertical scrolling from shared value (TV only, runs on UI thread)
  useAnimatedReaction(
    () => shelfScrollTargetY.value,
    (targetY, prevTargetY) => {
      'worklet';
      if (targetY >= 0 && targetY !== prevTargetY) {
        reanimatedScrollTo(scrollViewRef, 0, targetY, true);
      }
    },
    [scrollViewRef],
  );

  const scrollToShelf = useCallback(
    (shelfKey: string) => {
      if (!Platform.isTV) return;

      const cachedPosition = shelfPositionsRef.current[shelfKey];
      if (cachedPosition !== undefined) {
        shelfScrollTargetY.value = Math.max(0, cachedPosition);
        return;
      }

      const shelfRef = shelfRefs.current[shelfKey];
      const scrollViewNode = scrollViewRef.current;
      if (!shelfRef || !scrollViewNode) return;

      shelfRef.measureLayout(
        scrollViewNode as any,
        (_left, top) => {
          shelfPositionsRef.current[shelfKey] = top;
          shelfScrollTargetY.value = Math.max(0, top);
        },
        () => {},
      );
    },
    [scrollViewRef, shelfScrollTargetY],
  );

  const registerShelfRef = useCallback((key: string, ref: RNView | null) => {
    shelfRefs.current[key] = ref;
  }, []);

  const handleShelfFocus = useCallback(
    (shelfKey: string) => {
      if (focusedShelfKeyRef.current !== shelfKey) {
        focusedShelfKeyRef.current = shelfKey;
        scrollToShelf(shelfKey);
      }
    },
    [scrollToShelf],
  );

  // Handle left navigation at edge to open menu
  const onDirectionHandledWithoutMovement = useCallback(
    (direction: Direction) => {
      if (direction === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );



  // Poster URLs for collage cards
  const cwPosterUrls = useMemo(
    () => (continueWatchingItems ?? []).slice(0, 4).map((item) => item.posterUrl ?? ''),
    [continueWatchingItems],
  );

  const watchlistPosterUrls = useMemo(
    () =>
      (watchlistItems ?? [])
        .slice(0, 4)
        .map((item) => item.posterUrl ?? ''),
    [watchlistItems],
  );

  // Recommendation seeds from continue watching (top 3 movies + top 3 shows)
  const recommendationSeeds = useMemo(() => {
    if (!continueWatchingItems) return [];
    const seen = new Set<number>();
    const withTmdb = continueWatchingItems
      .filter((item) => item.externalIds?.tmdb)
      .map((item) => ({
        tmdbId: Number(item.externalIds!.tmdb),
        title: item.seriesTitle,
        backdropUrl: item.backdropUrl,
        mediaType: (item.nextEpisode ? 'tv' : 'movie') as 'tv' | 'movie',
      }))
      .filter((item) => {
        if (seen.has(item.tmdbId)) return false;
        seen.add(item.tmdbId);
        return true;
      });
    const movies = withTmdb.filter((s) => s.mediaType === 'movie').slice(0, 3);
    const shows = withTmdb.filter((s) => s.mediaType === 'tv').slice(0, 3);
    const interleaved: typeof withTmdb = [];
    const maxLen = Math.max(movies.length, shows.length);
    for (let i = 0; i < maxLen; i++) {
      if (i < movies.length) interleaved.push(movies[i]);
      if (i < shows.length) interleaved.push(shows[i]);
    }
    return interleaved;
  }, [continueWatchingItems]);

  // Check if Gemini API key is configured
  const hasGeminiKey = Boolean(settings?.metadata?.geminiApiKey);

  // Card dimensions — TV sizes scaled up ~20% from index page for the lists browsing context
  const { width: screenWidth } = useTVDimensions();
  const cardSpacing = Platform.isTV ? responsiveSize(20, 14) : 12;

  const cardWidth = useMemo(() => {
    if (Platform.isTV) return responsiveSize(384, 288);
    if (isTablet) return 220;
    return screenWidth * 0.42;
  }, [screenWidth]);

  const genreCardWidth = useMemo(() => {
    if (Platform.isTV) return responsiveSize(264, 204);
    if (isTablet) return 160;
    return screenWidth * 0.28;
  }, [screenWidth]);

  const wideCardWidth = useMemo(() => {
    if (Platform.isTV) return responsiveSize(504, 384);
    if (isTablet) return 300;
    return screenWidth * 0.6;
  }, [screenWidth]);

  // Navigation helpers
  const navigateToShelf = useCallback(
    (shelf: string, params?: Record<string, string>) => {
      router.push({
        pathname: '/(drawer)/watchlist',
        params: { shelf, ...params },
      } as any);
    },
    [router],
  );

  const handleSurprise = useCallback(async () => {
    if (surpriseLoading) return;
    setSurpriseLoading(true);
    try {
      const item = await apiService.getAISurprise(selectedDecade ?? undefined, selectedMediaType !== 'any' ? selectedMediaType : undefined);
      const t = item.title;
      router.push({
        pathname: '/details',
        params: {
          title: t.name,
          titleId: t.id ?? '',
          mediaType: t.mediaType ?? 'movie',
          description: t.overview ?? '',
          headerImage: t.backdrop?.url ?? t.poster?.url ?? '',
          posterUrl: t.poster?.url ?? '',
          backdropUrl: t.backdrop?.url ?? '',
          tmdbId: t.tmdbId ? String(t.tmdbId) : '',
        },
      } as any);
    } catch (err) {
      // Silently fail — the user can try again
    } finally {
      setSurpriseLoading(false);
    }
  }, [surpriseLoading, router, selectedDecade, selectedMediaType]);

  // Build sections
  const sections: SectionDef[] = [];

  // Default card aspect ratio (collage/backdrop cards use 16/10 in ListCard)
  const DEFAULT_ASPECT = 16 / 10;

  // Personal section
  const personalCards: ListCardData[] = [];
  if (continueWatchingItems && continueWatchingItems.length > 0) {
    personalCards.push({
      key: 'continue-watching',
      content: (
        <ListCard
          variant="collage"
          title="Continue Watching"
          count={continueWatchingItems.length}
          posterUrls={cwPosterUrls}
        />
      ),
      onPress: () => navigateToShelf('continue-watching'),
    });
  }
  if (watchlistItems && watchlistItems.length > 0) {
    personalCards.push({
      key: 'my-watchlist',
      content: (
        <ListCard
          variant="collage"
          title="My Watchlist"
          count={watchlistItems.length}
          posterUrls={watchlistPosterUrls}
        />
      ),
      onPress: () => router.push('/(drawer)/watchlist' as any),
    });
  }
  if (personalCards.length > 0) {
    sections.push({ title: 'Personal', key: 'personal', cards: personalCards, aspectRatio: DEFAULT_ASPECT });
  }

  // Pick a random backdrop from trending items (stable per day)
  const pickRandomBackdrop = (items: TrendingItem[] | null): string | undefined => {
    if (!items || items.length === 0) return undefined;
    const withBackdrop = items.filter((i) => i.title.backdrop?.url);
    if (withBackdrop.length === 0) return undefined;
    const dayOfYear = Math.floor(Date.now() / 86400000);
    return withBackdrop[dayOfYear % withBackdrop.length].title.backdrop!.url;
  };

  const trendingMovieBackdrop = pickRandomBackdrop(trendingMovies);
  const trendingTVBackdrop = pickRandomBackdrop(trendingTVShows);

  // Trending section — landscape backdrop art from a random trending item
  sections.push({
    title: 'Trending',
    key: 'trending',
    aspectRatio: DEFAULT_ASPECT,
    cards: [
      {
        key: 'trending-movies',
        content: (
          <ListCard
            variant="backdrop"
            title="Trending Movies"
            seedTitle=""
            backdropUrl={trendingMovieBackdrop}
          />
        ),
        onPress: () => navigateToShelf('trending-movies'),
      },
      {
        key: 'trending-tv',
        content: (
          <ListCard
            variant="backdrop"
            title="Trending Shows"
            seedTitle=""
            backdropUrl={trendingTVBackdrop}
          />
        ),
        onPress: () => navigateToShelf('trending-tv'),
      },
    ],
  });

  // Custom lists — backdrop art from a random item in each list
  if (customShelves.length > 0) {
    sections.push({
      title: 'Your Lists',
      key: 'custom-lists',
      aspectRatio: DEFAULT_ASPECT,
      cards: customShelves.map((shelf) => ({
        key: `custom-${shelf.id}`,
        content: (
          <ListCard
            variant="backdrop"
            title={shelf.name}
            seedTitle=""
            backdropUrl={listBackdrops[shelf.id]}
          />
        ),
        onPress: () => navigateToShelf(shelf.id),
      })),
    });
  }

  // Browse by Genre — combined grid for Movie + TV genres
  const makeGenreCards = (genres: GenreConfig[], mediaType: string) =>
    genres.map((genre) => ({
      key: `genre-${mediaType}-${genre.id}`,
      content: <ListCard variant="gradient" title={genre.name} iconName={genre.icon} iconFamily={genre.iconFamily} tintColor={genre.tintColor} aspectRatio={16 / 7} />,
      onPress: () => navigateToShelf(`genre-${genre.id}-${mediaType}`, { genreName: genre.name }),
    }));

  sections.push({
    title: '',
    key: 'genres',
    cards: [], // cards live inside gridGroups instead
    cardWidthOverride: genreCardWidth,
    aspectRatio: 16 / 7,
    layout: 'grid',
    gridGroups: [
      { title: 'Movie Genres', cards: makeGenreCards(MOVIE_GENRES, 'movie') },
      { title: 'TV Show Genres', cards: makeGenreCards(TV_GENRES, 'tv') },
    ],
  });

  // AI-powered curated list (only when Gemini key is configured)
  if (hasGeminiKey) {
    sections.push({
      title: 'Recommended For You',
      key: 'gemini-recs',
      aspectRatio: 16 / 4,
      cards: [{
        key: 'gemini-recs',
        content: <ListCard variant="gradient" title="Recommended For You" subtitle="Personalized by AI" iconName="sparkles" tintColor="rgba(139,92,246,0.12)" aspectRatio={16 / 4} />,
        onPress: () => navigateToShelf('gemini-recs'),
      }],
      cardWidthOverride: wideCardWidth,
    });
  }

  // Per-title "Because you watched" cards (uses Gemma when key set, TMDB otherwise)
  if (recommendationSeeds.length > 0) {
    sections.push({
      title: 'Because You Watched',
      key: 'recommendations',
      aspectRatio: DEFAULT_ASPECT,
      cards: recommendationSeeds.map((seed) => ({
        key: `similar-${seed.mediaType}-${seed.tmdbId}`,
        content: (
          <ListCard
            variant="backdrop"
            title=""
            seedTitle={seed.title}
            backdropUrl={seed.backdropUrl}
          />
        ),
        onPress: () =>
          navigateToShelf(`similar-${seed.tmdbId}`, {
            mediaType: seed.mediaType,
            seedTitle: seed.title,
            ...(hasGeminiKey ? { aiSimilar: '1' } : {}),
          }),
      })),
    });
  }

  // AI free-text search (after recommendations, only when Gemini key is set)
  if (hasGeminiKey) {
    sections.push({
      title: 'Ask AI',
      key: 'ai-search',
      cards: [],
      customRender: 'ai-section',
    });
  }

  // Seasonal — backdrop art from a random item in each seasonal list
  if (seasonalLists.length > 0) {
    sections.push({
      title: 'Seasonal',
      key: 'seasonal',
      aspectRatio: DEFAULT_ASPECT,
      cards: seasonalLists.map((list) => ({
        key: `seasonal-${list.id}`,
        content: (
          <ListCard
            variant="backdrop"
            title={list.name}
            seedTitle=""
            backdropUrl={listBackdrops[`seasonal-${list.id}`]}
          />
        ),
        onPress: () => navigateToShelf(`seasonal-${list.id}`),
      })),
    });
  }

  // Compute visible items per shelf width
  const getVisibleItems = useCallback(
    (cw: number) => {
      if (!Platform.isTV) return 1;
      const availableWidth = screenWidth - TV_LEFT_MARGIN; // account for left margin
      return Math.max(1, Math.floor(availableWidth / (cw + cardSpacing)));
    },
    [screenWidth, cardSpacing],
  );

  const content = (
    <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
      <View style={styles.container}>
        {/* Page title — zIndex keeps it above scrolling content on TV */}
        <View style={styles.titleRow}>
          <Text style={styles.title}>Lists</Text>
        </View>

        {/* Sections */}
        {Platform.isTV ? (
          <View style={styles.scrollClip}>
            <Animated.ScrollView
              ref={scrollViewRef}
              showsVerticalScrollIndicator={false}
              scrollEnabled={false}
              contentInsetAdjustmentBehavior="never"
              automaticallyAdjustContentInsets={false}
              removeClippedSubviews={false}
              style={{ overflow: 'visible' }}
              contentContainerStyle={{ overflow: 'visible' }}
            >
            <SpatialNavigationNode orientation="vertical">
              {sections.map((section, sIdx) => {
                if (section.customRender === 'ai-section') {
                  return (
                    <TVAISection
                      key={section.key}
                      aiQuery={aiQuery}
                      setAiQuery={setAiQuery}
                      handleSurprise={handleSurprise}
                      surpriseLoading={surpriseLoading}
                      selectedMediaType={selectedMediaType}
                      setSelectedMediaType={setSelectedMediaType}
                      selectedDecade={selectedDecade}
                      setSelectedDecade={setSelectedDecade}
                      shelfKey={section.key}
                      registerShelfRef={registerShelfRef}
                      onShelfFocus={handleShelfFocus}
                      styles={styles}
                      router={router}
                    />
                  );
                }

                const sectionCardWidth = section.cardWidthOverride ?? cardWidth;
                const sectionAspectRatio = section.aspectRatio ?? (16 / 6);

                if (section.layout === 'grid') {
                  const groups = section.gridGroups ?? [{ title: section.title, cards: section.cards }];
                  return (
                    <ListGrid
                      key={section.key}
                      groups={groups}
                      cardWidth={sectionCardWidth}
                      cardSpacing={cardSpacing}
                      columnsPerRow={getVisibleItems(sectionCardWidth)}
                      shelfKey={section.key}
                      registerShelfRef={registerShelfRef}
                      onShelfFocus={handleShelfFocus}
                      styles={styles}
                    />
                  );
                }

                return (
                  <ListShelf
                    key={section.key}
                    title={section.title}
                    cards={section.cards}
                    cardWidth={sectionCardWidth}
                    cardSpacing={cardSpacing}
                    cardAspectRatio={sectionAspectRatio}
                    autoFocus={sIdx === 0}
                    shelfKey={section.key}
                    registerShelfRef={registerShelfRef}
                    onShelfFocus={handleShelfFocus}
                    styles={styles}
                  />
                );
              })}
            </SpatialNavigationNode>
          </Animated.ScrollView>
          </View>
        ) : (
          <KeyboardAwareScrollView
            showsVerticalScrollIndicator={false}
            extraScrollHeight={20}
            enableOnAndroid
            keyboardShouldPersistTaps="handled"
          >
            {sections.map((section) =>
              section.customRender ? (
                <View key={section.key} style={styles.section}>
                  <Text style={styles.sectionTitle}>Ask AI</Text>
                  <TextInput
                    style={styles.aiSearchInput}
                    placeholder={'Ask for recommendations... e.g. "feel-good comedies from the 90s"'}
                    placeholderTextColor={theme.colors.text.muted}
                    value={aiQuery}
                    onChangeText={setAiQuery}
                    onSubmitEditing={() => {
                      const q = aiQuery.trim();
                      if (q) {
                        router.push({
                          pathname: '/(drawer)/watchlist',
                          params: { shelf: 'custom-ai', aiQuery: q },
                        } as any);
                        setAiQuery('');
                      }
                    }}
                    returnKeyType="search"
                  />
                  <View style={styles.surpriseRow}>
                    <Pressable
                      style={[styles.surpriseButton, surpriseLoading && styles.surpriseButtonLoading]}
                      onPress={handleSurprise}
                      disabled={surpriseLoading}
                    >
                      <Text style={styles.surpriseButtonText}>
                        {surpriseLoading ? 'Loading...' : 'Surprise Me'}
                      </Text>
                    </Pressable>
                    <View style={styles.decadeChips}>
                      {(['any', 'movie', 'show'] as const).map((t) => {
                        const label = t === 'any' ? 'Any' : t === 'movie' ? 'Movie' : 'Show';
                        const isSelected = selectedMediaType === t;
                        return (
                          <Pressable
                            key={t}
                            style={[styles.decadeChip, isSelected && styles.decadeChipSelected]}
                            onPress={() => setSelectedMediaType(t)}
                          >
                            <Text style={[styles.decadeChipText, isSelected && styles.decadeChipTextSelected]}>
                              {label}
                            </Text>
                          </Pressable>
                        );
                      })}
                    </View>
                  </View>
                  <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.decadeChips} style={styles.decadeRow}>
                    {['Any', '1960s', '1970s', '1980s', '1990s', '2000s', '2010s', '2020s'].map((d) => {
                      const isAny = d === 'Any';
                      const isSelected = isAny ? selectedDecade === null : selectedDecade === d;
                      return (
                        <Pressable
                          key={d}
                          style={[styles.decadeChip, isSelected && styles.decadeChipSelected]}
                          onPress={() => setSelectedDecade(isAny ? null : d)}
                        >
                          <Text style={[styles.decadeChipText, isSelected && styles.decadeChipTextSelected]}>
                            {d}
                          </Text>
                        </Pressable>
                      );
                    })}
                  </ScrollView>
                </View>
              ) : section.gridGroups ? (
                // Expand grid groups into separate mobile card sections
                <React.Fragment key={section.key}>
                  {section.gridGroups.map((group) => (
                    <CardSection key={group.title} title={group.title} styles={styles}>
                      {group.cards.map((card) => {
                        const width = section.cardWidthOverride ?? cardWidth;
                        return (
                          <Pressable key={card.key} onPress={card.onPress} style={{ width }}>
                            {card.content}
                          </Pressable>
                        );
                      })}
                    </CardSection>
                  ))}
                </React.Fragment>
              ) : (
                <CardSection key={section.key} title={section.title} styles={styles}>
                  {section.cards.map((card) => {
                    const isWide = section.key === 'gemini-recs';
                    const width = isWide ? wideCardWidth : (section.cardWidthOverride ?? cardWidth);
                    return (
                      <Pressable key={card.key} onPress={card.onPress} style={{ width }}>
                        {card.content}
                      </Pressable>
                    );
                  })}
                </CardSection>
              )
            )}
          </KeyboardAwareScrollView>
        )}
      </View>
    </FixedSafeAreaView>
  );

  return (
    <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
      <Stack.Screen options={{ headerShown: false }} />
      {content}
    </SpatialNavigationRoot>
  );
}

const TV_LEFT_MARGIN = responsiveSize(48, 36);

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      ...(Platform.isTV && { overflow: 'visible' as const }),
    },
    container: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: Platform.isTV ? 0 : theme.spacing.xl,
      marginLeft: Platform.isTV ? TV_LEFT_MARGIN : 0,
      paddingLeft: Platform.isTV ? 0 : theme.spacing.xl,
      paddingTop: Platform.isTV ? responsiveSize(32, 24) : theme.spacing.xl,
      ...(Platform.isTV && { overflow: 'visible' as const }),
    },
    titleRow: {
      marginBottom: Platform.isTV ? responsiveSize(24, 18) : theme.spacing.lg,
      ...(Platform.isTV && { zIndex: 1, backgroundColor: 'transparent' }),
    },
    scrollClip: {
      flex: 1,
      overflow: 'hidden',
      marginLeft: Platform.isTV ? -responsiveSize(12, 10) : 0,
      paddingLeft: Platform.isTV ? responsiveSize(12, 10) : 0,
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      ...(Platform.isTV && { fontSize: responsiveSize(36, 28) }),
    },
    // AI search input — mirrors search page: Pressable wrapper with parallax disabled
    aiSearchInputWrapper: {
      justifyContent: 'center',
    },
    aiSearchInputBox: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.md,
      borderWidth: Platform.isTV ? 3 : 1,
      borderColor: 'transparent',
    },
    aiSearchInputBoxFocused: {
      borderColor: theme.colors.accent.primary,
      ...(Platform.isTV && Platform.OS === 'ios'
        ? {
            shadowColor: theme.colors.accent.primary,
            shadowOffset: { width: 0, height: 0 },
            shadowOpacity: 0.5,
            shadowRadius: 8,
          }
        : {}),
    },
    aiSearchInput: {
      paddingHorizontal: Platform.isTV ? responsiveSize(20, 16) : theme.spacing.md,
      paddingVertical: Platform.isTV ? responsiveSize(16, 12) : 12,
      color: theme.colors.text.primary,
      fontSize: Platform.isTV ? responsiveSize(22, 18) : 15,
      minHeight: Platform.isTV ? responsiveSize(60, 48) : undefined,
    },
    surpriseButton: {
      backgroundColor: 'rgba(255,255,255,0.1)',
      borderRadius: 10,
      paddingHorizontal: Platform.isTV ? responsiveSize(24, 18) : 16,
      paddingVertical: Platform.isTV ? responsiveSize(14, 10) : 12,
      borderWidth: 1,
      borderColor: 'rgba(255,255,255,0.15)',
    },
    surpriseButtonLoading: {
      opacity: 0.5,
    },
    surpriseButtonText: {
      color: theme.colors.text.primary,
      fontSize: Platform.isTV ? responsiveSize(18, 15) : 15,
      fontWeight: '600',
    },
    surpriseRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: Platform.isTV ? responsiveSize(14, 10) : 10,
      marginTop: Platform.isTV ? responsiveSize(16, 12) : 12,
    },
    decadeChips: {
      flexDirection: 'row',
      gap: Platform.isTV ? responsiveSize(10, 8) : 6,
      paddingRight: theme.spacing.xl,
    },
    decadeRow: {
      marginTop: Platform.isTV ? responsiveSize(12, 8) : 8,
    },
    decadeChip: {
      paddingHorizontal: Platform.isTV ? responsiveSize(20, 16) : 14,
      paddingVertical: Platform.isTV ? responsiveSize(12, 10) : 6,
      borderRadius: Platform.isTV ? responsiveSize(20, 16) : 16,
      backgroundColor: 'rgba(255,255,255,0.06)',
      borderWidth: 1,
      borderColor: 'rgba(255,255,255,0.1)',
    },
    decadeChipSelected: {
      backgroundColor: 'rgba(255,255,255,0.18)',
      borderColor: 'rgba(255,255,255,0.3)',
    },
    decadeChipText: {
      color: theme.colors.text.secondary,
      fontSize: Platform.isTV ? responsiveSize(17, 14) : 13,
    },
    decadeChipTextSelected: {
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    chipFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    chipTextFocused: {
      color: theme.colors.text.inverse,
      fontWeight: '600',
    },
    section: {
      marginBottom: Platform.isTV ? responsiveSize(28, 20) : theme.spacing.lg,
      ...(Platform.isTV && { overflow: 'visible' as const }),
    },
    sectionTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      marginBottom: Platform.isTV ? responsiveSize(14, 10) : theme.spacing.md,
      ...(Platform.isTV && { fontSize: responsiveSize(24, 18) }),
    },
    cardRow: {
      flexDirection: 'row',
      gap: Platform.isTV ? responsiveSize(20, 14) : 12,
      paddingRight: theme.spacing.xl,
    },
    gridRow: {
      flexDirection: 'row',
      flexWrap: 'nowrap',
      marginBottom: Platform.isTV ? responsiveSize(16, 12) : 10,
      overflow: 'visible' as const,
    },
  });
