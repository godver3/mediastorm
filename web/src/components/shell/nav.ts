import type { IconName } from '../icons';

/** Sidebar entries. `to` must match a route under src/routes/_app/. */
export interface NavItem {
  to:
    | '/'
    | '/search'
    | '/watchlist'
    | '/live'
    | '/recordings'
    | '/calendar'
    | '/lists'
    | '/sports'
    | '/libraries'
    | '/settings';
  label: string;
  icon: IconName;
}

export const NAV_ITEMS: NavItem[] = [
  { to: '/', label: 'Home', icon: 'home' },
  { to: '/search', label: 'Search', icon: 'search' },
  { to: '/watchlist', label: 'Watchlist', icon: 'bookmark' },
  { to: '/live', label: 'Live TV', icon: 'tv' },
  { to: '/recordings', label: 'Recordings', icon: 'record' },
  { to: '/calendar', label: 'Calendar', icon: 'calendar' },
  { to: '/lists', label: 'Lists', icon: 'list' },
  { to: '/sports', label: 'Sports', icon: 'trophy' },
  { to: '/libraries', label: 'Libraries', icon: 'library' },
];

export const SETTINGS_ITEM: NavItem = { to: '/settings', label: 'Settings', icon: 'settings' };
