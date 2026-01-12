/**
 * Trailer functionality for the details screen
 * Fullscreen overlay with native video playback via yt-dlp stream extraction
 */

import type { Trailer } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { createElement, useCallback, useEffect, useMemo, useState, type ReactNode } from 'react';
import {
  ActivityIndicator,
  Dimensions,
  Linking,
  Modal,
  Platform,
  Pressable,
  StatusBar,
  StyleSheet,
  Text,
  View,
} from 'react-native';

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

const isYouTubeUrl = (url: string) => {
  try {
    const { hostname } = new URL(url);
    return hostname.includes('youtube.com') || hostname.includes('youtu.be');
  } catch {
    return url.includes('youtube.com') || url.includes('youtu.be');
  }
};

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
  /** Pre-loaded stream/proxy URL (for YouTube trailers, generated on details page load) */
  preloadedStreamUrl?: string | null;
}

export const TrailerModal = ({
  visible,
  trailer,
  onClose,
  theme,
  preloadedStreamUrl,
}: TrailerModalProps) => {
  const styles = useMemo(() => createTrailerStyles(theme), [theme]);
  const [error, setError] = useState<string | null>(null);
  const [isBuffering, setIsBuffering] = useState(true);

  const handleOpenTrailerExternal = useCallback(() => {
    const url = trailer?.url;
    if (!url) {
      return;
    }
    Linking.openURL(url).catch((err) => console.warn('Unable to open trailer URL', err));
  }, [trailer]);

  // Reset state when visibility changes
  useEffect(() => {
    if (visible) {
      setError(null);
      setIsBuffering(true);
    }
  }, [visible]);

  // Determine the stream URL to use
  const streamUrl = useMemo(() => {
    if (Platform.OS === 'web') {
      return null; // Web uses iframe
    }
    if (preloadedStreamUrl) {
      return preloadedStreamUrl;
    }
    if (trailer && isTrailerLikelyDirectMedia(trailer)) {
      return trailer.url;
    }
    return null;
  }, [preloadedStreamUrl, trailer]);

  if (!visible || !trailer) {
    return null;
  }

  const trailerName = trailer.name || 'Trailer';
  let playerContent: ReactNode | null = null;

  if (Platform.OS === 'web') {
    // Web: use iframe embed
    let src = buildTrailerEmbedUrl(trailer);
    if (!src) {
      const fallback = trailer.url || '';
      src = fallback.includes('?') ? `${fallback}&autoplay=1` : `${fallback}?autoplay=1`;
    }
    const safeSrc = src || 'about:blank';
    playerContent = createElement('iframe', {
      key: safeSrc,
      src: safeSrc,
      style: { width: '100%', height: '100%', border: 0 },
      allow: 'accelerometer; autoplay; clipboard-write; encrypted-media; gyroscope; picture-in-picture; web-share',
      allowFullScreen: true,
      title: trailerName,
    });
  } else if (error || !streamUrl) {
    // Error state with fallback to external player
    playerContent = (
      <View style={styles.errorContainer}>
        <Ionicons name="alert-circle-outline" size={48} color={theme.colors.text.muted} />
        <Text style={styles.errorText}>{error || 'Unable to play trailer'}</Text>
        {trailer.url && (
          <Pressable onPress={handleOpenTrailerExternal} style={styles.externalButton}>
            <Ionicons name="open-outline" size={20} color={theme.colors.text.inverse} />
            <Text style={styles.externalButtonText}>Open in Browser</Text>
          </Pressable>
        )}
      </View>
    );
  } else {
    // Native video player with proxy stream URL
    const Video = require('react-native-video').default;
    playerContent = (
      <>
        <Video
          style={styles.videoPlayer}
          source={{ uri: streamUrl }}
          paused={false}
          resizeMode="contain"
          onEnd={onClose}
          onError={(err: unknown) => {
            console.warn('Video playback error:', err);
            setError('Playback error');
          }}
          onBuffer={({ isBuffering: buffering }: { isBuffering: boolean }) => {
            setIsBuffering(buffering);
          }}
          onLoad={() => {
            setIsBuffering(false);
          }}
          controls
        />
        {isBuffering && (
          <View style={styles.bufferingOverlay}>
            <ActivityIndicator size="large" color="#ffffff" />
          </View>
        )}
      </>
    );
  }

  return (
    <Modal visible transparent animationType="fade" onRequestClose={onClose} statusBarTranslucent>
      <StatusBar hidden />
      <View style={styles.fullscreenOverlay}>
        {/* Top darkened area with title and close button */}
        <View style={styles.topBar}>
          <Text style={styles.trailerTitle} numberOfLines={1}>
            {trailerName}
          </Text>
          <Pressable onPress={onClose} style={styles.closeButton} hitSlop={16}>
            <Ionicons name="close" size={28} color="#ffffff" />
          </Pressable>
        </View>

        {/* Video player area */}
        <View style={styles.playerContainer}>{playerContent}</View>

        {/* Bottom darkened area */}
        <Pressable style={styles.bottomBar} onPress={onClose} />
      </View>
    </Modal>
  );
};

const { width: SCREEN_WIDTH } = Dimensions.get('window');
const VIDEO_HEIGHT = SCREEN_WIDTH * (9 / 16); // 16:9 aspect ratio

const createTrailerStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    fullscreenOverlay: {
      flex: 1,
      backgroundColor: '#000000',
    },
    topBar: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      paddingHorizontal: theme.spacing.lg,
      paddingTop: Platform.OS === 'ios' ? 50 : theme.spacing.lg,
      paddingBottom: theme.spacing.md,
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
    },
    trailerTitle: {
      ...theme.typography.body.lg,
      color: '#ffffff',
      flex: 1,
      marginRight: theme.spacing.md,
    },
    closeButton: {
      width: 44,
      height: 44,
      borderRadius: 22,
      backgroundColor: 'rgba(255, 255, 255, 0.15)',
      alignItems: 'center',
      justifyContent: 'center',
    },
    playerContainer: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: '#000000',
    },
    videoPlayer: {
      width: SCREEN_WIDTH,
      height: VIDEO_HEIGHT,
    },
    bottomBar: {
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.xl,
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
    },
    bufferingOverlay: {
      ...StyleSheet.absoluteFillObject,
      alignItems: 'center',
      justifyContent: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
    },
    errorContainer: {
      flex: 1,
      alignItems: 'center',
      justifyContent: 'center',
      gap: theme.spacing.md,
      padding: theme.spacing.xl,
    },
    errorText: {
      ...theme.typography.body.md,
      color: 'rgba(255, 255, 255, 0.7)',
      textAlign: 'center',
    },
    externalButton: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.accent.primary,
      marginTop: theme.spacing.md,
    },
    externalButtonText: {
      ...theme.typography.label.md,
      color: theme.colors.text.inverse,
    },
  });
