// Initialize why-did-you-render before any component imports (dev only)
import '../wdyr';

// Initialize logger first to capture all console output
import { logger } from '../services/logger';
logger.init();

import { startupTiming } from '../services/startup-timing';
startupTiming.mark('imports_start');

import { DarkTheme, ThemeProvider } from '@react-navigation/native';
import { MaterialCommunityIcons } from '@expo/vector-icons';
import Constants from 'expo-constants';
import { useFonts } from 'expo-font';
import { Stack } from 'expo-router';
import * as SplashScreen from 'expo-splash-screen';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ActivityIndicator, Dimensions, Linking, Platform, ScrollView, View } from 'react-native';
import { GestureHandlerRootView } from 'react-native-gesture-handler';
import {
  SafeAreaProvider,
  initialWindowMetrics,
  useSafeAreaFrame,
  useSafeAreaInsets,
} from 'react-native-safe-area-context';

import { AuthProvider, useAuth } from '../components/AuthContext';
import { BackendSettingsProvider } from '../components/BackendSettingsContext';
import { LiveProvider } from '../components/LiveContext';
import { MultiscreenProvider } from '../components/MultiscreenContext';
import { UpdateChecker } from '../components/UpdateChecker';
import { LoadingScreenProvider } from '../components/LoadingScreenContext';
import { MenuProvider } from '../components/MenuContext';
import { ContinueWatchingProvider } from '../components/ContinueWatchingContext';
import { PinEntryModal } from '../components/PinEntryModal';
import { ProfileSelectorModal } from '../components/ProfileSelectorModal';
import { StartupDataProvider } from '../components/StartupDataContext';
import { ToastProvider } from '../components/ToastContext';
import { UserProfilesProvider } from '../components/UserProfilesContext';
import { WatchlistProvider } from '../components/WatchlistContext';
import { WatchStatusProvider } from '../components/WatchStatusContext';
import { MovieReleasesProvider } from '../components/MovieReleasesContext';
import { NovaThemeProvider } from '../theme';
import { GoBackConfiguration } from '@/services/remote-control/GoBackConfiguration';
import { SpatialNavigationDeviceTypeProvider } from '@/services/tv-navigation';
import { hasTouchSupport, useTVLayout } from '@/theme/tokens/tvScale';
import LoginScreen from './login';

import ConfigureRemoteControl from './configureRemoteControl';
import { DevPerfOverlay } from '../components/DevPerfOverlay';

startupTiming.mark('imports_done');

// Prevent the splash screen from auto-hiding before asset loading is complete.
SplashScreen.preventAutoHideAsync();

/**
 * AuthGate renders the login screen when unauthenticated,
 * or the main app when authenticated.
 */
function AuthGate() {
  const { isAuthenticated, isLoading } = useAuth();
  startupTiming.mark(isLoading ? 'AuthGate_loading' : `AuthGate_resolved(authenticated=${isAuthenticated})`);

  // Show loading indicator while checking stored auth
  if (isLoading) {
    return (
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: '#0b0b0f' }}>
        <ActivityIndicator size="large" color="#3f66ff" />
      </View>
    );
  }

  // Show login screen if not authenticated
  if (!isAuthenticated) {
    return (
      <NovaThemeProvider>
        <LoadingScreenProvider>
          <ToastProvider>
            <SpatialNavigationDeviceTypeProvider>
              <ConfigureRemoteControl />
              <LoginScreen />
            </SpatialNavigationDeviceTypeProvider>
          </ToastProvider>
        </LoadingScreenProvider>
      </NovaThemeProvider>
    );
  }

  // Show main app when authenticated
  return (
    <UserProfilesProvider>
      <PinEntryModal />
      <ProfileSelectorModal />
      <StartupDataProvider>
        <LiveProvider>
          <MultiscreenProvider>
            <MovieReleasesProvider>
              <WatchlistProvider>
                <WatchStatusProvider>
                  <ContinueWatchingProvider>
                    <MenuProvider>
                      <NovaThemeProvider>
                        <LoadingScreenProvider>
                          <ToastProvider>
                            <ThemeProvider value={DarkTheme}>
                              <SpatialNavigationDeviceTypeProvider>
                                <ConfigureRemoteControl />
                                <GoBackConfiguration />
                                <Stack
                                  screenOptions={{
                                    headerShown: false,
                                    // Enable native swipe-back gesture on touch devices (phones + tablets)
                                    gestureEnabled: hasTouchSupport,
                                    gestureDirection: 'horizontal',
                                    // Tablets keep animations despite TV layout; only actual TV disables them
                                    animation: hasTouchSupport ? 'default' : 'none',
                                    // Freeze inactive screens to free memory - critical for low-RAM devices like Fire Stick
                                    freezeOnBlur: true,
                                  }}>
                                  {/* Drawer as the main screen - uses file-based routing */}
                                  <Stack.Screen name="(drawer)" options={{ headerShown: false }} />
                                  {/* Details should render as a standard screen so it shares navigation affordances */}
                                  <Stack.Screen
                                    name="details"
                                    options={{
                                      headerShown: false,
                                      // Enable swipe-back gesture on touch devices
                                      gestureEnabled: hasTouchSupport,
                                      gestureDirection: 'horizontal',
                                      animation: hasTouchSupport ? 'slide_from_right' : 'none',
                                    }}
                                  />
                                  <Stack.Screen
                                    name="player"
                                    options={{
                                      presentation: Platform.isTV ? 'card' : 'fullScreenModal',
                                      animation: 'fade',
                                    }}
                                  />
                                  <Stack.Screen
                                    name="multiscreen"
                                    options={{ presentation: Platform.isTV ? 'card' : 'fullScreenModal' }}
                                  />
                                </Stack>
                              </SpatialNavigationDeviceTypeProvider>
                            </ThemeProvider>
                          </ToastProvider>
                        </LoadingScreenProvider>
                      </NovaThemeProvider>
                    </MenuProvider>
                  </ContinueWatchingProvider>
                </WatchStatusProvider>
              </WatchlistProvider>
            </MovieReleasesProvider>
          </MultiscreenProvider>
        </LiveProvider>
      </StartupDataProvider>
    </UserProfilesProvider>
  );
}

if (Platform.OS === 'ios') {
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  (ScrollView as any).defaultProps = {
    ...((ScrollView as any).defaultProps ?? {}),
    contentInsetAdjustmentBehavior: 'never',
    automaticallyAdjustContentInsets: false,
  };
}

const fallbackSafeAreaMetrics =
  Platform.OS === 'ios'
    ? {
        frame: {
          x: 0,
          y: 0,
          width: Dimensions.get('window').width,
          height: Dimensions.get('window').height,
        },
        insets: {
          top: Constants.statusBarHeight ?? 0,
          bottom: 0,
          left: 0,
          right: 0,
        },
      }
    : null;

export default function RootLayout() {
  startupTiming.mark('RootLayout_render');
  const [loaded, error] = useFonts({
    SpaceMono: require('../assets/fonts/SpaceMono-Regular.ttf'),
    // Preload icon fonts to prevent layout shift when icons render
    ...MaterialCommunityIcons.font,
  });
  const [appIsReady, setAppIsReady] = useState(false);
  // Work around react-native-tvos reporting a zero top inset on first render, which causes a visible
  // layout jump once safe-area data arrives from native.
  const safeAreaInitialMetrics = useMemo(() => {
    if (__DEV__ && Platform.OS === 'ios') {
      console.log('[SafeArea] deriving initial metrics', {
        raw: initialWindowMetrics,
        fallback: fallbackSafeAreaMetrics,
      });
    }

    if (!initialWindowMetrics) {
      return fallbackSafeAreaMetrics ?? undefined;
    }

    const topInset = initialWindowMetrics.insets.top;
    if (topInset > 0) {
      return initialWindowMetrics;
    }

    if (fallbackSafeAreaMetrics?.insets.top) {
      return {
        frame: initialWindowMetrics.frame ?? fallbackSafeAreaMetrics.frame,
        insets: {
          ...initialWindowMetrics.insets,
          top: fallbackSafeAreaMetrics.insets.top,
        },
      };
    }

    return initialWindowMetrics;
  }, []);

  useEffect(() => {
    if (__DEV__ && Platform.OS === 'ios') {
      console.log('[SafeArea] using initial metrics', safeAreaInitialMetrics);
    }

    if (loaded || error) {
      startupTiming.mark('fonts_loaded');
      setAppIsReady(true);
      if (error) {
        console.warn(`Error in loading fonts: ${error}`);
      }
    }
  }, [loaded, error]);

  // Global deep link listener for debugging external player callbacks
  useEffect(() => {
    console.log('[DeepLink] Setting up global URL listener');

    // Get initial URL when app launches
    Linking.getInitialURL().then((url) => {
      if (url) {
        console.log('[DeepLink] Initial URL:', url);
      }
    });

    // Listen for URL events
    const subscription = Linking.addEventListener('url', (event) => {
      console.log('[DeepLink] ==========================================');
      console.log('[DeepLink] URL Event Received:', event.url);
      console.log('[DeepLink] ==========================================');

      // Parse and log details
      try {
        const parsed = new URL(event.url);
        console.log('[DeepLink] Scheme:', parsed.protocol);
        console.log('[DeepLink] Host:', parsed.host);
        console.log('[DeepLink] Pathname:', parsed.pathname);
        console.log('[DeepLink] Search:', parsed.search);
        console.log('[DeepLink] Hash:', parsed.hash);

        // Log all query parameters
        if (parsed.search) {
          console.log('[DeepLink] Query Parameters:');
          parsed.searchParams.forEach((value, key) => {
            console.log(`[DeepLink]   ${key} = ${value}`);
          });
        }
      } catch (e) {
        console.log('[DeepLink] Failed to parse URL:', e);
      }

      console.log('[DeepLink] ==========================================');
    });

    return () => {
      subscription.remove();
    };
  }, []);

  const onLayoutRootView = useCallback(async () => {
    if (appIsReady) {
      startupTiming.mark('splash_hide');
      startupTiming.end();
      await SplashScreen.hideAsync();
    }
  }, [appIsReady]);

  if (!appIsReady) {
    return null;
  }

  return (
    <GestureHandlerRootView style={{ flex: 1 }}>
      <SafeAreaProvider initialMetrics={safeAreaInitialMetrics}>
        {__DEV__ && Platform.OS === 'ios' ? <SafeAreaDebugLogger /> : null}
        <View style={{ flex: 1 }} onLayout={onLayoutRootView}>
          <UpdateChecker>
            <BackendSettingsProvider>
              <AuthProvider>
                <AuthGate />
              </AuthProvider>
            </BackendSettingsProvider>
          </UpdateChecker>
          {/* <DevPerfOverlay /> */}
        </View>
      </SafeAreaProvider>
    </GestureHandlerRootView>
  );
}

function SafeAreaDebugLogger() {
  const insets = useSafeAreaInsets();
  const frame = useSafeAreaFrame();
  const previousInsets = useRef(insets);
  const previousFrame = useRef(frame);

  useEffect(() => {
    if (!(__DEV__ && Platform.OS === 'ios')) {
      return;
    }

    if (
      !previousInsets.current ||
      previousInsets.current.top !== insets.top ||
      previousInsets.current.bottom !== insets.bottom ||
      previousInsets.current.left !== insets.left ||
      previousInsets.current.right !== insets.right
    ) {
      console.log('[SafeArea] insets changed', { previous: previousInsets.current, next: insets });
      previousInsets.current = insets;
    }
  }, [insets]);

  useEffect(() => {
    if (!(__DEV__ && Platform.OS === 'ios')) {
      return;
    }

    if (
      !previousFrame.current ||
      previousFrame.current.width !== frame.width ||
      previousFrame.current.height !== frame.height ||
      previousFrame.current.x !== frame.x ||
      previousFrame.current.y !== frame.y
    ) {
      console.log('[SafeArea] frame changed', { previous: previousFrame.current, next: frame });
      previousFrame.current = frame;
    }
  }, [frame]);

  return null;
}
