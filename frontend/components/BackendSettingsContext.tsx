import AsyncStorage from '@react-native-async-storage/async-storage';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { apiService, UserSettings } from '@/services/api';

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
}

export interface BackendLiveSettings {
  playlistUrl: string;
  playlistCacheTtlHours: number;
}

export interface BackendShelfConfig {
  id: string;
  name: string;
  enabled: boolean;
  order: number;
}

export type TrendingMovieSource = 'all' | 'released';

export interface BackendHomeShelvesSettings {
  shelves: BackendShelfConfig[];
  trendingMovieSource?: TrendingMovieSource;
}

export interface BackendFilterSettings {
  maxSizeMovieGb: number;
  maxSizeEpisodeGb: number;
  excludeHdr: boolean;
  prioritizeHdr: boolean;
  filterOutTerms?: string[];
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
  const retryTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const countdownTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const retryFnRef = useRef<(() => Promise<boolean>) | null>(null);

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

  // Returns: { success: boolean, authRequired: boolean }
  const refreshSettingsInternal = useCallback(async (): Promise<{ success: boolean; authRequired: boolean }> => {
    if (!mountedRef.current) {
      return { success: false, authRequired: false };
    }

    setLoading(true);
    try {
      const result = (await apiService.getSettings()) as BackendSettings;
      if (!mountedRef.current) {
        return { success: false, authRequired: false };
      }
      console.log('[BackendSettings] Successfully connected to backend');
      setSettings(result);
      setError(null);
      setLastLoadedAt(Date.now());
      setIsBackendReachable(true);
      stopRetryTimer();
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
    }
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

  useEffect(() => {
    let cancelled = false;

    const initialise = async () => {
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

      if (!cancelled && mountedRef.current) {
        setIsReady(true);
      }

      try {
        await refreshSettings();
      } catch (err) {
        if (!cancelled) {
          console.warn('Failed to load backend settings:', err);
        }
      }
    };

    void initialise();

    return () => {
      cancelled = true;
    };
  }, [applyApiBaseUrl, refreshSettings]);

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

  const updateBackendSettings = useCallback(
    async (next: BackendSettings) => {
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
    },
    [],
  );

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
