import React, { useCallback, useEffect, useMemo, useRef } from 'react';
import { Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
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
  const styles = useMemo(() => createStyles(theme), [theme]);
  const hasOptions = options.length > 0;

  const selectedLabel = useMemo(() => options.find((option) => option.id === selectedId)?.label, [options, selectedId]);

  // Manual scroll handling for TV platforms
  const scrollViewRef = useRef<ScrollView>(null);
  const itemLayoutsRef = useRef<{ y: number; height: number }[]>([]);

  const handleItemLayout = useCallback((index: number, y: number, height: number) => {
    itemLayoutsRef.current[index] = { y, height };
  }, []);

  const handleItemFocus = useCallback((index: number) => {
    if (!Platform.isTV) return;

    // Calculate cumulative Y position from measured layouts
    let cumulativeY = 0;
    for (let i = 0; i < index; i++) {
      const layout = itemLayoutsRef.current[i];
      if (layout) {
        cumulativeY += layout.height;
      }
    }

    // Scroll to position the focused item with some offset from top
    const scrollOffset = Math.max(0, cumulativeY - 50);
    scrollViewRef.current?.scrollTo({ y: scrollOffset, animated: true });
  }, []);

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
      withSelectGuard(() => onSelect(id));
    },
    [onSelect, withSelectGuard],
  );

  const handleClose = useCallback(() => {
    withSelectGuard(onClose);
  }, [onClose, withSelectGuard]);

  const handleSearchSubtitles = useCallback(() => {
    withSelectGuard(() => {
      onClose();
      onSearchSubtitles?.();
    });
  }, [onClose, onSearchSubtitles, withSelectGuard]);

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

  if (!visible) {
    return null;
  }

  const renderOption = (option: TrackSelectionOption, index: number) => {
    const isSelected = option.id === selectedId;
    const focusKey = `${focusKeyPrefix}-${option.id}`;
    const optionContent = (
      <SpatialNavigationFocusableView
        focusKey={focusKey}
        onSelect={() => handleOptionSelect(option.id)}
        onFocus={() => handleItemFocus(index)}
      >
        {({ isFocused }: { isFocused: boolean }) => (
          <View
            onLayout={(event) => {
              const { height } = event.nativeEvent.layout;
              handleItemLayout(index, 0, height);
            }}
          >
            <Pressable
              onPress={() => handleOptionSelect(option.id)}
              style={[
                styles.optionItem,
                isFocused && !isSelected && styles.optionItemFocused,
                isSelected && !isFocused && styles.optionItemSelected,
                isSelected && isFocused && styles.optionItemSelectedFocused,
              ]}
              tvParallaxProperties={{ enabled: false }}
            >
              <View style={styles.optionTextContainer}>
                <Text
                  style={[
                    styles.optionLabel,
                    isFocused && !isSelected && styles.optionLabelFocused,
                    isSelected && !isFocused && styles.optionLabelSelected,
                    isSelected && isFocused && styles.optionLabelSelectedFocused,
                  ]}
                  numberOfLines={1}
                  ellipsizeMode="tail"
                >
                  {option.label}
                </Text>
                {option.description ? (
                  <Text
                    style={[
                      styles.optionDescription,
                      isFocused && !isSelected && styles.optionDescriptionFocused,
                      isSelected && !isFocused && styles.optionDescriptionSelected,
                      isSelected && isFocused && styles.optionDescriptionSelectedFocused,
                    ]}
                    numberOfLines={2}
                    ellipsizeMode="tail"
                  >
                    {option.description}
                  </Text>
                ) : null}
              </View>
              {isSelected ? (
                <View style={[styles.optionStatusBadge, isFocused && styles.optionStatusBadgeFocused]}>
                  <Text style={[styles.optionStatusText, isFocused && styles.optionStatusTextFocused]}>Selected</Text>
                </View>
              ) : null}
            </Pressable>
          </View>
        )}
      </SpatialNavigationFocusableView>
    );

    const shouldDefaultFocus = selectedId ? isSelected : index === 0;
    if (shouldDefaultFocus) {
      return <DefaultFocus key={option.id}>{optionContent}</DefaultFocus>;
    }
    return <React.Fragment key={option.id}>{optionContent}</React.Fragment>;
  };

  return (
    <Modal
      visible={visible}
      animationType="fade"
      transparent
      onRequestClose={handleClose}
      supportedOrientations={['portrait', 'portrait-upside-down', 'landscape', 'landscape-left', 'landscape-right']}
      hardwareAccelerated
    >
      <SpatialNavigationRoot isActive={visible}>
        <View style={styles.overlay}>
          {/* Keep backdrop Pressable on TV as native focus anchor for spatial navigation */}
          <Pressable style={styles.backdrop} onPress={handleClose} tvParallaxProperties={{ enabled: false }} />
          <View style={styles.modalContainer}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>{title}</Text>
              {resolvedSubtitle ? <Text style={styles.modalSubtitle}>{resolvedSubtitle}</Text> : null}
            </View>

            <SpatialNavigationNode orientation="vertical">
              <ScrollView
                ref={scrollViewRef}
                style={styles.optionsScrollView}
                contentContainerStyle={styles.optionsList}
                scrollEnabled={!Platform.isTV}
              >
                {hasOptions ? (
                  options.map((option, index) => renderOption(option, index))
                ) : (
                  <View style={styles.emptyState}>
                    <Text style={styles.emptyStateText}>No embedded subtitles</Text>
                  </View>
                )}
                {onSearchSubtitles && (
                  <SpatialNavigationFocusableView
                    focusKey={`${focusKeyPrefix}-search-subtitles`}
                    onSelect={handleSearchSubtitles}
                  >
                    {({ isFocused }: { isFocused: boolean }) => (
                      <Pressable
                        onPress={handleSearchSubtitles}
                        style={[styles.optionItem, styles.searchOption, isFocused && styles.optionItemFocused]}
                        tvParallaxProperties={{ enabled: false }}
                      >
                        <View style={styles.optionTextContainer}>
                          <Text style={[styles.optionLabel, isFocused && styles.optionLabelFocused]}>
                            Search for Subtitles...
                          </Text>
                          <Text style={[styles.optionDescription, isFocused && styles.optionDescriptionFocused]}>
                            Find subtitles online
                          </Text>
                        </View>
                      </Pressable>
                    )}
                  </SpatialNavigationFocusableView>
                )}
              </ScrollView>
            </SpatialNavigationNode>

            <View style={styles.modalFooter}>
              <SpatialNavigationFocusableView focusKey={`${focusKeyPrefix}-close`} onSelect={handleClose}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <Pressable
                    onPress={handleClose}
                    style={[styles.closeButton, isFocused && styles.closeButtonFocused]}
                    tvParallaxProperties={{ enabled: false }}
                  >
                    <Text style={[styles.closeButtonText, isFocused && styles.closeButtonTextFocused]}>Close</Text>
                  </Pressable>
                )}
              </SpatialNavigationFocusableView>
            </View>
          </View>
        </View>
      </SpatialNavigationRoot>
    </Modal>
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
      ...StyleSheet.absoluteFillObject,
    },
    modalContainer: {
      width: '80%',
      maxWidth: 720,
      maxHeight: '80%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
    },
    modalHeader: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.xl,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      gap: theme.spacing.xs,
    },
    modalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
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
      paddingHorizontal: theme.spacing['3xl'],
      paddingVertical: theme.spacing['2xl'],
    },
    optionItem: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      paddingVertical: theme.spacing.lg,
      paddingHorizontal: theme.spacing.xl,
      borderRadius: theme.radius.md,
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.lg,
      marginHorizontal: theme.spacing.xl,
      marginBottom: theme.spacing.md,
    },
    searchOption: {
      marginTop: theme.spacing.lg,
      borderColor: theme.colors.accent.primary,
      borderWidth: 1,
      backgroundColor: 'rgba(255, 255, 255, 0.04)',
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
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.lg,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'center',
    },
    closeButton: {
      minWidth: 200,
      paddingHorizontal: theme.spacing['2xl'],
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.surface,
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
