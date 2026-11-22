import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { ApiError } from '@/api/client';
import {
  getSportsPreferences,
  normalizePreferences,
  putSportsPreferences,
  sportsPrefsKey,
  type SportsPreferences,
  type SportsPreferencesEnvelope,
} from '@/api/sportsFollows';
import { useActiveProfile } from '@/auth/hooks';

/**
 * The active profile's sports follows with an optimistic save. A 409 (another device saved first)
 * reloads and re-applies the change on the fresh revision once, as natives do.
 */
export function useSportsFollows() {
  const { profileId } = useActiveProfile();
  const qc = useQueryClient();
  const key = sportsPrefsKey(profileId ?? '');
  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => getSportsPreferences(profileId!, signal),
    enabled: !!profileId,
    staleTime: 5 * 60_000,
  });

  const mutation = useMutation({
    mutationFn: async (change: (p: SportsPreferences) => SportsPreferences) => {
      const current =
        qc.getQueryData<SportsPreferencesEnvelope | null>(key) ??
        ({ revision: 0, preferences: normalizePreferences(null) } as SportsPreferencesEnvelope);
      try {
        return await putSportsPreferences(profileId!, {
          revision: current.revision,
          preferences: change(current.preferences),
        });
      } catch (err) {
        if (!(err instanceof ApiError) || err.status !== 409) throw err;
        const fresh = await getSportsPreferences(profileId!);
        if (!fresh) throw err;
        return putSportsPreferences(profileId!, {
          revision: fresh.revision,
          preferences: change(fresh.preferences),
        });
      }
    },
    onMutate: async (change) => {
      await qc.cancelQueries({ queryKey: key });
      const previous = qc.getQueryData<SportsPreferencesEnvelope | null>(key);
      if (previous)
        qc.setQueryData(key, { ...previous, preferences: change(previous.preferences) });
      return { previous };
    },
    onError: (_e, _c, ctx) => {
      if (ctx?.previous) qc.setQueryData(key, ctx.previous);
    },
    onSuccess: (saved) => qc.setQueryData(key, saved),
  });

  return {
    /** undefined while loading or when the server has no follows. */
    preferences: query.data?.preferences,
    supported: query.data !== null,
    isLoading: query.isLoading,
    update: mutation.mutate,
    error: mutation.error,
  };
}
