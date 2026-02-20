/**
 * useWatchActions — owns watchlist toggle, watched status, and bulk mark watched/unwatched
 */

import { useCallback, useState } from 'react';
import type { SeriesEpisode, SeriesSeason, EpisodeWatchPayload } from '@/services/api';

interface UseWatchActionsParams {
  titleId: string;
  title: string;
  description: string;
  mediaType: string;
  isSeries: boolean;
  seriesIdentifier: string;
  yearNumber: number | undefined;
  posterUrl: string;
  backdropUrl: string;
  externalIds: Record<string, string> | undefined;
  genres?: string[];
  runtimeMinutes?: number;
  activeUserId: string | null;

  // From context providers
  addToWatchlist: (item: {
    id: string;
    mediaType: string;
    name: string;
    overview: string;
    year?: number;
    posterUrl?: string;
    backdropUrl?: string;
    externalIds?: Record<string, string>;
    genres?: string[];
    runtimeMinutes?: number;
  }) => Promise<unknown>;
  removeFromWatchlist: (mediaType: string, titleId: string) => Promise<void>;
  getItem: (mediaType: string, titleId: string) => unknown;
  isItemWatched: (mediaType: string, itemId: string) => boolean;
  toggleWatchStatus: (
    mediaType: string,
    itemId: string,
    metadata?: { name?: string; year?: number; externalIds?: Record<string, string> },
  ) => Promise<void>;
  bulkUpdateWatchStatus: (
    updates: Array<{
      mediaType: string;
      itemId: string;
      name?: string;
      watched: boolean;
      seasonNumber?: number;
      episodeNumber?: number;
      seriesId?: string;
      seriesName?: string;
    }>,
  ) => Promise<void>;
  refreshWatchStatus: () => Promise<void>;
  recordEpisodeWatch: (payload: EpisodeWatchPayload) => Promise<unknown>;

  // Episode data (from useEpisodeManager)
  allEpisodes: SeriesEpisode[];
  activeEpisode: SeriesEpisode | null;
  nextUpEpisode: SeriesEpisode | null;

  // Helpers
  findFirstEpisode: () => SeriesEpisode | null;
  findFirstEpisodeOfNextSeason: (seasonNumber: number) => SeriesEpisode | null;
  findNextEpisode: (episode: SeriesEpisode) => SeriesEpisode | null;
  handleEpisodeSelect: (episode: SeriesEpisode) => void;
  toEpisodeReference: (episode: SeriesEpisode) => EpisodeWatchPayload['episode'];
  dismissTrailerAutoPlay: () => void;
}

export interface WatchActionsResult {
  // Watchlist
  watchlistBusy: boolean;
  watchlistError: string | null;
  isWatchlisted: boolean;
  isWatched: boolean;
  canToggleWatchlist: boolean;
  watchlistButtonLabel: string;
  watchStateButtonLabel: string;

  // Bulk watch modal
  bulkWatchModalVisible: boolean;
  setBulkWatchModalVisible: React.Dispatch<React.SetStateAction<boolean>>;

  // Actions
  handleToggleWatchlist: () => Promise<void>;
  handleToggleWatched: () => Promise<void>;
  handleMarkAllWatched: () => Promise<void>;
  handleMarkAllUnwatched: () => Promise<void>;
  handleMarkSeasonWatched: (season: SeriesSeason) => Promise<void>;
  handleMarkSeasonUnwatched: (season: SeriesSeason) => Promise<void>;
  handleToggleEpisodeWatched: (episode: SeriesEpisode) => Promise<void>;
  isEpisodeWatched: (episode: SeriesEpisode) => boolean;
  recordEpisodePlayback: (episode: SeriesEpisode) => void;
}

export function useWatchActions(params: UseWatchActionsParams): WatchActionsResult {
  const {
    titleId,
    title,
    description,
    mediaType,
    isSeries,
    seriesIdentifier,
    yearNumber,
    posterUrl,
    backdropUrl,
    externalIds,
    genres,
    runtimeMinutes,
    activeUserId,
    addToWatchlist,
    removeFromWatchlist,
    getItem,
    isItemWatched,
    toggleWatchStatus,
    bulkUpdateWatchStatus,
    refreshWatchStatus,
    recordEpisodeWatch,
    allEpisodes,
    activeEpisode,
    nextUpEpisode,
    findFirstEpisode,
    findFirstEpisodeOfNextSeason,
    findNextEpisode,
    handleEpisodeSelect,
    toEpisodeReference,
    dismissTrailerAutoPlay,
  } = params;

  const [watchlistBusy, setWatchlistBusy] = useState(false);
  const [watchlistError, setWatchlistError] = useState<string | null>(null);
  const [bulkWatchModalVisible, setBulkWatchModalVisible] = useState(false);

  const watchlistItem = getItem(mediaType, titleId);
  const isWatchlisted = Boolean(watchlistItem);
  const canToggleWatchlist = Boolean(titleId && mediaType);

  // For series, check the current episode's watched status; for movies, check the title
  const currentEpisodeForWatchState = activeEpisode || nextUpEpisode;
  const isWatched = (() => {
    if (!titleId) return false;
    if (isSeries && currentEpisodeForWatchState && seriesIdentifier) {
      const episodeId = `${seriesIdentifier}:s${String(currentEpisodeForWatchState.seasonNumber).padStart(2, '0')}e${String(currentEpisodeForWatchState.episodeNumber).padStart(2, '0')}`;
      return isItemWatched('episode', episodeId);
    }
    return isItemWatched(mediaType, titleId);
  })();

  const watchlistButtonLabel = isWatchlisted ? 'Remove' : 'Watchlist';
  const watchStateButtonLabel = isSeries ? 'Watch State' : isWatched ? 'Mark as not watched' : 'Mark as watched';

  const handleToggleWatchlist = useCallback(async () => {
    if (!canToggleWatchlist || watchlistBusy) return;
    dismissTrailerAutoPlay();
    setWatchlistError(null);
    setWatchlistBusy(true);
    try {
      if (isWatchlisted) {
        await removeFromWatchlist(mediaType, titleId);
      } else {
        await addToWatchlist({
          id: titleId,
          mediaType,
          name: title,
          overview: description,
          year: yearNumber,
          posterUrl,
          backdropUrl,
          externalIds,
          genres,
          runtimeMinutes,
        });
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unable to update watchlist.';
      setWatchlistError(message);
      console.error('Watchlist update failed:', err);
    } finally {
      setWatchlistBusy(false);
    }
  }, [
    dismissTrailerAutoPlay, addToWatchlist, backdropUrl, canToggleWatchlist,
    description, externalIds, genres, runtimeMinutes, isWatchlisted, mediaType, posterUrl,
    removeFromWatchlist, title, titleId, watchlistBusy, yearNumber,
  ]);

  const handleToggleWatched = useCallback(async () => {
    if (!canToggleWatchlist || watchlistBusy) return;
    dismissTrailerAutoPlay();
    if (isSeries) {
      setBulkWatchModalVisible(true);
      return;
    }
    setWatchlistError(null);
    setWatchlistBusy(true);
    try {
      await toggleWatchStatus(mediaType, titleId, {
        name: title,
        year: yearNumber,
        externalIds,
      });
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unable to update watched state.';
      setWatchlistError(message);
      console.error('Unable to update watched state:', err);
    } finally {
      setWatchlistBusy(false);
    }
  }, [
    dismissTrailerAutoPlay, canToggleWatchlist, externalIds, isSeries,
    mediaType, title, titleId, toggleWatchStatus, watchlistBusy, yearNumber,
  ]);

  const buildEpisodeItemId = useCallback(
    (episode: SeriesEpisode) =>
      `${seriesIdentifier}:s${String(episode.seasonNumber).padStart(2, '0')}e${String(episode.episodeNumber).padStart(2, '0')}`,
    [seriesIdentifier],
  );

  const handleMarkAllWatched = useCallback(async () => {
    if (!seriesIdentifier || allEpisodes.length === 0) return;
    setWatchlistBusy(true);
    setWatchlistError(null);
    try {
      const updates = allEpisodes.map((episode) => ({
        mediaType: 'episode',
        itemId: buildEpisodeItemId(episode),
        name: episode.name,
        watched: true,
        seasonNumber: episode.seasonNumber,
        episodeNumber: episode.episodeNumber,
        seriesId: seriesIdentifier,
        seriesName: title,
      }));
      await bulkUpdateWatchStatus(updates);
      await refreshWatchStatus();
      const firstEp = findFirstEpisode();
      if (firstEp) handleEpisodeSelect(firstEp);
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unable to mark all episodes as watched.';
      setWatchlistError(message);
      console.error('Unable to mark all episodes as watched:', err);
    } finally {
      setWatchlistBusy(false);
    }
  }, [seriesIdentifier, allEpisodes, title, bulkUpdateWatchStatus, refreshWatchStatus, findFirstEpisode, handleEpisodeSelect, buildEpisodeItemId]);

  const handleMarkAllUnwatched = useCallback(async () => {
    if (!seriesIdentifier || allEpisodes.length === 0) return;
    setWatchlistBusy(true);
    setWatchlistError(null);
    try {
      const updates = allEpisodes.map((episode) => ({
        mediaType: 'episode',
        itemId: buildEpisodeItemId(episode),
        name: episode.name,
        watched: false,
        seasonNumber: episode.seasonNumber,
        episodeNumber: episode.episodeNumber,
        seriesId: seriesIdentifier,
        seriesName: title,
      }));
      await bulkUpdateWatchStatus(updates);
      await refreshWatchStatus();
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Unable to mark all episodes as unwatched.';
      setWatchlistError(message);
      console.error('Unable to mark all episodes as unwatched:', err);
    } finally {
      setWatchlistBusy(false);
    }
  }, [seriesIdentifier, allEpisodes, title, bulkUpdateWatchStatus, refreshWatchStatus, buildEpisodeItemId]);

  const handleMarkSeasonWatched = useCallback(
    async (season: SeriesSeason) => {
      if (!seriesIdentifier) return;
      setWatchlistBusy(true);
      setWatchlistError(null);
      try {
        const updates = season.episodes.map((episode) => ({
          mediaType: 'episode',
          itemId: buildEpisodeItemId(episode),
          name: episode.name,
          watched: true,
          seasonNumber: episode.seasonNumber,
          episodeNumber: episode.episodeNumber,
          seriesId: seriesIdentifier,
          seriesName: title,
        }));
        await bulkUpdateWatchStatus(updates);
        await refreshWatchStatus();
        const firstOfNext = findFirstEpisodeOfNextSeason(season.number);
        if (firstOfNext) handleEpisodeSelect(firstOfNext);
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Unable to mark season as watched.';
        setWatchlistError(message);
        console.error('Unable to mark season as watched:', err);
      } finally {
        setWatchlistBusy(false);
      }
    },
    [seriesIdentifier, title, bulkUpdateWatchStatus, refreshWatchStatus, findFirstEpisodeOfNextSeason, handleEpisodeSelect, buildEpisodeItemId],
  );

  const handleMarkSeasonUnwatched = useCallback(
    async (season: SeriesSeason) => {
      if (!seriesIdentifier) return;
      setWatchlistBusy(true);
      setWatchlistError(null);
      try {
        const updates = season.episodes.map((episode) => ({
          mediaType: 'episode',
          itemId: buildEpisodeItemId(episode),
          name: episode.name,
          watched: false,
          seasonNumber: episode.seasonNumber,
          episodeNumber: episode.episodeNumber,
          seriesId: seriesIdentifier,
          seriesName: title,
        }));
        await bulkUpdateWatchStatus(updates);
        await refreshWatchStatus();
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Unable to mark season as unwatched.';
        setWatchlistError(message);
        console.error('Unable to mark season as unwatched:', err);
      } finally {
        setWatchlistBusy(false);
      }
    },
    [seriesIdentifier, title, bulkUpdateWatchStatus, refreshWatchStatus, buildEpisodeItemId],
  );

  const handleToggleEpisodeWatched = useCallback(
    async (episode: SeriesEpisode) => {
      if (!seriesIdentifier) return;
      setWatchlistBusy(true);
      setWatchlistError(null);
      try {
        const episodeId = buildEpisodeItemId(episode);
        await toggleWatchStatus('episode', episodeId, {
          name: episode.name,
        });
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Unable to update episode watched state.';
        setWatchlistError(message);
        console.error('Unable to update episode watched state:', err);
      } finally {
        setWatchlistBusy(false);
      }
    },
    [seriesIdentifier, toggleWatchStatus, buildEpisodeItemId],
  );

  const isEpisodeWatchedFn = useCallback(
    (episode: SeriesEpisode): boolean => {
      if (!seriesIdentifier) return false;
      const episodeId = buildEpisodeItemId(episode);
      return isItemWatched('episode', episodeId);
    },
    [seriesIdentifier, isItemWatched, buildEpisodeItemId],
  );

  const recordEpisodePlayback = useCallback(
    (episode: SeriesEpisode) => {
      if (!isSeries || !seriesIdentifier) return;
      const nextEp = findNextEpisode(episode);
      const ids: Record<string, string> = {};
      if (externalIds) Object.assign(ids, externalIds);
      const payload: EpisodeWatchPayload = {
        seriesId: seriesIdentifier,
        seriesTitle: title,
        posterUrl: posterUrl || undefined,
        backdropUrl: backdropUrl || undefined,
        year: yearNumber,
        externalIds: Object.keys(ids).length > 0 ? ids : undefined,
        episode: toEpisodeReference(episode),
        nextEpisode: nextEp ? toEpisodeReference(nextEp) : undefined,
      };
      recordEpisodeWatch(payload).catch((err) => {
        console.warn('Unable to record watch history:', err);
      });
    },
    [backdropUrl, findNextEpisode, externalIds, isSeries, posterUrl, recordEpisodeWatch, seriesIdentifier, title, toEpisodeReference, yearNumber],
  );

  return {
    watchlistBusy,
    watchlistError,
    isWatchlisted,
    isWatched,
    canToggleWatchlist,
    watchlistButtonLabel,
    watchStateButtonLabel,
    bulkWatchModalVisible,
    setBulkWatchModalVisible,
    handleToggleWatchlist,
    handleToggleWatched,
    handleMarkAllWatched,
    handleMarkAllUnwatched,
    handleMarkSeasonWatched,
    handleMarkSeasonUnwatched,
    handleToggleEpisodeWatched,
    isEpisodeWatched: isEpisodeWatchedFn,
    recordEpisodePlayback,
  };
}
