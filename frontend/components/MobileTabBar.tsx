import { MaterialCommunityIcons } from '@expo/vector-icons';
import { usePathname, useRouter } from 'expo-router';
import { useMemo } from 'react';
import { Pressable, StyleSheet, Text, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import type { ComponentProps } from 'react';

import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';

import { useShouldUseTabs } from '../hooks/useShouldUseTabs';
import { useUserProfiles } from './UserProfilesContext';

type TabKey = 'index' | 'search' | 'watchlist' | 'live' | 'profiles' | 'settings';

type TabItem = {
  key: TabKey;
  label: string;
  icon: ComponentProps<typeof MaterialCommunityIcons>['name'];
  route: string;
};

const BASE_TAB_HEIGHT = 56;

const TAB_ITEMS: TabItem[] = [
  { key: 'index', label: 'Home', icon: 'home-variant', route: '/(drawer)' },
  { key: 'search', label: 'Search', icon: 'magnify', route: '/(drawer)/search' },
  { key: 'watchlist', label: 'Watchlist', icon: 'playlist-star', route: '/(drawer)/watchlist' },
  { key: 'live', label: 'Live', icon: 'television-play', route: '/(drawer)/live' },
  { key: 'profiles', label: 'Profiles', icon: 'account-multiple', route: '/(drawer)/profiles' },
  { key: 'settings', label: 'Settings', icon: 'cog', route: '/(drawer)/settings' },
];

export interface MobileTabBarProps {
  activeTab?: TabKey;
}

const getActiveTabFromPath = (pathname: string | null): TabKey | undefined => {
  if (!pathname) {
    return undefined;
  }

  if (pathname === '/' || pathname === '/(drawer)' || pathname === '/(drawer)/index') {
    return 'index';
  }

  if (pathname.startsWith('/(drawer)/search')) {
    return 'search';
  }

  if (pathname.startsWith('/(drawer)/watchlist')) {
    return 'watchlist';
  }

  if (pathname.startsWith('/(drawer)/live')) {
    return 'live';
  }

  if (pathname.startsWith('/(drawer)/profiles')) {
    return 'profiles';
  }

  if (pathname.startsWith('/(drawer)/settings')) {
    return 'settings';
  }

  return undefined;
};

export function MobileTabBar({ activeTab }: MobileTabBarProps) {
  const shouldShowTabs = useShouldUseTabs();
  const router = useRouter();
  const pathname = usePathname();
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const { activeUser } = useUserProfiles();

  const styles = useMemo(() => createStyles(theme, insets.bottom), [theme, insets.bottom]);

  const currentTab = activeTab ?? getActiveTabFromPath(pathname);

  if (!shouldShowTabs) {
    return null;
  }

  return (
    <View style={styles.container} accessibilityRole="tablist">
      {TAB_ITEMS.map((item) => {
        const isActive = currentTab === item.key;

        const onPress = () => {
          if (pathname === item.route) {
            return;
          }

          // Special handling for home button when coming from details screen
          // This provides the intuitive "slide from left" animation instead of "slide from right"
          if (item.key === 'index' && pathname === '/details') {
            router.back();
            return;
          }

          router.replace(item.route as any);
        };

        return (
          <Pressable
            key={item.key}
            accessibilityRole="tab"
            accessibilityState={{ selected: isActive }}
            onPress={onPress}
            style={styles.tabButton}
            testID={`mobile-tab-${item.key}`}>
            {item.key === 'profiles' && activeUser ? (
              <View
                style={{
                  width: 24,
                  height: 24,
                  borderRadius: 12,
                  backgroundColor: activeUser.color || theme.colors.background.elevated,
                  justifyContent: 'center',
                  alignItems: 'center',
                  borderWidth: isActive ? 2 : 0,
                  borderColor: theme.colors.accent.primary,
                }}>
                <Text style={{ fontSize: 12, lineHeight: 12, fontWeight: '700', color: '#fff' }}>
                  {activeUser.name.charAt(0).toUpperCase()}
                </Text>
              </View>
            ) : (
              <MaterialCommunityIcons
                name={item.icon}
                size={24}
                color={isActive ? theme.colors.accent.primary : theme.colors.text.muted}
              />
            )}
            <Text style={[styles.tabLabel, isActive && styles.tabLabelActive]}>{item.label}</Text>
          </Pressable>
        );
      })}
    </View>
  );
}

const createStyles = (theme: NovaTheme, bottomInset: number) => {
  const bottomPadding = Math.max(bottomInset, theme.spacing.md);

  return StyleSheet.create({
    container: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-around',
      backgroundColor: theme.colors.background.surface,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      paddingTop: theme.spacing.xs,
      paddingBottom: bottomPadding,
      minHeight: BASE_TAB_HEIGHT + bottomPadding,
    },
    tabButton: {
      flex: 1,
      alignItems: 'center',
      justifyContent: 'center',
      gap: theme.spacing.xs,
      paddingVertical: theme.spacing.xs,
    },
    tabLabel: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
    },
    tabLabelActive: {
      color: theme.colors.accent.primary,
    },
  });
};

export default MobileTabBar;
