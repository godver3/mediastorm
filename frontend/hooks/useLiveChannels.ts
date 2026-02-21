import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useStartupData } from '@/components/StartupDataContext';
import apiService, { LiveChannel } from '@/services/api';

// Re-export LiveChannel from api.ts for convenience
export type { LiveChannel } from '@/services/api';

export const useLiveChannels = (selectedCategories?: string[], favoriteChannelIds?: Set<string>, profileId?: string) => {
  const { settings, isReady } = useBackendSettings();
  const { startupData } = useStartupData();
  const [allChannels, setAllChannels] = useState<LiveChannel[]>([]);
  const [availableCategories, setAvailableCategories] = useState<string[]>([]);
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

  // Use effectivePlaylistUrl (computed from Xtream credentials if in xtream mode), fallback to playlistUrl
  const playlistUrl = useMemo(
    () => settings?.live?.effectivePlaylistUrl || settings?.live?.playlistUrl || '',
    [settings?.live?.effectivePlaylistUrl, settings?.live?.playlistUrl]
  );
  const normalisedPlaylistUrl = useMemo(() => playlistUrl.trim(), [playlistUrl]);

  // Check if the profile has per-profile IPTV overrides
  const profileLiveTV = startupData?.userSettings?.liveTV;
  const hasProfileIPTV = useMemo(() => {
    if (!profileLiveTV) return false;
    if (profileLiveTV.mode === 'xtream') {
      return !!(profileLiveTV.xtreamHost && profileLiveTV.xtreamUsername && profileLiveTV.xtreamPassword);
    }
    if (profileLiveTV.mode === 'm3u') {
      return !!profileLiveTV.playlistUrl?.trim();
    }
    return false;
  }, [profileLiveTV]);

  const hasPlaylistUrl = useMemo(() => hasProfileIPTV || !!normalisedPlaylistUrl, [hasProfileIPTV, normalisedPlaylistUrl]);

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

  // Filter channels by selected categories (user preference, not backend admin filter)
  // Note: Favorites are always included even if their category is not selected
  const channels = useMemo(() => {
    if (!selectedCategories || selectedCategories.length === 0) {
      return allChannels;
    }
    // If none of the selected categories exist in available channels, skip filtering
    // (prevents stale category selections from hiding all content after backend filter changes)
    const availableSet = new Set(allChannels.map((c) => c.group).filter(Boolean));
    const hasValidSelection = selectedCategories.some((cat) => availableSet.has(cat));
    if (!hasValidSelection) {
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
      setAvailableCategories([]);
      setError(null);
      setLoading(false);
      return;
    }

    try {
      console.log('[useLiveChannels] fetchChannels: Starting fetch from backend...');
      setLoading(true);
      setError(null);

      // Use new backend endpoint that returns pre-parsed and filtered channels
      const response = await apiService.getLiveChannels(controller.signal, profileId);
      console.log('[useLiveChannels] fetchChannels: Got response with', response.channels?.length ?? 0, 'channels');

      // Add stream URLs to channels
      const channelsWithStreamUrls = (response.channels || []).map((channel) => ({
        ...channel,
        streamUrl: apiService.buildLiveStreamUrl(channel.url),
      }));

      setAllChannels(channelsWithStreamUrls);
      setAvailableCategories(response.availableCategories || []);

      if (!channelsWithStreamUrls.length) {
        setError('No channels found in the playlist.');
      }
    } catch (err) {
      if ((err as Error)?.name === 'AbortError') {
        console.log('[useLiveChannels] fetchChannels: Aborted');
        return;
      }
      // Handle network errors (status 0) and other failures gracefully
      let message = 'Failed to load channels.';
      if (err instanceof Error) {
        if (err.message.includes('status') && err.message.includes('0')) {
          message = 'Unable to reach server. The server may be down or unreachable.';
        } else if (err.name === 'RangeError') {
          message = 'Unable to reach server. The server may be down or unreachable.';
        } else if (err.message.includes('no playlist URL configured')) {
          message = 'No Live TV playlist configured. Please configure one in Settings.';
        } else {
          message = err.message;
        }
      }
      console.log('[useLiveChannels] fetchChannels: Error:', message);
      setError(message);
      setAllChannels([]);
      setAvailableCategories([]);
    } finally {
      setLoading(false);
    }
  }, [hasPlaylistUrl, isReady, normalisedPlaylistUrl, profileId]);

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
