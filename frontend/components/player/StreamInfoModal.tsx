import React, { useCallback, useEffect, useMemo, useRef } from 'react';
import { Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
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
}

const InfoRow: React.FC<InfoRowProps & { styles: ReturnType<typeof createStyles> }> = ({ label, value, styles }) => {
  if (!value) return null;
  return (
    <View style={styles.infoRow}>
      <Text style={styles.infoLabel}>{label}</Text>
      <Text style={styles.infoValue} numberOfLines={2}>
        {value}
      </Text>
    </View>
  );
};

export const StreamInfoModal: React.FC<StreamInfoModalProps> = ({ visible, info, onClose }) => {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);

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

  if (!visible) {
    return null;
  }

  return (
    <Modal
      visible={visible}
      animationType="fade"
      transparent
      onRequestClose={handleClose}
      supportedOrientations={['portrait', 'portrait-upside-down', 'landscape', 'landscape-left', 'landscape-right']}
      hardwareAccelerated>
      <SpatialNavigationRoot isActive={visible}>
        <View style={styles.overlay}>
          <Pressable style={styles.backdrop} onPress={handleClose} tvParallaxProperties={{ enabled: false }} />
          <View style={styles.modalContainer}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>Stream Information</Text>
            </View>

            <ScrollView
              style={styles.contentScrollView}
              contentContainerStyle={styles.contentContainer}
              scrollEnabled={!Platform.isTV}>
              {/* Media Info Section */}
              {mediaTitle && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Media</Text>
                  <InfoRow label="Title" value={mediaTitle} styles={styles} />
                  {info.episodeName && <InfoRow label="Episode" value={info.episodeName} styles={styles} />}
                </View>
              )}

              {/* File Info Section */}
              {info.filename && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>File</Text>
                  <InfoRow label="Filename" value={info.filename} styles={styles} />
                </View>
              )}

              {/* Video Info Section */}
              {(info.resolution || info.videoCodec || videoBitrateStr || info.frameRate) && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Video</Text>
                  <InfoRow label="Resolution" value={info.resolution} styles={styles} />
                  <InfoRow label="Codec" value={info.videoCodec} styles={styles} />
                  <InfoRow label="Bitrate" value={videoBitrateStr} styles={styles} />
                  <InfoRow label="Frame Rate" value={info.frameRate} styles={styles} />
                </View>
              )}

              {/* Audio Info Section */}
              {(info.audioCodec || info.audioChannels || audioBitrateStr) && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Audio</Text>
                  <InfoRow label="Codec" value={info.audioCodec} styles={styles} />
                  <InfoRow label="Channels" value={info.audioChannels} styles={styles} />
                  <InfoRow label="Bitrate" value={audioBitrateStr} styles={styles} />
                </View>
              )}

              {/* Color Info Section */}
              {colorInfo && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Color</Text>
                  <InfoRow label="Format" value={colorInfo} styles={styles} />
                </View>
              )}

              {/* Player Info Section */}
              {info.playerImplementation && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Playback</Text>
                  <InfoRow label="Player" value={info.playerImplementation} styles={styles} />
                </View>
              )}
            </ScrollView>

            <View style={styles.modalFooter}>
              <SpatialNavigationNode orientation="vertical">
                <DefaultFocus>
                  <SpatialNavigationFocusableView focusKey="stream-info-close" onSelect={handleClose}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <Pressable
                        onPress={handleClose}
                        style={[styles.closeButton, isFocused && styles.closeButtonFocused]}
                        tvParallaxProperties={{ enabled: false }}>
                        <Text style={[styles.closeButtonText, isFocused && styles.closeButtonTextFocused]}>Close</Text>
                      </Pressable>
                    )}
                  </SpatialNavigationFocusableView>
                </DefaultFocus>
              </SpatialNavigationNode>
            </View>
          </View>
        </View>
      </SpatialNavigationRoot>
    </Modal>
  );
};

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    overlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    backdrop: {
      ...StyleSheet.absoluteFillObject,
    },
    modalContainer: {
      width: '70%',
      maxWidth: 600,
      maxHeight: '80%',
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
    section: {
      marginBottom: theme.spacing.lg,
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
  });
