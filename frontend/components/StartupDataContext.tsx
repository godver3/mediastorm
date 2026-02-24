import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
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
  /** Monotonic counter that bumps on each successful refreshStartup(). Child contexts
   *  compare against their last-hydrated generation to know when to re-hydrate. */
  generation: number;
  /** Re-fetches the startup bundle and bumps the generation counter. Returns true on
   *  success so callers can fall back to individual refreshes on failure. */
  refreshStartup: () => Promise<boolean>;
}

const noop = async () => false;

const StartupDataContext = createContext<StartupDataContextValue>({
  startupData: null,
  ready: false,
  progress: null,
  generation: 0,
  refreshStartup: noop,
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
  const [generation, setGeneration] = useState(0);
  const { activeUserId } = useUserProfiles();
  const { isReady: backendReady } = useBackendSettings();
  // Track which user ID we last fetched for so a profile switch triggers a new fetch
  const lastFetchedUserId = useRef<string | null>(null);
  // Keep activeUserId in a ref so refreshStartup doesn't need it as a dependency
  const activeUserIdRef = useRef(activeUserId);
  activeUserIdRef.current = activeUserId;

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
        const fetchStart = Date.now();
        console.log('[StartupData] Fetching startup bundle for user:', activeUserId);
        const data = await apiService.getStartupData(activeUserId);
        if (cancelled) return;
        const fetchMs = Date.now() - fetchStart;

        // Log bundle content breakdown for performance analysis
        const cwCount = data.continueWatching?.length ?? 0;
        const wlCount = data.watchlist?.length ?? 0;
        const whCount = data.watchHistory?.length ?? 0;
        const tmCount = data.trendingMovies?.items?.length ?? 0;
        const tsCount = data.trendingSeries?.items?.length ?? 0;
        console.log(
          `[StartupData] Bundle received in ${fetchMs}ms — ` +
          `CW: ${cwCount} items, WL: ${wlCount} items (total: ${data.watchlistTotal}), ` +
          `History: ${whCount} items, Movies: ${tmCount} items, Series: ${tsCount} items, ` +
          `Settings: ${data.userSettings ? 'yes' : 'no'}`
        );

        // Estimate object size by re-serializing sections (dev builds only — 6x JSON.stringify is expensive)
        if (__DEV__ && Platform.isTV) {
          const sizeOf = (obj: unknown) => {
            try { return JSON.stringify(obj)?.length ?? 0; } catch { return 0; }
          };
          const sections = {
            continueWatching: sizeOf(data.continueWatching),
            watchlist: sizeOf(data.watchlist),
            watchHistory: sizeOf(data.watchHistory),
            trendingMovies: sizeOf(data.trendingMovies),
            trendingSeries: sizeOf(data.trendingSeries),
            userSettings: sizeOf(data.userSettings),
          };
          const totalKB = Object.values(sections).reduce((a, b) => a + b, 0) / 1024;
          console.log(
            `[StartupData] Bundle section sizes (KB): ` +
            Object.entries(sections)
              .map(([k, v]) => `${k}: ${(v / 1024).toFixed(1)}`)
              .join(', ') +
            ` — TOTAL: ${totalKB.toFixed(1)} KB`
          );
        }

        const hydrateStart = Date.now();
        lastFetchedUserId.current = activeUserId;
        // Single state update: data + ready in one render
        setState({ startupData: data, ready: true });
        console.log(`[StartupData] State updated in ${Date.now() - hydrateStart}ms`);
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

  // Re-fetch the startup bundle (e.g. on return from player) and bump generation
  const refreshStartup = useCallback(async (): Promise<boolean> => {
    const userId = activeUserIdRef.current;
    if (!userId) return false;
    try {
      const fetchStart = Date.now();
      console.log('[StartupData] refreshStartup() for user:', userId);
      const data = await apiService.getStartupData(userId);
      console.log(`[StartupData] refreshStartup() completed in ${Date.now() - fetchStart}ms`);
      lastFetchedUserId.current = userId;
      setState({ startupData: data, ready: true });
      setGeneration((g) => g + 1);
      return true;
    } catch (err) {
      console.warn('[StartupData] refreshStartup() failed:', err);
      return false;
    }
  }, []);

  // Poll metadata enrichment progress while startup data is still loading
  // On TV, delay the initial poll to avoid a render during the startup window
  const progress = useMetadataProgress(!state.ready, METADATA_PROGRESS_INITIAL_DELAY_MS);

  const value = useMemo<StartupDataContextValue>(
    () => ({ startupData: state.startupData, ready: state.ready, progress, generation, refreshStartup }),
    [state.startupData, state.ready, progress, generation, refreshStartup],
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
