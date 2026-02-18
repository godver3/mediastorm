import React, { useCallback, useRef, useState } from 'react';
import { Image, Pressable, StyleSheet, Text, View } from 'react-native';
import Animated, { useAnimatedStyle, useSharedValue, withTiming } from 'react-native-reanimated';
import { LinearGradient } from 'expo-linear-gradient';
import { MaterialCommunityIcons } from '@expo/vector-icons';
import { usePathname, useRouter } from 'expo-router';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { useUserProfiles } from '@/components/UserProfilesContext';
import { useTVSidebar } from '@/components/TVSidebarContext';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { responsiveSize } from '@/theme/tokens/tvScale';

// Sidebar widths - collapsed shows only icons, expanded shows icons + labels
const SIDEBAR_WIDTH_EXPANDED = responsiveSize(400, 320);
const SIDEBAR_WIDTH_COLLAPSED = responsiveSize(100, 80);

interface SidebarItem {
  name: string;
  label: string;
  icon: React.ComponentProps<typeof MaterialCommunityIcons>['name'];
}

const SIDEBAR_ITEMS: SidebarItem[] = [
  { name: '/', label: 'Home', icon: 'home-variant' },
  { name: '/search', label: 'Search', icon: 'magnify' },
  { name: '/watchlist', label: 'Watchlist', icon: 'playlist-star' },
  { name: '/live', label: 'Live', icon: 'television-play' },
  { name: '/profiles', label: 'Profiles', icon: 'account-multiple' },
  { name: '/settings', label: 'Settings', icon: 'cog' },
];

export const TVSidebar = React.memo(function TVSidebar() {
  const router = useRouter();
  const pathname = usePathname();
  const theme = useTheme();
  const styles = useSidebarStyles(theme);
  const insets = useSafeAreaInsets();
  const { activeUser, getIconUrl } = useUserProfiles();
  const { setSidebarFirstItemRef, contentFirstItemTag } = useTVSidebar();

  // Track expanded state - expands when sidebar has focus
  const [isExpanded, setIsExpanded] = useState(false);

  // Use Reanimated for smoother animations
  const widthAnim = useSharedValue(SIDEBAR_WIDTH_COLLAPSED);
  const textOpacity = useSharedValue(0);

  // Track focused count to handle focus moving between sidebar items
  const focusedCountRef = useRef(0);

  const handleItemSelect = useCallback(
    (routeName: string) => {
      if (pathname !== routeName) {
        router.replace(routeName as any);
      }
    },
    [pathname, router],
  );

  const handleSidebarFocus = useCallback(() => {
    focusedCountRef.current += 1;
    setIsExpanded(true);
    widthAnim.value = withTiming(SIDEBAR_WIDTH_EXPANDED, { duration: 200 });
    textOpacity.value = withTiming(1, { duration: 200 });
  }, [widthAnim, textOpacity]);

  const handleSidebarBlur = useCallback(() => {
    focusedCountRef.current -= 1;
    // Use setTimeout to check if focus moved to another sidebar item
    setTimeout(() => {
      if (focusedCountRef.current <= 0) {
        focusedCountRef.current = 0;
        setIsExpanded(false);
        widthAnim.value = withTiming(SIDEBAR_WIDTH_COLLAPSED, { duration: 200 });
        textOpacity.value = withTiming(0, { duration: 100 });
      }
    }, 50);
  }, [widthAnim, textOpacity]);

  const containerStyle = useAnimatedStyle(() => ({
    width: widthAnim.value,
  }));

  const textAnimStyle = useAnimatedStyle(() => ({
    opacity: textOpacity.value,
  }));

  const iconSize = responsiveSize(38, 24);

  // Gradient colors: surface color to transparent
  const gradientColors = [theme.colors.background.surface, 'transparent'] as const;

  return (
    <Animated.View
      style={[
        styles.container,
        containerStyle,
        { paddingTop: insets.top, paddingBottom: insets.bottom },
      ]}>
      <LinearGradient
        colors={gradientColors}
        start={{ x: 0, y: 0 }}
        end={{ x: 1, y: 0 }}
        style={StyleSheet.absoluteFill}
      />
      {/* User avatar and name header */}
      <View style={[styles.header, !isExpanded && styles.headerCollapsed]}>
        {activeUser && (
          <>
            {activeUser.hasIcon ? (
              <Image source={{ uri: getIconUrl(activeUser.id) }} style={styles.headerAvatarImage} />
            ) : (
              <View style={[styles.headerAvatar, activeUser.color && { backgroundColor: activeUser.color }]}>
                <Text style={styles.headerAvatarText}>{activeUser.name.charAt(0).toUpperCase()}</Text>
              </View>
            )}
            {isExpanded && (
              <Animated.Text style={[styles.userName, textAnimStyle]} numberOfLines={1}>
                {activeUser.name}
              </Animated.Text>
            )}
          </>
        )}
        {!activeUser && (
          <View style={[styles.headerAvatar, { backgroundColor: theme.colors.background.elevated }]}>
            <Text style={styles.headerAvatarText}>?</Text>
          </View>
        )}
      </View>

      {/* Navigation items */}
      <View style={styles.navItems}>
        {SIDEBAR_ITEMS.map((item, index) => {
          const isActive = pathname === item.name || (item.name === '/' && pathname === '/index');
          const isFirst = index === 0;

          return (
            <Pressable
              key={item.name}
              ref={isFirst ? setSidebarFirstItemRef : undefined}
              onPress={() => handleItemSelect(item.name)}
              onFocus={handleSidebarFocus}
              onBlur={handleSidebarBlur}
              android_disableSound
              hasTVPreferredFocus={isFirst && isActive}
              tvParallaxProperties={{ enabled: false }}
              nextFocusRight={contentFirstItemTag ?? undefined}
              style={({ focused }) => [
                styles.navItem,
                !isExpanded && styles.navItemCollapsed,
                isActive && styles.navItemActive,
                focused && styles.navItemFocused,
              ]}>
              {({ focused }) => (
                <>
                  <MaterialCommunityIcons
                    name={item.icon}
                    size={iconSize}
                    color={focused ? theme.colors.background.base : theme.colors.text.primary}
                    style={[styles.icon, isExpanded && styles.iconExpanded]}
                  />
                  {isExpanded && (
                    <Animated.Text
                      style={[
                        styles.navItemText,
                        focused && styles.navItemTextFocused,
                        textAnimStyle,
                      ]}
                      numberOfLines={1}>
                      {item.label}
                    </Animated.Text>
                  )}
                </>
              )}
            </Pressable>
          );
        })}
      </View>
    </Animated.View>
  );
});

const useSidebarStyles = function (theme: NovaTheme) {
  // Match the original CustomMenu sizes
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
    container: {
      // Background is provided by LinearGradient
      overflow: 'hidden', // Hide text as it animates out
    },
    header: {
      flexDirection: 'row',
      alignItems: 'center',
      // Use same horizontal padding/margin as nav items so avatar aligns with icons
      paddingStart: menuItemPaddingStart,
      paddingEnd: menuItemPaddingEnd,
      paddingVertical: headerPadding,
      marginHorizontal: theme.spacing.md,
      gap: responsiveSize(16, 8),
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      marginBottom: theme.spacing.md,
    },
    headerCollapsed: {
      justifyContent: 'center',
      paddingStart: 0,
      paddingEnd: 0,
      marginHorizontal: 0,
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
      textAlign: 'center',
      // Adjust for better visual centering
      includeFontPadding: false,
      textAlignVertical: 'center',
    },
    userName: {
      fontSize: menuFontSize,
      lineHeight: menuLineHeight,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    navItems: {
      flex: 1,
      paddingVertical: theme.spacing.md,
    },
    navItem: {
      flexDirection: 'row',
      alignItems: 'center',
      paddingVertical: menuItemPaddingVertical,
      paddingStart: menuItemPaddingStart,
      paddingEnd: menuItemPaddingEnd,
      marginHorizontal: theme.spacing.md,
      borderRadius: theme.radius.md,
    },
    navItemCollapsed: {
      justifyContent: 'center',
      paddingStart: 0,
      paddingEnd: 0,
      marginHorizontal: 0,
    },
    navItemActive: {
      backgroundColor: theme.colors.background.elevated,
    },
    navItemFocused: {
      backgroundColor: theme.colors.accent.primary,
    },
    icon: {
      width: iconSize,
      height: iconSize,
    },
    iconExpanded: {
      marginRight: theme.spacing.md,
    },
    navItemText: {
      fontSize: menuFontSize,
      lineHeight: menuLineHeight,
      fontWeight: '500',
      color: theme.colors.text.primary,
    },
    navItemTextFocused: {
      color: theme.colors.background.base,
    },
  });
};

export { SIDEBAR_WIDTH_EXPANDED, SIDEBAR_WIDTH_COLLAPSED };
