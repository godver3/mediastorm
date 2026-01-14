import React, { useCallback, useEffect, useMemo, useRef } from 'react';
import { Pressable, ScrollView, StyleSheet, Text, View, Platform } from 'react-native';

import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';

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

  return (
    <View style={styles.overlay}>
      {!Platform.isTV ? <Pressable style={styles.backdrop} onPress={onClose} /> : null}
      <View style={styles.modalContainer}>
        <View style={styles.modalHeader}>
          <Text style={styles.modalTitle}>Filter by Category</Text>
          <Text style={styles.modalSubtitle}>
            {selectedCategories.length === 0
              ? 'All categories shown'
              : `${selectedCategories.length} ${selectedCategories.length === 1 ? 'category' : 'categories'} selected`}
          </Text>
        </View>

        <View style={styles.actionRow}>
          <Pressable
            onPress={() => withSelectGuard(handleSelectAll)}
            hasTVPreferredFocus={true}
            tvParallaxProperties={{ enabled: false }}
            style={({ focused }) => [styles.actionButton, focused && styles.actionButtonFocused]}
          >
            {({ focused }) => (
              <Text style={[styles.actionButtonText, focused && styles.actionButtonTextFocused]}>
                {allSelected ? 'Clear All' : 'Select All'}
              </Text>
            )}
          </Pressable>
        </View>

        <ScrollView contentContainerStyle={styles.categoriesList}>
          {categories.map((category) => {
            const isSelected = selectedCategories.includes(category);
            return (
              <Pressable
                key={category}
                onPress={() => withSelectGuard(() => onToggleCategory(category))}
                tvParallaxProperties={{ enabled: false }}
                style={({ focused }) => [
                  styles.categoryItem,
                  focused && styles.categoryItemFocused,
                  isSelected && styles.categoryItemSelected,
                ]}
              >
                {({ focused }) => (
                  <>
                    <View style={styles.checkbox}>{isSelected && <View style={styles.checkboxInner} />}</View>
                    <Text
                      style={[
                        styles.categoryText,
                        focused && styles.categoryTextFocused,
                        isSelected && styles.categoryTextSelected,
                      ]}
                    >
                      {category}
                    </Text>
                  </>
                )}
              </Pressable>
            );
          })}
        </ScrollView>

        <View style={styles.modalFooter}>
          <Pressable
            onPress={() => withSelectGuard(onClose)}
            tvParallaxProperties={{ enabled: false }}
            style={({ focused }) => [styles.closeButton, focused && styles.closeButtonFocused]}
          >
            {({ focused }) => (
              <Text style={[styles.closeButtonText, focused && styles.closeButtonTextFocused]}>Close</Text>
            )}
          </Pressable>
        </View>
      </View>
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
      width: '80%',
      maxWidth: 800,
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
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
    },
    actionButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    actionButtonText: {
      ...theme.typography.body.md,
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
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: 2,
      borderColor: 'transparent',
      gap: theme.spacing.md,
    },
    categoryItemFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
    },
    categoryItemSelected: {
      backgroundColor: theme.colors.accent.primary + '15',
    },
    checkbox: {
      width: 28,
      height: 28,
      borderRadius: theme.radius.sm,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      alignItems: 'center',
      justifyContent: 'center',
    },
    checkboxInner: {
      width: 16,
      height: 16,
      borderRadius: theme.radius.sm,
      backgroundColor: theme.colors.accent.primary,
    },
    categoryText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      flex: 1,
    },
    categoryTextFocused: {
      color: theme.colors.accent.primary,
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
      paddingHorizontal: theme.spacing['2xl'],
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      minWidth: 200,
      alignItems: 'center',
    },
    closeButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    closeButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    closeButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
  });
