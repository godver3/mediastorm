/**
 * Modal Selection Test Screen
 * Testing different approaches to handle select button presses in modals
 */

import FocusablePressable from '@/components/FocusablePressable';
import { useMenuContext } from '@/components/MenuContext';
import TvModal from '@/components/TvModal';
import { DefaultFocus, SpatialNavigationNode, SpatialNavigationRoot } from '@/services/tv-navigation';
import { useTheme } from '@/theme';
import { useCallback, useMemo, useRef, useState } from 'react';
import { FlatList, Platform, ScrollView, StyleSheet, Text, View } from 'react-native';

type TestModalType = 'tv-modal' | 'tv-modal-flatlist' | 'tv-modal-scrollnode' | 'tv-modal-manual-scroll' | null;

export default function ModalTestScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const [activeModal, setActiveModal] = useState<TestModalType>(null);
  const [logs, setLogs] = useState<string[]>([]);
  const { isOpen: isMenuOpen } = useMenuContext();

  const addLog = useCallback((message: string) => {
    const timestamp = new Date().toLocaleTimeString();
    setLogs((prev) => [`[${timestamp}] ${message}`, ...prev.slice(0, 49)]);
    console.log(`[ModalTest] ${message}`);
  }, []); // No dependencies - uses functional setState

  const closeModal = useCallback(() => {
    console.log('[ModalTest] Closing modal');
    setLogs((prev) => [`[${new Date().toLocaleTimeString()}] Closing modal`, ...prev.slice(0, 49)]);
    setActiveModal(null);
  }, []); // No dependencies - avoid re-creating

  const handleItemSelect = useCallback((itemNumber: number) => {
    const timestamp = new Date().toLocaleTimeString();
    console.log(`[ModalTest] ✅ Item ${itemNumber} SELECTED!`);
    setLogs((prev) => [`[${timestamp}] ✅ Item ${itemNumber} SELECTED!`, ...prev.slice(0, 49)]);
    setActiveModal(null);
  }, []); // No dependencies - avoid re-creating

  return (
    <SpatialNavigationRoot isActive={activeModal === null && !isMenuOpen}>
      <View style={styles.container}>
        <Text style={styles.title}>Modal Selection Tests</Text>
        <Text style={styles.subtitle}>Test different approaches to handle select in modals</Text>

        <SpatialNavigationNode orientation="vertical">
          <ScrollView style={styles.buttonContainer} contentContainerStyle={styles.buttonContent}>
            <DefaultFocus>
              <FocusablePressable
                text="Test 1: ScrollView (default)"
                onSelect={() => {
                  addLog('Opening TvModal with ScrollView');
                  setActiveModal('tv-modal');
                }}
                style={styles.testButton}
              />
            </DefaultFocus>

            <FocusablePressable
              text="Test 2: FlatList"
              onSelect={() => {
                addLog('Opening TvModal with FlatList');
                setActiveModal('tv-modal-flatlist');
              }}
              style={styles.testButton}
            />

            <FocusablePressable
              text="Test 3: ScrollView in ScrollNode"
              onSelect={() => {
                addLog('Opening TvModal with ScrollView in SpatialNavigationScrollView');
                setActiveModal('tv-modal-scrollnode');
              }}
              style={styles.testButton}
            />

            <FocusablePressable
              text="Test 4: Manual ScrollView control"
              onSelect={() => {
                addLog('Opening TvModal with manual scroll control');
                setActiveModal('tv-modal-manual-scroll');
              }}
              style={styles.testButton}
            />

            <FocusablePressable
              text="Clear Logs"
              onSelect={() => {
                addLog('Clearing logs');
                setLogs([]);
              }}
              style={styles.clearButton}
            />
          </ScrollView>
        </SpatialNavigationNode>

        <View style={styles.logContainer}>
          <Text style={styles.logTitle}>Event Log:</Text>
          <ScrollView style={styles.logScroll}>
            {logs.map((log, index) => (
              <Text key={index} style={styles.logText}>
                {log}
              </Text>
            ))}
          </ScrollView>
        </View>

        {/* Test 1: ScrollView (default) */}
        <TvModalTest
          visible={activeModal === 'tv-modal'}
          onClose={closeModal}
          onItemSelect={handleItemSelect}
          addLog={addLog}
        />

        {/* Test 2: FlatList */}
        <TvModalFlatListTest
          visible={activeModal === 'tv-modal-flatlist'}
          onClose={closeModal}
          onItemSelect={handleItemSelect}
          addLog={addLog}
        />

        {/* Test 3: ScrollView in ScrollNode */}
        <TvModalScrollNodeTest
          visible={activeModal === 'tv-modal-scrollnode'}
          onClose={closeModal}
          onItemSelect={handleItemSelect}
          addLog={addLog}
        />

        {/* Test 4: Manual ScrollView control */}
        <TvModalManualScrollTest
          visible={activeModal === 'tv-modal-manual-scroll'}
          onClose={closeModal}
          onItemSelect={handleItemSelect}
          addLog={addLog}
        />
      </View>
    </SpatialNavigationRoot>
  );
}

// Test 1: ScrollView (default) - ScrollView inside SpatialNavigationNode
function TvModalTest({
  visible,
  onClose,
  onItemSelect,
  addLog,
}: {
  visible: boolean;
  onClose: () => void;
  onItemSelect: (n: number) => void;
  addLog: (msg: string) => void;
}) {
  const theme = useTheme();
  const styles = useMemo(() => createModalStyles(theme), [theme]);

  if (!visible) return null;

  return (
    <TvModal visible={visible} onRequestClose={onClose}>
      <View style={styles.modal}>
        <Text style={styles.modalTitle}>Test 1: ScrollView (Default)</Text>
        <Text style={styles.modalDescription}>ScrollView inside SpatialNavigationNode</Text>

        <SpatialNavigationNode orientation="vertical">
          <ScrollView style={styles.scrollableList} contentContainerStyle={styles.scrollableListContent}>
            {Array.from({ length: 10 }, (_, i) => {
              const itemNumber = i + 1;
              const item = (
                <FocusablePressable
                  key={i}
                  text={`Item ${itemNumber}`}
                  onSelect={() => {
                    addLog(`🎯 Item ${itemNumber} SELECTED!`);
                    onItemSelect(itemNumber);
                  }}
                  onFocus={() => addLog(`👉 Item ${itemNumber} focused`)}
                  style={styles.item}
                />
              );

              return i === 0 ? <DefaultFocus key={`default-${i}`}>{item}</DefaultFocus> : item;
            })}
          </ScrollView>

          <FocusablePressable
            text="Cancel"
            onSelect={() => {
              addLog('🎯 Cancel SELECTED!');
              onClose();
            }}
            onFocus={() => addLog('👉 Cancel focused')}
            style={styles.cancelButton}
          />
        </SpatialNavigationNode>

        <Text style={styles.modalHint}>ScrollView with 10 items (max 5 visible)</Text>
      </View>
    </TvModal>
  );
}

// Test 2: FlatList - Using FlatList instead of ScrollView
function TvModalFlatListTest({
  visible,
  onClose,
  onItemSelect,
  addLog,
}: {
  visible: boolean;
  onClose: () => void;
  onItemSelect: (n: number) => void;
  addLog: (msg: string) => void;
}) {
  const theme = useTheme();
  const styles = useMemo(() => createModalStyles(theme), [theme]);
  const items = useMemo(() => Array.from({ length: 10 }, (_, i) => ({ id: i + 1 })), []);

  if (!visible) return null;

  return (
    <TvModal visible={visible} onRequestClose={onClose}>
      <View style={styles.modal}>
        <Text style={styles.modalTitle}>Test 2: FlatList</Text>
        <Text style={styles.modalDescription}>Using FlatList for better performance</Text>

        <SpatialNavigationNode orientation="vertical">
          <FlatList
            data={items}
            style={styles.scrollableList}
            contentContainerStyle={styles.scrollableListContent}
            keyExtractor={(item) => `item-${item.id}`}
            renderItem={({ item, index }) => {
              const itemNumber = item.id;
              const pressable = (
                <FocusablePressable
                  text={`Item ${itemNumber}`}
                  onSelect={() => {
                    addLog(`🎯 Item ${itemNumber} SELECTED!`);
                    onItemSelect(itemNumber);
                  }}
                  onFocus={() => addLog(`👉 Item ${itemNumber} focused`)}
                  style={styles.item}
                />
              );

              return index === 0 ? <DefaultFocus key={`default-${index}`}>{pressable}</DefaultFocus> : pressable;
            }}
          />

          <FocusablePressable
            text="Cancel"
            onSelect={() => {
              addLog('🎯 Cancel SELECTED!');
              onClose();
            }}
            onFocus={() => addLog('👉 Cancel focused')}
            style={styles.cancelButton}
          />
        </SpatialNavigationNode>

        <Text style={styles.modalHint}>FlatList with 10 items (max 5 visible)</Text>
      </View>
    </TvModal>
  );
}

// Test 3: ScrollView outside of items node
function TvModalScrollNodeTest({
  visible,
  onClose,
  onItemSelect,
  addLog,
}: {
  visible: boolean;
  onClose: () => void;
  onItemSelect: (n: number) => void;
  addLog: (msg: string) => void;
}) {
  const theme = useTheme();
  const styles = useMemo(() => createModalStyles(theme), [theme]);

  if (!visible) return null;

  return (
    <TvModal visible={visible} onRequestClose={onClose}>
      <View style={styles.modal}>
        <Text style={styles.modalTitle}>Test 3: ScrollView in ScrollNode</Text>
        <Text style={styles.modalDescription}>Wrapping SpatialNavigationNode in ScrollView</Text>

        <ScrollView style={styles.scrollableList} contentContainerStyle={styles.scrollableListContent}>
          <SpatialNavigationNode orientation="vertical">
            {Array.from({ length: 10 }, (_, i) => {
              const itemNumber = i + 1;
              const item = (
                <FocusablePressable
                  key={i}
                  text={`Item ${itemNumber}`}
                  onSelect={() => {
                    addLog(`🎯 Item ${itemNumber} SELECTED!`);
                    onItemSelect(itemNumber);
                  }}
                  onFocus={() => addLog(`👉 Item ${itemNumber} focused`)}
                  style={styles.item}
                />
              );

              return i === 0 ? <DefaultFocus key={`default-${i}`}>{item}</DefaultFocus> : item;
            })}

            <FocusablePressable
              text="Cancel"
              onSelect={() => {
                addLog('🎯 Cancel SELECTED!');
                onClose();
              }}
              onFocus={() => addLog('👉 Cancel focused')}
              style={styles.cancelButton}
            />
          </SpatialNavigationNode>
        </ScrollView>

        <Text style={styles.modalHint}>SpatialNavigationNode wrapped in ScrollView</Text>
      </View>
    </TvModal>
  );
}

// Test 4: Manual scroll control with onFocus scrolling
function TvModalManualScrollTest({
  visible,
  onClose,
  onItemSelect,
  addLog,
}: {
  visible: boolean;
  onClose: () => void;
  onItemSelect: (n: number) => void;
  addLog: (msg: string) => void;
}) {
  const theme = useTheme();
  const styles = useMemo(() => createModalStyles(theme), [theme]);
  const scrollViewRef = useRef<ScrollView>(null);
  const itemHeight = 88; // Approximate height with margin
  const _visibleItems = 5;

  const handleItemFocus = useCallback(
    (index: number) => {
      addLog(`👉 Item ${index + 1} focused - scrolling`);

      // Calculate scroll position to keep focused item in view
      const scrollOffset = Math.max(0, (index - 2) * itemHeight); // Keep item in middle
      scrollViewRef.current?.scrollTo({ y: scrollOffset, animated: true });
    },
    [addLog, itemHeight],
  );

  if (!visible) return null;

  return (
    <TvModal visible={visible} onRequestClose={onClose}>
      <View style={styles.modal}>
        <Text style={styles.modalTitle}>Test 4: Manual Scroll Control</Text>
        <Text style={styles.modalDescription}>Programmatic scrollTo on focus events</Text>

        <SpatialNavigationNode orientation="vertical">
          <ScrollView
            ref={scrollViewRef}
            style={styles.scrollableList}
            contentContainerStyle={styles.scrollableListContent}
            scrollEnabled={false} // Disable manual scroll, use programmatic only
          >
            {Array.from({ length: 10 }, (_, i) => {
              const itemNumber = i + 1;
              const item = (
                <FocusablePressable
                  key={i}
                  text={`Item ${itemNumber}`}
                  onSelect={() => {
                    addLog(`🎯 Item ${itemNumber} SELECTED!`);
                    onItemSelect(itemNumber);
                  }}
                  onFocus={() => handleItemFocus(i)}
                  style={styles.item}
                />
              );

              return i === 0 ? <DefaultFocus key={`default-${i}`}>{item}</DefaultFocus> : item;
            })}
          </ScrollView>

          <FocusablePressable
            text="Cancel"
            onSelect={() => {
              addLog('🎯 Cancel SELECTED!');
              onClose();
            }}
            onFocus={() => addLog('👉 Cancel focused')}
            style={styles.cancelButton}
          />
        </SpatialNavigationNode>

        <Text style={styles.modalHint}>Manual scrollTo() on item focus</Text>
      </View>
    </TvModal>
  );
}

const createStyles = (theme: any) =>
  StyleSheet.create({
    container: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      padding: theme.spacing.xl,
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.sm,
    },
    subtitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      marginBottom: theme.spacing.xl,
    },
    buttonContainer: {
      flex: 1,
      marginBottom: theme.spacing.xl,
    },
    buttonContent: {
      gap: theme.spacing.md,
    },
    testButton: {
      width: '100%',
      paddingVertical: theme.spacing.lg,
    },
    clearButton: {
      width: '100%',
      paddingVertical: theme.spacing.lg,
      marginTop: theme.spacing.lg,
    },
    logContainer: {
      height: 300,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      padding: theme.spacing.md,
    },
    logTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.sm,
    },
    logScroll: {
      flex: 1,
    },
    logText: {
      ...theme.typography.body.sm,
      fontFamily: Platform.select({ ios: 'Courier', android: 'monospace', default: 'monospace' }),
      color: theme.colors.text.secondary,
      marginBottom: theme.spacing.xs,
    },
  });

const createModalStyles = (theme: any) =>
  StyleSheet.create({
    overlay: {
      flex: 1,
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      justifyContent: 'center',
      alignItems: 'center',
      padding: theme.spacing.xl,
    },
    modal: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg,
      padding: theme.spacing['2xl'],
      width: '80%',
      maxWidth: 800,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    modalTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.sm,
    },
    modalDescription: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      marginBottom: theme.spacing.xl,
    },
    modalHint: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      marginTop: theme.spacing.lg,
      textAlign: 'center',
    },
    item: {
      paddingVertical: theme.spacing.lg,
      paddingHorizontal: theme.spacing.xl,
      borderRadius: theme.radius.md,
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      marginBottom: theme.spacing.md,
    },
    itemFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    itemText: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },
    itemTextFocused: {
      color: theme.colors.text.inverse,
      fontWeight: '600',
    },
    cancelButton: {
      marginTop: theme.spacing.lg,
      alignSelf: 'center',
    },
    scrollableList: {
      maxHeight: 400, // Approximately 5 items at 80px each
    },
    scrollableListContent: {
      gap: theme.spacing.md,
    },
  });
