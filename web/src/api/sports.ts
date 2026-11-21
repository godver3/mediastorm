import { sessionStore } from '@/auth/sessionStore';
import { apiPath } from './basePath';
import { apiRequest } from './client';

/**
 * Sports hub (mirrors native-tvos StrmrTV/API/APIClient+Sports.swift, +SportsRacing,
 * +SportsCycling and SportsModels / SportsRaceModels / SportsCyclingModels / SportsDetailModels).
 * Shapes verified against backend handlers/sports*.go.
 */

export interface SportsStatus {
  enabled: boolean;
  dateQueries?: boolean;
  lastRefresh?: string;
  lastError?: string;
}

export interface SportsLeague {
  id: string;
  enabled: boolean;
  name?: string;
  sport?: string;
  applicationSport?: string;
  eventKind?: string;
  implementationStatus?: string;
  capabilities?: string[];
}

export interface SportsTeam {
  id: string;
  name: string;
  location?: string;
  abbreviation?: string;
  logoUrl?: string;
  /** Hex without '#'. */
  color?: string;
  score?: string;
  shootoutScore?: number;
  winner?: boolean;
  record?: string;
  rank?: number;
  seed?: number;
}

export interface SportsParticipant {
  id: string;
  name: string;
  abbreviation?: string;
  score?: string;
  position?: number;
  winner?: boolean;
}

export interface SportsPeriodScore {
  label: string;
  away: string;
  home: string;
}
export interface SportsComparison {
  label: string;
  away: string;
  home: string;
}
export interface SportsLeader {
  label: string;
  name: string;
  teamAbbreviation: string;
  value: string;
  headshotUrl?: string;
}
export interface SportsPlay {
  id: string;
  periodLabel: string;
  clock?: string;
  title: string;
  description?: string;
  scoring: boolean;
  teamId?: string;
}

export interface SportsGameDetail {
  updatedAt?: string;
  stale?: boolean;
  coverageNote?: string;
  tournamentContext?: string;
  periods?: SportsPeriodScore[];
  plays?: SportsPlay[];
  comparisons?: SportsComparison[];
  leaders?: SportsLeader[];
}

export interface SportsGame {
  id: string;
  league: string;
  sport?: string;
  title?: string;
  eventKind?: string;
  eventContext?: string;
  startTime: string;
  endTime?: string;
  status: string;
  statusDetail?: string;
  period?: string;
  clock?: string;
  venue?: string;
  broadcasts?: string[];
  homeTeam: SportsTeam;
  awayTeam: SportsTeam;
  combat?: { cardName?: string; division?: string };
  participants?: SportsParticipant[];
  detail?: SportsGameDetail;
}

export interface SportsScoreboard {
  games: SportsGame[];
  updatedAt?: string;
  stale?: boolean;
  leagues?: { league: string; stale?: boolean }[];
}

export interface SportsStreamMatch {
  channelId: string;
  channelName: string;
  channelUrl: string;
  channelLogo?: string;
  channelTvgId?: string;
  sourceId?: string;
  sourceName?: string;
  programTitle?: string;
  confidence?: number;
  confidenceTier?: string;
  matchReason?: string;
}
export interface SportsStreamGroup {
  confidenceTier: string;
  primary: SportsStreamMatch;
  alternatives?: SportsStreamMatch[];
}

// Racing
export interface SportsRaceStatistic {
  name: string;
  label: string;
  value: string;
}
export interface SportsRaceDriver {
  id: string;
  name: string;
  abbreviation?: string;
  position?: number;
  winner?: boolean;
  number?: string;
  team?: string;
  result?: string;
  statistics?: SportsRaceStatistic[];
}
export interface SportsRaceEvent {
  id: string;
  providerEventId: string;
  sessionId?: string;
  sessionType?: string;
  title: string;
  league: string;
  eventKind: string;
  startTime: string;
  status: string;
  statusDetail?: string;
  venue?: string;
  broadcasts?: string[];
  participants?: SportsRaceDriver[];
  subEvents?: SportsRaceEvent[];
  circuit?: { name?: string; location?: string };
}
export interface SportsRaceBoard {
  events: SportsRaceEvent[];
  leagues?: { league: string; stale?: boolean; unavailable?: boolean }[];
}

// Cycling
export interface SportsCyclingResult {
  id: string;
  name: string;
  team?: string;
  bib?: number;
  nationality?: string;
  rank?: number;
  time?: string;
  gap?: string;
  result?: string;
}
export interface SportsCyclingResults {
  state: string;
  data?: SportsCyclingResult[];
  reason?: string;
}
export interface SportsCyclingStage {
  id: string;
  name: string;
  date?: string;
  departure?: string;
  arrival?: string;
  distance?: string;
  terrain?: string;
  status: string;
  results?: SportsCyclingResults;
  generalClassification?: SportsCyclingResults;
  generalClassificationLabel?: string;
  routeGuide?: { state: string; mapUrl?: string; profileUrl?: string; sourceUrl?: string };
}
export interface SportsCyclingRace {
  id: string;
  name: string;
  category?: string;
  raceKind?: string;
  classification?: string;
  country?: string;
  startDate?: string;
  endDate?: string;
  stages?: SportsCyclingStage[];
}
export interface SportsCyclingFeed {
  state: string;
  data?: SportsCyclingRace[];
  reason?: string;
}

export const sportsKeys = {
  all: ['sports'] as const,
  status: () => [...sportsKeys.all, 'status'] as const,
  leagues: () => [...sportsKeys.all, 'leagues'] as const,
  scoreboard: (date: string | undefined) =>
    [...sportsKeys.all, 'scoreboard', date ?? 'live'] as const,
  game: (id: string) => [...sportsKeys.all, 'game', id] as const,
  streams: (profileId: string, id: string, broad: boolean) =>
    [...sportsKeys.all, profileId, 'streams', id, broad] as const,
  races: () => [...sportsKeys.all, 'races'] as const,
  raceSession: (league: string, event: string, session: string) =>
    [...sportsKeys.all, 'race', league, event, session] as const,
  cycling: () => [...sportsKeys.all, 'cycling'] as const,
  cyclingStage: (id: string) => [...sportsKeys.all, 'cycling-stage', id] as const,
};

const seg = encodeURIComponent;

export const getSportsStatus = (signal?: AbortSignal) =>
  apiRequest<SportsStatus>('/sports/status', { signal });

/** Usable leagues only (enabled, validated/limited, with a schedule) as native SportsLeague.isUsable. */
export async function getSportsLeagues(signal?: AbortSignal): Promise<SportsLeague[]> {
  const res = await apiRequest<{ leagues?: SportsLeague[] | null }>('/sports/leagues', { signal });
  return (res?.leagues ?? []).filter(isUsableLeague);
}

export function isUsableLeague(l: SportsLeague): boolean {
  if (!l.enabled) return false;
  if (l.implementationStatus && !['validated', 'limited'].includes(l.implementationStatus))
    return false;
  if (l.capabilities && !l.capabilities.includes('schedule')) return false;
  return true;
}

export async function getSportsScoreboard(
  date: string | undefined,
  signal?: AbortSignal,
): Promise<SportsScoreboard> {
  const res = await apiRequest<Partial<SportsScoreboard> | null>('/sports/scoreboard', {
    query: { date },
    signal,
  });
  return { ...res, games: (res?.games ?? []).filter(validGame) };
}

const validGame = (g: SportsGame | null | undefined): g is SportsGame =>
  !!g && typeof g.id === 'string' && !!g.homeTeam && !!g.awayTeam;

export const getSportsGame = (id: string, signal?: AbortSignal) =>
  apiRequest<SportsGame>(`/sports/game/${seg(id)}`, { signal });

/** Channels that look like they carry the event (`kind`/`parentId` for race sessions & stages). */
export async function getSportsStreams(
  profileId: string,
  id: string,
  opts: { broad?: boolean; kind?: 'racing' | 'cycling'; parentId?: string } = {},
  signal?: AbortSignal,
): Promise<SportsStreamGroup[]> {
  const res = await apiRequest<{ groups?: SportsStreamGroup[] | null }>(
    `/sports/game/${seg(id)}/streams`,
    {
      query: {
        profileId,
        scope: opts.broad ? 'all' : undefined,
        kind: opts.kind,
        parentId: opts.parentId,
      },
      signal,
    },
  );
  return (res?.groups ?? []).filter((g) => g?.primary?.channelUrl);
}

export async function getSportsRaces(signal?: AbortSignal): Promise<SportsRaceBoard> {
  const res = await apiRequest<Partial<SportsRaceBoard> | null>('/sports/races', { signal });
  return { events: res?.events ?? [], leagues: res?.leagues ?? [] };
}

export const getSportsRaceSession = (
  league: string,
  event: string,
  session: string,
  signal?: AbortSignal,
) =>
  apiRequest<SportsRaceEvent>(`/sports/races/${seg(league)}/${seg(event)}/${seg(session)}`, {
    signal,
  });

export async function getSportsCycling(signal?: AbortSignal): Promise<SportsCyclingFeed> {
  const res = await apiRequest<Partial<SportsCyclingFeed> | null>('/sports/cycling', { signal });
  return { state: res?.state ?? 'unavailable', data: res?.data ?? [], reason: res?.reason };
}

/** `aso:tour:2026:21` -> { race: 'tour', year: '2026', number: '21' } (native SportsCycling.stagePath). */
export function cyclingStagePath(
  id: string,
): { race: string; year: string; number: string } | null {
  const parts = id.split(':');
  if (parts.length < 4) return null;
  const [, race, year, number] = parts as [string, string, string, string];
  if (!race || !year || !number) return null;
  return { race, year, number };
}

export function getSportsCyclingStage(id: string, signal?: AbortSignal) {
  const path = cyclingStagePath(id);
  if (!path) return Promise.reject(new Error("This stage can't be loaded."));
  return apiRequest<SportsCyclingStage>(
    `/sports/cycling/${seg(path.race)}/${seg(path.year)}/${seg(path.number)}`,
    { signal },
  );
}

// MARK: pure helpers

export type SportsLifecycle = 'scheduled' | 'live' | 'final' | 'delayed' | 'postponed' | 'canceled';

export function lifecycle(status: string, detail?: string): SportsLifecycle {
  const text = (detail ?? '').toLowerCase();
  if (text.includes('postpon')) return 'postponed';
  if (text.includes('cancel')) return 'canceled';
  if (text.includes('delay') || text.includes('suspend')) return 'delayed';
  if (status === 'live') return 'live';
  if (status === 'final') return 'final';
  return 'scheduled';
}

export const isMatchup = (g: SportsGame) => !!g.homeTeam.name && !!g.awayTeam.name && !g.combat;

export function gameTitle(g: SportsGame): string {
  if (g.combat?.cardName) return g.combat.cardName;
  if (g.title) return g.title;
  return `${g.awayTeam.name} at ${g.homeTeam.name}`;
}

export function showsScore(g: SportsGame): boolean {
  return (
    isMatchup(g) &&
    lifecycle(g.status, g.statusDetail) !== 'scheduled' &&
    (g.homeTeam.score != null || g.awayTeam.score != null)
  );
}

/** Live first, then upcoming by start, then finished most recent first. */
export function groupGames(games: SportsGame[]) {
  const live: SportsGame[] = [];
  const upcoming: SportsGame[] = [];
  const results: SportsGame[] = [];
  for (const g of games) {
    const lc = lifecycle(g.status, g.statusDetail);
    if (lc === 'live') live.push(g);
    else if (lc === 'final') results.push(g);
    else upcoming.push(g);
  }
  const t = (g: SportsGame) => Date.parse(g.startTime) || 0;
  live.sort((a, b) => t(a) - t(b));
  upcoming.sort((a, b) => t(a) - t(b));
  results.sort((a, b) => t(b) - t(a));
  return { live, upcoming, results };
}

export function matchesQuery(g: SportsGame, q: string): boolean {
  const needle = q.trim().toLowerCase();
  if (!needle) return true;
  return [
    gameTitle(g),
    g.homeTeam.name,
    g.awayTeam.name,
    g.homeTeam.abbreviation,
    g.awayTeam.abbreviation,
    g.league,
  ]
    .filter(Boolean)
    .some((s) => s!.toLowerCase().includes(needle));
}

/** A weekend's sessions by start; a lone session is its own. */
export function raceSessions(event: SportsRaceEvent): SportsRaceEvent[] {
  const subs = event.subEvents ?? [];
  if (subs.length === 0) return [event];
  return [...subs].sort((a, b) => (Date.parse(a.startTime) || 0) - (Date.parse(b.startTime) || 0));
}

/** Live session, else the last race, else the last session (Expo defaultRaceSession). */
export function defaultRaceSession(event: SportsRaceEvent): SportsRaceEvent {
  const subs = raceSessions(event);
  if (subs.length === 1) return subs[0]!;
  return (
    subs.find((s) => s.status === 'live') ??
    [...subs].reverse().find((s) => /^race\s*\d*$/i.test(s.sessionType ?? '')) ??
    subs[subs.length - 1]!
  );
}

export function cyclingRows(r: SportsCyclingResults | undefined): SportsCyclingResult[] {
  if (!r) return [];
  return r.state === 'available' || r.state === 'stale' ? (r.data ?? []) : [];
}

/** Today's date (YYYY-MM-DD) in US Eastern, the day the ESPN scoreboard is keyed by. */
export function easternDateKey(date = new Date()): string {
  const parts = new Intl.DateTimeFormat('en-CA', {
    timeZone: 'America/New_York',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
  }).format(date);
  return parts;
}

// MARK: playback

interface LiveHlsSession {
  sessionId?: string;
  playlistUrl?: string;
  streamUrl?: string;
  isDirect?: boolean;
  stremioStreamIndex?: number;
}

/**
 * Starts a live session for a matched channel (GET /api/live/hls/start, target=web, format=hls,
 * as the Expo web does) and returns the playback.html params for openPlayback().
 */
export async function prepareSportsPlayback(
  match: SportsStreamMatch,
  opts: { profileId: string; profileName?: string; title: string },
) {
  const session = await apiRequest<LiveHlsSession>('/live/hls/start', {
    query: {
      url: match.channelUrl,
      target: 'web',
      format: 'hls',
      profileId: opts.profileId,
      profileName: opts.profileName,
      sourceId: match.sourceId,
      liveSourceId: match.sourceId,
      mediaType: 'channel',
      title: opts.title,
      itemId: match.channelTvgId || match.channelId,
      channelId: match.channelId,
      channelName: match.channelName,
      liveSourceUrl: match.channelUrl,
      liveChannelLogo: match.channelLogo,
    },
  });
  const path = session.isDirect && session.streamUrl ? session.streamUrl : session.playlistUrl;
  if (!path) throw new Error('The server did not return a stream for this channel.');
  const movieUrl = new URL(apiPath(path), window.location.origin);
  const token = sessionStore.getToken();
  if (token) movieUrl.searchParams.set('token', token);
  const movie = movieUrl.toString();
  return {
    movie,
    title: opts.title,
    displayName: match.channelName,
    headerImage: match.channelLogo,
    mediaType: 'channel',
    tvgId: match.channelTvgId,
    liveSourceUrl: match.channelUrl,
    liveSourceId: match.sourceId,
    livePlaybackId: match.channelId,
    hlsSessionId: session.sessionId,
    stremioStreamIndex:
      typeof session.stremioStreamIndex === 'number'
        ? String(session.stremioStreamIndex)
        : undefined,
  };
}
