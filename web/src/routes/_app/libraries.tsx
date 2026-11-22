import { useQuery } from '@tanstack/react-query';
import { createFileRoute } from '@tanstack/react-router';
import { libraryKeys, listLibraries, sourceLabel, type LibraryInfo } from '@/api/library';
import { useSessionState } from '@/auth/sessionStore';
import { ListTile, TileGrid } from '@/components/browse/ListTile';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { Icon } from '@/components/icons';
import { Page } from '@/components/Page';
import { Skeleton } from '@/components/Spinner';

/** The local, Plex and Jellyfin libraries this profile may browse. */
export const Route = createFileRoute('/_app/libraries')({
  component: LibrariesPage,
});

const typeLabel = (t: string) => (t === 'movie' ? 'Movies' : t === 'show' ? 'Shows' : 'Other');

function libraryIcon(lib: LibraryInfo) {
  if (lib.type === 'show') return <Icon name="tv" size={20} />;
  if (lib.type === 'movie') return <Icon name="film" size={20} />;
  return <Icon name="folder" size={20} />;
}

function LibrariesPage() {
  const { profileId } = useSessionState();
  const pid = profileId ?? '';
  const libs = useQuery({
    queryKey: libraryKeys.libraries(pid),
    queryFn: ({ signal }) => listLibraries(pid, signal),
    enabled: !!pid,
  });

  const groups = new Map<string, LibraryInfo[]>();
  for (const lib of libs.data ?? []) {
    const k = sourceLabel(lib.sourceType);
    groups.set(k, [...(groups.get(k) ?? []), lib]);
  }

  return (
    <Page
      title="Libraries"
      subtitle="Movies and shows from your local, Plex and Jellyfin libraries."
    >
      {libs.isPending ? (
        <TileGrid>
          {[0, 1, 2, 3].map((i) => (
            <Skeleton key={i} className="aspect-[16/9] rounded-card" />
          ))}
        </TileGrid>
      ) : libs.isError ? (
        <ErrorState error={libs.error} onRetry={() => void libs.refetch()} />
      ) : !libs.data?.length ? (
        <EmptyState
          icon="library"
          title="No libraries"
          description="Your server has no libraries this profile can browse. An admin can add local folders or connect Plex and Jellyfin in the admin settings."
        />
      ) : (
        [...groups.entries()].map(([source, items]) => (
          <section key={source} className="mb-10">
            <h2 className="mb-4 text-lg font-semibold tracking-tight">{source}</h2>
            <TileGrid>
              {items.map((lib) => (
                <ListTile
                  key={lib.id}
                  title={lib.name}
                  kicker={typeLabel(lib.type)}
                  subtitle={
                    lib.lastScanStatus === 'scanning'
                      ? 'Scanning…'
                      : lib.sourceServerName
                        ? lib.sourceServerName
                        : undefined
                  }
                  icon={libraryIcon(lib)}
                  link={{ to: '/libraries/$libraryId', params: { libraryId: lib.id } }}
                  seed={lib.id}
                />
              ))}
            </TileGrid>
          </section>
        ))
      )}
    </Page>
  );
}
