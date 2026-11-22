import { apiRequest } from './client';
import type { AuthSession } from './types';

export interface LoginRequest {
  username: string;
  password: string;
  rememberMe?: boolean;
  /** Required (428 default_password) when the master account still uses the default password. */
  newPassword?: string;
}

/** Native clients always send rememberMe: true (persistent session); default to that. */
export const login = (req: LoginRequest) =>
  apiRequest<AuthSession>('/auth/login', {
    method: 'POST',
    body: { rememberMe: true, ...req },
    noAuthRecovery: true,
  });

export const logout = () =>
  apiRequest<void>('/auth/logout', { method: 'POST', noAuthRecovery: true });

export interface AuthMe {
  id: string;
  username: string;
  isMaster: boolean;
}

export const getMe = (signal?: AbortSignal) => apiRequest<AuthMe>('/auth/me', { signal });
