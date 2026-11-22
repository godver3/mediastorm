import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SportsGame, SportsStreamMatch } from './sports';
import {
  competitionFollowId,
  getSportsPreferences,
  isFollowedGame,
  isTeamFollowed,
  normalizePreferences,
  setCompetitionFollowed,
  setTeamFollowed,
  teamMatches,
} from './sportsFollows';
import { normalizeStandings, standingsColumns, standingsValue } from './sportsStandings';
import {
  expandStreamRows,
  hasStremioSources,
  streamLabel,
  withStream,
} from './sportsStreamOptions';
import { boxScoreGroups, winProbabilityPath } from '@/components/sports/GameExtras';

const prefs = normalizePreferences({
  teamIds: ['nba:espn:16'],
  competitionIds: ['premier-league'],
});

const game = (p: Partial<SportsGame> = {}): SportsGame => ({
  id: 'g',
  league: 'nba',
  startTime: '2026-11-20T00:00:00Z',
  status: 'scheduled',
  homeTeam: { id: '16', name: 'Timberwolves', abbreviation: 'MIN' },
  awayTeam: { id: '1', name: 'Hawks', abbreviation: 'ATL' },
  ...p,
});

describe('sports follows', () => {
  it('normalizes missing lists and keeps unknown fields', () => {
    const n = normalizePreferences({ teamIds: ['a:b', 3 as unknown as string], extra: 1 });
    expect(n.teamIds).toEqual(['a:b']);
    expect(n.competitionIds).toEqual([]);
    expect(n.extra).toBe(1);
    expect(n.version).toBe(1);
  });

  it('matches provider-prefixed team ids on their last part within a league', () => {
    expect(teamMatches('nba:espn:16', 'nba', '16')).toBe(true);
    expect(teamMatches('nba:16', 'nba', 'espn:16')).toBe(true);
    expect(teamMatches('nfl:16', 'nba', '16')).toBe(false);
    expect(isTeamFollowed(prefs, 'nba', '16')).toBe(true);
  });

  it('follows and unfollows a team once', () => {
    const a = setTeamFollowed(prefs, 'nba', '1', true);
    expect(a.teamIds).toEqual(['nba:espn:16', 'nba:1']);
    expect(setTeamFollowed(a, 'nba', '1', true)).toBe(a);
    expect(setTeamFollowed(a, 'nba', '16', false).teamIds).toEqual(['nba:1']);
  });

  it('saves competitions under the shared alias', () => {
    expect(competitionFollowId('soccer-eng.1')).toBe('premier-league');
    expect(setCompetitionFollowed(prefs, 'nba', true).competitionIds).toEqual([
      'premier-league',
      'nba',
    ]);
    expect(setCompetitionFollowed(prefs, 'soccer-eng.1', false).competitionIds).toEqual([]);
  });

  it('puts games of followed teams or competitions on the Following filter', () => {
    expect(isFollowedGame(game(), prefs)).toBe(true);
    expect(isFollowedGame(game({ homeTeam: { id: '9', name: 'X' } }), prefs)).toBe(false);
    expect(
      isFollowedGame(game({ league: 'soccer-eng.1', homeTeam: { id: '9', name: 'X' } }), prefs),
    ).toBe(true);
    expect(isFollowedGame(game(), undefined)).toBe(false);
  });

  afterEach(() => vi.unstubAllGlobals());
  it('treats a 404 as no preferences support', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response('nope', { status: 404 })),
    );
    expect(await getSportsPreferences('p1')).toBeNull();
  });
});

describe('standings', () => {
  const table = normalizeStandings(
    {
      groups: [
        {
          id: 'east',
          title: 'East',
          columns: [
            { key: 'overall', label: 'W-L' },
            { key: 'gamesBehind', label: 'GB' },
            { key: 'playoffSeed', label: 'Seed' },
            { key: 'other', label: 'X' },
          ],
          rows: [
            { id: '1', name: 'A', kind: '', values: { overall: '10-2', playoffSeed: '0' } },
            { id: 2 as unknown as string, name: 'bad', kind: 'team', values: {} },
          ],
        },
      ],
    },
    'nba',
  );

  it('drops malformed rows and defaults kind', () => {
    expect(table.state).toBe('unavailable');
    expect(table.groups[0]!.rows).toHaveLength(1);
    expect(table.groups[0]!.rows[0]!.kind).toBe('team');
  });

  it('orders columns per league and dashes empty seeds', () => {
    expect(standingsColumns('nba', table.groups[0]!).map((c) => c.key)).toEqual([
      'playoffSeed',
      'overall',
      'gamesBehind',
    ]);
    const row = table.groups[0]!.rows[0]!;
    expect(standingsValue(row, 'playoffSeed')).toBe('–');
    expect(standingsValue(row, 'gamesBehind')).toBe('–');
    expect(standingsValue(row, 'overall')).toBe('10-2');
  });
});

describe('stremio stream options', () => {
  const stremio: SportsStreamMatch = {
    channelId: 'c1',
    channelName: 'Sports 1',
    channelUrl: 'https://addon.example/stream/tv/abc.json',
  };
  const plain: SportsStreamMatch = {
    channelId: 'c2',
    channelName: 'Sports 2',
    channelUrl: 'http://iptv.example/live/2.ts',
  };

  it('recognises stream resource URLs', () => {
    expect(hasStremioSources(stremio.channelUrl)).toBe(true);
    expect(hasStremioSources(`${stremio.channelUrl}?x=1`)).toBe(true);
    expect(hasStremioSources(plain.channelUrl)).toBe(false);
  });

  it('labels streams and expands a looked-up channel into one row per stream', () => {
    expect(streamLabel({ index: 2 })).toBe('Feed 3');
    expect(streamLabel({ index: 0, label: ' ', title: 'HD' })).toBe('HD');
    const rows = expandStreamRows([stremio, plain, stremio], {
      [stremio.channelUrl]: [
        { index: 0, label: '1080p' },
        { index: 1, label: '720p' },
      ],
    });
    expect(rows.map((r) => [r.channelId, r.stremioStreamIndex, r.streamLabel])).toEqual([
      ['c1', 0, '1080p'],
      ['c1', 1, '720p'],
      ['c2', undefined, undefined],
    ]);
    expect(withStream(plain, { index: 4 }).stremioStreamIndex).toBe(4);
  });

  it('keeps the plain row while a lookup is pending or empty', () => {
    expect(expandStreamRows([stremio], {})).toEqual([stremio]);
    expect(expandStreamRows([stremio], { [stremio.channelUrl]: [] })).toEqual([stremio]);
  });
});

describe('game extras', () => {
  it('groups box score lines by team and category', () => {
    const groups = boxScoreGroups(game(), [
      {
        id: 'a',
        teamId: '16',
        name: 'Ant',
        category: 'starters',
        stats: [
          { label: 'PTS', value: '30' },
          { label: 'REB', value: '5' },
        ],
      },
      {
        id: 'b',
        teamId: '1',
        name: 'Trae',
        category: 'starters',
        stats: [{ label: 'AST', value: '11' }],
      },
    ]);
    expect(groups.map((g) => g.team.id)).toEqual(['1', '16']);
    expect(groups[1]!.categories[0]!.labels).toEqual(['PTS', 'REB']);
  });

  it('maps win probability to chart points with home on top', () => {
    expect(winProbabilityPath([{ period: 1, clock: '', home: 0.5 }], 100, 100)).toBe('');
    expect(
      winProbabilityPath(
        [
          { period: 1, clock: '', home: 1 },
          { period: 1, clock: '', home: 0 },
        ],
        100,
        100,
      ),
    ).toBe('0.0,0.0 100.0,100.0');
  });
});
