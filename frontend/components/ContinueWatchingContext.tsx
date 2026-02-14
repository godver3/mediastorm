import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useStartupData } from '@/components/StartupDataContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { apiService, type ApiError, type EpisodeWatchPayload, type SeriesWatchState } from '@/services/api';

interface ContinueWatchingContextValue {
  items: SeriesWatchState[];
  loading: boolean;
  error: string | null;
  refresh: (options?: { silent?: boolean }) => Promise<void>;
  recordEpisodeWatch: (payload: EpisodeWatchPayload) => Promise<SeriesWatchState>;
  hideFromContinueWatching: (seriesId: string) => Promise<void>;
}

const ContinueWatchingContext = createContext<ContinueWatchingContextValue | undefined>(undefined);

const normaliseItems = (items: SeriesWatchState[] | null | undefined) => {
  if (!items) {
    return [] as SeriesWatchState[];
  }
  return items.map((item) => ({
    ...item,
    nextEpisode: item.nextEpisode ?? undefined,
  }));
};

const formatError = (err: unknown) => {
  if (err instanceof Error) {
    return err.message;
  }
  if (typeof err === 'string') {
    return err;
  }
  return 'Unknown continue watching error';
};

const isAuthError = (err: unknown) => {
  if (!err || typeof err !== 'object') {
    return false;
  }
  const candidate = err as ApiError;
  return candidate.code === 'AUTH_INVALID_PIN' || candidate.status === 401;
};

export const ContinueWatchingProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [items, setItems] = useState<SeriesWatchState[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const { activeUserId } = useUserProfiles();
  const { backendUrl, isReady } = useBackendSettings();
  const { startupData, ready: startupReady } = useStartupData();
  const hydratedFromStartup = useRef(false);

  const refresh = useCallback(
    async (options?: { silent?: boolean }) => {
      if (!activeUserId) {
        setItems([]);
        setLoading(false);
        return;
      }

      // Only set loading state if not silent refresh
      if (!options?.silent) {
        setLoading(true);
      }
      try {
        console.log('[ContinueWatching] refresh() called', { silent: options?.silent, source: new Error().stack?.split('\n')[2]?.trim() });
        const [response, progressList] = await Promise.all([
          apiService.getContinueWatching(activeUserId),
          apiService.listPlaybackProgress(activeUserId).catch(() => []),
        ]);

        const progressByItemId = new Map<string, number>();
        const progressByEpisode = new Map<string, number>();
        const formatEpisodeProgressKey = (seriesId?: string, seasonNumber?: number, episodeNumber?: number) => {
          if (!seriesId || typeof seasonNumber !== 'number' || typeof episodeNumber !== 'number') {
            return null;
          }
          return `${seriesId}:S${seasonNumber}E${episodeNumber}`;
        };

        for (const progress of progressList) {
          if (progress.itemId) {
            progressByItemId.set(progress.itemId, progress.percentWatched);
          }
          if (progress.id) {
            progressByItemId.set(progress.id, progress.percentWatched);
          }

          if (progress.mediaType === 'episode') {
            const episodeKey = formatEpisodeProgressKey(
              progress.seriesId,
              progress.seasonNumber,
              progress.episodeNumber,
            );
            if (episodeKey) {
              progressByEpisode.set(episodeKey, progress.percentWatched);
            }
          }
        }

        const getEpisodePercent = (episode?: SeriesWatchState['nextEpisode'], seriesId?: string) => {
          if (!episode) {
            return 0;
          }

          if (episode.episodeId) {
            const byId = progressByItemId.get(episode.episodeId);
            if (typeof byId === 'number') {
              return byId;
            }
          }

          const episodeKey = formatEpisodeProgressKey(seriesId, episode.seasonNumber, episode.episodeNumber);
          if (episodeKey) {
            const byEpisodeKey = progressByEpisode.get(episodeKey);
            if (typeof byEpisodeKey === 'number') {
              return byEpisodeKey;
            }
          }

          return 0;
        };

        // Merge progress data with continue watching items
        const itemsWithProgress = (response || []).map((item) => {
          if (!item.nextEpisode) {
            // Movies resume off their own seriesId (tmdb:movie:123)
            const moviePercent = progressByItemId.get(item.seriesId) || 0;
            return {
              ...item,
              percentWatched: moviePercent,
              resumePercent: moviePercent,
            };
          }

          const nextProgress = getEpisodePercent(item.nextEpisode, item.seriesId);
          const lastProgress = getEpisodePercent(item.lastWatched, item.seriesId);
          const isSameEpisode =
            !!item.lastWatched &&
            item.lastWatched.seasonNumber === item.nextEpisode.seasonNumber &&
            item.lastWatched.episodeNumber === item.nextEpisode.episodeNumber;

          const resumePercent = nextProgress > 0 ? nextProgress : isSameEpisode ? lastProgress : 0;
          const percentWatched = Math.max(resumePercent, lastProgress);

          return {
            ...item,
            percentWatched,
            resumePercent,
          };
        });

        setItems(normaliseItems(itemsWithProgress));
        setError(null);
      } catch (err) {
        const message = formatError(err);
        const log = isAuthError(err) ? console.warn : console.error;
        log('Failed to load continue watching:', err);
        setError(message);
        setItems([]);
      } finally {
        // Only clear loading state if not silent refresh
        if (!options?.silent) {
          setLoading(false);
        }
      }
    },
    [activeUserId],
  );

  useEffect(() => {
    if (!isReady) {
      return;
    }
    if (!activeUserId) {
      setItems([]);
      setLoading(false);
      hydratedFromStartup.current = false;
      return;
    }
    // Hydrate from startup bundle if available. The backend pre-merges
    // playbackProgress into continueWatching items (percentWatched + resumePercent
    // are already computed), so we just set items directly — no JS-side processing.
    if (startupData?.continueWatching && !hydratedFromStartup.current) {
      console.log('[ContinueWatching] Hydrating from startup bundle');
      setItems(normaliseItems(startupData.continueWatching));
      setLoading(false);
      setError(null);
      hydratedFromStartup.current = true;
      return;
    }
    // Wait for startup bundle before falling back to independent fetch
    if (!startupReady) {
      return;
    }
    // Fallback: fetch independently (startup failed or didn't include continue watching)
    if (!hydratedFromStartup.current) {
      void refresh();
    }
  }, [isReady, backendUrl, activeUserId, refresh, startupData, startupReady]);

  const requireUserId = useCallback(() => {
    if (!activeUserId) {
      throw new Error('No active user selected');
    }
    return activeUserId;
  }, [activeUserId]);

  const recordEpisodeWatch = useCallback(
    async (payload: EpisodeWatchPayload) => {
      const userId = requireUserId();
      try {
        const response = await apiService.recordEpisodeWatch(userId, payload);
        // Note: Local state update removed - the index page will refresh when user navigates back
        return response;
      } catch (err) {
        const message = formatError(err);
        setError(message);
        const log = isAuthError(err) ? console.warn : console.error;
        log('Failed to record episode watch:', err);
        throw err;
      }
    },
    [requireUserId],
  );

  const hideFromContinueWatching = useCallback(
    async (seriesId: string) => {
      const userId = requireUserId();
      try {
        await apiService.hideFromContinueWatching(userId, seriesId);
        // Optimistically remove item from local state
        setItems((prev) => prev.filter((item) => item.seriesId !== seriesId));
      } catch (err) {
        const message = formatError(err);
        setError(message);
        const log = isAuthError(err) ? console.warn : console.error;
        log('Failed to hide from continue watching:', err);
        throw err;
      }
    },
    [requireUserId],
  );

  const value = useMemo<ContinueWatchingContextValue>(
    () => ({
      items,
      // Only use own loading state - don't cascade userLoading changes to all consumers
      // This prevents re-renders when UserProfilesContext.loading changes
      loading,
      error,
      refresh,
      recordEpisodeWatch,
      hideFromContinueWatching,
    }),
    [items, loading, error, refresh, recordEpisodeWatch, hideFromContinueWatching],
  );

  return <ContinueWatchingContext.Provider value={value}>{children}</ContinueWatchingContext.Provider>;
};

export const useContinueWatching = (): ContinueWatchingContextValue => {
  const context = useContext(ContinueWatchingContext);
  if (!context) {
    throw new Error('useContinueWatching must be used within a ContinueWatchingProvider');
  }
  return context;
};
