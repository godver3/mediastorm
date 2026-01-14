/**
 * SubtitleOverlay - Renders VTT subtitles as an overlay on the video player
 * Used for fMP4/HDR content where iOS AVPlayer doesn't expose muxed subtitles to react-native-video
 */
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { LayoutChangeEvent, Platform, StyleSheet, Text, View } from 'react-native';
import { getTVScaleMultiplier, ANDROID_TV_TO_TVOS_RATIO } from '@/theme/tokens/tvScale';

/** A segment of styled text within a subtitle cue */
export interface StyledTextSegment {
  text: string;
  italic: boolean;
}

export interface VTTCue {
  startTime: number; // seconds
  endTime: number; // seconds
  text: string; // Plain text (for compatibility)
  segments: StyledTextSegment[]; // Styled segments for rendering
}

/** Time range of available subtitle cues */
export interface SubtitleCuesRange {
  minTime: number;
  maxTime: number;
}

/** Debug info for subtitle sync troubleshooting */
export interface SubtitleDebugInfo {
  adjustedTime: number;
  activeCueStart: number | null;
  activeCueEnd: number | null;
  activeCueText: string | null;
  totalCues: number;
  firstCueStart: number | null;
}

interface SubtitleOverlayProps {
  /** URL to fetch the VTT file from */
  vttUrl: string | null;
  /** Current playback time in seconds (fallback if currentTimeRef not provided) */
  currentTime: number;
  /** Whether subtitles are enabled */
  enabled: boolean;
  /** Offset to add to subtitle times (for seek/warm start) */
  timeOffset?: number;
  /** Size scale factor for subtitles (1.0 = default) */
  sizeScale?: number;
  /** Whether player controls are visible (subtitles bump up to avoid overlap) */
  controlsVisible?: boolean;
  /**
   * Ref to current playback time - enables high-frequency updates via requestAnimationFrame
   * When provided, this is used instead of the currentTime prop for smoother subtitle sync
   */
  currentTimeRef?: React.MutableRefObject<number>;
  /** Video natural width (used for portrait mode positioning) */
  videoWidth?: number;
  /** Video natural height (used for portrait mode positioning) */
  videoHeight?: number;
  /** Callback when the available cue time range changes (for seek detection) */
  onCuesRangeChange?: (range: SubtitleCuesRange | null) => void;
  /** Whether content is HDR/Dolby Vision - uses grey text for better visibility */
  isHDRContent?: boolean;
  /** Callback for debug info (adjusted time, active cue, etc) */
  onDebugInfo?: (info: SubtitleDebugInfo) => void;
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
 * Parse text with <i> tags into styled segments
 * Handles nested tags and converts to flat segment array
 */
function parseStyledText(text: string): StyledTextSegment[] {
  const segments: StyledTextSegment[] = [];

  // Decode HTML entities first
  const decoded = text
    .replace(/&lt;/g, '<')
    .replace(/&gt;/g, '>')
    .replace(/&amp;/g, '&')
    .replace(/&nbsp;/g, ' ');

  // Match <i>...</i> tags and non-italic text between them
  // Use a state machine approach to handle the text
  let remaining = decoded;
  let inItalic = false;

  while (remaining.length > 0) {
    if (inItalic) {
      // Look for closing </i> tag
      const closeMatch = remaining.match(/^([\s\S]*?)<\/i>/i);
      if (closeMatch) {
        if (closeMatch[1]) {
          segments.push({ text: closeMatch[1], italic: true });
        }
        remaining = remaining.slice(closeMatch[0].length);
        inItalic = false;
      } else {
        // No closing tag found, treat rest as italic
        segments.push({ text: remaining, italic: true });
        break;
      }
    } else {
      // Look for opening <i> tag
      const openMatch = remaining.match(/^([\s\S]*?)<i>/i);
      if (openMatch) {
        if (openMatch[1]) {
          // Strip any other HTML tags from non-italic text
          const cleaned = openMatch[1].replace(/<[^>]+>/g, '');
          if (cleaned) {
            segments.push({ text: cleaned, italic: false });
          }
        }
        remaining = remaining.slice(openMatch[0].length);
        inItalic = true;
      } else {
        // No more <i> tags, add rest as non-italic (strip other tags)
        const cleaned = remaining.replace(/<[^>]+>/g, '');
        if (cleaned) {
          segments.push({ text: cleaned, italic: false });
        }
        break;
      }
    }
  }

  // If no segments were created (e.g., empty string), return empty array
  // If text had no tags at all, we should have one non-italic segment
  return segments;
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
      const rawLines: string[] = []; // Keep raw lines with tags for styled parsing
      i++;
      while (i < lines.length && lines[i].trim() !== '' && !lines[i].includes('-->')) {
        // Skip cue identifiers (numeric lines before timestamps)
        const trimmed = lines[i].trim();
        if (!/^\d+$/.test(trimmed)) {
          rawLines.push(trimmed);
          // Also create plain text version (strip all tags) for compatibility
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

      if (textLines.length > 0 || rawLines.length > 0) {
        const rawText = rawLines.join('\n');
        const segments = parseStyledText(rawText);
        cues.push({
          startTime,
          endTime,
          text: textLines.join('\n'),
          segments: segments.length > 0 ? segments : [{ text: textLines.join('\n'), italic: false }],
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
  sizeScale = 1.0,
  controlsVisible = false,
  currentTimeRef: externalTimeRef,
  videoWidth,
  videoHeight,
  onCuesRangeChange,
  isHDRContent = false,
  onDebugInfo,
}) => {
  // Use container dimensions instead of screen dimensions for accurate positioning
  // Screen dimensions include safe areas which may not be part of our container
  const [containerSize, setContainerSize] = useState<{ width: number; height: number } | null>(null);
  const [cues, setCues] = useState<VTTCue[]>([]);
  const [error, setError] = useState<string | null>(null);
  const lastFetchedLengthRef = useRef<number>(0);
  const fetchIntervalRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const lastUrlRef = useRef<string | null>(null);

  // High-frequency time polling via requestAnimationFrame
  // When externalTimeRef is provided, poll it frequently for smoother subtitle sync
  const [polledTime, setPolledTime] = useState(currentTime);
  const rafIdRef = useRef<number | null>(null);
  const lastPolledTimeRef = useRef<number>(currentTime);

  useEffect(() => {
    // If no external ref, just use the prop directly
    if (!externalTimeRef) {
      setPolledTime(currentTime);
      return;
    }

    if (!enabled) {
      if (rafIdRef.current !== null) {
        cancelAnimationFrame(rafIdRef.current);
        rafIdRef.current = null;
      }
      return;
    }

    // Poll the external ref at ~30fps for smooth subtitle updates
    // Only trigger re-render if time changed enough to potentially affect cue display
    const pollTime = () => {
      const newTime = externalTimeRef.current;
      // Only update state if time changed by more than 50ms to reduce renders
      if (Math.abs(newTime - lastPolledTimeRef.current) > 0.05) {
        lastPolledTimeRef.current = newTime;
        setPolledTime(newTime);
      }
      rafIdRef.current = requestAnimationFrame(pollTime);
    };

    rafIdRef.current = requestAnimationFrame(pollTime);

    return () => {
      if (rafIdRef.current !== null) {
        cancelAnimationFrame(rafIdRef.current);
        rafIdRef.current = null;
      }
    };
  }, [externalTimeRef, enabled, currentTime]);

  // Effective time to use for subtitle matching
  const effectiveTime = externalTimeRef ? polledTime : currentTime;

  // Calculate subtitle positioning based on orientation:
  // - Landscape: bottom of screen (in letterbox bars for widescreen content)
  // - Portrait: bottom of video content (above any letterbox bars)
  const subtitleBottomOffset = useMemo(() => {
    const basePadding = isAndroidTV ? 12 : Platform.isTV ? 20 : 10;

    if (!containerSize) {
      return basePadding;
    }

    const { width: containerWidth, height: containerHeight } = containerSize;
    const isLandscape = containerWidth > containerHeight;

    // Calculate control bar height based on actual component dimensions
    // This mirrors the styling in Controls.tsx and FocusablePressable.tsx
    let controlsOffset = 0;
    if (controlsVisible && isLandscape) {
      if (Platform.isTV) {
        // TV control bar calculation:
        // - Theme spacing uses legacy scale factors: tvOS 0.85, Android TV 0.5
        // - FocusablePressable uses: scale = (android ? 1.71875 : 1.375) * getTVScaleMultiplier()
        const themeScaleFactor = isAndroidTV ? 0.5 : 0.85;
        const buttonScale = (isAndroidTV ? 1.71875 : 1.375) * getTVScaleMultiplier();

        // Base spacing values (before theme scaling)
        const baseSpacingSm = 8;
        const baseSpacingMd = 12;
        const baseSpacingLg = 16;

        // Scaled spacing (as theme would provide)
        const spacingSm = baseSpacingSm * themeScaleFactor;
        const spacingMd = baseSpacingMd * themeScaleFactor;
        const spacingLg = baseSpacingLg * themeScaleFactor;

        // Button dimensions (icon button in FocusablePressable)
        // Icon size: tvScale(24 * 1.375, 24) - designed for tvOS, auto-scaled for Android TV
        const tvosIconSize = 24 * 1.375; // 33
        const iconSize = isAndroidTV ? Math.round(tvosIconSize * ANDROID_TV_TO_TVOS_RATIO) : tvosIconSize;
        const buttonPaddingVertical = spacingSm * buttonScale;
        const buttonHeight = iconSize + buttonPaddingVertical * 2;

        // Control bar: container padding + main row + secondary row + bottom offset
        const containerPadding = spacingMd * 2;
        const secondaryRowMargin = spacingSm;
        const bottomOffset = spacingLg;

        // Total: bottom offset + container padding + two rows of buttons + secondary row margin + extra padding
        const extraPadding = isAndroidTV ? 8 : 16; // Buffer between subtitle and controls
        controlsOffset = bottomOffset + containerPadding + buttonHeight * 2 + secondaryRowMargin + extraPadding;
      } else {
        // Mobile landscape: single row with track selection + seek bar
        // bottomControlsMobile: paddingVertical = 8 (theme.spacing.sm)
        // bottomControlsMobileLandscape: bottom = 4 (theme.spacing.xs)
        // Content height includes SeekBar (~40px with touch targets) + track buttons
        const containerPadding = 8 * 2; // theme.spacing.sm top + bottom
        const bottomOffset = 4; // theme.spacing.xs
        const contentHeight = 48; // seek bar with touch targets and track buttons
        const extraPadding = 12; // buffer between subtitle and controls
        controlsOffset = bottomOffset + containerPadding + contentHeight + extraPadding;
      }
    }

    // Landscape: position at screen bottom (subtitles in letterbox bars)
    if (isLandscape) {
      return basePadding + controlsOffset;
    }

    // Portrait: position at video content bottom (above letterbox bars)
    if (!videoWidth || !videoHeight) {
      return basePadding;
    }

    const videoAspectRatio = videoWidth / videoHeight;
    const containerAspectRatio = containerWidth / containerHeight;

    // Video is wider than container: letterboxing on top/bottom
    if (videoAspectRatio > containerAspectRatio) {
      const actualVideoHeight = containerWidth / videoAspectRatio;
      const letterboxHeight = (containerHeight - actualVideoHeight) / 2;
      return letterboxHeight + basePadding;
    }

    // Video fills height or is taller - no bottom letterbox
    return basePadding;
  }, [videoWidth, videoHeight, containerSize, controlsVisible]);

  // Fetch and parse VTT file
  const fetchVTT = useCallback(async () => {
    if (!vttUrl || !enabled) return;

    console.log('[SubtitleOverlay] fetching VTT:', vttUrl);

    try {
      const response = await fetch(vttUrl, {
        cache: 'no-store', // Don't cache since file is growing
      });

      if (!response.ok) {
        throw new Error(`Failed to fetch VTT: ${response.status}`);
      }

      const content = await response.text();
      const prevLength = lastFetchedLengthRef.current;
      const contentLength = content.length;

      // Detect edge cases
      const isHeaderOnly = content.trim() === 'WEBVTT' || content.trim() === 'WEBVTT\n';
      const hasTimestamps = content.includes('-->');

      // Log edge cases that might cause missing subtitles
      if (isHeaderOnly) {
        console.log(`[SubtitleOverlay] VTT is header-only (${contentLength} bytes) - extraction not started or no cues yet`);
      } else if (!hasTimestamps && contentLength > 10) {
        console.log(`[SubtitleOverlay] VTT has content (${contentLength} bytes) but NO timestamps - possibly truncated/corrupted`);
        console.log(`[SubtitleOverlay]   First 200 chars: ${content.substring(0, 200).replace(/\n/g, '\\n')}`);
      }

      // Check if content shrunk (unexpected)
      if (contentLength < prevLength && prevLength > 0) {
        console.log(`[SubtitleOverlay] WARNING: VTT content SHRUNK from ${prevLength} to ${contentLength} bytes!`);
      }

      // Only re-parse if content has grown
      if (contentLength > prevLength) {
        lastFetchedLengthRef.current = contentLength;
        const parsedCues = parseVTT(content);
        setCues(parsedCues);
        setError(null);

        // Log VTT fetch details for debugging
        const firstCue = parsedCues.length > 0 ? parsedCues[0] : null;
        const lastCue = parsedCues.length > 0 ? parsedCues[parsedCues.length - 1] : null;
        console.log(
          `[SubtitleOverlay] VTT updated: ${prevLength} -> ${contentLength} bytes, ` +
            `${parsedCues.length} cues, range: ${firstCue?.startTime.toFixed(2) ?? 'N/A'}-${lastCue?.endTime.toFixed(2) ?? 'N/A'}s`
        );

        // Warn if we have content but parsing returned 0 cues
        if (parsedCues.length === 0 && hasTimestamps) {
          console.log(`[SubtitleOverlay] WARNING: VTT has timestamps but parsed 0 cues - parse failure?`);
          console.log(`[SubtitleOverlay]   Content preview: ${content.substring(0, 500).replace(/\n/g, '\\n')}`);
        }
      } else if (contentLength === prevLength) {
        // Only log unchanged every 5th fetch to reduce noise
        console.log(`[SubtitleOverlay] VTT unchanged: ${contentLength} bytes, ${cues.length} cues cached`);
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
      // Report null range when URL changes (new extraction starting)
      onCuesRangeChange?.(null);
    }
  }, [vttUrl, onCuesRangeChange]);

  // Report available cue range when cues change
  useEffect(() => {
    if (cues.length === 0) {
      onCuesRangeChange?.(null);
      return;
    }
    // Cues are sorted by startTime, so first cue has min, last cue has max
    const minTime = cues[0].startTime;
    const maxTime = cues[cues.length - 1].endTime;
    onCuesRangeChange?.({ minTime, maxTime });
  }, [cues, onCuesRangeChange]);

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

  // Find active cues for current time
  // timeOffset is the keyframe-aligned offset from playback start
  const adjustedTime = effectiveTime + timeOffset;
  const activeCues = useMemo(() => {
    if (!enabled || cues.length === 0) return [];
    return findActiveCues(cues, adjustedTime);
  }, [cues, adjustedTime, enabled]);

  // Report debug info to parent for troubleshooting
  useEffect(() => {
    if (onDebugInfo) {
      const firstCue = cues.length > 0 ? cues[0] : null;
      const activeCue = activeCues.length > 0 ? activeCues[0] : null;
      onDebugInfo({
        adjustedTime,
        activeCueStart: activeCue?.startTime ?? null,
        activeCueEnd: activeCue?.endTime ?? null,
        activeCueText: activeCue?.text?.substring(0, 30) ?? null,
        totalCues: cues.length,
        firstCueStart: firstCue?.startTime ?? null,
      });
    }
  }, [onDebugInfo, adjustedTime, activeCues, cues]);

  // Debug logging: dump cues from last 60 seconds once per minute
  const lastDebugDumpRef = useRef<number>(0);
  useEffect(() => {
    if (!enabled || cues.length === 0) return;

    const now = Date.now();
    // Only log once per minute
    if (now - lastDebugDumpRef.current < 60000) return;
    lastDebugDumpRef.current = now;

    // Find cues in the window [adjustedTime - 60, adjustedTime]
    const windowStart = Math.max(0, adjustedTime - 60);
    const windowEnd = adjustedTime;
    const cuesInWindow = cues.filter(
      (cue) => cue.endTime >= windowStart && cue.startTime <= windowEnd
    );

    const lastCue = cues.length > 0 ? cues[cues.length - 1] : null;
    const activeCue = activeCues.length > 0 ? activeCues[0] : null;

    const timestamp = new Date().toISOString();
    console.log(`[SubtitleOverlay] === VTT Debug Dump (once per minute) @ ${timestamp} ===`);
    console.log(`[SubtitleOverlay] Platform: ${Platform.OS}, isTV: ${Platform.isTV}`);
    console.log(`[SubtitleOverlay] vttUrl: ${vttUrl}`);
    console.log(`[SubtitleOverlay] adjustedTime: ${adjustedTime.toFixed(2)}s (currentTime: ${effectiveTime.toFixed(2)}s + timeOffset: ${timeOffset})`);
    console.log(`[SubtitleOverlay] Total cues: ${cues.length}, First cue: ${cues[0]?.startTime.toFixed(2)}s, Last cue: ${lastCue?.endTime.toFixed(2)}s`);
    console.log(`[SubtitleOverlay] Active cue: ${activeCue ? `${activeCue.startTime.toFixed(2)}-${activeCue.endTime.toFixed(2)}s "${activeCue.text.substring(0, 40)}"` : 'NONE'}`);
    console.log(`[SubtitleOverlay] Cues in last 60s window (${windowStart.toFixed(2)}-${windowEnd.toFixed(2)}s): ${cuesInWindow.length}`);

    // Log each cue in the window
    cuesInWindow.forEach((cue, i) => {
      const isActive = adjustedTime >= cue.startTime && adjustedTime < cue.endTime;
      console.log(`[SubtitleOverlay]   [${i}] ${cue.startTime.toFixed(2)}-${cue.endTime.toFixed(2)}s ${isActive ? '>>> ACTIVE <<<' : ''} "${cue.text.substring(0, 50)}"`);
    });

    // Check for gaps - cues that should exist but don't
    if (cuesInWindow.length === 0 && adjustedTime > 30) {
      console.log('[SubtitleOverlay] WARNING: No cues in last 60 seconds! Possible issues:');
      console.log('[SubtitleOverlay]   - VTT extraction may be slow');
      console.log('[SubtitleOverlay]   - Time offset mismatch');
      console.log('[SubtitleOverlay]   - Cues haven\'t been extracted yet for this time range');
    }
    console.log('[SubtitleOverlay] === End VTT Debug Dump ===');
  }, [enabled, cues, adjustedTime, timeOffset, activeCues, vttUrl, effectiveTime]);

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

  // HDR content uses grey text for better visibility against bright HDR highlights
  const hdrTextColor = useMemo(() => {
    if (!isHDRContent) return undefined;
    // Use a darker grey on TV (larger screen, brighter HDR) vs phones
    const greyColor = Platform.isTV ? '#888888' : '#CCCCCC';
    return { color: greyColor };
  }, [isHDRContent]);

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
                  {cue.segments.map((segment, segIndex) => (
                    <Text
                      key={`seg-${segIndex}`}
                      style={segment.italic ? styles.italicText : undefined}>
                      {segment.text}
                    </Text>
                  ))}
                </Text>
              ))}
              {/* White text on top (or grey for HDR content) */}
              {/* Note: hdrTextColor is applied to both outer and inner Text elements because
                  TV platforms (tvOS/Android TV) don't properly inherit text color from parent */}
              <Text style={[styles.subtitleText, scaledTextStyles, hdrTextColor]}>
                {cue.segments.map((segment, segIndex) => (
                  <Text
                    key={`seg-${segIndex}`}
                    style={[segment.italic ? styles.italicText : undefined, hdrTextColor]}>
                    {segment.text}
                  </Text>
                ))}
              </Text>
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
  // Italic text style for <i> tags in VTT
  italicText: {
    fontStyle: 'italic',
  },
});

export default SubtitleOverlay;
