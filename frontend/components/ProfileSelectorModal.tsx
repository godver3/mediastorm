import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { AppState, type AppStateStatus, Image, Platform, StyleSheet, Text, useWindowDimensions, View } from 'react-native';
import { BlurView } from 'expo-blur';
import { Ionicons } from '@expo/vector-icons';

import { usePathname } from 'expo-router';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  SpatialNavigationFocusableView,
  DefaultFocus,
} from '@/services/tv-navigation';
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
      paddingHorizontal: responsiveSize(0, 24),
    },
    darkOverlay: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: 'rgba(0, 0, 0, 0.6)',
    },
    container: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: responsiveSize(28, 20),
      padding: responsiveSize(64, 28),
      minWidth: responsiveSize(700, 0),
      maxWidth: responsiveSize(960, 400),
      width: Platform.isTV ? undefined : '100%',
      alignItems: 'center',
    },
    title: {
      fontSize: responsiveSize(44, 24),
      fontWeight: '700',
      color: theme.colors.text.primary,
      marginBottom: responsiveSize(40, 24),
    },
    grid: {
      alignItems: 'center',
      gap: responsiveSize(36, 16),
    },
    gridRow: {
      flexDirection: 'row',
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
  const [visible, _setVisible] = useState(true);
  const setVisible = useCallback((v: boolean) => {
    console.log(`[ProfileSelector] setVisible(${v})`, new Error().stack?.split('\n').slice(1, 4).join(' | '));
    _setVisible(v);
  }, []);
  const appStateRef = useRef<AppStateStatus>(AppState.currentState);
  const hasShownInitialRef = useRef(false);

  useEffect(() => {
    console.log(`[ProfileSelector] MOUNT — initial visible=true`);
    return () => console.log(`[ProfileSelector] UNMOUNT`);
  }, []);

  const isEnabled = settings?.display?.alwaysShowProfileSelector !== false;
  const hasMultipleUsers = users.length > 1;
  const isPinModalUp = !!pendingPinUserId;

  const showGrid = visible && !isPinModalUp && !loading && hasMultipleUsers;

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

  // Block back/menu from dismissing the modal or reaching GoBackConfiguration.
  useEffect(() => {
    if (!visible) return;
    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      return true; // Consumed — profile must be selected, can't dismiss
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

  // Show when app returns from background (user actually left the app).
  // We track whether the app hit 'background' — mere 'inactive' blips (Control
  // Center, Notification Center, incoming calls, player dismiss) are ignored.
  const leftActiveAtRef = useRef<number>(0);
  const wentToBackgroundRef = useRef(false);
  useEffect(() => {
    if (!isEnabled || !hasMultipleUsers) {
      return;
    }
    const subscription = AppState.addEventListener('change', (nextAppState) => {
      console.log(`[ProfileSelector] AppState: ${appStateRef.current} → ${nextAppState}, pathname=${pathnameRef.current}`);
      if (appStateRef.current === 'active' && nextAppState !== 'active') {
        leftActiveAtRef.current = Date.now();
        wentToBackgroundRef.current = false;
      }
      if (nextAppState === 'background') {
        wentToBackgroundRef.current = true;
      }
      if (appStateRef.current.match(/inactive|background/) && nextAppState === 'active') {
        const awayMs = Date.now() - leftActiveAtRef.current;
        const onRNPlayer = pathnameRef.current === '/player';
        const didBackground = wentToBackgroundRef.current;
        console.log(`[ProfileSelector] Resume check: awayMs=${awayMs}, onRNPlayer=${onRNPlayer}, didBackground=${didBackground}`);
        // Only show if the app truly went to background (not just inactive from
        // Control Center / Notification Center / player dismiss), was away for
        // >2s, and isn't on the player screen.
        if (!didBackground || awayMs <= 2000 || onRNPlayer) {
          console.log(`[ProfileSelector] Skipping modal (didBackground=${didBackground}, awayMs<=2000=${awayMs <= 2000}, onPlayer=${onRNPlayer})`);
          appStateRef.current = nextAppState;
          return;
        }
        setVisible(true);
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
      // If same profile is selected, just dismiss without reloading
      if (id === activeUserId) {
        setVisible(false);
        return;
      }
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
    [selectUser, users, activeUserId],
  );

  if (!visible) {
    return null;
  }

  return (
    <View style={styles.fullscreen} focusable={false}>
      <BlurView intensity={40} tint="dark" style={styles.blurOverlay}>
        <View style={styles.darkOverlay} pointerEvents="none" />
        {/* Hide the profile grid while PIN modal is showing on top,
            but keep the blur overlay so there's no visual gap. */}
        {showGrid && (
          <SpatialNavigationRoot isActive={showGrid}>
            <ProfileGrid
              users={users}
              activeUserId={activeUserId}
              onSelect={handleSelectProfile}
              styles={styles}
            />
          </SpatialNavigationRoot>
        )}
      </BlurView>
    </View>
  );
};

// TV grid card width (180) + gap (36) = 216 per slot.
// Container: maxWidth 960, padding 64*2 = usable ~832. → 4 per row on TV.
// Mobile: single row wraps natively; spatial nav is inactive.
const TV_CARD_SLOT = responsiveSize(180 + 36, 90 + 16);
const TV_CONTAINER_PADDING = responsiveSize(64 * 2, 28 * 2);
const TV_CONTAINER_MAX = responsiveSize(960, 400);

interface ProfileGridProps {
  users: UserProfile[];
  activeUserId: string | null;
  onSelect: (id: string) => void;
  styles: ReturnType<typeof createStyles>;
}

const ProfileGrid: React.FC<ProfileGridProps> = ({ users, activeUserId, onSelect, styles }) => {
  const { width: screenWidth } = useWindowDimensions();
  const containerWidth = Math.min(TV_CONTAINER_MAX, screenWidth) - TV_CONTAINER_PADDING;
  const itemsPerRow = Math.max(1, Math.floor((containerWidth + responsiveSize(36, 16)) / TV_CARD_SLOT));

  const rows = useMemo(() => {
    const result: UserProfile[][] = [];
    for (let i = 0; i < users.length; i += itemsPerRow) {
      result.push(users.slice(i, i + itemsPerRow));
    }
    return result;
  }, [users, itemsPerRow]);

  let globalIndex = 0;

  return (
    <View style={styles.container} focusable={false}>
      <Text style={styles.title}>Who's watching?</Text>
      <SpatialNavigationNode orientation="vertical">
        <View style={styles.grid} focusable={false}>
          {rows.map((row, rowIndex) => (
            <SpatialNavigationNode key={rowIndex} orientation="horizontal">
              <View style={styles.gridRow} focusable={false}>
                {row.map((user) => {
                  const idx = globalIndex++;
                  const card = (
                    <SpatialNavigationFocusableView
                      key={user.id}
                      onSelect={() => onSelect(user.id)}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <ProfileCard
                          user={user}
                          isActive={user.id === activeUserId}
                          isFocused={isFocused}
                          styles={styles}
                        />
                      )}
                    </SpatialNavigationFocusableView>
                  );
                  return idx === 0 ? <DefaultFocus key={user.id}>{card}</DefaultFocus> : card;
                })}
              </View>
            </SpatialNavigationNode>
          ))}
        </View>
      </SpatialNavigationNode>
    </View>
  );
};

interface ProfileCardProps {
  user: UserProfile;
  isActive: boolean;
  isFocused: boolean;
  styles: ReturnType<typeof createStyles>;
}

const ProfileCard: React.FC<ProfileCardProps> = ({ user, isActive, isFocused, styles }) => {
  const theme = useTheme();
  const { getIconUrl } = useUserProfiles();

  const pinIconSize = responsiveSize(18, 12);

  return (
    <View style={[styles.profileCard, isFocused && styles.profileCardFocused]}>
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
      <Text style={[styles.profileName, isFocused && styles.profileNameFocused]}>{user.name}</Text>
      {isActive && <View style={styles.activeIndicator} />}
    </View>
  );
};

export default ProfileSelectorModal;
