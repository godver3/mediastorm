import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV, responsiveSize } from '@/theme/tokens/tvScale';
import { MaterialCommunityIcons } from '@expo/vector-icons';
import { usePathname, useRouter } from 'expo-router';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  Platform,
  StyleSheet,
  Text,
  View,
  Animated,
  Pressable,
  findNodeHandle,
  Image,
} from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useMenuContext } from './MenuContext';

// Routes that should remain accessible when backend is unreachable
const ALWAYS_ACCESSIBLE_ROUTES = ['/', '/settings'];

interface CustomMenuProps {
  isVisible: boolean;
  onClose: () => void;
}

// Unified responsive menu width - design for 1920px, scales to screen
const MENU_WIDTH = responsiveSize(400, 320);

export const CustomMenu = React.memo(function CustomMenu({ isVisible, onClose }: CustomMenuProps) {
  const router = useRouter();
  const pathname = usePathname();
  const theme = useTheme();
  const styles = useMenuStyles(theme);
  const insets = useSafeAreaInsets();
  const { activeUser, getIconUrl } = useUserProfiles();
  const { isBackendReachable, loading: settingsLoading, isReady: settingsReady } = useBackendSettings();
  const { firstContentFocusableTag } = useMenuContext();
  const slideAnim = useRef(new Animated.Value(isVisible ? 0 : -MENU_WIDTH)).current;
  const [isAnimatedHidden, setIsAnimatedHidden] = useState(!isVisible);

  // Refs for native TV focus trapping - prevent Left from leaving the menu
  const menuItemRefs = useRef<(View | null)[]>([]);
  const [menuItemTags, setMenuItemTags] = useState<(number | null)[]>([]);

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

  const menuItems = Platform.isTV
    ? [...baseMenuItems, { name: '/tv-perf-debug', label: 'Nav Performance' }]
    : [...baseMenuItems, { name: '/modal-test', label: 'Modal Tests' }, { name: '/debug', label: 'Nav Performance' }];

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

  // TV: Compute native tags for focus trapping after refs are assigned
  useEffect(() => {
    if (!isTV || !isVisible) return;

    // Small delay to ensure refs are assigned
    const timer = setTimeout(() => {
      const tags = menuItemRefs.current.map((ref) => (ref ? findNodeHandle(ref) : null));
      setMenuItemTags(tags);
    }, 50);

    return () => clearTimeout(timer);
  }, [isVisible, menuItems.length]);

  // TV: Native focus handles Right navigation out of drawer
  // The drawer closes when shelf items detect they received focus while drawer is open
  // Left navigation is trapped via nextFocusLeft on menu items pointing to themselves
  // No keydown listener needed - we work with the native focus system instead of against it

  const handleItemSelect = useCallback(
    (routeName: string) => {
      if (isRouteDisabled(routeName)) {
        return;
      }
      // On TV platforms, if already on the target route, just close the drawer
      if (isTV && pathname === routeName) {
        onClose();
        return;
      }
      // On TV, immediately hide the menu before navigating to avoid
      // Fabric race condition where animation and navigation both modify view hierarchy
      if (isTV) {
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

  // Unified responsive icon size for TV
  const iconSize = responsiveSize(38, 24);

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
        {/* Unified native focus for all TV platforms */}
        <View style={[styles.scrollView, styles.scrollContent]}>
          <View style={styles.header}>
            {isTV && activeUser && (
              activeUser.hasIcon ? (
                <Image
                  source={{ uri: getIconUrl(activeUser.id) }}
                  style={styles.headerAvatarImage}
                />
              ) : (
                <View style={[styles.headerAvatar, activeUser.color && { backgroundColor: activeUser.color }]}>
                  <Text style={styles.headerAvatarText}>{activeUser.name.charAt(0).toUpperCase()}</Text>
                </View>
              )
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
              // Use native Pressable focus handling
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
                  nextFocusRight={firstContentFocusableTag ?? undefined}
                  nextFocusUp={isFirstEnabled ? (firstEnabledTag ?? undefined) : undefined}
                  nextFocusDown={isLastEnabled ? (lastEnabledTag ?? undefined) : undefined}
                  tvParallaxProperties={{ enabled: false }}
                  style={({ focused }) => [
                    styles.menuItem,
                    focused && !disabled && styles.menuItemFocused,
                    disabled && styles.menuItemDisabled,
                  ]}>
                  {({ focused }) => (
                    <>
                      <MaterialCommunityIcons
                        name={getMenuIconName(item.name)}
                        size={iconSize}
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
      </Animated.View>
    </>
  );
});

const useMenuStyles = function (theme: NovaTheme) {
  // Unified responsive sizing - design for 1920px width
  const iconSize = responsiveSize(38, 24);
  const headerPadding = responsiveSize(32, 16);
  const menuItemPaddingVertical = responsiveSize(24, 12);
  const menuItemPaddingStart = responsiveSize(40, 24);
  const menuItemPaddingEnd = responsiveSize(24, 16);
  const avatarSize = responsiveSize(56, 40);
  const avatarFontSize = responsiveSize(24, 16);
  const menuFontSize = responsiveSize(28, 16);
  const menuLineHeight = responsiveSize(36, 22);

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
      flexDirection: isTV ? 'row' : 'column',
      alignItems: isTV ? 'center' : 'flex-start',
      paddingHorizontal: headerPadding,
      paddingVertical: headerPadding,
      gap: responsiveSize(16, 8),
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      marginBottom: theme.spacing.md,
    },
    headerAvatar: {
      width: avatarSize,
      height: avatarSize,
      borderRadius: avatarSize / 2,
      backgroundColor: theme.colors.background.elevated,
      justifyContent: 'center',
      alignItems: 'center',
    },
    headerAvatarImage: {
      width: avatarSize,
      height: avatarSize,
      borderRadius: avatarSize / 2,
    },
    headerAvatarText: {
      fontSize: avatarFontSize,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    userName: {
      fontSize: menuFontSize,
      lineHeight: menuLineHeight,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    menuItem: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingVertical: menuItemPaddingVertical,
      paddingStart: menuItemPaddingStart,
      paddingEnd: menuItemPaddingEnd,
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
      fontSize: menuFontSize,
      lineHeight: menuLineHeight,
      fontWeight: '500',
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
    case '/tv-perf-debug':
      return 'bug-outline';
    default:
      return 'dots-horizontal';
  }
}
