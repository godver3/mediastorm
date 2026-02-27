import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Animated, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, useWindowDimensions, View } from 'react-native';

import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  SpatialNavigationFocusableView,
  DefaultFocus,
} from '@/services/tv-navigation';
import { TVFocusGuard } from '@/components/tv-focus/TVFocusGuard';
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
  /** Use native Modal on TV for proper focus trapping and back button (default: false for player compatibility) */
  useNativeModal?: boolean;
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
  useNativeModal = false,
}) => {
  const theme = useTheme();
  const { width: screenWidth, height: screenHeight } = useWindowDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth, screenHeight), [theme, screenWidth, screenHeight]);
  const hasOptions = options.length > 0;

  const selectedLabel = useMemo(() => options.find((option) => option.id === selectedId)?.label, [options, selectedId]);

  // Manual scroll handling for TV platforms using animated transform
  const scrollOffsetRef = useRef(new Animated.Value(0)).current;
  const itemLayoutsRef = useRef<{ y: number; height: number }[]>([]);
  const containerHeightRef = useRef(0);
  const currentScrollRef = useRef(0);
  const pendingFocusIndexRef = useRef<number | null>(null);

  const contentHeightRef = useRef(0);

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

  // Viewport-height-based scale for TV
  const tvS = Platform.isTV ? screenHeight / TV_REFERENCE_HEIGHT : 1;

  // Calculate a fixed height for the options area on TV to prevent collapsing
  const tvOptionsHeight = useMemo(() => {
    if (!Platform.isTV) return undefined;
    // Cap at a reasonable portion of the screen
    return Math.min(Math.round(720 * tvS), screenHeight * 0.7);
  }, [screenHeight, tvS]);

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

  // Try to scroll to pending focus if all measurements are ready
  const tryScrollToPendingFocus = useCallback((animated: boolean) => {
    const index = pendingFocusIndexRef.current;
    if (index === null) return;

    const containerHeight = containerHeightRef.current;
    const contentHeight = contentHeightRef.current;

    // Need all measurements before we can scroll
    if (containerHeight === 0 || contentHeight === 0) return;

    // Item spacing (marginBottom)
    const itemSpacing = Platform.isTV ? Math.round(theme.spacing.lg * tvS) : theme.spacing.md;
    // Vertical padding from optionsList
    const listPaddingVertical = Math.round(theme.spacing['2xl'] * tvS);

    // Calculate cumulative Y position from measured layouts (more robust for TV)
    let itemY = listPaddingVertical;
    for (let i = 0; i < index; i++) {
      const layout = itemLayoutsRef.current[i];
      if (layout) {
        itemY += layout.height + itemSpacing;
      }
    }

    const layout = itemLayoutsRef.current[index];
    if (!layout) return;

    // All measurements ready - perform the scroll
    pendingFocusIndexRef.current = null;

    const itemHeight = layout.height;
    const itemBottom = itemY + itemHeight + itemSpacing;

    const currentScroll = currentScrollRef.current;
    const visibleTop = currentScroll;
    const visibleBottom = currentScroll + containerHeight;

    let newScroll = currentScroll;

    // Keep item in view with some padding
    const padding = 40 * tvS;
    if (itemY < visibleTop + padding) {
      newScroll = Math.max(0, itemY - padding);
    } else if (itemBottom > visibleBottom - padding) {
      newScroll = itemBottom - containerHeight + padding;
    }

    const maxScroll = Math.max(0, contentHeight - containerHeight);
    
    // For the last item, ensure we scroll to the very end to show bottom padding
    if (index === options.length - 1 && newScroll > maxScroll - 100 * tvS) {
      newScroll = maxScroll;
    }
    
    newScroll = Math.max(0, Math.min(newScroll, maxScroll));

    if (Math.abs(newScroll - currentScroll) > 1) {
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
  }, [scrollOffsetRef, theme.spacing.md, tvS]);

  const handleItemLayout = useCallback((index: number, _y: number, height: number) => {
    // We only care about height, we'll calculate Y ourselves to be robust
    itemLayoutsRef.current[index] = { y: 0, height };
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

  const handleItemFocus = useCallback((index: number) => {
    if (!Platform.isTV) return;

    // Store pending focus - will scroll when all measurements are ready
    // or immediately if already ready
    pendingFocusIndexRef.current = index;
    tryScrollToPendingFocus(true);
  }, [tryScrollToPendingFocus]);

  const scrollToIndex = useCallback((index: number, animated: boolean) => {
    const containerHeight = containerHeightRef.current;
    const contentHeight = contentHeightRef.current;
    if (containerHeight === 0 || contentHeight === 0) return;

    // Item spacing (marginBottom)
    const itemSpacing = Platform.isTV ? Math.round(theme.spacing.lg * tvS) : theme.spacing.md;
    // Vertical padding from optionsList
    const listPaddingVertical = Math.round(theme.spacing['2xl'] * tvS);

    // Calculate cumulative Y position
    let itemY = listPaddingVertical;
    for (let i = 0; i < index; i++) {
      const layout = itemLayoutsRef.current[i];
      if (layout) {
        itemY += layout.height + itemSpacing;
      }
    }

    const layout = itemLayoutsRef.current[index];
    if (!layout) return;

    const itemHeight = layout.height;
    const itemBottom = itemY + itemHeight + itemSpacing;

    const currentScroll = currentScrollRef.current;
    const visibleTop = currentScroll;
    const visibleBottom = currentScroll + containerHeight;

    let newScroll = currentScroll;

    const padding = 40 * tvS;
    if (itemY < visibleTop + padding) {
      newScroll = Math.max(0, itemY - padding);
    } else if (itemBottom > visibleBottom - padding) {
      newScroll = itemBottom - containerHeight + padding;
    }

    const maxScroll = Math.max(0, contentHeight - containerHeight);
    
    // For the last item, ensure we scroll to the very end to show bottom padding
    if (index === options.length - 1 && newScroll > maxScroll - 100 * tvS) {
      newScroll = maxScroll;
    }
    
    newScroll = Math.max(0, Math.min(newScroll, maxScroll));

    if (Math.abs(newScroll - currentScroll) > 1) {
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
  }, [scrollOffsetRef, theme.spacing.md, tvS]);

  if (!visible) {
    return null;
  }

  const renderOption = (option: TrackSelectionOption, index: number) => {
    const isSelected = option.id === selectedId;
    const shouldHaveInitialFocus = option.id === defaultFocusOptionId;

    if (Platform.isTV) {
      // TV: Use SpatialNavigationFocusableView for proper focus management
      const content = (
        <SpatialNavigationFocusableView
          onSelect={() => handleOptionSelect(option.id)}
          onFocus={() => handleItemFocus(index)}>
          {({ isFocused }: { isFocused: boolean }) => (
            <View
              onLayout={(event) => {
                const { y, height } = event.nativeEvent.layout;
                handleItemLayout(index, y, height);
              }}
              style={[
                styles.optionItem,
                isFocused && !isSelected && styles.optionItemFocused,
                isSelected && !isFocused && styles.optionItemSelected,
                isSelected && isFocused && styles.optionItemSelectedFocused,
              ]}>
              <View style={styles.optionTextContainer}>
                <Text
                  style={[
                    styles.optionLabel,
                    isFocused && !isSelected && styles.optionLabelFocused,
                    isSelected && !isFocused && styles.optionLabelSelected,
                    isSelected && isFocused && styles.optionLabelSelectedFocused,
                  ]}>
                  {option.label}
                </Text>
                {option.description ? (
                  <Text
                    style={[
                      styles.optionDescription,
                      isFocused && !isSelected && styles.optionDescriptionFocused,
                      isSelected && !isFocused && styles.optionDescriptionSelected,
                      isSelected && isFocused && styles.optionDescriptionSelectedFocused,
                    ]}>
                    {option.description}
                  </Text>
                ) : null}
              </View>
              {isSelected ? (
                <View style={[styles.optionStatusBadge, isFocused && styles.optionStatusBadgeFocused]}>
                  <Text style={[styles.optionStatusText, isFocused && styles.optionStatusTextFocused]}>Selected</Text>
                </View>
              ) : null}
            </View>
          )}
        </SpatialNavigationFocusableView>
      );

      if (shouldHaveInitialFocus) {
        return <DefaultFocus key={option.id}>{content}</DefaultFocus>;
      }
      return <View key={option.id}>{content}</View>;
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
        android_disableSound={true}
      />
      <TVFocusGuard trapFocus={['up', 'down', 'left', 'right']}>
        <View style={styles.modalContainer}>
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>{title}</Text>
            {resolvedSubtitle ? <Text style={styles.modalSubtitle}>{resolvedSubtitle}</Text> : null}
          </View>

          {Platform.isTV ? (
            // TV: Use SpatialNavigationFocusableView for proper focus management
            <SpatialNavigationNode orientation="vertical">
              {/* Options list with animated scrolling */}
              <SpatialNavigationNode orientation="vertical">
                <View
                  style={[
                    styles.optionsScrollView,
                    {
                      overflow: 'hidden',
                      maxHeight: tvOptionsHeight,
                    },
                  ]}
                  onLayout={(e) => handleContainerLayout(e.nativeEvent.layout.height)}>
                  <Animated.View
                    onLayout={(e) => handleContentLayout(e.nativeEvent.layout.height)}
                    style={[styles.optionsList, { transform: [{ translateY: scrollOffsetRef }], width: '100%' }]}>
                    {hasOptions ? (
                      options.map((option, index) => renderOption(option, index))
                    ) : (
                      <View style={styles.emptyState}>
                        <Text style={styles.emptyStateText}>No embedded subtitles</Text>
                      </View>
                    )}
                  </Animated.View>
                </View>
              </SpatialNavigationNode>
              {/* Footer with optional Search Online and Close button */}
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.modalFooter}>
                  {onSearchSubtitles && (
                    <SpatialNavigationFocusableView
                      onSelect={() => {
                        console.log('[TrackSelectionModal] Search Online pressed');
                        handleSearchSubtitles();
                      }}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <View style={[styles.closeButton, styles.searchButton, isFocused && styles.closeButtonFocused]}>
                          <Text style={[styles.closeButtonText, isFocused && styles.closeButtonTextFocused]}>
                            Search Online
                          </Text>
                        </View>
                      )}
                    </SpatialNavigationFocusableView>
                  )}
                  <SpatialNavigationFocusableView onSelect={handleClose}>
                    {({ isFocused }: { isFocused: boolean }) => {
                      const button = (
                        <View style={[styles.closeButton, isFocused && styles.closeButtonFocused]}>
                          <Text style={[styles.closeButtonText, isFocused && styles.closeButtonTextFocused]}>Close</Text>
                        </View>
                      );
                      return !hasOptions && !onSearchSubtitles ? <DefaultFocus>{button}</DefaultFocus> : button;
                    }}
                  </SpatialNavigationFocusableView>
                </View>
              </SpatialNavigationNode>
            </SpatialNavigationNode>
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
      </TVFocusGuard>
    </View>
  );

  // TV with native Modal: proper focus trapping and back button handling.
  // Used on details page where there's no parent Modal.
  // Only mount when visible — Android TV focus system breaks with hidden native Modals.
  if (Platform.isTV && useNativeModal) {
    if (!visible) return null;
    return (
      <Modal
        visible
        animationType="fade"
        transparent
        onRequestClose={handleClose}
        hardwareAccelerated>
        <SpatialNavigationRoot isActive={visible}>
          {modalContent}
        </SpatialNavigationRoot>
      </Modal>
    );
  }

  // TV: pseudo-modal (no native Modal window) — avoids nested Modal focus issues.
  // Used in player controls where native Modal may conflict with the player overlay.
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
        <SpatialNavigationRoot isActive={visible}>
          {modalContent}
        </SpatialNavigationRoot>
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

const TV_REFERENCE_HEIGHT = 1080;

const createStyles = (theme: NovaTheme, screenWidth: number, screenHeight: number) => {
  // Responsive breakpoint for very small mobile screens
  const isNarrow = screenWidth < 400;

  // Viewport-height-based scale for TV — consistent across tvOS and Android TV
  const tvS = Platform.isTV ? screenHeight / TV_REFERENCE_HEIGHT : 1;

  // Responsive width: fill more on narrow screens
  const modalWidth = Platform.isTV ? '90%' : (isNarrow ? '95%' : '90%');
  const modalMaxWidth = Platform.isTV ? Math.round(1800 * tvS) : (isNarrow ? 450 : 500);

  // Responsive padding - TV uses tvS scaling, mobile uses compact values
  const horizontalPadding = Platform.isTV ? Math.round(theme.spacing.xl * tvS) : (isNarrow ? theme.spacing.sm : theme.spacing.md);
  const verticalPadding = Platform.isTV ? Math.round(theme.spacing['2xl'] * tvS) : (isNarrow ? theme.spacing.md : theme.spacing.lg);
  const itemPadding = Platform.isTV ? Math.round(theme.spacing.xl * tvS) : (isNarrow ? theme.spacing.sm : theme.spacing.md);
  const itemMarginHorizontal = Platform.isTV ? Math.round(theme.spacing.xl * tvS) : (isNarrow ? 0 : theme.spacing.xs);
  const listPadding = Platform.isTV ? Math.round(theme.spacing['3xl'] * tvS) : (isNarrow ? theme.spacing.xs : theme.spacing.sm);

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
      minWidth: Platform.isTV ? Math.round(1000 * tvS) : (isNarrow ? 280 : 300),
      minHeight: Platform.isTV ? Math.round(500 * tvS) : (isNarrow ? 200 : 240),
      maxHeight: Math.round(screenHeight * (Platform.isTV ? 0.9 : 0.8)),
      backgroundColor: theme.colors.background.elevated,
      borderRadius: Platform.isTV ? Math.round(theme.radius.xl * tvS) : theme.radius.lg,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
      flexShrink: 1,
    },
    modalHeader: {
      paddingHorizontal: horizontalPadding,
      paddingVertical: verticalPadding,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      gap: Platform.isTV ? Math.round(theme.spacing.xs * tvS) : theme.spacing.xs,
    },
    modalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      fontSize: Platform.isTV ? Math.round(36 * tvS) : (isNarrow ? 16 : 18),
    },
    modalSubtitle: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontSize: Platform.isTV ? Math.round(18 * tvS) : (isNarrow ? 11 : 12),
    },
    optionsScrollView: {
      flexGrow: 1,
      flexShrink: 1,
    },
    optionsList: {
      paddingHorizontal: listPadding,
      paddingVertical: Platform.isTV ? Math.round(theme.spacing['2xl'] * tvS) : (isNarrow ? theme.spacing.sm : theme.spacing.md),
    },
    optionItem: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      paddingVertical: itemPadding,
      paddingHorizontal: Platform.isTV ? Math.round(theme.spacing['2xl'] * tvS) : (isNarrow ? theme.spacing.sm : theme.spacing.md),
      borderRadius: Platform.isTV ? Math.round(theme.radius.md * tvS) : theme.radius.md,
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      gap: Platform.isTV ? Math.round(theme.spacing.lg * tvS) : (isNarrow ? theme.spacing.xs : theme.spacing.sm),
      marginHorizontal: itemMarginHorizontal,
      marginBottom: Platform.isTV ? Math.round(theme.spacing.lg * tvS) : theme.spacing.sm,
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
      fontSize: Platform.isTV ? Math.round(24 * tvS) : (isNarrow ? 14 : 15),
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
      fontSize: Platform.isTV ? Math.round(16 * tvS) : (isNarrow ? 11 : 12),
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
      paddingHorizontal: Platform.isTV ? Math.round(theme.spacing.md * tvS) : theme.spacing.sm,
      paddingVertical: Platform.isTV ? Math.round(theme.spacing.xs * tvS) : 2,
      borderRadius: Platform.isTV ? Math.round(theme.radius.sm * tvS) : theme.radius.sm,
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
      paddingVertical: Platform.isTV ? Math.round(theme.spacing.lg * tvS) : (isNarrow ? theme.spacing.sm : theme.spacing.md),
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'center',
      justifyContent: 'center',
      gap: Platform.isTV ? Math.round(theme.spacing.md * tvS) : theme.spacing.sm,
    },
    closeButton: {
      minWidth: Platform.isTV ? Math.round(150 * tvS) : (isNarrow ? 80 : 100),
      paddingHorizontal: Platform.isTV ? Math.round(theme.spacing.xl * tvS) : (isNarrow ? theme.spacing.md : theme.spacing.lg),
      paddingVertical: Platform.isTV ? Math.round(theme.spacing.md * tvS) : theme.spacing.sm,
      borderRadius: Platform.isTV ? Math.round(theme.radius.md * tvS) : theme.radius.md,
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
      fontSize: Platform.isTV ? theme.typography.body.md.fontSize : 14,
    },
    closeButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
  });
};
