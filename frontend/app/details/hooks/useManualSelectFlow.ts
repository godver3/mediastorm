/**
 * useManualSelectFlow — owns manual NZB selection UI state and handlers
 */

import { useCallback, useState } from 'react';
import type { NZBResult, SeriesEpisode } from '@/services/api';
import {
  isTimeoutError,
  getTimeoutMessage,
} from '../playback';
import { isEpisodeUnreleased, formatUnreleasedMessage, padNumber } from '../utils';
import type { ManualTrackOverrides } from '../manual-selection';

interface UseManualSelectFlowParams {
  title: string;
  activeEpisode: SeriesEpisode | null;
  nextUpEpisode: SeriesEpisode | null;
  fetchIndexerResults: (opts: { query?: string; limit?: number; categories?: string[] }) => Promise<NZBResult[]>;
  getEpisodeSearchContext: (
    episode: SeriesEpisode,
  ) => { query: string; friendlyLabel: string; selectionMessage: string; episodeCode: string } | null;
  handleInitiatePlayback: (
    result: NZBResult,
    signal?: AbortSignal,
    overrides?: { useDebugPlayer?: boolean; trackOverrides?: ManualTrackOverrides },
  ) => Promise<void>;
  checkAndShowResumeModal: (action: () => Promise<void>) => Promise<void>;
  showLoadingScreenIfEnabled: () => Promise<void>;
  hideLoadingScreen: () => void;
  setSelectionInfo: React.Dispatch<React.SetStateAction<string | null>>;
  setSelectionError: React.Dispatch<React.SetStateAction<string | null>>;
  setIsResolving: React.Dispatch<React.SetStateAction<boolean>>;
  setShowBlackOverlay: React.Dispatch<React.SetStateAction<boolean>>;
  dismissTrailerAutoPlay: () => void;
  abortControllerRef: React.MutableRefObject<AbortController | null>;
}

export interface ManualSelectFlowResult {
  manualVisible: boolean;
  setManualVisible: React.Dispatch<React.SetStateAction<boolean>>;
  manualLoading: boolean;
  setManualLoading: React.Dispatch<React.SetStateAction<boolean>>;
  manualError: string | null;
  setManualError: React.Dispatch<React.SetStateAction<string | null>>;
  manualResults: NZBResult[];
  setManualResults: React.Dispatch<React.SetStateAction<NZBResult[]>>;

  handleManualSelect: () => Promise<void>;
  handleEpisodeLongPress: (episode: SeriesEpisode) => Promise<void>;
  handleManualSelection: (result: NZBResult, trackOverrides?: ManualTrackOverrides) => Promise<void>;
  closeManualPicker: () => void;
}

export function useManualSelectFlow(params: UseManualSelectFlowParams): ManualSelectFlowResult {
  const {
    title,
    activeEpisode,
    nextUpEpisode,
    fetchIndexerResults,
    getEpisodeSearchContext,
    handleInitiatePlayback,
    checkAndShowResumeModal,
    showLoadingScreenIfEnabled,
    hideLoadingScreen,
    setSelectionInfo,
    setSelectionError,
    setIsResolving,
    setShowBlackOverlay,
    dismissTrailerAutoPlay,
    abortControllerRef,
  } = params;

  const [manualVisible, setManualVisible] = useState(false);
  const [manualLoading, setManualLoading] = useState(false);
  const [manualError, setManualError] = useState<string | null>(null);
  const [manualResults, setManualResults] = useState<NZBResult[]>([]);

  const closeManualPicker = useCallback(() => {
    setManualVisible(false);
    setManualError(null);
    setManualLoading(false);
  }, []);

  const handleManualSelect = useCallback(async () => {
    if (manualLoading) return;
    dismissTrailerAutoPlay();

    const episodeToSelect = activeEpisode || nextUpEpisode;
    const context = episodeToSelect ? getEpisodeSearchContext(episodeToSelect) : null;
    setManualVisible(true);
    setManualError(null);
    setManualResults([]);
    setSelectionInfo(context?.selectionMessage ?? null);
    setSelectionError(null);
    setManualLoading(true);
    try {
      const results = await fetchIndexerResults({ limit: 50, query: context?.query });
      setManualResults(results);
      if (!results || results.length === 0) {
        if (episodeToSelect && isEpisodeUnreleased(episodeToSelect.airedDate, episodeToSelect.airedDateTimeUTC)) {
          const baseTitle = title.trim() || title;
          const episodeCode = `S${padNumber(episodeToSelect.seasonNumber)}E${padNumber(episodeToSelect.episodeNumber)}`;
          const episodeLabel = `${baseTitle} ${episodeCode}`;
          setManualError(formatUnreleasedMessage(episodeLabel, episodeToSelect.airedDate));
        } else {
          setManualError('No results available yet for manual selection.');
        }
      }
    } catch (err) {
      if (isTimeoutError(err)) {
        setManualError(getTimeoutMessage(err));
      } else {
        const message = err instanceof Error ? err.message : 'Failed to load results.';
        setManualError(message);
      }
    } finally {
      setManualLoading(false);
    }
  }, [dismissTrailerAutoPlay, activeEpisode, nextUpEpisode, fetchIndexerResults, getEpisodeSearchContext, manualLoading, title, setSelectionInfo, setSelectionError]);

  const handleEpisodeLongPress = useCallback(
    async (episode: SeriesEpisode) => {
      setSelectionError(null);
      setSelectionInfo(null);

      await new Promise((resolve) => setTimeout(resolve, 0));
      if (manualLoading) return;

      const context = getEpisodeSearchContext(episode);
      if (!context) {
        setSelectionError('Unable to build an episode search query.');
        return;
      }

      setManualVisible(true);
      setManualError(null);
      setManualResults([]);
      setSelectionInfo(context.selectionMessage);
      setSelectionError(null);
      setManualLoading(true);
      try {
        const results = await fetchIndexerResults({ limit: 50, query: context.query });
        setManualResults(results);
        if (!results || results.length === 0) {
          if (isEpisodeUnreleased(episode.airedDate, episode.airedDateTimeUTC)) {
            const baseTitle = title.trim() || title;
            const episodeCode = `S${padNumber(episode.seasonNumber)}E${padNumber(episode.episodeNumber)}`;
            const episodeLabel = `${baseTitle} ${episodeCode}`;
            setManualError(formatUnreleasedMessage(episodeLabel, episode.airedDate));
          } else {
            setManualError('No results available yet for manual selection.');
          }
        }
      } catch (err) {
        if (isTimeoutError(err)) {
          setManualError(getTimeoutMessage(err));
        } else {
          const message = err instanceof Error ? err.message : 'Failed to load results.';
          setManualError(message);
        }
      } finally {
        setManualLoading(false);
      }
    },
    [fetchIndexerResults, getEpisodeSearchContext, manualLoading, title, setSelectionInfo, setSelectionError],
  );

  const handleManualSelection = useCallback(
    async (result: NZBResult, trackOverrides?: ManualTrackOverrides) => {
      if (!result) return;

      if (abortControllerRef.current) {
        abortControllerRef.current.abort();
      }

      const abortController = new AbortController();
      abortControllerRef.current = abortController;

      setManualVisible(false);
      setManualError(null);
      setSelectionError(null);
      setIsResolving(true);

      const playAction = async () => {
        await showLoadingScreenIfEnabled();
        try {
          await handleInitiatePlayback(result, abortController.signal, { trackOverrides });
          if (abortController.signal.aborted) return;
          if (abortControllerRef.current === abortController) {
            abortControllerRef.current = null;
          }
        } catch (err) {
          const isAbortError = err instanceof Error && (err.name === 'AbortError' || err.message?.includes('aborted'));
          if (isAbortError) {
            setSelectionInfo(null);
            setSelectionError(null);
            return;
          }
          const message = err instanceof Error ? err.message : 'Playback failed';
          setSelectionError(message);
          setSelectionInfo(null);
          hideLoadingScreen();
          setShowBlackOverlay(false);
        } finally {
          setIsResolving(false);
        }
      };

      await checkAndShowResumeModal(playAction);
    },
    [
      checkAndShowResumeModal, handleInitiatePlayback, hideLoadingScreen,
      showLoadingScreenIfEnabled, setSelectionError, setSelectionInfo,
      setIsResolving, setShowBlackOverlay, abortControllerRef,
    ],
  );

  return {
    manualVisible,
    setManualVisible,
    manualLoading,
    setManualLoading,
    manualError,
    setManualError,
    manualResults,
    setManualResults,
    handleManualSelect,
    handleEpisodeLongPress,
    handleManualSelection,
    closeManualPicker,
  };
}
