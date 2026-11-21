import { useDocumentTitle } from '@/lib/documentTitle';
import { useQuery } from '@tanstack/react-query';
import { createFileRoute } from '@tanstack/react-router';
import {
  gameTitle,
  getSportsGame,
  isMatchup,
  lifecycle,
  showsScore,
  sportsKeys,
  type SportsGame,
  type SportsTeam,
} from '@/api/sports';
import { ErrorState } from '@/components/EmptyState';
import { Page } from '@/components/Page';
import { Skeleton } from '@/components/Spinner';
import {
  BackToSports,
  formatStart,
  gameStatusText,
  StatusPill,
  TeamLogo,
} from '@/components/sports/SportsBits';
import {
  BoxScore,
  FollowTeams,
  Lineups,
  PlaysFeed,
  WinProbability,
} from '@/components/sports/GameExtras';
import { WatchPanel } from '@/components/sports/WatchPanel';
import { cn } from '@/lib/cn';

export const Route = createFileRoute('/_app/sports_/game/$gameId')({
  component: GamePage,
});

function GamePage() {
  const { gameId } = Route.useParams();
  const game = useQuery({
    queryKey: sportsKeys.game(gameId),
    queryFn: ({ signal }) => getSportsGame(gameId, signal),
    refetchInterval: (q) =>
      q.state.data && lifecycle(q.state.data.status) === 'live' ? 20_000 : false,
  });

  const g = game.data;
  useDocumentTitle(
    g
      ? `${g.awayTeam.abbreviation ?? g.awayTeam.name} at ${g.homeTeam.abbreviation ?? g.homeTeam.name}`
      : 'Game',
  );

  return (
    <Page>
      <BackToSports />
      {game.isError ? (
        <ErrorState
          title="Couldn't load this event"
          error={game.error}
          onRetry={() => game.refetch()}
        />
      ) : !game.data ? (
        <div className="mt-6 grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_360px]">
          <Skeleton className="h-56 rounded-2xl" />
          <Skeleton className="h-56 rounded-2xl" />
        </div>
      ) : (
        <GameBody game={game.data} />
      )}
    </Page>
  );
}

function GameBody({ game }: { game: SportsGame }) {
  const d = game.detail;
  const title = gameTitle(game);
  return (
    <div className="mt-4 grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_360px]">
      <div className="flex min-w-0 flex-col gap-6">
        <Scoreboard game={game} />
        <FollowTeams game={game} />
        {d?.periods && d.periods.length > 0 && <LineScore game={game} />}
        {d?.comparisons && d.comparisons.length > 0 && (
          <Card title="Team stats">
            <div className="mb-2 flex justify-between text-xs font-semibold text-faint uppercase">
              <span>{game.awayTeam.abbreviation ?? game.awayTeam.name}</span>
              <span>{game.homeTeam.abbreviation ?? game.homeTeam.name}</span>
            </div>
            <ul className="divide-y divide-line">
              {d.comparisons.map((c) => (
                <li
                  key={c.label}
                  className="grid grid-cols-[1fr_auto_1fr] items-center gap-3 py-2 text-sm"
                >
                  <span className="font-semibold tabular-nums">{c.away}</span>
                  <span className="text-center text-xs text-muted">{c.label}</span>
                  <span className="text-right font-semibold tabular-nums">{c.home}</span>
                </li>
              ))}
            </ul>
          </Card>
        )}
        {d?.leaders && d.leaders.length > 0 && (
          <Card title="Leaders">
            <ul className="grid gap-3 sm:grid-cols-2">
              {d.leaders.map((l) => (
                <li
                  key={`${l.label}-${l.name}`}
                  className="flex items-center gap-3 rounded-xl bg-elevated p-3"
                >
                  {l.headshotUrl ? (
                    <img
                      src={l.headshotUrl}
                      alt=""
                      className="h-10 w-10 rounded-full bg-surface-strong object-cover"
                    />
                  ) : (
                    <span className="h-10 w-10 rounded-full bg-surface-strong" />
                  )}
                  <span className="min-w-0">
                    <span className="block text-xs text-faint">
                      {l.label} · {l.teamAbbreviation}
                    </span>
                    <span className="block truncate text-sm font-medium">{l.name}</span>
                    <span className="block text-xs text-muted">{l.value}</span>
                  </span>
                </li>
              ))}
            </ul>
          </Card>
        )}
        {d?.winProbability && d.winProbability.length > 1 && (
          <WinProbability game={game} points={d.winProbability} />
        )}
        {d?.plays && d.plays.length > 0 && <PlaysFeed game={game} plays={d.plays} />}
        {d?.playerStats && d.playerStats.length > 0 && (
          <BoxScore game={game} stats={d.playerStats} />
        )}
        {d?.lineups && d.lineups.some((l) => l.players?.length) && (
          <Lineups game={game} lineups={d.lineups.filter((l) => l.players?.length)} />
        )}
        {!isMatchup(game) && game.participants && game.participants.length > 0 && (
          <Card title="Field">
            <ol className="divide-y divide-line">
              {[...game.participants]
                .sort((a, b) => (a.position ?? 999) - (b.position ?? 999))
                .slice(0, 40)
                .map((p) => (
                  <li key={p.id} className="flex items-center gap-3 py-2 text-sm">
                    <span className="w-8 text-xs text-faint tabular-nums">{p.position ?? ''}</span>
                    <span className={cn('flex-1 truncate', p.winner && 'font-semibold')}>
                      {p.name}
                    </span>
                    <span className="tabular-nums">{p.score}</span>
                  </li>
                ))}
            </ol>
          </Card>
        )}
        {d?.coverageNote && <p className="text-xs text-faint">{d.coverageNote}</p>}
      </div>
      <div className="flex flex-col gap-4 lg:sticky lg:top-20 lg:self-start">
        <WatchPanel eventId={game.id} title={title} />
        <Card title="Info">
          <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-2 text-sm">
            <dt className="text-muted">Starts</dt>
            <dd>
              {formatStart(game.startTime, { month: 'short', day: 'numeric', year: 'numeric' })}
            </dd>
            {game.venue && (
              <>
                <dt className="text-muted">Venue</dt>
                <dd>{game.venue}</dd>
              </>
            )}
            {game.broadcasts?.length ? (
              <>
                <dt className="text-muted">TV</dt>
                <dd>{game.broadcasts.join(', ')}</dd>
              </>
            ) : null}
            {d?.tournamentContext && (
              <>
                <dt className="text-muted">Competition</dt>
                <dd>{d.tournamentContext}</dd>
              </>
            )}
          </dl>
        </Card>
      </div>
    </div>
  );
}

function Card({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <section className="rounded-2xl border border-line bg-surface p-5">
      <h2 className="mb-3 text-base font-semibold">{title}</h2>
      {children}
    </section>
  );
}

function Scoreboard({ game }: { game: SportsGame }) {
  const score = showsScore(game);
  if (!isMatchup(game)) {
    return (
      <section className="rounded-2xl border border-line bg-[linear-gradient(135deg,color-mix(in_srgb,var(--accent)_18%,transparent),transparent)] p-6">
        <StatusPill status={game.status} detail={game.statusDetail} />
        <h1 className="mt-3 text-2xl font-semibold sm:text-3xl">{gameTitle(game)}</h1>
        {game.eventContext && <p className="mt-1 text-muted">{game.eventContext}</p>}
        <p className="mt-2 text-sm text-muted">{gameStatusText(game)}</p>
      </section>
    );
  }
  const side = (team: SportsTeam, align: 'left' | 'right') => (
    <div
      className={cn(
        'flex min-w-0 flex-1 flex-col items-center gap-2 text-center sm:flex-row sm:gap-4',
        align === 'right' && 'sm:flex-row-reverse sm:text-right',
        align === 'left' && 'sm:text-left',
      )}
    >
      <TeamLogo team={team} size={64} />
      <div className="min-w-0">
        <p className="truncate text-base font-semibold sm:text-xl">{team.name}</p>
        {team.record && <p className="text-xs text-muted">{team.record}</p>}
      </div>
    </div>
  );
  const color = game.homeTeam.color ? `#${game.homeTeam.color}` : 'var(--accent)';
  return (
    <section
      className="rounded-2xl border border-line p-5 sm:p-8"
      style={{
        background: `linear-gradient(135deg, color-mix(in srgb, ${color} 26%, transparent), transparent 70%)`,
      }}
    >
      <h1 className="sr-only">{gameTitle(game)}</h1>
      <div className="flex items-center gap-3 sm:gap-6">
        {side(game.awayTeam, 'left')}
        <div className="flex shrink-0 flex-col items-center gap-2">
          {score ? (
            <p className="text-3xl font-bold tabular-nums sm:text-5xl">
              {game.awayTeam.score ?? '–'}
              <span className="mx-2 text-faint">–</span>
              {game.homeTeam.score ?? '–'}
            </p>
          ) : (
            <p className="text-xl font-semibold text-muted">vs</p>
          )}
          <StatusPill status={game.status} detail={game.statusDetail} />
          <p className="text-center text-xs text-muted">{gameStatusText(game)}</p>
        </div>
        {side(game.homeTeam, 'right')}
      </div>
    </section>
  );
}

function LineScore({ game }: { game: SportsGame }) {
  const periods = game.detail?.periods ?? [];
  return (
    <section className="overflow-x-auto rounded-2xl border border-line bg-surface p-5">
      <h2 className="mb-3 text-base font-semibold">Line score</h2>
      <table className="w-full text-sm tabular-nums">
        <thead>
          <tr className="text-xs text-faint">
            <th className="py-1 text-left font-semibold">Team</th>
            {periods.map((p) => (
              <th key={p.label} className="px-2 py-1 text-center font-semibold">
                {p.label}
              </th>
            ))}
            <th className="px-2 py-1 text-center font-semibold">T</th>
          </tr>
        </thead>
        <tbody>
          {(['away', 'home'] as const).map((s) => {
            const team = s === 'away' ? game.awayTeam : game.homeTeam;
            return (
              <tr key={s} className="border-t border-line">
                <td className="py-2 font-medium">{team.abbreviation ?? team.name}</td>
                {periods.map((p) => (
                  <td key={p.label} className="px-2 py-2 text-center text-muted">
                    {p[s]}
                  </td>
                ))}
                <td className="px-2 py-2 text-center font-semibold">{team.score ?? ''}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </section>
  );
}
