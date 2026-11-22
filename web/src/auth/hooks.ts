import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useCallback, useEffect } from 'react';
import { logout as apiLogout } from '@/api/auth';
import { getUserSettings, listUsers, usersKeys } from '@/api/users';
import { applyAccent } from '@/lib/theme';
import { sessionStore, useSessionState } from './sessionStore';

/** Profiles of the signed-in account (GET /api/users). */
export function useProfiles() {
  const { session } = useSessionState();
  return useQuery({
    queryKey: usersKeys.list(),
    queryFn: ({ signal }) => listUsers(signal),
    enabled: !!session,
    staleTime: 60_000,
  });
}

/** The selected profile (null while loading or when none is selected). */
export function useActiveProfile() {
  const { profileId } = useSessionState();
  const query = useProfiles();
  const profile = query.data?.find((p) => p.id === profileId) ?? null;
  return { profileId, profile, query };
}

/** Applies the profile's accent colour (display.appearance.accentColor) to --accent. */
export function useAccent() {
  const { profileId } = useSessionState();
  const settings = useQuery({
    queryKey: usersKeys.settings(profileId ?? ''),
    queryFn: ({ signal }) => getUserSettings(profileId!, signal),
    enabled: !!profileId,
    staleTime: 5 * 60_000,
  });
  const accent = settings.data?.display?.appearance?.accentColor;
  useEffect(() => applyAccent(accent), [accent]);
}

/** Revoke the session (best effort), clear local state and caches, go to /login. */
export function useLogout() {
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  return useCallback(async () => {
    try {
      await apiLogout();
    } catch {
      /* best effort */
    }
    sessionStore.logout();
    queryClient.clear();
    applyAccent(null);
    await navigate({ to: '/login' });
  }, [navigate, queryClient]);
}
