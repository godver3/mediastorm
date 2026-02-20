import React, { createContext, useContext, useState, useEffect, useCallback, useMemo, useRef } from 'react';
import { apiService, WatchStatusItem, WatchStatusUpdate } from '../services/api';
import { useStartupData } from './StartupDataContext';
import { useUserProfiles } from './UserProfilesContext';

interface WatchStatusContextValue {
  items: WatchStatusItem[];
  loading: boolean;
  error: string | null;
  isWatched: (mediaType: string, id: string) => boolean;
  getItem: (mediaType: string, id: string) => WatchStatusItem | undefined;
  toggleWatchStatus: (mediaType: string, id: string, metadata?: Partial<WatchStatusUpdate>) => Promise<void>;
  updateWatchStatus: (update: WatchStatusUpdate) => Promise<void>;
  bulkUpdateWatchStatus: (updates: WatchStatusUpdate[]) => Promise<void>;
  removeWatchStatus: (mediaType: string, id: string) => Promise<void>;
  refresh: () => Promise<void>;
}

const WatchStatusContext = createContext<WatchStatusContextValue | undefined>(undefined);

interface WSState {
  items: WatchStatusItem[];
  loading: boolean;
  error: string | null;
}

const INITIAL_WS_STATE: WSState = { items: [], loading: false, error: null };

export const WatchStatusProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [state, setState] = useState<WSState>(INITIAL_WS_STATE);
  const { activeUser } = useUserProfiles();
  const { startupData, ready: startupReady } = useStartupData();
  const hydratedFromStartup = useRef(false);

  const normaliseKeyPart = (value: string | undefined | null): string => {
    return value?.trim().toLowerCase() ?? '';
  };

  const makeKey = (mediaType: string, id: string): string => {
    return `${normaliseKeyPart(mediaType)}:${normaliseKeyPart(id)}`;
  };

  const refresh = useCallback(async () => {
    if (!activeUser?.id) {
      setState({ items: [], loading: false, error: null });
      return;
    }

    setState((prev) => ({ ...prev, loading: true, error: null }));

    try {
      const watchStatus = await apiService.getWatchStatus(activeUser.id);
      setState({ items: watchStatus || [], loading: false, error: null });
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to load watch status';
      console.error('Failed to fetch watch status:', err);

      // Handle auth errors gracefully
      const errorMsg = message.includes('401') || message.includes('AUTH_INVALID_PIN')
        ? 'Authentication failed'
        : message;
      setState({ items: [], loading: false, error: errorMsg });
    }
  }, [activeUser?.id]);

  useEffect(() => {
    if (!activeUser?.id) {
      hydratedFromStartup.current = false;
      return;
    }
    // Hydrate from startup bundle if available (avoids separate HTTP request)
    if (startupData?.watchHistory && !hydratedFromStartup.current) {
      setState({ items: startupData.watchHistory || [], loading: false, error: null });
      hydratedFromStartup.current = true;
      return;
    }
    // Wait for startup bundle before falling back to independent fetch
    if (!startupReady) {
      return;
    }
    // Fallback: fetch independently (startup failed or didn't include watch history)
    if (!hydratedFromStartup.current) {
      refresh();
    }
  }, [refresh, activeUser?.id, startupData, startupReady]);

  const isWatched = useCallback(
    (mediaType: string, id: string): boolean => {
      const key = makeKey(mediaType, id);
      const item = state.items.find((i) => makeKey(i.mediaType, i.itemId) === key);
      return item?.watched ?? false;
    },
    [state.items],
  );

  const getItem = useCallback(
    (mediaType: string, id: string): WatchStatusItem | undefined => {
      const key = makeKey(mediaType, id);
      return state.items.find((i) => makeKey(i.mediaType, i.itemId) === key);
    },
    [state.items],
  );

  const toggleWatchStatus = useCallback(
    async (mediaType: string, id: string, metadata?: Partial<WatchStatusUpdate>) => {
      if (!activeUser?.id) {
        throw new Error('No active user');
      }

      try {
        const updatedItem = await apiService.toggleWatchStatus(activeUser.id, mediaType, id, metadata);

        setState((prev) => {
          const key = makeKey(mediaType, id);
          const existingIndex = prev.items.findIndex((i) => makeKey(i.mediaType, i.itemId) === key);

          if (existingIndex >= 0) {
            const updated = [...prev.items];
            updated[existingIndex] = updatedItem;
            return { ...prev, items: updated };
          } else {
            return { ...prev, items: [updatedItem, ...prev.items] };
          }
        });
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Failed to toggle watch status';
        console.error('Failed to toggle watch status:', err);
        throw new Error(message);
      }
    },
    [activeUser?.id],
  );

  const updateWatchStatus = useCallback(
    async (update: WatchStatusUpdate) => {
      if (!activeUser?.id) {
        throw new Error('No active user');
      }

      try {
        const updatedItem = await apiService.updateWatchStatus(activeUser.id, update);

        setState((prev) => {
          const key = makeKey(update.mediaType, update.itemId);
          const existingIndex = prev.items.findIndex((i) => makeKey(i.mediaType, i.itemId) === key);

          if (existingIndex >= 0) {
            const updated = [...prev.items];
            updated[existingIndex] = updatedItem;
            return { ...prev, items: updated };
          } else {
            return { ...prev, items: [updatedItem, ...prev.items] };
          }
        });
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Failed to update watch status';
        console.error('Failed to update watch status:', err);
        throw new Error(message);
      }
    },
    [activeUser?.id],
  );

  const removeWatchStatus = useCallback(
    async (mediaType: string, id: string) => {
      if (!activeUser?.id) {
        throw new Error('No active user');
      }

      try {
        await apiService.removeWatchStatus(activeUser.id, mediaType, id);

        setState((prev) => {
          const key = makeKey(mediaType, id);
          return { ...prev, items: prev.items.filter((i) => makeKey(i.mediaType, i.itemId) !== key) };
        });
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Failed to remove watch status';
        console.error('Failed to remove watch status:', err);
        throw new Error(message);
      }
    },
    [activeUser?.id],
  );

  const bulkUpdateWatchStatus = useCallback(
    async (updates: WatchStatusUpdate[]) => {
      if (!activeUser?.id) {
        throw new Error('No active user');
      }

      try {
        const updatedItems = await apiService.bulkUpdateWatchStatus(activeUser.id, updates);

        setState((prev) => {
          const updated = [...prev.items];

          updatedItems.forEach((updatedItem) => {
            const key = makeKey(updatedItem.mediaType, updatedItem.itemId);
            const existingIndex = updated.findIndex((i) => makeKey(i.mediaType, i.itemId) === key);

            if (existingIndex >= 0) {
              updated[existingIndex] = updatedItem;
            } else {
              updated.push(updatedItem);
            }
          });

          return { ...prev, items: updated };
        });
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Failed to bulk update watch status';
        console.error('Failed to bulk update watch status:', err);
        throw new Error(message);
      }
    },
    [activeUser?.id],
  );

  // Memoize context value to prevent unnecessary consumer re-renders
  const value = useMemo<WatchStatusContextValue>(
    () => ({
      items: state.items,
      loading: state.loading,
      error: state.error,
      isWatched,
      getItem,
      toggleWatchStatus,
      updateWatchStatus,
      bulkUpdateWatchStatus,
      removeWatchStatus,
      refresh,
    }),
    [state, isWatched, getItem, toggleWatchStatus, updateWatchStatus, bulkUpdateWatchStatus, removeWatchStatus, refresh],
  );

  return <WatchStatusContext.Provider value={value}>{children}</WatchStatusContext.Provider>;
};

export const useWatchStatus = (): WatchStatusContextValue => {
  const context = useContext(WatchStatusContext);
  if (!context) {
    throw new Error('useWatchStatus must be used within a WatchStatusProvider');
  }
  return context;
};
