/**
 * useEpisodeManager — owns episode/season state, selection, navigation, and shuffle
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { apiService, type SeriesEpisode, type SeriesSeason, type DetailsBundleData } from '@/services/api';
import { buildEpisodeQuery, isEpisodeUnreleased, padNumber } from '../utils';

interface UseEpisodeManagerParams {
  isSeries: boolean;
  seriesIdentifier: string;
  title: string;
  activeUserId: string | null;
  detailsBundle: DetailsBundleData | null;
  bundleReady: boolean;
  /** Ref to resolveAndPlay from usePlayback (avoids circular dep) */
  resolveAndPlayRef: React.MutableRefObject<
    ((args: {
      query: string;
      friendlyLabel: string;
      limit?: number;
      selectionMessage?: string | null;
      useDebugPlayer?: boolean;
      targetEpisode?: { seasonNumber: number; episodeNumber: number; airedDate?: string };
    }) => Promise<void>) | null
  >;
  dismissTrailerAutoPlay: () => void;
  showLoadingScreenIfEnabled: () => Promise<void>;
  /** pendingShuffleModeRef shared with usePlayback */
  pendingShuffleModeRef: React.MutableRefObject<boolean>;
  /** Next episode passed from player via navigation */
  nextEpisodeFromPlayback: {
    seasonNumber: number;
    episodeNumber: number;
    autoPlay?: boolean;
  } | null;
  setNextEpisodeFromPlayback: React.Dispatch<
    React.SetStateAction<{
      seasonNumber: number;
      episodeNumber: number;
      autoPlay?: boolean;
    } | null>
  >;
  /** Resume modal integration */
  setCurrentProgress: React.Dispatch<React.SetStateAction<{
    position: number;
    duration: number;
    percentWatched: number;
  } | null>>;
  setPendingPlaybackAction: React.Dispatch<React.SetStateAction<((startOffset?: number) => Promise<void>) | null>>;
  setResumeModalVisible: React.Dispatch<React.SetStateAction<boolean>>;
  pendingStartOffsetRef: React.MutableRefObject<number | null>;
  setSelectionError: React.Dispatch<React.SetStateAction<string | null>>;
  setSelectionInfo: React.Dispatch<React.SetStateAction<string | null>>;
  imdbId: string;
  tmdbId: string;
  tvdbId: string;
  /** Lifted state — shared between useEpisodeManager and usePlayback */
  activeEpisode: SeriesEpisode | null;
  setActiveEpisode: React.Dispatch<React.SetStateAction<SeriesEpisode | null>>;
  nextUpEpisode: SeriesEpisode | null;
  setNextUpEpisode: React.Dispatch<React.SetStateAction<SeriesEpisode | null>>;
  isShuffleMode: boolean;
  setIsShuffleMode: React.Dispatch<React.SetStateAction<boolean>>;
}

export interface EpisodeManagerResult {
  activeEpisode: SeriesEpisode | null;
  setActiveEpisode: React.Dispatch<React.SetStateAction<SeriesEpisode | null>>;
  nextUpEpisode: SeriesEpisode | null;
  allEpisodes: SeriesEpisode[];
  allEpisodesRef: React.MutableRefObject<SeriesEpisode[]>;
  seasons: SeriesSeason[];
  selectedSeason: SeriesSeason | null;
  setSelectedSeason: React.Dispatch<React.SetStateAction<SeriesSeason | null>>;
  episodesLoading: boolean;
  hasWatchedEpisodes: boolean;
  isShuffleMode: boolean;
  isEpisodeStripFocused: boolean;
  setIsEpisodeStripFocused: React.Dispatch<React.SetStateAction<boolean>>;

  // Episode navigation
  findPreviousEpisode: (episode: SeriesEpisode) => SeriesEpisode | null;
  findNextEpisode: (episode: SeriesEpisode) => SeriesEpisode | null;
  findFirstEpisodeOfNextSeason: (seasonNumber: number) => SeriesEpisode | null;
  findFirstEpisode: () => SeriesEpisode | null;
  toEpisodeReference: (episode: SeriesEpisode) => import('@/services/api').EpisodeWatchPayload['episode'];

  // Handlers
  handleEpisodeFocus: (episode: SeriesEpisode) => void;
  handleEpisodeSelect: (episode: SeriesEpisode) => void;
  handlePlayEpisode: (episode: SeriesEpisode) => Promise<void>;
  handlePlaySeason: (season: SeriesSeason) => Promise<void>;
  handleSeasonSelect: (season: SeriesSeason, shouldAutoplay: boolean) => void;
  handleShufflePlay: () => void;
  handleShuffleSeasonPlay: () => void;
  handlePreviousEpisode: () => void;
  handleNextEpisode: () => void;
  handleEpisodeStripFocus: () => void;
  handleEpisodeStripBlur: () => void;
  handleEpisodesLoaded: (episodes: SeriesEpisode[]) => void;
  handleSeasonsLoaded: (seasons: SeriesSeason[]) => void;

  /** Episode code for the episode that will be played (TV display) */
  episodeToPlayCode: string | null;

  /** Ref to handlePlayEpisode for external use (navigation auto-play) */
  handlePlayEpisodeRef: React.MutableRefObject<((episode: SeriesEpisode) => Promise<void>) | null>;
  /** Ref to handleEpisodeSelect for external use */
  handleEpisodeSelectRef: React.MutableRefObject<((episode: SeriesEpisode) => void) | null>;
}

export function useEpisodeManager(params: UseEpisodeManagerParams): EpisodeManagerResult {
  const {
    isSeries,
    seriesIdentifier,
    title,
    activeUserId,
    detailsBundle,
    bundleReady,
    resolveAndPlayRef,
    dismissTrailerAutoPlay,
    showLoadingScreenIfEnabled,
    pendingShuffleModeRef,
    nextEpisodeFromPlayback,
    setNextEpisodeFromPlayback,
    setCurrentProgress,
    setPendingPlaybackAction,
    setResumeModalVisible,
    pendingStartOffsetRef,
    setSelectionError,
    setSelectionInfo,
    imdbId,
    tmdbId,
    tvdbId,
    activeEpisode,
    setActiveEpisode,
    nextUpEpisode,
    setNextUpEpisode,
    isShuffleMode,
    setIsShuffleMode,
  } = params;

  const [allEpisodes, setAllEpisodes] = useState<SeriesEpisode[]>([]);
  const allEpisodesRef = useRef<SeriesEpisode[]>([]);
  const [seasons, setSeasons] = useState<SeriesSeason[]>([]);
  const [selectedSeason, setSelectedSeason] = useState<SeriesSeason | null>(null);
  const [episodesLoading, setEpisodesLoading] = useState(true);
  const [hasWatchedEpisodes, setHasWatchedEpisodes] = useState(false);
  const [isEpisodeStripFocused, setIsEpisodeStripFocused] = useState(false);

  const hydratedWatchStateRef = useRef(false);

  // Keep pendingShuffleModeRef in sync with state
  useEffect(() => {
    pendingShuffleModeRef.current = isShuffleMode;
  }, [isShuffleMode, pendingShuffleModeRef]);

  // Reset episodes loading state when titleId changes or when it's not a series
  useEffect(() => {
    if (isSeries) {
      setEpisodesLoading(true);
    } else {
      setEpisodesLoading(false);
    }
  }, [isSeries]);

  // Fetch watch state to determine next episode
  useEffect(() => {
    if (!isSeries || !seriesIdentifier || !activeUserId || allEpisodes.length === 0) {
      setNextUpEpisode(null);
      setHasWatchedEpisodes(false);
      return;
    }

    const processWatchState = (watchState: import('@/services/api').SeriesWatchState | null) => {
      const watchedEpisodesCount = watchState?.watchedEpisodes ? Object.keys(watchState.watchedEpisodes).length : 0;
      setHasWatchedEpisodes(watchedEpisodesCount > 0);
      if (!watchState?.nextEpisode) return;
      const matchingEpisode = allEpisodes.find(
        (ep) =>
          ep.seasonNumber === watchState.nextEpisode!.seasonNumber &&
          ep.episodeNumber === watchState.nextEpisode!.episodeNumber,
      );
      if (matchingEpisode) {
        setNextUpEpisode(matchingEpisode);
      }
    };

    // Hydrate from bundle if available
    if (detailsBundle && !hydratedWatchStateRef.current) {
      hydratedWatchStateRef.current = true;
      processWatchState(detailsBundle.watchState);
      return;
    }

    if (!bundleReady) return;
    if (hydratedWatchStateRef.current) return;

    let cancelled = false;
    apiService
      .getSeriesWatchState(activeUserId, seriesIdentifier)
      .then((watchState) => {
        if (cancelled) return;
        processWatchState(watchState);
      })
      .catch((error) => {
        if (!cancelled) {
          console.log('Unable to fetch watch state (may not exist yet):', error);
          setHasWatchedEpisodes(false);
        }
      });

    return () => { cancelled = true; };
  }, [isSeries, seriesIdentifier, activeUserId, allEpisodes, detailsBundle, bundleReady]);

  // Episode navigation helpers
  const findPreviousEpisode = useCallback(
    (episode: SeriesEpisode): SeriesEpisode | null => {
      if (allEpisodes.length === 0) return null;
      const idx = allEpisodes.findIndex(
        (ep) => ep.seasonNumber === episode.seasonNumber && ep.episodeNumber === episode.episodeNumber,
      );
      return idx <= 0 ? null : allEpisodes[idx - 1];
    },
    [allEpisodes],
  );

  const findNextEpisode = useCallback(
    (episode: SeriesEpisode): SeriesEpisode | null => {
      if (allEpisodes.length === 0) return null;
      const idx = allEpisodes.findIndex(
        (ep) => ep.seasonNumber === episode.seasonNumber && ep.episodeNumber === episode.episodeNumber,
      );
      return idx === -1 || idx === allEpisodes.length - 1 ? null : allEpisodes[idx + 1];
    },
    [allEpisodes],
  );

  const findFirstEpisodeOfNextSeason = useCallback(
    (seasonNumber: number): SeriesEpisode | null => {
      if (allEpisodes.length === 0) return null;
      return allEpisodes.find((ep) => ep.seasonNumber === seasonNumber + 1) || null;
    },
    [allEpisodes],
  );

  const findFirstEpisode = useCallback((): SeriesEpisode | null => {
    return allEpisodes.length === 0 ? null : allEpisodes[0];
  }, [allEpisodes]);

  const toEpisodeReference = useCallback(
    (episode: SeriesEpisode): import('@/services/api').EpisodeWatchPayload['episode'] => ({
      seasonNumber: episode.seasonNumber,
      episodeNumber: episode.episodeNumber,
      episodeId: episode.id,
      tvdbId: episode.tvdbId ? String(episode.tvdbId) : undefined,
      title: episode.name,
      overview: episode.overview,
      runtimeMinutes: episode.runtimeMinutes,
      airDate: episode.airedDate,
    }),
    [],
  );

  // Handlers
  const handleEpisodeFocus = useCallback((episode: SeriesEpisode) => {
    setActiveEpisode(episode);
  }, []);

  const handleEpisodeSelect = useCallback(
    (episode: SeriesEpisode) => {
      setActiveEpisode(episode);
      setSelectionError(null);
      setSelectionInfo(null);
      setCurrentProgress(null);
      setSelectedSeason((currentSeason) => {
        if (currentSeason?.number !== episode.seasonNumber) {
          const matchingSeason = seasons.find((s) => s.number === episode.seasonNumber);
          return matchingSeason ?? currentSeason;
        }
        return currentSeason;
      });
    },
    [seasons, setSelectionError, setSelectionInfo, setCurrentProgress],
  );

  const handlePlayEpisodeRef = useRef<((episode: SeriesEpisode) => Promise<void>) | null>(null);
  const handleEpisodeSelectRef = useRef<((episode: SeriesEpisode) => void) | null>(null);

  const handlePlayEpisode = useCallback(
    async (episode: SeriesEpisode) => {
      setActiveEpisode(episode);

      const playAction = async () => {
        const baseTitle = title.trim() || title;
        const query = buildEpisodeQuery(baseTitle, episode.seasonNumber, episode.episodeNumber);
        if (!query) {
          setSelectionError('Unable to build an episode search query.');
          return;
        }
        const episodeCode = `S${padNumber(episode.seasonNumber)}E${padNumber(episode.episodeNumber)}`;
        const friendlyLabel = `${baseTitle} ${episodeCode}${episode.name ? ` – "${episode.name}"` : ''}`;
        const selectionMessage = `${baseTitle} • ${episodeCode}`;
        await resolveAndPlayRef.current?.({
          query,
          friendlyLabel,
          limit: 50,
          selectionMessage,
          targetEpisode: { seasonNumber: episode.seasonNumber, episodeNumber: episode.episodeNumber, airedDate: episode.airedDate },
        });
      };

      // Skip resume check for shuffle mode
      const isShuffling = pendingShuffleModeRef.current;

      if (!isShuffling && activeUserId && seriesIdentifier) {
        const episodeItemId = `${seriesIdentifier}:S${String(episode.seasonNumber).padStart(2, '0')}E${String(episode.episodeNumber).padStart(2, '0')}`;
        try {
          const progress = await apiService.getPlaybackProgress(activeUserId, 'episode', episodeItemId);
          if (progress && progress.percentWatched > 5 && progress.percentWatched < 95) {
            setCurrentProgress(progress);
            setPendingPlaybackAction(() => async (startOffset?: number) => {
              if (startOffset !== undefined) {
                pendingStartOffsetRef.current = startOffset;
              }
              await playAction();
            });
            setResumeModalVisible(true);
            return;
          }
        } catch (error) {
          console.warn('Failed to check playback progress:', error);
        }
      }

      await showLoadingScreenIfEnabled();
      await playAction();
    },
    [
      resolveAndPlayRef, title, activeUserId, seriesIdentifier,
      showLoadingScreenIfEnabled, pendingShuffleModeRef, pendingStartOffsetRef,
      setCurrentProgress, setPendingPlaybackAction, setResumeModalVisible, setSelectionError,
    ],
  );

  // Keep refs in sync
  useEffect(() => {
    handlePlayEpisodeRef.current = handlePlayEpisode;
  }, [handlePlayEpisode]);

  useEffect(() => {
    handleEpisodeSelectRef.current = handleEpisodeSelect;
  }, [handleEpisodeSelect]);

  const handlePlaySeason = useCallback(
    async (season: SeriesSeason) => {
      const baseTitle = title.trim() || title;
      const { buildSeasonQuery, getSeasonLabel } = await import('../utils');
      const query = buildSeasonQuery(baseTitle, season.number);
      if (!query) {
        setSelectionError('Unable to build a season search query.');
        return;
      }
      const friendlyLabel = `${baseTitle} ${getSeasonLabel(season.number, season.name)}`;
      const selectionMessage = `${baseTitle} • ${getSeasonLabel(season.number, season.name)}`;
      await resolveAndPlayRef.current?.({ query, friendlyLabel, limit: 50, selectionMessage });
    },
    [resolveAndPlayRef, title, setSelectionError],
  );

  const handleSeasonSelect = useCallback(
    (season: SeriesSeason, shouldAutoplay: boolean) => {
      setSelectedSeason(season);
      if (shouldAutoplay) {
        void handlePlaySeason(season);
      }
    },
    [handlePlaySeason],
  );

  const handleShufflePlay = useCallback(() => {
    dismissTrailerAutoPlay();
    const shuffleableEpisodes = allEpisodes.filter((ep) => ep.seasonNumber !== 0);
    if (shuffleableEpisodes.length === 0) return;
    const randomIndex = Math.floor(Math.random() * shuffleableEpisodes.length);
    const randomEpisode = shuffleableEpisodes[randomIndex];
    setIsShuffleMode(true);
    pendingShuffleModeRef.current = true;
    const matchingSeason = seasons.find((s) => s.number === randomEpisode.seasonNumber);
    if (matchingSeason) setSelectedSeason(matchingSeason);
    setActiveEpisode(randomEpisode);
    handlePlayEpisode(randomEpisode);
  }, [dismissTrailerAutoPlay, allEpisodes, seasons, handlePlayEpisode, pendingShuffleModeRef]);

  const handleShuffleSeasonPlay = useCallback(() => {
    dismissTrailerAutoPlay();
    const seasonEpisodes = selectedSeason?.episodes ?? [];
    if (seasonEpisodes.length === 0) return;
    const randomIndex = Math.floor(Math.random() * seasonEpisodes.length);
    const randomEpisode = seasonEpisodes[randomIndex];
    setIsShuffleMode(true);
    pendingShuffleModeRef.current = true;
    setActiveEpisode(randomEpisode);
    handlePlayEpisode(randomEpisode);
  }, [dismissTrailerAutoPlay, selectedSeason?.episodes, handlePlayEpisode, pendingShuffleModeRef]);

  const handlePreviousEpisode = useCallback(() => {
    if (!activeEpisode) return;
    const previousEp = findPreviousEpisode(activeEpisode);
    if (previousEp) handleEpisodeSelect(previousEp);
  }, [activeEpisode, findPreviousEpisode, handleEpisodeSelect]);

  const handleNextEpisode = useCallback(() => {
    if (!activeEpisode) return;
    const nextEp = findNextEpisode(activeEpisode);
    if (nextEp) handleEpisodeSelect(nextEp);
  }, [activeEpisode, findNextEpisode, handleEpisodeSelect]);

  const handleEpisodeStripFocus = useCallback(() => {
    setIsEpisodeStripFocused(true);
  }, []);

  const handleEpisodeStripBlur = useCallback(() => {
    setIsEpisodeStripFocused(false);
  }, []);

  const handleEpisodesLoaded = useCallback((episodes: SeriesEpisode[]) => {
    setAllEpisodes(episodes);
    allEpisodesRef.current = episodes;
    setEpisodesLoading(false);
  }, []);

  const handleSeasonsLoaded = useCallback((loadedSeasons: SeriesSeason[]) => {
    setSeasons(loadedSeasons);
  }, []);

  // Select/play next episode when episodes are loaded and we have a next episode to show
  useEffect(() => {
    if (nextEpisodeFromPlayback && allEpisodes.length > 0) {
      const matchingEpisode = allEpisodes.find(
        (ep) =>
          ep.seasonNumber === nextEpisodeFromPlayback.seasonNumber &&
          ep.episodeNumber === nextEpisodeFromPlayback.episodeNumber,
      );
      if (matchingEpisode) {
        if (nextEpisodeFromPlayback.autoPlay && handlePlayEpisodeRef.current) {
          handlePlayEpisodeRef.current(matchingEpisode);
        } else {
          handleEpisodeSelect(matchingEpisode);
        }
        setNextEpisodeFromPlayback(null);
      }
    }
  }, [nextEpisodeFromPlayback, allEpisodes, handleEpisodeSelect, setNextEpisodeFromPlayback]);

  const episodeToPlayCode = useMemo(() => {
    const episode = activeEpisode || nextUpEpisode;
    if (!isSeries || !episode) return null;
    const seasonStr = String(episode.seasonNumber).padStart(2, '0');
    const episodeStr = String(episode.episodeNumber).padStart(2, '0');
    return `S${seasonStr}E${episodeStr}`;
  }, [isSeries, activeEpisode, nextUpEpisode]);

  return {
    activeEpisode,
    setActiveEpisode,
    nextUpEpisode,
    allEpisodes,
    allEpisodesRef,
    seasons,
    selectedSeason,
    setSelectedSeason,
    episodesLoading,
    hasWatchedEpisodes,
    isShuffleMode,
    isEpisodeStripFocused,
    setIsEpisodeStripFocused,
    findPreviousEpisode,
    findNextEpisode,
    findFirstEpisodeOfNextSeason,
    findFirstEpisode,
    toEpisodeReference,
    handleEpisodeFocus,
    handleEpisodeSelect,
    handlePlayEpisode,
    handlePlaySeason,
    handleSeasonSelect,
    handleShufflePlay,
    handleShuffleSeasonPlay,
    handlePreviousEpisode,
    handleNextEpisode,
    handleEpisodeStripFocus,
    handleEpisodeStripBlur,
    handleEpisodesLoaded,
    handleSeasonsLoaded,
    episodeToPlayCode,
    handlePlayEpisodeRef,
    handleEpisodeSelectRef,
  };
}
