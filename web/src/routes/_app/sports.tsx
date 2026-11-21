import { useQuery } from '@tanstack/react-query';
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router';
import { useMemo, type ReactNode } from 'react';
import { addDays, fromDateKey } from '@/api/calendar';
import {
  defaultRaceSession,
  easternDateKey,
  getSportsCycling,
  getSportsLeagues,
  getSportsRaces,
  getSportsScoreboard,
  getSportsStatus,
  groupGames,
  matchesQuery,
  raceSessions,
  sportsKeys,
  type SportsCyclingRace,
  type SportsRaceEvent,
} from '@/api/sports';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { Icon } from '@/components/icons';
import { Page } from '@/components/Page';
import { Segmented } from '@/components/Segmented';
import { Skeleton } from '@/components/Spinner';
import { formatStart, GameCard, StatusPill } from '@/components/sports/SportsBits';
import { cn } from '@/lib/cn';

type SportsTab = 'games' | 'racing' | 'cycling';

interface SportsSearch {
  tab?: SportsTab;
  league?: string;
  date?: string;
  q?: string;
}

export const Route = createFileRoute('/_app/sports')({
  validateSearch: (raw: Record<string, unknown>): SportsSearch => ({
    tab: raw.tab === 'racing' || raw.tab === 'cycling' ? raw.tab : undefined,
    league: typeof raw.league === 'string' && raw.league ? raw.league : undefined,
    date:
      typeof raw.date === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(raw.date) ? raw.date : undefined,
    q: typeof raw.q === 'string' && raw.q ? raw.q : undefined,
  }),
  component: SportsPage,
});

function SportsPage() {
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const set = (patch: Partial<SportsSearch>) =>
    navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  const status = useQuery({
    queryKey: sportsKeys.status(),
    queryFn: ({ signal }) => getSportsStatus(signal),
    staleTime: 5 * 60_000,
  });

  if (status.data && !status.data.enabled) {
    return (
      <Page title="Sports">
        <EmptyState
          icon="trophy"
          title="Sports is turned off"
          description="An admin can enable the sports hub in the server settings."
        />
      </Page>
    );
  }

  return (
    <Page
      title="Sports"
      subtitle="Scores, schedules and where to watch."
      actions={
        <Segmented
          label="Section"
          value={search.tab ?? 'games'}
          onChange={(tab) =>
            navigate({ search: { tab: tab === 'games' ? undefined : tab }, replace: true })
          }
          options={[
            { value: 'games', label: 'Games' },
            { value: 'racing', label: 'Racing' },
            { value: 'cycling', label: 'Cycling' },
          ]}
        />
      }
    >
      {(search.tab ?? 'games') === 'games' && (
        <GamesTab search={search} set={set} dateQueries={status.data?.dateQueries ?? false} />
      )}
      {search.tab === 'racing' && <RacingTab />}
      {search.tab === 'cycling' && <CyclingTab />}
    </Page>
  );
}

function GamesTab({
  search,
  set,
  dateQueries,
}: {
  search: SportsSearch;
  set: (p: Partial<SportsSearch>) => void;
  dateQueries: boolean;
}) {
  const leagues = useQuery({
    queryKey: sportsKeys.leagues(),
    queryFn: ({ signal }) => getSportsLeagues(signal),
    staleTime: 10 * 60_000,
  });
  const board = useQuery({
    queryKey: sportsKeys.scoreboard(search.date),
    queryFn: ({ signal }) => getSportsScoreboard(search.date, signal),
    refetchInterval: 30_000,
  });
  const leagueName = useMemo(
    () => new Map((leagues.data ?? []).map((l) => [l.id, l.name ?? l.id.toUpperCase()])),
    [leagues.data],
  );
  const games = useMemo(
    () =>
      (board.data?.games ?? []).filter(
        (g) => (!search.league || g.league === search.league) && matchesQuery(g, search.q ?? ''),
      ),
    [board.data, search.league, search.q],
  );
  const groups = groupGames(games);
  const boardLeagues = useMemo(() => {
    const ids = new Set((board.data?.games ?? []).map((g) => g.league));
    return [...ids].sort((a, b) => (leagueName.get(a) ?? a).localeCompare(leagueName.get(b) ?? b));
  }, [board.data, leagueName]);
  const today = easternDateKey();
  const dates = [-2, -1, 0, 1, 2, 3, 4].map((n) => addDays(today, n));

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-col gap-3">
        {dateQueries && (
          <div className="flex gap-2 overflow-x-auto no-scrollbar" role="tablist" aria-label="Day">
            {dates.map((d) => {
              const active = (search.date ?? today) === d;
              const dt = fromDateKey(d);
              return (
                <button
                  key={d}
                  type="button"
                  role="tab"
                  aria-selected={active}
                  onClick={() => set({ date: d === today ? undefined : d })}
                  className={cn(
                    'flex min-w-16 shrink-0 cursor-pointer flex-col items-center rounded-xl border px-3 py-1.5 transition',
                    active
                      ? 'border-transparent accent-soft'
                      : 'border-line text-muted hover:bg-surface hover:text-fg',
                  )}
                >
                  <span className="text-[11px] font-semibold uppercase">
                    {d === today ? 'Today' : dt.toLocaleDateString(undefined, { weekday: 'short' })}
                  </span>
                  <span className="text-sm font-semibold">
                    {dt.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}
                  </span>
                </button>
              );
            })}
          </div>
        )}
        <div className="flex flex-wrap items-center gap-2">
          <div className="relative w-full sm:w-64">
            <Icon
              name="search"
              size={16}
              className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-faint"
            />
            <input
              type="search"
              aria-label="Filter teams"
              placeholder="Filter teams or leagues"
              defaultValue={search.q}
              onChange={(e) => set({ q: e.target.value || undefined })}
              className="h-9 w-full rounded-full border border-line bg-surface pr-3 pl-9 text-sm outline-none placeholder:text-faint focus:border-accent"
            />
          </div>
          <div className="flex max-w-full gap-1.5 overflow-x-auto no-scrollbar">
            <Chip active={!search.league} onClick={() => set({ league: undefined })}>
              All leagues
            </Chip>
            {boardLeagues.map((id) => (
              <Chip key={id} active={search.league === id} onClick={() => set({ league: id })}>
                {leagueName.get(id) ?? id.toUpperCase()}
              </Chip>
            ))}
          </div>
        </div>
      </div>

      {board.isError ? (
        <ErrorState
          title="Couldn't load the scoreboard"
          error={board.error}
          onRetry={() => board.refetch()}
        />
      ) : board.isLoading ? (
        <GridSkeleton />
      ) : games.length === 0 ? (
        <EmptyState
          icon="trophy"
          title="No games"
          description={
            search.league || search.q
              ? 'Nothing matches these filters.'
              : 'There is nothing on the board for this day.'
          }
        />
      ) : (
        <>
          {board.data?.stale && (
            <p className="text-xs text-warning">
              Updates delayed — showing the last scores the server had.
            </p>
          )}
          <GameSection title="Live now" count={groups.live.length} accent>
            {groups.live.map((g) => (
              <GameCard key={g.id} game={g} leagueName={leagueName.get(g.league)} />
            ))}
          </GameSection>
          <GameSection title="Upcoming" count={groups.upcoming.length}>
            {groups.upcoming.map((g) => (
              <GameCard key={g.id} game={g} leagueName={leagueName.get(g.league)} />
            ))}
          </GameSection>
          <GameSection title="Results" count={groups.results.length}>
            {groups.results.map((g) => (
              <GameCard key={g.id} game={g} leagueName={leagueName.get(g.league)} />
            ))}
          </GameSection>
        </>
      )}
    </div>
  );
}

function Chip({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        'h-9 shrink-0 cursor-pointer rounded-full border px-3.5 text-sm whitespace-nowrap transition',
        active
          ? 'border-transparent accent-soft font-medium'
          : 'border-line text-muted hover:bg-surface hover:text-fg',
      )}
    >
      {children}
    </button>
  );
}

function GameSection({
  title,
  count,
  accent,
  children,
}: {
  title: string;
  count: number;
  accent?: boolean;
  children: ReactNode;
}) {
  if (count === 0) return null;
  return (
    <section aria-label={title}>
      <h2 className="mb-3 flex items-center gap-2 text-lg font-semibold">
        {accent && <span className="h-2 w-2 animate-pulse rounded-full bg-danger" />}
        {title}
        <span className="text-sm font-normal text-faint">{count}</span>
      </h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">{children}</div>
    </section>
  );
}

function GridSkeleton() {
  return (
    <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
      {Array.from({ length: 8 }, (_, i) => (
        <Skeleton key={i} className="h-36 rounded-2xl" />
      ))}
    </div>
  );
}

const SERIES_LABEL: Record<string, string> = {
  f1: 'Formula 1',
  motogp: 'MotoGP',
  nascar: 'NASCAR',
  indycar: 'IndyCar',
};

function RacingTab() {
  const board = useQuery({
    queryKey: sportsKeys.races(),
    queryFn: ({ signal }) => getSportsRaces(signal),
    refetchInterval: 60_000,
  });
  if (board.isError)
    return (
      <ErrorState
        title="Couldn't load race weekends"
        error={board.error}
        onRetry={() => board.refetch()}
      />
    );
  if (board.isLoading) return <GridSkeleton />;
  const events = [...(board.data?.events ?? [])].sort(
    (a, b) => (Date.parse(a.startTime) || 0) - (Date.parse(b.startTime) || 0),
  );
  if (events.length === 0)
    return (
      <EmptyState
        icon="trophy"
        title="No race weekends"
        description="No series has a race weekend in the current window."
      />
    );
  const now = Date.now();
  const firstUpcoming = events.findIndex(
    (e) => e.status === 'live' || (Date.parse(lastSessionStart(e)) || 0) >= now - 6 * 3600_000,
  );
  const ordered =
    firstUpcoming > 0
      ? [...events.slice(firstUpcoming), ...events.slice(0, firstUpcoming).reverse()]
      : events;
  return (
    <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
      {board.data?.leagues?.some((l) => l.stale || l.unavailable) && (
        <p className="text-xs text-warning md:col-span-2 2xl:col-span-3">
          Some series feeds are delayed.
        </p>
      )}
      {ordered.map((e) => (
        <RaceCard key={e.id} event={e} />
      ))}
    </div>
  );
}

const lastSessionStart = (e: SportsRaceEvent) => raceSessions(e).at(-1)?.startTime ?? e.startTime;

function RaceCard({ event }: { event: SportsRaceEvent }) {
  const session = defaultRaceSession(event);
  const sessions = raceSessions(event);
  const podium = (session.participants ?? [])
    .filter((p) => p.position && p.position <= 3)
    .sort((a, b) => a.position! - b.position!);
  return (
    <Link
      to="/sports/race/$eventId"
      params={{ eventId: event.id }}
      className="flex flex-col gap-3 rounded-2xl border border-line bg-surface p-4 transition hover:-translate-y-0.5 hover:border-line-strong hover:bg-surface-strong hover:shadow-card"
    >
      <div className="flex items-center justify-between gap-2">
        <span className="text-xs font-semibold tracking-wide text-faint uppercase">
          {SERIES_LABEL[event.league] ?? event.league.toUpperCase()}
        </span>
        <StatusPill status={session.status} detail={session.statusDetail} />
      </div>
      <div>
        <p className="font-semibold">{event.title}</p>
        <p className="truncate text-xs text-muted">{event.venue || event.circuit?.name || ''}</p>
      </div>
      {podium.length > 0 ? (
        <ol className="flex flex-col gap-1 text-sm">
          {podium.map((p) => (
            <li key={p.id} className="flex items-center gap-2">
              <span className="w-5 text-xs font-semibold text-faint tabular-nums">
                P{p.position}
              </span>
              <span className="truncate">{p.name}</span>
              {p.team && <span className="ml-auto truncate text-xs text-faint">{p.team}</span>}
            </li>
          ))}
        </ol>
      ) : (
        <p className="text-xs text-muted">
          {sessions.length > 1 ? `${sessions.length} sessions · ` : ''}
          {session.sessionType ?? 'Race'}{' '}
          {formatStart(session.startTime, { month: 'short', day: 'numeric' })}
        </p>
      )}
    </Link>
  );
}

function CyclingTab() {
  const feed = useQuery({
    queryKey: sportsKeys.cycling(),
    queryFn: ({ signal }) => getSportsCycling(signal),
    staleTime: 5 * 60_000,
  });
  if (feed.isError)
    return (
      <ErrorState title="Couldn't load cycling" error={feed.error} onRetry={() => feed.refetch()} />
    );
  if (feed.isLoading) return <GridSkeleton />;
  const races = feed.data?.data ?? [];
  if (races.length === 0)
    return (
      <EmptyState
        icon="trophy"
        title="No races"
        description={feed.data?.reason || 'The organizers have no races in the current window.'}
      />
    );
  return (
    <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-3">
      {races.map((r) => (
        <CyclingCard key={r.id} race={r} />
      ))}
    </div>
  );
}

function CyclingCard({ race }: { race: SportsCyclingRace }) {
  const stages = race.stages ?? [];
  const fmt = (d?: string) =>
    d ? fromDateKey(d).toLocaleDateString(undefined, { month: 'short', day: 'numeric' }) : '';
  return (
    <Link
      to="/sports/cycling/$raceId"
      params={{ raceId: race.id }}
      className="flex flex-col gap-2 rounded-2xl border border-line bg-surface p-4 transition hover:-translate-y-0.5 hover:border-line-strong hover:bg-surface-strong hover:shadow-card"
    >
      <span className="text-xs font-semibold tracking-wide text-faint uppercase">
        {race.classification ?? race.category ?? 'Cycling'}
      </span>
      <p className="font-semibold">{race.name}</p>
      <p className="text-xs text-muted">
        {[
          race.country,
          race.startDate &&
            `${fmt(race.startDate)}${race.endDate && race.endDate !== race.startDate ? ` – ${fmt(race.endDate)}` : ''}`,
          stages.length > 1 && `${stages.length} stages`,
        ]
          .filter(Boolean)
          .join(' · ')}
      </p>
    </Link>
  );
}
