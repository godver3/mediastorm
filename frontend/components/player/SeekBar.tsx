import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { useCallback, useMemo, useRef, useState } from 'react';
import { GestureResponderEvent, LayoutChangeEvent, Platform, StyleSheet, Text, View } from 'react-native';
import { tvScale } from '@/theme/tokens/tvScale';

interface SeekBarProps {
  currentTime: number;
  duration: number;
  onSeek?: (time: number) => void;
  onScrubStart?: () => void;
  onScrubEnd?: () => void;
  seekIndicatorAmount?: number;
  seekIndicatorStartTime?: number;
}

const SeekBar = ({
  currentTime,
  duration,
  onSeek,
  onScrubStart,
  onScrubEnd,
  seekIndicatorAmount = 0,
  seekIndicatorStartTime = 0,
}: SeekBarProps) => {
  const theme = useTheme();
  const [trackWidth, setTrackWidth] = useState(0);
  const [isDragging, setIsDragging] = useState(false);
  const [scrubTime, setScrubTime] = useState<number | null>(null);
  const isDraggingRef = useRef(false);
  const thumbSize = theme.spacing.lg * 1.2;
  const isTvPlatform = Platform.isTV;
  const styles = useMemo(() => useSeekBarStyles(theme, thumbSize, isTvPlatform), [theme, thumbSize, isTvPlatform]);

  const formatTime = useCallback((value: number) => {
    if (!Number.isFinite(value) || value < 0) {
      return '0:00';
    }

    const totalSeconds = Math.floor(value);
    const hours = Math.floor(totalSeconds / 3600);
    const minutes = Math.floor((totalSeconds % 3600) / 60);
    const seconds = totalSeconds % 60;

    const paddedSeconds = seconds.toString().padStart(2, '0');
    if (hours > 0) {
      const paddedMinutes = minutes.toString().padStart(2, '0');
      return `${hours}:${paddedMinutes}:${paddedSeconds}`;
    }
    return `${minutes}:${paddedSeconds}`;
  }, []);

  const clampTime = useCallback(
    (value: number) => {
      if (!Number.isFinite(value) || value < 0) {
        return 0;
      }
      if (!Number.isFinite(duration) || duration <= 0) {
        return 0;
      }
      if (value >= duration) {
        return duration;
      }
      return value;
    },
    [duration],
  );

  const computeTimeFromLocation = useCallback(
    (locationX: number) => {
      if (!duration || trackWidth <= 0) {
        return 0;
      }

      const clampedLocation = Math.min(Math.max(locationX, 0), trackWidth);
      const ratio = clampedLocation / trackWidth;
      return clampTime(duration * ratio);
    },
    [clampTime, duration, trackWidth],
  );

  const handleSeekUpdate = useCallback(
    (locationX: number, shouldCommit: boolean) => {
      const nextTime = computeTimeFromLocation(locationX);
      console.log('[SeekBar] handleSeekUpdate', { locationX, nextTime, shouldCommit, hasOnSeek: !!onSeek });
      setScrubTime(nextTime);

      if (shouldCommit && onSeek) {
        console.log('[SeekBar] calling onSeek', { nextTime });
        onSeek(nextTime);
      }
    },
    [computeTimeFromLocation, onSeek],
  );

  const handleLayout = useCallback((event: LayoutChangeEvent) => {
    setTrackWidth(event.nativeEvent.layout.width);
  }, []);

  const handleResponderGrant = useCallback(
    (event: GestureResponderEvent) => {
      console.log('[SeekBar] handleResponderGrant', { locationX: event.nativeEvent.locationX });
      isDraggingRef.current = true;
      setIsDragging(true);
      onScrubStart?.();
      handleSeekUpdate(event.nativeEvent.locationX, false);
    },
    [handleSeekUpdate, onScrubStart],
  );

  const handleResponderMove = useCallback(
    (event: GestureResponderEvent) => {
      if (!isDraggingRef.current) {
        return;
      }
      handleSeekUpdate(event.nativeEvent.locationX, false);
    },
    [handleSeekUpdate],
  );

  const handleResponderEnd = useCallback(
    (event: GestureResponderEvent) => {
      console.log('[SeekBar] handleResponderEnd', {
        isDragging: isDraggingRef.current,
        locationX: event.nativeEvent.locationX,
      });
      if (!isDraggingRef.current) {
        return;
      }
      isDraggingRef.current = false;
      setIsDragging(false);
      handleSeekUpdate(event.nativeEvent.locationX, true);
      setScrubTime(null);
      onScrubEnd?.();
    },
    [handleSeekUpdate, onScrubEnd],
  );

  // TV seeking indicator calculations - compute this first so we can use it for effectiveTime
  const isSeekingOnTV = isTvPlatform && seekIndicatorAmount !== 0;

  // When seeking on TV, show the start time position for the current thumb, not the live currentTime
  const effectiveTime =
    isDragging && scrubTime !== null ? scrubTime : isSeekingOnTV ? seekIndicatorStartTime : currentTime;
  const hasKnownDuration = Number.isFinite(duration) && duration > 0;
  const remainingTime = hasKnownDuration ? Math.max(duration - effectiveTime, 0) : 0;
  const progressRatio = duration > 0 ? Math.min(Math.max(effectiveTime / duration, 0), 1) : 0;
  const progressWidth = trackWidth * progressRatio;
  const thumbLeft = Math.min(Math.max(progressWidth - thumbSize / 2, -thumbSize / 2), trackWidth - thumbSize / 2);
  const targetTime = isSeekingOnTV
    ? Math.max(0, Math.min(seekIndicatorStartTime + seekIndicatorAmount, duration || Infinity))
    : 0;
  const targetRatio = hasKnownDuration ? Math.min(Math.max(targetTime / duration, 0), 1) : 0;
  const targetWidth = trackWidth * targetRatio;
  const targetThumbLeft = Math.min(Math.max(targetWidth - thumbSize / 2, -thumbSize / 2), trackWidth - thumbSize / 2);

  const getThumbPosition = () => {
    if (!Number.isFinite(duration) || duration <= 0) {
      return -thumbSize / 2;
    }

    return thumbLeft;
  };

  return (
    <View style={styles.seekbarContainer}>
      {/* TV Seeking indicator text */}
      {isSeekingOnTV && (
        <View style={styles.seekIndicatorTextContainer}>
          <Text style={styles.seekIndicatorText}>
            {seekIndicatorAmount > 0 ? '+' : ''}
            {seekIndicatorAmount}s
          </Text>
        </View>
      )}
      <View
        style={styles.seekbarInteractiveArea}
        onLayout={handleLayout}
        onStartShouldSetResponder={() => true}
        onMoveShouldSetResponder={() => true}
        onResponderGrant={handleResponderGrant}
        onResponderMove={handleResponderMove}
        onResponderRelease={handleResponderEnd}
        onResponderTerminate={handleResponderEnd}>
        <View style={styles.seekbarTrack}>
          <View style={[styles.seekbarProgress, { width: progressWidth }]} />
        </View>
        <View style={[styles.seekbarThumb, { left: getThumbPosition() }]} />
        {/* Target position thumb for TV seeking */}
        {isSeekingOnTV && <View style={[styles.seekbarTargetThumb, { left: targetThumbLeft }]} />}
      </View>
      <View style={styles.timeContainer}>
        {isSeekingOnTV ? (
          <>
            <View style={styles.seekTimeDisplay}>
              <Text style={styles.timeText}>{formatTime(seekIndicatorStartTime)}</Text>
              <Text style={styles.seekArrow}>→</Text>
              <Text style={styles.targetTimeText}>{formatTime(targetTime)}</Text>
            </View>
            <Text style={styles.timeText}>{hasKnownDuration ? formatTime(duration) : ''}</Text>
          </>
        ) : (
          <>
            <Text style={styles.timeText}>{formatTime(effectiveTime)}</Text>
            <Text style={styles.timeText}>
              {hasKnownDuration ? `-${formatTime(remainingTime)}` : formatTime(duration)}
            </Text>
          </>
        )}
      </View>
    </View>
  );
};

const useSeekBarStyles = (theme: NovaTheme, thumbSize: number, isTvPlatform: boolean) => {
  return StyleSheet.create({
    seekbarContainer: {
      flex: 1,
      justifyContent: 'center',
    },
    seekbarInteractiveArea: {
      height: theme.spacing['2xl'],
      justifyContent: 'center',
      width: '100%',
    },
    seekbarTrack: {
      height: 6,
      backgroundColor: theme.colors.border.subtle,
      borderRadius: 3,
      overflow: 'hidden',
      width: '100%',
      pointerEvents: 'none',
    },
    seekbarProgress: {
      height: '100%',
      backgroundColor: theme.colors.accent.primary,
      borderRadius: 3,
      pointerEvents: 'none',
    },
    seekbarThumb: {
      position: 'absolute',
      width: thumbSize,
      height: thumbSize,
      borderRadius: thumbSize / 2,
      backgroundColor: theme.colors.accent.primary,
      top: (theme.spacing['2xl'] - thumbSize) / 2,
      pointerEvents: 'none',
    },
    timeContainer: {
      marginTop: theme.spacing.xs,
      flexDirection: 'row',
      justifyContent: 'space-between',
      paddingHorizontal: theme.spacing.xs,
    },
    timeText: {
      ...(isTvPlatform ? theme.typography.body.lg : theme.typography.body.sm),
      color: theme.colors.text.primary,
    },
    seekIndicatorTextContainer: {
      alignItems: 'center',
      marginBottom: theme.spacing.sm,
    },
    seekIndicatorText: {
      fontSize: isTvPlatform ? tvScale(36, 24) : 24,
      fontWeight: '700',
      color: theme.colors.text.primary,
      letterSpacing: 1,
    },
    seekbarTargetThumb: {
      position: 'absolute',
      width: thumbSize,
      height: thumbSize,
      borderRadius: thumbSize / 2,
      backgroundColor: '#4CAF50',
      borderWidth: 2,
      borderColor: '#ffffff',
      top: (theme.spacing['2xl'] - thumbSize) / 2,
      pointerEvents: 'none',
      zIndex: 2,
    },
    seekTimeDisplay: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.xs,
    },
    seekArrow: {
      ...(isTvPlatform ? theme.typography.body.lg : theme.typography.body.sm),
      color: 'rgba(255, 255, 255, 0.6)',
    },
    targetTimeText: {
      ...(isTvPlatform ? theme.typography.body.lg : theme.typography.body.sm),
      color: '#4CAF50',
      fontWeight: '700',
    },
  });
};

export default SeekBar;
