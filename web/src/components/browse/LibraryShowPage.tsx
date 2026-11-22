import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import { useState } from 'react';
import {
  fetchGroupWithFiles,
  groupBackdrop,
  groupPoster,
  libraryKeys,
  listLibraries,
  sourceLabel,
  versionQuality,
  type LibrarySeason,
} from '@/api/library';
import { useActiveProfile } from '@/auth/hooks';
import { EmptyState, ErrorState, errorMessage } from '@/components/EmptyState';
import { Icon } from '@/components/icons';
import { Skeleton, Spinner } from '@/components/Spinner';
import { useDocumentTitle } from '@/lib/documentTitle';
import { artworkUrl } from '@/lib/artwork';
import { cn } from '@/lib/cn';
import { useLibraryStart } from './useLibraryStart';

/** Real seasons first, specials last (native LibraryShowView.seasons). */
export function orderSeasons(seasons: LibrarySeason[] = []): LibrarySeason[] {
  const all = [...seasons].sort((a, b) => a.seasonNumber - b.seasonNumber);
  return [...all.filter((s) => s.seasonNumber > 0), ...all.filter((s) => s.seasonNumber === 0)];
}

/**
 * An unmatched library show (no catalog details page): backdrop, season chips and episode
 * rows; playing an episode offers Resume / Start Over when it was left partway.
 */
export function LibraryShowPage({
  libraryId,
  groupId,
  title,
}: {
  libraryId: string;
  groupId: string;
  title?: string;
}) {
  const { profileId, profile } = useActiveProfile();
  const pid = profileId ?? '';
  const libs = useQuery({
    queryKey: libraryKeys.libraries(pid),
    queryFn: ({ signal }) => listLibraries(pid, signal),
    enabled: !!pid,
  });
  const library = libs.data?.find((l) => l.id === libraryId);
  const group = useQuery({
    queryKey: libraryKeys.group(pid, libraryId, groupId),
    queryFn: ({ signal }) =>
      fetchGroupWithFiles(pid, libraryId, { id: groupId, title: title ?? '' }, signal),
    enabled: !!pid && !!title,
  });
  const g = group.data;
  useDocumentTitle(g?.title ?? title ?? 'Library');
  const seasons = orderSeasons(g?.seasons);
  const [picked, setPicked] = useState<number | null>(null);
  const season = seasons.find((s) => s.seasonNumber === picked) ?? seasons[0];
  const { start, starting, error, dialog } = useLibraryStart(pid, profile?.name);
  const backdrop = g ? groupBackdrop(g) : undefined;
  const poster = g ? groupPoster(g) : undefined;

  return (
    <div className="relative -mt-2 min-h-[70vh]">
      {backdrop && (
        <div className="absolute inset-x-0 top-0 -z-10 h-[56vh] overflow-hidden" aria-hidden>
          <img
            src={artworkUrl(backdrop, 'w1280')}
            alt=""
            className="h-full w-full object-cover object-[center_20%] opacity-60"
          />
          <div className="absolute inset-0 bg-gradient-to-t from-bg via-bg/70 to-bg/20" />
        </div>
      )}
      <div className="px-4 pt-10 pb-16 sm:px-6 lg:px-10 lg:pt-24">
        <div className="mb-2 text-sm text-muted">
          <Link
            to="/libraries/$libraryId"
            params={{ libraryId }}
            className="hover:text-fg hover:underline"
          >
            {library?.name ?? 'Library'}
          </Link>
          {library && <span> · {sourceLabel(library.sourceType)}</span>}
        </div>
        <div className="flex items-end gap-6">
          {poster && (
            <img
              src={artworkUrl(poster, 'w342')}
              alt=""
              className="hidden aspect-[2/3] w-40 shrink-0 rounded-card object-cover shadow-card ring-1 ring-line sm:block"
            />
          )}
          <div className="min-w-0">
            <h1 className="text-3xl font-bold tracking-tight sm:text-5xl">
              {g?.title ?? title ?? '…'}
            </h1>
            {g && (
              <p className="mt-2 text-sm text-muted">
                {[g.year, `${seasons.length} ${seasons.length === 1 ? 'season' : 'seasons'}`]
                  .filter(Boolean)
                  .join(' · ')}
              </p>
            )}
            {g?.overview && (
              <p className="mt-3 line-clamp-3 max-w-3xl text-sm text-fg/85">{g.overview}</p>
            )}
          </div>
        </div>

        <div className="mt-8 max-w-5xl">
          {group.isPending && !!title ? (
            <div className="flex flex-col gap-2">
              {[0, 1, 2, 3].map((i) => (
                <Skeleton key={i} className="h-20 w-full rounded-xl" />
              ))}
            </div>
          ) : group.isError ? (
            <ErrorState error={group.error} onRetry={() => void group.refetch()} />
          ) : !g || seasons.length === 0 ? (
            <EmptyState
              icon="library"
              title="No episodes"
              description="This show has no playable episodes in the library."
            />
          ) : (
            <>
              {seasons.length > 1 && (
                <div
                  role="tablist"
                  aria-label="Seasons"
                  className="no-scrollbar -mx-4 mb-5 flex gap-2 overflow-x-auto px-4 sm:mx-0 sm:px-0"
                >
                  {seasons.map((s) => {
                    const active = s.seasonNumber === season?.seasonNumber;
                    return (
                      <button
                        key={s.seasonNumber}
                        type="button"
                        role="tab"
                        aria-selected={active}
                        onClick={() => setPicked(s.seasonNumber)}
                        className={cn(
                          'h-9 shrink-0 cursor-pointer rounded-full px-4 text-sm font-medium ring-1 transition',
                          active
                            ? 'accent-soft ring-transparent'
                            : 'text-muted ring-line hover:text-fg hover:ring-line-strong',
                        )}
                      >
                        {s.seasonNumber === 0 ? 'Specials' : `Season ${s.seasonNumber}`}
                      </button>
                    );
                  })}
                </div>
              )}
              <ul className="flex flex-col gap-2">
                {season?.episodes.map((e) => {
                  const item = e.items[0];
                  if (!item) return null;
                  const name = e.episodeTitle ?? `Episode ${e.episodeNumber}`;
                  const image = e.episodeImage?.url ?? item.episodeImage?.url;
                  return (
                    <li key={e.episodeNumber}>
                      <button
                        type="button"
                        disabled={starting === item.id}
                        onClick={() =>
                          void start(g, item, {
                            seasonNumber: season.seasonNumber,
                            episodeNumber: e.episodeNumber,
                            episodeTitle: e.episodeTitle,
                            image,
                          })
                        }
                        className="group/ep flex w-full cursor-pointer items-center gap-4 rounded-xl border border-line bg-surface p-2.5 text-left transition hover:border-line-strong focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
                      >
                        <div className="relative aspect-video w-32 shrink-0 overflow-hidden rounded-lg bg-surface-strong sm:w-44">
                          {image && (
                            <img
                              src={artworkUrl(image, 'w300')}
                              alt=""
                              loading="lazy"
                              className="h-full w-full object-cover"
                            />
                          )}
                          <span className="absolute inset-0 flex items-center justify-center bg-black/30 text-white opacity-0 transition group-hover/ep:opacity-100">
                            <Icon name="play" size={22} filled />
                          </span>
                        </div>
                        <div className="min-w-0 flex-1">
                          <div className="truncate text-sm font-semibold">
                            {e.episodeNumber}. {name}
                          </div>
                          <div className="truncate text-xs text-muted">{versionQuality(item)}</div>
                          {e.episodeOverview && (
                            <p className="mt-1 line-clamp-2 text-xs text-faint max-sm:hidden">
                              {e.episodeOverview}
                            </p>
                          )}
                        </div>
                        <span
                          aria-hidden
                          className="flex h-8 shrink-0 items-center gap-1.5 rounded-full bg-surface-strong px-3 text-xs font-medium text-fg ring-1 ring-line transition group-hover/ep:ring-line-strong max-sm:hidden"
                        >
                          {starting === item.id ? (
                            <Spinner />
                          ) : (
                            <Icon name="play" size={13} filled />
                          )}
                          Play
                        </span>
                      </button>
                    </li>
                  );
                })}
              </ul>
            </>
          )}
          {error !== null && (
            <p role="alert" className="mt-3 text-sm text-danger">
              {errorMessage(error)}
            </p>
          )}
        </div>
      </div>
      {dialog}
    </div>
  );
}
