import type { QueryClient } from '@tanstack/react-query';
import { calendarKeys } from './calendar';
import { detailsKeys } from './details';
import { homeKeys } from './home';
import { listsKeys } from './lists';

/**
 * Cross-page cache invalidation. Watchlist membership and watch state show up on
 * several pages (details bundle, Home shelves + continue watching, the Watchlist /
 * Lists display-list pages, Calendar), each under its own key factory. Mutations call
 * these instead of guessing keys, so every page that shows the change refetches.
 */
function invalidateProfile(
  qc: QueryClient,
  roots: readonly (readonly string[])[],
  profileId: string,
) {
  return Promise.all(roots.map((root) => qc.invalidateQueries({ queryKey: [...root, profileId] })));
}

/** After a watchlist add/remove. */
export function invalidateWatchlist(qc: QueryClient, profileId: string) {
  return invalidateProfile(
    qc,
    [listsKeys.all, homeKeys.all, detailsKeys.all, calendarKeys.all],
    profileId,
  );
}

/** After mark watched / unwatched (movie, episodes, season, show). */
export function invalidateWatchState(qc: QueryClient, profileId: string) {
  return invalidateProfile(
    qc,
    [listsKeys.all, homeKeys.all, detailsKeys.all, calendarKeys.all],
    profileId,
  );
}
