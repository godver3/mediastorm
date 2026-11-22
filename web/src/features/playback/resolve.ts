/**
 * Resolve a stream the way the native clients do, then hand off to playback.html.
 * Mirrors native-tvos Player/PlaybackCoordinator.swift (play / resolveStream /
 * awaitPrequeue / prequeueBody), +Library (playLibrary) and the manual-source path
 * (play(source:)): library copy -> library playback path; manual pick ->
 * /playback/resolve; otherwise POST /playback/prequeue and poll until ready.
 */
import type { EpisodeNumbering, LibraryVersion } from '@/api/details';
import { libraryPlayback } from '@/api/details';
import {
  abortableSleep,
  describePrequeueStage,
  getPrequeueStatus,
  resolveSource,
  startPrequeue,
  type PrequeueBody,
  type PrequeueStatus,
  type SourceResult,
  type TrackOverrides,
  type TrackSelection,
} from '@/api/playback';
import {
  vodPlaybackParams,
  type PlaybackEpisodeInfo,
  type PlaybackParams,
  type PlaybackTitleInfo,
  type ResolvedStream,
  type StartPoint,
} from '@/lib/playback';

export interface PlayEpisode extends PlaybackEpisodeInfo {
  absoluteEpisodeNumber?: number;
  numbering?: EpisodeNumbering;
}

export interface PlayRequest {
  title: PlaybackTitleInfo;
  /** Series: the episode to play. Without one the server picks the next-up episode. */
  episode?: PlayEpisode;
  start?: StartPoint;
  /** Play this library copy instead of streaming. */
  library?: LibraryVersion;
  /** Play this manually picked source instead of the automatic prequeue. */
  source?: SourceResult;
  /** The player's back/close target (`detailsReturnPath()`). */
  returnTo?: string;
  /**
   * The details page's warm-up prequeue (native PlaybackCoordinator.prewarm): reused when it
   * resolves the episode asked for, else a fresh prequeue starts.
   */
  warmPrequeueId?: string;
  /** Audio & Subtitles picks for the stream (by ffprobe stream number). */
  tracks?: TrackOverrides;
}

export interface ResolveProgress {
  stage: string;
  detail?: string;
}

export interface ResolveContext {
  profileId: string;
  profileName?: string;
  signal?: AbortSignal;
  onProgress?: (p: ResolveProgress) => void;
}

const startSeconds = (start: StartPoint) =>
  start && 'seconds' in start && start.seconds > 0 ? start.seconds : undefined;

export function prequeueBody(req: PlayRequest): PrequeueBody {
  const t = req.title;
  const body: PrequeueBody = {
    titleId: t.titleId,
    titleName: t.name,
    mediaType: t.mediaType,
  };
  if (t.imdbId) body.imdbId = t.imdbId;
  if (t.tmdbId) body.tmdbId = String(t.tmdbId);
  if (t.tvdbId) body.tvdbId = String(t.tvdbId);
  if (t.year) body.year = t.year;
  const ep = req.episode;
  if (ep && t.mediaType === 'series') {
    body.seasonNumber = ep.seasonNumber;
    body.episodeNumber = ep.episodeNumber;
    if (ep.absoluteEpisodeNumber && ep.absoluteEpisodeNumber > 0)
      body.absoluteEpisodeNumber = ep.absoluteEpisodeNumber;
    if (ep.numbering) body.numbering = ep.numbering;
  }
  const s = startSeconds(req.start);
  if (s) body.startOffset = s;
  return body;
}

/** Track indexes for playback.html (ffprobe stream numbers; text subtitles only). */
export function webTracks(
  selection: TrackSelection | undefined,
  status?: Pick<PrequeueStatus, 'selectedAudioTrack' | 'selectedSubtitleTrack' | 'subtitleTracks'>,
): { audioTrack?: number; subtitleTrack?: number } {
  if (selection) {
    const sub =
      selection.textSubtitle ?? (selection.subtitle?.bitmap ? undefined : selection.subtitle);
    return { audioTrack: selection.audio?.streamIndex, subtitleTrack: sub?.streamIndex };
  }
  if (!status) return {};
  const subIdx = status.selectedSubtitleTrack;
  const meta = status.subtitleTracks?.find((t) => t.index === subIdx || t.absoluteIndex === subIdx);
  return {
    audioTrack: status.selectedAudioTrack,
    subtitleTrack: meta?.isBitmap ? undefined : subIdx,
  };
}

export function streamFromPrequeue(s: PrequeueStatus): ResolvedStream {
  return {
    streamPath: s.streamPath ?? '',
    prequeueId: s.prequeueId,
    displayName: s.displayName,
    serviceType: s.serviceType,
    debridProvider: s.debridProvider,
    passthroughName: s.passthroughName,
    dv: s.hasDolbyVision,
    dvProfile: s.dolbyVisionProfile,
    hdr10: s.hasHdr10,
    forceAAC: s.needsAudioTranscode,
    durationSeconds: s.duration,
    ...webTracks(s.trackSelection, s),
  };
}

/** Polls a prequeue until its stream is ready (750 ms, 3 min deadline, as native). */
export async function awaitPrequeue(id: string, ctx: ResolveContext): Promise<PrequeueStatus> {
  const deadline = Date.now() + 180_000;
  while (Date.now() < deadline) {
    const status = await getPrequeueStatus(id, ctx.signal);
    if (status.status === 'ready') {
      if (!status.streamPath) throw new Error('Stream resolved without a path');
      return status;
    }
    if (status.status === 'failed' || status.status === 'expired')
      throw new Error(status.error || 'No playable stream found');
    ctx.onProgress?.({
      stage: describePrequeueStage(status),
      detail: status.progressDetail || undefined,
    });
    await abortableSleep(750, ctx.signal);
  }
  throw new Error('Timed out waiting for a stream');
}

/** Resolves `req` to the playback.html params (does not navigate). */
export async function resolvePlayback(
  req: PlayRequest,
  ctx: ResolveContext,
): Promise<PlaybackParams> {
  const opts = { episode: req.episode, start: req.start, returnTo: req.returnTo };
  let params: PlaybackParams;
  let tracks = req.library || req.source ? undefined : req.tracks;
  if (req.library) {
    ctx.onProgress?.({ stage: 'Preparing…' });
    const info = await libraryPlayback(req.library.item.id, ctx.profileId, ctx.profileName);
    params = vodPlaybackParams(
      req.title,
      {
        streamPath: info.streamPath,
        displayName: req.library.item.fileName,
        serviceType: req.library.item.sourceType || 'local',
        durationSeconds: info.durationSeconds,
      },
      opts,
    );
  } else if (req.source) {
    ctx.onProgress?.({ stage: 'Preparing…', detail: req.source.title });
    const resolved = await resolveSource(
      ctx.profileId,
      req.source,
      req.title.titleId,
      startSeconds(req.start),
      ctx.signal,
    );
    params = vodPlaybackParams(
      req.title,
      {
        streamPath: resolved.path,
        displayName: req.source.title,
        serviceType: req.source.serviceType,
        ...webTracks(resolved.tracks),
      },
      opts,
    );
  } else {
    ctx.onProgress?.({ stage: 'Searching…' });
    const warm = req.warmPrequeueId ? await reuseWarm(req.warmPrequeueId, req, ctx) : undefined;
    // The picks name the warm stream's tracks; another stream keeps its own defaults.
    if (!warm) tracks = undefined;
    const status =
      warm ||
      (await awaitPrequeue(
        (await startPrequeue(ctx.profileId, prequeueBody(req), ctx.signal)).prequeueId,
        ctx,
      ));
    // The server picks the next-up episode when none was asked for.
    const target = status.targetEpisode;
    const episode =
      req.episode ??
      (target && req.title.mediaType === 'series'
        ? { seasonNumber: target.seasonNumber, episodeNumber: target.episodeNumber }
        : undefined);
    params = vodPlaybackParams(req.title, streamFromPrequeue(status), { ...opts, episode });
  }
  applyTrackOverrides(params, tracks);
  if (ctx.profileName) params.profileName = ctx.profileName;
  return params;
}

/** Puts Audio & Subtitles picks over the stream's defaults; '-1' = subtitles off. */
export function applyTrackOverrides(params: PlaybackParams, tracks?: TrackOverrides) {
  if (!tracks) return;
  if (tracks.audio !== undefined) params.preselectedAudioTrack = String(tracks.audio);
  if (tracks.subtitle === 'off') params.preselectedSubtitleTrack = '-1';
  else if (tracks.subtitle !== undefined) params.preselectedSubtitleTrack = String(tracks.subtitle);
}

/**
 * The warm-up prequeue's result, when it is for what `req` plays (the same episode, or the
 * server's up next when none was asked for); undefined to start a fresh one.
 */
async function reuseWarm(
  id: string,
  req: PlayRequest,
  ctx: ResolveContext,
): Promise<PrequeueStatus | undefined> {
  try {
    const status = await awaitPrequeue(id, ctx);
    const ep = req.episode;
    const t = status.targetEpisode;
    if (ep && req.title.mediaType === 'series') {
      if (!t || t.seasonNumber !== ep.seasonNumber || t.episodeNumber !== ep.episodeNumber)
        return undefined;
    }
    return status;
  } catch (err) {
    if (ctx.signal?.aborted) throw err;
    return undefined;
  }
}
