import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV, getTVScaleMultiplier } from '@/theme/tokens/tvScale';
import { Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { useState, useEffect, useRef } from 'react';

interface HdrInfo {
  isDolbyVision?: boolean;
  dolbyVisionProfile?: string;
  isHDR10?: boolean;
  colorTransfer?: string;
  colorPrimaries?: string;
  colorSpace?: string;
}

interface SafeAreaInsets {
  top: number;
  right: number;
  bottom: number;
  left: number;
}

interface MediaInfoDisplayProps {
  mediaType?: 'movie' | 'series' | 'tv' | 'show';
  title: string;
  year?: number;
  seasonNumber?: number;
  episodeNumber?: number;
  episodeName?: string;
  visible?: boolean;
  sourcePath?: string;
  displayName?: string; // If provided, use this instead of extracting from sourcePath
  playerImplementation?: string | null;
  onFilenameDisplayChange?: (isDisplaying: boolean) => void;
  onShowStreamInfo?: () => void; // Called when user taps on mobile to show stream info modal
  hdrInfo?: HdrInfo;
  resolution?: string; // Raw resolution (e.g., "3840x2160") - will be formatted to category
  safeAreaInsets?: SafeAreaInsets;
}

const formatDvProfile = (profile?: string): string => {
  if (!profile) return '';
  // Format common Dolby Vision profiles
  const profileLower = profile.toLowerCase();
  if (profileLower.includes('dvhe.05') || profileLower === '5' || profileLower === 'profile 5') return 'Profile 5';
  if (profileLower.includes('dvhe.07') || profileLower === '7' || profileLower === 'profile 7') return 'Profile 7';
  if (profileLower.includes('dvhe.08') || profileLower === '8' || profileLower === 'profile 8') return 'Profile 8';
  if (profileLower.includes('dvav.09') || profileLower === '9' || profileLower === 'profile 9') return 'Profile 9';
  if (profileLower.includes('dvav.10') || profileLower === '10' || profileLower === 'profile 10') return 'Profile 10';
  // Return as-is if it's just a number or already formatted
  const num = parseInt(profile, 10);
  if (!isNaN(num)) return `Profile ${num}`;
  return profile;
};

const formatColorInfo = (value?: string): string => {
  if (!value) return '';
  const lower = value.toLowerCase();
  // Transfer characteristics
  if (lower === 'smpte2084' || lower === 'smpte-st-2084') return 'PQ (HDR)';
  if (lower === 'arib-std-b67' || lower === 'hlg') return 'HLG';
  if (lower === 'bt709' || lower === 'bt.709') return 'BT.709 (SDR)';
  // Color primaries
  if (lower === 'bt2020') return 'BT.2020';
  if (lower === 'bt709') return 'BT.709';
  if (lower === 'smpte432' || lower === 'p3') return 'P3';
  // Color space/matrix
  if (lower === 'bt2020nc' || lower === 'bt2020_ncl') return 'BT.2020 NCL';
  if (lower === 'bt2020c' || lower === 'bt2020_cl') return 'BT.2020 CL';
  // Capitalize first letter of each word for unknown values
  return value.replace(/\b\w/g, (c) => c.toUpperCase());
};

const formatResolution = (resolution?: string): string | null => {
  if (!resolution) return null;
  // Parse resolution string like "3840x2160" or "1920x1080"
  const match = resolution.match(/^(\d+)x(\d+)$/);
  if (!match) return null;
  const height = parseInt(match[2], 10);
  if (isNaN(height)) return null;
  // Categorize by height
  if (height >= 2160) return '2160p';
  if (height >= 1080) return '1080p';
  if (height >= 720) return '720p';
  return '480p';
};

export default function MediaInfoDisplay({
  mediaType = 'movie',
  title,
  year,
  seasonNumber,
  episodeNumber,
  episodeName: _episodeName,
  visible = true,
  sourcePath,
  displayName,
  playerImplementation,
  onFilenameDisplayChange,
  onShowStreamInfo,
  hdrInfo,
  resolution,
  safeAreaInsets,
}: MediaInfoDisplayProps) {
  const theme = useTheme();
  const { width, height } = useTVDimensions();
  const isLandscape = width > height;
  const [showFilename, setShowFilename] = useState(false);
  const styles = createStyles(theme, safeAreaInsets, showFilename, isLandscape);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Clear timeout when component unmounts
  useEffect(() => {
    return () => {
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
    };
  }, []);

  // Auto-hide filename after 10 seconds and notify parent
  useEffect(() => {
    if (showFilename) {
      // Notify parent that filename is being displayed
      onFilenameDisplayChange?.(true);

      // Clear any existing timeout
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }

      // Set new timeout to hide after 10 seconds
      timeoutRef.current = setTimeout(() => {
        setShowFilename(false);
        onFilenameDisplayChange?.(false);
        timeoutRef.current = null;
      }, 10000);
    } else {
      // Notify parent that filename is no longer displayed
      onFilenameDisplayChange?.(false);
    }
  }, [showFilename, onFilenameDisplayChange]);

  if (!visible) {
    return null;
  }

  const isSeries = mediaType === 'series' || mediaType === 'tv' || mediaType === 'show';

  const formatMediaInfo = () => {
    if (isSeries && seasonNumber && episodeNumber) {
      const seasonStr = seasonNumber.toString().padStart(2, '0');
      const episodeStr = episodeNumber.toString().padStart(2, '0');
      const episodeCode = `S${seasonStr}E${episodeStr}`;

      if (year) {
        return `${title} - ${episodeCode} (${year})`;
      }
      return `${title} - ${episodeCode}`;
    }

    // For movies
    if (year) {
      return `${title} (${year})`;
    }
    return title;
  };

  const extractFilename = (path?: string) => {
    if (!path) {
      return null;
    }

    try {
      // Try to parse as URL first
      const url = new URL(path);
      const pathname = url.pathname;
      // Extract filename from path
      const filename = pathname.split('/').pop();
      return filename ? decodeURIComponent(filename) : null;
    } catch {
      // If not a URL, treat as a file path
      const filename = path.split('/').pop();
      return filename || null;
    }
  };

  // If displayName is provided, use it instead of extracting from path
  // This is used in demo mode to hide actual filenames
  const filename = displayName || extractFilename(sourcePath);
  const displayText = showFilename && filename ? filename : formatMediaInfo();
  // Don't allow toggling if displayName is provided (we want to hide the real filename)
  const canToggle = !displayName && !!filename && filename !== formatMediaInfo();
  // On mobile, pressing shows stream info modal if callback provided
  const canShowInfo = !!onShowStreamInfo;

  const handlePress = () => {
    if (canShowInfo) {
      onShowStreamInfo();
    } else if (canToggle) {
      setShowFilename(!showFilename);
    }
  };

  // Build HDR/DV info display
  const buildHdrDisplay = (): { badge: string | null; details: string | null } => {
    if (!hdrInfo) return { badge: null, details: null };

    const { isDolbyVision, dolbyVisionProfile, isHDR10, colorTransfer, colorPrimaries, colorSpace } = hdrInfo;

    // Determine the badge (main HDR format)
    let badge: string | null = null;
    if (isDolbyVision) {
      const profile = formatDvProfile(dolbyVisionProfile);
      badge = profile ? `Dolby Vision ${profile}` : 'Dolby Vision';
    } else if (isHDR10) {
      badge = 'HDR10';
    }

    // Build color metadata details
    const colorParts: string[] = [];
    if (colorTransfer) {
      const formatted = formatColorInfo(colorTransfer);
      if (formatted && !badge?.includes(formatted)) {
        colorParts.push(formatted);
      }
    }
    if (colorPrimaries) {
      const formatted = formatColorInfo(colorPrimaries);
      if (formatted) {
        colorParts.push(formatted);
      }
    }
    if (colorSpace) {
      const formatted = formatColorInfo(colorSpace);
      // Only add if different from primaries
      if (formatted && !colorParts.includes(formatted)) {
        colorParts.push(formatted);
      }
    }

    const details = colorParts.length > 0 ? colorParts.join(' · ') : null;
    return { badge, details };
  };

  const hdrDisplay = buildHdrDisplay();
  const resolutionBadge = formatResolution(resolution);

  // Allow more lines for filename display since they tend to be longer
  const maxLines = showFilename ? 3 : 2;

  // Check if we have any badges to display
  const hasBadges = resolutionBadge || hdrDisplay.badge;

  // Hide color info for native players (VLC/RNV) - only show for Expo/Web/System players
  const showColorInfo =
    playerImplementation !== 'React Native VLC' && playerImplementation !== 'React Native Video';

  if (Platform.isTV) {
    return (
      <View style={styles.container} pointerEvents="none">
        <Text style={styles.text} numberOfLines={maxLines}>
          {displayText}
        </Text>
        {playerImplementation && <Text style={styles.playerImplementationText}>{playerImplementation}</Text>}
        {hasBadges && (
          <View style={styles.badgesRow}>
            {resolutionBadge && (
              <View style={styles.resolutionBadgeContainer}>
                <Text style={styles.resolutionBadgeText}>{resolutionBadge}</Text>
              </View>
            )}
            {hdrDisplay.badge && (
              <View style={styles.hdrBadgeContainer}>
                <Text style={styles.hdrBadgeText}>{hdrDisplay.badge}</Text>
              </View>
            )}
          </View>
        )}
        {hdrDisplay.details && showColorInfo && <Text style={styles.colorInfoText}>{hdrDisplay.details}</Text>}
      </View>
    );
  }

  return (
    <Pressable style={styles.container} onPress={handlePress} disabled={!canShowInfo && !canToggle}>
      <Text style={styles.text} numberOfLines={maxLines}>
        {displayText}
      </Text>
      {playerImplementation && <Text style={styles.playerImplementationText}>{playerImplementation}</Text>}
      {hasBadges && (
        <View style={styles.badgesRow}>
          {resolutionBadge && (
            <View style={styles.resolutionBadgeContainer}>
              <Text style={styles.resolutionBadgeText}>{resolutionBadge}</Text>
            </View>
          )}
          {hdrDisplay.badge && (
            <View style={styles.hdrBadgeContainer}>
              <Text style={styles.hdrBadgeText}>{hdrDisplay.badge}</Text>
            </View>
          )}
        </View>
      )}
      {hdrDisplay.details && showColorInfo && <Text style={styles.colorInfoText}>{hdrDisplay.details}</Text>}
    </Pressable>
  );
}

const createStyles = (
  theme: NovaTheme,
  safeAreaInsets?: SafeAreaInsets,
  showFilename?: boolean,
  isLandscape?: boolean,
) => {
  const isWeb = Platform.OS === 'web';
  const isMobileHandheld = (Platform.OS === 'ios' || Platform.OS === 'android') && Platform.isTV !== true;
  // Unified TV scaling - tvOS is baseline, Android TV auto-derives
  const tvScale = isTV ? getTVScaleMultiplier() : 1;
  const sizeScale = isMobileHandheld ? 0.7 : tvScale;

  // For TV: base * 1.5 * tvScale (tvOS gets 1.5x, Android TV gets 1.5 * 0.55 = 0.825x)
  const getScaledValue = (baseValue: number) => baseValue * 1.5 * sizeScale;

  // Calculate right position with safe area inset for mobile
  const baseRight = isWeb ? 12 : 16;
  const safeAreaRight = isMobileHandheld ? (safeAreaInsets?.right ?? 0) : 0;
  const rightPosition = baseRight + safeAreaRight;

  // Use wider maxWidth when showing filename (filenames are typically longer)
  // In portrait mode, cap at 50% since screen is narrower
  const getBaseMaxWidth = () => {
    if (isMobileHandheld && !isLandscape) {
      // Portrait mode: capped at 50%
      return 50;
    }
    // Landscape or non-mobile: wider widths
    return showFilename ? 85 : 60;
  };
  const baseMaxWidth = getBaseMaxWidth();
  const maxWidthPercent = isMobileHandheld
    ? (`${baseMaxWidth - Math.min(safeAreaRight / 4, 10)}%` as const)
    : (`${baseMaxWidth}%` as const);

  return StyleSheet.create({
    container: {
      position: 'absolute',
      top: isWeb ? 12 : 16,
      right: rightPosition,
      backgroundColor: 'rgba(0, 0, 0, 0.65)',
      paddingHorizontal: getScaledValue(isWeb ? 12 : 16),
      paddingVertical: getScaledValue(isWeb ? 8 : 12),
      borderRadius: getScaledValue(isWeb ? 8 : 12),
      maxWidth: maxWidthPercent as `${number}%`,
      zIndex: 3,
    },
    text: {
      fontSize: getScaledValue(isWeb ? 14 : 16),
      fontWeight: '600',
      color: theme.colors.text.primary,
      textAlign: 'right',
    },
    playerImplementationText: {
      fontSize: getScaledValue(isWeb ? 11 : 12),
      fontStyle: 'italic',
      fontWeight: '400',
      color: theme.colors.text.secondary,
      textAlign: 'right',
      marginTop: getScaledValue(4),
      letterSpacing: getScaledValue(0.3),
    },
    badgesRow: {
      flexDirection: 'row',
      justifyContent: 'flex-end',
      alignItems: 'center',
      marginTop: getScaledValue(6),
      gap: getScaledValue(6),
    },
    resolutionBadgeContainer: {
      backgroundColor: 'rgba(100, 149, 237, 0.85)',
      paddingHorizontal: getScaledValue(8),
      paddingVertical: getScaledValue(3),
      borderRadius: getScaledValue(4),
    },
    resolutionBadgeText: {
      fontSize: getScaledValue(isWeb ? 10 : 11),
      fontWeight: '700',
      color: '#fff',
      textAlign: 'center',
      letterSpacing: getScaledValue(0.5),
    },
    hdrBadgeContainer: {
      backgroundColor: 'rgba(255, 215, 0, 0.85)',
      paddingHorizontal: getScaledValue(8),
      paddingVertical: getScaledValue(3),
      borderRadius: getScaledValue(4),
    },
    hdrBadgeText: {
      fontSize: getScaledValue(isWeb ? 10 : 11),
      fontWeight: '700',
      color: '#000',
      textAlign: 'center',
      letterSpacing: getScaledValue(0.5),
    },
    colorInfoText: {
      fontSize: getScaledValue(isWeb ? 10 : 11),
      fontWeight: '400',
      color: theme.colors.text.secondary,
      textAlign: 'right',
      marginTop: getScaledValue(3),
      letterSpacing: getScaledValue(0.3),
    },
  });
};
