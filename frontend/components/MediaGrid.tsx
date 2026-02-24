import React, { useCallback, useMemo, useRef, forwardRef, useImperativeHandle } from 'react';

import {
  ActivityIndicator,
  FlatList,
  Image as RNImage,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { Image as CachedImage } from './Image';
import { DefaultFocus, SpatialNavigationNode } from '@/services/tv-navigation';
import { Title } from '../services/api';
import { useResponsiveColumns } from '../hooks/useResponsiveColumns';
import { useTVDimensions } from '../hooks/useTVDimensions';
import type { ColumnOverride } from '../hooks/useResponsiveColumns';
import { useTheme } from '../theme';
import type { NovaTheme } from '../theme';
import { isAndroidTablet, isAndroidTV, isTablet } from '../theme/tokens/tvScale';
import MediaItem, { getMovieReleaseIcon } from './MediaItem';
import { LinearGradient } from 'expo-linear-gradient';
import { Ionicons, MaterialCommunityIcons } from '@expo/vector-icons';

type DisplayTitle = Title & { uniqueKey?: string; collagePosters?: string[]; cardSubtitle?: string };

// Imperative handle for MediaGrid - allows parent to control scrolling
export interface MediaGridHandle {
  scrollToTop: () => void;
}

interface MediaGridProps {
  title: string;
  items: DisplayTitle[];
  loading?: boolean;
  error?: string | null;
  onItemPress?: (title: DisplayTitle) => void;
  onItemLongPress?: (title: DisplayTitle) => void;
  numColumns?: ColumnOverride;
  layout?: 'carousel' | 'grid'; // carousel = horizontal scroll, grid = vertical 2-column grid
  defaultFocusFirstItem?: boolean; // when entering from above, focus first item (TV)
  disableFocusScroll?: boolean; // disable programmatic scroll on focus (TV)
  badgeVisibility?: string[]; // Which badges to show on MediaItem cards
  watchStateIconStyle?: 'colored' | 'white'; // Icon color style for watch state badges
  emptyMessage?: string; // Custom message when no items
  onEndReached?: () => void; // Called when user scrolls near the end (for infinite scroll)
  loadingMore?: boolean; // Show loading indicator at the bottom for progressive loading
  hasMoreItems?: boolean; // Whether there are more items to load
  onItemFocus?: (index: number) => void; // Called when an item receives focus (TV) - index is the item's position in the list
  ListHeaderComponent?: React.ReactElement | null; // Component to render above the list (scrolls with content)
  listKey?: string; // Key suffix to force spatial navigation recalculation when list order changes
  cardLayout?: 'portrait' | 'landscape'; // Card layout style (default: portrait, landscape for continue watching)
}

const createStyles = (theme: NovaTheme, screenWidth?: number, parentPadding: number = 0) => {
  const isCompact = theme.breakpoint === 'compact';

  // Calculate card dimensions for mobile grid layout (matching search page)
  // Use 4 columns on wide mobile screens (foldables, tablets), 2 on phones
  // Note: This is the default - actual columns may be overridden by numColumns prop
  const isWideCompact = isCompact && screenWidth && screenWidth >= 600;
  const mobileColumnsCount = isWideCompact ? 4 : 2;
  const mobileGap = theme.spacing.md;
  // Account for parent container padding (watchlist page has theme.spacing.xl)
  const totalPadding = parentPadding > 0 ? parentPadding : 0;
  const mobileAvailableWidth = screenWidth ? screenWidth - totalPadding * 2 : 0;
  const mobileTotalGapWidth = mobileGap * (mobileColumnsCount - 1);
  // Card width includes border (React Native width includes border by default)
  const mobileCardWidth =
    screenWidth && mobileAvailableWidth > 0
      ? Math.floor((mobileAvailableWidth - mobileTotalGapWidth) / mobileColumnsCount)
      : 160;
  const mobileCardHeight = Math.round(mobileCardWidth * (3 / 2)); // Portrait aspect ratio

  return StyleSheet.create({
    container: {
      flex: 1,
      paddingHorizontal: theme.spacing.xl,
    },
    containerCompact: {
      paddingHorizontal: theme.spacing.none,
      paddingLeft: theme.spacing.md,
      paddingRight: theme.spacing.none,
    },
    containerCompactGrid: {
      paddingHorizontal: 0, // No extra padding - parent container handles it
    },
    title: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.lg,
      marginTop: theme.spacing.lg,
    },
    scrollView: {
      flex: 1,
    },
    grid: {
      paddingBottom: theme.spacing['2xl'],
    },
    gridInner: {
      flexDirection: 'row',
      flexWrap: 'wrap',
    },
    rowContainer: {
      marginBottom: theme.spacing['2xl'],
    },
    gridRow: {
      flexDirection: 'row',
      alignItems: 'flex-start',
      flexWrap: 'nowrap',
    },
    itemWrapper: {
      paddingBottom: theme.spacing['2xl'],
    },
    carouselContent: {
      flexDirection: 'row',
      alignItems: 'flex-start',
    },
    carouselContentCompact: {
      paddingLeft: theme.spacing.none,
      paddingRight: theme.spacing.md,
    },
    carouselItem: {
      width: 160,
    },
    // Mobile grid styles (matching search page)
    mobileGridContainer: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      justifyContent: 'flex-start',
      marginHorizontal: -mobileGap / 2, // Negative margin to offset card margins
    },
    mobileCard: {
      width: mobileCardWidth,
      height: mobileCardHeight,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      borderWidth: 3,
      borderColor: 'transparent',
      marginHorizontal: mobileGap / 2, // Half gap on each side
      marginBottom: mobileGap, // Full gap on bottom
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
      top: theme.spacing.xs,
      right: theme.spacing.xs,
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: 2,
      borderRadius: theme.radius.sm,
      borderWidth: 1,
      borderColor: theme.colors.accent.primary,
    },
    badgeText: {
      ...theme.typography.caption.sm,
      color: theme.colors.accent.primary,
      fontWeight: '700',
      fontSize: 10,
      letterSpacing: 0.5,
    },
    // Release status badge (top-left)
    releaseStatusBadge: {
      position: 'absolute',
      top: theme.spacing.xs,
      left: theme.spacing.xs,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
    },
    cardTextContainer: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      padding: theme.spacing.sm,
      gap: theme.spacing.xs,
      alignItems: 'center',
      justifyContent: 'flex-end',
      minHeight: '40%',
    },
    cardTextGradient: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
    },
    cardTitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      textAlign: 'center',
      zIndex: 1,
      fontWeight: '600',
    },
    cardYear: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      textAlign: 'center',
      zIndex: 1,
    },
    cardSubtitle: {
      ...theme.typography.caption.sm,
      color: theme.colors.accent.primary,
      textAlign: 'center',
      zIndex: 1,
      fontWeight: '600',
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
    },
  });
};

// Memoized wrapper that gives each MediaItem stable callback references.
// Without this, inline `() => onItemPress?.(item)` creates a new function
// every parent render, defeating MediaItem's memo() across 100+ items.
interface MediaGridItemProps {
  item: DisplayTitle;
  onItemPressRef: React.RefObject<((item: DisplayTitle) => void) | undefined>;
  onItemLongPress?: (item: DisplayTitle) => void;
  onFocus?: () => void;
  badgeVisibility?: string[];
  watchStateIconStyle?: 'colored' | 'white';
  style?: any;
}

const MediaGridItem = React.memo(function MediaGridItem({
  item,
  onItemPressRef,
  onItemLongPress,
  onFocus,
  badgeVisibility,
  watchStateIconStyle,
  style,
}: MediaGridItemProps) {
  const handlePress = useCallback(() => {
    onItemPressRef.current?.(item);
  }, [item, onItemPressRef]);

  const handleLongPress = useMemo(
    () => (onItemLongPress ? () => onItemLongPress(item) : undefined),
    [item, onItemLongPress],
  );

  return (
    <MediaItem
      title={item}
      onPress={handlePress}
      onLongPress={handleLongPress}
      onFocus={onFocus}
      badgeVisibility={badgeVisibility}
      watchStateIconStyle={watchStateIconStyle}
      style={style}
    />
  );
});

const MediaGrid = forwardRef<MediaGridHandle, MediaGridProps>(function MediaGrid({
    title,
    items,
    loading = false,
    error = null,
    onItemPress,
    onItemLongPress,
    numColumns,
    layout = 'carousel', // Default to carousel for backwards compatibility
    defaultFocusFirstItem = false,
    disableFocusScroll = false,
    badgeVisibility,
    watchStateIconStyle,
    emptyMessage,
    onEndReached,
    loadingMore = false,
    hasMoreItems = false,
    onItemFocus,
    ListHeaderComponent,
    listKey,
    cardLayout = 'portrait',
  }, ref) {
    const theme = useTheme();
    const { width: screenWidth, height: screenHeight } = useTVDimensions();
    const isCompact = theme.breakpoint === 'compact';

    // Stable ref for onItemPress — lets MediaGridItem callbacks survive parent re-renders
    const onItemPressRef = useRef(onItemPress);
    onItemPressRef.current = onItemPress;

    // Tablet-specific carousel card sizing
    const isLandscape = screenWidth > screenHeight;
    // Tablets get larger shelf cards: 200px portrait, 240px landscape
    const tabletCarouselCardWidth = isLandscape ? 240 : 200;
    const carouselCardWidth = isTablet ? tabletCarouselCardWidth : 160;

    // For grid layout on mobile, account for parent container padding (watchlist has theme.spacing.xl)
    const parentPadding = isCompact && layout === 'grid' ? theme.spacing.xl : 0;
    const styles = useMemo(() => createStyles(theme, screenWidth, parentPadding), [theme, screenWidth, parentPadding]);

    const { columns, gap } = useResponsiveColumns(numColumns);
    const halfGap = gap / 2;

    // tvOS scrolling helpers (row-based), mirrors Search/Live screen approach
    const rowRefs = useRef<{ [key: string]: View | null }>({});
    const mainScrollViewRef = useRef<any>(null);

    // Expose scrollToTop to parent via imperative handle
    useImperativeHandle(ref, () => ({
      scrollToTop: () => {
        if (mainScrollViewRef.current) {
          mainScrollViewRef.current.scrollTo({ y: 0, animated: true });
        }
      },
    }), []);

    // Scroll to row when it receives focus (for TV navigation)
    const scrollToRow = useCallback(
      (rowKey: string) => {
        if (!Platform.isTV || !mainScrollViewRef.current || disableFocusScroll) {
          return;
        }

        const rowRef = rowRefs.current[rowKey];
        if (!rowRef) {
          return;
        }

        const scrollView = mainScrollViewRef.current;
        rowRef.measureLayout(
          scrollView as any,
          (_left, top) => {
            const targetY = Math.max(0, top - 20);
            scrollView?.scrollTo({ y: targetY, animated: true });
          },
          () => {
            // silently ignore failures
          },
        );
      },
      [disableFocusScroll],
    );

    const keyExtractor = (item: DisplayTitle, index: number) => {
      if (item.uniqueKey) {
        return item.uniqueKey;
      }
      if (item.id) {
        return `${item.id}-${index}`;
      }
      const fallback = item.name || 'item';
      return `${fallback}-${index}`;
    };

    // Handle scroll to detect when user is near the end (defined at component level to follow hooks rules)
    const handleScroll = useCallback(
      (event: any) => {
        if (!onEndReached || loadingMore || !hasMoreItems) return;

        const { layoutMeasurement, contentOffset, contentSize } = event.nativeEvent;
        const paddingToBottom = 600; // Trigger when 600px from bottom (about 2 rows early)
        const isNearBottom = layoutMeasurement.height + contentOffset.y >= contentSize.height - paddingToBottom;

        if (isNearBottom) {
          onEndReached();
        }
      },
      [onEndReached, loadingMore, hasMoreItems],
    );

    // Loading more indicator component
    const LoadingMoreIndicator = useMemo(
      () =>
        loadingMore ? (
          <View style={{ paddingVertical: theme.spacing.xl, alignItems: 'center' }}>
            <ActivityIndicator size="small" color={theme.colors.accent.primary} />
            <Text style={[styles.loadingText, { marginTop: theme.spacing.sm }]}>Loading more...</Text>
          </View>
        ) : hasMoreItems ? (
          <View style={{ paddingVertical: theme.spacing.lg, alignItems: 'center' }}>
            <Text style={styles.emptyText}>Scroll for more</Text>
          </View>
        ) : null,
      [
        loadingMore,
        hasMoreItems,
        theme.spacing.xl,
        theme.spacing.lg,
        theme.spacing.sm,
        theme.colors.accent.primary,
        styles.loadingText,
        styles.emptyText,
      ],
    );

    // Memoize row slicing so new array references aren't created every render,
    // which would trigger spatial navigation recalculation on TV.
    const rows = useMemo(() => {
      const result: DisplayTitle[][] = [];
      for (let i = 0; i < items.length; i += columns) {
        result.push(items.slice(i, i + columns));
      }
      return result;
    }, [items, columns]);

    const renderContent = () => {
      if (loading) {
        return (
          <View style={styles.loadingContainer}>
            <ActivityIndicator size="large" color={theme.colors.accent.primary} />
            <Text style={styles.loadingText}>Loading...</Text>
          </View>
        );
      }

      if (error) {
        return (
          <View style={styles.errorContainer}>
            <Text style={styles.errorText}>Error: {error}</Text>
          </View>
        );
      }

      if (!items || items.length === 0) {
        return (
          <View style={styles.emptyContainer}>
            <Text style={styles.emptyText}>{emptyMessage ?? `No ${title.toLowerCase()} found`}</Text>
          </View>
        );
      }

      // Use virtualized FlatList for compact devices AND Android tablets
      const useVirtualizedGrid = isCompact || isAndroidTablet;

      if (useVirtualizedGrid) {
        // Grid layout for mobile/tablet - virtualized for performance
        if (layout === 'grid') {
          const isAndroid = Platform.OS === 'android';
          const gridColumns = typeof numColumns === 'number' ? numColumns : 3;

          // Calculate dynamic card dimensions based on actual gridColumns
          const dynamicGap = theme.spacing.md;
          const containerPadding = theme.spacing.xl;
          const availableWidth = (screenWidth || 0) - containerPadding * 2;
          const totalGapWidth = dynamicGap * (gridColumns - 1);
          const dynamicCardWidth = availableWidth > 0 ? Math.floor((availableWidth - totalGapWidth) / gridColumns) : 160;
          const dynamicCardHeight = Math.round(dynamicCardWidth * (3 / 2));

          const renderGridItem = ({ item, index }: { item: DisplayTitle; index: number }) => {
            const isExploreCard =
              item.mediaType === 'explore' && item.collagePosters && item.collagePosters.length >= 4;
            const yearDisplay = item.mediaType === 'explore' && item.year ? `+${item.year} More` : item.year;

            return (
              <Pressable
                key={keyExtractor(item, index)}
                style={[styles.mobileCard, { width: dynamicCardWidth, height: dynamicCardHeight }]}
                onPress={() => onItemPress?.(item)}
                android_ripple={{ color: theme.colors.accent.primary + '30' }}>
                <View style={styles.cardImageContainer}>
                  {isExploreCard ? (
                    <View style={{ flexDirection: 'row', flexWrap: 'wrap', width: '100%', height: '100%' }}>
                      {item.collagePosters!.slice(0, 4).map((poster, i) => (
                        <RNImage
                          key={`collage-${i}`}
                          source={{ uri: poster }}
                          style={{ width: '50%', height: '50%' }}
                          resizeMode="cover"
                        />
                      ))}
                    </View>
                  ) : item.poster?.url ? (
                    <RNImage source={{ uri: item.poster.url }} style={styles.cardImage} resizeMode="cover" />
                  ) : (
                    <View style={styles.placeholder}>
                      <Text style={styles.placeholderImageText}>No Image</Text>
                    </View>
                  )}
                  {/* Release status badge (top-left) for movies - check badgeVisibility */}
                  {item.mediaType === 'movie' &&
                    badgeVisibility?.includes('releaseStatus') &&
                    (() => {
                      const releaseIcon = getMovieReleaseIcon(item);
                      return releaseIcon ? (
                        <View style={styles.releaseStatusBadge}>
                          <MaterialCommunityIcons name={releaseIcon.name} size={14} color={watchStateIconStyle === 'white' ? '#ffffff' : releaseIcon.color} />
                        </View>
                      ) : null;
                    })()}
                  {/* Media type badge (top-right) */}
                  {item.mediaType && item.mediaType !== 'explore' && (
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
                    {item.cardSubtitle ? <Text style={styles.cardSubtitle}>{item.cardSubtitle}</Text> : null}
                    <Text style={styles.cardTitle} numberOfLines={2}>
                      {item.name}
                    </Text>
                    {yearDisplay ? <Text style={styles.cardYear}>{yearDisplay}</Text> : null}
                  </View>
                </View>
              </Pressable>
            );
          };

          return (
            <FlatList
              key={`grid-${gridColumns}`}
              style={styles.scrollView}
              contentContainerStyle={styles.grid}
              showsVerticalScrollIndicator={false}
              onScroll={handleScroll}
              scrollEventThrottle={100}
              data={items}
              extraData={badgeVisibility}
              keyExtractor={keyExtractor}
              numColumns={gridColumns}
              renderItem={renderGridItem}
              initialNumToRender={isAndroid ? 9 : 12}
              maxToRenderPerBatch={isAndroid ? 6 : 9}
              windowSize={isAndroid ? 3 : 5}
              removeClippedSubviews={isAndroid}
              onEndReached={onEndReached}
              onEndReachedThreshold={1.5}
              ListHeaderComponent={ListHeaderComponent}
              ListFooterComponent={LoadingMoreIndicator}
            />
          );
        }

        // Carousel layout for mobile (horizontal scroll)
        // Android: aggressive virtualization to reduce memory pressure
        const isAndroid = Platform.OS === 'android';
        const isLandscapeLayout = cardLayout === 'landscape';

        // Landscape cards are wider (16:9 ratio), portrait are taller (2:3 ratio)
        const landscapeCardWidth = Math.round(carouselCardWidth * 1.6); // Wider for landscape
        const landscapeCardHeight = Math.round(landscapeCardWidth * (9 / 16)); // 16:9 aspect ratio
        const portraitCardHeight = Math.round(carouselCardWidth * 1.5); // 2:3 aspect ratio

        const actualCardWidth = isLandscapeLayout ? landscapeCardWidth : carouselCardWidth;
        const actualCardHeight = isLandscapeLayout ? landscapeCardHeight : portraitCardHeight;

        return (
          <FlatList
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={[styles.carouselContent, styles.carouselContentCompact]}
            data={items}
            extraData={[badgeVisibility, carouselCardWidth, cardLayout]}
            keyExtractor={keyExtractor}
            initialNumToRender={isAndroid ? 3 : 5}
            maxToRenderPerBatch={isAndroid ? 2 : 5}
            windowSize={isAndroid ? 2 : 3}
            removeClippedSubviews={isAndroid}
            renderItem={({ item, index }) => {
              // For landscape cards, render custom card with progress bar
              if (isLandscapeLayout) {
                const isLandscapeExploreCard =
                  item.mediaType === 'explore' && item.collagePosters && item.collagePosters.length >= 4;

                if (isLandscapeExploreCard) {
                  const yearDisplay = item.year ? `+${item.year} More` : undefined;
                  return (
                    <Pressable
                      style={{
                        width: actualCardWidth,
                        height: actualCardHeight,
                        marginRight: index === items.length - 1 ? 0 : gap,
                        borderRadius: theme.radius.md,
                        overflow: 'hidden',
                        backgroundColor: theme.colors.background.surface,
                      }}
                      onPress={() => onItemPress?.(item)}>
                      <View style={{ flexDirection: 'row', flexWrap: 'wrap', width: '100%', height: '100%' }}>
                        {item.collagePosters!.slice(0, 4).map((poster: string, i: number) => (
                          <RNImage
                            key={`collage-${i}`}
                            source={{ uri: poster }}
                            style={{ width: '50%', height: '50%' }}
                            resizeMode="cover"
                          />
                        ))}
                      </View>
                      <View
                        style={{
                          position: 'absolute',
                          bottom: 0,
                          left: 0,
                          right: 0,
                          paddingHorizontal: theme.spacing.sm,
                          paddingTop: theme.spacing.lg,
                          paddingBottom: theme.spacing.sm,
                          zIndex: 3,
                        }}>
                        <LinearGradient
                          pointerEvents="none"
                          colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
                          locations={[0, 0.5, 1]}
                          start={{ x: 0.5, y: 0 }}
                          end={{ x: 0.5, y: 1 }}
                          style={{ position: 'absolute', top: 0, left: 0, right: 0, bottom: 0 }}
                        />
                        <Text
                          style={{
                            ...theme.typography.body.sm,
                            color: theme.colors.text.primary,
                            fontWeight: '600',
                            zIndex: 1,
                          }}
                          numberOfLines={1}>
                          {item.name}
                        </Text>
                        {yearDisplay ? (
                          <Text
                            style={{
                              ...theme.typography.caption.sm,
                              color: theme.colors.text.secondary,
                              zIndex: 1,
                            }}>
                            {yearDisplay}
                          </Text>
                        ) : null}
                      </View>
                    </Pressable>
                  );
                }

                const percentWatched = (item as any).percentWatched as number | undefined;
                const itemIsUnreleased = (item as any).isUnreleased as string | undefined;
                const showProgressBar = percentWatched !== undefined && percentWatched >= 5 && percentWatched < 100;
                const imageUrl = item.backdrop?.url || item.poster?.url;

                return (
                  <Pressable
                    style={[
                      {
                        width: actualCardWidth,
                        height: actualCardHeight,
                        marginRight: index === items.length - 1 ? 0 : gap,
                        borderRadius: theme.radius.md,
                        overflow: 'hidden',
                        backgroundColor: theme.colors.background.surface,
                      },
                    ]}
                    onPress={() => onItemPress?.(item)}
                    onLongPress={onItemLongPress ? () => onItemLongPress(item) : undefined}>
                    {imageUrl ? (
                      <CachedImage
                        source={imageUrl}
                        style={{ width: '100%', height: '100%', position: 'absolute' }}
                        contentFit="cover"
                        transition={0}
                      />
                    ) : (
                      <View style={{ flex: 1, backgroundColor: theme.colors.background.elevated }} />
                    )}
                    {/* Desaturation overlay for unreleased episodes */}
                    {itemIsUnreleased && (
                      <View
                        style={{
                          position: 'absolute',
                          top: 0,
                          left: 0,
                          right: 0,
                          bottom: 0,
                          backgroundColor: 'rgba(0, 0, 0, 0.55)',
                          zIndex: 2,
                        }}
                        pointerEvents="none"
                      />
                    )}
                    {/* Coming soon badge (top-right) */}
                    {itemIsUnreleased && (
                      <View
                        style={{
                          position: 'absolute',
                          top: theme.spacing.xs,
                          right: theme.spacing.xs,
                          flexDirection: 'row',
                          alignItems: 'center',
                          gap: 3,
                          paddingHorizontal: 6,
                          paddingVertical: 3,
                          borderRadius: 12,
                          backgroundColor: theme.colors.status.warning,
                          zIndex: 5,
                        }}>
                        <Ionicons name="calendar-outline" size={10} color="#000000" />
                        <Text style={{ fontSize: 9, fontWeight: '700', color: '#000000' }}>{itemIsUnreleased}</Text>
                      </View>
                    )}
                    {/* Text container with gradient */}
                    <View
                      style={{
                        position: 'absolute',
                        bottom: 0,
                        left: 0,
                        right: 0,
                        paddingHorizontal: theme.spacing.sm,
                        paddingTop: theme.spacing.lg,
                        paddingBottom: showProgressBar ? theme.spacing.sm + 4 : theme.spacing.sm,
                        zIndex: 3, // Above unreleased overlay (zIndex 2)
                      }}>
                      <LinearGradient
                        pointerEvents="none"
                        colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
                        locations={[0, 0.5, 1]}
                        start={{ x: 0.5, y: 0 }}
                        end={{ x: 0.5, y: 1 }}
                        style={{ position: 'absolute', top: 0, left: 0, right: 0, bottom: 0 }}
                      />
                      <Text
                        style={{
                          ...theme.typography.body.sm,
                          color: theme.colors.text.primary,
                          fontWeight: '600',
                          zIndex: 1,
                        }}
                        numberOfLines={1}>
                        {item.name}
                      </Text>
                      {item.year ? (
                        <Text
                          style={{
                            ...theme.typography.caption.sm,
                            color: theme.colors.text.secondary,
                            zIndex: 1,
                          }}>
                          {item.year}
                        </Text>
                      ) : null}
                    </View>
                    {/* Progress bar at bottom */}
                    {showProgressBar && (
                      <View
                        style={{
                          position: 'absolute',
                          bottom: 0,
                          left: 0,
                          right: 0,
                          height: isAndroidTV ? 1.5 : 4,
                          zIndex: 3,
                        }}>
                        <View
                          style={{
                            position: 'absolute',
                            top: 0,
                            left: 0,
                            right: 0,
                            bottom: 0,
                            backgroundColor: 'rgba(255, 255, 255, 0.4)',
                          }}
                        />
                        <View
                          style={{
                            position: 'absolute',
                            top: 0,
                            left: 0,
                            bottom: 0,
                            width: `${percentWatched}%`,
                            backgroundColor: 'rgba(255, 255, 255, 0.9)',
                          }}
                        />
                      </View>
                    )}
                  </Pressable>
                );
              }

              // Portrait cards use MediaGridItem for stable callbacks
              return (
                <View
                  style={[
                    styles.carouselItem,
                    {
                      width: actualCardWidth,
                      marginRight: index === items.length - 1 ? 0 : gap,
                    },
                  ]}>
                  <MediaGridItem
                    item={item}
                    onItemPressRef={onItemPressRef}
                    onItemLongPress={onItemLongPress}
                    badgeVisibility={badgeVisibility}
                    watchStateIconStyle={watchStateIconStyle}
                    style={{ width: actualCardWidth, height: actualCardHeight }}
                  />
                </View>
              );
            }}
          />
        );
      }

      // Key changes when row count or listKey changes, forcing spatial navigation to recalculate layout
      const gridKey = `grid-${rows.length}${listKey ? `-${listKey}` : ''}`;

      // Standard mode: Use SpatialNavigationNode for focus management
      const isAndroidDevice = Platform.OS === 'android';
      return (
        <ScrollView
          ref={mainScrollViewRef}
          style={styles.scrollView}
          contentContainerStyle={styles.grid}
          bounces={false}
          showsVerticalScrollIndicator={false}
          // TV: disable native scroll - use programmatic scrolling only to prevent
          // native focus from moving the grid when drawer is open
          scrollEnabled={!Platform.isTV}
          contentInsetAdjustmentBehavior="never"
          automaticallyAdjustContentInsets={false}
          removeClippedSubviews={Platform.isTV || isAndroidDevice}
          scrollEventThrottle={16}
          // TV: prevent native focus-based scrolling and gestures
          focusable={false}
          // @ts-ignore - TV-specific prop
          isTVSelectable={false}
          // @ts-ignore - TV-specific prop
          tvRemoveGestureEnabled={Platform.isTV}>
          <SpatialNavigationNode key={gridKey} orientation="vertical" alignInGrid>
            {ListHeaderComponent}
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
                    <View style={[styles.gridRow, { marginHorizontal: -halfGap }]}>
                      {row.map((item, colIndex) => {
                        const index = rowIndex * columns + colIndex;
                        const content = (
                          <MediaGridItem
                            item={item}
                            onItemPressRef={onItemPressRef}
                            onFocus={() => scrollToRow(rowKey)}
                            badgeVisibility={badgeVisibility}
                            watchStateIconStyle={watchStateIconStyle}
                          />
                        );
                        return (
                          <View
                            key={keyExtractor(item, index)}
                            style={[styles.itemWrapper, { width: `${100 / columns}%`, paddingHorizontal: halfGap }]}>
                            {defaultFocusFirstItem && index === 0 ? <DefaultFocus>{content}</DefaultFocus> : content}
                          </View>
                        );
                      })}
                    </View>
                  </SpatialNavigationNode>
                </View>
              );
            })}
          </SpatialNavigationNode>
        </ScrollView>
      );
    };

    const containerStyle = [
      styles.container,
      isCompact && (layout === 'grid' ? styles.containerCompactGrid : styles.containerCompact),
    ];

    return (
      <View
        style={containerStyle}
        // @ts-ignore - TV-specific props to prevent native focus handling
        focusable={false}
        isTVSelectable={false}>
        {title ? <Text style={styles.title}>{title}</Text> : null}
        {renderContent()}
      </View>
    );
  });

// Wrap in memo with custom areEqual for performance
const MemoizedMediaGrid = React.memo(MediaGrid, (prevProps, nextProps) => {
  // Only re-render if props actually changed
  return (
    prevProps.title === nextProps.title &&
    prevProps.items === nextProps.items &&
    prevProps.loading === nextProps.loading &&
    prevProps.error === nextProps.error &&
    prevProps.numColumns === nextProps.numColumns &&
    prevProps.layout === nextProps.layout &&
    prevProps.defaultFocusFirstItem === nextProps.defaultFocusFirstItem &&
    prevProps.badgeVisibility === nextProps.badgeVisibility &&
    prevProps.watchStateIconStyle === nextProps.watchStateIconStyle &&
    prevProps.loadingMore === nextProps.loadingMore &&
    prevProps.hasMoreItems === nextProps.hasMoreItems &&
    prevProps.listKey === nextProps.listKey &&
    prevProps.cardLayout === nextProps.cardLayout
    // onItemPress and onEndReached are omitted - function reference changes are expected
  );
});

export default MemoizedMediaGrid;
