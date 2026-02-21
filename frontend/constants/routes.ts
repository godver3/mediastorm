/**
 * Route constants for the application
 */

/**
 * Drawer routes where the drawer should auto-open on tvOS
 * and where the menu/back button toggles the drawer
 */
export const DRAWER_ROUTES = [
  '/',
  '/index',
  '/search',
  '/watchlist',
  '/live',
  '/profiles',
  '/settings',
  '/lists',
  '/explore',
  '/tv',
  '/modal-test',
] as const;

/**
 * Check if a pathname is a drawer route
 */
export const isDrawerRoute = (pathname: string): boolean => {
  return DRAWER_ROUTES.includes(pathname as any);
};
