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

  useEffect(() => {
    if (!isMobile) return;

    downloadManager.initialize();
    const unsubscribe = downloadManager.subscribe(setItems);
    return unsubscribe;
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
            startDownload,
            pauseDownload,
            resumeDownload,
            cancelDownload,
            deleteDownload,
            getDownloadForTitle,
            getLocalFileUri,
          }
        : NOOP_CONTEXT,
    [items, startDownload, pauseDownload, resumeDownload, cancelDownload, deleteDownload, getDownloadForTitle, getLocalFileUri],
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
