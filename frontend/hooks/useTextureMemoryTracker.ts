import { useEffect, useRef, useCallback } from 'react';
import { Platform } from 'react-native';

// Bytes per pixel for decoded textures on Android (ARGB_8888)
const BYTES_PER_PIXEL = 4;

interface TrackedTexture {
  id: string;
  label: string; // e.g. "continue-watching", "watchlist", "hero"
  url: string;
  width: number;
  height: number;
  estimatedBytes: number;
}

type SnapshotEntry = {
  label: string;
  count: number;
  estimatedMB: number;
  largestItem?: { url: string; widthxheight: string; mb: number };
};

class TextureMemoryTracker {
  private textures = new Map<string, TrackedTexture>();
  private nextId = 0;
  private enabled = false;

  enable() {
    this.enabled = true;
  }

  disable() {
    this.enabled = false;
  }

  isEnabled() {
    return this.enabled;
  }

  /**
   * Register a mounted image. Returns an ID to use for unregistering.
   */
  register(label: string, url: string): string {
    const id = `tex_${this.nextId++}`;
    if (this.enabled) {
      this.textures.set(id, {
        id,
        label,
        url: url?.substring(0, 120) ?? '',
        width: 0,
        height: 0,
        estimatedBytes: 0,
      });
    }
    return id;
  }

  /**
   * Called when an image finishes loading — records the decoded dimensions.
   */
  onLoad(id: string, width: number, height: number) {
    if (!this.enabled) return;
    const entry = this.textures.get(id);
    if (entry) {
      entry.width = width;
      entry.height = height;
      entry.estimatedBytes = width * height * BYTES_PER_PIXEL;
    }
  }

  /**
   * Unregister an image (on unmount).
   */
  unregister(id: string) {
    this.textures.delete(id);
  }

  /**
   * Get a snapshot of current GPU texture memory usage grouped by label.
   */
  snapshot(): { totalMB: number; byLabel: SnapshotEntry[]; topTextures: { label: string; url: string; dims: string; mb: number }[] } {
    const byLabel = new Map<string, { count: number; bytes: number; largest?: TrackedTexture }>();
    let totalBytes = 0;

    for (const tex of this.textures.values()) {
      totalBytes += tex.estimatedBytes;
      const group = byLabel.get(tex.label) ?? { count: 0, bytes: 0 };
      group.count++;
      group.bytes += tex.estimatedBytes;
      if (!group.largest || tex.estimatedBytes > group.largest.estimatedBytes) {
        group.largest = tex;
      }
      byLabel.set(tex.label, group);
    }

    const labels: SnapshotEntry[] = [];
    for (const [label, data] of byLabel) {
      const entry: SnapshotEntry = {
        label,
        count: data.count,
        estimatedMB: Math.round((data.bytes / (1024 * 1024)) * 100) / 100,
      };
      if (data.largest && data.largest.estimatedBytes > 0) {
        entry.largestItem = {
          url: data.largest.url,
          widthxheight: `${data.largest.width}x${data.largest.height}`,
          mb: Math.round((data.largest.estimatedBytes / (1024 * 1024)) * 100) / 100,
        };
      }
      labels.push(entry);
    }

    // Sort by estimated MB descending
    labels.sort((a, b) => b.estimatedMB - a.estimatedMB);

    // Top 5 individual textures by size
    const allTextures = Array.from(this.textures.values())
      .filter(t => t.estimatedBytes > 0)
      .sort((a, b) => b.estimatedBytes - a.estimatedBytes)
      .slice(0, 5)
      .map(t => ({
        label: t.label,
        url: t.url,
        dims: `${t.width}x${t.height}`,
        mb: Math.round((t.estimatedBytes / (1024 * 1024)) * 100) / 100,
      }));

    return {
      totalMB: Math.round((totalBytes / (1024 * 1024)) * 100) / 100,
      byLabel: labels,
      topTextures: allTextures,
    };
  }

  /**
   * Log a formatted snapshot to console.
   */
  logSnapshot(context?: string) {
    if (!this.enabled) return;
    const snap = this.snapshot();
    const ctx = context ? ` [${context}]` : '';
    console.log(`[TextureMemory]${ctx} Total: ${snap.totalMB} MB (${this.textures.size} textures)`);
    for (const entry of snap.byLabel) {
      const largest = entry.largestItem ? ` (largest: ${entry.largestItem.widthxheight} = ${entry.largestItem.mb}MB)` : '';
      console.log(`  ${entry.label}: ${entry.estimatedMB} MB (${entry.count} images)${largest}`);
    }
    if (snap.topTextures.length > 0) {
      console.log(`  Top textures:`);
      for (const t of snap.topTextures) {
        console.log(`    ${t.dims} ${t.mb}MB [${t.label}] ${t.url}`);
      }
    }
  }
}

// Singleton instance
export const textureTracker = new TextureMemoryTracker();

/**
 * Hook to enable periodic texture memory logging on the index page.
 * @param label Context label for log messages
 * @param intervalMs Logging interval (default 15s)
 * @param enabled Whether tracking is active
 */
export function useTextureMemoryMonitor(label: string, intervalMs: number = 15000, enabled: boolean = false) {
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    if (!enabled) {
      textureTracker.disable();
      return;
    }

    textureTracker.enable();
    console.log(`[TextureMemory] Monitoring started (interval=${intervalMs}ms)`);

    // Initial snapshot after short delay to let images load
    const initialTimeout = setTimeout(() => textureTracker.logSnapshot(label), 3000);

    intervalRef.current = setInterval(() => {
      textureTracker.logSnapshot(label);
    }, intervalMs);

    return () => {
      clearTimeout(initialTimeout);
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
      textureTracker.disable();
      console.log(`[TextureMemory] Monitoring stopped`);
    };
  }, [enabled, intervalMs, label]);

  const logNow = useCallback((context?: string) => {
    textureTracker.logSnapshot(context ?? label);
  }, [label]);

  return { logNow };
}
