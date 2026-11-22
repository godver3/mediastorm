import { useQueryClient } from '@tanstack/react-query';
import { useCallback } from 'react';
import {
  episodeWatchedUpdate,
  movieWatchedUpdate,
  setWatchedBulk,
  watchlistUpsert,
  type DetailsTarget,
} from '@/api/details';
import {
  episodeCode,
  hiddenItemFor,
  hiddenKeys,
  hideFromContinueWatching,
  hideTitle,
  homeKeys,
  isMovieId,
  type ContinueWatchingItem,
} from '@/api/home';
import { invalidateWatchState } from '@/api/invalidate';
import {
  addToCustomList,
  createCustomList,
  isSetAside,
  listCustomLists,
  listsKeys,
} from '@/api/lists';
import type { TileMenuItem } from '@/components/browse/ListTile';

const num = (v?: string) => (v && Number(v) > 0 ? Number(v) : undefined);

export function cwTarget(item: ContinueWatchingItem): DetailsTarget {
  const ids = item.externalIds ?? {};
  return {
    mediaType: isMovieId(item.seriesId) ? 'movie' : 'series',
    titleId: item.seriesId,
    name: item.seriesTitle,
    year: item.year,
    tmdbId: num(ids.tmdb),
    tvdbId: num(ids.tvdb),
    imdbId: ids.imdb || undefined,
    posterUrl: item.posterUrl,
    backdropUrl: item.backdropUrl,
    overview: item.overview,
  };
}

/**
 * A Continue Watching card's actions, in native TitleActionsMenu order: Hide from Continue
 * Watching, Set Aside, Hide from This Profile, Mark as Watched (the movie, or the card's
 * next episode). The card leaves the row at once; a failure brings it back on refetch.
 */
export function useContinueWatchingActions(profileId: string) {
  const qc = useQueryClient();

  const dropCard = useCallback(
    (seriesId: string) =>
      qc.setQueryData<ContinueWatchingItem[]>(homeKeys.continueWatching(profileId), (rows) =>
        rows?.filter((r) => r.seriesId !== seriesId),
      ),
    [qc, profileId],
  );
  const refresh = useCallback(
    () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: [...homeKeys.all, profileId] }),
        qc.invalidateQueries({ queryKey: [...listsKeys.all, profileId] }),
        qc.invalidateQueries({ queryKey: hiddenKeys.list(profileId) }),
      ]),
    [qc, profileId],
  );
  const run = useCallback(
    (item: ContinueWatchingItem, action: () => Promise<unknown>, drop = true) => {
      if (drop) dropCard(item.seriesId);
      void action()
        .catch(() => undefined)
        .finally(() => void refresh());
    },
    [dropCard, refresh],
  );

  return useCallback(
    (item: ContinueWatchingItem): TileMenuItem[] => {
      const target = cwTarget(item);
      const ep = item.nextEpisode;
      const movie = target.mediaType === 'movie';
      const items: TileMenuItem[] = [
        {
          label: 'Hide from Continue Watching',
          onSelect: () => run(item, () => hideFromContinueWatching(profileId, item.seriesId)),
        },
        {
          label: 'Set Aside',
          onSelect: () =>
            run(item, async () => {
              const lists = await listCustomLists(profileId);
              const list =
                lists?.find(isSetAside) ?? (await createCustomList(profileId, 'Set Aside'));
              await addToCustomList(profileId, list.id, watchlistUpsert(target));
            }),
        },
      ];
      if (movie) {
        items.push({
          label: 'Mark as Watched',
          onSelect: () =>
            run(item, async () => {
              await setWatchedBulk(profileId, [movieWatchedUpdate(target, true)]);
              await invalidateWatchState(qc, profileId);
            }),
        });
      } else if (ep) {
        items.push({
          label: `Mark ${episodeCode(ep.seasonNumber, ep.episodeNumber)} as Watched`,
          onSelect: () =>
            run(
              item,
              async () => {
                await setWatchedBulk(profileId, [
                  episodeWatchedUpdate(
                    target,
                    {
                      itemId: ep.itemId,
                      name: ep.title ?? '',
                      seasonNumber: ep.seasonNumber,
                      episodeNumber: ep.episodeNumber,
                    },
                    true,
                  ),
                ]);
                await invalidateWatchState(qc, profileId);
              },
              false,
            ),
        });
      }
      items.push({
        label: 'Hide from This Profile',
        danger: true,
        onSelect: () => run(item, () => hideTitle(profileId, hiddenItemFor(item))),
      });
      return items;
    },
    [profileId, qc, run],
  );
}
