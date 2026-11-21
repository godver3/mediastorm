import { apiRequest } from './client';
import { sportsKeys } from './sports';

/**
 * League tables and racing enrichment. Mirrors native-tvos StrmrTV/API/SportsStandings.swift
 * (GET /api/sports/standings/{league}) and SportsRaceEnrichmentModels.swift
 * (GET /api/sports/motogp/standings, /api/sports/motogp/circuit/{providerEventId}).
 */

export interface SportsTableColumn {
  key: string;
  label: string;
}
export interface SportsTableRow {
  id: string;
  name: string;
  abbreviation?: string;
  kind: string;
  values: Record<string, string>;
}
export interface SportsLeagueTable {
  id: string;
  title: string;
  seasonLabel?: string;
  columns: SportsTableColumn[];
  rows: SportsTableRow[];
}
export interface SportsLeagueStandings {
  league: string;
  state: string;
  source?: string;
  sourceUrl?: string;
  updatedAt?: string;
  reason?: string;
  groups: SportsLeagueTable[];
}

export interface MotoGPRider {
  riderId: string;
  name: string;
  number?: number;
  team?: string;
  constructor?: string;
  position?: number;
  points?: number;
  wins?: number;
  podiums?: number;
}
export interface MotoGPCircuit {
  id?: string;
  name?: string;
  mapUrl?: string;
  lengthMeters?: number;
  leftCorners?: number;
  rightCorners?: number;
  longestStraightMeters?: number;
}
export interface MotoGPEnrichment {
  state: string;
  season?: number;
  source?: string;
  sourceUrl?: string;
  reason?: string;
  standings: MotoGPRider[];
  circuit?: MotoGPCircuit;
}

export const standingsKeys = {
  league: (league: string) => [...sportsKeys.all, 'standings', league] as const,
  motogp: () => [...sportsKeys.all, 'motogp-standings'] as const,
  circuit: (event: string) => [...sportsKeys.all, 'motogp-circuit', event] as const,
};

const seg = encodeURIComponent;
const arr = <T>(v: unknown): T[] => (Array.isArray(v) ? (v.filter(Boolean) as T[]) : []);

export function normalizeStandings(raw: Partial<SportsLeagueStandings> | null, league: string) {
  return {
    league: raw?.league || league,
    state: raw?.state || 'unavailable',
    source: raw?.source,
    sourceUrl: raw?.sourceUrl,
    updatedAt: raw?.updatedAt,
    reason: raw?.reason,
    groups: arr<SportsLeagueTable>(raw?.groups)
      .filter((g) => typeof g.id === 'string')
      .map((g) => ({
        ...g,
        title: g.title ?? '',
        columns: arr<SportsTableColumn>(g.columns).filter((c) => typeof c.key === 'string'),
        rows: arr<SportsTableRow>(g.rows)
          .filter((r) => typeof r.id === 'string' && typeof r.name === 'string')
          .map((r) => ({ ...r, kind: r.kind || 'team', values: r.values ?? {} })),
      })),
  } satisfies SportsLeagueStandings;
}

export async function getLeagueStandings(league: string, signal?: AbortSignal) {
  const res = await apiRequest<Partial<SportsLeagueStandings> | null>(
    `/sports/standings/${seg(league)}`,
    { signal },
  );
  return normalizeStandings(res, league);
}

/** The columns a table shows (native SportsStandings.columns), at most `limit`. */
export function standingsColumns(
  league: string,
  table: SportsLeagueTable,
  limit = 6,
): SportsTableColumn[] {
  const order = league.startsWith('soccer-')
    ? ['rank', 'points', 'gamesPlayed', 'pointDifferential', 'overall']
    : league.includes('college')
      ? ['overall', 'vs. Conf.', 'rank', 'playoffSeed', 'winPercent']
      : [
          'rank',
          'playoffSeed',
          'overall',
          'championshipPts',
          'points',
          'winPercent',
          'gamesBehind',
        ];
  const picked = order
    .map((k) => table.columns.find((c) => c.key === k))
    .filter((c): c is SportsTableColumn => !!c);
  return (picked.length ? picked : table.columns).slice(0, limit);
}

/** Rank/seed 0 means none; a missing value is a dash. */
export function standingsValue(row: SportsTableRow, key: string): string {
  const v = row.values[key];
  if ((key === 'rank' || key === 'playoffSeed') && v === '0') return '–';
  return v ?? '–';
}

function normalizeMotoGP(raw: Partial<MotoGPEnrichment> | null): MotoGPEnrichment {
  return {
    state: raw?.state || 'unavailable',
    season: raw?.season,
    source: raw?.source,
    sourceUrl: raw?.sourceUrl,
    reason: raw?.reason,
    standings: arr<MotoGPRider>(raw?.standings)
      .filter((r) => typeof r.name === 'string')
      .sort((a, b) => (a.position ?? 999) - (b.position ?? 999)),
    circuit: raw?.circuit ?? undefined,
  };
}

export const getMotoGPStandings = async (signal?: AbortSignal) =>
  normalizeMotoGP(await apiRequest('/sports/motogp/standings', { signal }));

export const getMotoGPCircuit = async (providerEventId: string, signal?: AbortSignal) =>
  normalizeMotoGP(await apiRequest(`/sports/motogp/circuit/${seg(providerEventId)}`, { signal }));

/** Only https maps are shown (native SportsMotoGPCircuit.map). */
export const circuitMapUrl = (c?: MotoGPCircuit) =>
  c?.mapUrl?.startsWith('https://') ? c.mapUrl : undefined;
