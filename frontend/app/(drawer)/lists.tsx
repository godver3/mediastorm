import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useContinueWatching } from '@/components/ContinueWatchingContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { ListCard } from '@/components/ListCard';
import { useMenuContext } from '@/components/MenuContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useWatchlist } from '@/components/WatchlistContext';
import { MOVIE_GENRES, TV_GENRES, type GenreConfig } from '@/constants/genres';
import { getActiveSeasonalLists } from '@/constants/seasonal';
import { apiService } from '@/services/api';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTablet, responsiveSize } from '@/theme/tokens/tvScale';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { Stack, useRouter } from 'expo-router';
import React, { useCallback, useMemo, useState } from 'react';
import { Platform, Pressable, ScrollView, StyleSheet, Text, TextInput, View } from 'react-native';
import { KeyboardAwareScrollView } from 'react-native-keyboard-aware-scroll-view';
import { useTVDimensions } from '@/hooks/useTVDimensions';

// Horizontal section of cards
function CardSection({
  title,
  children,
  theme,
  styles,
}: {
  title: string;
  children: React.ReactNode;
  theme: NovaTheme;
  styles: ReturnType<typeof createStyles>;
}) {
  return (
    <View style={styles.section}>
      <Text style={styles.sectionTitle}>{title}</Text>
      {Platform.isTV ? (
        <SpatialNavigationNode orientation="horizontal">
          <View style={styles.cardRow}>{children}</View>
        </SpatialNavigationNode>
      ) : (
        <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={styles.cardRow}>
          {children}
        </ScrollView>
      )}
    </View>
  );
}

export default function ListsScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const router = useRouter();
  const { pendingPinUserId } = useUserProfiles();
  const { settings, userSettings } = useBackendSettings();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const isFocused = useIsFocused();
  const isActive = isFocused && !isMenuOpen && !pendingPinUserId;

  const { items: continueWatchingItems } = useContinueWatching();
  const { items: watchlistItems } = useWatchlist();
  const [aiQuery, setAiQuery] = useState('');
  const [surpriseLoading, setSurpriseLoading] = useState(false);
  const [selectedDecade, setSelectedDecade] = useState<string | null>(null);
  const [selectedMediaType, setSelectedMediaType] = useState<'any' | 'movie' | 'show'>('any');

  // Handle left navigation at edge to open menu
  const onDirectionHandledWithoutMovement = useCallback(
    (direction: Direction) => {
      if (direction === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

  // Get custom shelves from settings
  const customShelves = useMemo(() => {
    const allShelves = userSettings?.homeShelves?.shelves ?? settings?.homeShelves?.shelves ?? [];
    return allShelves.filter((s) => s.type === 'mdblist' && s.enabled && s.listUrl);
  }, [userSettings?.homeShelves?.shelves, settings?.homeShelves?.shelves]);

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
    const withTmdb = continueWatchingItems
      .filter((item) => item.externalIds?.tmdb)
      .map((item) => ({
        tmdbId: Number(item.externalIds!.tmdb),
        title: item.seriesTitle,
        backdropUrl: item.backdropUrl,
        mediaType: (item.nextEpisode ? 'tv' : 'movie') as 'tv' | 'movie',
      }));
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

  // Seasonal lists active right now
  const seasonalLists = useMemo(() => getActiveSeasonalLists(), []);

  // Card dimensions
  const { width: screenWidth } = useTVDimensions();
  const cardWidth = useMemo(() => {
    if (Platform.isTV) return responsiveSize(320, 240);
    if (isTablet) return 220;
    return screenWidth * 0.42;
  }, [screenWidth]);

  const genreCardWidth = useMemo(() => {
    if (Platform.isTV) return responsiveSize(220, 170);
    if (isTablet) return 160;
    return screenWidth * 0.28;
  }, [screenWidth]);

  const wideCardWidth = useMemo(() => {
    if (Platform.isTV) return responsiveSize(420, 320);
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

  const renderCard = useCallback(
    (
      cardContent: React.ReactElement<any>,
      onPress: () => void,
      key: string,
      isFirst?: boolean,
    ) => {
      if (Platform.isTV) {
        const focusable = (
          <SpatialNavigationFocusableView key={key} onSelect={onPress}>
            {({ isFocused }: { isFocused: boolean }) =>
              React.cloneElement(cardContent, { isFocused })
            }
          </SpatialNavigationFocusableView>
        );
        if (isFirst) {
          return <DefaultFocus key={key}>{focusable}</DefaultFocus>;
        }
        return focusable;
      }
      return (
        <Pressable key={key} onPress={onPress}>
          {cardContent}
        </Pressable>
      );
    },
    [],
  );

  // Build sections
  const sections: Array<{ title: string; key: string; cards: Array<{ key: string; content: React.ReactElement; onPress: () => void }>; customRender?: React.ReactNode }> = [];

  // Personal section
  const personalCards: typeof sections[0]['cards'] = [];
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
    sections.push({ title: 'Personal', key: 'personal', cards: personalCards });
  }

  // Trending section
  sections.push({
    title: 'Trending',
    key: 'trending',
    cards: [
      {
        key: 'trending-movies',
        content: <ListCard variant="gradient" title="Trending Movies" iconName="trending-up" tintColor="rgba(59,130,246,0.12)" aspectRatio={16 / 6} />,
        onPress: () => navigateToShelf('trending-movies'),
      },
      {
        key: 'trending-tv',
        content: <ListCard variant="gradient" title="Trending Shows" iconName="trending-up" tintColor="rgba(59,130,246,0.12)" aspectRatio={16 / 6} />,
        onPress: () => navigateToShelf('trending-tv'),
      },
    ],
  });

  // Custom lists
  if (customShelves.length > 0) {
    sections.push({
      title: 'Your Lists',
      key: 'custom-lists',
      cards: customShelves.map((shelf) => ({
        key: `custom-${shelf.id}`,
        content: <ListCard variant="gradient" title={shelf.name} iconName="list" tintColor="rgba(100,116,139,0.12)" aspectRatio={16 / 6} />,
        onPress: () => navigateToShelf(shelf.id),
      })),
    });
  }

  // Browse by Genre — Movies
  const makeGenreCards = (genres: GenreConfig[], mediaType: string) =>
    genres.map((genre) => ({
      key: `genre-${mediaType}-${genre.id}`,
      content: <ListCard variant="gradient" title={genre.name} iconName={genre.icon} iconFamily={genre.iconFamily} tintColor={genre.tintColor} aspectRatio={16 / 7} />,
      onPress: () => navigateToShelf(`genre-${genre.id}-${mediaType}`, { genreName: genre.name }),
    }));

  sections.push({
    title: 'Movie Genres',
    key: 'movie-genres',
    cards: makeGenreCards(MOVIE_GENRES, 'movie'),
  });

  // Browse by Genre — TV
  sections.push({
    title: 'TV Show Genres',
    key: 'tv-genres',
    cards: makeGenreCards(TV_GENRES, 'tv'),
  });

  // AI-powered curated list (only when Gemini key is configured)
  if (hasGeminiKey) {
    sections.push({
      title: 'Recommended For You',
      key: 'gemini-recs',
      cards: [{
        key: 'gemini-recs',
        content: <ListCard variant="gradient" title="Recommended For You" subtitle="Personalized by AI" iconName="sparkles" tintColor="rgba(139,92,246,0.12)" aspectRatio={16 / 4} />,
        onPress: () => navigateToShelf('gemini-recs'),
      }],
    });
  }

  // Per-title "Because you watched" cards (uses Gemma when key set, TMDB otherwise)
  if (recommendationSeeds.length > 0) {
    sections.push({
      title: 'Because You Watched',
      key: 'recommendations',
      cards: recommendationSeeds.map((seed) => ({
        key: `similar-${seed.tmdbId}`,
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
      customRender: (
        <View style={styles.section}>
          <Text style={styles.sectionTitle}>Ask AI</Text>
          <TextInput
            style={styles.aiSearchInput}
            placeholder={'Ask for recommendations... e.g. "feel-good comedies from the 90s"'}
            placeholderTextColor={theme.colors.text.tertiary}
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
      ),
    });
  }

  // Seasonal
  if (seasonalLists.length > 0) {
    sections.push({
      title: 'Seasonal',
      key: 'seasonal',
      cards: seasonalLists.map((list) => ({
        key: `seasonal-${list.id}`,
        content: <ListCard variant="gradient" title={list.name} iconName={list.icon} iconFamily={list.iconFamily} tintColor={list.tintColor} aspectRatio={16 / 6} />,
        onPress: () => navigateToShelf(`seasonal-${list.id}`),
      })),
    });
  }

  const content = (
    <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
      <View style={styles.container}>
        {/* Page title */}
        <View style={styles.titleRow}>
          <Text style={styles.title}>Lists</Text>
        </View>

        {/* Sections */}
        {Platform.isTV ? (
          <SpatialNavigationNode orientation="vertical">
            {sections.map((section, sIdx) =>
              section.customRender ? (
                <React.Fragment key={section.key}>{section.customRender}</React.Fragment>
              ) : (
              <CardSection key={section.key} title={section.title} theme={theme} styles={styles}>
                {section.cards.map((card, cIdx) => {
                  const isGenre = section.key.includes('genres');
                  const isWide = section.key === 'gemini-recs';
                  const width = isWide ? wideCardWidth : isGenre ? genreCardWidth : cardWidth;
                  return (
                    <View key={card.key} style={{ width }}>
                      {renderCard(card.content, card.onPress, card.key, sIdx === 0 && cIdx === 0)}
                    </View>
                  );
                })}
              </CardSection>
              )
            )}
          </SpatialNavigationNode>
        ) : (
          <KeyboardAwareScrollView
            showsVerticalScrollIndicator={false}
            extraScrollHeight={20}
            enableOnAndroid
            keyboardShouldPersistTaps="handled"
          >
            {sections.map((section) =>
              section.customRender ? (
                <React.Fragment key={section.key}>{section.customRender}</React.Fragment>
              ) : (
                <CardSection key={section.key} title={section.title} theme={theme} styles={styles}>
                  {section.cards.map((card) => {
                    const isGenre = section.key.includes('genres');
                    const isWide = section.key === 'gemini-recs';
                    const width = isWide ? wideCardWidth : isGenre ? genreCardWidth : cardWidth;
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
    titleRow: {
      marginBottom: theme.spacing.lg,
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    aiSearchInput: {
      backgroundColor: 'rgba(255,255,255,0.08)',
      borderRadius: 10,
      paddingHorizontal: theme.spacing.md,
      paddingVertical: Platform.isTV ? responsiveSize(12, 10) : 12,
      color: theme.colors.text.primary,
      fontSize: Platform.isTV ? responsiveSize(16, 14) : 15,
      borderWidth: 1,
      borderColor: 'rgba(255,255,255,0.12)',
    },
    surpriseButton: {
      backgroundColor: 'rgba(255,255,255,0.1)',
      borderRadius: 10,
      paddingHorizontal: Platform.isTV ? responsiveSize(20, 16) : 16,
      paddingVertical: Platform.isTV ? responsiveSize(12, 10) : 12,
      borderWidth: 1,
      borderColor: 'rgba(255,255,255,0.15)',
    },
    surpriseButtonLoading: {
      opacity: 0.5,
    },
    surpriseButtonText: {
      color: theme.colors.text.primary,
      fontSize: Platform.isTV ? responsiveSize(16, 14) : 15,
      fontWeight: '600',
    },
    surpriseRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 10,
      marginTop: 12,
    },
    decadeChips: {
      flexDirection: 'row',
      gap: 6,
      paddingRight: theme.spacing.xl,
    },
    decadeRow: {
      marginTop: 8,
    },
    decadeChip: {
      paddingHorizontal: 14,
      paddingVertical: 6,
      borderRadius: 16,
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
      fontSize: 13,
    },
    decadeChipTextSelected: {
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    section: {
      marginBottom: Platform.isTV ? theme.spacing.xl : theme.spacing.lg,
    },
    sectionTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.md,
    },
    cardRow: {
      flexDirection: 'row',
      gap: Platform.isTV ? responsiveSize(16, 12) : 12,
      paddingRight: theme.spacing.xl,
    },
  });
