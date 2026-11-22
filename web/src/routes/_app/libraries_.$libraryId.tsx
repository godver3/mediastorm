import { useInfiniteQuery, useQuery } from '@tanstack/react-query';
import { createFileRoute, Link } from '@tanstack/react-router';
import { useCallback, useMemo, useState } from 'react';
import {
  groupPoster,
  groupToTitle,
  isShow,
  libraryKeys,
  listGroups,
  listLibraries,
  sourceLabel,
  type LibraryBrowseOptions,
  type LibraryGroup,
  type LibrarySort,
  type LibraryTypeFilter,
} from '@/api/library';
import { useActiveProfile } from '@/auth/hooks';
import { useSessionState } from '@/auth/sessionStore';
import {
  DebouncedSearch,
  DirectionToggle,
  LetterIndex,
  Segmented,
  SelectPill,
} from '@/components/browse/Controls';
import { LibraryGroupDialog } from '@/components/browse/LibraryGroupDialog';
import {
  GridPoster,
  LoadMoreSentinel,
  MediaGrid,
  MediaGridSkeleton,
} from '@/components/browse/MediaGrid';
import { Button } from '@/components/Button';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { Page } from '@/components/Page';
import { artworkUrl } from '@/lib/artwork';
import { detailsLink } from '@/lib/links';

export interface LibrarySearch {
  sort?: Exclude<LibrarySort, 'default'>;
  dir?: 'asc' | 'desc';
  type?: Exclude<LibraryTypeFilter, 'all'>;
  q?: string;
  letter?: string;
}

const SORTS: { value: LibrarySort; label: string }[] = [
  { value: 'default', label: 'Default' },
  { value: 'added', label: 'Date Added' },
  { value: 'name', label: 'Name' },
  { value: 'year', label: 'Year' },
];

export function validateLibrarySearch(raw: Record<string, unknown>): LibrarySearch {
  const str = (v: unknown) => (typeof v === 'string' && v.trim() ? v : undefined);
  return {
    sort: raw.sort === 'added' || raw.sort === 'name' || raw.sort === 'year' ? raw.sort : undefined,
    dir: raw.dir === 'asc' || raw.dir === 'desc' ? raw.dir : undefined,
    type: raw.type === 'movie' || raw.type === 'show' ? raw.type : undefined,
    q: str(raw.q),
    letter: str(raw.letter)?.slice(0, 1).toUpperCase(),
  };
}

/** A library's titles as a grid with sort, type filter, search and an A–Z index. */
export const Route = createFileRoute('/_app/libraries_/$libraryId')({
  validateSearch: validateLibrarySearch,
  component: LibraryPage,
});

function LibraryPage() {
  const { libraryId } = Route.useParams();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const { profileId } = useSessionState();
  const { profile } = useActiveProfile();
  const pid = profileId ?? '';

  const libs = useQuery({
    queryKey: libraryKeys.libraries(pid),
    queryFn: ({ signal }) => listLibraries(pid, signal),
    enabled: !!pid,
  });
  const library = libs.data?.find((l) => l.id === libraryId);

  const options: LibraryBrowseOptions = useMemo(
    () => ({
      sort: search.sort ?? 'default',
      dir: search.dir,
      type: search.type ?? 'all',
      q: search.q,
      letter: search.sort === 'name' ? search.letter : undefined,
    }),
    [search],
  );
  const query = useInfiniteQuery({
    queryKey: libraryKeys.groups(pid, libraryId, options),
    queryFn: ({ pageParam, signal }) => listGroups(pid, libraryId, options, pageParam, signal),
    initialPageParam: 0,
    getNextPageParam: (last, pages) => {
      const fetched = pages.reduce((n, p) => n + p.groups.length, 0);
      return !last.groups.length || fetched >= last.total ? undefined : fetched;
    },
    enabled: !!pid,
    staleTime: 30_000,
  });
  const groups = useMemo(() => {
    const seen = new Set<string>();
    return (query.data?.pages ?? [])
      .flatMap((p) => p.groups)
      .filter((g) => (seen.has(g.id) ? false : (seen.add(g.id), true)));
  }, [query.data]);
  const first = query.data?.pages[0];
  const total = first?.total ?? 0;

  const [opened, setOpened] = useState<LibraryGroup | null>(null);
  const set = useCallback(
    (patch: Partial<LibrarySearch>) =>
      void navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true }),
    [navigate],
  );
  const onQuery = useCallback((q: string) => set({ q: q.trim() || undefined }), [set]);

  const sort: LibrarySort = search.sort ?? 'default';
  const ascending = (search.dir ?? (sort === 'name' ? 'asc' : 'desc')) === 'asc';
  // Movie and show libraries hold one kind; mixed ("other") libraries get the filter.
  const showTypeFilter = !library || library.type === 'other';
  const filtered = !!(search.q || search.type || search.letter);

  const subtitle = (
    <span className="flex flex-wrap items-center gap-x-2">
      <Link to="/libraries" className="hover:text-fg hover:underline">
        Libraries
      </Link>
      {library && (
        <>
          <span aria-hidden="true">·</span>
          <span>{sourceLabel(library.sourceType)}</span>
        </>
      )}
      {query.isSuccess && (
        <>
          <span aria-hidden="true">·</span>
          <span>
            {total} {total === 1 ? 'title' : 'titles'}
          </span>
        </>
      )}
    </span>
  );

  return (
    <Page title={library?.name ?? (libs.isPending ? '…' : 'Library')} subtitle={subtitle}>
      <div className="mb-6 flex flex-col gap-3">
        <div className="flex flex-wrap items-center gap-2">
          <DebouncedSearch
            label={`Search ${library?.name ?? 'library'}`}
            placeholder="Search this library"
            value={search.q ?? ''}
            onChange={onQuery}
          />
          {showTypeFilter && (
            <Segmented<LibraryTypeFilter>
              label="Type"
              value={search.type ?? 'all'}
              options={[
                { value: 'all', label: 'All' },
                { value: 'movie', label: 'Movies' },
                { value: 'show', label: 'Shows' },
              ]}
              onChange={(v) => set({ type: v === 'all' ? undefined : v })}
            />
          )}
          <div className="flex-1" />
          <SelectPill<LibrarySort>
            label="Sort"
            value={sort}
            options={SORTS}
            onChange={(v) =>
              set({ sort: v === 'default' ? undefined : v, dir: undefined, letter: undefined })
            }
          />
          <DirectionToggle
            ascending={ascending}
            disabled={sort === 'default'}
            onToggle={() => set({ dir: ascending ? 'desc' : 'asc' })}
          />
        </div>
        {sort === 'name' && (
          <LetterIndex
            letters={first?.alphabetBuckets ?? []}
            value={search.letter}
            onChange={(letter) => set({ letter })}
          />
        )}
      </div>

      {query.isPending ? (
        <MediaGridSkeleton />
      ) : query.isError ? (
        <ErrorState error={query.error} onRetry={() => void query.refetch()} />
      ) : groups.length === 0 ? (
        filtered ? (
          <EmptyState
            icon="search"
            title="No matches"
            description={search.q ? `Nothing in this library matches “${search.q}”.` : undefined}
            action={
              <Button
                variant="secondary"
                onClick={() => set({ q: undefined, type: undefined, letter: undefined })}
              >
                Clear search
              </Button>
            }
          />
        ) : (
          <EmptyState
            icon="library"
            title="This library is empty"
            description={
              library?.lastScanStatus === 'scanning'
                ? 'A scan is running. Titles appear here as they are found.'
                : 'No titles have been found in this library yet.'
            }
          />
        )
      ) : (
        <>
          <MediaGrid>
            {groups.map((g) => {
              const title = groupToTitle(g);
              return (
                <GridPoster
                  key={g.id}
                  title={g.title}
                  subtitle={[
                    g.year,
                    isShow(g) ? 'Series' : g.groupType === 'file' ? 'File' : 'Movie',
                  ]
                    .filter(Boolean)
                    .join(' · ')}
                  imageUrl={artworkUrl(groupPoster(g))}
                  link={
                    title
                      ? detailsLink(title)
                      : isShow(g)
                        ? {
                            to: '/libraries/$libraryId/show/$groupId',
                            params: { libraryId, groupId: g.id },
                            search: { title: g.title },
                          }
                        : undefined
                  }
                  onClick={title || isShow(g) ? undefined : () => setOpened(g)}
                />
              );
            })}
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
      {library && (
        <LibraryGroupDialog
          profileId={pid}
          profileName={profile?.name}
          library={library}
          group={opened}
          onOpenChange={(o) => !o && setOpened(null)}
        />
      )}
    </Page>
  );
}
