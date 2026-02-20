import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useDownloads, type DownloadItem } from '@/components/DownloadsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { Image as ProxiedImage } from '@/components/Image';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { useRouter } from 'expo-router';
import { useCallback, useMemo, useState } from 'react';
import { Alert, FlatList, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { launchNativePlayer } from '../details/playback';
import { ResumePlaybackModal } from '../details/resume-modal';
import { buildItemIdForProgress, checkResumeProgress } from '../details/checkResumeProgress';

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const formatBytes = (bytes: number): string => {
  if (bytes === 0) return '0 B';
  const k = 1024;
  const sizes = ['B', 'KB', 'MB', 'GB'];
  const i = Math.floor(Math.log(bytes) / Math.log(k));
  return `${(bytes / Math.pow(k, i)).toFixed(1)} ${sizes[i]}`;
};

const formatEta = (item: DownloadItem): string => {
  if (item.bytesPerSecond <= 0 || item.fileSize <= 0) return '';
  const remaining = item.fileSize - item.bytesWritten;
  if (remaining <= 0) return '';
  const seconds = Math.round(remaining / item.bytesPerSecond);
  if (seconds < 60) return `${seconds}s left`;
  if (seconds < 3600) return `${Math.round(seconds / 60)}m left`;
  const h = Math.floor(seconds / 3600);
  const m = Math.round((seconds % 3600) / 60);
  return m > 0 ? `${h}h ${m}m left` : `${h}h left`;
};

const formatSpeed = (bytesPerSecond: number): string => {
  if (bytesPerSecond <= 0) return '';
  return `${formatBytes(bytesPerSecond)}/s`;
};

const formatEpisodeLabel = (item: DownloadItem): string => {
  if (item.mediaType !== 'episode') return '';
  const s = String(item.seasonNumber ?? 0).padStart(2, '0');
  const e = String(item.episodeNumber ?? 0).padStart(2, '0');
  return `S${s}E${e}`;
};

// ---------------------------------------------------------------------------
// DownloadRow
// ---------------------------------------------------------------------------

function DownloadRow({
  item,
  theme,
  styles,
  onPlay,
  onPause,
  onResume,
  onDelete,
}: {
  item: DownloadItem;
  theme: NovaTheme;
  styles: ReturnType<typeof createStyles>;
  onPlay: (item: DownloadItem) => void;
  onPause: (id: string) => void;
  onResume: (id: string) => void;
  onDelete: (id: string) => void;
}) {
  const isActive = item.status === 'downloading' || item.status === 'paused' || item.status === 'pending';
  const isCompleted = item.status === 'completed';
  const isError = item.status === 'error';

  const handlePress = () => {
    if (isCompleted) {
      onPlay(item);
    } else if (item.status === 'downloading') {
      onPause(item.id);
    } else if (item.status === 'paused' || isError) {
      onResume(item.id);
    }
  };

  const handleLongPress = () => {
    Alert.alert('Delete Download', `Remove "${item.title}"?`, [
      { text: 'Cancel', style: 'cancel' },
      { text: 'Delete', style: 'destructive', onPress: () => onDelete(item.id) },
    ]);
  };

  const statusIcon = (): keyof typeof Ionicons.glyphMap => {
    switch (item.status) {
      case 'downloading':
        return 'pause-circle';
      case 'paused':
        return 'play-circle';
      case 'completed':
        return 'play-circle';
      case 'error':
        return 'refresh-circle';
      case 'pending':
        return 'hourglass';
      default:
        return 'ellipse';
    }
  };

  const statusColor = (): string => {
    switch (item.status) {
      case 'downloading':
        return theme.colors.accent.primary;
      case 'completed':
        return theme.colors.status.success;
      case 'error':
        return theme.colors.status.danger;
      default:
        return theme.colors.text.muted;
    }
  };

  const episodeLabel = formatEpisodeLabel(item);
  const subtitle = item.seriesTitle
    ? `${item.seriesTitle} ${episodeLabel}${item.episodeName ? ` \u2022 ${item.episodeName}` : ''}`
    : formatBytes(item.fileSize);

  return (
    <Pressable style={styles.row} onPress={handlePress} onLongPress={handleLongPress}>
      <ProxiedImage
        source={{ uri: item.posterUrl }}
        style={styles.poster}
      />
      <View style={styles.rowInfo}>
        <Text style={styles.rowTitle} numberOfLines={1}>
          {item.title}
        </Text>
        <Text style={styles.rowSubtitle} numberOfLines={1}>
          {subtitle}
        </Text>
        {isActive && (
          <View style={styles.progressBarContainer}>
            <View style={[styles.progressBarFill, { width: `${Math.round(item.progress * 100)}%` }]} />
          </View>
        )}
        {isActive && (
          <Text style={styles.rowMeta}>
            {item.status === 'pending'
              ? 'Waiting...'
              : item.status === 'paused'
                ? `Paused \u2022 ${formatBytes(item.bytesWritten)} / ${formatBytes(item.fileSize)}`
                : `${Math.round(item.progress * 100)}% \u2022 ${formatBytes(item.bytesWritten)} / ${formatBytes(item.fileSize)}`}
          </Text>
        )}
        {item.status === 'downloading' && (formatSpeed(item.bytesPerSecond) || formatEta(item)) && (
          <Text style={styles.rowMeta}>
            {[formatSpeed(item.bytesPerSecond), formatEta(item)].filter(Boolean).join(' \u2022 ')}
          </Text>
        )}
        {isCompleted && (
          <Text style={styles.rowMeta}>{formatBytes(item.fileSize)}</Text>
        )}
        {isError && (
          <Text style={[styles.rowMeta, { color: theme.colors.status.danger }]}>
            {item.errorMessage || 'Download failed'} \u2022 Tap to retry
          </Text>
        )}
      </View>
      <View style={styles.rowAction}>
        <Ionicons name={statusIcon()} size={28} color={statusColor()} />
      </View>
      <Pressable style={styles.deleteButton} onPress={() => onDelete(item.id)} hitSlop={8}>
        <Ionicons name="trash-outline" size={20} color={theme.colors.text.muted} />
      </Pressable>
    </Pressable>
  );
}

// ---------------------------------------------------------------------------
// Screen
// ---------------------------------------------------------------------------

export default function DownloadsScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const router = useRouter();
  const { items, pauseDownload, resumeDownload, deleteDownload } = useDownloads();
  const { activeUserId } = useUserProfiles();
  const { settings, userSettings } = useBackendSettings();

  // Resume modal state
  const [resumeModalVisible, setResumeModalVisible] = useState(false);
  const [pendingResumeItem, setPendingResumeItem] = useState<DownloadItem | null>(null);
  const [pendingResumePosition, setPendingResumePosition] = useState(0);
  const [resumePercent, setResumePercent] = useState(0);

  const activeItems = useMemo(
    () => items.filter((i) => i.status === 'downloading' || i.status === 'paused' || i.status === 'pending'),
    [items],
  );
  const completedItems = useMemo(
    () => items.filter((i) => i.status === 'completed'),
    [items],
  );
  const errorItems = useMemo(
    () => items.filter((i) => i.status === 'error'),
    [items],
  );
  const allItems = useMemo(
    () => [...activeItems, ...errorItems, ...completedItems],
    [activeItems, errorItems, completedItems],
  );

  const launchDownloadedItem = useCallback(
    (item: DownloadItem, startOffset?: number) => {
      launchNativePlayer(item.localFilePath, item.posterUrl, item.title, router, {
        mediaType: item.mediaType,
        seriesTitle: item.seriesTitle,
        seasonNumber: item.seasonNumber,
        episodeNumber: item.episodeNumber,
        episodeName: item.episodeName,
        titleId: item.titleId,
        imdbId: item.imdbId,
        tvdbId: item.tvdbId,
        seriesIdentifier: item.seriesIdentifier,
        userId: activeUserId || undefined,
        ...(typeof startOffset === 'number' ? { startOffset } : {}),
        useNativePlayer: true,
      });
    },
    [router, activeUserId],
  );

  const handlePlay = useCallback(
    async (item: DownloadItem) => {
      // Check for resume progress
      if (activeUserId) {
        const itemId = buildItemIdForProgress({
          mediaType: item.mediaType,
          titleId: item.titleId,
          seriesIdentifier: item.seriesIdentifier,
          seasonNumber: item.seasonNumber,
          episodeNumber: item.episodeNumber,
        });
        if (itemId) {
          const progress = await checkResumeProgress(activeUserId, item.mediaType, itemId);
          if (progress) {
            setPendingResumeItem(item);
            setPendingResumePosition(progress.position);
            setResumePercent(progress.percentWatched);
            setResumeModalVisible(true);
            return;
          }
        }
      }
      // No progress found — play from start
      launchDownloadedItem(item);
    },
    [activeUserId, launchDownloadedItem],
  );

  const handleResumePlayback = useCallback(() => {
    if (!pendingResumeItem) return;
    const rewindAmount =
      (userSettings?.playback as any)?.rewindOnPlaybackStart ?? settings?.playback?.rewindOnPlaybackStart ?? 0;
    const resumePosition = Math.max(0, pendingResumePosition - rewindAmount);
    launchDownloadedItem(pendingResumeItem, resumePosition);
    setResumeModalVisible(false);
    setPendingResumeItem(null);
  }, [pendingResumeItem, pendingResumePosition, launchDownloadedItem, userSettings, settings]);

  const handlePlayFromBeginning = useCallback(() => {
    if (!pendingResumeItem) return;
    launchDownloadedItem(pendingResumeItem);
    setResumeModalVisible(false);
    setPendingResumeItem(null);
  }, [pendingResumeItem, launchDownloadedItem]);

  const handleCloseResumeModal = useCallback(() => {
    setResumeModalVisible(false);
    setPendingResumeItem(null);
  }, []);

  const handleDelete = useCallback(
    (id: string) => {
      const item = items.find((i) => i.id === id);
      Alert.alert('Delete Download', `Remove "${item?.title}"?`, [
        { text: 'Cancel', style: 'cancel' },
        { text: 'Delete', style: 'destructive', onPress: () => deleteDownload(id) },
      ]);
    },
    [items, deleteDownload],
  );

  const renderItem = useCallback(
    ({ item }: { item: DownloadItem }) => (
      <DownloadRow
        item={item}
        theme={theme}
        styles={styles}
        onPlay={handlePlay}
        onPause={pauseDownload}
        onResume={resumeDownload}
        onDelete={handleDelete}
      />
    ),
    [theme, styles, handlePlay, pauseDownload, resumeDownload, handleDelete],
  );

  const keyExtractor = useCallback((item: DownloadItem) => item.id, []);

  return (
    <FixedSafeAreaView style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Downloads</Text>
      </View>
      {allItems.length === 0 ? (
        <View style={styles.emptyContainer}>
          <Ionicons name="cloud-download-outline" size={64} color={theme.colors.text.disabled} />
          <Text style={styles.emptyTitle}>No Downloads</Text>
          <Text style={styles.emptySubtitle}>
            Download movies and episodes for offline playback from the details page.
          </Text>
        </View>
      ) : (
        <FlatList
          data={allItems}
          renderItem={renderItem}
          keyExtractor={keyExtractor}
          contentContainerStyle={styles.list}
          showsVerticalScrollIndicator={false}
        />
      )}
      <ResumePlaybackModal
        visible={resumeModalVisible}
        onClose={handleCloseResumeModal}
        onResume={handleResumePlayback}
        onPlayFromBeginning={handlePlayFromBeginning}
        theme={theme}
        percentWatched={resumePercent}
      />
    </FixedSafeAreaView>
  );
}

// ---------------------------------------------------------------------------
// Styles
// ---------------------------------------------------------------------------

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    container: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    header: {
      paddingHorizontal: theme.spacing.lg,
      paddingTop: theme.spacing.md,
      paddingBottom: theme.spacing.sm,
    },
    headerTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    list: {
      paddingHorizontal: theme.spacing.md,
      paddingBottom: theme.spacing['2xl'],
    },
    row: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.sm,
      marginBottom: theme.spacing.xs,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
    },
    poster: {
      width: 48,
      height: 72,
      borderRadius: theme.radius.sm,
    },
    rowInfo: {
      flex: 1,
      marginLeft: theme.spacing.md,
      marginRight: theme.spacing.sm,
    },
    rowTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },
    rowSubtitle: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      marginTop: 2,
    },
    rowMeta: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      marginTop: 4,
    },
    progressBarContainer: {
      height: 3,
      backgroundColor: theme.colors.background.elevated,
      borderRadius: 1.5,
      marginTop: 6,
      overflow: 'hidden',
    },
    progressBarFill: {
      height: '100%',
      backgroundColor: theme.colors.accent.primary,
      borderRadius: 1.5,
    },
    rowAction: {
      padding: theme.spacing.xs,
    },
    deleteButton: {
      padding: theme.spacing.xs,
      marginLeft: theme.spacing.xs,
    },
    emptyContainer: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      paddingHorizontal: theme.spacing['2xl'],
    },
    emptyTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      marginTop: theme.spacing.lg,
    },
    emptySubtitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.muted,
      textAlign: 'center',
      marginTop: theme.spacing.sm,
    },
  });
