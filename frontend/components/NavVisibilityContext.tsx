import AsyncStorage from '@react-native-async-storage/async-storage';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { useUserProfiles } from './UserProfilesContext';

export type NavTabKey = 'home' | 'search' | 'lists' | 'live' | 'profiles' | 'downloads';

const ALL_TAB_KEYS: NavTabKey[] = ['home', 'search', 'lists', 'live', 'profiles', 'downloads'];

type VisibilityMap = Record<NavTabKey, boolean>;

const DEFAULT_VISIBILITY: VisibilityMap = {
  home: true,
  search: true,
  lists: true,
  live: true,
  profiles: true,
  downloads: true,
};

const storageKey = (userId: string) => `strmr.navVisibility.${userId}`;

interface NavVisibilityContextValue {
  isTabVisible: (key: NavTabKey) => boolean;
  setTabVisible: (key: NavTabKey, visible: boolean) => boolean;
  visibilityMap: VisibilityMap;
  isReady: boolean;
}

const NavVisibilityContext = createContext<NavVisibilityContextValue | undefined>(undefined);

export function NavVisibilityProvider({ children }: { children: React.ReactNode }) {
  const { activeUserId } = useUserProfiles();
  const [map, setMap] = useState<VisibilityMap>(DEFAULT_VISIBILITY);
  const [isReady, setIsReady] = useState(false);
  const lastLoadedUserId = useRef<string | null>(null);

  // Load visibility when active user changes
  useEffect(() => {
    if (!activeUserId) {
      setMap(DEFAULT_VISIBILITY);
      setIsReady(true);
      return;
    }

    if (activeUserId === lastLoadedUserId.current) return;
    lastLoadedUserId.current = activeUserId;

    let cancelled = false;
    (async () => {
      try {
        const raw = await AsyncStorage.getItem(storageKey(activeUserId));
        if (cancelled) return;
        if (raw) {
          const parsed = JSON.parse(raw) as Partial<VisibilityMap>;
          // Merge with defaults so new tabs added in future are visible by default
          setMap({ ...DEFAULT_VISIBILITY, ...parsed });
        } else {
          setMap(DEFAULT_VISIBILITY);
        }
      } catch {
        if (!cancelled) setMap(DEFAULT_VISIBILITY);
      }
      if (!cancelled) setIsReady(true);
    })();

    return () => {
      cancelled = true;
    };
  }, [activeUserId]);

  const isTabVisible = useCallback((key: NavTabKey) => map[key] ?? true, [map]);

  const setTabVisible = useCallback(
    (key: NavTabKey, visible: boolean): boolean => {
      // Safety: prevent disabling the last visible tab
      if (!visible) {
        const enabledCount = ALL_TAB_KEYS.filter((k) => (k === key ? false : map[k])).length;
        if (enabledCount < 1) {
          return false; // Caller should show a toast
        }
      }

      const next = { ...map, [key]: visible };
      setMap(next);

      // Persist asynchronously
      if (activeUserId) {
        AsyncStorage.setItem(storageKey(activeUserId), JSON.stringify(next)).catch(() => {});
      }
      return true;
    },
    [map, activeUserId],
  );

  const value = useMemo<NavVisibilityContextValue>(
    () => ({ isTabVisible, setTabVisible, visibilityMap: map, isReady }),
    [isTabVisible, setTabVisible, map, isReady],
  );

  return <NavVisibilityContext.Provider value={value}>{children}</NavVisibilityContext.Provider>;
}

export function useNavVisibility(): NavVisibilityContextValue {
  const ctx = useContext(NavVisibilityContext);
  if (!ctx) {
    throw new Error('useNavVisibility must be used within NavVisibilityProvider');
  }
  return ctx;
}
