/**
 * Playback resolution and player launching functionality for the details screen
 */

import type { PlaybackPreference } from '@/components/BackendSettingsContext';
import { apiService, type ApiError, type NZBResult, type PlaybackResolutionResponse } from '@/services/api';
import { Linking, Platform } from 'react-native';
import { findAudioTrackByLanguage, findSubtitleTrackByPreference } from './track-selection';
import { formatFileSize } from './utils';

const APP_SCHEME = 'com.strmr.app';

const formatProviderName = (provider?: string, demoMode?: boolean): string => {
  if (demoMode) {
    return 'Provider';
  }
  const raw = provider?.trim();
  if (!raw) {
    return 'Debrid provider';
  }

  const lower = raw.toLowerCase();
  if (lower === 'realdebrid' || lower === 'real-debrid' || lower === 'real_debrid') {
    return 'Real-Debrid';
  }
  if (lower === 'alldebrid' || lower === 'all-debrid' || lower === 'all_debrid') {
    return 'AllDebrid';
  }

  return raw
    .replace(/[_-]+/g, ' ')
    .replace(/\s+/g, ' ')
    .trim()
    .replace(/\b\w/g, (char) => char.toUpperCase());
};

const extractFileName = (value?: string): string => {
  if (!value) {
    return '';
  }
  const trimmed = value.trim();
  if (!trimmed) {
    return '';
  }

  const segments = trimmed.split(/[\\/]/);
  const last = segments[segments.length - 1];
  return last || trimmed;
};

// Build a display name for demo mode (matches backend buildDisplayName format)
const buildDisplayName = (titleName?: string, year?: number, seasonNumber?: number, episodeNumber?: number): string => {
  if (!titleName?.trim()) {
    return 'Media';
  }

  const name = titleName.trim();

  // For series with episode info
  if (seasonNumber && seasonNumber > 0 && episodeNumber && episodeNumber > 0) {
    const s = String(seasonNumber).padStart(2, '0');
    const e = String(episodeNumber).padStart(2, '0');
    return `${name} S${s}E${e}`;
  }

  // For movies with year
  if (year && year > 0) {
    return `${name} (${year})`;
  }

  return name;
};

const describeDebridStatus = (result: NZBResult, update: PlaybackResolutionResponse, demoMode?: boolean): string => {
  const status = (update.healthStatus || '').trim().toLowerCase();
  const provider = formatProviderName(result.attributes?.provider, demoMode);
  const title = result.title?.trim() || 'Selected release';

  switch (status) {
    case 'cached':
      return `${provider} already cached "${title}". Preparing stream…`;
    case 'downloading':
      return `${provider} is downloading "${title}"…`;
    case 'magnet_conversion':
      return `${provider} is fetching metadata for "${title}"…`;
    case 'processing':
      return `${provider} is preparing "${title}"…`;
    case 'healthy':
      return `${provider} prepared "${title}". Finalizing stream…`;
    case '':
      return `Contacting ${provider} for "${title}"…`;
    default:
      return `${provider} status for "${title}": ${status}.`;
  }
};

const describeUsenetStatus = (result: NZBResult, update: PlaybackResolutionResponse): string => {
  const status = (update.healthStatus || '').trim().toLowerCase();
  const queueId = Number(update.queueId);
  const queueLabel = Number.isFinite(queueId) && queueId > 0 ? ` (Queue #${Math.trunc(queueId)})` : '';
  const title = result.title?.trim() || 'Selected release';
  const indexer = result.indexer?.trim() || 'the indexer';
  const nzbName = extractFileName(update.sourceNzbPath);

  switch (status) {
    case 'queued':
      return `Queued “${title}” from ${indexer}${queueLabel}.`;
    case 'processing':
      if (nzbName) {
        return `Processing ${nzbName}${queueLabel}…`;
      }
      return `Processing “${title}”${queueLabel}…`;
    case 'healthy': {
      const sizeLabel =
        typeof update.fileSize === 'number' && update.fileSize > 0 ? formatFileSize(update.fileSize) : null;
      if (update.webdavPath) {
        return sizeLabel ? `Stream ready • ${sizeLabel} • status healthy` : 'Stream ready • status healthy';
      }
      return `Usenet health checks passed for “${title}”. Preparing stream…`;
    }
    case 'failed':
      return `Usenet processing failed for “${title}”.`;
    case '':
      return 'Preparing Usenet release…';
    default:
      return `Usenet status “${status}” for “${title}”.`;
  }
};

const describePlaybackStatusMessage = (
  result: NZBResult,
  update: PlaybackResolutionResponse,
  demoMode?: boolean,
): string | null => {
  const serviceType = (result.serviceType ?? 'usenet').toLowerCase();
  if (serviceType === 'debrid') {
    return describeDebridStatus(result, update, demoMode);
  }
  return describeUsenetStatus(result, update);
};

export const sanitizeStreamUrlForExternalPlayers = (urlString: string) => {
  try {
    const parsed = new URL(urlString);

    // Only sanitize URLs that go through our video stream proxy (have token or path params)
    // Don't modify direct debrid/CDN URLs
    const isProxyUrl = parsed.searchParams.has('token') || parsed.searchParams.has('path');
    if (!isProxyUrl) {
      return urlString;
    }

    parsed.searchParams.delete('target');
    parsed.searchParams.delete('format');
    parsed.searchParams.set('transmux', '0');
    return parsed.toString();
  } catch (error) {
    console.warn('Unable to sanitize external stream URL; using raw value.', error);
    return urlString;
  }
};

export const detectDolbyVision = (
  metadata: Awaited<ReturnType<typeof apiService.getVideoMetadata>> | null,
): boolean => {
  if (!metadata?.videoStreams || metadata.videoStreams.length === 0) {
    return false;
  }

  // Check primary video stream for Dolby Vision
  const primaryVideo = metadata.videoStreams[0];
  return primaryVideo.hasDolbyVision === true || primaryVideo.hdrFormat === 'DV';
};

export const detectHDR10 = (metadata: Awaited<ReturnType<typeof apiService.getVideoMetadata>> | null): boolean => {
  if (!metadata?.videoStreams || metadata.videoStreams.length === 0) {
    return false;
  }

  const primaryVideo = metadata.videoStreams[0];

  // HDR10 uses PQ (SMPTE ST 2084) transfer function and BT.2020 color primaries
  const isPQ = primaryVideo.colorTransfer === 'smpte2084';
  const isBT2020 = primaryVideo.colorPrimaries === 'bt2020';

  // Also check hdrFormat field if backend provides it
  if (primaryVideo.hdrFormat === 'HDR10' || primaryVideo.hdrFormat === 'HDR10+') {
    return true;
  }

  return isPQ && isBT2020;
};

export const detectAnyHDR = (
  metadata: Awaited<ReturnType<typeof apiService.getVideoMetadata>> | null,
): { isHDR: boolean; isDolbyVision: boolean; isHDR10: boolean } => {
  const isDolbyVision = detectDolbyVision(metadata);
  const isHDR10 = detectHDR10(metadata);
  return {
    isHDR: isDolbyVision || isHDR10,
    isDolbyVision,
    isHDR10,
  };
};

export const buildStreamUrl = (
  webdavPath: string,
  settings: any,
  options: {
    forceTransmux?: boolean;
    disableTransmux?: boolean;
    hasDolbyVision?: boolean;
    dolbyVisionProfile?: string;
    hasHDR10?: boolean;
    needsAudioTranscode?: boolean; // TrueHD, DTS, etc.
    startOffset?: number;
    audioTrack?: number;
    subtitleTrack?: number;
    profileId?: string;
    profileName?: string;
  } = {},
) => {
  // Check if this is a debrid path - these always need to go through the API endpoint
  const isDebridPath = webdavPath.includes('/debrid/');

  // Native platforms always use HLS with react-native-video for consistent experience
  const useHlsOnNative = Platform.OS !== 'web';
  console.log(
    `🎬 buildStreamUrl: Platform.OS=${Platform.OS}, useHlsOnNative=${useHlsOnNative}, webdavPath=${webdavPath.substring(0, 100)}...`,
  );

  if (useHlsOnNative) {
    const hdrType = options.hasDolbyVision ? 'Dolby Vision' : options.hasHDR10 ? 'HDR10' : 'SDR';
    console.log(`🎬 Native platform - using HLS streaming (${hdrType})`);
    const base = apiService.getBaseUrl().replace(/\/$/, '');
    const queryParams: Record<string, string> = {};

    let normalizedPath = webdavPath;
    try {
      normalizedPath = decodeURIComponent(webdavPath);
    } catch (decodeError) {
      console.debug('Unable to decode WebDAV path; using raw value.', decodeError);
    }

    queryParams.path = normalizedPath;

    const authToken = apiService.getAuthToken();
    if (authToken) {
      queryParams.token = authToken;
    }

    // Signal HDR type to backend
    if (options.hasDolbyVision) {
      queryParams.dv = 'true';
      queryParams.dvProfile = options.dolbyVisionProfile || '';
    } else if (options.hasHDR10) {
      queryParams.hdr = 'true';
    }

    // Force AAC audio transcoding for TrueHD/DTS
    if (options.needsAudioTranscode) {
      queryParams.forceAAC = '1';
    }

    // Add startOffset if provided (for resume functionality)
    if (typeof options.startOffset === 'number' && options.startOffset > 0) {
      queryParams.startOffset = options.startOffset.toString();
    }

    // Add track selection parameters
    if (typeof options.audioTrack === 'number') {
      queryParams.audioTrack = options.audioTrack.toString();
    }
    if (typeof options.subtitleTrack === 'number') {
      queryParams.subtitleTrack = options.subtitleTrack.toString();
    }

    // Add profile info for stream tracking
    if (options.profileId) {
      queryParams.profileId = options.profileId;
    }
    if (options.profileName) {
      queryParams.profileName = options.profileName;
    }

    const search = Object.entries(queryParams)
      .map(([key, value]) => `${encodeURIComponent(key)}=${encodeURIComponent(value)}`)
      .join('&');

    // Return HLS start endpoint - this will create a session and return the playlist URL
    return `${base}/video/hls/start?${search}`;
  }

  // Check if the path is already an external URL (e.g., from AIOStreams pre-resolved streams)
  // External URLs should be routed through the video proxy endpoint, not constructed as WebDAV URLs
  const isExternalUrl = webdavPath.startsWith('http://') || webdavPath.startsWith('https://');

  // For web platform, use direct WebDAV URLs with basic auth (except for debrid paths and external URLs)
  // Native platforms already returned via HLS path above
  if (settings?.webdav && !isDebridPath && !isExternalUrl) {
    const webdavConfig = settings.webdav;

    let normalizedPath = webdavPath;
    try {
      normalizedPath = decodeURIComponent(webdavPath);
    } catch (decodeError) {
      console.debug('Unable to decode WebDAV path; using raw value.', decodeError);
    }

    // Extract base URL and construct direct WebDAV URL with auth
    try {
      const baseUrl = new URL(webdavConfig.baseUrl || apiService.getBaseUrl());
      const username = encodeURIComponent(webdavConfig.username || '');
      const password = encodeURIComponent(webdavConfig.password || '');

      // Encode the path properly by encoding each segment while preserving slashes
      const encodedPath = normalizedPath
        .split('/')
        .map((segment) => encodeURIComponent(segment))
        .join('/');

      // Construct URL with basic auth: http://username:password@host:port/path
      const directUrl = `${baseUrl.protocol}//${username}:${password}@${baseUrl.host}${encodedPath}`;

      // Check if transmuxing is needed for web
      let lower = webdavPath.toLowerCase();
      try {
        lower = decodeURIComponent(webdavPath).toLowerCase();
      } catch {
        // ignore decode errors, fall back to original lower-case path
      }

      let naiveExt = '';
      try {
        const parsed = new URL(lower, 'http://placeholder');
        const pathName = parsed.pathname;
        const dotIndex = pathName.lastIndexOf('.');
        naiveExt = dotIndex >= 0 ? pathName.substring(dotIndex) : '';
      } catch {
        const dotIndex = lower.lastIndexOf('.');
        naiveExt = dotIndex >= 0 ? lower.substring(dotIndex) : '';
      }
      const knownProblemExts = ['.mkv', '.ts', '.m2ts', '.mts', '.avi', '.mpg', '.mpeg'];
      const looksObfuscated = /\.mkv_|\.ts_|\.avi_|\.mpg_|\.mpeg_/i.test(lower);
      const needsTransmux = naiveExt !== '.mp4' && (knownProblemExts.includes(naiveExt) || looksObfuscated);

      // If transmuxing is needed for web, use the video streaming endpoint with WebDAV auth
      if (needsTransmux || options.forceTransmux) {
        console.log('🎬 Web browser needs transmuxing, using video streaming endpoint with WebDAV auth');
        // Fall through to proxy endpoint with WebDAV credentials
      } else {
        console.log('🎬 Using direct WebDAV URL for web browser (no transmuxing needed)');
        return directUrl;
      }
    } catch (urlError) {
      console.warn('Failed to construct direct WebDAV URL, falling back to proxy:', urlError);
      // Fall through to proxy endpoint
    }
  }

  // For web or when WebDAV config is unavailable, use the proxy endpoint
  const base = apiService.getBaseUrl().replace(/\/$/, '');
  const queryParams: Record<string, string> = {};

  let normalizedPath = webdavPath;
  try {
    normalizedPath = decodeURIComponent(webdavPath);
  } catch (decodeError) {
    console.debug('Unable to decode WebDAV path; using raw value.', decodeError);
  }

  queryParams.path = normalizedPath;

  const authToken = apiService.getAuthToken();
  if (authToken) {
    queryParams.token = authToken;
  }

  if (options.disableTransmux) {
    queryParams.transmux = '0';
  } else if (options.forceTransmux) {
    queryParams.transmux = 'force';
    queryParams.format = 'mp4';
  }

  // Heuristic for web: prefer mp4 transmux for non-MP4 containers or obfuscated names
  if (Platform.OS === 'web') {
    let lower = webdavPath.toLowerCase();
    try {
      lower = decodeURIComponent(webdavPath).toLowerCase();
    } catch {
      // ignore decode errors, fall back to original lower-case path
    }

    let naiveExt = '';
    try {
      const parsed = new URL(lower, 'http://placeholder');
      const pathName = parsed.pathname;
      const dotIndex = pathName.lastIndexOf('.');
      naiveExt = dotIndex >= 0 ? pathName.substring(dotIndex) : '';
    } catch {
      const dotIndex = lower.lastIndexOf('.');
      naiveExt = dotIndex >= 0 ? lower.substring(dotIndex) : '';
    }
    const knownProblemExts = ['.mkv', '.ts', '.m2ts', '.mts', '.avi', '.mpg', '.mpeg'];
    const looksObfuscated = /\.mkv_|\.ts_|\.avi_|\.mpg_|\.mpeg_/i.test(lower);
    const needsTransmux = naiveExt !== '.mp4' && (knownProblemExts.includes(naiveExt) || looksObfuscated);
    if (needsTransmux) {
      queryParams.format = 'mp4';
      queryParams.target = 'web';
    } else if (naiveExt !== '.mp4') {
      // Hint the backend anyway when uncertain
      queryParams.target = 'web';
    }
  }

  // Add profile info for stream tracking
  if (options.profileId) {
    queryParams.profileId = options.profileId;
  }
  if (options.profileName) {
    queryParams.profileName = options.profileName;
  }

  const search = Object.entries(queryParams)
    .map(([key, value]) => `${encodeURIComponent(key)}=${encodeURIComponent(value)}`)
    .join('&');

  const prefix = `${base}/video/stream`;
  return search ? `${prefix}?${search}` : prefix;
};

/**
 * Build a direct stream URL for external players like Infuse.
 * External players don't need our HLS transcoding or proxy - they handle everything natively.
 * - For debrid: use backend proxy URL (to handle IP-locked RD URLs)
 * - For external URLs (AIOStreams): use backend proxy URL
 * - For usenet: build a direct WebDAV URL with auth
 */
export const buildDirectUrlForExternalPlayer = async (
  playback: { webdavPath: string; sourceNzbPath?: string },
  settings: any,
  options?: { profileId?: string; profileName?: string },
): Promise<string | null> => {
  const isDebridPath = playback.webdavPath.includes('/debrid/');
  const isExternalUrl = playback.webdavPath.startsWith('http://') || playback.webdavPath.startsWith('https://');

  // For debrid content or external URLs (AIOStreams), use backend proxy URL
  // Real-Debrid URLs are IP-locked, and external URLs need to be proxied through the backend
  if (isDebridPath || isExternalUrl) {
    const base = apiService.getBaseUrl().replace(/\/$/, '');
    // Build URL manually to ensure proper encoding of special chars like semicolons
    // URLSearchParams doesn't encode semicolons which breaks some parsers
    const queryParts: string[] = [];
    queryParts.push(`path=${encodeURIComponent(playback.webdavPath)}`);
    queryParts.push('transmux=0'); // No transmuxing needed for external players
    const authToken = apiService.getAuthToken();
    if (authToken) {
      queryParts.push(`token=${encodeURIComponent(authToken)}`);
    }
    // Add profile info for stream tracking
    if (options?.profileId) {
      queryParts.push(`profileId=${encodeURIComponent(options.profileId)}`);
    }
    if (options?.profileName) {
      queryParts.push(`profileName=${encodeURIComponent(options.profileName)}`);
    }
    const proxyUrl = `${base}/video/stream?${queryParts.join('&')}`;
    console.log(
      `🎬 [External Player] Using backend proxy URL for ${isExternalUrl ? 'external URL' : 'debrid'}:`,
      proxyUrl,
    );
    return proxyUrl;
  }

  // For usenet content, build direct WebDAV URL with auth
  if (!isDebridPath && settings?.webdav) {
    const webdavConfig = settings.webdav;
    let normalizedPath = playback.webdavPath;
    try {
      normalizedPath = decodeURIComponent(playback.webdavPath);
    } catch {
      // ignore decode errors
    }

    try {
      const baseUrl = new URL(webdavConfig.baseUrl || apiService.getBaseUrl());
      const username = encodeURIComponent(webdavConfig.username || '');
      const password = encodeURIComponent(webdavConfig.password || '');

      const encodedPath = normalizedPath
        .split('/')
        .map((segment) => encodeURIComponent(segment))
        .join('/');

      const directUrl = `${baseUrl.protocol}//${username}:${password}@${baseUrl.host}${encodedPath}`;
      console.log('🎬 [External Player] Using direct WebDAV URL');
      return directUrl;
    } catch (error) {
      console.warn('Failed to construct direct WebDAV URL for external player:', error);
    }
  }

  return null;
};

export const buildExternalPlayerTargets = (
  player: PlaybackPreference,
  streamUrl: string,
  isIosWeb: boolean,
): string[] => {
  const trimmedUrl = sanitizeStreamUrlForExternalPlayers(streamUrl?.trim() ?? '');
  if (!trimmedUrl) {
    return [];
  }

  if (player === 'outplayer') {
    if (Platform.OS === 'ios' || isIosWeb) {
      const callbackBase = `${APP_SCHEME}://x-callback-url`;
      const successUrl = `${callbackBase}/playbackDidFinish`;
      const errorUrl = `${callbackBase}/playbackDidFail`;
      const query = `x-success=${encodeURIComponent(successUrl)}&x-error=${encodeURIComponent(errorUrl)}&url=${encodeURIComponent(trimmedUrl)}`;
      return [`outplayer://x-callback-url/play?${query}`];
    }
    return [];
  }

  if (player === 'infuse') {
    if (Platform.OS === 'ios' || (Platform.OS as string) === 'tvos' || Platform.OS === 'macos' || isIosWeb) {
      const callbackBase = `${APP_SCHEME}://x-callback-url`;
      const successUrl = `${callbackBase}/playbackDidFinish`;
      const errorUrl = `${callbackBase}/playbackDidFail`;
      const query = `x-success=${encodeURIComponent(successUrl)}&x-error=${encodeURIComponent(errorUrl)}&url=${encodeURIComponent(trimmedUrl)}`;
      return [`infuse://x-callback-url/play?${query}`];
    }
    return [];
  }

  return [];
};

export const launchNativePlayer = (
  streamUrl: string,
  headerImage: string,
  title: string,
  router: any,
  options: {
    preferSystemPlayer?: boolean;
    mediaType?: string;
    seriesTitle?: string; // Clean series title without episode code
    year?: number;
    seasonNumber?: number;
    episodeNumber?: number;
    episodeName?: string;
    durationHint?: number;
    sourcePath?: string;
    displayName?: string; // For demo mode - masks actual filename
    releaseName?: string; // Original release name for subtitle matching
    dv?: boolean;
    dvProfile?: string;
    forceAAC?: boolean;
    startOffset?: number;
    titleId?: string;
    imdbId?: string;
    tvdbId?: string;
    debugPlayer?: boolean;
    shuffleMode?: boolean;
    preExtractedSubtitles?: string; // JSON stringified SubtitleSessionInfo[]
    passthroughName?: string; // AIOStreams passthrough format: raw display name
    passthroughDescription?: string; // AIOStreams passthrough format: raw description
    preselectedAudioTrack?: number; // Track index baked into HLS session
    preselectedSubtitleTrack?: number; // Track index baked into HLS session
  } = {},
) => {
  const {
    preferSystemPlayer,
    mediaType,
    seriesTitle,
    year,
    seasonNumber,
    episodeNumber,
    episodeName,
    durationHint,
    sourcePath,
    displayName,
    releaseName,
    dv,
    dvProfile,
    forceAAC,
    startOffset,
    titleId,
    imdbId,
    tvdbId,
    debugPlayer,
    shuffleMode,
    preExtractedSubtitles,
    passthroughName,
    passthroughDescription,
    preselectedAudioTrack,
    preselectedSubtitleTrack,
  } = options;
  let debugLogs: string | undefined;
  if (typeof window !== 'undefined' && window.location?.search) {
    const debugParam = new URLSearchParams(window.location.search).get('debugLogs');
    if (debugParam) {
      debugLogs = debugParam;
    }
  }
  router.push({
    pathname: debugPlayer ? '/player-debug' : '/player',
    params: {
      movie: streamUrl,
      headerImage,
      title,
      ...(seriesTitle ? { seriesTitle } : {}), // Clean series title for metadata lookups
      ...(debugLogs ? { debugLogs } : {}),
      ...(preferSystemPlayer ? { preferSystemPlayer: '1' } : {}),
      ...(mediaType ? { mediaType } : {}),
      ...(year ? { year: year.toString() } : {}),
      ...(seasonNumber ? { seasonNumber: seasonNumber.toString() } : {}),
      ...(episodeNumber ? { episodeNumber: episodeNumber.toString() } : {}),
      ...(episodeName ? { episodeName } : {}),
      ...(durationHint ? { durationHint: durationHint.toString() } : {}),
      ...(sourcePath ? { sourcePath: encodeURIComponent(sourcePath) } : {}),
      ...(displayName ? { displayName } : {}),
      ...(releaseName ? { releaseName } : {}),
      ...(dv ? { dv: '1' } : {}),
      ...(dvProfile ? { dvProfile } : {}),
      ...(forceAAC ? { forceAAC: '1' } : {}),
      ...(typeof startOffset === 'number' ? { startOffset: startOffset.toString() } : {}),
      ...(titleId ? { titleId } : {}),
      ...(imdbId ? { imdbId } : {}),
      ...(tvdbId ? { tvdbId } : {}),
      ...(shuffleMode ? { shuffleMode: '1' } : {}),
      ...(preExtractedSubtitles ? { preExtractedSubtitles } : {}),
      ...(passthroughName ? { passthroughName } : {}),
      ...(passthroughDescription ? { passthroughDescription } : {}),
      ...(typeof preselectedAudioTrack === 'number' && preselectedAudioTrack >= 0
        ? { preselectedAudioTrack: preselectedAudioTrack.toString() }
        : {}),
      ...(typeof preselectedSubtitleTrack === 'number' && preselectedSubtitleTrack >= 0
        ? { preselectedSubtitleTrack: preselectedSubtitleTrack.toString() }
        : {}),
    },
  });
};

export const initiatePlayback = async (
  result: NZBResult,
  playbackPreference: PlaybackPreference,
  settings: any,
  headerImage: string,
  title: string,
  router: any,
  isIosWeb: boolean,
  setSelectionInfo: (info: string | null) => void,
  setSelectionError: (error: string | null) => void,
  options: {
    mediaType?: string;
    seriesTitle?: string; // Clean series title without episode code
    year?: number;
    seasonNumber?: number;
    episodeNumber?: number;
    episodeName?: string;
    signal?: AbortSignal;
    titleId?: string;
    imdbId?: string;
    tvdbId?: string;
    startOffset?: number;
    debugPlayer?: boolean;
    onExternalPlayerLaunch?: () => void; // Callback to hide loading screen when launching external player
    userSettings?: any; // Per-user settings override
    profileId?: string;
    profileName?: string;
    shuffleMode?: boolean;
    trackOverrides?: { audioTrack?: number; subtitleTrack?: number }; // Manual track selection override
  } = {},
) => {
  setSelectionError(null);
  const releaseTitle = result.title?.trim() || 'this release';
  const serviceType = (result.serviceType ?? 'usenet').toLowerCase();
  const demoMode = settings?.demoMode;
  // Generate displayName for demo mode to mask actual filenames in player
  const displayName = demoMode
    ? buildDisplayName(title, options.year, options.seasonNumber, options.episodeNumber)
    : undefined;
  if (serviceType === 'debrid') {
    const provider = formatProviderName(result.attributes?.provider, demoMode);
    setSelectionInfo(`Checking ${provider} for "${releaseTitle}"…`);
  } else {
    const indexer = result.indexer?.trim() || 'the indexer';
    setSelectionInfo(`Preparing "${releaseTitle}" from ${indexer}…`);
  }

  const playback = await apiService.resolvePlayback(result, {
    onStatus: (update) => {
      const message = describePlaybackStatusMessage(result, update, demoMode);
      if (message) {
        setSelectionInfo(message);
      }
    },
    signal: options.signal,
    // Pass startOffset for subtitle extraction to start from resume position
    startOffset: options.startOffset,
  });

  // Check if using external player - they handle HDR natively and don't need HLS
  const isExternalPlayer = playbackPreference === 'infuse' || playbackPreference === 'outplayer';

  // For external players, skip HDR detection and HLS - they handle everything natively
  // Just build the direct URL and launch
  if (isExternalPlayer) {
    console.log('[initiatePlayback] External player selected, skipping HLS creation');
    setSelectionInfo(null);

    const player = playbackPreference;
    const label = player === 'outplayer' ? 'Outplayer' : 'Infuse';

    // Build direct URL for external player
    setSelectionInfo(`Preparing stream for ${label}…`);
    const directExternalUrl = await buildDirectUrlForExternalPlayer(playback, settings, {
      profileId: options.profileId,
      profileName: options.profileName,
    });
    if (!directExternalUrl) {
      setSelectionError(`Unable to build direct URL for ${label}. Launching native player instead.`);
      // Fall back to native player - continue with rest of function
    } else {
      console.log('[initiatePlayback] Using direct URL for external player:', directExternalUrl);

      const externalTargets = buildExternalPlayerTargets(player, directExternalUrl, isIosWeb);

      if (externalTargets.length === 0) {
        setSelectionError(`${label} playback is not available on this platform. Launching native player instead.`);
        // Fall back to native - continue with rest of function
      } else {
        // Launch external player with direct URL
        try {
          if (Platform.OS === 'web') {
            const target = externalTargets[0];

            if (typeof window === 'undefined') {
              throw new Error('Unable to access window to trigger external player launch.');
            }

            setSelectionInfo(`Preparing ${label} launch…`);

            let clipboardCopied = false;
            if (typeof navigator !== 'undefined' && navigator.clipboard?.writeText) {
              try {
                await navigator.clipboard.writeText(directExternalUrl);
                clipboardCopied = true;
              } catch (clipboardError) {
                console.warn('Unable to copy stream URL to clipboard.', clipboardError);
              }
            }

            if (!clipboardCopied) {
              setSelectionError(`Copy this stream URL into ${label}: ${directExternalUrl}`);
            }

            const attemptMessage = clipboardCopied
              ? `Stream URL copied. Attempting to open ${label}… If nothing happens, paste the copied stream URL into ${label}.`
              : `Attempting to open ${label}… If nothing happens, copy the stream URL into ${label} manually.`;
            setSelectionInfo(attemptMessage);

            if (options.onExternalPlayerLaunch) {
              options.onExternalPlayerLaunch();
            }

            try {
              window.location.assign(target);
            } catch (openError) {
              console.warn('Failed to open external player via window.location:', openError);
              setSelectionError(`Unable to launch ${label} automatically. Paste the stream URL into the app manually.`);
            }
            return;
          }

          // Native platform (iOS/tvOS)
          let lastError: unknown = null;
          let sawUnsupported = false;

          for (const externalUrl of externalTargets) {
            try {
              const supported = await Linking.canOpenURL(externalUrl);
              if (!supported) {
                sawUnsupported = true;
                continue;
              }

              if (options.onExternalPlayerLaunch) {
                options.onExternalPlayerLaunch();
              }

              await Linking.openURL(externalUrl);
              setSelectionInfo(`Opening ${label}…`);
              return;
            } catch (err) {
              lastError = err;
              console.error(`⚠️ Unable to launch ${label} via ${externalUrl}`, err);
            }
          }

          if (player === 'outplayer' && sawUnsupported && Platform.OS === 'ios') {
            const appStoreUrl = 'https://apps.apple.com/app/outplayer/id1449923287';
            try {
              await Linking.openURL(appStoreUrl);
            } catch (storeError) {
              console.warn('⚠️ Unable to open Outplayer App Store listing.', storeError);
            }
          }

          if (lastError) {
            console.error(`⚠️ Exhausted external targets for ${label}`, lastError);
          }

          const fallbackMessage = sawUnsupported
            ? `${label} is not installed. Launching native player instead.`
            : `Unable to launch ${label}. Launching native player instead.`;
          setSelectionError(fallbackMessage);
          // Fall through to native player below
        } catch (err) {
          console.error(`⚠️ Error while launching ${label}`, err);
          setSelectionError(`Unable to launch ${label}. Launching native player instead.`);
          // Fall through to native player below
        }
      }
    }
  }

  // Fetch metadata for HDR detection, audio codec detection, and track selection
  // All native playback uses HLS with react-native-video for consistent experience
  let hasDolbyVision = false;
  let hasHDR10 = false;
  let dolbyVisionProfile = '';
  let needsAudioTranscode = false; // TrueHD, DTS, etc.
  let selectedAudioTrack: number | undefined;
  let selectedSubtitleTrack: number | undefined;

  const playbackSettings = options.userSettings?.playback ?? settings?.playback;
  const isNativePlatform = Platform.OS !== 'web';

  // For native platforms, always fetch metadata for track selection and HDR detection
  if (isNativePlatform) {
    try {
      setSelectionInfo('Preparing stream…');
      console.log('🎬 Fetching metadata for native playback...');
      const metadata = await apiService.getVideoMetadata(playback.webdavPath, {
        profileId: options.profileId,
        clientId: apiService.getClientId() ?? undefined,
        audioLang: playbackSettings?.preferredAudioLanguage,
      });

      // Detect HDR
      hasDolbyVision = detectDolbyVision(metadata);
      hasHDR10 = detectHDR10(metadata);

      // Detect incompatible audio (TrueHD, DTS, etc.)
      needsAudioTranscode = metadata.needsAudioTranscode ?? false;
      if (needsAudioTranscode) {
        console.log('🎬 TrueHD/DTS audio detected - will use HLS with audio transcoding');
      }

      if (hasDolbyVision && metadata.videoStreams && metadata.videoStreams[0]) {
        dolbyVisionProfile = metadata.videoStreams[0].dolbyVisionProfile || 'dv-hevc';
        setSelectionInfo('Dolby Vision detected - preparing HLS stream…');
      } else if (hasHDR10) {
        setSelectionInfo('HDR10 detected - preparing HLS stream…');
      } else if (needsAudioTranscode) {
        setSelectionInfo('TrueHD/DTS audio detected - preparing HLS stream…');
      } else {
        setSelectionInfo('Preparing stream…');
      }

      // Fetch content-specific language preference (overrides global settings)
      let contentPreference: { audioLanguage?: string; subtitleLanguage?: string; subtitleMode?: string } | null = null;
      if (options.profileId && options.titleId) {
        try {
          contentPreference = await apiService.getContentPreference(options.profileId, options.titleId);
          if (contentPreference) {
            console.log('🎬 Loaded content-specific language preference:', contentPreference);
          }
        } catch (error) {
          console.warn('🎬 Failed to fetch content preference, using global settings:', error);
        }
      }

      // Select audio/subtitle tracks
      // Priority: manual override > content preference > user preference
      if (metadata) {
        // Check for manual track override first (from manual selection modal)
        if (options.trackOverrides?.audioTrack !== undefined) {
          selectedAudioTrack = options.trackOverrides.audioTrack;
          console.log(`🎬 Using manual audio track override: ${selectedAudioTrack}`);
        } else if (metadata.audioStreams && metadata.audioStreams.length > 0) {
          // Fall back to preference-based selection
          const audioLang = contentPreference?.audioLanguage || playbackSettings?.preferredAudioLanguage || 'eng';
          const preferenceSource = contentPreference?.audioLanguage ? 'content-preference' : 'user-settings';
          const match = findAudioTrackByLanguage(metadata.audioStreams, audioLang);
          if (match !== null) {
            selectedAudioTrack = match;
            console.log(`🎬 Selected audio track ${match} for language ${audioLang} (source: ${preferenceSource})`);
          } else {
            selectedAudioTrack = metadata.audioStreams[0].index;
            console.log(`🎬 No ${audioLang} audio found, using first track: ${selectedAudioTrack}`);
          }
        }

        // Check for manual subtitle track override first
        // -1 means "Off" was explicitly selected
        if (options.trackOverrides?.subtitleTrack !== undefined) {
          if (options.trackOverrides.subtitleTrack === -1) {
            selectedSubtitleTrack = undefined;
            console.log(`🎬 Using manual subtitle track override: Off`);
          } else {
            selectedSubtitleTrack = options.trackOverrides.subtitleTrack;
            console.log(`🎬 Using manual subtitle track override: ${selectedSubtitleTrack}`);
          }
        } else if (metadata.subtitleStreams && metadata.subtitleStreams.length > 0) {
          // Fall back to preference-based selection
          const subLang = contentPreference?.subtitleLanguage || playbackSettings?.preferredSubtitleLanguage || 'eng';
          const subMode = contentPreference?.subtitleMode || playbackSettings?.preferredSubtitleMode || 'off';
          const match = findSubtitleTrackByPreference(metadata.subtitleStreams, subLang, subMode);
          if (match !== null) {
            selectedSubtitleTrack = match;
            console.log(`🎬 Selected subtitle track ${match} for language ${subLang} (mode: ${subMode})`);
          }
        }
      }
    } catch (error) {
      // Check if this is a DV policy violation - re-throw so it triggers fallback
      const errorMessage = error instanceof Error ? error.message : String(error);
      if (
        errorMessage.toLowerCase().includes('dv_profile_incompatible') ||
        errorMessage.toLowerCase().includes('no hdr fallback')
      ) {
        console.error('🚫 DV profile incompatible with user policy, cannot play:', errorMessage);
        throw error;
      }
      console.warn('Failed to fetch metadata, proceeding with default settings', error);
    }
  }

  const hasAnyHDR = hasDolbyVision || hasHDR10;
  const needsHLS = hasAnyHDR || needsAudioTranscode;

  // Build stream URL
  // On native platforms: Always use HLS for react-native-video
  // On web: Use direct streaming with transmux as needed
  let streamUrl = buildStreamUrl(playback.webdavPath, settings, {
    hasDolbyVision,
    dolbyVisionProfile,
    hasHDR10,
    needsAudioTranscode,
    startOffset: options.startOffset,
    audioTrack: selectedAudioTrack,
    subtitleTrack: selectedSubtitleTrack,
    profileId: options.profileId,
    profileName: options.profileName,
    // Disable transmux for native - HLS handles all formats
    disableTransmux: isNativePlatform && !needsHLS,
  });

  // If HLS session URL (native platforms), fetch the actual playlist URL
  let hlsDuration: number | undefined;
  if (isNativePlatform && streamUrl.includes('/video/hls/start')) {
    try {
      const contentType = hasDolbyVision
        ? 'Dolby Vision'
        : hasHDR10
          ? 'HDR10'
          : needsAudioTranscode
            ? 'TrueHD/DTS audio'
            : 'SDR';
      const startOffsetInfo = options.startOffset
        ? ` (will seek to ${Math.floor(options.startOffset)}s after loading)`
        : '';
      setSelectionInfo(`Creating HLS session (${contentType})${startOffsetInfo}…`);
      console.log(`🎬 Creating HLS session (${contentType}) with URL:`, streamUrl);
      const response = await fetch(streamUrl);
      if (!response.ok) {
        throw new Error(`HLS session creation failed: ${response.statusText}`);
      }
      const hlsData = await response.json();

      console.log('🎬 HLS session response:', {
        duration: hlsData.duration,
        startOffset: hlsData.startOffset,
        playlistUrl: hlsData.playlistUrl,
      });

      // Extract duration from HLS session response
      if (typeof hlsData.duration === 'number' && hlsData.duration > 0) {
        hlsDuration = hlsData.duration;
        console.log('🎬 HLS session duration:', hlsDuration, 'seconds');
      }

      // Build playlist URL with auth token
      const baseUrl = apiService.getBaseUrl().replace(/\/$/, '');
      const authToken = apiService.getAuthToken();
      streamUrl = `${baseUrl}${hlsData.playlistUrl}${authToken ? `?token=${encodeURIComponent(authToken)}` : ''}`;

      console.log('🎬 HLS playlist URL:', streamUrl);

      // For HLS sessions, we'll pass the startOffset to the player instead of trying to seek during session creation
      // This is because FFmpeg HLS transcoding always starts from segment 0
      if (options.startOffset) {
        console.log('🎬 Will pass startOffset to player for seeking after HLS loads:', options.startOffset);
      }
    } catch (error) {
      console.error('Failed to create HLS session:', error);
      throw new Error(`Failed to create HLS session: ${error}`);
    }
  }

  // Don't show "Stream ready" toast as it might overlay the player content
  setSelectionInfo(null);

  // If we reach here, we're using the native player
  // (External players would have returned early above)
  console.log(
    '[initiatePlayback] Using native player, playbackPreference:',
    playbackPreference,
    'Platform.OS:',
    Platform.OS,
  );

  // Build pre-extracted subtitles JSON if available
  const preExtractedSubtitles =
    playback.subtitleSessions && Object.keys(playback.subtitleSessions).length > 0
      ? JSON.stringify(Object.values(playback.subtitleSessions))
      : undefined;

  // Extract passthrough format data from AIOStreams results
  const passthroughName = result.attributes?.passthrough_format === 'true' ? result.attributes?.raw_name : undefined;
  const passthroughDescription =
    result.attributes?.passthrough_format === 'true' ? result.attributes?.raw_description : undefined;

  launchNativePlayer(streamUrl, headerImage, title, router, {
    ...options,
    ...(hlsDuration ? { durationHint: hlsDuration } : {}),
    sourcePath: playback.webdavPath,
    ...(displayName ? { displayName } : {}),
    releaseName: result.title,
    ...(hasDolbyVision ? { dv: true } : {}),
    ...(hasDolbyVision && dolbyVisionProfile ? { dvProfile: dolbyVisionProfile } : {}),
    ...(hasHDR10 ? { hdr10: true } : {}),
    ...(typeof options.startOffset === 'number' ? { startOffset: options.startOffset } : {}),
    ...(preExtractedSubtitles ? { preExtractedSubtitles } : {}),
    ...(passthroughName ? { passthroughName } : {}),
    ...(passthroughDescription ? { passthroughDescription } : {}),
    // Pass selected tracks so player UI shows correct selection
    ...(typeof selectedAudioTrack === 'number' ? { preselectedAudioTrack: selectedAudioTrack } : {}),
    ...(typeof selectedSubtitleTrack === 'number' ? { preselectedSubtitleTrack: selectedSubtitleTrack } : {}),
  });
};

export const extractErrorMessage = (value: unknown): string => {
  if (!value) {
    return '';
  }
  if (value instanceof Error && typeof value.message === 'string') {
    return value.message;
  }
  if (typeof value === 'string') {
    return value;
  }
  if (typeof value === 'object') {
    const maybeMessage = (value as { message?: unknown; error?: unknown }).message;
    if (typeof maybeMessage === 'string') {
      return maybeMessage;
    }
    const maybeError = (value as { error?: unknown }).error;
    if (typeof maybeError === 'string') {
      return maybeError;
    }
  }
  return String(value);
};

export const getHealthFailureReason = (error: unknown): string | null => {
  const rawMessage = extractErrorMessage(error);
  if (!rawMessage) {
    return null;
  }

  const lowerMessage = rawMessage.toLowerCase();

  // Check for DV profile incompatibility error
  if (lowerMessage.includes('dv_profile_incompatible') || lowerMessage.includes('no hdr fallback')) {
    return 'DV profile 5 not compatible (no HDR fallback)';
  }

  const reasonMatch =
    rawMessage.match(/health (?:check|status)(?: reported)?\s*"?([a-z0-9 _-]+)/i) ||
    rawMessage.match(/reported\s+"?([a-z0-9 _-]+)"?/i) ||
    rawMessage.match(/(missing[_\s-]+segments?)/i) ||
    rawMessage.match(/unavailable\s+"?([a-z0-9 _-]+)"?/i);

  if (reasonMatch) {
    const captured = reasonMatch[1] ?? reasonMatch[0];
    if (captured) {
      const normalized = captured.replace(/[_-]+/g, ' ').trim().toLowerCase();
      if (normalized) {
        return normalized;
      }
    }
  }

  return null;
};

export const getTimeoutMessage = (_error: unknown): string => {
  // Return a user-friendly message for timeout errors
  // The backend already provides a helpful message, but this provides a fallback
  return 'Search timed out. If using Aiostreams, consider increasing the indexer timeout in Settings.';
};

export const isTimeoutError = (error: unknown): boolean => {
  if (!error) {
    return false;
  }

  const maybeApiError = error as ApiError | undefined;
  if (maybeApiError?.code === 'GATEWAY_TIMEOUT') {
    return true;
  }
  if (typeof maybeApiError?.status === 'number' && maybeApiError.status === 504) {
    return true;
  }

  const message = extractErrorMessage(error).toLowerCase();
  if (!message) {
    return false;
  }

  const timeoutKeywords = ['timeout', 'timed out', 'gateway timeout', 'context deadline exceeded', '504'];
  return timeoutKeywords.some((keyword) => message.includes(keyword));
};

export const isHealthFailureError = (error: unknown): boolean => {
  if (!error) {
    return false;
  }

  // Timeouts are NOT health failures - they're a separate error type
  if (isTimeoutError(error)) {
    return false;
  }

  const maybeApiError = error as ApiError | undefined;
  if (maybeApiError?.code === 'NZB_HEALTH_FAILED') {
    return true;
  }
  if (typeof maybeApiError?.status === 'number' && maybeApiError.status === 502) {
    return true;
  }

  const message = extractErrorMessage(error).toLowerCase();
  if (!message) {
    return false;
  }

  const healthKeywords = [
    'health check',
    'nzb health',
    'usenet health',
    'missing segment',
    'missing_segment',
    'missing segments',
    'unsupported_archive',
    'unsupported archive',
    'unsupported',
    'unavailable',
    'bad gateway',
    '502',
    // Debrid-specific health failures
    'not cached',
    'torrent not cached',
    'no media files found',
    'no download links',
    // DV profile compatibility failures
    'dv_profile_incompatible',
    'dv profile',
    'no hdr fallback',
  ];
  return healthKeywords.some((keyword) => message.includes(keyword));
};
