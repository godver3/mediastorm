import { useDocumentTitle } from '@/lib/documentTitle';
import { useQuery } from '@tanstack/react-query';
import { createFileRoute, useNavigate } from '@tanstack/react-router';
import { fromDateKey, toDateKey } from '@/api/calendar';
import {
  cyclingRows,
  getSportsCycling,
  getSportsCyclingStage,
  sportsKeys,
  type SportsCyclingResults,
  type SportsCyclingStage,
} from '@/api/sports';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { Page } from '@/components/Page';
import { Skeleton } from '@/components/Spinner';
import { BackToSports } from '@/components/sports/SportsBits';
import { WatchPanel } from '@/components/sports/WatchPanel';
import { cn } from '@/lib/cn';

export const Route = createFileRoute('/_app/sports_/cycling/$raceId')({
  validateSearch: (raw: Record<string, unknown>): { stage?: string } => ({
    stage: typeof raw.stage === 'string' && raw.stage ? raw.stage : undefined,
  }),
  component: CyclingPage,
});

/** Today's stage, else the next one, else the last. */
function defaultStage(stages: SportsCyclingStage[]): SportsCyclingStage | undefined {
  const today = toDateKey(new Date());
  return (
    stages.find((s) => s.date === today) ??
    stages.find((s) => (s.date ?? '') > today) ??
    stages[stages.length - 1]
  );
}

function CyclingPage() {
  const { raceId } = Route.useParams();
  const { stage: stageParam } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const feed = useQuery({
    queryKey: sportsKeys.cycling(),
    queryFn: ({ signal }) => getSportsCycling(signal),
    staleTime: 5 * 60_000,
  });
  const race = feed.data?.data?.find((r) => r.id === raceId);
  useDocumentTitle(race?.name ?? 'Race');
  const stages = race?.stages ?? [];
  const chosen = stages.find((s) => s.id === stageParam) ?? defaultStage(stages);
  const stage = useQuery({
    queryKey: sportsKeys.cyclingStage(chosen?.id ?? ''),
    queryFn: ({ signal }) => getSportsCyclingStage(chosen!.id, signal),
    enabled: !!chosen,
    staleTime: 60_000,
  });
  const s = stage.data ?? chosen;

  if (feed.isError)
    return (
      <Page>
        <BackToSports tab="cycling" />
        <ErrorState error={feed.error} onRetry={() => feed.refetch()} />
      </Page>
    );
  if (feed.isLoading)
    return (
      <Page>
        <BackToSports tab="cycling" />
        <Skeleton className="mt-6 h-72 rounded-2xl" />
      </Page>
    );
  if (!race)
    return (
      <Page>
        <BackToSports tab="cycling" />
        <EmptyState icon="trophy" title="Race not found" />
      </Page>
    );

  const fmt = (d?: string) =>
    d
      ? fromDateKey(d).toLocaleDateString(undefined, {
          weekday: 'short',
          month: 'short',
          day: 'numeric',
        })
      : '';

  return (
    <Page>
      <BackToSports tab="cycling" />
      <div className="mt-4 grid gap-6 lg:grid-cols-[minmax(0,1fr)_360px]">
        <div className="flex min-w-0 flex-col gap-6">
          <section className="rounded-2xl border border-line bg-[linear-gradient(135deg,color-mix(in_srgb,var(--warning)_22%,transparent),transparent_70%)] p-6">
            <p className="text-xs font-semibold tracking-wide text-faint uppercase">
              {race.classification ?? race.category ?? 'Cycling'}
            </p>
            <h1 className="mt-1 text-2xl font-semibold sm:text-3xl">{race.name}</h1>
            <p className="mt-1 text-sm text-muted">
              {[race.country, race.startDate && `${fmt(race.startDate)} – ${fmt(race.endDate)}`]
                .filter(Boolean)
                .join(' · ')}
            </p>
          </section>
          {stages.length > 1 && (
            <div
              className="flex gap-2 overflow-x-auto pb-1 no-scrollbar"
              role="tablist"
              aria-label="Stage"
            >
              {stages.map((st, i) => {
                const active = st.id === chosen?.id;
                return (
                  <button
                    key={st.id}
                    type="button"
                    role="tab"
                    aria-selected={active}
                    onClick={() => navigate({ search: { stage: st.id }, replace: true })}
                    className={cn(
                      'flex min-w-20 shrink-0 cursor-pointer flex-col items-start rounded-xl border px-3 py-2 text-left transition',
                      active
                        ? 'border-transparent accent-soft'
                        : 'border-line text-muted hover:bg-surface hover:text-fg',
                      st.status === 'cancelled' && 'line-through opacity-60',
                    )}
                  >
                    <span className="text-sm font-semibold">Stage {i + 1}</span>
                    <span className="text-[11px]">{fmt(st.date)}</span>
                  </button>
                );
              })}
            </div>
          )}
          {s && (
            <section className="rounded-2xl border border-line bg-surface p-5">
              <h2 className="text-lg font-semibold">{s.name}</h2>
              <p className="mt-1 text-sm text-muted">
                {[
                  fmt(s.date),
                  s.departure && s.arrival ? `${s.departure} → ${s.arrival}` : null,
                  s.distance,
                  s.terrain,
                ]
                  .filter(Boolean)
                  .join(' · ')}
              </p>
              {s.routeGuide?.state === 'available' &&
                (s.routeGuide.profileUrl || s.routeGuide.mapUrl) && (
                  <div className="mt-4 grid gap-3 sm:grid-cols-2">
                    {s.routeGuide.profileUrl && (
                      <img
                        src={s.routeGuide.profileUrl}
                        alt="Stage profile"
                        className="w-full rounded-xl bg-white object-contain"
                      />
                    )}
                    {s.routeGuide.mapUrl && (
                      <img
                        src={s.routeGuide.mapUrl}
                        alt="Route map"
                        className="w-full rounded-xl bg-white object-contain"
                      />
                    )}
                  </div>
                )}
            </section>
          )}
          {stage.isLoading ? (
            <Skeleton className="h-48 rounded-2xl" />
          ) : (
            <div className="grid gap-6 xl:grid-cols-2">
              <ResultsTable title="Stage result" results={s?.results} />
              <ResultsTable
                title={s?.generalClassificationLabel ?? 'General classification'}
                results={s?.generalClassification}
              />
            </div>
          )}
        </div>
        <div className="lg:sticky lg:top-20 lg:self-start">
          {chosen && (
            <WatchPanel
              eventId={chosen.id}
              title={`${race.name} · ${chosen.name}`}
              kind="cycling"
              parentId={race.id}
            />
          )}
        </div>
      </div>
    </Page>
  );
}

function ResultsTable({ title, results }: { title: string; results?: SportsCyclingResults }) {
  const rows = cyclingRows(results).slice(0, 25);
  return (
    <section className="overflow-x-auto rounded-2xl border border-line bg-surface p-5">
      <h2 className="mb-3 text-base font-semibold">{title}</h2>
      {rows.length === 0 ? (
        <p className="text-sm text-muted">{results?.reason || 'Not available yet.'}</p>
      ) : (
        <table className="w-full text-sm">
          <tbody>
            {rows.map((r) => (
              <tr key={r.id} className="border-t border-line first:border-t-0">
                <td className="w-8 py-2 pr-2 font-semibold tabular-nums">
                  {r.rank ?? r.result ?? '–'}
                </td>
                <td className="py-2 pr-2">
                  <span className="block font-medium">{r.name}</span>
                  {r.team && <span className="block text-xs text-faint">{r.team}</span>}
                </td>
                <td className="py-2 text-right text-muted tabular-nums">
                  {r.rank === 1 ? r.time : r.gap || r.time}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
