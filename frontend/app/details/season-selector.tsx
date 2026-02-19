import type { NovaTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { useCallback, useMemo, useRef } from 'react';
import { Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import type { SeriesSeason } from '@/services/api';
import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  DefaultFocus,
  SpatialNavigationFocusableView,
} from '@/services/tv-navigation';
import MarqueeText from '@/components/tv/MarqueeText';
import { getSeasonLabel } from './utils';

interface SeasonSelectorProps {
  visible: boolean;
  onClose: () => void;
  seasons: SeriesSeason[];
  onSeasonSelect: (season: SeriesSeason) => void;
  theme: NovaTheme;
}

export function SeasonSelector({ visible, onClose, seasons, onSeasonSelect, theme }: SeasonSelectorProps) {
  const styles = useMemo(() => createStyles(theme), [theme]);
  const safeAreaInsets = useSafeAreaInsets();
  const scrollViewRef = useRef<ScrollView>(null);
  const itemLayoutsRef = useRef<{ y: number; height: number }[]>([]);
  const showMobileIOSCloseButton = !Platform.isTV && Platform.OS === 'ios';

  const handleItemLayout = useCallback((index: number, y: number, height: number) => {
    itemLayoutsRef.current[index] = { y, height };
  }, []);

  const handleItemFocus = useCallback(
    (index: number) => {
      if (!Platform.isTV) return;

      // Get the margin between items (matches seasonItem marginBottom for TV)
      const itemMargin = theme.spacing.lg;

      // Calculate cumulative Y position from measured layouts (including margins)
      let cumulativeY = 0;
      for (let i = 0; i < index; i++) {
        const layout = itemLayoutsRef.current[i];
        if (layout) {
          cumulativeY += layout.height + itemMargin;
        }
      }

      // Scroll to position the focused item with some offset from top
      const scrollOffset = Math.max(0, cumulativeY - 100);
      scrollViewRef.current?.scrollTo({ y: scrollOffset, animated: true });
    },
    [theme.spacing.lg],
  );

  const handleSeasonPress = useCallback(
    (season: SeriesSeason) => {
      onSeasonSelect(season);
    },
    [onSeasonSelect],
  );

  if (!visible) {
    return null;
  }

  const overlayStyle = [
    styles.overlay,
    {
      paddingTop: (theme.breakpoint === 'compact' ? theme.spacing['2xl'] : theme.spacing['3xl']) + safeAreaInsets.top,
      paddingBottom:
        (theme.breakpoint === 'compact' ? theme.spacing['2xl'] : theme.spacing['3xl']) + safeAreaInsets.bottom,
    },
  ];

  return (
    <Modal transparent visible={visible} onRequestClose={onClose} animationType="fade">
      <SpatialNavigationRoot isActive={visible}>
        <View style={styles.backdrop}>
          {!Platform.isTV && <Pressable style={styles.overlayPressable} onPress={onClose} />}
          <View style={overlayStyle} pointerEvents="box-none">
            <View style={styles.container}>
              <View style={styles.header}>
                <Text style={styles.title}>Select Season</Text>
                {Platform.isTV ? (
                  <SpatialNavigationFocusableView onSelect={onClose}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <View style={[styles.closeButton, isFocused && styles.closeButtonFocused]}>
                        <Text style={[styles.closeButtonText, isFocused && styles.closeButtonTextFocused]}>Close</Text>
                      </View>
                    )}
                  </SpatialNavigationFocusableView>
                ) : showMobileIOSCloseButton ? (
                  <Pressable
                    onPress={onClose}
                    hitSlop={8}
                    accessibilityRole="button"
                    accessibilityLabel="Close season selector"
                    style={styles.mobileCloseButton}>
                    <Text style={styles.mobileCloseButtonText}>Close</Text>
                  </Pressable>
                ) : null}
              </View>
              {seasons.length === 0 ? (
                <Text style={styles.emptyStateText}>Loading seasons...</Text>
              ) : (
                <SpatialNavigationNode orientation="vertical">
                  <ScrollView
                    ref={scrollViewRef}
                    style={styles.scrollView}
                    contentContainerStyle={styles.scrollContent}
                    scrollEnabled={!Platform.isTV}>
                    {seasons.map((season, index) => {
                      const seasonContent = ({ isFocused }: { isFocused: boolean }) => (
                        <View
                          style={[styles.seasonItem, isFocused && styles.seasonItemFocused]}
                          onLayout={(event) => {
                            const { y, height } = event.nativeEvent.layout;
                            handleItemLayout(index, y, height);
                          }}>
                          <View style={styles.seasonInfo}>
                            <MarqueeText
                              style={styles.seasonTitle}
                              containerStyle={styles.seasonTitleContainer}
                              focused={isFocused}
                              speed={30}
                              delay={400}>
                              {getSeasonLabel(season.number, season.name)}
                            </MarqueeText>
                            {season.episodes && season.episodes.length > 0 && (
                              <Text style={styles.seasonMeta}>
                                {season.episodes.length} episode{season.episodes.length !== 1 ? 's' : ''}
                              </Text>
                            )}
                          </View>
                          <Ionicons
                            name="chevron-forward"
                            size={Platform.isTV ? 32 : 24}
                            color={theme.colors.text.secondary}
                          />
                        </View>
                      );

                      const focusableItem = Platform.isTV ? (
                        <SpatialNavigationFocusableView
                          key={season.id}
                          onSelect={() => handleSeasonPress(season)}
                          onFocus={() => handleItemFocus(index)}>
                          {seasonContent}
                        </SpatialNavigationFocusableView>
                      ) : (
                        <Pressable
                          key={season.id}
                          onPress={() => handleSeasonPress(season)}>
                          {seasonContent({ isFocused: false })}
                        </Pressable>
                      );

                      return index === 0 ? <DefaultFocus key={season.id}>{focusableItem}</DefaultFocus> : focusableItem;
                    })}
                  </ScrollView>
                </SpatialNavigationNode>
              )}
            </View>
          </View>
        </View>
      </SpatialNavigationRoot>
    </Modal>
  );
}

const createStyles = (theme: NovaTheme) => {
  const isCompactBreakpoint = theme.breakpoint === 'compact';

  return StyleSheet.create({
    backdrop: {
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
    overlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: isCompactBreakpoint ? 'flex-start' : 'center',
      alignItems: isCompactBreakpoint ? 'stretch' : 'center',
      paddingHorizontal: isCompactBreakpoint ? theme.spacing.xl : theme.spacing['3xl'],
      paddingTop: isCompactBreakpoint ? theme.spacing['2xl'] : theme.spacing['3xl'],
      paddingBottom: isCompactBreakpoint ? theme.spacing['2xl'] : theme.spacing['3xl'],
    },
    container: {
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
    header: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      gap: theme.spacing.lg,
    },
    title: {
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
    closeButton: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
    },
    closeButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    closeButtonText: {
      ...theme.typography.body.md,
      fontSize: theme.typography.body.md.fontSize * 1.2,
      color: theme.colors.text.primary,
    },
    closeButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    mobileCloseButton: {
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    mobileCloseButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
    },
    scrollView: {
      paddingRight: theme.spacing.sm,
      marginBottom: theme.spacing.lg,
      flexGrow: 1,
      flexShrink: 1,
      width: '100%',
    },
    scrollContent: {
      paddingBottom: theme.spacing.lg,
    },
    seasonItem: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
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
    seasonItemFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    seasonInfo: {
      flex: 1,
    },
    seasonTitleContainer: {
      maxWidth: Platform.isTV ? 400 : 200, // Truncate long season names, marquee scrolls on focus
    },
    seasonTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      ...(Platform.isTV
        ? {
            ...theme.typography.title.md,
          }
        : {}),
    },
    seasonMeta: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.xs,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.md.fontSize * 1.2,
            lineHeight: theme.typography.body.md.lineHeight * 1.2,
            marginTop: theme.spacing.sm,
          }
        : {}),
    },
    emptyStateText: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.md.fontSize * 1.2,
            lineHeight: theme.typography.body.md.lineHeight * 1.2,
          }
        : {}),
    },
  });
};
