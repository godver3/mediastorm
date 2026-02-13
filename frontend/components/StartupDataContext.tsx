import React, { createContext, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { apiService, type StartupData } from '@/services/api';

interface StartupDataContextValue {
  /** The bundled startup payload, or null while loading / on error. */
  startupData: StartupData | null;
  /** True once the startup fetch has resolved (success or failure). */
  ready: boolean;
}

const StartupDataContext = createContext<StartupDataContextValue>({
  startupData: null,
  ready: false,
});

/**
 * StartupDataProvider fetches `/api/users/{userId}/startup` once when the
 * active user is set, then exposes the bundled payload to child providers
 * so they can hydrate from it instead of making separate HTTP requests.
 *
 * This reduces ~21 startup requests to 1, which is critical for low-power
 * devices like Fire Stick where the React Native OkHttp→JS bridge is slow.
 */
export const StartupDataProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [startupData, setStartupData] = useState<StartupData | null>(null);
  const [ready, setReady] = useState(false);
  const { activeUserId } = useUserProfiles();
  const { isReady: backendReady } = useBackendSettings();
  // Track which user ID we last fetched for so a profile switch triggers a new fetch
  const lastFetchedUserId = useRef<string | null>(null);

  useEffect(() => {
    if (!backendReady || !activeUserId) {
      // Reset when no user is active (e.g. profile switch)
      setStartupData(null);
      setReady(false);
      lastFetchedUserId.current = null;
      return;
    }

    // Don't re-fetch if we already have data for this user
    if (lastFetchedUserId.current === activeUserId && startupData) {
      return;
    }

    let cancelled = false;

    const fetchStartup = async () => {
      try {
        console.log('[StartupData] Fetching startup bundle for user:', activeUserId);
        const data = await apiService.getStartupData(activeUserId);
        if (cancelled) return;
        console.log('[StartupData] Startup bundle received');
        setStartupData(data);
        lastFetchedUserId.current = activeUserId;
      } catch (err) {
        if (cancelled) return;
        console.warn('[StartupData] Startup fetch failed, providers will fetch individually:', err);
        // Even on failure, mark ready so child providers fall back to independent fetches
        setStartupData(null);
      } finally {
        if (!cancelled) {
          setReady(true);
        }
      }
    };

    void fetchStartup();

    return () => {
      cancelled = true;
    };
  }, [backendReady, activeUserId]); // eslint-disable-line react-hooks/exhaustive-deps

  const value = useMemo<StartupDataContextValue>(
    () => ({ startupData, ready }),
    [startupData, ready],
  );

  return <StartupDataContext.Provider value={value}>{children}</StartupDataContext.Provider>;
};

/**
 * useStartupData returns the bundled startup payload.  Returns
 * `{ startupData: null, ready: false }` if the provider is not in the tree
 * (graceful fallback so existing code continues to work).
 */
export const useStartupData = (): StartupDataContextValue => {
  return useContext(StartupDataContext);
};
