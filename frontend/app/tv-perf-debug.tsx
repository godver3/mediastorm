import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  findNodeHandle,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { Stack } from 'expo-router';
import {
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import FocusablePressable from '@/components/FocusablePressable';

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

const ITEM_COUNT = 100;
const COLUMNS = 10;
const GAP = 16;

// Poster ratio cards (2:3 aspect ratio)
const CARD_WIDTH = 120;
const CARD_HEIGHT = 180;

type TestMode = 'native' | 'hybrid';

// Native card - pure native focus, no spatial nav overhead
const NativeCard = React.memo(function NativeCard({
  item,
  autoFocus,
}: { item: TestItem; autoFocus?: boolean }) {
  return (
    <Pressable
      // @ts-ignore - TV-specific props
      hasTVPreferredFocus={autoFocus}
      style={({ focused }) => [
        styles.card,
        { backgroundColor: item.color },
        focused && styles.cardFocused,
      ]}
    >
      <Text style={styles.cardTitle}>{item.title}</Text>
    </Pressable>
  );
});

// Hybrid approach: Pure native focus with nextFocus* props to maintain column position
// and trap horizontal focus within rows
type HybridCardProps = {
  item: TestItem;
  rowIndex: number;
  colIndex: number;
  autoFocus?: boolean;
  onFocus?: () => void;
  onSelect?: () => void;
  // Native focus linking for navigation
  nextFocusUp?: number;
  nextFocusDown?: number;
  nextFocusLeft?: number;
  nextFocusRight?: number;
  // Stable callback for registering tags
  registerTag: (rowIndex: number, colIndex: number, tag: number | null) => void;
};

const HybridCard = React.memo(function HybridCard({
  item,
  rowIndex,
  colIndex,
  autoFocus,
  onFocus,
  onSelect,
  nextFocusUp,
  nextFocusDown,
  nextFocusLeft,
  nextFocusRight,
  registerTag,
}: HybridCardProps) {
  const ref = useRef<View>(null);

  useEffect(() => {
    if (ref.current) {
      const tag = findNodeHandle(ref.current);
      registerTag(rowIndex, colIndex, tag);
    }
    // Only run on mount
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return (
    <Pressable
      ref={ref}
      onPress={onSelect}
      onFocus={onFocus}
      // @ts-ignore - TV-specific props
      hasTVPreferredFocus={autoFocus}
      nextFocusUp={nextFocusUp}
      nextFocusDown={nextFocusDown}
      nextFocusLeft={nextFocusLeft}
      nextFocusRight={nextFocusRight}
      style={({ focused }) => [
        styles.card,
        { backgroundColor: item.color },
        focused && styles.cardFocused,
      ]}
    >
      <Text style={styles.cardTitle}>{item.title}</Text>
    </Pressable>
  );
});

export default function TVPerfDebugScreen() {
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const [mode, setMode] = useState<TestMode>('native');
  const [lastAction, setLastAction] = useState<string>('None');
  const [actionTime, setActionTime] = useState<number>(0);
  const [focusRate, setFocusRate] = useState<number>(0);
  const lastPressTime = useRef<number>(0);
  const focusCountRef = useRef<number>(0);
  const currentRowRef = useRef<number>(0); // Track current row to avoid unnecessary scrolls
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

  // Row height: label (~26px) + marginBottom (8) + card (180) + container marginBottom (24) = ~238px
  const ROW_HEIGHT = CARD_HEIGHT + 58;
  const SCROLL_OFFSET = 20; // smaller offset = more scrolling

  const scrollToRow = useCallback((rowIndex: number) => {
    if (!scrollViewRef.current) return;
    // Calculate position directly for immediate, predictable scrolling
    const targetY = Math.max(0, rowIndex * ROW_HEIGHT - SCROLL_OFFSET);
    scrollViewRef.current.scrollTo({ y: targetY, animated: false });
  }, []);

  const modes: { key: TestMode; label: string }[] = [
    { key: 'native', label: 'Native' },
    { key: 'hybrid', label: 'Hybrid' },
  ];

  // Native - pure native focus, no spatial nav
  const renderNative = () => (
    <ScrollView
      style={styles.scrollView}
      contentContainerStyle={styles.scrollContent}
    >
      {rows.map((row, rowIndex) => (
        <View key={`row-${rowIndex}`} style={styles.rowContainer}>
          <Text style={styles.rowLabel}>Row {rowIndex + 1} (Native)</Text>
          <ScrollView
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={styles.rowInner}
          >
            {row.map((item, colIndex) => (
              <View key={item.id} style={styles.cardWrapper}>
                <NativeCard
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

  // Hybrid mode - pure native focus with nextFocusUp/Down for column alignment
  // Store card tags in a 2D grid: cardTags[rowIndex][colIndex] = native tag
  const cardTagsRef = useRef<(number | null)[][]>([]);
  const [cardTagsVersion, setCardTagsVersion] = useState(0);
  const pendingUpdateRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Initialize the 2D array structure
  if (cardTagsRef.current.length !== rows.length) {
    cardTagsRef.current = rows.map((row) => row.map(() => null));
  }

  // Debounced tag registration - batch all updates into single re-render
  const registerTag = useCallback((rowIndex: number, colIndex: number, tag: number | null) => {
    if (cardTagsRef.current[rowIndex]?.[colIndex] !== tag) {
      cardTagsRef.current[rowIndex][colIndex] = tag;

      // Debounce: only trigger one re-render after all cards register
      if (pendingUpdateRef.current) {
        clearTimeout(pendingUpdateRef.current);
      }
      pendingUpdateRef.current = setTimeout(() => {
        setCardTagsVersion((v) => v + 1);
        pendingUpdateRef.current = null;
      }, 50);
    }
  }, []);

  const getNextFocusUp = useCallback((rowIndex: number, colIndex: number) => {
    if (rowIndex === 0) return undefined;
    return cardTagsRef.current[rowIndex - 1]?.[colIndex] ?? undefined;
  }, [cardTagsVersion]); // eslint-disable-line react-hooks/exhaustive-deps

  const getNextFocusDown = useCallback((rowIndex: number, colIndex: number) => {
    if (rowIndex >= rows.length - 1) return undefined;
    return cardTagsRef.current[rowIndex + 1]?.[colIndex] ?? undefined;
  }, [cardTagsVersion, rows.length]); // eslint-disable-line react-hooks/exhaustive-deps

  // For trapping focus at row edges - first item points left to itself, last item points right to itself
  const getNextFocusLeft = useCallback((rowIndex: number, colIndex: number) => {
    if (colIndex === 0) {
      // First item in row - trap focus by pointing to self
      return cardTagsRef.current[rowIndex]?.[colIndex] ?? undefined;
    }
    return undefined; // Let native handle normal left navigation
  }, [cardTagsVersion]); // eslint-disable-line react-hooks/exhaustive-deps

  const getNextFocusRight = useCallback((rowIndex: number, colIndex: number, rowLength: number) => {
    if (colIndex === rowLength - 1) {
      // Last item in row - trap focus by pointing to self
      return cardTagsRef.current[rowIndex]?.[colIndex] ?? undefined;
    }
    return undefined; // Let native handle normal right navigation
  }, [cardTagsVersion]); // eslint-disable-line react-hooks/exhaustive-deps

  const renderHybrid = () => (
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
            <Text style={styles.rowLabel}>Row {rowIndex + 1} (Hybrid)</Text>
            <ScrollView
              horizontal
              showsHorizontalScrollIndicator={false}
              contentContainerStyle={styles.rowInner}
            >
              {row.map((item, colIndex) => (
                <View key={item.id} style={styles.cardWrapper}>
                  <HybridCard
                    item={item}
                    rowIndex={rowIndex}
                    colIndex={colIndex}
                    autoFocus={rowIndex === 0 && colIndex === 0}
                    onFocus={() => {
                      handleFocus(item);
                      // Only scroll when row changes (vertical navigation)
                      if (currentRowRef.current !== rowIndex) {
                        currentRowRef.current = rowIndex;
                        scrollToRow(rowIndex);
                      }
                    }}
                    onSelect={() => handleSelect(item)}
                    nextFocusUp={getNextFocusUp(rowIndex, colIndex)}
                    nextFocusDown={getNextFocusDown(rowIndex, colIndex)}
                    nextFocusLeft={getNextFocusLeft(rowIndex, colIndex)}
                    nextFocusRight={getNextFocusRight(rowIndex, colIndex, row.length)}
                    registerTag={registerTag}
                  />
                </View>
              ))}
            </ScrollView>
          </View>
        );
      })}
    </ScrollView>
  );

  const renderContent = () => {
    switch (mode) {
      case 'native':
        return renderNative();
      case 'hybrid':
        return renderHybrid();
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
  cardWrapper: {
    width: CARD_WIDTH,
  },
  card: {
    width: CARD_WIDTH,
    height: CARD_HEIGHT,
    borderRadius: 8,
    justifyContent: 'center',
    alignItems: 'center',
    borderWidth: 3,
    borderColor: 'transparent',
  },
  cardFocused: {
    borderColor: '#fff',
  },
  cardTitle: {
    fontSize: 14,
    fontWeight: 'bold',
    color: '#fff',
  },
});
