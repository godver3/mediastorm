import React, { useCallback, useMemo, useRef, useState, useEffect } from 'react';

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
import { isAndroidTablet } from '../theme/tokens/tvScale';
import MediaItem, { getMovieReleaseIcon } from './MediaItem';
import { LinearGradient } from 'expo-linear-gradient';
import { MaterialCommunityIcons } from '@expo/vector-icons';

type DisplayTitle = Title & { uniqueKey?: string; collagePosters?: string[] };

// Context for stable callbacks - avoids prop changes that defeat memoization
interface MediaGridHandlers {
  onItemPress: (itemId: string) => void;
  onItemLongPress: (itemId: string) => void;
  onRowFocus: (rowIndex: number) => void;
  onItemFocus: (index: number) => void;
}
const MediaGridHandlersContext = React.createContext<MediaGridHandlers | null>(null);

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
  emptyMessage?: string; // Custom message when no items
  useNativeFocus?: boolean; // Use native Pressable focus instead of SpatialNavigation (faster on Android TV)
  useMinimalCards?: boolean; // Use ultra-simple cards for performance testing
  minimalCardLevel?: number; // 0=minimal, 1=+image, 2=+gradient, 3=+text overlay
  onEndReached?: () => void; // Called when user scrolls near the end (for infinite scroll)
  loadingMore?: boolean; // Show loading indicator at the bottom for progressive loading
  hasMoreItems?: boolean; // Whether there are more items to load
  onItemFocus?: (index: number) => void; // Called when an item receives focus (TV) - index is the item's position in the list
}

// Static styles for MinimalCard - avoids object creation per render
const minimalCardStyles = {
  base: {
    flex: 1,
    aspectRatio: 2 / 3,
    backgroundColor: '#2a1245',
    borderRadius: 8,
    overflow: 'hidden' as const,
    borderWidth: 3,
    borderColor: 'transparent',
  },
  focused: {
    flex: 1,
    aspectRatio: 2 / 3,
    backgroundColor: '#2a1245',
    borderRadius: 8,
    overflow: 'hidden' as const,
    borderWidth: 3,
    borderColor: '#fff',
  },
  image: { width: '100%' as const, height: '100%' as const, position: 'absolute' as const },
  gradient: { position: 'absolute' as const, bottom: 0, left: 0, right: 0, height: '50%' as const },
  textContainer: {
    position: 'absolute' as const,
    bottom: 0,
    left: 0,
    right: 0,
    padding: 8,
    alignItems: 'center' as const,
  },
  title: { color: '#fff', fontSize: 14, fontWeight: '600' as const, textAlign: 'center' as const },
  year: { color: 'rgba(255,255,255,0.7)', fontSize: 12, marginTop: 2 },
};

// Optimized card for native focus mode - uses context for callbacks
const MinimalCard = React.memo(
  function MinimalCard({
    item,
    rowIndex,
    itemIndex,
    autoFocus,
  }: {
    item: DisplayTitle;
    rowIndex: number;
    itemIndex: number;
    autoFocus?: boolean;
  }) {
    const handlers = React.useContext(MediaGridHandlersContext);

    const handlePress = useCallback(() => {
      handlers?.onItemPress(item.id);
    }, [handlers, item.id]);

    const handleLongPress = useCallback(() => {
      handlers?.onItemLongPress(item.id);
    }, [handlers, item.id]);

    const handleFocus = useCallback(() => {
      handlers?.onRowFocus(rowIndex);
      handlers?.onItemFocus(itemIndex);
    }, [handlers, rowIndex, itemIndex]);

    return (
      <Pressable
        onPress={handlePress}
        onLongPress={handleLongPress}
        onFocus={handleFocus}
        hasTVPreferredFocus={autoFocus}
        style={({ focused }) => (focused ? minimalCardStyles.focused : minimalCardStyles.base)}>
        {item.poster?.url ? (
          <CachedImage
            source={item.poster.url}
            style={minimalCardStyles.image}
            contentFit="cover"
            transition={0}
            priority="low"
            recyclingKey={item.id}
          />
        ) : null}

        <LinearGradient
          colors={['transparent', 'rgba(0,0,0,0.8)']}
          style={minimalCardStyles.gradient}
          pointerEvents="none"
        />

        <View style={minimalCardStyles.textContainer}>
          <Text style={minimalCardStyles.title} numberOfLines={2}>
            {item.name}
          </Text>
          {item.year ? <Text style={minimalCardStyles.year}>{item.year}</Text> : null}
        </View>
      </Pressable>
    );
  },
  (prevProps, nextProps) =>
    prevProps.item.id === nextProps.item.id &&
    prevProps.item.name === nextProps.item.name &&
    prevProps.item.poster?.url === nextProps.item.poster?.url &&
    prevProps.item.year === nextProps.item.year &&
    prevProps.rowIndex === nextProps.rowIndex &&
    prevProps.itemIndex === nextProps.itemIndex &&
    prevProps.autoFocus === nextProps.autoFocus,
);

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

const MediaGrid = React.memo(
  function MediaGrid({
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
    emptyMessage,
    useNativeFocus = false,
    useMinimalCards = false,
    minimalCardLevel: _minimalCardLevel = 0,
    onEndReached,
    loadingMore = false,
    hasMoreItems = false,
    onItemFocus,
  }: MediaGridProps) {
    const theme = useTheme();
    const { width: screenWidth } = useTVDimensions();
    const isCompact = theme.breakpoint === 'compact';

    // For grid layout on mobile, account for parent container padding (watchlist has theme.spacing.xl)
    const parentPadding = isCompact && layout === 'grid' ? theme.spacing.xl : 0;
    const styles = useMemo(() => createStyles(theme, screenWidth, parentPadding), [theme, screenWidth, parentPadding]);

    const { columns, gap } = useResponsiveColumns(numColumns);
    const halfGap = gap / 2;

    // tvOS scrolling helpers (row-based), mirrors Search/Live screen approach
    const rowRefs = useRef<{ [key: string]: View | null }>({});
    const mainScrollViewRef = useRef<any>(null);

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

    // Native focus mode: refs and scroll handling
    const nativeScrollViewRef = useRef<ScrollView>(null);
    const nativeRowRefs = useRef<{ [key: string]: View | null }>({});
    const _nativeRowHeightsRef = useRef<{ [key: string]: number }>({});

    // Native focus scroll handler - keeps focused row at top
    // ScrollView naturally clamps to content bounds, so no manual maxScroll needed
    const scrollToRowNative = useCallback((rowIndex: number, _totalRows: number) => {
      if (!Platform.isTV || !nativeScrollViewRef.current) {
        return;
      }

      const rowKey = `native-row-${rowIndex}`;
      const rowRef = nativeRowRefs.current[rowKey];

      if (!rowRef) {
        return;
      }

      // Measure the row position and scroll to put it at top
      rowRef.measureLayout(
        nativeScrollViewRef.current as any,
        (_left, top) => {
          const topOffset = 20; // Small padding from top
          const targetY = Math.max(0, top - topOffset);

          nativeScrollViewRef.current?.scrollTo({ y: targetY, animated: true });
        },
        () => {
          // silently ignore failures
        },
      );
    }, []);

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

    // Progressive rendering for native focus mode
    const INITIAL_ROW_COUNT = 8;
    const LOAD_MORE_ROWS = 4;
    const [renderedRowCount, setRenderedRowCount] = useState(INITIAL_ROW_COUNT);

    // Reset row count when items change
    useEffect(() => {
      setRenderedRowCount(INITIAL_ROW_COUNT);
    }, [items.length]);

    // Item lookup map for O(1) access
    const itemMap = useMemo(() => {
      const map = new Map<string, DisplayTitle>();
      items.forEach((item) => map.set(item.id, item));
      return map;
    }, [items]);

    // Scroll to row ref for use in handlers
    const scrollToRowNativeRef = useRef(scrollToRowNative);
    scrollToRowNativeRef.current = scrollToRowNative;

    // Ref for onEndReached to use in gridHandlers without causing rerender
    const onEndReachedRef = useRef(onEndReached);
    onEndReachedRef.current = onEndReached;
    const hasMoreItemsRef = useRef(hasMoreItems);
    hasMoreItemsRef.current = hasMoreItems;
    const loadingMoreRef = useRef(loadingMore);
    loadingMoreRef.current = loadingMore;
    const onItemFocusRef = useRef(onItemFocus);
    onItemFocusRef.current = onItemFocus;

    // Stable handlers via context
    const gridHandlers = useMemo<MediaGridHandlers>(
      () => ({
        onItemPress: (itemId: string) => {
          const item = itemMap.get(itemId);
          if (item) onItemPress?.(item);
        },
        onItemLongPress: (itemId: string) => {
          const item = itemMap.get(itemId);
          if (item) onItemLongPress?.(item);
        },
        onRowFocus: (rowIndex: number) => {
          scrollToRowNativeRef.current(rowIndex, 0);
          // Load more rows if near the end
          if (rowIndex >= renderedRowCount - 2) {
            setRenderedRowCount((prev) => prev + LOAD_MORE_ROWS);
          }
          // Trigger onEndReached for progressive loading when near the end (5 rows early)
          if (rowIndex >= renderedRowCount - 5 && hasMoreItemsRef.current && !loadingMoreRef.current) {
            onEndReachedRef.current?.();
          }
        },
        onItemFocus: (index: number) => {
          onItemFocusRef.current?.(index);
        },
      }),
      [itemMap, onItemPress, onItemLongPress, renderedRowCount],
    );

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

    const renderContent = () => {
      if (loading) {
        return (
          <View style={styles.loadingContainer}>
            <ActivityIndicator size="large" color={theme.colors.accent.primary} />
            <Text style={styles.loadingText}>Loading {title.toLowerCase()}...</Text>
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
                          <MaterialCommunityIcons name={releaseIcon.name} size={14} color={releaseIcon.color} />
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
              ListFooterComponent={LoadingMoreIndicator}
            />
          );
        }

        // Carousel layout for mobile (horizontal scroll)
        // Android: aggressive virtualization to reduce memory pressure
        const isAndroid = Platform.OS === 'android';
        return (
          <FlatList
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={[styles.carouselContent, styles.carouselContentCompact]}
            data={items}
            extraData={badgeVisibility}
            keyExtractor={keyExtractor}
            initialNumToRender={isAndroid ? 3 : 5}
            maxToRenderPerBatch={isAndroid ? 2 : 5}
            windowSize={isAndroid ? 2 : 3}
            removeClippedSubviews={isAndroid}
            renderItem={({ item, index }) => (
              <View
                style={[
                  styles.carouselItem,
                  {
                    marginRight: index === items.length - 1 ? 0 : gap,
                  },
                ]}>
                <MediaItem
                  title={item}
                  onPress={() => onItemPress?.(item)}
                  onLongPress={onItemLongPress ? () => onItemLongPress(item) : undefined}
                  badgeVisibility={badgeVisibility}
                />
              </View>
            )}
          />
        );
      }

      // Build explicit rows like the Search screen for predictable TV navigation
      const rows: DisplayTitle[][] = [];
      for (let i = 0; i < items.length; i += columns) {
        rows.push(items.slice(i, i + columns));
      }

      // Native focus mode: Use Pressable focus with a proper grid layout
      // Programmatic scroll keeps focused row at top unless near bottom
      // Uses progressive rendering and context for stable callbacks
      if (useNativeFocus) {
        // Progressive rendering - only render visible rows
        const visibleRows = rows.slice(0, renderedRowCount);
        const isAndroidDevice = Platform.OS === 'android';

        return (
          <MediaGridHandlersContext.Provider value={gridHandlers}>
            <ScrollView
              ref={nativeScrollViewRef}
              style={styles.scrollView}
              contentContainerStyle={styles.grid}
              showsVerticalScrollIndicator={false}
              scrollEnabled={!Platform.isTV}
              onScroll={handleScroll}
              scrollEventThrottle={100}
              removeClippedSubviews={isAndroidDevice}>
              {visibleRows.map((row, rowIndex) => (
                <View
                  key={`row-${rowIndex}`}
                  ref={(ref) => {
                    nativeRowRefs.current[`native-row-${rowIndex}`] = ref;
                  }}
                  style={styles.rowContainer}>
                  <View style={[styles.gridRow, { marginHorizontal: -halfGap }]}>
                    {row.map((item, colIndex) => {
                      const index = rowIndex * columns + colIndex;
                      const isFirstItem = index === 0;
                      const isLeftmostColumn = colIndex === 0;
                      return (
                        <View
                          key={keyExtractor(item, index)}
                          style={[styles.itemWrapper, { width: `${100 / columns}%`, paddingHorizontal: halfGap }]}>
                          {useMinimalCards ? (
                            <MinimalCard
                              item={item}
                              rowIndex={rowIndex}
                              itemIndex={index}
                              autoFocus={defaultFocusFirstItem && isFirstItem}
                            />
                          ) : (
                            <MediaItem
                              title={item}
                              onPress={() => onItemPress?.(item)}
                              onFocus={() => {
                                gridHandlers.onRowFocus(rowIndex);
                                gridHandlers.onItemFocus(index);
                              }}
                              badgeVisibility={badgeVisibility}
                              useNativeFocus={true}
                              autoFocus={defaultFocusFirstItem && isFirstItem}
                              trapLeftFocus={isLeftmostColumn}
                            />
                          )}
                        </View>
                      );
                    })}
                  </View>
                </View>
              ))}
              {LoadingMoreIndicator}
            </ScrollView>
          </MediaGridHandlersContext.Provider>
        );
      }

      // Key changes when row count changes, forcing spatial navigation to recalculate layout
      const gridKey = `grid-${rows.length}`;

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
                          <MediaItem
                            title={item}
                            onPress={() => onItemPress?.(item)}
                            onFocus={() => scrollToRow(rowKey)}
                            badgeVisibility={badgeVisibility}
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
        <Text style={styles.title}>{title}</Text>
        {renderContent()}
      </View>
    );
  },
  (prevProps, nextProps) => {
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
      prevProps.useNativeFocus === nextProps.useNativeFocus &&
      prevProps.useMinimalCards === nextProps.useMinimalCards &&
      prevProps.loadingMore === nextProps.loadingMore &&
      prevProps.hasMoreItems === nextProps.hasMoreItems
      // onItemPress and onEndReached are omitted - function reference changes are expected
    );
  },
);

export default MediaGrid;
