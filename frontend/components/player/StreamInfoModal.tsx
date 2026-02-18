import React, { useCallback, useEffect, useMemo, useRef } from 'react';
import { Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { useLockSpatialNavigation } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';

export interface StreamInfoData {
  // Media info
  title?: string;
  episodeCode?: string; // e.g., "S01E05"
  episodeName?: string;
  year?: number;

  // File info
  filename?: string;

  // Video stream info
  resolution?: string; // e.g., "3840x2160"
  videoBitrate?: number; // in bits per second
  videoCodec?: string;
  frameRate?: string;

  // Audio info
  audioCodec?: string;
  audioChannels?: string; // e.g., "7.1" or "stereo"
  audioBitrate?: number;

  // Color info
  colorSpace?: string;
  colorPrimaries?: string;
  colorTransfer?: string;
  hdrFormat?: string; // e.g., "Dolby Vision Profile 7", "HDR10"

  // Player info
  playerImplementation?: string;

  // AIOStreams passthrough format
  passthroughName?: string; // Raw display name from AIOStreams
  passthroughDescription?: string; // Raw description from AIOStreams
}

interface StreamInfoModalProps {
  visible: boolean;
  info: StreamInfoData;
  onClose: () => void;
}

const formatBitrate = (bitsPerSecond?: number): string | null => {
  if (!bitsPerSecond || bitsPerSecond <= 0) return null;
  const mbps = bitsPerSecond / 1_000_000;
  if (mbps >= 1) {
    return `${mbps.toFixed(1)} Mbps`;
  }
  const kbps = bitsPerSecond / 1_000;
  return `${kbps.toFixed(0)} kbps`;
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

interface InfoRowProps {
  label: string;
  value?: string | null;
  /** Allow unlimited lines (for long text like filenames) */
  fullText?: boolean;
}

const InfoRow: React.FC<InfoRowProps & { styles: ReturnType<typeof createStyles> }> = ({
  label,
  value,
  styles,
  fullText,
}) => {
  if (!value) return null;
  const isMobile = Platform.OS !== 'web' && !Platform.isTV;

  // For full text items on mobile, use vertical stacked layout
  if (fullText && isMobile) {
    return (
      <View style={styles.infoRowStacked}>
        <Text style={styles.infoLabelStacked}>{label}</Text>
        <Text style={styles.infoValueStacked}>{value}</Text>
      </View>
    );
  }

  return (
    <View style={styles.infoRow}>
      <Text style={styles.infoLabel}>{label}</Text>
      <Text style={styles.infoValue} numberOfLines={fullText ? undefined : 2}>
        {value}
      </Text>
    </View>
  );
};

export const StreamInfoModal: React.FC<StreamInfoModalProps> = ({ visible, info, onClose }) => {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);

  // Lock spatial navigation when modal is visible to prevent dual focus system conflicts
  const { lock, unlock } = useLockSpatialNavigation();
  useEffect(() => {
    if (!Platform.isTV) return;
    if (visible) {
      lock();
    } else {
      unlock();
    }
    return () => {
      unlock();
    };
  }, [visible, lock, unlock]);

  // Build display values
  const mediaTitle = useMemo(() => {
    if (!info.title) return null;
    let display = info.title;
    if (info.episodeCode) {
      display += ` - ${info.episodeCode}`;
    }
    if (info.year) {
      display += ` (${info.year})`;
    }
    return display;
  }, [info.title, info.episodeCode, info.year]);

  const videoBitrateStr = formatBitrate(info.videoBitrate);
  const audioBitrateStr = formatBitrate(info.audioBitrate);

  // Build color info summary
  const colorInfo = useMemo(() => {
    const parts: string[] = [];
    if (info.hdrFormat) {
      parts.push(info.hdrFormat);
    }
    if (info.colorTransfer) {
      const formatted = formatColorInfo(info.colorTransfer);
      if (formatted && !info.hdrFormat?.includes(formatted)) {
        parts.push(formatted);
      }
    }
    if (info.colorPrimaries) {
      parts.push(formatColorInfo(info.colorPrimaries));
    }
    if (info.colorSpace) {
      const formatted = formatColorInfo(info.colorSpace);
      if (!parts.includes(formatted)) {
        parts.push(formatted);
      }
    }
    return parts.length > 0 ? parts.join(' · ') : null;
  }, [info.hdrFormat, info.colorTransfer, info.colorPrimaries, info.colorSpace]);

  // Back button handling for TV
  const onCloseRef = useRef(onClose);
  const removeInterceptorRef = useRef<(() => void) | null>(null);
  const canCloseWithBackRef = useRef(true);
  const backCloseDelayTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    onCloseRef.current = onClose;
  }, [onClose]);

  useEffect(() => {
    if (visible) {
      canCloseWithBackRef.current = false;
      if (backCloseDelayTimeoutRef.current) {
        clearTimeout(backCloseDelayTimeoutRef.current);
      }
      backCloseDelayTimeoutRef.current = setTimeout(() => {
        canCloseWithBackRef.current = true;
        backCloseDelayTimeoutRef.current = null;
      }, 300);
    } else {
      canCloseWithBackRef.current = true;
      if (backCloseDelayTimeoutRef.current) {
        clearTimeout(backCloseDelayTimeoutRef.current);
        backCloseDelayTimeoutRef.current = null;
      }
    }
  }, [visible]);

  useEffect(() => {
    if (!Platform.isTV) {
      return;
    }

    if (!visible) {
      if (removeInterceptorRef.current) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
      return;
    }

    let isHandling = false;
    let cleanupScheduled = false;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      if (!canCloseWithBackRef.current) {
        return true;
      }
      if (isHandling) {
        return true;
      }

      isHandling = true;
      onCloseRef.current();

      if (!cleanupScheduled) {
        cleanupScheduled = true;
        setTimeout(() => {
          if (removeInterceptorRef.current) {
            removeInterceptorRef.current();
            removeInterceptorRef.current = null;
          }
          isHandling = false;
        }, 750);
      }

      return true;
    });

    removeInterceptorRef.current = removeInterceptor;

    return () => {
      if (removeInterceptorRef.current === removeInterceptor && !cleanupScheduled) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
    };
  }, [visible]);

  useEffect(() => {
    return () => {
      if (removeInterceptorRef.current) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
      if (backCloseDelayTimeoutRef.current) {
        clearTimeout(backCloseDelayTimeoutRef.current);
        backCloseDelayTimeoutRef.current = null;
      }
    };
  }, []);

  const selectGuardRef = useRef(false);
  const withSelectGuard = useCallback((fn: () => void) => {
    if (!Platform.isTV) {
      fn();
      return;
    }
    if (selectGuardRef.current) {
      return;
    }
    selectGuardRef.current = true;
    try {
      fn();
    } finally {
      setTimeout(() => {
        selectGuardRef.current = false;
      }, 250);
    }
  }, []);

  const handleClose = useCallback(() => {
    withSelectGuard(onClose);
  }, [onClose, withSelectGuard]);

  // Ref for manual scroll control on TV (must be before early return to maintain hooks order)
  const tvScrollViewRef = useRef<ScrollView>(null);

  // Approximate section height for scroll calculations
  const APPROX_SECTION_HEIGHT = 120;

  // Handle section focus - scroll to keep focused item in view
  const handleSectionFocus = useCallback((index: number) => {
    const scrollOffset = Math.max(0, (index - 1) * APPROX_SECTION_HEIGHT);
    tvScrollViewRef.current?.scrollTo({ y: scrollOffset, animated: true });
  }, []);

  if (!visible) {
    return null;
  }

  // Helper to render a section - returns plain JSX, not a component
  const renderSection = (title: string, content: React.ReactNode) => (
    <View style={styles.section}>
      <Text style={styles.sectionTitle}>{title}</Text>
      {content}
    </View>
  );

  // Build sections array for TV (focusable) and non-TV (plain)
  const sections: Array<{ key: string; title: string; content: React.ReactNode; show: boolean }> = [
    {
      key: 'media',
      title: 'Media',
      show: !!mediaTitle,
      content: (
        <>
          <InfoRow label="Title" value={mediaTitle} styles={styles} fullText />
          {info.episodeName && <InfoRow label="Episode" value={info.episodeName} styles={styles} fullText />}
        </>
      ),
    },
    {
      key: 'source',
      title: 'Source',
      show: !!(info.passthroughName || info.passthroughDescription),
      content: (
        <>
          {info.passthroughName && <InfoRow label="Name" value={info.passthroughName} styles={styles} fullText />}
          {info.passthroughDescription && (
            <InfoRow label="Details" value={info.passthroughDescription} styles={styles} fullText />
          )}
        </>
      ),
    },
    {
      key: 'file',
      title: 'File',
      show: !!info.filename,
      content: <InfoRow label="Filename" value={info.filename} styles={styles} fullText />,
    },
    {
      key: 'video',
      title: 'Video',
      show: !!(info.resolution || info.videoCodec || videoBitrateStr || info.frameRate),
      content: (
        <>
          <InfoRow label="Resolution" value={info.resolution} styles={styles} />
          <InfoRow label="Codec" value={info.videoCodec} styles={styles} />
          <InfoRow label="Bitrate" value={videoBitrateStr} styles={styles} />
          <InfoRow label="Frame Rate" value={info.frameRate} styles={styles} />
        </>
      ),
    },
    {
      key: 'audio',
      title: 'Audio',
      show: !!(info.audioCodec || info.audioChannels || audioBitrateStr),
      content: (
        <>
          <InfoRow label="Codec" value={info.audioCodec} styles={styles} />
          <InfoRow label="Channels" value={info.audioChannels} styles={styles} />
          <InfoRow label="Bitrate" value={audioBitrateStr} styles={styles} />
        </>
      ),
    },
    {
      key: 'color',
      title: 'Color',
      show: !!colorInfo,
      content: <InfoRow label="Format" value={colorInfo} styles={styles} />,
    },
    {
      key: 'playback',
      title: 'Playback',
      show: !!info.playerImplementation,
      content: <InfoRow label="Player" value={info.playerImplementation} styles={styles} />,
    },
  ];

  const visibleSections = sections.filter((s) => s.show);

  // Non-TV scroll content with plain sections
  const scrollContent = visibleSections.map((s) => (
    <View key={s.key}>{renderSection(s.title, s.content)}</View>
  ));

  // TV version with native focus handling (spatial navigation is locked)
  const tvModalContent = (
    <View style={styles.modalContainer}>
      <View style={styles.tvModalHeader}>
        <Text style={styles.modalTitle}>Stream Information</Text>
      </View>

      {/* Scrollable sections list with manual scroll on focus */}
      <ScrollView
        ref={tvScrollViewRef}
        style={styles.tvListContainer}
        contentContainerStyle={styles.tvListContent}
        scrollEnabled={false}
        showsVerticalScrollIndicator={false}>
        {visibleSections.map((section, index) => (
          <Pressable
            key={section.key}
            onFocus={() => handleSectionFocus(index)}
            android_disableSound
            hasTVPreferredFocus={index === 0}
            tvParallaxProperties={{ enabled: false }}>
            {({ focused: isFocused }) => (
              <View style={[styles.tvSection, isFocused && styles.sectionFocused]}>
                <Text style={styles.sectionTitle}>{section.title}</Text>
                {section.content}
              </View>
            )}
          </Pressable>
        ))}
      </ScrollView>

      {/* Close button */}
      <View style={styles.tvModalFooter}>
        <Pressable onPress={handleClose} android_disableSound tvParallaxProperties={{ enabled: false }}>
          {({ focused: isFocused }) => (
            <View style={[styles.closeButton, isFocused && styles.closeButtonFocused]}>
              <Text style={[styles.closeButtonText, isFocused && styles.closeButtonTextFocused]}>Close</Text>
            </View>
          )}
        </Pressable>
      </View>
    </View>
  );

  // Non-TV version with regular ScrollView
  const nonTvModalContent = (
    <View style={styles.modalContainer}>
      <View style={styles.modalHeader}>
        <Text style={styles.modalTitle}>Stream Information</Text>
      </View>

      <ScrollView style={styles.contentScrollView} contentContainerStyle={styles.contentContainer}>
        {scrollContent}
      </ScrollView>

      <View style={styles.modalFooter}>
        <Pressable onPress={handleClose}>
          {({ focused: isCloseFocused }) => (
            <View style={[styles.closeButton, isCloseFocused && styles.closeButtonFocused]}>
              <Text style={[styles.closeButtonText, isCloseFocused && styles.closeButtonTextFocused]}>Close</Text>
            </View>
          )}
        </Pressable>
      </View>
    </View>
  );

  return (
    <Modal
      visible={visible}
      animationType="fade"
      transparent
      onRequestClose={handleClose}
      supportedOrientations={['portrait', 'portrait-upside-down', 'landscape', 'landscape-left', 'landscape-right']}
      hardwareAccelerated>
      <View style={styles.overlay}>
        <Pressable
          style={styles.backdrop}
          onPress={handleClose}
          tvParallaxProperties={{ enabled: false }}
          focusable={false}
        />
        {Platform.isTV ? tvModalContent : nonTvModalContent}
      </View>
    </Modal>
  );
};

const createStyles = (theme: NovaTheme) => {
  const isMobile = Platform.OS !== 'web' && !Platform.isTV;
  const isTV = Platform.isTV;

  return StyleSheet.create({
    overlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
      // Add horizontal padding for TV
      paddingHorizontal: isTV ? 120 : 0,
    },
    backdrop: {
      ...StyleSheet.absoluteFillObject,
    },
    modalContainer: {
      width: isMobile ? '92%' : isTV ? '100%' : '70%',
      maxWidth: isMobile ? undefined : isTV ? 700 : 600,
      maxHeight: isTV ? '90%' : '85%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
    },
    modalHeader: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.xl,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
    },
    tvModalHeader: {
      paddingHorizontal: theme.spacing['2xl'],
      paddingVertical: theme.spacing.xl,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
    },
    modalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    contentScrollView: {
      flexGrow: 1,
      flexShrink: 1,
    },
    contentContainer: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.lg,
    },
    tvContentContainer: {
      paddingHorizontal: theme.spacing['2xl'],
      paddingVertical: theme.spacing.xl,
    },
    tvListContainer: {
      maxHeight: 450,
      width: '100%',
      overflow: 'hidden',
    },
    tvListContent: {
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.lg,
      gap: theme.spacing.md,
    },
    section: {
      marginBottom: theme.spacing.lg,
      borderRadius: theme.radius.md,
      padding: theme.spacing.sm,
      marginHorizontal: -theme.spacing.sm,
    },
    tvSection: {
      borderRadius: theme.radius.lg,
      padding: theme.spacing.lg,
      backgroundColor: 'rgba(255, 255, 255, 0.03)',
    },
    sectionFocused: {
      backgroundColor: 'rgba(255, 255, 255, 0.1)',
    },
    sectionTitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      fontWeight: '600',
      textTransform: 'uppercase',
      letterSpacing: 1,
      marginBottom: theme.spacing.sm,
    },
    infoRow: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'flex-start',
      paddingVertical: theme.spacing.xs,
      paddingHorizontal: theme.spacing.sm,
      backgroundColor: 'rgba(255, 255, 255, 0.05)',
      borderRadius: theme.radius.sm,
      marginBottom: theme.spacing.xs,
    },
    infoLabel: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      flex: 1,
    },
    infoValue: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      fontWeight: '500',
      flex: 2,
      textAlign: 'right',
    },
    modalFooter: {
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.lg,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'center',
    },
    tvModalFooter: {
      paddingHorizontal: theme.spacing['2xl'],
      paddingVertical: theme.spacing.xl,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'center',
    },
    tvCloseButton: {
      paddingVertical: theme.spacing.md * 1.5,
      paddingHorizontal: theme.spacing['2xl'] * 1.5,
      borderRadius: theme.radius.md * 1.5,
      backgroundColor: theme.colors.overlay.button,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    tvCloseButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    tvCloseButtonText: {
      ...theme.typography.label.md,
      fontSize: theme.typography.label.md.fontSize * 1.5,
      color: theme.colors.text.primary,
    },
    tvCloseButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    closeButton: {
      minWidth: 200,
      paddingHorizontal: theme.spacing['2xl'],
      paddingVertical: theme.spacing.md,
      borderRadius: theme.radius.md,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.surface,
      alignItems: 'center',
    },
    closeButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    closeButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    closeButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    // Stacked layout for full text items on mobile
    infoRowStacked: {
      flexDirection: 'column',
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.sm,
      backgroundColor: 'rgba(255, 255, 255, 0.05)',
      borderRadius: theme.radius.sm,
      marginBottom: theme.spacing.xs,
    },
    infoLabelStacked: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      marginBottom: theme.spacing.xs,
    },
    infoValueStacked: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      fontWeight: '500',
    },
  });
};
