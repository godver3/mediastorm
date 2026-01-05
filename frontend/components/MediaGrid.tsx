import React, { useCallback, useMemo, useRef } from 'react';

import {
  ActivityIndicator,
  Image as RNImage,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { Image as CachedImage } from './Image';
import Animated, { Easing, FadeOut, Layout } from 'react-native-reanimated';
import { DefaultFocus, SpatialNavigationNode } from '@/services/tv-navigation';
import { Title } from '../services/api';
import { useResponsiveColumns } from '../hooks/useResponsiveColumns';
import { useTVDimensions } from '../hooks/useTVDimensions';
import type { ColumnOverride } from '../hooks/useResponsiveColumns';
import { useTheme } from '../theme';
import type { NovaTheme } from '../theme';
import MediaItem from './MediaItem';
import { LinearGradient } from 'expo-linear-gradient';

type DisplayTitle = Title & { uniqueKey?: string };

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
  textContainer: { position: 'absolute' as const, bottom: 0, left: 0, right: 0, padding: 8, alignItems: 'center' as const },
  title: { color: '#fff', fontSize: 14, fontWeight: '600' as const, textAlign: 'center' as const },
  year: { color: 'rgba(255,255,255,0.7)', fontSize: 12, marginTop: 2 },
};

// Optimized card for native focus mode - uses cached images
const MinimalCard = React.memo(function MinimalCard({
  item,
  onPress,
  onFocus,
  autoFocus,
}: {
  item: DisplayTitle;
  onPress?: () => void;
  onFocus?: () => void;
  autoFocus?: boolean;
}) {
  return (
    <Pressable
      onPress={onPress}
      onFocus={onFocus}
      hasTVPreferredFocus={autoFocus}
      style={({ focused }) => focused ? minimalCardStyles.focused : minimalCardStyles.base}
    >
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
        {item.year ? (
          <Text style={minimalCardStyles.year}>{item.year}</Text>
        ) : null}
      </View>
    </Pressable>
  );
});


const createStyles = (theme: NovaTheme, screenWidth?: number, parentPadding: number = 0) => {
  const isCompact = theme.breakpoint === 'compact';

  // Calculate card dimensions for mobile grid layout (matching search page)
  // Use 4 columns on wide mobile screens (foldables, tablets), 2 on phones
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
    minimalCardLevel = 0,
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

      if (isCompact) {
        // Grid layout for mobile (matching search page)
        if (layout === 'grid') {
          return (
            <ScrollView
              style={styles.scrollView}
              contentContainerStyle={styles.grid}
              showsVerticalScrollIndicator={false}
            >
              <View style={styles.mobileGridContainer}>
                {items.map((item, index) => (
                  <Pressable
                    key={keyExtractor(item, index)}
                    style={styles.mobileCard}
                    onPress={() => onItemPress?.(item)}
                    android_ripple={{ color: theme.colors.accent.primary + '30' }}
                  >
                    <View style={styles.cardImageContainer}>
                      {item.poster?.url ? (
                        <RNImage source={{ uri: item.poster.url }} style={styles.cardImage} resizeMode="cover" />
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
            </ScrollView>
          );
        }

        // Carousel layout for mobile (horizontal scroll)
        return (
          <Animated.FlatList
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={[styles.carouselContent, styles.carouselContentCompact]}
            data={items}
            keyExtractor={keyExtractor}
            initialNumToRender={5}
            maxToRenderPerBatch={5}
            windowSize={3}
            removeClippedSubviews={false}
            itemLayoutAnimation={Layout.duration(250).easing(Easing.out(Easing.ease))}
            renderItem={({ item, index }) => (
              <Animated.View
                exiting={FadeOut.duration(200)}
                style={[
                  styles.carouselItem,
                  {
                    marginRight: index === items.length - 1 ? 0 : gap,
                  },
                ]}
              >
                <MediaItem
                  title={item}
                  onPress={() => onItemPress?.(item)}
                  onLongPress={onItemLongPress ? () => onItemLongPress(item) : undefined}
                  badgeVisibility={badgeVisibility}
                />
              </Animated.View>
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
      // NO onFocus callbacks - relies on native scroll behavior for maximum performance
      if (useNativeFocus) {
        return (
          <ScrollView
            style={styles.scrollView}
            contentContainerStyle={styles.grid}
            showsVerticalScrollIndicator={false}
          >
            {rows.map((row, rowIndex) => (
              <View key={`row-${rowIndex}`} style={styles.rowContainer}>
                <View style={[styles.gridRow, { marginHorizontal: -halfGap }]}>
                  {row.map((item, colIndex) => {
                    const index = rowIndex * columns + colIndex;
                    const isFirstItem = index === 0;
                    return (
                      <View
                        key={keyExtractor(item, index)}
                        style={[styles.itemWrapper, { width: `${100 / columns}%`, paddingHorizontal: halfGap }]}
                      >
                        {useMinimalCards ? (
                          <MinimalCard
                            item={item}
                            onPress={() => onItemPress?.(item)}
                            autoFocus={defaultFocusFirstItem && isFirstItem}
                          />
                        ) : (
                          <MediaItem
                            title={item}
                            onPress={() => onItemPress?.(item)}
                            badgeVisibility={badgeVisibility}
                            useNativeFocus={true}
                            autoFocus={defaultFocusFirstItem && isFirstItem}
                          />
                        )}
                      </View>
                    );
                  })}
                </View>
              </View>
            ))}
          </ScrollView>
        );
      }

      // Key changes when row count changes, forcing spatial navigation to recalculate layout
      const gridKey = `grid-${rows.length}`;

      // Standard mode: Use SpatialNavigationNode for focus management
      return (
        <ScrollView
          ref={mainScrollViewRef}
          style={styles.scrollView}
          contentContainerStyle={styles.grid}
          bounces={false}
          showsVerticalScrollIndicator={false}
          scrollEnabled={true}
          contentInsetAdjustmentBehavior="never"
          automaticallyAdjustContentInsets={false}
          removeClippedSubviews={Platform.isTV}
          scrollEventThrottle={16}
          // Android TV: prevent native focus-based scrolling
          focusable={false}
          // @ts-ignore - TV-specific prop
          isTVSelectable={false}
        >
          <SpatialNavigationNode key={gridKey} orientation="vertical" alignInGrid>
            {rows.map((row, rowIndex) => {
              const rowKey = `row-${rowIndex}`;
              return (
                <View
                  key={rowKey}
                  ref={(ref) => {
                    rowRefs.current[rowKey] = ref;
                  }}
                  style={styles.rowContainer}
                >
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
                            style={[styles.itemWrapper, { width: `${100 / columns}%`, paddingHorizontal: halfGap }]}
                          >
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
      <View style={containerStyle}>
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
      prevProps.useMinimalCards === nextProps.useMinimalCards
      // onItemPress is omitted - function reference changes are expected
    );
  },
);

export default MediaGrid;
