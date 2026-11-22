import type { QueryClient } from '@tanstack/react-query';
import { sessionStore } from './sessionStore';

/**
 * Account- and server-level queries survive a switch: the profile list (and each
 * profile's settings, keyed by id) and the server branding.
 */
const KEEP = new Set(['users', 'branding']);
const keepOnSwitch = (key: readonly unknown[]) => KEEP.has(key[0] as string);

/**
 * Drops every profile-scoped query when the selected profile changes: in-flight
 * fetches (and their retries) are aborted first, so nothing still running for the
 * previous profile can land or keep firing requests under its id. Runs on every
 * selectProfile, whichever flow made it (picker, in-place PIN prompt, lock-out).
 * Returns the unsubscribe.
 */
export function bindQueriesToProfile(queryClient: QueryClient): () => void {
  let last = sessionStore.getState().profileId;
  return sessionStore.subscribe(() => {
    const next = sessionStore.getState().profileId;
    if (next === last) return;
    last = next;
    const predicate = (q: { queryKey: readonly unknown[] }) => !keepOnSwitch(q.queryKey);
    void queryClient.cancelQueries({ predicate });
    queryClient.removeQueries({ predicate });
  });
}
