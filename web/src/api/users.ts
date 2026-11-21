import { apiPath } from './basePath';
import { apiRequest } from './client';
import type { UserProfile, UserSettingsLite, VerifyPinResponse } from './types';

export const usersKeys = {
  all: ['users'] as const,
  list: () => [...usersKeys.all, 'list'] as const,
  settings: (profileId: string) => [...usersKeys.all, profileId, 'settings'] as const,
};

export const listUsers = (signal?: AbortSignal) => apiRequest<UserProfile[]>('/users', { signal });

export const verifyPin = (profileId: string, pin: string) =>
  apiRequest<VerifyPinResponse>(`/users/${encodeURIComponent(profileId)}/pin/verify`, {
    method: 'POST',
    body: { pin },
  });

export const getUserSettings = (profileId: string, signal?: AbortSignal) =>
  apiRequest<UserSettingsLite>(`/users/${encodeURIComponent(profileId)}/settings`, { signal });

/** Public (no auth) profile icon URL when the profile has one; `v` busts caches (as native). */
export const profileIconUrl = (profile: Pick<UserProfile, 'id' | 'hasIcon' | 'iconUrl'>) =>
  profile.hasIcon
    ? `${apiPath(`/users/${encodeURIComponent(profile.id)}/icon`)}?v=${encodeURIComponent(profile.iconUrl ?? '')}`
    : null;
