import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import apiService from '@/services/api';

export interface LiveChannel {
  id: string;
  name: string;
  url: string;
  logo?: string;
  group?: string;
  tvgId?: string;
  tvgName?: string;
  tvgLanguage?: string;
  streamUrl?: string;
}

const ATTRIBUTE_REGEX = /([a-zA-Z0-9\-]+)="([^"]*)"/g;

const parseM3UPlaylist = (contents: string): LiveChannel[] => {
  if (!contents?.trim()) {
    return [];
  }

  const lines = contents.split(/\r?\n/);
  const channels: LiveChannel[] = [];
  const usedIds = new Set<string>();

  const assignId = (baseId: string): string => {
    const sanitizedBase = baseId.trim() || 'channel';
    if (!usedIds.has(sanitizedBase)) {
      usedIds.add(sanitizedBase);
      return sanitizedBase;
    }
    let suffix = 1;
    let candidate = `${sanitizedBase}-${suffix}`;
    while (usedIds.has(candidate)) {
      suffix += 1;
      candidate = `${sanitizedBase}-${suffix}`;
    }
    usedIds.add(candidate);
    return candidate;
  };
  let pending: Partial<LiveChannel> | null = null;

  for (const rawLine of lines) {
    const line = rawLine.trim();
    if (!line) {
      continue;
    }

    if (line.startsWith('#EXTINF')) {
      const [, metaAndName] = line.split('#EXTINF:', 2);
      if (!metaAndName) {
        pending = null;
        continue;
      }

      const [metadataPart, namePart = ''] = metaAndName.split(',', 2);
      const attributes: Record<string, string> = {};
      let match: RegExpExecArray | null = null;
      while ((match = ATTRIBUTE_REGEX.exec(metadataPart)) !== null) {
        attributes[match[1].toLowerCase()] = match[2];
      }
      ATTRIBUTE_REGEX.lastIndex = 0;

      const name = namePart.trim() || attributes['tvg-name']?.trim() || 'Channel';
      const idFallbackSource = attributes['tvg-id']?.trim() || name || `channel-${channels.length + 1}`;

      pending = {
        id: idFallbackSource,
        name,
        logo: attributes['tvg-logo']?.trim(),
        group: attributes['group-title']?.trim(),
        tvgId: attributes['tvg-id']?.trim(),
        tvgName: attributes['tvg-name']?.trim(),
        tvgLanguage: attributes['tvg-language']?.trim(),
      };
      continue;
    }

    if (line.startsWith('#')) {
      continue;
    }

    if (pending) {
      const assignedId = assignId(pending.id || `channel-${channels.length + 1}`);
      channels.push({
        id: assignedId,
        name: pending.name || assignedId,
        url: line,
        logo: pending.logo,
        group: pending.group,
        tvgId: pending.tvgId,
        tvgName: pending.tvgName,
        tvgLanguage: pending.tvgLanguage,
      });
      pending = null;
    }
  }

  return channels;
};

export const useLiveChannels = (selectedCategories?: string[], favoriteChannelIds?: Set<string>) => {
  const { settings, isReady } = useBackendSettings();
  const [allChannels, setAllChannels] = useState<LiveChannel[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const abortControllerRef = useRef<AbortController | null>(null);
  const mountCountRef = useRef(0);

  // Log on mount
  useEffect(() => {
    mountCountRef.current += 1;
    console.log('[useLiveChannels] Hook mounted, count:', mountCountRef.current);
    return () => {
      console.log('[useLiveChannels] Hook unmounting, count:', mountCountRef.current);
    };
  }, []);

  const playlistUrl = useMemo(() => settings?.live?.playlistUrl ?? '', [settings?.live?.playlistUrl]);
  const normalisedPlaylistUrl = useMemo(() => playlistUrl.trim(), [playlistUrl]);
  const hasPlaylistUrl = useMemo(() => !!normalisedPlaylistUrl, [normalisedPlaylistUrl]);

  // Log state changes
  useEffect(() => {
    console.log('[useLiveChannels] State:', {
      isReady,
      hasSettings: !!settings,
      playlistUrl: playlistUrl ? `${playlistUrl.substring(0, 50)}...` : '(empty)',
      hasPlaylistUrl,
      channelCount: allChannels.length,
    });
  }, [isReady, settings, playlistUrl, hasPlaylistUrl, allChannels.length]);

  // Extract unique categories from all channels
  const availableCategories = useMemo(() => {
    const categorySet = new Set<string>();
    allChannels.forEach((channel) => {
      if (channel.group) {
        categorySet.add(channel.group);
      }
    });
    return Array.from(categorySet).sort();
  }, [allChannels]);

  // Filter channels by selected categories
  // Note: Favorites are always included even if their category is not selected
  const channels = useMemo(() => {
    if (!selectedCategories || selectedCategories.length === 0) {
      return allChannels;
    }
    return allChannels.filter((channel) => {
      // Always include favorites regardless of category filter
      if (favoriteChannelIds?.has(channel.id)) {
        return true;
      }
      // For non-favorites, only include if their category is selected
      return channel.group && selectedCategories.includes(channel.group);
    });
  }, [allChannels, selectedCategories, favoriteChannelIds]);

  const fetchChannels = useCallback(async () => {
    console.log('[useLiveChannels] fetchChannels called:', {
      isReady,
      hasPlaylistUrl,
      normalisedPlaylistUrl: normalisedPlaylistUrl ? `${normalisedPlaylistUrl.substring(0, 50)}...` : '(empty)',
    });

    if (!isReady) {
      console.log('[useLiveChannels] fetchChannels: Early return - not ready');
      return;
    }

    abortControllerRef.current?.abort();
    const controller = new AbortController();
    abortControllerRef.current = controller;

    if (!hasPlaylistUrl) {
      console.log('[useLiveChannels] fetchChannels: Early return - no playlist URL');
      setAllChannels([]);
      setError(null);
      setLoading(false);
      return;
    }

    try {
      console.log('[useLiveChannels] fetchChannels: Starting fetch...');
      setLoading(true);
      setError(null);
      const text = await apiService.getLivePlaylist(normalisedPlaylistUrl, controller.signal);
      console.log('[useLiveChannels] fetchChannels: Got response, length:', text?.length ?? 0);
      const parsed = parseM3UPlaylist(text).map((channel) => ({
        ...channel,
        streamUrl: apiService.buildLiveStreamUrl(channel.url),
      }));
      console.log('[useLiveChannels] fetchChannels: Parsed', parsed.length, 'channels');
      setAllChannels(parsed);
      if (!parsed.length) {
        setError('No channels found in the playlist.');
      }
    } catch (err) {
      if ((err as Error)?.name === 'AbortError') {
        console.log('[useLiveChannels] fetchChannels: Aborted');
        return;
      }
      // Handle network errors (status 0) and other failures gracefully
      let message = 'Failed to load playlist.';
      if (err instanceof Error) {
        if (err.message.includes('status') && err.message.includes('0')) {
          message = 'Unable to reach playlist server. The server may be down or unreachable.';
        } else if (err.name === 'RangeError') {
          message = 'Unable to reach playlist server. The server may be down or unreachable.';
        } else {
          message = err.message;
        }
      }
      console.log('[useLiveChannels] fetchChannels: Error:', message);
      setError(message);
      setAllChannels([]);
    } finally {
      setLoading(false);
    }
  }, [hasPlaylistUrl, isReady, normalisedPlaylistUrl]);

  useEffect(() => {
    console.log('[useLiveChannels] Fetch useEffect triggered:', {
      isReady,
      hasPlaylistUrl,
      normalisedPlaylistUrl: normalisedPlaylistUrl ? 'set' : 'empty',
    });

    if (!isReady) {
      console.log('[useLiveChannels] Fetch useEffect: Early return - not ready');
      return;
    }

    console.log('[useLiveChannels] Fetch useEffect: Calling fetchChannels...');
    void fetchChannels();

    return () => {
      console.log('[useLiveChannels] Fetch useEffect: Cleanup - aborting');
      abortControllerRef.current?.abort();
      abortControllerRef.current = null;
    };
  }, [fetchChannels, isReady, normalisedPlaylistUrl]);

  return {
    channels,
    loading,
    error,
    refresh: fetchChannels,
    playlistUrl,
    hasPlaylistUrl,
    isReady,
    availableCategories,
  };
};
