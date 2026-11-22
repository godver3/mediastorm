import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import {
  continueWatchingCard,
  getContinueWatching,
  getHiddenItems,
  hiddenKeys,
  homeKeys,
  unhideTitle,
  type HiddenItem,
} from '@/api/home';
import { listsKeys } from '@/api/lists';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { Page } from '@/components/Page';
import { useContinueWatchingActions } from '@/components/home/useContinueWatchingActions';
import { artworkUrl } from '@/lib/artwork';
import { detailsLink } from '@/lib/links';
import { CardMenu } from './CardMenu';
import { GridPoster, MediaGrid, MediaGridSkeleton } from './MediaGrid';

const eyebrow = (
  <Link to="/lists" className="hover:text-fg hover:underline">
    Lists
  </Link>
);

const titleCount = (n: number) => `${n} ${n === 1 ? 'title' : 'titles'}`;

function subtitle(extra?: string) {
  return (
    <span className="flex flex-wrap items-center gap-x-2">
      {eyebrow}
      {extra && <span aria-hidden="true">·</span>}
      {extra}
    </span>
  );
}

/** Continue Watching as a grid, each card with its actions (native MobileContinueWatchingGrid). */
export function ContinueWatchingGrid({ profileId }: { profileId: string }) {
  const cw = useQuery({
    queryKey: homeKeys.continueWatching(profileId),
    queryFn: ({ signal }) => getContinueWatching(profileId, signal),
    enabled: !!profileId,
    staleTime: 30_000,
  });
  const menuFor = useContinueWatchingActions(profileId);
  const items = cw.data ?? [];
  return (
    <Page
      title="Continue Watching"
      subtitle={subtitle(cw.isSuccess && items.length ? titleCount(items.length) : undefined)}
    >
      {cw.isPending ? (
        <MediaGridSkeleton />
      ) : cw.isError ? (
        <ErrorState error={cw.error} onRetry={() => void cw.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState
          icon="play"
          title="Nothing to continue"
          description="Titles you start watching show up here."
        />
      ) : (
        <MediaGrid>
          {items.map((item) => {
            const card = continueWatchingCard(item);
            return (
              <CardMenu key={item.seriesId} label={card.title} items={menuFor(item)}>
                <GridPoster
                  title={card.title}
                  subtitle={card.subtitle}
                  imageUrl={artworkUrl(card.posterUrl)}
                  badge={
                    card.badge ??
                    (card.progress && card.progress > 0
                      ? `${Math.round(card.progress * 100)}%`
                      : undefined)
                  }
                  link={
                    card.link
                      ? detailsLink(
                          card.link,
                          card.season && card.episode
                            ? { season: card.season, episode: card.episode }
                            : undefined,
                        )
                      : undefined
                  }
                />
              </CardMenu>
            );
          })}
        </MediaGrid>
      )}
    </Page>
  );
}

const hiddenTitle = (h: HiddenItem) => ({
  id: h.id,
  name: h.name || h.id,
  mediaType: h.mediaType === 'movie' ? ('movie' as const) : ('series' as const),
  year: h.year,
  tmdbId: Number(h.externalIds?.tmdb) || undefined,
  tvdbId: Number(h.externalIds?.tvdb) || undefined,
  imdbId: h.externalIds?.imdb || undefined,
});

/** Titles hidden from the profile, each with Unhide (native HiddenTitlesView). */
export function HiddenGrid({ profileId }: { profileId: string }) {
  const qc = useQueryClient();
  const key = hiddenKeys.list(profileId);
  const hidden = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => getHiddenItems(profileId, signal),
    enabled: !!profileId,
  });
  const unhide = useMutation({
    mutationFn: (h: HiddenItem) => unhideTitle(profileId, h.mediaType, h.id),
    onMutate: (h) =>
      qc.setQueryData<HiddenItem[]>(key, (rows) =>
        rows?.filter((r) => !(r.id === h.id && r.mediaType === h.mediaType)),
      ),
    onSettled: () =>
      Promise.all([
        qc.invalidateQueries({ queryKey: key }),
        qc.invalidateQueries({ queryKey: [...homeKeys.all, profileId] }),
        qc.invalidateQueries({ queryKey: [...listsKeys.all, profileId] }),
      ]),
  });
  const items = hidden.data ?? [];
  return (
    <Page
      title="Hidden"
      subtitle={subtitle(hidden.isSuccess && items.length ? titleCount(items.length) : undefined)}
    >
      {hidden.isPending ? (
        <MediaGridSkeleton />
      ) : hidden.isError ? (
        <ErrorState error={hidden.error} onRetry={() => void hidden.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState
          icon="eye"
          title="Nothing hidden"
          description="Nothing is hidden from this profile."
        />
      ) : (
        <>
          <p className="mb-5 text-sm text-muted">
            Hidden titles stay off Home and your lists. Unhide one to bring it back.
          </p>
          <MediaGrid>
            {items.map((h) => (
              <CardMenu
                key={`${h.mediaType}:${h.id}`}
                label={h.name || h.id}
                items={[{ label: 'Unhide', onSelect: () => unhide.mutate(h) }]}
              >
                <GridPoster
                  title={h.name || h.id}
                  subtitle={[h.year, h.mediaType === 'movie' ? 'Movie' : 'Series']
                    .filter(Boolean)
                    .join(' · ')}
                  imageUrl={artworkUrl(h.posterUrl)}
                  link={detailsLink(hiddenTitle(h))}
                />
              </CardMenu>
            ))}
          </MediaGrid>
        </>
      )}
    </Page>
  );
}
