import { useEffect, useRef, useCallback } from 'react';
import { Platform } from 'react-native';
import DeviceInfo from 'react-native-device-info';

interface MemoryStats {
  usedMB: number;
  totalMB: number;
  percentUsed: number;
  maxMemoryMB?: number; // Android JVM max heap
}

/**
 * Hook to monitor memory usage at regular intervals
 * @param label - Label for log messages (e.g., "HomePage", "Player")
 * @param intervalMs - How often to log (default: 10000ms = 10s)
 * @param enabled - Whether monitoring is active (default: true)
 */
export function useMemoryMonitor(
  label: string,
  intervalMs: number = 10000,
  enabled: boolean = true
) {
  const intervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const mountTimeRef = useRef<number>(Date.now());

  const logMemory = useCallback(async () => {
    try {
      const [usedMemory, totalMemory] = await Promise.all([
        DeviceInfo.getUsedMemory(),
        DeviceInfo.getTotalMemory(),
      ]);

      const usedMB = Math.round(usedMemory / (1024 * 1024));
      const totalMB = Math.round(totalMemory / (1024 * 1024));
      const percentUsed = Math.round((usedMemory / totalMemory) * 100);
      const elapsedSec = Math.round((Date.now() - mountTimeRef.current) / 1000);

      const stats: MemoryStats = {
        usedMB,
        totalMB,
        percentUsed,
      };

      // On Android, also get max JVM heap
      if (Platform.OS === 'android') {
        try {
          const maxMemory = await DeviceInfo.getMaxMemory();
          stats.maxMemoryMB = Math.round(maxMemory / (1024 * 1024));
        } catch {
          // getMaxMemory may not be available on all versions
        }
      }

      const maxInfo = stats.maxMemoryMB ? `, heap_limit=${stats.maxMemoryMB}MB` : '';
      console.log(
        `[MemoryMonitor:${label}] elapsed=${elapsedSec}s, used=${usedMB}MB/${totalMB}MB (${percentUsed}%)${maxInfo}`
      );

      // Warn if memory usage is high
      if (percentUsed > 80) {
        console.warn(`[MemoryMonitor:${label}] HIGH MEMORY WARNING: ${percentUsed}% used`);
      }

      return stats;
    } catch (error) {
      console.error(`[MemoryMonitor:${label}] Error getting memory info:`, error);
      return null;
    }
  }, [label]);

  useEffect(() => {
    if (!enabled) {
      return;
    }

    // Log immediately on mount
    console.log(`[MemoryMonitor:${label}] Starting memory monitoring (interval=${intervalMs}ms)`);
    logMemory();

    // Set up interval
    intervalRef.current = setInterval(logMemory, intervalMs);

    return () => {
      if (intervalRef.current) {
        clearInterval(intervalRef.current);
        intervalRef.current = null;
      }
      console.log(`[MemoryMonitor:${label}] Stopped memory monitoring`);
    };
  }, [enabled, intervalMs, label, logMemory]);

  return { logMemory };
}

/**
 * One-shot memory log function for use outside of hooks
 */
export async function logMemorySnapshot(label: string): Promise<MemoryStats | null> {
  try {
    const [usedMemory, totalMemory] = await Promise.all([
      DeviceInfo.getUsedMemory(),
      DeviceInfo.getTotalMemory(),
    ]);

    const usedMB = Math.round(usedMemory / (1024 * 1024));
    const totalMB = Math.round(totalMemory / (1024 * 1024));
    const percentUsed = Math.round((usedMemory / totalMemory) * 100);

    const stats: MemoryStats = {
      usedMB,
      totalMB,
      percentUsed,
    };

    if (Platform.OS === 'android') {
      try {
        const maxMemory = await DeviceInfo.getMaxMemory();
        stats.maxMemoryMB = Math.round(maxMemory / (1024 * 1024));
      } catch {
        // Ignore
      }
    }

    const maxInfo = stats.maxMemoryMB ? `, heap_limit=${stats.maxMemoryMB}MB` : '';
    console.log(
      `[MemorySnapshot:${label}] used=${usedMB}MB/${totalMB}MB (${percentUsed}%)${maxInfo}`
    );

    return stats;
  } catch (error) {
    console.error(`[MemorySnapshot:${label}] Error:`, error);
    return null;
  }
}
