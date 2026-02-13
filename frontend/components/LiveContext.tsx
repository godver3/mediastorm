import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { apiService, UserSettings } from '@/services/api';

import { useStartupData } from './StartupDataContext';
import { useUserProfiles } from './UserProfilesContext';

interface LiveContextValue {
  // Settings
  playlistUrl: string;
  setPlaylistUrl: (url: string) => Promise<void>;

  // Favorites
  favorites: Set<string>;
  toggleFavorite: (channelId: string) => Promise<void>;
  isFavorite: (channelId: string) => boolean;

  // Hidden Channels
  hiddenChannels: Set<string>;
  hideChannel: (channelId: string) => Promise<void>;
  unhideChannel: (channelId: string) => Promise<void>;
  isHidden: (channelId: string) => boolean;

  // Categories
  selectedCategories: string[];
  setSelectedCategories: (categories: string[]) => Promise<void>;
  toggleCategory: (category: string) => Promise<void>;

  // Ready state
  isReady: boolean;
}

const LiveContext = createContext<LiveContextValue | undefined>(undefined);

export const LiveProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const mountedRef = useRef(true);
  const { activeUserId } = useUserProfiles();
  const { startupData, ready: startupReady } = useStartupData();
  const userSettingsRef = useRef<UserSettings | null>(null);
  const hydratedFromStartup = useRef(false);

  // State
  const [playlistUrl, setPlaylistUrlState] = useState('');
  const [favorites, setFavorites] = useState<Set<string>>(new Set());
  const [hiddenChannels, setHiddenChannels] = useState<Set<string>>(new Set());
  const [selectedCategories, setSelectedCategoriesState] = useState<string[]>([]);
  const [isReady, setIsReady] = useState(false);

  useEffect(() => {
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // Apply user settings to Live TV state
  const applySettings = useCallback((settings: UserSettings | null) => {
    if (!mountedRef.current) return;
    userSettingsRef.current = settings;
    const liveTV = settings?.liveTV;
    if (liveTV) {
      setFavorites(new Set(liveTV.favoriteChannels || []));
      setHiddenChannels(new Set(liveTV.hiddenChannels || []));
      setSelectedCategoriesState(liveTV.selectedCategories || []);
    } else {
      setFavorites(new Set());
      setHiddenChannels(new Set());
      setSelectedCategoriesState([]);
    }
    setIsReady(true);
  }, []);

  // Load Live TV data from backend when active user changes
  useEffect(() => {
    let cancelled = false;

    if (!activeUserId) {
      hydratedFromStartup.current = false;
      if (mountedRef.current) {
        setFavorites(new Set());
        setHiddenChannels(new Set());
        setSelectedCategoriesState([]);
        setIsReady(true);
      }
      return;
    }

    // Hydrate from startup bundle if available (avoids separate HTTP request)
    if (startupData?.userSettings && !hydratedFromStartup.current) {
      applySettings(startupData.userSettings);
      hydratedFromStartup.current = true;
      return;
    }

    // Wait for startup bundle before falling back to independent fetch
    if (!startupReady) {
      return;
    }

    // Fallback: fetch independently (startup failed or didn't include userSettings)
    if (!hydratedFromStartup.current) {
      setIsReady(false);
      const fetchSettings = async () => {
        try {
          const settings = await apiService.getUserSettings(activeUserId);
          if (cancelled || !mountedRef.current) return;
          applySettings(settings);
        } catch (err) {
          console.warn('Failed to load Live TV user settings.', err);
          if (mountedRef.current) {
            applySettings(null);
          }
        }
      };
      void fetchSettings();
    }

    return () => {
      cancelled = true;
    };
  }, [activeUserId, startupData, startupReady, applySettings]);

  // Helper to save Live TV settings to backend
  const saveLiveTVSettings = useCallback(
    async (newFavorites: string[], newHidden: string[], newCategories: string[]) => {
      if (!activeUserId || !userSettingsRef.current) {
        return;
      }

      try {
        const updatedSettings: UserSettings = {
          ...userSettingsRef.current,
          liveTV: {
            favoriteChannels: newFavorites,
            hiddenChannels: newHidden,
            selectedCategories: newCategories,
          },
        };
        await apiService.updateUserSettings(activeUserId, updatedSettings);
        userSettingsRef.current = updatedSettings;
      } catch (err) {
        console.warn('Failed to save Live TV settings.', err);
      }
    },
    [activeUserId],
  );

  // Settings functions - playlist URL is now a global setting in backend
  const setPlaylistUrl = useCallback(async (candidate: string) => {
    const trimmed = candidate.trim();
    // Playlist URL is configured globally via backend settings
    // This is kept for API compatibility but does nothing persistent
    if (mountedRef.current) {
      setPlaylistUrlState(trimmed);
    }
  }, []);

  // Favorites functions
  const toggleFavorite = useCallback(
    async (channelId: string) => {
      setFavorites((prevFavorites) => {
        const newFavorites = new Set(prevFavorites);
        if (newFavorites.has(channelId)) {
          newFavorites.delete(channelId);
        } else {
          newFavorites.add(channelId);
        }

        // Persist to backend
        const favoritesArray = Array.from(newFavorites);
        saveLiveTVSettings(favoritesArray, Array.from(hiddenChannels), selectedCategories);

        return newFavorites;
      });
    },
    [hiddenChannels, saveLiveTVSettings, selectedCategories],
  );

  const isFavorite = useCallback(
    (channelId: string) => {
      return favorites.has(channelId);
    },
    [favorites],
  );

  // Hidden channels functions
  const hideChannel = useCallback(
    async (channelId: string) => {
      setHiddenChannels((prevHidden) => {
        const newHidden = new Set(prevHidden);
        newHidden.add(channelId);

        // Persist to backend
        const hiddenArray = Array.from(newHidden);
        saveLiveTVSettings(Array.from(favorites), hiddenArray, selectedCategories);

        return newHidden;
      });
    },
    [favorites, saveLiveTVSettings, selectedCategories],
  );

  const unhideChannel = useCallback(
    async (channelId: string) => {
      setHiddenChannels((prevHidden) => {
        const newHidden = new Set(prevHidden);
        newHidden.delete(channelId);

        // Persist to backend
        const hiddenArray = Array.from(newHidden);
        saveLiveTVSettings(Array.from(favorites), hiddenArray, selectedCategories);

        return newHidden;
      });
    },
    [favorites, saveLiveTVSettings, selectedCategories],
  );

  const isHidden = useCallback(
    (channelId: string) => {
      return hiddenChannels.has(channelId);
    },
    [hiddenChannels],
  );

  // Categories functions
  const setSelectedCategories = useCallback(
    async (categories: string[]) => {
      if (mountedRef.current) {
        setSelectedCategoriesState(categories);
      }

      // Persist to backend
      saveLiveTVSettings(Array.from(favorites), Array.from(hiddenChannels), categories);
    },
    [favorites, hiddenChannels, saveLiveTVSettings],
  );

  const toggleCategory = useCallback(
    async (category: string) => {
      const newCategories = selectedCategories.includes(category)
        ? selectedCategories.filter((c) => c !== category)
        : [...selectedCategories, category];

      await setSelectedCategories(newCategories);
    },
    [selectedCategories, setSelectedCategories],
  );

  const value = useMemo<LiveContextValue>(
    () => ({
      playlistUrl,
      setPlaylistUrl,
      favorites,
      toggleFavorite,
      isFavorite,
      hiddenChannels,
      hideChannel,
      unhideChannel,
      isHidden,
      selectedCategories,
      setSelectedCategories,
      toggleCategory,
      isReady,
    }),
    [
      playlistUrl,
      setPlaylistUrl,
      favorites,
      toggleFavorite,
      isFavorite,
      hiddenChannels,
      hideChannel,
      unhideChannel,
      isHidden,
      selectedCategories,
      setSelectedCategories,
      toggleCategory,
      isReady,
    ],
  );

  return <LiveContext.Provider value={value}>{children}</LiveContext.Provider>;
};

export const useLive = () => {
  const context = useContext(LiveContext);
  if (!context) {
    throw new Error('useLive must be used within a LiveProvider');
  }
  return context;
};

// Legacy hooks for backward compatibility - these just delegate to the main context
export const useLiveSettings = () => {
  const context = useLive();
  return {
    playlistUrl: context.playlistUrl,
    isReady: context.isReady,
    setPlaylistUrl: context.setPlaylistUrl,
  };
};

export const useLiveFavorites = () => {
  const context = useLive();
  return {
    favorites: context.favorites,
    isReady: context.isReady,
    toggleFavorite: context.toggleFavorite,
    isFavorite: context.isFavorite,
  };
};

export const useLiveHiddenChannels = () => {
  const context = useLive();
  return {
    hiddenChannels: context.hiddenChannels,
    isReady: context.isReady,
    hideChannel: context.hideChannel,
    unhideChannel: context.unhideChannel,
    isHidden: context.isHidden,
  };
};

export const useLiveCategories = () => {
  const context = useLive();
  return {
    selectedCategories: context.selectedCategories,
    isReady: context.isReady,
    setSelectedCategories: context.setSelectedCategories,
    toggleCategory: context.toggleCategory,
  };
};
