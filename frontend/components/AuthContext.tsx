import AsyncStorage from '@react-native-async-storage/async-storage';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { apiService } from '@/services/api';
import { getClientId, getClientRegistrationPayload } from '@/services/clientId';
import { useBackendSettings } from './BackendSettingsContext';

const AUTH_TOKEN_KEY = 'strmr.authToken';
const AUTH_ACCOUNT_KEY = 'strmr.authAccount';
const BACKEND_URL_KEY = 'strmr.backendUrl';

export interface AuthAccount {
  id: string;
  username: string;
  isMaster: boolean;
}

export interface LoginResponse {
  token: string;
  expiresAt: string;
  accountId: string;
  username: string;
  isMaster: boolean;
}

interface AuthContextValue {
  isAuthenticated: boolean;
  isLoading: boolean;
  account: AuthAccount | null;
  token: string | null;
  error: string | null;
  login: (username: string, password: string) => Promise<void>;
  logout: () => Promise<void>;
  refreshSession: () => Promise<void>;
  clearError: () => void;
}

const AuthContext = createContext<AuthContextValue | undefined>(undefined);

export const AuthProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const mountedRef = useRef(true);
  const { refreshSettings } = useBackendSettings();
  const refreshSettingsRef = useRef(refreshSettings);
  const [isLoading, setIsLoading] = useState(true);
  const [token, setToken] = useState<string | null>(null);
  const [account, setAccount] = useState<AuthAccount | null>(null);
  const [error, setError] = useState<string | null>(null);

  // Keep ref updated with latest refreshSettings
  useEffect(() => {
    refreshSettingsRef.current = refreshSettings;
  }, [refreshSettings]);

  useEffect(() => {
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // Load stored auth state on mount
  useEffect(() => {
    const loadStoredAuth = async () => {
      try {
        // Initialize client ID first (always, regardless of auth state)
        const clientId = await getClientId();
        apiService.setClientId(clientId);

        // Read backend URL, token, and account in parallel
        const [storedBackendUrl, storedToken, storedAccountJson] = await Promise.all([
          AsyncStorage.getItem(BACKEND_URL_KEY),
          AsyncStorage.getItem(AUTH_TOKEN_KEY),
          AsyncStorage.getItem(AUTH_ACCOUNT_KEY),
        ]);

        // Apply stored backend URL before validating (avoids race with BackendSettingsProvider)
        if (storedBackendUrl) {
          apiService.setBaseUrl(storedBackendUrl);
        }

        if (storedToken && storedAccountJson) {
          const storedAccount = JSON.parse(storedAccountJson) as AuthAccount;

          // Update API service with stored token
          apiService.setAuthToken(storedToken);

          if (mountedRef.current) {
            setToken(storedToken);
            setAccount(storedAccount);
          }

          // Validate the session is still valid (only if we have a backend URL)
          if (storedBackendUrl) {
            const controller = new AbortController();
            const timeoutId = setTimeout(() => controller.abort(), 5000);
            try {
              const response = await fetch(`${apiService.getBaseUrl()}/auth/me`, {
                headers: {
                  Authorization: `Bearer ${storedToken}`,
                  'Content-Type': 'application/json',
                },
                signal: controller.signal,
              });

              if (!response.ok) {
                // Session expired or invalid - clear auth state
                console.log('[Auth] Stored session is invalid, clearing auth state');
                await clearStoredAuth();
                if (mountedRef.current) {
                  setToken(null);
                  setAccount(null);
                }
                apiService.setAuthToken(null);
              } else {
                // Session is valid - register client with backend
                try {
                  const clientPayload = await getClientRegistrationPayload();
                  await apiService.registerClient(clientPayload);
                  console.log('[Auth] Client registered with backend');
                } catch (regErr) {
                  // Non-fatal - client registration is optional
                  console.warn('[Auth] Failed to register client:', regErr);
                }
                // Refresh backend settings now that we're authenticated
                try {
                  console.log('[Auth] Refreshing backend settings after auth validation');
                  await refreshSettingsRef.current();
                } catch (settingsErr) {
                  console.warn('[Auth] Failed to refresh settings after auth:', settingsErr);
                }
              }
            } catch (err) {
              // Network error or timeout - keep stored auth, will be validated on next request
              console.warn('[Auth] Failed to validate stored session:', err);
            } finally {
              clearTimeout(timeoutId);
            }
          }
        }
      } catch (err) {
        console.warn('[Auth] Failed to load stored auth:', err);
      } finally {
        if (mountedRef.current) {
          setIsLoading(false);
        }
      }
    };

    loadStoredAuth();
  }, []);

  const clearStoredAuth = async () => {
    await Promise.all([AsyncStorage.removeItem(AUTH_TOKEN_KEY), AsyncStorage.removeItem(AUTH_ACCOUNT_KEY)]);
  };

  const storeAuth = async (authToken: string, authAccount: AuthAccount) => {
    await Promise.all([
      AsyncStorage.setItem(AUTH_TOKEN_KEY, authToken),
      AsyncStorage.setItem(AUTH_ACCOUNT_KEY, JSON.stringify(authAccount)),
    ]);
  };

  const login = useCallback(
    async (username: string, password: string) => {
      if (!mountedRef.current) return;

      setIsLoading(true);
      setError(null);

      try {
        const response = await fetch(`${apiService.getBaseUrl()}/auth/login`, {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({ username, password, rememberMe: true }),
        });

        if (!response.ok) {
          const errorData = await response.json().catch(() => ({}));
          throw new Error(errorData.error || 'Invalid username or password');
        }

        const data: LoginResponse = await response.json();

        const authAccount: AuthAccount = {
          id: data.accountId,
          username: data.username,
          isMaster: data.isMaster,
        };

        // Store auth state
        await storeAuth(data.token, authAccount);

        // Update API service with new token
        apiService.setAuthToken(data.token);

        if (mountedRef.current) {
          setToken(data.token);
          setAccount(authAccount);
        }

        // Register client with backend (non-blocking, non-fatal)
        try {
          const clientPayload = await getClientRegistrationPayload();
          await apiService.registerClient(clientPayload);
          console.log('[Auth] Client registered with backend after login');
        } catch (regErr) {
          console.warn('[Auth] Failed to register client after login:', regErr);
        }

        // Refresh backend settings now that we're authenticated
        try {
          console.log('[Auth] Refreshing backend settings after login');
          await refreshSettings();
        } catch (settingsErr) {
          console.warn('[Auth] Failed to refresh settings after login:', settingsErr);
        }
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Login failed';
        if (mountedRef.current) {
          setError(message);
        }
        throw err;
      } finally {
        if (mountedRef.current) {
          setIsLoading(false);
        }
      }
    },
    [refreshSettings],
  );

  const logout = useCallback(async () => {
    // Capture token before clearing state
    const currentToken = token;

    // Clear local auth state first so UI updates immediately
    await clearStoredAuth();
    apiService.setAuthToken(null);

    if (mountedRef.current) {
      setToken(null);
      setAccount(null);
      setError(null);
    }

    // Then try to notify backend (non-blocking, with short timeout)
    if (currentToken) {
      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), 3000);
      try {
        await fetch(`${apiService.getBaseUrl()}/auth/logout`, {
          method: 'POST',
          headers: {
            Authorization: `Bearer ${currentToken}`,
            'Content-Type': 'application/json',
          },
          signal: controller.signal,
        });
      } catch {
        // Ignore logout errors - local state already cleared
      } finally {
        clearTimeout(timeoutId);
      }
    }
  }, [token]);

  const refreshSession = useCallback(async () => {
    if (!token || !mountedRef.current) return;

    try {
      const response = await fetch(`${apiService.getBaseUrl()}/auth/refresh`, {
        method: 'POST',
        headers: {
          Authorization: `Bearer ${token}`,
          'Content-Type': 'application/json',
        },
      });

      if (!response.ok) {
        // Session expired - logout
        await logout();
        return;
      }

      const data: LoginResponse = await response.json();

      const authAccount: AuthAccount = {
        id: data.accountId,
        username: data.username,
        isMaster: data.isMaster,
      };

      await storeAuth(data.token, authAccount);
      apiService.setAuthToken(data.token);

      if (mountedRef.current) {
        setToken(data.token);
        setAccount(authAccount);
      }
    } catch (err) {
      console.warn('[Auth] Failed to refresh session:', err);
    }
  }, [token, logout]);

  const clearError = useCallback(() => {
    setError(null);
  }, []);

  const value = useMemo<AuthContextValue>(
    () => ({
      isAuthenticated: !!token && !!account,
      isLoading,
      account,
      token,
      error,
      login,
      logout,
      refreshSession,
      clearError,
    }),
    [isLoading, token, account, error, login, logout, refreshSession, clearError],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};

export const useAuth = () => {
  const context = useContext(AuthContext);
  if (!context) {
    throw new Error('useAuth must be used within an AuthProvider');
  }
  return context;
};
