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

export const WatchStatusProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [items, setItems] = useState<WatchStatusItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
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
      setItems([]);
      return;
    }

    setLoading(true);
    setError(null);

    try {
      const watchStatus = await apiService.getWatchStatus(activeUser.id);
      setItems(watchStatus || []);
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to load watch status';
      console.error('Failed to fetch watch status:', err);

      // Handle auth errors gracefully
      if (message.includes('401') || message.includes('AUTH_INVALID_PIN')) {
        setError('Authentication failed');
      } else {
        setError(message);
      }
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, [activeUser?.id]);

  useEffect(() => {
    if (!activeUser?.id) {
      hydratedFromStartup.current = false;
      return;
    }
    // Hydrate from startup bundle if available (avoids separate HTTP request)
    if (startupData?.watchHistory && !hydratedFromStartup.current) {
      setItems(startupData.watchHistory || []);
      setLoading(false);
      setError(null);
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
      const item = items.find((i) => makeKey(i.mediaType, i.itemId) === key);
      return item?.watched ?? false;
    },
    [items],
  );

  const getItem = useCallback(
    (mediaType: string, id: string): WatchStatusItem | undefined => {
      const key = makeKey(mediaType, id);
      return items.find((i) => makeKey(i.mediaType, i.itemId) === key);
    },
    [items],
  );

  const toggleWatchStatus = useCallback(
    async (mediaType: string, id: string, metadata?: Partial<WatchStatusUpdate>) => {
      if (!activeUser?.id) {
        throw new Error('No active user');
      }

      try {
        const updatedItem = await apiService.toggleWatchStatus(activeUser.id, mediaType, id, metadata);

        setItems((prev) => {
          const key = makeKey(mediaType, id);
          const existingIndex = prev.findIndex((i) => makeKey(i.mediaType, i.itemId) === key);

          if (existingIndex >= 0) {
            const updated = [...prev];
            updated[existingIndex] = updatedItem;
            return updated;
          } else {
            return [updatedItem, ...prev];
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

        setItems((prev) => {
          const key = makeKey(update.mediaType, update.itemId);
          const existingIndex = prev.findIndex((i) => makeKey(i.mediaType, i.itemId) === key);

          if (existingIndex >= 0) {
            const updated = [...prev];
            updated[existingIndex] = updatedItem;
            return updated;
          } else {
            return [updatedItem, ...prev];
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

        setItems((prev) => {
          const key = makeKey(mediaType, id);
          return prev.filter((i) => makeKey(i.mediaType, i.itemId) !== key);
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

        setItems((prev) => {
          const updated = [...prev];

          updatedItems.forEach((updatedItem) => {
            const key = makeKey(updatedItem.mediaType, updatedItem.itemId);
            const existingIndex = updated.findIndex((i) => makeKey(i.mediaType, i.itemId) === key);

            if (existingIndex >= 0) {
              updated[existingIndex] = updatedItem;
            } else {
              updated.push(updatedItem);
            }
          });

          return updated;
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
      items,
      loading,
      error,
      isWatched,
      getItem,
      toggleWatchStatus,
      updateWatchStatus,
      bulkUpdateWatchStatus,
      removeWatchStatus,
      refresh,
    }),
    [
      items,
      loading,
      error,
      isWatched,
      getItem,
      toggleWatchStatus,
      updateWatchStatus,
      bulkUpdateWatchStatus,
      removeWatchStatus,
      refresh,
    ],
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
