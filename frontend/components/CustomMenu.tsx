import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV, responsiveSize } from '@/theme/tokens/tvScale';
import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  SpatialNavigationFocusableView,
  DefaultFocus,
} from '@/services/tv-navigation';
import { MaterialCommunityIcons } from '@expo/vector-icons';
import { usePathname, useRouter } from 'expo-router';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Platform, StyleSheet, Text, View, Animated, Pressable, Image } from 'react-native';
import { LinearGradient } from 'expo-linear-gradient';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

// Routes that should remain accessible when backend is unreachable
const ALWAYS_ACCESSIBLE_ROUTES = ['/', '/settings'];

interface CustomMenuProps {
  isVisible: boolean;
  onClose: () => void;
}

// Unified responsive menu width - design for 1920px, scales to screen
const MENU_WIDTH = responsiveSize(520, 416);

export const CustomMenu = React.memo(function CustomMenu({ isVisible, onClose }: CustomMenuProps) {
  const router = useRouter();
  const pathname = usePathname();
  const theme = useTheme();
  const styles = useMenuStyles(theme);
  const insets = useSafeAreaInsets();
  const { activeUser, getIconUrl } = useUserProfiles();
  const { isBackendReachable, loading: settingsLoading, isReady: settingsReady } = useBackendSettings();
  const slideAnim = useRef(new Animated.Value(isVisible ? 0 : -MENU_WIDTH)).current;
  const [isAnimatedHidden, setIsAnimatedHidden] = useState(!isVisible);

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
    { name: '/lists', label: 'Lists' },
    { name: '/live', label: 'Live' },
    { name: '/profiles', label: 'Profiles' },
    { name: '/settings', label: 'Settings' },
  ];

  const menuItems = Platform.isTV ? baseMenuItems : [...baseMenuItems, { name: '/modal-test', label: 'Modal Tests' }];

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

  // TV: close menu when user presses right
  useEffect(() => {
    if (!Platform.isTV || !isVisible) return;
    const unsubscribe = RemoteControlManager.addKeydownListener((key) => {
      if (key === SupportedKeys.Right) onClose();
    });
    return unsubscribe;
  }, [isVisible, onClose]);

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

  // Find first enabled item index for default focus
  const firstEnabledIndex = menuItems.findIndex((i) => !isRouteDisabled(i.name));

  // Render menu item content - shared between TV and non-TV Pressable
  const renderMenuItemContent = (item: { name: string; label: string }, isFocused: boolean, disabled: boolean) => (
    <>
      <MaterialCommunityIcons
        name={getMenuIconName(item.name)}
        size={iconSize}
        color={
          disabled ? theme.colors.text.disabled : theme.colors.text.primary
        }
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
    </>
  );

  // TV platform: use SpatialNavigationFocusableView for D-pad navigation
  const renderTVMenuItems = () => (
    <SpatialNavigationNode orientation="vertical">
      {menuItems.map((item, index) => {
        const disabled = isRouteDisabled(item.name);
        const isFirstEnabled = index === firstEnabledIndex;

        if (disabled) {
          return (
            <View key={item.name} style={[styles.menuItem, styles.menuItemDisabled]}>
              {renderMenuItemContent(item, false, true)}
            </View>
          );
        }

        const focusableItem = (
          <SpatialNavigationFocusableView
            key={item.name}
            onSelect={() => handleItemSelect(item.name)}>
            {({ isFocused }: { isFocused: boolean }) => (
              <View style={[styles.menuItem]}>
                {isFocused && (
                  <LinearGradient
                    colors={['rgba(63, 102, 255, 1)', 'rgba(63, 102, 255, 0)']}
                    locations={[0, 1]}
                    start={{ x: 0, y: 0 }}
                    end={{ x: 1, y: 0 }}
                    style={StyleSheet.absoluteFill}
                  />
                )}
                {renderMenuItemContent(item, isFocused, false)}
              </View>
            )}
          </SpatialNavigationFocusableView>
        );

        if (isFirstEnabled) {
          return <DefaultFocus key={item.name}>{focusableItem}</DefaultFocus>;
        }
        return focusableItem;
      })}
    </SpatialNavigationNode>
  );

  // Non-TV platform: use regular Pressable
  const renderNonTVMenuItems = () => (
    <>
      {menuItems.map((item) => {
        const disabled = isRouteDisabled(item.name);
        return (
          <Pressable
            key={item.name}
            onPress={() => handleItemSelect(item.name)}
            disabled={disabled}
            style={({ pressed }) => [
              styles.menuItem,
              disabled && styles.menuItemDisabled,
            ]}>
            {({ pressed }) => (
              <>
                {pressed && !disabled && (
                  <LinearGradient
                    colors={['rgba(63, 102, 255, 1)', 'rgba(63, 102, 255, 0)']}
                    locations={[0, 1]}
                    start={{ x: 0, y: 0 }}
                    end={{ x: 1, y: 0 }}
                    style={StyleSheet.absoluteFill}
                  />
                )}
                {renderMenuItemContent(item, pressed, disabled)}
              </>
            )}
          </Pressable>
        );
      })}
    </>
  );

  const menuContent = (
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
      <LinearGradient
        colors={[
          'rgba(22, 22, 31, 0.97)',
          'rgba(22, 22, 31, 0.9)',
          'rgba(22, 22, 31, 0.7)',
          'rgba(22, 22, 31, 0.35)',
          'rgba(22, 22, 31, 0.1)',
          'rgba(22, 22, 31, 0)',
        ]}
        locations={[0, 0.35, 0.5, 0.68, 0.85, 1]}
        start={{ x: 0, y: 0 }}
        end={{ x: 1, y: 0 }}
        style={StyleSheet.absoluteFill}
      />
      <View style={[styles.scrollView, styles.scrollContent]}>
        <View style={styles.header}>
          {isTV &&
            activeUser &&
            (activeUser.hasIcon ? (
              <Image source={{ uri: getIconUrl(activeUser.id) }} style={styles.headerAvatarImage} />
            ) : (
              <View style={[styles.headerAvatar, activeUser.color && { backgroundColor: activeUser.color }]}>
                <Text style={styles.headerAvatarText}>{activeUser.name.charAt(0).toUpperCase()}</Text>
              </View>
            ))}
          <Text style={styles.userName}>{activeUser?.name ?? 'Loading profile…'}</Text>
        </View>
        {Platform.isTV ? renderTVMenuItems() : renderNonTVMenuItems()}
      </View>
    </Animated.View>
  );

  return (
    <>
      {Platform.isTV ? (
        <SpatialNavigationRoot isActive={isVisible} onDirectionHandledWithoutMovement={(direction: string) => {
          if (direction === 'right') onClose();
        }}>
          {menuContent}
        </SpatialNavigationRoot>
      ) : (
        menuContent
      )}
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
    menuContainer: {
      position: 'absolute',
      left: 0,
      top: 0,
      bottom: 0,
      width: MENU_WIDTH,
      zIndex: 1000,
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
      borderBottomColor: 'rgba(255, 255, 255, 0.1)',
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
      overflow: 'hidden',
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
      color: theme.colors.text.primary,
    },
  });
};

function getMenuIconName(routeName: string): React.ComponentProps<typeof MaterialCommunityIcons>['name'] {
  switch (routeName) {
    case '/':
      return 'home-variant';
    case '/search':
      return 'magnify';
    case '/lists':
      return 'format-list-bulleted-square';
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
