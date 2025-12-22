import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
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
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';

import FocusablePressable from '@/components/FocusablePressable';
import { useUserProfiles } from '@/components/UserProfilesContext';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';

const createStyles = (theme: NovaTheme, isLargeScreen: boolean) => {
  // Use larger sizes for TV and wide screens (tablets, foldables)
  const useLargeSizing = Platform.isTV || isLargeScreen;
  return StyleSheet.create({
    modalOverlay: {
      flex: 1,
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      justifyContent: 'center',
      alignItems: 'center',
    },
    modalContainer: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: 16,
      padding: 32,
      minWidth: useLargeSizing ? 480 : 320,
      maxWidth: useLargeSizing ? 600 : 400,
      alignItems: 'center',
    },
    header: {
      alignItems: 'center',
      marginBottom: 24,
    },
    avatar: {
      width: 64,
      height: 64,
      borderRadius: 32,
      backgroundColor: theme.colors.accent.primary,
      justifyContent: 'center',
      alignItems: 'center',
      marginBottom: 16,
    },
    avatarText: {
      fontSize: 28,
      fontWeight: '600',
      color: 'white',
    },
    modalTitle: {
      fontSize: useLargeSizing ? 28 : 22,
      fontWeight: '700',
      color: theme.colors.text.primary,
      marginBottom: 8,
    },
    modalSubtitle: {
      fontSize: useLargeSizing ? 18 : 14,
      color: theme.colors.text.secondary,
      textAlign: 'center',
    },
    errorContainer: {
      backgroundColor: 'rgba(239, 68, 68, 0.15)',
      borderRadius: 8,
      padding: 12,
      marginBottom: 16,
      width: '100%',
    },
    errorText: {
      color: '#EF4444',
      fontSize: useLargeSizing ? 16 : 14,
      textAlign: 'center',
    },
    pinInput: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: 12,
      paddingHorizontal: 20,
      paddingVertical: useLargeSizing ? 16 : 14,
      fontSize: useLargeSizing ? 24 : 18,
      color: theme.colors.text.primary,
      textAlign: 'center',
      minWidth: useLargeSizing ? 280 : 200,
      marginBottom: 24,
      borderWidth: 2,
      borderColor: 'transparent',
      fontFamily: Platform.OS === 'ios' ? 'System' : 'sans-serif',
    },
    pinInputFocused: {
      borderColor: theme.colors.accent.primary,
    },
    pinInputError: {
      borderColor: '#EF4444',
    },
    actions: {
      flexDirection: 'row',
      justifyContent: 'center',
      gap: 16,
    },
    button: {
      paddingHorizontal: 24,
      paddingVertical: useLargeSizing ? 14 : 12,
      borderRadius: 8,
      backgroundColor: theme.colors.background.elevated,
      minWidth: useLargeSizing ? 140 : 100,
      alignItems: 'center',
    },
    buttonPrimary: {
      backgroundColor: theme.colors.accent.primary,
    },
    buttonFocused: {
      backgroundColor: theme.colors.accent.primary,
      transform: [{ scale: 1.05 }],
    },
    buttonPrimaryFocused: {
      backgroundColor: '#2563eb',
    },
    buttonText: {
      fontSize: useLargeSizing ? 18 : 16,
      fontWeight: '600',
      color: theme.colors.text.primary,
    },
    buttonTextFocused: {
      color: 'white',
    },
  });
};

export const PinEntryModal: React.FC = () => {
  const theme = useTheme();
  const { width: screenWidth } = useWindowDimensions();
  const isLargeScreen = screenWidth >= 600;
  const styles = useMemo(() => createStyles(theme, isLargeScreen), [theme, isLargeScreen]);
  const {
    users,
    pendingPinUserId,
    selectUserWithPin,
    cancelPinEntry,
    isInitialPinCheck,
  } = useUserProfiles();

  // Check if cancel should be allowed - not allowed if all users have PINs on initial load
  const allUsersHavePins = users.length > 0 && users.every((u) => u.hasPin);
  const canCancel = !isInitialPinCheck || !allUsersHavePins;

  const [pin, setPin] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const inputRef = useRef<TextInput>(null);
  const tempPinRef = useRef('');
  const autoSubmitTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const pendingUser = pendingPinUserId ? users.find((u) => u.id === pendingPinUserId) : null;
  const isVisible = !!pendingPinUserId && !!pendingUser;

  // Reset state when modal opens/closes
  useEffect(() => {
    if (isVisible) {
      setPin('');
      tempPinRef.current = '';
      setError(null);
      setLoading(false);
    }
    return () => {
      if (autoSubmitTimerRef.current) {
        clearTimeout(autoSubmitTimerRef.current);
        autoSubmitTimerRef.current = null;
      }
    };
  }, [isVisible]);

  const handleChangeText = useCallback(
    (text: string) => {
      // Clear any pending auto-submit
      if (autoSubmitTimerRef.current) {
        clearTimeout(autoSubmitTimerRef.current);
        autoSubmitTimerRef.current = null;
      }

      if (Platform.isTV) {
        tempPinRef.current = text;
      } else {
        setPin(text);
        // Auto-submit after 800ms pause if PIN is at least 4 chars (non-TV only)
        if (text.trim().length >= 4 && !loading) {
          autoSubmitTimerRef.current = setTimeout(() => {
            if (pendingPinUserId) {
              void selectUserWithPin(pendingPinUserId, text.trim()).catch((err) => {
                setError(err instanceof Error ? err.message : 'Invalid PIN');
                setPin('');
              });
            }
          }, 800);
        }
      }
      setError(null);
    },
    [loading, pendingPinUserId, selectUserWithPin]
  );

  const handleFocus = useCallback(() => {
    if (Platform.isTV) {
      tempPinRef.current = pin;
    }
  }, [pin]);

  const handleBlur = useCallback(() => {
    if (Platform.isTV) {
      const pinValue = tempPinRef.current;
      setPin(pinValue);
      // Auto-submit on tvOS when keyboard closes if PIN is at least 4 chars
      if (pinValue.trim().length >= 4 && !loading && pendingPinUserId) {
        void selectUserWithPin(pendingPinUserId, pinValue.trim()).catch((err) => {
          setError(err instanceof Error ? err.message : 'Invalid PIN');
          setPin('');
          tempPinRef.current = '';
        });
      }
    }
  }, [loading, pendingPinUserId, selectUserWithPin]);

  const handleSubmit = useCallback(async () => {
    const pinValue = Platform.isTV ? tempPinRef.current : pin;
    if (!pinValue.trim() || !pendingPinUserId) {
      setError('Please enter a PIN');
      return;
    }

    setLoading(true);
    setError(null);

    try {
      await selectUserWithPin(pendingPinUserId, pinValue.trim());
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Invalid PIN');
      setPin('');
      tempPinRef.current = '';
    } finally {
      setLoading(false);
    }
  }, [pin, pendingPinUserId, selectUserWithPin]);

  const handleCancel = useCallback(() => {
    Keyboard.dismiss();
    setPin('');
    tempPinRef.current = '';
    setError(null);
    cancelPinEntry();
  }, [cancelPinEntry]);

  if (!isVisible) {
    return null;
  }

  return (
    <Modal
      visible={isVisible}
      transparent={true}
      animationType="fade"
      onRequestClose={handleCancel}>
      <SpatialNavigationRoot isActive={isVisible}>
        <View style={styles.modalOverlay}>
          <View style={styles.modalContainer}>
            <View style={styles.header}>
              {pendingUser && (
                <View
                  style={[
                    styles.avatar,
                    pendingUser.color && { backgroundColor: pendingUser.color },
                  ]}>
                  <Text style={styles.avatarText}>
                    {pendingUser.name.charAt(0).toUpperCase()}
                  </Text>
                </View>
              )}
              <Text style={styles.modalTitle}>Enter PIN</Text>
              <Text style={styles.modalSubtitle}>
                {isInitialPinCheck
                  ? `Enter PIN to continue as ${pendingUser?.name}`
                  : `${pendingUser?.name} is protected with a PIN`}
              </Text>
            </View>

            {error && (
              <View style={styles.errorContainer}>
                <Text style={styles.errorText}>{error}</Text>
              </View>
            )}

            <SpatialNavigationNode orientation="vertical">
              <DefaultFocus>
                <SpatialNavigationFocusableView
                  focusKey="pin-input"
                  onSelect={() => inputRef.current?.focus()}
                  onBlur={() => inputRef.current?.blur()}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <Pressable tvParallaxProperties={{ enabled: false }}>
                    <TextInput
                      ref={inputRef}
                      {...(Platform.isTV ? { defaultValue: pin } : { value: pin })}
                      onChangeText={handleChangeText}
                      onFocus={handleFocus}
                      onBlur={handleBlur}
                      placeholder="Enter PIN"
                      placeholderTextColor={theme.colors.text.muted}
                      style={[
                        styles.pinInput,
                        isFocused && styles.pinInputFocused,
                        error && styles.pinInputError,
                      ]}
                      secureTextEntry={!Platform.isTV}
                      autoCapitalize="none"
                      autoCorrect={false}
                      autoComplete="off"
                      textContentType="none"
                      keyboardType="numeric"
                      maxLength={16}
                      returnKeyType="done"
                      onSubmitEditing={() => {
                        const pinValue = Platform.isTV ? tempPinRef.current : pin;
                        if (pinValue.trim()) {
                          void handleSubmit();
                        }
                      }}
                      editable={Platform.isTV ? isFocused : true}
                      {...(Platform.OS === 'ios' &&
                        Platform.isTV && {
                          keyboardAppearance: 'dark',
                        })}
                    />
                  </Pressable>
                )}
              </SpatialNavigationFocusableView>
              </DefaultFocus>

              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.actions}>
                  {canCancel ? (
                    <FocusablePressable
                      focusKey="pin-cancel"
                      text="Cancel"
                      onSelect={handleCancel}
                      style={styles.button}
                      focusedStyle={styles.buttonFocused}
                      textStyle={styles.buttonText}
                      focusedTextStyle={styles.buttonTextFocused}
                    />
                  ) : null}
                  <FocusablePressable
                    focusKey="pin-submit"
                    text={loading ? 'Verifying...' : 'Submit'}
                    onSelect={handleSubmit}
                    disabled={loading}
                    style={[styles.button, styles.buttonPrimary]}
                    focusedStyle={[styles.buttonFocused, styles.buttonPrimaryFocused]}
                    textStyle={styles.buttonText}
                    focusedTextStyle={styles.buttonTextFocused}
                  />
                </View>
              </SpatialNavigationNode>
            </SpatialNavigationNode>
          </View>
        </View>
      </SpatialNavigationRoot>
    </Modal>
  );
};

export default PinEntryModal;
