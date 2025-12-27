import { Ionicons } from '@expo/vector-icons';
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Keyboard,
  Modal,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  useWindowDimensions,
  View,
} from 'react-native';

import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import { useMenuContext } from '@/components/MenuContext';
import { useToast } from '@/components/ToastContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import type { UserProfile } from '@/services/api';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  SpatialNavigationScrollView,
  SpatialNavigationVirtualizedGrid,
  useLockSpatialNavigation,
  useSpatialNavigator,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { Stack } from 'expo-router';

type PendingAction = null | 'create' | 'refresh' | `activate:${string}` | `color:${string}`;

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

// Grid item types for TV virtualized grid
type CreateProfileGridItem = { type: 'create' };
type ProfileGridItem = { type: 'profile'; profile: UserProfile };
type GridItem = CreateProfileGridItem | ProfileGridItem;

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
  const { width: screenWidth, height: screenHeight } = useWindowDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth, screenHeight), [theme, screenWidth, screenHeight]);
  const isFocused = useIsFocused();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const [isCreateModalVisible, setIsCreateModalVisible] = useState(false);
  const {
    users,
    loading,
    error,
    activeUserId,
    selectUser,
    createUser,
    updateColor,
    refresh,
    pendingPinUserId,
  } = useUserProfiles();
  const { showToast } = useToast();

  const [newProfileName, setNewProfileName] = useState('');
  const [pending, setPending] = useState<PendingAction>(null);
  const [selectedProfile, setSelectedProfile] = useState<UserProfile | null>(null);
  const [newlyCreatedProfileId, setNewlyCreatedProfileId] = useState<string | null>(null);
  const [openColorSelectorId, setOpenColorSelectorId] = useState<string | null>(null);
  const newProfileInputRef = useRef<TextInput | null>(null);
  const tempProfileNameRef = useRef('');
  const { lock, unlock } = useLockSpatialNavigation();
  const { grabFocus } = useSpatialNavigator();

  const isProfileModalVisible = selectedProfile !== null;
  const isActive = isFocused && !isMenuOpen && !isCreateModalVisible && !isProfileModalVisible && !pendingPinUserId;

  // Auto-focus newly created profile
  useEffect(() => {
    if (newlyCreatedProfileId && users.some((u) => u.id === newlyCreatedProfileId)) {
      // Small delay to ensure the grid has rendered the new item
      const timer = setTimeout(() => {
        grabFocus(`profile-card-${newlyCreatedProfileId}`);
        setNewlyCreatedProfileId(null);
      }, 100);
      return () => clearTimeout(timer);
    }
  }, [newlyCreatedProfileId, users, grabFocus]);

  useEffect(() => {
    if (error) {
      showToast(error, { tone: 'danger', duration: 7000 });
    }
  }, [error, showToast]);

  const handleCreateProfile = useCallback(async () => {
    const trimmed = newProfileName.trim();
    if (!trimmed) {
      showToast('Profile name cannot be empty.', { tone: 'danger' });
      return;
    }

    setPending('create');
    try {
      const created = await createUser(trimmed);
      setNewProfileName('');
      showToast(`Created profile "${created.name}".`, { tone: 'success' });
    } catch (err) {
      showToast(formatErrorMessage(err), { tone: 'danger' });
    } finally {
      setPending(null);
    }
  }, [createUser, newProfileName, showToast]);

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

  const handleRefreshProfiles = useCallback(async () => {
    setPending('refresh');
    try {
      await refresh();
    } catch (err) {
      showToast(formatErrorMessage(err), { tone: 'danger' });
    } finally {
      setPending(null);
    }
  }, [refresh, showToast]);

  const isCreateDisabled = pending === 'create' || !newProfileName.trim();

  const handleFocus = useCallback(() => {
    // Lock spatial navigation to prevent d-pad from navigating away while typing
    lock();
  }, [lock]);

  const handleBlur = useCallback(() => {
    // Unlock spatial navigation to re-enable d-pad navigation
    unlock();
    // On TV, sync the temp ref value to state on blur
    if (Platform.isTV) {
      const finalValue = tempProfileNameRef.current;
      if (finalValue !== newProfileName) {
        setNewProfileName(finalValue);
      }
    }
  }, [unlock, newProfileName]);

  const handleChangeText = useCallback((text: string) => {
    if (Platform.isTV) {
      // On tvOS, store in ref to avoid controlled input issues
      tempProfileNameRef.current = text;
    } else {
      // On mobile, use normal controlled input
      setNewProfileName(text);
    }
  }, []);

  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      if (movement === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

  // TV: Grid data combining create card and profile cards
  const gridData = useMemo<GridItem[]>(() => {
    const items: GridItem[] = [{ type: 'create' }];
    users.forEach((profile) => {
      items.push({ type: 'profile', profile });
    });
    return items;
  }, [users]);

  // TV: Handle selecting a profile card to show actions
  const handleProfileCardSelect = useCallback((profile: UserProfile) => {
    setSelectedProfile(profile);
  }, []);

  // TV: Close profile actions (deselect)
  const handleCloseProfileActions = useCallback(() => {
    setSelectedProfile(null);
  }, []);

  // TV: Open create modal
  const handleOpenCreateModal = useCallback(() => {
    setIsCreateModalVisible(true);
  }, []);

  // TV: Close create modal
  const handleCloseCreateModal = useCallback(() => {
    // Clean up keyboard on TV before closing
    if (Platform.isTV) {
      newProfileInputRef.current?.blur();
      Keyboard.dismiss();
    }
    setIsCreateModalVisible(false);
    setNewProfileName('');
    tempProfileNameRef.current = '';
  }, []);

  // TV: Create profile from modal
  const handleCreateFromModal = useCallback(async () => {
    // On TV, use the temp ref value since we're using uncontrolled input
    const nameValue = Platform.isTV ? tempProfileNameRef.current : newProfileName;
    const trimmed = nameValue.trim();
    if (!trimmed) {
      showToast('Profile name cannot be empty.', { tone: 'danger' });
      return;
    }

    // Clean up keyboard on TV before closing
    if (Platform.isTV) {
      newProfileInputRef.current?.blur();
      Keyboard.dismiss();
    }

    setPending('create');
    try {
      const created = await createUser(trimmed);
      setNewProfileName('');
      tempProfileNameRef.current = '';
      showToast(`Created profile "${created.name}".`, { tone: 'success' });
      setIsCreateModalVisible(false);
      setNewlyCreatedProfileId(created.id);
    } catch (err) {
      showToast(formatErrorMessage(err), { tone: 'danger' });
    } finally {
      setPending(null);
    }
  }, [createUser, newProfileName, showToast]);

  // TV: Render grid item
  const renderGridItem = useCallback(
    ({ item }: { item: GridItem }) => {
      if (item.type === 'create') {
        return (
          <SpatialNavigationFocusableView focusKey="create-profile-card" onSelect={handleOpenCreateModal}>
            {({ isFocused }: { isFocused: boolean }) => (
              <View style={[styles.gridCard, styles.createCard, isFocused && styles.gridCardFocused]}>
                <Text style={styles.createCardIcon}>+</Text>
                <Text style={styles.createCardText}>Create Profile</Text>
              </View>
            )}
          </SpatialNavigationFocusableView>
        );
      }

      const { profile } = item;
      const isProfileActive = activeUserId === profile.id;
      const avatarColor = profile.color || undefined;

      return (
        <SpatialNavigationFocusableView
          focusKey={`profile-card-${profile.id}`}
          onSelect={() => handleProfileCardSelect(profile)}
        >
          {({ isFocused }: { isFocused: boolean }) => (
            <View
              style={[styles.gridCard, isFocused && styles.gridCardFocused, isProfileActive && styles.gridCardActive]}
            >
              <View style={[styles.gridCardAvatar, avatarColor && { backgroundColor: avatarColor }]}>
                <Text style={styles.gridCardAvatarText}>{profile.name.charAt(0).toUpperCase()}</Text>
                {profile.hasPin && (
                  <View style={styles.pinIndicator}>
                    <Text style={styles.pinIndicatorText}>PIN</Text>
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
    },
    [activeUserId, styles, handleOpenCreateModal, handleProfileCardSelect],
  );

  // TV Layout
  if (Platform.isTV) {
    return (
      <>
        <SpatialNavigationRoot
          isActive={isActive}
          onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}
        >
          <Stack.Screen options={{ headerShown: false }} />
          <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
            <View style={styles.tvCenteredWrapper}>
              <View style={styles.tvContentContainer}>
                <View style={styles.headerRow}>
                  <View>
                    <Text style={styles.title}>Profiles</Text>
                    <Text style={styles.description}>Select a profile or create a new one</Text>
                  </View>
                  <SpatialNavigationNode orientation="horizontal">
                    <FocusablePressable
                      focusKey="profiles-refresh"
                      text={pending === 'refresh' ? 'Refreshing…' : 'Refresh'}
                      icon="refresh-outline"
                      onSelect={handleRefreshProfiles}
                      disabled={pending === 'refresh'}
                      style={styles.headerButton}
                    />
                  </SpatialNavigationNode>
                </View>

                {loading ? (
                  <View style={styles.loadingContainer}>
                    <ActivityIndicator size="large" color={theme.colors.accent.primary} />
                    <Text style={styles.loadingText}>Loading profiles…</Text>
                  </View>
                ) : (
                  <DefaultFocus>
                    <SpatialNavigationVirtualizedGrid
                      data={gridData}
                      renderItem={renderGridItem}
                      numberOfColumns={3}
                      itemHeight={styles.gridItemHeight}
                      numberOfRenderedRows={4}
                      numberOfRowsVisibleOnScreen={2}
                      rowContainerStyle={styles.gridRowContainer}
                      style={styles.virtualizedGrid}
                    />
                  </DefaultFocus>
                )}
              </View>
            </View>
          </FixedSafeAreaView>
        </SpatialNavigationRoot>

        {/* Create Profile Modal */}
        <Modal
          visible={isCreateModalVisible}
          transparent={true}
          animationType="fade"
          onRequestClose={handleCloseCreateModal}
        >
          <SpatialNavigationRoot isActive={isCreateModalVisible}>
            <View style={styles.modalOverlay}>
              <View style={styles.modalContainer}>
                <Text style={styles.modalTitle}>Create Profile</Text>
                <Text style={styles.modalSubtitle}>Enter a name for the new profile</Text>

                <SpatialNavigationNode orientation="vertical">
                  <SpatialNavigationFocusableView
                    focusKey="create-modal-input"
                    onSelect={() => {
                      newProfileInputRef.current?.focus();
                    }}
                    onBlur={() => newProfileInputRef.current?.blur()}
                  >
                    {({ isFocused }: { isFocused: boolean }) => (
                      <Pressable tvParallaxProperties={{ enabled: false }}>
                        <TextInput
                          ref={newProfileInputRef}
                          {...(Platform.isTV ? { defaultValue: newProfileName } : { value: newProfileName })}
                          onChangeText={handleChangeText}
                          onFocus={handleFocus}
                          onBlur={handleBlur}
                          placeholder="Profile name"
                          placeholderTextColor={theme.colors.text.muted}
                          style={[styles.modalInput, isFocused && styles.modalInputFocused]}
                          autoCapitalize="none"
                          autoCorrect={false}
                          autoComplete="off"
                          textContentType="none"
                          spellCheck={false}
                          clearButtonMode="never"
                          enablesReturnKeyAutomatically={false}
                          multiline={false}
                          numberOfLines={1}
                          underlineColorAndroid="transparent"
                          importantForAutofill="no"
                          disableFullscreenUI={true}
                          returnKeyType="done"
                          onSubmitEditing={() => {
                            const nameValue = Platform.isTV ? tempProfileNameRef.current : newProfileName;
                            if (nameValue.trim()) {
                              void handleCreateFromModal();
                            }
                          }}
                          showSoftInputOnFocus={true}
                          editable={Platform.isTV ? isFocused : true}
                          {...(Platform.OS === 'ios' &&
                            Platform.isTV && {
                              keyboardAppearance: 'dark',
                            })}
                        />
                      </Pressable>
                    )}
                  </SpatialNavigationFocusableView>

                  <SpatialNavigationNode orientation="horizontal">
                    <View style={styles.modalActions}>
                      <DefaultFocus>
                        <FocusablePressable
                          focusKey="create-modal-cancel"
                          text="Cancel"
                          onSelect={handleCloseCreateModal}
                          style={[styles.modalButton, styles.modalButtonHorizontal]}
                          focusedStyle={styles.modalButtonFocused}
                          textStyle={styles.modalButtonText}
                          focusedTextStyle={styles.modalButtonTextFocused}
                        />
                      </DefaultFocus>
                      <FocusablePressable
                        focusKey="create-modal-create"
                        text={pending === 'create' ? 'Creating…' : 'Create'}
                        onSelect={handleCreateFromModal}
                        disabled={pending === 'create' || !newProfileName.trim()}
                        style={[styles.modalButton, styles.modalButtonHorizontal]}
                        focusedStyle={styles.modalButtonFocused}
                        textStyle={styles.modalButtonText}
                        focusedTextStyle={styles.modalButtonTextFocused}
                      />
                    </View>
                  </SpatialNavigationNode>
                </SpatialNavigationNode>
              </View>
            </View>
          </SpatialNavigationRoot>
        </Modal>

        {/* Profile Actions Modal */}
        <Modal
          visible={isProfileModalVisible && selectedProfile !== null}
          transparent={true}
          animationType="fade"
          onRequestClose={handleCloseProfileActions}
        >
          <SpatialNavigationRoot isActive={isProfileModalVisible}>
            <View style={styles.modalOverlay}>
              <View style={styles.modalContainer}>
                {selectedProfile && (
                  <>
                    <View style={styles.profileModalHeader}>
                      <View
                        style={[
                          styles.profileModalAvatar,
                          selectedProfile.color && { backgroundColor: selectedProfile.color },
                        ]}
                      >
                        <Text style={styles.profileModalAvatarText}>
                          {selectedProfile.name.charAt(0).toUpperCase()}
                        </Text>
                      </View>
                      <Text style={styles.modalTitle}>{selectedProfile.name}</Text>
                    </View>

                    <View style={styles.colorPickerSection}>
                      <Text style={styles.colorPickerLabel}>Profile Color</Text>
                      <SpatialNavigationNode orientation="horizontal">
                        <View style={styles.colorPickerRow}>
                          {PROFILE_COLORS.map((color) => {
                            const isSelected = selectedProfile.color === color.value;
                            return (
                              <SpatialNavigationFocusableView
                                key={color.value}
                                focusKey={`color-${color.value}`}
                                onSelect={() => handleUpdateColor(selectedProfile.id, color.value)}
                              >
                                {({ isFocused }: { isFocused: boolean }) => (
                                  <View
                                    style={[
                                      styles.colorSwatch,
                                      { backgroundColor: color.value },
                                      isFocused && styles.colorSwatchFocused,
                                      isSelected && styles.colorSwatchSelected,
                                    ]}
                                  />
                                )}
                              </SpatialNavigationFocusableView>
                            );
                          })}
                        </View>
                      </SpatialNavigationNode>
                    </View>

                    <SpatialNavigationNode orientation="vertical">
                      <View style={styles.modalButtonsContainer}>
                        <DefaultFocus>
                          <FocusablePressable
                            focusKey="profile-modal-activate"
                            text={activeUserId === selectedProfile.id ? 'Currently Active' : 'Set as Active'}
                            onSelect={() => {
                              void handleActivateProfile(selectedProfile.id);
                              handleCloseProfileActions();
                            }}
                            disabled={
                              activeUserId === selectedProfile.id || pending === `activate:${selectedProfile.id}`
                            }
                            style={styles.modalButton}
                            focusedStyle={styles.modalButtonFocused}
                            textStyle={styles.modalButtonText}
                            focusedTextStyle={styles.modalButtonTextFocused}
                          />
                        </DefaultFocus>
                        <FocusablePressable
                          focusKey="profile-modal-cancel"
                          text="Close"
                          onSelect={handleCloseProfileActions}
                          style={styles.modalButton}
                          focusedStyle={styles.modalButtonFocused}
                          textStyle={styles.modalButtonText}
                          focusedTextStyle={styles.modalButtonTextFocused}
                        />
                      </View>
                    </SpatialNavigationNode>

                    <View style={styles.adminInfoNote}>
                      <Ionicons name="information-circle-outline" size={18} color={theme.colors.text.muted} />
                      <Text style={styles.adminInfoNoteText}>
                        To rename, set PIN, or delete profiles, use the Admin Web UI
                      </Text>
                    </View>
                  </>
                )}
              </View>
            </View>
          </SpatialNavigationRoot>
        </Modal>

      </>
    );
  }

  // Mobile Layout
  return (
    <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          <SpatialNavigationScrollView
            contentContainerStyle={styles.scrollContent}
            contentInsetAdjustmentBehavior="never"
            automaticallyAdjustContentInsets={false}
          >
            <View style={styles.header}>
              <Text style={styles.title}>Profiles</Text>
              <Text style={styles.description}>
                Manage who is watching. Create profiles for each person, give them unique names, and switch between them
                when needed.
              </Text>
            </View>

            <View style={styles.section}>
              <Text style={styles.sectionTitle}>Create a profile</Text>
              <DefaultFocus>
                <SpatialNavigationFocusableView
                  onSelect={() => {
                    // Programmatically focus the TextInput to show keyboard on TV only on press
                    newProfileInputRef.current?.focus();
                  }}
                  onBlur={() => {
                    // Blur the TextInput when spatial navigation moves away
                    newProfileInputRef.current?.blur();
                  }}
                >
                  {({ isFocused }: { isFocused: boolean }) => (
                    <TextInput
                      ref={newProfileInputRef}
                      value={newProfileName}
                      onChangeText={setNewProfileName}
                      onFocus={handleFocus}
                      onBlur={handleBlur}
                      placeholder="Profile name"
                      placeholderTextColor={theme.colors.text.muted}
                      style={[styles.input, isFocused && styles.inputFocused]}
                      autoCapitalize="words"
                      autoCorrect={false}
                      returnKeyType="done"
                      onSubmitEditing={() => (!isCreateDisabled ? void handleCreateProfile() : undefined)}
                      showSoftInputOnFocus={true}
                      editable={Platform.isTV ? isFocused : true}
                    />
                  )}
                </SpatialNavigationFocusableView>
              </DefaultFocus>
              <FocusablePressable
                text={pending === 'create' ? 'Creating…' : 'Create profile'}
                onSelect={handleCreateProfile}
                disabled={isCreateDisabled}
              />
            </View>

            <View style={styles.section}>
              <View style={styles.sectionHeaderRow}>
                <View style={styles.sectionHeaderContent}>
                  <Text style={styles.sectionTitle}>Existing profiles</Text>
                  <Text style={styles.sectionDescription}>Switch between profiles or change colors.</Text>
                </View>
                <View style={styles.sectionHeaderAction}>
                  <FocusablePressable
                    text={pending === 'refresh' ? 'Refreshing…' : 'Refresh'}
                    onSelect={handleRefreshProfiles}
                    disabled={pending === 'refresh'}
                  />
                </View>
              </View>

              {loading ? (
                <View style={styles.loadingRow}>
                  <ActivityIndicator size="small" color={theme.colors.accent.primary} />
                  <Text style={styles.loadingText}>Loading profiles…</Text>
                </View>
              ) : users.length === 0 ? (
                <Text style={styles.emptyText}>No profiles yet. Create your first profile to get started.</Text>
              ) : (
                <View style={styles.profileList}>
                  {users.map((user) => {
                    const isActive = activeUserId === user.id;
                    const activateKey = `activate:${user.id}` as const;
                    const avatarColor = user.color || undefined;
                    const isColorPickerOpen = openColorSelectorId === user.id;

                    return (
                      <View key={user.id} style={[styles.profileCard, isActive && styles.profileCardActive]}>
                        <View style={styles.profileHeader}>
                          <Pressable
                            onPress={() => setOpenColorSelectorId(isColorPickerOpen ? null : user.id)}
                            style={[styles.mobileAvatar, avatarColor && { backgroundColor: avatarColor }]}
                          >
                            <Text style={styles.mobileAvatarText}>{user.name.charAt(0).toUpperCase()}</Text>
                            {user.hasPin && (
                              <View style={styles.mobilePinIndicator}>
                                <Text style={styles.mobilePinIndicatorText}>PIN</Text>
                              </View>
                            )}
                          </Pressable>
                          <Text style={styles.profileName}>{user.name}</Text>
                          {isActive && <Text style={styles.activeBadge}>Active</Text>}
                        </View>

                        {isColorPickerOpen && (
                          <View style={styles.mobileColorPickerRow}>
                            {PROFILE_COLORS.map((color) => {
                              const isSelected = user.color === color.value;
                              return (
                                <Pressable
                                  key={color.value}
                                  onPress={() => handleUpdateColor(user.id, color.value)}
                                  style={[
                                    styles.colorSwatch,
                                    { backgroundColor: color.value },
                                    isSelected && styles.colorSwatchSelected,
                                  ]}
                                />
                              );
                            })}
                          </View>
                        )}

                        <SpatialNavigationNode orientation="horizontal">
                          <View style={styles.actionsRow}>
                            <FocusablePressable
                              text={isActive ? 'Active profile' : 'Set as active'}
                              onSelect={() => handleActivateProfile(user.id)}
                              disabled={isActive || pending === activateKey}
                            />
                          </View>
                        </SpatialNavigationNode>
                      </View>
                    );
                  })}
                </View>
              )}

              <View style={styles.adminInfoNoteMobile}>
                <Ionicons name="information-circle-outline" size={16} color={theme.colors.text.muted} />
                <Text style={styles.adminInfoNoteTextMobile}>
                  To rename, set PIN, or delete profiles, use the Admin Web UI
                </Text>
              </View>
            </View>
          </SpatialNavigationScrollView>
        </View>
      </FixedSafeAreaView>

    </SpatialNavigationRoot>
  );
}

const createStyles = (theme: NovaTheme, screenWidth = 1920, screenHeight = 1080) => {
  const isTV = Platform.isTV;
  const isCompact = theme.breakpoint === 'compact';
  const horizontalPadding = isTV ? theme.spacing.xl * 1.5 : isCompact ? theme.spacing.lg : theme.spacing['2xl'];

  // TV centered content (60% of screen width)
  const tvContentWidth = isTV ? screenWidth * 0.6 : screenWidth;
  const tvContentPadding = isTV ? theme.spacing.xl : horizontalPadding;

  // TV grid configuration
  const columnsCount = isTV ? 3 : 4; // Fewer columns since content area is narrower
  const gap = theme.spacing.xl;
  const availableWidth = isTV ? tvContentWidth - tvContentPadding * 2 : screenWidth - horizontalPadding * 2;
  const totalGapWidth = gap * (columnsCount - 1);
  const cardWidth = isTV ? Math.floor((availableWidth - totalGapWidth) / columnsCount) : 0;
  const cardHeight = isTV ? Math.round(cardWidth * 1.1) : 0; // Slightly taller than wide for profile cards
  const gridItemHeight = cardHeight + gap;

  const styles = StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: isTV ? 'transparent' : theme.colors.background.base,
    },
    // TV: Full-screen wrapper that centers content
    tvCenteredWrapper: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
    },
    // TV: Centered content container (60% width)
    tvContentContainer: {
      width: tvContentWidth,
      flex: 1,
      paddingHorizontal: tvContentPadding,
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
    // TV header row
    headerRow: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      marginBottom: theme.spacing.xl,
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
    section: {
      gap: theme.spacing.md,
      padding: theme.spacing.xl,
      borderRadius: theme.radius.lg,
      backgroundColor: theme.colors.background.base,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    sectionTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    sectionDescription: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    sectionHeaderRow: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      gap: theme.spacing.lg,
    },
    sectionHeaderContent: {
      flex: 1,
      flexShrink: 1,
      gap: theme.spacing.xs,
    },
    sectionHeaderAction: {
      flexShrink: 0,
    },
    input: {
      fontSize: isCompact ? theme.typography.body.lg.fontSize : 32,
      borderWidth: 2,
      borderColor: 'transparent',
      backgroundColor: theme.colors.background.surface,
      color: theme.colors.text.primary,
      borderRadius: theme.radius.md,
      paddingHorizontal: isCompact ? theme.spacing.md : theme.spacing.lg,
      paddingVertical: isCompact ? theme.spacing.sm : theme.spacing.md,
      minHeight: isCompact ? 44 : 60,
    },
    profileInput: {
      flex: 1,
    },
    inputFocused: {
      borderColor: theme.colors.accent.primary,
      borderWidth: 3,
      ...(isTV
        ? {
            shadowColor: theme.colors.accent.primary,
            shadowOpacity: 0.4,
            shadowOffset: { width: 0, height: 4 },
            shadowRadius: 12,
          }
        : null),
    },
    loadingRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
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
    profileList: {
      gap: theme.spacing.lg,
    },
    profileCard: {
      gap: theme.spacing.md,
      padding: theme.spacing.lg,
      borderRadius: theme.radius.lg,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.surface,
    },
    profileCardActive: {
      borderColor: theme.colors.accent.primary,
    },
    profileHeader: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.md,
    },
    profileMetaRow: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
    },
    profileMetaText: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
    },
    actionsRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.md,
    },
    activeBadge: {
      ...theme.typography.label.md,
      color: theme.colors.accent.primary,
    },
    mobileAvatar: {
      width: 44,
      height: 44,
      borderRadius: 22,
      backgroundColor: theme.colors.background.elevated,
      justifyContent: 'center',
      alignItems: 'center',
      position: 'relative',
    },
    mobilePinIndicator: {
      position: 'absolute',
      bottom: -2,
      right: -2,
      backgroundColor: theme.colors.accent.primary,
      paddingHorizontal: 4,
      paddingVertical: 1,
      borderRadius: 3,
      minWidth: 22,
      alignItems: 'center',
    },
    mobilePinIndicatorText: {
      fontSize: 8,
      fontWeight: '700',
      color: 'white',
      letterSpacing: 0.3,
    },
    mobileAvatarText: {
      fontSize: 20,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    mobileColorPickerRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm,
      marginVertical: theme.spacing.sm,
    },

    // TV Grid styles
    virtualizedGrid: {
      flex: 1,
    },
    gridRowContainer: {
      gap: gap,
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
    // Create profile card
    createCard: {
      borderStyle: 'dashed',
      borderColor: theme.colors.border.subtle,
    },
    createCardIcon: {
      fontSize: 64,
      fontWeight: '300',
      color: theme.colors.text.muted,
    },
    createCardText: {
      ...theme.typography.title.md,
      color: theme.colors.text.secondary,
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
      width: '80%',
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
    modalSubtitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.secondary,
      fontWeight: '400',
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
    modalInput: {
      fontSize: 28,
      borderWidth: 2,
      borderColor: 'transparent',
      backgroundColor: theme.colors.background.surface,
      color: theme.colors.text.primary,
      borderRadius: theme.radius.md,
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md,
      minHeight: 60,
    },
    modalInputFocused: {
      borderColor: theme.colors.accent.primary,
      borderWidth: 3,
      shadowColor: theme.colors.accent.primary,
      shadowOpacity: 0.4,
      shadowOffset: { width: 0, height: 4 },
      shadowRadius: 12,
    },
    modalActions: {
      flexDirection: 'row',
      justifyContent: 'center',
      gap: theme.spacing.lg,
      marginTop: theme.spacing.lg,
      marginBottom: theme.spacing.xl,
    },
    modalButton: {
      minWidth: 280,
      minHeight: 64,
      justifyContent: 'center',
      alignItems: 'center',
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing['2xl'],
      borderWidth: 3,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderColor: theme.colors.border.subtle,
    },
    modalButtonHorizontal: {
      flex: 1,
      minWidth: 0,
    },
    modalButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
    },
    modalButtonText: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    modalButtonTextFocused: {
      color: theme.colors.text.primary,
    },
    modalButtonsContainer: {
      gap: theme.spacing.md,
      alignItems: 'center',
    },
    // Profile actions modal styles
    profileModalHeader: {
      alignItems: 'center',
      gap: theme.spacing.lg,
      marginBottom: theme.spacing.lg,
    },
    profileModalAvatar: {
      width: 100,
      height: 100,
      borderRadius: 50,
      backgroundColor: theme.colors.background.surface,
      justifyContent: 'center',
      alignItems: 'center',
    },
    profileModalAvatarText: {
      fontSize: 48,
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
    // Color picker styles
    colorPickerSection: {
      marginBottom: theme.spacing.lg,
      gap: theme.spacing.md,
    },
    colorPickerLabel: {
      ...theme.typography.title.md,
      color: theme.colors.text.secondary,
      fontWeight: '400',
      textAlign: 'center',
    },
    colorPickerRow: {
      flexDirection: 'row',
      justifyContent: 'center',
      gap: theme.spacing.md,
      flexWrap: 'wrap',
    },
    colorSwatch: {
      width: 48,
      height: 48,
      borderRadius: 24,
      borderWidth: 3,
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
