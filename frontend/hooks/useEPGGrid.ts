import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import apiService, { EPGProgram, EPGStatus } from '@/services/api';

// Re-export types
export type { EPGProgram } from '@/services/api';

// Grid configuration
export const EPG_GRID_SLOT_MINUTES = 30; // Each slot represents 30 minutes
export const EPG_GRID_DEFAULT_HOURS = 4; // Default visible time window
export const EPG_GRID_CACHE_DURATION = 2 * 60 * 1000; // 2 minute cache

// Program with grid positioning information
export interface GridProgram extends EPGProgram {
  gridStartMinutes: number; // Minutes from grid start time
  gridDurationMinutes: number; // Duration in minutes
  columnSpan: number; // Number of 30-minute slots it spans
  isPartial: boolean; // Program extends beyond visible window
  isCurrent: boolean; // Program is currently airing
}

// Schedule data for a single channel
export interface ChannelSchedule {
  channelId: string;
  programs: GridProgram[];
}

// Grid state for time navigation
export interface EPGGridState {
  timeWindowStart: Date; // Start of visible window
  timeWindowHours: number; // Hours visible in grid
}

interface UseEPGGridResult {
  schedules: Map<string, GridProgram[]>;
  loading: boolean;
  error: string | null;
  gridState: EPGGridState;
  epgStatus: EPGStatus | null;
  isEnabled: boolean;
  fetchSchedules: (channelIds: string[]) => Promise<void>;
  scrollTimeForward: () => void;
  scrollTimeBackward: () => void;
  jumpToNow: () => void;
  setTimeWindowHours: (hours: number) => void;
  getTimeSlots: () => Date[];
  getCurrentTimePosition: () => number | null;
}

/**
 * Round down to nearest 30-minute slot
 */
function roundToSlot(date: Date): Date {
  const result = new Date(date);
  const minutes = result.getMinutes();
  result.setMinutes(minutes - (minutes % EPG_GRID_SLOT_MINUTES), 0, 0);
  return result;
}

/**
 * Convert program times to grid positions
 */
function calculateGridProgram(
  program: EPGProgram,
  gridStart: Date,
  gridEnd: Date,
): GridProgram {
  const programStart = new Date(program.start);
  const programEnd = new Date(program.stop);
  const now = new Date();

  // Calculate position relative to grid start
  const gridStartMs = gridStart.getTime();
  const gridEndMs = gridEnd.getTime();

  // Clamp program times to grid window
  const visibleStart = Math.max(programStart.getTime(), gridStartMs);
  const visibleEnd = Math.min(programEnd.getTime(), gridEndMs);

  // Normalize start time to nearest 15-minute increment
  const rawStartMinutes = (visibleStart - gridStartMs) / 60000;
  const gridStartMinutes = Math.round(rawStartMinutes / 15) * 15;

  // Duration will be recalculated later based on next program's start
  // For now, use the original duration as a fallback for the last program
  const rawDurationMinutes = (visibleEnd - visibleStart) / 60000;
  const gridDurationMinutes = Math.max(15, Math.round(rawDurationMinutes / 15) * 15);
  const columnSpan = gridDurationMinutes / EPG_GRID_SLOT_MINUTES;

  // Check if program extends beyond visible window
  const isPartial =
    programStart.getTime() < gridStartMs || programEnd.getTime() > gridEndMs;

  // Check if program is currently airing
  const isCurrent =
    now >= programStart && now < programEnd;

  return {
    ...program,
    gridStartMinutes,
    gridDurationMinutes,
    columnSpan,
    isPartial,
    isCurrent,
  };
}

/**
 * Hook to manage EPG grid data and time navigation.
 */
export const useEPGGrid = (): UseEPGGridResult => {
  // Initialize grid state with current time rounded to slot
  const [gridState, setGridState] = useState<EPGGridState>(() => ({
    timeWindowStart: roundToSlot(new Date()),
    timeWindowHours: EPG_GRID_DEFAULT_HOURS,
  }));

  const [schedules, setSchedules] = useState<Map<string, GridProgram[]>>(new Map());
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [epgStatus, setEpgStatus] = useState<EPGStatus | null>(null);

  const abortControllerRef = useRef<AbortController | null>(null);
  const lastFetchRef = useRef<{ time: number; channelIds: string[] }>({
    time: 0,
    channelIds: [],
  });

  // Fetch EPG status on mount
  useEffect(() => {
    let cancelled = false;

    const fetchStatus = async () => {
      try {
        const status = await apiService.getEPGStatus();
        if (!cancelled) {
          setEpgStatus(status);
        }
      } catch (err) {
        console.log('[useEPGGrid] Failed to fetch EPG status:', err);
      }
    };

    void fetchStatus();

    return () => {
      cancelled = true;
    };
  }, []);

  // Calculate grid end time
  const gridEnd = useMemo(() => {
    const end = new Date(gridState.timeWindowStart);
    end.setHours(end.getHours() + gridState.timeWindowHours);
    return end;
  }, [gridState.timeWindowStart, gridState.timeWindowHours]);

  // Fetch schedules for channels
  const fetchSchedules = useCallback(
    async (channelIds: string[]) => {
      const validIds = channelIds.filter((id) => id && id.trim());
      if (validIds.length === 0) {
        return;
      }

      // Check cache - skip if same channels within cache duration
      const now = Date.now();
      const isSameChannels =
        lastFetchRef.current.channelIds.length === validIds.length &&
        lastFetchRef.current.channelIds.every((id) => validIds.includes(id));

      if (
        isSameChannels &&
        now - lastFetchRef.current.time < EPG_GRID_CACHE_DURATION
      ) {
        return;
      }

      // Abort previous request
      abortControllerRef.current?.abort();
      const controller = new AbortController();
      abortControllerRef.current = controller;

      try {
        setLoading(true);
        setError(null);

        // Calculate offset from now in minutes
        const nowRounded = roundToSlot(new Date());
        const startOffsetMinutes = Math.floor(
          (gridState.timeWindowStart.getTime() - nowRounded.getTime()) / 60000,
        );

        const data = await apiService.getEPGScheduleBatch(
          validIds,
          gridState.timeWindowHours,
          startOffsetMinutes,
          controller.signal,
        );

        if (controller.signal.aborted) {
          return;
        }

        // Convert to GridPrograms
        const newSchedules = new Map<string, GridProgram[]>();
        for (const [channelId, programs] of Object.entries(data)) {
          // Handle null/empty programs array
          if (!programs || !Array.isArray(programs) || programs.length === 0) {
            newSchedules.set(channelId, []);
            continue;
          }
          let gridPrograms = programs.map((p) =>
            calculateGridProgram(p, gridState.timeWindowStart, gridEnd),
          );
          // Sort by start time
          gridPrograms.sort((a, b) => a.gridStartMinutes - b.gridStartMinutes);

          // Deduplicate: remove programs with same start time (keep first)
          const seenStarts = new Set<number>();
          gridPrograms = gridPrograms.filter((p) => {
            if (seenStarts.has(p.gridStartMinutes)) {
              return false;
            }
            seenStarts.add(p.gridStartMinutes);
            return true;
          });

          // Adjust durations: each program ends where the next one starts (no gaps or overlaps)
          for (let i = 0; i < gridPrograms.length - 1; i++) {
            const curr = gridPrograms[i];
            const next = gridPrograms[i + 1];

            // Set current program's duration to reach exactly to next program's start
            curr.gridDurationMinutes = next.gridStartMinutes - curr.gridStartMinutes;
            curr.columnSpan = curr.gridDurationMinutes / EPG_GRID_SLOT_MINUTES;
          }

          // Filter out programs with zero or negative duration
          gridPrograms = gridPrograms.filter(
            (p) => p.gridDurationMinutes > 0 && p.columnSpan > 0
          );

          newSchedules.set(channelId, gridPrograms);
        }

        setSchedules(newSchedules);
        lastFetchRef.current = { time: Date.now(), channelIds: validIds };
      } catch (err) {
        if ((err as Error)?.name === 'AbortError') {
          return;
        }
        console.log('[useEPGGrid] Error fetching schedules:', err);
        setError((err as Error)?.message || 'Failed to load EPG data');
      } finally {
        setLoading(false);
      }
    },
    [gridState.timeWindowStart, gridState.timeWindowHours, gridEnd],
  );

  // Time navigation
  const scrollTimeForward = useCallback(() => {
    setGridState((prev) => {
      const newStart = new Date(prev.timeWindowStart);
      newStart.setMinutes(newStart.getMinutes() + 30); // Scroll by 30 minutes
      return { ...prev, timeWindowStart: newStart };
    });
    // Clear cache to force refetch
    lastFetchRef.current = { time: 0, channelIds: [] };
  }, []);

  const scrollTimeBackward = useCallback(() => {
    setGridState((prev) => {
      const newStart = new Date(prev.timeWindowStart);
      newStart.setMinutes(newStart.getMinutes() - 30); // Scroll by 30 minutes
      return { ...prev, timeWindowStart: newStart };
    });
    // Clear cache to force refetch
    lastFetchRef.current = { time: 0, channelIds: [] };
  }, []);

  const jumpToNow = useCallback(() => {
    setGridState((prev) => ({
      ...prev,
      timeWindowStart: roundToSlot(new Date()),
    }));
    // Clear cache to force refetch
    lastFetchRef.current = { time: 0, channelIds: [] };
  }, []);

  const setTimeWindowHours = useCallback((hours: number) => {
    const clampedHours = Math.max(1.5, Math.min(12, hours));
    setGridState((prev) => ({
      ...prev,
      timeWindowHours: clampedHours,
    }));
    // Clear cache to force refetch
    lastFetchRef.current = { time: 0, channelIds: [] };
  }, []);

  // Generate time slots for header
  const getTimeSlots = useCallback((): Date[] => {
    const slots: Date[] = [];
    const totalSlots = (gridState.timeWindowHours * 60) / EPG_GRID_SLOT_MINUTES;

    for (let i = 0; i < totalSlots; i++) {
      const slotTime = new Date(gridState.timeWindowStart);
      slotTime.setMinutes(slotTime.getMinutes() + i * EPG_GRID_SLOT_MINUTES);
      slots.push(slotTime);
    }

    return slots;
  }, [gridState.timeWindowStart, gridState.timeWindowHours]);

  // Calculate current time position as percentage across grid
  const getCurrentTimePosition = useCallback((): number | null => {
    const now = Date.now();
    const gridStartMs = gridState.timeWindowStart.getTime();
    const gridEndMs = gridEnd.getTime();

    if (now < gridStartMs || now > gridEndMs) {
      return null; // Current time not in visible window
    }

    const totalDuration = gridEndMs - gridStartMs;
    const elapsed = now - gridStartMs;

    return (elapsed / totalDuration) * 100;
  }, [gridState.timeWindowStart, gridEnd]);

  const isEnabled = useMemo(() => {
    return epgStatus?.enabled ?? false;
  }, [epgStatus?.enabled]);

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      abortControllerRef.current?.abort();
    };
  }, []);

  return {
    schedules,
    loading,
    error,
    gridState,
    epgStatus,
    isEnabled,
    fetchSchedules,
    scrollTimeForward,
    scrollTimeBackward,
    jumpToNow,
    setTimeWindowHours,
    getTimeSlots,
    getCurrentTimePosition,
  };
};

/**
 * Format time for display in grid header
 */
export function formatTimeSlot(date: Date): string {
  return date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
}

/**
 * Format duration for display
 */
export function formatDuration(minutes: number): string {
  if (minutes < 60) {
    return `${minutes}m`;
  }
  const hours = Math.floor(minutes / 60);
  const mins = minutes % 60;
  if (mins === 0) {
    return `${hours}h`;
  }
  return `${hours}h ${mins}m`;
}
