import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { AppState, type AppStateStatus, findNodeHandle, Image, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { BlurView } from 'expo-blur';
import { Ionicons } from '@expo/vector-icons';

import { usePathname } from 'expo-router';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import type { UserProfile } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { responsiveSize } from '@/theme/tokens/tvScale';

const createStyles = (theme: NovaTheme) => {
  // responsiveSize(tvDesign, mobile) — designs for 1920px TV, scales to actual screen.
  const avatarSize = responsiveSize(120, 56);
  const pinBadgeSize = responsiveSize(32, 20);

  return StyleSheet.create({
    fullscreen: {
      ...StyleSheet.absoluteFillObject,
      zIndex: 1000,
    },
    blurOverlay: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
    },
    darkOverlay: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: 'rgba(0, 0, 0, 0.6)',
    },
    container: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: responsiveSize(28, 20),
      padding: responsiveSize(64, 28),
      minWidth: responsiveSize(700, 320),
      maxWidth: responsiveSize(960, 400),
      alignItems: 'center',
    },
    title: {
      fontSize: responsiveSize(44, 24),
      fontWeight: '700',
      color: theme.colors.text.primary,
      marginBottom: responsiveSize(40, 24),
    },
    grid: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      justifyContent: 'center',
      gap: responsiveSize(36, 16),
    },
    profileCard: {
      alignItems: 'center',
      width: responsiveSize(180, 90),
      paddingVertical: responsiveSize(24, 12),
      paddingHorizontal: responsiveSize(12, 8),
      borderRadius: responsiveSize(16, 12),
      backgroundColor: 'transparent',
    },
    profileCardFocused: {
      backgroundColor: theme.colors.accent.primary,
      transform: [{ scale: 1.08 }],
    },
    avatarWrapper: {
      position: 'relative',
      marginBottom: responsiveSize(12, 8),
    },
    avatar: {
      width: avatarSize,
      height: avatarSize,
      borderRadius: avatarSize / 2,
      backgroundColor: theme.colors.accent.primary,
      justifyContent: 'center',
      alignItems: 'center',
    },
    avatarImage: {
      width: avatarSize,
      height: avatarSize,
      borderRadius: avatarSize / 2,
    },
    avatarText: {
      fontSize: responsiveSize(48, 22),
      fontWeight: '600',
      color: 'white',
    },
    pinBadge: {
      position: 'absolute',
      bottom: -2,
      right: -2,
      width: pinBadgeSize,
      height: pinBadgeSize,
      borderRadius: pinBadgeSize / 2,
      backgroundColor: theme.colors.background.surface,
      justifyContent: 'center',
      alignItems: 'center',
      borderWidth: 2,
      borderColor: theme.colors.text.muted,
    },
    profileName: {
      fontSize: responsiveSize(24, 13),
      fontWeight: '500',
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    profileNameFocused: {
      color: 'white',
    },
    activeIndicator: {
      width: responsiveSize(12, 8),
      height: responsiveSize(12, 8),
      borderRadius: responsiveSize(6, 4),
      backgroundColor: theme.colors.accent.primary,
      marginTop: responsiveSize(6, 4),
    },
  });
};

export const ProfileSelectorModal: React.FC = () => {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);

  const pathname = usePathname();
  const pathnameRef = useRef(pathname);
  pathnameRef.current = pathname;

  const { settings } = useBackendSettings();
  const {
    users,
    activeUserId,
    selectUser,
    loading,
    pendingPinUserId,
    setProfileSelectorActive,
    setProfileSelectorVisible,
    pinVerifiedGeneration,
  } = useUserProfiles();

  // Start visible so the overlay covers the home screen immediately (no flash).
  // We dismiss once we know the selector isn't needed.
  const [visible, setVisible] = useState(true);
  const appStateRef = useRef<AppStateStatus>(AppState.currentState);
  const hasShownInitialRef = useRef(false);

  const isEnabled = settings?.display?.alwaysShowProfileSelector !== false;
  const hasMultipleUsers = users.length > 1;
  const isPinModalUp = !!pendingPinUserId;

  // Focus trapping: track native handles for each profile card so directional
  // navigation can't escape the grid (same pattern as remove-from-CW modal).
  const cardRefs = useRef<(View | null)[]>([]);
  const [cardHandles, setCardHandles] = useState<(number | null)[]>([]);

  // Resolve native handles when the grid becomes visible
  const showGrid = visible && !isPinModalUp && !loading && hasMultipleUsers;
  useEffect(() => {
    if (!showGrid || !Platform.isTV) return;
    const timer = setTimeout(() => {
      const handles = cardRefs.current.map((ref) => (ref ? findNodeHandle(ref) : null));
      setCardHandles(handles);
    }, 50);
    return () => clearTimeout(timer);
  }, [showGrid, users]);

  // Register with context immediately so refresh() knows to skip auto-PIN.
  useEffect(() => {
    if (isEnabled) {
      setProfileSelectorActive(true);
    }
    return () => setProfileSelectorActive(false);
  }, [isEnabled, setProfileSelectorActive]);

  // Sync overlay visibility to context so pages can deactivate spatial navigation.
  useEffect(() => {
    setProfileSelectorVisible(visible);
  }, [visible, setProfileSelectorVisible]);

  // On tvOS, disable menu key when profile selector is visible so the system
  // handles Menu (backgrounds the app) instead of our event handler capturing it.
  useEffect(() => {
    if (Platform.OS !== 'ios' || !Platform.isTV || !visible) return;
    RemoteControlManager.setTvMenuKeyEnabled(false);
    return () => RemoteControlManager.setTvMenuKeyEnabled(true);
  }, [visible]);

  // Block back from reaching GoBackConfiguration (which would open the drawer).
  useEffect(() => {
    if (!visible) return;
    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      return true; // Consumed — prevent GoBackConfiguration from opening drawer
    });
    return () => removeInterceptor();
  }, [visible]);

  // Dismiss if we determine selector isn't needed (setting off, single user)
  useEffect(() => {
    if (!loading && (!hasMultipleUsers || !isEnabled)) {
      hasShownInitialRef.current = true;
      setVisible(false);
    } else if (!loading && hasMultipleUsers && isEnabled && !hasShownInitialRef.current) {
      hasShownInitialRef.current = true;
      // Already visible from initial state — just mark as shown
    }
  }, [loading, hasMultipleUsers, isEnabled]);

  // Show when app returns from background.
  // Track when the app left 'active' so we can ignore brief inactive blips
  // (e.g. iOS fullScreenModal dismiss triggers active→inactive→active).
  const leftActiveAtRef = useRef<number>(0);
  useEffect(() => {
    if (!isEnabled || !hasMultipleUsers) {
      return;
    }
    const subscription = AppState.addEventListener('change', (nextAppState) => {
      if (appStateRef.current === 'active' && nextAppState !== 'active') {
        leftActiveAtRef.current = Date.now();
      }
      if (appStateRef.current.match(/inactive|background/) && nextAppState === 'active') {
        // Only show if the app was away for >2s — brief inactive blips from
        // screen transitions (e.g. player dismiss) are ignored.
        // Also skip if the player is active (PiP return to foreground).
        const awayMs = Date.now() - leftActiveAtRef.current;
        const onPlayer = pathnameRef.current === '/player';
        if (awayMs > 2000 && !onPlayer) {
          setVisible(true);
        }
      }
      appStateRef.current = nextAppState;
    });
    return () => subscription.remove();
  }, [isEnabled, hasMultipleUsers]);

  // Dismiss after successful PIN verification.
  // pinVerifiedGeneration increments on every successful selectUserWithPin().
  const awaitingPinRef = useRef(false);
  const initialGenRef = useRef(pinVerifiedGeneration);
  useEffect(() => {
    if (awaitingPinRef.current && pinVerifiedGeneration !== initialGenRef.current) {
      awaitingPinRef.current = false;
      initialGenRef.current = pinVerifiedGeneration;
      setVisible(false);
    }
  }, [pinVerifiedGeneration]);

  const handleSelectProfile = useCallback(
    async (id: string) => {
      const user = users.find((u) => u.id === id);
      if (user?.hasPin) {
        // PIN flow: selectUser sets pendingPinUserId → PinEntryModal opens.
        // Keep our overlay visible (dark bg behind the PIN modal).
        // When PIN succeeds pinVerifiedGeneration increments and the effect dismisses us.
        awaitingPinRef.current = true;
        await selectUser(id);
      } else {
        // Non-PIN: selectUser sets activeUserId immediately. Dismiss.
        await selectUser(id);
        setVisible(false);
      }
    },
    [selectUser, users],
  );

  if (!visible) {
    return null;
  }

  // Use an absolutely-positioned View (not Modal) so it doesn't block
  // the PinEntryModal's Modal from rendering on top.
  return (
    <View style={styles.fullscreen} focusable={false}>
      <BlurView intensity={40} tint="dark" style={styles.blurOverlay}>
        <View style={styles.darkOverlay} pointerEvents="none" />
        {/* Hide the profile grid while PIN modal is showing on top,
            but keep the blur overlay so there's no visual gap. */}
        {showGrid && (
          <View style={styles.container} focusable={false}>
            <Text style={styles.title}>Who's watching?</Text>
            <View style={styles.grid} focusable={false}>
              {users.map((user, index) => {
                const selfHandle = cardHandles[index] ?? undefined;
                const leftHandle = index > 0 ? (cardHandles[index - 1] ?? undefined) : selfHandle;
                const rightHandle = index < users.length - 1 ? (cardHandles[index + 1] ?? undefined) : selfHandle;
                return (
                  <ProfileCard
                    key={user.id}
                    ref={(ref) => { cardRefs.current[index] = ref; }}
                    user={user}
                    isActive={user.id === activeUserId}
                    styles={styles}
                    onSelect={handleSelectProfile}
                    hasTVPreferredFocus={index === 0}
                    nextFocusUp={selfHandle}
                    nextFocusDown={selfHandle}
                    nextFocusLeft={leftHandle}
                    nextFocusRight={rightHandle}
                  />
                );
              })}
            </View>
          </View>
        )}
      </BlurView>
    </View>
  );
};

interface ProfileCardProps {
  user: UserProfile;
  isActive: boolean;
  styles: ReturnType<typeof createStyles>;
  onSelect: (id: string) => void;
  hasTVPreferredFocus?: boolean;
  nextFocusUp?: number;
  nextFocusDown?: number;
  nextFocusLeft?: number;
  nextFocusRight?: number;
}

const ProfileCard = React.forwardRef<View, ProfileCardProps>(
  ({ user, isActive, styles, onSelect, hasTVPreferredFocus, nextFocusUp, nextFocusDown, nextFocusLeft, nextFocusRight }, ref) => {
    const theme = useTheme();
    const { getIconUrl } = useUserProfiles();

    const handleSelect = useCallback(() => {
      onSelect(user.id);
    }, [onSelect, user.id]);

    const pinIconSize = responsiveSize(18, 12);

    return (
      <Pressable
        ref={ref}
        onPress={handleSelect}
        hasTVPreferredFocus={hasTVPreferredFocus}
        tvParallaxProperties={{ enabled: false }}
        nextFocusUp={nextFocusUp}
        nextFocusDown={nextFocusDown}
        nextFocusLeft={nextFocusLeft}
        nextFocusRight={nextFocusRight}
        style={({ focused, pressed }) => [
          styles.profileCard,
          focused && styles.profileCardFocused,
          pressed && !Platform.isTV && { opacity: 0.7 },
        ]}>
        {({ focused }) => (
          <>
            <View style={styles.avatarWrapper}>
              {user.hasIcon ? (
                <Image source={{ uri: getIconUrl(user.id) }} style={styles.avatarImage} resizeMode="cover" />
              ) : (
                <View style={[styles.avatar, user.color ? { backgroundColor: user.color } : undefined]}>
                  <Text style={styles.avatarText}>{user.name.charAt(0).toUpperCase()}</Text>
                </View>
              )}
              {user.hasPin && (
                <View style={styles.pinBadge}>
                  <Ionicons name="lock-closed" size={pinIconSize} color={theme.colors.text.muted} />
                </View>
              )}
            </View>
            <Text style={[styles.profileName, focused && styles.profileNameFocused]}>{user.name}</Text>
            {isActive && <View style={styles.activeIndicator} />}
          </>
        )}
      </Pressable>
    );
  },
);

export default ProfileSelectorModal;
