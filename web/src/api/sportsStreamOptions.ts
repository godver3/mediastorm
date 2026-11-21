import { apiRequest } from './client';
import { sportsKeys, type SportsStreamMatch } from './sports';

/**
 * A Stremio channel among the sports matches lists several streams (GET /api/live/stremio/streams);
 * the picker shows one row per stream and tunes it with `stremioStreamIndex`. Mirrors native-tvos
 * Views/Sports/SportsStreamOptions.swift and APIClient+Live.liveStremioStreams.
 */

export interface LiveStremioStream {
  index: number;
  label?: string;
  name?: string;
  title?: string;
  description?: string;
}

export const stremioStreamsKey = (profileId: string, url: string) =>
  [...sportsKeys.all, profileId, 'stremio-streams', url] as const;

/** A Stremio stream resource URL (…/stream/{type}/{id}.json), not a playable one. */
export function hasStremioSources(url: string): boolean {
  const path = url.split('?')[0] ?? url;
  return url.startsWith('http') && path.includes('/stream/') && path.endsWith('.json');
}

export async function getStremioStreams(
  profileId: string,
  url: string,
  signal?: AbortSignal,
): Promise<LiveStremioStream[]> {
  const res = await apiRequest<{ streams?: LiveStremioStream[] | null }>('/live/stremio/streams', {
    query: { url, profileId },
    signal,
  });
  const seen = new Set<number>();
  return (res?.streams ?? []).filter(
    (s) =>
      !!s &&
      typeof s.index === 'number' &&
      s.index >= 0 &&
      !seen.has(s.index) &&
      !!seen.add(s.index),
  );
}

export function streamLabel(s: LiveStremioStream): string {
  for (const t of [s.label, s.title, s.name]) {
    const v = t?.trim();
    if (v) return v;
  }
  return `Feed ${s.index + 1}`;
}

/** The match as one of its channel's streams. */
export const withStream = (m: SportsStreamMatch, s: LiveStremioStream): SportsStreamMatch => ({
  ...m,
  stremioStreamIndex: s.index,
  streamLabel: streamLabel(s),
});

export const streamIdentity = (m: SportsStreamMatch) =>
  `${m.sourceId ?? ''}|${m.channelUrl}|${m.stremioStreamIndex ?? 'channel'}`;

/**
 * Rows for the matches given the lookups so far (by channel URL): a Stremio channel with streams
 * becomes one row per stream; otherwise the plain row (the server picks). Repeats dropped.
 */
export function expandStreamRows<T extends SportsStreamMatch>(
  matches: T[],
  lookups: Record<string, LiveStremioStream[] | undefined>,
): T[] {
  const rows = matches.flatMap((m) => {
    const streams = hasStremioSources(m.channelUrl) ? lookups[m.channelUrl] : undefined;
    return streams && streams.length > 0 ? streams.map((s) => withStream(m, s) as T) : [m];
  });
  const seen = new Set<string>();
  return rows.filter((r) => {
    const id = streamIdentity(r);
    if (seen.has(id)) return false;
    seen.add(id);
    return true;
  });
}
