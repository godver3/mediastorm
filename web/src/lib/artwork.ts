import { apiPath } from '@/api/basePath';
import { sessionStore } from '@/auth/sessionStore';

/**
 * Image URL usable in an <img>.
 * - Server artwork (`/api/...`, e.g. Plex/Jellyfin/local posters via
 *   `/api/library/items/{id}/artwork/{kind}`) needs the session. Browsers can't
 *   attach the Authorization header to an image, so the token goes in `?token=`
 *   (allowed only for image/media paths, api/middleware.go queryTokenAllowed)
 *   with the active profile, as the native clients' ServerArtwork does.
 * - TMDB "original" images are multi-megabyte, so they are swapped for a sized
 *   variant (as native `Artwork.url` does). Pass `size: null` to keep the URL.
 */
export function artworkUrl(
  url: string | null | undefined,
  size: string | null = 'w780',
): string | undefined {
  if (!url) return undefined;
  if (!url.startsWith('/api/')) {
    return size ? url.replace('/t/p/original/', `/t/p/${size}/`) : url;
  }
  const { profileId } = sessionStore.getState();
  const q = url.indexOf('?');
  const path = (q >= 0 ? url.slice(0, q) : url).slice('/api'.length);
  const params = new URLSearchParams(q >= 0 ? url.slice(q + 1) : '');
  if (profileId && !params.has('profileId')) params.set('profileId', profileId);
  const token = sessionStore.getToken();
  if (token) params.set('token', token);
  const qs = params.toString();
  return `${apiPath(path)}${qs ? `?${qs}` : ''}`;
}
