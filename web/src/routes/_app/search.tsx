import { useQuery } from '@tanstack/react-query';
import { createFileRoute, useNavigate } from '@tanstack/react-router';
import { useEffect, useMemo, useRef, useState } from 'react';
import { titleCard } from '@/api/home';
import {
  addingRecent,
  getTopTen,
  MAX_QUERY_LENGTH,
  MIN_QUERY_LENGTH,
  readRecent,
  runSearch,
  SEARCH_DEBOUNCE_MS,
  searchKeys,
  writeRecent,
} from '@/api/search';
import type { Title } from '@/api/titles';
import { useActiveProfile } from '@/auth/hooks';
import { Button, EmptyState, Icon, Page, PosterCard, Skeleton } from '@/components';
import { useDetailsHref } from '@/components/home/useDetailsHref';
import { PersonCard } from '@/components/search/PersonCard';
import { artworkUrl } from '@/lib/artwork';
import { cn } from '@/lib/cn';

export type SearchScope = 'all' | 'movie' | 'series' | 'person';
const SCOPES: { id: SearchScope; label: string }[] = [
  { id: 'all', label: 'All' },
  { id: 'movie', label: 'Movies' },
  { id: 'series', label: 'TV Shows' },
  { id: 'person', label: 'People' },
];

export interface SearchParams {
  q?: string;
  type?: Exclude<SearchScope, 'all'>;
}

export const Route = createFileRoute('/_app/search')({
  validateSearch: (search: Record<string, unknown>): SearchParams => ({
    q:
      typeof search.q === 'string' && search.q.trim()
        ? search.q.slice(0, MAX_QUERY_LENGTH)
        : undefined,
    type:
      search.type === 'movie' || search.type === 'series' || search.type === 'person'
        ? search.type
        : undefined,
  }),
  component: SearchPage,
});

const GRID =
  'grid grid-cols-[repeat(auto-fill,minmax(130px,1fr))] gap-x-4 gap-y-6 sm:grid-cols-[repeat(auto-fill,minmax(160px,1fr))]';

function useDebounced<T>(value: T, ms: number) {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = window.setTimeout(() => setV(value), ms);
    return () => window.clearTimeout(t);
  }, [value, ms]);
  return v;
}

function SearchPage() {
  const { q, type } = Route.useSearch();
  const navigate = useNavigate({ from: '/search' });
  const { profileId, profile } = useActiveProfile();
  const scope: SearchScope = type ?? 'all';
  const [text, setText] = useState(q ?? '');
  const input = useRef<HTMLInputElement>(null);

  // The top bar (or back/forward) changed ?q=: follow it.
  useEffect(() => setText(q ?? ''), [q]);

  const debounced = useDebounced(text.trim(), SEARCH_DEBOUNCE_MS);
  useEffect(() => {
    if ((q ?? '') === debounced) return;
    void navigate({ search: (s) => ({ ...s, q: debounced || undefined }), replace: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [debounced]);

  const [recent, setRecent] = useState<string[]>(() => (profileId ? readRecent(profileId) : []));
  useEffect(() => setRecent(profileId ? readRecent(profileId) : []), [profileId]);
  const remember = (query: string) => {
    if (!profileId || query.trim().length < MIN_QUERY_LENGTH) return;
    const next = addingRecent(readRecent(profileId), query);
    writeRecent(profileId, next);
    setRecent(next);
  };

  const query = (q ?? '').trim();
  const hasQuery = query.length >= MIN_QUERY_LENGTH;
  const curatedKids = profile?.isKidsProfile && profile.kidsMode === 'content_list';

  const results = useQuery({
    queryKey: searchKeys.query(profileId ?? '', query.toLowerCase()),
    queryFn: ({ signal }) => runSearch(profileId!, query, signal),
    enabled: !!profileId && hasQuery && !curatedKids,
    staleTime: 2 * 60_000,
  });
  const topTen = useQuery({
    queryKey: searchKeys.topTen(profileId ?? ''),
    queryFn: ({ signal }) => getTopTen(profileId!, signal),
    enabled: !!profileId && !hasQuery && !curatedKids,
    staleTime: 10 * 60_000,
  });

  const groups = useMemo(() => {
    const all = results.data?.results.map((r) => r.title) ?? [];
    return {
      person: all.filter((t) => t.mediaType === 'person'),
      movie: all.filter((t) => t.mediaType === 'movie'),
      series: all.filter((t) => t.mediaType !== 'movie' && t.mediaType !== 'person'),
    };
  }, [results.data]);

  if (curatedKids) {
    return (
      <Page title="Search">
        <EmptyState
          icon="lock"
          title="Search is unavailable for this profile"
          description="Content is limited to the curated lists selected for this profile."
        />
      </Page>
    );
  }

  const setScope = (s: SearchScope) =>
    void navigate({ search: (p) => ({ ...p, type: s === 'all' ? undefined : s }), replace: true });
  const typing = text.trim() !== query && text.trim().length >= MIN_QUERY_LENGTH;
  const loading = hasQuery && (results.isPending || typing);
  const counts: Record<SearchScope, number> = {
    all: groups.person.length + groups.movie.length + groups.series.length,
    person: groups.person.length,
    movie: groups.movie.length,
    series: groups.series.length,
  };

  return (
    <Page>
      <h1 className="sr-only">Search</h1>
      <form
        role="search"
        className="sticky top-0 z-10 -mx-4 bg-bg/85 px-4 pt-6 pb-4 backdrop-blur sm:-mx-6 sm:px-6 lg:-mx-10 lg:px-10"
        onSubmit={(e) => {
          e.preventDefault();
          const v = text.trim();
          void navigate({ search: (s) => ({ ...s, q: v || undefined }), replace: true });
          remember(v);
        }}
      >
        <div className="relative max-w-3xl">
          <Icon
            name="search"
            size={22}
            className="pointer-events-none absolute top-1/2 left-4 -translate-y-1/2 text-faint"
          />
          <input
            ref={input}
            type="search"
            autoFocus={!q}
            value={text}
            maxLength={MAX_QUERY_LENGTH}
            onChange={(e) => setText(e.target.value)}
            placeholder="Movies, shows and people"
            aria-label="Search movies, shows and people"
            className="h-14 w-full rounded-2xl border border-line bg-surface pr-12 pl-12 text-lg outline-none transition placeholder:text-faint focus:border-accent focus:bg-surface-strong"
          />
          {text && (
            <button
              type="button"
              aria-label="Clear search"
              onClick={() => {
                setText('');
                input.current?.focus();
              }}
              className="absolute top-1/2 right-3 flex h-8 w-8 -translate-y-1/2 items-center justify-center rounded-full text-muted hover:bg-surface-strong hover:text-fg"
            >
              <Icon name="close" size={18} />
            </button>
          )}
        </div>
        {hasQuery && (
          <div role="tablist" aria-label="Result type" className="mt-4 flex flex-wrap gap-2">
            {SCOPES.map((s) => (
              <button
                key={s.id}
                type="button"
                role="tab"
                aria-selected={scope === s.id}
                onClick={() => setScope(s.id)}
                className={cn(
                  'h-9 rounded-full border px-4 text-sm transition',
                  scope === s.id
                    ? 'accent-soft border-transparent font-medium text-fg'
                    : 'border-line text-muted hover:border-line-strong hover:text-fg',
                )}
              >
                {s.label}
                {results.data && !loading && (
                  <span className="ml-1.5 text-xs opacity-70">{counts[s.id]}</span>
                )}
              </button>
            ))}
          </div>
        )}
      </form>

      {!hasQuery ? (
        <BeforeSearch
          recent={recent}
          onPick={(r) => {
            setText(r);
            void navigate({ search: (s) => ({ ...s, q: r }), replace: true });
          }}
          onClear={() => {
            if (profileId) writeRecent(profileId, []);
            setRecent([]);
          }}
          topTen={topTen.data}
          topTenLoading={topTen.isPending}
          short={text.trim().length > 0}
        />
      ) : loading ? (
        <ResultsSkeleton />
      ) : results.error ? (
        <EmptyState
          icon="alert"
          title="Search couldn’t complete"
          description="Please try again."
          action={<Button onClick={() => void results.refetch()}>Retry</Button>}
        />
      ) : (
        <div className="flex flex-col gap-10 pt-2">
          {results.data?.notice && (
            <div
              role="status"
              className="flex items-center gap-3 rounded-card border border-line bg-surface px-4 py-3 text-sm text-muted"
            >
              {results.data.notice}
              <Button size="sm" variant="secondary" onClick={() => void results.refetch()}>
                Retry
              </Button>
            </div>
          )}
          {counts[scope] === 0 ? (
            <EmptyState
              icon="search"
              title={`No results for “${query}”`}
              description="Check the spelling or try another title, show or person."
            />
          ) : (
            <>
              {(scope === 'all' || scope === 'person') && groups.person.length > 0 && (
                <ResultSection title="People" count={groups.person.length}>
                  <div className="grid grid-cols-[repeat(auto-fill,minmax(110px,1fr))] gap-x-4 gap-y-6 sm:grid-cols-[repeat(auto-fill,minmax(140px,1fr))]">
                    {(scope === 'all' ? groups.person.slice(0, 12) : groups.person).map((t) => (
                      <PersonCard key={t.id} title={t} onOpen={() => remember(query)} />
                    ))}
                  </div>
                </ResultSection>
              )}
              {(scope === 'all' || scope === 'movie') && groups.movie.length > 0 && (
                <ResultSection title="Movies" count={groups.movie.length}>
                  <TitleGrid titles={groups.movie} onOpen={() => remember(query)} />
                </ResultSection>
              )}
              {(scope === 'all' || scope === 'series') && groups.series.length > 0 && (
                <ResultSection title="TV Shows" count={groups.series.length}>
                  <TitleGrid titles={groups.series} onOpen={() => remember(query)} />
                </ResultSection>
              )}
            </>
          )}
        </div>
      )}
    </Page>
  );
}

function ResultSection({
  title,
  count,
  children,
}: {
  title: string;
  count: number;
  children: React.ReactNode;
}) {
  return (
    <section aria-label={title}>
      <h2 className="mb-4 flex items-baseline gap-2 text-lg font-semibold tracking-tight">
        {title}
        <span className="text-sm font-normal text-faint">{count}</span>
      </h2>
      {children}
    </section>
  );
}

function TitleGrid({
  titles,
  ranked,
  onOpen,
}: {
  titles: Title[];
  ranked?: boolean;
  onOpen?: () => void;
}) {
  const linkFor = useDetailsHref();
  return (
    <div className={GRID}>
      {titles.map((t, i) => {
        const card = titleCard(t, ranked ? i + 1 : undefined);
        const link = linkFor(card);
        return (
          <PosterCard
            key={t.id}
            className="w-full!"
            title={card.title}
            subtitle={card.subtitle}
            badge={card.badge ?? (t.watchState === 'complete' ? 'Watched' : undefined)}
            imageUrl={artworkUrl(card.posterUrl, 'w342')}
            href={link.href}
            onClick={(e) => {
              onOpen?.();
              link.onClick?.(e);
            }}
          />
        );
      })}
    </div>
  );
}

function ResultsSkeleton() {
  return (
    <div className="pt-2">
      <Skeleton className="mb-4 h-5 w-24 rounded" />
      <div className={GRID}>
        {Array.from({ length: 12 }, (_, i) => (
          <div key={i}>
            <Skeleton className="aspect-[2/3] rounded-card" />
            <Skeleton className="mt-2 h-3.5 w-3/4 rounded" />
          </div>
        ))}
      </div>
    </div>
  );
}

function BeforeSearch({
  recent,
  onPick,
  onClear,
  topTen,
  topTenLoading,
  short,
}: {
  recent: string[];
  onPick: (q: string) => void;
  onClear: () => void;
  topTen?: Title[];
  topTenLoading: boolean;
  short: boolean;
}) {
  return (
    <div className="flex flex-col gap-10 pt-2">
      {short && <p className="text-sm text-muted">Type at least {MIN_QUERY_LENGTH} characters.</p>}
      {recent.length > 0 && (
        <section aria-label="Recent searches">
          <div className="mb-3 flex items-baseline justify-between gap-4">
            <h2 className="text-lg font-semibold tracking-tight">Recent searches</h2>
            <button type="button" onClick={onClear} className="text-sm text-muted hover:text-fg">
              Clear
            </button>
          </div>
          <div className="flex flex-wrap gap-2">
            {recent.map((r) => (
              <button
                key={r}
                type="button"
                onClick={() => onPick(r)}
                className="inline-flex h-9 items-center gap-2 rounded-full border border-line bg-surface px-4 text-sm transition hover:border-line-strong hover:bg-surface-strong"
              >
                <Icon name="search" size={14} className="text-faint" />
                {r}
              </button>
            ))}
          </div>
        </section>
      )}
      <section aria-label="Top 10 Today">
        <h2 className="mb-4 text-lg font-semibold tracking-tight">Top 10 Today</h2>
        {topTenLoading ? (
          <div className={GRID}>
            {Array.from({ length: 10 }, (_, i) => (
              <Skeleton key={i} className="aspect-[2/3] rounded-card" />
            ))}
          </div>
        ) : topTen && topTen.length > 0 ? (
          <TitleGrid titles={topTen.slice(0, 10)} ranked />
        ) : (
          <p className="text-sm text-muted">Start typing to find movies, shows and people.</p>
        )}
      </section>
    </div>
  );
}
