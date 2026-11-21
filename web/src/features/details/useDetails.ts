import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useMemo, useState } from 'react';
import {
  addToWatchlist,
  detailsKeys,
  episodeWatchedUpdate,
  getCollection,
  getDetailsBundle,
  libraryMatches,
  movieWatchedUpdate,
  removeFromWatchlist,
  setWatchedBulk,
  toLibraryVersions,
  type DetailsTarget,
  type Episode,
} from '@/api/details';
import { invalidateWatchlist, invalidateWatchState } from '@/api/invalidate';
import type { DetailsSearch } from '@/lib/links';

export function targetFromSearch(s: DetailsSearch): DetailsTarget {
  return {
    mediaType: s.mediaType,
    titleId: s.titleId,
    name: s.name,
    year: s.year,
    tmdbId: s.tmdbId,
    tvdbId: s.tvdbId,
    imdbId: s.imdbId,
  };
}

/** Details bundle + library copies + collection, and the page's toggles. */
export function useDetails(profileId: string, search: DetailsSearch) {
  const queryClient = useQueryClient();
  const baseTarget = useMemo(() => targetFromSearch(search), [search]);
  const bundleKey = detailsKeys.bundle(profileId, baseTarget.mediaType, baseTarget.titleId);

  const bundle = useQuery({
    queryKey: bundleKey,
    queryFn: ({ signal }) => getDetailsBundle(profileId, baseTarget, signal),
    enabled: !!profileId && !!baseTarget.titleId,
    staleTime: 15_000,
  });

  const title = bundle.data?.movieDetails ?? bundle.data?.seriesDetails?.title ?? undefined;

  // Fill IDs / art the link didn't carry from the loaded title (watchlist, prequeue, library).
  const target = useMemo<DetailsTarget>(
    () => ({
      ...baseTarget,
      year: baseTarget.year ?? title?.year,
      tmdbId: baseTarget.tmdbId ?? title?.tmdbId,
      tvdbId: baseTarget.tvdbId ?? title?.tvdbId,
      imdbId: baseTarget.imdbId ?? title?.imdbId,
      posterUrl: title?.poster?.url,
      backdropUrl: title?.backdrop?.url,
      overview: title?.overview,
    }),
    [baseTarget, title],
  );

  const library = useQuery({
    queryKey: detailsKeys.library(profileId, baseTarget.mediaType, baseTarget.titleId),
    queryFn: ({ signal }) => libraryMatches(profileId, baseTarget, signal).then(toLibraryVersions),
    enabled: !!profileId && !!baseTarget.titleId,
    staleTime: 5 * 60_000,
    retry: false,
  });

  const collectionId = title?.collection?.id;
  const collection = useQuery({
    queryKey: detailsKeys.collection(collectionId ?? 0),
    queryFn: ({ signal }) => getCollection(collectionId!, signal),
    enabled: !!collectionId,
    staleTime: 10 * 60_000,
  });

  // Toggles flip at once and the bundle is re-read after (native watchedOverrides).
  const [watchlistOverride, setWatchlistOverride] = useState<boolean | null>(null);
  const [movieWatchedOverride, setMovieWatchedOverride] = useState<boolean | null>(null);
  const [episodeOverrides, setEpisodeOverrides] = useState<Record<string, boolean>>({});

  const inWatchlist = watchlistOverride ?? bundle.data?.inWatchlist ?? false;
  const movieWatched = movieWatchedOverride ?? bundle.data?.watched ?? false;
  const isEpisodeWatched = useCallback(
    (e: Episode) => episodeOverrides[e.id] ?? e.watched === true,
    [episodeOverrides],
  );


  const watchlist = useMutation({
    mutationFn: (add: boolean) =>
      add ? addToWatchlist(profileId, target) : removeFromWatchlist(profileId, target),
    onMutate: (add) => setWatchlistOverride(add),
    onError: () => setWatchlistOverride(null),
    onSuccess: () => void invalidateWatchlist(queryClient, profileId),
  });

  const movieWatchedMutation = useMutation({
    mutationFn: (watched: boolean) =>
      setWatchedBulk(profileId, [movieWatchedUpdate(target, watched)]),
    onMutate: (w) => setMovieWatchedOverride(w),
    onError: () => setMovieWatchedOverride(null),
    onSuccess: () => void invalidateWatchState(queryClient, profileId),
  });

  const episodesWatched = useMutation({
    mutationFn: (args: { episodes: Episode[]; watched: boolean; scope?: 'season' | 'show' }) =>
      setWatchedBulk(
        profileId,
        args.episodes.map((e) => episodeWatchedUpdate(target, e, args.watched)),
        args.scope,
      ),
    onMutate: ({ episodes, watched }) =>
      setEpisodeOverrides((o) => {
        const next = { ...o };
        for (const e of episodes) next[e.id] = watched;
        return next;
      }),
    onError: (_err, { episodes }) =>
      setEpisodeOverrides((o) => {
        const next = { ...o };
        for (const e of episodes) delete next[e.id];
        return next;
      }),
    onSuccess: () => void invalidateWatchState(queryClient, profileId),
  });

  return {
    bundle,
    title,
    target,
    library,
    collection,
    inWatchlist,
    movieWatched,
    isEpisodeWatched,
    watchlist,
    movieWatchedMutation,
    episodesWatched,
  };
}
