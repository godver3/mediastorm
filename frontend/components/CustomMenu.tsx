import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import { DefaultFocus, SpatialNavigationNode } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { MaterialCommunityIcons } from '@expo/vector-icons';
import { usePathname, useRouter } from 'expo-router';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Platform,
  StyleSheet,
  Text,
  View,
  Animated,
  ScrollView,
  Pressable,
  findNodeHandle,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

const isAndroidTV = Platform.isTV && Platform.OS === 'android';
const isTVOS = Platform.isTV && Platform.OS === 'ios';

// Routes that should remain accessible when backend is unreachable
const ALWAYS_ACCESSIBLE_ROUTES = ['/', '/settings'];

interface CustomMenuProps {
  isVisible: boolean;
  onClose: () => void;
}

const MENU_WIDTH = isAndroidTV ? 288 : Platform.isTV ? 400 : 320;

export const CustomMenu = React.memo(function CustomMenu({ isVisible, onClose }: CustomMenuProps) {
  const router = useRouter();
  const pathname = usePathname();
  const theme = useTheme();
  const styles = useMenuStyles(theme);
  const insets = useSafeAreaInsets();
  const { activeUser } = useUserProfiles();
  const { isBackendReachable, loading: settingsLoading, isReady: settingsReady } = useBackendSettings();
  const slideAnim = useRef(new Animated.Value(isVisible ? 0 : -MENU_WIDTH)).current;
  const [isAnimatedHidden, setIsAnimatedHidden] = useState(!isVisible);

  // Refs for Android TV focus trapping - prevent Left from leaving the menu
  const menuItemRefs = useRef<(View | null)[]>([]);
  const [menuItemTags, setMenuItemTags] = useState<(number | null)[]>([]);

  // Track menu open count to force re-registration of spatial navigation nodes
  // This fixes an issue where spatial navigation skips menu items after navigation
  const openCountRef = useRef(0);
  const [menuKey, setMenuKey] = useState(0);

  // Increment key when menu becomes visible to force SpatialNavigationNode re-registration
  useEffect(() => {
    if (isVisible) {
      openCountRef.current += 1;
      setMenuKey(openCountRef.current);
    }
  }, [isVisible]);

  // Backend is considered available if reachable OR still loading initially
  const isBackendAvailable = isBackendReachable || (settingsLoading && !settingsReady);

  const isRouteDisabled = useCallback(
    (routeName: string) => {
      if (isBackendAvailable) {
        return false;
      }
      return !ALWAYS_ACCESSIBLE_ROUTES.includes(routeName);
    },
    [isBackendAvailable],
  );

  const baseMenuItems = [
    { name: '/', label: 'Home' },
    { name: '/search', label: 'Search' },
    { name: '/watchlist', label: 'Watchlist' },
    { name: '/live', label: 'Live' },
    { name: '/profiles', label: 'Profiles' },
    { name: '/settings', label: 'Settings' },
  ];

  const menuItems = Platform.isTV ? baseMenuItems : [...baseMenuItems, { name: '/modal-test', label: 'Modal Tests' }];

  // Calculate which menu item corresponds to the current route
  const currentRouteIndex = React.useMemo(() => {
    const index = menuItems.findIndex((item) => item.name === pathname);
    return index >= 0 ? index : 0;
  }, [pathname, menuItems]);

  React.useEffect(() => {
    if (isVisible) {
      setIsAnimatedHidden(false);
    }
    Animated.timing(slideAnim, {
      toValue: isVisible ? 0 : -MENU_WIDTH,
      duration: 250,
      useNativeDriver: true,
    }).start(() => {
      if (!isVisible) {
        setIsAnimatedHidden(true);
      }
    });
  }, [isVisible, slideAnim]);

  // Android TV: Compute native tags for focus trapping after refs are assigned
  useEffect(() => {
    if (!isAndroidTV || !isVisible) return;

    // Small delay to ensure refs are assigned
    const timer = setTimeout(() => {
      const tags = menuItemRefs.current.map((ref) => (ref ? findNodeHandle(ref) : null));
      setMenuItemTags(tags);
    }, 50);

    return () => clearTimeout(timer);
  }, [isVisible, menuItems.length]);

  // Android TV: Use native Pressable focus handling instead of manual D-pad navigation
  // Manual handling caused race conditions between our focusedIndex state and native focus
  useEffect(() => {
    if (!isAndroidTV || !isVisible) return;

    // Back button is NOT intercepted here - it propagates to minimize the app
    // (handled in GoBackConfiguration.tsx by returning false when drawer is open)

    // Handle left/right D-pad keys when drawer is open
    // Left: disabled (do nothing), Right: close drawer
    const handleKeyDown = (key: SupportedKeys) => {
      if (key === SupportedKeys.Right) {
        onClose();
      }
      // Left is intentionally ignored (disabled)
    };

    RemoteControlManager.addKeydownListener(handleKeyDown);

    return () => {
      RemoteControlManager.removeKeydownListener(handleKeyDown);
    };
  }, [isVisible, onClose]);

  const handleItemSelect = useCallback(
    (routeName: string) => {
      if (isRouteDisabled(routeName)) {
        return;
      }
      // On TV platforms, if already on the target route, just close the drawer
      if (Platform.isTV && pathname === routeName) {
        onClose();
        return;
      }
      // On Android TV, immediately hide the menu before navigating to avoid
      // Fabric race condition where animation and navigation both modify view hierarchy
      if (isAndroidTV) {
        setIsAnimatedHidden(true);
        slideAnim.setValue(-MENU_WIDTH);
        onClose();
        // Delay navigation slightly to let the view hierarchy settle
        setTimeout(() => {
          router.replace(routeName as any);
        }, 50);
      } else {
        onClose();
        router.replace(routeName as any);
      }
    },
    [onClose, router, isRouteDisabled, pathname, slideAnim],
  );

  if (!isVisible && isAnimatedHidden) {
    return null;
  }

  return (
    <>
      {isVisible && <View style={styles.overlay} pointerEvents="none" />}

      <Animated.View
        renderToHardwareTextureAndroid={true}
        style={[
          styles.menuContainer,
          {
            transform: [{ translateX: slideAnim }],
            paddingTop: insets.top,
            paddingBottom: insets.bottom,
          },
        ]}>
        {/* Android TV: Simple View with Pressable items */}
        {isAndroidTV ? (
          <View style={[styles.scrollView, styles.scrollContent]}>
            <View style={styles.header}>
              {activeUser && (
                <View style={[styles.headerAvatar, activeUser.color && { backgroundColor: activeUser.color }]}>
                  <Text style={styles.headerAvatarText}>{activeUser.name.charAt(0).toUpperCase()}</Text>
                </View>
              )}
              <Text style={styles.userName}>{activeUser?.name ?? 'Loading profile…'}</Text>
            </View>
            {(() => {
              // Find first and last enabled items for focus trapping
              const firstEnabledIndex = menuItems.findIndex((i) => !isRouteDisabled(i.name));
              const lastEnabledIndex = menuItems.findLastIndex((i) => !isRouteDisabled(i.name));
              const firstEnabledTag = menuItemTags[firstEnabledIndex];
              const lastEnabledTag = menuItemTags[lastEnabledIndex];

              return menuItems.map((item, index) => {
                const disabled = isRouteDisabled(item.name);
                const isFirstEnabled = index === firstEnabledIndex;
                const isLastEnabled = index === lastEnabledIndex;
                // Use native Pressable focus handling instead of manual RemoteControlManager tracking
                // nextFocusLeft points to self to trap focus within menu (prevent Left from leaving)
                // nextFocusUp on first enabled item and nextFocusDown on last enabled item trap vertical navigation
                const selfTag = menuItemTags[index];
                return (
                  <Pressable
                    key={item.name}
                    ref={(ref) => {
                      menuItemRefs.current[index] = ref;
                    }}
                    hasTVPreferredFocus={isFirstEnabled && isVisible}
                    onPress={() => handleItemSelect(item.name)}
                    disabled={disabled}
                    focusable={!disabled}
                    nextFocusLeft={selfTag ?? undefined}
                    nextFocusUp={isFirstEnabled ? (firstEnabledTag ?? undefined) : undefined}
                    nextFocusDown={isLastEnabled ? (lastEnabledTag ?? undefined) : undefined}
                    style={({ focused }) => [
                      styles.menuItem,
                      focused && !disabled && styles.menuItemFocused,
                      disabled && styles.menuItemDisabled,
                    ]}>
                  {({ focused }) => (
                    <>
                      <MaterialCommunityIcons
                        name={getMenuIconName(item.name)}
                        size={26}
                        color={
                          disabled
                            ? theme.colors.text.disabled
                            : focused
                              ? theme.colors.background.base
                              : theme.colors.text.primary
                        }
                        style={[styles.icon, disabled && styles.iconDisabled]}
                      />
                      <Text
                        style={[
                          styles.menuText,
                          focused && !disabled && styles.menuTextFocused,
                          disabled && styles.menuTextDisabled,
                        ]}>
                        {item.label}
                      </Text>
                    </>
                  )}
                </Pressable>
                );
              });
            })()}
          </View>
        ) : (
          <ScrollView
            style={styles.scrollView}
            contentContainerStyle={styles.scrollContent}
            scrollEnabled={!Platform.isTV}>
            <View style={styles.header}>
              {Platform.isTV && activeUser && (
                <View style={[styles.headerAvatar, activeUser.color && { backgroundColor: activeUser.color }]}>
                  <Text style={styles.headerAvatarText}>{activeUser.name.charAt(0).toUpperCase()}</Text>
                </View>
              )}
              <Text style={styles.userName}>{activeUser?.name ?? 'Loading profile…'}</Text>
            </View>
            {/* Key changes when menu opens, forcing re-registration of spatial navigation nodes */}
            <SpatialNavigationNode key={`menu-nav-${menuKey}`} orientation="vertical">
              {menuItems.map((item, index) => {
                const disabled = isRouteDisabled(item.name);
                const getIconColor = (isFocused: boolean) => {
                  if (disabled) return theme.colors.text.disabled;
                  if (isFocused) return theme.colors.background.base;
                  return theme.colors.text.primary;
                };

                // Find the first non-disabled item to use as default focus target
                const firstEnabledIndex = menuItems.findIndex((i) => !isRouteDisabled(i.name));
                // Use first enabled item as fallback if current route is disabled
                const defaultFocusIndex = isRouteDisabled(menuItems[currentRouteIndex]?.name)
                  ? firstEnabledIndex
                  : currentRouteIndex;
                const shouldHaveDefaultFocus = index === defaultFocusIndex;

                // Use SpatialNavigationNode with isFocusable={!disabled} to skip disabled items
                const focusableNode = (
                  <SpatialNavigationNode
                    key={item.name}
                    isFocusable={!disabled}
                    onSelect={disabled ? undefined : () => handleItemSelect(item.name)}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <Pressable
                        style={[
                          styles.menuItem,
                          isFocused && !disabled && styles.menuItemFocused,
                          disabled && styles.menuItemDisabled,
                        ]}
                        tvParallaxProperties={{ enabled: false }}>
                        <MaterialCommunityIcons
                          name={getMenuIconName(item.name)}
                          size={Platform.isTV ? 38 : 24}
                          color={getIconColor(isFocused)}
                          style={[styles.icon, disabled && styles.iconDisabled]}
                        />
                        <Text
                          style={[
                            styles.menuText,
                            isFocused && !disabled && styles.menuTextFocused,
                            disabled && styles.menuTextDisabled,
                          ]}>
                          {item.label}
                        </Text>
                      </Pressable>
                    )}
                  </SpatialNavigationNode>
                );

                return shouldHaveDefaultFocus ? (
                  <DefaultFocus key={item.name}>{focusableNode}</DefaultFocus>
                ) : (
                  focusableNode
                );
              })}
            </SpatialNavigationNode>
          </ScrollView>
        )}
      </Animated.View>
    </>
  );
});

const useMenuStyles = function (theme: NovaTheme) {
  const getIconSize = () => {
    if (isAndroidTV) return 26;
    if (isTVOS) return 38;
    return 24;
  };

  const getHeaderPadding = () => {
    if (isAndroidTV) return theme.spacing.md;
    if (isTVOS) return theme.spacing.xl;
    return theme.spacing.lg;
  };

  const getMenuItemPaddingVertical = () => {
    if (isAndroidTV) return theme.spacing.md;
    if (isTVOS) return theme.spacing.xl;
    return theme.spacing.md;
  };

  const getMenuItemPaddingStart = () => {
    if (isAndroidTV) return theme.spacing.xl;
    if (isTVOS) return theme.spacing['3xl'];
    return theme.spacing['2xl'];
  };

  const getMenuItemPaddingEnd = () => {
    if (isAndroidTV) return theme.spacing.md;
    if (isTVOS) return theme.spacing.xl;
    return theme.spacing.lg;
  };

  const iconSize = getIconSize();

  return StyleSheet.create({
    overlay: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: 'rgba(0, 0, 0, 0.5)',
      zIndex: 999,
    },
    menuContainer: {
      position: 'absolute',
      left: 0,
      top: 0,
      bottom: 0,
      width: MENU_WIDTH,
      backgroundColor: theme.colors.background.surface,
      zIndex: 1000,
      ...Platform.select({
        ios: {
          shadowColor: '#000',
          shadowOffset: { width: 2, height: 0 },
          shadowOpacity: 0.25,
          shadowRadius: 8,
        },
        android: {
          elevation: 8,
        },
      }),
    },
    scrollView: {
      flex: 1,
    },
    scrollContent: {
      flexGrow: 1,
    },
    header: {
      flexDirection: Platform.isTV ? 'row' : 'column',
      alignItems: Platform.isTV ? 'center' : 'flex-start',
      paddingHorizontal: getHeaderPadding(),
      paddingVertical: getHeaderPadding(),
      gap: Platform.isTV ? theme.spacing.md : theme.spacing.sm,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      marginBottom: theme.spacing.md,
    },
    headerAvatar: {
      width: isAndroidTV ? 40 : 56,
      height: isAndroidTV ? 40 : 56,
      borderRadius: isAndroidTV ? 20 : 28,
      backgroundColor: theme.colors.background.elevated,
      justifyContent: 'center',
      alignItems: 'center',
    },
    headerAvatarText: {
      fontSize: isAndroidTV ? 18 : 24,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    userName: {
      ...(isAndroidTV ? theme.typography.title.lg : isTVOS ? theme.typography.title.xl : theme.typography.title.md),
      color: theme.colors.text.primary,
    },
    menuItem: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingVertical: getMenuItemPaddingVertical(),
      paddingStart: getMenuItemPaddingStart(),
      paddingEnd: getMenuItemPaddingEnd(),
      marginHorizontal: theme.spacing.md,
      borderRadius: theme.radius.md,
    },
    menuItemFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    menuItemDisabled: {
      opacity: 0.5,
    },
    icon: {
      width: iconSize,
      height: iconSize,
      marginRight: theme.spacing.md,
    },
    iconDisabled: {
      opacity: 0.5,
    },
    menuText: {
      ...(isAndroidTV ? theme.typography.title.lg : isTVOS ? theme.typography.title.xl : theme.typography.title.md),
      color: theme.colors.text.primary,
    },
    menuTextDisabled: {
      color: theme.colors.text.disabled,
    },
    menuTextFocused: {
      color: theme.colors.background.base,
    },
  });
};

function getMenuIconName(routeName: string): React.ComponentProps<typeof MaterialCommunityIcons>['name'] {
  switch (routeName) {
    case '/':
      return 'home-variant';
    case '/search':
      return 'magnify';
    case '/watchlist':
      return 'playlist-star';
    case '/live':
      return 'television-play';
    case '/profiles':
      return 'account-multiple';
    case '/settings':
      return 'cog';
    case '/modal-test':
      return 'application-brackets-outline';
    case '/debug':
      return 'bug-outline';
    default:
      return 'dots-horizontal';
  }
}
