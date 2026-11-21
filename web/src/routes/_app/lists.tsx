import { useQuery } from '@tanstack/react-query';
import { createFileRoute, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import {
  decades,
  GENRES,
  isSetAside,
  listCustomLists,
  listKey,
  listsKeys,
  listTitle,
  type BrowseKind,
  type BrowseList,
  type CustomList,
} from '@/api/lists';
import { useSessionState } from '@/auth/sessionStore';
import { Segmented } from '@/components/browse/Controls';
import { Glyph } from '@/components/browse/Glyph';
import { DeleteListDialog, ListNameDialog } from '@/components/browse/ListDialogs';
import { ListTile, Section, TileGrid, type ListTileProps } from '@/components/browse/ListTile';
import { Button } from '@/components/Button';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { Icon } from '@/components/icons';
import { Page } from '@/components/Page';
import { Skeleton } from '@/components/Spinner';

interface ListsSearch {
  /** Kind shown in the Genres / Decades sections. */
  kind?: BrowseKind;
}

export const Route = createFileRoute('/_app/lists')({
  validateSearch: (raw: Record<string, unknown>): ListsSearch => ({
    kind: raw.kind === 'series' ? 'series' : undefined,
  }),
  component: ListsPage,
});

const tileLink = (list: BrowseList): ListTileProps['link'] => ({
  to: '/lists/$listKey',
  params: { listKey: listKey(list) },
});

const CHARTS: { list: BrowseList; kicker: string }[] = [
  { list: { kind: 'top-ten' }, kicker: 'Top 10' },
  { list: { kind: 'top-ten', mediaType: 'movie' }, kicker: 'Top 10' },
  { list: { kind: 'top-ten', mediaType: 'series' }, kicker: 'Top 10' },
  { list: { kind: 'trending', mediaType: 'movie' }, kicker: 'Trending' },
  { list: { kind: 'trending', mediaType: 'series' }, kicker: 'Trending' },
  { list: { kind: 'popular-on-server' }, kicker: 'Server' },
  { list: { kind: 'personalized' }, kicker: 'For you' },
];

const count = (n?: number) =>
  n === undefined ? undefined : `${n} ${n === 1 ? 'title' : 'titles'}`;

function ListsPage() {
  const { profileId } = useSessionState();
  const pid = profileId ?? '';
  const { kind = 'movie' } = Route.useSearch();
  const navigate = useNavigate();
  const routeNavigate = Route.useNavigate();
  const lists = useQuery({
    queryKey: listsKeys.custom(pid),
    queryFn: ({ signal }) => listCustomLists(pid, signal),
    enabled: !!pid,
  });
  const [creating, setCreating] = useState(false);
  const [renaming, setRenaming] = useState<CustomList | null>(null);
  const [deleting, setDeleting] = useState<CustomList | null>(null);

  const setAside = lists.data?.find(isSetAside);
  const own = (lists.data ?? []).filter((l) => !isSetAside(l));

  return (
    <Page
      title="Lists"
      subtitle="Your lists, charts and collections to browse."
      actions={
        <Button onClick={() => setCreating(true)} leftIcon={<Glyph name="plus" size={16} />}>
          New list
        </Button>
      }
    >
      <Section title="Personal">
        <TileGrid>
          <ListTile
            title="Watchlist"
            kicker="Personal"
            icon={<Icon name="bookmark" size={20} />}
            link={{ to: '/watchlist' }}
            seed="watchlist-tile"
          />
          {setAside && (
            <ListTile
              title={setAside.name}
              kicker="Parked"
              subtitle={count(setAside.itemCount)}
              link={tileLink({ kind: 'custom', id: setAside.id })}
              seed="set-aside"
            />
          )}
          <ListTile
            title="Permanent Prequeue"
            kicker="Always ready"
            icon={<Icon name="play" size={18} />}
            link={tileLink({ kind: 'permanent-prequeue' })}
            seed="prequeue-tile"
          />
        </TileGrid>
      </Section>

      <Section
        title="My lists"
        action={
          own.length > 0 ? (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setCreating(true)}
              leftIcon={<Glyph name="plus" size={15} />}
            >
              New
            </Button>
          ) : undefined
        }
      >
        {lists.isPending ? (
          <TileGrid>
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="aspect-[16/9] rounded-card" />
            ))}
          </TileGrid>
        ) : lists.isError ? (
          <ErrorState error={lists.error} onRetry={() => void lists.refetch()} className="py-8" />
        ) : own.length === 0 ? (
          <EmptyState
            className="rounded-card border border-dashed border-line py-10"
            title="No lists yet"
            description="Make a list to collect titles for later: a movie night, a genre deep-dive, anything."
            action={<Button onClick={() => setCreating(true)}>Create a list</Button>}
          />
        ) : (
          <TileGrid>
            {own.map((l) => (
              <ListTile
                key={l.id}
                title={l.name}
                subtitle={count(l.itemCount ?? 0)}
                kicker="List"
                link={tileLink({ kind: 'custom', id: l.id })}
                seed={l.id}
                menu={[
                  { label: 'Rename', onSelect: () => setRenaming(l) },
                  { label: 'Delete', onSelect: () => setDeleting(l), danger: true },
                ]}
              />
            ))}
          </TileGrid>
        )}
      </Section>

      <Section title="Charts">
        <TileGrid>
          {CHARTS.map(({ list, kicker }) => (
            <ListTile
              key={listKey(list)}
              title={listTitle(list)}
              kicker={kicker}
              link={tileLink(list)}
              seed={listKey(list)}
            />
          ))}
        </TileGrid>
      </Section>

      <div className="mb-4 flex items-center justify-between gap-3">
        <h2 className="text-lg font-semibold tracking-tight">Browse by genre and decade</h2>
        <Segmented
          label="Kind"
          value={kind}
          options={[
            { value: 'movie', label: 'Movies' },
            { value: 'series', label: 'Shows' },
          ]}
          onChange={(v) =>
            void routeNavigate({
              search: { kind: v === 'series' ? 'series' : undefined },
              replace: true,
            })
          }
        />
      </div>
      <Section title="Genres">
        <TileGrid dense>
          {GENRES[kind].map((g) => {
            const list: BrowseList = { kind: 'genre', mediaType: kind, genreId: g.id };
            return (
              <ListTile
                key={g.id}
                size="sm"
                title={g.name}
                link={tileLink(list)}
                seed={`genre-${g.name}`}
              />
            );
          })}
        </TileGrid>
      </Section>
      <Section title="Decades">
        <TileGrid dense>
          {decades(kind).map((d) => (
            <ListTile
              key={d}
              size="sm"
              title={`${d}s`}
              link={tileLink({ kind: 'decade', mediaType: kind, decade: d })}
              seed={`decade-${d}`}
            />
          ))}
        </TileGrid>
      </Section>

      <ListNameDialog
        profileId={pid}
        open={creating}
        onOpenChange={setCreating}
        onSaved={(l) =>
          void navigate({
            to: '/lists/$listKey',
            params: { listKey: listKey({ kind: 'custom', id: l.id }) },
          })
        }
      />
      <ListNameDialog
        profileId={pid}
        list={renaming ?? undefined}
        open={!!renaming}
        onOpenChange={(o) => !o && setRenaming(null)}
      />
      <DeleteListDialog
        profileId={pid}
        list={deleting}
        open={!!deleting}
        onOpenChange={(o) => !o && setDeleting(null)}
      />
    </Page>
  );
}
