import { useQuery } from '@tanstack/react-query';
import { Link } from '@tanstack/react-router';
import {
  calendarItemKey,
  calendarItemTitle,
  calendarKeys,
  formatAirTime,
  fromDateKey,
  getCalendarEnrichment,
  itemSubtitle,
  type CalendarItem,
} from '@/api/calendar';
import { Icon } from '@/components/icons';
import { Skeleton } from '@/components/Spinner';
import { cn } from '@/lib/cn';
import { detailsLink } from '@/lib/links';

/** Detail panel for the selected release, enriched from /metadata/{movies|series}/details. */
export function CalendarDetail({
  profileId,
  item,
  className,
}: {
  profileId: string;
  item: CalendarItem;
  className?: string;
}) {
  const enrichment = useQuery({
    queryKey: calendarKeys.enrichment(profileId, calendarItemKey(item)),
    queryFn: ({ signal }) => getCalendarEnrichment(profileId, item, signal),
    staleTime: 30 * 60_000,
    retry: false,
  });
  const e = enrichment.data;
  const backdrop = item.backdropUrl || e?.backdropUrl || item.backdropUrls?.[0];
  const time = formatAirTime(item.airTime);
  const date = fromDateKey(item.airDate).toLocaleDateString(undefined, {
    weekday: 'long',
    month: 'long',
    day: 'numeric',
  });
  const synopsis = item.episodeOverview || e?.synopsis || item.overview;
  const facts = [
    e?.year ?? item.year,
    e?.certification,
    (e?.runtimeMinutes ?? item.runtimeMinutes)
      ? `${e?.runtimeMinutes ?? item.runtimeMinutes} min`
      : null,
    item.network,
  ].filter(Boolean);
  const genres = e?.genres.length ? e.genres : (item.genres ?? []);

  return (
    <aside
      aria-label="Release details"
      className={cn(
        'overflow-hidden rounded-2xl border border-line bg-elevated shadow-card',
        className,
      )}
    >
      <div className="relative aspect-video w-full bg-surface">
        {backdrop ? (
          <img src={backdrop} alt="" className="h-full w-full object-cover" />
        ) : (
          <div className="h-full w-full bg-[linear-gradient(135deg,color-mix(in_srgb,var(--accent)_35%,transparent),transparent)]" />
        )}
        <div className="absolute inset-0 bg-gradient-to-t from-elevated via-elevated/30 to-transparent" />
        {item.logo?.url ? (
          <img
            src={item.logo.url}
            alt={item.title}
            className="absolute bottom-3 left-5 max-h-14 max-w-[60%] object-contain drop-shadow"
          />
        ) : null}
      </div>
      <div className="space-y-4 p-5">
        <div>
          <p className="text-xs font-semibold tracking-wide text-accent uppercase">
            {date}
            {time ? ` · ${time}` : ''}
          </p>
          <h2 className="mt-1 text-xl font-semibold">{item.title}</h2>
          <p className="text-sm text-muted">{itemSubtitle(item)}</p>
        </div>
        {facts.length > 0 && (
          <p className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted">
            {facts.map((f) => (
              <span key={String(f)}>{f}</span>
            ))}
          </p>
        )}
        {enrichment.isLoading && !synopsis ? (
          <div className="space-y-2">
            <Skeleton className="h-3 w-full" />
            <Skeleton className="h-3 w-5/6" />
            <Skeleton className="h-3 w-2/3" />
          </div>
        ) : synopsis ? (
          <p className="text-sm leading-relaxed text-fg/90 line-clamp-6">{synopsis}</p>
        ) : null}
        {genres.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {genres.slice(0, 5).map((g) => (
              <span
                key={g}
                className="rounded-full border border-line px-2.5 py-0.5 text-xs text-muted"
              >
                {g}
              </span>
            ))}
          </div>
        )}
        {e && e.cast.length > 0 && (
          <p className="text-xs text-muted">
            <span className="font-medium text-fg">Cast </span>
            {e.cast.join(', ')}
          </p>
        )}
        <Link
          {...detailsLink(
            calendarItemTitle(item),
            item.mediaType === 'series' && item.seasonNumber && item.episodeNumber
              ? { season: item.seasonNumber, episode: item.episodeNumber }
              : undefined,
          )}
          className="inline-flex h-10 items-center gap-2 rounded-xl bg-accent px-4 text-sm font-medium text-accent-contrast transition hover:brightness-110"
        >
          View details
          <Icon name="chevronRight" size={16} />
        </Link>
      </div>
    </aside>
  );
}
