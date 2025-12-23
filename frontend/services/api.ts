// API service for strmr backend integration

import { getApiConfig } from '../config/api';

export interface ApiError extends Error {
  status?: number;
  statusText?: string;
  body?: string;
  url?: string;
  code?: string;
}

export interface Image {
  url: string;
  type: string;
  width: number;
  height: number;
}

export interface Trailer {
  name: string;
  site?: string;
  type?: string;
  url: string;
  embedUrl?: string;
  thumbnailUrl?: string;
  language?: string;
  country?: string;
  key?: string;
  official?: boolean;
  publishedAt?: string;
  resolution?: number;
  source?: string;
  durationSeconds?: number;
}

export interface ReleaseWindow {
  type: string;
  date: string;
  country?: string;
  note?: string;
  source: string;
  primary?: boolean;
  released?: boolean;
}

export interface Title {
  id: string;
  name: string;
  originalName?: string;
  alternateTitles?: string[];
  overview: string;
  year: number;
  language: string;
  poster?: Image;
  backdrop?: Image;
  mediaType: string;
  tvdbId?: number;
  imdbId?: string;
  tmdbId?: number;
  popularity?: number;
  network?: string;
  primaryTrailer?: Trailer;
  trailers?: Trailer[];
  releases?: ReleaseWindow[];
  theatricalRelease?: ReleaseWindow;
  homeRelease?: ReleaseWindow;
}

export interface TrendingItem {
  rank: number;
  title: Title;
}

export interface SearchResult {
  title: Title;
  score: number;
}

export interface SubtitleSearchResult {
  id: string;
  provider: string;
  language: string;
  release: string;
  downloads: number;
  hearing_impaired: boolean;
  page_link?: string;
}

export interface SeriesEpisode {
  id: string;
  tvdbId?: number;
  name: string;
  overview: string;
  seasonNumber: number;
  episodeNumber: number;
  airedDate?: string;
  runtimeMinutes?: number;
  image?: Image;
}

export interface SeriesSeason {
  id: string;
  tvdbId?: number;
  name: string;
  number: number;
  overview: string;
  type?: string;
  image?: Image;
  episodeCount: number;
  episodes: SeriesEpisode[];
}

export interface SeriesDetails {
  title: Title;
  seasons: SeriesSeason[];
}

export interface BatchSeriesDetailsRequest {
  queries: Array<{
    titleId?: string;
    name?: string;
    year?: number;
    tvdbId?: number;
    tmdbId?: number;
  }>;
}

export interface BatchSeriesDetailsItem {
  query: {
    titleId?: string;
    name?: string;
    year?: number;
    tvdbId?: number;
    tmdbId?: number;
  };
  details?: SeriesDetails;
  error?: string;
}

export interface BatchSeriesDetailsResponse {
  results: BatchSeriesDetailsItem[];
}

export interface TrailerResponse {
  primaryTrailer?: Trailer;
  trailers: Trailer[];
}

export interface TrailerQuery {
  mediaType?: string;
  titleId?: string;
  name?: string;
  year?: number;
  imdbId?: string;
  tmdbId?: number;
  tvdbId?: number;
}

export interface NZBResult {
  title: string;
  indexer: string;
  guid: string;
  link: string;
  downloadUrl: string;
  sizeBytes: number;
  publishDate: string;
  categories?: string[];
  attributes?: Record<string, string>;
  serviceType?: 'usenet' | 'debrid';
}

export interface NZBHealthCheck {
  status: string;
  healthy: boolean;
  checkedSegments: number;
  totalSegments: number;
  missingSegments?: string[];
  fileName?: string;
  sampled?: boolean;
}

export interface DebridHealthCheck {
  healthy: boolean;
  status: string;
  cached: boolean;
  provider: string;
  infoHash?: string;
  errorMessage?: string;
}

export interface PlaybackResolutionResponse {
  queueId: number;
  webdavPath?: string;
  healthStatus: string;
  fileSize?: number;
  sourceNzbPath?: string;
}

export type PlaybackResolution = PlaybackResolutionResponse & {
  webdavPath: string;
};

export interface AudioStreamMetadata {
  index: number;
  codecName: string;
  codecLongName?: string;
  channels?: number;
  sampleRate?: number;
  bitRate?: number;
  channelLayout?: string;
  language?: string;
  title?: string;
  disposition?: Record<string, number>;
  copySupported: boolean;
}

export interface SubtitleStreamMetadata {
  index: number;
  codecName?: string;
  codecLongName?: string;
  language?: string;
  title?: string;
  disposition?: Record<string, number>;
  isForced?: boolean;
  isDefault?: boolean;
}

export interface VideoStreamMetadata {
  index: number;
  codecName: string;
  codecLongName?: string;
  width?: number;
  height?: number;
  bitRate?: number;
  pixFmt?: string;
  profile?: string;
  avgFrameRate?: string;
  hasDolbyVision?: boolean;
  dolbyVisionProfile?: string;
  hdrFormat?: string;
  // HDR color metadata
  colorTransfer?: string;
  colorPrimaries?: string;
  colorSpace?: string;
}

export interface VideoMetadata {
  path: string;
  durationSeconds: number;
  fileSizeBytes?: number;
  formatName?: string;
  formatLongName?: string;
  formatBitRate?: number;
  audioStreams: AudioStreamMetadata[];
  videoStreams: VideoStreamMetadata[];
  subtitleStreams?: SubtitleStreamMetadata[];
  audioStrategy: string;
  audioPlanReason?: string;
  selectedAudioIndex: number;
  selectedAudioCodec?: string;
  selectedSubtitleIndex?: number | null;
  audioCopySupported: boolean;
  needsAudioTranscode: boolean;
  notes?: string[];
}

export interface HlsSessionStartResponse {
  sessionId: string;
  playlistUrl: string;
  duration?: number;
  startOffset?: number;
  remainingDuration?: number;
}

export interface HlsSessionStatus {
  sessionId: string;
  status: 'active' | 'completed' | 'error';
  fatalError?: string;
  fatalErrorTime?: number; // Unix timestamp
  duration?: number;
  segmentsCreated: number;
  bitstreamErrors: number;
  hdrMetadataDisabled: boolean;
  dvDisabled: boolean;
  recoveryAttempts: number;
}

export interface WatchProgress {
  percentage: number;
  currentSeason?: number;
  currentEpisode?: number;
  episodeTitle?: string;
  lastUpdatedTime?: string;
}

export interface WatchlistItem {
  id: string;
  mediaType: string;
  name: string;
  overview?: string;
  year?: number;
  posterUrl?: string;
  backdropUrl?: string;
  addedAt: string;
  externalIds?: Record<string, string>;
}

export interface WatchlistUpsertPayload {
  id: string;
  mediaType: string;
  name: string;
  overview?: string;
  year?: number;
  posterUrl?: string;
  backdropUrl?: string;
  externalIds?: Record<string, string>;
}

export interface WatchlistStateUpdate {
  // Deprecated: Watch status is now tracked separately via watchstatus endpoints
}

export interface WatchStatusItem {
  id: string; // "mediaType:itemId"
  mediaType: string;
  itemId: string;
  name: string;
  year?: number;
  watched: boolean;
  watchedAt?: string;
  externalIds?: Record<string, string>;
  // Episode-specific fields
  seasonNumber?: number;
  episodeNumber?: number;
  seriesId?: string;
  seriesName?: string;
}

export interface WatchStatusUpdate {
  mediaType: string;
  itemId: string;
  name?: string;
  year?: number;
  watched?: boolean;
  externalIds?: Record<string, string>;
  // Episode-specific
  seasonNumber?: number;
  episodeNumber?: number;
  seriesId?: string;
  seriesName?: string;
}

export interface EpisodeReference {
  seasonNumber: number;
  episodeNumber: number;
  episodeId?: string;
  tvdbId?: string;
  title?: string;
  overview?: string;
  runtimeMinutes?: number;
  airDate?: string;
  watchedAt?: string;
}

export interface SeriesWatchState {
  seriesId: string;
  seriesTitle: string;
  posterUrl?: string;
  backdropUrl?: string;
  year?: number;
  externalIds?: Record<string, string>;
  updatedAt: string;
  lastWatched: EpisodeReference;
  nextEpisode?: EpisodeReference | null;
  watchedEpisodes?: Record<string, EpisodeReference>;
  percentWatched?: number;
  resumePercent?: number;
}

export interface EpisodeWatchPayload {
  seriesId: string;
  seriesTitle: string;
  posterUrl?: string;
  backdropUrl?: string;
  year?: number;
  externalIds?: Record<string, string>;
  episode: EpisodeReference;
  nextEpisode?: EpisodeReference | null;
}

export interface PlaybackProgressUpdate {
  mediaType: 'movie' | 'episode';
  itemId: string;
  position: number;
  duration: number;
  timestamp?: string;
  externalIds?: Record<string, string>;
  // Episode-specific fields
  seasonNumber?: number;
  episodeNumber?: number;
  seriesId?: string;
  seriesName?: string;
  episodeName?: string;
  // Movie-specific fields
  movieName?: string;
  year?: number;
}

export interface PlaybackProgress {
  id: string;
  mediaType: 'movie' | 'episode';
  itemId: string;
  position: number;
  duration: number;
  percentWatched: number;
  updatedAt: string;
  externalIds?: Record<string, string>;
  // Episode-specific fields
  seasonNumber?: number;
  episodeNumber?: number;
  seriesId?: string;
  seriesName?: string;
  episodeName?: string;
  // Movie-specific fields
  movieName?: string;
  year?: number;
}

export interface UserProfile {
  id: string;
  name: string;
  color?: string;
  hasPin?: boolean; // Whether this profile has a PIN set (pinHash not exposed to frontend)
  createdAt: string;
  updatedAt: string;
}

// Per-user settings types
export interface UserPlaybackSettings {
  preferredPlayer: string;
  preferredAudioLanguage?: string;
  preferredSubtitleLanguage?: string;
  preferredSubtitleMode?: string;
  useLoadingScreen?: boolean;
}

export interface UserShelfConfig {
  id: string;
  name: string;
  enabled: boolean;
  order: number;
}

export interface UserHomeShelvesSettings {
  shelves: UserShelfConfig[];
  trendingMovieSource?: string;
}

export interface UserFilterSettings {
  maxSizeMovieGb: number;
  maxSizeEpisodeGb: number;
  excludeHdr: boolean;
  prioritizeHdr: boolean;
  filterOutTerms?: string[];
}

export interface UserLiveTVSettings {
  hiddenChannels: string[];
  favoriteChannels: string[];
  selectedCategories: string[];
}

export interface UserSettings {
  playback: UserPlaybackSettings;
  homeShelves: UserHomeShelvesSettings;
  filtering: UserFilterSettings;
  liveTV: UserLiveTVSettings;
}

// Prequeue types for pre-loading playback streams
export interface PrequeueRequest {
  titleId: string;
  titleName: string; // The actual title name for search queries
  mediaType: string; // "movie" or "series"
  userId: string;
  imdbId?: string;
  year?: number;
  seasonNumber?: number;
  episodeNumber?: number;
}

export interface PrequeueResponse {
  prequeueId: string;
  targetEpisode?: EpisodeReference;
  status: PrequeueStatus;
}

export type PrequeueStatus = 'queued' | 'searching' | 'resolving' | 'probing' | 'ready' | 'failed' | 'expired';

export interface PrequeueStatusResponse {
  prequeueId: string;
  status: PrequeueStatus;
  targetEpisode?: EpisodeReference;

  // When ready:
  streamPath?: string;
  displayName?: string; // For display instead of extracting from path (demo mode)
  fileSize?: number;
  healthStatus?: string;

  // HDR detection results
  hasDolbyVision?: boolean;
  hasHdr10?: boolean;
  dolbyVisionProfile?: string;

  // For HLS (HDR content):
  hlsSessionId?: string;
  hlsPlaylistUrl?: string;

  // Selected tracks (based on user preferences, -1 = default/none)
  selectedAudioTrack?: number;
  selectedSubtitleTrack?: number;

  // On failure:
  error?: string;
}

class ApiService {
  private baseUrl!: string;
  private fallbackUrls!: string[];
  private apiKey: string | null = null;
  private readonly playbackQueuePollIntervalMs = 1500;
  private readonly playbackQueueTimeoutMs = 120000;
  private readonly allowedInProgressStatuses = new Set(['queued', 'processing', 'pending', 'retrying']);
  private readonly allowedFinalStatuses = new Set(['healthy', 'partial', 'cached']);

  constructor(baseUrl?: string, apiKey?: string) {
    this.configure(baseUrl);
    this.setApiKey(apiKey);
  }

  getBaseUrl() {
    return this.baseUrl;
  }

  setBaseUrl(nextBaseUrl?: string | null) {
    this.configure(nextBaseUrl ?? undefined);
  }

  getApiKey() {
    return this.apiKey ?? '';
  }

  setApiKey(nextKey?: string | null) {
    const trimmed = nextKey?.trim() ?? '';
    this.apiKey = trimmed ? trimmed : null;
  }

  private configure(baseUrl?: string) {
    const config = getApiConfig();

    const defaultBase = this.normaliseUrl(config.BASE_URL) || 'http://localhost:7777/api';
    const resolvedBase = this.normaliseUrl(baseUrl) || defaultBase;
    this.baseUrl = resolvedBase;

    // Remove fallback URLs - only use the user-configured backend URL
    this.fallbackUrls = [];
  }

  private normaliseUrl(url?: string | null) {
    if (!url) {
      return '';
    }

    let trimmed = url.trim();

    if (!trimmed) {
      return '';
    }

    // Remove trailing slashes
    trimmed = trimmed.replace(/\/$/, '');

    // Fix double /api/api prefixes
    if (trimmed.endsWith('/api/api')) {
      trimmed = trimmed.substring(0, trimmed.length - 4); // Remove the extra /api
    }

    return trimmed;
  }

  private normaliseUserId(userId: string): string {
    const trimmed = userId?.trim();
    if (!trimmed) {
      throw new Error('User ID is required for this request');
    }
    return encodeURIComponent(trimmed);
  }

  private mergeHeaders(headers?: HeadersInit): Record<string, string> {
    if (!headers) {
      return {};
    }

    if (headers instanceof Headers) {
      const result: Record<string, string> = {};
      headers.forEach((value, key) => {
        result[key] = value;
      });
      return result;
    }

    if (Array.isArray(headers)) {
      return headers.reduce<Record<string, string>>((acc, [key, value]) => {
        acc[key] = value;
        return acc;
      }, {});
    }

    return { ...headers } as Record<string, string>;
  }

  private async request<T>(endpoint: string, options: RequestInit = {}): Promise<T> {
    const url = `${this.baseUrl}${endpoint}`;

    const requestInit: RequestInit = { ...options };
    const headerMap: Record<string, string> = this.mergeHeaders(options.headers);

    if (!headerMap['Content-Type']) {
      headerMap['Content-Type'] = 'application/json';
    }
    if (!headerMap['Accept']) {
      headerMap['Accept'] = 'application/json';
    }

    if (this.apiKey) {
      // Use PIN header for new authentication, fallback to API key for backward compatibility
      if (!headerMap['X-PIN']) {
        headerMap['X-PIN'] = this.apiKey;
      }
      if (!headerMap['X-API-Key']) {
        headerMap['X-API-Key'] = this.apiKey;
      }
      if (!headerMap['Authorization']) {
        headerMap['Authorization'] = `Bearer ${this.apiKey}`;
      }
    }

    requestInit.headers = headerMap;

    // Check if operation was aborted before making request
    if (options.signal?.aborted) {
      const error = new Error('Operation was aborted');
      error.name = 'AbortError';
      throw error;
    }

    const response = await fetch(url, requestInit);

    if (!response.ok) {
      const errorText = await response.text();
      const isAuthFailure = response.status === 401;
      // Use console.warn for handled errors (400/404 client errors, health check failures, auth issues) that surface via UI
      const isHandledError =
        response.status === 400 || response.status === 404 || response.status === 502 || isAuthFailure;
      const logLevel = isHandledError ? console.warn : console.error;
      logLevel('API request failed:', response.status, response.statusText, errorText);
      const apiError: ApiError = new Error(
        `API request failed: ${response.status} ${response.statusText} - ${errorText}`,
      );
      apiError.status = response.status;
      apiError.statusText = response.statusText;
      apiError.body = errorText;
      apiError.url = url;
      if (isAuthFailure) {
        apiError.code = 'AUTH_INVALID_PIN';
      }
      throw apiError;
    }

    const responseText = await response.text();
    const expectJson = headerMap['Accept']?.toLowerCase().includes('json') ?? true;
    if (!expectJson) {
      return responseText as T;
    }

    if (!responseText) {
      return undefined as T;
    }

    try {
      const data = JSON.parse(responseText);
      return data;
    } catch (parseError) {
      console.error('Failed to parse API response JSON:', parseError, responseText);
      const message = parseError instanceof Error ? parseError.message : 'Unknown parse error';
      throw new Error(`Failed to parse API response: ${message}`);
    }
  }

  // Discover trending movies
  async getTrendingMovies(userId?: string): Promise<TrendingItem[]> {
    const params = new URLSearchParams({ type: 'movie' });
    if (userId) {
      params.set('userId', userId);
    }
    return this.request<TrendingItem[]>(`/discover/new?${params.toString()}`);
  }

  // Discover trending TV shows
  async getTrendingTVShows(userId?: string): Promise<TrendingItem[]> {
    const params = new URLSearchParams({ type: 'series' });
    if (userId) {
      params.set('userId', userId);
    }
    return this.request<TrendingItem[]>(`/discover/new?${params.toString()}`);
  }

  // Search movies
  async searchMovies(query: string): Promise<SearchResult[]> {
    const encodedQuery = encodeURIComponent(query);
    return this.request<SearchResult[]>(`/search?q=${encodedQuery}&type=movie`);
  }

  // Search TV shows
  async searchTVShows(query: string): Promise<SearchResult[]> {
    const encodedQuery = encodeURIComponent(query);
    return this.request<SearchResult[]>(`/search?q=${encodedQuery}&type=series`);
  }

  async getSeriesDetails(params: {
    tvdbId?: string | number;
    tmdbId?: string | number;
    titleId?: string;
    name?: string;
    year?: number;
  }): Promise<SeriesDetails> {
    const searchParams = new URLSearchParams();
    if (params.tvdbId) {
      searchParams.set('tvdbId', String(params.tvdbId));
    }
    if (params.tmdbId) {
      searchParams.set('tmdbId', String(params.tmdbId));
    }
    if (params.titleId) {
      searchParams.set('titleId', params.titleId);
    }
    if (params.name) {
      searchParams.set('name', params.name);
    }
    if (typeof params.year === 'number' && Number.isFinite(params.year)) {
      searchParams.set('year', String(params.year));
    }

    const query = searchParams.toString();
    const endpoint = `/metadata/series/details${query ? `?${query}` : ''}`;
    return this.request<SeriesDetails>(endpoint);
  }

  async batchSeriesDetails(
    queries: Array<{
      tvdbId?: string | number;
      tmdbId?: string | number;
      titleId?: string;
      name?: string;
      year?: number;
    }>,
  ): Promise<BatchSeriesDetailsResponse> {
    const requestBody: BatchSeriesDetailsRequest = {
      queries: queries.map((q) => ({
        titleId: q.titleId,
        name: q.name,
        year: q.year,
        tvdbId: q.tvdbId ? Number(q.tvdbId) : undefined,
        tmdbId: q.tmdbId ? Number(q.tmdbId) : undefined,
      })),
    };

    return this.request<BatchSeriesDetailsResponse>('/metadata/series/batch', {
      method: 'POST',
      body: JSON.stringify(requestBody),
    });
  }

  async getMovieDetails(params: {
    tvdbId?: string | number;
    tmdbId?: string | number;
    titleId?: string;
    name?: string;
    year?: number;
    imdbId?: string;
  }): Promise<Title> {
    const searchParams = new URLSearchParams();
    if (params.tvdbId) {
      searchParams.set('tvdbId', String(params.tvdbId));
    }
    if (params.tmdbId) {
      searchParams.set('tmdbId', String(params.tmdbId));
    }
    if (params.titleId) {
      searchParams.set('titleId', params.titleId);
    }
    if (params.name) {
      searchParams.set('name', params.name);
    }
    if (params.imdbId) {
      searchParams.set('imdbId', params.imdbId);
    }
    if (typeof params.year === 'number' && Number.isFinite(params.year)) {
      searchParams.set('year', String(params.year));
    }

    const query = searchParams.toString();
    const endpoint = `/metadata/movies/details${query ? `?${query}` : ''}`;
    return this.request<Title>(endpoint);
  }

  async getTrailers(params: TrailerQuery): Promise<TrailerResponse> {
    const searchParams = new URLSearchParams();
    if (params.mediaType) {
      searchParams.set('type', params.mediaType);
    }
    if (params.titleId) {
      searchParams.set('titleId', params.titleId);
    }
    if (params.name) {
      searchParams.set('name', params.name);
    }
    if (typeof params.year === 'number' && Number.isFinite(params.year)) {
      searchParams.set('year', String(params.year));
    }
    if (params.imdbId) {
      searchParams.set('imdbId', params.imdbId);
    }
    if (typeof params.tmdbId === 'number' && Number.isFinite(params.tmdbId) && params.tmdbId > 0) {
      searchParams.set('tmdbId', String(Math.trunc(params.tmdbId)));
    }
    if (typeof params.tvdbId === 'number' && Number.isFinite(params.tvdbId) && params.tvdbId > 0) {
      searchParams.set('tvdbId', String(Math.trunc(params.tvdbId)));
    }

    const query = searchParams.toString();
    const endpoint = `/metadata/trailers${query ? `?${query}` : ''}`;
    const response = await this.request<TrailerResponse>(endpoint);
    return {
      primaryTrailer: response.primaryTrailer,
      trailers: Array.isArray(response.trailers) ? response.trailers : [],
    };
  }

  // Get settings
  async getSettings(): Promise<any> {
    return this.request('/settings');
  }

  // Update settings
  async updateSettings(settings: any): Promise<any> {
    return this.request('/settings', {
      method: 'PUT',
      body: JSON.stringify(settings),
    });
  }

  // Get user-specific settings (merged with global defaults)
  async getUserSettings(userId: string): Promise<UserSettings> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<UserSettings>(`/users/${safeUserId}/settings`);
  }

  // Update user-specific settings
  async updateUserSettings(userId: string, settings: UserSettings): Promise<UserSettings> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<UserSettings>(`/users/${safeUserId}/settings`, {
      method: 'PUT',
      body: JSON.stringify(settings),
    });
  }

  async getLivePlaylist(playlistUrl: string, signal?: AbortSignal): Promise<string> {
    const params = new URLSearchParams({ url: playlistUrl });
    return this.request<string>(`/live/playlist?${params.toString()}`, {
      headers: {
        Accept: 'text/plain',
      },
      signal,
    });
  }

  buildLiveStreamUrl(sourceUrl: string): string {
    const params = new URLSearchParams({ url: sourceUrl });
    const key = this.getApiKey().trim();
    if (key) {
      params.set('pin', key);
    }
    return `${this.baseUrl}/live/stream?${params.toString()}`;
  }

  async searchIndexer(
    query: string,
    limit = 50,
    categories: string[] = [],
    imdbId?: string,
    mediaType?: string,
    year?: number,
    userId?: string,
  ): Promise<NZBResult[]> {
    const params = new URLSearchParams();
    if (query) {
      params.append('q', query);
    }
    if (limit) {
      params.append('limit', String(limit));
    }
    if (imdbId) {
      params.append('imdbId', imdbId);
    }
    if (mediaType) {
      params.append('mediaType', mediaType);
    }
    if (year && year > 0) {
      params.append('year', String(year));
    }
    if (userId) {
      params.append('userId', userId);
    }
    categories.forEach((cat) => params.append('cat', cat));
    const qs = params.toString();
    const endpoint = `/indexers/search${qs ? `?${qs}` : ''}`;
    return this.request<NZBResult[]>(endpoint);
  }

  async resolvePlayback(
    result: NZBResult,
    options?: { onStatus?: (update: PlaybackResolutionResponse) => void; signal?: AbortSignal },
  ): Promise<PlaybackResolution> {
    try {
      const initial = await this.request<PlaybackResolutionResponse>('/playback/resolve', {
        method: 'POST',
        body: JSON.stringify({ result }),
        signal: options?.signal,
      });

      return await this.waitForPlaybackReady(initial, options?.onStatus, options?.signal);
    } catch (error) {
      const apiError = error as ApiError | undefined;
      if (apiError?.code === 'NZB_HEALTH_FAILED' || apiError?.code === 'PLAYBACK_QUEUE_TIMEOUT') {
        throw error;
      }

      const message = typeof apiError?.message === 'string' ? apiError.message : '';
      const body = typeof apiError?.body === 'string' ? apiError.body : '';
      const combined = `${message} ${body}`.toLowerCase();

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
        'queue item failed',
        'playback queue item failed',
        '502',
        // Debrid-specific health failures
        'not cached',
        'torrent not cached',
        'no media files found',
        'no download links',
      ];

      const indicatesHealthFailure =
        healthKeywords.some((keyword) => combined.includes(keyword)) || apiError?.status === 502;

      if (indicatesHealthFailure) {
        const reasonSource = message || body;
        const reasonMatch =
          reasonSource.match(/health (?:check|status)(?: reported)?\s*"?([a-z0-9 _-]+)/i) ||
          reasonSource.match(/reported\s+"?([a-z0-9 _-]+)"?/i) ||
          reasonSource.match(/(missing[_\s-]+segments?)/i);
        let reasonText = '';
        if (reasonMatch) {
          const captured = reasonMatch[1] ?? reasonMatch[0];
          if (captured) {
            reasonText = captured.replace(/[_-]+/g, ' ').trim().toLowerCase();
          }
        }

        const displayReason = reasonText ? `"${reasonText}"` : 'a failed health check';
        const healthError: ApiError = new Error(`Health check reported ${displayReason} for the requested release.`);
        healthError.code = 'NZB_HEALTH_FAILED';
        healthError.status = apiError?.status;
        healthError.statusText = apiError?.statusText;
        healthError.body = apiError?.body;
        healthError.url = apiError?.url;
        throw healthError;
      }

      throw error;
    }
  }

  async getPlaybackQueueStatus(queueId: number): Promise<PlaybackResolutionResponse> {
    const numericId = Number(queueId);
    if (!Number.isFinite(numericId) || numericId <= 0) {
      throw new Error('A valid queueId is required to poll playback status.');
    }

    const endpoint = `/playback/queue/${encodeURIComponent(String(numericId))}`;
    return this.request<PlaybackResolutionResponse>(endpoint);
  }

  // Prequeue API methods for pre-loading playback streams
  async prequeuePlayback(request: PrequeueRequest): Promise<PrequeueResponse> {
    return this.request<PrequeueResponse>('/playback/prequeue', {
      method: 'POST',
      body: JSON.stringify(request),
    });
  }

  async getPrequeueStatus(prequeueId: string): Promise<PrequeueStatusResponse> {
    if (!prequeueId?.trim()) {
      throw new Error('A valid prequeueId is required.');
    }
    const endpoint = `/playback/prequeue/${encodeURIComponent(prequeueId.trim())}`;
    return this.request<PrequeueStatusResponse>(endpoint);
  }

  // Check if prequeue is in an "in-progress" state (still loading)
  isPrequeueInProgress(status: PrequeueStatus): boolean {
    return ['queued', 'searching', 'resolving', 'probing'].includes(status);
  }

  // Check if prequeue is ready for playback
  isPrequeueReady(status: PrequeueStatus): boolean {
    return status === 'ready';
  }

  private async waitForPlaybackReady(
    initial: PlaybackResolutionResponse,
    onStatus?: (update: PlaybackResolutionResponse) => void,
    signal?: AbortSignal,
  ): Promise<PlaybackResolution> {
    // Check if already aborted
    if (signal?.aborted) {
      const error = new Error('Operation was aborted');
      error.name = 'AbortError';
      throw error;
    }

    let lastStatus: string | null = null;
    let lastPath: string | null = null;

    const emitStatus = (update: PlaybackResolutionResponse, normalizedStatus: string) => {
      if (!onStatus) {
        return;
      }

      const trimmedStatus = normalizedStatus.trim();
      const normalizedPath = typeof update.webdavPath === 'string' ? update.webdavPath.trim() : '';

      if (trimmedStatus === lastStatus && normalizedPath === lastPath) {
        return;
      }

      lastStatus = trimmedStatus;
      lastPath = normalizedPath;
      onStatus({ ...update, healthStatus: trimmedStatus });
    };

    let current = initial;
    let status = this.normalizeHealthStatus(current.healthStatus);
    this.ensureStatusAllowed(status);

    const normalizedInitial: PlaybackResolutionResponse = { ...current, healthStatus: status };

    if (this.isResolutionReady(normalizedInitial)) {
      const ready = this.normalizeReadyResolution(normalizedInitial);
      emitStatus(ready, this.normalizeHealthStatus(ready.healthStatus));
      return ready;
    }

    emitStatus(normalizedInitial, status);

    const queueId = Number(normalizedInitial.queueId);
    if (!Number.isFinite(queueId) || queueId <= 0) {
      throw new Error('Playback queue id was not provided by the backend.');
    }

    const started = Date.now();
    while (Date.now() - started < this.playbackQueueTimeoutMs) {
      // Check if aborted during polling
      if (signal?.aborted) {
        const error = new Error('Operation was aborted');
        error.name = 'AbortError';
        throw error;
      }

      await this.delay(this.playbackQueuePollIntervalMs);

      // Check again after delay
      if (signal?.aborted) {
        const error = new Error('Operation was aborted');
        error.name = 'AbortError';
        throw error;
      }

      current = await this.request<PlaybackResolutionResponse>(`/playback/queue/${queueId}`, { signal });
      status = this.normalizeHealthStatus(current.healthStatus);
      this.ensureStatusAllowed(status);

      const normalizedCurrent: PlaybackResolutionResponse = { ...current, healthStatus: status };

      if (this.isResolutionReady(normalizedCurrent)) {
        const ready = this.normalizeReadyResolution(normalizedCurrent);
        emitStatus(ready, this.normalizeHealthStatus(ready.healthStatus));
        return ready;
      }

      emitStatus(normalizedCurrent, status);
    }

    const timeoutError: ApiError = new Error('Timed out waiting for the backend to prepare playback.');
    timeoutError.code = 'PLAYBACK_QUEUE_TIMEOUT';
    throw timeoutError;
  }

  private async delay(ms: number): Promise<void> {
    if (ms <= 0) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, ms));
  }

  private normalizeHealthStatus(status?: string): string {
    return typeof status === 'string' ? status.trim().toLowerCase() : '';
  }

  private ensureStatusAllowed(status: string): void {
    if (!status) {
      return;
    }

    if (this.allowedFinalStatuses.has(status) || this.allowedInProgressStatuses.has(status)) {
      return;
    }

    throw this.createHealthErrorFromStatus(status);
  }

  private isResolutionReady(resolution: PlaybackResolutionResponse): boolean {
    return typeof resolution.webdavPath === 'string' && resolution.webdavPath.trim().length > 0;
  }

  private normalizeReadyResolution(resolution: PlaybackResolutionResponse): PlaybackResolution {
    const webdavPath = resolution.webdavPath?.trim();
    if (!webdavPath) {
      throw new Error('Playback resolution is missing a webdavPath.');
    }

    const normalizedStatus = this.normalizeHealthStatus(resolution.healthStatus) || 'healthy';
    const queueId = Number(resolution.queueId);

    const ready: PlaybackResolution = {
      queueId: Number.isFinite(queueId) ? queueId : 0,
      webdavPath,
      healthStatus: normalizedStatus,
    };

    if (typeof resolution.fileSize === 'number') {
      ready.fileSize = resolution.fileSize;
    }

    if (typeof resolution.sourceNzbPath === 'string' && resolution.sourceNzbPath.trim()) {
      ready.sourceNzbPath = resolution.sourceNzbPath.trim();
    }

    return ready;
  }

  private createHealthErrorFromStatus(status: string): ApiError {
    const friendly = status.replace(/[_-]+/g, ' ').trim();
    const reason = friendly ? `"${friendly}"` : 'a failed health check';
    const error: ApiError = new Error(`Health check reported ${reason} for the requested release.`);
    error.code = 'NZB_HEALTH_FAILED';
    return error;
  }

  async checkUsenetHealth(result: NZBResult): Promise<NZBHealthCheck> {
    return this.request<NZBHealthCheck>('/usenet/health', {
      method: 'POST',
      body: JSON.stringify({ result }),
    });
  }

  async checkDebridCached(result: NZBResult): Promise<DebridHealthCheck> {
    return this.request<DebridHealthCheck>('/debrid/cached', {
      method: 'POST',
      body: JSON.stringify({ result }),
    });
  }

  async getUsers(): Promise<UserProfile[]> {
    return this.request<UserProfile[]>('/users');
  }

  async createUser(name: string): Promise<UserProfile> {
    return this.request<UserProfile>('/users', {
      method: 'POST',
      body: JSON.stringify({ name }),
    });
  }

  async renameUser(id: string, name: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(id);
    return this.request<UserProfile>(`/users/${safeId}`, {
      method: 'PATCH',
      body: JSON.stringify({ name }),
    });
  }

  async updateUserColor(id: string, color: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(id);
    return this.request<UserProfile>(`/users/${safeId}/color`, {
      method: 'PUT',
      body: JSON.stringify({ color }),
    });
  }

  async setUserPin(id: string, pin: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(id);
    return this.request<UserProfile>(`/users/${safeId}/pin`, {
      method: 'PUT',
      body: JSON.stringify({ pin }),
    });
  }

  async clearUserPin(id: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(id);
    return this.request<UserProfile>(`/users/${safeId}/pin`, {
      method: 'DELETE',
    });
  }

  async verifyUserPin(id: string, pin: string): Promise<boolean> {
    const safeId = this.normaliseUserId(id);
    try {
      const result = await this.request<{ valid: boolean }>(`/users/${safeId}/pin/verify`, {
        method: 'POST',
        body: JSON.stringify({ pin }),
      });
      return result.valid;
    } catch (error) {
      // 401 means invalid PIN
      if ((error as ApiError).status === 401) {
        return false;
      }
      throw error;
    }
  }

  async deleteUser(id: string): Promise<void> {
    const safeId = this.normaliseUserId(id);
    await this.request<void>(`/users/${safeId}`, {
      method: 'DELETE',
    });
  }

  async getWatchlist(userId: string): Promise<WatchlistItem[]> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<WatchlistItem[]>(`/users/${safeUserId}/watchlist`);
  }

  async addToWatchlist(userId: string, payload: WatchlistUpsertPayload): Promise<WatchlistItem> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<WatchlistItem>(`/users/${safeUserId}/watchlist`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  }

  async updateWatchlistState(
    userId: string,
    mediaType: string,
    id: string,
    update: WatchlistStateUpdate,
  ): Promise<WatchlistItem> {
    const safeMediaType = encodeURIComponent(mediaType);
    const safeId = encodeURIComponent(id);
    const safeUserId = this.normaliseUserId(userId);
    return this.request<WatchlistItem>(`/users/${safeUserId}/watchlist/${safeMediaType}/${safeId}`, {
      method: 'PATCH',
      body: JSON.stringify(update),
    });
  }

  async removeFromWatchlist(userId: string, mediaType: string, id: string): Promise<void> {
    const safeMediaType = encodeURIComponent(mediaType);
    const safeId = encodeURIComponent(id);
    const safeUserId = this.normaliseUserId(userId);
    await this.request<void>(`/users/${safeUserId}/watchlist/${safeMediaType}/${safeId}`, {
      method: 'DELETE',
    });
  }

  async getContinueWatching(userId: string): Promise<SeriesWatchState[]> {
    const safeUserId = this.normaliseUserId(userId);
    // Add cache-busting to ensure fresh data when navigating back
    return this.request<SeriesWatchState[]>(`/users/${safeUserId}/history/continue`, {
      cache: 'no-store',
      headers: {
        'Cache-Control': 'no-cache, no-store, must-revalidate',
        Pragma: 'no-cache',
      },
    });
  }

  async getSeriesWatchState(userId: string, seriesId: string): Promise<SeriesWatchState | null> {
    const safeUserId = this.normaliseUserId(userId);
    const safeSeriesId = encodeURIComponent(seriesId.trim());
    try {
      return await this.request<SeriesWatchState>(`/users/${safeUserId}/history/series/${safeSeriesId}`);
    } catch (error) {
      const apiError = error as ApiError | undefined;
      if (apiError?.status === 404) {
        return null;
      }
      throw error;
    }
  }

  async hideFromContinueWatching(userId: string, seriesId: string): Promise<void> {
    const safeUserId = this.normaliseUserId(userId);
    const safeSeriesId = encodeURIComponent(seriesId.trim());
    await this.request<void>(`/users/${safeUserId}/history/continue/${safeSeriesId}/hide`, {
      method: 'POST',
    });
  }

  async recordEpisodeWatch(userId: string, payload: EpisodeWatchPayload): Promise<SeriesWatchState> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<SeriesWatchState>(`/users/${safeUserId}/history/episodes`, {
      method: 'POST',
      body: JSON.stringify(payload),
    });
  }

  async updatePlaybackProgress(userId: string, update: PlaybackProgressUpdate): Promise<PlaybackProgress> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<PlaybackProgress>(`/users/${safeUserId}/history/progress`, {
      method: 'POST',
      body: JSON.stringify({
        ...update,
        timestamp: new Date().toISOString(),
      }),
    });
  }

  async getPlaybackProgress(userId: string, mediaType: string, itemId: string): Promise<PlaybackProgress | null> {
    const safeUserId = this.normaliseUserId(userId);
    try {
      return await this.request<PlaybackProgress>(
        `/users/${safeUserId}/history/progress/${mediaType}/${encodeURIComponent(itemId)}`,
      );
    } catch (error) {
      const apiError = error as { status?: number };
      if (apiError?.status === 404) {
        return null;
      }
      throw error;
    }
  }

  async listPlaybackProgress(userId: string): Promise<PlaybackProgress[]> {
    const safeUserId = this.normaliseUserId(userId);
    // Add cache-busting to ensure fresh data when navigating back
    return this.request<PlaybackProgress[]>(`/users/${safeUserId}/history/progress`, {
      cache: 'no-store',
      headers: {
        'Cache-Control': 'no-cache, no-store, must-revalidate',
        Pragma: 'no-cache',
      },
    });
  }

  async deletePlaybackProgress(userId: string, mediaType: string, itemId: string): Promise<void> {
    const safeUserId = this.normaliseUserId(userId);
    await this.request<void>(`/users/${safeUserId}/history/progress/${mediaType}/${encodeURIComponent(itemId)}`, {
      method: 'DELETE',
    });
  }

  async getVideoMetadata(path: string): Promise<VideoMetadata> {
    const params = new URLSearchParams({ path });
    const authKey = this.getApiKey().trim();
    if (!authKey) {
      console.warn('[api] getVideoMetadata missing authentication key; metadata request may fail');
    } else {
      params.set('pin', authKey);
    }
    return this.request<VideoMetadata>(`/video/metadata?${params.toString()}`);
  }

  /**
   * Get the direct download URL for a given path.
   * For debrid paths, this unrestricts the link and returns the CDN URL.
   * Useful for external players like Infuse that don't need our proxy.
   */
  async getDirectUrl(path: string): Promise<{ url: string }> {
    const params = new URLSearchParams({ path });
    const authKey = this.getApiKey().trim();
    if (authKey) {
      params.set('apiKey', authKey);
    }
    return this.request<{ url: string }>(`/video/direct-url?${params.toString()}`);
  }

  async createHlsSession(params: {
    path: string;
    dv?: boolean;
    dvProfile?: string;
    hdr?: boolean;
    forceAAC?: boolean;
    start?: number;
    audioTrack?: number;
    subtitleTrack?: number;
    apiKey?: string;
  }): Promise<HlsSessionStartResponse> {
    const trimmedPath = params.path?.trim();
    if (!trimmedPath) {
      throw new Error('Path is required to create an HLS session.');
    }

    const search = new URLSearchParams();
    search.set('path', trimmedPath);

    const authKey = params.apiKey?.trim() || this.apiKey?.trim() || '';
    if (authKey) {
      search.set('apiKey', authKey);
    }

    if (params.dv) {
      search.set('dv', 'true');
    }
    if (params.dvProfile) {
      search.set('dvProfile', params.dvProfile);
    }
    if (params.hdr) {
      search.set('hdr', 'true');
    }
    if (params.forceAAC) {
      search.set('forceAAC', 'true');
    }
    if (typeof params.start === 'number' && Number.isFinite(params.start) && params.start >= 0) {
      search.set('start', params.start.toFixed(3));
    }
    if (typeof params.audioTrack === 'number' && Number.isFinite(params.audioTrack) && params.audioTrack >= 0) {
      search.set('audioTrack', params.audioTrack.toString());
    }
    if (
      typeof params.subtitleTrack === 'number' &&
      Number.isFinite(params.subtitleTrack) &&
      params.subtitleTrack >= 0
    ) {
      search.set('subtitleTrack', params.subtitleTrack.toString());
    }

    return this.request<HlsSessionStartResponse>(`/video/hls/start?${search.toString()}`);
  }

  /**
   * Send a keepalive ping for an HLS session to prevent idle timeout while paused
   * @param sessionId - The HLS session ID
   * @param currentTime - Optional current playback time in seconds for rate limiting
   */
  async keepaliveHlsSession(sessionId: string, currentTime?: number): Promise<void> {
    if (!sessionId) {
      throw new Error('Session ID is required for keepalive');
    }
    const params = currentTime !== undefined && currentTime >= 0 ? `?time=${currentTime}` : '';
    await this.request(`/video/hls/${encodeURIComponent(sessionId)}/keepalive${params}`, {
      method: 'POST',
    });
  }

  /**
   * Get the status of an HLS session, including any fatal errors
   * Used for polling during playback to detect stream errors
   */
  async getHlsSessionStatus(sessionId: string): Promise<HlsSessionStatus> {
    if (!sessionId) {
      throw new Error('Session ID is required for status');
    }
    return this.request<HlsSessionStatus>(`/video/hls/${encodeURIComponent(sessionId)}/status`);
  }

  // Watch Status API methods (now using History API)
  async getWatchStatus(userId: string): Promise<WatchStatusItem[]> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<WatchStatusItem[]>(`/users/${safeUserId}/history/watched`);
  }

  async getWatchStatusItem(userId: string, mediaType: string, id: string): Promise<WatchStatusItem | null> {
    const safeUserId = this.normaliseUserId(userId);
    const safeMediaType = encodeURIComponent(mediaType);
    const safeId = encodeURIComponent(id);
    try {
      return await this.request<WatchStatusItem>(`/users/${safeUserId}/history/watched/${safeMediaType}/${safeId}`);
    } catch (error) {
      const apiError = error as ApiError | undefined;
      if (apiError?.status === 404) {
        return null;
      }
      throw error;
    }
  }

  async updateWatchStatus(userId: string, update: WatchStatusUpdate): Promise<WatchStatusItem> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<WatchStatusItem>(`/users/${safeUserId}/history/watched`, {
      method: 'POST',
      body: JSON.stringify(update),
    });
  }

  async toggleWatchStatus(
    userId: string,
    mediaType: string,
    id: string,
    metadata?: Partial<WatchStatusUpdate>,
  ): Promise<WatchStatusItem> {
    const safeUserId = this.normaliseUserId(userId);
    const safeMediaType = encodeURIComponent(mediaType);
    const safeId = encodeURIComponent(id);
    return this.request<WatchStatusItem>(`/users/${safeUserId}/history/watched/${safeMediaType}/${safeId}/toggle`, {
      method: 'POST',
      body: metadata ? JSON.stringify(metadata) : undefined,
    });
  }

  async removeWatchStatus(userId: string, mediaType: string, id: string): Promise<void> {
    const safeUserId = this.normaliseUserId(userId);
    const safeMediaType = encodeURIComponent(mediaType);
    const safeId = encodeURIComponent(id);
    await this.request<void>(`/users/${safeUserId}/history/watched/${safeMediaType}/${safeId}`, {
      method: 'DELETE',
    });
  }

  async bulkUpdateWatchStatus(userId: string, updates: WatchStatusUpdate[]): Promise<WatchStatusItem[]> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<WatchStatusItem[]>(`/users/${safeUserId}/history/watched/bulk`, {
      method: 'POST',
      body: JSON.stringify(updates),
    });
  }

  // Subtitle search methods
  async searchSubtitles(params: {
    imdbId?: string;
    title?: string;
    year?: number;
    season?: number;
    episode?: number;
    language?: string;
  }): Promise<SubtitleSearchResult[]> {
    const query = new URLSearchParams();
    if (params.imdbId) query.set('imdbId', params.imdbId);
    if (params.title) query.set('title', params.title);
    if (params.year !== undefined) query.set('year', String(params.year));
    if (params.season !== undefined) query.set('season', String(params.season));
    if (params.episode !== undefined) query.set('episode', String(params.episode));
    if (params.language) query.set('language', params.language);

    return this.request<SubtitleSearchResult[]>(`/subtitles/search?${query.toString()}`);
  }

  getSubtitleDownloadUrl(params: {
    subtitleId: string;
    provider: string;
    imdbId?: string;
    title?: string;
    year?: number;
    season?: number;
    episode?: number;
    language?: string;
  }): string {
    const query = new URLSearchParams();
    query.set('subtitleId', params.subtitleId);
    query.set('provider', params.provider);
    if (params.imdbId) query.set('imdbId', params.imdbId);
    if (params.title) query.set('title', params.title);
    if (params.year !== undefined) query.set('year', String(params.year));
    if (params.season !== undefined) query.set('season', String(params.season));
    if (params.episode !== undefined) query.set('episode', String(params.episode));
    if (params.language) query.set('language', params.language);
    if (this.apiKey) query.set('apiKey', this.apiKey);

    return `${this.baseUrl}/subtitles/download?${query.toString()}`;
  }
}

export { ApiService };

// Default instance - API key can be set via EXPO_PUBLIC_API_KEY env var or settings screen
const defaultApiKey = process.env.EXPO_PUBLIC_API_KEY || undefined;
export const apiService = new ApiService(undefined, defaultApiKey);
export default apiService;
