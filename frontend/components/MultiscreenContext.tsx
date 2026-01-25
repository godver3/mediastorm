import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { apiService, MultiscreenChannel, MultiscreenSession, UserSettings } from '@/services/api';

import { useUserProfiles } from './UserProfilesContext';

interface MultiscreenContextValue {
  // Session state
  session: MultiscreenSession | null;
  hasSavedSession: boolean;

  // Selection mode (for Live TV page)
  isSelectionMode: boolean;
  selectedChannels: MultiscreenChannel[];

  // Actions
  enterSelectionMode: () => void;
  exitSelectionMode: () => void;
  toggleChannelSelection: (channel: MultiscreenChannel) => void;
  getChannelSelectionOrder: (channelId: string) => number | null;
  launchMultiscreen: () => MultiscreenChannel[] | null;
  resumeSession: () => MultiscreenChannel[] | null;
  clearSession: () => Promise<void>;
  setActiveAudioIndex: (index: number) => void;

  // Ready state
  isReady: boolean;
}

const MultiscreenContext = createContext<MultiscreenContextValue | undefined>(undefined);

const MAX_SCREENS = 5;

export const MultiscreenProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const mountedRef = useRef(true);
  const { activeUserId } = useUserProfiles();
  const userSettingsRef = useRef<UserSettings | null>(null);

  // State
  const [session, setSession] = useState<MultiscreenSession | null>(null);
  const [isSelectionMode, setIsSelectionMode] = useState(false);
  const [selectedChannels, setSelectedChannels] = useState<MultiscreenChannel[]>([]);
  const [isReady, setIsReady] = useState(false);

  useEffect(() => {
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // Load multiscreen session from backend when active user changes
  useEffect(() => {
    let cancelled = false;

    const loadUserSettings = async () => {
      if (!activeUserId) {
        if (mountedRef.current) {
          setSession(null);
          setIsReady(true);
        }
        return;
      }

      try {
        const settings = await apiService.getUserSettings(activeUserId);

        if (cancelled || !mountedRef.current) {
          return;
        }

        userSettingsRef.current = settings;

        // Load multiscreen session from user settings
        const liveTV = settings.liveTV;
        if (liveTV?.multiscreenSession) {
          setSession(liveTV.multiscreenSession);
        } else {
          setSession(null);
        }
      } catch (err) {
        console.warn('Failed to load multiscreen session.', err);
        if (mountedRef.current) {
          setSession(null);
        }
      } finally {
        if (!cancelled && mountedRef.current) {
          setIsReady(true);
        }
      }
    };

    setIsReady(false);
    void loadUserSettings();

    return () => {
      cancelled = true;
    };
  }, [activeUserId]);

  // Helper to save multiscreen session to backend
  const saveSession = useCallback(
    async (newSession: MultiscreenSession | null) => {
      if (!activeUserId || !userSettingsRef.current) {
        return;
      }

      try {
        const updatedSettings: UserSettings = {
          ...userSettingsRef.current,
          liveTV: {
            ...userSettingsRef.current.liveTV,
            multiscreenSession: newSession ?? undefined,
          },
        };
        await apiService.updateUserSettings(activeUserId, updatedSettings);
        userSettingsRef.current = updatedSettings;
      } catch (err) {
        console.warn('Failed to save multiscreen session.', err);
      }
    },
    [activeUserId],
  );

  // Selection mode functions
  const enterSelectionMode = useCallback(() => {
    setIsSelectionMode(true);
    setSelectedChannels([]);
  }, []);

  const exitSelectionMode = useCallback(() => {
    setIsSelectionMode(false);
    setSelectedChannels([]);
  }, []);

  const toggleChannelSelection = useCallback((channel: MultiscreenChannel) => {
    setSelectedChannels((prev) => {
      const existingIndex = prev.findIndex((c) => c.id === channel.id);
      if (existingIndex >= 0) {
        // Remove from selection
        return prev.filter((c) => c.id !== channel.id);
      } else if (prev.length < MAX_SCREENS) {
        // Add to selection
        return [...prev, channel];
      }
      // Already at max, don't add
      return prev;
    });
  }, []);

  const getChannelSelectionOrder = useCallback(
    (channelId: string): number | null => {
      const index = selectedChannels.findIndex((c) => c.id === channelId);
      return index >= 0 ? index + 1 : null;
    },
    [selectedChannels],
  );

  // Launch multiscreen with selected channels
  const launchMultiscreen = useCallback((): MultiscreenChannel[] | null => {
    if (selectedChannels.length < 2) {
      return null;
    }

    const newSession: MultiscreenSession = {
      channels: selectedChannels,
      activeAudioIndex: 0,
    };

    setSession(newSession);
    setIsSelectionMode(false);
    setSelectedChannels([]);

    // Save to backend
    void saveSession(newSession);

    return selectedChannels;
  }, [selectedChannels, saveSession]);

  // Resume saved session
  const resumeSession = useCallback((): MultiscreenChannel[] | null => {
    if (!session || session.channels.length < 2) {
      return null;
    }
    return session.channels;
  }, [session]);

  // Clear saved session
  const clearSession = useCallback(async () => {
    setSession(null);
    await saveSession(null);
  }, [saveSession]);

  // Update active audio index - use ref to avoid dependency on session
  // which would cause infinite loops when this callback is used in useEffect dependencies
  const sessionRef = useRef(session);
  sessionRef.current = session;

  const setActiveAudioIndex = useCallback((index: number) => {
    const currentSession = sessionRef.current;
    if (!currentSession) return;

    const updatedSession: MultiscreenSession = {
      ...currentSession,
      activeAudioIndex: index,
    };
    setSession(updatedSession);
    // Don't persist audio index changes (too frequent)
  }, []);

  const hasSavedSession = useMemo(() => {
    return session !== null && session.channels.length >= 2;
  }, [session]);

  const value = useMemo<MultiscreenContextValue>(
    () => ({
      session,
      hasSavedSession,
      isSelectionMode,
      selectedChannels,
      enterSelectionMode,
      exitSelectionMode,
      toggleChannelSelection,
      getChannelSelectionOrder,
      launchMultiscreen,
      resumeSession,
      clearSession,
      setActiveAudioIndex,
      isReady,
    }),
    [
      session,
      hasSavedSession,
      isSelectionMode,
      selectedChannels,
      enterSelectionMode,
      exitSelectionMode,
      toggleChannelSelection,
      getChannelSelectionOrder,
      launchMultiscreen,
      resumeSession,
      clearSession,
      setActiveAudioIndex,
      isReady,
    ],
  );

  return <MultiscreenContext.Provider value={value}>{children}</MultiscreenContext.Provider>;
};

export const useMultiscreen = () => {
  const context = useContext(MultiscreenContext);
  if (!context) {
    throw new Error('useMultiscreen must be used within a MultiscreenProvider');
  }
  return context;
};
