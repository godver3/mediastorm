import React, { useEffect } from 'react';
import { act, create, ReactTestRenderer } from 'react-test-renderer';

import AsyncStorage from '@react-native-async-storage/async-storage';

import { BackendSettingsProvider, useBackendSettings, type BackendSettings } from '@/components/BackendSettingsContext';
import { apiService } from '@/services/api';

jest.mock('@react-native-async-storage/async-storage', () => {
  const store: Record<string, string> = {};
  return {
    setItem: jest.fn(async (key: string, value: string) => {
      store[key] = value;
    }),
    getItem: jest.fn(async (key: string) => (Object.prototype.hasOwnProperty.call(store, key) ? store[key] : null)),
    removeItem: jest.fn(async (key: string) => {
      delete store[key];
    }),
    clear: jest.fn(async () => {
      Object.keys(store).forEach((key) => delete store[key]);
    }),
  };
});

const flushPromises = () => new Promise<void>((resolve) => setTimeout(resolve, 0));

const baseSettings: BackendSettings = {
  server: { host: '0.0.0.0', port: 7777 },
  usenet: [
    {
      name: 'Default',
      host: 'news.example',
      port: 563,
      ssl: true,
      username: 'demo',
      password: 'password',
      connections: 8,
      enabled: true,
    },
  ],
  indexers: [
    {
      name: 'Example Indexer',
      url: 'https://indexer.example/api',
      apiKey: 'key',
      type: 'newznab',
      enabled: true,
    },
  ],
  torrentScrapers: [],
  metadata: { tvdbApiKey: 'tvdb', tmdbApiKey: 'tmdb', language: 'en' },
  cache: { directory: 'cache', metadataTtlHours: 24 },
  webdav: { enabled: true, prefix: '/webdav', username: 'user', password: 'secret' },
  streaming: {
    maxDownloadWorkers: 4,
    maxCacheSizeMB: 1024,
    serviceMode: 'usenet',
    servicePriority: 'none',
    debridProviders: [],
  },
  transmux: { enabled: true, ffmpegPath: 'ffmpeg', ffprobePath: 'ffprobe' },
  playback: { preferredPlayer: 'native' },
  live: { playlistUrl: '', playlistCacheTtlHours: 6 },
  homeShelves: { shelves: [] },
  filtering: { maxSizeMovieGb: 0, maxSizeEpisodeGb: 0, excludeHdr: false, prioritizeHdr: false },
};

describe('BackendSettingsContext', () => {
  let renderer: ReactTestRenderer | null = null;
  let latestContext: ReturnType<typeof useBackendSettings> | null = null;

  const Capture: React.FC = () => {
    const context = useBackendSettings();
    useEffect(() => {
      latestContext = context;
    }, [context]);
    return null;
  };

  const mountProvider = async () => {
    await act(async () => {
      renderer = create(
        <BackendSettingsProvider>
          <Capture />
        </BackendSettingsProvider>,
      );
    });
  };

  const waitForCondition = async (predicate: () => boolean, timeoutMs = 2000) => {
    const started = Date.now();
    while (!predicate()) {
      if (Date.now() - started > timeoutMs) {
        throw new Error('Timed out waiting for condition');
      }
      await act(async () => {
        await flushPromises();
      });
    }
  };

  beforeEach(async () => {
    jest.clearAllMocks();
    await (AsyncStorage as any).clear();
    latestContext = null;
  });

  afterEach(async () => {
    if (renderer) {
      await act(async () => {
        renderer?.unmount();
      });
      renderer = null;
    }
    jest.restoreAllMocks();
  });

  it('initialises with stored backend URL and loads settings', async () => {
    const getSettingsSpy = jest.spyOn(apiService, 'getSettings').mockResolvedValue(baseSettings);
    const setBaseUrlSpy = jest.spyOn(apiService, 'setBaseUrl');

    await AsyncStorage.setItem('strmr.backendUrl', 'http://stored.example:9000/api/');

    await mountProvider();

    await waitForCondition(() => !!latestContext?.isReady);

    expect(
      setBaseUrlSpy.mock.calls.some(
        ([url]) => url === 'http://stored.example:9000/api' || url === 'http://stored.example:9000/api/',
      ),
    ).toBe(true);
    expect(getSettingsSpy).toHaveBeenCalledTimes(1);
    expect(latestContext?.backendUrl).toBe('http://stored.example:9000/api');
    expect(latestContext?.settings).toEqual(baseSettings);
    expect(latestContext?.error).toBeNull();
  });

  it('normalises and persists backend URL updates', async () => {
    jest.spyOn(apiService, 'getSettings').mockResolvedValue(baseSettings);
    const setBaseUrlSpy = jest.spyOn(apiService, 'setBaseUrl');

    await mountProvider();
    await waitForCondition(() => !!latestContext?.isReady);

    await act(async () => {
      await latestContext?.setBackendUrl('demo-host');
    });

    await waitForCondition(() => latestContext?.backendUrl === 'http://demo-host:7777/api');

    expect(setBaseUrlSpy).toHaveBeenCalledWith('http://demo-host:7777/api');
    expect((AsyncStorage.setItem as jest.Mock).mock.calls).toContainEqual([
      'strmr.backendUrl',
      'http://demo-host:7777/api',
    ]);
  });

  it('surfaces errors when updating backend settings fails', async () => {
    jest.spyOn(apiService, 'getSettings').mockResolvedValue(baseSettings);
    jest.spyOn(apiService, 'setBaseUrl');
    const updateSpy = jest.spyOn(apiService, 'updateSettings').mockRejectedValue(new Error('save failed'));

    await mountProvider();
    await waitForCondition(() => !!latestContext?.isReady);

    await act(async () => {
      await expect(latestContext?.updateBackendSettings(baseSettings)).rejects.toThrow('save failed');
    });

    expect(updateSpy).toHaveBeenCalledWith(baseSettings);
    await waitForCondition(() => latestContext?.error === 'save failed');
  });
});
