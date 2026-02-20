import React, { createContext, startTransition, useCallback, useContext, useMemo, useRef, useState } from 'react';
import { apiService, type Title } from '@/services/api';

type ReleaseData = {
  theatricalRelease?: Title['theatricalRelease'];
  homeRelease?: Title['homeRelease'];
};

interface MovieReleasesContextValue {
  /** Map of movie ID to release data */
  releases: Map<string, ReleaseData>;
  /** Check if a movie's releases have been fetched or queued */
  hasRelease: (id: string) => boolean;
  /** Queue movies for release fetching (batched and debounced) */
  queueReleaseFetch: (movies: Array<{ id: string; tmdbId?: number; imdbId?: string }>) => void;
}

const MovieReleasesContext = createContext<MovieReleasesContextValue | undefined>(undefined);

export const MovieReleasesProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [releases, setReleases] = useState<Map<string, ReleaseData>>(new Map());

  // Track which movie IDs we've already queued for fetching (prevents re-fetch)
  const fetchedIdsRef = useRef<Set<string>>(new Set());

  // Pending updates - batched and flushed with debounce
  const pendingUpdatesRef = useRef<Map<string, ReleaseData>>(new Map());
  const flushTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Movies queued for the next batch fetch
  const pendingFetchQueueRef = useRef<Array<{ id: string; tmdbId?: number; imdbId?: string }>>([]);
  const fetchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Flush pending release updates to state
  const flushPendingUpdates = useCallback(() => {
    if (pendingUpdatesRef.current.size === 0) return;
    const updates = pendingUpdatesRef.current;
    pendingUpdatesRef.current = new Map();
    if (__DEV__) {
      console.log(`[MovieReleasesContext] Flushing ${updates.size} release updates to state`);
    }
    startTransition(() => {
      setReleases((prev) => new Map([...prev, ...updates]));
    });
  }, []);

  // Queue release updates and schedule debounced flush
  const queueUpdates = useCallback(
    (updates: Map<string, ReleaseData>) => {
      for (const [id, data] of updates) {
        pendingUpdatesRef.current.set(id, data);
      }
      if (flushTimerRef.current) {
        clearTimeout(flushTimerRef.current);
      }
      // Use longer debounce during initial load to batch more updates together
      flushTimerRef.current = setTimeout(flushPendingUpdates, 300);
    },
    [flushPendingUpdates],
  );

  // Execute the batch fetch
  const executeFetch = useCallback(async () => {
    const moviesToFetch = pendingFetchQueueRef.current;
    pendingFetchQueueRef.current = [];

    if (moviesToFetch.length === 0) return;

    if (__DEV__) {
      console.log(`[MovieReleasesContext] Fetching releases for ${moviesToFetch.length} movies`);
    }

    try {
      const batchResponse = await apiService.batchMovieReleases(
        moviesToFetch.map((m) => ({ titleId: m.id, tmdbId: m.tmdbId, imdbId: m.imdbId })),
      );

      const updates = new Map<string, ReleaseData>();

      for (let i = 0; i < batchResponse.results.length; i++) {
        const result = batchResponse.results[i];
        const movie = moviesToFetch[i];

        if (!result.error) {
          updates.set(movie.id, {
            theatricalRelease: result.theatricalRelease,
            homeRelease: result.homeRelease,
          });
        }
      }

      if (updates.size > 0) {
        queueUpdates(updates);
      }
    } catch (error) {
      console.warn('[MovieReleasesContext] Failed to batch fetch movie releases:', error);
    }
  }, [queueUpdates]);

  // Queue movies for release fetching
  const queueReleaseFetch = useCallback(
    (movies: Array<{ id: string; tmdbId?: number; imdbId?: string }>) => {
      let addedCount = 0;
      for (const movie of movies) {
        if (!fetchedIdsRef.current.has(movie.id)) {
          fetchedIdsRef.current.add(movie.id);
          pendingFetchQueueRef.current.push(movie);
          addedCount++;
        }
      }

      if (addedCount === 0) return;

      // Debounce the fetch to batch multiple queueReleaseFetch calls
      if (fetchTimerRef.current) {
        clearTimeout(fetchTimerRef.current);
      }
      fetchTimerRef.current = setTimeout(executeFetch, 50);
    },
    [executeFetch],
  );

  // Check if a movie's releases have been fetched or queued
  const hasRelease = useCallback((id: string) => {
    return fetchedIdsRef.current.has(id);
  }, []);

  // Memoize context value to prevent unnecessary consumer re-renders
  const value = useMemo<MovieReleasesContextValue>(
    () => ({
      releases,
      hasRelease,
      queueReleaseFetch,
    }),
    [releases, hasRelease, queueReleaseFetch],
  );

  return <MovieReleasesContext.Provider value={value}>{children}</MovieReleasesContext.Provider>;
};

export const useMovieReleases = (): MovieReleasesContextValue => {
  const context = useContext(MovieReleasesContext);
  if (!context) {
    throw new Error('useMovieReleases must be used within a MovieReleasesProvider');
  }
  return context;
};
