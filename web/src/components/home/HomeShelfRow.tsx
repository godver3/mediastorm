import { Link } from '@tanstack/react-router';
import type { HomeCardModel, HomeSettings, PlannedShelf } from '@/api/home';
import { artworkUrl } from '@/lib/artwork';
import { LandscapeCard, PosterCard } from '../MediaCard';
import { Shelf } from '../Shelf';
import { Skeleton } from '../Spinner';
import { Button } from '../Button';
import { useDetailsHref } from './useDetailsHref';
import { useShelfCards } from './useShelfCards';
import { CardMenu } from '../browse/CardMenu';
import type { TileMenuItem } from '../browse/ListTile';
import { useContinueWatchingActions } from './useContinueWatchingActions';

export function ShelfCards({
  cards,
  landscape,
  menuFor,
}: {
  cards: HomeCardModel[];
  landscape: boolean;
  /** Per-card actions (Continue Watching); cards without get none. */
  menuFor?: (index: number) => TileMenuItem[] | undefined;
}) {
  const linkFor = useDetailsHref();
  return (
    <>
      {cards.map((card, index) => {
        const common = {
          title: card.title,
          subtitle: card.subtitle,
          progress: card.progress,
          badge: card.badge,
          ...linkFor(card),
        };
        const el = landscape ? (
          <LandscapeCard
            key={card.id}
            {...common}
            imageUrl={artworkUrl(card.landscapeUrl ?? card.backdropUrl, 'w780')}
            overlayText
          />
        ) : (
          <PosterCard key={card.id} {...common} imageUrl={artworkUrl(card.posterUrl, 'w342')} />
        );
        const menu = menuFor?.(index);
        return menu ? (
          <CardMenu key={card.id} label={card.title} items={menu} className="snap-start">
            {el}
          </CardMenu>
        ) : (
          el
        );
      })}
    </>
  );
}

export function ShelfSkeleton({ title, landscape }: { title: string; landscape: boolean }) {
  return (
    <Shelf title={title}>
      {Array.from({ length: landscape ? 5 : 8 }, (_, i) => (
        <div
          key={i}
          className={
            landscape
              ? 'w-[260px] shrink-0 sm:w-[300px] lg:w-[340px]'
              : 'w-[140px] shrink-0 sm:w-[160px] lg:w-[180px]'
          }
        >
          <Skeleton
            className={landscape ? 'aspect-video rounded-card' : 'aspect-[2/3] rounded-card'}
          />
          {!landscape && <Skeleton className="mt-2 h-3.5 w-3/4 rounded" />}
        </div>
      ))}
    </Shelf>
  );
}

/**
 * One configured Home shelf. Empty rows hide (as native); a failed row shows a
 * compact retry line instead of taking the page down.
 */
export function HomeShelfRow({
  shelf,
  settings,
  profileId,
}: {
  shelf: PlannedShelf;
  settings: HomeSettings;
  profileId: string;
}) {
  const data = useShelfCards(shelf, settings, profileId);
  const cwMenu = useContinueWatchingActions(profileId);
  if (data.isLoading) return <ShelfSkeleton title={shelf.title} landscape={shelf.landscape} />;
  if (data.error && data.cards.length === 0) {
    return (
      <section className="px-4 py-3 sm:px-6 lg:px-10" aria-label={shelf.title}>
        <h2 className="mb-2 text-lg font-semibold tracking-tight">{shelf.title}</h2>
        <div className="flex items-center gap-3 rounded-card border border-line bg-surface px-4 py-3 text-sm text-muted">
          Couldn’t load this row.
          <Button size="sm" variant="secondary" onClick={data.refetch}>
            Retry
          </Button>
        </div>
      </section>
    );
  }
  if (data.cards.length === 0) return null;
  const count = data.total && data.total > data.cards.length ? ` (${data.total})` : '';
  const linkClass = 'text-sm text-muted transition hover:text-fg';
  const more =
    shelf.source.kind === 'watchlist' && !shelf.limited ? (
      <Link to="/watchlist" search={seeAllSearch(settings)} className={linkClass}>
        See all{count}
      </Link>
    ) : shelf.seeAll ? (
      <Link
        to="/lists/$listKey"
        params={{ listKey: shelf.seeAll }}
        search={seeAllSearch(settings)}
        className={linkClass}
        aria-label={`See all ${shelf.title}`}
      >
        See all{count}
      </Link>
    ) : undefined;
  const items = data.items;
  return (
    <Shelf title={shelf.title} action={more}>
      <ShelfCards
        cards={data.cards}
        landscape={shelf.landscape}
        menuFor={items ? (i) => (items[i] ? cwMenu(items[i]) : undefined) : undefined}
      />
    </Shelf>
  );
}

/** A Movies / Shows Home view opens its lists filtered to that kind. */
function seeAllSearch(settings: HomeSettings) {
  return settings.filter === 'movies'
    ? { type: 'movie' as const }
    : settings.filter === 'shows'
      ? { type: 'series' as const }
      : {};
}
