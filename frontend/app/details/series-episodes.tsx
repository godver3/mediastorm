/**
 * Series and episodes functionality for the details screen
 */

import { type SeriesDetails, type SeriesEpisode, type SeriesSeason } from '@/services/api';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  type SpatialNavigationNodeRef,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { View as RNView } from 'react-native';
import { Image, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { episodesMatch, formatPublishDate, getSeasonLabel, isEpisodeUnreleased, padNumber } from './utils';
import MarqueeText from '@/components/tv/MarqueeText';

// Helper function to determine if we're on TV platform
const isTVPlatform = (): boolean => Platform.isTV;

interface SeriesEpisodesProps {
  isSeries: boolean;
  title: string;
  tvdbId?: string;
  titleId?: string;
  yearNumber?: number;
  seriesDetails?: SeriesDetails | null;
  seriesDetailsLoading?: boolean;
  initialSeasonNumber?: number | null;
  initialEpisodeNumber?: number | null;
  isTouchSeasonLayout: boolean;
  shouldUseSeasonModal: boolean;
  shouldAutoPlaySeasonSelection: boolean;
  onSeasonSelect: (season: SeriesSeason, shouldAutoplay: boolean) => void;
  onEpisodeSelect: (episode: SeriesEpisode) => void;
  onEpisodeFocus: (episode: SeriesEpisode) => void;
  onPlaySeason: (season: SeriesSeason) => void;
  onPlayEpisode: (episode: SeriesEpisode) => void;
  onEpisodeLongPress: (episode: SeriesEpisode) => void;
  onToggleEpisodeWatched?: (episode: SeriesEpisode) => Promise<void>;
  isEpisodeWatched?: (episode: SeriesEpisode) => boolean;
  activeEpisode: SeriesEpisode | null;
  isResolving: boolean;
  theme: NovaTheme;
  onRegisterSeasonFocusHandler?: (focusHandler: (() => boolean) | null) => void;
  onRequestFocusShift?: () => void;
  onEpisodesLoaded?: (episodes: SeriesEpisode[]) => void;
  onSeasonsLoaded?: (seasons: SeriesSeason[]) => void;
  renderContent?: boolean;
}

export const SeriesEpisodes = ({
  isSeries,
  title,
  tvdbId,
  titleId,
  yearNumber: _yearNumber,
  seriesDetails,
  seriesDetailsLoading = false,
  initialSeasonNumber,
  initialEpisodeNumber,
  isTouchSeasonLayout,
  shouldUseSeasonModal,
  shouldAutoPlaySeasonSelection: _shouldAutoPlaySeasonSelection,
  onSeasonSelect,
  onEpisodeSelect,
  onEpisodeFocus,
  onPlaySeason: _onPlaySeason,
  onPlayEpisode,
  onEpisodeLongPress,
  onToggleEpisodeWatched,
  isEpisodeWatched,
  activeEpisode,
  isResolving: _isResolving,
  theme,
  onRegisterSeasonFocusHandler,
  onRequestFocusShift: _onRequestFocusShift,
  onEpisodesLoaded,
  onSeasonsLoaded,
  renderContent = true,
}: SeriesEpisodesProps) => {
  const styles = useMemo(() => createSeriesEpisodesStyles(theme), [theme]);
  const [selectedSeasonNumber, setSelectedSeasonNumber] = useState<number | null>(null);
  const [seasonPickerVisible, setSeasonPickerVisible] = useState(false);
  const [flashingEpisode, setFlashingEpisode] = useState<string | null>(null);
  const initialEpisodeAppliedRef = useRef(false);
  const seasonFocusRefs = useRef<Map<number, SpatialNavigationNodeRef | null>>(new Map());

  // Refs for manual episode scrolling (TV only)
  const episodeScrollViewRef = useRef<ScrollView | null>(null);
  const episodeRefs = useRef<{ [key: string]: RNView | null }>({});

  // Refs for manual season scrolling (TV only, horizontal)
  const seasonScrollViewRef = useRef<ScrollView | null>(null);
  const seasonCardRefs = useRef<{ [key: string]: RNView | null }>({});

  const orderedSeasons = useMemo(() => {
    if (!seriesDetails?.seasons?.length) {
      return [] as SeriesDetails['seasons'];
    }

    const seasonsWithEpisodes = seriesDetails.seasons.filter(
      (season) => season.episodeCount > 0 || season.episodes.length > 0,
    );

    if (!seasonsWithEpisodes.length) {
      return [] as SeriesDetails['seasons'];
    }

    return seasonsWithEpisodes.sort((a, b) => a.number - b.number);
  }, [seriesDetails?.seasons]);

  const selectedSeason = useMemo(() => {
    if (!orderedSeasons.length) {
      return null;
    }
    // First try the explicitly selected season
    if (selectedSeasonNumber) {
      const match = orderedSeasons.find((season) => season.number === selectedSeasonNumber);
      if (match) return match;
    }
    // Then try the initial season from navigation params
    if (initialSeasonNumber !== null && initialSeasonNumber !== undefined) {
      const initialMatch = orderedSeasons.find((season) => season.number === initialSeasonNumber);
      if (initialMatch) return initialMatch;
    }
    // Finally fall back to first non-specials season or first season
    return orderedSeasons.find((season) => season.number > 0) ?? orderedSeasons[0];
  }, [orderedSeasons, selectedSeasonNumber, initialSeasonNumber]);

  const episodesBySeason = useMemo(() => {
    const map = new Map<number, SeriesEpisode[]>();
    orderedSeasons.forEach((season) => {
      const sorted = [...season.episodes].sort((a, b) => a.episodeNumber - b.episodeNumber);
      map.set(season.number, sorted);
    });
    return map;
  }, [orderedSeasons]);

  // Notify parent when episodes are loaded
  useEffect(() => {
    if (!onEpisodesLoaded || orderedSeasons.length === 0) {
      return;
    }

    const allEps: SeriesEpisode[] = [];
    orderedSeasons.forEach((season) => {
      allEps.push(...season.episodes);
    });

    onEpisodesLoaded(allEps);
  }, [orderedSeasons, onEpisodesLoaded]);

  // Notify parent when seasons are loaded
  useEffect(() => {
    if (!onSeasonsLoaded) {
      return;
    }

    onSeasonsLoaded(orderedSeasons);
  }, [orderedSeasons, onSeasonsLoaded]);

  // Notify parent of initial season selection when seasons are first loaded
  const hasNotifiedInitialSeasonRef = useRef(false);
  useEffect(() => {
    if (hasNotifiedInitialSeasonRef.current || !selectedSeason) {
      return;
    }
    // Only notify once when we first have a selected season
    hasNotifiedInitialSeasonRef.current = true;
    onSeasonSelect(selectedSeason, false);
  }, [selectedSeason, onSeasonSelect]);

  // Manual focus scrolling for episodes (TV only, vertical)
  const scrollToEpisode = useCallback((episodeKey: string) => {
    if (!Platform.isTV || !episodeScrollViewRef.current) {
      return;
    }

    const episodeRef = episodeRefs.current[episodeKey];
    if (!episodeRef) {
      return;
    }

    try {
      const scrollView = episodeScrollViewRef.current;

      episodeRef.measureLayout(
        scrollView as any,
        (left, top, _width, _height) => {
          // Scroll to show the episode with some offset
          const offsetFromTop = 80;
          const targetY = Math.max(0, top - offsetFromTop);

          scrollView?.scrollTo({ y: targetY, animated: true });
        },
        () => {
          if (__DEV__) {
            console.warn('❌ measureLayout failed for episode:', episodeKey);
          }
        },
      );
    } catch (error) {
      if (__DEV__) {
        console.warn('❌ measureLayout exception:', error);
      }
    }
  }, []);

  // Scroll to active episode on TV when activeEpisode changes
  useEffect(() => {
    if (!Platform.isTV || !activeEpisode || !episodeScrollViewRef.current) {
      return;
    }

    const episodeKey = `episode-${activeEpisode.id}`;
    // Small delay to ensure the episode is rendered and refs are populated
    const timer = setTimeout(() => {
      scrollToEpisode(episodeKey);
    }, 150);

    return () => clearTimeout(timer);
  }, [activeEpisode?.id, scrollToEpisode]);

  // Manual focus scrolling for seasons (TV only, horizontal)
  const scrollToSeason = useCallback((seasonKey: string) => {
    if (!Platform.isTV || !seasonScrollViewRef.current) {
      return;
    }

    const seasonCardRef = seasonCardRefs.current[seasonKey];
    if (!seasonCardRef) {
      return;
    }

    try {
      const scrollView = seasonScrollViewRef.current;

      seasonCardRef.measureLayout(
        scrollView as any,
        (left, _top, _width, _height) => {
          // Scroll to show the season card with some offset (horizontal scrolling)
          const offsetFromLeft = 100;
          const targetX = Math.max(0, left - offsetFromLeft);

          scrollView?.scrollTo({ x: targetX, animated: true });
        },
        () => {
          if (__DEV__) {
            console.warn('❌ measureLayout failed for season:', seasonKey);
          }
        },
      );
    } catch (error) {
      if (__DEV__) {
        console.warn('❌ measureLayout exception:', error);
      }
    }
  }, []);

  const focusSelectedSeason = useCallback(() => {
    if (isTouchSeasonLayout || orderedSeasons.length === 0) {
      return false;
    }

    const fallbackSeason =
      orderedSeasons.find((season) => season.number > 0)?.number ?? orderedSeasons[0]?.number ?? null;
    const desiredSeasonNumber = selectedSeason?.number ?? fallbackSeason;

    if (desiredSeasonNumber == null) {
      return false;
    }

    const targetRef = seasonFocusRefs.current.get(desiredSeasonNumber);
    if (targetRef?.focus) {
      targetRef.focus();
      return true;
    }

    for (const ref of seasonFocusRefs.current.values()) {
      if (ref?.focus) {
        ref.focus();
        return true;
      }
    }

    return false;
  }, [isTouchSeasonLayout, orderedSeasons, selectedSeason]);

  useEffect(() => {
    if (!onRegisterSeasonFocusHandler) {
      return;
    }

    if (isTouchSeasonLayout || orderedSeasons.length === 0) {
      onRegisterSeasonFocusHandler(null);
      return;
    }

    onRegisterSeasonFocusHandler(() => focusSelectedSeason());

    return () => {
      onRegisterSeasonFocusHandler(null);
    };
  }, [focusSelectedSeason, isTouchSeasonLayout, onRegisterSeasonFocusHandler, orderedSeasons.length]);

  useEffect(() => {
    if (!selectedSeason) {
      return;
    }

    const seasonEpisodes = episodesBySeason.get(selectedSeason.number) ?? [];
    if (seasonEpisodes.length === 0) {
      return;
    }

    const hasActiveEpisode =
      !!activeEpisode &&
      seasonEpisodes.some(
        (episode) =>
          episode.id === activeEpisode.id ||
          (episode.seasonNumber === activeEpisode.seasonNumber &&
            episode.episodeNumber === activeEpisode.episodeNumber),
      );

    console.log('[SeriesEpisodes] Episode selection check:', {
      selectedSeasonNumber: selectedSeason.number,
      seasonEpisodesCount: seasonEpisodes.length,
      hasActiveEpisode,
      activeEpisodeId: activeEpisode?.id,
      activeEpisodeNumber: activeEpisode?.episodeNumber,
      initialEpisodeNumber,
      initialEpisodeApplied: initialEpisodeAppliedRef.current,
    });

    if (hasActiveEpisode) {
      console.log('[SeriesEpisodes] Active episode already set, skipping selection');
      return;
    }

    // If we have an active episode but it's from a different season, don't auto-select
    // The season sync effect will handle switching seasons
    if (activeEpisode && activeEpisode.seasonNumber !== selectedSeason.number) {
      console.log('[SeriesEpisodes] Active episode is from a different season, skipping auto-selection');
      return;
    }

    if (!initialEpisodeAppliedRef.current && initialEpisodeNumber !== null) {
      const initialMatch = seasonEpisodes.find((episode) => episode.episodeNumber === initialEpisodeNumber);
      console.log('[SeriesEpisodes] Looking for initial episode:', {
        initialEpisodeNumber,
        foundMatch: !!initialMatch,
        matchedEpisodeId: initialMatch?.id,
      });
      if (initialMatch) {
        initialEpisodeAppliedRef.current = true;
        onEpisodeSelect(initialMatch);
        return;
      }
    }

    console.log('[SeriesEpisodes] Selecting first episode as fallback');
    initialEpisodeAppliedRef.current = true;
    onEpisodeSelect(seasonEpisodes[0] ?? null);
  }, [activeEpisode, episodesBySeason, initialEpisodeNumber, selectedSeason, onEpisodeSelect]);

  useEffect(() => {
    initialEpisodeAppliedRef.current = false;
  }, [title, tvdbId, titleId]);

  // Sync selected season when activeEpisode changes to a different season
  useEffect(() => {
    if (!activeEpisode || !orderedSeasons.length) {
      return;
    }

    const activeSeasonNumber = activeEpisode.seasonNumber;
    // Only update if the season is actually different
    if (activeSeasonNumber && activeSeasonNumber !== selectedSeasonNumber) {
      const seasonExists = orderedSeasons.some((s) => s.number === activeSeasonNumber);
      if (seasonExists) {
        console.log('[SeriesEpisodes] Syncing selected season to active episode season:', activeSeasonNumber);
        setSelectedSeasonNumber(activeSeasonNumber);
        // Mark that we've applied the initial episode to prevent re-selection
        initialEpisodeAppliedRef.current = true;
      }
    }
  }, [activeEpisode?.id, activeEpisode?.seasonNumber, orderedSeasons.length, selectedSeasonNumber]);

  const seasonPickerLabel = useMemo(() => {
    if (!selectedSeason) {
      return orderedSeasons.length ? 'Select a season' : 'No seasons available';
    }
    return getSeasonLabel(selectedSeason.number, selectedSeason.name);
  }, [orderedSeasons.length, selectedSeason]);

  const seasonPickerMeta = useMemo(() => {
    if (!selectedSeason) {
      return orderedSeasons.length ? 'Choose a season to browse episodes' : 'Season data pending';
    }
    return `Episodes • ${selectedSeason.episodeCount}`;
  }, [orderedSeasons.length, selectedSeason]);

  useEffect(() => {
    if (!orderedSeasons.length) {
      setSeasonPickerVisible(false);
    }
  }, [orderedSeasons.length]);

  useEffect(() => {
    if (!shouldUseSeasonModal) {
      setSeasonPickerVisible(false);
    }
  }, [shouldUseSeasonModal]);

  // Handle initial season selection when seriesDetails changes (from parent prop)
  useEffect(() => {
    if (!isSeries || !seriesDetails?.seasons?.length) {
      setSelectedSeasonNumber(null);
      setSeasonPickerVisible(false);
      return;
    }

    setSelectedSeasonNumber((current): number | null => {
      console.log('[SeriesEpisodes] Setting initial season:', {
        initialSeasonNumber,
        currentSeasonNumber: current,
        availableSeasons: seriesDetails.seasons.map((s) => s.number),
      });

      if (
        initialSeasonNumber !== null &&
        initialSeasonNumber !== undefined &&
        seriesDetails.seasons.some((season) => season.number === initialSeasonNumber)
      ) {
        console.log('[SeriesEpisodes] Using initialSeasonNumber:', initialSeasonNumber);
        return initialSeasonNumber;
      }
      if (current && seriesDetails.seasons.some((season) => season.number === current)) {
        console.log('[SeriesEpisodes] Using current season:', current);
        return current;
      }
      const primarySeason = seriesDetails.seasons.find((season) => season.number > 0) ?? seriesDetails.seasons[0];
      if (primarySeason && typeof primarySeason.number === 'number') {
        console.log('[SeriesEpisodes] Using primary season:', primarySeason.number);
        return primarySeason.number;
      }
      console.log('[SeriesEpisodes] No season found, returning null');
      return null;
    });
  }, [isSeries, seriesDetails, initialSeasonNumber]);

  const openSeasonPicker = useCallback(() => {
    setSeasonPickerVisible(true);
  }, []);

  const closeSeasonPicker = useCallback(() => {
    setSeasonPickerVisible(false);
  }, []);

  const handleSeasonSelect = useCallback(
    (season: SeriesSeason, shouldAutoplay: boolean) => {
      setSelectedSeasonNumber(season.number);
      onSeasonSelect(season, shouldAutoplay);
    },
    [onSeasonSelect],
  );

  const renderEpisodeThumbnail = useCallback(
    (episode: SeriesEpisode, isFocused?: boolean) => {
      const episodeCode = `S${padNumber(episode.seasonNumber)}E${padNumber(episode.episodeNumber)}`;
      if (episode.image?.url) {
        return (
          <View style={[styles.episodeThumbnail, isFocused && styles.episodeThumbnailFocused]}>
            <Image source={{ uri: episode.image.url }} style={styles.episodeThumbnailImage} resizeMode="cover" />
          </View>
        );
      }
      return (
        <View
          style={[
            styles.episodeThumbnail,
            styles.episodeThumbnailPlaceholder,
            isFocused && styles.episodeThumbnailFocused,
          ]}>
          <Text
            style={[styles.episodeThumbnailPlaceholderText, isFocused && styles.episodeThumbnailPlaceholderTextFocused]}
            numberOfLines={1}>
            {episodeCode}
          </Text>
        </View>
      );
    },
    [styles],
  );

  if (!isSeries) {
    return null;
  }

  if (!renderContent) {
    return null;
  }

  return (
    <View style={styles.seriesContainer}>
      <Text style={styles.seriesHeading}>Seasons & Episodes</Text>
      {seriesDetailsLoading && <Text style={styles.seriesStatus}>Loading season data…</Text>}
      {!seriesDetailsLoading && orderedSeasons.length === 0 && (
        <Text style={styles.seriesStatus}>Season data not available yet.</Text>
      )}
      {!seriesDetailsLoading && orderedSeasons.length > 0 && (
        <View style={[styles.seasonAndEpisodeWrapper, isTouchSeasonLayout && styles.seasonAndEpisodeWrapperStacked]}>
          {shouldUseSeasonModal ? (
            <>
              <Pressable
                accessibilityRole="button"
                onPress={openSeasonPicker}
                style={[styles.seasonPickerButton, seasonPickerVisible && styles.seasonPickerButtonActive]}>
                <Text style={styles.seasonPickerLabel} numberOfLines={1}>
                  {seasonPickerLabel}
                </Text>
                <Text style={styles.seasonPickerMeta} numberOfLines={1}>
                  {seasonPickerMeta}
                </Text>
              </Pressable>
              <Modal visible={seasonPickerVisible} transparent animationType="fade" onRequestClose={closeSeasonPicker}>
                <View style={styles.seasonModalOverlay}>
                  <Pressable style={styles.seasonModalBackdrop} onPress={closeSeasonPicker} />
                  <View style={styles.seasonModalContent}>
                    <Text style={styles.seasonModalTitle}>Select a season</Text>
                    <ScrollView
                      style={styles.seasonModalScroll}
                      contentContainerStyle={styles.seasonModalScrollContent}
                      showsVerticalScrollIndicator={false}>
                      {orderedSeasons.map((season) => {
                        const isActive = selectedSeason?.number === season.number;
                        const seasonLabel = getSeasonLabel(season.number, season.name);
                        return (
                          <Pressable
                            key={season.id || `${season.number}`}
                            onPress={() => {
                              handleSeasonSelect(season, false);
                              closeSeasonPicker();
                            }}
                            style={[styles.seasonModalOption, isActive && styles.seasonModalOptionActive]}>
                            <Text
                              style={[styles.seasonModalOptionLabel, isActive && styles.seasonModalOptionLabelActive]}
                              numberOfLines={1}>
                              {seasonLabel}
                            </Text>
                            <Text
                              style={[styles.seasonModalOptionMeta, isActive && styles.seasonModalOptionMetaActive]}
                              numberOfLines={1}>
                              {`Episodes • ${season.episodeCount}`}
                            </Text>
                          </Pressable>
                        );
                      })}
                    </ScrollView>
                    <Pressable
                      accessibilityRole="button"
                      onPress={closeSeasonPicker}
                      style={styles.seasonModalCloseButton}>
                      <Text style={styles.seasonModalCloseButtonText}>Cancel</Text>
                    </Pressable>
                  </View>
                </View>
              </Modal>
            </>
          ) : isTouchSeasonLayout ? (
            <ScrollView
              horizontal
              showsHorizontalScrollIndicator={false}
              contentContainerStyle={styles.seasonScrollContent}
              style={styles.seasonScroll}>
              {orderedSeasons.map((season) => {
                const isActive = selectedSeason?.number === season.number;
                const seasonLabel = getSeasonLabel(season.number, season.name);
                return (
                  <Pressable
                    key={season.id || `${season.number}`}
                    onPress={() => handleSeasonSelect(season, false)}
                    style={[styles.seasonCard, isActive && styles.seasonCardActive]}>
                    <Text style={[styles.seasonCardTitle, isActive && styles.seasonCardTitleActive]} numberOfLines={1}>
                      {seasonLabel}
                    </Text>
                    <Text style={[styles.seasonCardMeta, isActive && styles.seasonCardMetaActive]} numberOfLines={1}>
                      {`Episodes • ${season.episodeCount}`}
                    </Text>
                    {season.type && (
                      <Text style={[styles.seasonCardType, isActive && styles.seasonCardTypeActive]} numberOfLines={1}>
                        {season.type}
                      </Text>
                    )}
                  </Pressable>
                );
              })}
            </ScrollView>
          ) : (
            <SpatialNavigationNode orientation="horizontal">
              <ScrollView
                ref={seasonScrollViewRef}
                horizontal
                showsHorizontalScrollIndicator={false}
                scrollEnabled={false}
                contentContainerStyle={styles.seasonScrollContent}
                style={styles.seasonScroll}>
                {orderedSeasons.map((season, index) => {
                  const isActive = selectedSeason?.number === season.number;
                  const seasonLabel = getSeasonLabel(season.number, season.name);
                  const seasonKey = season.id || `${season.number}`;

                  const handleSeasonFocus = () => {
                    setSelectedSeasonNumber(season.number);
                    scrollToSeason(seasonKey);
                  };

                  const focusable = (
                    <SpatialNavigationFocusableView
                      ref={(node: SpatialNavigationNodeRef | null) => {
                        if (node) {
                          seasonFocusRefs.current.set(season.number, node);
                        } else {
                          seasonFocusRefs.current.delete(season.number);
                        }
                      }}
                      onFocus={handleSeasonFocus}
                      onSelect={() => {
                        // On TV platforms, season button select does nothing
                        // Only focus changes the season, not selection
                      }}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <View
                          ref={(ref) => {
                            seasonCardRefs.current[seasonKey] = ref;
                          }}
                          style={[
                            styles.seasonCard,
                            isActive && styles.seasonCardActive,
                            isFocused && styles.seasonCardFocused,
                          ]}>
                          <MarqueeText
                            style={[styles.seasonCardTitle, (isFocused || isActive) ? styles.seasonCardTitleActive : undefined]}
                            containerStyle={styles.seasonCardTitleContainer}
                            focused={isFocused}
                            speed={30}
                            delay={400}>
                            {seasonLabel}
                          </MarqueeText>
                          <Text
                            style={[styles.seasonCardMeta, (isFocused || isActive) && styles.seasonCardMetaActive]}
                            numberOfLines={1}>
                            {`Episodes • ${season.episodeCount}`}
                          </Text>
                          {season.type && (
                            <Text
                              style={[styles.seasonCardType, (isFocused || isActive) && styles.seasonCardTypeActive]}
                              numberOfLines={1}>
                              {season.type}
                            </Text>
                          )}
                        </View>
                      )}
                    </SpatialNavigationFocusableView>
                  );

                  // Default focus to selected season, or first season if none selected
                  if (isActive || (!selectedSeason && index === 0)) {
                    return <DefaultFocus key={seasonKey}>{focusable}</DefaultFocus>;
                  }

                  return <Fragment key={seasonKey}>{focusable}</Fragment>;
                })}
              </ScrollView>
            </SpatialNavigationNode>
          )}

          {selectedSeason && (
            <View style={styles.episodesContainer}>
              {selectedSeason.overview ? (
                <Text style={styles.seasonOverview} numberOfLines={3}>
                  {selectedSeason.overview}
                </Text>
              ) : null}
              {selectedSeason.episodes.length === 0 && (
                <Text style={styles.seriesStatus}>No episodes listed for this season yet.</Text>
              )}
              {isTouchSeasonLayout ? (
                <View style={[styles.episodeScrollContent, styles.episodeListTouch]}>
                  {selectedSeason.episodes.map((episode) => {
                    const episodeFlashKey = `${episode.seasonNumber}-${episode.episodeNumber}`;
                    const isActive = episodesMatch(activeEpisode, episode);
                    const watched = isEpisodeWatched?.(episode) ?? false;
                    const isFlashing = flashingEpisode === episodeFlashKey;
                    const watchIconColor = isFlashing
                      ? theme.colors.status.success
                      : isActive
                        ? watched
                          ? '#ffffff'
                          : 'rgba(255, 255, 255, 0.7)'
                        : watched
                          ? theme.colors.accent.primary
                          : theme.colors.text.secondary;
                    return (
                      <Pressable
                        key={episode.id}
                        onPress={() => onEpisodeSelect(episode)}
                        onLongPress={() => {
                          setFlashingEpisode(episodeFlashKey);
                          setTimeout(() => setFlashingEpisode(null), 600);
                          onEpisodeLongPress(episode);
                        }}
                        delayLongPress={500}
                        style={[
                          styles.episodeCard,
                          isActive && styles.episodeCardFocused,
                          isFlashing && styles.episodeCardFlashing,
                        ]}>
                        <View style={styles.episodeThumbnailColumn}>
                          {renderEpisodeThumbnail(episode, isActive)}
                          {onToggleEpisodeWatched && (
                            <Pressable
                              onPress={(e) => {
                                e.stopPropagation();
                                setFlashingEpisode(episodeFlashKey);
                                setTimeout(() => setFlashingEpisode(null), 600);
                                onToggleEpisodeWatched(episode);
                              }}
                              style={[styles.watchButtonTouch, isFlashing && styles.watchButtonFlashing]}>
                              <Ionicons name={watched ? 'eye' : 'eye-outline'} size={20} color={watchIconColor} />
                            </Pressable>
                          )}
                        </View>
                        <View style={styles.episodeDetails}>
                          <Text style={[styles.episodeTitle, isActive && styles.episodeTitleFocused]} numberOfLines={2}>
                            {episode.name || `Episode ${episode.episodeNumber}`}
                          </Text>
                          <View style={styles.episodeMetaRow}>
                            {isEpisodeUnreleased(episode.airedDate) && (
                              <Ionicons name="time" size={12} color={isActive ? '#ffffff' : theme.colors.status.warning} />
                            )}
                            <Text style={[styles.episodeMeta, isActive && styles.episodeMetaFocused]} numberOfLines={1}>
                              {`S${padNumber(episode.seasonNumber)}E${padNumber(episode.episodeNumber)}`}
                              {episode.airedDate ? ` • ${formatPublishDate(episode.airedDate)}` : ''}
                              {episode.runtimeMinutes ? ` • ${episode.runtimeMinutes}m` : ''}
                            </Text>
                          </View>
                          {episode.overview && (
                            <Text style={styles.episodeOverview} numberOfLines={3}>
                              {episode.overview}
                            </Text>
                          )}
                        </View>
                      </Pressable>
                    );
                  })}
                </View>
              ) : (
                <SpatialNavigationNode orientation="vertical">
                  <ScrollView
                    ref={episodeScrollViewRef}
                    style={styles.episodeScroll}
                    contentContainerStyle={styles.episodeScrollContent}
                    showsVerticalScrollIndicator>
                    {selectedSeason.episodes.map((episode, index) => {
                      const episodeKey = `episode-${episode.id}`;
                      const episodeFlashKey = `${episode.seasonNumber}-${episode.episodeNumber}`;
                      const watched = isEpisodeWatched?.(episode) ?? false;
                      const isActiveEpisode = episodesMatch(activeEpisode, episode);
                      const isFlashing = flashingEpisode === episodeFlashKey;

                      const handleEpisodeFocus = () => {
                        scrollToEpisode(episodeKey);
                        onEpisodeFocus(episode);
                      };

                      const handleEpisodeSelect = () => {
                        // On TV, onSelect is the primary action (play episode)
                        onPlayEpisode(episode);
                      };

                      const handleEpisodeLongPressWithFlash = () => {
                        setFlashingEpisode(episodeFlashKey);
                        setTimeout(() => setFlashingEpisode(null), 600);
                        onEpisodeLongPress(episode);
                      };

                      const focusable = (
                        <SpatialNavigationFocusableView
                          key={episode.id}
                          onFocus={handleEpisodeFocus}
                          onSelect={handleEpisodeSelect}
                          onLongSelect={handleEpisodeLongPressWithFlash}
                          focusKey={episodeKey}>
                          {({ isFocused }: { isFocused: boolean }) => {
                            const watchIconColor = isFlashing
                              ? theme.colors.status.success
                              : isFocused
                                ? watched
                                  ? '#ffffff'
                                  : 'rgba(255, 255, 255, 0.7)'
                                : watched
                                  ? theme.colors.accent.primary
                                  : theme.colors.text.secondary;
                            return (
                              <Pressable
                                onPress={handleEpisodeSelect}
                                onLongPress={handleEpisodeLongPressWithFlash}
                                delayLongPress={500}
                                style={[
                                  styles.episodeCard,
                                  isFocused && styles.episodeCardFocused,
                                  isFlashing && styles.episodeCardFlashing,
                                ]}>
                                <View
                                  ref={(ref) => {
                                    episodeRefs.current[episodeKey] = ref;
                                  }}
                                  style={{
                                    flexDirection: 'row',
                                    alignItems: 'flex-start',
                                    gap: styles.episodeCard.gap,
                                  }}>
                                  <View style={styles.episodeThumbnailColumn}>
                                    {renderEpisodeThumbnail(episode, isFocused)}
                                    {onToggleEpisodeWatched && (
                                      <Pressable
                                        onPress={(e) => {
                                          e.stopPropagation();
                                          setFlashingEpisode(episodeFlashKey);
                                          setTimeout(() => setFlashingEpisode(null), 600);
                                          onToggleEpisodeWatched(episode);
                                        }}
                                        style={[styles.watchButtonTV, isFlashing && styles.watchButtonFlashing]}>
                                        <Ionicons
                                          name={watched ? 'eye' : 'eye-outline'}
                                          size={24}
                                          color={watchIconColor}
                                        />
                                      </Pressable>
                                    )}
                                  </View>
                                  <View style={styles.episodeDetails}>
                                    <Text
                                      style={[styles.episodeTitle, isFocused && styles.episodeTitleFocused]}
                                      numberOfLines={2}>
                                      {episode.name || `Episode ${episode.episodeNumber}`}
                                    </Text>
                                    <View style={styles.episodeMetaRow}>
                                      {isEpisodeUnreleased(episode.airedDate) && (
                                        <Ionicons name="time" size={12} color={isFocused ? '#ffffff' : theme.colors.status.warning} />
                                      )}
                                      <Text
                                        style={[styles.episodeMeta, isFocused && styles.episodeMetaFocused]}
                                        numberOfLines={1}>
                                        {`S${padNumber(episode.seasonNumber)}E${padNumber(episode.episodeNumber)}`}
                                        {episode.airedDate ? ` • ${formatPublishDate(episode.airedDate)}` : ''}
                                        {episode.runtimeMinutes ? ` • ${episode.runtimeMinutes}m` : ''}
                                      </Text>
                                    </View>
                                    {episode.overview && (
                                      <Text
                                        style={[styles.episodeOverview, isFocused && styles.episodeOverviewFocused]}
                                        numberOfLines={3}>
                                        {episode.overview}
                                      </Text>
                                    )}
                                  </View>
                                </View>
                              </Pressable>
                            );
                          }}
                        </SpatialNavigationFocusableView>
                      );

                      // Default focus to active episode, or first episode if none active
                      if (isActiveEpisode || (!activeEpisode && index === 0)) {
                        return <DefaultFocus key={`episode-default-${episode.id}`}>{focusable}</DefaultFocus>;
                      }

                      return focusable;
                    })}
                  </ScrollView>
                </SpatialNavigationNode>
              )}
            </View>
          )}
        </View>
      )}
    </View>
  );
};

const createSeriesEpisodesStyles = (theme: NovaTheme) => {
  const isCompactBreakpoint = theme.breakpoint === 'compact';
  const selectedTextColor = '#ffffff';
  const episodeThumbnailWidth = isCompactBreakpoint ? 140 : 200;

  return StyleSheet.create({
    seriesContainer: {
      marginTop: isTVPlatform() ? theme.spacing.md : theme.spacing.lg,
      gap: isTVPlatform() ? theme.spacing.md : theme.spacing.lg,
      flex: 1,
    },
    seriesHeading: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    seriesStatus: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    seriesError: {
      ...theme.typography.body.md,
      color: theme.colors.status.danger,
    },
    seasonAndEpisodeWrapper: {
      flex: 1,
    },
    seasonAndEpisodeWrapperStacked: {
      gap: theme.spacing.md,
    },
    seasonScroll: {
      width: '100%',
      flexGrow: 0,
      flexShrink: 0,
    },
    seasonScrollContent: {
      flexDirection: 'row',
      gap: isTVPlatform() ? theme.spacing.xs : theme.spacing.md,
      paddingVertical: isTVPlatform() ? theme.spacing.none : theme.spacing.sm,
    },
    seasonPickerButton: {
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.lg,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.xs,
    },
    seasonPickerButtonActive: {
      borderColor: theme.colors.accent.primary,
    },
    seasonPickerLabel: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },
    seasonPickerMeta: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
    },
    seasonModalOverlay: {
      flex: 1,
      backgroundColor: 'rgba(0, 0, 0, 0.65)',
      justifyContent: 'center',
      alignItems: 'center',
      padding: theme.spacing.lg,
    },
    seasonModalBackdrop: {
      ...StyleSheet.absoluteFillObject,
    },
    seasonModalContent: {
      width: '100%',
      maxWidth: 420,
      borderRadius: theme.radius.lg,
      backgroundColor: theme.colors.background.surface,
      padding: theme.spacing.lg,
      gap: theme.spacing.md,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    seasonModalTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
    },
    seasonModalScroll: {
      maxHeight: 360,
    },
    seasonModalScrollContent: {
      gap: theme.spacing.sm,
      paddingBottom: theme.spacing.sm,
    },
    seasonModalOption: {
      borderRadius: theme.radius.md,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.surface,
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.md,
      gap: theme.spacing.xs,
    },
    seasonModalOptionActive: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    seasonModalOptionLabel: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },
    seasonModalOptionLabelActive: {
      color: selectedTextColor,
    },
    seasonModalOptionMeta: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
    },
    seasonModalOptionMetaActive: {
      color: selectedTextColor,
    },
    seasonModalCloseButton: {
      alignSelf: 'flex-end',
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.sm,
    },
    seasonModalCloseButtonText: {
      ...theme.typography.label.md,
      color: theme.colors.accent.primary,
    },
    seasonCard: {
      minWidth: 160,
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.lg,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    seasonCardActive: {
      borderColor: theme.colors.accent.primary,
    },
    seasonCardFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    seasonCardTitleContainer: {
      maxWidth: 90, // Truncate long titles (~10 chars), marquee scrolls on focus
    },
    seasonCardTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },
    seasonCardTitleActive: {
      color: selectedTextColor,
    },
    seasonCardMeta: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.xs,
    },
    seasonCardMetaActive: {
      color: selectedTextColor,
    },
    seasonCardType: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      marginTop: theme.spacing.xs,
    },
    seasonCardTypeActive: {
      color: selectedTextColor,
    },
    episodesContainer: {
      gap: isTVPlatform() ? theme.spacing.sm : theme.spacing.md,
      flex: 1,
      marginTop: isTVPlatform() ? theme.spacing.xs : theme.spacing.sm,
    },
    seasonOverview: {
      ...theme.typography.title.md,
      color: theme.colors.text.secondary,
      fontWeight: '400',
    },
    episodeScroll: {
      flex: 1,
    },
    episodeListTouch: {
      width: '100%',
    },
    episodeScrollContent: {
      gap: isTVPlatform() ? theme.spacing.sm : theme.spacing.md,
      paddingBottom: isTVPlatform() ? theme.spacing.xs : theme.spacing.sm,
    },
    episodeCard: {
      flexDirection: 'row',
      alignItems: 'flex-start',
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.lg,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.md,
    },
    episodeCardFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    episodeCardFlashing: {
      borderColor: theme.colors.status.success,
      borderWidth: 2,
    },
    episodeThumbnail: {
      width: episodeThumbnailWidth,
      aspectRatio: 16 / 9,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    episodeThumbnailFocused: {
      borderColor: theme.colors.accent.primary,
    },
    episodeThumbnailImage: {
      width: '100%',
      height: '100%',
    },
    episodeThumbnailPlaceholder: {
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: theme.colors.background.surface,
    },
    episodeThumbnailPlaceholderText: {
      ...theme.typography.label.md,
      color: theme.colors.text.secondary,
    },
    episodeThumbnailPlaceholderTextFocused: {
      color: selectedTextColor,
    },
    episodeDetails: {
      flex: 1,
      gap: theme.spacing.xs,
    },
    episodeThumbnailColumn: {
      flexDirection: 'column',
      gap: theme.spacing.xs,
    },
    watchButtonTouch: {
      alignItems: 'center',
      justifyContent: 'center',
      paddingVertical: theme.spacing.xs,
    },
    watchButtonTV: {
      alignItems: 'center',
      justifyContent: 'center',
      paddingVertical: theme.spacing.xs,
    },
    watchButtonFlashing: {
      transform: [{ scale: 1.3 }],
    },
    episodeTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
    },
    episodeTitleFocused: {
      color: selectedTextColor,
    },
    episodeMetaRow: {
      flexDirection: 'row' as const,
      alignItems: 'center' as const,
      gap: theme.spacing.xs,
    },
    episodeMeta: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    episodeMetaFocused: {
      color: selectedTextColor,
    },
    episodeOverview: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    episodeOverviewFocused: {
      color: selectedTextColor,
    },
  });
};
