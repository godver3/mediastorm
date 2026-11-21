import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  cyclingStagePath,
  defaultRaceSession,
  gameTitle,
  getSportsLeagues,
  getSportsStreams,
  groupGames,
  isUsableLeague,
  lifecycle,
  matchesQuery,
  showsScore,
  type SportsGame,
  type SportsRaceEvent,
} from './sports';

const team = (name: string, score?: string) => ({
  id: name,
  name,
  abbreviation: name.slice(0, 3).toUpperCase(),
  score,
});
const game = (p: Partial<SportsGame>): SportsGame => ({
  id: 'g',
  league: 'nba',
  startTime: '2026-11-21T20:00:00Z',
  status: 'scheduled',
  homeTeam: team('Celtics'),
  awayTeam: team('Lakers'),
  ...p,
});

afterEach(() => vi.restoreAllMocks());

describe('lifecycle', () => {
  it('reads schedule changes from the status text', () => {
    expect(lifecycle('scheduled', 'Postponed')).toBe('postponed');
    expect(lifecycle('live', 'Rain delay')).toBe('delayed');
    expect(lifecycle('live')).toBe('live');
    expect(lifecycle('final')).toBe('final');
    expect(lifecycle('whatever')).toBe('scheduled');
  });
});

describe('games', () => {
  it('groups live / upcoming / results with results newest first', () => {
    const g = groupGames([
      game({ id: 'a', status: 'final', startTime: '2026-11-20T00:00:00Z' }),
      game({ id: 'b', status: 'final', startTime: '2026-11-21T00:00:00Z' }),
      game({ id: 'c', status: 'live' }),
      game({ id: 'd', startTime: '2026-11-23T00:00:00Z' }),
      game({ id: 'e', startTime: '2026-11-22T00:00:00Z' }),
    ]);
    expect(g.live.map((x) => x.id)).toEqual(['c']);
    expect(g.upcoming.map((x) => x.id)).toEqual(['e', 'd']);
    expect(g.results.map((x) => x.id)).toEqual(['b', 'a']);
  });
  it('titles, scores and filters', () => {
    expect(gameTitle(game({}))).toBe('Lakers at Celtics');
    expect(gameTitle(game({ combat: { cardName: 'UFC 320' } }))).toBe('UFC 320');
    expect(showsScore(game({ homeTeam: team('Celtics', '99') }))).toBe(false);
    expect(showsScore(game({ status: 'live', homeTeam: team('Celtics', '99') }))).toBe(true);
    expect(matchesQuery(game({}), 'lak')).toBe(true);
    expect(matchesQuery(game({}), 'nets')).toBe(false);
  });
});

describe('leagues', () => {
  it('keeps only usable leagues', async () => {
    expect(isUsableLeague({ id: 'x', enabled: true })).toBe(true);
    expect(isUsableLeague({ id: 'x', enabled: true, implementationStatus: 'planned' })).toBe(false);
    expect(isUsableLeague({ id: 'x', enabled: true, capabilities: ['scores'] })).toBe(false);
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          leagues: [
            { id: 'nba', enabled: true },
            { id: 'nhl', enabled: false },
          ],
        }),
      ),
    );
    expect((await getSportsLeagues()).map((l) => l.id)).toEqual(['nba']);
  });
});

describe('streams', () => {
  it('sends profileId, scope and kind, and drops groups without a channel', async () => {
    const fetch = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          groups: [
            {
              confidenceTier: 'strong',
              primary: { channelId: '1', channelName: 'ESPN', channelUrl: 'http://x' },
            },
            {
              confidenceTier: 'weak',
              primary: { channelId: '2', channelName: 'Bad', channelUrl: '' },
            },
          ],
        }),
      ),
    );
    const groups = await getSportsStreams('p1', 'f1:2026:abu', {
      broad: true,
      kind: 'racing',
      parentId: 'w1',
    });
    expect(groups).toHaveLength(1);
    const url = String(fetch.mock.calls[0]![0]);
    expect(url).toContain('/sports/game/f1%3A2026%3Aabu/streams?');
    expect(url).toContain('profileId=p1');
    expect(url).toContain('scope=all');
    expect(url).toContain('kind=racing');
    expect(url).toContain('parentId=w1');
  });
});

describe('racing & cycling', () => {
  const s = (
    id: string,
    sessionType: string,
    status = 'final',
    startTime = '2026-01-01T00:00:00Z',
  ): SportsRaceEvent => ({
    id,
    providerEventId: 'e',
    sessionId: id,
    sessionType,
    title: id,
    league: 'f1',
    eventKind: 'race-session',
    startTime,
    status,
  });
  it('picks the live session, else the last race', () => {
    const weekend: SportsRaceEvent = {
      ...s('w', 'Weekend'),
      eventKind: 'race',
      subEvents: [
        s('q', 'Qualifying', 'final', '2026-01-01T00:00:00Z'),
        s('r', 'Race', 'scheduled', '2026-01-02T00:00:00Z'),
        s('p', 'Practice 1', 'final', '2025-12-31T00:00:00Z'),
      ],
    };
    expect(defaultRaceSession(weekend).id).toBe('r');
    weekend.subEvents![0] = s('q', 'Qualifying', 'live');
    expect(defaultRaceSession(weekend).id).toBe('q');
  });
  it('parses cycling stage ids like native SportsCycling.stagePath', () => {
    expect(cyclingStagePath('aso:tour:2026:21')).toEqual({
      race: 'tour',
      year: '2026',
      number: '21',
    });
    expect(cyclingStagePath('bad')).toBeNull();
  });
});
