import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useStartupData } from '@/components/StartupDataContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import {
  apiService,
  type ApiError,
  type WatchlistItem,
  type WatchlistStateUpdate,
  type WatchlistUpsertPayload,
} from '@/services/api';

interface WatchlistContextValue {
  items: WatchlistItem[];
  loading: boolean;
  error: string | null;
  refresh: (options?: { silent?: boolean }) => Promise<void>;
  addToWatchlist: (payload: WatchlistUpsertPayload) => Promise<WatchlistItem>;
  removeFromWatchlist: (mediaType: string, id: string) => Promise<void>;
  updateWatchlistState: (mediaType: string, id: string, update: WatchlistStateUpdate) => Promise<WatchlistItem>;
  isInWatchlist: (mediaType: string, id: string) => boolean;
  getItem: (mediaType: string, id: string) => WatchlistItem | undefined;
}

const WatchlistContext = createContext<WatchlistContextValue | undefined>(undefined);

const toKey = (mediaType: string, id: string) => `${mediaType.toLowerCase()}:${id}`;

const normaliseItems = (items: WatchlistItem[] | undefined | null) => {
  if (!items) {
    return [] as WatchlistItem[];
  }
  return items.map((item) => ({
    ...item,
    mediaType: item.mediaType.toLowerCase(),
  }));
};

const errorMessage = (err: unknown) => {
  if (err instanceof Error) {
    return err.message;
  }
  if (typeof err === 'string') {
    return err;
  }
  return 'Unknown watchlist error';
};

const isAuthError = (err: unknown) => {
  if (!err || typeof err !== 'object') {
    return false;
  }
  const candidate = err as ApiError;
  return candidate.code === 'AUTH_INVALID_PIN' || candidate.status === 401;
};

interface WLState {
  items: WatchlistItem[];
  loading: boolean;
  error: string | null;
}

const INITIAL_WL_STATE: WLState = { items: [], loading: true, error: null };

export const WatchlistProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [state, setState] = useState<WLState>(INITIAL_WL_STATE);
  const { activeUserId } = useUserProfiles();
  const { backendUrl, isReady } = useBackendSettings();
  const { startupData, ready: startupReady } = useStartupData();
  const hydratedFromStartup = useRef(false);

  const requireUserId = useCallback(() => {
    if (!activeUserId) {
      throw new Error('No active user selected');
    }
    return activeUserId;
  }, [activeUserId]);

  const refresh = useCallback(
    async (options?: { silent?: boolean }) => {
      if (!activeUserId) {
        setState({ items: [], loading: false, error: null });
        return;
      }

      // Only set loading state if not silent refresh
      if (!options?.silent) {
        setState((prev) => ({ ...prev, loading: true }));
      }
      try {
        const response = await apiService.getWatchlist(activeUserId);
        setState({ items: normaliseItems(response), loading: false, error: null });
      } catch (err) {
        const message = errorMessage(err);
        const log = isAuthError(err) ? console.warn : console.error;
        log('Failed to load watchlist:', err);
        setState({ items: [], loading: false, error: message });
      } finally {
        // Only clear loading state if not silent refresh (already handled in success/error)
        if (!options?.silent) {
          setState((prev) => ({ ...prev, loading: false }));
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
      setState({ items: [], loading: false, error: null });
      hydratedFromStartup.current = false;
      return;
    }
    // Hydrate from startup bundle if available (avoids separate HTTP request)
    if (startupData?.watchlist && !hydratedFromStartup.current) {
      setState({ items: normaliseItems(startupData.watchlist), loading: false, error: null });
      hydratedFromStartup.current = true;
      return;
    }
    // Wait for startup bundle before falling back to independent fetch
    if (!startupReady) {
      return;
    }
    // Fallback: fetch independently (startup failed or didn't include watchlist)
    if (!hydratedFromStartup.current) {
      void refresh();
    }
  }, [isReady, backendUrl, activeUserId, refresh, startupData, startupReady]);

  const addToWatchlist = useCallback(
    async (payload: WatchlistUpsertPayload) => {
      try {
        const userId = requireUserId();
        const response = await apiService.addToWatchlist(userId, payload);
        const item = { ...response, mediaType: response.mediaType.toLowerCase() };
        setState((prev) => {
          const key = toKey(item.mediaType, item.id);
          const withoutItem = prev.items.filter((existing) => toKey(existing.mediaType, existing.id) !== key);
          return { ...prev, items: [item, ...withoutItem], error: null };
        });
        return item;
      } catch (err) {
        const message = errorMessage(err);
        setState((prev) => ({ ...prev, error: message }));
        const log = isAuthError(err) ? console.warn : console.error;
        log('Failed to add to watchlist:', err);
        throw err;
      }
    },
    [requireUserId],
  );

  const updateWatchlistState = useCallback(
    async (mediaType: string, id: string, update: WatchlistStateUpdate) => {
      try {
        const userId = requireUserId();
        const response = await apiService.updateWatchlistState(userId, mediaType, id, update);
        const item = { ...response, mediaType: response.mediaType.toLowerCase() };
        setState((prev) => {
          const key = toKey(item.mediaType, item.id);
          const next = prev.items.map((existing) => (toKey(existing.mediaType, existing.id) === key ? item : existing));
          if (!next.some((existing) => toKey(existing.mediaType, existing.id) === key)) {
            next.unshift(item);
          }
          return { ...prev, items: next };
        });
        return item;
      } catch (err) {
        const message = errorMessage(err);
        setState((prev) => ({ ...prev, error: message }));
        const log = isAuthError(err) ? console.warn : console.error;
        log('Failed to update watchlist state:', err);
        throw err;
      }
    },
    [requireUserId],
  );

  const removeFromWatchlist = useCallback(
    async (mediaType: string, id: string) => {
      try {
        const userId = requireUserId();
        await apiService.removeFromWatchlist(userId, mediaType, id);
        setState((prev) => ({
          ...prev,
          items: prev.items.filter((item) => toKey(item.mediaType, item.id) !== toKey(mediaType, id)),
        }));
      } catch (err) {
        const message = errorMessage(err);
        setState((prev) => ({ ...prev, error: message }));
        const log = isAuthError(err) ? console.warn : console.error;
        log('Failed to remove from watchlist:', err);
        throw err;
      }
    },
    [requireUserId],
  );

  const value = useMemo<WatchlistContextValue>(() => {
    const index = new Map(state.items.map((item) => [toKey(item.mediaType, item.id), item]));

    const isInWatchlist = (mediaType: string, id: string) => index.has(toKey(mediaType, id));
    const getItem = (mediaType: string, id: string) => index.get(toKey(mediaType, id));

    return {
      items: state.items,
      loading: state.loading,
      error: state.error,
      refresh,
      addToWatchlist,
      removeFromWatchlist,
      updateWatchlistState,
      isInWatchlist,
      getItem,
    };
  }, [state, refresh, addToWatchlist, removeFromWatchlist, updateWatchlistState]);

  return <WatchlistContext.Provider value={value}>{children}</WatchlistContext.Provider>;
};

export const useWatchlist = (): WatchlistContextValue => {
  const context = useContext(WatchlistContext);
  if (!context) {
    throw new Error('useWatchlist must be used within a WatchlistProvider');
  }
  return context;
};
