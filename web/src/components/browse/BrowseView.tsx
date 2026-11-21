import {
  useInfiniteQuery,
  useMutation,
  useQueryClient,
  type InfiniteData,
} from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useMemo, type ReactNode } from 'react';
import {
  browsePage,
  isItemList,
  listFilters,
  listKey,
  listsKeys,
  listSorts,
  removeFromCustomList,
  removeFromWatchlist,
  SORT_LABELS,
  WATCH_LABELS,
  type BrowseEntry,
  type BrowseList,
  type BrowsePage,
  type BrowseSort,
  type WatchFilter,
} from '@/api/lists';
import { Button } from '@/components/Button';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { Page } from '@/components/Page';
import { artworkUrl } from '@/lib/artwork';
import { searchToOptions, type BrowseSearch } from '@/lib/browseSearch';
import { detailsLink } from '@/lib/links';
import { Chip, DirectionToggle, LetterIndex, Segmented, SelectPill } from './Controls';
import { Glyph } from './Glyph';
import { GridPoster, LoadMoreSentinel, MediaGrid, MediaGridSkeleton } from './MediaGrid';

export interface BrowseViewProps {
  profileId: string;
  list: BrowseList;
  title: ReactNode;
  search: BrowseSearch;
  onSearch: (next: BrowseSearch) => void;
  actions?: ReactNode;
  /** Shown above the grid (e.g. breadcrumbs). */
  eyebrow?: ReactNode;
  emptyTitle?: string;
  emptyDescription?: ReactNode;
}

/** Subtitle line under a card: "2024 · Series". */
export function entrySubtitle(e: BrowseEntry): string {
  const parts: string[] = [];
  if (e.title.year) parts.push(String(e.title.year));
  parts.push(e.title.mediaType === 'movie' ? 'Movie' : 'Series');
  return parts.join(' · ');
}

/** Badge in the poster corner: rank, unwatched episodes, in progress, release status. */
export function entryBadge(e: BrowseEntry): ReactNode {
  if (e.rank) return `#${e.rank}`;
  const t = e.title;
  if (
    t.mediaType !== 'movie' &&
    t.unwatchedCount &&
    t.unwatchedCount > 0 &&
    t.watchState !== 'none'
  )
    return `${t.unwatchedCount} new`;
  if (t.watchState === 'partial') return 'In progress';
  if (t.availability === 'unreleased' || t.status === 'unreleased') return 'Coming soon';
  return undefined;
}

export function BrowseView({
  profileId,
  list,
  title,
  search,
  onSearch,
  actions,
  eyebrow,
  emptyTitle = 'Nothing here yet',
  emptyDescription,
}: BrowseViewProps) {
  const options = useMemo(() => searchToOptions(search), [search]);
  const key = listKey(list);
  const queryKey = listsKeys.browse(profileId, key, options);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const query = useInfiniteQuery({
    queryKey,
    queryFn: ({ pageParam, signal }) => browsePage(profileId, list, options, pageParam, signal),
    initialPageParam: 0,
    getNextPageParam: (last, pages) => {
      const fetched = pages.reduce((n, p) => n + p.entries.length, 0);
      // Short pages mid-list happen (hidden / kids-rated titles dropped); only an empty
      // page or reaching the total ends the list (BrowsePage.listSize).
      if (!last.entries.length || fetched >= last.total) return undefined;
      return fetched;
    },
    staleTime: 30_000,
  });

  const entries = useMemo(() => {
    const seen = new Set<string>();
    return (query.data?.pages ?? [])
      .flatMap((p) => p.entries)
      .filter((e) => (seen.has(e.key) ? false : (seen.add(e.key), true)));
  }, [query.data]);
  const first = query.data?.pages[0];
  const total = first?.total ?? 0;

  const remove = useMutation({
    mutationFn: async (e: BrowseEntry) => {
      if (!e.item) return;
      if (list.kind === 'watchlist')
        await removeFromWatchlist(profileId, e.item.mediaType, e.item.id);
      else if (list.kind === 'custom')
        await removeFromCustomList(profileId, list.id, e.item.mediaType, e.item.id);
    },
    onMutate: (e) => {
      queryClient.setQueryData<InfiniteData<BrowsePage, number>>(queryKey, (data) =>
        data
          ? {
              ...data,
              pages: data.pages.map((p) => ({
                ...p,
                entries: p.entries.filter((x) => x.key !== e.key),
                total: Math.max(0, p.total - 1),
              })),
            }
          : data,
      );
    },
    onSettled: () => {
      void queryClient.invalidateQueries({ queryKey: listsKeys.all });
    },
  });
  const removable = list.kind === 'watchlist' || list.kind === 'custom';

  const sorts = listSorts(list);
  const filters = listFilters(list);
  const sort: BrowseSort = search.sort ?? 'default';
  const ascending = (search.dir ?? (sort === 'name' ? 'asc' : 'desc')) === 'asc';
  const selectedGenres = useMemo(() => options.genres ?? [], [options.genres]);
  const genres = useMemo(() => {
    const all = new Set([...(first?.genres ?? []), ...selectedGenres]);
    return [...all].sort((a, b) => a.localeCompare(b));
  }, [first?.genres, selectedGenres]);
  const isFiltered = !!(search.type || search.status || search.genre);

  const set = (patch: Partial<BrowseSearch>) => onSearch({ ...search, ...patch });

  const surprise = () => {
    if (!entries.length) return;
    const pick = entries[Math.floor(Math.random() * entries.length)]!;
    void navigate(detailsLink(pick.title));
  };

  const countLabel =
    query.isSuccess && total > 0 ? `${total} ${total === 1 ? 'title' : 'titles'}` : undefined;

  return (
    <Page
      title={title}
      subtitle={
        eyebrow || countLabel ? (
          <span className="flex flex-wrap items-center gap-x-2">
            {eyebrow}
            {eyebrow && countLabel && <span aria-hidden="true">·</span>}
            {countLabel}
          </span>
        ) : undefined
      }
      actions={
        <>
          {entries.length > 3 && list.kind !== 'top-ten' && (
            <Button
              variant="secondary"
              size="sm"
              onClick={surprise}
              leftIcon={<Glyph name="shuffle" size={16} />}
            >
              Surprise me
            </Button>
          )}
          {actions}
        </>
      }
    >
      {(sorts.length > 0 || filters) && (
        <div className="mb-6 flex flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2">
            {filters && (
              <Segmented
                label="Type"
                value={search.type ?? 'all'}
                options={[
                  { value: 'all', label: 'All' },
                  { value: 'movie', label: 'Movies' },
                  { value: 'series', label: 'Shows' },
                ]}
                onChange={(v) => set({ type: v === 'all' ? undefined : v })}
              />
            )}
            {filters && (
              <SelectPill<WatchFilter>
                label="Status"
                value={search.status ?? 'all'}
                options={(Object.keys(WATCH_LABELS) as WatchFilter[]).map((v) => ({
                  value: v,
                  label: WATCH_LABELS[v],
                }))}
                onChange={(v) => set({ status: v === 'all' ? undefined : v })}
              />
            )}
            <div className="flex-1" />
            {sorts.length > 0 && (
              <>
                <SelectPill<BrowseSort>
                  label="Sort"
                  value={sort}
                  options={sorts.map((s) => ({ value: s, label: SORT_LABELS[s] }))}
                  onChange={(v) =>
                    set({
                      sort: v === 'default' ? undefined : v,
                      dir: undefined,
                      letter: v === 'name' ? search.letter : undefined,
                    })
                  }
                />
                <DirectionToggle
                  ascending={ascending}
                  disabled={sort === 'default'}
                  onToggle={() => set({ dir: ascending ? 'desc' : 'asc' })}
                />
              </>
            )}
          </div>
          {filters && genres.length > 0 && (
            <div className="-mx-4 flex gap-2 overflow-x-auto px-4 pb-1 sm:mx-0 sm:flex-wrap sm:px-0">
              {genres.map((g) => {
                const active = selectedGenres.includes(g);
                return (
                  <Chip
                    key={g}
                    active={active}
                    onClick={() => {
                      const next = active
                        ? selectedGenres.filter((x) => x !== g)
                        : [...selectedGenres, g];
                      set({ genre: next.length ? next.join(',') : undefined });
                    }}
                  >
                    {g}
                  </Chip>
                );
              })}
            </div>
          )}
          {filters && sort === 'name' && (
            <LetterIndex
              letters={first?.letters ?? []}
              value={search.letter}
              onChange={(letter) => set({ letter })}
            />
          )}
        </div>
      )}

      {query.isPending ? (
        <MediaGridSkeleton />
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : entries.length === 0 ? (
        isFiltered || search.letter ? (
          <EmptyState
            icon="search"
            title="Nothing matches these filters"
            description="Try another type, status or genre."
            action={
              <Button
                variant="secondary"
                onClick={() => onSearch({ sort: search.sort, dir: search.dir })}
              >
                Clear filters
              </Button>
            }
          />
        ) : (
          <EmptyState
            icon={list.kind === 'watchlist' ? 'bookmark' : 'list'}
            title={emptyTitle}
            description={emptyDescription}
          />
        )
      ) : (
        <>
          <MediaGrid>
            {entries.map((e) => (
              <GridPoster
                key={e.key}
                title={e.title.name}
                subtitle={entrySubtitle(e)}
                imageUrl={artworkUrl(e.title.textPoster?.url ?? e.title.poster?.url)}
                badge={entryBadge(e)}
                watched={isItemList(list) && e.title.watchState === 'complete'}
                link={detailsLink(e.title)}
                onRemove={removable && e.item ? () => remove.mutate(e) : undefined}
                removeLabel={
                  list.kind === 'watchlist' ? 'Remove from watchlist' : 'Remove from list'
                }
              />
            ))}
          </MediaGrid>
          <LoadMoreSentinel
            hasMore={!!query.hasNextPage}
            loading={query.isFetchingNextPage}
            onVisible={() => {
              if (!query.isFetchingNextPage) void query.fetchNextPage();
            }}
          />
        </>
      )}
    </Page>
  );
}
