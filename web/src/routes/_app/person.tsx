import { useQuery } from '@tanstack/react-query';
import { createFileRoute } from '@tanstack/react-router';
import { useMemo, useState } from 'react';
import { titleCard } from '@/api/home';
import { getPerson, searchKeys } from '@/api/search';
import { ErrorState, Page, PosterCard, Skeleton } from '@/components';
import { useDetailsHref } from '@/components/home/useDetailsHref';
import { artworkUrl } from '@/lib/artwork';
import { cn } from '@/lib/cn';

export interface PersonSearch {
  id: number;
  name?: string;
}

export const Route = createFileRoute('/_app/person')({
  validateSearch: (raw: Record<string, unknown>): PersonSearch => {
    const id = Number(raw.id);
    return {
      id: Number.isFinite(id) && id > 0 ? id : 0,
      name: typeof raw.name === 'string' ? raw.name : undefined,
    };
  },
  component: PersonPage,
});

type Filter = 'all' | 'movie' | 'series';

/** A person and their filmography (native PersonPage / MobilePersonView). */
function PersonPage() {
  const { id, name } = Route.useSearch();
  const [filter, setFilter] = useState<Filter>('all');
  const [expanded, setExpanded] = useState(false);
  const linkFor = useDetailsHref();
  const q = useQuery({
    queryKey: searchKeys.person(id),
    queryFn: ({ signal }) => getPerson(id, signal),
    enabled: id > 0,
    staleTime: 30 * 60_000,
  });
  const films = useMemo(() => q.data?.filmography ?? [], [q.data]);
  const shown = films.filter((t) =>
    filter === 'all'
      ? true
      : filter === 'movie'
        ? t.mediaType === 'movie'
        : t.mediaType !== 'movie',
  );
  const person = q.data?.person;
  const displayName = person?.name ?? name ?? 'Person';

  if (!id)
    return (
      <Page title="Person">
        <ErrorState title="Person not found" />
      </Page>
    );
  if (q.error)
    return (
      <Page title={displayName}>
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      </Page>
    );

  const photo = artworkUrl(person?.profileUrl, 'h632');
  const movies = films.filter((t) => t.mediaType === 'movie').length;

  return (
    <Page documentTitle={displayName}>
      <header className="flex flex-col gap-6 pt-8 pb-8 sm:flex-row sm:items-end">
        <div className="h-40 w-40 shrink-0 overflow-hidden rounded-full bg-surface-strong ring-1 ring-line sm:h-48 sm:w-48">
          {q.isPending ? (
            <Skeleton className="h-full w-full rounded-full" />
          ) : photo ? (
            <img src={photo} alt="" className="h-full w-full object-cover" />
          ) : null}
        </div>
        <div className="min-w-0 max-w-3xl">
          {person?.knownFor && (
            <p className="mb-1 text-sm font-medium tracking-wide text-accent uppercase">
              {person.knownFor}
            </p>
          )}
          <h1 className="text-3xl font-semibold tracking-tight sm:text-4xl">{displayName}</h1>
          {q.isPending ? (
            <div className="mt-3 flex flex-col gap-2">
              <Skeleton className="h-4 w-full rounded" />
              <Skeleton className="h-4 w-2/3 rounded" />
            </div>
          ) : (
            person?.biography && (
              <div className="mt-3">
                <p
                  className={cn(
                    'text-sm whitespace-pre-line text-muted',
                    !expanded && 'line-clamp-4',
                  )}
                >
                  {person.biography}
                </p>
                {person.biography.length > 320 && (
                  <button
                    type="button"
                    onClick={() => setExpanded((e) => !e)}
                    className="mt-1 text-sm font-medium text-fg hover:underline"
                  >
                    {expanded ? 'Show less' : 'Read more'}
                  </button>
                )}
              </div>
            )
          )}
        </div>
      </header>

      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <h2 className="text-lg font-semibold tracking-tight">Known for</h2>
        {films.length > 0 && (
          <div
            role="tablist"
            aria-label="Filter filmography"
            className="flex gap-1 rounded-xl bg-surface p-1"
          >
            {(
              [
                ['all', `All ${films.length}`],
                ['movie', `Movies ${movies}`],
                ['series', `TV ${films.length - movies}`],
              ] as const
            ).map(([f, label]) => (
              <button
                key={f}
                role="tab"
                aria-selected={filter === f}
                onClick={() => setFilter(f)}
                className={cn(
                  'rounded-lg px-3 py-1 text-sm transition',
                  filter === f ? 'accent-soft text-fg' : 'text-muted hover:text-fg',
                )}
              >
                {label}
              </button>
            ))}
          </div>
        )}
      </div>
      <div className="grid grid-cols-[repeat(auto-fill,minmax(130px,1fr))] gap-x-4 gap-y-6 sm:grid-cols-[repeat(auto-fill,minmax(160px,1fr))]">
        {q.isPending
          ? Array.from({ length: 12 }, (_, i) => (
              <Skeleton key={i} className="aspect-[2/3] rounded-card" />
            ))
          : shown.map((t) => {
              const card = titleCard(t);
              return (
                <PosterCard
                  key={t.id}
                  className="w-full!"
                  title={card.title}
                  subtitle={card.subtitle}
                  imageUrl={artworkUrl(card.posterUrl, 'w342')}
                  {...linkFor(card)}
                />
              );
            })}
      </div>
      {!q.isPending && shown.length === 0 && (
        <p className="py-10 text-center text-sm text-muted">No titles to show.</p>
      )}
    </Page>
  );
}
