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
  subtitleTracks?: { index: number; absoluteIndex?: number; codec?: string; isBitmap?: boolean }[];
  passthroughName?: string;
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
