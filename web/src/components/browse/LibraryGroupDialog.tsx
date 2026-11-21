import { useQuery } from '@tanstack/react-query';
import {
  fetchGroupWithFiles,
  isShow,
  libraryKeys,
  sourceLabel,
  versionQuality,
  versionSize,
  type LibraryGroup,
  type LibraryInfo,
  type LibraryItem,
} from '@/api/library';
import { Button } from '@/components/Button';
import { Dialog } from '@/components/Dialog';
import { ErrorState, errorMessage } from '@/components/EmptyState';
import { Icon } from '@/components/icons';
import { Skeleton } from '@/components/Spinner';
import { useLibraryStart } from './useLibraryStart';

/**
 * An unmatched library movie or file (no catalog details page): pick a copy and play it,
 * with Resume / Start Over when it was left partway. Mirrors native LibraryEntryLink +
 * LibraryVersionsView. Unmatched shows get their own page (LibraryShowPage).
 */
export function LibraryGroupDialog({
  profileId,
  profileName,
  library,
  group,
  onOpenChange,
}: {
  profileId: string;
  profileName?: string;
  library: LibraryInfo;
  group: LibraryGroup | null;
  onOpenChange: (open: boolean) => void;
}) {
  const full = useQuery({
    queryKey: libraryKeys.group(profileId, library.id, group?.id ?? ''),
    queryFn: ({ signal }) => fetchGroupWithFiles(profileId, library.id, group!, signal),
    enabled: !!group,
  });
  const { start, starting, error, dialog } = useLibraryStart(profileId, profileName);
  const play = (item: LibraryItem, label: { episode?: LibraryItem }) => {
    if (!group) return;
    const ep = label.episode;
    void start(
      group,
      item,
      ep && ep.seasonNumber != null && ep.episodeNumber != null
        ? {
            seasonNumber: ep.seasonNumber,
            episodeNumber: ep.episodeNumber,
            episodeTitle: ep.episodeTitle,
          }
        : undefined,
    );
  };

  const g = full.data;
  return (
    <>
      {dialog}
      <Dialog
        open={!!group}
        onOpenChange={onOpenChange}
        title={group?.title ?? ''}
        description={[group?.year, library.name].filter(Boolean).join(' · ')}
        size="md"
      >
        {full.isPending ? (
          <div className="flex flex-col gap-2">
            <Skeleton className="h-14 w-full" />
            <Skeleton className="h-14 w-full" />
          </div>
        ) : full.isError ? (
          <ErrorState error={full.error} onRetry={() => void full.refetch()} className="py-6" />
        ) : g && isShow(g) ? (
          <div className="max-h-[60vh] overflow-y-auto pr-1">
            {(g.seasons ?? []).map((s) => (
              <div key={s.seasonNumber} className="mb-4">
                <h3 className="mb-2 text-sm font-semibold text-muted">
                  {s.seasonNumber === 0 ? 'Specials' : `Season ${s.seasonNumber}`}
                </h3>
                <ul className="flex flex-col gap-1.5">
                  {s.episodes.map((e) => {
                    const item = e.items[0];
                    if (!item) return null;
                    return (
                      <li key={e.episodeNumber}>
                        <VersionRow
                          title={`${e.episodeNumber}. ${e.episodeTitle ?? `Episode ${e.episodeNumber}`}`}
                          detail={versionQuality(item)}
                          loading={starting === item.id}
                          onPlay={() =>
                            play(item, {
                              episode: {
                                ...item,
                                seasonNumber: s.seasonNumber,
                                episodeNumber: e.episodeNumber,
                                episodeTitle: e.episodeTitle,
                              },
                            })
                          }
                        />
                      </li>
                    );
                  })}
                </ul>
              </div>
            ))}
          </div>
        ) : (
          <ul className="flex flex-col gap-1.5">
            {(g?.items ?? []).length === 0 && (
              <li className="py-6 text-center text-sm text-muted">
                This title has no playable file.
              </li>
            )}
            {(g?.items ?? []).map((item) => (
              <li key={item.id}>
                <VersionRow
                  title={versionQuality(item)}
                  detail={[
                    `${sourceLabel(item.sourceType ?? library.sourceType)} · ${library.name}`,
                    versionSize(item),
                  ]
                    .filter(Boolean)
                    .join(' · ')}
                  hint={item.fileName}
                  loading={starting === item.id}
                  onPlay={() => play(item, {})}
                />
              </li>
            ))}
          </ul>
        )}
        {error !== null && (
          <p role="alert" className="mt-3 text-sm text-danger">
            {errorMessage(error)}
          </p>
        )}
      </Dialog>
    </>
  );
}

export function VersionRow({
  title,
  detail,
  hint,
  loading,
  onPlay,
}: {
  title: string;
  detail?: string;
  hint?: string;
  loading: boolean;
  onPlay: () => void;
}) {
  return (
    <div className="flex items-center gap-3 rounded-xl border border-line bg-surface px-3 py-2.5 transition hover:border-line-strong">
      <div className="min-w-0 flex-1">
        <div className="truncate text-sm font-medium">{title}</div>
        {detail && <div className="truncate text-xs text-muted">{detail}</div>}
        {hint && <div className="truncate text-[11px] text-faint">{hint}</div>}
      </div>
      <Button
        size="sm"
        onClick={onPlay}
        loading={loading}
        leftIcon={<Icon name="play" size={14} />}
      >
        Play
      </Button>
    </div>
  );
}
