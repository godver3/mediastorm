import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Animated, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, useWindowDimensions, View } from 'react-native';

import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SpatialNavigationRoot } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';

interface TrackSelectionOption {
  id: string;
  label: string;
  description?: string;
}

interface TrackSelectionModalProps {
  visible: boolean;
  title: string;
  subtitle?: string;
  options: TrackSelectionOption[];
  selectedId?: string | null;
  onSelect: (id: string) => void;
  onClose: () => void;
  focusKeyPrefix?: string;
  /** Optional callback to open subtitle search modal */
  onSearchSubtitles?: () => void;
}

export const TrackSelectionModal: React.FC<TrackSelectionModalProps> = ({
  visible,
  title,
  subtitle,
  options,
  selectedId,
  onSelect,
  onClose,
  focusKeyPrefix = 'track',
  onSearchSubtitles,
}) => {
  const theme = useTheme();
  const { width: screenWidth, height: screenHeight } = useWindowDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth), [theme, screenWidth]);
  const hasOptions = options.length > 0;

  const selectedLabel = useMemo(() => options.find((option) => option.id === selectedId)?.label, [options, selectedId]);

  // Manual scroll handling for TV platforms using animated transform
  const scrollOffsetRef = useRef(new Animated.Value(0)).current;
  const itemLayoutsRef = useRef<{ y: number; height: number }[]>([]);
  const containerHeightRef = useRef(0);
  const currentScrollRef = useRef(0);
  const pendingFocusIndexRef = useRef<number | null>(null);

  const contentHeightRef = useRef(0);

  // Try to scroll to pending focus if all measurements are ready
  const tryScrollToPendingFocus = useCallback((animated: boolean) => {
    const index = pendingFocusIndexRef.current;
    if (index === null) return;

    const layout = itemLayoutsRef.current[index];
    const containerHeight = containerHeightRef.current;
    const contentHeight = contentHeightRef.current;

    // Need all measurements before we can scroll
    if (!layout || containerHeight === 0 || contentHeight === 0) return;

    // All measurements ready - perform the scroll
    pendingFocusIndexRef.current = null;

    const itemY = layout.y;
    const itemHeight = layout.height;
    const itemBottom = itemY + itemHeight;

    const currentScroll = currentScrollRef.current;
    const visibleTop = currentScroll;
    const visibleBottom = currentScroll + containerHeight;

    let newScroll = currentScroll;

    if (itemY < visibleTop) {
      newScroll = itemY;
    } else if (itemBottom > visibleBottom) {
      newScroll = itemBottom - containerHeight;
    }

    const maxScroll = Math.max(0, contentHeight - containerHeight);
    newScroll = Math.max(0, Math.min(newScroll, maxScroll));

    if (newScroll !== currentScroll) {
      currentScrollRef.current = newScroll;
      if (animated) {
        Animated.timing(scrollOffsetRef, {
          toValue: -newScroll,
          duration: 200,
          useNativeDriver: true,
        }).start();
      } else {
        scrollOffsetRef.setValue(-newScroll);
      }
    }
  }, [scrollOffsetRef]);

  const handleItemLayout = useCallback((index: number, y: number, height: number) => {
    itemLayoutsRef.current[index] = { y, height };
    // Check if this completes our pending scroll
    tryScrollToPendingFocus(false);
  }, [tryScrollToPendingFocus]);

  const handleContentLayout = useCallback((height: number) => {
    contentHeightRef.current = height;
    // Check if this completes our pending scroll
    tryScrollToPendingFocus(false);
  }, [tryScrollToPendingFocus]);

  const handleContainerLayout = useCallback((height: number) => {
    containerHeightRef.current = height;
    // Check if this completes our pending scroll
    tryScrollToPendingFocus(false);
  }, [tryScrollToPendingFocus]);

  const scrollToIndex = useCallback((index: number, animated: boolean) => {
    const layout = itemLayoutsRef.current[index];
    if (!layout) return;

    const containerHeight = containerHeightRef.current;
    const contentHeight = contentHeightRef.current;
    if (containerHeight === 0 || contentHeight === 0) return;

    const itemY = layout.y;
    const itemHeight = layout.height;
    const itemBottom = itemY + itemHeight;

    const currentScroll = currentScrollRef.current;
    const visibleTop = currentScroll;
    const visibleBottom = currentScroll + containerHeight;

    let newScroll = currentScroll;

    if (itemY < visibleTop) {
      newScroll = itemY;
    } else if (itemBottom > visibleBottom) {
      newScroll = itemBottom - containerHeight;
    }

    const maxScroll = Math.max(0, contentHeight - containerHeight);
    newScroll = Math.max(0, Math.min(newScroll, maxScroll));

    if (newScroll !== currentScroll) {
      currentScrollRef.current = newScroll;
      if (animated) {
        Animated.timing(scrollOffsetRef, {
          toValue: -newScroll,
          duration: 200,
          useNativeDriver: true,
        }).start();
      } else {
        scrollOffsetRef.setValue(-newScroll);
      }
    }
  }, [scrollOffsetRef]);

  const handleItemFocus = useCallback((index: number) => {
    if (!Platform.isTV) return;

    // Store pending focus - will scroll when all measurements are ready
    // or immediately if already ready
    pendingFocusIndexRef.current = index;
    tryScrollToPendingFocus(true);
  }, [tryScrollToPendingFocus]);

  const resolvedSubtitle = useMemo(() => {
    if (subtitle) {
      return subtitle;
    }
    if (!hasOptions) {
      return 'No tracks available';
    }
    if (selectedLabel) {
      return `Current selection: ${selectedLabel}`;
    }
    return 'Select a track';
  }, [hasOptions, selectedLabel, subtitle]);

  // Reset scroll position when modal opens/closes
  useEffect(() => {
    if (!visible) {
      scrollOffsetRef.setValue(0);
      currentScrollRef.current = 0;
      itemLayoutsRef.current = [];
      contentHeightRef.current = 0;
    }
  }, [visible, scrollOffsetRef]);

  const selectGuardRef = useRef(false);
  const withSelectGuard = useCallback((fn: () => void) => {
    if (!Platform.isTV) {
      fn();
      return;
    }
    if (selectGuardRef.current) {
      return;
    }
    selectGuardRef.current = true;
    try {
      fn();
    } finally {
      setTimeout(() => {
        selectGuardRef.current = false;
      }, 250);
    }
  }, []);

  const handleOptionSelect = useCallback(
    (id: string) => {
      console.log('[TrackSelectionModal] handleOptionSelect called', { id, guardActive: selectGuardRef.current });
      withSelectGuard(() => {
        console.log('[TrackSelectionModal] calling onSelect callback', { id });
        onSelect(id);
      });
    },
    [onSelect, withSelectGuard],
  );

  const handleClose = useCallback(() => {
    withSelectGuard(onClose);
  }, [onClose, withSelectGuard]);

  const handleSearchSubtitles = useCallback(() => {
    if (selectGuardRef.current) {
      return;
    }
    selectGuardRef.current = true;
    try {
      // Don't call onClose() here - just trigger the search callback.
      // The parent (Controls) will handle closing this modal without
      // briefly setting isModalOpen=false, which would cause double focus.
      onSearchSubtitles?.();
    } finally {
      setTimeout(() => {
        selectGuardRef.current = false;
      }, 250);
    }
  }, [onSearchSubtitles]);

  const onCloseRef = useRef(onClose);
  const removeInterceptorRef = useRef<(() => void) | null>(null);
  const canCloseWithBackRef = useRef(true);
  const backCloseDelayTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    // tvOS emits a spurious "blur/back" event when focus jumps into the modal; delay
    // enabling the back interceptor so that initial focus changes don't immediately close it.
    if (visible) {
      canCloseWithBackRef.current = false;
      if (backCloseDelayTimeoutRef.current) {
        clearTimeout(backCloseDelayTimeoutRef.current);
      }
      backCloseDelayTimeoutRef.current = setTimeout(() => {
        canCloseWithBackRef.current = true;
        backCloseDelayTimeoutRef.current = null;
      }, 300);
    } else {
      canCloseWithBackRef.current = true;
      if (backCloseDelayTimeoutRef.current) {
        clearTimeout(backCloseDelayTimeoutRef.current);
        backCloseDelayTimeoutRef.current = null;
      }
    }
  }, [visible]);

  useEffect(() => {
    if (!Platform.isTV) {
      return;
    }

    if (!visible) {
      if (removeInterceptorRef.current) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
      return;
    }

    let isHandling = false;
    let cleanupScheduled = false;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      if (!canCloseWithBackRef.current) {
        return true;
      }
      if (isHandling) {
        return true;
      }

      isHandling = true;
      onCloseRef.current();

      if (!cleanupScheduled) {
        cleanupScheduled = true;
        setTimeout(() => {
          if (removeInterceptorRef.current) {
            removeInterceptorRef.current();
            removeInterceptorRef.current = null;
          }
          isHandling = false;
        }, 750);
      }

      return true;
    });

    removeInterceptorRef.current = removeInterceptor;

    return () => {
      if (removeInterceptorRef.current === removeInterceptor && !cleanupScheduled) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
    };
  }, [visible]);

  useEffect(() => {
    return () => {
      if (removeInterceptorRef.current) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
      if (backCloseDelayTimeoutRef.current) {
        clearTimeout(backCloseDelayTimeoutRef.current);
        backCloseDelayTimeoutRef.current = null;
      }
    };
  }, []);

  // Determine which option should have initial focus
  const defaultFocusOptionId = useMemo(() => {
    if (selectedId && options.some((opt) => opt.id === selectedId)) {
      return selectedId;
    }
    return options[0]?.id ?? null;
  }, [selectedId, options]);

  // TV: measure component position so overlay can cover the full screen.
  // Renders invisible (opacity 0) first, measures, then shows at correct position.
  const tvOverlayRef = useRef<View>(null);
  const [tvOverlayReady, setTvOverlayReady] = useState(false);
  const [tvOffset, setTvOffset] = useState<{ x: number; y: number }>({ x: 0, y: 0 });

  useEffect(() => {
    if (Platform.isTV && visible) {
      setTvOverlayReady(false);
      requestAnimationFrame(() => {
        tvOverlayRef.current?.measureInWindow((x, y) => {
          if (x !== undefined && y !== undefined) {
            setTvOffset({ x, y });
          }
          setTvOverlayReady(true);
        });
      });
    }
    if (!visible) {
      setTvOverlayReady(false);
      setTvOffset({ x: 0, y: 0 });
    }
  }, [visible]);

  if (!visible) {
    return null;
  }

  const renderOption = (option: TrackSelectionOption, index: number) => {
    const isSelected = option.id === selectedId;
    const shouldHaveInitialFocus = option.id === defaultFocusOptionId;

    if (Platform.isTV) {
      // TV: Use native Pressable for focus management (SpatialNavigationFocusableView
      // doesn't work reliably when backing controls are disabled)
      return (
        <Pressable
          key={option.id}
          onPress={() => handleOptionSelect(option.id)}
          onFocus={() => handleItemFocus(index)}
          hasTVPreferredFocus={shouldHaveInitialFocus}>
          {({ focused }: { focused: boolean }) => (
            <View
              onLayout={(event) => {
                const { y, height } = event.nativeEvent.layout;
                handleItemLayout(index, y, height);
              }}
              style={[
                styles.optionItem,
                focused && !isSelected && styles.optionItemFocused,
                isSelected && !focused && styles.optionItemSelected,
                isSelected && focused && styles.optionItemSelectedFocused,
              ]}>
              <View style={styles.optionTextContainer}>
                <Text
                  style={[
                    styles.optionLabel,
                    focused && !isSelected && styles.optionLabelFocused,
                    isSelected && !focused && styles.optionLabelSelected,
                    isSelected && focused && styles.optionLabelSelectedFocused,
                  ]}>
                  {option.label}
                </Text>
                {option.description ? (
                  <Text
                    style={[
                      styles.optionDescription,
                      focused && !isSelected && styles.optionDescriptionFocused,
                      isSelected && !focused && styles.optionDescriptionSelected,
                      isSelected && focused && styles.optionDescriptionSelectedFocused,
                    ]}>
                    {option.description}
                  </Text>
                ) : null}
              </View>
              {isSelected ? (
                <View style={[styles.optionStatusBadge, focused && styles.optionStatusBadgeFocused]}>
                  <Text style={[styles.optionStatusText, focused && styles.optionStatusTextFocused]}>Selected</Text>
                </View>
              ) : null}
            </View>
          )}
        </Pressable>
      );
    }

    // Non-TV: Use Pressable
    return (
      <View
        key={option.id}
        onLayout={(event) => {
          const { height } = event.nativeEvent.layout;
          handleItemLayout(index, 0, height);
        }}>
        <Pressable onPress={() => handleOptionSelect(option.id)}>
          {({ pressed }) => (
            <View
              style={[
                styles.optionItem,
                pressed && styles.optionItemFocused,
                isSelected && !pressed && styles.optionItemSelected,
                isSelected && pressed && styles.optionItemSelectedFocused,
              ]}>
              <View style={styles.optionTextContainer}>
                <Text
                  style={[
                    styles.optionLabel,
                    pressed && !isSelected && styles.optionLabelFocused,
                    isSelected && !pressed && styles.optionLabelSelected,
                    isSelected && pressed && styles.optionLabelSelectedFocused,
                  ]}>
                  {option.label}
                </Text>
                {option.description ? (
                  <Text
                    style={[
                      styles.optionDescription,
                      pressed && !isSelected && styles.optionDescriptionFocused,
                      isSelected && !pressed && styles.optionDescriptionSelected,
                      isSelected && pressed && styles.optionDescriptionSelectedFocused,
                    ]}>
                    {option.description}
                  </Text>
                ) : null}
              </View>
              {isSelected ? (
                <View style={[styles.optionStatusBadge, pressed && styles.optionStatusBadgeFocused]}>
                  <Text style={[styles.optionStatusText, pressed && styles.optionStatusTextFocused]}>Selected</Text>
                </View>
              ) : null}
            </View>
          )}
        </Pressable>
      </View>
    );
  };

  const modalContent = (
    <View style={styles.overlay}>
      <Pressable
        style={styles.backdrop}
        onPress={Platform.isTV ? undefined : handleClose}
        focusable={false}
      />
      <View style={styles.modalContainer}>
        <View style={styles.modalHeader}>
          <Text style={styles.modalTitle}>{title}</Text>
          {resolvedSubtitle ? <Text style={styles.modalSubtitle}>{resolvedSubtitle}</Text> : null}
        </View>

        {Platform.isTV ? (
          // TV: Use native Pressable for focus management
          <View>
            {/* Options list with animated scrolling */}
            <View
              style={[styles.optionsScrollView, { overflow: 'hidden' }]}
              onLayout={(e) => handleContainerLayout(e.nativeEvent.layout.height)}>
              <Animated.View
                onLayout={(e) => handleContentLayout(e.nativeEvent.layout.height)}
                style={[styles.optionsList, { transform: [{ translateY: scrollOffsetRef }] }]}>
                {hasOptions ? (
                  options.map((option, index) => renderOption(option, index))
                ) : (
                  <View style={styles.emptyState}>
                    <Text style={styles.emptyStateText}>No embedded subtitles</Text>
                  </View>
                )}
              </Animated.View>
            </View>
            {/* Footer with optional Search Online and Close button */}
            <View style={styles.modalFooter}>
              {onSearchSubtitles && (
                <Pressable onPress={() => {
                  console.log('[TrackSelectionModal] Search Online pressed');
                  handleSearchSubtitles();
                }}>
                  {({ focused }: { focused: boolean }) => (
                    <View style={[styles.closeButton, styles.searchButton, focused && styles.closeButtonFocused]}>
                      <Text style={[styles.closeButtonText, focused && styles.closeButtonTextFocused]}>
                        Search Online
                      </Text>
                    </View>
                  )}
                </Pressable>
              )}
              <Pressable
                onPress={handleClose}
                hasTVPreferredFocus={!hasOptions && !onSearchSubtitles}>
                {({ focused }: { focused: boolean }) => (
                  <View style={[styles.closeButton, focused && styles.closeButtonFocused]}>
                    <Text style={[styles.closeButtonText, focused && styles.closeButtonTextFocused]}>Close</Text>
                  </View>
                )}
              </Pressable>
            </View>
          </View>
        ) : (
          <>
            <ScrollView style={styles.optionsScrollView} contentContainerStyle={styles.optionsList}>
              {hasOptions ? (
                options.map((option, index) => renderOption(option, index))
              ) : (
                <View style={styles.emptyState}>
                  <Text style={styles.emptyStateText}>No embedded subtitles</Text>
                </View>
              )}
            </ScrollView>
            <View style={styles.modalFooter}>
              {onSearchSubtitles && (
                <Pressable onPress={handleSearchSubtitles}>
                  {({ pressed }) => (
                    <View style={[styles.closeButton, styles.searchButton, pressed && styles.closeButtonFocused]}>
                      <Text style={[styles.closeButtonText, pressed && styles.closeButtonTextFocused]}>
                        Search Online
                      </Text>
                    </View>
                  )}
                </Pressable>
              )}
              <Pressable onPress={handleClose}>
                {({ pressed }) => (
                  <View style={[styles.closeButton, pressed && styles.closeButtonFocused]}>
                    <Text style={[styles.closeButtonText, pressed && styles.closeButtonTextFocused]}>Close</Text>
                  </View>
                )}
              </Pressable>
            </View>
          </>
        )}
      </View>
    </View>
  );

  // TV: pseudo-modal (no native Modal window) — avoids nested Modal focus issues.
  // Renders invisible first, measures position, then shows at correct full-screen offset.
  if (Platform.isTV) {
    return (
      <View
        ref={tvOverlayRef}
        style={{
          position: 'absolute',
          top: -tvOffset.y,
          left: -tvOffset.x,
          width: screenWidth,
          height: screenHeight,
          zIndex: 1000,
          opacity: tvOverlayReady ? 1 : 0,
        }}
        pointerEvents="box-none">
        {modalContent}
      </View>
    );
  }

  // Non-TV: keep native Modal
  return (
    <Modal
      visible={visible}
      animationType="fade"
      transparent
      onRequestClose={handleClose}
      supportedOrientations={['portrait', 'portrait-upside-down', 'landscape', 'landscape-left', 'landscape-right']}
      hardwareAccelerated>
      <SpatialNavigationRoot isActive={visible}>
        {modalContent}
      </SpatialNavigationRoot>
    </Modal>
  );
};

const createStyles = (theme: NovaTheme, screenWidth: number) => {
  // Responsive breakpoints
  const isNarrow = screenWidth < 400;
  const isMedium = screenWidth >= 400 && screenWidth < 600;

  // Responsive width: fill more on narrow screens
  const modalWidth = isNarrow ? '95%' : isMedium ? '90%' : '80%';
  const modalMaxWidth = isNarrow ? 400 : 720;

  // Responsive padding - minimize on narrow screens so cards fill width
  const horizontalPadding = isNarrow ? theme.spacing.sm : theme.spacing.xl;
  const itemPadding = isNarrow ? theme.spacing.md : theme.spacing.lg;
  const itemMarginHorizontal = isNarrow ? 0 : isMedium ? theme.spacing.sm : theme.spacing.xl;
  const listPadding = isNarrow ? theme.spacing.xs : isMedium ? theme.spacing.md : theme.spacing['3xl'];

  return StyleSheet.create({
    overlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    backdrop: {
      ...StyleSheet.absoluteFillObject,
    },
    modalContainer: {
      width: modalWidth,
      maxWidth: modalMaxWidth,
      maxHeight: '85%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: isNarrow ? theme.radius.lg : theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
    },
    modalHeader: {
      paddingHorizontal: horizontalPadding,
      paddingVertical: isNarrow ? theme.spacing.lg : theme.spacing.xl,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      gap: theme.spacing.xs,
    },
    modalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      fontSize: isNarrow ? 18 : theme.typography.title.xl.fontSize,
    },
    modalSubtitle: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    optionsScrollView: {
      flexGrow: 1,
      flexShrink: 1,
    },
    optionsList: {
      paddingHorizontal: listPadding,
      paddingVertical: isNarrow ? theme.spacing.lg : theme.spacing['2xl'],
    },
    optionItem: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      paddingVertical: itemPadding,
      paddingHorizontal: isNarrow ? theme.spacing.md : theme.spacing.xl,
      borderRadius: theme.radius.md,
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      gap: isNarrow ? theme.spacing.md : theme.spacing.lg,
      marginHorizontal: itemMarginHorizontal,
      marginBottom: theme.spacing.md,
    },
    optionItemFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    optionItemSelected: {
      backgroundColor: 'rgba(255, 255, 255, 0.12)',
      borderColor: theme.colors.accent.primary,
    },
    optionItemSelectedFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.text.primary,
    },
    optionTextContainer: {
      flex: 1,
      gap: theme.spacing.xs,
    },
    optionLabel: {
      ...theme.typography.body.lg,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    optionLabelFocused: {
      color: theme.colors.text.inverse,
      fontWeight: '600',
    },
    optionLabelSelected: {
      color: '#FFFFFF',
    },
    optionLabelSelectedFocused: {
      color: theme.colors.text.inverse,
      fontWeight: '600',
    },
    optionDescription: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    optionDescriptionFocused: {
      color: theme.colors.text.inverse,
    },
    optionDescriptionSelected: {
      color: 'rgba(255, 255, 255, 0.85)',
    },
    optionDescriptionSelectedFocused: {
      color: theme.colors.text.inverse,
    },
    optionStatusBadge: {
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
      backgroundColor: 'rgba(0, 0, 0, 0.3)',
    },
    optionStatusBadgeFocused: {
      backgroundColor: 'rgba(0, 0, 0, 0.2)',
    },
    optionStatusText: {
      ...theme.typography.body.sm,
      color: '#FFFFFF',
      fontWeight: '500',
      textTransform: 'uppercase',
      letterSpacing: 0.5,
    },
    optionStatusTextFocused: {
      color: '#FFFFFF',
    },
    emptyState: {
      padding: theme.spacing['2xl'],
      alignItems: 'center',
      justifyContent: 'center',
    },
    emptyStateText: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    modalFooter: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      paddingHorizontal: horizontalPadding,
      paddingVertical: isNarrow ? theme.spacing.md : theme.spacing.lg,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'center',
      justifyContent: 'center',
      gap: theme.spacing.md,
    },
    closeButton: {
      minWidth: isNarrow ? 100 : 150,
      paddingHorizontal: isNarrow ? theme.spacing.lg : theme.spacing.xl,
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.surface,
      alignItems: 'center',
    },
    searchButton: {
      borderColor: theme.colors.accent.primary,
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
};
