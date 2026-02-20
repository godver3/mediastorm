/**
 * Download Manager — singleton service for offline playback downloads.
 *
 * Handles file downloads via @kesha-antonov/react-native-background-downloader,
 * persistence via AsyncStorage, progress tracking, pause/resume, and disk-space checks.
 *
 * Uses OS-managed background sessions that survive app termination.
 * On relaunch, checkForExistingDownloads() re-attaches to in-flight tasks.
 *
 * Mobile-only (iOS & Android phones/tablets). No-ops on TV and web.
 */

import { Platform } from 'react-native';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { Paths, File, Directory } from 'expo-file-system';
import {
  createDownloadTask,
  getExistingDownloadTasks,
  completeHandler,
  setConfig,
  type DownloadTask,
} from '@kesha-antonov/react-native-background-downloader';
import { apiService } from './api';

// Lazy-load expo-network — native module may not be compiled in yet.
// Falls back gracefully: assumes WiFi if module unavailable.
let _Network: typeof import('expo-network') | null = null;
let _networkLoaded = false;
const getNetwork = (): typeof import('expo-network') | null => {
  if (!_networkLoaded) {
    _networkLoaded = true;
    try {
      _Network = require('expo-network');
    } catch {
      console.warn('[DownloadManager] expo-network native module not available — wifi-only check disabled');
    }
  }
  return _Network;
};

const generateId = (): string => {
  const hex = '0123456789abcdef';
  let id = '';
  for (let i = 0; i < 16; i++) {
    id += hex[Math.floor(Math.random() * 16)];
  }
  return `dl_${Date.now()}_${id}`;
};

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

export type DownloadStatus = 'pending' | 'downloading' | 'paused' | 'completed' | 'error';

export interface DownloadItem {
  id: string;
  titleId: string;
  mediaType: 'movie' | 'episode';
  title: string;
  posterUrl: string;
  seriesTitle?: string;
  seasonNumber?: number;
  episodeNumber?: number;
  episodeName?: string;
  streamPath: string;
  fileSize: number;
  status: DownloadStatus;
  progress: number; // 0.0–1.0
  bytesWritten: number;
  bytesPerSecond: number; // current download speed (transient, not persisted)
  errorMessage?: string;
  localFilePath: string;
  createdAt: string;
  completedAt?: string;
  imdbId?: string;
  tvdbId?: string;
  seriesIdentifier?: string; // For progress itemId: titleId stripped of episode suffix
}

export interface StartDownloadParams {
  titleId: string;
  mediaType: 'movie' | 'episode';
  title: string;
  posterUrl: string;
  streamPath: string;
  fileSize: number;
  seriesTitle?: string;
  seasonNumber?: number;
  episodeNumber?: number;
  episodeName?: string;
  imdbId?: string;
  tvdbId?: string;
  seriesIdentifier?: string;
}

type Listener = (items: DownloadItem[]) => void;

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const STORAGE_KEY = 'strmr.downloads';
const WIFI_ONLY_KEY = 'strmr.downloads.wifiOnly';
const MAX_WORKERS_KEY = 'strmr.downloads.maxWorkers';
const isMobile = (Platform.OS === 'ios' || Platform.OS === 'android') && !Platform.isTV;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const sanitize = (name: string) =>
  name
    .replace(/[^a-zA-Z0-9._-]/g, '_')
    .replace(/_+/g, '_')
    .substring(0, 80);

const extensionFrom = (path: string): string => {
  const lastDot = path.lastIndexOf('.');
  if (lastDot === -1) return 'mkv'; // default
  const ext = path.substring(lastDot + 1).split(/[?#]/)[0].toLowerCase();
  return ext || 'mkv';
};

const getDownloadsDir = (): Directory => new Directory(Paths.document, 'downloads');
const getDownloadsDirUri = (): string => getDownloadsDir().uri;

/** Strip file:// prefix for the native downloader's destination param */
const toPlainPath = (uri: string): string =>
  uri.startsWith('file://') ? uri.slice(7) : uri;

// ---------------------------------------------------------------------------
// Singleton
// ---------------------------------------------------------------------------

class DownloadManager {
  private items: DownloadItem[] = [];
  private listeners = new Set<Listener>();
  private tasks = new Map<string, DownloadTask>();
  private initialized = false;
  private initializing: Promise<void> | null = null;
  private wifiOnly = false;
  private maxWorkers = 1;
  private networkSubscription: { remove: () => void } | null = null;
  private _wifiChangePending = false;

  // -------------------------------------------------------------------------
  // Init
  // -------------------------------------------------------------------------

  async initialize(): Promise<void> {
    if (!isMobile) return;
    if (this.initialized) return;
    if (this.initializing) return this.initializing;

    this.initializing = this._doInit();
    await this.initializing;
  }

  private async _doInit(): Promise<void> {
    try {
      // Configure the background downloader
      setConfig({
        progressInterval: 500,
        isLogsEnabled: __DEV__,
      });

      // Ensure downloads directory exists
      const dir = getDownloadsDir();
      if (!dir.exists) {
        dir.create();
      }

      // Load persisted metadata
      const raw = await AsyncStorage.getItem(STORAGE_KEY);
      if (raw) {
        const parsed: DownloadItem[] = JSON.parse(raw);
        // Verify completed files still exist
        const verified: DownloadItem[] = [];
        for (const item of parsed) {
          if (item.status === 'completed') {
            const file = new File(item.localFilePath);
            if (file.exists) {
              verified.push(item);
            } else {
              console.log(`[DownloadManager] File missing for completed download "${item.title}", removing`);
            }
          } else if (item.status === 'downloading' || item.status === 'paused') {
            // Keep as-is for now; we'll reconcile with native tasks below
            verified.push({ ...item, status: 'paused' });
          } else {
            verified.push(item);
          }
        }
        this.items = verified;
      }

      // Load preferences
      const wifiOnlyRaw = await AsyncStorage.getItem(WIFI_ONLY_KEY);
      this.wifiOnly = wifiOnlyRaw === 'true';
      const maxWorkersRaw = await AsyncStorage.getItem(MAX_WORKERS_KEY);
      this.maxWorkers = maxWorkersRaw ? Math.min(3, Math.max(1, parseInt(maxWorkersRaw, 10) || 1)) : 1;

      // Re-attach to background tasks that survived app termination
      await this._reattachExistingTasks();

      // Listen for network changes — pause on WiFi loss, resume on WiFi reconnect
      const net = getNetwork();
      if (net) {
        this.networkSubscription = net.addNetworkStateListener((state) => {
          if (this.wifiOnly && state.type !== net.NetworkStateType.WIFI) {
            const active = this.items.filter((i) => i.status === 'downloading');
            for (const item of active) this._suspendForWifi(item);
          } else {
            this._processQueue();
          }
        });
      }

      await this.persist();
    } catch (err) {
      console.error('[DownloadManager] Init error:', err);
    }
    this.initialized = true;
    this.notify();
    this._processQueue();
  }

  private async _reattachExistingTasks(): Promise<void> {
    try {
      const existingTasks = await getExistingDownloadTasks();
      console.log(`[DownloadManager] Found ${existingTasks.length} existing background task(s)`);

      for (const task of existingTasks) {
        const item = this.items.find((i) => i.id === task.id);
        if (!item) {
          // Orphaned native task with no metadata — stop it
          console.log(`[DownloadManager] Stopping orphaned task: ${task.id}`);
          await task.stop();
          continue;
        }

        if (task.state === 'DONE') {
          // Task completed while app was terminated
          item.status = 'completed';
          item.progress = 1;
          item.bytesWritten = item.fileSize;
          item.completedAt = new Date().toISOString();
          await completeHandler(task.id);
          continue;
        }

        if (task.state === 'FAILED' || task.state === 'STOPPED') {
          item.status = 'error';
          item.errorMessage = 'Download interrupted';
          continue;
        }

        // DOWNLOADING or PAUSED — re-attach callbacks
        this._attachCallbacks(item, task);
        this.tasks.set(item.id, task);

        if (task.state === 'DOWNLOADING') {
          item.status = 'downloading';
          // Update progress from native state
          if (task.bytesTotal > 0) {
            item.progress = task.bytesDownloaded / task.bytesTotal;
            item.bytesWritten = task.bytesDownloaded;
          }
        } else if (task.state === 'PAUSED') {
          item.status = 'paused';
        }
      }
    } catch (err) {
      console.error('[DownloadManager] Error re-attaching existing tasks:', err);
    }
  }

  // -------------------------------------------------------------------------
  // Persistence
  // -------------------------------------------------------------------------

  private async persist(): Promise<void> {
    try {
      await AsyncStorage.setItem(STORAGE_KEY, JSON.stringify(this.items));
    } catch (err) {
      console.error('[DownloadManager] Persist error:', err);
    }
  }

  // -------------------------------------------------------------------------
  // Event emitter
  // -------------------------------------------------------------------------

  subscribe(listener: Listener): () => void {
    this.listeners.add(listener);
    // Immediately send current state
    listener([...this.items]);
    return () => {
      this.listeners.delete(listener);
    };
  }

  private notify(): void {
    const snapshot = [...this.items];
    for (const fn of this.listeners) {
      fn(snapshot);
    }
  }

  // -------------------------------------------------------------------------
  // Queries
  // -------------------------------------------------------------------------

  getDownloads(): DownloadItem[] {
    return [...this.items];
  }

  getDownloadForTitle(titleId: string, season?: number, episode?: number): DownloadItem | undefined {
    return this.items.find((item) => {
      if (item.titleId !== titleId) return false;
      if (season !== undefined && item.seasonNumber !== season) return false;
      if (episode !== undefined && item.episodeNumber !== episode) return false;
      return true;
    });
  }

  getLocalFileUri(titleId: string, season?: number, episode?: number): string | null {
    const item = this.getDownloadForTitle(titleId, season, episode);
    if (item?.status === 'completed') return item.localFilePath;
    return null;
  }

  // -------------------------------------------------------------------------
  // Start download
  // -------------------------------------------------------------------------

  async startDownload(params: StartDownloadParams): Promise<string> {
    if (!isMobile) throw new Error('Downloads only available on mobile');
    await this.initialize();

    // Check for existing download of same content
    const existing = this.getDownloadForTitle(
      params.titleId,
      params.seasonNumber,
      params.episodeNumber,
    );
    if (existing) {
      if (existing.status === 'completed') return existing.id;
      if (existing.status === 'downloading' || existing.status === 'pending') return existing.id;
      if (existing.status === 'paused') {
        await this.resumeDownload(existing.id);
        return existing.id;
      }
      // error status — remove and re-download
      await this._removeItem(existing.id);
    }

    // Check disk space (skip if fileSize is unknown)
    if (params.fileSize > 0) {
      let freeBytes: number;
      try {
        freeBytes = Paths.availableDiskSpace;
      } catch (err) {
        console.warn('[DownloadManager] Could not read available disk space:', err);
        freeBytes = Infinity; // allow download if we can't check
      }
      console.log(`[DownloadManager] Disk space check: free=${freeBytes}, needed=${params.fileSize}`);
      if (freeBytes < params.fileSize * 1.1) {
        throw new Error('Not enough disk space for this download');
      }
    }

    const id = generateId();
    const ext = extensionFrom(params.streamPath);
    const filename = `${id}_${sanitize(params.title)}.${ext}`;
    const localFilePath = `${getDownloadsDirUri()}${filename}`;

    const item: DownloadItem = {
      id,
      titleId: params.titleId,
      mediaType: params.mediaType,
      title: params.title,
      posterUrl: params.posterUrl,
      seriesTitle: params.seriesTitle,
      seasonNumber: params.seasonNumber,
      episodeNumber: params.episodeNumber,
      episodeName: params.episodeName,
      streamPath: params.streamPath,
      fileSize: params.fileSize,
      status: 'pending',
      progress: 0,
      bytesWritten: 0,
      bytesPerSecond: 0,
      localFilePath,
      createdAt: new Date().toISOString(),
      imdbId: params.imdbId,
      tvdbId: params.tvdbId,
      seriesIdentifier: params.seriesIdentifier,
    };

    this.items.push(item);
    await this.persist();
    this.notify();

    // Start if no other active download
    this._processQueue();

    return id;
  }

  // -------------------------------------------------------------------------
  // Pause / Resume / Cancel / Delete
  // -------------------------------------------------------------------------

  async pauseDownload(id: string): Promise<void> {
    const item = this.items.find((i) => i.id === id);
    if (!item || item.status !== 'downloading') return;

    const task = this.tasks.get(id);
    if (task) {
      try {
        await task.pause();
      } catch (err) {
        console.warn('[DownloadManager] Pause error:', err);
      }
    }
    item.status = 'paused';
    await this.persist();
    this.notify();
  }

  async resumeDownload(id: string): Promise<void> {
    const item = this.items.find((i) => i.id === id);
    if (!item || (item.status !== 'paused' && item.status !== 'error')) return;

    // If we still have the native task handle, resume directly
    const task = this.tasks.get(id);
    if (task && item.status === 'paused') {
      try {
        await task.resume();
        item.status = 'downloading';
        item.errorMessage = undefined;
        await this.persist();
        this.notify();
        return;
      } catch (err) {
        console.warn('[DownloadManager] Direct resume failed, re-queuing:', err);
        this.tasks.delete(id);
      }
    }

    // Fallback: re-queue as pending for a fresh download
    item.status = 'pending';
    item.errorMessage = undefined;
    await this.persist();
    this.notify();
    this._processQueue();
  }

  async cancelDownload(id: string): Promise<void> {
    const task = this.tasks.get(id);
    if (task) {
      try {
        await task.stop();
      } catch {
        // ignore
      }
      this.tasks.delete(id);
    }
    await this._removeItem(id);
  }

  async deleteDownload(id: string): Promise<void> {
    await this.cancelDownload(id);
  }

  private async _removeItem(id: string): Promise<void> {
    const item = this.items.find((i) => i.id === id);
    if (item) {
      try {
        const file = new File(item.localFilePath);
        if (file.exists) {
          file.delete();
        }
      } catch {
        // ignore
      }
    }
    this.items = this.items.filter((i) => i.id !== id);
    this.tasks.delete(id);
    await this.persist();
    this.notify();
    // Start next pending if there was an active download removed
    this._processQueue();
  }

  // -------------------------------------------------------------------------
  // Wi-Fi only preference
  // -------------------------------------------------------------------------

  getWifiOnly(): boolean {
    return this.wifiOnly;
  }

  async setWifiOnly(value: boolean): Promise<void> {
    this.wifiOnly = value;
    AsyncStorage.setItem(WIFI_ONLY_KEY, String(value));

    // Coalesce rapid toggles — only the last one takes effect
    if (this._wifiChangePending) return;
    this._wifiChangePending = true;

    // Yield to let any further synchronous toggles update this.wifiOnly first
    await new Promise((r) => setTimeout(r, 150));
    this._wifiChangePending = false;

    await this._enforceNetworkPolicy();
  }

  private async _enforceNetworkPolicy(): Promise<void> {
    if (this.wifiOnly) {
      const onWifi = await this._isOnWifi();
      if (!onWifi) {
        const active = this.items.filter((i) => i.status === 'downloading');
        for (const item of active) await this._suspendForWifi(item);
        if (active.length > 0) return;
      }
    }
    this.notify();
    this._processQueue();
  }

  /** Stop active download and return it to pending so _processQueue picks it up first. */
  private async _suspendForWifi(item: DownloadItem): Promise<void> {
    const task = this.tasks.get(item.id);
    if (task) {
      try { await task.stop(); } catch { /* ignore */ }
      this.tasks.delete(item.id);
    }
    item.status = 'pending';
    await this.persist();
    this.notify();
  }

  getMaxWorkers(): number {
    return this.maxWorkers;
  }

  async setMaxWorkers(value: number): Promise<void> {
    this.maxWorkers = Math.min(3, Math.max(1, value));
    await AsyncStorage.setItem(MAX_WORKERS_KEY, String(this.maxWorkers));
    this.notify();
    this._processQueue();
  }

  private async _isOnWifi(): Promise<boolean> {
    const net = getNetwork();
    if (!net) return true; // native module unavailable — allow download
    try {
      const state = await net.getNetworkStateAsync();
      return state.type === net.NetworkStateType.WIFI;
    } catch {
      return true; // allow download if we can't determine network type
    }
  }

  // -------------------------------------------------------------------------
  // Queue processing (up to maxWorkers concurrent downloads)
  // -------------------------------------------------------------------------

  private _processQueue(): void {
    const activeCount = this.items.filter((i) => i.status === 'downloading').length;
    const slots = this.maxWorkers - activeCount;
    if (slots <= 0) return;

    const pending = this.items.filter((i) => i.status === 'pending').slice(0, slots);
    if (pending.length === 0) return;

    if (this.wifiOnly) {
      this._isOnWifi().then((onWifi) => {
        if (onWifi) {
          for (const item of pending) this._startDownloading(item);
        }
      });
      return;
    }

    for (const item of pending) this._startDownloading(item);
  }

  private _startDownloading(item: DownloadItem): void {
    const url = this._buildDownloadUrl(item.streamPath);
    const destination = toPlainPath(item.localFilePath);

    const task = createDownloadTask({
      id: item.id,
      url,
      destination,
    });

    this._attachCallbacks(item, task);
    this.tasks.set(item.id, task);

    item.status = 'downloading';
    this.persist();
    this.notify();

    task.start();
  }

  private _attachCallbacks(item: DownloadItem, task: DownloadTask): void {
    let lastSpeedBytes = item.bytesWritten;
    let lastSpeedTime = Date.now();

    task
      .begin(({ expectedBytes }) => {
        // Update fileSize if we didn't know it before
        if (item.fileSize <= 0 && expectedBytes > 0) {
          item.fileSize = expectedBytes;
        }
        console.log(`[DownloadManager] Download began: "${item.title}" (${expectedBytes} bytes)`);
      })
      .progress(({ bytesDownloaded, bytesTotal }) => {
        item.bytesWritten = bytesDownloaded;
        item.progress = bytesTotal > 0 ? bytesDownloaded / bytesTotal : 0;

        // EMA speed calculation
        const now = Date.now();
        const elapsed = (now - lastSpeedTime) / 1000;
        if (elapsed > 0) {
          const bytesDelta = bytesDownloaded - lastSpeedBytes;
          const instantSpeed = bytesDelta / elapsed;
          item.bytesPerSecond = item.bytesPerSecond > 0
            ? item.bytesPerSecond * 0.3 + instantSpeed * 0.7
            : instantSpeed;
        }
        lastSpeedBytes = bytesDownloaded;
        lastSpeedTime = now;

        this.notify();
      })
      .done(async () => {
        item.status = 'completed';
        item.progress = 1;
        item.bytesWritten = item.fileSize;
        item.completedAt = new Date().toISOString();
        item.bytesPerSecond = 0;
        this.tasks.delete(item.id);

        // iOS background session cleanup
        try {
          await completeHandler(task.id);
        } catch {
          // ignore — may not be in background session context
        }

        await this.persist();
        this.notify();
        this._processQueue();
      })
      .error(async ({ error, errorCode }) => {
        // Don't mark as error if it was paused/cancelled
        if (item.status === 'paused') return;
        item.status = 'error';
        item.errorMessage = error || `Download failed (code ${errorCode})`;
        item.bytesPerSecond = 0;
        this.tasks.delete(item.id);
        console.error(`[DownloadManager] Download error for "${item.title}":`, error, errorCode);

        await this.persist();
        this.notify();
        this._processQueue();
      });
  }

  private _buildDownloadUrl(streamPath: string): string {
    const base = apiService.getBaseUrl().replace(/\/$/, '');
    const token = apiService.getAuthToken();

    let normalizedPath = streamPath;
    try {
      normalizedPath = decodeURIComponent(streamPath);
    } catch {
      // use raw
    }

    const params: Record<string, string> = {
      path: normalizedPath,
      transmux: '0',
    };
    if (token) {
      params.token = token;
    }

    const search = Object.entries(params)
      .map(([key, value]) => `${encodeURIComponent(key)}=${encodeURIComponent(value)}`)
      .join('&');

    return `${base}/video/stream?${search}`;
  }
}

// ---------------------------------------------------------------------------
// Export singleton
// ---------------------------------------------------------------------------

export const downloadManager = new DownloadManager();
