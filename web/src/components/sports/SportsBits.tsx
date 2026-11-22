import { Link } from '@tanstack/react-router';
import {
  gameTitle,
  isMatchup,
  lifecycle,
  showsScore,
  type SportsGame,
  type SportsLifecycle,
  type SportsTeam,
} from '@/api/sports';
import { Icon } from '@/components/icons';
import { cn } from '@/lib/cn';

const pillStyles: Record<SportsLifecycle, string> = {
  live: 'bg-danger text-white',
  final: 'bg-surface-strong text-muted',
  scheduled: 'accent-soft',
  delayed: 'bg-[color-mix(in_srgb,var(--warning)_22%,transparent)] text-fg',
  postponed: 'bg-[color-mix(in_srgb,var(--warning)_22%,transparent)] text-fg',
  canceled: 'bg-[color-mix(in_srgb,var(--danger)_18%,transparent)] text-fg',
};
const pillLabels: Record<SportsLifecycle, string> = {
  live: 'Live',
  final: 'Final',
  scheduled: 'Upcoming',
  delayed: 'Delayed',
  postponed: 'Postponed',
  canceled: 'Canceled',
};

export function StatusPill({ status, detail }: { status: string; detail?: string }) {
  const lc = lifecycle(status, detail);
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-[11px] font-semibold tracking-wide uppercase',
        pillStyles[lc],
      )}
    >
      {lc === 'live' && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-white" />}
      {pillLabels[lc]}
    </span>
  );
}

export function TeamLogo({
  team,
  size = 32,
}: {
  team: Pick<SportsTeam, 'logoUrl' | 'abbreviation' | 'name' | 'color'>;
  size?: number;
}) {
  const safe = team.logoUrl?.startsWith('https://') ? team.logoUrl : undefined;
  if (safe) {
    return (
      <img
        src={safe}
        alt=""
        width={size}
        height={size}
        loading="lazy"
        className="shrink-0 object-contain"
        style={{ width: size, height: size }}
      />
    );
  }
  const label = (team.abbreviation || team.name || '?').slice(0, 3).toUpperCase();
  return (
    <span
      aria-hidden
      className="flex shrink-0 items-center justify-center rounded-full text-[10px] font-bold text-white"
      style={{
        width: size,
        height: size,
        background: team.color ? `#${team.color}` : 'var(--surface-strong)',
      }}
    >
      {label}
    </span>
  );
}

export function formatStart(iso: string, opts: Intl.DateTimeFormatOptions = {}) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleString(undefined, {
    weekday: 'short',
    hour: 'numeric',
    minute: '2-digit',
    ...opts,
  });
}

export function gameStatusText(g: SportsGame): string {
  const lc = lifecycle(g.status, g.statusDetail);
  if (lc === 'live')
    return g.statusDetail || [g.period, g.clock].filter(Boolean).join(' · ') || 'In progress';
  if (lc === 'final') return g.statusDetail || 'Final';
  if (lc === 'scheduled') return formatStart(g.startTime);
  return g.statusDetail || pillLabels[lc];
}

function TeamRow({
  team,
  score,
  showScore,
  dim,
}: {
  team: SportsTeam;
  score?: string;
  showScore: boolean;
  dim: boolean;
}) {
  return (
    <div className={cn('flex items-center gap-3', dim && 'opacity-55')}>
      <TeamLogo team={team} size={28} />
      <span className="min-w-0 flex-1 truncate text-sm font-medium">
        {team.rank && team.rank <= 25 ? (
          <span className="mr-1 text-xs text-faint">{team.rank}</span>
        ) : null}
        {team.name}
        {team.record ? (
          <span className="ml-2 text-xs font-normal text-faint">{team.record}</span>
        ) : null}
      </span>
      {showScore && <span className="text-lg font-semibold tabular-nums">{score ?? '–'}</span>}
    </div>
  );
}

export function GameCard({ game, leagueName }: { game: SportsGame; leagueName?: string }) {
  const score = showsScore(game);
  const final = lifecycle(game.status, game.statusDetail) === 'final';
  const homeWon = final && game.homeTeam.winner;
  const awayWon = final && game.awayTeam.winner;
  return (
    <Link
      to="/sports/game/$gameId"
      params={{ gameId: game.id }}
      className="group flex flex-col gap-3 rounded-2xl border border-line bg-surface p-4 transition hover:-translate-y-0.5 hover:border-line-strong hover:bg-surface-strong hover:shadow-card"
    >
      <div className="flex items-center justify-between gap-2">
        <span className="truncate text-xs font-semibold tracking-wide text-faint uppercase">
          {leagueName ?? game.league}
        </span>
        <StatusPill status={game.status} detail={game.statusDetail} />
      </div>
      {isMatchup(game) ? (
        <div className="flex flex-col gap-2">
          <TeamRow
            team={game.awayTeam}
            score={game.awayTeam.score}
            showScore={score}
            dim={final && !!homeWon}
          />
          <TeamRow
            team={game.homeTeam}
            score={game.homeTeam.score}
            showScore={score}
            dim={final && !!awayWon}
          />
        </div>
      ) : (
        <div>
          <p className="line-clamp-2 font-medium">{gameTitle(game)}</p>
          {game.eventContext && <p className="truncate text-xs text-muted">{game.eventContext}</p>}
        </div>
      )}
      <div className="mt-auto flex items-center justify-between gap-2 text-xs text-muted">
        <span className="truncate">{gameStatusText(game)}</span>
        {game.broadcasts?.length ? (
          <span className="truncate text-faint">{game.broadcasts.slice(0, 2).join(', ')}</span>
        ) : null}
      </div>
    </Link>
  );
}

export function BackToSports({ tab }: { tab?: 'games' | 'racing' | 'cycling' }) {
  return (
    <Link
      to="/sports"
      search={{ tab: tab ?? 'games' }}
      className="inline-flex items-center gap-1 pt-6 text-sm text-muted hover:text-fg"
    >
      <Icon name="chevronLeft" size={16} /> Sports
    </Link>
  );
}
