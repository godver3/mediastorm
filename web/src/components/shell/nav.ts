import { useQuery } from '@tanstack/react-query';
import { navTabs } from '@/api/settingsExtras';
import { getUserSettings, usersKeys } from '@/api/users';
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
  /** Key in the profile's display.navigationTabVisibility; entries without one always show. */
  tab?: string;
}

export const NAV_ITEMS: NavItem[] = [
  { to: '/', label: 'Home', icon: 'home' },
  { to: '/search', label: 'Search', icon: 'search', tab: 'search' },
  { to: '/watchlist', label: 'Watchlist', icon: 'bookmark', tab: 'watchlist' },
  { to: '/live', label: 'Live TV', icon: 'tv', notForKids: true, tab: 'live' },
  { to: '/recordings', label: 'Recordings', icon: 'record', notForKids: true, tab: 'recordings' },
  { to: '/calendar', label: 'Calendar', icon: 'calendar', tab: 'calendar' },
  { to: '/lists', label: 'Lists', icon: 'list', tab: 'lists' },
  { to: '/sports', label: 'Sports', icon: 'trophy', tab: 'sports' },
  { to: '/libraries', label: 'Libraries', icon: 'library' },
];

export const SETTINGS_ITEM: NavItem = { to: '/settings', label: 'Settings', icon: 'settings' };

/**
 * The sidebar entries the active profile can use: kids profiles get no Live TV or Recordings, and
 * the profile's menu tab visibility (native MenuTabs.swift) hides the rest. `tabs` undefined (not
 * loaded yet) shows everything rather than flashing a shorter menu.
 */
export function visibleNavItems(isKids: boolean, tabs?: string[]): NavItem[] {
  return NAV_ITEMS.filter(
    (i) => !(isKids && i.notForKids) && (!tabs || !i.tab || tabs.includes(i.tab)),
  );
}

export function useNavItems(): NavItem[] {
  const { profileId, profile } = useActiveProfile();
  const settings = useQuery({
    queryKey: usersKeys.settings(profileId ?? ''),
    queryFn: ({ signal }) => getUserSettings(profileId!, signal),
    enabled: !!profileId,
    staleTime: 5 * 60_000,
  });
  const raw = (settings.data as { display?: { navigationTabVisibility?: unknown } } | undefined)
    ?.display?.navigationTabVisibility;
  return visibleNavItems(profile?.isKidsProfile === true, settings.data ? navTabs(raw) : undefined);
}
