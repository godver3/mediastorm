/**
 * The backend can be mounted under a base path (settings.Server.BasePath), e.g.
 * `https://host/media/watch-next/`. Everything before the first `/watch` segment of
 * the current pathname is that base path — same rule the Expo web app and
 * playback.html use.
 */
export function serverBasePath(pathname: string = window.location.pathname): string {
  const index = pathname.indexOf('/watch');
  if (index <= 0) return '';
  return pathname.slice(0, index).replace(/\/+$/, '');
}

/** Absolute-path URL for an API endpoint, e.g. apiPath('/users') -> '/media/api/users'. */
export function apiPath(endpoint: string): string {
  const suffix = endpoint.startsWith('/') ? endpoint : `/${endpoint}`;
  return `${serverBasePath()}/api${suffix}`;
}
