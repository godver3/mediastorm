import {
  keepPreviousData,
  useInfiniteQuery,
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query';
import { useEffect, useMemo, useState } from 'react';
import {
  fetchChannelLists,
  fetchChannels,
  fetchEpgStatus,
  fetchNowNext,
  fetchSchedule,
  guideKey,
  liveKeys,
  saveChannelLists,
  type ChannelQuery,
  type EPGNowPlaying,
  type EPGProgram,
  type LiveChannel,
  type LiveChannelLists,
} from '@/api/live';
import { fetchActiveRecordings, fetchRules, recordingKeys } from '@/api/recordings';
import { chunk } from '@/lib/guide';

/** Current time, re-rendered every `intervalMs`. */
export function useNow(intervalMs = 30_000) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);
  return now;
}

export function useChannelLists(profileId: string) {
  return useQuery({
    queryKey: liveKeys.lists(profileId),
    queryFn: ({ signal }) => fetchChannelLists(profileId, signal),
    staleTime: 60_000,
  });
}

/** Optimistic favorite / hidden toggles, saved to the profile's settings. */
export function useToggleChannelList(profileId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (next: LiveChannelLists) => saveChannelLists(profileId, next),
    onMutate: async (next) => {
      await qc.cancelQueries({ queryKey: liveKeys.lists(profileId) });
      const prev = qc.getQueryData<LiveChannelLists>(liveKeys.lists(profileId));
      qc.setQueryData(liveKeys.lists(profileId), next);
      return { prev };
    },
    onError: (_e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(liveKeys.lists(profileId), ctx.prev);
    },
    onSettled: () => {
      void qc.invalidateQueries({ queryKey: ['users', profileId, 'settings'] });
    },
  });
}

export const toggled = (list: string[], id: string) =>
  list.includes(id) ? list.filter((x) => x !== id) : [...list, id];

export function useChannels(profileId: string, q: ChannelQuery, favorites: string[]) {
  return useInfiniteQuery({
    queryKey: liveKeys.channels(profileId, q, favorites),
    queryFn: ({ pageParam, signal }) => fetchChannels(profileId, q, favorites, pageParam, signal),
    initialPageParam: 0,
    getNextPageParam: (last, pages) =>
      last.hasMore ? pages.reduce((n, p) => n + p.channels.length, 0) : undefined,
    placeholderData: keepPreviousData,
    staleTime: 5 * 60_000,
  });
}

export function useEpgStatus(profileId: string) {
  return useQuery({
    queryKey: liveKeys.epgStatus(profileId),
    queryFn: ({ signal }) => fetchEpgStatus(profileId, signal),
    staleTime: 5 * 60_000,
  });
}

export function useActiveRecordings(profileId: string) {
  return useQuery({
    queryKey: recordingKeys.active(profileId),
    queryFn: ({ signal }) => fetchActiveRecordings(profileId, signal),
    staleTime: 30_000,
    refetchInterval: 60_000,
  });
}

export function useRecordingRules(profileId: string) {
  return useQuery({
    queryKey: recordingKeys.rules(profileId),
    queryFn: ({ signal }) => fetchRules(profileId, signal),
    staleTime: 60_000,
  });
}

function uniqueKeys(channels: LiveChannel[]) {
  return [...new Set(channels.map(guideKey))];
}

/** Now / next for every listed channel, 50 keys a request. */
export function useNowNext(profileId: string, channels: LiveChannel[], enabled: boolean) {
  const batches = useMemo(() => chunk(uniqueKeys(channels), 50), [channels]);
  const results = useQueries({
    queries: batches.map((keys) => ({
      queryKey: liveKeys.nowNext(profileId, keys),
      queryFn: ({ signal }: { signal: AbortSignal }) => fetchNowNext(profileId, keys, signal),
      enabled,
      staleTime: 60_000,
      refetchInterval: 120_000,
    })),
  });
  return useMemo(() => {
    const out: Record<string, EPGNowPlaying> = {};
    for (const r of results) Object.assign(out, r.data);
    return out;
  }, [results]);
}

/** Guide schedules from `start` for `hours`, 40 keys a request (native batch size). */
export function useSchedules(
  profileId: string,
  channels: LiveChannel[],
  start: number,
  hours: number,
  enabled: boolean,
) {
  const batches = useMemo(() => chunk(uniqueKeys(channels), 40), [channels]);
  // Minutes from now; fixed per guide start so the key stays stable while time passes.
  const [offset] = useState(() => Math.floor((start - Date.now()) / 60_000));
  const results = useQueries({
    queries: batches.map((keys) => ({
      queryKey: liveKeys.schedule(profileId, keys, start, hours),
      queryFn: ({ signal }: { signal: AbortSignal }) =>
        fetchSchedule(profileId, keys, offset, hours + 1, signal),
      enabled,
      staleTime: 10 * 60_000,
    })),
  });
  const schedules = useMemo(() => {
    const out: Record<string, EPGProgram[]> = {};
    for (const r of results) Object.assign(out, r.data);
    return out;
  }, [results]);
  const loading = results.some((r) => r.isPending && r.fetchStatus !== 'idle');
  return { schedules, loading };
}

/** Current programme from a schedule. */
export const currentIn = (list: EPGProgram[] | undefined, now: number) =>
  list?.find((p) => p.start <= now && now < p.stop);
