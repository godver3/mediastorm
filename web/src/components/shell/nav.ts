import { useActiveProfile } from '@/auth/hooks';
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
  /** Live TV areas: the server refuses them for kids profiles, so natives hide them. */
  notForKids?: boolean;
}

export const NAV_ITEMS: NavItem[] = [
  { to: '/', label: 'Home', icon: 'home' },
  { to: '/search', label: 'Search', icon: 'search' },
  { to: '/watchlist', label: 'Watchlist', icon: 'bookmark' },
  { to: '/live', label: 'Live TV', icon: 'tv', notForKids: true },
  { to: '/recordings', label: 'Recordings', icon: 'record', notForKids: true },
  { to: '/calendar', label: 'Calendar', icon: 'calendar' },
  { to: '/lists', label: 'Lists', icon: 'list' },
  { to: '/sports', label: 'Sports', icon: 'trophy' },
  { to: '/libraries', label: 'Libraries', icon: 'library' },
];

export const SETTINGS_ITEM: NavItem = { to: '/settings', label: 'Settings', icon: 'settings' };

/** The sidebar entries the active profile can use (kids profiles get no Live TV or Recordings). */
export function visibleNavItems(isKids: boolean): NavItem[] {
  return isKids ? NAV_ITEMS.filter((i) => !i.notForKids) : NAV_ITEMS;
}

export function useNavItems(): NavItem[] {
  const { profile } = useActiveProfile();
  return visibleNavItems(profile?.isKidsProfile === true);
}
