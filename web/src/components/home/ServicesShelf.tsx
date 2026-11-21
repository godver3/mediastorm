import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import {
  getDisplayList,
  homeKeys,
  titleCard,
  type HomeSettings,
  type StreamingService,
  type TrendingItem,
} from '@/api/home';
import { cn } from '@/lib/cn';
import { artworkUrl } from '@/lib/artwork';
import { Shelf } from '../Shelf';
import { ShelfCards, ShelfSkeleton } from './HomeShelfRow';

/**
 * Streaming Services: the service logos as a tab strip, and the picked service's
 * top list (MDBList via display-list) below. Native opens a list page per service;
 * on the web the row expands in place.
 */
export function ServicesShelf({
  title,
  services,
  settings,
  profileId,
  cap,
}: {
  title: string;
  services: StreamingService[];
  settings: HomeSettings;
  profileId: string;
  cap: number;
}) {
  const [serviceId, setServiceId] = useState(services[0]?.id);
  const [part, setPart] = useState(0);
  const service = services.find((s) => s.id === serviceId) ?? services[0];
  const list = service?.lists[Math.min(part, (service?.lists.length ?? 1) - 1)];
  const query: Record<string, string> = {
    source: 'mdblist',
    name: service?.name ?? '',
    url: list?.url ?? '',
    limit: String(cap),
    offset: '0',
    ...(settings.hideWatched ? { hideWatched: 'true' } : {}),
  };
  const titles = useQuery({
    queryKey: homeKeys.shelf(profileId, `service-${service?.id}`, query),
    queryFn: async ({ signal }) =>
      (await getDisplayList<TrendingItem>(profileId, query, signal)).items
        .filter((i) => i?.title)
        .map((i, idx) => titleCard(i.title, idx + 1)),
    enabled: !!list,
    staleTime: 10 * 60_000,
  });
  if (!service) return null;

  return (
    <section className="py-3">
      <div className="mb-3 flex flex-wrap items-center justify-between gap-3 px-4 sm:px-6 lg:px-10">
        <h2 className="text-lg font-semibold tracking-tight">{title}</h2>
        {service.lists.length > 1 && (
          <div
            role="tablist"
            aria-label={`${service.name} lists`}
            className="flex gap-1 rounded-xl bg-surface p-1"
          >
            {service.lists.map((l, i) => (
              <button
                key={l.key ?? l.url}
                role="tab"
                aria-selected={i === part}
                onClick={() => setPart(i)}
                className={cn(
                  'rounded-lg px-3 py-1 text-sm transition',
                  i === part ? 'accent-soft text-fg' : 'text-muted hover:text-fg',
                )}
              >
                {l.title ?? `List ${i + 1}`}
              </button>
            ))}
          </div>
        )}
      </div>
      <div
        role="tablist"
        aria-label="Streaming services"
        className="no-scrollbar flex gap-3 overflow-x-auto px-4 pb-1 sm:px-6 lg:px-10"
      >
        {services.map((s) => (
          <button
            key={s.id}
            role="tab"
            aria-selected={s.id === service.id}
            aria-label={s.name}
            title={s.name}
            onClick={() => {
              setServiceId(s.id);
              setPart(0);
            }}
            className={cn(
              'flex h-16 w-32 shrink-0 items-center justify-center rounded-card border px-4 transition',
              'bg-[#0d0d12] hover:border-line-strong focus-visible:outline-2 focus-visible:outline-accent',
              s.id === service.id
                ? 'border-accent shadow-[0_0_0_1px_var(--accent)]'
                : 'border-line opacity-70 hover:opacity-100',
            )}
          >
            {s.logoUrl ? (
              <img
                src={artworkUrl(s.logoUrl, 'w300')}
                alt=""
                className="max-h-9 max-w-full object-contain"
              />
            ) : (
              <span className="text-sm font-semibold text-white">{s.name}</span>
            )}
          </button>
        ))}
      </div>
      {titles.isPending ? (
        <ShelfSkeleton title="" landscape={false} />
      ) : titles.data && titles.data.length > 0 ? (
        <Shelf
          title={<span className="sr-only">{`${service.name} ${list?.title ?? ''}`}</span>}
          className="pt-0"
        >
          <ShelfCards cards={titles.data} landscape={false} />
        </Shelf>
      ) : (
        <p className="px-4 py-6 text-sm text-muted sm:px-6 lg:px-10">
          {titles.error ? 'Couldn’t load this list.' : 'Nothing on this list right now.'}
        </p>
      )}
    </section>
  );
}
