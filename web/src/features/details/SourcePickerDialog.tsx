import { useQuery } from '@tanstack/react-query';
import { useEffect, useState } from 'react';
import type { EpisodeNumbering, LibraryVersion } from '@/api/details';
import { versionOrigin, versionQuality } from '@/api/details';
import {
  getSourceHealth,
  searchSources,
  sourceQuery,
  type SourceHealth,
  type SourceResult,
} from '@/api/playback';
import { Dialog, EmptyState, ErrorState, Skeleton } from '@/components';
import { cn } from '@/lib/cn';
import { formatBytes } from './format';

export interface SourcePickerTarget {
  titleId: string;
  name: string;
  mediaType: 'movie' | 'series';
  year?: number;
  imdbId?: string;
  episode?: { season: number; episode: number; numbering?: EpisodeNumbering };
  /** Library copies, listed above the stream results (native Choose Source). */
  versions?: LibraryVersion[];
  /** A quality preset picked on the details page: the results are ranked with it. */
  qualityPreset?: string;
}

const HEALTH: Record<string, { label: string; tone: string }> = {
  cached: { label: 'Cached', tone: 'text-success' },
  healthy: { label: 'Healthy', tone: 'text-success' },
  not_cached: { label: 'Not cached', tone: 'text-warning' },
  unhealthy: { label: 'Unhealthy', tone: 'text-danger' },
  pending: { label: 'Checking…', tone: 'text-muted' },
};

/** Badges from the result's parsed attributes (resolution, HDR, codec, audio). */
export function sourceBadges(r: SourceResult): string[] {
  const a = r.attributes;
  const out: string[] = [];
  const res = a.resolution || a.quality;
  if (res) out.push(res.toUpperCase().replace('2160P', '4K'));
  if (a.hdr) out.push(a.hdr);
  else if (a.dolbyVision === 'true' || a.dv === 'true') out.push('DV');
  const codec = a.codec || a.videoCodec;
  if (codec) out.push(codec.toUpperCase());
  const audio = a.audio || a.audioCodec;
  if (audio) out.push(audio);
  return out.slice(0, 4);
}

/**
 * Manual source selection (native Views/Sources/SourcePickerView): library copies,
 * then the indexer results in the server's rank order with their debrid-cache /
 * usenet health; pending checks are long-polled via /indexers/source-health.
 */
export function SourcePickerDialog({
  profileId,
  target,
  open,
  onOpenChange,
  onPickSource,
  onPickVersion,
}: {
  profileId: string;
  target: SourcePickerTarget;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onPickSource: (r: SourceResult) => void;
  onPickVersion: (v: LibraryVersion) => void;
}) {
  const query = sourceQuery(
    target.name,
    target.year,
    target.episode ? { season: target.episode.season, episode: target.episode.episode } : undefined,
  );
  const results = useQuery({
    queryKey: [
      'sources',
      profileId,
      target.titleId,
      query,
      target.episode?.numbering?.ordering ?? '',
      target.qualityPreset ?? '',
    ],
    queryFn: ({ signal }) =>
      searchSources(
        profileId,
        {
          query,
          titleId: target.titleId,
          mediaType: target.mediaType,
          imdbId: target.imdbId,
          year: target.year,
          numbering: target.episode?.numbering,
          qualityPreset: target.qualityPreset,
        },
        signal,
      ),
    enabled: open,
    staleTime: 30 * 60_000,
  });

  const [health, setHealth] = useState<Record<number, SourceHealth>>({});
  const pendingToken = results.data?.find((r) => r.health?.state === 'pending' && r.health.token)
    ?.health?.token;

  useEffect(() => {
    if (!open || !pendingToken) return;
    const controller = new AbortController();
    (async () => {
      for (let i = 0; i < 6 && !controller.signal.aborted; i++) {
        try {
          const snap = await getSourceHealth(pendingToken, controller.signal);
          setHealth((h) => {
            const next = { ...h };
            for (const e of snap.results ?? []) next[e.index] = e.health;
            return next;
          });
          if (snap.complete) return;
        } catch {
          return;
        }
      }
    })();
    return () => controller.abort();
  }, [open, pendingToken]);

  const versions = target.versions ?? [];
  const title = target.episode
    ? `Choose source · S${String(target.episode.season).padStart(2, '0')}E${String(target.episode.episode).padStart(2, '0')}`
    : 'Choose source';

  return (
    <Dialog open={open} onOpenChange={onOpenChange} title={title} description={query} size="lg">
      <div className="-mx-2 max-h-[65vh] overflow-y-auto px-2">
        {versions.length > 0 && (
          <section className="mb-4">
            <h3 className="mb-2 text-xs font-semibold tracking-wide text-faint uppercase">
              In your library
            </h3>
            <ul className="space-y-1.5">
              {versions.map((v) => (
                <li key={v.item.id}>
                  <button
                    type="button"
                    onClick={() => onPickVersion(v)}
                    className="flex w-full cursor-pointer items-center gap-3 rounded-xl border border-line bg-surface px-3 py-2.5 text-left transition hover:bg-surface-strong focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
                  >
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-sm font-medium">{versionQuality(v)}</div>
                      <div className="truncate text-xs text-muted">
                        {versionOrigin(v)}
                        {v.item.fileName ? ` · ${v.item.fileName}` : ''}
                      </div>
                    </div>
                    {formatBytes(v.item.sizeBytes ?? 0) && (
                      <span className="shrink-0 text-xs text-muted">
                        {formatBytes(v.item.sizeBytes ?? 0)}
                      </span>
                    )}
                  </button>
                </li>
              ))}
            </ul>
          </section>
        )}
        <h3 className="mb-2 text-xs font-semibold tracking-wide text-faint uppercase">Streams</h3>
        {results.isPending && (
          <div className="space-y-1.5" aria-label="Loading sources">
            {Array.from({ length: 6 }, (_, i) => (
              <Skeleton key={i} className="h-14 w-full rounded-xl" />
            ))}
          </div>
        )}
        {results.isError && (
          <ErrorState
            title="Couldn’t search sources"
            error={results.error}
            onRetry={() => void results.refetch()}
            className="py-8"
          />
        )}
        {results.data && results.data.length === 0 && (
          <EmptyState
            icon="search"
            title="No sources found"
            description="The indexers returned nothing for this title."
            className="py-8"
          />
        )}
        {results.data && results.data.length > 0 && (
          <ul className="space-y-1.5">
            {results.data.map((r) => {
              const h = health[r.index] ?? r.health;
              const tag = h ? HEALTH[h.state] : undefined;
              const size = formatBytes(r.sizeBytes);
              return (
                <li key={`${r.index}-${r.id}`}>
                  <button
                    type="button"
                    onClick={() => onPickSource(r)}
                    className="flex w-full cursor-pointer items-start gap-3 rounded-xl border border-line bg-surface px-3 py-2.5 text-left transition hover:bg-surface-strong focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
                  >
                    <div className="min-w-0 flex-1">
                      <div className="line-clamp-2 text-sm font-medium break-all">{r.title}</div>
                      <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-muted">
                        {sourceBadges(r).map((b) => (
                          <span
                            key={b}
                            className="rounded-md bg-surface-strong px-1.5 py-0.5 font-medium text-fg"
                          >
                            {b}
                          </span>
                        ))}
                        <span>{[r.serviceType, r.indexer].filter(Boolean).join(' · ')}</span>
                      </div>
                    </div>
                    <div className="flex shrink-0 flex-col items-end gap-1 text-xs">
                      {size && <span className="text-muted">{size}</span>}
                      {tag && <span className={cn('font-medium', tag.tone)}>{tag.label}</span>}
                    </div>
                  </button>
                </li>
              );
            })}
          </ul>
        )}
      </div>
    </Dialog>
  );
}
