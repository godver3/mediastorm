// API service for strmr backend integration

import { Platform } from 'react-native';
import { getApiConfig } from '../config/api';

// Network request profiling
const DEBUG_API_TIMING = __DEV__ && true; // Set to true to enable API request timing
const apiRequestStats: { endpoint: string; duration: number; method: string }[] = [];
let apiRequestCount = 0;
let totalApiTime = 0;

// Log API stats periodically
if (DEBUG_API_TIMING && Platform.isTV) {
  setInterval(() => {
    if (apiRequestCount > 0) {
      const avgTime = totalApiTime / apiRequestCount;
      const slowRequests = apiRequestStats.filter((r) => r.duration > 500).slice(-5);
      console.log(
        `[API:Perf] ${apiRequestCount} requests, avg=${avgTime.toFixed(0)}ms` +
          (slowRequests.length > 0
            ? `, slow: ${slowRequests.map((r) => `${r.endpoint}=${r.duration.toFixed(0)}ms`).join(', ')}`
            : ''),
      );
      // Reset counters
      apiRequestCount = 0;
      totalApiTime = 0;
      apiRequestStats.length = 0;
    }
  }, 10000);
}

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
  is_dark?: boolean;
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
  seasonNumber?: number; // 0 = show-level trailer, >0 = season-specific
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

export interface Rating {
  source: string; // imdb, tmdb, trakt, letterboxd, tomatoes, audience, metacritic
  value: number;
  max: number;
}

export interface CastMember {
  id: number;
  name: string;
  character: string;
  order: number;
  profilePath?: string;
  profileUrl?: string;
}

export interface Credits {
  cast: CastMember[];
}

export interface Collection {
  id: number;
  name: string;
  poster?: Image;
  backdrop?: Image;
}

export interface CollectionDetails {
  id: number;
  name: string;
  overview?: string;
  poster?: Image;
  backdrop?: Image;
  movies: Title[];
}

export interface Person {
  id: number;
  name: string;
  biography?: string;
  birthday?: string;
  deathday?: string;
  placeOfBirth?: string;
  profileUrl?: string;
  knownFor?: string; // "Acting", "Directing", etc.
}

export interface PersonDetails {
  person: Person;
  filmography: Title[];
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
  backdrops?: Image[]; // Additional backdrop options beyond the primary
  logo?: Image;
  mediaType: string;
  tvdbId?: number;
  imdbId?: string;
  tmdbId?: number;
  popularity?: number;
  network?: string;
  status?: string; // For series: "Continuing", "Ended", "Upcoming", etc.
  certification?: string; // MPAA/TV content rating (G, PG, PG-13, R, TV-Y, TV-G, TV-PG, TV-14, TV-MA)
  primaryTrailer?: Trailer;
  trailers?: Trailer[];
  releases?: ReleaseWindow[];
  theatricalRelease?: ReleaseWindow;
  homeRelease?: ReleaseWindow;
  ratings?: Rating[];
  credits?: Credits;
  runtimeMinutes?: number; // Runtime in minutes (movies only)
  collection?: Collection; // Movie collection (movies only)
  genres?: string[]; // Genre names from TMDB
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
  absoluteEpisodeNumber?: number;
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

export interface BatchMovieReleasesQuery {
  titleId?: string;
  tmdbId?: number;
  imdbId?: string;
}

export interface BatchMovieReleasesRequest {
  queries: BatchMovieReleasesQuery[];
}

export interface BatchMovieReleasesItem {
  query: BatchMovieReleasesQuery;
  theatricalRelease?: ReleaseWindow;
  homeRelease?: ReleaseWindow;
  error?: string;
}

export interface BatchMovieReleasesResponse {
  results: BatchMovieReleasesItem[];
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
  season?: number; // Season number for season-specific trailers (0 or undefined = show-level)
}

export type TrailerPrequeueStatus = 'pending' | 'downloading' | 'ready' | 'failed';

export interface TrailerPrequeueResponse {
  id: string;
  status: TrailerPrequeueStatus;
  error?: string;
  fileSize?: number;
}

export interface MetadataProgressTask {
  id: string;
  label: string;
  phase: string;
  current: number;
  total: number;
  startedAt: number;
}

export interface MetadataProgressSnapshot {
  tasks: MetadataProgressTask[];
  activeCount: number;
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
  episodeCount?: number; // Number of episodes in pack (0 if not a pack)
  sizePerFile?: boolean; // True when sizeBytes is per-file (Stremio scrapers), false when total pack
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

export interface AudioTrackInfo {
  index: number;
  language: string;
  codec: string;
  title?: string;
}

export interface SubtitleTrackInfo {
  index: number;
  language: string;
  codec: string;
  title?: string;
  forced: boolean;
  isBitmap: boolean;
  bitmapType?: string;
}

export interface DebridHealthCheck {
  healthy: boolean;
  status: string;
  cached: boolean;
  provider: string;
  infoHash?: string;
  errorMessage?: string;
  // Track info (populated when cached)
  audioTracks?: AudioTrackInfo[];
  subtitleTracks?: SubtitleTrackInfo[];
  trackProbeError?: string;
  tracksLoading?: boolean;
}

// SubtitleSessionInfo represents a pre-extracted subtitle track session
export interface SubtitleSessionInfo {
  sessionId: string;
  vttUrl: string;
  trackIndex: number;
  language: string;
  title: string;
  codec: string;
  isForced: boolean;
  isExtracting: boolean; // true if extraction is still in progress
  firstCueTime?: number; // Time of first extracted cue (for subtitle sync)
}

export interface PlaybackResolutionResponse {
  queueId: number;
  webdavPath?: string;
  healthStatus: string;
  fileSize?: number;
  sourceNzbPath?: string;
  // Pre-extracted subtitles (for manual selection path)
  subtitleSessions?: Record<number, SubtitleSessionInfo>;
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
  isBitmap?: boolean; // True for PGS, HDMV, DVD subtitles (native player only, can't extract to VTT)
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

export interface Client {
  id: string;
  userId: string;
  name: string;
  deviceType: string;
  os: string;
  appVersion: string;
  lastSeenAt: string;
  firstSeenAt: string;
  filterEnabled: boolean;
}

export interface ClientFilterSettings {
  maxSizeMovieGb?: number;
  maxSizeEpisodeGb?: number;
  maxResolution?: string;
  excludeHdr?: boolean;
  prioritizeHdr?: boolean;
  filterOutTerms?: string[];
  preferredTerms?: string[];
  // Network settings for URL switching based on WiFi
  homeWifiSSID?: string;
  homeBackendUrl?: string;
  remoteBackendUrl?: string;
}

export interface HlsSessionStartResponse {
  sessionId: string;
  playlistUrl: string;
  duration?: number;
  startOffset?: number;
  actualStartOffset?: number; // Keyframe-aligned start time for subtitle sync
  keyframeDelta?: number; // Delta between actual keyframe and requested position (negative = earlier)
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

export interface HlsSeekResponse {
  sessionId: string;
  startOffset: number;
  actualStartOffset?: number; // Keyframe-aligned start time for subtitle sync
  keyframeDelta?: number; // Delta between actual keyframe and requested position (negative = earlier)
  duration?: number;
  playlistUrl: string;
}

export interface HlsKeepaliveResponse {
  status: string;
  startOffset: number;
  actualStartOffset?: number; // Keyframe-aligned start time for subtitle sync
  keyframeDelta?: number; // Delta between actual keyframe and requested position (negative = earlier)
  segmentDuration: number;
  duration?: number;
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
  absoluteEpisodeNumber?: number;
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
  overview?: string;
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
  // Episode counts for tracking series completion (excludes specials/season 0)
  watchedEpisodeCount?: number;
  totalEpisodeCount?: number;
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
  iconUrl?: string; // Local filename of downloaded profile icon image
  hasPin?: boolean; // Whether this profile has a PIN set (pinHash not exposed to frontend)
  hasIcon?: boolean; // Whether this profile has a custom icon set
  isKidsProfile?: boolean; // Whether this is a kids profile with content restrictions
  // Kids profile content restriction settings
  kidsMode?: 'rating' | 'content_list' | ''; // Restriction mode
  kidsMaxRating?: string; // Deprecated: use kidsMaxMovieRating/kidsMaxTVRating instead
  kidsMaxMovieRating?: string; // Max allowed movie rating: G, PG, PG-13, R, NC-17
  kidsMaxTVRating?: string; // Max allowed TV rating: TV-Y, TV-Y7, TV-G, TV-PG, TV-14, TV-MA
  kidsAllowedLists?: string[]; // MDBList URLs allowed for content_list mode
  traktAccountId?: string; // ID of linked Trakt account
  createdAt: string;
  updatedAt: string;
}

// Trakt account types
export interface TraktAccount {
  id: string;
  name: string;
  username?: string; // Trakt username (populated after OAuth)
  connected: boolean; // Whether OAuth tokens are present
  scrobblingEnabled: boolean;
  expiresAt?: number; // Token expiry timestamp
  linkedProfiles?: string[]; // Profile IDs using this account
}

export interface TraktAccountsResponse {
  accounts: TraktAccount[];
}

export interface TraktDeviceCodeResponse {
  deviceCode: string;
  userCode: string;
  verificationUrl: string;
  expiresIn: number;
  interval: number;
}

export interface TraktAuthCheckResponse {
  authenticated: boolean;
  pending?: boolean;
  username?: string;
}

// Per-user settings types
export interface UserPlaybackSettings {
  preferredPlayer: string;
  preferredAudioLanguage?: string;
  preferredSubtitleLanguage?: string;
  preferredSubtitleMode?: string;
  useLoadingScreen?: boolean;
  subtitleSize?: number;
}

export interface UserShelfConfig {
  id: string;
  name: string;
  enabled: boolean;
  order: number;
  type?: 'builtin' | 'mdblist'; // Type of shelf - builtin or custom MDBList
  listUrl?: string; // MDBList URL for custom lists
  limit?: number; // Optional limit on number of items returned (0 = unlimited)
  hideUnreleased?: boolean; // Filter out unreleased/in-theaters content
}

export interface UserHomeShelvesSettings {
  shelves: UserShelfConfig[];
  exploreCardPosition?: 'front' | 'end';
}

export interface UserFilterSettings {
  maxSizeMovieGb: number;
  maxSizeEpisodeGb: number;
  excludeHdr: boolean;
  prioritizeHdr: boolean;
  filterOutTerms?: string[];
}

export interface MultiscreenChannel {
  id: string;
  name: string;
  url: string;
  streamUrl: string;
  logo?: string;
}

export interface MultiscreenSession {
  channels: MultiscreenChannel[];
  activeAudioIndex: number;
}

// Backend-parsed Live TV channel
export interface LiveChannel {
  id: string;
  name: string;
  url: string;
  logo?: string;
  group?: string;
  tvgId?: string;
  tvgName?: string;
  tvgLanguage?: string;
  streamUrl?: string;
}

// Response from GET /live/channels
export interface LiveChannelsResponse {
  channels: LiveChannel[];
  totalBeforeFilter: number;
  availableCategories: string[];
}

// EPG (Electronic Program Guide) types
export interface EPGProgram {
  channelId: string;
  title: string;
  description?: string;
  start: string; // ISO 8601 datetime
  stop: string; // ISO 8601 datetime
  icon?: string;
  categories?: string[];
  episode?: string; // e.g., "S01E05"
  rating?: string;
}

export interface EPGNowPlaying {
  channelId: string;
  current?: EPGProgram;
  next?: EPGProgram;
}

export interface EPGStatus {
  enabled: boolean;
  lastRefresh?: string;
  lastError?: string;
  channelCount: number;
  programCount: number;
  refreshing: boolean;
  sourceCount: number;
}

// Response from GET /live/epg/now (array of EPGNowPlaying)
export type EPGNowPlayingResponse = EPGNowPlaying[];

// Category info with channel count
export interface CategoryInfo {
  name: string;
  channelCount: number;
}

// Response from GET /live/categories
export interface CategoriesResponse {
  categories: CategoryInfo[];
}

export interface UserLiveTVSettings {
  hiddenChannels: string[];
  favoriteChannels: string[];
  selectedCategories: string[];
  multiscreenSession?: MultiscreenSession;
}

export interface UserDisplaySettings {
  badgeVisibility: string[]; // "watchProgress", "releaseStatus", "watchState", "unwatchedCount"
  watchStateIconStyle?: 'colored' | 'white'; // "colored" (default) = green/yellow, "white" = all white
}

export interface UserNetworkSettings {
  homeWifiSSID: string; // WiFi SSID to detect for home network
  homeBackendUrl: string; // Backend URL when on home WiFi
  remoteBackendUrl: string; // Backend URL when on mobile/other networks
}

export interface UserSettings {
  playback: UserPlaybackSettings;
  homeShelves: UserHomeShelvesSettings;
  filtering: UserFilterSettings;
  liveTV: UserLiveTVSettings;
  display: UserDisplaySettings;
  network: UserNetworkSettings;
}

// Combined startup data (reduces ~21 HTTP requests to 1 for low-power devices)
export interface TrendingResponse {
  items: TrendingItem[];
  total: number;
  unfilteredTotal?: number;
}

export interface StartupData {
  userSettings: UserSettings | null;
  watchlist: WatchlistItem[];
  watchlistTotal: number;
  continueWatching: SeriesWatchState[];
  continueWatchingTotal: number;
  watchHistory: WatchStatusItem[];
  trendingMovies: TrendingResponse;
  trendingSeries: TrendingResponse;
}

export interface DetailsBundleData {
  seriesDetails: SeriesDetails | null;
  movieDetails: Title | null;
  similar: Title[];
  trailers: TrailerResponse | null;
  contentPreference: ContentPreference | null;
  watchState: SeriesWatchState | null;
  playbackProgress: PlaybackProgress[];
}

// Per-content language preferences (overrides user settings for specific content)
export interface ContentPreference {
  contentId: string; // e.g., "tmdb:tv:12345" for series, "tmdb:movie:67890" for movies
  contentType: 'series' | 'movie';
  audioLanguage?: string; // ISO 639-2 code (eng, jpn, spa, etc.)
  subtitleLanguage?: string; // ISO 639-2 code or empty
  subtitleMode?: 'off' | 'on' | 'forced-only';
  updatedAt?: string;
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
  absoluteEpisodeNumber?: number; // For anime: absolute episode number (e.g., 1153 for One Piece)
  startOffset?: number; // Resume position in seconds for subtitle extraction
  skipHLS?: boolean; // Native clients skip HLS session creation (they use direct streaming)
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
  userId?: string; // The user who created this prequeue
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

  // Audio transcoding detection (TrueHD, DTS, etc.)
  needsAudioTranscode?: boolean;

  // For HLS (HDR content or audio transcoding):
  hlsSessionId?: string;
  hlsPlaylistUrl?: string;
  duration?: number; // Total duration in seconds (from ffprobe)

  // Selected tracks (based on user preferences, -1 = default/none)
  selectedAudioTrack?: number;
  selectedSubtitleTrack?: number;

  // Available tracks (for display in UI)
  audioTracks?: AudioTrackInfo[];
  subtitleTracks?: SubtitleTrackInfo[];

  // Pre-extracted subtitle sessions (for direct streaming/VLC path)
  subtitleSessions?: Record<number, SubtitleSessionInfo>;

  // AIOStreams passthrough format
  passthroughName?: string; // Raw display name from AIOStreams
  passthroughDescription?: string; // Raw description from AIOStreams

  // On failure:
  error?: string;
}

class ApiService {
  private baseUrl!: string;
  private fallbackUrls!: string[];
  private authToken: string | null = null;
  private clientId: string | null = null;
  private readonly playbackQueuePollIntervalMs = 1500;
  private readonly playbackQueueTimeoutMs = 120000;
  private readonly allowedInProgressStatuses = new Set(['queued', 'processing', 'pending', 'retrying']);
  private readonly allowedFinalStatuses = new Set(['healthy', 'partial', 'cached']);

  constructor(baseUrl?: string) {
    this.configure(baseUrl);
  }

  getBaseUrl() {
    return this.baseUrl;
  }

  setBaseUrl(nextBaseUrl?: string | null) {
    this.configure(nextBaseUrl ?? undefined);
  }

  getAuthToken(): string | null {
    return this.authToken;
  }

  setAuthToken(token: string | null) {
    this.authToken = token?.trim() || null;
  }

  getClientId(): string | null {
    return this.clientId;
  }

  setClientId(id: string | null) {
    this.clientId = id?.trim() || null;
  }

  /**
   * Get the full URL for a relative API path
   * @param relativePath - Relative path starting with /
   * @returns Full URL with base URL and auth token for streaming
   */
  getFullUrl(relativePath: string): string {
    // Remove /api prefix from baseUrl if present, since relativePath already includes /api
    const baseWithoutApi = this.baseUrl.replace(/\/api\/?$/, '');
    const url = `${baseWithoutApi}${relativePath}`;
    // Add auth token if present (for streaming URLs that can't use headers)
    if (this.authToken) {
      const separator = url.includes('?') ? '&' : '?';
      return `${url}${separator}token=${encodeURIComponent(this.authToken)}`;
    }
    return url;
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

    // Don't set Content-Type for FormData - let the browser set it with boundary
    const isFormData = options.body instanceof FormData;
    if (!headerMap['Content-Type'] && !isFormData) {
      headerMap['Content-Type'] = 'application/json';
    }
    if (!headerMap['Accept']) {
      headerMap['Accept'] = 'application/json';
    }

    // Use auth token for session-based authentication
    // Send both Authorization and X-PIN headers for reverse proxy compatibility (e.g., Traefik)
    if (this.authToken) {
      if (!headerMap['Authorization']) {
        headerMap['Authorization'] = `Bearer ${this.authToken}`;
      }
      if (!headerMap['X-PIN']) {
        headerMap['X-PIN'] = this.authToken;
      }
    }

    // Include client ID for per-client settings
    if (this.clientId && !headerMap['X-Client-ID']) {
      headerMap['X-Client-ID'] = this.clientId;
    }

    requestInit.headers = headerMap;

    // Check if operation was aborted before making request
    if (options.signal?.aborted) {
      const error = new Error('Operation was aborted');
      error.name = 'AbortError';
      throw error;
    }

    const requestStartTime = DEBUG_API_TIMING ? performance.now() : 0;
    const response = await fetch(url, requestInit);

    if (DEBUG_API_TIMING && requestStartTime) {
      const duration = performance.now() - requestStartTime;
      apiRequestCount++;
      totalApiTime += duration;
      if (duration > 200) {
        apiRequestStats.push({ endpoint, duration, method: options.method || 'GET' });
      }
      // Log all requests to identify duplicates and unnecessary calls
      console.log(`[API] ${options.method || 'GET'} ${endpoint} ${duration.toFixed(0)}ms`);
    }

    if (!response.ok) {
      const errorText = await response.text();
      const isAuthFailure = response.status === 401;
      const isTimeout = response.status === 504;
      // Use console.warn for handled errors (400/404 client errors, health check failures, auth issues, timeouts) that surface via UI
      const isHandledError =
        response.status === 400 ||
        response.status === 404 ||
        response.status === 502 ||
        response.status === 504 ||
        isAuthFailure;
      const logLevel = isHandledError ? console.warn : console.error;
      logLevel('API request failed:', response.status, response.statusText, errorText);

      // Try to parse structured error response from backend
      let errorCode: string | undefined;
      let errorMessage: string | undefined;
      try {
        const parsed = JSON.parse(errorText);
        if (parsed.code) {
          errorCode = parsed.code;
        }
        if (parsed.message) {
          errorMessage = parsed.message;
        }
      } catch {
        // Not JSON or invalid, use raw error text
      }

      // Use friendly message for user-facing errors, fall back to raw error text
      const displayMessage = errorMessage || errorText;
      const apiError: ApiError = new Error(
        `API request failed: ${response.status} ${response.statusText} - ${displayMessage}`,
      );
      apiError.status = response.status;
      apiError.statusText = response.statusText;
      apiError.body = errorText;
      apiError.url = url;

      if (isAuthFailure) {
        apiError.code = 'AUTH_INVALID_PIN';
      } else if (isTimeout || errorCode === 'GATEWAY_TIMEOUT') {
        apiError.code = 'GATEWAY_TIMEOUT';
      } else if (errorCode) {
        apiError.code = errorCode;
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
  // If limit is provided, returns paginated results with total count
  // If no limit, returns all items for backward compatibility
  // unfilteredTotal is returned when hideUnreleased or hideWatched is true (for explore card logic)
  async getTrendingMovies(
    userId?: string,
    limit?: number,
    offset?: number,
    hideUnreleased?: boolean,
    hideWatched?: boolean,
  ): Promise<TrendingItem[] | { items: TrendingItem[]; total: number; unfilteredTotal?: number }> {
    const params = new URLSearchParams({ type: 'movie' });
    if (userId) {
      params.set('userId', userId);
    }
    if (limit && limit > 0) {
      params.set('limit', limit.toString());
    }
    if (offset && offset > 0) {
      params.set('offset', offset.toString());
    }
    if (hideUnreleased) {
      params.set('hideUnreleased', 'true');
    }
    if (hideWatched) {
      params.set('hideWatched', 'true');
    }
    const url = `/discover/new?${params.toString()}`;
    console.log('[API] getTrendingMovies URL:', url);
    // New API returns { items, total, unfilteredTotal? }, but we need backward compatibility
    const response = await this.request<{ items: TrendingItem[]; total: number; unfilteredTotal?: number }>(
      url,
    );
    console.log('[API] getTrendingMovies response: total=', response.total, 'items.length=', response.items?.length);
    // If limit was specified, return full response for pagination
    if (limit && limit > 0) {
      return response;
    }
    // Otherwise return just items for backward compatibility
    return response.items;
  }

  // Discover trending TV shows
  // If limit is provided, returns paginated results with total count
  // If no limit, returns all items for backward compatibility
  // unfilteredTotal is returned when hideUnreleased or hideWatched is true (for explore card logic)
  async getTrendingTVShows(
    userId?: string,
    limit?: number,
    offset?: number,
    hideUnreleased?: boolean,
    hideWatched?: boolean,
  ): Promise<TrendingItem[] | { items: TrendingItem[]; total: number; unfilteredTotal?: number }> {
    const params = new URLSearchParams({ type: 'series' });
    if (userId) {
      params.set('userId', userId);
    }
    if (limit && limit > 0) {
      params.set('limit', limit.toString());
    }
    if (offset && offset > 0) {
      params.set('offset', offset.toString());
    }
    if (hideUnreleased) {
      params.set('hideUnreleased', 'true');
    }
    if (hideWatched) {
      params.set('hideWatched', 'true');
    }
    // New API returns { items, total, unfilteredTotal? }, but we need backward compatibility
    const response = await this.request<{ items: TrendingItem[]; total: number; unfilteredTotal?: number }>(
      `/discover/new?${params.toString()}`,
    );
    // If limit was specified, return full response for pagination
    if (limit && limit > 0) {
      return response;
    }
    // Otherwise return just items for backward compatibility
    return response.items;
  }

  // Get custom MDBList items
  // If limit is provided, only that many items will be enriched with metadata
  // Returns items and total count for pagination
  // unfilteredTotal is returned when hideUnreleased or hideWatched is true (for explore card logic)
  async getCustomList(
    listUrl: string,
    userId?: string,
    limit?: number,
    offset?: number,
    hideUnreleased?: boolean,
    hideWatched?: boolean,
    name?: string,
  ): Promise<{ items: TrendingItem[]; total: number; unfilteredTotal?: number }> {
    const params = new URLSearchParams({ url: listUrl });
    if (userId) {
      params.set('userId', userId);
    }
    if (limit && limit > 0) {
      params.set('limit', limit.toString());
    }
    if (offset && offset > 0) {
      params.set('offset', offset.toString());
    }
    if (hideUnreleased) {
      params.set('hideUnreleased', 'true');
    }
    if (hideWatched) {
      params.set('hideWatched', 'true');
    }
    if (name) {
      params.set('name', name);
    }
    return this.request<{ items: TrendingItem[]; total: number; unfilteredTotal?: number }>(
      `/lists/custom?${params.toString()}`,
    );
  }

  // Search movies
  async searchMovies(query: string, userId?: string): Promise<SearchResult[]> {
    const encodedQuery = encodeURIComponent(query);
    let url = `/search?q=${encodedQuery}&type=movie`;
    if (userId) url += `&userId=${encodeURIComponent(userId)}`;
    return this.request<SearchResult[]>(url);
  }

  // Search TV shows
  async searchTVShows(query: string, userId?: string): Promise<SearchResult[]> {
    const encodedQuery = encodeURIComponent(query);
    let url = `/search?q=${encodedQuery}&type=series`;
    if (userId) url += `&userId=${encodeURIComponent(userId)}`;
    return this.request<SearchResult[]>(url);
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

  async batchMovieReleases(
    queries: Array<{
      titleId?: string;
      tmdbId?: string | number;
      imdbId?: string;
    }>,
  ): Promise<BatchMovieReleasesResponse> {
    const requestBody: BatchMovieReleasesRequest = {
      queries: queries.map((q) => ({
        titleId: q.titleId,
        tmdbId: q.tmdbId ? Number(q.tmdbId) : undefined,
        imdbId: q.imdbId,
      })),
    };

    return this.request<BatchMovieReleasesResponse>('/metadata/movies/releases', {
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

  async getCollectionDetails(collectionId: number): Promise<CollectionDetails> {
    return this.request<CollectionDetails>(`/metadata/collection?id=${collectionId}`);
  }

  async getSimilarContent(mediaType: string, tmdbId: number): Promise<Title[]> {
    const params = new URLSearchParams();
    params.set('type', mediaType);
    params.set('tmdbId', String(tmdbId));
    return this.request<Title[]>(`/metadata/similar?${params.toString()}`);
  }

  async getPersonDetails(personId: number): Promise<PersonDetails> {
    return this.request<PersonDetails>(`/metadata/person?id=${personId}`);
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
    if (typeof params.season === 'number' && Number.isFinite(params.season) && params.season > 0) {
      searchParams.set('season', String(Math.trunc(params.season)));
    }

    const query = searchParams.toString();
    const endpoint = `/metadata/trailers${query ? `?${query}` : ''}`;
    const response = await this.request<TrailerResponse>(endpoint);
    return {
      primaryTrailer: response.primaryTrailer,
      trailers: Array.isArray(response.trailers) ? response.trailers : [],
    };
  }

  // Get proxy URL for a YouTube trailer (streams through backend to bypass iOS restrictions)
  getTrailerProxyUrl(videoUrl: string): string {
    const searchParams = new URLSearchParams();
    searchParams.set('url', videoUrl);
    // Include auth token in URL for the proxy endpoint
    const token = this.authToken;
    if (token) {
      searchParams.set('token', token);
    }
    return `${this.baseUrl}/metadata/trailers/proxy?${searchParams.toString()}`;
  }

  // Start prequeue download for a YouTube trailer (1080p merged video+audio)
  async prequeueTrailer(videoUrl: string): Promise<TrailerPrequeueResponse> {
    return this.request<TrailerPrequeueResponse>('/metadata/trailers/prequeue', {
      method: 'POST',
      body: JSON.stringify({ url: videoUrl }),
    });
  }

  // Check status of a prequeued trailer download
  async getTrailerPrequeueStatus(id: string): Promise<TrailerPrequeueResponse> {
    return this.request<TrailerPrequeueResponse>(`/metadata/trailers/prequeue/status?id=${encodeURIComponent(id)}`);
  }

  // Get metadata enrichment progress snapshot
  async getMetadataProgress(): Promise<MetadataProgressSnapshot> {
    return this.request<MetadataProgressSnapshot>('/metadata/progress');
  }

  // Get URL to serve a prequeued trailer (for video player)
  getTrailerPrequeueServeUrl(id: string): string {
    const searchParams = new URLSearchParams();
    searchParams.set('id', id);
    const token = this.authToken;
    if (token) {
      searchParams.set('token', token);
    }
    return `${this.baseUrl}/metadata/trailers/prequeue/serve?${searchParams.toString()}`;
  }

  // Get settings
  async getSettings(): Promise<any> {
    return this.request('/settings');
  }

  // Get backend version
  async getBackendVersion(): Promise<{ version: string }> {
    return this.request('/version');
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

  // Get combined startup data (reduces ~21 requests to 1 for low-power devices)
  async getStartupData(
    userId: string,
    options?: { hideUnreleased?: boolean; hideWatched?: boolean },
  ): Promise<StartupData> {
    const safeUserId = this.normaliseUserId(userId);
    const params = new URLSearchParams();
    if (options?.hideUnreleased) {
      params.set('hideUnreleased', 'true');
    }
    if (options?.hideWatched) {
      params.set('hideWatched', 'true');
    }
    const qs = params.toString();
    const url = `/users/${safeUserId}/startup${qs ? `?${qs}` : ''}`;
    return this.request<StartupData>(url);
  }

  // Fetch all details-page data in a single request (bundle endpoint)
  async getDetailsBundleData(
    userId: string,
    params: {
      type: 'series' | 'movie';
      titleId?: string;
      name?: string;
      year?: number;
      tvdbId?: number;
      tmdbId?: number;
      imdbId?: string;
      season?: number;
    },
  ): Promise<DetailsBundleData> {
    const safeUserId = this.normaliseUserId(userId);
    const qs = new URLSearchParams();
    qs.set('type', params.type);
    if (params.titleId) qs.set('titleId', params.titleId);
    if (params.name) qs.set('name', params.name);
    if (params.year) qs.set('year', String(params.year));
    if (params.tvdbId) qs.set('tvdbId', String(params.tvdbId));
    if (params.tmdbId) qs.set('tmdbId', String(params.tmdbId));
    if (params.imdbId) qs.set('imdbId', params.imdbId);
    if (params.season) qs.set('season', String(params.season));
    return this.request<DetailsBundleData>(`/users/${safeUserId}/details-bundle?${qs.toString()}`);
  }

  // Update user-specific settings
  async updateUserSettings(userId: string, settings: UserSettings): Promise<UserSettings> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<UserSettings>(`/users/${safeUserId}/settings`, {
      method: 'PUT',
      body: JSON.stringify(settings),
    });
  }

  // Get per-content language preference
  async getContentPreference(userId: string, contentId: string): Promise<ContentPreference | null> {
    const safeUserId = this.normaliseUserId(userId);
    const encodedContentId = encodeURIComponent(contentId);
    try {
      const result = await this.request<ContentPreference | Record<string, never>>(
        `/users/${safeUserId}/preferences/content/${encodedContentId}`,
      );
      // Backend returns empty object if no preference exists
      if (!result || !('contentId' in result)) {
        return null;
      }
      return result as ContentPreference;
    } catch {
      return null;
    }
  }

  // Set per-content language preference
  async setContentPreference(userId: string, preference: ContentPreference): Promise<ContentPreference> {
    const safeUserId = this.normaliseUserId(userId);
    return this.request<ContentPreference>(`/users/${safeUserId}/preferences/content`, {
      method: 'PUT',
      body: JSON.stringify(preference),
    });
  }

  // Delete per-content language preference
  async deleteContentPreference(userId: string, contentId: string): Promise<void> {
    const safeUserId = this.normaliseUserId(userId);
    const encodedContentId = encodeURIComponent(contentId);
    await this.request<void>(`/users/${safeUserId}/preferences/content/${encodedContentId}`, {
      method: 'DELETE',
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
    if (this.authToken) {
      params.set('token', this.authToken);
    }
    return `${this.baseUrl}/live/stream?${params.toString()}`;
  }

  async startLiveHlsSession(sourceUrl: string): Promise<{
    sessionId: string;
    playlistUrl: string;
    isLive: boolean;
  }> {
    const params = new URLSearchParams({ url: sourceUrl });
    return this.request<{ sessionId: string; playlistUrl: string; isLive: boolean }>(
      `/live/hls/start?${params.toString()}`,
    );
  }

  async clearLivePlaylistCache(): Promise<{ status: string; cleared: number }> {
    return this.request<{ status: string; cleared: number }>('/live/cache/clear', {
      method: 'POST',
    });
  }

  async getLiveChannels(signal?: AbortSignal): Promise<LiveChannelsResponse> {
    return this.request<LiveChannelsResponse>('/live/channels', { signal });
  }

  async getLiveCategories(): Promise<CategoriesResponse> {
    return this.request<CategoriesResponse>('/live/categories');
  }

  // EPG (Electronic Program Guide) methods
  async getEPGStatus(): Promise<EPGStatus> {
    return this.request<EPGStatus>('/live/epg/status');
  }

  async getEPGNowPlaying(channelIds: string[], signal?: AbortSignal): Promise<EPGNowPlayingResponse> {
    if (channelIds.length === 0) {
      return [];
    }
    const params = new URLSearchParams();
    params.set('channels', channelIds.join(','));
    return this.request<EPGNowPlayingResponse>(`/live/epg/now?${params.toString()}`, { signal });
  }

  async refreshEPG(): Promise<{ status: string }> {
    return this.request<{ status: string }>('/live/epg/refresh', {
      method: 'POST',
    });
  }

  /**
   * Fetch EPG schedule for multiple channels within a time window.
   * Used by the EPG grid view for efficient batch fetching.
   * @param channelIds - Array of channel IDs (tvgId values)
   * @param hours - Number of hours to fetch (default 4)
   * @param startOffsetMinutes - Offset in minutes from now (can be negative for past)
   * @returns Map of channelId -> array of programs
   */
  async getEPGScheduleBatch(
    channelIds: string[],
    hours = 4,
    startOffsetMinutes = 0,
    signal?: AbortSignal,
  ): Promise<Record<string, EPGProgram[]>> {
    if (channelIds.length === 0) {
      return {};
    }
    const params = new URLSearchParams();
    params.set('channels', channelIds.join(','));
    params.set('hours', hours.toString());
    if (startOffsetMinutes !== 0) {
      params.set('startOffset', startOffsetMinutes.toString());
    }
    return this.request<Record<string, EPGProgram[]>>(`/live/epg/schedule/batch?${params.toString()}`, { signal });
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
    options?: { onStatus?: (update: PlaybackResolutionResponse) => void; signal?: AbortSignal; startOffset?: number },
  ): Promise<PlaybackResolution> {
    try {
      const initial = await this.request<PlaybackResolutionResponse>('/playback/resolve', {
        method: 'POST',
        body: JSON.stringify({ result, startOffset: options?.startOffset }),
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

  // Start subtitle extraction for a prequeue with the given offset
  // Called when user plays, after they've chosen resume/start position
  async startPrequeueSubtitles(
    prequeueId: string,
    startOffset: number,
  ): Promise<{ subtitleSessions: Record<number, SubtitleSessionInfo> }> {
    if (!prequeueId?.trim()) {
      throw new Error('A valid prequeueId is required.');
    }
    const endpoint = `/playback/prequeue/${encodeURIComponent(prequeueId.trim())}/start-subtitles`;
    return this.request<{ subtitleSessions: Record<number, SubtitleSessionInfo> }>(endpoint, {
      method: 'POST',
      body: JSON.stringify({ startOffset }),
    });
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

    // Copy pre-extracted subtitle sessions for manual selection path
    if (resolution.subtitleSessions && Object.keys(resolution.subtitleSessions).length > 0) {
      ready.subtitleSessions = resolution.subtitleSessions;
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

  // Client device registration
  async registerClient(data: {
    id: string;
    userId?: string;
    deviceType: string;
    os: string;
    appVersion: string;
  }): Promise<{ client: Client }> {
    return this.request<{ client: Client }>('/clients/register', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  async getClients(userId?: string): Promise<Client[]> {
    const params = userId ? `?userId=${encodeURIComponent(userId)}` : '';
    return this.request<Client[]>(`/clients${params}`);
  }

  async getClientSettings(clientId: string): Promise<ClientFilterSettings> {
    return this.request<ClientFilterSettings>(`/clients/${encodeURIComponent(clientId)}/settings`);
  }

  async updateClientSettings(clientId: string, settings: ClientFilterSettings): Promise<ClientFilterSettings> {
    return this.request<ClientFilterSettings>(`/clients/${encodeURIComponent(clientId)}/settings`, {
      method: 'PUT',
      body: JSON.stringify(settings),
    });
  }

  // Check if there's a pending ping for this client (for device identification)
  async checkClientPing(clientId: string): Promise<{ ping: boolean }> {
    return this.request<{ ping: boolean }>(`/clients/${encodeURIComponent(clientId)}/ping`);
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

  async setUserIconUrl(id: string, iconUrl: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(id);
    return this.request<UserProfile>(`/users/${safeId}/icon`, {
      method: 'PUT',
      body: JSON.stringify({ iconUrl }),
    });
  }

  async clearUserIcon(id: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(id);
    return this.request<UserProfile>(`/users/${safeId}/icon`, {
      method: 'DELETE',
    });
  }

  async uploadUserIcon(id: string, file: { uri: string; type: string; name: string }): Promise<UserProfile> {
    const safeId = this.normaliseUserId(id);
    const formData = new FormData();
    formData.append('icon', file as unknown as Blob);
    return this.request<UserProfile>(`/users/${safeId}/icon/upload`, {
      method: 'POST',
      body: formData,
    });
  }

  getProfileIconUrl(id: string): string {
    const safeId = this.normaliseUserId(id);
    return `${this.baseUrl}/users/${safeId}/icon`;
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

  // User Trakt account association
  async setUserTraktAccount(userId: string, traktAccountId: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(userId);
    return this.request<UserProfile>(`/users/${safeId}/trakt`, {
      method: 'PUT',
      body: JSON.stringify({ traktAccountId }),
    });
  }

  async clearUserTraktAccount(userId: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(userId);
    return this.request<UserProfile>(`/users/${safeId}/trakt`, {
      method: 'DELETE',
    });
  }

  // Kids profile content restriction configuration
  async setKidsMode(userId: string, mode: 'rating' | 'content_list' | ''): Promise<UserProfile> {
    const safeId = this.normaliseUserId(userId);
    return this.request<UserProfile>(`/users/${safeId}/kids/mode`, {
      method: 'PUT',
      body: JSON.stringify({ mode }),
    });
  }

  async setKidsMaxRating(userId: string, rating: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(userId);
    return this.request<UserProfile>(`/users/${safeId}/kids/rating`, {
      method: 'PUT',
      body: JSON.stringify({ rating }),
    });
  }

  async setKidsAllowedLists(userId: string, lists: string[]): Promise<UserProfile> {
    const safeId = this.normaliseUserId(userId);
    return this.request<UserProfile>(`/users/${safeId}/kids/lists`, {
      method: 'PUT',
      body: JSON.stringify({ lists }),
    });
  }

  async addKidsAllowedList(userId: string, url: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(userId);
    return this.request<UserProfile>(`/users/${safeId}/kids/lists`, {
      method: 'POST',
      body: JSON.stringify({ url }),
    });
  }

  async removeKidsAllowedList(userId: string, url: string): Promise<UserProfile> {
    const safeId = this.normaliseUserId(userId);
    return this.request<UserProfile>(`/users/${safeId}/kids/lists?url=${encodeURIComponent(url)}`, {
      method: 'DELETE',
    });
  }

  // Trakt accounts management
  async getTraktAccounts(): Promise<TraktAccount[]> {
    const response = await this.request<TraktAccountsResponse>('/trakt/accounts');
    return response.accounts;
  }

  async createTraktAccount(name: string, clientId: string, clientSecret: string): Promise<TraktAccount> {
    const response = await this.request<{ success: boolean; account: TraktAccount }>('/trakt/accounts', {
      method: 'POST',
      body: JSON.stringify({ name, clientId, clientSecret }),
    });
    return response.account;
  }

  async updateTraktAccount(
    accountId: string,
    updates: { name?: string; clientId?: string; clientSecret?: string; scrobblingEnabled?: boolean },
  ): Promise<void> {
    await this.request<{ success: boolean }>(`/trakt/accounts/${encodeURIComponent(accountId)}`, {
      method: 'PATCH',
      body: JSON.stringify(updates),
    });
  }

  async deleteTraktAccount(accountId: string): Promise<void> {
    await this.request<{ success: boolean }>(`/trakt/accounts/${encodeURIComponent(accountId)}`, {
      method: 'DELETE',
    });
  }

  async startTraktAuth(accountId: string): Promise<TraktDeviceCodeResponse> {
    return this.request<TraktDeviceCodeResponse>(`/trakt/accounts/${encodeURIComponent(accountId)}/auth/start`, {
      method: 'POST',
    });
  }

  async checkTraktAuth(accountId: string, deviceCode: string): Promise<TraktAuthCheckResponse> {
    return this.request<TraktAuthCheckResponse>(
      `/trakt/accounts/${encodeURIComponent(accountId)}/auth/check/${encodeURIComponent(deviceCode)}`,
    );
  }

  async disconnectTraktAccount(accountId: string): Promise<void> {
    await this.request<{ success: boolean }>(`/trakt/accounts/${encodeURIComponent(accountId)}/disconnect`, {
      method: 'POST',
    });
  }

  async setTraktScrobbling(accountId: string, enabled: boolean): Promise<void> {
    await this.request<{ success: boolean; scrobblingEnabled: boolean }>(
      `/trakt/accounts/${encodeURIComponent(accountId)}/scrobbling`,
      {
        method: 'POST',
        body: JSON.stringify({ enabled }),
      },
    );
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

  async getVideoMetadata(
    path: string,
    options?: { profileId?: string; clientId?: string; audioLang?: string },
  ): Promise<VideoMetadata> {
    // Build URL manually to ensure proper encoding of special chars like semicolons
    // URLSearchParams doesn't encode semicolons which breaks some parsers
    const queryParts: string[] = [`path=${encodeURIComponent(path)}`];
    if (this.authToken) {
      queryParts.push(`token=${encodeURIComponent(this.authToken)}`);
    }
    if (options?.profileId) {
      queryParts.push(`profileId=${encodeURIComponent(options.profileId)}`);
    }
    if (options?.clientId) {
      queryParts.push(`clientId=${encodeURIComponent(options.clientId)}`);
    }
    if (options?.audioLang) {
      queryParts.push(`audioLang=${encodeURIComponent(options.audioLang)}`);
    }
    return this.request<VideoMetadata>(`/video/metadata?${queryParts.join('&')}`);
  }

  /**
   * Get the direct download URL for a given path.
   * For debrid paths, this unrestricts the link and returns the CDN URL.
   * Useful for external players like Infuse that don't need our proxy.
   */
  async getDirectUrl(path: string): Promise<{ url: string }> {
    // Build URL manually to ensure proper encoding of special chars like semicolons
    // URLSearchParams doesn't encode semicolons which breaks some parsers
    const queryParts: string[] = [`path=${encodeURIComponent(path)}`];
    if (this.authToken) {
      queryParts.push(`token=${encodeURIComponent(this.authToken)}`);
    }
    return this.request<{ url: string }>(`/video/direct-url?${queryParts.join('&')}`);
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
    profileId?: string;
    profileName?: string;
    trackSwitch?: boolean;
  }): Promise<HlsSessionStartResponse> {
    const trimmedPath = params.path?.trim();
    if (!trimmedPath) {
      throw new Error('Path is required to create an HLS session.');
    }

    // Build URL manually to ensure proper encoding of special chars like semicolons
    // URLSearchParams doesn't encode semicolons which breaks some parsers
    const queryParts: string[] = [];
    queryParts.push(`path=${encodeURIComponent(trimmedPath)}`);

    if (this.authToken) {
      queryParts.push(`token=${encodeURIComponent(this.authToken)}`);
    }

    if (params.dv) {
      queryParts.push('dv=true');
    }
    if (params.dvProfile) {
      queryParts.push(`dvProfile=${encodeURIComponent(params.dvProfile)}`);
    }
    if (params.hdr) {
      queryParts.push('hdr=true');
    }
    if (params.forceAAC) {
      queryParts.push('forceAAC=true');
    }
    if (typeof params.start === 'number' && Number.isFinite(params.start) && params.start >= 0) {
      queryParts.push(`start=${params.start.toFixed(3)}`);
    }
    if (typeof params.audioTrack === 'number' && Number.isFinite(params.audioTrack) && params.audioTrack >= 0) {
      queryParts.push(`audioTrack=${params.audioTrack.toString()}`);
    }
    if (
      typeof params.subtitleTrack === 'number' &&
      Number.isFinite(params.subtitleTrack) &&
      params.subtitleTrack >= 0
    ) {
      queryParts.push(`subtitleTrack=${params.subtitleTrack.toString()}`);
    }

    // Add profile info for stream tracking
    if (params.profileId) {
      queryParts.push(`profileId=${encodeURIComponent(params.profileId)}`);
    }
    if (params.profileName) {
      queryParts.push(`profileName=${encodeURIComponent(params.profileName)}`);
    }

    // Track switch flag skips waiting for first segment (faster audio/subtitle changes)
    if (params.trackSwitch) {
      queryParts.push('trackSwitch=true');
    }

    return this.request<HlsSessionStartResponse>(`/video/hls/start?${queryParts.join('&')}`);
  }

  /**
   * Send a keepalive ping for an HLS session to prevent idle timeout while paused
   * @param sessionId - The HLS session ID
   * @param currentTime - Optional current playback time in seconds for rate limiting
   * @param bufferStart - Optional earliest time still in player's buffer (for safe segment cleanup)
   * @returns Segment timing info for accurate subtitle sync
   */
  async keepaliveHlsSession(
    sessionId: string,
    currentTime?: number,
    bufferStart?: number,
  ): Promise<HlsKeepaliveResponse> {
    if (!sessionId) {
      throw new Error('Session ID is required for keepalive');
    }
    const params = new URLSearchParams();
    if (currentTime !== undefined && currentTime >= 0) {
      params.set('time', String(currentTime));
    }
    if (bufferStart !== undefined && bufferStart >= 0) {
      params.set('bufferStart', String(bufferStart));
    }
    const queryString = params.toString();
    return this.request<HlsKeepaliveResponse>(
      `/video/hls/${encodeURIComponent(sessionId)}/keepalive${queryString ? `?${queryString}` : ''}`,
      {
        method: 'POST',
      },
    );
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

  /**
   * Seek within an existing HLS session by restarting transcoding from a new offset.
   * This is faster than creating a new session since it reuses the existing session structure.
   * @param sessionId - The ID of the existing HLS session
   * @param targetTime - The target seek position in absolute media time (seconds)
   * @returns Updated session info with new start offset and playlist URL
   */
  async seekHlsSession(sessionId: string, targetTime: number): Promise<HlsSeekResponse> {
    if (!sessionId) {
      throw new Error('Session ID is required for seek');
    }
    if (targetTime < 0) {
      throw new Error('Target time must be non-negative');
    }
    return this.request<HlsSeekResponse>(`/video/hls/${encodeURIComponent(sessionId)}/seek?time=${targetTime}`, {
      method: 'POST',
    });
  }

  /**
   * Probe subtitle tracks for a video file
   * @param path - The source path of the video file
   * @returns Available subtitle tracks with metadata
   */
  async probeSubtitleTracks(path: string): Promise<{
    tracks: Array<{
      index: number;
      language: string;
      title: string;
      codec: string;
      forced: boolean;
    }>;
  }> {
    return this.request(`/video/subtitles/tracks?path=${encodeURIComponent(path)}`);
  }

  /**
   * Start a subtitle extraction session for non-HLS streams
   * @param path - The source path of the video file
   * @param subtitleTrack - The subtitle track index to extract
   * @param startOffset - Optional resume position in seconds for seeking
   * @returns Session info with the VTT URL and firstCueTime for sync
   */
  async startSubtitleExtract(
    path: string,
    subtitleTrack: number,
    startOffset?: number,
  ): Promise<{ sessionId: string; subtitleUrl: string; firstCueTime?: number }> {
    const params = new URLSearchParams();
    params.set('subtitleTrack', subtitleTrack.toString());
    if (startOffset !== undefined && startOffset > 0) {
      params.set('startOffset', startOffset.toString());
    }
    return this.request<{ sessionId: string; subtitleUrl: string }>(`/video/subtitles/start?path=${encodeURIComponent(path)}&${params.toString()}`);
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
    if (this.authToken) query.set('token', this.authToken);

    return `${this.baseUrl}/subtitles/download?${query.toString()}`;
  }

  /**
   * Submit frontend and backend logs to paste service for debugging
   * @param frontendLogs - Frontend console logs captured by the logger service
   * @returns Promise with the paste URL
   */
  async submitLogs(frontendLogs: string): Promise<{ url?: string; error?: string }> {
    return this.request<{ url?: string; error?: string }>('/logs/submit', {
      method: 'POST',
      body: JSON.stringify({ frontendLogs }),
    });
  }
}

export { ApiService };

// Default instance
export const apiService = new ApiService();
export default apiService;
