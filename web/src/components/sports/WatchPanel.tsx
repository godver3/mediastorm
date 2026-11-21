import { useMutation, useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import {
  getSportsStreams,
  prepareSportsPlayback,
  sportsKeys,
  type SportsStreamMatch,
} from '@/api/sports';
import { useActiveProfile } from '@/auth/hooks';
import { Button } from '@/components/Button';
import { errorMessage } from '@/components/EmptyState';
import { Icon } from '@/components/icons';
import { Skeleton } from '@/components/Spinner';
import { cn } from '@/lib/cn';
import { openPlayback } from '@/lib/playback';

/**
 * "Watch" panel: channels the server matched to the event (/sports/game/{id}/streams), with the
 * native "Search more sources" (scope=all). Playing starts a live HLS session and hands off to
 * /watch/playback.html.
 */
export function WatchPanel({
  eventId,
  title,
  kind,
  parentId,
  className,
}: {
  eventId: string;
  title: string;
  kind?: 'racing' | 'cycling';
  parentId?: string;
  className?: string;
}) {
  const { profileId, profile } = useActiveProfile();
  const [broad, setBroad] = useState(false);
  const streams = useQuery({
    queryKey: [...sportsKeys.streams(profileId ?? '', eventId, broad), kind ?? '', parentId ?? ''],
    queryFn: ({ signal }) =>
      getSportsStreams(profileId!, eventId, { broad, kind, parentId }, signal),
    enabled: !!profileId,
    staleTime: 60_000,
  });
  const play = useMutation({
    mutationFn: (match: SportsStreamMatch) =>
      prepareSportsPlayback(match, { profileId: profileId!, profileName: profile?.name, title }),
    onSuccess: (params) => openPlayback(params),
  });

  const matches = (streams.data ?? []).flatMap((g) => [
    { ...g.primary, confidenceTier: g.primary.confidenceTier ?? g.confidenceTier, alt: false },
    ...(g.alternatives ?? []).map((a) => ({ ...a, alt: true })),
  ]);

  return (
    <section
      aria-labelledby="watch-heading"
      className={cn('rounded-2xl border border-line bg-surface p-5', className)}
    >
      <div className="mb-4 flex items-center justify-between gap-2">
        <h2 id="watch-heading" className="flex items-center gap-2 text-base font-semibold">
          <Icon name="play" size={18} /> Watch
        </h2>
        {streams.isFetching && !streams.isLoading ? (
          <span className="text-xs text-faint">Updating…</span>
        ) : null}
      </div>
      {streams.isLoading ? (
        <div className="space-y-2">
          <Skeleton className="h-14 rounded-xl" />
          <Skeleton className="h-14 rounded-xl" />
        </div>
      ) : streams.isError ? (
        <p className="text-sm text-muted">
          Couldn't look for channels: {errorMessage(streams.error)}
        </p>
      ) : matches.length === 0 ? (
        <p className="text-sm text-muted">
          No channel looks like it carries this event{broad ? '' : ' in the default sources'}.
        </p>
      ) : (
        <ul className="flex flex-col gap-2">
          {matches.map((m) => (
            <li key={`${m.channelId}|${m.channelUrl}`}>
              <button
                type="button"
                disabled={play.isPending}
                onClick={() => play.mutate(m)}
                className="group flex w-full cursor-pointer items-center gap-3 rounded-xl border border-line bg-elevated p-3 text-left transition hover:border-line-strong hover:bg-surface-strong disabled:opacity-60"
              >
                {m.channelLogo ? (
                  <img
                    src={m.channelLogo}
                    alt=""
                    className="h-9 w-12 shrink-0 rounded object-contain"
                  />
                ) : (
                  <span className="flex h-9 w-12 shrink-0 items-center justify-center rounded bg-surface-strong text-faint">
                    <Icon name="tv" size={18} />
                  </span>
                )}
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{m.channelName}</span>
                  <span className="block truncate text-xs text-muted">
                    {[m.programTitle, m.sourceName].filter(Boolean).join(' · ') || m.matchReason}
                  </span>
                </span>
                {m.confidenceTier === 'strong' && !m.alt && (
                  <span className="rounded-full bg-[color-mix(in_srgb,var(--success)_18%,transparent)] px-2 py-0.5 text-[11px] font-semibold text-fg">
                    Best match
                  </span>
                )}
                <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-accent text-accent-contrast opacity-80 transition group-hover:opacity-100">
                  <Icon name="play" size={14} />
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      {play.isError && (
        <p role="alert" className="mt-3 text-sm text-danger">
          {errorMessage(play.error)}
        </p>
      )}
      {!broad && (
        <Button variant="ghost" size="sm" className="mt-3" onClick={() => setBroad(true)}>
          Search more sources
        </Button>
      )}
    </section>
  );
}
