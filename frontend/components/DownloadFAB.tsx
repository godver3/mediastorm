/**
 * Floating download progress indicator.
 *
 * 56px circular button at bottom-right (above tab bar) showing:
 *  - SVG circular progress ring (aggregate across active downloads)
 *  - Download arrow icon + active count badge
 *  - Tapping navigates to /(drawer)/downloads
 *
 * Only renders on mobile when there are active (downloading/pending) downloads.
 */

import React, { useMemo } from 'react';
import { Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { useRouter } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import Svg, { Circle } from 'react-native-svg';
import { useTheme } from '@/theme';
import { useDownloads } from './DownloadsContext';

const SIZE = 56;
const STROKE = 4;
const RADIUS = (SIZE - STROKE) / 2;
const CIRCUMFERENCE = 2 * Math.PI * RADIUS;

const isMobile = (Platform.OS === 'ios' || Platform.OS === 'android') && !Platform.isTV;

export const DownloadFAB: React.FC = () => {
  const { items } = useDownloads();
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const router = useRouter();

  const activeItems = useMemo(
    () => items.filter((i) => i.status === 'downloading' || i.status === 'pending'),
    [items],
  );

  // Aggregate progress across all active downloads
  const aggregateProgress = useMemo(() => {
    if (activeItems.length === 0) return 0;
    let totalBytes = 0;
    let writtenBytes = 0;
    for (const item of activeItems) {
      totalBytes += item.fileSize > 0 ? item.fileSize : 0;
      writtenBytes += item.bytesWritten;
    }
    return totalBytes > 0 ? writtenBytes / totalBytes : 0;
  }, [activeItems]);

  if (!isMobile || activeItems.length === 0) return null;

  const strokeDashoffset = CIRCUMFERENCE * (1 - aggregateProgress);

  // Position above the tab bar: tab bar height (~56) + bottom inset + some margin
  const bottomOffset = 56 + Math.max(insets.bottom, 12) + 16;

  return (
    <Pressable
      onPress={() => router.push('/(drawer)/downloads')}
      style={[
        styles.container,
        {
          bottom: bottomOffset,
          right: theme.spacing.lg,
          backgroundColor: theme.colors.background.elevated,
          shadowColor: '#000',
        },
      ]}
      accessibilityRole="button"
      accessibilityLabel={`${activeItems.length} active download${activeItems.length !== 1 ? 's' : ''}`}>
      {/* Progress ring */}
      <Svg width={SIZE} height={SIZE} style={styles.svg}>
        {/* Background track */}
        <Circle
          cx={SIZE / 2}
          cy={SIZE / 2}
          r={RADIUS}
          stroke={theme.colors.border.subtle}
          strokeWidth={STROKE}
          fill="none"
        />
        {/* Progress arc */}
        <Circle
          cx={SIZE / 2}
          cy={SIZE / 2}
          r={RADIUS}
          stroke={theme.colors.accent.primary}
          strokeWidth={STROKE}
          fill="none"
          strokeLinecap="round"
          strokeDasharray={`${CIRCUMFERENCE}`}
          strokeDashoffset={strokeDashoffset}
          rotation="-90"
          origin={`${SIZE / 2}, ${SIZE / 2}`}
        />
      </Svg>

      {/* Center content: arrow + count */}
      <View style={styles.center}>
        <Text style={[styles.arrow, { color: theme.colors.accent.primary }]}>
          {'\u2193'}
        </Text>
        {activeItems.length > 1 && (
          <View style={[styles.badge, { backgroundColor: theme.colors.accent.primary }]}>
            <Text style={styles.badgeText}>{activeItems.length}</Text>
          </View>
        )}
      </View>
    </Pressable>
  );
};

const styles = StyleSheet.create({
  container: {
    position: 'absolute',
    width: SIZE,
    height: SIZE,
    borderRadius: SIZE / 2,
    justifyContent: 'center',
    alignItems: 'center',
    elevation: 6,
    shadowOffset: { width: 0, height: 3 },
    shadowOpacity: 0.3,
    shadowRadius: 4,
    zIndex: 100,
  },
  svg: {
    position: 'absolute',
  },
  center: {
    justifyContent: 'center',
    alignItems: 'center',
  },
  arrow: {
    fontSize: 22,
    fontWeight: '700',
    lineHeight: 24,
  },
  badge: {
    position: 'absolute',
    top: -6,
    right: -10,
    minWidth: 16,
    height: 16,
    borderRadius: 8,
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 3,
  },
  badgeText: {
    color: '#fff',
    fontSize: 10,
    fontWeight: '700',
  },
});
