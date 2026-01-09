/**
 * Trailer functionality for the details screen
 */

import type { Trailer } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { createElement, useCallback, useMemo, type ReactNode } from 'react';
import { Linking, Modal, Platform, Pressable, StyleSheet, Text, View } from 'react-native';

const TRAILER_DIRECT_MEDIA_EXTENSIONS = /\.(m3u8|mp4|mov|mkv|webm|avi|ts|m4v|mpd)$/i;
const TRAILER_PORTAL_SITE_KEYWORDS = ['youtube', 'youtu.be', 'vimeo', 'dailymotion', 'bilibili', 'facebook', 'tiktok'];
const TRAILER_PORTAL_HOST_KEYWORDS = [
  'youtube.com',
  'youtu.be',
  'vimeo.com',
  'dailymotion.com',
  'bilibili.com',
  'facebook.com',
  'tiktok.com',
];

const isPortalHost = (url: string) => {
  try {
    const { hostname } = new URL(url);
    return TRAILER_PORTAL_HOST_KEYWORDS.some((keyword) => hostname.toLowerCase().includes(keyword));
  } catch {
    return TRAILER_PORTAL_HOST_KEYWORDS.some((keyword) => url.toLowerCase().includes(keyword));
  }
};

export const isTrailerLikelyDirectMedia = (trailer?: Trailer | null) => {
  if (!trailer) {
    return false;
  }

  const { site, url, embedUrl } = trailer;

  if (!url && !embedUrl) {
    return false;
  }

  const normalizedSite = site?.toLowerCase();
  if (normalizedSite && TRAILER_PORTAL_SITE_KEYWORDS.includes(normalizedSite)) {
    return false;
  }

  const candidateUrls = [url, embedUrl].filter((candidate): candidate is string => Boolean(candidate));

  for (const candidate of candidateUrls) {
    if (TRAILER_DIRECT_MEDIA_EXTENSIONS.test(candidate)) {
      return true;
    }

    const normalized = candidate.toLowerCase();
    if (normalized.includes('.m3u8') || normalized.includes('manifest.mpd') || normalized.includes('playlist.m3u8')) {
      return true;
    }

    if (isPortalHost(candidate)) {
      return false;
    }
  }

  return false;
};

const getWebViewModule = () => {
  try {
    // eslint-disable-next-line @typescript-eslint/no-var-requires, global-require
    return require('react-native-webview');
  } catch (error) {
    console.warn('Trailer modal: react-native-webview is not available. Falling back to external link.', error);
    return null;
  }
};

export const buildTrailerEmbedUrl = (trailer: Trailer | null) => {
  if (!trailer) {
    return '';
  }
  const base = trailer.embedUrl || trailer.url;
  if (!base) {
    return '';
  }
  return base.includes('?') ? `${base}&autoplay=1` : `${base}?autoplay=1`;
};

interface TrailerModalProps {
  visible: boolean;
  trailer: Trailer | null;
  onClose: () => void;
  theme: NovaTheme;
}

export const TrailerModal = ({ visible, trailer, onClose, theme }: TrailerModalProps) => {
  const styles = useMemo(() => createTrailerStyles(theme), [theme]);

  const handleOpenTrailerExternal = useCallback(() => {
    const url = trailer?.url;
    if (!url) {
      return;
    }
    Linking.openURL(url).catch((err) => console.warn('Unable to open trailer URL', err));
  }, [trailer]);

  if (!visible || !trailer) {
    return null;
  }

  const trailerName = trailer.name || 'Trailer';
  let playerContent: ReactNode | null = null;
  const useNativePlayer = Platform.OS !== 'web' && isTrailerLikelyDirectMedia(trailer);

  if (Platform.OS === 'web') {
    let src = buildTrailerEmbedUrl(trailer);
    if (!src) {
      const fallback = trailer.url || '';
      src = fallback.includes('?') ? `${fallback}&autoplay=1` : `${fallback}?autoplay=1`;
    }
    const safeSrc = src || 'about:blank';
    playerContent = createElement('iframe', {
      key: safeSrc,
      src: safeSrc,
      style: { width: '100%', height: '100%', border: 0, borderRadius: 12 },
      allow: 'accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share',
      allowFullScreen: true,
      title: trailerName,
    });
  } else if (useNativePlayer && trailer.url) {
    const Video = require('react-native-video').default;
    playerContent = (
      <Video
        style={styles.trailerVideoPlayer}
        source={{ uri: trailer.url }}
        paused={false}
        resizeMode="contain"
        onEnd={onClose}
        onError={() => onClose()}
        controls
      />
    );
  } else {
    const webViewModule = getWebViewModule();
    const WebView: (typeof import('react-native-webview'))['WebView'] | undefined = webViewModule?.WebView;
    const embedUrl = buildTrailerEmbedUrl(trailer);
    const resolvedUrl = embedUrl || trailer.url;
    if (!resolvedUrl || !WebView) {
      if (!WebView) {
        console.warn('Trailer modal: WebView component unavailable, defaulting to external player button.');
      }
      playerContent = (
        <View style={styles.trailerUnavailable}>
          <Text style={styles.trailerUnavailableText}>Trailer unavailable.</Text>
        </View>
      );
    } else {
      playerContent = (
        <WebView
          originWhitelist={['*']}
          source={{ uri: resolvedUrl }}
          style={styles.trailerWebView}
          mediaPlaybackRequiresUserAction={false}
          allowsInlineMediaPlayback
        />
      );
    }
  }

  return (
    <Modal visible transparent animationType="fade" onRequestClose={onClose}>
      <View style={styles.trailerModalOverlay}>
        <Pressable
          style={styles.trailerModalBackdrop}
          onPress={() => {
            if (!useNativePlayer && !getWebViewModule()?.WebView) {
              handleOpenTrailerExternal();
            }
            onClose();
          }}
        />
        <View style={styles.trailerModalContent}>
          <View style={styles.trailerModalHeader}>
            <Text style={styles.trailerModalTitle} numberOfLines={2}>
              {trailerName}
            </Text>
            <Pressable onPress={onClose} style={styles.trailerModalClose}>
              <Text style={styles.trailerModalCloseText}>Close</Text>
            </Pressable>
          </View>
          <View style={styles.trailerModalPlayer}>{playerContent}</View>
          <Pressable onPress={handleOpenTrailerExternal} style={styles.trailerModalOpenButton}>
            <Text style={styles.trailerModalOpenLabel}>Open in external player</Text>
          </Pressable>
        </View>
      </View>
    </Modal>
  );
};

const createTrailerStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    trailerModalOverlay: {
      flex: 1,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      justifyContent: 'center',
      alignItems: 'center',
      padding: theme.spacing.lg,
    },
    trailerModalBackdrop: {
      ...StyleSheet.absoluteFillObject,
    },
    trailerModalContent: {
      width: '100%',
      maxWidth: 720,
      borderRadius: theme.radius.lg,
      backgroundColor: theme.colors.background.surface,
      padding: theme.spacing.lg,
      gap: theme.spacing.md,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    trailerModalHeader: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      gap: theme.spacing.md,
    },
    trailerModalTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      flex: 1,
    },
    trailerModalClose: {
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    trailerModalCloseText: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },
    trailerModalPlayer: {
      width: '100%',
      aspectRatio: 16 / 9,
      backgroundColor: '#000000',
      borderRadius: theme.radius.md,
      overflow: 'hidden',
    },
    trailerVideoPlayer: {
      width: '100%',
      height: '100%',
    },
    trailerWebView: {
      width: '100%',
      height: '100%',
      backgroundColor: 'transparent',
    },
    trailerUnavailable: {
      flex: 1,
      alignItems: 'center',
      justifyContent: 'center',
      padding: theme.spacing.lg,
    },
    trailerUnavailableText: {
      ...theme.typography.body.md,
      color: theme.colors.text.muted,
      textAlign: 'center',
    },
    trailerModalOpenButton: {
      alignSelf: 'flex-start',
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.surface,
    },
    trailerModalOpenLabel: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
    },
  });
