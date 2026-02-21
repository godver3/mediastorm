/**
 * Manual search result selection functionality for the details screen
 */

import FocusablePressable from '@/components/FocusablePressable';
import { Ionicons } from '@expo/vector-icons';
import { useUnplayableReleases } from '@/hooks/useUnplayableReleases';
import {
  apiService,
  type AudioTrackInfo,
  type ContentPreference,
  type DebridHealthCheck,
  type NZBHealthCheck,
  type NZBResult,
  type SubtitleTrackInfo,
  type UserSettings,
} from '@/services/api';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  Animated,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
  type StyleProp,
  type ViewStyle,
  type TextStyle,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { formatFileSize, formatPublishDate, getResultKey } from './utils';

type ManualResultHealthStatus = 'checking' | 'healthy' | 'unhealthy' | 'error' | 'not_applicable' | 'stream_error';

interface ManualResultHealthState {
  state: ManualResultHealthStatus;
  details?: NZBHealthCheck;
  debridDetails?: DebridHealthCheck;
  error?: string;
}

const isResultUnplayable = (health?: ManualResultHealthState) =>
  health?.state === 'unhealthy' || health?.state === 'error' || health?.state === 'stream_error';

export interface ManualTrackOverrides {
  audioTrack?: number;
  subtitleTrack?: number; // -1 means "Off"
}

interface ManualSelectionProps {
  visible: boolean;
  loading: boolean;
  error: string | null;
  results: NZBResult[];
  healthChecks: Record<string, ManualResultHealthState>;
  onClose: () => void;
  onSelect: (result: NZBResult, trackOverrides?: ManualTrackOverrides) => void;
  onDownload?: (result: NZBResult) => void;
  downloadOnly?: boolean;
  onCheckHealth: (result: NZBResult) => void;
  theme: NovaTheme;
  isWebTouch: boolean;
  isMobile: boolean;
  maxHeight: number;
  demoMode?: boolean;
  userSettings?: UserSettings;
  contentPreference?: ContentPreference | null;
}

export const ManualSelection = ({
  visible,
  loading,
  error,
  results,
  healthChecks,
  onClose,
  onSelect,
  onDownload,
  downloadOnly,
  onCheckHealth,
  theme,
  isWebTouch,
  isMobile,
  maxHeight,
  demoMode,
  userSettings,
  contentPreference,
}: ManualSelectionProps) => {
  const styles = useMemo(() => createManualSelectionStyles(theme), [theme]);
  const safeAreaInsets = useSafeAreaInsets();
  const _scrollViewRef = useRef<ScrollView>(null);
  const scrollOffsetRef = useRef(new Animated.Value(0)).current;
  const itemLayoutsRef = useRef<{ y: number; height: number }[]>([]);
  const showMobileIOSCloseButton = !Platform.isTV && isMobile && Platform.OS === 'ios';

  // Track expansion state for showing audio/subtitle tracks (mobile only)
  const [expandedTracks, setExpandedTracks] = useState<Set<string>>(new Set());

  // Track override state - user's manual selection per result (keyed by result key)
  const [trackOverrides, setTrackOverrides] = useState<Record<string, ManualTrackOverrides>>({});

  // Track pending health check: when user taps an item and we initiate a health check,
  // store the result so we can auto-proceed when it resolves healthy
  const pendingHealthCheckRef = useRef<{ key: string; result: NZBResult } | null>(null);

  // TV: Track which result is currently focused for side panel display
  const [tvFocusedKey, setTvFocusedKey] = useState<string | null>(null);

  const toggleTracks = useCallback((key: string) => {
    setExpandedTracks((prev) => {
      const next = new Set(prev);
      if (next.has(key)) {
        next.delete(key);
      } else {
        next.add(key);
      }
      return next;
    });
  }, []);

  // Helper to set audio track override for a result
  const setAudioTrackOverride = useCallback((key: string, trackIndex: number) => {
    setTrackOverrides((prev) => ({
      ...prev,
      [key]: { ...prev[key], audioTrack: trackIndex },
    }));
  }, []);

  // Helper to set subtitle track override for a result (-1 means "Off")
  const setSubtitleTrackOverride = useCallback((key: string, trackIndex: number) => {
    setTrackOverrides((prev) => ({
      ...prev,
      [key]: { ...prev[key], subtitleTrack: trackIndex },
    }));
  }, []);

  // Reset overrides when modal closes
  useEffect(() => {
    if (!visible) {
      setTrackOverrides({});
      setExpandedTracks(new Set());
      pendingHealthCheckRef.current = null;
    }
  }, [visible]);

  // Auto-proceed when a pending health check resolves as healthy
  useEffect(() => {
    const pending = pendingHealthCheckRef.current;
    if (!pending) return;

    const healthState = healthChecks[pending.key];
    if (!healthState || healthState.state === 'checking') return;

    // Health check resolved — clear pending regardless of outcome
    pendingHealthCheckRef.current = null;

    if (healthState.state === 'healthy') {
      if (downloadOnly && onDownload) {
        onDownload(pending.result);
      } else {
        onSelect(pending.result, trackOverrides[pending.key]);
      }
    }
    // If unhealthy/error/stream_error, do nothing — user sees red status
  }, [healthChecks, downloadOnly, onDownload, onSelect, trackOverrides]);

  // Auto-refetch tracks when tracksLoading is true
  useEffect(() => {
    const loadingEntries = Object.entries(healthChecks).filter(
      ([_, state]) => state.debridDetails?.tracksLoading && state.debridDetails?.cached,
    );

    if (loadingEntries.length === 0) return;

    // Set up polling for each loading entry
    const timeoutId = setTimeout(() => {
      loadingEntries.forEach(([key]) => {
        // Find the result for this key and re-check health
        const result = results.find((r) => getResultKey(r) === key);
        if (result) {
          onCheckHealth(result);
        }
      });
    }, 2000); // Poll every 2 seconds

    return () => clearTimeout(timeoutId);
  }, [healthChecks, results, onCheckHealth]);

  // Filter out releases that have been marked as unplayable
  const { isUnplayableByTitle, loading: loadingUnplayable } = useUnplayableReleases();

  const filteredResults = useMemo(() => {
    if (!results) {
      return []; // Handle null/undefined results
    }
    if (loadingUnplayable) {
      return results; // Show all while loading unplayable list
    }
    return results.filter((result) => !isUnplayableByTitle(result.title));
  }, [results, isUnplayableByTitle, loadingUnplayable]);

  const handleItemLayout = useCallback((index: number, y: number, height: number) => {
    itemLayoutsRef.current[index] = { y, height };
  }, []);

  const handleItemFocus = useCallback(
    (index: number) => {
      if (!Platform.isTV) return;

      console.log(`[ManualSelection] Focusing item ${index}`);

      // Calculate cumulative Y position from measured layouts
      let cumulativeY = 0;
      for (let i = 0; i < index; i++) {
        const layout = itemLayoutsRef.current[i];
        if (layout) {
          cumulativeY += layout.height;
        }
      }

      console.log(`[ManualSelection] Calculated cumulative Y: ${cumulativeY}`);

      // Scroll to position the focused item with some offset from top
      const scrollOffset = Math.max(0, cumulativeY - 100); // 100px from top
      console.log(`[ManualSelection] Scrolling to: ${scrollOffset}`);

      // Use animated transform for TV (bypasses native scroll)
      Animated.timing(scrollOffsetRef, {
        toValue: -scrollOffset,
        duration: 200,
        useNativeDriver: true,
      }).start();
    },
    [scrollOffsetRef],
  );

  // Helper to format language code to display name
  const formatLanguage = useCallback((lang: string | undefined): string => {
    if (!lang) return 'Unknown';
    // Common language code mappings
    const langMap: Record<string, string> = {
      eng: 'English',
      en: 'English',
      jpn: 'Japanese',
      ja: 'Japanese',
      spa: 'Spanish',
      es: 'Spanish',
      fre: 'French',
      fra: 'French',
      fr: 'French',
      ger: 'German',
      deu: 'German',
      de: 'German',
      ita: 'Italian',
      it: 'Italian',
      por: 'Portuguese',
      pt: 'Portuguese',
      rus: 'Russian',
      ru: 'Russian',
      chi: 'Chinese',
      zho: 'Chinese',
      zh: 'Chinese',
      kor: 'Korean',
      ko: 'Korean',
      ara: 'Arabic',
      ar: 'Arabic',
      hin: 'Hindi',
      hi: 'Hindi',
      und: 'Unknown',
    };
    return langMap[lang.toLowerCase()] || lang.toUpperCase();
  }, []);

  // Get track preferences: content override > user preference > empty (no preference)
  // Content override takes priority over user settings
  const audioLangPref = (
    contentPreference?.audioLanguage ||
    userSettings?.playback?.preferredAudioLanguage ||
    ''
  ).toLowerCase();
  const subLangPref = (
    contentPreference?.subtitleLanguage ||
    userSettings?.playback?.preferredSubtitleLanguage ||
    ''
  ).toLowerCase();
  const subModePref =
    contentPreference?.subtitleMode ||
    userSettings?.playback?.preferredSubtitleMode ||
    'off';

  // Helper to check if a language matches the preference
  const matchesLanguage = useCallback((language: string | undefined, title: string | undefined, pref: string): boolean => {
    if (!pref) return false; // No preference set
    const lang = (language || '').toLowerCase();
    const t = (title || '').toLowerCase();
    const normalizedPref = pref.toLowerCase();

    // Exact match
    if (lang === normalizedPref || t === normalizedPref) return true;
    // Partial match (skip empty strings)
    if (lang && (lang.includes(normalizedPref) || normalizedPref.includes(lang))) return true;
    if (t && (t.includes(normalizedPref) || normalizedPref.includes(t))) return true;
    return false;
  }, []);

  // Compatible audio codecs that don't need transcoding
  const compatibleAudioCodecs = useMemo(() => new Set(['aac', 'ac3', 'eac3', 'mp3']), []);
  const trueHdCodecs = useMemo(() => new Set(['truehd', 'mlp']), []);

  // Check if a track is a commentary track
  const isCommentary = useCallback((title: string) => {
    const lower = (title || '').toLowerCase();
    return lower.includes('commentary') || lower.includes('isolated score') || lower.includes('music only');
  }, []);

  // Find first compatible audio track (when no language preference or no match)
  const findFirstCompatibleAudioTrack = useCallback((tracks: AudioTrackInfo[]): number | null => {
    // First try: compatible codec, skip commentary
    for (const track of tracks) {
      if (compatibleAudioCodecs.has(track.codec?.toLowerCase() || '') && !isCommentary(track.title || '')) {
        return track.index;
      }
    }
    // Second try: non-TrueHD, skip commentary
    for (const track of tracks) {
      if (!trueHdCodecs.has(track.codec?.toLowerCase() || '') && !isCommentary(track.title || '')) {
        return track.index;
      }
    }
    // Last resort: first track
    return tracks[0]?.index ?? null;
  }, [compatibleAudioCodecs, trueHdCodecs, isCommentary]);

  // Find selected audio track (matches backend logic in track_helper.go)
  // Returns: { index: number | null, reason: 'preference' | 'first' | 'none' }
  const findSelectedAudioTrack = useCallback((tracks: AudioTrackInfo[] | undefined): { index: number | null; reason: 'preference' | 'first' | 'none' } => {
    if (!tracks?.length) return { index: null, reason: 'none' };

    // If no language preference set, default to first compatible track
    if (!audioLangPref) {
      const firstIdx = findFirstCompatibleAudioTrack(tracks);
      return { index: firstIdx, reason: 'first' };
    }

    // Try to find track matching language preference
    // Pass 1: Compatible codec, matching language, skip commentary
    for (const track of tracks) {
      if (matchesLanguage(track.language, track.title, audioLangPref) &&
          compatibleAudioCodecs.has(track.codec?.toLowerCase() || '') &&
          !isCommentary(track.title || '')) {
        return { index: track.index, reason: 'preference' };
      }
    }
    // Pass 2: Non-TrueHD, matching language, skip commentary
    for (const track of tracks) {
      if (matchesLanguage(track.language, track.title, audioLangPref) &&
          !trueHdCodecs.has(track.codec?.toLowerCase() || '') &&
          !isCommentary(track.title || '')) {
        return { index: track.index, reason: 'preference' };
      }
    }
    // Pass 3: TrueHD, matching language, skip commentary
    for (const track of tracks) {
      if (matchesLanguage(track.language, track.title, audioLangPref) &&
          !isCommentary(track.title || '')) {
        return { index: track.index, reason: 'preference' };
      }
    }
    // Pass 4-6: Include commentary tracks
    for (const track of tracks) {
      if (matchesLanguage(track.language, track.title, audioLangPref) &&
          compatibleAudioCodecs.has(track.codec?.toLowerCase() || '')) {
        return { index: track.index, reason: 'preference' };
      }
    }
    for (const track of tracks) {
      if (matchesLanguage(track.language, track.title, audioLangPref) &&
          !trueHdCodecs.has(track.codec?.toLowerCase() || '')) {
        return { index: track.index, reason: 'preference' };
      }
    }
    for (const track of tracks) {
      if (matchesLanguage(track.language, track.title, audioLangPref)) {
        return { index: track.index, reason: 'preference' };
      }
    }

    // No match found - fall back to first compatible track
    const firstIdx = findFirstCompatibleAudioTrack(tracks);
    return { index: firstIdx, reason: 'first' };
  }, [audioLangPref, matchesLanguage, compatibleAudioCodecs, trueHdCodecs, isCommentary, findFirstCompatibleAudioTrack]);

  // Find selected subtitle track (matches backend logic)
  // Returns: { index: number | null, willSearchExternal: boolean }
  const findSelectedSubtitleTrack = useCallback((tracks: SubtitleTrackInfo[] | undefined): { index: number | null; willSearchExternal: boolean } => {
    // Default mode is "off" - no subtitles
    if (subModePref === 'off' || subModePref === '') {
      return { index: null, willSearchExternal: false };
    }

    if (!tracks?.length) {
      // No tracks available - will search externally if mode is "on" and language is set
      return { index: null, willSearchExternal: subModePref === 'on' && !!subLangPref };
    }

    // Include all tracks (including PGS/bitmap) — native players (KSPlayer/MPV) can render them
    const usableTracks = tracks;

    // If no language preference, can't select a track
    if (!subLangPref) {
      return { index: null, willSearchExternal: false };
    }

    // Helper to check if track is SDH (Subtitles for Deaf/Hard of Hearing)
    const isSDH = (track: SubtitleTrackInfo) => {
      const title = (track.title || '').toLowerCase();
      return title.includes('sdh') || title.includes('deaf') || title.includes('hard of hearing');
    };

    if (subModePref === 'forced-only') {
      // Only consider forced tracks
      const forcedTracks = usableTracks.filter(t => t.forced);
      for (const track of forcedTracks) {
        if (matchesLanguage(track.language, track.title, subLangPref)) {
          return { index: track.index, willSearchExternal: false };
        }
      }
      return { index: null, willSearchExternal: false };
    }

    // Mode is "on" - prefer SDH > regular > forced
    // Pass 1: SDH tracks matching language (preferred for "on" mode)
    for (const track of usableTracks) {
      if (!track.forced && isSDH(track) && matchesLanguage(track.language, track.title, subLangPref)) {
        return { index: track.index, willSearchExternal: false };
      }
    }

    // Pass 2: Regular non-forced, non-SDH tracks matching language
    for (const track of usableTracks) {
      if (!track.forced && !isSDH(track) && matchesLanguage(track.language, track.title, subLangPref)) {
        return { index: track.index, willSearchExternal: false };
      }
    }

    // Pass 3: Forced tracks matching language (last resort)
    for (const track of usableTracks) {
      if (track.forced && matchesLanguage(track.language, track.title, subLangPref)) {
        return { index: track.index, willSearchExternal: false };
      }
    }

    // No match found - will search externally if mode is "on"
    return { index: null, willSearchExternal: true };
  }, [subModePref, subLangPref, matchesLanguage]);

  // Helper to render track panel (with tappable tracks for override selection)
  const renderTrackPanel = useCallback(
    (
      audioTracks: AudioTrackInfo[] | undefined,
      subtitleTracks: SubtitleTrackInfo[] | undefined,
      isFocused: boolean,
      resultKey: string,
    ) => {
      if (!audioTracks?.length && !subtitleTracks?.length) {
        return null;
      }

      // Get default selection from preferences
      const { index: defaultAudioIdx } = findSelectedAudioTrack(audioTracks);
      const { index: defaultSubIdx, willSearchExternal } = findSelectedSubtitleTrack(subtitleTracks);

      // Check for user overrides
      const overrides = trackOverrides[resultKey];
      const selectedAudioIdx = overrides?.audioTrack ?? defaultAudioIdx;
      // For subtitles: -1 means "Off", undefined means use default
      const selectedSubIdx = overrides?.subtitleTrack !== undefined
        ? (overrides.subtitleTrack === -1 ? null : overrides.subtitleTrack)
        : defaultSubIdx;
      const isSubtitleOff = overrides?.subtitleTrack === -1 || (overrides?.subtitleTrack === undefined && defaultSubIdx === null);

      return (
        <View style={[styles.trackPanel, isFocused && styles.trackPanelFocused]}>
          {audioTracks && audioTracks.length > 0 && (
            <View style={styles.trackSection}>
              <Text style={[styles.trackSectionTitle, isFocused && styles.trackSectionTitleFocused]}>Audio</Text>
              {audioTracks.map((track) => {
                const isSelected = track.index === selectedAudioIdx;
                return (
                  <Pressable
                    key={track.index}
                    onPress={(e) => {
                      e.stopPropagation();
                      setAudioTrackOverride(resultKey, track.index);
                    }}
                    style={styles.trackItemRow}
                  >
                    {isSelected && <Text style={styles.selectedIndicator}>▶</Text>}
                    <Text style={[
                      styles.trackItem,
                      isFocused && styles.trackItemFocused,
                      isSelected && styles.trackItemSelected,
                      !isSelected && styles.trackItemDimmed,
                    ]}>
                      {formatLanguage(track.language)}
                      {track.title ? ` - ${track.title}` : ''}
                      {track.codec ? ` (${track.codec.toUpperCase()})` : ''}
                    </Text>
                  </Pressable>
                );
              })}
            </View>
          )}
          {(subtitleTracks && subtitleTracks.length > 0) || true ? (
            <View style={styles.trackSection}>
              <Text style={[styles.trackSectionTitle, isFocused && styles.trackSectionTitleFocused]}>Subtitles</Text>
              {/* Always show "Off" option - tappable */}
              <Pressable
                onPress={(e) => {
                  e.stopPropagation();
                  setSubtitleTrackOverride(resultKey, -1);
                }}
                style={styles.trackItemRow}
              >
                {isSubtitleOff && <Text style={styles.selectedIndicator}>▶</Text>}
                <Text style={[
                  styles.trackItem,
                  isFocused && styles.trackItemFocused,
                  isSubtitleOff && styles.trackItemSelected,
                  !isSubtitleOff && styles.trackItemDimmed,
                ]}>
                  Off
                </Text>
              </Pressable>
              {/* Show subtitle tracks */}
              {subtitleTracks?.map((track) => {
                const isSelected = track.index === selectedSubIdx;
                return (
                  <Pressable
                    key={track.index}
                    onPress={(e) => {
                      e.stopPropagation();
                      setSubtitleTrackOverride(resultKey, track.index);
                    }}
                    style={styles.trackItemRow}
                  >
                    {isSelected && <Text style={styles.selectedIndicator}>▶</Text>}
                    <Text style={[
                      styles.trackItem,
                      isFocused && styles.trackItemFocused,
                      isSelected && styles.trackItemSelected,
                      !isSelected && styles.trackItemDimmed,
                    ]}>
                      {formatLanguage(track.language)}
                      {track.title ? ` - ${track.title}` : ''}
                      {track.forced && ' (Forced)'}
                    </Text>
                    {track.isBitmap && (
                      <Text style={styles.bitmapBadge}>{track.bitmapType || 'PGS'}</Text>
                    )}
                  </Pressable>
                );
              })}
              {/* Show external search indicator when no subtitle selected and would search */}
              {willSearchExternal && !isSubtitleOff && selectedSubIdx === null && (
                <View style={styles.trackItemRow}>
                  <Text style={styles.selectedIndicator}>▶</Text>
                  <Text style={[styles.trackItem, isFocused && styles.trackItemFocused, styles.trackItemSelected]}>
                    External search ({formatLanguage(subLangPref)})
                  </Text>
                  <Text style={styles.externalSearchBadge}>SEARCH</Text>
                </View>
              )}
            </View>
          ) : null}
        </View>
      );
    },
    [formatLanguage, styles, findSelectedAudioTrack, findSelectedSubtitleTrack, subModePref, subLangPref, trackOverrides, setAudioTrackOverride, setSubtitleTrackOverride],
  );

  // TV: Render the tracks side panel for the focused item
  const renderTvTracksSidePanel = useCallback(() => {
    if (downloadOnly) return null;
    if (!tvFocusedKey) {
      return (
        <View style={styles.tvTracksSidePanel}>
          <Text style={styles.tvTracksSidePanelTitle}>Track Info</Text>
          <Text style={styles.tvTracksSidePanelPlaceholder}>Select an item to see track details</Text>
        </View>
      );
    }

    const healthState = healthChecks[tvFocusedKey];
    const serviceType = filteredResults.find((r) => getResultKey(r) === tvFocusedKey)?.serviceType?.toLowerCase();
    const isDebrid = serviceType === 'debrid';
    const isCached = healthState?.state === 'healthy' && healthState?.debridDetails?.cached;
    const tracksLoading = healthState?.debridDetails?.tracksLoading;
    const hasTracks =
      healthState?.debridDetails?.audioTracks?.length || healthState?.debridDetails?.subtitleTracks?.length;

    if (!isDebrid) {
      return (
        <View style={styles.tvTracksSidePanel}>
          <Text style={styles.tvTracksSidePanelTitle}>Track Info</Text>
          <Text style={styles.tvTracksSidePanelPlaceholder}>Track info only available for debrid sources</Text>
        </View>
      );
    }

    if (!healthState || healthState.state === 'not_applicable') {
      return (
        <View style={styles.tvTracksSidePanel}>
          <Text style={styles.tvTracksSidePanelTitle}>Track Info</Text>
          <Text style={styles.tvTracksSidePanelPlaceholder}>Check cache status to see tracks</Text>
        </View>
      );
    }

    if (healthState.state === 'checking') {
      return (
        <View style={styles.tvTracksSidePanel}>
          <Text style={styles.tvTracksSidePanelTitle}>Track Info</Text>
          <Text style={styles.tvTracksSidePanelPlaceholder}>Checking cache status…</Text>
        </View>
      );
    }

    if (!isCached) {
      return (
        <View style={styles.tvTracksSidePanel}>
          <Text style={styles.tvTracksSidePanelTitle}>Track Info</Text>
          <Text style={styles.tvTracksSidePanelPlaceholder}>Not cached - tracks unavailable</Text>
        </View>
      );
    }

    if (tracksLoading) {
      return (
        <View style={styles.tvTracksSidePanel}>
          <Text style={styles.tvTracksSidePanelTitle}>Track Info</Text>
          <Text style={styles.tvTracksSidePanelPlaceholder}>Loading tracks…</Text>
        </View>
      );
    }

    if (!hasTracks) {
      return (
        <View style={styles.tvTracksSidePanel}>
          <Text style={styles.tvTracksSidePanelTitle}>Track Info</Text>
          <Text style={styles.tvTracksSidePanelPlaceholder}>No track info available</Text>
        </View>
      );
    }

    const audioTracks = healthState.debridDetails?.audioTracks || [];
    const subtitleTracks = healthState.debridDetails?.subtitleTracks || [];

    // Get default selection from preferences
    const { index: defaultAudioIdx } = findSelectedAudioTrack(audioTracks);
    const { index: defaultSubIdx, willSearchExternal } = findSelectedSubtitleTrack(subtitleTracks);

    // Check for user overrides
    const overrides = trackOverrides[tvFocusedKey];
    const selectedAudioIdx = overrides?.audioTrack ?? defaultAudioIdx;
    const selectedSubIdx = overrides?.subtitleTrack !== undefined
      ? (overrides.subtitleTrack === -1 ? null : overrides.subtitleTrack)
      : defaultSubIdx;
    const isSubtitleOff = overrides?.subtitleTrack === -1 || (overrides?.subtitleTrack === undefined && defaultSubIdx === null);

    return (
      <SpatialNavigationNode orientation="vertical">
        <View style={styles.tvTracksSidePanel}>
          <Text style={styles.tvTracksSidePanelTitle}>Track Selection</Text>
          <ScrollView style={styles.tvTracksSidePanelScroll} showsVerticalScrollIndicator={false}>
            {audioTracks.length > 0 && (
              <View style={styles.tvTrackSection}>
                <Text style={styles.tvTrackSectionTitle}>Audio ({audioTracks.length})</Text>
                {audioTracks.map((track) => {
                  const isSelected = track.index === selectedAudioIdx;
                  return (
                    <SpatialNavigationFocusableView
                      key={track.index}
                      onSelect={() => setAudioTrackOverride(tvFocusedKey, track.index)}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <View style={[
                          styles.tvTrackItemRow,
                          styles.tvTrackItemSelectable,
                          isFocused && styles.tvTrackItemFocused,
                        ]}>
                          {isSelected && <Text style={[styles.tvSelectedIndicator, isFocused && styles.tvSelectedIndicatorFocused]}>▶</Text>}
                          <Text style={[
                            styles.tvTrackItem,
                            isSelected && styles.tvTrackItemSelected,
                            !isSelected && styles.tvTrackItemDimmed,
                            isFocused && styles.tvTrackItemTextFocused,
                          ]}>
                            {formatLanguage(track.language)}
                            {track.title ? ` - ${track.title}` : ''}
                            {track.codec ? ` (${track.codec.toUpperCase()})` : ''}
                          </Text>
                        </View>
                      )}
                    </SpatialNavigationFocusableView>
                  );
                })}
              </View>
            )}
            <View style={styles.tvTrackSection}>
              <Text style={styles.tvTrackSectionTitle}>Subtitles ({subtitleTracks.length})</Text>
              {/* Show "Off" option */}
              <SpatialNavigationFocusableView
                onSelect={() => setSubtitleTrackOverride(tvFocusedKey, -1)}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <View style={[
                    styles.tvTrackItemRow,
                    styles.tvTrackItemSelectable,
                    isFocused && styles.tvTrackItemFocused,
                  ]}>
                    {isSubtitleOff && <Text style={[styles.tvSelectedIndicator, isFocused && styles.tvSelectedIndicatorFocused]}>▶</Text>}
                    <Text style={[
                      styles.tvTrackItem,
                      isSubtitleOff && styles.tvTrackItemSelected,
                      !isSubtitleOff && styles.tvTrackItemDimmed,
                      isFocused && styles.tvTrackItemTextFocused,
                    ]}>Off</Text>
                  </View>
                )}
              </SpatialNavigationFocusableView>
              {subtitleTracks.map((track) => {
                const isSelected = track.index === selectedSubIdx;
                return (
                  <SpatialNavigationFocusableView
                    key={track.index}
                    onSelect={() => {
                      setSubtitleTrackOverride(tvFocusedKey, track.index);
                    }}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <View style={[
                        styles.tvTrackItemRow,
                        styles.tvTrackItemSelectable,
                        isFocused && styles.tvTrackItemFocused,
                      ]}>
                        {isSelected && <Text style={[styles.tvSelectedIndicator, isFocused && styles.tvSelectedIndicatorFocused]}>▶</Text>}
                        <Text style={[
                          styles.tvTrackItem,
                          isSelected && styles.tvTrackItemSelected,
                          !isSelected && styles.tvTrackItemDimmed,
                          isFocused && styles.tvTrackItemTextFocused,
                        ]}>
                          {formatLanguage(track.language)}
                          {track.title ? ` - ${track.title}` : ''}
                          {track.forced && ' (Forced)'}
                        </Text>
                        {track.isBitmap && <Text style={styles.bitmapBadge}>{track.bitmapType || 'PGS'}</Text>}
                      </View>
                    )}
                  </SpatialNavigationFocusableView>
                );
              })}
              {/* Show external search indicator */}
              {willSearchExternal && !isSubtitleOff && selectedSubIdx === null && (
                <View style={styles.tvTrackItemRow}>
                  <Text style={styles.tvSelectedIndicator}>▶</Text>
                  <Text style={[styles.tvTrackItem, styles.tvTrackItemSelected]}>
                    External search ({formatLanguage(subLangPref)})
                  </Text>
                  <Text style={styles.externalSearchBadge}>SEARCH</Text>
                </View>
              )}
            </View>
          </ScrollView>
        </View>
      </SpatialNavigationNode>
    );
  }, [tvFocusedKey, healthChecks, filteredResults, formatLanguage, styles, findSelectedAudioTrack, findSelectedSubtitleTrack, subModePref, subLangPref, trackOverrides, setAudioTrackOverride, setSubtitleTrackOverride, downloadOnly]);

  const renderManualResultContent = useCallback(
    (result: NZBResult, isFocused: boolean) => {
      const key = getResultKey(result);
      const healthState = healthChecks[key];
      const isUnplayable = isResultUnplayable(healthState);
      const serviceType = (result.serviceType ?? 'usenet').toLowerCase() as 'usenet' | 'debrid';
      const serviceLabel = serviceType === 'debrid' ? 'D' : 'U';

      // Check if tracks are available or loading (debrid only, when cached)
      const tracksLoading =
        serviceType === 'debrid' &&
        healthState?.state === 'healthy' &&
        healthState?.debridDetails?.cached &&
        healthState?.debridDetails?.tracksLoading;
      const hasTracks =
        serviceType === 'debrid' &&
        healthState?.state === 'healthy' &&
        healthState?.debridDetails?.cached &&
        !tracksLoading &&
        (healthState?.debridDetails?.audioTracks?.length || healthState?.debridDetails?.subtitleTracks?.length);
      const isExpanded = expandedTracks.has(key);

      let statusLabel: string | null = null;
      if (healthState) {
        switch (healthState.state) {
          case 'checking':
            statusLabel =
              serviceType === 'debrid'
                ? 'Checking cache status…'
                : demoMode
                  ? 'Checking health…'
                  : 'Checking Usenet health…';
            break;
          case 'healthy': {
            const actionText = downloadOnly ? 'Tap to download' : Platform.isTV ? 'Select to play' : 'Tap to play';
            statusLabel = serviceType === 'debrid' ? `Cached • ${actionText}` : `Healthy • ${actionText}`;
            break;
          }
          case 'unhealthy': {
            statusLabel = serviceType === 'debrid' ? 'Not Cached' : 'Unhealthy';
            break;
          }
          case 'error':
            statusLabel = `Health check failed${healthState.error ? ` • ${healthState.error}` : ''}`;
            break;
          case 'stream_error':
            statusLabel = `Stream error${healthState.error ? ` • ${healthState.error}` : ''} • Cannot play`;
            break;
          case 'not_applicable':
            statusLabel = Platform.isTV ? 'Select to check health' : 'Tap to check health';
            break;
          default:
            statusLabel = null;
        }
      } else {
        statusLabel = Platform.isTV ? 'Select to check health' : 'Tap to check health';
      }

      const titleStyles: StyleProp<TextStyle>[] = [styles.manualResultTitle];
      const metaStyles: StyleProp<TextStyle>[] = [styles.manualResultMeta];
      const statusStyles: StyleProp<TextStyle>[] = [styles.manualResultStatus];
      const containerStyles: StyleProp<ViewStyle>[] = [styles.manualResult];
      const badgeStyles: StyleProp<TextStyle>[] = [
        styles.manualResultBadge,
        serviceType === 'debrid' ? styles.manualResultBadgeDebrid : styles.manualResultBadgeUsenet,
      ];

      if (isFocused) {
        containerStyles.push(styles.manualResultFocused);
        statusStyles.push(styles.manualResultStatusFocused);
        if (!isUnplayable) {
          titleStyles.push(styles.manualResultTitleFocused);
          metaStyles.push(styles.manualResultMetaFocused);
        }
      }

      if (isUnplayable) {
        containerStyles.push(styles.manualResultUnhealthy);
        statusStyles.push(styles.manualResultStatusUnhealthy);
        titleStyles.push(styles.manualResultTitleUnhealthy);
        metaStyles.push(styles.manualResultMetaUnhealthy);
        if (isFocused) {
          containerStyles.push(styles.manualResultUnhealthyFocused);
        }
      }

      // Check if this is an aiostreams result with passthrough format enabled
      const usePassthroughFormat =
        result.attributes?.passthrough_format === 'true' &&
        result.attributes?.raw_name &&
        result.attributes?.raw_description;

      if (usePassthroughFormat) {
        return (
          <View style={containerStyles}>
            <Text style={titleStyles}>{result.attributes!.raw_name}</Text>
            <Text style={[metaStyles, styles.manualResultDescription]}>{result.attributes!.raw_description}</Text>
            <View style={styles.statusRow}>
              {statusLabel && <Text style={statusStyles}>{statusLabel}</Text>}
              {!downloadOnly && tracksLoading && !Platform.isTV && (
                <Text style={[styles.tracksLoadingText, isFocused && styles.tracksButtonTextFocused]}>
                  Loading tracks…
                </Text>
              )}
              {!downloadOnly && hasTracks && !Platform.isTV && (
                <Pressable
                  onPress={(e) => {
                    e.stopPropagation();
                    toggleTracks(key);
                  }}
                  hitSlop={8}
                  style={styles.tracksButton}>
                  <Text style={[styles.tracksButtonText, isFocused && styles.tracksButtonTextFocused]}>
                    Tracks {isExpanded ? '▲' : '▼'}
                  </Text>
                </Pressable>
              )}
              {downloadOnly && onDownload && healthState?.state === 'healthy' && !Platform.isTV && (
                <Pressable
                  onPress={(e) => {
                    e.stopPropagation();
                    onDownload(result);
                  }}
                  hitSlop={8}
                  style={styles.downloadButton}>
                  <Ionicons name="download-outline" size={18} color={theme.colors.accent.primary} />
                </Pressable>
              )}
            </View>
            {!downloadOnly && isExpanded &&
              hasTracks &&
              renderTrackPanel(
                healthState?.debridDetails?.audioTracks,
                healthState?.debridDetails?.subtitleTracks,
                isFocused,
                key,
              )}
          </View>
        );
      }

      return (
        <View style={containerStyles}>
          <Text style={titleStyles}>{result.title}</Text>
          <View style={styles.manualResultMetaRow}>
            {!demoMode && <Text style={badgeStyles}>{serviceLabel}</Text>}
            <Text style={metaStyles}>
              {result.indexer} •{' '}
              {result.episodeCount && result.episodeCount > 0
                ? result.sizePerFile
                  ? `${formatFileSize(result.sizeBytes)}/ep`
                  : `${formatFileSize(Math.floor(result.sizeBytes / result.episodeCount))}/ep (${formatFileSize(result.sizeBytes)})`
                : result.sizePerFile
                  ? `${formatFileSize(result.sizeBytes)}/ep`
                  : formatFileSize(result.sizeBytes)}
              {serviceType === 'usenet' && result.publishDate ? ` • ${formatPublishDate(result.publishDate)}` : ''}
            </Text>
          </View>
          <View style={styles.statusRow}>
            {statusLabel && <Text style={statusStyles}>{statusLabel}</Text>}
            {!downloadOnly && tracksLoading && !Platform.isTV && (
              <Text style={[styles.tracksLoadingText, isFocused && styles.tracksButtonTextFocused]}>
                Loading tracks…
              </Text>
            )}
            {!downloadOnly && hasTracks && !Platform.isTV && (
              <Pressable
                onPress={(e) => {
                  e.stopPropagation();
                  toggleTracks(key);
                }}
                hitSlop={8}
                style={styles.tracksButton}>
                <Text style={[styles.tracksButtonText, isFocused && styles.tracksButtonTextFocused]}>
                  Tracks {isExpanded ? '▲' : '▼'}
                </Text>
              </Pressable>
            )}
              {downloadOnly && onDownload && healthState?.state === 'healthy' && !Platform.isTV && (
                <Pressable
                  onPress={(e) => {
                    e.stopPropagation();
                    onDownload(result);
                  }}
                  hitSlop={8}
                  style={styles.downloadButton}>
                  <Ionicons name="download-outline" size={18} color={theme.colors.accent.primary} />
                </Pressable>
              )}
          </View>
          {!downloadOnly && isExpanded &&
            hasTracks &&
            renderTrackPanel(
              healthState?.debridDetails?.audioTracks,
              healthState?.debridDetails?.subtitleTracks,
              isFocused,
              key,
            )}
        </View>
      );
    },
    [healthChecks, styles, demoMode, expandedTracks, toggleTracks, renderTrackPanel, trackOverrides, onDownload, downloadOnly, theme],
  );

  if (!visible) {
    return null;
  }

  console.log('[ManualSelection] Rendering modal, visible:', visible);

  const manualOverlayStyle = [
    styles.manualOverlay,
    {
      paddingTop: (theme.breakpoint === 'compact' ? theme.spacing['2xl'] : theme.spacing['3xl']) + safeAreaInsets.top,
      paddingBottom:
        (theme.breakpoint === 'compact' ? theme.spacing['2xl'] : theme.spacing['3xl']) + safeAreaInsets.bottom,
    },
  ];

  return (
    <Modal transparent visible={visible} onRequestClose={onClose} animationType="fade">
      <SpatialNavigationRoot isActive={visible}>
        <View style={styles.overlay}>
          {/* Backdrop for closing on mobile (TV uses back button via onRequestClose) */}
          <Pressable style={styles.overlayPressable} onPress={Platform.isTV ? undefined : onClose} focusable={false} />
          <View style={manualOverlayStyle} pointerEvents="box-none">
            <View style={styles.manualContainer}>
              <View style={styles.manualHeader}>
                <Text style={styles.manualTitle}>Select a source</Text>
                {showMobileIOSCloseButton ? (
                  <Pressable
                    onPress={onClose}
                    hitSlop={8}
                    accessibilityRole="button"
                    accessibilityLabel="Close manual selection"
                    style={styles.manualMobileCloseButton}>
                    <Text style={styles.manualMobileCloseButtonText}>Close</Text>
                  </Pressable>
                ) : null}
              </View>
              {loading && <Text style={styles.manualStatus}>Loading search results…</Text>}
              {!loading && error && (
                <View style={styles.manualErrorContainer}>
                  <Text style={styles.manualError}>{error}</Text>
                  <DefaultFocus>
                    <FocusablePressable text="Close" onSelect={onClose} style={styles.manualCancelButton} />
                  </DefaultFocus>
                </View>
              )}
              {!loading && !error && (!results || results.length === 0) && (
                <Text style={styles.manualStatus}>No results yet. Try again later.</Text>
              )}
              {!loading && !error && results && results.length > 0 && filteredResults.length === 0 && (
                <Text style={styles.manualStatus}>All results have been marked as unplayable.</Text>
              )}
              {!loading &&
                !error &&
                filteredResults.length > 0 &&
                (Platform.isTV ? (
                  // TV: Two-column layout with results on left, tracks on right
                  <View style={[styles.tvLayoutRow, { maxHeight }]}>
                    <SpatialNavigationNode orientation="vertical">
                      <View style={[styles.tvResultsColumn, { overflow: 'hidden' }]}>
                        <Animated.View
                          style={[styles.manualResultsContent, { transform: [{ translateY: scrollOffsetRef }] }]}>
                        {filteredResults.map((result, index) => {
                          const key = getResultKey(result) || `${result.indexer}-${index}`;
                          const healthState = healthChecks[key];
                          const hasHealthCheck = healthState && healthState.state !== 'checking';
                          const isHealthy = healthState?.state === 'healthy';

                          const handleSelect = () => {
                            console.log(
                              '[ManualSelection] Item selected:',
                              result.title,
                              'health:',
                              healthState?.state,
                            );

                            // First tap: check health if not already checked or checking
                            // Auto-proceed is handled by useEffect when health resolves healthy
                            if (!healthState || (!hasHealthCheck && healthState.state !== 'checking')) {
                              console.log('[ManualSelection] Checking health for:', result.title);
                              pendingHealthCheckRef.current = { key, result };
                              onCheckHealth(result);
                              return;
                            }

                            // If checking, do nothing
                            if (healthState.state === 'checking') {
                              console.log('[ManualSelection] Currently checking, ignoring select');
                              return;
                            }

                            // Already resolved: play (or download) if healthy
                            if (isHealthy) {
                              if (downloadOnly && onDownload) {
                                console.log('[ManualSelection] Downloading healthy result:', result.title);
                                onDownload(result);
                              } else {
                                console.log('[ManualSelection] Selecting healthy result:', result.title, 'trackOverrides:', trackOverrides[key]);
                                onSelect(result, trackOverrides[key]);
                              }
                            } else {
                              console.log(
                                '[ManualSelection] Result not healthy, cannot select. State:',
                                healthState?.state,
                              );
                            }
                          };

                          const focusableItem = (
                            <SpatialNavigationFocusableView
                              key={key}
                              focusKey={`manual-result-${key}`}
                              onSelect={() => {
                                console.log(
                                  '[ManualSelection] SpatialNavigationFocusableView onSelect called for index:',
                                  index,
                                );
                                handleSelect();
                              }}
                              onFocus={() => {
                                console.log(
                                  '[ManualSelection] SpatialNavigationFocusableView onFocus called for index:',
                                  index,
                                );
                                handleItemFocus(index);
                                setTvFocusedKey(key);
                              }}>
                              {({ isFocused }: { isFocused: boolean }) => (
                                <Pressable tvParallaxProperties={{ enabled: false }}>
                                  <View
                                    onLayout={(event) => {
                                      const { y, height } = event.nativeEvent.layout;
                                      handleItemLayout(index, y, height);
                                    }}>
                                    {renderManualResultContent(result, isFocused)}
                                  </View>
                                </Pressable>
                              )}
                            </SpatialNavigationFocusableView>
                          );

                          // Auto-focus first item
                          return index === 0 ? <DefaultFocus key={key}>{focusableItem}</DefaultFocus> : focusableItem;
                        })}
                        </Animated.View>
                      </View>
                    </SpatialNavigationNode>
                    {renderTvTracksSidePanel()}
                  </View>
                ) : isMobile || isWebTouch ? (
                  <ScrollView
                    style={[styles.manualResultsContainer, { maxHeight }]}
                    showsVerticalScrollIndicator
                    keyboardShouldPersistTaps="handled"
                    contentContainerStyle={styles.manualResultsContent}>
                    {filteredResults.map((result, index) => {
                      const key = getResultKey(result) || `${result.indexer}-${index}`;
                      const healthState = healthChecks[key];
                      const isUnplayable = isResultUnplayable(healthState);
                      const hasHealthCheck = healthState && healthState.state !== 'checking';
                      const isHealthy = healthState?.state === 'healthy';

                      const onSelectResult = () => {
                        // First tap: check health if not already checked or checking
                        // Auto-proceed is handled by useEffect when health resolves healthy
                        if (!healthState || (!hasHealthCheck && healthState.state !== 'checking')) {
                          pendingHealthCheckRef.current = { key, result };
                          onCheckHealth(result);
                          return;
                        }

                        // If checking, do nothing
                        if (healthState.state === 'checking') {
                          return;
                        }

                        // Already resolved: play (or download) if healthy
                        if (isHealthy) {
                          if (downloadOnly && onDownload) {
                            onDownload(result);
                          } else {
                            onSelect(result, trackOverrides[key]);
                          }
                        }
                      };

                      return (
                        <Pressable
                          key={key}
                          onPress={onSelectResult}
                          disabled={false}
                          style={isUnplayable ? styles.manualResultDisabled : undefined}>
                          {renderManualResultContent(result, false)}
                        </Pressable>
                      );
                    })}
                  </ScrollView>
                ) : null)}
            </View>
          </View>
        </View>
      </SpatialNavigationRoot>
    </Modal>
  );
};

export const useManualHealthChecks = (_results: NZBResult[]) => {
  const [healthChecks, setHealthChecks] = useState<Record<string, ManualResultHealthState>>({});

  // Return healthChecks state and a function to manually check a specific result
  const checkHealth = useCallback(async (result: NZBResult) => {
    const key = getResultKey(result);
    const serviceType = (result.serviceType ?? 'usenet').toLowerCase();

    // Check if we already have a cached/healthy result - if so, we're just polling for tracks
    // Don't reset to 'checking' state in that case
    const existingState = healthChecks[key];
    const isTrackPolling =
      existingState?.state === 'healthy' &&
      existingState?.debridDetails?.cached &&
      existingState?.debridDetails?.tracksLoading;

    if (!isTrackPolling) {
      // Set to checking state only for initial health checks
      setHealthChecks((prev) => ({
        ...prev,
        [key]: { state: 'checking' },
      }));
    }

    try {
      if (serviceType === 'debrid') {
        const debridDetails = await apiService.checkDebridCached(result);
        setHealthChecks((prev) => ({
          ...prev,
          [key]: {
            state: debridDetails.cached ? 'healthy' : 'unhealthy',
            debridDetails,
          },
        }));
      } else {
        const details = await apiService.checkUsenetHealth(result);
        setHealthChecks((prev) => ({
          ...prev,
          [key]: {
            state: details.healthy ? 'healthy' : 'unhealthy',
            details,
          },
        }));
      }
    } catch (err) {
      // On error during track polling, preserve the cached state but clear tracksLoading
      if (isTrackPolling) {
        setHealthChecks((prev) => ({
          ...prev,
          [key]: {
            ...prev[key],
            debridDetails: prev[key].debridDetails
              ? { ...prev[key].debridDetails, tracksLoading: false }
              : undefined,
          },
        }));
      } else {
        const message = err instanceof Error ? err.message : 'Health check failed.';
        setHealthChecks((prev) => ({
          ...prev,
          [key]: { state: 'error', error: message },
        }));
      }
    }
  }, [healthChecks]);

  return { healthChecks, checkHealth };
};

const createManualSelectionStyles = (theme: NovaTheme) => {
  const isCompactBreakpoint = theme.breakpoint === 'compact';

  return StyleSheet.create({
    overlay: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
    },
    overlayPressable: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
    },
    manualOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: isCompactBreakpoint ? 'flex-start' : 'center',
      alignItems: isCompactBreakpoint ? 'stretch' : 'center',
      paddingHorizontal: isCompactBreakpoint ? theme.spacing.xl : theme.spacing['3xl'],
      paddingTop: isCompactBreakpoint ? theme.spacing['2xl'] : theme.spacing['3xl'],
      paddingBottom: isCompactBreakpoint ? theme.spacing['2xl'] : theme.spacing['3xl'],
    },
    manualContainer: {
      width: isCompactBreakpoint ? '100%' : '70%',
      maxWidth: isCompactBreakpoint ? undefined : 960,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg,
      padding: isCompactBreakpoint ? theme.spacing['2xl'] : theme.spacing['3xl'],
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.md,
      alignSelf: isCompactBreakpoint ? 'stretch' : 'center',
      flexShrink: 1,
    },
    manualHeader: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      gap: theme.spacing.lg,
    },
    manualTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      flex: 1,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.title.lg.fontSize * 1.2,
            lineHeight: theme.typography.title.lg.lineHeight * 1.2,
          }
        : {}),
    },
    manualCloseButton: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.md,
    },
    manualCloseButtonText: {
      fontSize: theme.typography.body.md.fontSize * 1.2,
    },
    manualMobileCloseButton: {
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    manualMobileCloseButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
    },
    manualStatus: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.md.fontSize * 1.2,
            lineHeight: theme.typography.body.md.lineHeight * 1.2,
          }
        : {}),
    },
    manualResultsContainer: {
      paddingRight: theme.spacing.sm,
      marginBottom: theme.spacing.lg,
      flexGrow: 1,
      flexShrink: 1,
      width: '100%',
    },
    manualResultsContent: {
      paddingBottom: theme.spacing.lg,
    },
    manualResultPressable: {
      width: '100%',
    },
    manualResult: {
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.lg,
      borderRadius: theme.radius.md,
      backgroundColor: 'rgba(255, 255, 255, 0.08)',
      marginBottom: theme.spacing.md,
      ...(Platform.isTV
        ? {
            paddingVertical: theme.spacing.xl,
            paddingHorizontal: theme.spacing['2xl'],
            marginBottom: theme.spacing.lg,
          }
        : {}),
    },
    manualResultFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    manualResultUnhealthy: {
      backgroundColor: theme.colors.status.danger,
    },
    manualResultUnhealthyFocused: {
      backgroundColor: theme.colors.status.danger,
    },
    manualResultTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
      ...(Platform.isTV
        ? {
            ...theme.typography.title.md,
            marginBottom: theme.spacing.sm,
          }
        : {}),
    },
    manualResultTitleFocused: {
      color: theme.colors.background.base,
    },
    manualResultTitleUnhealthy: {
      color: theme.colors.text.inverse,
    },
    manualResultMeta: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.md.fontSize * 1.2,
            lineHeight: theme.typography.body.md.lineHeight * 1.2,
            fontWeight: theme.typography.body.md.fontWeight,
            fontFamily: theme.typography.body.md.fontFamily,
          }
        : {}),
    },
    manualResultMetaFocused: {
      color: theme.colors.background.base,
    },
    manualResultMetaUnhealthy: {
      color: theme.colors.text.inverse,
    },
    manualResultStatus: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.xs,
      ...(Platform.isTV
        ? {
            fontSize: theme.typography.body.sm.fontSize * 1.2,
            lineHeight: theme.typography.body.sm.lineHeight * 1.2,
            fontWeight: theme.typography.body.sm.fontWeight,
            fontFamily: theme.typography.body.sm.fontFamily,
            marginTop: theme.spacing.sm,
          }
        : {}),
    },
    manualResultStatusFocused: {
      color: theme.colors.background.base,
    },
    manualResultStatusUnhealthy: {
      color: theme.colors.text.inverse,
    },
    manualResultMetaRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
    },
    manualResultDescription: {
      marginTop: theme.spacing.xs,
    },
    manualResultBadge: {
      ...theme.typography.caption.sm,
      fontWeight: '700',
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: 2,
      borderRadius: theme.radius.sm,
      overflow: 'hidden',
      ...(Platform.isTV
        ? {
            ...theme.typography.body.md,
            paddingHorizontal: theme.spacing.md,
            paddingVertical: theme.spacing.xs,
          }
        : {}),
    },
    manualResultBadgeUsenet: {
      backgroundColor: theme.colors.accent.primary,
      color: theme.colors.text.inverse,
    },
    manualResultBadgeDebrid: {
      backgroundColor: theme.colors.accent.secondary,
      color: theme.colors.text.inverse,
    },
    manualCancelButton: {
      paddingHorizontal: theme.spacing['2xl'],
      alignSelf: 'flex-end',
      marginTop: theme.spacing.md,
    },
    manualErrorContainer: {
      marginTop: theme.spacing.md,
    },
    manualError: {
      ...theme.typography.body.md,
      color: theme.colors.status.danger,
    },
    manualResultDisabled: {
      opacity: 0.5,
    },
    // Track display styles
    statusRow: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      marginTop: theme.spacing.xs,
    },
    tracksButton: {
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
      backgroundColor: 'rgba(255, 255, 255, 0.12)',
    },
    downloadButton: {
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
      backgroundColor: 'rgba(255, 255, 255, 0.12)',
      marginLeft: theme.spacing.xs,
    },
    tracksButtonText: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      fontWeight: '600',
    },
    tracksButtonTextFocused: {
      color: theme.colors.background.base,
    },
    trackPanel: {
      marginTop: theme.spacing.md,
      paddingTop: theme.spacing.md,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: 'rgba(255, 255, 255, 0.2)',
    },
    trackPanelFocused: {
      borderTopColor: 'rgba(0, 0, 0, 0.2)',
    },
    trackSection: {
      marginBottom: theme.spacing.sm,
    },
    trackSectionTitle: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      fontWeight: '700',
      textTransform: 'uppercase',
      marginBottom: theme.spacing.xs,
    },
    trackSectionTitleFocused: {
      color: theme.colors.background.base,
    },
    trackItem: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.primary,
      marginBottom: 2,
    },
    trackItemFocused: {
      color: theme.colors.background.base,
    },
    trackItemRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
      marginBottom: 2,
    },
    bitmapBadge: {
      ...theme.typography.caption.sm,
      fontSize: 10,
      fontWeight: '700',
      color: '#fff',
      backgroundColor: '#e67e22',
      paddingHorizontal: theme.spacing.xs,
      paddingVertical: 1,
      borderRadius: theme.radius.sm,
      overflow: 'hidden',
    },
    tracksLoadingText: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      fontStyle: 'italic',
    },
    selectedIndicator: {
      ...theme.typography.caption.sm,
      color: theme.colors.accent.primary,
      marginRight: theme.spacing.xs,
      width: 12,
    },
    trackItemSelected: {
      color: theme.colors.accent.primary,
      fontWeight: '600',
    },
    trackItemDimmed: {
      opacity: 0.5,
    },
    trackItemUnusable: {
      opacity: 0.4,
      textDecorationLine: 'line-through',
    },
    externalSearchBadge: {
      ...theme.typography.caption.sm,
      fontSize: 10,
      fontWeight: '700',
      color: '#fff',
      backgroundColor: theme.colors.accent.secondary,
      paddingHorizontal: theme.spacing.xs,
      paddingVertical: 1,
      borderRadius: theme.radius.sm,
      overflow: 'hidden',
      marginLeft: theme.spacing.xs,
    },
    // TV side panel styles
    tvLayoutRow: {
      flexDirection: 'row',
      gap: theme.spacing.xl,
      flexGrow: 1,
      flexShrink: 1,
      width: '100%',
      marginBottom: theme.spacing.lg,
    },
    tvResultsColumn: {
      flex: 1,
      flexShrink: 1,
      paddingRight: theme.spacing.sm,
    },
    tvTracksSidePanel: {
      width: 280,
      backgroundColor: 'rgba(255, 255, 255, 0.05)',
      borderRadius: theme.radius.md,
      padding: theme.spacing.lg,
      alignSelf: 'flex-start',
    },
    tvTracksSidePanelTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.md,
      fontSize: theme.typography.title.md.fontSize * 1.1,
    },
    tvTracksSidePanelPlaceholder: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      fontSize: theme.typography.body.md.fontSize * 1.1,
    },
    tvTracksSidePanelScroll: {
      flex: 1,
    },
    tvTrackSection: {
      marginBottom: theme.spacing.lg,
    },
    tvTrackSectionTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.secondary,
      fontWeight: '700',
      textTransform: 'uppercase',
      marginBottom: theme.spacing.sm,
      fontSize: theme.typography.label.md.fontSize * 1.1,
    },
    tvTrackItem: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
      fontSize: theme.typography.body.md.fontSize * 1.1,
    },
    tvTrackItemRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
      marginBottom: theme.spacing.xs,
    },
    tvSelectedIndicator: {
      ...theme.typography.body.md,
      color: theme.colors.accent.primary,
      marginRight: theme.spacing.xs,
      width: 16,
      fontSize: theme.typography.body.md.fontSize * 1.1,
    },
    tvTrackItemSelected: {
      color: theme.colors.accent.primary,
      fontWeight: '600',
    },
    tvTrackItemDimmed: {
      opacity: 0.5,
    },
    tvTrackItemUnusable: {
      opacity: 0.4,
      textDecorationLine: 'line-through',
    },
    tvTrackItemSelectable: {
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.md,
      borderRadius: theme.radius.sm,
    },
    tvTrackItemFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    tvTrackItemTextFocused: {
      color: theme.colors.text.inverse,
      opacity: 1,
    },
    tvSelectedIndicatorFocused: {
      color: theme.colors.text.inverse,
    },
    tvTrackItemDisabled: {
      opacity: 0.4,
    },
  });
};
