import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useStartupData } from '@/components/StartupDataContext';

import { apiService, SearchResult, TrendingItem } from '../services/api';

export interface UseApiState<T> {
  data: T | null;
  loading: boolean;
  error: string | null;
  refetch: () => void;
}

const DEFAULT_SEARCH_DEBOUNCE_MS = 400;
const MIN_SEARCH_QUERY_LENGTH = 2;
// Match the backend startupShelfLimit (20) to avoid fetching hundreds of
// trending items that the home screen will never display.
const TRENDING_FETCH_LIMIT = 20;

function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debouncedValue, setDebouncedValue] = useState(value);

  useEffect(() => {
    const timeoutId = setTimeout(() => setDebouncedValue(value), delayMs);
    return () => clearTimeout(timeoutId);
  }, [value, delayMs]);

  return debouncedValue;
}

interface TrendingState {
  data: TrendingItem[] | null;
  loading: boolean;
  error: string | null;
}

// Hook for trending movies
export function useTrendingMovies(
  userId?: string | null,
  enabled = true,
  hideUnreleased = false,
  hideWatched = false,
): UseApiState<TrendingItem[]> {
  const { backendUrl, isReady } = useBackendSettings();
  const { startupData, ready: startupReady } = useStartupData();
  const [state, setState] = useState<TrendingState>({ data: null, loading: enabled, error: null });
  const [refreshToken, setRefreshToken] = useState(0);
  const hydratedFromStartup = useRef(false);

  const refetch = useCallback(() => {
    hydratedFromStartup.current = false;
    setRefreshToken((t) => t + 1);
  }, []);

  useEffect(() => {
    if (!isReady || !enabled) {
      setState((prev) => ({ ...prev, loading: false }));
      return;
    }

    // Hydrate from startup bundle if available (avoids separate HTTP request)
    if (startupData?.trendingMovies && !hydratedFromStartup.current && refreshToken === 0) {
      console.log('[useTrendingMovies] Hydrating from startup bundle');
      setState({ data: startupData.trendingMovies.items, loading: false, error: null });
      hydratedFromStartup.current = true;
      return;
    }

    // Wait for startup bundle before falling back to independent fetch
    if (!startupReady && refreshToken === 0) {
      return;
    }

    let cancelled = false;
    const fetchData = async () => {
      try {
        setState((prev) => ({ ...prev, loading: true, error: null }));
        console.log('[useTrendingMovies] Fetching with userId:', userId, 'hideUnreleased:', hideUnreleased, 'hideWatched:', hideWatched);
        // Without limit, getTrendingMovies returns TrendingItem[]
        const result = await apiService.getTrendingMovies(
          userId ?? undefined,
          TRENDING_FETCH_LIMIT,
          undefined,
          hideUnreleased,
          hideWatched,
        );
        if (cancelled) {
          console.log('[useTrendingMovies] Request cancelled, ignoring response');
          return;
        }
        // With limit, getTrendingMovies returns { items, total } — extract items
        const items = Array.isArray(result) ? result : (result as { items: TrendingItem[] }).items;
        console.log('[useTrendingMovies] Received', items.length, 'items');
        if (items.length > 0) {
          const first = items[0];
          console.log('[useTrendingMovies] First item:', first.title.name, 'certification:', first.title.certification);
        }
        setState({ data: items, loading: false, error: null });
      } catch (err) {
        if (cancelled) return;
        console.error('[useTrendingMovies] Error:', err);
        setState((prev) => ({ ...prev, loading: false, error: err instanceof Error ? err.message : 'Failed to fetch trending movies' }));
      }
    };

    if (!hydratedFromStartup.current) {
      fetchData();
    }

    return () => {
      cancelled = true;
    };
  }, [isReady, backendUrl, userId, enabled, hideUnreleased, hideWatched, refreshToken, startupData, startupReady]);

  // Memoize return value to prevent unnecessary re-renders of consumers
  return useMemo(() => ({ data: state.data, loading: state.loading, error: state.error, refetch }), [state, refetch]);
}

// Hook for trending TV shows
export function useTrendingTVShows(
  userId?: string | null,
  enabled = true,
  hideUnreleased = false,
  hideWatched = false,
): UseApiState<TrendingItem[]> {
  const { backendUrl, isReady } = useBackendSettings();
  const { startupData, ready: startupReady } = useStartupData();
  const [state, setState] = useState<TrendingState>({ data: null, loading: enabled, error: null });
  const [refreshToken, setRefreshToken] = useState(0);
  const hydratedFromStartup = useRef(false);

  const refetch = useCallback(() => {
    hydratedFromStartup.current = false;
    setRefreshToken((t) => t + 1);
  }, []);

  useEffect(() => {
    if (!isReady || !enabled) {
      setState((prev) => ({ ...prev, loading: false }));
      return;
    }

    // Hydrate from startup bundle if available (avoids separate HTTP request)
    if (startupData?.trendingSeries && !hydratedFromStartup.current && refreshToken === 0) {
      console.log('[useTrendingTVShows] Hydrating from startup bundle');
      setState({ data: startupData.trendingSeries.items, loading: false, error: null });
      hydratedFromStartup.current = true;
      return;
    }

    // Wait for startup bundle before falling back to independent fetch
    if (!startupReady && refreshToken === 0) {
      return;
    }

    let cancelled = false;
    const fetchData = async () => {
      try {
        setState((prev) => ({ ...prev, loading: true, error: null }));
        console.log('[useTrendingTVShows] Fetching with userId:', userId, 'hideUnreleased:', hideUnreleased, 'hideWatched:', hideWatched);
        const result = await apiService.getTrendingTVShows(
          userId ?? undefined,
          TRENDING_FETCH_LIMIT,
          undefined,
          hideUnreleased,
          hideWatched,
        );
        if (cancelled) {
          console.log('[useTrendingTVShows] Request cancelled, ignoring response');
          return;
        }
        // With limit, getTrendingTVShows returns { items, total } — extract items
        const items = Array.isArray(result) ? result : (result as { items: TrendingItem[] }).items;
        console.log('[useTrendingTVShows] Received', items.length, 'items');
        setState({ data: items, loading: false, error: null });
      } catch (err) {
        if (cancelled) return;
        setState((prev) => ({ ...prev, loading: false, error: err instanceof Error ? err.message : 'Failed to fetch trending TV shows' }));
      }
    };

    if (!hydratedFromStartup.current) {
      fetchData();
    }

    return () => {
      cancelled = true;
    };
  }, [isReady, backendUrl, userId, enabled, hideUnreleased, hideWatched, refreshToken, startupData, startupReady]);

  // Memoize return value to prevent unnecessary re-renders of consumers
  return useMemo(() => ({ data: state.data, loading: state.loading, error: state.error, refetch }), [state, refetch]);
}

// Helper to deduplicate and sort search results
function mergeAndSortResults(results: SearchResult[]): SearchResult[] {
  const deduped = new Map<string, SearchResult>();

  for (const result of results) {
    const title = result.title;
    const idKey =
      title.id ||
      `${title.mediaType ?? 'unknown'}-${title.name?.toLowerCase() ?? 'unknown'}-${title.year ?? 'unknown'}`;
    const existing = deduped.get(idKey);
    if (!existing || existing.score < result.score) {
      deduped.set(idKey, result);
    }
  }

  return Array.from(deduped.values()).sort((a, b) => b.score - a.score);
}

export function useSearchTitles(query: string, userId?: string): UseApiState<SearchResult[]> {
  const { backendUrl, isReady } = useBackendSettings();
  const [data, setData] = useState<SearchResult[] | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const debouncedQuery = useDebouncedValue(query.trim(), DEFAULT_SEARCH_DEBOUNCE_MS);

  const refetch = useCallback(() => {
    setRefreshToken((token) => token + 1);
  }, []);

  useEffect(() => {
    if (!isReady) {
      return;
    }

    if (!debouncedQuery || debouncedQuery.length < MIN_SEARCH_QUERY_LENGTH) {
      setData(null);
      setError(null);
      setLoading(false);
      return;
    }

    let cancelled = false;
    let pendingCount = 2;
    const allResults: SearchResult[] = [];

    setLoading(true);
    setError(null);
    setData(null);

    const handleResults = (results: SearchResult[], mediaType: string) => {
      if (cancelled) return;

      const normalised = (results ?? []).map((result) => ({
        ...result,
        title: { ...result.title, mediaType: result.title.mediaType || mediaType },
      }));

      allResults.push(...normalised);
      setData(mergeAndSortResults(allResults));
    };

    const handleComplete = () => {
      pendingCount--;
      if (pendingCount === 0 && !cancelled) {
        setLoading(false);
      }
    };

    const handleError = (err: unknown) => {
      if (cancelled) return;
      // Only set error if we have no results yet
      if (allResults.length === 0) {
        setError(err instanceof Error ? err.message : 'Failed to search titles');
      }
      handleComplete();
    };

    // Fire both searches independently - show results as they arrive
    apiService
      .searchMovies(debouncedQuery, userId)
      .then((results) => {
        handleResults(results, 'movie');
        handleComplete();
      })
      .catch(handleError);

    apiService
      .searchTVShows(debouncedQuery, userId)
      .then((results) => {
        handleResults(results, 'series');
        handleComplete();
      })
      .catch(handleError);

    return () => {
      cancelled = true;
    };
  }, [debouncedQuery, backendUrl, isReady, refreshToken, userId]);

  // Memoize return value to prevent unnecessary re-renders of consumers
  return useMemo(() => ({ data, loading, error, refetch }), [data, loading, error, refetch]);
}
