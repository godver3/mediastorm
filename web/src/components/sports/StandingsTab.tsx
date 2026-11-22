import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';
import { getSportsLeagues, sportsKeys, type SportsLeague } from '@/api/sports';
import {
  isCompetitionFollowed,
  isTeamFollowed,
  isTeamLeague,
  setCompetitionFollowed,
  setTeamFollowed,
} from '@/api/sportsFollows';
import {
  getLeagueStandings,
  standingsColumns,
  standingsKeys,
  standingsValue,
  type SportsLeagueTable,
} from '@/api/sportsStandings';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { Skeleton } from '@/components/Spinner';
import { cn } from '@/lib/cn';
import { FollowButton } from './FollowButton';
import { useSportsFollows } from './useSportsFollows';

/**
 * League tables (GET /api/sports/standings/{league}) with follow toggles for the competition and
 * each team, as the native hub's standings pages (SportsStandings.swift, SportsTeamFollows.swift).
 */
export function StandingsTab({
  league,
  onLeague,
}: {
  league?: string;
  onLeague: (id: string) => void;
}) {
  const leagues = useQuery({
    queryKey: sportsKeys.leagues(),
    queryFn: ({ signal }) => getSportsLeagues(signal),
    staleTime: 10 * 60_000,
  });
  const follows = useSportsFollows();
  const teamLeagues = (leagues.data ?? []).filter(
    (l) => isTeamLeague(l) && l.supportsTeams !== false,
  );
  const prefs = follows.preferences;
  const ordered = [...teamLeagues].sort(
    (a, b) =>
      Number(!!prefs && isCompetitionFollowed(prefs, b.id)) -
      Number(!!prefs && isCompetitionFollowed(prefs, a.id)),
  );
  const active = ordered.find((l) => l.id === league) ?? ordered[0];

  if (leagues.isLoading) return <Skeleton className="h-96 rounded-2xl" />;
  if (leagues.isError)
    return (
      <ErrorState
        title="Couldn't load leagues"
        error={leagues.error}
        onRetry={() => leagues.refetch()}
      />
    );
  if (!active)
    return (
      <EmptyState icon="trophy" title="No standings" description="No team league is enabled." />
    );

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <label className="sr-only" htmlFor="standings-league">
          League
        </label>
        <select
          id="standings-league"
          value={active.id}
          onChange={(e) => onLeague(e.target.value)}
          className="h-9 cursor-pointer rounded-full border border-line bg-surface px-3 text-sm outline-none focus:border-accent"
        >
          {ordered.map((l) => (
            <option key={l.id} value={l.id}>
              {leagueLabel(l)}
            </option>
          ))}
        </select>
        {prefs && (
          <FollowButton
            name={leagueLabel(active)}
            followed={isCompetitionFollowed(prefs, active.id)}
            onToggle={(v) => follows.update((p) => setCompetitionFollowed(p, active.id, v))}
          />
        )}
      </div>
      <LeagueTables
        league={active.id}
        followedTeam={(id) => !!prefs && isTeamFollowed(prefs, active.id, id)}
        onFollowTeam={
          prefs ? (id, v) => follows.update((p) => setTeamFollowed(p, active.id, id, v)) : undefined
        }
      />
    </div>
  );
}

const leagueLabel = (l: SportsLeague) => l.name ?? l.id.toUpperCase();

function LeagueTables({
  league,
  followedTeam,
  onFollowTeam,
}: {
  league: string;
  followedTeam: (id: string) => boolean;
  onFollowTeam?: (id: string, v: boolean) => void;
}) {
  const standings = useQuery({
    queryKey: standingsKeys.league(league),
    queryFn: ({ signal }) => getLeagueStandings(league, signal),
    staleTime: 10 * 60_000,
  });
  const [groupId, setGroupId] = useState<string>();
  if (standings.isLoading) return <Skeleton className="h-96 rounded-2xl" />;
  if (standings.isError)
    return (
      <ErrorState
        title="Couldn't load standings"
        error={standings.error}
        onRetry={() => standings.refetch()}
      />
    );
  const data = standings.data!;
  if (data.groups.length === 0)
    return (
      <EmptyState
        icon="trophy"
        title="Standings unavailable"
        description={data.reason || 'The provider has no table for this league right now.'}
      />
    );
  const group = data.groups.find((g) => g.id === groupId) ?? data.groups[0]!;
  return (
    <div className="flex flex-col gap-3">
      {data.groups.length > 1 && (
        <div
          className="flex gap-1.5 overflow-x-auto no-scrollbar"
          role="tablist"
          aria-label="Table"
        >
          {data.groups.map((g) => (
            <button
              key={g.id}
              type="button"
              role="tab"
              aria-selected={g.id === group.id}
              onClick={() => setGroupId(g.id)}
              className={cn(
                'h-8 shrink-0 cursor-pointer rounded-full border px-3 text-sm whitespace-nowrap transition',
                g.id === group.id
                  ? 'border-transparent accent-soft font-medium'
                  : 'border-line text-muted hover:bg-surface hover:text-fg',
              )}
            >
              {g.title || g.id}
            </button>
          ))}
        </div>
      )}
      {data.state === 'stale' && (
        <p className="text-xs text-warning">Updates delayed — showing the last table.</p>
      )}
      <StandingsTable
        league={league}
        table={group}
        followedTeam={followedTeam}
        onFollowTeam={onFollowTeam}
      />
    </div>
  );
}

export function StandingsTable({
  league,
  table,
  followedTeam,
  onFollowTeam,
}: {
  league: string;
  table: SportsLeagueTable;
  followedTeam: (id: string) => boolean;
  onFollowTeam?: (id: string, v: boolean) => void;
}) {
  const columns = standingsColumns(league, table);
  return (
    <div className="overflow-x-auto rounded-2xl border border-line bg-surface">
      <table className="w-full min-w-[420px] text-sm">
        <caption className="px-4 pt-3 text-left text-xs font-semibold tracking-wide text-faint uppercase">
          {[table.title, table.seasonLabel].filter(Boolean).join(' · ')}
        </caption>
        <thead>
          <tr className="text-xs text-faint">
            <th className="px-4 py-2 text-left font-medium">Team</th>
            {columns.map((c) => (
              <th key={c.key} className="px-3 py-2 text-right font-medium whitespace-nowrap">
                {c.label || c.key}
              </th>
            ))}
            {onFollowTeam && <th className="w-12" aria-label="Follow" />}
          </tr>
        </thead>
        <tbody>
          {table.rows.map((r, i) => {
            const followed = r.kind === 'team' && followedTeam(r.id);
            return (
              <tr key={r.id} className={cn('border-t border-line', followed && 'accent-soft')}>
                <td className="px-4 py-2">
                  <span className="mr-2 inline-block w-5 text-xs text-faint tabular-nums">
                    {i + 1}
                  </span>
                  <span className="font-medium">{r.name}</span>
                </td>
                {columns.map((c) => (
                  <td key={c.key} className="px-3 py-2 text-right tabular-nums">
                    {standingsValue(r, c.key)}
                  </td>
                ))}
                {onFollowTeam && (
                  <td className="px-2 py-1 text-right">
                    {r.kind === 'team' && (
                      <FollowButton
                        compact
                        name={r.name}
                        followed={followed}
                        onToggle={(v) => onFollowTeam(r.id, v)}
                      />
                    )}
                  </td>
                )}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
