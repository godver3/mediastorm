import AsyncStorage from '@react-native-async-storage/async-storage';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { AppState, AppStateStatus, Platform } from 'react-native';

import { apiService, UserSettings, ClientFilterSettings } from '@/services/api';
import { getClientId } from '@/services/clientId';
import {
  cacheNetworkSettings,
  getNetworkBasedUrl,
  getCachedNetworkSettings,
  getCurrentSSID,
  type NetworkUrlResult,
} from '@/services/networkUrl';

const STORAGE_KEY = 'strmr.backendUrl';
const DEFAULT_PORT = 7777;
const DEFAULT_API_PATH = '/api';

export interface BackendServerSettings {
  host: string;
  port: number;
}

export interface BackendUsenetSettings {
  name: string;
  host: string;
  port: number;
  ssl: boolean;
  username: string;
  password: string;
  connections: number;
  enabled: boolean;
}

export interface BackendIndexerConfig {
  name: string;
  url: string;
  apiKey: string;
  type: string;
  enabled: boolean;
}

export interface BackendTorrentScraperConfig {
  name: string;
  type: string;
  url: string;
  apiKey: string;
  enabled: boolean;
  config?: Record<string, string>;
}

export interface BackendMetadataSettings {
  tvdbApiKey: string;
  tmdbApiKey: string;
  language: string;
}

export interface BackendCacheSettings {
  directory: string;
  metadataTtlHours: number;
}

export interface BackendWebDAVSettings {
  enabled: boolean;
  prefix: string;
  username: string;
  password: string;
}

export type StreamingServiceMode = 'usenet' | 'debrid' | 'hybrid';
export type StreamingServicePriority = 'none' | 'usenet' | 'debrid';
export type MultiProviderMode = 'fastest' | 'preferred';

export interface BackendDebridProvider {
  name: string;
  provider: string;
  apiKey: string;
  enabled: boolean;
}

export interface BackendStreamingSettings {
  maxDownloadWorkers: number;
  maxCacheSizeMB: number;
  serviceMode: StreamingServiceMode;
  servicePriority: StreamingServicePriority;
  multiProviderMode?: MultiProviderMode;
  debridProviders: BackendDebridProvider[];
}

export interface BackendTransmuxSettings {
  enabled: boolean;
  ffmpegPath: string;
  ffprobePath: string;
}

export type PlaybackPreference = 'native' | 'outplayer' | 'infuse';

export interface BackendPlaybackSettings {
  preferredPlayer: PlaybackPreference;
  preferredAudioLanguage?: string;
  preferredSubtitleLanguage?: string;
  preferredSubtitleMode?: 'off' | 'on' | 'forced-only';
  useLoadingScreen?: boolean;
  subtitleSize?: number; // Scaling factor for subtitle size (1.0 = default)
  seekForwardSeconds?: number; // Seconds to skip forward (default 30)
  seekBackwardSeconds?: number; // Seconds to skip backward (default 10)
  forceAacTranscoding?: boolean; // Force AC3/EAC3/DTS audio to AAC for Bluetooth compatibility
  autoPlayTrailersTV?: boolean; // Auto-play trailers in backdrop on TV details pages
}

export interface BackendLiveTVFilterSettings {
  enabledCategories?: string[]; // Only show channels in these categories (empty = show all)
  maxChannels?: number; // Overall channel limit (0 = no limit)
}

export interface BackendLiveSettings {
  mode?: 'm3u' | 'xtream';
  playlistUrl: string;
  xtreamHost?: string;
  xtreamUsername?: string;
  xtreamPassword?: string;
  playlistCacheTtlHours: number;
  effectivePlaylistUrl?: string; // Computed URL (constructed from Xtream credentials if in xtream mode)
  filtering?: BackendLiveTVFilterSettings; // Backend-side channel filtering
}

export interface BackendShelfConfig {
  id: string;
  name: string;
  enabled: boolean;
  order: number;
  type?: 'builtin' | 'mdblist'; // Type of shelf - builtin or custom MDBList
  listUrl?: string; // MDBList URL for custom lists
  limit?: number; // Optional limit on number of items returned (0 = unlimited)
  hideUnreleased?: boolean; // Filter out unreleased/in-theaters content
}

export type ExploreCardPosition = 'front' | 'end';

export interface BackendHomeShelvesSettings {
  shelves: BackendShelfConfig[];
  exploreCardPosition?: ExploreCardPosition;
}

export interface BackendFilterSettings {
  maxSizeMovieGb: number;
  maxSizeEpisodeGb: number;
  excludeHdr: boolean;
  prioritizeHdr: boolean;
  filterOutTerms?: string[];
}

export interface BackendSubtitleSettings {
  openSubtitlesUsername?: string;
  openSubtitlesPassword?: string;
}

export interface BackendDisplaySettings {
  badgeVisibility: string[]; // "watchProgress", "releaseStatus", "watchState", "unwatchedCount"
  watchStateIconStyle?: 'colored' | 'white'; // "colored" (default) = green/yellow, "white" = all white
  hideWatched?: boolean; // Filter out fully watched content from trending shelves and custom lists
  alwaysShowProfileSelector?: boolean; // Show profile selector on app open / un-background
}

export interface BackendNetworkSettings {
  homeWifiSSID: string; // WiFi SSID to detect for home network
  homeBackendUrl: string; // Backend URL when on home WiFi
  remoteBackendUrl: string; // Backend URL when on mobile/other networks
}

export interface BackendRankingCriterion {
  id: string;
  name: string;
  enabled: boolean;
  order: number;
}

export interface BackendRankingSettings {
  criteria: BackendRankingCriterion[];
}

export interface BackendSettings {
  server: BackendServerSettings;
  usenet: BackendUsenetSettings[];
  indexers: BackendIndexerConfig[];
  torrentScrapers: BackendTorrentScraperConfig[];
  metadata: BackendMetadataSettings;
  cache: BackendCacheSettings;
  webdav?: BackendWebDAVSettings | null;
  streaming: BackendStreamingSettings;
  transmux: BackendTransmuxSettings;
  playback: BackendPlaybackSettings;
  live: BackendLiveSettings;
  homeShelves: BackendHomeShelvesSettings;
  filtering: BackendFilterSettings;
  display?: BackendDisplaySettings;
  network?: BackendNetworkSettings;
  subtitles?: BackendSubtitleSettings;
  ranking?: BackendRankingSettings;
  demoMode?: boolean;
}

interface BackendSettingsContextValue {
  backendUrl: string;
  isReady: boolean;
  loading: boolean;
  saving: boolean;
  error: string | null;
  settings: BackendSettings | null;
  lastLoadedAt: number | null;
  isBackendReachable: boolean;
  retryCountdown: number | null;
  refreshSettings: () => Promise<void>;
  setBackendUrl: (url: string) => Promise<void>;
  updateBackendSettings: (settings: BackendSettings) => Promise<BackendSettings>;
  // Per-user settings
  userSettings: UserSettings | null;
  userSettingsLoading: boolean;
  loadUserSettings: (userId: string) => Promise<UserSettings>;
  updateUserSettings: (userId: string, settings: UserSettings) => Promise<UserSettings>;
  clearUserSettings: () => void;
  // Network-based URL switching
  networkUrlInfo: NetworkUrlResult | null;
  checkNetworkAndUpdateUrl: () => Promise<void>;
}

const BackendSettingsContext = createContext<BackendSettingsContextValue | undefined>(undefined);

const formatErrorMessage = (err: unknown) => {
  if (err instanceof Error) {
    return err.message;
  }
  if (typeof err === 'string') {
    return err;
  }
  return 'Unknown backend settings error';
};

const normaliseBackendUrl = (input: string) => {
  const trimmed = input?.trim() ?? '';
  if (!trimmed) {
    return '';
  }

  if (/^https?:\/\//i.test(trimmed)) {
    return trimmed.replace(/\/$/, '');
  }

  const [hostPart, ...pathParts] = trimmed.split('/');
  const hasExplicitProtocol = hostPart.includes('://');
  if (hasExplicitProtocol) {
    return hostPart.replace(/\/$/, '');
  }

  const hasPort = hostPart.includes(':');
  const hostWithPort = hasPort ? hostPart : `${hostPart}:${DEFAULT_PORT}`;
  const path = pathParts.length > 0 ? `/${pathParts.join('/')}` : DEFAULT_API_PATH;

  return `http://${hostWithPort}${path}`.replace(/\/$/, '');
};

const RETRY_INTERVAL_SECONDS = 10;

const isNetworkError = (err: unknown): boolean => {
  if (err instanceof TypeError && err.message === 'Network request failed') {
    return true;
  }
  // Also check for string message patterns
  if (err instanceof Error && /network|connection|timeout|ECONNREFUSED/i.test(err.message)) {
    return true;
  }
  return false;
};

export const BackendSettingsProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const mountedRef = useRef(true);
  const [backendUrl, setBackendUrlState] = useState<string>(() => apiService.getBaseUrl());
  const [isReady, setIsReady] = useState(false);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [settings, setSettings] = useState<BackendSettings | null>(null);
  const [lastLoadedAt, setLastLoadedAt] = useState<number | null>(null);
  const [isBackendReachable, setIsBackendReachable] = useState(false);
  const [retryCountdown, setRetryCountdown] = useState<number | null>(null);
  const [userSettings, setUserSettings] = useState<UserSettings | null>(null);
  const [userSettingsLoading, setUserSettingsLoading] = useState(false);
  const [clientSettings, setClientSettings] = useState<ClientFilterSettings | null>(null);
  const [networkUrlInfo, setNetworkUrlInfo] = useState<NetworkUrlResult | null>(null);
  const retryTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const countdownTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const retryFnRef = useRef<(() => Promise<boolean>) | null>(null);
  const lastAppliedNetworkUrlRef = useRef<string | null>(null);
  const clientIdRef = useRef<string | null>(null);
  const inflightRefreshRef = useRef<Promise<{ success: boolean; authRequired: boolean }> | null>(null);
  const lastRefreshSuccessRef = useRef<number>(0);
  const REFRESH_COOLDOWN_MS = 2000;

  useEffect(() => {
    return () => {
      mountedRef.current = false;
      if (retryTimerRef.current) {
        clearInterval(retryTimerRef.current);
      }
      if (countdownTimerRef.current) {
        clearInterval(countdownTimerRef.current);
      }
    };
  }, []);

  const stopRetryTimer = useCallback(() => {
    if (retryTimerRef.current) {
      console.log('[BackendSettings] Stopping retry timer');
      clearInterval(retryTimerRef.current);
      retryTimerRef.current = null;
    }
    if (countdownTimerRef.current) {
      clearInterval(countdownTimerRef.current);
      countdownTimerRef.current = null;
    }
    if (mountedRef.current) {
      setRetryCountdown(null);
    }
    retryFnRef.current = null;
  }, []);

  const startRetryTimer = useCallback(
    (retryFn: () => Promise<boolean>) => {
      // Clear any existing timers
      stopRetryTimer();

      // Store the function in a ref so we always call the latest version
      retryFnRef.current = retryFn;

      // Start countdown
      if (mountedRef.current) {
        setRetryCountdown(RETRY_INTERVAL_SECONDS);
      }

      // Update countdown every second
      countdownTimerRef.current = setInterval(() => {
        if (mountedRef.current) {
          setRetryCountdown((prev) => {
            if (prev === null || prev <= 1) {
              return RETRY_INTERVAL_SECONDS;
            }
            return prev - 1;
          });
        }
      }, 1000);

      // Retry at interval - call via ref to always get latest function
      retryTimerRef.current = setInterval(() => {
        if (retryFnRef.current) {
          console.log('[BackendSettings] Retrying connection...');
          retryFnRef
            .current()
            .then((success) => {
              console.log('[BackendSettings] Retry result:', success ? 'SUCCESS' : 'FAILED');
            })
            .catch((err) => {
              console.log('[BackendSettings] Retry error:', err);
            });
        } else {
          console.warn('[BackendSettings] Retry function ref is null!');
        }
      }, RETRY_INTERVAL_SECONDS * 1000);

      console.log('[BackendSettings] Retry timer started, will retry every', RETRY_INTERVAL_SECONDS, 'seconds');
    },
    [stopRetryTimer],
  );

  const applyApiBaseUrl = useCallback((candidate?: string | null) => {
    apiService.setBaseUrl(candidate ?? undefined);
    if (mountedRef.current) {
      setBackendUrlState(apiService.getBaseUrl());
    }
  }, []);

  const persistBackendUrl = useCallback(async (value: string) => {
    const trimmed = value.trim();
    if (!trimmed) {
      await AsyncStorage.removeItem(STORAGE_KEY);
      return;
    }
    await AsyncStorage.setItem(STORAGE_KEY, trimmed);
  }, []);

  // Check current network and update URL if network settings are configured
  // Priority: client settings > user settings > global settings
  const checkNetworkAndUpdateUrl = useCallback(async (): Promise<void> => {
    console.log('[BackendSettings] checkNetworkAndUpdateUrl: Starting...');
    if (!mountedRef.current) {
      console.log('[BackendSettings] checkNetworkAndUpdateUrl: Component not mounted, returning');
      return;
    }

    // DEBUG: Always log current WiFi SSID regardless of settings
    try {
      const debugSSID = await getCurrentSSID();
      console.log('[BackendSettings] DEBUG - Current WiFi SSID:', debugSSID ?? '(not detected)');
    } catch (err) {
      console.log('[BackendSettings] DEBUG - Failed to get WiFi SSID:', err);
    }

    try {
      // Priority: client > user > global
      let networkConfig = null;
      let source: 'client' | 'user' | 'global' = 'global';

      console.log('[BackendSettings] checkNetworkAndUpdateUrl: Checking available settings...');
      console.log('[BackendSettings] checkNetworkAndUpdateUrl:   clientSettings:', JSON.stringify(clientSettings));
      console.log(
        '[BackendSettings] checkNetworkAndUpdateUrl:   userSettings?.network:',
        JSON.stringify(userSettings?.network),
      );
      console.log(
        '[BackendSettings] checkNetworkAndUpdateUrl:   settings?.network:',
        JSON.stringify(settings?.network),
      );

      if (clientSettings?.homeWifiSSID) {
        networkConfig = clientSettings;
        source = 'client';
        console.log('[BackendSettings] checkNetworkAndUpdateUrl: Using CLIENT settings');
      } else if (userSettings?.network?.homeWifiSSID) {
        networkConfig = userSettings.network;
        source = 'user';
        console.log('[BackendSettings] checkNetworkAndUpdateUrl: Using USER settings');
      } else if (settings?.network?.homeWifiSSID) {
        networkConfig = settings.network;
        source = 'global';
        console.log('[BackendSettings] checkNetworkAndUpdateUrl: Using GLOBAL settings');
      } else {
        console.log('[BackendSettings] checkNetworkAndUpdateUrl: No network settings found at any level');
      }

      console.log('[BackendSettings] checkNetworkAndUpdateUrl: Final networkConfig:', JSON.stringify(networkConfig));

      // Get network-based URL (uses cached settings if available)
      const result = await getNetworkBasedUrl(networkConfig);
      console.log('[BackendSettings] checkNetworkAndUpdateUrl: getNetworkBasedUrl result:', JSON.stringify(result));

      if (mountedRef.current) {
        setNetworkUrlInfo(result);
      }

      // If we got a URL and it's different from current, apply it
      console.log('[BackendSettings] checkNetworkAndUpdateUrl: result.url:', result.url);
      console.log(
        '[BackendSettings] checkNetworkAndUpdateUrl: lastAppliedNetworkUrlRef:',
        lastAppliedNetworkUrlRef.current,
      );
      if (result.url && result.url !== lastAppliedNetworkUrlRef.current) {
        console.log(
          `[BackendSettings] Network changed - source: ${source}, isHome: ${result.isHomeNetwork}, SSID: ${result.currentSSID}, switching to: ${result.url}`,
        );
        lastAppliedNetworkUrlRef.current = result.url;
        applyApiBaseUrl(result.url);
        // Don't persist - the network URL is dynamic based on current network
        // The manually set URL is still stored as fallback
      } else if (!result.url) {
        console.log('[BackendSettings] checkNetworkAndUpdateUrl: No URL returned, not changing');
      } else {
        console.log('[BackendSettings] checkNetworkAndUpdateUrl: URL unchanged, not applying');
      }
    } catch (err) {
      console.warn('[BackendSettings] Failed to check network URL:', err);
    }
  }, [clientSettings, userSettings?.network, settings?.network, applyApiBaseUrl]);

  // Returns: { success: boolean, authRequired: boolean }
  const refreshSettingsInternal = useCallback(async (): Promise<{ success: boolean; authRequired: boolean }> => {
    if (!mountedRef.current) {
      return { success: false, authRequired: false };
    }

    // Deduplicate concurrent calls (e.g. BackendSettings init + AuthContext post-validation)
    if (inflightRefreshRef.current) {
      return inflightRefreshRef.current;
    }

    // Skip if we recently refreshed successfully (prevents rapid sequential calls
    // e.g. AuthContext login + BackendSettings init completing back-to-back)
    if (lastRefreshSuccessRef.current > 0 && Date.now() - lastRefreshSuccessRef.current < REFRESH_COOLDOWN_MS) {
      return { success: true, authRequired: false };
    }

    const doRefresh = async (): Promise<{ success: boolean; authRequired: boolean }> => {
      setLoading(true);
      try {
        const result = (await apiService.getSettings()) as BackendSettings;
        if (!mountedRef.current) {
          return { success: false, authRequired: false };
        }
        console.log(
          '[BackendSettings] Successfully connected to backend, has live playlist:',
          !!result?.live?.playlistUrl,
        );
        setSettings(result);
        setError(null);
        setLastLoadedAt(Date.now());
        setIsBackendReachable(true);
        lastRefreshSuccessRef.current = Date.now();
        stopRetryTimer();

        // Cache network settings for offline use
        if (result?.network) {
          await cacheNetworkSettings(result.network);
          console.log('[BackendSettings] Cached network settings:', result.network.homeWifiSSID || '(none)');
        }

        return { success: true, authRequired: false };
      } catch (err) {
        const message = formatErrorMessage(err);
        const networkFailure = isNetworkError(err);
        // Check if this is an auth error (401) - if so, don't treat as a connection error
        // The user just needs to log in first, settings will be fetched after authentication
        const isAuthError = message.includes('401') || message.toLowerCase().includes('unauthorized');
        console.warn('[BackendSettings] Failed to connect:', message, isAuthError ? '(auth required)' : '');
        if (mountedRef.current) {
          setSettings(null);
          // Don't set error for auth failures - this is expected before login
          if (!isAuthError) {
            setError(message);
          } else {
            // Clear any previous error when we detect auth is required
            setError(null);
          }
          if (networkFailure) {
            setIsBackendReachable(false);
          } else if (isAuthError) {
            // Server is reachable, just needs auth
            setIsBackendReachable(true);
          }
        }
        return { success: false, authRequired: isAuthError };
      } finally {
        if (mountedRef.current) {
          setLoading(false);
        }
        inflightRefreshRef.current = null;
      }
    };

    const promise = doRefresh();
    inflightRefreshRef.current = promise;
    return promise;
  }, [stopRetryTimer]);

  // Keep the retry function ref in sync with the latest version
  useEffect(() => {
    if (retryTimerRef.current) {
      retryFnRef.current = async () => {
        const result = await refreshSettingsInternal();
        return result.success;
      };
    }
  }, [refreshSettingsInternal]);

  const refreshSettings = useCallback(async () => {
    const result = await refreshSettingsInternal();
    // Don't start retry timer or throw for auth errors - user just needs to log in
    if (!result.success && !result.authRequired && !retryTimerRef.current && mountedRef.current) {
      // Start retry timer only for non-auth failures
      startRetryTimer(async () => {
        const r = await refreshSettingsInternal();
        return r.success;
      });
    }
    if (!result.success && !result.authRequired) {
      throw new Error('Failed to refresh settings');
    }
  }, [refreshSettingsInternal, startRetryTimer]);

  // Load client settings (device-specific settings including network)
  const loadClientSettings = useCallback(async (): Promise<void> => {
    console.log('[BackendSettings] loadClientSettings: Starting...');
    try {
      const clientId = await getClientId();
      clientIdRef.current = clientId;
      console.log('[BackendSettings] loadClientSettings: clientId:', clientId);
      const settings = await apiService.getClientSettings(clientId);
      console.log('[BackendSettings] loadClientSettings: Loaded settings:', JSON.stringify(settings));
      if (mountedRef.current) {
        setClientSettings(settings);
        console.log('[BackendSettings] loadClientSettings: Settings applied to state');
      }
      // Cache client's network settings with highest priority
      if (settings?.homeWifiSSID) {
        await cacheNetworkSettings(settings, 'client');
        console.log('[BackendSettings] Cached client network settings:', settings.homeWifiSSID);
      } else {
        console.log('[BackendSettings] loadClientSettings: No homeWifiSSID in client settings');
      }
    } catch (err) {
      // Client settings might not exist yet, that's OK
      console.log('[BackendSettings] Could not load client settings:', err);
    }
  }, []);

  useEffect(() => {
    let cancelled = false;

    const initialise = async () => {
      // DEBUG: Log current WiFi SSID on startup
      if (Platform.OS !== 'web') {
        try {
          const debugSSID = await getCurrentSSID();
          console.log('[BackendSettings] STARTUP DEBUG - Current WiFi SSID:', debugSSID ?? '(not detected)');
          console.log('[BackendSettings] STARTUP DEBUG - Platform:', Platform.OS, 'isTV:', Platform.isTV);
        } catch (err) {
          console.log('[BackendSettings] STARTUP DEBUG - Failed to get WiFi SSID:', err);
        }
      }

      // First, try to use cached network settings for initial URL selection
      // This allows the app to connect correctly even before fetching fresh settings
      try {
        const cachedNetwork = await getCachedNetworkSettings();
        if (!cancelled && cachedNetwork?.homeWifiSSID) {
          const networkResult = await getNetworkBasedUrl(cachedNetwork);
          if (!cancelled && networkResult.url) {
            console.log('[BackendSettings] Using cached network URL on startup:', networkResult.url);
            applyApiBaseUrl(networkResult.url);
            lastAppliedNetworkUrlRef.current = networkResult.url;
            setNetworkUrlInfo(networkResult);
          }
        }
      } catch (err) {
        console.warn('[BackendSettings] Failed to apply cached network URL:', err);
      }

      // Fall back to stored URL if no network URL was applied
      if (!lastAppliedNetworkUrlRef.current) {
        try {
          const stored = await AsyncStorage.getItem(STORAGE_KEY);
          if (cancelled) {
            return;
          }
          applyApiBaseUrl(stored ?? undefined);
        } catch (err) {
          console.warn('Failed to read stored backend URL. Falling back to defaults.', err);
          if (!cancelled) {
            applyApiBaseUrl(undefined);
          }
        }
      }

      if (!cancelled && mountedRef.current) {
        console.log('[BackendSettings] Setting isReady = true');
        setIsReady(true);
      }

      try {
        console.log('[BackendSettings] Calling refreshSettings...');
        await refreshSettings();
        console.log('[BackendSettings] refreshSettings completed');

        // Load client-specific settings (including network settings)
        if (!cancelled) {
          await loadClientSettings();
        }
      } catch (err) {
        if (!cancelled) {
          console.warn('[BackendSettings] Failed to load backend settings:', err);
        }
      }
    };

    void initialise();

    return () => {
      cancelled = true;
    };
  }, [applyApiBaseUrl, refreshSettings, loadClientSettings]);

  // Check network when app becomes active (user returns to app)
  useEffect(() => {
    if (Platform.OS === 'web') return; // Web doesn't support AppState or network detection

    const handleAppStateChange = (nextAppState: AppStateStatus) => {
      if (nextAppState === 'active') {
        // Re-check network when app becomes active
        void checkNetworkAndUpdateUrl();
      }
    };

    const subscription = AppState.addEventListener('change', handleAppStateChange);
    return () => {
      subscription.remove();
    };
  }, [checkNetworkAndUpdateUrl]);

  // Check network whenever settings are updated with new network config
  // Watches client, user, and global settings (priority: client > user > global)
  useEffect(() => {
    const hasNetworkSettings =
      clientSettings?.homeWifiSSID || userSettings?.network?.homeWifiSSID || settings?.network?.homeWifiSSID;
    console.log('[BackendSettings] Network settings effect triggered:');
    console.log('[BackendSettings]   hasNetworkSettings:', !!hasNetworkSettings);
    console.log('[BackendSettings]   clientSettings?.homeWifiSSID:', clientSettings?.homeWifiSSID);
    console.log('[BackendSettings]   userSettings?.network?.homeWifiSSID:', userSettings?.network?.homeWifiSSID);
    console.log('[BackendSettings]   settings?.network?.homeWifiSSID:', settings?.network?.homeWifiSSID);
    if (hasNetworkSettings) {
      console.log('[BackendSettings] Calling checkNetworkAndUpdateUrl from settings effect...');
      void checkNetworkAndUpdateUrl();
    }
  }, [clientSettings, userSettings?.network, settings?.network, checkNetworkAndUpdateUrl]);

  const setBackendUrlHandler = useCallback(
    async (url: string) => {
      const normalised = normaliseBackendUrl(url);
      await persistBackendUrl(normalised);
      applyApiBaseUrl(normalised || undefined);

      // Try to refresh settings but don't fail if it requires auth
      // Settings will be fetched properly after login
      try {
        await refreshSettings();
      } catch (err) {
        console.log('[BackendSettings] Settings fetch skipped (may require auth)');
        // Don't throw - we just want to verify the URL is saved
      }
    },
    [applyApiBaseUrl, persistBackendUrl, refreshSettings],
  );

  const updateBackendSettings = useCallback(async (next: BackendSettings) => {
    setSaving(true);
    try {
      const updated = (await apiService.updateSettings(next)) as BackendSettings;
      if (mountedRef.current) {
        setSettings(updated);
        setError(null);
        setLastLoadedAt(Date.now());
      }
      return updated;
    } catch (err) {
      const message = formatErrorMessage(err);
      if (mountedRef.current) {
        setError(message);
      }
      throw err;
    } finally {
      if (mountedRef.current) {
        setSaving(false);
      }
    }
  }, []);

  const loadUserSettings = useCallback(async (userId: string): Promise<UserSettings> => {
    if (!userId?.trim()) {
      throw new Error('User ID is required to load user settings');
    }
    setUserSettingsLoading(true);
    try {
      const result = await apiService.getUserSettings(userId);
      if (mountedRef.current) {
        setUserSettings(result);
      }
      // Cache user's network settings for offline use (if configured)
      if (result?.network?.homeWifiSSID) {
        await cacheNetworkSettings(result.network);
        console.log('[BackendSettings] Cached user network settings:', result.network.homeWifiSSID);
      }
      return result;
    } finally {
      if (mountedRef.current) {
        setUserSettingsLoading(false);
      }
    }
  }, []);

  const updateUserSettingsHandler = useCallback(async (userId: string, next: UserSettings): Promise<UserSettings> => {
    if (!userId?.trim()) {
      throw new Error('User ID is required to update user settings');
    }
    setSaving(true);
    try {
      const updated = await apiService.updateUserSettings(userId, next);
      if (mountedRef.current) {
        setUserSettings(updated);
      }
      // Cache updated network settings for offline use
      if (updated?.network?.homeWifiSSID) {
        await cacheNetworkSettings(updated.network);
        console.log('[BackendSettings] Cached updated user network settings:', updated.network.homeWifiSSID);
      }
      return updated;
    } finally {
      if (mountedRef.current) {
        setSaving(false);
      }
    }
  }, []);

  const clearUserSettings = useCallback(() => {
    setUserSettings(null);
  }, []);

  const value = useMemo<BackendSettingsContextValue>(
    () => ({
      backendUrl,
      isReady,
      loading,
      saving,
      error,
      settings,
      lastLoadedAt,
      isBackendReachable,
      retryCountdown,
      refreshSettings,
      setBackendUrl: setBackendUrlHandler,
      updateBackendSettings,
      userSettings,
      userSettingsLoading,
      loadUserSettings,
      updateUserSettings: updateUserSettingsHandler,
      clearUserSettings,
      networkUrlInfo,
      checkNetworkAndUpdateUrl,
    }),
    [
      backendUrl,
      isReady,
      loading,
      saving,
      error,
      settings,
      lastLoadedAt,
      isBackendReachable,
      retryCountdown,
      refreshSettings,
      setBackendUrlHandler,
      updateBackendSettings,
      userSettings,
      userSettingsLoading,
      loadUserSettings,
      updateUserSettingsHandler,
      clearUserSettings,
      networkUrlInfo,
      checkNetworkAndUpdateUrl,
    ],
  );

  return <BackendSettingsContext.Provider value={value}>{children}</BackendSettingsContext.Provider>;
};

export const useBackendSettings = () => {
  const context = useContext(BackendSettingsContext);
  if (!context) {
    throw new Error('useBackendSettings must be used within a BackendSettingsProvider');
  }
  return context;
};
