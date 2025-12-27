import AsyncStorage from '@react-native-async-storage/async-storage';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { apiService, type ApiError, type UserProfile } from '@/services/api';

const USER_SETTINGS_LOAD_DEBOUNCE_MS = 100;

const ACTIVE_USER_STORAGE_KEY = 'strmr.activeUserId';

type Nullable<T> = T | null;

interface UserProfilesContextValue {
  users: UserProfile[];
  loading: boolean;
  error: string | null;
  activeUserId: Nullable<string>;
  activeUser: Nullable<UserProfile>;
  selectUser: (id: string) => Promise<void>;
  selectUserWithPin: (id: string, pin: string) => Promise<void>;
  verifyPin: (id: string, pin: string) => Promise<boolean>;
  refresh: (preferredUserId?: string | null) => Promise<void>;
  createUser: (name: string) => Promise<UserProfile>;
  renameUser: (id: string, name: string) => Promise<UserProfile>;
  updateColor: (id: string, color: string) => Promise<UserProfile>;
  setPin: (id: string, pin: string) => Promise<UserProfile>;
  clearPin: (id: string) => Promise<UserProfile>;
  deleteUser: (id: string) => Promise<void>;
  setTraktAccount: (id: string, traktAccountId: string) => Promise<UserProfile>;
  clearTraktAccount: (id: string) => Promise<UserProfile>;
  // PIN entry modal state
  pendingPinUserId: Nullable<string>;
  setPendingPinUserId: (id: Nullable<string>) => void;
  cancelPinEntry: () => void;
  // Whether this is an initial app load PIN check (can't cancel if all users have PINs)
  isInitialPinCheck: boolean;
}

const UserProfilesContext = createContext<UserProfilesContextValue | undefined>(undefined);

const formatErrorMessage = (err: unknown) => {
  if (err instanceof Error) {
    return err.message;
  }
  if (typeof err === 'string') {
    return err;
  }
  return 'Unknown user profile error';
};

const isAuthError = (err: unknown) => {
  if (!err || typeof err !== 'object') {
    return false;
  }
  const candidate = err as ApiError;
  return candidate.code === 'AUTH_INVALID_PIN' || candidate.status === 401;
};

const isNetworkError = (err: unknown) => {
  return err instanceof TypeError && err.message === 'Network request failed';
};

const persistActiveUserId = async (id: Nullable<string>) => {
  try {
    if (id) {
      await AsyncStorage.setItem(ACTIVE_USER_STORAGE_KEY, id);
    } else {
      await AsyncStorage.removeItem(ACTIVE_USER_STORAGE_KEY);
    }
  } catch (err) {
    console.warn('Failed to persist active user ID', err);
  }
};

export const UserProfilesProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [users, setUsers] = useState<UserProfile[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [activeUserId, setActiveUserId] = useState<Nullable<string>>(null);
  const [pendingPinUserId, setPendingPinUserId] = useState<Nullable<string>>(null);
  const [isInitialPinCheck, setIsInitialPinCheck] = useState(false);
  const activeUserIdRef = useRef<Nullable<string>>(null);
  const usersRef = useRef<UserProfile[]>([]);
  const { backendUrl, isReady, loadUserSettings, isBackendReachable } = useBackendSettings();

  const findUser = useCallback(
    (id: string | null | undefined, list: UserProfile[] = users) => {
      if (!id) {
        return undefined;
      }
      return list.find((user) => user.id === id);
    },
    [users],
  );

  const resolveActiveUserId = useCallback((candidate: Nullable<string>, list: UserProfile[]): Nullable<string> => {
    if (candidate && list.some((user) => user.id === candidate)) {
      return candidate;
    }
    return list.length > 0 ? list[0].id : null;
  }, []);

  const refresh = useCallback(
    async (preferredUserId?: string | null, skipPinCheck = false) => {
      setLoading(true);
      try {
        const [list, storedId] = await Promise.all([
          apiService.getUsers(),
          AsyncStorage.getItem(ACTIVE_USER_STORAGE_KEY),
        ]);

        setUsers(list);
        usersRef.current = list;
        setError(null);

        const nextId = resolveActiveUserId(preferredUserId ?? activeUserIdRef.current ?? storedId, list);
        const nextUser = list.find((u) => u.id === nextId);

        // Check if the user has a PIN and we haven't already verified
        // Skip PIN check if explicitly requested (e.g., after successful PIN entry)
        if (!skipPinCheck && nextUser?.hasPin && !activeUserIdRef.current) {
          // This is initial app load with a PIN-protected user
          console.log('[UserProfiles] Active user has PIN, prompting for verification');
          setIsInitialPinCheck(true);
          setPendingPinUserId(nextId);
          // Don't set activeUserId yet - wait for PIN verification
        } else {
          setActiveUserId(nextId);
          activeUserIdRef.current = nextId;
          await persistActiveUserId(nextId);
        }
      } catch (err) {
        const message = formatErrorMessage(err);
        const log = isAuthError(err) || isNetworkError(err) ? console.warn : console.error;
        log('Failed to load users:', err);
        setUsers([]);
        usersRef.current = [];
        setError(message);
      } finally {
        setLoading(false);
      }
    },
    [resolveActiveUserId],
  );

  useEffect(() => {
    if (!isReady) {
      return;
    }
    void refresh();
  }, [isReady, backendUrl, refresh]);

  // Load user settings when activeUserId changes
  useEffect(() => {
    if (!activeUserId || !isBackendReachable) {
      return;
    }

    // Debounce to avoid rapid reloads during initialization
    const timeoutId = setTimeout(() => {
      console.log('[UserProfiles] Loading user settings for user:', activeUserId);
      loadUserSettings(activeUserId).catch((err) => {
        console.warn('[UserProfiles] Failed to load user settings:', err);
      });
    }, USER_SETTINGS_LOAD_DEBOUNCE_MS);

    return () => clearTimeout(timeoutId);
  }, [activeUserId, isBackendReachable, loadUserSettings]);

  const selectUser = useCallback(
    async (id: string) => {
      const trimmed = id?.trim();
      if (!trimmed) {
        throw new Error('User ID is required');
      }
      const user = findUser(trimmed);
      if (!user) {
        throw new Error('User not found');
      }
      // If the user has a PIN, set pendingPinUserId to trigger PIN entry modal
      if (user.hasPin) {
        setPendingPinUserId(trimmed);
        return; // Don't switch yet - wait for PIN verification
      }
      setActiveUserId(trimmed);
      activeUserIdRef.current = trimmed;
      await persistActiveUserId(trimmed);
    },
    [findUser],
  );

  const selectUserWithPin = useCallback(
    async (id: string, pin: string) => {
      const trimmed = id?.trim();
      if (!trimmed) {
        throw new Error('User ID is required');
      }
      const user = findUser(trimmed);
      if (!user) {
        throw new Error('User not found');
      }
      // Verify PIN before switching
      const isValid = await apiService.verifyUserPin(trimmed, pin);
      if (!isValid) {
        throw new Error('Invalid PIN');
      }
      setPendingPinUserId(null);
      setIsInitialPinCheck(false);
      setActiveUserId(trimmed);
      activeUserIdRef.current = trimmed;
      await persistActiveUserId(trimmed);
    },
    [findUser],
  );

  const verifyPin = useCallback(async (id: string, pin: string): Promise<boolean> => {
    return apiService.verifyUserPin(id, pin);
  }, []);

  const cancelPinEntry = useCallback(async () => {
    if (isInitialPinCheck) {
      // On initial app load, try to fall back to a user without a PIN
      const userWithoutPin = usersRef.current.find((u) => !u.hasPin);
      if (userWithoutPin) {
        console.log('[UserProfiles] Falling back to user without PIN:', userWithoutPin.name);
        setActiveUserId(userWithoutPin.id);
        activeUserIdRef.current = userWithoutPin.id;
        await persistActiveUserId(userWithoutPin.id);
        setPendingPinUserId(null);
        setIsInitialPinCheck(false);
      } else {
        // All users have PINs - can't cancel, must enter PIN
        console.log('[UserProfiles] All users have PINs, cannot cancel');
        return;
      }
    } else {
      // Normal profile switch - just cancel
      setPendingPinUserId(null);
    }
  }, [isInitialPinCheck]);

  const createUser = useCallback(
    async (name: string) => {
      const user = await apiService.createUser(name);
      await refresh(user.id);
      return user;
    },
    [refresh],
  );

  const renameUser = useCallback(async (id: string, name: string) => {
    const updated = await apiService.renameUser(id, name);
    setUsers((current) => current.map((user) => (user.id === updated.id ? updated : user)));
    if (activeUserIdRef.current === updated.id) {
      setActiveUserId(updated.id);
    }
    return updated;
  }, []);

  const updateColor = useCallback(async (id: string, color: string) => {
    const updated = await apiService.updateUserColor(id, color);
    setUsers((current) => current.map((user) => (user.id === updated.id ? updated : user)));
    return updated;
  }, []);

  const setPin = useCallback(async (id: string, pin: string) => {
    const updated = await apiService.setUserPin(id, pin);
    setUsers((current) => current.map((user) => (user.id === updated.id ? updated : user)));
    return updated;
  }, []);

  const clearPin = useCallback(async (id: string) => {
    const updated = await apiService.clearUserPin(id);
    setUsers((current) => current.map((user) => (user.id === updated.id ? updated : user)));
    return updated;
  }, []);

  const setTraktAccount = useCallback(async (id: string, traktAccountId: string) => {
    const updated = await apiService.setUserTraktAccount(id, traktAccountId);
    setUsers((current) => current.map((user) => (user.id === updated.id ? updated : user)));
    return updated;
  }, []);

  const clearTraktAccount = useCallback(async (id: string) => {
    const updated = await apiService.clearUserTraktAccount(id);
    setUsers((current) => current.map((user) => (user.id === updated.id ? updated : user)));
    return updated;
  }, []);

  const deleteUser = useCallback(
    async (id: string) => {
      await apiService.deleteUser(id);
      const nextId = activeUserIdRef.current === id ? null : activeUserIdRef.current;
      await refresh(nextId);
    },
    [refresh],
  );

  const value = useMemo<UserProfilesContextValue>(() => {
    const activeUser = findUser(activeUserId ?? undefined);
    return {
      users,
      loading,
      error,
      activeUserId,
      activeUser: activeUser ?? null,
      selectUser,
      selectUserWithPin,
      verifyPin,
      refresh,
      createUser,
      renameUser,
      updateColor,
      setPin,
      clearPin,
      deleteUser,
      setTraktAccount,
      clearTraktAccount,
      pendingPinUserId,
      setPendingPinUserId,
      cancelPinEntry,
      isInitialPinCheck,
    };
  }, [
    users,
    loading,
    error,
    activeUserId,
    selectUser,
    selectUserWithPin,
    verifyPin,
    refresh,
    createUser,
    renameUser,
    updateColor,
    setPin,
    clearPin,
    deleteUser,
    setTraktAccount,
    clearTraktAccount,
    pendingPinUserId,
    cancelPinEntry,
    isInitialPinCheck,
    findUser,
  ]);

  return <UserProfilesContext.Provider value={value}>{children}</UserProfilesContext.Provider>;
};

export const useUserProfiles = (): UserProfilesContextValue => {
  const context = useContext(UserProfilesContext);
  if (context === undefined) {
    throw new Error('useUserProfiles must be used within a UserProfilesProvider');
  }
  return context;
};
