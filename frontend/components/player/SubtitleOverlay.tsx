/**
 * SubtitleOverlay - Renders VTT subtitles as an overlay on the video player
 * Used for fMP4/HDR content where iOS AVPlayer doesn't expose muxed subtitles to react-native-video
 */
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { LayoutChangeEvent, Platform, StyleSheet, Text, View } from 'react-native';

export interface VTTCue {
  startTime: number; // seconds
  endTime: number; // seconds
  text: string;
}

interface SubtitleOverlayProps {
  /** URL to fetch the VTT file from */
  vttUrl: string | null;
  /** Current playback time in seconds */
  currentTime: number;
  /** Whether subtitles are enabled */
  enabled: boolean;
  /** Offset to add to subtitle times (for seek/warm start) */
  timeOffset?: number;
  /** Video natural width (for positioning subtitles at video content boundary) */
  videoWidth?: number;
  /** Video natural height (for positioning subtitles at video content boundary) */
  videoHeight?: number;
  /** Size scale factor for subtitles (1.0 = default) */
  sizeScale?: number;
  /** Whether player controls are visible (subtitles bump up to avoid overlap) */
  controlsVisible?: boolean;
}

/**
 * Parse VTT timestamp to seconds
 * Formats: "00:00:00.000" or "00:00.000"
 */
function parseVTTTimestamp(timestamp: string): number {
  const parts = timestamp.trim().split(':');
  if (parts.length === 3) {
    // HH:MM:SS.mmm
    const hours = parseInt(parts[0], 10);
    const minutes = parseInt(parts[1], 10);
    const seconds = parseFloat(parts[2]);
    return hours * 3600 + minutes * 60 + seconds;
  } else if (parts.length === 2) {
    // MM:SS.mmm
    const minutes = parseInt(parts[0], 10);
    const seconds = parseFloat(parts[1]);
    return minutes * 60 + seconds;
  }
  return 0;
}

/**
 * Parse VTT file content into an array of cues
 */
function parseVTT(content: string): VTTCue[] {
  const cues: VTTCue[] = [];
  const lines = content.split('\n');

  let i = 0;
  // Skip WEBVTT header and any metadata
  while (i < lines.length && !lines[i].includes('-->')) {
    i++;
  }

  while (i < lines.length) {
    const line = lines[i].trim();

    // Look for timestamp line (contains "-->")
    if (line.includes('-->')) {
      const [startStr, endStr] = line.split('-->').map((s) => s.trim().split(' ')[0]);
      const startTime = parseVTTTimestamp(startStr);
      const endTime = parseVTTTimestamp(endStr);

      // Collect text lines until empty line or next cue
      const textLines: string[] = [];
      i++;
      while (i < lines.length && lines[i].trim() !== '' && !lines[i].includes('-->')) {
        // Skip cue identifiers (numeric lines before timestamps)
        const trimmed = lines[i].trim();
        if (!/^\d+$/.test(trimmed)) {
          // Strip VTT tags like <c.color>, </c>, <i>, </i>, etc.
          const cleanedText = trimmed
            .replace(/<[^>]+>/g, '') // Remove all HTML-like tags
            .replace(/&lt;/g, '<')
            .replace(/&gt;/g, '>')
            .replace(/&amp;/g, '&')
            .replace(/&nbsp;/g, ' ');
          if (cleanedText) {
            textLines.push(cleanedText);
          }
        }
        i++;
      }

      if (textLines.length > 0) {
        cues.push({
          startTime,
          endTime,
          text: textLines.join('\n'),
        });
      }
    } else {
      i++;
    }
  }

  return cues;
}

/**
 * Find active cues for the current time using binary search
 */
function findActiveCues(cues: VTTCue[], currentTime: number): VTTCue[] {
  if (cues.length === 0) return [];

  // Find cues that overlap with currentTime
  const active: VTTCue[] = [];
  for (const cue of cues) {
    if (currentTime >= cue.startTime && currentTime < cue.endTime) {
      active.push(cue);
    }
    // Early exit if we've passed all possible active cues
    if (cue.startTime > currentTime) {
      break;
    }
  }
  return active;
}

// Platform detection for styling - defined before component for use in useMemo
const isAndroidTV = Platform.isTV && Platform.OS === 'android';

const SubtitleOverlay: React.FC<SubtitleOverlayProps> = ({
  vttUrl,
  currentTime,
  enabled,
  timeOffset = 0,
  videoWidth,
  videoHeight,
  sizeScale = 1.0,
  controlsVisible = false,
}) => {
  // Use container dimensions instead of screen dimensions for accurate positioning
  // Screen dimensions include safe areas which may not be part of our container
  const [containerSize, setContainerSize] = useState<{ width: number; height: number } | null>(null);
  const [cues, setCues] = useState<VTTCue[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [syncTick, setSyncTick] = useState(0);
  const lastFetchedLengthRef = useRef<number>(0);
  const fetchIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const syncIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const lastUrlRef = useRef<string | null>(null);
  const lastSyncTimeRef = useRef<number>(currentTime);

  // Calculate subtitle positioning based on actual video content bounds
  // When using resizeMode="contain", the video may have letterboxing (black bars)
  // We need to position subtitles at the bottom of the actual video, not the screen
  const subtitleBottomOffset = useMemo(() => {
    // Small padding from video content edge (not screen edge)
    // This just provides a slight margin above the video's bottom border
    // Android TV: 40% lower positioning (25% + 20%)
    const basePadding = isAndroidTV ? 12 : Platform.isTV ? 20 : 10;

    // If we don't have video or container dimensions, use default positioning
    if (!videoWidth || !videoHeight || !containerSize) {
      return basePadding;
    }

    const { width: containerWidth, height: containerHeight } = containerSize;
    const isLandscape = containerWidth > containerHeight;

    // Extra offset when controls are visible to avoid overlap with control bar (landscape only)
    // Control bar heights: ~120px mobile, ~180px tvOS, ~72px Android TV (60% less shift)
    const tvControlsOffset = isAndroidTV ? 72 : 180;
    const controlsOffset = controlsVisible && isLandscape ? (Platform.isTV ? tvControlsOffset : 120) : 0;

    const videoAspectRatio = videoWidth / videoHeight;
    const containerAspectRatio = containerWidth / containerHeight;

    // Video is wider than container: letterboxing on top/bottom
    // Video height will be less than container height
    if (videoAspectRatio > containerAspectRatio) {
      // Video fills container width, calculate actual video height
      const actualVideoHeight = containerWidth / videoAspectRatio;
      const letterboxHeight = (containerHeight - actualVideoHeight) / 2;
      return letterboxHeight + basePadding + controlsOffset;
    }

    // Video is taller than container: letterboxing on left/right
    // Video fills height, no bottom offset needed beyond base padding
    // Add extra safe area padding in landscape mode (Android TV: 40% less)
    const landscapeExtra = isLandscape ? (isAndroidTV ? 12 : 20) : 0;
    return basePadding + landscapeExtra + controlsOffset;
  }, [videoWidth, videoHeight, containerSize, controlsVisible]);

  // Fetch and parse VTT file
  const fetchVTT = useCallback(async () => {
    if (!vttUrl || !enabled) return;

    try {
      const response = await fetch(vttUrl, {
        cache: 'no-store', // Don't cache since file is growing
      });

      if (!response.ok) {
        throw new Error(`Failed to fetch VTT: ${response.status}`);
      }

      const content = await response.text();

      // Only re-parse if content has grown
      if (content.length > lastFetchedLengthRef.current) {
        lastFetchedLengthRef.current = content.length;
        const parsedCues = parseVTT(content);
        setCues(parsedCues);
        setError(null);
      }
    } catch (err) {
      console.warn('[SubtitleOverlay] Failed to fetch VTT:', err);
      // Don't set error state for network errors - file might not be ready yet
    }
  }, [vttUrl, enabled]);

  // Reset state when URL changes
  useEffect(() => {
    if (vttUrl !== lastUrlRef.current) {
      lastUrlRef.current = vttUrl;
      lastFetchedLengthRef.current = 0;
      setCues([]);
      setError(null);
    }
  }, [vttUrl]);

  // Set up polling to fetch VTT updates
  useEffect(() => {
    if (!enabled || !vttUrl) {
      if (fetchIntervalRef.current) {
        clearInterval(fetchIntervalRef.current);
        fetchIntervalRef.current = null;
      }
      return;
    }

    // Initial fetch
    fetchVTT();

    // Poll every 5 seconds for new cues (file grows as transcoding progresses)
    fetchIntervalRef.current = setInterval(fetchVTT, 5000);

    return () => {
      if (fetchIntervalRef.current) {
        clearInterval(fetchIntervalRef.current);
        fetchIntervalRef.current = null;
      }
    };
  }, [vttUrl, enabled, fetchVTT]);

  // Keep refs updated for use in sync interval
  const currentTimeRef = useRef(currentTime);
  const timeOffsetRef = useRef(timeOffset);
  useEffect(() => {
    currentTimeRef.current = currentTime;
  }, [currentTime]);
  useEffect(() => {
    timeOffsetRef.current = timeOffset;
  }, [timeOffset]);

  // Periodic sync to detect and correct drift
  // This helps keep subtitles in sync especially after seeking
  const hasInitializedSyncRef = useRef(false);
  useEffect(() => {
    if (!enabled || !vttUrl) {
      if (syncIntervalRef.current) {
        clearInterval(syncIntervalRef.current);
        syncIntervalRef.current = null;
      }
      // Reset initialization flag when disabled
      hasInitializedSyncRef.current = false;
      return;
    }

    // Check for drift every 3 seconds
    syncIntervalRef.current = setInterval(() => {
      const now = currentTimeRef.current;

      // Skip drift detection on first tick - just establish baseline
      // This prevents false positives when resuming playback at a non-zero position
      if (!hasInitializedSyncRef.current) {
        hasInitializedSyncRef.current = true;
        lastSyncTimeRef.current = now;
        return;
      }

      const timeDelta = Math.abs(now - lastSyncTimeRef.current);

      // If time drifted more than expected (allowing ~0.5s for normal 3s interval variance),
      // trigger a re-sync. This catches buffering stalls and frame drops, not just seeks.
      // Expected delta is ~3s (our interval), so anything outside 2.5-3.5s range indicates drift.
      const expectedDelta = 3; // seconds (matches our interval)
      const driftTolerance = 0.5;
      const hasDrift = timeDelta < expectedDelta - driftTolerance || timeDelta > expectedDelta + driftTolerance;
      if (hasDrift) {
        setSyncTick((prev) => prev + 1);
        // Also re-fetch VTT in case new cues are available
        fetchVTT();
      }

      lastSyncTimeRef.current = now;
    }, 3000);

    return () => {
      if (syncIntervalRef.current) {
        clearInterval(syncIntervalRef.current);
        syncIntervalRef.current = null;
      }
    };
  }, [vttUrl, enabled, fetchVTT]);

  // Find active cues for current time
  // syncTick is included to force re-evaluation on drift detection
  // SUBTITLE_DELAY_SECONDS: positive = subtitles appear later (fixes ahead-of-audio)
  const SUBTITLE_DELAY_SECONDS = 0;
  const activeCues = useMemo(() => {
    if (!enabled || cues.length === 0) return [];
    const adjustedTime = currentTime + timeOffset - SUBTITLE_DELAY_SECONDS;
    return findActiveCues(cues, adjustedTime);
  }, [cues, currentTime, timeOffset, enabled, syncTick]);

  // Render subtitle text with outline effect by layering
  // Multiple offset black text layers create the outline, white text on top
  const outlineOffsets = [
    { x: -1, y: -1 },
    { x: 1, y: -1 },
    { x: -1, y: 1 },
    { x: 1, y: 1 },
    { x: 0, y: -1 },
    { x: 0, y: 1 },
    { x: -1, y: 0 },
    { x: 1, y: 0 },
  ];

  const handleLayout = useCallback((event: LayoutChangeEvent) => {
    const { width, height } = event.nativeEvent.layout;
    setContainerSize({ width, height });
  }, []);

  const shouldShowSubtitles = enabled && activeCues.length > 0;

  // Calculate scaled text styles based on sizeScale prop
  const scaledTextStyles = useMemo(() => {
    // Base font sizes per platform (these are the "1.0" scale values)
    const baseFontSize = isAndroidTV ? 26 : Platform.isTV ? 62 : 24;
    const baseLineHeight = isAndroidTV ? 36 : Platform.isTV ? 86 : 34;

    // Apply scale factor
    const scaledFontSize = Math.round(baseFontSize * sizeScale);
    const scaledLineHeight = Math.round(baseLineHeight * sizeScale);

    return {
      fontSize: scaledFontSize,
      lineHeight: scaledLineHeight,
    };
  }, [sizeScale]);

  // Always render container to capture dimensions via onLayout
  // Only render subtitle content when enabled and we have active cues
  return (
    <View style={styles.container} pointerEvents="none" onLayout={handleLayout}>
      {shouldShowSubtitles && (
        <View style={[styles.subtitlePositioner, { bottom: subtitleBottomOffset }]}>
          {activeCues.map((cue, index) => (
            <View key={`${cue.startTime}-${index}`} style={styles.cueContainer}>
              {/* Black outline layers */}
              {outlineOffsets.map((offset, i) => (
                <Text
                  key={`outline-${i}`}
                  style={[
                    styles.subtitleTextOutline,
                    scaledTextStyles,
                    { transform: [{ translateX: offset.x }, { translateY: offset.y }] },
                  ]}>
                  {cue.text}
                </Text>
              ))}
              {/* White text on top */}
              <Text style={[styles.subtitleText, scaledTextStyles]}>{cue.text}</Text>
            </View>
          ))}
        </View>
      )}
    </View>
  );
};

// Styling to match VLC's default subtitle appearance:
// - White text with black outline (no background box)
// - VLC uses freetype renderer with outline for visibility
// - tvOS: --sub-text-scale=60, --freetype-rel-fontsize=10
// - Android TV: half size of tvOS for better readability
// Note: isAndroidTV is defined above the component for use in useMemo
const styles = StyleSheet.create({
  container: {
    position: 'absolute',
    left: 0,
    right: 0,
    top: 0,
    bottom: 0,
  },
  subtitlePositioner: {
    position: 'absolute',
    left: 0,
    right: 0,
    // bottom is set dynamically based on video content bounds
    alignItems: 'center',
    paddingHorizontal: Platform.isTV ? 60 : 20,
  },
  cueContainer: {
    // Container for layered text (outline + foreground)
    // Subtitles grow upward from bottom (anchor at bottom line)
    position: 'relative',
    alignItems: 'center',
    justifyContent: 'flex-end',
  },
  subtitleText: {
    color: '#FFFFFF',
    // Font sizes to match VLC's scaled appearance
    // tvOS: VLC uses scale=60 of default, roughly 24-26pt
    // Android TV: half of tvOS size
    // iOS mobile: reduced 30% for better fit
    fontSize: isAndroidTV ? 26 : Platform.isTV ? 62 : 24,
    fontWeight: '600',
    textAlign: 'center',
    lineHeight: isAndroidTV ? 36 : Platform.isTV ? 86 : 34,
    // VLC-style black outline effect
    // React Native only supports single shadow, so we use a tight radius
    // to approximate the outline effect VLC uses with freetype
    textShadowColor: '#000000',
    textShadowOffset: isAndroidTV
      ? { width: 1, height: 1 }
      : Platform.isTV
        ? { width: 2, height: 2 }
        : { width: 1, height: 1 },
    textShadowRadius: isAndroidTV ? 2 : Platform.isTV ? 4 : 1.5,
    // Additional padding for multi-line subtitles
    paddingVertical: 2,
  },
  // For a more authentic VLC outline, we layer the text
  // This is handled in the component by rendering shadow layers
  subtitleTextOutline: {
    position: 'absolute',
    color: '#000000',
    fontSize: isAndroidTV ? 26 : Platform.isTV ? 62 : 24,
    fontWeight: '600',
    textAlign: 'center',
    lineHeight: isAndroidTV ? 36 : Platform.isTV ? 86 : 34,
    paddingVertical: 2,
  },
});

export default SubtitleOverlay;
