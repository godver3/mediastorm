/**
 * Dev-only performance overlay for TV.
 * Shows FPS, memory, JS thread usage on-screen.
 * Only renders in __DEV__ mode.
 */
import { useEffect, useRef, useState, useCallback } from 'react';
import { Platform, StyleSheet, Text, View } from 'react-native';
import DeviceInfo from 'react-native-device-info';

interface PerfStats {
  fps: number;
  memoryMB: number;
  memoryPercent: number;
  jsFrameDrops: number;
}

export function DevPerfOverlay() {
  // Only show in dev mode
  if (!__DEV__) return null;

  return <PerfOverlayInner />;
}

function PerfOverlayInner() {
  const [stats, setStats] = useState<PerfStats>({
    fps: 0,
    memoryMB: 0,
    memoryPercent: 0,
    jsFrameDrops: 0,
  });
  const frameCountRef = useRef(0);
  const lastTimeRef = useRef(performance.now());
  const droppedRef = useRef(0);
  const rafRef = useRef<number | null>(null);

  // FPS counter using requestAnimationFrame
  const measureFPS = useCallback(() => {
    frameCountRef.current++;
    const now = performance.now();
    const elapsed = now - lastTimeRef.current;

    // Calculate every second
    if (elapsed >= 1000) {
      const fps = Math.round((frameCountRef.current * 1000) / elapsed);
      // Detect frame drops (target 60fps = ~16.7ms per frame)
      if (elapsed / frameCountRef.current > 20) {
        droppedRef.current++;
      }

      setStats((prev) => ({
        ...prev,
        fps,
        jsFrameDrops: droppedRef.current,
      }));

      frameCountRef.current = 0;
      lastTimeRef.current = now;
    }

    rafRef.current = requestAnimationFrame(measureFPS);
  }, []);

  // Start FPS measurement
  useEffect(() => {
    rafRef.current = requestAnimationFrame(measureFPS);
    return () => {
      if (rafRef.current !== null) {
        cancelAnimationFrame(rafRef.current);
      }
    };
  }, [measureFPS]);

  // Memory polling (every 3 seconds — more frequent than the logger for on-screen visibility)
  useEffect(() => {
    const updateMemory = async () => {
      try {
        const [used, total] = await Promise.all([
          DeviceInfo.getUsedMemory(),
          DeviceInfo.getTotalMemory(),
        ]);
        const usedMB = Math.round(used / (1024 * 1024));
        const percent = Math.round((used / total) * 100);
        setStats((prev) => ({ ...prev, memoryMB: usedMB, memoryPercent: percent }));
      } catch {
        // Ignore errors silently
      }
    };

    updateMemory();
    const interval = setInterval(updateMemory, 3000);
    return () => clearInterval(interval);
  }, []);

  const fpsColor = stats.fps >= 50 ? '#4CAF50' : stats.fps >= 30 ? '#FF9800' : '#F44336';
  const memColor = stats.memoryPercent < 60 ? '#4CAF50' : stats.memoryPercent < 80 ? '#FF9800' : '#F44336';

  return (
    <View style={styles.container} pointerEvents="none">
      <Text style={[styles.text, { color: fpsColor }]}>
        {stats.fps} FPS
      </Text>
      <Text style={[styles.text, { color: memColor }]}>
        {stats.memoryMB}MB ({stats.memoryPercent}%)
      </Text>
      {stats.jsFrameDrops > 0 && (
        <Text style={[styles.text, { color: '#F44336' }]}>
          {stats.jsFrameDrops} drops
        </Text>
      )}
    </View>
  );
}

const styles = StyleSheet.create({
  container: {
    position: 'absolute',
    top: Platform.isTV ? 20 : 50,
    right: 20,
    backgroundColor: 'rgba(0, 0, 0, 0.7)',
    paddingHorizontal: 10,
    paddingVertical: 6,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: 'rgba(255, 255, 255, 0.15)',
    zIndex: 99999,
    flexDirection: 'row',
    gap: 12,
  },
  text: {
    fontSize: Platform.isTV ? 16 : 11,
    fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
    fontWeight: '600',
  },
});
