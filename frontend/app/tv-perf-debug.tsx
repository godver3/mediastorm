import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  FlatList,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { Stack } from 'expo-router';
import { LinearGradient } from 'expo-linear-gradient';
import Animated, {
  useAnimatedStyle,
  useSharedValue,
  withTiming,
  Easing,
} from 'react-native-reanimated';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  SpatialNavigationVirtualizedList,
  SpatialNavigationVirtualizedGrid,
} from '@/services/tv-navigation';
import { useTheme } from '@/theme';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import FocusablePressable from '@/components/FocusablePressable';

const isAndroidTV = Platform.isTV && Platform.OS === 'android';

type TestItem = {
  id: string;
  title: string;
  color: string;
};

// Generate test data
const generateItems = (count: number): TestItem[] =>
  Array.from({ length: count }, (_, i) => ({
    id: `item-${i}`,
    title: `Item ${i + 1}`,
    color: `hsl(${(i * 37) % 360}, 70%, 50%)`,
  }));

const ITEM_COUNT = 50;
const COLUMNS = 7;
const CARD_WIDTH = 180;
const CARD_HEIGHT = 270;
const GAP = 16;

type TestMode =
  | 'ultra'
  | 'ultra-hscroll'
  | 'ultra-more-items'
  | 'manual-rows'
  | 'virtualized-grid';

type CardProps = {
  item: TestItem;
  showGradient?: boolean;
  useScale?: boolean;
  onSelect?: () => void;
  onFocus?: () => void;
};

// Basic card without SpatialNavigationFocusableView (for wrapping externally)
const BasicCard = React.memo(function BasicCard({
  item,
  isFocused,
  showGradient = true,
  useScale = true,
}: {
  item: TestItem;
  isFocused: boolean;
  showGradient?: boolean;
  useScale?: boolean;
}) {
  const focusedStyle = useScale ? styles.cardFocused : styles.cardFocusedNoScale;
  return (
    <View
      style={[
        styles.card,
        { backgroundColor: item.color },
        isFocused && focusedStyle,
      ]}
      renderToHardwareTextureAndroid={isAndroidTV}
    >
      {showGradient && (
        <LinearGradient
          colors={['transparent', 'rgba(0,0,0,0.8)']}
          style={styles.cardGradient}
        />
      )}
      <Text style={styles.cardTitle}>{item.title}</Text>
      {isFocused && <Text style={styles.focusedLabel}>FOCUSED</Text>}
    </View>
  );
});

// Card with built-in SpatialNavigationFocusableView
const FocusableCard = React.memo(function FocusableCard({
  item,
  showGradient = true,
  useScale = true,
  onSelect,
  onFocus,
}: CardProps) {
  return (
    <SpatialNavigationFocusableView onSelect={onSelect} onFocus={onFocus}>
      {({ isFocused }: { isFocused: boolean }) => (
        <BasicCard item={item} isFocused={isFocused} showGradient={showGradient} useScale={useScale} />
      )}
    </SpatialNavigationFocusableView>
  );
});

// Simple focusable - minimal overhead
const SimpleFocusable = React.memo(function SimpleFocusable({
  item,
  onSelect,
  onFocus,
}: CardProps) {
  return (
    <SpatialNavigationFocusableView onSelect={onSelect} onFocus={onFocus}>
      {({ isFocused }: { isFocused: boolean }) => (
        <View
          style={[
            styles.simpleCard,
            { backgroundColor: item.color },
            isFocused && styles.simpleCardFocused,
          ]}
        >
          <Text style={styles.simpleCardTitle}>{item.title}</Text>
        </View>
      )}
    </SpatialNavigationFocusableView>
  );
});

// Animated card using Reanimated for smooth focus transitions
const AnimatedFocusableCard = React.memo(function AnimatedFocusableCard({
  item,
  onSelect,
  onFocus,
}: CardProps) {
  const scale = useSharedValue(1);
  const borderOpacity = useSharedValue(0);

  const animatedStyle = useAnimatedStyle(() => ({
    transform: [{ scale: scale.value }],
    borderColor: `rgba(255, 255, 255, ${borderOpacity.value})`,
  }));

  return (
    <SpatialNavigationFocusableView onSelect={onSelect} onFocus={onFocus}>
      {({ isFocused }: { isFocused: boolean }) => {
        // Update animations based on focus state
        scale.value = withTiming(isFocused ? 1.05 : 1, {
          duration: 150,
          easing: Easing.out(Easing.ease),
        });
        borderOpacity.value = withTiming(isFocused ? 1 : 0, {
          duration: 150,
          easing: Easing.out(Easing.ease),
        });

        return (
          <Animated.View
            style={[
              styles.card,
              { backgroundColor: item.color, borderWidth: 3 },
              animatedStyle,
            ]}
          >
            <LinearGradient
              colors={['transparent', 'rgba(0,0,0,0.8)']}
              style={styles.cardGradient}
            />
            <Text style={styles.cardTitle}>{item.title}</Text>
            {isFocused && <Text style={styles.focusedLabel}>FOCUSED</Text>}
          </Animated.View>
        );
      }}
    </SpatialNavigationFocusableView>
  );
});

// Native focus card using Pressable with Android TV/tvOS native focus
const NativeFocusCard = React.memo(function NativeFocusCard({
  item,
  onSelect,
  onFocus,
  autoFocus,
}: CardProps & { autoFocus?: boolean }) {
  const [isFocused, setIsFocused] = useState(false);

  return (
    <Pressable
      onPress={onSelect}
      onFocus={() => {
        setIsFocused(true);
        onFocus?.();
      }}
      onBlur={() => setIsFocused(false)}
      // @ts-ignore - TV-specific props
      hasTVPreferredFocus={autoFocus}
      style={[
        styles.card,
        { backgroundColor: item.color },
        isFocused && styles.cardFocused,
      ]}
    >
      <LinearGradient
        colors={['transparent', 'rgba(0,0,0,0.8)']}
        style={styles.cardGradient}
      />
      <Text style={styles.cardTitle}>{item.title}</Text>
      {isFocused && <Text style={styles.focusedLabel}>NATIVE</Text>}
    </Pressable>
  );
});

// Native focus card using Pressable's style function - no setState re-renders
const NativeFocusCardNoState = React.memo(function NativeFocusCardNoState({
  item,
  onSelect,
  onFocus,
  autoFocus,
}: CardProps & { autoFocus?: boolean }) {
  return (
    <Pressable
      onPress={onSelect}
      onFocus={onFocus}
      // @ts-ignore - TV-specific props
      hasTVPreferredFocus={autoFocus}
      style={({ focused }) => [
        styles.card,
        { backgroundColor: item.color },
        focused && styles.cardFocusedNoScale,
      ]}
    >
      <Text style={styles.cardTitle}>{item.title}</Text>
    </Pressable>
  );
});

// Absolute minimal native card - no gradient, no scale, uses Pressable's native focused prop
const NativeMinimalCard = React.memo(function NativeMinimalCard({
  item,
  onSelect,
  onFocus,
  autoFocus,
}: CardProps & { autoFocus?: boolean }) {
  return (
    <Pressable
      onPress={onSelect}
      onFocus={onFocus}
      // @ts-ignore - TV-specific props
      hasTVPreferredFocus={autoFocus}
      style={({ focused }) => [
        styles.simpleCard,
        { backgroundColor: item.color },
        focused && styles.simpleCardFocused,
      ]}
    >
      <Text style={styles.simpleCardTitle}>{item.title}</Text>
    </Pressable>
  );
});

// Ultra minimal - NO JS callbacks at all, pure native focus
const NativeUltraMinimalCard = React.memo(function NativeUltraMinimalCard({
  item,
  autoFocus,
}: { item: TestItem; autoFocus?: boolean }) {
  return (
    <Pressable
      // @ts-ignore - TV-specific props
      hasTVPreferredFocus={autoFocus}
      style={({ focused }) => [
        styles.simpleCard,
        { backgroundColor: item.color },
        focused && styles.simpleCardFocused,
      ]}
    >
      <Text style={styles.simpleCardTitle}>{item.title}</Text>
    </Pressable>
  );
});

export default function TVPerfDebugScreen() {
  const theme = useTheme();
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const [mode, setMode] = useState<TestMode>('ultra');
  const [lastAction, setLastAction] = useState<string>('None');
  const [actionTime, setActionTime] = useState<number>(0);
  const [focusRate, setFocusRate] = useState<number>(0);
  const lastPressTime = useRef<number>(0);
  const focusCountRef = useRef<number>(0);
  const scrollViewRef = useRef<ScrollView>(null);
  const rowRefs = useRef<{ [key: string]: View | null }>({});

  // Calculate focus rate (focuses per second)
  useEffect(() => {
    const interval = setInterval(() => {
      setFocusRate(focusCountRef.current);
      focusCountRef.current = 0;
    }, 1000);
    return () => clearInterval(interval);
  }, []);

  const items = useMemo(() => generateItems(ITEM_COUNT), []);
  const rows = useMemo(() => {
    const result: typeof items[] = [];
    for (let i = 0; i < items.length; i += COLUMNS) {
      result.push(items.slice(i, i + COLUMNS));
    }
    return result;
  }, [items]);

  const handleSelect = useCallback((item: { id: string; title: string }) => {
    const now = performance.now();
    const delta = lastPressTime.current ? now - lastPressTime.current : 0;
    lastPressTime.current = now;
    setLastAction(`Selected: ${item.title}`);
    setActionTime(delta);
  }, []);

  const handleFocus = useCallback((item: { id: string; title: string }) => {
    const now = performance.now();
    const delta = lastPressTime.current ? now - lastPressTime.current : 0;
    lastPressTime.current = now;
    focusCountRef.current++;
    setLastAction(`Focused: ${item.title}`);
    setActionTime(delta);
  }, []);

  const scrollToRow = useCallback((rowKey: string) => {
    const rowRef = rowRefs.current[rowKey];
    if (!rowRef || !scrollViewRef.current) return;

    rowRef.measureLayout(
      scrollViewRef.current as any,
      (_left, top) => {
        scrollViewRef.current?.scrollTo({ y: Math.max(0, top - 100), animated: true });
      },
      () => {}
    );
  }, []);

  const modes: { key: TestMode; label: string }[] = [
    { key: 'ultra', label: 'Ultra (7 cols)' },
    { key: 'ultra-hscroll', label: 'Ultra + HScroll' },
    { key: 'ultra-more-items', label: 'Ultra (15/row)' },
    { key: 'manual-rows', label: 'Spatial Nav' },
    { key: 'virtualized-grid', label: 'VGrid' },
  ];

  const renderVirtualizedGrid = () => (
    <View style={styles.gridContainer}>
      <SpatialNavigationVirtualizedGrid
        data={items}
        itemHeight={CARD_HEIGHT + GAP}
        numberOfColumns={COLUMNS}
        renderItem={({ item }: { item: TestItem }) => (
          <View style={styles.gridItemWrapper}>
            <FocusableCard
              item={item}
              onSelect={() => handleSelect(item)}
              onFocus={() => handleFocus(item)}
            />
          </View>
        )}
      />
    </View>
  );

  const renderVirtualizedListHorizontal = () => (
    <ScrollView
      ref={scrollViewRef}
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
    >
      <SpatialNavigationNode orientation="vertical">
        {rows.map((row, rowIndex) => (
          <View key={`row-${rowIndex}`} style={styles.rowContainer}>
            <Text style={styles.rowLabel}>Row {rowIndex + 1}</Text>
            <SpatialNavigationNode orientation="horizontal">
              {rowIndex === 0 ? (
                <DefaultFocus>
                  <SpatialNavigationVirtualizedList
                    data={row}
                    itemSize={CARD_WIDTH + GAP}
                    orientation="horizontal"
                    numberOfRenderedItems={isAndroidTV ? 7 : 9}
                    numberOfItemsVisibleOnScreen={isAndroidTV ? 5 : 7}
                    renderItem={({ item }: { item: TestItem }) => (
                      <View style={{ width: CARD_WIDTH, marginRight: GAP }}>
                        <FocusableCard
                          item={item}
                          onSelect={() => handleSelect(item)}
                          onFocus={() => handleFocus(item)}
                        />
                      </View>
                    )}
                  />
                </DefaultFocus>
              ) : (
                <SpatialNavigationVirtualizedList
                  data={row}
                  itemSize={CARD_WIDTH + GAP}
                  orientation="horizontal"
                  numberOfRenderedItems={isAndroidTV ? 7 : 9}
                  numberOfItemsVisibleOnScreen={isAndroidTV ? 5 : 7}
                  renderItem={({ item }: { item: TestItem }) => (
                    <View style={{ width: CARD_WIDTH, marginRight: GAP }}>
                      <FocusableCard
                        item={item}
                        onSelect={() => handleSelect(item)}
                        onFocus={() => handleFocus(item)}
                      />
                    </View>
                  )}
                />
              )}
            </SpatialNavigationNode>
          </View>
        ))}
      </SpatialNavigationNode>
    </ScrollView>
  );

  const renderManualRows = (showGradient: boolean, useScale: boolean = true) => (
    <ScrollView
      ref={scrollViewRef}
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
      removeClippedSubviews={isAndroidTV}
    >
      <SpatialNavigationNode orientation="vertical" alignInGrid>
        {rows.map((row, rowIndex) => {
          const rowKey = `row-${rowIndex}`;
          return (
            <View
              key={rowKey}
              ref={(ref) => { rowRefs.current[rowKey] = ref; }}
              style={styles.rowContainer}
            >
              <Text style={styles.rowLabel}>Row {rowIndex + 1}</Text>
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.rowInner}>
                  {row.map((item, colIndex) => {
                    const isFirst = rowIndex === 0 && colIndex === 0;
                    const card = (
                      <FocusableCard
                        key={item.id}
                        item={item}
                        showGradient={showGradient}
                        useScale={useScale}
                        onSelect={() => handleSelect(item)}
                        onFocus={() => {
                          handleFocus(item);
                          scrollToRow(rowKey);
                        }}
                      />
                    );
                    return (
                      <View key={item.id} style={styles.manualCardWrapper}>
                        {isFirst ? <DefaultFocus>{card}</DefaultFocus> : card}
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

  const renderSimpleFocusable = () => (
    <ScrollView
      ref={scrollViewRef}
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
      removeClippedSubviews={isAndroidTV}
    >
      <SpatialNavigationNode orientation="vertical" alignInGrid>
        {rows.map((row, rowIndex) => {
          const rowKey = `row-${rowIndex}`;
          return (
            <View
              key={rowKey}
              ref={(ref) => { rowRefs.current[rowKey] = ref; }}
              style={styles.rowContainer}
            >
              <Text style={styles.rowLabel}>Row {rowIndex + 1}</Text>
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.rowInner}>
                  {row.map((item, colIndex) => {
                    const isFirst = rowIndex === 0 && colIndex === 0;
                    const card = (
                      <SimpleFocusable
                        key={item.id}
                        item={item}
                        onSelect={() => handleSelect(item)}
                        onFocus={() => {
                          handleFocus(item);
                          scrollToRow(rowKey);
                        }}
                      />
                    );
                    return (
                      <View key={item.id} style={styles.simpleCardWrapper}>
                        {isFirst ? <DefaultFocus>{card}</DefaultFocus> : card}
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

  const renderAnimatedRows = () => (
    <ScrollView
      ref={scrollViewRef}
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
      removeClippedSubviews={isAndroidTV}
    >
      <SpatialNavigationNode orientation="vertical" alignInGrid>
        {rows.map((row, rowIndex) => {
          const rowKey = `row-${rowIndex}`;
          return (
            <View
              key={rowKey}
              ref={(ref) => { rowRefs.current[rowKey] = ref; }}
              style={styles.rowContainer}
            >
              <Text style={styles.rowLabel}>Row {rowIndex + 1}</Text>
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.rowInner}>
                  {row.map((item, colIndex) => {
                    const isFirst = rowIndex === 0 && colIndex === 0;
                    const card = (
                      <AnimatedFocusableCard
                        key={item.id}
                        item={item}
                        onSelect={() => handleSelect(item)}
                        onFocus={() => {
                          handleFocus(item);
                          scrollToRow(rowKey);
                        }}
                      />
                    );
                    return (
                      <View key={item.id} style={styles.manualCardWrapper}>
                        {isFirst ? <DefaultFocus>{card}</DefaultFocus> : card}
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

  // Native focus - uses Android TV / tvOS native focus system without spatial navigation library
  const renderNativeFocus = () => (
    <ScrollView
      ref={scrollViewRef}
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
    >
      {rows.map((row, rowIndex) => {
        const rowKey = `row-${rowIndex}`;
        return (
          <View
            key={rowKey}
            ref={(ref) => { rowRefs.current[rowKey] = ref; }}
            style={styles.rowContainer}
          >
            <Text style={styles.rowLabel}>Row {rowIndex + 1} (Native + setState)</Text>
            <View style={styles.rowInner}>
              {row.map((item, colIndex) => (
                <View key={item.id} style={styles.manualCardWrapper}>
                  <NativeFocusCard
                    item={item}
                    autoFocus={rowIndex === 0 && colIndex === 0}
                    onSelect={() => handleSelect(item)}
                    onFocus={() => {
                      handleFocus(item);
                      scrollToRow(rowKey);
                    }}
                  />
                </View>
              ))}
            </View>
          </View>
        );
      })}
    </ScrollView>
  );

  // Native focus without setState - uses Pressable's style function
  const renderNativeNoState = () => (
    <ScrollView
      ref={scrollViewRef}
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
    >
      {rows.map((row, rowIndex) => {
        const rowKey = `row-${rowIndex}`;
        return (
          <View
            key={rowKey}
            ref={(ref) => { rowRefs.current[rowKey] = ref; }}
            style={styles.rowContainer}
          >
            <Text style={styles.rowLabel}>Row {rowIndex + 1} (No setState)</Text>
            <View style={styles.rowInner}>
              {row.map((item, colIndex) => (
                <View key={item.id} style={styles.manualCardWrapper}>
                  <NativeFocusCardNoState
                    item={item}
                    autoFocus={rowIndex === 0 && colIndex === 0}
                    onSelect={() => handleSelect(item)}
                    onFocus={() => {
                      handleFocus(item);
                      scrollToRow(rowKey);
                    }}
                  />
                </View>
              ))}
            </View>
          </View>
        );
      })}
    </ScrollView>
  );

  // Native minimal - absolute minimum overhead
  const renderNativeMinimal = () => (
    <ScrollView
      ref={scrollViewRef}
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
    >
      {rows.map((row, rowIndex) => {
        const rowKey = `row-${rowIndex}`;
        return (
          <View
            key={rowKey}
            ref={(ref) => { rowRefs.current[rowKey] = ref; }}
            style={styles.rowContainer}
          >
            <Text style={styles.rowLabel}>Row {rowIndex + 1} (Minimal)</Text>
            <View style={styles.rowInner}>
              {row.map((item, colIndex) => (
                <View key={item.id} style={styles.simpleCardWrapper}>
                  <NativeMinimalCard
                    item={item}
                    autoFocus={rowIndex === 0 && colIndex === 0}
                    onSelect={() => handleSelect(item)}
                    onFocus={() => {
                      handleFocus(item);
                      scrollToRow(rowKey);
                    }}
                  />
                </View>
              ))}
            </View>
          </View>
        );
      })}
    </ScrollView>
  );

  // Ultra - NO JS callbacks, pure native focus
  const renderUltra = () => (
    <ScrollView
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
    >
      {rows.map((row, rowIndex) => (
        <View key={`row-${rowIndex}`} style={styles.rowContainer}>
          <Text style={styles.rowLabel}>Row {rowIndex + 1}</Text>
          <View style={styles.rowInner}>
            {row.map((item, colIndex) => (
              <View key={item.id} style={styles.simpleCardWrapper}>
                <NativeUltraMinimalCard
                  item={item}
                  autoFocus={rowIndex === 0 && colIndex === 0}
                />
              </View>
            ))}
          </View>
        </View>
      ))}
    </ScrollView>
  );

  // Ultra with horizontal ScrollView per row
  const renderUltraHScroll = () => (
    <ScrollView
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
    >
      {rows.map((row, rowIndex) => (
        <View key={`row-${rowIndex}`} style={styles.rowContainer}>
          <Text style={styles.rowLabel}>Row {rowIndex + 1} (HScroll)</Text>
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={styles.rowInner}
          >
            {row.map((item, colIndex) => (
              <View key={item.id} style={styles.simpleCardWrapper}>
                <NativeUltraMinimalCard
                  item={item}
                  autoFocus={rowIndex === 0 && colIndex === 0}
                />
              </View>
            ))}
          </ScrollView>
        </View>
      ))}
    </ScrollView>
  );

  // Ultra with more items per row (15) to force horizontal scrolling
  const moreItemsPerRow = 15;
  const moreRows = useMemo(() => {
    const result: TestItem[][] = [];
    for (let i = 0; i < items.length; i += moreItemsPerRow) {
      result.push(items.slice(i, i + moreItemsPerRow));
    }
    // If we don't have enough items, pad with more
    if (result.length < 4) {
      const extraItems = generateItems(60);
      for (let i = 0; i < extraItems.length; i += moreItemsPerRow) {
        result.push(extraItems.slice(i, i + moreItemsPerRow));
      }
    }
    return result;
  }, [items]);

  const renderUltraMoreItems = () => (
    <ScrollView
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
    >
      {moreRows.map((row, rowIndex) => (
        <View key={`row-${rowIndex}`} style={styles.rowContainer}>
          <Text style={styles.rowLabel}>Row {rowIndex + 1} ({row.length} items)</Text>
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={styles.rowInner}
          >
            {row.map((item, colIndex) => (
              <View key={item.id} style={styles.simpleCardWrapper}>
                <NativeUltraMinimalCard
                  item={item}
                  autoFocus={rowIndex === 0 && colIndex === 0}
                />
              </View>
            ))}
          </ScrollView>
        </View>
      ))}
    </ScrollView>
  );

  // Native FlatList - uses RN FlatList with native focus
  const renderNativeFlatList = () => (
    <FlatList<TestItem>
      data={items}
      numColumns={COLUMNS}
      keyExtractor={(item) => item.id}
      contentContainerStyle={styles.scrollContent}
      removeClippedSubviews={isAndroidTV}
      initialNumToRender={14}
      maxToRenderPerBatch={7}
      windowSize={5}
      renderItem={({ item, index }) => (
        <View style={styles.flatListItem}>
          <NativeFocusCard
            item={item}
            autoFocus={index === 0}
            onSelect={() => handleSelect(item)}
            onFocus={() => handleFocus(item)}
          />
        </View>
      )}
    />
  );

  const renderContent = () => {
    switch (mode) {
      case 'ultra':
        return renderUltra();
      case 'ultra-hscroll':
        return renderUltraHScroll();
      case 'ultra-more-items':
        return renderUltraMoreItems();
      case 'manual-rows':
        return renderManualRows(true, true);
      case 'virtualized-grid':
        return renderVirtualizedGrid();
    }
  };

  return (
    <SpatialNavigationRoot isActive={true}>
      <Stack.Screen options={{ headerShown: false }} />
      <View style={styles.container}>
        {/* Header with mode selector */}
        <View style={styles.header}>
          <Text style={styles.title}>TV Performance Debug</Text>
          <View style={styles.stats}>
            <Text style={styles.statText}>Mode: {mode}</Text>
            <Text style={styles.statText}>Last: {lastAction}</Text>
            <Text style={styles.statText}>
              Delta: {actionTime > 0 ? `${actionTime.toFixed(0)}ms` : '-'}
            </Text>
            <Text style={[styles.statText, focusRate > 5 && styles.statHighlight]}>
              Rate: {focusRate}/s
            </Text>
            <Text style={styles.statText}>
              {screenWidth}x{screenHeight} | {ITEM_COUNT} items
            </Text>
          </View>
        </View>

        {/* Mode selector */}
        <SpatialNavigationNode orientation="horizontal">
          <View style={styles.modeSelector}>
            {modes.map((m, index) => (
              <FocusablePressable
                key={m.key}
                focusKey={`mode-${m.key}`}
                text={m.label}
                onSelect={() => setMode(m.key)}
                style={[
                  styles.modeButton,
                  mode === m.key && styles.modeButtonActive,
                ]}
              />
            ))}
          </View>
        </SpatialNavigationNode>

        {/* Content area */}
        <View style={styles.content}>
          {renderContent()}
        </View>
      </View>
    </SpatialNavigationRoot>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#121212',
  },
  header: {
    paddingHorizontal: 40,
    paddingTop: 40,
    paddingBottom: 16,
  },
  title: {
    fontSize: 32,
    fontWeight: 'bold',
    color: '#fff',
    marginBottom: 8,
  },
  stats: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    gap: 24,
  },
  statText: {
    fontSize: 16,
    color: '#aaa',
  },
  statHighlight: {
    color: '#4ade80',
    fontWeight: 'bold',
  },
  modeSelector: {
    flexDirection: 'row',
    paddingHorizontal: 40,
    paddingBottom: 16,
    gap: 12,
  },
  modeButton: {
    paddingHorizontal: 20,
    paddingVertical: 10,
    backgroundColor: '#333',
    borderRadius: 8,
  },
  modeButtonActive: {
    backgroundColor: '#6366f1',
  },
  content: {
    flex: 1,
    paddingHorizontal: 40,
  },
  scrollView: {
    flex: 1,
  },
  scrollContent: {
    paddingBottom: 100,
  },
  gridContainer: {
    flex: 1,
  },
  gridItemWrapper: {
    width: CARD_WIDTH,
    height: CARD_HEIGHT,
    marginRight: GAP,
    marginBottom: GAP,
  },
  rowContainer: {
    marginBottom: 24,
  },
  rowLabel: {
    fontSize: 18,
    color: '#888',
    marginBottom: 8,
  },
  rowInner: {
    flexDirection: 'row',
    gap: GAP,
  },
  manualCardWrapper: {
    width: CARD_WIDTH,
  },
  simpleCardWrapper: {
    width: CARD_WIDTH,
  },
  flatListItem: {
    width: CARD_WIDTH,
    marginRight: GAP,
    marginBottom: GAP,
  },
  card: {
    width: CARD_WIDTH,
    height: CARD_HEIGHT,
    borderRadius: 12,
    overflow: 'hidden',
    justifyContent: 'flex-end',
    borderWidth: 3,
    borderColor: 'transparent',
  },
  cardFocused: {
    borderColor: '#fff',
    transform: [{ scale: 1.05 }],
  },
  cardFocusedNoScale: {
    borderColor: '#fff',
  },
  cardGradient: {
    ...StyleSheet.absoluteFillObject,
  },
  cardTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: '#fff',
    padding: 12,
  },
  focusedLabel: {
    position: 'absolute',
    top: 12,
    right: 12,
    fontSize: 12,
    fontWeight: 'bold',
    color: '#fff',
    backgroundColor: 'rgba(0,0,0,0.7)',
    paddingHorizontal: 8,
    paddingVertical: 4,
    borderRadius: 4,
  },
  simpleCard: {
    width: CARD_WIDTH,
    height: CARD_HEIGHT,
    borderRadius: 12,
    justifyContent: 'center',
    alignItems: 'center',
    borderWidth: 3,
    borderColor: 'transparent',
  },
  simpleCardFocused: {
    borderColor: '#fff',
  },
  simpleCardTitle: {
    fontSize: 20,
    fontWeight: 'bold',
    color: '#fff',
  },
});
