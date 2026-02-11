import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { AppState, type AppStateStatus, Image, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { BlurView } from 'expo-blur';
import { Ionicons } from '@expo/vector-icons';
import { useTVDimensions } from '@/hooks/useTVDimensions';

import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import type { UserProfile } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';

const createStyles = (theme: NovaTheme, isLargeScreen: boolean) => {
  const useLargeSizing = Platform.isTV || isLargeScreen;
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
      borderRadius: 20,
      padding: useLargeSizing ? 40 : 28,
      minWidth: useLargeSizing ? 560 : 320,
      maxWidth: useLargeSizing ? 720 : 400,
      alignItems: 'center',
    },
    title: {
      fontSize: useLargeSizing ? 30 : 24,
      fontWeight: '700',
      color: theme.colors.text.primary,
      marginBottom: useLargeSizing ? 32 : 24,
    },
    grid: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      justifyContent: 'center',
      gap: useLargeSizing ? 20 : 16,
    },
    profileCard: {
      alignItems: 'center',
      width: useLargeSizing ? 120 : 90,
      paddingVertical: useLargeSizing ? 16 : 12,
      paddingHorizontal: 8,
      borderRadius: 12,
      backgroundColor: 'transparent',
    },
    profileCardFocused: {
      backgroundColor: theme.colors.accent.primary,
      transform: [{ scale: 1.08 }],
    },
    avatarWrapper: {
      position: 'relative',
      marginBottom: 8,
    },
    avatar: {
      width: useLargeSizing ? 72 : 56,
      height: useLargeSizing ? 72 : 56,
      borderRadius: useLargeSizing ? 36 : 28,
      backgroundColor: theme.colors.accent.primary,
      justifyContent: 'center',
      alignItems: 'center',
    },
    avatarImage: {
      width: useLargeSizing ? 72 : 56,
      height: useLargeSizing ? 72 : 56,
      borderRadius: useLargeSizing ? 36 : 28,
    },
    avatarText: {
      fontSize: useLargeSizing ? 28 : 22,
      fontWeight: '600',
      color: 'white',
    },
    pinBadge: {
      position: 'absolute',
      bottom: -2,
      right: -2,
      width: useLargeSizing ? 24 : 20,
      height: useLargeSizing ? 24 : 20,
      borderRadius: useLargeSizing ? 12 : 10,
      backgroundColor: theme.colors.background.surface,
      justifyContent: 'center',
      alignItems: 'center',
      borderWidth: 2,
      borderColor: theme.colors.text.muted,
    },
    profileName: {
      fontSize: useLargeSizing ? 16 : 13,
      fontWeight: '500',
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    profileNameFocused: {
      color: 'white',
    },
    activeIndicator: {
      width: 8,
      height: 8,
      borderRadius: 4,
      backgroundColor: theme.colors.accent.primary,
      marginTop: 4,
    },
  });
};

export const ProfileSelectorModal: React.FC = () => {
  const theme = useTheme();
  const { width: screenWidth } = useTVDimensions();
  const isLargeScreen = screenWidth >= 600;
  const styles = useMemo(() => createStyles(theme, isLargeScreen), [theme, isLargeScreen]);

  const { settings } = useBackendSettings();
  const { users, activeUserId, selectUser, loading, pendingPinUserId, setProfileSelectorActive, pinVerifiedGeneration } =
    useUserProfiles();

  // Start visible so the overlay covers the home screen immediately (no flash).
  // We dismiss once we know the selector isn't needed.
  const [visible, setVisible] = useState(true);
  const appStateRef = useRef<AppStateStatus>(AppState.currentState);
  const hasShownInitialRef = useRef(false);

  const isEnabled = settings?.display?.alwaysShowProfileSelector !== false;
  const hasMultipleUsers = users.length > 1;
  const isPinModalUp = !!pendingPinUserId;

  // Register with context immediately so refresh() knows to skip auto-PIN.
  useEffect(() => {
    if (isEnabled) {
      setProfileSelectorActive(true);
    }
    return () => setProfileSelectorActive(false);
  }, [isEnabled, setProfileSelectorActive]);

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

  // Show when app returns from background
  useEffect(() => {
    if (!isEnabled || !hasMultipleUsers) {
      return;
    }
    const subscription = AppState.addEventListener('change', (nextAppState) => {
      if (appStateRef.current.match(/inactive|background/) && nextAppState === 'active') {
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
    <View style={styles.fullscreen}>
      <BlurView intensity={40} tint="dark" style={styles.blurOverlay}>
        <View style={styles.darkOverlay} pointerEvents="none" />
        {/* Hide the profile grid while PIN modal is showing on top,
            but keep the blur overlay so there's no visual gap. */}
        {!isPinModalUp && !loading && hasMultipleUsers && (
          <View style={styles.container}>
            <Text style={styles.title}>Who's watching?</Text>
            <View style={styles.grid}>
              {users.map((user, index) => (
                <ProfileCard
                  key={user.id}
                  user={user}
                  isActive={user.id === activeUserId}
                  styles={styles}
                  onSelect={handleSelectProfile}
                  hasTVPreferredFocus={index === 0}
                />
              ))}
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
}

const ProfileCard: React.FC<ProfileCardProps> = ({ user, isActive, styles, onSelect, hasTVPreferredFocus }) => {
  const theme = useTheme();
  const { getIconUrl } = useUserProfiles();

  const handleSelect = useCallback(() => {
    onSelect(user.id);
  }, [onSelect, user.id]);

  const pinIconSize = Platform.isTV || styles.avatar.width > 56 ? 14 : 12;

  return (
    <Pressable
      onPress={handleSelect}
      hasTVPreferredFocus={hasTVPreferredFocus}
      tvParallaxProperties={{ enabled: false }}
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
};

export default ProfileSelectorModal;
