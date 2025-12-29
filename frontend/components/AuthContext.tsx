import AsyncStorage from '@react-native-async-storage/async-storage';
import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';

import { apiService } from '@/services/api';

const AUTH_TOKEN_KEY = 'strmr.authToken';
const AUTH_ACCOUNT_KEY = 'strmr.authAccount';

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
  const [isLoading, setIsLoading] = useState(true);
  const [token, setToken] = useState<string | null>(null);
  const [account, setAccount] = useState<AuthAccount | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    return () => {
      mountedRef.current = false;
    };
  }, []);

  // Load stored auth state on mount
  useEffect(() => {
    const loadStoredAuth = async () => {
      try {
        const [storedToken, storedAccountJson] = await Promise.all([
          AsyncStorage.getItem(AUTH_TOKEN_KEY),
          AsyncStorage.getItem(AUTH_ACCOUNT_KEY),
        ]);

        if (storedToken && storedAccountJson) {
          const storedAccount = JSON.parse(storedAccountJson) as AuthAccount;

          // Update API service with stored token
          apiService.setAuthToken(storedToken);

          if (mountedRef.current) {
            setToken(storedToken);
            setAccount(storedAccount);
          }

          // Validate the session is still valid
          try {
            const response = await fetch(`${apiService.getBaseUrl()}/auth/me`, {
              headers: {
                Authorization: `Bearer ${storedToken}`,
                'Content-Type': 'application/json',
              },
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
            }
          } catch (err) {
            // Network error - keep stored auth, will be validated on next request
            console.warn('[Auth] Failed to validate stored session:', err);
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
    await Promise.all([
      AsyncStorage.removeItem(AUTH_TOKEN_KEY),
      AsyncStorage.removeItem(AUTH_ACCOUNT_KEY),
    ]);
  };

  const storeAuth = async (authToken: string, authAccount: AuthAccount) => {
    await Promise.all([
      AsyncStorage.setItem(AUTH_TOKEN_KEY, authToken),
      AsyncStorage.setItem(AUTH_ACCOUNT_KEY, JSON.stringify(authAccount)),
    ]);
  };

  const login = useCallback(async (username: string, password: string) => {
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
  }, []);

  const logout = useCallback(async () => {
    try {
      // Call logout endpoint if we have a token
      if (token) {
        await fetch(`${apiService.getBaseUrl()}/auth/logout`, {
          method: 'POST',
          headers: {
            Authorization: `Bearer ${token}`,
            'Content-Type': 'application/json',
          },
        }).catch(() => {
          // Ignore logout errors - we'll clear local state anyway
        });
      }
    } finally {
      // Clear local auth state
      await clearStoredAuth();
      apiService.setAuthToken(null);

      if (mountedRef.current) {
        setToken(null);
        setAccount(null);
        setError(null);
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
