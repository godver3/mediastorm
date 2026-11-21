import { apiPath } from '@/api/basePath';
import { sessionStore } from '@/auth/sessionStore';

/**
 * Image URL usable in an <img>. The server serves some artwork itself (Plex and
 * Jellyfin posters via `/api/library/items/{id}/artwork/{kind}`), which needs the
 * session. Browsers can't attach the Authorization header to an image, so the
 * token goes in `?token=` (the backend allows that only for image/media paths,
 * api/middleware.go queryTokenAllowed) with the active profile, as the native
 * clients' ServerArtwork does. Absolute (TMDB etc.) URLs pass through unchanged.
 */
export function artworkUrl(url: string | null | undefined): string | undefined {
  if (!url) return undefined;
  if (!url.startsWith('/api/')) return url;
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
