import { apiRequest } from './client';

/**
 * Live TV API. Mirrors native-tvos StrmrTV/API/APIClient+Live.swift and LiveModels.swift.
 * Every call names the profile (`profileId`) like the native clients.
 */

export interface LiveChannel {
  /** `{sourceId}:{rawId}` with several sources; favorites / hidden are stored by this. */
  id: string;
  name: string;
  /** Provider URL that /api/live/hls/start tunes. */
  url: string;
  logo?: string;
  group?: string;
  tvgId?: string;
  tvgName?: string;
  sourceId?: string;
  sourceName?: string;
  /** Provider-local id, sent as `channelId` when tuning. */
  playbackId?: string;
}

export interface LiveSource {
  id: string;
  name: string;
}

export interface LiveChannelsPage {
  channels: LiveChannel[];
  total: number;
  hasMore: boolean;
  availableCategories: string[];
  sources: LiveSource[];
}

export interface EPGProgram {
  title: string;
  description?: string;
  /** Epoch ms (the server has already applied the profile's offset). */
  start: number;
  stop: number;
  icon?: string;
  categories?: string[];
  episode?: string;
  isNew: boolean;
  isRerun: boolean;
}

export interface EPGNowPlaying {
  channelId: string;
  current?: EPGProgram;
  next?: EPGProgram;
}

export interface EPGStatus {
  enabled: boolean;
  refreshing?: boolean;
  lastRefresh?: string;
  lastError?: string;
}

export interface LiveStart {
  streamUrl?: string;
  isDirect?: boolean;
  sessionId?: string;
  playlistUrl?: string;
  stremioStreamIndex?: number;
  stremioStreamIndexes?: number[];
}

// ---------- mappers ----------

const str = (v: unknown): string | undefined =>
  typeof v === 'string' && v.trim() !== '' ? v : undefined;

/** Playlists often link GitHub's HTML page for an image; rewrite it to the raw file (native logoURL). */
export function normalizeLogoUrl(logo: string | undefined): string | undefined {
  if (!logo) return undefined;
  if (logo.startsWith('https://github.com/') && logo.includes('/blob/')) {
    return logo
      .replace('https://github.com/', 'https://raw.githubusercontent.com/')
      .replace('/blob/', '/');
  }
  return logo;
}

export function mapChannel(raw: unknown): LiveChannel | null {
  const r = (raw ?? {}) as Record<string, unknown>;
  const id = str(r.id);
  const url = str(r.url);
  if (!id || !url) return null;
  return {
    id,
    name: str(r.name) ?? id,
    url,
    logo: normalizeLogoUrl(str(r.logo)),
    group: str(r.group),
    tvgId: str(r.tvgId),
    tvgName: str(r.tvgName),
    sourceId: str(r.sourceId),
    sourceName: str(r.sourceName),
    playbackId: str(r.playbackId),
  };
}

/** The key EPG data is filed under: tvgId, else the channel id (native guideKey). */
export const guideKey = (c: Pick<LiveChannel, 'id' | 'tvgId'>) => c.tvgId || c.id;

const time = (v: unknown): number => {
  const t = typeof v === 'string' ? Date.parse(v) : NaN;
  return Number.isFinite(t) ? t : NaN;
};

export function mapProgram(raw: unknown): EPGProgram | null {
  const r = (raw ?? {}) as Record<string, unknown>;
  const title = str(r.title);
  const start = time(r.start);
  const stop = time(r.stop);
  if (!title || !Number.isFinite(start) || !Number.isFinite(stop)) return null;
  return {
    title,
    description: str(r.description),
    start,
    stop,
    icon: str(r.icon),
    categories: Array.isArray(r.categories)
      ? r.categories.filter((c): c is string => typeof c === 'string')
      : undefined,
    episode: str(r.episode),
    isNew: r.new === true,
    isRerun: r.previouslyShown === true,
  };
}

/** Sorted by start, one per start time, each cut off where the next begins (native merge). */
export function normalizeSchedule(programs: EPGProgram[]): EPGProgram[] {
  const byStart = new Map<number, EPGProgram>();
  for (const p of programs) byStart.set(p.start, p);
  const sorted = [...byStart.values()].sort((a, b) => a.start - b.start);
  return sorted.map((p, i) => {
    const next = sorted[i + 1];
    return next && next.start < p.stop ? { ...p, stop: next.start } : p;
  });
}

export const isAiring = (p: Pick<EPGProgram, 'start' | 'stop'>, now = Date.now()) =>
  p.start <= now && now < p.stop;

export const programProgress = (p: Pick<EPGProgram, 'start' | 'stop'>, now = Date.now()) => {
  const len = p.stop - p.start;
  return len > 0 ? Math.min(Math.max((now - p.start) / len, 0), 1) : 0;
};

// ---------- query keys ----------

export interface ChannelQuery {
  sourceId?: string;
  categories: string[];
  favoritesOnly: boolean;
  search: string;
}

export const liveKeys = {
  all: (profileId: string) => ['live', profileId] as const,
  channels: (profileId: string, q: ChannelQuery, favorites: string[]) =>
    [...liveKeys.all(profileId), 'channels', q, q.favoritesOnly ? favorites : []] as const,
  epgStatus: (profileId: string) => [...liveKeys.all(profileId), 'epg-status'] as const,
  nowNext: (profileId: string, keys: string[]) =>
    [...liveKeys.all(profileId), 'now', keys] as const,
  schedule: (profileId: string, keys: string[], startOffset: number, hours: number) =>
    [...liveKeys.all(profileId), 'schedule', keys, startOffset, hours] as const,
  lists: (profileId: string) => [...liveKeys.all(profileId), 'lists'] as const,
};

// ---------- requests ----------

export const CHANNEL_PAGE_SIZE = 250;

export async function fetchChannels(
  profileId: string,
  q: ChannelQuery,
  favoriteIds: string[],
  offset: number,
  signal?: AbortSignal,
): Promise<LiveChannelsPage> {
  const body: Record<string, unknown> = {
    profileId,
    offset,
    limit: CHANNEL_PAGE_SIZE,
    favoriteIds: q.favoritesOnly ? favoriteIds : [],
    favoritesOnly: q.favoritesOnly,
  };
  if (q.sourceId) body.sourceId = q.sourceId;
  if (q.categories.length) body.categories = q.categories;
  if (q.search) body.filter = q.search;
  const raw = await apiRequest<Record<string, unknown>>('/live/channels', {
    method: 'POST',
    body,
    signal,
  });
  return mapChannelsPage(raw);
}

export function mapChannelsPage(raw: Record<string, unknown> | null): LiveChannelsPage {
  const r = raw ?? {};
  const channels = (Array.isArray(r.channels) ? r.channels : [])
    .map(mapChannel)
    .filter((c): c is LiveChannel => c !== null);
  const sources = (Array.isArray(r.sources) ? r.sources : [])
    .map((s) => s as Record<string, unknown>)
    .filter((s) => str(s.id))
    .map((s) => ({ id: String(s.id), name: str(s.name) ?? String(s.id) }));
  return {
    channels,
    total: typeof r.total === 'number' ? r.total : channels.length,
    hasMore: r.hasMore === true,
    availableCategories: Array.isArray(r.availableCategories)
      ? r.availableCategories.filter((c): c is string => typeof c === 'string')
      : [],
    sources,
  };
}

export async function fetchEpgStatus(profileId: string, signal?: AbortSignal): Promise<EPGStatus> {
  const raw = await apiRequest<Partial<EPGStatus> | null>('/live/epg/status', {
    query: { profileId },
    signal,
  });
  return { ...raw, enabled: raw?.enabled === true };
}

/** Now / next for up to 50 EPG keys. */
export async function fetchNowNext(
  profileId: string,
  keys: string[],
  signal?: AbortSignal,
): Promise<Record<string, EPGNowPlaying>> {
  if (!keys.length) return {};
  const raw = await apiRequest<unknown[] | null>('/live/epg/now', {
    method: 'POST',
    query: { profileId },
    body: { channels: keys },
    signal,
  });
  const out: Record<string, EPGNowPlaying> = {};
  for (const item of Array.isArray(raw) ? raw : []) {
    const r = item as Record<string, unknown>;
    const channelId = str(r.channelId);
    if (!channelId) continue;
    out[channelId] = {
      channelId,
      current: mapProgram(r.current) ?? undefined,
      next: mapProgram(r.next) ?? undefined,
    };
  }
  return out;
}

/** Programmes overlapping [now + startOffset min, + hours] per key. */
export async function fetchSchedule(
  profileId: string,
  keys: string[],
  startOffsetMinutes: number,
  hours: number,
  signal?: AbortSignal,
): Promise<Record<string, EPGProgram[]>> {
  if (!keys.length) return {};
  const raw = await apiRequest<Record<string, unknown[] | null> | null>(
    '/live/epg/schedule/batch',
    {
      query: {
        channels: keys.join(','),
        hours,
        startOffset: startOffsetMinutes,
        profileId,
      },
      signal,
    },
  );
  const out: Record<string, EPGProgram[]> = {};
  for (const key of keys) {
    const list = raw?.[key];
    out[key] = normalizeSchedule(
      (Array.isArray(list) ? list : []).map(mapProgram).filter((p): p is EPGProgram => !!p),
    );
  }
  return out;
}

// ---------- favorites / hidden (profile settings `liveTV`) ----------

export interface LiveChannelLists {
  favorites: string[];
  hidden: string[];
}

const settingsPath = (profileId: string) => `/users/${encodeURIComponent(profileId)}/settings`;

export async function fetchChannelLists(
  profileId: string,
  signal?: AbortSignal,
): Promise<LiveChannelLists> {
  const settings = await apiRequest<Record<string, unknown> | null>(settingsPath(profileId), {
    signal,
  });
  const live = (settings?.liveTV ?? {}) as Record<string, unknown>;
  const list = (v: unknown) =>
    Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
  return { favorites: list(live.favoriteChannels), hidden: list(live.hiddenChannels) };
}

/** The settings PUT replaces the whole document: re-read and change only these keys (as native). */
export async function saveChannelLists(profileId: string, lists: LiveChannelLists): Promise<void> {
  const settings =
    (await apiRequest<Record<string, unknown> | null>(settingsPath(profileId))) ?? {};
  const live = { ...((settings.liveTV ?? {}) as Record<string, unknown>) };
  live.favoriteChannels = lists.favorites;
  live.hiddenChannels = lists.hidden;
  await apiRequest(settingsPath(profileId), {
    method: 'PUT',
    body: { ...settings, liveTV: live },
  });
}

// ---------- tuning ----------

/** The /api/live/hls/start query for the web player (always an HLS session, as Expo web). */
export function liveStartQuery(
  channel: LiveChannel,
  profileId: string,
  opts: { programTitle?: string; profileName?: string } = {},
): Record<string, string> {
  const q: Record<string, string> = {
    url: channel.url,
    target: 'web',
    format: 'hls',
    profileId,
    mediaType: 'channel',
    title: opts.programTitle || channel.name,
    channelId: channel.playbackId ?? channel.id,
    channelName: channel.name,
    liveSourceUrl: channel.url,
  };
  if (opts.profileName) q.profileName = opts.profileName;
  if (channel.sourceId) {
    q.sourceId = channel.sourceId;
    q.liveSourceId = channel.sourceId;
  }
  q.itemId = channel.tvgId ?? channel.id;
  if (channel.tvgName) q.tvgName = channel.tvgName;
  if (channel.group) q.category = channel.group;
  if (channel.logo) q.liveChannelLogo = channel.logo;
  return q;
}

export const startLive = (query: Record<string, string>) =>
  apiRequest<LiveStart>('/live/hls/start', { query });

/**
 * playback.html params for a tuned channel (mirrors Expo NewLiveScreen's web handoff):
 * `movie` is the server-relative playlist / stream path (playback.html adds /api + token).
 */
export function livePlaybackParams(channel: LiveChannel, start: LiveStart) {
  const path = start.isDirect ? start.streamUrl : start.playlistUrl;
  if (!path) throw new Error('The server did not return a stream for this channel.');
  return {
    movie: path,
    hlsSessionId: start.isDirect ? undefined : start.sessionId,
    headerImage: channel.logo,
    title: channel.name,
    mediaType: 'channel',
    preferSystemPlayer: '1',
    tvgId: channel.tvgId,
    liveSourceUrl: channel.url,
    liveSourceId: channel.sourceId,
    livePlaybackId: channel.playbackId ?? channel.id,
    stremioStreamIndex:
      typeof start.stremioStreamIndex === 'number' ? String(start.stremioStreamIndex) : undefined,
    stremioStreamIndexes: start.stremioStreamIndexes?.length
      ? JSON.stringify(start.stremioStreamIndexes)
      : undefined,
  };
}

/** Readable message for a failed tune; 429 carries the provider's stream limit (as Expo). */
export function liveStartErrorMessage(err: unknown): string {
  const e = err as { status?: number; body?: unknown; message?: string } | null;
  if (e?.status === 429) {
    const b = (e.body ?? {}) as Record<string, unknown>;
    if (b.currentStreams != null && b.maxStreams != null) {
      const provider = String(b.provider ?? '').toUpperCase() || 'Provider';
      return `${provider} limit reached (${String(b.currentStreams)}/${String(b.maxStreams)})`;
    }
    return 'Live stream limit reached';
  }
  return e?.message || 'Could not start this channel.';
}
