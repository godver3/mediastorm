import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Keyboard, Modal, Platform, Pressable, StyleSheet, Text, TextInput, View } from 'react-native';
import { BlurView } from 'expo-blur';

import FocusablePressable from '@/components/FocusablePressable';
import { useUserProfiles } from '@/components/UserProfilesContext';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { responsiveSize } from '@/theme/tokens/tvScale';

const createStyles = (theme: NovaTheme) => {
  const avatarSize = responsiveSize(100, 64);

  return StyleSheet.create({
    blurOverlay: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
    },
    darkOverlay: {
      ...StyleSheet.absoluteFillObject,
      backgroundColor: 'rgba(0, 0, 0, 0.6)',
    },
    modalContainer: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: responsiveSize(24, 16),
      padding: responsiveSize(56, 32),
      minWidth: responsiveSize(640, 320),
      maxWidth: responsiveSize(800, 400),
      alignItems: 'center',
    },
    header: {
      alignItems: 'center',
      marginBottom: responsiveSize(36, 24),
    },
    avatar: {
      width: avatarSize,
      height: avatarSize,
      borderRadius: avatarSize / 2,
      backgroundColor: theme.colors.accent.primary,
      justifyContent: 'center',
      alignItems: 'center',
      marginBottom: responsiveSize(24, 16),
    },
    avatarText: {
      fontSize: responsiveSize(44, 28),
      fontWeight: '600',
      color: 'white',
    },
    modalTitle: {
      fontSize: responsiveSize(40, 22),
      fontWeight: '700',
      color: theme.colors.text.primary,
      marginBottom: responsiveSize(12, 8),
    },
    modalSubtitle: {
      fontSize: responsiveSize(24, 14),
      color: theme.colors.text.secondary,
      textAlign: 'center',
    },
    errorContainer: {
      backgroundColor: 'rgba(239, 68, 68, 0.15)',
      borderRadius: responsiveSize(12, 8),
      padding: responsiveSize(18, 12),
      marginBottom: responsiveSize(24, 16),
      width: '100%',
    },
    errorText: {
      color: '#EF4444',
      fontSize: responsiveSize(22, 14),
      textAlign: 'center',
    },
    pinInputWrapper: {
      marginBottom: responsiveSize(36, 24),
    },
    pinInput: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: responsiveSize(12, 12),
      paddingHorizontal: responsiveSize(24, 20),
      paddingVertical: responsiveSize(14, 14),
      fontSize: responsiveSize(24, 18),
      color: theme.colors.text.primary,
      textAlign: 'center',
      minWidth: responsiveSize(280, 200),
      borderWidth: responsiveSize(2, 2),
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
      gap: responsiveSize(24, 16),
    },
    button: {
      backgroundColor: theme.colors.overlay.button,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    buttonPrimary: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    buttonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    buttonPrimaryFocused: {
      borderColor: theme.colors.text.inverse,
    },
    buttonText: {
      color: theme.colors.text.primary,
    },
    buttonPrimaryText: {
      color: theme.colors.text.inverse,
    },
    buttonTextFocused: {
      color: theme.colors.text.inverse,
    },
  });
};

export const PinEntryModal: React.FC = () => {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const { users, pendingPinUserId, selectUserWithPin, cancelPinEntry, isInitialPinCheck } = useUserProfiles();

  // Check if cancel should be allowed - not allowed if all users have PINs on initial load
  const allUsersHavePins = users.length > 0 && users.every((u) => u.hasPin);
  const canCancel = !isInitialPinCheck || !allUsersHavePins;

  const [pin, setPin] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const inputRef = useRef<TextInput>(null);
  const tempPinRef = useRef('');
  const autoSubmitTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const verifyingRef = useRef(false);

  const pendingUser = pendingPinUserId ? users.find((u) => u.id === pendingPinUserId) : null;
  const isVisible = !!pendingPinUserId && !!pendingUser;

  // Reset state when modal opens/closes, and auto-focus the input
  useEffect(() => {
    if (isVisible) {
      setPin('');
      tempPinRef.current = '';
      setError(null);
      setLoading(false);
      verifyingRef.current = false;
      // Auto-focus the PIN input after a short delay to let the modal animate in
      if (!Platform.isTV) {
        const timer = setTimeout(() => inputRef.current?.focus(), 100);
        return () => clearTimeout(timer);
      }
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
        // Immediately try to verify on each keystroke once >= 4 chars.
        // If correct, auto-accepts instantly. If wrong, silently continues.
        const pinValue = text.trim();
        if (pinValue.length >= 4 && !loading && pendingPinUserId && !verifyingRef.current) {
          verifyingRef.current = true;
          void (async () => {
            try {
              await selectUserWithPin(pendingPinUserId, pinValue);
              // Success — modal will close via pendingPinUserId clearing
            } catch {
              // Wrong PIN — silently allow user to keep typing
              verifyingRef.current = false;
            }
          })();
        }
      }
      setError(null);
    },
    [loading, pendingPinUserId, selectUserWithPin],
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
    <Modal visible={isVisible} transparent={true} animationType="fade" onRequestClose={handleCancel}>
      <BlurView intensity={40} tint="dark" style={styles.blurOverlay}>
        <View style={styles.darkOverlay} pointerEvents="none" />
        <View style={styles.modalContainer}>
          <View style={styles.header}>
            {pendingUser && (
              <View style={[styles.avatar, pendingUser.color && { backgroundColor: pendingUser.color }]}>
                <Text style={styles.avatarText}>{pendingUser.name.charAt(0).toUpperCase()}</Text>
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

          <Pressable
            onPress={() => inputRef.current?.focus()}
            hasTVPreferredFocus={true}
            tvParallaxProperties={{ enabled: false }}
            style={({ focused }) => [styles.pinInputWrapper, focused && { opacity: 1 }]}>
            {({ focused }) => (
              <TextInput
                ref={inputRef}
                {...(Platform.isTV ? { defaultValue: pin } : { value: pin })}
                onChangeText={handleChangeText}
                onFocus={handleFocus}
                onBlur={handleBlur}
                placeholder="Enter PIN"
                placeholderTextColor={theme.colors.text.muted}
                style={[styles.pinInput, focused && styles.pinInputFocused, error && styles.pinInputError]}
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
                editable
                {...(Platform.OS === 'ios' &&
                  Platform.isTV && {
                    keyboardAppearance: 'dark',
                  })}
              />
            )}
          </Pressable>

          <View style={styles.actions}>
            {canCancel ? (
              <FocusablePressable
                text="Cancel"
                onSelect={handleCancel}
                style={styles.button}
                focusedStyle={styles.buttonFocused}
                textStyle={styles.buttonText}
                focusedTextStyle={styles.buttonTextFocused}
              />
            ) : null}
            <FocusablePressable
              text={loading ? 'Verifying...' : 'Submit'}
              onSelect={handleSubmit}
              disabled={loading}
              style={[styles.button, styles.buttonPrimary]}
              focusedStyle={[styles.buttonFocused, styles.buttonPrimaryFocused]}
              textStyle={styles.buttonPrimaryText}
              focusedTextStyle={styles.buttonTextFocused}
            />
          </View>
        </View>
      </BlurView>
    </Modal>
  );
};

export default PinEntryModal;
