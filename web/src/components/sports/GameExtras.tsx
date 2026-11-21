import { useState, type ReactNode } from 'react';
import type {
  SportsGame,
  SportsLineup,
  SportsPlay,
  SportsPlayerStats,
  SportsTeam,
  SportsWinProbabilityPoint,
} from '@/api/sports';
import { isTeamFollowed, isTeamLeagueId, setTeamFollowed } from '@/api/sportsFollows';
import { cn } from '@/lib/cn';
import { FollowButton } from './FollowButton';
import { useSportsFollows } from './useSportsFollows';

/**
 * Game page sections beyond the scoreboard, as the native game page shows them
 * (native-tvos Views/Sports game detail; SportsDetailModels / SportsContextModels /
 * SportsTrackingModels): follow buttons, plays feed, box score, lineups, win probability.
 */

export function DetailCard({
  title,
  actions,
  children,
}: {
  title: string;
  actions?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="rounded-2xl border border-line bg-surface p-5">
      <div className="mb-3 flex items-center justify-between gap-2">
        <h2 className="text-base font-semibold">{title}</h2>
        {actions}
      </div>
      {children}
    </section>
  );
}

const short = (t: SportsTeam) => t.abbreviation ?? t.name;

/** "Follow" for each side of a team matchup (saved as `league:teamId`). */
export function FollowTeams({ game }: { game: SportsGame }) {
  const follows = useSportsFollows();
  const prefs = follows.preferences;
  if (!prefs || !isTeamLeagueId(game.league) || game.combat) return null;
  const teams = [game.awayTeam, game.homeTeam].filter((t) => !!t.id && !!t.name);
  if (teams.length === 0) return null;
  return (
    <div className="flex flex-wrap gap-2" aria-label="Follow teams">
      {teams.map((t) => (
        <FollowButton
          key={t.id}
          name={t.name}
          text={t.name}
          followed={isTeamFollowed(prefs, game.league, t.id)}
          onToggle={(v) => follows.update((p) => setTeamFollowed(p, game.league, t.id, v))}
        />
      ))}
    </div>
  );
}

/** The play-by-play, newest first as sent, with a scoring-only filter. */
export function PlaysFeed({ game, plays }: { game: SportsGame; plays: SportsPlay[] }) {
  const hasScoring = plays.some((p) => p.scoring);
  const [scoringOnly, setScoringOnly] = useState(hasScoring);
  const [expanded, setExpanded] = useState(false);
  const shown = scoringOnly ? plays.filter((p) => p.scoring) : plays;
  const visible = expanded ? shown : shown.slice(0, 25);
  const teamAbbr = (id?: string) =>
    id === game.homeTeam.id
      ? short(game.homeTeam)
      : id === game.awayTeam.id
        ? short(game.awayTeam)
        : '';
  return (
    <DetailCard
      title={scoringOnly ? 'Scoring' : 'Plays'}
      actions={
        hasScoring ? (
          <div className="flex gap-1 text-xs" role="tablist" aria-label="Plays">
            {[
              [true, 'Scoring'],
              [false, 'All plays'],
            ].map(([v, label]) => (
              <button
                key={String(v)}
                type="button"
                role="tab"
                aria-selected={scoringOnly === v}
                onClick={() => setScoringOnly(v as boolean)}
                className={cn(
                  'h-7 cursor-pointer rounded-full px-2.5 transition',
                  scoringOnly === v ? 'accent-soft font-medium' : 'text-muted hover:text-fg',
                )}
              >
                {label as string}
              </button>
            ))}
          </div>
        ) : undefined
      }
    >
      <ol className="flex flex-col gap-3">
        {visible.map((p) => (
          <li key={p.id} className="flex gap-3 text-sm">
            <span className="w-16 shrink-0 text-xs text-faint">
              {[p.periodLabel, p.clock].filter(Boolean).join(' ')}
            </span>
            <span className="min-w-0">
              <span className={cn('font-medium', p.scoring && 'text-accent')}>
                {teamAbbr(p.teamId) && (
                  <span className="mr-1.5 text-xs text-faint">{teamAbbr(p.teamId)}</span>
                )}
                {p.title}
              </span>
              {p.description && p.description !== p.title && (
                <span className="block text-muted">{p.description}</span>
              )}
            </span>
          </li>
        ))}
      </ol>
      {shown.length > visible.length && (
        <button
          type="button"
          onClick={() => setExpanded(true)}
          className="mt-3 cursor-pointer text-sm text-muted hover:text-fg"
        >
          Show all {shown.length}
        </button>
      )}
    </DetailCard>
  );
}

/** Groups box-score lines by team then category, keeping the feed's column order. */
export function boxScoreGroups(game: SportsGame, stats: SportsPlayerStats[]) {
  return [game.awayTeam, game.homeTeam].map((team) => {
    const lines = stats.filter((s) => s.teamId === team.id);
    const categories = [...new Set(lines.map((l) => l.category))];
    return {
      team,
      categories: categories.map((category) => {
        const rows = lines.filter((l) => l.category === category);
        const labels = [...new Set(rows.flatMap((r) => r.stats.map((s) => s.label)))];
        return { category, labels, rows };
      }),
    };
  });
}

export function BoxScore({ game, stats }: { game: SportsGame; stats: SportsPlayerStats[] }) {
  const groups = boxScoreGroups(game, stats).filter((g) => g.categories.length > 0);
  const [teamId, setTeamId] = useState(groups[0]?.team.id);
  const group = groups.find((g) => g.team.id === teamId) ?? groups[0];
  if (!group) return null;
  return (
    <DetailCard
      title="Box score"
      actions={
        <TeamTabs teams={groups.map((g) => g.team)} value={group.team.id} onChange={setTeamId} />
      }
    >
      <div className="flex flex-col gap-4">
        {group.categories.map((c) => (
          <div key={c.category} className="overflow-x-auto">
            <table className="w-full text-sm">
              <thead>
                <tr className="text-xs text-faint">
                  <th className="py-1.5 pr-3 text-left font-semibold capitalize">{c.category}</th>
                  {c.labels.map((l) => (
                    <th key={l} className="px-2 py-1.5 text-right font-medium whitespace-nowrap">
                      {l}
                    </th>
                  ))}
                </tr>
              </thead>
              <tbody>
                {c.rows.map((r) => (
                  <tr key={r.id} className="border-t border-line">
                    <td className="py-1.5 pr-3 whitespace-nowrap">
                      {r.jersey && <span className="mr-1.5 text-xs text-faint">#{r.jersey}</span>}
                      {r.name}
                    </td>
                    {c.labels.map((l) => (
                      <td key={l} className="px-2 py-1.5 text-right tabular-nums">
                        {r.stats.find((s) => s.label === l)?.value ?? '–'}
                      </td>
                    ))}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
      </div>
    </DetailCard>
  );
}

function TeamTabs({
  teams,
  value,
  onChange,
}: {
  teams: SportsTeam[];
  value?: string;
  onChange: (id: string) => void;
}) {
  if (teams.length < 2) return null;
  return (
    <div className="flex gap-1 text-xs" role="tablist" aria-label="Team">
      {teams.map((t) => (
        <button
          key={t.id}
          type="button"
          role="tab"
          aria-selected={t.id === value}
          onClick={() => onChange(t.id)}
          className={cn(
            'h-7 cursor-pointer rounded-full px-2.5 transition',
            t.id === value ? 'accent-soft font-medium' : 'text-muted hover:text-fg',
          )}
        >
          {short(t)}
        </button>
      ))}
    </div>
  );
}

export function Lineups({ game, lineups }: { game: SportsGame; lineups: SportsLineup[] }) {
  const teamOf = (id: string) =>
    id === game.homeTeam.id ? game.homeTeam : id === game.awayTeam.id ? game.awayTeam : undefined;
  const ordered = [...lineups].sort(
    (a, b) => Number(a.teamId === game.homeTeam.id) - Number(b.teamId === game.homeTeam.id),
  );
  return (
    <DetailCard title="Lineups">
      <div className="grid gap-5 sm:grid-cols-2">
        {ordered.map((l) => {
          const starters = l.players.filter((p) => p.starter);
          const bench = l.players.filter((p) => !p.starter);
          return (
            <div key={l.teamId}>
              <p className="mb-2 text-sm font-semibold">
                {teamOf(l.teamId)?.name ?? l.teamId}
                {l.formation && (
                  <span className="ml-2 text-xs font-normal text-muted">{l.formation}</span>
                )}
              </p>
              <PlayerList players={starters.length ? starters : l.players} />
              {starters.length > 0 && bench.length > 0 && (
                <>
                  <p className="mt-3 mb-1 text-xs font-semibold text-faint uppercase">Bench</p>
                  <PlayerList players={bench} />
                </>
              )}
            </div>
          );
        })}
      </div>
    </DetailCard>
  );
}

function PlayerList({ players }: { players: SportsLineup['players'] }) {
  return (
    <ul className="flex flex-col gap-1 text-sm">
      {players.map((p) => (
        <li key={p.id} className="flex items-center gap-2">
          <span className="w-6 text-right text-xs text-faint tabular-nums">{p.number}</span>
          <span className="truncate">{p.name}</span>
          {p.subbedIn && <span className="text-xs text-success">↑</span>}
          {p.subbedOut && <span className="text-xs text-danger">↓</span>}
          {p.position && <span className="ml-auto text-xs text-faint">{p.position}</span>}
        </li>
      ))}
    </ul>
  );
}

/** SVG polyline points for a 0..1 home-probability series (home on top). */
export function winProbabilityPath(points: SportsWinProbabilityPoint[], w: number, h: number) {
  if (points.length < 2) return '';
  return points
    .map((p, i) => {
      const x = (i / (points.length - 1)) * w;
      const y = (1 - Math.min(1, Math.max(0, p.home))) * h;
      return `${x.toFixed(1)},${y.toFixed(1)}`;
    })
    .join(' ');
}

export function WinProbability({
  game,
  points,
}: {
  game: SportsGame;
  points: SportsWinProbabilityPoint[];
}) {
  const W = 600;
  const H = 160;
  const last = points.at(-1)!;
  const home = Math.round(last.home * 100);
  return (
    <DetailCard
      title="Win probability"
      actions={
        <span className="text-xs text-muted tabular-nums">
          {short(game.homeTeam)} {home}% · {short(game.awayTeam)} {100 - home}%
        </span>
      }
    >
      <div className="relative">
        <span className="absolute top-0 left-0 text-[11px] text-faint">{short(game.homeTeam)}</span>
        <span className="absolute bottom-0 left-0 text-[11px] text-faint">
          {short(game.awayTeam)}
        </span>
        <svg
          viewBox={`0 0 ${W} ${H}`}
          preserveAspectRatio="none"
          className="h-40 w-full"
          role="img"
          aria-label={`Win probability: ${short(game.homeTeam)} ${home}%`}
        >
          <line
            x1="0"
            x2={W}
            y1={H / 2}
            y2={H / 2}
            stroke="var(--line-strong, #444)"
            strokeDasharray="4 4"
          />
          <polyline
            points={winProbabilityPath(points, W, H)}
            fill="none"
            stroke="var(--accent)"
            strokeWidth="2.5"
            vectorEffect="non-scaling-stroke"
          />
        </svg>
      </div>
    </DetailCard>
  );
}
