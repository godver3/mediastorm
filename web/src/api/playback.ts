/**
 * Stream resolution: prequeue (automatic pick) and manual source search/resolve.
 * Mirrors native-tvos StrmrTV/API/APIClient+Playback.swift + PlaybackModels.swift and
 * native-androidtv core/.../player/PrequeueResolver.kt. Shapes match backend
 * services/playback/prequeue.go (PrequeueRequest / PrequeueStatusResponse).
 */
import { getClientId } from './storage';
import { apiRequest } from './client';
import type { EpisodeNumbering } from './details';

export interface SelectedTrack {
  streamIndex: number;
  typeIndex: number;
  language?: string;
  codec?: string;
  title?: string;
  forced?: boolean;
  bitmap?: boolean;
}

export interface TrackSelection {
  audio?: SelectedTrack;
  subtitle?: SelectedTrack;
  /** `subtitle` restricted to text codecs (what the web player can render). */
  textSubtitle?: SelectedTrack;
  subtitleMode?: string;
}

export interface PrequeueStatus {
  prequeueId: string;
  /** queued | searching | resolving | probing | ready | failed | expired */
  status: string;
  targetEpisode?: { seasonNumber: number; episodeNumber: number };
  progressStage?: string;
  progressDetail?: string;
  streamPath?: string;
  displayName?: string;
  serviceType?: string;
  debridProvider?: string;
  hasDolbyVision?: boolean;
  hasHdr10?: boolean;
  dolbyVisionProfile?: string;
  needsAudioTranscode?: boolean;
  duration?: number;
  /** ffprobe stream index; -1 = default. */
  selectedAudioTrack?: number;
  /** ffprobe stream index; -1 = none. */
  selectedSubtitleTrack?: number;
  trackSelection?: TrackSelection;
  subtitleTracks?: PrequeueSubtitleTrack[];
  audioTracks?: PrequeueAudioTrack[];
  passthroughName?: string;
  resultAttributes?: Record<string, string>;
  selectedResult?: { title?: string; attributes?: Record<string, string> };
  error?: string;
}

export interface PrequeueBody {
  titleId: string;
  titleName: string;
  mediaType: 'movie' | 'series';
  imdbId?: string;
  /** Strings on the wire (PrequeueRequest.TmdbID / TvdbID). */
  tmdbId?: string;
  tvdbId?: string;
  year?: number;
  seasonNumber?: number;
  episodeNumber?: number;
  absoluteEpisodeNumber?: number;
  numbering?: EpisodeNumbering;
  startOffset?: number;
}

export function startPrequeue(profileId: string, body: PrequeueBody, signal?: AbortSignal) {
  return apiRequest<{ prequeueId: string; status: string }>('/playback/prequeue', {
    method: 'POST',
    signal,
    // skipHLS: playback.html creates its own HLS session from the stream path.
    body: { ...body, userId: profileId, clientId: getClientId(), reason: 'details', skipHLS: true },
  });
}

export function getPrequeueStatus(id: string, signal?: AbortSignal) {
  return apiRequest<PrequeueStatus>(`/playback/prequeue/${encodeURIComponent(id)}`, { signal });
}

/** A friendly line for the prequeue's stage (native PlaybackCoordinator.describe). */
export function describePrequeueStage(s: Pick<PrequeueStatus, 'status' | 'progressStage'>) {
  switch (s.progressStage || s.status) {
    case 'queued':
    case 'metadata':
      return 'Starting…';
    case 'ranking':
    case 'preparing_candidates':
      return 'Ranking sources…';
    case 'resolving_candidate':
    case 'waiting_provider':
      return 'Fetching…';
    case 'validating_candidate':
      return 'Checking…';
    case 'analyzing_media':
    case 'probing':
    case 'selecting_tracks':
      return 'Inspecting…';
    case 'preparing_playback':
      return 'Preparing…';
    default:
      return 'Searching…';
  }
}

// ---------------------------------------------------------------- manual sources

export interface SourceHealth {
  /** healthy | cached | unhealthy | not_cached | pending | unknown */
  state: string;
  token?: string;
}

/** One indexer/scraper result. `raw` goes back verbatim to /playback/resolve. */
export interface SourceResult {
  raw: Record<string, unknown>;
  index: number;
  id: string;
  title: string;
  indexer: string;
  sizeBytes: number;
  serviceType: string;
  attributes: Record<string, string>;
  health?: SourceHealth;
}

export function toSourceResult(raw: Record<string, unknown>, index: number): SourceResult | null {
  if (typeof raw.title !== 'string') return null;
  const attrs: Record<string, string> = {};
  for (const [k, v] of Object.entries((raw.attributes as Record<string, unknown>) ?? {})) {
    if (typeof v === 'string' || typeof v === 'number') attrs[k] = String(v);
  }
  const h = raw.sourceHealth as { state?: unknown; token?: unknown } | undefined;
  return {
    raw,
    index,
    id: typeof raw.guid === 'string' && raw.guid ? raw.guid : raw.title,
    title: raw.title,
    indexer: typeof raw.indexer === 'string' ? raw.indexer : '',
    sizeBytes: typeof raw.sizeBytes === 'number' ? raw.sizeBytes : 0,
    serviceType: typeof raw.serviceType === 'string' ? raw.serviceType : '',
    attributes: attrs,
    health:
      h && typeof h.state === 'string'
        ? { state: h.state, token: typeof h.token === 'string' ? h.token : undefined }
        : undefined,
  };
}

/** "Name S01E02" or "Name 2024" (native SourceSearch.query). */
export function sourceQuery(
  name: string,
  year?: number,
  episode?: { season: number; episode: number },
) {
  const pad = (n: number) => String(n).padStart(2, '0');
  if (episode) return `${name} S${pad(episode.season)}E${pad(episode.episode)}`;
  return year ? `${name} ${year}` : name;
}

export async function searchSources(
  profileId: string,
  args: {
    query: string;
    titleId: string;
    mediaType: 'movie' | 'series';
    imdbId?: string;
    year?: number;
    numbering?: EpisodeNumbering;
  },
  signal?: AbortSignal,
): Promise<SourceResult[]> {
  const list = await apiRequest<Record<string, unknown>[] | null>('/indexers/search', {
    signal,
    query: {
      q: args.query,
      titleId: args.titleId,
      mediaType: args.mediaType,
      userId: profileId,
      includeSourceHealth: true,
      limit: 50,
      imdbId: args.imdbId,
      year: args.year,
      episodeNumberingId: args.numbering?.seriesId,
      episodeOrdering: args.numbering?.ordering || undefined,
    },
  });
  return (list ?? [])
    .map((r, i) => toSourceResult(r, i))
    .filter((r): r is SourceResult => r !== null);
}

export function getSourceHealth(token: string, signal?: AbortSignal) {
  return apiRequest<{ complete: boolean; results: { index: number; health: SourceHealth }[] }>(
    '/indexers/source-health',
    { signal, query: { token, waitMs: 20000 } },
  );
}

interface PlaybackResolution {
  queueId?: number;
  webdavPath?: string;
  trackSelection?: TrackSelection;
}

const sleep = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    const t = setTimeout(resolve, ms);
    signal?.addEventListener('abort', () => {
      clearTimeout(t);
      reject(new DOMException('Aborted', 'AbortError'));
    });
  });

/** Resolves a picked result to a stream path, polling the queue when it's asynchronous. */
export async function resolveSource(
  profileId: string,
  result: SourceResult,
  titleId: string,
  startOffset?: number,
  signal?: AbortSignal,
): Promise<{ path: string; tracks?: TrackSelection }> {
  const clientId = getClientId();
  let res = await apiRequest<PlaybackResolution>('/playback/resolve', {
    method: 'POST',
    signal,
    body: {
      result: result.raw,
      profileId,
      titleId,
      clientId,
      includeTrackSelection: true,
      ...(startOffset ? { startOffset } : {}),
    },
  });
  const deadline = Date.now() + 180_000;
  while (!res.webdavPath) {
    if (!res.queueId || res.queueId <= 0 || Date.now() > deadline)
      throw new Error('Source could not be resolved');
    await sleep(1000, signal);
    res = await apiRequest<PlaybackResolution>(`/playback/queue/${res.queueId}`, {
      signal,
      query: { includeTrackSelection: true, profileId, titleId, clientId },
    });
  }
  return { path: res.webdavPath, tracks: res.trackSelection };
}

export { sleep as abortableSleep };

// ---------------------------------------------------------------- details warm-up + tracks

/** A source audio stream (`index` is the ffprobe stream number). */
export interface PrequeueAudioTrack {
  index: number;
  language?: string;
  codec?: string;
  /** Codec profile ("E-AC-3 + Atmos"), newer servers. */
  profile?: string;
  title?: string;
}

export interface PrequeueSubtitleTrack {
  /** 0-based index among subtitle streams. */
  index: number;
  /** ffprobe stream number (what playback.html selects by). */
  absoluteIndex?: number;
  language?: string;
  title?: string;
  codec?: string;
  forced?: boolean;
  isBitmap?: boolean;
}

/** The picked release's name (native PrequeueStatus.releaseTitle). */
export function releaseTitle(s: PrequeueStatus): string | undefined {
  const attrs = s.resultAttributes ?? s.selectedResult?.attributes;
  const passthrough =
    s.passthroughName ?? (attrs?.passthrough_format === 'true' ? attrs?.raw_name : undefined);
  const file = s.streamPath?.split('/').pop();
  for (const c of [passthrough, attrs?.raw_title, s.selectedResult?.title, file, s.displayName]) {
    const line = c?.split('\n')[0]?.trim();
    if (line) return line;
  }
  return undefined;
}

/** Subtitle stream number the player selects by. */
export const subtitleStream = (t: PrequeueSubtitleTrack) => t.absoluteIndex ?? t.index;

/**
 * What Play would pick for this stream (native trackDefaults): the server's selection,
 * else the legacy indexes. Subtitle `undefined` = off.
 */
export function trackDefaults(s: PrequeueStatus): { audio?: number; subtitle?: number } {
  const sel = s.trackSelection;
  if (sel) {
    const sub = sel.subtitleMode === 'off' ? undefined : (sel.subtitle ?? sel.textSubtitle);
    return { audio: sel.audio?.streamIndex, subtitle: sub?.streamIndex };
  }
  const subIdx = s.selectedSubtitleTrack;
  const sub =
    subIdx !== undefined && subIdx >= 0
      ? s.subtitleTracks?.find((t) => t.absoluteIndex === subIdx || t.index === subIdx)
      : undefined;
  const audio = s.selectedAudioTrack;
  return {
    audio: audio !== undefined && audio >= 0 ? audio : undefined,
    subtitle: sub ? subtitleStream(sub) : undefined,
  };
}

/** Picks made in Audio & Subtitles; `subtitle: 'off'` turns them off. */
export interface TrackOverrides {
  audio?: number;
  subtitle?: number | 'off';
}

const LANGUAGES: Record<string, string> = {
  en: 'English',
  eng: 'English',
  es: 'Spanish',
  spa: 'Spanish',
  fr: 'French',
  fre: 'French',
  fra: 'French',
  de: 'German',
  ger: 'German',
  deu: 'German',
  it: 'Italian',
  ita: 'Italian',
  ja: 'Japanese',
  jpn: 'Japanese',
  ko: 'Korean',
  kor: 'Korean',
  zh: 'Chinese',
  chi: 'Chinese',
  zho: 'Chinese',
  pt: 'Portuguese',
  por: 'Portuguese',
  ru: 'Russian',
  rus: 'Russian',
  nl: 'Dutch',
  dut: 'Dutch',
  nld: 'Dutch',
  sv: 'Swedish',
  swe: 'Swedish',
  da: 'Danish',
  dan: 'Danish',
  no: 'Norwegian',
  nor: 'Norwegian',
  fi: 'Finnish',
  fin: 'Finnish',
  pl: 'Polish',
  pol: 'Polish',
  hi: 'Hindi',
  hin: 'Hindi',
  ar: 'Arabic',
  ara: 'Arabic',
  tr: 'Turkish',
  tur: 'Turkish',
  und: 'Unknown',
};

/** "English" for "eng"; the code upper-cased when unknown (native TrackLabels.language). */
export function languageLabel(code?: string): string {
  const c = (code ?? '').trim().toLowerCase();
  if (!c) return 'Unknown';
  return LANGUAGES[c] ?? c.toUpperCase();
}

const CODECS: Record<string, string> = {
  eac3: 'E-AC-3',
  ac3: 'AC-3',
  truehd: 'TrueHD',
  dts: 'DTS',
  aac: 'AAC',
  flac: 'FLAC',
  opus: 'Opus',
  mp3: 'MP3',
  subrip: 'SRT',
  srt: 'SRT',
  ass: 'ASS',
  ssa: 'SSA',
  mov_text: 'Text',
  webvtt: 'WebVTT',
  hdmv_pgs_subtitle: 'PGS',
  dvd_subtitle: 'VobSub',
};

/** "E-AC-3" for "eac3" (native TrackLabels.format). */
export const codecLabel = (codec?: string) => {
  const c = (codec ?? '').toLowerCase();
  return c ? (CODECS[c] ?? c.toUpperCase()) : '';
};

/** "English · E-AC-3" */
export function audioLabel(t: PrequeueAudioTrack): string {
  return [languageLabel(t.language), t.profile || codecLabel(t.codec)].filter(Boolean).join(' · ');
}

// ---------------------------------------------------------------- details playback settings

/** The details page's playback settings, device override over profile (native layering). */
export interface DetailsPlaybackSettings {
  /** Streams start resolving as a details page opens (`playback.disablePrequeue` off). */
  prequeueOnOpen: boolean;
  /** Info line: the prequeued release's name (`display.showDetailsPrequeueFilename`). */
  showFilename: boolean;
  /** Info line: the tracks Play will use (`display.showDetailsSelectedTracks`). */
  showSelectedTracks: boolean;
}

interface UserSettingsSlice {
  playback?: { disablePrequeue?: boolean };
  display?: { showDetailsPrequeueFilename?: boolean; showDetailsSelectedTracks?: boolean };
}

interface DeviceSettingsSlice {
  disablePrequeue?: boolean;
  showDetailsPrequeueFilename?: boolean;
  showDetailsSelectedTracks?: boolean;
}

/**
 * Pure layering (native APIClient.playbackSettings / detailsInfoLine). The server defaults
 * `disablePrequeue` to true (uncached debrid upload churn), so warm-up is off unless the
 * profile or device turns it on, and off when neither answers.
 */
export function layerDetailsSettings(
  user: UserSettingsSlice | null,
  device: DeviceSettingsSlice | null,
): DetailsPlaybackSettings {
  const disable = device?.disablePrequeue ?? user?.playback?.disablePrequeue ?? true;
  return {
    prequeueOnOpen: !disable,
    showFilename:
      device?.showDetailsPrequeueFilename ?? user?.display?.showDetailsPrequeueFilename ?? false,
    showSelectedTracks:
      device?.showDetailsSelectedTracks ?? user?.display?.showDetailsSelectedTracks ?? false,
  };
}

export async function getDetailsPlaybackSettings(
  profileId: string,
  signal?: AbortSignal,
): Promise<DetailsPlaybackSettings> {
  const [user, device] = await Promise.all([
    apiRequest<UserSettingsSlice>(`/users/${encodeURIComponent(profileId)}/settings`, {
      signal,
    }).catch(() => null),
    apiRequest<DeviceSettingsSlice>(`/clients/${encodeURIComponent(getClientId())}/settings`, {
      signal,
      query: { userId: profileId },
    }).catch(() => null),
  ]);
  return layerDetailsSettings(user, device);
}

export const playbackKeys = {
  detailsSettings: (profileId: string) => ['playback', profileId, 'detailsSettings'] as const,
};
