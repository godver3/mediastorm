import { useQuery } from '@tanstack/react-query';
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { getHomeSettings, homeKeys } from '@/api/home';
import { resolveListKey } from '@/api/homeLists';
import {
  listCustomLists,
  listsKeys,
  listTitle,
  parseListKey,
  type BrowseList,
  type ParsedListKey,
} from '@/api/lists';
import { useActiveProfile } from '@/auth/hooks';
import { useSessionState } from '@/auth/sessionStore';
import { ContinueWatchingGrid, HiddenGrid } from '@/components/browse/SpecialGrids';
import { PageSpinner } from '@/components/Spinner';
import { BrowseView } from '@/components/browse/BrowseView';
import { DeleteListDialog, ListNameDialog } from '@/components/browse/ListDialogs';
import { Button } from '@/components/Button';
import { EmptyState } from '@/components/EmptyState';
import { Page } from '@/components/Page';
import { validateBrowseSearch } from '@/lib/browseSearch';

/**
 * One list as a grid (native BrowseGridView): custom lists, charts, genres, decades, Home
 * shelves' lists (See all), streaming services, Ask AI, Because You Watched; Continue
 * Watching and Hidden have their own grids.
 */
export const Route = createFileRoute('/_app/lists_/$listKey')({
  validateSearch: validateBrowseSearch,
  component: ListPage,
});

function ListPage() {
  const { listKey } = Route.useParams();
  const parsed = parseListKey(listKey);
  if (!parsed) return <NotFound />;
  return <ResolvedList parsed={parsed} />;
}

function NotFound() {
  const go = useNavigate();
  return (
    <Page documentTitle="List not found">
      <EmptyState
        icon="list"
        title="List not found"
        action={
          <Button variant="secondary" onClick={() => void go({ to: '/lists' })}>
            Back to Lists
          </Button>
        }
      />
    </Page>
  );
}

function ResolvedList({ parsed }: { parsed: ParsedListKey }) {
  const { profileId, profile } = useActiveProfile();
  const pid = profileId ?? '';
  const needsHome = parsed.kind === 'shelf' || parsed.kind === 'service';
  const settings = useQuery({
    queryKey: homeKeys.settings(pid),
    queryFn: ({ signal }) => getHomeSettings(pid, signal),
    enabled: !!pid && needsHome,
    staleTime: 60_000,
  });
  const list = resolveListKey(parsed, settings.data, profile);
  if (list === undefined) return settings.isError ? <NotFound /> : <PageSpinner />;
  if (list === null) return <NotFound />;
  if (list.kind === 'continue-watching') return <ContinueWatchingGrid profileId={pid} />;
  if (list.kind === 'hidden') return <HiddenGrid profileId={pid} />;
  return <ListBrowse list={list} />;
}

function ListBrowse({ list }: { list: BrowseList }) {
  const { profileId } = useSessionState();
  const pid = profileId ?? '';
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const go = useNavigate();
  const lists = useQuery({
    queryKey: listsKeys.custom(pid),
    queryFn: ({ signal }) => listCustomLists(pid, signal),
    enabled: !!pid && list.kind === 'custom',
  });
  const custom = list.kind === 'custom' ? lists.data?.find((l) => l.id === list.id) : undefined;
  const [renaming, setRenaming] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const title =
    list.kind === 'custom' ? (custom?.name ?? (lists.isPending ? '…' : 'List')) : listTitle(list);

  return (
    <>
      <BrowseView
        profileId={pid}
        list={list}
        title={title}
        search={search}
        onSearch={(next) => void navigate({ search: next, replace: true })}
        eyebrow={
          <Link to="/lists" className="hover:text-fg hover:underline">
            Lists
          </Link>
        }
        emptyTitle={list.kind === 'custom' ? 'This list is empty' : 'Nothing here right now'}
        emptyDescription={
          list.kind === 'custom'
            ? 'Add titles to it from their details page.'
            : list.kind === 'permanent-prequeue'
              ? 'Titles you keep resolved for instant playback show up here.'
              : 'The server has nothing for this list yet. Try again later.'
        }
        actions={
          custom ? (
            <>
              <Button variant="secondary" size="sm" onClick={() => setRenaming(true)}>
                Rename
              </Button>
              <Button variant="ghost" size="sm" onClick={() => setDeleting(true)}>
                Delete
              </Button>
            </>
          ) : undefined
        }
      />
      {custom && (
        <>
          <ListNameDialog
            profileId={pid}
            list={custom}
            open={renaming}
            onOpenChange={setRenaming}
          />
          <DeleteListDialog
            profileId={pid}
            list={custom}
            open={deleting}
            onOpenChange={setDeleting}
            onDeleted={() => void go({ to: '/lists' })}
          />
        </>
      )}
    </>
  );
}
