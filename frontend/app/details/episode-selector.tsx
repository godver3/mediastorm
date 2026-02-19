import type { NovaTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { useCallback, useMemo, useRef } from 'react';
import { Image, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import type { SeriesEpisode, SeriesSeason } from '@/services/api';
import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  DefaultFocus,
  SpatialNavigationFocusableView,
} from '@/services/tv-navigation';
import { getSeasonLabel, isEpisodeUnreleased } from './utils';

interface EpisodeSelectorProps {
  visible: boolean;
  onClose: () => void;
  onBack: () => void;
  season: SeriesSeason | null;
  onEpisodeSelect: (episode: SeriesEpisode) => void;
  isEpisodeWatched: (episode: SeriesEpisode) => boolean;
  theme: NovaTheme;
}

export function EpisodeSelector({
  visible,
  onClose,
  onBack,
  season,
  onEpisodeSelect,
  isEpisodeWatched,
  theme,
}: EpisodeSelectorProps) {
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

      // Get the margin between items (matches episodeItem marginBottom for TV)
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

  const handleEpisodePress = useCallback(
    (episode: SeriesEpisode) => {
      onEpisodeSelect(episode);
      onClose();
    },
    [onEpisodeSelect, onClose],
  );

  if (!season || !visible) {
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
                {Platform.isTV ? (
                  <SpatialNavigationFocusableView onSelect={onBack}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <View style={[styles.backButton, isFocused && styles.backButtonFocused]}>
                        <Text style={[styles.backButtonText, isFocused && styles.backButtonTextFocused]}>Back</Text>
                      </View>
                    )}
                  </SpatialNavigationFocusableView>
                ) : (
                  <Pressable onPress={onBack} style={styles.backButtonIcon}>
                    <Ionicons name="chevron-back" size={28} color={theme.colors.text.primary} />
                  </Pressable>
                )}
                <Text style={styles.title}>{getSeasonLabel(season.number, season.name)}</Text>
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
                    accessibilityLabel="Close episode selector"
                    style={styles.mobileCloseButton}>
                    <Text style={styles.mobileCloseButtonText}>Close</Text>
                  </Pressable>
                ) : null}
              </View>
              <SpatialNavigationNode orientation="vertical">
                <ScrollView
                  ref={scrollViewRef}
                  style={styles.scrollView}
                  contentContainerStyle={styles.scrollContent}
                  scrollEnabled={!Platform.isTV}>
                  {season.episodes &&
                    season.episodes.map((episode, index) => {
                      const watched = isEpisodeWatched(episode);

                      const episodeContent = ({ isFocused }: { isFocused: boolean }) => (
                        <View
                          style={[
                            styles.episodeItem,
                            watched && styles.episodeItemWatched,
                            isFocused && styles.episodeItemFocused,
                          ]}
                          onLayout={(event) => {
                            const { y, height } = event.nativeEvent.layout;
                            handleItemLayout(index, y, height);
                          }}>
                          <View style={styles.episodeInfo}>
                            <View style={styles.episodeHeader}>
                              <Text style={styles.episodeNumber}>
                                E{String(episode.episodeNumber).padStart(2, '0')}
                              </Text>
                              {watched ? (
                                <View style={styles.watchedBadge}>
                                  <Ionicons
                                    name="checkmark-circle"
                                    size={Platform.isTV ? 24 : 16}
                                    color={theme.colors.accent.primary}
                                  />
                                </View>
                              ) : isEpisodeUnreleased(episode.airedDate) ? (
                                <View style={styles.watchedBadge}>
                                  <Ionicons
                                    name="time"
                                    size={Platform.isTV ? 24 : 16}
                                    color={theme.colors.status.warning}
                                  />
                                </View>
                              ) : null}
                            </View>
                            {episode.name && (
                              <Text style={styles.episodeTitle} numberOfLines={2}>
                                {episode.name}
                              </Text>
                            )}
                            {episode.overview && (
                              <Text style={styles.episodeOverview} numberOfLines={2}>
                                {episode.overview}
                              </Text>
                            )}
                            <View style={styles.episodeMeta}>
                              {episode.runtimeMinutes && (
                                <Text style={styles.episodeMetaText}>{episode.runtimeMinutes}m</Text>
                              )}
                              {episode.airedDate && (
                                <Text style={styles.episodeMetaText}>
                                  {new Date(episode.airedDate).toLocaleDateString('en-US', {
                                    month: 'short',
                                    day: 'numeric',
                                    year: 'numeric',
                                  })}
                                </Text>
                              )}
                            </View>
                          </View>
                          {episode.image?.url ? (
                            <Image
                              source={{ uri: episode.image.url }}
                              style={styles.episodeImage}
                              resizeMode="cover"
                            />
                          ) : (
                            <View style={styles.episodeImagePlaceholder}>
                              <Ionicons
                                name="film-outline"
                                size={Platform.isTV ? 32 : 24}
                                color={theme.colors.text.muted}
                              />
                            </View>
                          )}
                        </View>
                      );

                      const focusableItem = Platform.isTV ? (
                        <SpatialNavigationFocusableView
                          key={episode.id}
                          onSelect={() => handleEpisodePress(episode)}
                          onFocus={() => handleItemFocus(index)}>
                          {episodeContent}
                        </SpatialNavigationFocusableView>
                      ) : (
                        <Pressable
                          key={episode.id}
                          onPress={() => handleEpisodePress(episode)}>
                          {episodeContent({ isFocused: false })}
                        </Pressable>
                      );

                      return index === 0 ? (
                        <DefaultFocus key={episode.id}>{focusableItem}</DefaultFocus>
                      ) : (
                        focusableItem
                      );
                    })}
                </ScrollView>
              </SpatialNavigationNode>
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
      textAlign: 'center',
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.title.lg.fontSize * 1.2,
            lineHeight: theme.typography.title.lg.lineHeight * 1.2,
          }
        : {}),
    },
    backButton: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
    },
    backButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    backButtonText: {
      ...theme.typography.body.md,
      fontSize: theme.typography.body.md.fontSize * 1.2,
      color: theme.colors.text.primary,
    },
    backButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    backButtonIcon: {
      padding: theme.spacing.sm,
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
    episodeItem: {
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
    episodeItemWatched: {
      opacity: 0.7,
    },
    episodeItemFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    episodeInfo: {
      flex: 1,
      marginRight: theme.spacing.md,
    },
    episodeHeader: {
      flexDirection: 'row',
      alignItems: 'center',
      marginBottom: theme.spacing.xs,
    },
    episodeNumber: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
      marginRight: theme.spacing.sm,
      ...(Platform.isTV
        ? {
            ...theme.typography.title.md,
          }
        : {}),
    },
    watchedBadge: {
      flexDirection: 'row',
      alignItems: 'center',
    },
    episodeTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
      ...(Platform.isTV
        ? {
            ...theme.typography.title.md,
          }
        : {}),
    },
    episodeOverview: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      marginBottom: theme.spacing.xs,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.md.fontSize * 1.1,
            lineHeight: theme.typography.body.md.lineHeight * 1.1,
          }
        : {}),
    },
    episodeMeta: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.md,
    },
    episodeMetaText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.sm.fontSize * 1.1,
          }
        : {}),
    },
    episodeImage: {
      width: Platform.isTV ? 160 : 120,
      height: Platform.isTV ? 90 : 68,
      borderRadius: theme.radius.sm,
      backgroundColor: theme.colors.background.surface,
    },
    episodeImagePlaceholder: {
      width: Platform.isTV ? 160 : 120,
      height: Platform.isTV ? 90 : 68,
      borderRadius: theme.radius.sm,
      backgroundColor: theme.colors.background.surface,
      alignItems: 'center',
      justifyContent: 'center',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
  });
};
