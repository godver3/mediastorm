import { serverBasePath } from '@/api/basePath';
import { sessionStore } from '@/auth/sessionStore';

/**
 * Card / hero image URL. TMDB "original" images are multi-megabyte, so they are
 * swapped for a sized variant (as native `Artwork.url` does). Server-relative
 * `/api/...` art (Plex/Jellyfin/local posters) gets the base path plus the token
 * and profile as query params, since an <img> can't send the bearer header.
 */
export function artworkUrl(raw: string | null | undefined, size = 'w780'): string | undefined {
  if (!raw) return undefined;
  if (raw.startsWith('/api/')) {
    const url = new URL(`${serverBasePath()}${raw}`, window.location.origin);
    const token = sessionStore.getToken();
    const profileId = sessionStore.getState().profileId;
    if (token && !url.searchParams.has('token')) url.searchParams.set('token', token);
    if (profileId && !url.searchParams.has('profileId'))
      url.searchParams.set('profileId', profileId);
    return url.pathname + url.search;
  }
  return raw.replace('/t/p/original/', `/t/p/${size}/`);
}
