import { useRouter, usePathname, useNavigation } from 'expo-router';
import React, { useEffect, useRef } from 'react';
import { Platform } from 'react-native';
import { useMenuContext } from '../../components/MenuContext';
import { isDrawerRoute } from '../../constants/routes';
import RemoteControlManager from './RemoteControlManager';

export const GoBackConfiguration: React.FC = () => {
  const router = useRouter();
  const navigation = useNavigation();
  const pathname = usePathname();
  const { isOpen: isMenuOpen, openMenu, closeMenu } = useMenuContext();
  const lastActionTimeRef = useRef(0);
  const isDrawerRouteRef = useRef(false);

  // Use refs to avoid stale closures in the interceptor
  const isMenuOpenRef = useRef(isMenuOpen);
  const openMenuRef = useRef(openMenu);
  const closeMenuRef = useRef(closeMenu);

  // Keep refs up to date
  isMenuOpenRef.current = isMenuOpen;
  openMenuRef.current = openMenu;
  closeMenuRef.current = closeMenu;

  // Track if we're on a drawer route
  isDrawerRouteRef.current = isDrawerRoute(pathname);

  // Prevent React Navigation from handling back on drawer routes (tvOS)
  // This must run BEFORE the back interceptor to ensure React Navigation doesn't interfere
  useEffect(() => {
    if (!Platform.isTV || !isDrawerRoute(pathname)) {
      return;
    }

    // Prevent React Navigation from handling back events on drawer routes
    const unsubscribe = navigation.addListener('beforeRemove', (e) => {
      e.preventDefault();
    });

    // NOTE: We don't register a BackHandler here because RemoteControlManager already
    // handles BackHandler events and routes them through the interceptor chain.
    // Registering another BackHandler here would interfere with modal back handling.

    return () => {
      unsubscribe();
    };
  }, [navigation, pathname]);

  // Use back interceptor for ALL back handling - both drawer and non-drawer routes
  // This ensures that modals and other components can properly intercept back events
  // by pushing their own interceptors (which run first due to LIFO order)
  useEffect(() => {
    // Create refs for router to avoid stale closures
    const routerRef = { current: router };

    // This interceptor handles all back events for this route
    // It runs AFTER any modal/overlay interceptors (since they're pushed later)
    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      const now = Date.now();
      const timeSinceLastAction = now - lastActionTimeRef.current;

      if (timeSinceLastAction < 200) {
        return true; // Consume duplicate events
      }
      lastActionTimeRef.current = now;

      const currentIsMenuOpen = isMenuOpenRef.current;
      const currentIsDrawerRoute = isDrawerRouteRef.current;

      // Handle drawer routes
      if (currentIsDrawerRoute) {
        if (currentIsMenuOpen) {
          // Drawer is open - let the back event propagate to minimize the app
          return false;
        }

        openMenuRef.current();
        return true; // Consume the event
      }

      // Handle non-drawer routes (details, player, etc) - explicitly navigate back
      if (routerRef.current.canGoBack()) {
        routerRef.current.back();
        return true; // Consumed - we handled it
      }

      return false; // Not handled - propagate
    });

    return () => {
      removeInterceptor();
    };
  }, [pathname, router]); // Only depend on pathname and router

  return null;
};
