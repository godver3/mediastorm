import { CustomMenu } from '@/components/CustomMenu';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { useTheme } from '@/theme';
import { isTablet } from '@/theme/tokens/tvScale';
import { useFocusEffect } from 'expo-router';
import { Stack } from 'expo-router/stack';
import { Tabs } from 'expo-router/tabs';
import { useCallback, useEffect } from 'react';
import { Platform, View } from 'react-native';
import { MobileTabBar } from '../../components/MobileTabBar';
import { useMenuContext } from '../../components/MenuContext';
import { TVBackground } from '../../components/TVBackground';
import { useUserProfiles } from '../../components/UserProfilesContext';
import { useShouldUseTabs } from '../../hooks/useShouldUseTabs';
import { useMemoryMonitor } from '../../hooks/useMemoryMonitor';
import { DownloadFAB } from '../../components/DownloadFAB';

export default function DrawerLayout() {
  const theme = useTheme();
  const { isOpen: isMenuOpen, closeMenu } = useMenuContext();
  const { profileSelectorVisibleRef } = useUserProfiles();

  const shouldUseTabs = useShouldUseTabs();

  // Monitor memory usage in dev mode (logs every 15s)
  useMemoryMonitor('DrawerLayout', 15000, __DEV__);

  // Lock orientation to portrait for phones only
  // Tablets and TVs can use any orientation
  // Use useFocusEffect to re-lock orientation when returning from player
  useFocusEffect(
    useCallback(() => {
      // Skip orientation lock for web, TV, and tablets
      if (Platform.OS === 'web' || Platform.isTV || isTablet) {
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

  // Close the drawer when profile selector becomes visible so it's not
  // lingering behind the overlay. Check ref in a menu-state-driven effect.
  useEffect(() => {
    if (profileSelectorVisibleRef.current && isMenuOpen) {
      closeMenu();
    }
  }, [isMenuOpen, closeMenu]);

  // On tvOS, disable menu key handling when drawer is open so the Menu
  // button minimizes the app instead of being captured. Profile selector
  // handles its own tvOS menu key state independently.
  useEffect(() => {
    if (Platform.OS !== 'ios' || !Platform.isTV) {
      return;
    }

    RemoteControlManager.setTvMenuKeyEnabled(!isMenuOpen);
  }, [isMenuOpen]);

  if (shouldUseTabs) {
    return (
      <View style={{ flex: 1 }}>
      <Tabs
        tabBar={(props) => <MobileTabBar activeTab={props.state.routes[props.state.index]?.name as any} />}
        screenOptions={{
          headerShown: false,
          sceneStyle: { backgroundColor: theme.colors.background.base },
        }}>
        <Tabs.Screen name="index" options={{ title: 'Home' }} />
        <Tabs.Screen name="search" options={{ title: 'Search' }} />
        <Tabs.Screen name="lists" options={{ title: 'Lists' }} />
        <Tabs.Screen name="watchlist" options={{ href: null }} />
        <Tabs.Screen name="live" options={{ title: 'Live' }} />
        <Tabs.Screen name="profiles" options={{ title: 'Profiles' }} />
        <Tabs.Screen name="downloads" options={Platform.isTV || Platform.OS === 'web' ? { href: null } : { title: 'Downloads' }} />
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
      </Tabs>
      <DownloadFAB />
      </View>
    );
  }

  return (
    <View style={{ flex: 1 }}>
      <TVBackground>
        <Stack
          screenOptions={{
            headerShown: false,
            contentStyle: { backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base },
            // Keep screens mounted when navigating away to preserve state
            freezeOnBlur: true,
          }}>
          <Stack.Screen name="index" />
          <Stack.Screen name="search" />
          <Stack.Screen name="lists" />
          <Stack.Screen name="watchlist" />
          <Stack.Screen name="live" />
          <Stack.Screen name="profiles" />
          <Stack.Screen name="downloads" />
          <Stack.Screen name="settings" />
          <Stack.Screen name="tv" />
          <Stack.Screen name="nav-test-basic" />
          <Stack.Screen name="nav-test-manual" />
          <Stack.Screen name="nav-test-minimal" />
          <Stack.Screen name="nav-test-flatlist" />
          <Stack.Screen name="nav-test-native" />
          <Stack.Screen name="modal-test" />
        </Stack>
      </TVBackground>

      {/* Custom menu overlay - unified native focus for all platforms */}
      <CustomMenu isVisible={isMenuOpen} onClose={closeMenu} />
    </View>
  );
}

