import React, { useCallback, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Keyboard,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  View,
  type TextStyle,
  type ViewStyle,
} from 'react-native';
import Animated, { FadeIn, FadeOut } from 'react-native-reanimated';

import { useAuth } from '@/components/AuthContext';
import { useBackendSettings } from '@/components/BackendSettingsContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import { useTheme, type NovaTheme } from '@/theme';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  useLockSpatialNavigation,
} from '@/services/tv-navigation';

export default function LoginScreen() {
  const theme = useTheme();
  const styles = createStyles(theme);
  const { login, isLoading, error, clearError } = useAuth();
  const { backendUrl, setBackendUrl, refreshSettings } = useBackendSettings();

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [localError, setLocalError] = useState<string | null>(null);
  const [showServerConfig, setShowServerConfig] = useState(!backendUrl);
  const [serverUrl, setServerUrl] = useState(backendUrl?.replace(/\/api$/, '') || '');
  const [isSavingServer, setIsSavingServer] = useState(false);

  const usernameRef = useRef<TextInput | null>(null);
  const passwordRef = useRef<TextInput | null>(null);
  const serverUrlRef = useRef<TextInput | null>(null);

  const handleLogin = useCallback(async () => {
    Keyboard.dismiss();
    setLocalError(null);
    clearError();

    if (!username.trim()) {
      setLocalError('Username is required');
      return;
    }
    if (!password) {
      setLocalError('Password is required');
      return;
    }

    try {
      await login(username.trim(), password);
      // Refresh settings now that we're authenticated
      try {
        await refreshSettings();
      } catch (err) {
        console.warn('[Login] Failed to refresh settings after login:', err);
        // Don't block login if settings refresh fails
      }
      // Navigation will be handled by the layout detecting auth state change
    } catch (err) {
      // Error is already set in the auth context
    }
  }, [username, password, login, clearError, refreshSettings]);

  const handleSaveServer = useCallback(async () => {
    Keyboard.dismiss();
    setLocalError(null);

    if (!serverUrl.trim()) {
      setLocalError('Server URL is required');
      return;
    }

    setIsSavingServer(true);
    try {
      // Normalize URL: ensure /api suffix
      let normalizedUrl = serverUrl.trim();
      if (!normalizedUrl.endsWith('/api')) {
        normalizedUrl = normalizedUrl.replace(/\/$/, '') + '/api';
      }

      await setBackendUrl(normalizedUrl);
      setShowServerConfig(false);
    } catch (err) {
      setLocalError(err instanceof Error ? err.message : 'Failed to connect to server');
    } finally {
      setIsSavingServer(false);
    }
  }, [serverUrl, setBackendUrl]);

  const displayError = localError || error;

  // Server configuration content
  const serverConfigContent = (
    <View style={styles.container}>
      <View style={styles.card}>
        <View style={styles.header}>
          <Text style={styles.title}>strmr</Text>
          <Text style={styles.subtitle}>Configure Server</Text>
        </View>

        {displayError ? (
          <Animated.View entering={FadeIn} exiting={FadeOut} style={styles.errorContainer}>
            <Text style={styles.errorText}>{displayError}</Text>
          </Animated.View>
        ) : null}

        <View style={styles.form}>
          <LoginTextInput
            ref={serverUrlRef}
            label="Server URL"
            value={serverUrl}
            onChangeText={setServerUrl}
            placeholder="http://192.168.1.100:7777/api"
            autoCapitalize="none"
            autoCorrect={false}
            returnKeyType="done"
            onSubmitEditing={handleSaveServer}
            styles={styles}
            theme={theme}
          />

          {Platform.isTV ? (
            <DefaultFocus>
              <SpatialNavigationFocusableView onSelect={handleSaveServer}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <View style={[styles.button, isFocused && styles.buttonFocused]}>
                    {isSavingServer ? (
                      <ActivityIndicator size="small" color={theme.colors.text.primary} />
                    ) : (
                      <Text style={styles.buttonText}>Connect</Text>
                    )}
                  </View>
                )}
              </SpatialNavigationFocusableView>
            </DefaultFocus>
          ) : (
            <Pressable
              onPress={handleSaveServer}
              disabled={isSavingServer}
              style={({ pressed }) => [styles.button, pressed && styles.buttonPressed]}>
              {isSavingServer ? (
                <ActivityIndicator size="small" color={theme.colors.text.primary} />
              ) : (
                <Text style={styles.buttonText}>Connect</Text>
              )}
            </Pressable>
          )}
        </View>
      </View>
    </View>
  );

  // Login content
  const loginContent = (
    <View style={styles.container}>
      <View style={styles.card}>
        <View style={styles.header}>
          <Text style={styles.title}>strmr</Text>
          <Text style={styles.subtitle}>Sign in to your account</Text>
          {backendUrl ? (
            <Pressable onPress={() => setShowServerConfig(true)}>
              <Text style={styles.serverInfo} numberOfLines={1}>
                {backendUrl.replace(/\/api$/, '')} (change)
              </Text>
            </Pressable>
          ) : null}
        </View>

        {displayError ? (
          <Animated.View entering={FadeIn} exiting={FadeOut} style={styles.errorContainer}>
            <Text style={styles.errorText}>{displayError}</Text>
          </Animated.View>
        ) : null}

        <View style={styles.form}>
          <LoginTextInput
            ref={usernameRef}
            label="Username"
            value={username}
            onChangeText={setUsername}
            placeholder="Enter username"
            autoCapitalize="none"
            autoCorrect={false}
            autoComplete="off"
            textContentType="none"
            returnKeyType="next"
            onSubmitEditing={() => passwordRef.current?.focus()}
            styles={styles}
            theme={theme}
          />

          <LoginTextInput
            ref={passwordRef}
            label="Password"
            value={password}
            onChangeText={setPassword}
            placeholder="Enter password"
            secureTextEntry
            returnKeyType="done"
            onSubmitEditing={handleLogin}
            styles={styles}
            theme={theme}
          />

          {Platform.isTV ? (
            <DefaultFocus>
              <SpatialNavigationFocusableView onSelect={handleLogin}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <View style={[styles.button, isFocused && styles.buttonFocused]}>
                    {isLoading ? (
                      <ActivityIndicator size="small" color={theme.colors.text.primary} />
                    ) : (
                      <Text style={styles.buttonText}>Sign In</Text>
                    )}
                  </View>
                )}
              </SpatialNavigationFocusableView>
            </DefaultFocus>
          ) : (
            <Pressable
              onPress={handleLogin}
              disabled={isLoading}
              style={({ pressed }) => [styles.button, pressed && styles.buttonPressed]}>
              {isLoading ? (
                <ActivityIndicator size="small" color={theme.colors.text.primary} />
              ) : (
                <Text style={styles.buttonText}>Sign In</Text>
              )}
            </Pressable>
          )}

          {Platform.isTV ? (
            <SpatialNavigationFocusableView onSelect={() => setShowServerConfig(true)}>
              {({ isFocused }: { isFocused: boolean }) => (
                <View style={[styles.secondaryButton, isFocused && styles.buttonFocused]}>
                  <Text style={styles.secondaryButtonText}>Change Server</Text>
                </View>
              )}
            </SpatialNavigationFocusableView>
          ) : null}
        </View>
      </View>
    </View>
  );

  const content = showServerConfig ? serverConfigContent : loginContent;

  if (Platform.isTV) {
    return (
      <FixedSafeAreaView style={styles.safeArea}>
        <SpatialNavigationRoot>
          <SpatialNavigationNode orientation="vertical">{content}</SpatialNavigationNode>
        </SpatialNavigationRoot>
      </FixedSafeAreaView>
    );
  }

  return (
    <FixedSafeAreaView style={styles.safeArea}>
      <KeyboardAvoidingView
        style={styles.keyboardAvoid}
        behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
        keyboardVerticalOffset={Platform.OS === 'ios' ? 0 : 20}>
        <ScrollView
          contentContainerStyle={styles.scrollContent}
          keyboardShouldPersistTaps="handled"
          bounces={false}>
          {content}
        </ScrollView>
      </KeyboardAvoidingView>
    </FixedSafeAreaView>
  );
}

interface LoginTextInputProps {
  label: string;
  value: string;
  onChangeText: (text: string) => void;
  placeholder?: string;
  secureTextEntry?: boolean;
  autoCapitalize?: 'none' | 'sentences' | 'words' | 'characters';
  autoCorrect?: boolean;
  autoComplete?: 'off' | 'username' | 'password' | 'email';
  textContentType?: 'none' | 'username' | 'password' | 'emailAddress';
  returnKeyType?: 'done' | 'next';
  onSubmitEditing?: () => void;
  styles: ReturnType<typeof createStyles>;
  theme: NovaTheme;
}

const LoginTextInput = React.forwardRef<TextInput, LoginTextInputProps>(
  (
    {
      label,
      value,
      onChangeText,
      placeholder,
      secureTextEntry,
      autoCapitalize,
      autoCorrect,
      autoComplete,
      textContentType,
      returnKeyType,
      onSubmitEditing,
      styles,
      theme,
    },
    ref,
  ) => {
    const inputRef = useRef<TextInput | null>(null);
    const { lock, unlock } = useLockSpatialNavigation();

    React.useImperativeHandle(ref, () => inputRef.current as TextInput);

    const handleFocus = useCallback(() => {
      lock();
    }, [lock]);

    const handleBlur = useCallback(() => {
      unlock();
    }, [unlock]);

    if (Platform.isTV) {
      return (
        <View style={styles.inputContainer}>
          <Text style={styles.inputLabel}>{label}</Text>
          <SpatialNavigationFocusableView
            onSelect={() => inputRef.current?.focus()}
            onBlur={() => inputRef.current?.blur()}>
            {({ isFocused }: { isFocused: boolean }) => (
              <TextInput
                ref={inputRef}
                value={value}
                onChangeText={onChangeText}
                onFocus={handleFocus}
                onBlur={handleBlur}
                placeholder={placeholder}
                placeholderTextColor={theme.colors.text.muted}
                secureTextEntry={secureTextEntry}
                autoCapitalize={autoCapitalize}
                autoCorrect={autoCorrect}
                returnKeyType={returnKeyType}
                onSubmitEditing={onSubmitEditing}
                style={[styles.input, isFocused && styles.inputFocused]}
                editable={isFocused}
                showSoftInputOnFocus={true}
                underlineColorAndroid="transparent"
              />
            )}
          </SpatialNavigationFocusableView>
        </View>
      );
    }

    return (
      <View style={styles.inputContainer}>
        <Text style={styles.inputLabel}>{label}</Text>
        <TextInput
          ref={inputRef}
          value={value}
          onChangeText={onChangeText}
          placeholder={placeholder}
          placeholderTextColor={theme.colors.text.muted}
          secureTextEntry={secureTextEntry}
          autoCapitalize={autoCapitalize}
          autoCorrect={autoCorrect}
          autoComplete={autoComplete}
          textContentType={textContentType}
          returnKeyType={returnKeyType}
          onSubmitEditing={onSubmitEditing}
          style={styles.input}
        />
      </View>
    );
  },
);

LoginTextInput.displayName = 'LoginTextInput';

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    keyboardAvoid: {
      flex: 1,
    },
    scrollContent: {
      flexGrow: 1,
    },
    container: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      padding: 24,
    },
    card: {
      width: '100%',
      maxWidth: 400,
      backgroundColor: theme.colors.background.surface,
      borderRadius: 16,
      padding: 32,
      shadowColor: '#000',
      shadowOffset: { width: 0, height: 4 },
      shadowOpacity: 0.3,
      shadowRadius: 8,
      elevation: 8,
    },
    header: {
      alignItems: 'center',
      marginBottom: 24,
    },
    title: {
      fontSize: 32,
      fontWeight: '700',
      color: theme.colors.accent.primary,
      marginBottom: 8,
    },
    subtitle: {
      fontSize: 16,
      color: theme.colors.text.secondary,
    },
    serverInfo: {
      fontSize: 12,
      color: theme.colors.text.muted,
      marginTop: 8,
    },
    errorContainer: {
      backgroundColor: `${theme.colors.status.danger}20`,
      borderWidth: 1,
      borderColor: theme.colors.status.danger,
      borderRadius: 8,
      padding: 12,
      marginBottom: 16,
    },
    errorText: {
      color: theme.colors.status.danger,
      fontSize: 14,
      textAlign: 'center',
    },
    form: {
      gap: 16,
    },
    inputContainer: {
      marginBottom: 8,
    },
    inputLabel: {
      fontSize: 14,
      color: theme.colors.text.secondary,
      marginBottom: 8,
    },
    input: {
      backgroundColor: theme.colors.background.elevated,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      borderRadius: 8,
      padding: 14,
      fontSize: 16,
      color: theme.colors.text.primary,
    },
    inputFocused: {
      borderColor: theme.colors.accent.primary,
      borderWidth: 2,
    },
    button: {
      backgroundColor: theme.colors.accent.primary,
      borderRadius: 8,
      padding: 16,
      alignItems: 'center',
      marginTop: 8,
    },
    buttonPressed: {
      opacity: 0.8,
    },
    buttonFocused: {
      borderWidth: 3,
      borderColor: theme.colors.text.primary,
    },
    buttonText: {
      color: theme.colors.text.inverse,
      fontSize: 16,
      fontWeight: '600',
    },
    secondaryButton: {
      backgroundColor: 'transparent',
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      borderRadius: 8,
      padding: 12,
      alignItems: 'center',
      marginTop: 12,
    },
    secondaryButtonText: {
      color: theme.colors.text.secondary,
      fontSize: 14,
      fontWeight: '500',
    },
  });
