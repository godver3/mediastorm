import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useDownloads, type DownloadItem } from '@/components/DownloadsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { Image as ProxiedImage } from '@/components/Image';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useRouter } from 'expo-router';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { Alert, FlatList, Platform, Pressable, StyleSheet, Switch, Text, View } from 'react-native';
import { launchNativePlayer } from '../details/playback';
import { ResumePlaybackModal } from '../details/resume-modal';
import { buildItemIdForProgress, checkResumeProgress } from '../details/checkResumeProgress';

// ---------------------------------------------------------------------------
// Sort types
// ---------------------------------------------------------------------------

type SortOption = 'date-desc' | 'date-asc' | 'name-asc' | 'name-desc';

const SORT_OPTIONS: { key: SortOption; label: string; icon: keyof typeof Ionicons.glyphMap }[] = [
  { key: 'date-desc', label: 'Newest First', icon: 'arrow-down' },
  { key: 'date-asc', label: 'Oldest First', icon: 'arrow-up' },
  { key: 'name-asc', label: 'Name A\u2013Z', icon: 'arrow-down' },
  { key: 'name-desc', label: 'Name Z\u2013A', icon: 'arrow-up' },
];

const SORT_STORAGE_KEY = 'strmr.downloads.sort';

// ---------------------------------------------------------------------------
// List row types
// ---------------------------------------------------------------------------

type ListRow =
  | { type: 'show-header'; key: string; showKey: string; title: string; posterUrl: string; episodeCount: number }
  | { type: 'season-header'; key: string; showKey: string; seasonKey: string; seasonNumber: number; episodeCount: number }
  | { type: 'episode'; key: string; item: DownloadItem }
  | { type: 'movie'; key: string; item: DownloadItem };

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

const getShowKey = (item: DownloadItem): string =>
  item.seriesTitle || item.seriesIdentifier || item.title;

const getSortName = (item: DownloadItem): string =>
  (item.seriesTitle || item.title).toLowerCase();

// ---------------------------------------------------------------------------
// ShowHeader
// ---------------------------------------------------------------------------

function ShowHeader({
  title,
  posterUrl,
  episodeCount,
  expanded,
  onPress,
  theme,
  styles,
}: {
  title: string;
  posterUrl: string;
  episodeCount: number;
  expanded: boolean;
  onPress: () => void;
  theme: NovaTheme;
  styles: ReturnType<typeof createStyles>;
}) {
  return (
    <Pressable style={styles.showHeader} onPress={onPress}>
      <ProxiedImage source={{ uri: posterUrl }} style={styles.showHeaderPoster} />
      <View style={styles.showHeaderInfo}>
        <Text style={styles.showHeaderTitle} numberOfLines={1}>{title}</Text>
        <Text style={styles.showHeaderCount}>
          {episodeCount} episode{episodeCount !== 1 ? 's' : ''}
        </Text>
      </View>
      <Ionicons
        name={expanded ? 'chevron-up' : 'chevron-down'}
        size={20}
        color={theme.colors.text.muted}
      />
    </Pressable>
  );
}

// ---------------------------------------------------------------------------
// SeasonHeader
// ---------------------------------------------------------------------------

function SeasonHeader({
  seasonNumber,
  episodeCount,
  expanded,
  onPress,
  theme,
  styles,
}: {
  seasonNumber: number;
  episodeCount: number;
  expanded: boolean;
  onPress: () => void;
  theme: NovaTheme;
  styles: ReturnType<typeof createStyles>;
}) {
  return (
    <Pressable style={styles.seasonHeader} onPress={onPress}>
      <Text style={styles.seasonHeaderTitle}>Season {seasonNumber}</Text>
      <Text style={styles.seasonHeaderCount}>
        {episodeCount} episode{episodeCount !== 1 ? 's' : ''}
      </Text>
      <Ionicons
        name={expanded ? 'chevron-up' : 'chevron-down'}
        size={16}
        color={theme.colors.text.muted}
      />
    </Pressable>
  );
}

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
  selectMode,
  isSelected,
  onToggleSelect,
  onEnterSelectMode,
  indented,
}: {
  item: DownloadItem;
  theme: NovaTheme;
  styles: ReturnType<typeof createStyles>;
  onPlay: (item: DownloadItem) => void;
  onPause: (id: string) => void;
  onResume: (id: string) => void;
  onDelete: (id: string) => void;
  selectMode: boolean;
  isSelected: boolean;
  onToggleSelect: (id: string) => void;
  onEnterSelectMode: (id: string) => void;
  indented?: boolean;
}) {
  const isActive = item.status === 'downloading' || item.status === 'paused' || item.status === 'pending';
  const isCompleted = item.status === 'completed';
  const isError = item.status === 'error';

  const handlePress = () => {
    if (selectMode) {
      onToggleSelect(item.id);
      return;
    }
    if (isCompleted) {
      onPlay(item);
    } else if (item.status === 'downloading') {
      onPause(item.id);
    } else if (item.status === 'paused' || isError) {
      onResume(item.id);
    }
  };

  const handleLongPress = () => {
    if (selectMode) return;
    onEnterSelectMode(item.id);
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
    <Pressable
      style={[styles.row, indented && styles.rowIndented, selectMode && isSelected && styles.rowSelected]}
      onPress={handlePress}
      onLongPress={handleLongPress}
    >
      <View>
        <ProxiedImage
          source={{ uri: item.posterUrl }}
          style={styles.poster}
        />
        {selectMode && (
          <View style={styles.checkboxOverlay}>
            <Ionicons
              name={isSelected ? 'checkbox' : 'square-outline'}
              size={22}
              color={isSelected ? theme.colors.accent.primary : theme.colors.text.muted}
            />
          </View>
        )}
      </View>
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
      {!selectMode && (
        <>
          <View style={styles.rowAction}>
            <Ionicons name={statusIcon()} size={28} color={statusColor()} />
          </View>
          <Pressable style={styles.deleteButton} onPress={() => onDelete(item.id)} hitSlop={8}>
            <Ionicons name="trash-outline" size={20} color={theme.colors.text.muted} />
          </Pressable>
        </>
      )}
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
  const { items, wifiOnly, setWifiOnly, maxWorkers, setMaxWorkers, pauseDownload, resumeDownload, deleteDownload } = useDownloads();
  const { activeUserId } = useUserProfiles();
  const { settings, userSettings } = useBackendSettings();

  // Resume modal state
  const [resumeModalVisible, setResumeModalVisible] = useState(false);
  const [pendingResumeItem, setPendingResumeItem] = useState<DownloadItem | null>(null);
  const [pendingResumePosition, setPendingResumePosition] = useState(0);
  const [resumePercent, setResumePercent] = useState(0);

  // Sort state
  const [sortOption, setSortOption] = useState<SortOption>('date-desc');

  useEffect(() => {
    AsyncStorage.getItem(SORT_STORAGE_KEY).then((v) => {
      if (v && SORT_OPTIONS.some((o) => o.key === v)) {
        setSortOption(v as SortOption);
      }
    });
  }, []);

  const currentSortConfig = SORT_OPTIONS.find((o) => o.key === sortOption) ?? SORT_OPTIONS[0];

  const cycleSortOption = useCallback(() => {
    setSortOption((prev) => {
      const idx = SORT_OPTIONS.findIndex((o) => o.key === prev);
      const next = SORT_OPTIONS[(idx + 1) % SORT_OPTIONS.length].key;
      AsyncStorage.setItem(SORT_STORAGE_KEY, next);
      return next;
    });
  }, []);

  // Sorted items (replaces old active/error/completed split)
  const sortedItems = useMemo(() => {
    const sorted = [...items];
    switch (sortOption) {
      case 'date-desc':
        sorted.sort((a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime());
        break;
      case 'date-asc':
        sorted.sort((a, b) => new Date(a.createdAt).getTime() - new Date(b.createdAt).getTime());
        break;
      case 'name-asc':
        sorted.sort((a, b) => getSortName(a).localeCompare(getSortName(b)));
        break;
      case 'name-desc':
        sorted.sort((a, b) => getSortName(b).localeCompare(getSortName(a)));
        break;
    }
    return sorted;
  }, [items, sortOption]);

  // Expand/collapse state for TV show grouping
  const [expandedShows, setExpandedShows] = useState<Record<string, boolean>>({});
  const [expandedSeasons, setExpandedSeasons] = useState<Record<string, boolean>>({});

  const hasEpisodes = useMemo(() => sortedItems.some((i) => i.mediaType === 'episode'), [sortedItems]);
  const anyShowExpanded = useMemo(() => Object.values(expandedShows).some((v) => v), [expandedShows]);

  const toggleShowExpand = useCallback((showKey: string) => {
    setExpandedShows((prev) => ({ ...prev, [showKey]: !prev[showKey] }));
  }, []);

  const toggleSeasonExpand = useCallback((seasonKey: string) => {
    setExpandedSeasons((prev) => ({
      ...prev,
      [seasonKey]: prev[seasonKey] === false,
    }));
  }, []);

  const toggleExpandAll = useCallback(() => {
    if (anyShowExpanded) {
      setExpandedShows({});
    } else {
      const shows: Record<string, boolean> = {};
      for (const item of sortedItems) {
        if (item.mediaType === 'episode') {
          shows[getShowKey(item)] = true;
        }
      }
      setExpandedShows(shows);
    }
  }, [anyShowExpanded, sortedItems]);

  // Build grouped list data
  const listData = useMemo(() => {
    const rows: ListRow[] = [];
    const emittedShows = new Set<string>();

    for (const item of sortedItems) {
      if (item.mediaType !== 'episode') {
        rows.push({ type: 'movie', key: item.id, item });
        continue;
      }

      const showKey = getShowKey(item);
      if (emittedShows.has(showKey)) continue;
      emittedShows.add(showKey);

      // Collect all episodes for this show
      const showEpisodes = sortedItems.filter(
        (i) => i.mediaType === 'episode' && getShowKey(i) === showKey,
      );

      // Group by season
      const seasonMap = new Map<number, DownloadItem[]>();
      for (const ep of showEpisodes) {
        const season = ep.seasonNumber ?? 0;
        if (!seasonMap.has(season)) seasonMap.set(season, []);
        seasonMap.get(season)!.push(ep);
      }
      const seasons = [...seasonMap.keys()].sort((a, b) => a - b);
      const multiSeason = seasons.length > 1;

      rows.push({
        type: 'show-header',
        key: `show:${showKey}`,
        showKey,
        title: item.seriesTitle || item.title,
        posterUrl: item.posterUrl,
        episodeCount: showEpisodes.length,
      });

      if (!expandedShows[showKey]) continue;

      for (const season of seasons) {
        const episodes = seasonMap.get(season)!;
        episodes.sort((a, b) => (a.episodeNumber ?? 0) - (b.episodeNumber ?? 0));

        const seasonKey = `${showKey}:S${season}`;

        if (multiSeason) {
          rows.push({
            type: 'season-header',
            key: `season:${seasonKey}`,
            showKey,
            seasonKey,
            seasonNumber: season,
            episodeCount: episodes.length,
          });

          if (expandedSeasons[seasonKey] === false) continue;
        }

        for (const ep of episodes) {
          rows.push({ type: 'episode', key: ep.id, item: ep });
        }
      }
    }

    return rows;
  }, [sortedItems, expandedShows, expandedSeasons]);

  // Multi-select state
  const [selectMode, setSelectMode] = useState(false);
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const exitSelectMode = useCallback(() => {
    setSelectMode(false);
    setSelected(new Set());
  }, []);

  const enterSelectMode = useCallback((id: string) => {
    setSelectMode(true);
    setSelected(new Set([id]));
  }, []);

  const toggleSelect = useCallback((id: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(id)) {
        next.delete(id);
      } else {
        next.add(id);
      }
      if (next.size === 0) {
        setSelectMode(false);
      }
      return next;
    });
  }, []);

  const toggleSelectAll = useCallback(() => {
    if (selected.size === sortedItems.length) {
      setSelected(new Set());
      setSelectMode(false);
    } else {
      setSelected(new Set(sortedItems.map((i) => i.id)));
    }
  }, [sortedItems, selected.size]);

  const handleBulkDelete = useCallback(() => {
    const count = selected.size;
    Alert.alert(`Delete ${count} Download${count > 1 ? 's' : ''}`, `Remove ${count} selected download${count > 1 ? 's' : ''}?`, [
      { text: 'Cancel', style: 'cancel' },
      {
        text: 'Delete',
        style: 'destructive',
        onPress: () => {
          for (const id of selected) {
            deleteDownload(id);
          }
          exitSelectMode();
        },
      },
    ]);
  }, [selected, deleteDownload, exitSelectMode]);

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

  const renderRow = useCallback(
    ({ item: row }: { item: ListRow }) => {
      switch (row.type) {
        case 'show-header':
          return (
            <ShowHeader
              title={row.title}
              posterUrl={row.posterUrl}
              episodeCount={row.episodeCount}
              expanded={!!expandedShows[row.showKey]}
              onPress={() => toggleShowExpand(row.showKey)}
              theme={theme}
              styles={styles}
            />
          );
        case 'season-header':
          return (
            <SeasonHeader
              seasonNumber={row.seasonNumber}
              episodeCount={row.episodeCount}
              expanded={expandedSeasons[row.seasonKey] !== false}
              onPress={() => toggleSeasonExpand(row.seasonKey)}
              theme={theme}
              styles={styles}
            />
          );
        case 'episode':
          return (
            <DownloadRow
              item={row.item}
              theme={theme}
              styles={styles}
              onPlay={handlePlay}
              onPause={pauseDownload}
              onResume={resumeDownload}
              onDelete={handleDelete}
              selectMode={selectMode}
              isSelected={selected.has(row.item.id)}
              onToggleSelect={toggleSelect}
              onEnterSelectMode={enterSelectMode}
              indented
            />
          );
        case 'movie':
          return (
            <DownloadRow
              item={row.item}
              theme={theme}
              styles={styles}
              onPlay={handlePlay}
              onPause={pauseDownload}
              onResume={resumeDownload}
              onDelete={handleDelete}
              selectMode={selectMode}
              isSelected={selected.has(row.item.id)}
              onToggleSelect={toggleSelect}
              onEnterSelectMode={enterSelectMode}
            />
          );
      }
    },
    [theme, styles, handlePlay, pauseDownload, resumeDownload, handleDelete, selectMode, selected, toggleSelect, enterSelectMode, expandedShows, expandedSeasons, toggleShowExpand, toggleSeasonExpand],
  );

  const keyExtractor = useCallback((row: ListRow) => row.key, []);

  return (
    <FixedSafeAreaView style={styles.container}>
      {selectMode ? (
        <View style={styles.selectHeader}>
          <Pressable onPress={exitSelectMode} hitSlop={8}>
            <Text style={styles.selectHeaderAction}>Cancel</Text>
          </Pressable>
          <Text style={styles.headerTitle}>{selected.size} selected</Text>
          <Pressable onPress={toggleSelectAll} hitSlop={8}>
            <Text style={styles.selectHeaderAction}>
              {selected.size === sortedItems.length ? 'Deselect All' : 'Select All'}
            </Text>
          </Pressable>
        </View>
      ) : (
        <View style={styles.header}>
          <Text style={styles.headerTitle}>Downloads</Text>
          {hasEpisodes && (
            <Pressable onPress={toggleExpandAll} hitSlop={8}>
              <Ionicons
                name={anyShowExpanded ? 'contract-outline' : 'expand-outline'}
                size={22}
                color={theme.colors.text.secondary}
              />
            </Pressable>
          )}
        </View>
      )}
      <View style={styles.settingsRow}>
        <Text style={styles.settingsLabel}>Wi-Fi Only</Text>
        <Switch
          value={wifiOnly}
          onValueChange={setWifiOnly}
          trackColor={{ false: theme.colors.background.elevated, true: theme.colors.accent.primary }}
          thumbColor={theme.colors.text.primary}
        />
      </View>
      <View style={styles.settingsRow}>
        <Text style={styles.settingsLabel}>Simultaneous Downloads</Text>
        <View style={styles.stepper}>
          <Pressable
            style={[styles.stepperButton, maxWorkers <= 1 && styles.stepperButtonDisabled]}
            onPress={() => maxWorkers > 1 && setMaxWorkers(maxWorkers - 1)}
            hitSlop={8}
          >
            <Ionicons name="remove" size={18} color={maxWorkers <= 1 ? theme.colors.text.disabled : theme.colors.text.primary} />
          </Pressable>
          <Text style={styles.stepperValue}>{maxWorkers}</Text>
          <Pressable
            style={[styles.stepperButton, maxWorkers >= 3 && styles.stepperButtonDisabled]}
            onPress={() => maxWorkers < 3 && setMaxWorkers(maxWorkers + 1)}
            hitSlop={8}
          >
            <Ionicons name="add" size={18} color={maxWorkers >= 3 ? theme.colors.text.disabled : theme.colors.text.primary} />
          </Pressable>
        </View>
      </View>
      <View style={styles.settingsRow}>
        <Text style={styles.settingsLabel}>Sort By</Text>
        <Pressable style={styles.sortPill} onPress={cycleSortOption}>
          <Ionicons name={currentSortConfig.icon} size={14} color={theme.colors.text.primary} />
          <Text style={styles.sortPillText}>{currentSortConfig.label}</Text>
          <Ionicons name="swap-vertical" size={14} color={theme.colors.text.muted} />
        </Pressable>
      </View>
      {sortedItems.length === 0 ? (
        <View style={styles.emptyContainer}>
          <Ionicons name="cloud-download-outline" size={64} color={theme.colors.text.disabled} />
          <Text style={styles.emptyTitle}>No Downloads</Text>
          <Text style={styles.emptySubtitle}>
            Download movies and episodes for offline playback from the details page.
          </Text>
        </View>
      ) : (
        <FlatList
          data={listData}
          renderItem={renderRow}
          keyExtractor={keyExtractor}
          contentContainerStyle={[styles.list, selectMode && selected.size > 0 && styles.listWithBar]}
          showsVerticalScrollIndicator={false}
        />
      )}
      {selectMode && selected.size > 0 && (
        <View style={styles.selectionBar}>
          <Pressable style={styles.selectionBarButton} onPress={handleBulkDelete}>
            <Text style={styles.selectionBarButtonText}>Delete ({selected.size})</Text>
          </Pressable>
        </View>
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
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      paddingHorizontal: theme.spacing.lg,
      paddingTop: theme.spacing.md,
      paddingBottom: theme.spacing.sm,
    },
    headerTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    settingsRow: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.sm,
    },
    settingsLabel: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    stepper: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
    },
    stepperButton: {
      width: 30,
      height: 30,
      borderRadius: theme.radius.sm,
      backgroundColor: theme.colors.background.elevated,
      justifyContent: 'center',
      alignItems: 'center',
    },
    stepperButtonDisabled: {
      opacity: 0.4,
    },
    stepperValue: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      minWidth: 16,
      textAlign: 'center',
    },
    sortPill: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: 6,
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
      backgroundColor: theme.colors.background.elevated,
    },
    sortPillText: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    selectHeader: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      paddingHorizontal: theme.spacing.lg,
      paddingTop: theme.spacing.md,
      paddingBottom: theme.spacing.sm,
    },
    selectHeaderAction: {
      ...theme.typography.body.md,
      color: theme.colors.accent.primary,
    },
    list: {
      paddingHorizontal: theme.spacing.md,
      paddingBottom: theme.spacing['2xl'],
    },
    listWithBar: {
      paddingBottom: 80,
    },
    // Show header
    showHeader: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.sm,
      marginBottom: theme.spacing.xs,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
    },
    showHeaderPoster: {
      width: 36,
      height: 54,
      borderRadius: theme.radius.sm,
    },
    showHeaderInfo: {
      flex: 1,
      marginLeft: theme.spacing.md,
      marginRight: theme.spacing.sm,
    },
    showHeaderTitle: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },
    showHeaderCount: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      marginTop: 2,
    },
    // Season header
    seasonHeader: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingVertical: theme.spacing.xs,
      paddingHorizontal: theme.spacing.sm,
      marginLeft: theme.spacing.xl,
      marginBottom: theme.spacing.xs,
    },
    seasonHeaderTitle: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      fontWeight: '600',
    },
    seasonHeaderCount: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      marginLeft: theme.spacing.sm,
      flex: 1,
    },
    // Download row
    row: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.sm,
      marginBottom: theme.spacing.xs,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
    },
    rowIndented: {
      marginLeft: theme.spacing.xl,
    },
    rowSelected: {
      backgroundColor: theme.colors.background.elevated,
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
    checkboxOverlay: {
      position: 'absolute',
      bottom: -4,
      right: -4,
      backgroundColor: theme.colors.background.base,
      borderRadius: 4,
    },
    selectionBar: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md,
      paddingBottom: theme.spacing.lg,
      backgroundColor: theme.colors.background.surface,
      borderTopWidth: 1,
      borderTopColor: theme.colors.background.elevated,
      alignItems: 'center',
    },
    selectionBarButton: {
      backgroundColor: theme.colors.status.danger,
      paddingHorizontal: theme.spacing['2xl'],
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.md,
    },
    selectionBarButtonText: {
      ...theme.typography.label.md,
      color: '#fff',
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
