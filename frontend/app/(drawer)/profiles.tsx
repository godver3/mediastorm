import { Ionicons } from '@expo/vector-icons';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ActivityIndicator, Image, Modal, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';

import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { useMenuContext } from '@/components/MenuContext';
import { useToast } from '@/components/ToastContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import type { UserProfile } from '@/services/api';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV, responsiveSize } from '@/theme/tokens/tvScale';
import { useIsFocused } from '@react-navigation/native';
import { Stack } from 'expo-router';
import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  SpatialNavigationFocusableView,
  SpatialNavigationScrollView,
  DefaultFocus,
} from '@/services/tv-navigation';

type PendingAction = null | `activate:${string}` | `color:${string}`;

// Predefined profile colors for TV
const PROFILE_COLORS = [
  { name: 'Blue', value: '#3B82F6' },
  { name: 'Purple', value: '#8B5CF6' },
  { name: 'Pink', value: '#EC4899' },
  { name: 'Red', value: '#EF4444' },
  { name: 'Orange', value: '#F97316' },
  { name: 'Yellow', value: '#EAB308' },
  { name: 'Green', value: '#22C55E' },
  { name: 'Teal', value: '#14B8A6' },
];

// Grid item type for TV virtualized grid
type GridItem = { profile: UserProfile };

const formatErrorMessage = (err: unknown) => {
  if (err instanceof Error) {
    return err.message;
  }
  if (typeof err === 'string') {
    return err;
  }
  return 'Unexpected profile error';
};

export default function ProfilesScreen() {
  const theme = useTheme();
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth, screenHeight), [theme, screenWidth, screenHeight]);
  const _isFocused = useIsFocused();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const { users, loading, error, activeUserId, selectUser, updateColor, getIconUrl, pendingPinUserId: _pendingPinUserId } =
    useUserProfiles();
  const { showToast } = useToast();

  const [pending, setPending] = useState<PendingAction>(null);
  const [selectedProfile, setSelectedProfile] = useState<UserProfile | null>(null);
  const [openColorSelectorId, setOpenColorSelectorId] = useState<string | null>(null);

  const isProfileModalVisible = selectedProfile !== null;

  // Refs for native focus management
  const refreshButtonRef = useRef<View>(null);
  const profileCardRefs = useRef<(View | null)[]>([]);
  const colorSwatchRefs = useRef<(View | null)[]>([]);
  const modalButtonRefs = useRef<(View | null)[]>([]);

  useEffect(() => {
    if (error) {
      showToast(error, { tone: 'danger', duration: 7000 });
    }
  }, [error, showToast]);

  const handleActivateProfile = useCallback(
    async (id: string) => {
      if (activeUserId === id) {
        return;
      }

      setPending(`activate:${id}`);
      try {
        await selectUser(id);
        const displayName = users.find((user) => user.id === id)?.name ?? 'profile';
        showToast(`Switched to ${displayName}.`, { tone: 'success' });
      } catch (err) {
        showToast(formatErrorMessage(err), { tone: 'danger' });
      } finally {
        setPending(null);
      }
    },
    [activeUserId, selectUser, users, showToast],
  );

  const handleUpdateColor = useCallback(
    async (id: string, color: string) => {
      setPending(`color:${id}`);
      try {
        await updateColor(id, color);
        // Update the selected profile with the new color so UI reflects immediately
        setSelectedProfile((current) => (current && current.id === id ? { ...current, color } : current));
        setOpenColorSelectorId(null);
        showToast('Profile color updated.', { tone: 'success' });
      } catch (err) {
        showToast(formatErrorMessage(err), { tone: 'danger' });
      } finally {
        setPending(null);
      }
    },
    [updateColor, showToast],
  );

  // TV: Close profile actions (deselect) - defined before the back button handler
  const handleCloseProfileActions = useCallback(() => {
    setSelectedProfile(null);
  }, []);

  // Guard against duplicate "select" events on tvOS (e.g., key down/up or Modal duplication)
  const selectGuardRef = useRef(false);
  const withSelectGuard = useCallback((fn: () => void) => {
    if (isTV) {
      if (selectGuardRef.current) return;
      selectGuardRef.current = true;
      try {
        fn();
      } finally {
        setTimeout(() => {
          selectGuardRef.current = false;
        }, 250);
      }
    } else {
      fn();
    }
  }, []);

  // Keep ref up to date to avoid stale closures
  const onCloseRef = useRef(handleCloseProfileActions);
  const removeInterceptorRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    onCloseRef.current = handleCloseProfileActions;
  }, [handleCloseProfileActions]);

  // Register back interceptor to close modal when menu/back button is pressed on tvOS
  useEffect(() => {
    if (!isProfileModalVisible) {
      // Clean up interceptor when modal is hidden
      if (removeInterceptorRef.current) {
        removeInterceptorRef.current();
        removeInterceptorRef.current = null;
      }
      return;
    }

    // Install interceptor when modal is shown
    let isHandling = false;
    let cleanupScheduled = false;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      // Prevent duplicate handling if called multiple times
      if (isHandling) {
        return true;
      }

      isHandling = true;

      // Call onClose using ref to avoid stale closure
      onCloseRef.current();

      // Delay the cleanup to ensure it stays active long enough to swallow duplicate events
      if (!cleanupScheduled) {
        cleanupScheduled = true;
        setTimeout(() => {
          if (removeInterceptorRef.current) {
            removeInterceptorRef.current();
            removeInterceptorRef.current = null;
          }
          isHandling = false;
        }, 750);
      }

      return true; // Handled - prevents further interceptors from running
    });

    removeInterceptorRef.current = removeInterceptor;

    // Cleanup on unmount
    return () => {
      // Interceptor will be removed by delayed cleanup if scheduled
    };
  }, [isProfileModalVisible]);

  // TV: Handle left key to open menu when spatial nav can't move further left
  const handleDirectionWithoutMovement = useCallback(
    (direction: string) => {
      if (direction === 'left' && !isMenuOpen) {
        openMenu();
      }
    },
    [isMenuOpen, openMenu],
  );

  // TV: Grid data for profile cards
  const gridData = useMemo<GridItem[]>(() => {
    return users.map((profile) => ({ profile }));
  }, [users]);

  // TV: Split grid data into rows of 3 columns for proper grid navigation
  const gridRows = useMemo(() => {
    const columnsPerRow = 3;
    const rows: GridItem[][] = [];
    for (let i = 0; i < gridData.length; i += columnsPerRow) {
      rows.push(gridData.slice(i, i + columnsPerRow));
    }
    return rows;
  }, [gridData]);

  // TV: Handle selecting a profile card to show actions
  const handleProfileCardSelect = useCallback((profile: UserProfile) => {
    setSelectedProfile(profile);
  }, []);

  // TV: Render grid item with spatial navigation
  const renderGridItem = useCallback(
    ({ item, index }: { item: GridItem; index: number }) => {
      const { profile } = item;
      const isProfileActive = activeUserId === profile.id;
      const avatarColor = profile.color || undefined;
      const hasIcon = profile.hasIcon;

      const profileCard = (
        <SpatialNavigationFocusableView
          key={profile.id}
          onSelect={() => handleProfileCardSelect(profile)}>
          {({ isFocused }: { isFocused: boolean }) => (
            <View
              style={[
                styles.gridCard,
                isFocused && styles.gridCardFocused,
                isProfileActive && styles.gridCardActive,
              ]}>
              <View style={styles.avatarContainer}>
                {hasIcon ? (
                  <Image source={{ uri: getIconUrl(profile.id) }} style={styles.gridCardAvatar} resizeMode="cover" />
                ) : (
                  <View style={[styles.gridCardAvatar, avatarColor && { backgroundColor: avatarColor }]}>
                    <Text style={styles.gridCardAvatarText}>{profile.name.charAt(0).toUpperCase()}</Text>
                  </View>
                )}
                {profile.hasPin && (
                  <View style={styles.pinIndicator}>
                    <Text style={styles.pinIndicatorText}>PIN</Text>
                  </View>
                )}
                {profile.isKidsProfile && (
                  <View style={styles.kidsIndicator}>
                    <Text style={styles.kidsIndicatorText}>KIDS</Text>
                  </View>
                )}
              </View>
              <Text style={styles.gridCardName} numberOfLines={1}>
                {profile.name}
              </Text>
              {isProfileActive && <Text style={styles.gridCardBadge}>Active</Text>}
            </View>
          )}
        </SpatialNavigationFocusableView>
      );

      // Give first profile default focus
      if (index === 0) {
        return <DefaultFocus key={profile.id}>{profileCard}</DefaultFocus>;
      }
      return profileCard;
    },
    [activeUserId, styles, handleProfileCardSelect, getIconUrl],
  );

  // TV Layout - Spatial navigation
  if (Platform.isTV) {
    // Spatial navigation is active when menu and modal are closed
    const isActive = !isMenuOpen && !isProfileModalVisible;

    // Main page content wrapped in spatial navigation
    const pageContent = (
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.tvContainer}>
          <View style={styles.headerRow}>
            <View>
              <Text style={styles.title}>Profiles</Text>
              <Text style={styles.description}>Select a profile to switch or customize</Text>
            </View>
          </View>

          {loading ? (
            <View style={styles.loadingContainer}>
              <ActivityIndicator size="large" color={theme.colors.accent.primary} />
              <Text style={styles.loadingText}>Loading profiles…</Text>
            </View>
          ) : (
            <SpatialNavigationScrollView horizontal={false} style={styles.virtualizedGrid}>
              <SpatialNavigationNode orientation="vertical" alignInGrid>
                {gridRows.map((row, rowIndex) => (
                  <View key={`row-${rowIndex}`} style={styles.gridRowContainer}>
                    <SpatialNavigationNode orientation="horizontal">
                      {row.map((item, colIndex) => renderGridItem({ item, index: rowIndex * 3 + colIndex }))}
                    </SpatialNavigationNode>
                  </View>
                ))}
              </SpatialNavigationNode>
            </SpatialNavigationScrollView>
          )}
        </View>
      </FixedSafeAreaView>
    );

    // Profile Actions Modal - outside SpatialNavigationRoot with raw Pressable for native focus
    const profileModal = isProfileModalVisible && selectedProfile ? (
      <View style={styles.modalOverlay}>
        <View style={styles.modalContainer}>
          <View style={styles.profileModalHeader}>
            {selectedProfile.hasIcon ? (
              <Image
                source={{ uri: getIconUrl(selectedProfile.id) }}
                style={styles.profileModalAvatar}
                resizeMode="cover"
              />
            ) : (
              <View
                style={[
                  styles.profileModalAvatar,
                  selectedProfile.color && { backgroundColor: selectedProfile.color },
                ]}>
                <Text style={styles.profileModalAvatarText}>
                  {selectedProfile.name.charAt(0).toUpperCase()}
                </Text>
              </View>
            )}
            <Text style={styles.modalTitle}>{selectedProfile.name}</Text>
          </View>

          {!selectedProfile.hasIcon && (
            <View style={styles.colorPickerSection}>
              <Text style={styles.colorPickerLabel}>Profile Color</Text>
              {/* First row of 4 colors */}
              <View style={styles.colorPickerRow}>
                {PROFILE_COLORS.slice(0, 4).map((color, colorIndex) => {
                  const isSelected = selectedProfile.color === color.value;
                  return (
                    <Pressable
                      key={color.value}
                      onPress={() => withSelectGuard(() => handleUpdateColor(selectedProfile.id, color.value))}
                      android_disableSound
                      hasTVPreferredFocus={colorIndex === 0}
                      tvParallaxProperties={{ enabled: false }}
                      style={({ focused }) => [
                        styles.colorSwatch,
                        { backgroundColor: color.value },
                        focused && styles.colorSwatchFocused,
                        isSelected && styles.colorSwatchSelected,
                      ]}
                    />
                  );
                })}
              </View>
              {/* Second row of 4 colors */}
              <View style={styles.colorPickerRow}>
                {PROFILE_COLORS.slice(4, 8).map((color) => {
                  const isSelected = selectedProfile.color === color.value;
                  return (
                    <Pressable
                      key={color.value}
                      onPress={() => withSelectGuard(() => handleUpdateColor(selectedProfile.id, color.value))}
                      android_disableSound
                      tvParallaxProperties={{ enabled: false }}
                      style={({ focused }) => [
                        styles.colorSwatch,
                        { backgroundColor: color.value },
                        focused && styles.colorSwatchFocused,
                        isSelected && styles.colorSwatchSelected,
                      ]}
                    />
                  );
                })}
              </View>
            </View>
          )}

          <View style={styles.modalButtonsContainer}>
            <Pressable
              onPress={() => withSelectGuard(() => {
                void handleActivateProfile(selectedProfile.id);
                handleCloseProfileActions();
              })}
              disabled={activeUserId === selectedProfile.id || pending === `activate:${selectedProfile.id}`}
              android_disableSound
              hasTVPreferredFocus={selectedProfile.hasIcon}
              tvParallaxProperties={{ enabled: false }}
              style={({ focused }) => [
                styles.modalButton,
                focused && styles.modalButtonFocused,
                activeUserId === selectedProfile.id && { opacity: 0.5 },
              ]}>
              {({ focused }) => (
                <Text style={[styles.modalButtonText, focused && styles.modalButtonTextFocused]}>
                  {activeUserId === selectedProfile.id ? 'Currently Active' : 'Set as Active'}
                </Text>
              )}
            </Pressable>
            <Pressable
              onPress={() => withSelectGuard(handleCloseProfileActions)}
              android_disableSound
              tvParallaxProperties={{ enabled: false }}
              style={({ focused }) => [styles.modalButton, focused && styles.modalButtonFocused]}>
              {({ focused }) => (
                <Text style={[styles.modalButtonText, focused && styles.modalButtonTextFocused]}>Close</Text>
              )}
            </Pressable>
          </View>

          <View style={styles.adminInfoNote}>
            <Ionicons
              name="information-circle-outline"
              size={responsiveSize(24, 18)}
              color={theme.colors.text.muted}
            />
            <Text style={styles.adminInfoNoteText}>
              To create, rename, set PIN, or delete profiles, use the Admin Web UI
            </Text>
          </View>
        </View>
      </View>
    ) : null;

    return (
      <>
        <Stack.Screen options={{ headerShown: false }} />
        <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={handleDirectionWithoutMovement}>
          {pageContent}
        </SpatialNavigationRoot>
        {profileModal}
      </>
    );
  }

  // Mobile Layout - uses card grid similar to TV
  return (
    <>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          <ScrollView
            contentContainerStyle={styles.scrollContent}
            contentInsetAdjustmentBehavior="never"
            automaticallyAdjustContentInsets={false}>
            <View style={styles.headerRow}>
              <View style={styles.headerContent}>
                <Text style={styles.title}>Profiles</Text>
                <Text style={styles.description}>Select a profile to switch or customize</Text>
              </View>
            </View>

            {loading ? (
              <View style={styles.loadingContainer}>
                <ActivityIndicator size="large" color={theme.colors.accent.primary} />
                <Text style={styles.loadingText}>Loading profiles…</Text>
              </View>
            ) : users.length === 0 ? (
              <Text style={styles.emptyText}>No profiles yet. Create profiles in the Admin Web UI.</Text>
            ) : (
              <View style={styles.mobileCardGrid}>
                {users.map((user) => {
                  const isProfileActive = activeUserId === user.id;
                  const avatarColor = user.color || undefined;
                  const hasIcon = user.hasIcon;

                  return (
                    <Pressable
                      key={user.id}
                      onPress={() => handleProfileCardSelect(user)}
                      style={[styles.mobileCard, isProfileActive && styles.mobileCardActive]}>
                      {hasIcon ? (
                        <Image
                          source={{ uri: getIconUrl(user.id) }}
                          style={styles.mobileCardAvatar}
                          resizeMode="cover"
                        />
                      ) : (
                        <View style={[styles.mobileCardAvatar, avatarColor && { backgroundColor: avatarColor }]}>
                          <Text style={styles.mobileCardAvatarText}>{user.name.charAt(0).toUpperCase()}</Text>
                        </View>
                      )}
                      {user.hasPin && (
                        <View style={styles.mobileCardPinIndicator}>
                          <Text style={styles.mobileCardPinIndicatorText}>PIN</Text>
                        </View>
                      )}
                      {user.isKidsProfile && (
                        <View style={styles.mobileCardKidsIndicator}>
                          <Text style={styles.mobileCardKidsIndicatorText}>KIDS</Text>
                        </View>
                      )}
                      <Text style={styles.mobileCardName} numberOfLines={1}>
                        {user.name}
                      </Text>
                      {isProfileActive && <Text style={styles.mobileCardBadge}>Active</Text>}
                    </Pressable>
                  );
                })}
              </View>
            )}

            <View style={styles.adminInfoNoteMobile}>
              <Ionicons name="information-circle-outline" size={16} color={theme.colors.text.muted} />
              <Text style={styles.adminInfoNoteTextMobile}>
                To create, rename, set PIN, or delete profiles, use the Admin Web UI
              </Text>
            </View>
          </ScrollView>
        </View>
      </FixedSafeAreaView>

      {/* Profile Actions Modal for Mobile */}
      <Modal
        visible={isProfileModalVisible && selectedProfile !== null}
        transparent={true}
        animationType="fade"
        onRequestClose={handleCloseProfileActions}>
        <View style={styles.modalOverlay}>
          <View style={styles.mobileModalContainer}>
            {selectedProfile && (
              <>
                <View style={styles.profileModalHeader}>
                  {selectedProfile.hasIcon ? (
                    <Image
                      source={{ uri: getIconUrl(selectedProfile.id) }}
                      style={styles.profileModalAvatar}
                      resizeMode="cover"
                    />
                  ) : (
                    <View
                      style={[
                        styles.profileModalAvatar,
                        selectedProfile.color && { backgroundColor: selectedProfile.color },
                      ]}>
                      <Text style={styles.profileModalAvatarText}>{selectedProfile.name.charAt(0).toUpperCase()}</Text>
                    </View>
                  )}
                  <Text style={styles.mobileModalTitle}>{selectedProfile.name}</Text>
                  {selectedProfile.isKidsProfile && (
                    <View style={styles.mobileModalKidsBadge}>
                      <Text style={styles.mobileModalKidsBadgeText}>Kids Profile</Text>
                    </View>
                  )}
                </View>

                {!selectedProfile.hasIcon && (
                  <View style={styles.colorPickerSection}>
                    <Text style={styles.colorPickerLabel}>Profile Color</Text>
                    <View style={styles.mobileColorPickerRow}>
                      {PROFILE_COLORS.map((color) => {
                        const isSelected = selectedProfile.color === color.value;
                        return (
                          <Pressable
                            key={color.value}
                            onPress={() => handleUpdateColor(selectedProfile.id, color.value)}
                            style={[
                              styles.colorSwatch,
                              { backgroundColor: color.value },
                              isSelected && styles.colorSwatchSelected,
                            ]}
                          />
                        );
                      })}
                    </View>
                  </View>
                )}

                <View style={styles.mobileModalActions}>
                  {activeUserId !== selectedProfile.id && (
                    <Pressable
                      onPress={() => {
                        handleActivateProfile(selectedProfile.id);
                        handleCloseProfileActions();
                      }}
                      style={[styles.mobileModalButton, styles.mobileModalButtonPrimary]}>
                      <Text style={[styles.mobileModalButtonText, styles.mobileModalButtonPrimaryText]}>
                        Set as Active
                      </Text>
                    </Pressable>
                  )}
                  <Pressable onPress={handleCloseProfileActions} style={styles.mobileModalButton}>
                    <Text style={styles.mobileModalButtonText}>Close</Text>
                  </Pressable>
                </View>
              </>
            )}
          </View>
        </View>
      </Modal>
    </>
  );
}

const createStyles = (theme: NovaTheme, screenWidth = 1920, screenHeight = 1080) => {
  const isTVPlatform = Platform.isTV;
  const isCompact = theme.breakpoint === 'compact';
  const horizontalPadding = isTVPlatform ? theme.spacing.xl * 1.5 : isCompact ? theme.spacing.lg : theme.spacing['2xl'];

  // Ensure we have valid screen dimensions (fallback to 1920x1080 for TV)
  const effectiveWidth = screenWidth > 0 ? screenWidth : isTV ? 1920 : 375;
  const _effectiveHeight = screenHeight > 0 ? screenHeight : isTV ? 1080 : 812;

  // TV grid configuration
  const columnsCount = isTV ? 3 : 4; // Fewer columns since content area is narrower
  const gap = theme.spacing.xl;
  // TV: 20% padding each side means 60% content width
  const tvAvailableWidth = effectiveWidth * 0.6;
  const availableWidth = isTV ? tvAvailableWidth : effectiveWidth - horizontalPadding * 2;
  const totalGapWidth = gap * (columnsCount - 1);
  const cardWidth = isTV ? Math.floor((availableWidth - totalGapWidth) / columnsCount) : 0;
  const cardHeight = isTV ? Math.round(cardWidth * 1.1) : 0; // Slightly taller than wide for profile cards
  const gridItemHeight = cardHeight + gap;

  // Mobile grid configuration - 3 columns responsive to screen width
  const mobileGap = theme.spacing.md;
  const mobileAvailableWidth = effectiveWidth - horizontalPadding * 2;
  const mobileCardWidth = Math.floor((mobileAvailableWidth - mobileGap * 2) / 3);
  const mobileAvatarSize = Math.min(56, Math.floor(mobileCardWidth * 0.5));
  const mobileAvatarFontSize = Math.floor(mobileAvatarSize * 0.45);

  const styles = StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: isTV ? 'transparent' : theme.colors.background.base,
    },
    // TV: Simple container with horizontal padding for centering effect
    tvContainer: {
      flex: 1,
      paddingHorizontal: isTV ? effectiveWidth * 0.2 : horizontalPadding, // 20% padding each side = 60% content
      paddingTop: theme.spacing.xl * 1.5,
    },
    container: {
      flex: 1,
      backgroundColor: isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: horizontalPadding,
      paddingTop: theme.spacing.xl * (isTV ? 1.5 : 1),
    },
    scrollContent: {
      paddingBottom: theme.spacing['3xl'],
      gap: theme.spacing.xl,
    },
    header: {
      gap: theme.spacing.sm,
    },
    // Header row (shared TV/mobile)
    headerRow: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      marginBottom: theme.spacing.xl,
      flexWrap: 'wrap',
      gap: theme.spacing.md,
    },
    headerContent: {
      flex: 1,
      minWidth: 200,
    },
    headerButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    description: {
      ...theme.typography.title.md,
      color: theme.colors.text.secondary,
      fontWeight: '400',
    },
    loadingContainer: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      gap: theme.spacing.lg,
    },
    loadingText: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    emptyText: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    mobileColorPickerRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm,
      marginVertical: theme.spacing.sm,
    },
    mobileRefreshButton: {
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.sm,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    mobileRefreshButtonText: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },

    // TV Grid styles
    virtualizedGrid: {
      flex: 1,
    },
    gridRowContainer: {
      flexDirection: 'row',
      gap: gap,
      marginBottom: gap,
    },
    gridCard: {
      width: cardWidth,
      height: cardHeight,
      borderRadius: theme.radius.lg,
      backgroundColor: theme.colors.background.surface,
      borderWidth: 3,
      borderColor: 'transparent',
      justifyContent: 'center',
      alignItems: 'center',
      gap: theme.spacing.lg,
      padding: theme.spacing.xl,
    },
    gridCardFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
      shadowColor: theme.colors.accent.primary,
      shadowOpacity: 0.4,
      shadowOffset: { width: 0, height: 8 },
      shadowRadius: 16,
    },
    gridCardActive: {
      borderColor: theme.colors.accent.primary,
    },
    avatarContainer: {
      position: 'relative',
    },
    gridCardAvatar: {
      width: cardWidth * 0.4,
      height: cardWidth * 0.4,
      borderRadius: cardWidth * 0.2,
      backgroundColor: theme.colors.background.elevated,
      justifyContent: 'center',
      alignItems: 'center',
      position: 'relative',
    },
    pinIndicator: {
      position: 'absolute',
      bottom: -4,
      right: -4,
      backgroundColor: theme.colors.accent.primary,
      paddingHorizontal: 6,
      paddingVertical: 2,
      borderRadius: 4,
      minWidth: 28,
      alignItems: 'center',
    },
    pinIndicatorText: {
      fontSize: 10,
      fontWeight: '700',
      color: 'white',
      letterSpacing: 0.5,
    },
    kidsIndicator: {
      position: 'absolute',
      top: 12,
      right: 4,
      backgroundColor: '#22C55E',
      paddingHorizontal: 8,
      paddingVertical: 4,
      borderRadius: 6,
      minWidth: 40,
      alignItems: 'center',
    },
    kidsIndicatorText: {
      fontSize: 14,
      fontWeight: '700',
      color: 'white',
      letterSpacing: 0.5,
    },
    gridCardAvatarText: {
      fontSize: cardWidth * 0.2,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    gridCardName: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    gridCardBadge: {
      ...theme.typography.title.md,
      color: theme.colors.accent.primary,
      textAlign: 'center',
      position: 'absolute',
      bottom: theme.spacing.lg,
      left: 0,
      right: 0,
    },

    // Mobile card grid styles
    mobileCardGrid: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: mobileGap,
      justifyContent: 'flex-start',
    },
    mobileCard: {
      width: mobileCardWidth,
      aspectRatio: 0.85,
      borderRadius: theme.radius.lg,
      backgroundColor: theme.colors.background.surface,
      borderWidth: 2,
      borderColor: 'transparent',
      justifyContent: 'center',
      alignItems: 'center',
      gap: theme.spacing.sm,
      padding: theme.spacing.sm,
    },
    mobileCardActive: {
      borderColor: theme.colors.accent.primary,
    },
    mobileCardAvatar: {
      width: mobileAvatarSize,
      height: mobileAvatarSize,
      borderRadius: mobileAvatarSize / 2,
      backgroundColor: theme.colors.background.elevated,
      justifyContent: 'center',
      alignItems: 'center',
      position: 'relative',
    },
    mobileCardAvatarText: {
      fontSize: mobileAvatarFontSize,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    mobileCardPinIndicator: {
      position: 'absolute',
      bottom: -3,
      right: -3,
      backgroundColor: theme.colors.accent.primary,
      paddingHorizontal: 4,
      paddingVertical: 1,
      borderRadius: 3,
      minWidth: 22,
      alignItems: 'center',
    },
    mobileCardPinIndicatorText: {
      fontSize: 8,
      fontWeight: '700',
      color: 'white',
      letterSpacing: 0.3,
    },
    mobileCardKidsIndicator: {
      position: 'absolute',
      bottom: -3,
      left: -3,
      backgroundColor: '#22C55E',
      paddingHorizontal: 4,
      paddingVertical: 1,
      borderRadius: 3,
      minWidth: 26,
      alignItems: 'center',
    },
    mobileCardKidsIndicatorText: {
      fontSize: 8,
      fontWeight: '700',
      color: 'white',
      letterSpacing: 0.3,
    },
    mobileCardName: {
      ...theme.typography.body.sm,
      fontWeight: '600',
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    mobileCardBadge: {
      ...theme.typography.caption.sm,
      color: theme.colors.accent.primary,
      textAlign: 'center',
    },
    mobileModalKidsBadge: {
      backgroundColor: '#22C55E',
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
    },
    mobileModalKidsBadgeText: {
      ...theme.typography.label.md,
      color: 'white',
      fontWeight: '600',
    },
    colorPickerSection: {
      gap: isTV ? theme.spacing.sm : theme.spacing.sm,
      marginBottom: isTV ? theme.spacing.lg : theme.spacing.md,
    },
    colorPickerLabel: {
      ...(isTV ? theme.typography.label.md : theme.typography.body.sm),
      color: theme.colors.text.secondary,
      textAlign: 'center',
      marginBottom: isTV ? theme.spacing.sm : theme.spacing.xs,
    },

    // Modal styles for TV
    modalOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    modalContainer: {
      width: '60%',
      maxWidth: 700,
      margin: '10%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      padding: theme.spacing['2xl'],
      gap: theme.spacing.lg,
    },
    modalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    pinErrorContainer: {
      backgroundColor: 'rgba(239, 68, 68, 0.15)',
      borderRadius: theme.radius.sm,
      padding: theme.spacing.md,
      marginTop: theme.spacing.md,
      marginBottom: theme.spacing.md,
    },
    pinErrorText: {
      color: '#EF4444',
      fontSize: Platform.isTV ? 16 : 14,
      textAlign: 'center',
    },
    // Mobile modal styles
    mobileModalContainer: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg,
      padding: theme.spacing.xl,
      marginHorizontal: theme.spacing.lg,
      maxWidth: 400,
      width: '100%',
      alignSelf: 'center',
    },
    mobileModalTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      textAlign: 'center',
      marginBottom: theme.spacing.sm,
    },
    mobileModalSubtitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      textAlign: 'center',
      marginBottom: theme.spacing.lg,
    },
    pinModalInput: {
      marginBottom: theme.spacing.lg,
      textAlign: 'center',
      fontFamily: Platform.OS === 'ios' ? 'System' : 'sans-serif',
    },
    mobileModalActions: {
      flexDirection: 'row',
      justifyContent: 'center',
      gap: theme.spacing.md,
    },
    mobileModalButton: {
      flex: 1,
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.lg,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.elevated,
      alignItems: 'center',
    },
    mobileModalButtonPrimary: {
      backgroundColor: theme.colors.accent.primary,
    },
    mobileModalButtonText: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },
    mobileModalButtonPrimaryText: {
      color: 'white',
    },
    mobileModalButtonDanger: {
      backgroundColor: theme.colors.status.danger,
    },
    mobileModalButtonDangerText: {
      color: 'white',
    },
    modalButton: {
      paddingHorizontal: theme.spacing.lg * (isTV ? 1.375 : 1),
      paddingVertical: theme.spacing.md * (isTV ? 1.375 : 1),
      borderRadius: theme.radius.md * (isTV ? 1.375 : 1),
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.overlay.button,
      alignItems: 'center',
      alignSelf: 'center',
      minWidth: isTV ? 200 : 120,
    },
    modalButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    modalButtonText: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      fontSize: theme.typography.label.md.fontSize * (isTV ? 1.375 : 1),
      lineHeight: theme.typography.label.md.lineHeight * (isTV ? 1.375 : 1),
    },
    modalButtonTextFocused: {
      ...theme.typography.label.md,
      color: theme.colors.text.inverse,
      fontSize: theme.typography.label.md.fontSize * (isTV ? 1.375 : 1),
      lineHeight: theme.typography.label.md.lineHeight * (isTV ? 1.375 : 1),
    },
    modalButtonsContainer: {
      gap: theme.spacing.md,
      alignItems: 'center',
    },
    // Profile actions modal styles
    profileModalHeader: {
      alignItems: 'center',
      gap: isTV ? theme.spacing.lg : theme.spacing.md,
      marginBottom: isTV ? theme.spacing.lg : theme.spacing.md,
    },
    profileModalAvatar: {
      width: isTV ? 100 : 64,
      height: isTV ? 100 : 64,
      borderRadius: isTV ? 50 : 32,
      backgroundColor: theme.colors.background.surface,
      justifyContent: 'center',
      alignItems: 'center',
    },
    profileModalAvatarText: {
      fontSize: isTV ? 48 : 28,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    modalButtonDanger: {
      borderColor: theme.colors.status.danger,
      backgroundColor: theme.colors.status.danger + '20',
    },
    modalButtonDangerFocused: {
      borderColor: theme.colors.status.danger,
      backgroundColor: theme.colors.status.danger + '30',
    },
    modalButtonDangerText: {
      color: theme.colors.status.danger,
    },
    modalButtonDangerTextFocused: {
      color: theme.colors.status.danger,
    },
    // Color picker styles (TV)
    colorPickerRow: {
      flexDirection: 'row',
      justifyContent: 'center',
      gap: theme.spacing.md,
      flexWrap: 'wrap',
    },
    colorSwatch: {
      width: isTV ? 48 : 36,
      height: isTV ? 48 : 36,
      borderRadius: isTV ? 24 : 18,
      borderWidth: isTV ? 3 : 2,
      borderColor: 'transparent',
    },
    colorSwatchFocused: {
      borderColor: theme.colors.text.primary,
      transform: [{ scale: 1.15 }],
    },
    colorSwatchSelected: {
      borderColor: theme.colors.text.primary,
    },
    // Profile name text (mobile)
    profileName: {
      ...theme.typography.body.lg,
      color: theme.colors.text.primary,
      flex: 1,
    },
    // Admin info note styles (TV)
    adminInfoNote: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'center',
      gap: theme.spacing.sm,
      marginTop: theme.spacing.md,
      paddingTop: theme.spacing.md,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
    },
    adminInfoNoteText: {
      ...theme.typography.body.md,
      color: theme.colors.text.muted,
    },
    // Admin info note styles (Mobile)
    adminInfoNoteMobile: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
      marginTop: theme.spacing.lg,
      paddingTop: theme.spacing.md,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
    },
    adminInfoNoteTextMobile: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      flex: 1,
    },
  });

  return {
    ...styles,
    gridItemHeight,
  };
};
