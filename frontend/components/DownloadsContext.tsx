import React, { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import { Platform } from 'react-native';

import {
  downloadManager,
  type DownloadItem,
  type DownloadStatus,
  type StartDownloadParams,
} from '@/services/downloadManager';

// ---------------------------------------------------------------------------
// Context shape
// ---------------------------------------------------------------------------

interface DownloadsContextValue {
  items: DownloadItem[];
  wifiOnly: boolean;
  setWifiOnly: (value: boolean) => void;
  maxWorkers: number;
  setMaxWorkers: (value: number) => void;
  startDownload: (params: StartDownloadParams) => Promise<string>;
  pauseDownload: (id: string) => Promise<void>;
  resumeDownload: (id: string) => Promise<void>;
  cancelDownload: (id: string) => Promise<void>;
  deleteDownload: (id: string) => Promise<void>;
  getDownloadForTitle: (titleId: string, season?: number, episode?: number) => DownloadItem | undefined;
  getLocalFileUri: (titleId: string, season?: number, episode?: number) => string | null;
}

const NOOP_CONTEXT: DownloadsContextValue = {
  items: [],
  wifiOnly: false,
  setWifiOnly: () => {},
  maxWorkers: 1,
  setMaxWorkers: () => {},
  startDownload: async () => '',
  pauseDownload: async () => {},
  resumeDownload: async () => {},
  cancelDownload: async () => {},
  deleteDownload: async () => {},
  getDownloadForTitle: () => undefined,
  getLocalFileUri: () => null,
};

const DownloadsContext = createContext<DownloadsContextValue>(NOOP_CONTEXT);

// ---------------------------------------------------------------------------
// Provider
// ---------------------------------------------------------------------------

const isMobile = (Platform.OS === 'ios' || Platform.OS === 'android') && !Platform.isTV;

export const DownloadsProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [items, setItems] = useState<DownloadItem[]>([]);
  const [wifiOnly, setWifiOnlyState] = useState(false);
  const [maxWorkers, setMaxWorkersState] = useState(1);

  useEffect(() => {
    if (!isMobile) return;

    downloadManager.initialize().then(() => {
      setWifiOnlyState(downloadManager.getWifiOnly());
      setMaxWorkersState(downloadManager.getMaxWorkers());
    });
    const unsubscribe = downloadManager.subscribe(setItems);
    return unsubscribe;
  }, []);

  const setWifiOnly = useCallback((value: boolean) => {
    setWifiOnlyState(value);
    downloadManager.setWifiOnly(value);
  }, []);

  const setMaxWorkers = useCallback((value: number) => {
    setMaxWorkersState(value);
    downloadManager.setMaxWorkers(value);
  }, []);

  const startDownload = useCallback(async (params: StartDownloadParams) => {
    return downloadManager.startDownload(params);
  }, []);

  const pauseDownload = useCallback(async (id: string) => {
    await downloadManager.pauseDownload(id);
  }, []);

  const resumeDownload = useCallback(async (id: string) => {
    await downloadManager.resumeDownload(id);
  }, []);

  const cancelDownload = useCallback(async (id: string) => {
    await downloadManager.cancelDownload(id);
  }, []);

  const deleteDownload = useCallback(async (id: string) => {
    await downloadManager.deleteDownload(id);
  }, []);

  const getDownloadForTitle = useCallback(
    (titleId: string, season?: number, episode?: number) => {
      return downloadManager.getDownloadForTitle(titleId, season, episode);
    },
    // Re-derive when items change
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [items],
  );

  const getLocalFileUri = useCallback(
    (titleId: string, season?: number, episode?: number) => {
      return downloadManager.getLocalFileUri(titleId, season, episode);
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [items],
  );

  const value = useMemo<DownloadsContextValue>(
    () =>
      isMobile
        ? {
            items,
            wifiOnly,
            setWifiOnly,
            maxWorkers,
            setMaxWorkers,
            startDownload,
            pauseDownload,
            resumeDownload,
            cancelDownload,
            deleteDownload,
            getDownloadForTitle,
            getLocalFileUri,
          }
        : NOOP_CONTEXT,
    [items, wifiOnly, setWifiOnly, maxWorkers, setMaxWorkers, startDownload, pauseDownload, resumeDownload, cancelDownload, deleteDownload, getDownloadForTitle, getLocalFileUri],
  );

  return <DownloadsContext.Provider value={value}>{children}</DownloadsContext.Provider>;
};

// ---------------------------------------------------------------------------
// Hook
// ---------------------------------------------------------------------------

export const useDownloads = (): DownloadsContextValue => {
  return useContext(DownloadsContext);
};

export type { DownloadItem, DownloadStatus, StartDownloadParams };
