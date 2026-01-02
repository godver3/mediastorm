/**
 * Manual search result selection functionality for the details screen
 */

import FocusablePressable from '@/components/FocusablePressable';
import { useUnplayableReleases } from '@/hooks/useUnplayableReleases';
import { apiService, type DebridHealthCheck, type NZBHealthCheck, type NZBResult } from '@/services/api';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useCallback, useMemo, useRef, useState } from 'react';
import {
  Animated,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
  type StyleProp,
  type ViewStyle,
  type TextStyle,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { formatFileSize, formatPublishDate, getResultKey } from './utils';

type ManualResultHealthStatus = 'checking' | 'healthy' | 'unhealthy' | 'error' | 'not_applicable' | 'stream_error';

interface ManualResultHealthState {
  state: ManualResultHealthStatus;
  details?: NZBHealthCheck;
  debridDetails?: DebridHealthCheck;
  error?: string;
}

const isResultUnplayable = (health?: ManualResultHealthState) =>
  health?.state === 'unhealthy' || health?.state === 'error' || health?.state === 'stream_error';

interface ManualSelectionProps {
  visible: boolean;
  loading: boolean;
  error: string | null;
  results: NZBResult[];
  healthChecks: Record<string, ManualResultHealthState>;
  onClose: () => void;
  onSelect: (result: NZBResult) => void;
  onCheckHealth: (result: NZBResult) => void;
  theme: NovaTheme;
  isWebTouch: boolean;
  isMobile: boolean;
  maxHeight: number;
  demoMode?: boolean;
}

export const ManualSelection = ({
  visible,
  loading,
  error,
  results,
  healthChecks,
  onClose,
  onSelect,
  onCheckHealth,
  theme,
  isWebTouch,
  isMobile,
  maxHeight,
  demoMode,
}: ManualSelectionProps) => {
  const styles = useMemo(() => createManualSelectionStyles(theme), [theme]);
  const safeAreaInsets = useSafeAreaInsets();
  const scrollViewRef = useRef<ScrollView>(null);
  const scrollOffsetRef = useRef(new Animated.Value(0)).current;
  const itemLayoutsRef = useRef<{ y: number; height: number }[]>([]);
  const showMobileIOSCloseButton = !Platform.isTV && isMobile && Platform.OS === 'ios';

  // Filter out releases that have been marked as unplayable
  const { isUnplayableByTitle, loading: loadingUnplayable } = useUnplayableReleases();

  const filteredResults = useMemo(() => {
    if (!results) {
      return []; // Handle null/undefined results
    }
    if (loadingUnplayable) {
      return results; // Show all while loading unplayable list
    }
    return results.filter((result) => !isUnplayableByTitle(result.title));
  }, [results, isUnplayableByTitle, loadingUnplayable]);

  const handleItemLayout = useCallback((index: number, y: number, height: number) => {
    itemLayoutsRef.current[index] = { y, height };
  }, []);

  const handleItemFocus = useCallback(
    (index: number) => {
      if (!Platform.isTV) return;

      console.log(`[ManualSelection] Focusing item ${index}`);

      // Calculate cumulative Y position from measured layouts
      let cumulativeY = 0;
      for (let i = 0; i < index; i++) {
        const layout = itemLayoutsRef.current[i];
        if (layout) {
          cumulativeY += layout.height;
        }
      }

      console.log(`[ManualSelection] Calculated cumulative Y: ${cumulativeY}`);

      // Scroll to position the focused item with some offset from top
      const scrollOffset = Math.max(0, cumulativeY - 100); // 100px from top
      console.log(`[ManualSelection] Scrolling to: ${scrollOffset}`);

      // Use animated transform for TV (bypasses native scroll)
      Animated.timing(scrollOffsetRef, {
        toValue: -scrollOffset,
        duration: 200,
        useNativeDriver: true,
      }).start();
    },
    [scrollOffsetRef],
  );

  const renderManualResultContent = useCallback(
    (result: NZBResult, isFocused: boolean) => {
      const key = getResultKey(result);
      const healthState = healthChecks[key];
      const isUnplayable = isResultUnplayable(healthState);
      const serviceType = (result.serviceType ?? 'usenet').toLowerCase() as 'usenet' | 'debrid';
      const serviceLabel = serviceType === 'debrid' ? 'D' : 'U';

      let statusLabel: string | null = null;
      if (healthState) {
        switch (healthState.state) {
          case 'checking':
            statusLabel =
              serviceType === 'debrid'
                ? 'Checking cache status…'
                : demoMode
                  ? 'Checking health…'
                  : 'Checking Usenet health…';
            break;
          case 'healthy': {
            const actionText = Platform.isTV ? 'Select to play' : 'Tap to play';
            statusLabel = serviceType === 'debrid' ? `Cached • ${actionText}` : `Healthy • ${actionText}`;
            break;
          }
          case 'unhealthy': {
            statusLabel = serviceType === 'debrid' ? 'Not Cached' : 'Unhealthy';
            break;
          }
          case 'error':
            statusLabel = `Health check failed${healthState.error ? ` • ${healthState.error}` : ''}`;
            break;
          case 'stream_error':
            statusLabel = `Stream error${healthState.error ? ` • ${healthState.error}` : ''} • Cannot play`;
            break;
          case 'not_applicable':
            statusLabel = Platform.isTV ? 'Select to check health' : 'Tap to check health';
            break;
          default:
            statusLabel = null;
        }
      } else {
        statusLabel = Platform.isTV ? 'Select to check health' : 'Tap to check health';
      }

      const titleStyles: StyleProp<TextStyle>[] = [styles.manualResultTitle];
      const metaStyles: StyleProp<TextStyle>[] = [styles.manualResultMeta];
      const statusStyles: StyleProp<TextStyle>[] = [styles.manualResultStatus];
      const containerStyles: StyleProp<ViewStyle>[] = [styles.manualResult];
      const badgeStyles: StyleProp<TextStyle>[] = [
        styles.manualResultBadge,
        serviceType === 'debrid' ? styles.manualResultBadgeDebrid : styles.manualResultBadgeUsenet,
      ];

      if (isFocused) {
        containerStyles.push(styles.manualResultFocused);
        statusStyles.push(styles.manualResultStatusFocused);
        if (!isUnplayable) {
          titleStyles.push(styles.manualResultTitleFocused);
          metaStyles.push(styles.manualResultMetaFocused);
        }
      }

      if (isUnplayable) {
        containerStyles.push(styles.manualResultUnhealthy);
        statusStyles.push(styles.manualResultStatusUnhealthy);
        titleStyles.push(styles.manualResultTitleUnhealthy);
        metaStyles.push(styles.manualResultMetaUnhealthy);
        if (isFocused) {
          containerStyles.push(styles.manualResultUnhealthyFocused);
        }
      }

      // Check if this is an aiostreams result with passthrough format enabled
      const usePassthroughFormat =
        result.attributes?.passthrough_format === 'true' &&
        result.attributes?.raw_name &&
        result.attributes?.raw_description;

      if (usePassthroughFormat) {
        return (
          <View style={containerStyles}>
            <Text style={titleStyles}>{result.attributes!.raw_name}</Text>
            <Text style={[metaStyles, styles.manualResultDescription]}>{result.attributes!.raw_description}</Text>
            {statusLabel && <Text style={statusStyles}>{statusLabel}</Text>}
          </View>
        );
      }

      return (
        <View style={containerStyles}>
          <Text style={titleStyles}>{result.title}</Text>
          <View style={styles.manualResultMetaRow}>
            {!demoMode && <Text style={badgeStyles}>{serviceLabel}</Text>}
            <Text style={metaStyles}>
              {result.indexer} •{' '}
              {result.episodeCount && result.episodeCount > 0
                ? `${formatFileSize(Math.floor(result.sizeBytes / result.episodeCount))}/ep (${formatFileSize(result.sizeBytes)})`
                : formatFileSize(result.sizeBytes)}
              {serviceType === 'usenet' && result.publishDate ? ` • ${formatPublishDate(result.publishDate)}` : ''}
            </Text>
          </View>
          {statusLabel && <Text style={statusStyles}>{statusLabel}</Text>}
        </View>
      );
    },
    [healthChecks, styles, demoMode],
  );

  if (!visible) {
    return null;
  }

  console.log('[ManualSelection] Rendering modal, visible:', visible);

  const manualOverlayStyle = [
    styles.manualOverlay,
    {
      paddingTop: (theme.breakpoint === 'compact' ? theme.spacing['2xl'] : theme.spacing['3xl']) + safeAreaInsets.top,
      paddingBottom:
        (theme.breakpoint === 'compact' ? theme.spacing['2xl'] : theme.spacing['3xl']) + safeAreaInsets.bottom,
    },
  ];

  return (
    <Modal transparent visible={visible} onRequestClose={onClose} animationType="fade">
      <SpatialNavigationRoot isActive={visible}>
        <View style={styles.overlay}>
          {/* Keep backdrop Pressable on TV as native focus anchor for spatial navigation */}
          <Pressable style={styles.overlayPressable} onPress={onClose} />
          <View style={manualOverlayStyle} pointerEvents="box-none">
            <View style={styles.manualContainer}>
              <View style={styles.manualHeader}>
                <Text style={styles.manualTitle}>Select a source</Text>
                {Platform.isTV ? (
                  <FocusablePressable
                    text="Close"
                    onSelect={onClose}
                    style={styles.manualCloseButton}
                    textStyle={styles.manualCloseButtonText}
                  />
                ) : showMobileIOSCloseButton ? (
                  <Pressable
                    onPress={onClose}
                    hitSlop={8}
                    accessibilityRole="button"
                    accessibilityLabel="Close manual selection"
                    style={styles.manualMobileCloseButton}>
                    <Text style={styles.manualMobileCloseButtonText}>Close</Text>
                  </Pressable>
                ) : null}
              </View>
              {loading && <Text style={styles.manualStatus}>Loading search results…</Text>}
              {!loading && error && (
                <View style={styles.manualErrorContainer}>
                  <Text style={styles.manualError}>{error}</Text>
                  <DefaultFocus>
                    <FocusablePressable text="Close" onSelect={onClose} style={styles.manualCancelButton} />
                  </DefaultFocus>
                </View>
              )}
              {!loading && !error && (!results || results.length === 0) && (
                <Text style={styles.manualStatus}>No results yet. Try again later.</Text>
              )}
              {!loading && !error && results && results.length > 0 && filteredResults.length === 0 && (
                <Text style={styles.manualStatus}>All results have been marked as unplayable.</Text>
              )}
              {!loading &&
                !error &&
                filteredResults.length > 0 &&
                (Platform.isTV ? (
                  // TV: Use View + Animated.View to bypass native scroll gestures
                  <SpatialNavigationNode orientation="vertical">
                    <View style={[styles.manualResultsContainer, { maxHeight, overflow: 'hidden' }]}>
                      <Animated.View
                        style={[
                          styles.manualResultsContent,
                          { transform: [{ translateY: scrollOffsetRef }] },
                        ]}>
                        {filteredResults.map((result, index) => {
                          const key = getResultKey(result) || `${result.indexer}-${index}`;
                          const healthState = healthChecks[key];
                          const hasHealthCheck = healthState && healthState.state !== 'checking';
                          const isHealthy = healthState?.state === 'healthy';

                          const handleSelect = () => {
                            console.log('[ManualSelection] Item selected:', result.title, 'health:', healthState?.state);

                            // First tap: check health if not already checked or checking
                            if (!healthState || (!hasHealthCheck && healthState.state !== 'checking')) {
                              console.log('[ManualSelection] Checking health for:', result.title);
                              onCheckHealth(result);
                              return;
                            }

                            // If checking, do nothing
                            if (healthState.state === 'checking') {
                              console.log('[ManualSelection] Currently checking, ignoring select');
                              return;
                            }

                            // Second tap: play if healthy
                            if (isHealthy) {
                              console.log('[ManualSelection] Selecting healthy result:', result.title);
                              onSelect(result);
                            } else {
                              console.log(
                                '[ManualSelection] Result not healthy, cannot select. State:',
                                healthState?.state,
                              );
                            }
                          };

                          const focusableItem = (
                            <SpatialNavigationFocusableView
                              key={key}
                              focusKey={`manual-result-${key}`}
                              onSelect={() => {
                                console.log(
                                  '[ManualSelection] SpatialNavigationFocusableView onSelect called for index:',
                                  index,
                                );
                                handleSelect();
                              }}
                              onFocus={() => {
                                console.log(
                                  '[ManualSelection] SpatialNavigationFocusableView onFocus called for index:',
                                  index,
                                );
                                handleItemFocus(index);
                              }}>
                              {({ isFocused }: { isFocused: boolean }) => (
                                <Pressable tvParallaxProperties={{ enabled: false }}>
                                  <View
                                    onLayout={(event) => {
                                      const { y, height } = event.nativeEvent.layout;
                                      handleItemLayout(index, y, height);
                                    }}>
                                    {renderManualResultContent(result, isFocused)}
                                  </View>
                                </Pressable>
                              )}
                            </SpatialNavigationFocusableView>
                          );

                          // Auto-focus first item
                          return index === 0 ? <DefaultFocus key={key}>{focusableItem}</DefaultFocus> : focusableItem;
                        })}
                      </Animated.View>
                    </View>
                  </SpatialNavigationNode>
                ) : isMobile || isWebTouch ? (
                  <ScrollView
                    style={[styles.manualResultsContainer, { maxHeight }]}
                    showsVerticalScrollIndicator
                    keyboardShouldPersistTaps="handled"
                    contentContainerStyle={styles.manualResultsContent}>
                    {filteredResults.map((result, index) => {
                      const key = getResultKey(result) || `${result.indexer}-${index}`;
                      const healthState = healthChecks[key];
                      const isUnplayable = isResultUnplayable(healthState);
                      const hasHealthCheck = healthState && healthState.state !== 'checking';
                      const isHealthy = healthState?.state === 'healthy';

                      const onSelectResult = () => {
                        // First tap: check health if not already checked or checking
                        if (!healthState || (!hasHealthCheck && healthState.state !== 'checking')) {
                          onCheckHealth(result);
                          return;
                        }

                        // If checking, do nothing
                        if (healthState.state === 'checking') {
                          return;
                        }

                        // Second tap: play if healthy
                        if (isHealthy) {
                          onSelect(result);
                        }
                      };

                      return (
                        <Pressable
                          key={key}
                          onPress={onSelectResult}
                          disabled={false}
                          style={isUnplayable ? styles.manualResultDisabled : undefined}>
                          {renderManualResultContent(result, false)}
                        </Pressable>
                      );
                    })}
                  </ScrollView>
                ) : null)}
            </View>
          </View>
        </View>
      </SpatialNavigationRoot>
    </Modal>
  );
};

export const useManualHealthChecks = (results: NZBResult[]) => {
  const [healthChecks, setHealthChecks] = useState<Record<string, ManualResultHealthState>>({});

  // Return healthChecks state and a function to manually check a specific result
  const checkHealth = useCallback(async (result: NZBResult) => {
    const key = getResultKey(result);
    const serviceType = (result.serviceType ?? 'usenet').toLowerCase();

    // Set to checking state
    setHealthChecks((prev) => ({
      ...prev,
      [key]: { state: 'checking' },
    }));

    try {
      if (serviceType === 'debrid') {
        const debridDetails = await apiService.checkDebridCached(result);
        setHealthChecks((prev) => ({
          ...prev,
          [key]: {
            state: debridDetails.cached ? 'healthy' : 'unhealthy',
            debridDetails,
          },
        }));
      } else {
        const details = await apiService.checkUsenetHealth(result);
        setHealthChecks((prev) => ({
          ...prev,
          [key]: {
            state: details.healthy ? 'healthy' : 'unhealthy',
            details,
          },
        }));
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Health check failed.';
      setHealthChecks((prev) => ({
        ...prev,
        [key]: { state: 'error', error: message },
      }));
    }
  }, []);

  return { healthChecks, checkHealth };
};

const createManualSelectionStyles = (theme: NovaTheme) => {
  const isCompactBreakpoint = theme.breakpoint === 'compact';

  return StyleSheet.create({
    overlay: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
    },
    overlayPressable: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
    },
    manualOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: isCompactBreakpoint ? 'flex-start' : 'center',
      alignItems: isCompactBreakpoint ? 'stretch' : 'center',
      paddingHorizontal: isCompactBreakpoint ? theme.spacing.xl : theme.spacing['3xl'],
      paddingTop: isCompactBreakpoint ? theme.spacing['2xl'] : theme.spacing['3xl'],
      paddingBottom: isCompactBreakpoint ? theme.spacing['2xl'] : theme.spacing['3xl'],
    },
    manualContainer: {
      width: isCompactBreakpoint ? '100%' : '70%',
      maxWidth: isCompactBreakpoint ? undefined : 960,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg,
      padding: isCompactBreakpoint ? theme.spacing['2xl'] : theme.spacing['3xl'],
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.md,
      alignSelf: isCompactBreakpoint ? 'stretch' : 'center',
      flexShrink: 1,
    },
    manualHeader: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      gap: theme.spacing.lg,
    },
    manualTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      flex: 1,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.title.lg.fontSize * 1.2,
            lineHeight: theme.typography.title.lg.lineHeight * 1.2,
          }
        : {}),
    },
    manualCloseButton: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.md,
    },
    manualCloseButtonText: {
      fontSize: theme.typography.body.md.fontSize * 1.2,
    },
    manualMobileCloseButton: {
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    manualMobileCloseButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
    },
    manualStatus: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.md.fontSize * 1.2,
            lineHeight: theme.typography.body.md.lineHeight * 1.2,
          }
        : {}),
    },
    manualResultsContainer: {
      paddingRight: theme.spacing.sm,
      marginBottom: theme.spacing.lg,
      flexGrow: 1,
      flexShrink: 1,
      width: '100%',
    },
    manualResultsContent: {
      paddingBottom: theme.spacing.lg,
    },
    manualResultPressable: {
      width: '100%',
    },
    manualResult: {
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.lg,
      borderRadius: theme.radius.md,
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      marginBottom: theme.spacing.md,
      ...(Platform.isTV
        ? {
            paddingVertical: theme.spacing.xl,
            paddingHorizontal: theme.spacing['2xl'],
            marginBottom: theme.spacing.lg,
          }
        : {}),
    },
    manualResultFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    manualResultUnhealthy: {
      backgroundColor: theme.colors.status.danger,
    },
    manualResultUnhealthyFocused: {
      backgroundColor: theme.colors.status.danger,
    },
    manualResultTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
      ...(Platform.isTV
        ? {
            ...theme.typography.title.md,
            marginBottom: theme.spacing.sm,
          }
        : {}),
    },
    manualResultTitleFocused: {
      color: theme.colors.background.base,
    },
    manualResultTitleUnhealthy: {
      color: theme.colors.text.inverse,
    },
    manualResultMeta: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.md.fontSize * 1.2,
            lineHeight: theme.typography.body.md.lineHeight * 1.2,
            fontWeight: theme.typography.body.md.fontWeight,
            fontFamily: theme.typography.body.md.fontFamily,
          }
        : {}),
    },
    manualResultMetaFocused: {
      color: theme.colors.background.base,
    },
    manualResultMetaUnhealthy: {
      color: theme.colors.text.inverse,
    },
    manualResultStatus: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.xs,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.sm.fontSize * 1.2,
            lineHeight: theme.typography.body.sm.lineHeight * 1.2,
            fontWeight: theme.typography.body.sm.fontWeight,
            fontFamily: theme.typography.body.sm.fontFamily,
            marginTop: theme.spacing.sm,
          }
        : {}),
    },
    manualResultStatusFocused: {
      color: theme.colors.background.base,
    },
    manualResultStatusUnhealthy: {
      color: theme.colors.text.inverse,
    },
    manualResultMetaRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
    },
    manualResultDescription: {
      marginTop: theme.spacing.xs,
    },
    manualResultBadge: {
      ...theme.typography.caption.sm,
      fontWeight: '700',
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: 2,
      borderRadius: theme.radius.sm,
      overflow: 'hidden',
      ...(Platform.isTV
        ? {
            ...theme.typography.body.md,
            paddingHorizontal: theme.spacing.md,
            paddingVertical: theme.spacing.xs,
          }
        : {}),
    },
    manualResultBadgeUsenet: {
      backgroundColor: theme.colors.accent.primary,
      color: theme.colors.text.inverse,
    },
    manualResultBadgeDebrid: {
      backgroundColor: theme.colors.accent.secondary,
      color: theme.colors.text.inverse,
    },
    manualCancelButton: {
      paddingHorizontal: theme.spacing['2xl'],
      alignSelf: 'flex-end',
      marginTop: theme.spacing.md,
    },
    manualErrorContainer: {
      marginTop: theme.spacing.md,
    },
    manualError: {
      ...theme.typography.body.md,
      color: theme.colors.status.danger,
    },
    manualResultDisabled: {
      opacity: 0.5,
    },
  });
};
