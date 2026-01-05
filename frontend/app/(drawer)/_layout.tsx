import { useBackendSettings } from '@/components/BackendSettingsContext';
import { CustomMenu } from '@/components/CustomMenu';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SpatialNavigationRoot } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Direction } from '@bam.tech/lrud';
import { MaterialCommunityIcons } from '@expo/vector-icons';
import { useFocusEffect } from 'expo-router';
import { Stack } from 'expo-router/stack';
import { Tabs } from 'expo-router/tabs';
import { useCallback, useEffect, type ComponentProps } from 'react';
import { Platform, StyleSheet, View, Text } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useMenuContext } from '../../components/MenuContext';
import { TVBackground } from '../../components/TVBackground';
import { useUserProfiles } from '../../components/UserProfilesContext';
import { useShouldUseTabs } from '../../hooks/useShouldUseTabs';

// Tabs that should remain accessible when backend is unreachable
const ALWAYS_ACCESSIBLE_TABS = ['index', 'settings'];

export default function DrawerLayout() {
  const theme = useTheme();
  const tabStyles = useTabsStyles(theme);
  const { isOpen: isMenuOpen, closeMenu } = useMenuContext();
  const insets = useSafeAreaInsets();
  const { isBackendReachable, loading: settingsLoading, isReady: settingsReady } = useBackendSettings();
  const { activeUser } = useUserProfiles();

  const shouldUseTabs = useShouldUseTabs();

  // Backend is considered available if reachable OR still loading initially
  const isBackendAvailable = isBackendReachable || (settingsLoading && !settingsReady);

  const isTabDisabled = useCallback(
    (tabName: string) => {
      if (isBackendAvailable) {
        return false;
      }
      return !ALWAYS_ACCESSIBLE_TABS.includes(tabName);
    },
    [isBackendAvailable],
  );

  // Lock orientation to portrait for drawer screens on mobile devices
  // Use useFocusEffect to re-lock orientation when returning from player
  useFocusEffect(
    useCallback(() => {
      if (Platform.OS === 'web' || Platform.isTV) {
        return;
      }

      const lockOrientation = async () => {
        try {
          // Dynamic require to avoid loading native module at parse time
          const ScreenOrientation = require('expo-screen-orientation');
          await ScreenOrientation.lockAsync(ScreenOrientation.OrientationLock.PORTRAIT_UP);
          console.log('[DrawerLayout] Screen orientation locked to portrait');
        } catch (error) {
          console.warn('[DrawerLayout] Failed to lock screen orientation:', error);
        }
      };

      lockOrientation();
    }, []),
  );

  // On tvOS, disable menu key handling when drawer is open so the Menu button
  // will minimize the app instead of being captured by our event handler
  useEffect(() => {
    if (Platform.OS !== 'ios' || !Platform.isTV) {
      return;
    }

    // When drawer is open, disable menu key so system handles it (minimizes app)
    // When drawer is closed, enable menu key so we can use it for navigation
    RemoteControlManager.setTvMenuKeyEnabled(!isMenuOpen);
  }, [isMenuOpen]);

  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      if (movement === 'right') {
        closeMenu();
      }
    },
    [closeMenu],
  );

  if (shouldUseTabs) {
    return (
      <Tabs
        screenOptions={({ route }) => {
          const disabled = isTabDisabled(route.name);
          return {
            headerShown: false,
            sceneStyle: { backgroundColor: theme.colors.background.base },
            tabBarActiveTintColor: disabled ? theme.colors.text.disabled : theme.colors.accent.primary,
            tabBarInactiveTintColor: disabled ? theme.colors.text.disabled : theme.colors.text.muted,
            tabBarLabelStyle: [tabStyles.tabLabel, disabled && tabStyles.tabLabelDisabled],
            tabBarStyle: [
              tabStyles.tabBar,
              {
                paddingBottom: Math.max(insets.bottom, theme.spacing.md),
                height: 56 + Math.max(insets.bottom, theme.spacing.md),
              },
            ],
            tabBarIcon: ({ color, focused }) => {
              if (route.name === 'profiles' && activeUser) {
                return (
                  <View
                    style={{
                      width: 24,
                      height: 24,
                      borderRadius: 12,
                      backgroundColor: activeUser.color || theme.colors.background.elevated,
                      justifyContent: 'center',
                      alignItems: 'center',
                      borderWidth: focused ? 2 : 0,
                      borderColor: theme.colors.accent.primary,
                      opacity: disabled ? 0.4 : 1,
                    }}>
                    <Text style={{ fontSize: 12, fontWeight: '700', color: '#fff' }}>
                      {activeUser.name.charAt(0).toUpperCase()}
                    </Text>
                  </View>
                );
              }
              return (
                <MaterialCommunityIcons
                  name={getTabIconName(route.name)}
                  size={24}
                  color={disabled ? theme.colors.text.disabled : (color ?? theme.colors.text.muted)}
                  style={disabled ? { opacity: 0.4 } : undefined}
                />
              );
            },
            // Slide animation for tab transitions
            animation: 'shift',
          };
        }}
        screenListeners={{
          tabPress: (e) => {
            // Prevent navigation to disabled tabs
            const routeName = e.target?.split('-')[0];
            if (routeName && isTabDisabled(routeName)) {
              e.preventDefault();
            }
          },
        }}>
        <Tabs.Screen name="index" options={{ title: 'Home' }} />
        <Tabs.Screen name="search" options={{ title: 'Search' }} />
        <Tabs.Screen name="watchlist" options={{ title: 'Watchlist' }} />
        <Tabs.Screen name="live" options={{ title: 'Live' }} />
        <Tabs.Screen name="profiles" options={{ title: 'Profiles' }} />
        <Tabs.Screen name="settings" options={{ title: 'Settings' }} />
        <Tabs.Screen
          name="tv"
          options={{
            href: null,
          }}
        />
        <Tabs.Screen
          name="nav-test-basic"
          options={{
            href: null,
          }}
        />
        <Tabs.Screen
          name="nav-test-manual"
          options={{
            href: null,
          }}
        />
        <Tabs.Screen
          name="nav-test-minimal"
          options={{
            href: null,
          }}
        />
        <Tabs.Screen
          name="nav-test-flatlist"
          options={{
            href: null,
          }}
        />
        <Tabs.Screen
          name="nav-test-native"
          options={{
            href: null,
          }}
        />
        <Tabs.Screen
          name="modal-test"
          options={{
            href: null,
          }}
        />
        <Tabs.Screen
          name="debug"
          options={{
            href: null,
          }}
        />
        <Tabs.Screen
          name="debug2"
          options={{
            href: null,
          }}
        />
      </Tabs>
    );
  }

  return (
    <View style={{ flex: 1 }}>
      <TVBackground>
        <Stack
          screenOptions={{
            headerShown: false,
            contentStyle: { backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base },
          }}>
          <Stack.Screen name="index" />
          <Stack.Screen name="search" />
          <Stack.Screen name="watchlist" />
          <Stack.Screen name="live" />
          <Stack.Screen name="profiles" />
          <Stack.Screen name="settings" />
          <Stack.Screen name="tv" />
          <Stack.Screen name="nav-test-basic" />
          <Stack.Screen name="nav-test-manual" />
          <Stack.Screen name="nav-test-minimal" />
          <Stack.Screen name="nav-test-flatlist" />
          <Stack.Screen name="nav-test-native" />
          <Stack.Screen name="modal-test" />
          <Stack.Screen name="debug" />
          <Stack.Screen name="debug2" />
        </Stack>
      </TVBackground>

      {/* Custom menu overlay */}
      {/* Android TV uses native focus handling, so skip SpatialNavigationRoot */}
      {Platform.OS === 'android' && Platform.isTV ? (
        <CustomMenu isVisible={isMenuOpen} onClose={closeMenu} />
      ) : (
        <SpatialNavigationRoot
          isActive={isMenuOpen}
          onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
          <CustomMenu isVisible={isMenuOpen} onClose={closeMenu} />
        </SpatialNavigationRoot>
      )}
    </View>
  );
}

const useTabsStyles = function (theme: NovaTheme) {
  return StyleSheet.create({
    tabBar: {
      backgroundColor: theme.colors.background.surface,
      borderTopColor: theme.colors.border.subtle,
      borderTopWidth: StyleSheet.hairlineWidth,
      paddingTop: theme.spacing.xs,
    },
    tabLabel: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
    },
    tabLabelDisabled: {
      color: theme.colors.text.disabled,
      opacity: 0.5,
    },
  });
};

type TabIconName = ComponentProps<typeof MaterialCommunityIcons>['name'];

const getTabIconName = (routeName: string): TabIconName => {
  switch (routeName) {
    case 'index':
      return 'home-variant';
    case 'search':
      return 'magnify';
    case 'watchlist':
      return 'playlist-star';
    case 'live':
      return 'television-play';
    case 'profiles':
      return 'account-multiple';
    case 'settings':
      return 'cog';
    default:
      return 'dots-horizontal';
  }
};
