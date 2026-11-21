/**
 * Search, Top 10 Today and people. Mirrors native-tvos APIClient+Search.swift,
 * Views/Search/SearchRanking.swift + SearchStore.swift, APIClient+Related.swift
 * (person) and native-androidtv mobile RecentSearches.kt.
 */
import { apiRequest } from './client';
import type { Title } from './titles';

export interface SearchResult {
  title: Title;
  score?: number;
}

export type SearchType = 'movie' | 'series' | 'person';
export const SEARCH_TYPES: SearchType[] = ['movie', 'series', 'person'];
export const MIN_QUERY_LENGTH = 2;
export const MAX_QUERY_LENGTH = 40;
export const SEARCH_DEBOUNCE_MS = 400;

export const searchKeys = {
  all: ['search'] as const,
  query: (profileId: string, q: string) => [...searchKeys.all, profileId, 'q', q] as const,
  topTen: (profileId: string) => [...searchKeys.all, profileId, 'top-ten'] as const,
  person: (id: number) => [...searchKeys.all, 'person', id] as const,
};

/** GET /api/search?q=&type=&userId= (the server drops people for kids profiles). */
export async function searchTitles(
  profileId: string,
  q: string,
  type: SearchType,
  signal?: AbortSignal,
) {
  return (
    (await apiRequest<SearchResult[] | null>('/search', {
      query: { q, type, userId: profileId },
      signal,
    })) ?? []
  );
}

export async function getTopTen(profileId: string, signal?: AbortSignal): Promise<Title[]> {
  const page = await apiRequest<{ items?: { rank: number; title: Title }[] | null }>(
    '/discover/top-ten',
    { query: { userId: profileId }, signal },
  );
  return (page?.items ?? []).map((i) => i.title).filter(Boolean);
}

export interface PersonDetails {
  person: {
    id: number;
    name: string;
    biography?: string;
    profileUrl?: string;
    knownFor?: string;
  };
  filmography?: Title[] | null;
}

export const getPerson = (id: number, signal?: AbortSignal) =>
  apiRequest<PersonDetails>('/metadata/person', { query: { id }, signal });

/** NFKC, trimmed, inner whitespace collapsed, lower-cased. */
export const normalizedName = (name: string) =>
  name.normalize('NFKC').trim().split(/\s+/).join(' ').toLowerCase();

const isExactPerson = (t: Title, q: string) =>
  t.mediaType === 'person' && normalizedName(t.name) === normalizedName(q);

/**
 * One result per title id (higher score wins), exact person matches first,
 * then by score; ties keep arrival order (SearchRanking.merge).
 */
export function mergeResults(results: SearchResult[], query: string): SearchResult[] {
  const order: string[] = [];
  const best = new Map<string, SearchResult>();
  for (const r of results) {
    const t = r.title;
    const key = t.id || `${t.mediaType}-${t.name.toLowerCase()}-${t.year ?? 'unknown'}`;
    const existing = best.get(key);
    if (existing) {
      if ((existing.score ?? 0) < (r.score ?? 0)) best.set(key, r);
    } else {
      order.push(key);
      best.set(key, r);
    }
  }
  return order
    .map((k, i) => ({ r: best.get(k)!, i }))
    .sort((a, b) => {
      const ea = isExactPerson(a.r.title, query);
      const eb = isExactPerson(b.r.title, query);
      if (ea !== eb) return ea ? -1 : 1;
      const sa = a.r.score ?? 0;
      const sb = b.r.score ?? 0;
      return sa !== sb ? sb - sa : a.i - b.i;
    })
    .map((x) => x.r);
}

/** Notice for searches that failed; undefined when none did. */
export function failureNotice(failed: Set<SearchType>): string | undefined {
  if (failed.size === 0) return undefined;
  if (failed.size === SEARCH_TYPES.length) return 'Search couldn’t complete. Please try again.';
  const names = (
    [
      ['movie', 'Movie'],
      ['series', 'TV show'],
      ['person', 'Actor'],
    ] as const
  )
    .filter(([t]) => failed.has(t))
    .map(([, n]) => n)
    .join(' and ');
  return `${names} search is unavailable. Results may be incomplete.`;
}

export interface SearchOutcome {
  results: SearchResult[];
  notice?: string;
}

/** Runs the movie, series and person searches together and merges them. */
export async function runSearch(
  profileId: string,
  query: string,
  signal?: AbortSignal,
): Promise<SearchOutcome> {
  const outcomes = await Promise.allSettled(
    SEARCH_TYPES.map((t) => searchTitles(profileId, query, t, signal)),
  );
  const failed = new Set<SearchType>();
  const collected: SearchResult[] = [];
  outcomes.forEach((o, i) => {
    if (o.status === 'fulfilled') collected.push(...o.value.filter((r) => r?.title));
    else failed.add(SEARCH_TYPES[i]!);
  });
  if (failed.size === SEARCH_TYPES.length) {
    const first = outcomes[0] as PromiseRejectedResult;
    if (signal?.aborted) throw first.reason;
  }
  return { results: mergeResults(collected, query), notice: failureNotice(failed) };
}

// ---------------------------------------------------------------------------
// Recent searches (per profile, this browser; 8 newest, as Expo/Android)
// ---------------------------------------------------------------------------

export const RECENT_LIMIT = 8;
const recentKey = (profileId: string) => `msweb.recentSearches.${profileId}`;

export function addingRecent(items: string[], query: string): string[] {
  const q = query.trim();
  if (!q) return items;
  return [q, ...items.filter((i) => i.toLowerCase() !== q.toLowerCase())].slice(0, RECENT_LIMIT);
}

export function readRecent(profileId: string): string[] {
  try {
    const raw = localStorage.getItem(recentKey(profileId));
    const parsed: unknown = raw ? JSON.parse(raw) : [];
    return Array.isArray(parsed) ? parsed.filter((s): s is string => typeof s === 'string') : [];
  } catch {
    return [];
  }
}

export function writeRecent(profileId: string, items: string[]) {
  try {
    if (items.length) localStorage.setItem(recentKey(profileId), JSON.stringify(items));
    else localStorage.removeItem(recentKey(profileId));
  } catch {
    /* storage unavailable */
  }
}

/** TMDB person id of a person result (`tmdbId`, else the `tmdb:person:N` id). */
export function personIdFromTitle(t: Pick<Title, 'id' | 'tmdbId'>): number | undefined {
  if (t.tmdbId && t.tmdbId > 0) return t.tmdbId;
  const m = /^tmdb:person:(\d+)$/.exec(t.id);
  return m ? Number(m[1]) : undefined;
}
