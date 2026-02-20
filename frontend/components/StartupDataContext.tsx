import React, { createContext, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { Platform } from 'react-native';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { apiService, type MetadataProgressSnapshot, type StartupData } from '@/services/api';
import { useMetadataProgress } from '@/hooks/useMetadataProgress';

const METADATA_PROGRESS_INITIAL_DELAY_MS = Platform.isTV ? 2000 : 0;

interface StartupDataContextValue {
  /** The bundled startup payload, or null while loading / on error. */
  startupData: StartupData | null;
  /** True once the startup fetch has resolved (success or failure). */
  ready: boolean;
  /** Live progress snapshot for metadata enrichment, or null when idle/unavailable. */
  progress: MetadataProgressSnapshot | null;
}

const StartupDataContext = createContext<StartupDataContextValue>({
  startupData: null,
  ready: false,
  progress: null,
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
  // Batched state: startupData + ready in a single object so updates produce one
  // context change instead of two separate cascades through the component tree.
  const [state, setState] = useState<{ startupData: StartupData | null; ready: boolean }>({
    startupData: null,
    ready: false,
  });
  const { activeUserId } = useUserProfiles();
  const { isReady: backendReady } = useBackendSettings();
  // Track which user ID we last fetched for so a profile switch triggers a new fetch
  const lastFetchedUserId = useRef<string | null>(null);

  useEffect(() => {
    if (!backendReady || !activeUserId) {
      // Reset when no user is active (e.g. profile switch)
      setState({ startupData: null, ready: false });
      lastFetchedUserId.current = null;
      return;
    }

    // Don't re-fetch if we already have data for this user
    if (lastFetchedUserId.current === activeUserId && state.startupData) {
      return;
    }

    let cancelled = false;

    const fetchStartup = async () => {
      try {
        console.log('[StartupData] Fetching startup bundle for user:', activeUserId);
        const data = await apiService.getStartupData(activeUserId);
        if (cancelled) return;
        console.log('[StartupData] Startup bundle received');
        lastFetchedUserId.current = activeUserId;
        // Single state update: data + ready in one render
        setState({ startupData: data, ready: true });
      } catch (err) {
        if (cancelled) return;
        console.warn('[StartupData] Startup fetch failed, providers will fetch individually:', err);
        // Even on failure, mark ready so child providers fall back to independent fetches
        setState({ startupData: null, ready: true });
      }
    };

    void fetchStartup();

    return () => {
      cancelled = true;
    };
  }, [backendReady, activeUserId]); // eslint-disable-line react-hooks/exhaustive-deps

  // Poll metadata enrichment progress while startup data is still loading
  // On TV, delay the initial poll to avoid a render during the startup window
  const progress = useMetadataProgress(!state.ready, METADATA_PROGRESS_INITIAL_DELAY_MS);

  const value = useMemo<StartupDataContextValue>(
    () => ({ startupData: state.startupData, ready: state.ready, progress }),
    [state.startupData, state.ready, progress],
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
