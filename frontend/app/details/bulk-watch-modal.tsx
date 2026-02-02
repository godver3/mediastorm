import FocusablePressable from '@/components/FocusablePressable';
import type { NovaTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { useCallback, useRef, useState } from 'react';
import { ActivityIndicator, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import type { SeriesEpisode, SeriesSeason } from '@/services/api';
import { getSeasonLabel } from './utils';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';

interface BulkWatchModalProps {
  visible: boolean;
  onClose: () => void;
  theme: NovaTheme;
  seasons: SeriesSeason[];
  allEpisodes: SeriesEpisode[];
  currentEpisode?: SeriesEpisode | null;
  onMarkAllWatched: () => Promise<void>;
  onMarkAllUnwatched: () => Promise<void>;
  onMarkSeasonWatched: (season: SeriesSeason) => Promise<void>;
  onMarkSeasonUnwatched: (season: SeriesSeason) => Promise<void>;
  onMarkEpisodeWatched?: (episode: SeriesEpisode) => Promise<void>;
  onMarkEpisodeUnwatched?: (episode: SeriesEpisode) => Promise<void>;
  isEpisodeWatched?: (episode: SeriesEpisode) => boolean;
}

export const BulkWatchModal = ({
  visible,
  onClose,
  theme,
  seasons,
  allEpisodes,
  currentEpisode,
  onMarkAllWatched,
  onMarkAllUnwatched,
  onMarkSeasonWatched,
  onMarkSeasonUnwatched,
  onMarkEpisodeWatched,
  onMarkEpisodeUnwatched,
  isEpisodeWatched,
}: BulkWatchModalProps) => {
  const styles = createStyles(theme);
  const scrollViewRef = useRef<ScrollView>(null);
  const itemRefsRef = useRef<Map<number, View>>(new Map());

  // Track loading and success states for season buttons
  const [loadingSeasons, setLoadingSeasons] = useState<Set<string>>(new Set());
  const [successSeasons, setSuccessSeasons] = useState<Set<string>>(new Set());

  const handleMarkAllWatched = useCallback(async () => {
    await onMarkAllWatched();
    onClose();
  }, [onMarkAllWatched, onClose]);

  const handleMarkAllUnwatched = useCallback(async () => {
    await onMarkAllUnwatched();
    onClose();
  }, [onMarkAllUnwatched, onClose]);

  const handleMarkSeasonWithFeedback = useCallback(async (
    seasonId: string,
    action: 'watched' | 'unwatched',
    handler: () => Promise<void>
  ) => {
    const key = `${seasonId}-${action}`;
    setLoadingSeasons(prev => new Set(prev).add(key));
    setSuccessSeasons(prev => {
      const next = new Set(prev);
      next.delete(key);
      return next;
    });

    try {
      await handler();
      setSuccessSeasons(prev => new Set(prev).add(key));
      // Clear success state after a brief delay
      setTimeout(() => {
        setSuccessSeasons(prev => {
          const next = new Set(prev);
          next.delete(key);
          return next;
        });
      }, 1500);
    } finally {
      setLoadingSeasons(prev => {
        const next = new Set(prev);
        next.delete(key);
        return next;
      });
    }
  }, []);

  const handleItemFocus = useCallback((index: number) => {
    if (Platform.isTV && scrollViewRef.current) {
      const itemView = itemRefsRef.current.get(index);
      if (itemView) {
        (itemView as any).measureLayout(
          scrollViewRef.current as any,
          (x: number, y: number, _width: number, _height: number) => {
            // Scroll to position the focused item with some padding from the top
            const scrollOffset = Math.max(0, y - 100);
            scrollViewRef.current?.scrollTo({ y: scrollOffset, animated: true });
          },
          () => {
            console.log(`[BulkWatchModal] Failed to measure item ${index}`);
          },
        );
      }
    }
  }, []);

  if (!visible) {
    return null;
  }

  const totalEpisodes = allEpisodes.length;

  return (
    <Modal transparent visible={visible} onRequestClose={onClose} animationType="fade">
      <SpatialNavigationRoot isActive={visible}>
        <View style={styles.overlay}>
          {!Platform.isTV && <Pressable style={styles.overlayPressable} onPress={onClose} />}
          <View style={styles.modalWrapper} pointerEvents="box-none">
            <View style={styles.modal}>
              <View style={styles.container}>
                <View style={styles.header}>
                  <Text style={styles.title}>Mark Episodes as Watched</Text>
                  {Platform.isTV ? (
                    <FocusablePressable
                      text="Close"
                      onSelect={onClose}
                      style={styles.closeButton}
                      textStyle={styles.closeButtonText}
                    />
                  ) : (
                    <Pressable onPress={onClose} style={styles.closeButtonTouch}>
                      <Ionicons name="close" size={24} color={theme.colors.text.primary} />
                    </Pressable>
                  )}
                </View>

                <SpatialNavigationNode orientation="vertical">
                  <ScrollView
                    ref={scrollViewRef}
                    style={styles.content}
                    contentContainerStyle={styles.contentContainer}
                    scrollEnabled={!Platform.isTV}>
                    <Text style={styles.sectionTitle}>Mark All Episodes</Text>

                    <DefaultFocus>
                      <SpatialNavigationFocusableView
                        focusKey="mark-all-watched"
                        onSelect={handleMarkAllWatched}
                        onFocus={() => handleItemFocus(0)}>
                        {({ isFocused }: { isFocused: boolean }) => (
                          <View
                            ref={(ref) => {
                              if (ref) itemRefsRef.current.set(0, ref);
                            }}
                            collapsable={false}>
                            <Pressable
                              style={[styles.option, isFocused && styles.optionFocused]}
                              onPress={!Platform.isTV ? handleMarkAllWatched : undefined}>
                              <View style={styles.optionContent}>
                                <Ionicons
                                  name="checkmark-done"
                                  size={Platform.isTV ? 28 : 24}
                                  color={isFocused ? theme.colors.background.base : theme.colors.accent.primary}
                                />
                                <View style={styles.optionText}>
                                  <Text style={[styles.optionTitle, isFocused && styles.optionTitleFocused]}>
                                    Mark All as Watched
                                  </Text>
                                  <Text
                                    style={[styles.optionDescription, isFocused && styles.optionDescriptionFocused]}>
                                    Mark all {totalEpisodes} episodes across all seasons as watched
                                  </Text>
                                </View>
                              </View>
                            </Pressable>
                          </View>
                        )}
                      </SpatialNavigationFocusableView>
                    </DefaultFocus>

                    <SpatialNavigationFocusableView
                      focusKey="mark-all-unwatched"
                      onSelect={handleMarkAllUnwatched}
                      onFocus={() => handleItemFocus(1)}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <View
                          ref={(ref) => {
                            if (ref) itemRefsRef.current.set(1, ref);
                          }}
                          collapsable={false}>
                          <Pressable
                            style={[styles.option, isFocused && styles.optionFocused]}
                            onPress={!Platform.isTV ? handleMarkAllUnwatched : undefined}>
                            <View style={styles.optionContent}>
                              <Ionicons
                                name="close-circle"
                                size={Platform.isTV ? 28 : 24}
                                color={isFocused ? theme.colors.background.base : theme.colors.text.secondary}
                              />
                              <View style={styles.optionText}>
                                <Text style={[styles.optionTitle, isFocused && styles.optionTitleFocused]}>
                                  Mark All as Unwatched
                                </Text>
                                <Text style={[styles.optionDescription, isFocused && styles.optionDescriptionFocused]}>
                                  Mark all {totalEpisodes} episodes across all seasons as unwatched
                                </Text>
                              </View>
                            </View>
                          </Pressable>
                        </View>
                      )}
                    </SpatialNavigationFocusableView>

                    {currentEpisode && onMarkEpisodeWatched && onMarkEpisodeUnwatched && isEpisodeWatched && (
                      <>
                        <Text style={[styles.sectionTitle, styles.sectionTitleSpaced]}>Current Episode</Text>

                        <View style={styles.seasonGroup}>
                          <Text style={styles.seasonGroupTitle}>
                            S{currentEpisode.seasonNumber}E{currentEpisode.episodeNumber}
                            {currentEpisode.name ? ` - ${currentEpisode.name}` : ''}
                          </Text>

                          <SpatialNavigationFocusableView
                            focusKey={`mark-episode-${currentEpisode.id}-watched`}
                            onSelect={async () => {
                              if (currentEpisode) {
                                await onMarkEpisodeWatched(currentEpisode);
                                onClose();
                              }
                            }}
                            onFocus={() => handleItemFocus(2)}>
                            {({ isFocused }: { isFocused: boolean }) => (
                              <View
                                ref={(ref) => {
                                  if (ref) itemRefsRef.current.set(2, ref);
                                }}
                                collapsable={false}>
                                <Pressable
                                  style={[styles.option, isFocused && styles.optionFocused]}
                                  onPress={
                                    !Platform.isTV
                                      ? async () => {
                                          if (currentEpisode) {
                                            await onMarkEpisodeWatched(currentEpisode);
                                            onClose();
                                          }
                                        }
                                      : undefined
                                  }>
                                  <View style={styles.optionContent}>
                                    <Ionicons
                                      name="checkmark"
                                      size={Platform.isTV ? 24 : 20}
                                      color={isFocused ? theme.colors.background.base : theme.colors.accent.primary}
                                    />
                                    <View style={styles.optionText}>
                                      <Text style={[styles.optionTitle, isFocused && styles.optionTitleFocused]}>
                                        Mark as Watched
                                      </Text>
                                      <Text
                                        style={[
                                          styles.optionDescription,
                                          isFocused && styles.optionDescriptionFocused,
                                        ]}>
                                        {isEpisodeWatched(currentEpisode)
                                          ? 'Already watched'
                                          : 'Mark this episode as watched'}
                                      </Text>
                                    </View>
                                  </View>
                                </Pressable>
                              </View>
                            )}
                          </SpatialNavigationFocusableView>

                          <SpatialNavigationFocusableView
                            focusKey={`mark-episode-${currentEpisode.id}-unwatched`}
                            onSelect={async () => {
                              if (currentEpisode) {
                                await onMarkEpisodeUnwatched(currentEpisode);
                                onClose();
                              }
                            }}
                            onFocus={() => handleItemFocus(3)}>
                            {({ isFocused }: { isFocused: boolean }) => (
                              <View
                                ref={(ref) => {
                                  if (ref) itemRefsRef.current.set(3, ref);
                                }}
                                collapsable={false}>
                                <Pressable
                                  style={[styles.option, isFocused && styles.optionFocused]}
                                  onPress={
                                    !Platform.isTV
                                      ? async () => {
                                          if (currentEpisode) {
                                            await onMarkEpisodeUnwatched(currentEpisode);
                                            onClose();
                                          }
                                        }
                                      : undefined
                                  }>
                                  <View style={styles.optionContent}>
                                    <Ionicons
                                      name="close"
                                      size={Platform.isTV ? 24 : 20}
                                      color={isFocused ? theme.colors.background.base : theme.colors.text.secondary}
                                    />
                                    <View style={styles.optionText}>
                                      <Text style={[styles.optionTitle, isFocused && styles.optionTitleFocused]}>
                                        Mark as Unwatched
                                      </Text>
                                      <Text
                                        style={[
                                          styles.optionDescription,
                                          isFocused && styles.optionDescriptionFocused,
                                        ]}>
                                        {!isEpisodeWatched(currentEpisode)
                                          ? 'Already unwatched'
                                          : 'Mark this episode as unwatched'}
                                      </Text>
                                    </View>
                                  </View>
                                </Pressable>
                              </View>
                            )}
                          </SpatialNavigationFocusableView>
                        </View>
                      </>
                    )}

                    {seasons.length > 0 && (
                      <>
                        <Text style={[styles.sectionTitle, styles.sectionTitleSpaced]}>Mark by Season</Text>

                        {seasons.map((season, seasonIndex) => {
                          // Calculate index: 2 "Mark All" items + (2 current episode items if present) + (seasonIndex * 2 items per season)
                          const baseIndex = 2 + (currentEpisode ? 2 : 0);
                          const watchedIndex = baseIndex + seasonIndex * 2;
                          const unwatchedIndex = watchedIndex + 1;

                          const watchedKey = `${season.id}-watched`;
                          const unwatchedKey = `${season.id}-unwatched`;
                          const isWatchedLoading = loadingSeasons.has(watchedKey);
                          const isWatchedSuccess = successSeasons.has(watchedKey);
                          const isUnwatchedLoading = loadingSeasons.has(unwatchedKey);
                          const isUnwatchedSuccess = successSeasons.has(unwatchedKey);

                          return (
                            <View key={season.id} style={styles.seasonGroup}>
                              <Text style={styles.seasonGroupTitle}>{getSeasonLabel(season.number, season.name)}</Text>

                              <SpatialNavigationFocusableView
                                focusKey={`mark-season-${season.id}-watched`}
                                onSelect={async () => {
                                  if (!isWatchedLoading) {
                                    await handleMarkSeasonWithFeedback(season.id, 'watched', () => onMarkSeasonWatched(season));
                                  }
                                }}
                                onFocus={() => handleItemFocus(watchedIndex)}>
                                {({ isFocused }: { isFocused: boolean }) => (
                                  <View
                                    ref={(ref) => {
                                      if (ref) itemRefsRef.current.set(watchedIndex, ref);
                                    }}
                                    collapsable={false}>
                                    <Pressable
                                      style={[
                                        styles.option,
                                        isFocused && styles.optionFocused,
                                        isWatchedSuccess && styles.optionSuccess,
                                      ]}
                                      disabled={isWatchedLoading}
                                      onPress={
                                        !Platform.isTV
                                          ? async () => {
                                              if (!isWatchedLoading) {
                                                await handleMarkSeasonWithFeedback(season.id, 'watched', () => onMarkSeasonWatched(season));
                                              }
                                            }
                                          : undefined
                                      }>
                                      <View style={styles.optionContent}>
                                        {isWatchedLoading ? (
                                          <ActivityIndicator
                                            size="small"
                                            color={isFocused ? theme.colors.background.base : theme.colors.accent.primary}
                                          />
                                        ) : (
                                          <Ionicons
                                            name={isWatchedSuccess ? 'checkmark-circle' : 'checkmark'}
                                            size={Platform.isTV ? 24 : 20}
                                            color={
                                              isWatchedSuccess
                                                ? theme.colors.status.success
                                                : isFocused
                                                  ? theme.colors.background.base
                                                  : theme.colors.accent.primary
                                            }
                                          />
                                        )}
                                        <View style={styles.optionText}>
                                          <Text style={[styles.optionTitle, isFocused && styles.optionTitleFocused]}>
                                            {isWatchedSuccess ? 'Marked as Watched' : 'Mark as Watched'}
                                          </Text>
                                          <Text
                                            style={[
                                              styles.optionDescription,
                                              isFocused && styles.optionDescriptionFocused,
                                            ]}>
                                            {season.episodes.length} episode{season.episodes.length !== 1 ? 's' : ''}
                                          </Text>
                                        </View>
                                      </View>
                                    </Pressable>
                                  </View>
                                )}
                              </SpatialNavigationFocusableView>

                              <SpatialNavigationFocusableView
                                focusKey={`mark-season-${season.id}-unwatched`}
                                onSelect={async () => {
                                  if (!isUnwatchedLoading) {
                                    await handleMarkSeasonWithFeedback(season.id, 'unwatched', () => onMarkSeasonUnwatched(season));
                                  }
                                }}
                                onFocus={() => handleItemFocus(unwatchedIndex)}>
                                {({ isFocused }: { isFocused: boolean }) => (
                                  <View
                                    ref={(ref) => {
                                      if (ref) itemRefsRef.current.set(unwatchedIndex, ref);
                                    }}
                                    collapsable={false}>
                                    <Pressable
                                      style={[
                                        styles.option,
                                        isFocused && styles.optionFocused,
                                        isUnwatchedSuccess && styles.optionSuccess,
                                      ]}
                                      disabled={isUnwatchedLoading}
                                      onPress={
                                        !Platform.isTV
                                          ? async () => {
                                              if (!isUnwatchedLoading) {
                                                await handleMarkSeasonWithFeedback(season.id, 'unwatched', () => onMarkSeasonUnwatched(season));
                                              }
                                            }
                                          : undefined
                                      }>
                                      <View style={styles.optionContent}>
                                        {isUnwatchedLoading ? (
                                          <ActivityIndicator
                                            size="small"
                                            color={isFocused ? theme.colors.background.base : theme.colors.text.secondary}
                                          />
                                        ) : (
                                          <Ionicons
                                            name={isUnwatchedSuccess ? 'checkmark-circle' : 'close'}
                                            size={Platform.isTV ? 24 : 20}
                                            color={
                                              isUnwatchedSuccess
                                                ? theme.colors.status.success
                                                : isFocused
                                                  ? theme.colors.background.base
                                                  : theme.colors.text.secondary
                                            }
                                          />
                                        )}
                                        <View style={styles.optionText}>
                                          <Text style={[styles.optionTitle, isFocused && styles.optionTitleFocused]}>
                                            {isUnwatchedSuccess ? 'Marked as Unwatched' : 'Mark as Unwatched'}
                                          </Text>
                                          <Text
                                            style={[
                                              styles.optionDescription,
                                              isFocused && styles.optionDescriptionFocused,
                                            ]}>
                                            {season.episodes.length} episode{season.episodes.length !== 1 ? 's' : ''}
                                          </Text>
                                        </View>
                                      </View>
                                    </Pressable>
                                  </View>
                                )}
                              </SpatialNavigationFocusableView>
                            </View>
                          );
                        })}
                      </>
                    )}
                  </ScrollView>
                </SpatialNavigationNode>

                <View style={styles.footer}>
                  <FocusablePressable
                    text="Cancel"
                    onSelect={onClose}
                    style={styles.cancelButton}
                    textStyle={styles.cancelButtonText}
                  />
                </View>
              </View>
            </View>
          </View>
        </View>
      </SpatialNavigationRoot>
    </Modal>
  );
};

const createStyles = (theme: NovaTheme) => {
  const isCompactBreakpoint = theme.breakpoint === 'compact';
  const tvScale = Platform.isTV ? 1.2 : 1;

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
    modalWrapper: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      paddingHorizontal: isCompactBreakpoint ? theme.spacing.xl : theme.spacing['3xl'],
      paddingVertical: isCompactBreakpoint ? theme.spacing['2xl'] : theme.spacing['3xl'],
    },
    modal: {
      width: Platform.isTV ? '70%' : isCompactBreakpoint ? '100%' : '90%',
      maxWidth: Platform.isTV ? 960 : 600,
      maxHeight: '90%',
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg,
      overflow: 'hidden',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      ...Platform.select({
        ios: {
          shadowColor: '#000',
          shadowOffset: { width: 0, height: 4 },
          shadowOpacity: 0.3,
          shadowRadius: 8,
        },
        android: {
          elevation: 8,
        },
      }),
    },
    container: {
      height: '100%',
      flexDirection: 'column',
    },
    header: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      paddingHorizontal: Platform.isTV ? theme.spacing['3xl'] : theme.spacing['2xl'],
      paddingVertical: Platform.isTV ? theme.spacing['2xl'] : theme.spacing.xl,
      borderBottomWidth: 1,
      borderBottomColor: theme.colors.border.subtle,
      flexShrink: 0,
      gap: theme.spacing.lg,
    },
    title: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      flex: 1,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.title.lg.fontSize * tvScale,
            lineHeight: theme.typography.title.lg.lineHeight * tvScale,
          }
        : {}),
    },
    closeButton: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.md,
    },
    closeButtonText: {
      fontSize: theme.typography.body.md.fontSize * tvScale,
    },
    closeButtonTouch: {
      padding: theme.spacing.sm,
    },
    content: {
      flex: 1,
    },
    contentContainer: {
      paddingHorizontal: Platform.isTV ? theme.spacing['3xl'] : theme.spacing['2xl'],
      paddingTop: Platform.isTV ? theme.spacing['2xl'] : theme.spacing.xl,
      paddingBottom: Platform.isTV ? theme.spacing['2xl'] : theme.spacing.xl,
    },
    sectionTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.md,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.title.md.fontSize * tvScale,
            lineHeight: theme.typography.title.md.lineHeight * tvScale,
          }
        : {}),
    },
    sectionTitleSpaced: {
      marginTop: Platform.isTV ? theme.spacing['3xl'] : theme.spacing['2xl'],
    },
    seasonGroup: {
      marginBottom: Platform.isTV ? theme.spacing['2xl'] : theme.spacing.xl,
    },
    seasonGroupTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.secondary,
      marginBottom: theme.spacing.md,
      marginLeft: theme.spacing.xs,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.label.md.fontSize * tvScale,
            lineHeight: theme.typography.label.md.lineHeight * tvScale,
          }
        : {}),
    },
    option: {
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      borderRadius: theme.radius.md,
      paddingVertical: Platform.isTV ? theme.spacing.xl : theme.spacing.md,
      paddingHorizontal: Platform.isTV ? theme.spacing['2xl'] : theme.spacing.lg,
      marginBottom: Platform.isTV ? theme.spacing.lg : theme.spacing.md,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    optionFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    optionSuccess: {
      borderColor: theme.colors.status.success,
    },
    optionContent: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: Platform.isTV ? theme.spacing.lg : theme.spacing.md,
    },
    optionText: {
      flex: 1,
    },
    optionTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.label.md.fontSize * tvScale,
            lineHeight: theme.typography.label.md.lineHeight * tvScale,
          }
        : {}),
    },
    optionTitleFocused: {
      color: theme.colors.background.base,
    },
    optionDescription: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.sm.fontSize * tvScale,
            lineHeight: theme.typography.body.sm.lineHeight * tvScale,
          }
        : {}),
    },
    optionDescriptionFocused: {
      color: theme.colors.background.base,
    },
    footer: {
      paddingHorizontal: Platform.isTV ? theme.spacing['3xl'] : theme.spacing['2xl'],
      paddingVertical: Platform.isTV ? theme.spacing['2xl'] : theme.spacing.xl,
      borderTopWidth: 1,
      borderTopColor: theme.colors.border.subtle,
      flexShrink: 0,
      alignItems: 'flex-end',
    },
    cancelButton: {
      paddingHorizontal: theme.spacing['2xl'],
      paddingVertical: theme.spacing.md,
    },
    cancelButtonText: {
      fontSize: theme.typography.body.md.fontSize * tvScale,
    },
  });
};
