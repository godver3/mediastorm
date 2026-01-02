import React, { useCallback, useMemo, useState } from 'react';
import { Dimensions, Platform, StyleSheet, Text, useWindowDimensions, View } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';

import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { useMenuContext } from '@/components/MenuContext';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationRoot,
  SpatialNavigationVirtualizedGrid,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { Stack } from 'expo-router';

interface DebugItem {
  id: string;
  index: number;
  label: string;
}

// Generate test items for the grid
const ITEM_COUNT = 200;
const GRID_ITEMS: DebugItem[] = Array.from({ length: ITEM_COUNT }, (_, i) => ({
  id: `item-${i}`,
  index: i,
  label: `Item ${i + 1}`,
}));

interface DebugCardContentProps {
  item: DebugItem;
  isFocused: boolean;
  cardWidth: number;
  cardHeight: number;
}

const DebugCardContent: React.FC<DebugCardContentProps> = React.memo(({ item, isFocused, cardWidth, cardHeight }) => {
  const theme = useTheme();

  const styles = useMemo(
    () =>
      StyleSheet.create({
        card: {
          width: cardWidth,
          height: cardHeight,
          borderRadius: theme.radius.md,
          backgroundColor: theme.colors.background.surface,
          borderWidth: 3,
          borderColor: theme.colors.border.subtle,
          justifyContent: 'center',
          alignItems: 'center',
          gap: theme.spacing.sm,
        },
        cardFocused: {
          borderColor: theme.colors.accent.primary,
          backgroundColor: theme.colors.background.elevated,
          transform: [{ scale: 1.02 }],
        },
        cardIndex: {
          ...theme.typography.title.xl,
          color: theme.colors.text.primary,
          fontSize: 48,
          fontWeight: '700',
        },
        cardLabel: {
          ...theme.typography.body.lg,
          color: theme.colors.text.secondary,
        },
        cardTextFocused: {
          color: theme.colors.accent.primary,
        },
        cardHint: {
          ...theme.typography.caption.sm,
          color: theme.colors.text.muted,
          marginTop: theme.spacing.xs,
        },
      }),
    [theme, cardWidth, cardHeight],
  );

  return (
    <View style={[styles.card, isFocused && styles.cardFocused]}>
      <Text style={[styles.cardIndex, isFocused && styles.cardTextFocused]}>{item.index + 1}</Text>
      <Text style={[styles.cardLabel, isFocused && styles.cardTextFocused]}>{item.label}</Text>
      {isFocused && <Text style={styles.cardHint}>Press to select</Text>}
    </View>
  );
});

interface GridCardProps {
  item: DebugItem;
  onSelect: () => void;
  onFocus: () => void;
  cardWidth: number;
  cardHeight: number;
}

const GridCard: React.FC<GridCardProps> = React.memo(({ item, onSelect, onFocus, cardWidth, cardHeight }) => {
  return (
    <SpatialNavigationFocusableView onSelect={onSelect} onFocus={onFocus}>
      {({ isFocused }: { isFocused: boolean }) => (
        <DebugCardContent item={item} isFocused={isFocused} cardWidth={cardWidth} cardHeight={cardHeight} />
      )}
    </SpatialNavigationFocusableView>
  );
});

function DebugScreen() {
  const theme = useTheme();
  const windowDims = useWindowDimensions();
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth, screenHeight), [theme, screenWidth, screenHeight]);
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const isFocused = useIsFocused();
  const isActive = isFocused && !isMenuOpen;
  const [lastPressed, setLastPressed] = useState<string | null>(null);
  const [focusedIndex, setFocusedIndex] = useState<number | null>(null);

  // Callbacks for grid items
  const handleItemSelect = useCallback((label: string) => {
    console.log('[debug] Item pressed:', label);
    setLastPressed(label);
  }, []);

  const handleItemFocus = useCallback((index: number) => {
    setFocusedIndex(index);
  }, []);

  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      if (movement === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

  // Grid configuration
  const numberOfColumns = 6;
  const gap = theme.spacing.lg;
  const horizontalPadding = theme.spacing.xl * 1.5;
  const availableWidth = screenWidth - horizontalPadding * 2;
  const totalGapWidth = gap * (numberOfColumns - 1);
  const cardWidth = Math.floor((availableWidth - totalGapWidth) / numberOfColumns);
  const cardHeight = Math.round(cardWidth * 0.75);

  // Calculate grid height (leave room for header)
  const headerHeight = 80;
  const gridHeight = screenHeight - headerHeight - theme.spacing.xl * 3;

  // Render a grid item
  const renderGridItem = useCallback(
    ({ item }: { item: DebugItem }) => {
      return (
        <GridCard
          item={item}
          onSelect={() => handleItemSelect(item.label)}
          onFocus={() => handleItemFocus(item.index)}
          cardWidth={cardWidth}
          cardHeight={cardHeight}
        />
      );
    },
    [cardWidth, cardHeight, handleItemSelect, handleItemFocus],
  );

  return (
    <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          <View style={styles.headerRow}>
            <Text style={styles.title}>Debug Grid</Text>
            <View style={styles.statsContainer}>
              <Text style={styles.statsText}>useTVDimensions: {screenWidth}x{screenHeight}</Text>
              <Text style={styles.statsText}>useWindowDimensions: {windowDims.width}x{windowDims.height}</Text>
              <Text style={styles.statsText}>Dimensions.screen: {Dimensions.get('screen').width}x{Dimensions.get('screen').height}</Text>
              <Text style={styles.statsText}>Card: {cardWidth}x{cardHeight}</Text>
              <Text style={styles.statsText}>
                Focused: {focusedIndex !== null ? `Item ${focusedIndex + 1}` : 'None'}
              </Text>
            </View>
          </View>

          {/* Virtualized Grid */}
          <View style={{ height: gridHeight }}>
            <DefaultFocus>
              <SpatialNavigationVirtualizedGrid
                data={GRID_ITEMS}
                renderItem={renderGridItem}
                itemHeight={cardHeight + gap}
                numberOfColumns={numberOfColumns}
                numberOfRenderedRows={6}
                numberOfRowsVisibleOnScreen={3}
                rowContainerStyle={{ gap }}
              />
            </DefaultFocus>
          </View>
        </View>
      </FixedSafeAreaView>
    </SpatialNavigationRoot>
  );
}

export default React.memo(DebugScreen);

const createStyles = (theme: NovaTheme, _screenWidth: number = 1920, _screenHeight: number = 1080) => {
  const scaleFactor = Platform.isTV ? 1.5 : 1;
  const gap = theme.spacing.lg;
  const horizontalPadding = theme.spacing.xl * scaleFactor;

  return StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
    },
    container: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: horizontalPadding,
      paddingTop: theme.spacing.xl * scaleFactor,
    },
    headerRow: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      marginBottom: theme.spacing.lg * scaleFactor,
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    statsContainer: {
      flexDirection: 'row',
      gap: theme.spacing.xl,
    },
    statsText: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
    },
    gridContainer: {
      overflow: 'hidden',
    },
    rowContainer: {
      gap: gap,
      paddingHorizontal: 0,
    },
    rowTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.md,
    },
  });
};
