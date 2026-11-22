import { apiRequest } from './client';
import type { EPGProgram, LiveChannel } from './live';

/**
 * Live TV recordings and recording rules. Mirrors native-tvos
 * StrmrTV/API/APIClient+Recordings.swift, APIClient+RecordingRules.swift,
 * RecordingModels.swift and RecordingRule+Matching.swift.
 */

export type RecordingStatus =
  'pending' | 'starting' | 'running' | 'completed' | 'cancelled' | 'failed' | 'unknown';

export interface Recording {
  id: string;
  ruleId?: string;
  status: RecordingStatus;
  channelId: string;
  tvgId?: string;
  channelName: string;
  title: string;
  description?: string;
  /** ISO strings as the server sent them (playback.html wants recordingEndsAt verbatim). */
  startAt: string;
  endAt: string;
  paddingAfterSeconds?: number;
  outputPath?: string;
  outputSizeBytes?: number;
  actualStartAt?: string;
  error?: string;
}

export interface RecordingRule {
  id: string;
  matchType: 'title' | 'regex' | string;
  pattern: string;
  channelId?: string;
  tvgId?: string;
  channelName?: string;
  allChannels: boolean;
  enabled: boolean;
  newOnly: boolean;
  paddingBeforeSeconds: number;
  paddingAfterSeconds: number;
}

export interface RecordingSettings {
  paddingBeforeSeconds: number;
  paddingAfterSeconds: number;
}

const STATUSES: RecordingStatus[] = [
  'pending',
  'starting',
  'running',
  'completed',
  'cancelled',
  'failed',
];

const str = (v: unknown) => (typeof v === 'string' && v !== '' ? v : undefined);
const num = (v: unknown) => (typeof v === 'number' && Number.isFinite(v) ? v : undefined);

export function mapRecording(raw: unknown): Recording | null {
  const r = (raw ?? {}) as Record<string, unknown>;
  const id = str(r.id);
  if (!id) return null;
  const status = STATUSES.includes(r.status as RecordingStatus)
    ? (r.status as RecordingStatus)
    : 'unknown';
  return {
    id,
    ruleId: str(r.ruleId),
    status,
    channelId: str(r.channelId) ?? '',
    tvgId: str(r.tvgId),
    channelName: str(r.channelName) ?? '',
    title: str(r.title) ?? '',
    description: str(r.description),
    startAt: str(r.startAt) ?? '',
    endAt: str(r.endAt) ?? '',
    paddingAfterSeconds: num(r.paddingAfterSeconds),
    outputPath: str(r.outputPath),
    outputSizeBytes: num(r.outputSizeBytes),
    actualStartAt: str(r.actualStartAt),
    error: str(r.error),
  };
}

export function mapRule(raw: unknown): RecordingRule | null {
  const r = (raw ?? {}) as Record<string, unknown>;
  const id = str(r.id);
  if (!id) return null;
  return {
    id,
    matchType: str(r.matchType) ?? 'title',
    pattern: typeof r.pattern === 'string' ? r.pattern : '',
    channelId: str(r.channelId),
    tvgId: str(r.tvgId),
    channelName: str(r.channelName),
    allChannels: r.allChannels === true,
    enabled: r.enabled !== false,
    newOnly: r.newOnly === true,
    paddingBeforeSeconds: num(r.paddingBeforeSeconds) ?? 0,
    paddingAfterSeconds: num(r.paddingAfterSeconds) ?? 0,
  };
}

// ---------- derived (native RecordingModels) ----------

export const STATUS_LABELS: Record<RecordingStatus, string> = {
  pending: 'Scheduled',
  starting: 'Starting',
  running: 'Recording',
  completed: 'Completed',
  cancelled: 'Cancelled',
  failed: 'Failed',
  unknown: 'Failed',
};

const ms = (iso: string | undefined) => {
  const t = iso ? Date.parse(iso) : NaN;
  return Number.isFinite(t) ? t : 0;
};
export const startMs = (r: Recording) => ms(r.startAt);
export const endMs = (r: Recording) => ms(r.endAt);

/** A file to play: completed, still recording, or stopped / failed part way. */
export const canPlay = (r: Recording) =>
  !!r.outputPath && ['completed', 'running', 'cancelled', 'failed'].includes(r.status);
/** Cancel a scheduled recording, or stop one in progress. */
export const canCancel = (r: Recording) => ['pending', 'starting', 'running'].includes(r.status);
/** Everything but a scheduled recording a rule made (the rule owns those). */
export const canDelete = (r: Recording) => !(r.ruleId && r.status === 'pending');

const clean = (v: string) => v.replace(/\+/g, ' ').split(/\s+/).filter(Boolean).join(' ');

/** Programme title, else the one in the file name, else the channel (native displayTitle). */
export function displayTitle(r: Pick<Recording, 'title' | 'channelName' | 'outputPath'>): string {
  const channel = clean(r.channelName) || 'Recording';
  const raw = clean(r.title);
  if (raw && raw !== channel) return raw;
  const name = r.outputPath?.split('/').pop();
  if (name) {
    const stem = name.replace(/\.[^.]+$/, '');
    for (const re of [
      /^.+ - \d{4}-\d{2}-\d{2} \d{4}-\d{4} - (.+)$/i,
      /^.+ - \d{4}-\d{2}-\d{2} \d{4} - (.+)$/i,
    ]) {
      const m = stem.match(re);
      const derived = m?.[1] ? clean(m[1]) : '';
      if (derived && derived !== channel) return derived;
    }
  }
  return raw || channel;
}

/** The error worth showing (a paused or removed rule's cancellations aren't). */
export function visibleError(r: Pick<Recording, 'error'>): string | undefined {
  const e = r.error;
  if (!e || e === 'recording rule paused' || e.startsWith('cancelled because the recording rule'))
    return undefined;
  return e;
}

/** "45m", "1h 30m", "2h". */
export function durationText(r: Recording): string {
  const minutes = Math.round((endMs(r) - startMs(r)) / 60000);
  if (minutes <= 0) return '';
  if (minutes < 60) return `${minutes}m`;
  return minutes % 60 ? `${Math.floor(minutes / 60)}h ${minutes % 60}m` : `${minutes / 60}h`;
}

export function sizeText(bytes: number | undefined): string | undefined {
  if (!bytes || bytes <= 0) return undefined;
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let v = bytes;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i++;
  }
  return `${v >= 100 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

export const RECORDING_FILTERS = ['all', 'upcoming', 'recording', 'completed', 'history'] as const;
export type RecordingFilter = (typeof RECORDING_FILTERS)[number];

export const FILTER_LABELS: Record<RecordingFilter, string> = {
  all: 'All',
  upcoming: 'Upcoming',
  recording: 'Recording',
  completed: 'Completed',
  history: 'History',
};

export const FILTER_EMPTY: Record<RecordingFilter, string> = {
  all: 'No recordings',
  upcoming: 'Nothing scheduled',
  recording: 'Nothing recording right now',
  completed: 'No completed recordings',
  history: 'No failed or cancelled recordings',
};

export function matchesFilter(f: RecordingFilter, r: Recording): boolean {
  switch (f) {
    case 'all':
      return true;
    case 'upcoming':
      return r.status === 'pending';
    case 'recording':
      return r.status === 'starting' || r.status === 'running';
    case 'completed':
      return r.status === 'completed';
    case 'history':
      return ['failed', 'cancelled', 'unknown'].includes(r.status);
  }
}

/** In progress first, then upcoming soonest first, then the rest newest first. */
export function sortRecordings(list: Recording[]): Recording[] {
  const rank = (r: Recording) =>
    r.status === 'running' || r.status === 'starting' ? 0 : r.status === 'pending' ? 1 : 2;
  return [...list].sort((a, b) => {
    const d = rank(a) - rank(b);
    if (d) return d;
    return rank(a) === 1 ? startMs(a) - startMs(b) : startMs(b) - startMs(a);
  });
}

// ---------- rule matching (native RecordingRule+Matching) ----------

/** Lower-cased letters and digits, other runs collapsed to a space (server normalizeRecordingTitle). */
export function normalizedTitle(value: string): string {
  return value
    .toLowerCase()
    .replace(/[^\p{L}\p{Nd}]+/gu, ' ')
    .trim();
}

export function ruleCovers(
  rule: RecordingRule,
  title: string,
  channel: Pick<LiveChannel, 'id' | 'tvgId'>,
): boolean {
  const eq = (a?: string, b?: string) => !!a && !!b && a.toLowerCase() === b.toLowerCase();
  const onChannel =
    rule.allChannels || eq(rule.channelId, channel.id) || eq(rule.tvgId, channel.tvgId);
  if (!onChannel) return false;
  if (rule.matchType === 'regex') {
    try {
      return new RegExp(rule.pattern, 'i').test(title);
    } catch {
      return false;
    }
  }
  return normalizedTitle(rule.pattern) === normalizedTitle(title);
}

export function seriesRuleFor(
  rules: RecordingRule[],
  title: string,
  channel: Pick<LiveChannel, 'id' | 'tvgId'>,
): RecordingRule | undefined {
  const matches = rules.filter((r) => ruleCovers(r, title, channel));
  return matches.find((r) => r.matchType !== 'regex') ?? matches[0];
}

/** The active recording covering the programme (or now) on the channel; running ones first. */
export function recordingFor(
  recordings: Recording[],
  channel: Pick<LiveChannel, 'id'>,
  program?: Pick<EPGProgram, 'start' | 'stop'>,
  now = Date.now(),
): Recording | undefined {
  const matches = recordings.filter((r) => {
    if (r.channelId !== channel.id || !canCancel(r)) return false;
    if (program && program.stop > program.start)
      return startMs(r) < program.stop && endMs(r) > program.start;
    return startMs(r) <= now && endMs(r) > now;
  });
  return matches.find((r) => r.status === 'running') ?? matches[0];
}

export const ruleSummary = (r: RecordingRule) =>
  `“${r.pattern}” on ${r.allChannels ? 'all channels' : (r.channelName ?? 'this channel')}`;

// ---------- query keys ----------

export const recordingKeys = {
  all: (profileId: string) => ['recordings', profileId] as const,
  list: (profileId: string) => [...recordingKeys.all(profileId), 'list'] as const,
  active: (profileId: string) => [...recordingKeys.all(profileId), 'active'] as const,
  rules: (profileId: string) => [...recordingKeys.all(profileId), 'rules'] as const,
  settings: (profileId: string) => [...recordingKeys.all(profileId), 'settings'] as const,
};

// ---------- requests ----------

const seg = encodeURIComponent;

async function getRecordings(profileId: string, status?: string, signal?: AbortSignal) {
  const raw = await apiRequest<{ recordings?: unknown[] } | null>('/live/recordings', {
    query: { profileId, status },
    signal,
  });
  return (raw?.recordings ?? []).map(mapRecording).filter((r): r is Recording => !!r);
}

export const fetchRecordings = (profileId: string, signal?: AbortSignal) =>
  getRecordings(profileId, undefined, signal);

/** Scheduled, starting and recording ones, which the guide marks. */
export const fetchActiveRecordings = (profileId: string, signal?: AbortSignal) =>
  getRecordings(profileId, 'pending,starting,running', signal);

export const cancelRecording = (id: string) =>
  apiRequest(`/live/recordings/${seg(id)}/cancel`, { method: 'POST' });

export const deleteRecording = (id: string) =>
  apiRequest(`/live/recordings/${seg(id)}`, { method: 'DELETE' });

function recordingBody(
  profileId: string,
  channel: LiveChannel,
  title: string,
  start: number,
  stop: number,
  description?: string,
) {
  const body: Record<string, unknown> = {
    profileId,
    channelId: channel.id,
    channelName: channel.name,
    title,
    sourceUrl: channel.url,
    start: new Date(start).toISOString(),
    stop: new Date(stop).toISOString(),
  };
  if (channel.tvgId) body.tvgId = channel.tvgId;
  if (description) body.description = description;
  return body;
}

/** Records one programme; the server adds the profile's padding. */
export const recordProgram = (profileId: string, program: EPGProgram, channel: LiveChannel) =>
  apiRequest('/live/recordings/epg', {
    method: 'POST',
    body: recordingBody(
      profileId,
      channel,
      program.title,
      program.start,
      program.stop,
      program.description,
    ),
  });

/** Records the channel from now for `durationMs`. */
export function recordTimeBlock(
  profileId: string,
  channel: LiveChannel,
  durationMs: number,
  now = Date.now(),
) {
  return apiRequest('/live/recordings/time-block', {
    method: 'POST',
    body: recordingBody(
      profileId,
      channel,
      `${channel.name} Live Block`,
      now,
      now + durationMs,
      'Scheduled from the Live TV action menu.',
    ),
  });
}

/** Every airing of `title` on this channel (a channel-scoped title rule). */
export function recordSeries(
  profileId: string,
  title: string,
  channel: LiveChannel,
  newOnly = false,
) {
  const body: Record<string, unknown> = {
    profileId,
    matchType: 'title',
    pattern: title,
    channelId: channel.id,
    channelName: channel.name,
    newOnly,
  };
  if (channel.tvgId) body.tvgId = channel.tvgId;
  return apiRequest('/live/recording-rules', { method: 'POST', body });
}

export async function fetchRules(profileId: string, signal?: AbortSignal) {
  const raw = await apiRequest<{ rules?: unknown[] } | null>('/live/recording-rules', {
    query: { profileId },
    signal,
  });
  return (raw?.rules ?? []).map(mapRule).filter((r): r is RecordingRule => !!r);
}

/** A rule recording every guide title matching `pattern` (regex, ignoring case) on any channel. */
export const createRegexRule = (profileId: string, pattern: string) =>
  apiRequest('/live/recording-rules', {
    method: 'POST',
    body: { profileId, matchType: 'regex', pattern, allChannels: true },
  });

export interface RuleUpdate {
  enabled?: boolean;
  newOnly?: boolean;
  paddingBeforeSeconds?: number;
  paddingAfterSeconds?: number;
}

export const updateRule = (profileId: string, id: string, patch: RuleUpdate) =>
  apiRequest(`/live/recording-rules/${seg(id)}`, {
    method: 'PUT',
    body: { profileId, ...patch },
  });

/** Deletes a rule; the server cancels the upcoming recordings it scheduled. */
export const deleteRule = (id: string) =>
  apiRequest(`/live/recording-rules/${seg(id)}`, { method: 'DELETE' });

export async function fetchRecordingSettings(
  profileId: string,
  signal?: AbortSignal,
): Promise<RecordingSettings> {
  const raw = await apiRequest<Partial<RecordingSettings> | null>('/live/recording-settings', {
    query: { profileId },
    signal,
  });
  return {
    paddingBeforeSeconds: num(raw?.paddingBeforeSeconds) ?? 0,
    paddingAfterSeconds: num(raw?.paddingAfterSeconds) ?? 0,
  };
}

export const saveRecordingSettings = (profileId: string, s: RecordingSettings) =>
  apiRequest('/live/recording-settings', { method: 'PUT', body: { profileId, ...s } });

/** playback.html params for a recording (mirrors Expo NewRecordingsScreen's web handoff). */
export function recordingPlaybackParams(r: Recording) {
  const title = displayTitle(r);
  return {
    sourcePath: `recording:${r.id}/recording.ts`,
    mediaType: 'movie',
    title,
    displayName: `${title}.ts`,
    growingRecording: r.status === 'running' ? '1' : undefined,
    recordingEndsAt: r.endAt || undefined,
  };
}
