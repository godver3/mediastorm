/**
 * SubtitleOverlay - Renders VTT subtitles as an overlay on the video player
 * Used for fMP4/HDR content where iOS AVPlayer doesn't expose muxed subtitles to react-native-video
 */
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { LayoutChangeEvent, Platform, StyleSheet, Text, View } from 'react-native';
import { ANDROID_TV_TO_TVOS_RATIO, getTVScaleMultiplier, tvScale } from '@/theme/tokens/tvScale';

/**
 * ASS subtitle alignment (numpad-style positioning)
 * 7 8 9  = top-left, top-center, top-right
 * 4 5 6  = middle-left, middle-center, middle-right
 * 1 2 3  = bottom-left, bottom-center, bottom-right
 */
export type ASSAlignment = 1 | 2 | 3 | 4 | 5 | 6 | 7 | 8 | 9;

/** A segment of styled text within a subtitle cue */
export interface StyledTextSegment {
  text: string;
  italic: boolean;
  bold?: boolean;
  underline?: boolean;
  color?: string; // Hex color like '#FFFFFF'
}

export interface VTTCue {
  startTime: number; // seconds
  endTime: number; // seconds
  text: string; // Plain text (for compatibility)
  segments: StyledTextSegment[]; // Styled segments for rendering
  alignment?: ASSAlignment; // ASS-style numpad alignment (1-9), default 2 (bottom-center)
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
  /** Height of the bottom letterbox bar in pixels (passed from player for accurate positioning) */
  letterboxBottom?: number;
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
 * Parse ASS-styled text with tags like {\an8}, {\i1}, {\b1}, {\c&HBBGGRR&}
 * Returns both the alignment and styled text segments
 */
function parseASSStyledText(text: string): { alignment?: ASSAlignment; segments: StyledTextSegment[] } {
  // Default alignment is 2 (bottom-center)
  let alignment: ASSAlignment | undefined = undefined;

  // Extract alignment tag {\anN} - must be at start
  const alignMatch = text.match(/^\{\\an([1-9])\}/);
  if (alignMatch) {
    alignment = parseInt(alignMatch[1], 10) as ASSAlignment;
    text = text.slice(alignMatch[0].length);
  }

  // Parse remaining text for styling tags
  const segments: StyledTextSegment[] = [];

  // Current style state
  let currentItalic = false;
  let currentBold = false;
  let currentUnderline = false;
  let currentColor: string | undefined = undefined;

  // Split text by ASS tags, keeping the tags
  // Tags like {\i1}, {\i0}, {\b1}, {\b0}, {\u1}, {\u0}, {\c&HBBGGRR&}, {\r}
  const tagRegex = /(\{[^}]*\})/g;
  const parts = text.split(tagRegex);

  for (const part of parts) {
    if (!part) continue;

    if (part.startsWith('{') && part.endsWith('}')) {
      // This is a tag - parse it for style changes
      const tagContent = part.slice(1, -1); // Remove { and }

      // Handle multiple tags in one block like {\i1\b1}
      const tagCommands = tagContent.split('\\').filter(Boolean);

      for (const cmd of tagCommands) {
        if (cmd === 'i1' || cmd === 'i') {
          currentItalic = true;
        } else if (cmd === 'i0') {
          currentItalic = false;
        } else if (cmd === 'b1' || cmd === 'b') {
          currentBold = true;
        } else if (cmd === 'b0') {
          currentBold = false;
        } else if (cmd === 'u1' || cmd === 'u') {
          currentUnderline = true;
        } else if (cmd === 'u0') {
          currentUnderline = false;
        } else if (cmd === 'r') {
          // Reset all styles
          currentItalic = false;
          currentBold = false;
          currentUnderline = false;
          currentColor = undefined;
        } else if (cmd.startsWith('c&H') || cmd.startsWith('1c&H')) {
          // Color tag: {\c&HBBGGRR&} or {\1c&HBBGGRR&}
          // Note: ASS uses BGR format, need to convert to RGB
          const colorMatch = cmd.match(/c&H([0-9A-Fa-f]{6})&?/);
          if (colorMatch) {
            const bgr = colorMatch[1];
            // Convert BGR to RGB
            const b = bgr.slice(0, 2);
            const g = bgr.slice(2, 4);
            const r = bgr.slice(4, 6);
            currentColor = `#${r}${g}${b}`;
          }
        }
      }
    } else {
      // This is text content - add it with current styles
      if (part.trim()) {
        segments.push({
          text: part,
          italic: currentItalic,
          bold: currentBold || undefined,
          underline: currentUnderline || undefined,
          color: currentColor,
        });
      }
    }
  }

  // If no segments were created, return the original text as plain
  if (segments.length === 0 && text.trim()) {
    segments.push({ text: text.trim(), italic: false });
  }

  return { alignment, segments };
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

        // Detect ASS tags ({\...}) and use ASS parser, otherwise use HTML parser
        const hasASSTags = rawText.includes('{\\');
        let segments: StyledTextSegment[];
        let alignment: ASSAlignment | undefined;

        if (hasASSTags) {
          const assResult = parseASSStyledText(rawText);
          segments = assResult.segments;
          alignment = assResult.alignment;
        } else {
          segments = parseStyledText(rawText);
        }

        cues.push({
          startTime,
          endTime,
          text: textLines.join('\n'),
          segments: segments.length > 0 ? segments : [{ text: textLines.join('\n'), italic: false }],
          alignment,
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
  letterboxBottom,
}) => {
  // Use container dimensions instead of screen dimensions for accurate positioning
  // Screen dimensions include safe areas which may not be part of our container
  const [containerSize, setContainerSize] = useState<{ width: number; height: number } | null>(null);
  const [cues, setCues] = useState<VTTCue[]>([]);
  const [_error, setError] = useState<string | null>(null);
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

  // Calculate subtitle positioning:
  // - Use letterboxBottom from player when available (accurate measurement)
  // - Fall back to calculation based on video dimensions
  // - Account for control bar height when controls are visible
  const subtitleBottomOffset = useMemo(() => {
    const basePadding = isAndroidTV ? 12 : Platform.isTV ? 20 : 10;

    // Calculate control bar height based on actual component dimensions
    // This mirrors the styling in Controls.tsx and FocusablePressable.tsx
    let controlsOffset = 0;
    if (controlsVisible && containerSize) {
      const { width: containerWidth, height: containerHeight } = containerSize;
      const isLandscape = containerWidth > containerHeight;

      if (isLandscape) {
        if (Platform.isTV) {
          // TV control bar calculation:
          // - Theme spacing uses legacy scale factors: tvOS 0.85, Android TV 0.5
          // - FocusablePressable uses: scale = tvScale(1.375, 1) * getTVScaleMultiplier()
          const themeScaleFactor = isAndroidTV ? 0.5 : 0.85;
          const buttonScale = tvScale(1.375, 1) * getTVScaleMultiplier();

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
    }

    // Check if we're on mobile in portrait mode
    const isMobile = !Platform.isTV;
    const isPortrait = containerSize ? containerSize.height > containerSize.width : false;
    const isMobilePortrait = isMobile && isPortrait;

    // Mobile portrait uses extra padding for better visibility
    const mobilePortraitPadding = basePadding + 30;

    // Calculate mobile portrait controls offset (controls are at bottom in portrait)
    let mobilePortraitControlsOffset = 0;
    if (isMobilePortrait && controlsVisible) {
      // Position subtitles above portrait controls
      mobilePortraitControlsOffset = 210;
    }

    // Use letterboxBottom from player when available (accurate measurement from screen dimensions)
    if (letterboxBottom !== undefined) {
      // Mobile portrait: position near bottom of screen (ignore letterbox)
      if (isMobilePortrait) {
        return controlsVisible ? mobilePortraitControlsOffset : mobilePortraitPadding;
      }
      // When controls are visible, position above controls (which are at screen bottom)
      // When controls are hidden, position above letterbox bars
      const effectiveBottom = controlsVisible ? Math.max(letterboxBottom, controlsOffset) : letterboxBottom;
      return effectiveBottom + basePadding;
    }

    // Fall back to calculation based on video dimensions and container size
    if (!containerSize) {
      return basePadding + controlsOffset;
    }

    // Mobile portrait: position near bottom of screen
    if (isMobilePortrait) {
      return controlsVisible ? mobilePortraitControlsOffset : mobilePortraitPadding;
    }

    const { width: containerWidth, height: containerHeight } = containerSize;

    // Calculate letterbox height if we have video dimensions
    let letterboxHeight = 0;
    if (videoWidth && videoHeight) {
      const videoAspectRatio = videoWidth / videoHeight;
      const containerAspectRatio = containerWidth / containerHeight;

      // Video is wider than container: letterboxing on top/bottom
      if (videoAspectRatio > containerAspectRatio) {
        const actualVideoHeight = containerWidth / videoAspectRatio;
        letterboxHeight = (containerHeight - actualVideoHeight) / 2;
      }
    }

    // Position subtitles just above the letterbox bars (or screen bottom if no letterbox)
    return letterboxHeight + basePadding + controlsOffset;
  }, [videoWidth, videoHeight, containerSize, controlsVisible, letterboxBottom]);

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
        console.log(
          `[SubtitleOverlay] VTT is header-only (${contentLength} bytes) - extraction not started or no cues yet`,
        );
      } else if (!hasTimestamps && contentLength > 10) {
        console.log(
          `[SubtitleOverlay] VTT has content (${contentLength} bytes) but NO timestamps - possibly truncated/corrupted`,
        );
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
            `${parsedCues.length} cues, range: ${firstCue?.startTime.toFixed(2) ?? 'N/A'}-${lastCue?.endTime.toFixed(2) ?? 'N/A'}s`,
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
    const cuesInWindow = cues.filter((cue) => cue.endTime >= windowStart && cue.startTime <= windowEnd);

    const lastCue = cues.length > 0 ? cues[cues.length - 1] : null;
    const activeCue = activeCues.length > 0 ? activeCues[0] : null;

    const timestamp = new Date().toISOString();
    console.log(`[SubtitleOverlay] === VTT Debug Dump (once per minute) @ ${timestamp} ===`);
    console.log(`[SubtitleOverlay] Platform: ${Platform.OS}, isTV: ${Platform.isTV}`);
    console.log(`[SubtitleOverlay] vttUrl: ${vttUrl}`);
    console.log(
      `[SubtitleOverlay] adjustedTime: ${adjustedTime.toFixed(2)}s (currentTime: ${effectiveTime.toFixed(2)}s + timeOffset: ${timeOffset})`,
    );
    console.log(
      `[SubtitleOverlay] Total cues: ${cues.length}, First cue: ${cues[0]?.startTime.toFixed(2)}s, Last cue: ${lastCue?.endTime.toFixed(2)}s`,
    );
    console.log(
      `[SubtitleOverlay] Active cue: ${activeCue ? `${activeCue.startTime.toFixed(2)}-${activeCue.endTime.toFixed(2)}s "${activeCue.text.substring(0, 40)}"` : 'NONE'}`,
    );
    console.log(
      `[SubtitleOverlay] Cues in last 60s window (${windowStart.toFixed(2)}-${windowEnd.toFixed(2)}s): ${cuesInWindow.length}`,
    );

    // Log each cue in the window
    cuesInWindow.forEach((cue, i) => {
      const isActive = adjustedTime >= cue.startTime && adjustedTime < cue.endTime;
      console.log(
        `[SubtitleOverlay]   [${i}] ${cue.startTime.toFixed(2)}-${cue.endTime.toFixed(2)}s ${isActive ? '>>> ACTIVE <<<' : ''} "${cue.text.substring(0, 50)}"`,
      );
    });

    // Check for gaps - cues that should exist but don't
    if (cuesInWindow.length === 0 && adjustedTime > 30) {
      console.log('[SubtitleOverlay] WARNING: No cues in last 60 seconds! Possible issues:');
      console.log('[SubtitleOverlay]   - VTT extraction may be slow');
      console.log('[SubtitleOverlay]   - Time offset mismatch');
      console.log("[SubtitleOverlay]   - Cues haven't been extracted yet for this time range");
    }
    console.log('[SubtitleOverlay] === End VTT Debug Dump ===');
  }, [enabled, cues, adjustedTime, timeOffset, activeCues, vttUrl, effectiveTime]);

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

  // Group active cues by vertical position for ASS alignment
  // 7,8,9 = top; 4,5,6 = middle; 1,2,3 = bottom (default)
  const groupedCues = useMemo(() => {
    const top: VTTCue[] = [];
    const middle: VTTCue[] = [];
    const bottom: VTTCue[] = [];

    for (const cue of activeCues) {
      const align = cue.alignment || 2; // Default to bottom-center
      if (align >= 7) {
        top.push(cue);
      } else if (align >= 4) {
        middle.push(cue);
      } else {
        bottom.push(cue);
      }
    }

    return { top, middle, bottom };
  }, [activeCues]);

  // Get horizontal alignment style based on ASS alignment
  const getHorizontalAlign = (alignment: ASSAlignment | undefined): 'flex-start' | 'center' | 'flex-end' => {
    const align = alignment || 2;
    const col = align % 3; // 1,4,7 = left; 2,5,8 = center; 0(3,6,9) = right
    if (col === 1) return 'flex-start';
    if (col === 0) return 'flex-end';
    return 'center';
  };

  // Get text alignment style based on ASS alignment
  const getTextAlign = (alignment: ASSAlignment | undefined): 'left' | 'center' | 'right' => {
    const align = alignment || 2;
    const col = align % 3;
    if (col === 1) return 'left';
    if (col === 0) return 'right';
    return 'center';
  };

  // Render a single cue with proper styling
  const renderCue = (cue: VTTCue, index: number) => {
    const textAlign = getTextAlign(cue.alignment);

    return (
      <Text
        key={`${cue.startTime}-${index}`}
        style={[styles.subtitleText, scaledTextStyles, hdrTextColor, { textAlign }]}
      >
        {cue.segments.map((segment, segIndex) => {
          const segmentStyle: any[] = [];
          if (segment.italic) segmentStyle.push(styles.italicText);
          if (segment.bold) segmentStyle.push(styles.boldText);
          if (segment.underline) segmentStyle.push(styles.underlineText);
          if (segment.color && !isHDRContent) segmentStyle.push({ color: segment.color });
          if (hdrTextColor) segmentStyle.push(hdrTextColor);

          return (
            <Text key={`seg-${segIndex}`} style={segmentStyle.length > 0 ? segmentStyle : undefined}>
              {segment.text}
            </Text>
          );
        })}
      </Text>
    );
  };

  // Render a group of cues in a container
  const renderCueGroup = (cues: VTTCue[], position: 'top' | 'middle' | 'bottom') => {
    if (cues.length === 0) return null;

    // Use alignment of first cue for container positioning
    const horizontalAlign = getHorizontalAlign(cues[0].alignment);

    const positionStyle: any = {
      position: 'absolute' as const,
      left: 0,
      right: 0,
      paddingHorizontal: Platform.isTV ? 60 : 20,
      alignItems: horizontalAlign,
    };

    if (position === 'top') {
      positionStyle.top = Platform.isTV ? 40 : 20;
    } else if (position === 'middle') {
      positionStyle.top = '50%';
      positionStyle.transform = [{ translateY: -50 }];
    } else {
      positionStyle.bottom = subtitleBottomOffset;
    }

    return (
      <View style={positionStyle}>
        <View style={[styles.cueContainer, { alignItems: horizontalAlign }]}>
          {cues.map((cue, index) => renderCue(cue, index))}
        </View>
      </View>
    );
  };

  // Always render container to capture dimensions via onLayout
  // Only render subtitle content when enabled and we have active cues
  return (
    <View style={styles.container} pointerEvents="none" onLayout={handleLayout}>
      {shouldShowSubtitles && (
        <>
          {renderCueGroup(groupedCues.top, 'top')}
          {renderCueGroup(groupedCues.middle, 'middle')}
          {renderCueGroup(groupedCues.bottom, 'bottom')}
        </>
      )}
    </View>
  );
};

// Subtitle styling with semi-transparent background box for readability
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
    // Subtitles grow upward from bottom (anchor at bottom line)
    // Single container for all active cues - ensures multi-line subtitles have connected backgrounds
    position: 'relative',
    alignItems: 'center',
    justifyContent: 'flex-end',
    // Semi-transparent background for better readability
    backgroundColor: 'rgba(0, 0, 0, 0.6)',
    paddingHorizontal: Platform.isTV ? 16 : 8,
    paddingVertical: Platform.isTV ? 6 : 3,
    borderRadius: Platform.isTV ? 6 : 4,
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
    // Additional padding for multi-line subtitles
    paddingVertical: 2,
  },
  // Italic text style for <i> tags in VTT
  italicText: {
    fontStyle: 'italic',
  },
  // Bold text style for ASS {\b1} tags
  boldText: {
    fontWeight: '800',
  },
  // Underline text style for ASS {\u1} tags
  underlineText: {
    textDecorationLine: 'underline',
  },
});

export default SubtitleOverlay;
