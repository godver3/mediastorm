import { useQuery } from '@tanstack/react-query';
import { createFileRoute, useNavigate } from '@tanstack/react-router';
import {
  defaultRaceSession,
  getSportsRaces,
  getSportsRaceSession,
  raceSessions,
  sportsKeys,
  type SportsRaceDriver,
} from '@/api/sports';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { Page } from '@/components/Page';
import { Skeleton } from '@/components/Spinner';
import { BackToSports, formatStart, StatusPill } from '@/components/sports/SportsBits';
import { WatchPanel } from '@/components/sports/WatchPanel';
import { cn } from '@/lib/cn';

export const Route = createFileRoute('/_app/sports_/race/$eventId')({
  validateSearch: (raw: Record<string, unknown>): { session?: string } => ({
    session: typeof raw.session === 'string' && raw.session ? raw.session : undefined,
  }),
  component: RacePage,
});

function RacePage() {
  const { eventId } = Route.useParams();
  const { session: sessionParam } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const board = useQuery({
    queryKey: sportsKeys.races(),
    queryFn: ({ signal }) => getSportsRaces(signal),
    refetchInterval: 60_000,
  });
  const event = board.data?.events.find((e) => e.id === eventId);
  const sessions = event ? raceSessions(event) : [];
  const chosen =
    sessions.find((s) => s.id === sessionParam) ?? (event ? defaultRaceSession(event) : undefined);

  const snapshot = useQuery({
    queryKey: sportsKeys.raceSession(
      chosen?.league ?? '',
      chosen?.providerEventId ?? '',
      chosen?.sessionId ?? '',
    ),
    queryFn: ({ signal }) =>
      getSportsRaceSession(chosen!.league, chosen!.providerEventId, chosen!.sessionId!, signal),
    enabled: !!chosen?.sessionId,
    refetchInterval: chosen?.status === 'live' ? 15_000 : false,
  });
  const session = snapshot.data ?? chosen;

  if (board.isError)
    return (
      <Page>
        <BackToSports tab="racing" />
        <ErrorState error={board.error} onRetry={() => board.refetch()} />
      </Page>
    );
  if (board.isLoading)
    return (
      <Page>
        <BackToSports tab="racing" />
        <Skeleton className="mt-6 h-72 rounded-2xl" />
      </Page>
    );
  if (!event || !session)
    return (
      <Page>
        <BackToSports tab="racing" />
        <EmptyState
          icon="trophy"
          title="Race weekend not found"
          description="It may have dropped out of the current window."
        />
      </Page>
    );

  const drivers = [...(session.participants ?? [])].sort(
    (a, b) => (a.position ?? 999) - (b.position ?? 999),
  );
  const statCols = drivers[0]?.statistics?.slice(0, 3) ?? [];

  return (
    <Page>
      <BackToSports tab="racing" />
      <div className="mt-4 grid gap-6 lg:grid-cols-[minmax(0,1fr)_360px]">
        <div className="flex min-w-0 flex-col gap-6">
          <section className="rounded-2xl border border-line bg-[linear-gradient(135deg,color-mix(in_srgb,var(--accent)_22%,transparent),transparent_70%)] p-6">
            <p className="text-xs font-semibold tracking-wide text-faint uppercase">
              {event.league}
            </p>
            <h1 className="mt-1 text-2xl font-semibold sm:text-3xl">{event.title}</h1>
            <p className="mt-1 text-sm text-muted">{event.venue || event.circuit?.name}</p>
          </section>
          {sessions.length > 1 && (
            <div
              className="flex gap-2 overflow-x-auto no-scrollbar"
              role="tablist"
              aria-label="Session"
            >
              {sessions.map((s) => {
                const active = s.id === chosen?.id;
                return (
                  <button
                    key={s.id}
                    type="button"
                    role="tab"
                    aria-selected={active}
                    onClick={() => navigate({ search: { session: s.id }, replace: true })}
                    className={cn(
                      'flex shrink-0 cursor-pointer flex-col items-start rounded-xl border px-3 py-2 text-left transition',
                      active
                        ? 'border-transparent accent-soft'
                        : 'border-line text-muted hover:bg-surface hover:text-fg',
                    )}
                  >
                    <span className="text-sm font-semibold">{s.sessionType ?? s.title}</span>
                    <span className="text-[11px]">
                      {formatStart(s.startTime, { month: 'short', day: 'numeric' })}
                    </span>
                  </button>
                );
              })}
            </div>
          )}
          <section className="overflow-x-auto rounded-2xl border border-line bg-surface p-5">
            <div className="mb-3 flex items-center justify-between gap-2">
              <h2 className="text-base font-semibold">
                {session.sessionType ?? 'Session'} classification
              </h2>
              <StatusPill status={session.status} detail={session.statusDetail} />
            </div>
            {snapshot.isLoading ? (
              <Skeleton className="h-40 rounded-xl" />
            ) : drivers.length === 0 ? (
              <p className="text-sm text-muted">
                {session.status === 'scheduled'
                  ? `Starts ${formatStart(session.startTime, { month: 'short', day: 'numeric' })}.`
                  : 'No classification yet.'}
              </p>
            ) : (
              <table className="w-full text-sm">
                <thead>
                  <tr className="text-left text-xs text-faint">
                    <th className="py-1 pr-2 font-semibold">Pos</th>
                    <th className="py-1 pr-2 font-semibold">
                      {event.league === 'motogp' ? 'Rider' : 'Driver'}
                    </th>
                    <th className="hidden py-1 pr-2 font-semibold sm:table-cell">Team</th>
                    {statCols.map((c) => (
                      <th key={c.name} className="py-1 pr-2 text-right font-semibold">
                        {c.label}
                      </th>
                    ))}
                  </tr>
                </thead>
                <tbody>
                  {drivers.map((d) => (
                    <DriverRow key={d.id} driver={d} cols={statCols.map((c) => c.name)} />
                  ))}
                </tbody>
              </table>
            )}
          </section>
        </div>
        <div className="lg:sticky lg:top-20 lg:self-start">
          <WatchPanel
            eventId={session.id}
            title={`${event.title} · ${session.sessionType ?? ''}`.trim()}
            kind="racing"
            parentId={event.id}
          />
        </div>
      </div>
    </Page>
  );
}

function DriverRow({ driver, cols }: { driver: SportsRaceDriver; cols: string[] }) {
  const outcome = driver.result && driver.result !== 'Classified' ? driver.result : undefined;
  return (
    <tr className="border-t border-line">
      <td className="py-2 pr-2 font-semibold tabular-nums">{driver.position ?? '–'}</td>
      <td className="py-2 pr-2">
        <span className={cn(driver.winner && 'font-semibold')}>{driver.name}</span>
        {driver.number && <span className="ml-2 text-xs text-faint">#{driver.number}</span>}
        {outcome && <span className="ml-2 text-xs text-danger">{outcome}</span>}
      </td>
      <td className="hidden py-2 pr-2 text-muted sm:table-cell">{driver.team}</td>
      {cols.map((c) => (
        <td key={c} className="py-2 pr-2 text-right text-muted tabular-nums">
          {driver.statistics?.find((s) => s.name === c)?.value ?? ''}
        </td>
      ))}
    </tr>
  );
}
