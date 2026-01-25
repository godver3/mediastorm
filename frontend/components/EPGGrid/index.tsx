import React, { useCallback, useEffect, useMemo, useState } from 'react';
import {
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';
import { Image } from '@/components/Image';
import { Ionicons } from '@expo/vector-icons';

import { LiveChannel } from '@/hooks/useLiveChannels';
import {
  useEPGGrid,
  GridProgram,
  EPG_GRID_SLOT_MINUTES,
  formatTimeSlot,
} from '@/hooks/useEPGGrid';
import { useTheme } from '@/theme';
import type { NovaTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';
import {
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
} from '@/services/tv-navigation';

// Grid dimensions
const CHANNEL_COLUMN_WIDTH = Platform.isTV ? tvScale(220, 180) : 120;
const TIME_SLOT_WIDTH = Platform.isTV ? tvScale(150, 120) : 100;
const ROW_HEIGHT = Platform.isTV ? tvScale(70, 56) : 50;
const HEADER_HEIGHT = Platform.isTV ? tvScale(50, 40) : 36;
const TIME_INDICATOR_WIDTH = 2;

interface EPGGridProps {
  channels: LiveChannel[];
  onChannelSelect: (channel: LiveChannel) => void;
  favoriteChannelIds: Set<string>;
}

/**
 * EPG Grid View - Single unified scrollable table.
 * Channel column shrinks as you scroll right, then content scrolls.
 */
export const EPGGrid = ({
  channels,
  onChannelSelect,
  favoriteChannelIds,
}: EPGGridProps) => {
  const theme = useTheme();
  const isTV = Platform.isTV;

  const {
    schedules,
    loading,
    gridState,
    isEnabled,
    fetchSchedules,
    scrollTimeForward,
    scrollTimeBackward,
    jumpToNow,
    getTimeSlots,
    getCurrentTimePosition,
  } = useEPGGrid();

  const [currentTimePosition, setCurrentTimePosition] = useState<number | null>(null);

  // Fixed channel width
  const channelWidth = CHANNEL_COLUMN_WIDTH;

  // Calculate grid width
  const totalSlots = (gridState.timeWindowHours * 60) / EPG_GRID_SLOT_MINUTES;
  const gridContentWidth = totalSlots * TIME_SLOT_WIDTH;

  // Total width
  const totalWidth = channelWidth + gridContentWidth;

  // Fetch EPG data when channels change or time window changes
  useEffect(() => {
    const tvgIds = channels
      .filter((ch) => ch.tvgId)
      .map((ch) => ch.tvgId!);
    if (tvgIds.length > 0) {
      fetchSchedules(tvgIds);
    }
  }, [channels, fetchSchedules, gridState.timeWindowStart, gridState.timeWindowHours]);

  // Update current time indicator position every minute
  useEffect(() => {
    const updatePosition = () => {
      setCurrentTimePosition(getCurrentTimePosition());
    };
    updatePosition();
    const interval = setInterval(updatePosition, 60000);
    return () => clearInterval(interval);
  }, [getCurrentTimePosition]);

  const timeSlots = useMemo(() => getTimeSlots(), [getTimeSlots]);

  const handleChannelPress = useCallback(
    (channel: LiveChannel) => {
      onChannelSelect(channel);
    },
    [onChannelSelect],
  );


  const styles = useMemo(() => createStyles(theme), [theme]);

  if (!isEnabled) {
    return (
      <View style={styles.disabledContainer}>
        <Ionicons name="calendar-outline" size={48} color={theme.colors.text.muted} />
        <Text style={styles.disabledText}>EPG is not configured</Text>
        <Text style={styles.disabledSubtext}>
          Add an EPG source in settings to enable the program guide
        </Text>
      </View>
    );
  }

  return (
    <View style={styles.container}>
      {/* Time navigation controls */}
      <View style={styles.navBar}>
        <Pressable style={styles.navButton} onPress={scrollTimeBackward}>
          <Ionicons name="chevron-back" size={20} color={theme.colors.text.primary} />
        </Pressable>
        <Pressable style={styles.navButtonNow} onPress={jumpToNow}>
          <Text style={styles.navButtonText}>Now</Text>
        </Pressable>
        <Pressable style={styles.navButton} onPress={scrollTimeForward}>
          <Ionicons name="chevron-forward" size={20} color={theme.colors.text.primary} />
        </Pressable>
        <Text style={styles.navDate}>
          {gridState.timeWindowStart.toLocaleDateString([], {
            weekday: 'short',
            month: 'short',
            day: 'numeric',
          })}
        </Text>
      </View>

      {/* Single horizontal scroll for entire table */}
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        style={styles.tableScroll}>

        {/* Table content */}
        <View style={{ width: totalWidth }}>
          {/* Header row */}
          <View style={styles.headerRow}>
            <View style={[styles.cornerCell, { width: channelWidth }]} />
            <View style={[styles.timeHeader, { width: gridContentWidth }]}>
              {timeSlots.map((slot, index) => (
                <View key={index} style={[styles.timeSlot, { width: TIME_SLOT_WIDTH }]}>
                  <Text style={styles.timeSlotText}>{formatTimeSlot(slot)}</Text>
                </View>
              ))}
              {currentTimePosition !== null && (
                <View style={[styles.timeIndicator, { left: `${currentTimePosition}%` }]} />
              )}
            </View>
          </View>

          {/* Vertical scroll for channel rows */}
          <ScrollView showsVerticalScrollIndicator={false} style={styles.bodyScroll}>
            {isTV ? (
              <SpatialNavigationNode orientation="vertical">
                {channels.map((channel) => (
                  <EPGRow
                    key={channel.id}
                    channel={channel}
                    programs={schedules.get(channel.tvgId || '') || []}
                    channelWidth={channelWidth}
                    gridContentWidth={gridContentWidth}
                    isFavorite={favoriteChannelIds.has(channel.id)}
                    currentTimePosition={currentTimePosition}
                    onPress={() => handleChannelPress(channel)}
                    theme={theme}
                    isTV={isTV}
                  />
                ))}
              </SpatialNavigationNode>
            ) : (
              channels.map((channel) => (
                <EPGRow
                  key={channel.id}
                  channel={channel}
                  programs={schedules.get(channel.tvgId || '') || []}
                  channelWidth={channelWidth}
                  gridContentWidth={gridContentWidth}
                  isFavorite={favoriteChannelIds.has(channel.id)}
                  currentTimePosition={currentTimePosition}
                  onPress={() => handleChannelPress(channel)}
                  theme={theme}
                  isTV={isTV}
                />
              ))
            )}
          </ScrollView>
        </View>
      </ScrollView>

      {/* Loading overlay */}
      {loading && (
        <View style={styles.loadingOverlay}>
          <Text style={styles.loadingText}>Loading guide...</Text>
        </View>
      )}
    </View>
  );
};

interface EPGRowProps {
  channel: LiveChannel;
  programs: GridProgram[];
  channelWidth: number;
  gridContentWidth: number;
  isFavorite: boolean;
  currentTimePosition: number | null;
  onPress: () => void;
  theme: NovaTheme;
  isTV: boolean;
}

const EPGRow = React.memo(function EPGRow({
  channel,
  programs,
  channelWidth,
  gridContentWidth,
  isFavorite,
  currentTimePosition,
  onPress,
  theme,
  isTV,
}: EPGRowProps) {
  const styles = useMemo(() => createStyles(theme), [theme]);

  const rowContent = (
    <>
      <View style={[styles.channelCell, { width: channelWidth }]}>
        {channel.logo ? (
          <Image
            source={{ uri: channel.logo }}
            style={styles.channelLogo}
            contentFit="contain"
          />
        ) : (
          <View style={styles.channelLogoPlaceholder}>
            <Ionicons name="tv-outline" size={18} color={theme.colors.text.muted} />
          </View>
        )}
        <Text style={styles.channelName} numberOfLines={1}>
          {channel.name}
        </Text>
        {isFavorite && (
          <Ionicons
            name="star"
            size={12}
            color={theme.colors.status.warning}
            style={styles.favoriteIcon}
          />
        )}
      </View>
      <View style={[styles.programsContainer, { width: gridContentWidth }]}>
        {programs.length > 0 ? (
          programs.map((program, index) => (
            <ProgramCell
              key={`${program.channelId}-${program.start}-${index}`}
              program={program}
              theme={theme}
            />
          ))
        ) : (
          <View style={styles.noDataCell}>
            <Text style={styles.noDataText}>No guide data</Text>
          </View>
        )}
        {currentTimePosition !== null && (
          <View style={[styles.timeIndicator, { left: `${currentTimePosition}%` }]} pointerEvents="none" />
        )}
      </View>
    </>
  );

  if (isTV) {
    return (
      <SpatialNavigationFocusableView onSelect={onPress}>
        {({ isFocused }: { isFocused: boolean }) => (
          <View style={[styles.row, isFocused && styles.rowFocused]}>
            {rowContent}
          </View>
        )}
      </SpatialNavigationFocusableView>
    );
  }

  return (
    <Pressable onPress={onPress}>
      <View style={styles.row}>{rowContent}</View>
    </Pressable>
  );
});

interface ProgramCellProps {
  program: GridProgram;
  theme: NovaTheme;
}

const ProgramCell = React.memo(function ProgramCell({ program, theme }: ProgramCellProps) {
  const styles = useMemo(() => createStyles(theme), [theme]);

  // Calculate position based on slot width
  const leftPosition = Math.round((program.gridStartMinutes / EPG_GRID_SLOT_MINUTES) * TIME_SLOT_WIDTH);
  const cellWidth = Math.round(program.columnSpan * TIME_SLOT_WIDTH) - 4; // 2px gap on each side

  const progressPercent = program.isCurrent
    ? Math.min(100, Math.max(0,
        ((Date.now() - new Date(program.start).getTime()) /
          (new Date(program.stop).getTime() - new Date(program.start).getTime())) * 100
      ))
    : 0;

  return (
    <View style={[
      styles.programCell,
      program.isCurrent && styles.programCellCurrent,
      { width: cellWidth, left: leftPosition + 2 }
    ]}>
      <Text style={styles.programTitle} numberOfLines={1}>{program.title}</Text>
      {program.episode && (
        <Text style={styles.programEpisode} numberOfLines={1}>{program.episode}</Text>
      )}
      {program.isCurrent && (
        <View style={styles.progressBar}>
          <View style={[styles.progressFill, { width: `${progressPercent}%` }]} />
        </View>
      )}
    </View>
  );
});

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    container: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    disabledContainer: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      padding: theme.spacing.xl,
      gap: theme.spacing.md,
    },
    disabledText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    disabledSubtext: {
      ...theme.typography.body.md,
      color: theme.colors.text.muted,
      textAlign: 'center',
    },

    // Navigation bar
    navBar: {
      flexDirection: 'row',
      alignItems: 'center',
      padding: theme.spacing.sm,
      gap: theme.spacing.sm,
      borderBottomWidth: 1,
      borderBottomColor: theme.colors.border.subtle,
    },
    navButton: {
      padding: theme.spacing.sm,
      borderRadius: theme.radius.sm,
      backgroundColor: theme.colors.overlay.button,
    },
    navButtonNow: {
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.sm,
      borderRadius: theme.radius.sm,
      backgroundColor: theme.colors.accent.primary,
    },
    navButtonText: {
      ...theme.typography.label.md,
      color: '#fff',
      fontWeight: '600',
    },
    navDate: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      marginLeft: 'auto',
    },

    // Table
    tableScroll: {
      flex: 1,
    },
    bodyScroll: {
      flex: 1,
    },

    // Header
    headerRow: {
      flexDirection: 'row',
      height: HEADER_HEIGHT,
      borderBottomWidth: 1,
      borderBottomColor: theme.colors.border.subtle,
    },
    cornerCell: {
      backgroundColor: theme.colors.background.base,
    },
    timeHeader: {
      flexDirection: 'row',
      position: 'relative',
    },
    timeSlot: {
      justifyContent: 'center',
      paddingHorizontal: theme.spacing.sm,
    },
    timeSlotText: {
      ...theme.typography.label.md,
      fontSize: 12,
      color: theme.colors.text.secondary,
    },

    // Row
    row: {
      flexDirection: 'row',
      height: ROW_HEIGHT,
      borderBottomWidth: 1,
      borderBottomColor: theme.colors.border.subtle,
    },
    rowFocused: {
      backgroundColor: theme.colors.accent.primary,
    },

    // Channel cell
    channelCell: {
      height: '100%',
      flexDirection: 'row',
      alignItems: 'center',
      paddingHorizontal: theme.spacing.sm,
      gap: theme.spacing.xs,
      backgroundColor: theme.colors.background.base,
      overflow: 'hidden',
    },
    channelLogo: {
      width: 32,
      height: 24,
      flexShrink: 0,
    },
    channelLogoPlaceholder: {
      width: 32,
      height: 24,
      justifyContent: 'center',
      alignItems: 'center',
      flexShrink: 0,
    },
    channelName: {
      ...theme.typography.label.md,
      fontSize: 12,
      color: theme.colors.text.primary,
      flex: 1,
    },
    favoriteIcon: {
      marginLeft: 2,
      flexShrink: 0,
    },

    // Programs
    programsContainer: {
      height: '100%',
      position: 'relative',
    },
    noDataCell: {
      position: 'absolute',
      top: 2,
      bottom: 2,
      left: 2,
      right: 2,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.sm,
    },
    noDataText: {
      ...theme.typography.label.md,
      fontSize: 12,
      color: theme.colors.text.muted,
    },
    programCell: {
      position: 'absolute',
      top: 2,
      bottom: 2,
      paddingHorizontal: theme.spacing.sm,
      paddingVertical: theme.spacing.xs,
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.sm,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
      justifyContent: 'center',
    },
    programCellCurrent: {
      backgroundColor: 'rgba(63, 102, 255, 0.15)',
      borderColor: theme.colors.accent.primary,
    },
    programTitle: {
      ...theme.typography.label.md,
      fontSize: 12,
      color: theme.colors.text.primary,
      fontWeight: '500',
    },
    programEpisode: {
      ...theme.typography.label.md,
      fontSize: 10,
      color: theme.colors.text.muted,
      marginTop: 1,
    },
    progressBar: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: 3,
      backgroundColor: 'rgba(255, 255, 255, 0.1)',
    },
    progressFill: {
      height: '100%',
      backgroundColor: theme.colors.accent.primary,
    },

    // Time indicator
    timeIndicator: {
      position: 'absolute',
      top: 0,
      bottom: 0,
      width: TIME_INDICATOR_WIDTH,
      backgroundColor: theme.colors.status.danger,
      zIndex: 10,
    },

    // Loading
    loadingOverlay: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
    },
    loadingText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
    },
  });

export default EPGGrid;
