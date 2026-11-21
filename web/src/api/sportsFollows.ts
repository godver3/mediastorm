import { ApiError, apiRequest } from './client';
import { sportsKeys, type SportsGame, type SportsLeague } from './sports';

/**
 * Per-profile sports follows (GET/PUT /api/users/{id}/sports/preferences), shared with the native
 * apps and Expo. Mirrors native-tvos SportsModels.swift (SportsPreferencesEnvelope), SportsTeamFollows
 * .swift and SportsLeague.followID. The PUT sends the revision it read; 409 = another device saved.
 */

export interface SportsPreferences {
  version: number;
  pinnedSportIds: string[];
  competitionIds: string[];
  teamIds: string[];
  athleteIds: string[];
  /** Kept as sent (clients don't edit school follows). */
  schools: unknown[];
  [extra: string]: unknown;
}

export interface SportsPreferencesEnvelope {
  revision: number;
  preferences: SportsPreferences;
}

export const sportsPrefsKey = (profileId: string) =>
  [...sportsKeys.all, profileId, 'preferences'] as const;

const seg = encodeURIComponent;

export function normalizePreferences(raw: Partial<SportsPreferences> | null | undefined) {
  const list = (v: unknown) =>
    Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  return {
    ...(raw ?? {}),
    version: typeof raw?.version === 'number' ? raw.version : 1,
    pinnedSportIds: list(raw?.pinnedSportIds),
    competitionIds: list(raw?.competitionIds),
    teamIds: list(raw?.teamIds),
    athleteIds: list(raw?.athleteIds),
    schools: Array.isArray(raw?.schools) ? raw.schools : [],
  } as SportsPreferences;
}

/** null when the server has no preferences endpoint (404 from older servers). */
export async function getSportsPreferences(
  profileId: string,
  signal?: AbortSignal,
): Promise<SportsPreferencesEnvelope | null> {
  try {
    const res = await apiRequest<Partial<SportsPreferencesEnvelope> | null>(
      `/users/${seg(profileId)}/sports/preferences`,
      { signal },
    );
    return { revision: res?.revision ?? 0, preferences: normalizePreferences(res?.preferences) };
  } catch (err) {
    if (err instanceof ApiError && err.status === 404) return null;
    throw err;
  }
}

export async function putSportsPreferences(
  profileId: string,
  envelope: SportsPreferencesEnvelope,
): Promise<SportsPreferencesEnvelope> {
  const res = await apiRequest<Partial<SportsPreferencesEnvelope> | null>(
    `/users/${seg(profileId)}/sports/preferences`,
    { method: 'PUT', body: envelope },
  );
  return {
    revision: res?.revision ?? envelope.revision + 1,
    preferences: normalizePreferences(res?.preferences ?? envelope.preferences),
  };
}

// MARK: competitions (SportsLeague.followID)

export const FOLLOW_ALIASES: Record<string, string> = {
  'soccer-eng.1': 'premier-league',
  'soccer-esp.1': 'la-liga',
  'soccer-ger.1': 'bundesliga',
  'soccer-ita.1': 'serie-a',
  'soccer-fra.1': 'ligue-1',
  'soccer-usa.1': 'mls',
  'soccer-usa.nwsl': 'nwsl',
  'soccer-uefa.champions': 'uefa-champions-league',
  'college-football': 'ncaa-football',
  'mens-college-basketball': 'ncaa-mens-basketball',
  'womens-college-basketball': 'ncaa-womens-basketball',
  f1: 'formula-one',
  nascar: 'nascar-cup',
};

export const competitionFollowId = (leagueId: string) => FOLLOW_ALIASES[leagueId] ?? leagueId;

export const isCompetitionFollowed = (prefs: SportsPreferences, leagueId: string) =>
  prefs.competitionIds.includes(competitionFollowId(leagueId));

export function setCompetitionFollowed(
  prefs: SportsPreferences,
  leagueId: string,
  followed: boolean,
): SportsPreferences {
  const id = competitionFollowId(leagueId);
  const rest = prefs.competitionIds.filter((x) => x !== id && x !== leagueId);
  return { ...prefs, competitionIds: followed ? [...rest, id] : rest };
}

// MARK: teams (SportsTeamFollows)

export const teamFollowId = (league: string, teamId: string) => `${league}:${teamId}`;
const lastPart = (id: string) => id.split(':').at(-1) ?? id;
export const followLeague = (stored: string) => stored.split(':')[0] ?? stored;

/** Whether the stored follow is `teamId` of `league` (provider-prefixed ids match on the last part). */
export function teamMatches(stored: string, league: string, teamId: string): boolean {
  if (!league || !teamId || !stored.startsWith(`${league}:`)) return false;
  return stored === teamFollowId(league, teamId) || lastPart(stored) === lastPart(teamId);
}

export const isTeamFollowed = (prefs: SportsPreferences, league: string, teamId: string) =>
  prefs.teamIds.some((s) => teamMatches(s, league, teamId));

export function setTeamFollowed(
  prefs: SportsPreferences,
  league: string,
  teamId: string,
  followed: boolean,
): SportsPreferences {
  if (followed) {
    if (isTeamFollowed(prefs, league, teamId)) return prefs;
    return { ...prefs, teamIds: [...prefs.teamIds, teamFollowId(league, teamId)] };
  }
  return { ...prefs, teamIds: prefs.teamIds.filter((s) => !teamMatches(s, league, teamId)) };
}

/** Away-first sports (tennis, combat, golf) aren't team matchups. */
const NON_TEAM = /tennis|ufc|mma|boxing|pga|golf|lpga|liv/i;

export const isTeamLeagueId = (league: string) => !NON_TEAM.test(league);

export const isTeamLeague = (l: SportsLeague) =>
  isTeamLeagueId(l.id) &&
  !['racing', 'cycling'].includes(l.eventKind ?? '') &&
  !['racing', 'cycling'].includes(l.sport ?? '');

/** A game on the hub's Following filter: a followed team plays, or its competition is followed. */
export function isFollowedGame(g: SportsGame, prefs: SportsPreferences | undefined): boolean {
  if (!prefs) return false;
  if (isCompetitionFollowed(prefs, g.league)) return true;
  if (!isTeamLeagueId(g.league) || g.combat) return false;
  return [g.homeTeam, g.awayTeam].some((t) => !!t.id && isTeamFollowed(prefs, g.league, t.id));
}
