import React, { useCallback, useEffect, useMemo, useRef } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View, Platform } from 'react-native';

import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  SpatialNavigationFocusableView,
  DefaultFocus,
} from '@/services/tv-navigation';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';

interface CategoryFilterModalProps {
  visible: boolean;
  onClose: () => void;
  categories: string[];
  selectedCategories: string[];
  onToggleCategory: (category: string) => void;
  onSelectAll: () => void;
  onClearAll: () => void;
}

export const CategoryFilterModal: React.FC<CategoryFilterModalProps> = ({
  visible,
  onClose,
  categories,
  selectedCategories,
  onToggleCategory,
  onSelectAll,
  onClearAll,
}) => {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);

  const allSelected = selectedCategories.length === categories.length;

  // Guard against duplicate "select" events on tvOS (e.g., key down/up or Modal duplication)
  const selectGuardRef = useRef(false);
  const withSelectGuard = useCallback((fn: () => void) => {
    if (Platform.isTV) {
      if (selectGuardRef.current) return;
      selectGuardRef.current = true;
      try {
        fn();
      } finally {
        setTimeout(() => {
          selectGuardRef.current = false;
        }, 250);
      }
    } else {
      fn();
    }
  }, []);

  const handleSelectAll = useCallback(() => {
    if (allSelected) {
      onClearAll();
    } else {
      onSelectAll();
    }
  }, [allSelected, onSelectAll, onClearAll]);

  // Keep ref up to date to avoid stale closures
  const onCloseRef = useRef(onClose);
  const removeInterceptorRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  // Register back interceptor to close modal when menu/back button is pressed on tvOS
  // Following the same pattern as TvModal for proper handling
  useEffect(() => {
    if (!visible) {
      // Clean up interceptor when modal is hidden
      if (removeInterceptorRef.current) {
        console.log('[CategoryFilterModal] Removing back interceptor (modal hidden)');
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
      return;
    }

    // Install interceptor when modal is shown
    console.log('[CategoryFilterModal] ========== INSTALLING BACK INTERCEPTOR ==========');
    let isHandling = false;
    let cleanupScheduled = false;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      console.log('[CategoryFilterModal] ========== INTERCEPTOR CALLED ==========');

      // Prevent duplicate handling if called multiple times
      if (isHandling) {
        console.log('[CategoryFilterModal] Already handling back press, ignoring duplicate');
        return true;
      }

      isHandling = true;
      console.log('[CategoryFilterModal] Back interceptor called, closing modal');

      // Call onClose using ref to avoid stale closure
      onCloseRef.current();

      // Delay the cleanup to ensure it stays active long enough to swallow duplicate events
      if (!cleanupScheduled) {
        cleanupScheduled = true;
        setTimeout(() => {
          if (removeInterceptorRef.current) {
            console.log('[CategoryFilterModal] Removing back interceptor (delayed cleanup)');
            removeInterceptorRef.current();
            removeInterceptorRef.current = null;
          }
          isHandling = false;
        }, 750);
      }

      console.log('[CategoryFilterModal] ========== RETURNING TRUE (HANDLED) ==========');
      return true; // Handled - prevents further interceptors from running
    });

    removeInterceptorRef.current = removeInterceptor;
    console.log('[CategoryFilterModal] ========== INTERCEPTOR INSTALLED ==========');

    // Cleanup on unmount
    return () => {
      console.log(
        '[CategoryFilterModal] Unmount cleanup - interceptor will be removed by delayed cleanup if scheduled',
      );
    };
  }, [visible]);

  if (!visible) {
    return null;
  }

  const modalContent = (
    <View style={styles.modalContainer} focusable={false}>
      <View style={styles.modalHeader} focusable={false}>
        <Text style={styles.modalTitle}>Filter by Category</Text>
        <Text style={styles.modalSubtitle}>
          {selectedCategories.length === 0
            ? 'All categories shown'
            : `${selectedCategories.length} ${selectedCategories.length === 1 ? 'category' : 'categories'} selected`}
        </Text>
      </View>

      {Platform.isTV ? (
        <SpatialNavigationNode orientation="vertical">
          <View style={styles.actionRow} focusable={false}>
            <DefaultFocus>
              <SpatialNavigationFocusableView onSelect={() => withSelectGuard(handleSelectAll)}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <View style={[styles.actionButton, isFocused && styles.actionButtonFocused]}>
                    <Text style={[styles.actionButtonText, isFocused && styles.actionButtonTextFocused]}>
                      {allSelected ? 'Clear All' : 'Select All'}
                    </Text>
                  </View>
                )}
              </SpatialNavigationFocusableView>
            </DefaultFocus>
          </View>

          <ScrollView contentContainerStyle={styles.categoriesList}>
            <SpatialNavigationNode orientation="vertical">
              {categories.map((category) => {
                const isSelected = selectedCategories.includes(category);
                return (
                  <SpatialNavigationFocusableView
                    key={category}
                    onSelect={() => withSelectGuard(() => onToggleCategory(category))}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <View
                        style={[
                          styles.categoryItem,
                          isSelected && styles.categoryItemSelected,
                          isFocused && styles.categoryItemFocused,
                          isFocused && isSelected && styles.categoryItemFocusedSelected,
                        ]}>
                        <View style={styles.checkbox}>{isSelected && <View style={styles.checkboxInner} />}</View>
                        <Text
                          style={[
                            styles.categoryText,
                            isFocused && styles.categoryTextFocused,
                            isSelected && styles.categoryTextSelected,
                          ]}>
                          {category}
                        </Text>
                      </View>
                    )}
                  </SpatialNavigationFocusableView>
                );
              })}
            </SpatialNavigationNode>
          </ScrollView>

          <View style={styles.modalFooter} focusable={false}>
            <SpatialNavigationFocusableView
              onSelect={() =>
                withSelectGuard(() => {
                  if (removeInterceptorRef.current) {
                    console.log('[CategoryFilterModal] Removing back interceptor (Close button pressed)');
                    removeInterceptorRef.current();
                    removeInterceptorRef.current = null;
                  }
                  onClose();
                })
              }>
              {({ isFocused }: { isFocused: boolean }) => (
                <View style={[styles.closeButton, isFocused && styles.closeButtonFocused]}>
                  <Text style={[styles.closeButtonText, isFocused && styles.closeButtonTextFocused]}>Close</Text>
                </View>
              )}
            </SpatialNavigationFocusableView>
          </View>
        </SpatialNavigationNode>
      ) : (
        <>
          <View style={styles.actionRow} focusable={false}>
            <Pressable
              onPress={() => withSelectGuard(handleSelectAll)}
              style={({ pressed }) => [styles.actionButton, pressed && { opacity: 0.7 }]}>
              <Text style={styles.actionButtonText}>{allSelected ? 'Clear All' : 'Select All'}</Text>
            </Pressable>
          </View>

          <ScrollView contentContainerStyle={styles.categoriesList}>
            {categories.map((category) => {
              const isSelected = selectedCategories.includes(category);
              return (
                <Pressable
                  key={category}
                  onPress={() => withSelectGuard(() => onToggleCategory(category))}
                  style={[
                    styles.categoryItem,
                    isSelected && styles.categoryItemSelected,
                  ]}>
                  <View style={styles.checkbox}>{isSelected && <View style={styles.checkboxInner} />}</View>
                  <Text
                    style={[
                      styles.categoryText,
                      isSelected && styles.categoryTextSelected,
                    ]}>
                    {category}
                  </Text>
                </Pressable>
              );
            })}
          </ScrollView>

          <View style={styles.modalFooter} focusable={false}>
            <Pressable
              onPress={() =>
                withSelectGuard(() => {
                  if (removeInterceptorRef.current) {
                    removeInterceptorRef.current();
                    removeInterceptorRef.current = null;
                  }
                  onClose();
                })
              }
              style={({ pressed }) => [styles.closeButton, pressed && { opacity: 0.7 }]}>
              <Text style={styles.closeButtonText}>Close</Text>
            </Pressable>
          </View>
        </>
      )}
    </View>
  );

  return (
    <View style={styles.overlay} focusable={false}>
      {!Platform.isTV ? <Pressable style={styles.backdrop} onPress={onClose} /> : null}
      {Platform.isTV ? (
        <SpatialNavigationRoot isActive={visible}>{modalContent}</SpatialNavigationRoot>
      ) : (
        modalContent
      )}
    </View>
  );
};

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    overlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    backdrop: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
    },
    modalContainer: {
      width: Platform.isTV ? '45%' : '90%',
      maxWidth: 500,
      maxHeight: '80%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
    },
    modalHeader: {
      padding: theme.spacing.xl,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
    },
    modalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
    },
    modalSubtitle: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    actionRow: {
      flexDirection: 'row',
      padding: theme.spacing.lg,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
    },
    actionButton: {
      // TVActionButton consistent scaling using tvScale
      paddingHorizontal: theme.spacing.lg * tvScale(1.375, 1),
      paddingVertical: theme.spacing.md * tvScale(1.375, 1),
      borderRadius: theme.radius.md * tvScale(1.375, 1),
      backgroundColor: theme.colors.overlay.button,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    actionButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    actionButtonText: {
      ...theme.typography.label.md,
      fontSize: theme.typography.label.md.fontSize * tvScale(1.375, 1),
      lineHeight: theme.typography.label.md.lineHeight * tvScale(1.375, 1),
      color: theme.colors.text.primary,
    },
    actionButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    categoriesList: {
      padding: theme.spacing.lg,
      gap: theme.spacing.sm,
    },
    categoryItem: {
      flexDirection: 'row',
      alignItems: 'center',
      padding: theme.spacing.lg,
      borderRadius: theme.radius.md * tvScale(1.375, 1),
      backgroundColor: theme.colors.overlay.button,
      borderWidth: 2,
      borderColor: 'transparent',
      gap: theme.spacing.md,
    },
    categoryItemFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    categoryItemSelected: {
      backgroundColor: theme.colors.accent.primary + '20',
      borderColor: theme.colors.accent.primary + '60',
    },
    categoryItemFocusedSelected: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.text.inverse,
    },
    checkbox: {
      width: 20,
      height: 20,
      borderRadius: theme.radius.xs,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      alignItems: 'center',
      justifyContent: 'center',
    },
    checkboxInner: {
      width: 12,
      height: 12,
      borderRadius: theme.radius.xs,
      backgroundColor: theme.colors.accent.primary,
    },
    categoryText: {
      ...theme.typography.label.md,
      fontSize: theme.typography.label.md.fontSize * tvScale(1.375, 1),
      lineHeight: theme.typography.label.md.lineHeight * tvScale(1.375, 1),
      color: theme.colors.text.primary,
      flex: 1,
    },
    categoryTextFocused: {
      color: theme.colors.text.inverse,
    },
    categoryTextSelected: {
      fontWeight: '600',
    },
    modalFooter: {
      padding: theme.spacing.xl,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'center',
    },
    closeButton: {
      paddingHorizontal: theme.spacing.lg * tvScale(1.375, 1),
      paddingVertical: theme.spacing.md * tvScale(1.375, 1),
      borderRadius: theme.radius.md * tvScale(1.375, 1),
      backgroundColor: theme.colors.overlay.button,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      width: '60%',
      alignItems: 'center',
    },
    closeButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    closeButtonText: {
      ...theme.typography.label.md,
      fontSize: theme.typography.label.md.fontSize * tvScale(1.375, 1),
      lineHeight: theme.typography.label.md.lineHeight * tvScale(1.375, 1),
      color: theme.colors.text.primary,
    },
    closeButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
  });
