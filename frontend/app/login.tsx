import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Animated as RNAnimated,
  Easing,
  Keyboard,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
} from 'react-native';
import { Image } from '@/components/Image';
import Animated, { useAnimatedStyle, useSharedValue, withTiming } from 'react-native-reanimated';
import { LinearGradient } from 'expo-linear-gradient';

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
} from '@/services/tv-navigation';
import { useToast } from '@/components/ToastContext';

// Animation constant from strmr-loading.tsx
const CIRCLE_PULSE_DURATION_MS = 3200;

export default function LoginScreen() {
  const theme = useTheme();
  const isTV = Platform.isTV;
  const styles = createStyles(theme, isTV);
  const { login, isLoading, error, clearError } = useAuth();
  const { backendUrl, setBackendUrl, refreshSettings } = useBackendSettings();
  const { showToast } = useToast();

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [showServerConfig, setShowServerConfig] = useState(!backendUrl);
  const [serverUrl, setServerUrl] = useState(backendUrl?.replace(/\/api$/, '') || '');
  const [isSavingServer, setIsSavingServer] = useState(false);
  const tvPasswordFocused = useSharedValue(0);

  const usernameRef = useRef<TextInput | null>(null);
  const passwordRef = useRef<TextInput | null>(null);
  const serverUrlRef = useRef<TextInput | null>(null);
  const lowerFieldFocused = useRef(false);

  // Track keyboard visibility for animations
  const [keyboardVisible, setKeyboardVisible] = useState(false);
  const keyboardHideTimeout = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    const showEvent = Platform.OS === 'ios' ? 'keyboardWillShow' : 'keyboardDidShow';
    const hideEvent = Platform.OS === 'ios' ? 'keyboardWillHide' : 'keyboardDidHide';

    const showSub = Keyboard.addListener(showEvent, () => {
      if (keyboardHideTimeout.current) {
        clearTimeout(keyboardHideTimeout.current);
        keyboardHideTimeout.current = null;
      }
      setKeyboardVisible(true);
      // TV: trigger animation for lower fields (password, server URL)
      if (Platform.isTV && lowerFieldFocused.current) {
        tvPasswordFocused.value = 1;
      }
    });

    const hideSub = Keyboard.addListener(hideEvent, () => {
      keyboardHideTimeout.current = setTimeout(() => {
        setKeyboardVisible(false);
        // TV: revert animation when keyboard hides
        if (Platform.isTV) {
          tvPasswordFocused.value = 0;
        }
      }, 100);
    });

    return () => {
      showSub.remove();
      hideSub.remove();
      if (keyboardHideTimeout.current) {
        clearTimeout(keyboardHideTimeout.current);
      }
    };
  }, [tvPasswordFocused]);

  // Mobile: shift content up when keyboard is visible
  const KEYBOARD_OFFSET = 150;
  const animatedContainerStyle = useAnimatedStyle(() => ({
    transform: [
      {
        translateY: withTiming(keyboardVisible ? -KEYBOARD_OFFSET : 0, {
          duration: 250,
        }),
      },
    ],
  }));

  // TV: shift content up when keyboard is shown
  const TV_PASSWORD_OFFSET = 120;
  const tvAnimatedStyle = useAnimatedStyle(() => ({
    transform: [
      {
        translateY: withTiming(tvPasswordFocused.value ? -TV_PASSWORD_OFFSET : 0, {
          duration: 250,
        }),
      },
    ],
  }));

  // Show auth errors as toasts
  useEffect(() => {
    if (error) {
      showToast(error, { tone: 'danger' });
      clearError();
    }
  }, [error, showToast, clearError]);

  const handleLogin = useCallback(async () => {
    Keyboard.dismiss();
    clearError();

    if (!username.trim()) {
      showToast('Username is required', { tone: 'danger' });
      return;
    }
    if (!password) {
      showToast('Password is required', { tone: 'danger' });
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
      // Error is already set in the auth context and shown via useEffect
    }
  }, [username, password, login, clearError, refreshSettings, showToast]);

  const handleSaveServer = useCallback(async () => {
    Keyboard.dismiss();

    if (!serverUrl.trim()) {
      showToast('Server URL is required', { tone: 'danger' });
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
      showToast(err instanceof Error ? err.message : 'Failed to connect to server', { tone: 'danger' });
    } finally {
      setIsSavingServer(false);
    }
  }, [serverUrl, setBackendUrl, showToast]);

  // Temp refs for uncontrolled TV inputs
  const tempUsernameRef = useRef(username);
  const tempPasswordRef = useRef(password);
  const tempServerUrlRef = useRef(serverUrl);

  // Background animation state
  const circlePulse = useRef(new RNAnimated.Value(0)).current;

  // Background glow animation (runs on both TV and mobile)
  useEffect(() => {
    circlePulse.setValue(0);
    const loop = RNAnimated.loop(
      RNAnimated.sequence([
        RNAnimated.timing(circlePulse, {
          toValue: 1,
          duration: CIRCLE_PULSE_DURATION_MS,
          easing: Easing.inOut(Easing.quad),
          useNativeDriver: true,
        }),
        RNAnimated.timing(circlePulse, {
          toValue: 0,
          duration: CIRCLE_PULSE_DURATION_MS,
          easing: Easing.inOut(Easing.quad),
          useNativeDriver: true,
        }),
      ]),
    );
    loop.start();
    return () => loop.stop();
  }, [circlePulse]);

  // Don't lock spatial navigation on login - let user navigate freely between fields
  // This is simpler UX than requiring keyboard dismissal between each field
  const handleUsernameFocus = useCallback(() => {
    // Don't lock - allow D-pad navigation while keyboard is up
  }, []);
  const handleUsernameBlur = useCallback(() => {
    setUsername(tempUsernameRef.current);
  }, []);

  const handlePasswordFocus = useCallback(() => {
    lowerFieldFocused.current = true;
  }, []);
  const handlePasswordBlur = useCallback(() => {
    lowerFieldFocused.current = false;
    setPassword(tempPasswordRef.current);
  }, []);

  const handleServerUrlFocus = useCallback(() => {
    lowerFieldFocused.current = true;
  }, []);
  const handleServerUrlBlur = useCallback(() => {
    lowerFieldFocused.current = false;
    setServerUrl(tempServerUrlRef.current);
  }, []);

  // TV-specific render
  if (Platform.isTV) {
    return (
      <SpatialNavigationRoot isActive={true}>
        <FixedSafeAreaView style={styles.safeArea}>
          {/* Animated background */}
          <View style={StyleSheet.absoluteFill}>
            <RNAnimated.View pointerEvents="none" style={[tvBgStyles.gradientLayer, { opacity: 1 }]}>
              <LinearGradient
                colors={['#2a1245', '#3d1a5c', theme.colors.background.base]}
                start={{ x: 0, y: 0 }}
                end={{ x: 1, y: 0.85 }}
                style={StyleSheet.absoluteFill}
              />
            </RNAnimated.View>
            <RNAnimated.View pointerEvents="none" style={[tvBgStyles.gradientLayer, { opacity: 0.75 }]}>
              <LinearGradient
                colors={['rgba(232, 238, 255, 0.55)', 'rgba(40, 44, 54, 0.08)', 'rgba(210, 222, 255, 0.32)']}
                start={{ x: 0, y: 0 }}
                end={{ x: 0.95, y: 1 }}
                style={StyleSheet.absoluteFill}
              />
            </RNAnimated.View>
            {/* Center glow and arc effects */}
            <View style={tvBgStyles.center}>
              <RNAnimated.View
                pointerEvents="none"
                style={[
                  tvBgStyles.radialBlur,
                  {
                    transform: [
                      {
                        scale: circlePulse.interpolate({
                          inputRange: [0, 1],
                          outputRange: [1.01, 1.08],
                        }),
                      },
                    ],
                    opacity: circlePulse.interpolate({
                      inputRange: [0, 1],
                      outputRange: [0.2, 0.4],
                    }),
                  },
                ]}
              >
                <LinearGradient
                  colors={[`${theme.colors.accent.primary}30`, `${theme.colors.accent.primary}00`]}
                  start={{ x: 0.5, y: 0 }}
                  end={{ x: 0.5, y: 1 }}
                  style={StyleSheet.absoluteFill}
                />
                <LinearGradient
                  colors={[`${theme.colors.accent.primary}20`, `${theme.colors.accent.primary}00`]}
                  start={{ x: 0, y: 0.5 }}
                  end={{ x: 1, y: 0.5 }}
                  style={[StyleSheet.absoluteFill, { transform: [{ rotate: '45deg' }] }]}
                />
              </RNAnimated.View>
            </View>
          </View>
          {/* Login card overlay */}
          <Animated.View style={[styles.container, tvAnimatedStyle]}>
            <View style={styles.card}>
              <View style={styles.header}>
                {backendUrl ? (
                  <Image
                    source={{ uri: `${backendUrl}/static/app-logo-wide.png` }}
                    style={styles.logoImage}
                    contentFit="contain"
                  />
                ) : (
                  <Text style={styles.title}>strmr</Text>
                )}
                <Text style={styles.subtitle}>{showServerConfig ? 'Configure Server' : 'Sign in to your account'}</Text>
                {!showServerConfig && backendUrl ? (
                  <Text style={styles.serverInfo} numberOfLines={1}>
                    {backendUrl.replace(/\/api$/, '')}
                  </Text>
                ) : null}
              </View>

              <SpatialNavigationNode orientation="vertical">
                {showServerConfig ? (
                  <View style={styles.formContainer}>
                    <DefaultFocus>
                      <SpatialNavigationFocusableView
                        focusKey="server-url"
                        onSelect={() => serverUrlRef.current?.focus()}
                        onBlur={() => serverUrlRef.current?.blur()}
                      >
                        {({ isFocused }: { isFocused: boolean }) => (
                          <Pressable tvParallaxProperties={{ enabled: false }}>
                            <View style={styles.inputContainer}>
                              <Text style={styles.inputLabel}>Server URL</Text>
                              <TextInput
                                ref={serverUrlRef}
                                defaultValue={serverUrl}
                                onChangeText={(text) => {
                                  tempServerUrlRef.current = text;
                                }}
                                onFocus={handleServerUrlFocus}
                                onBlur={handleServerUrlBlur}
                                placeholder="http://192.168.1.100:7777"
                                placeholderTextColor={theme.colors.text.muted}
                                autoCapitalize="none"
                                autoCorrect={false}
                                autoComplete="off"
                                textContentType="none"
                                returnKeyType="done"
                                onSubmitEditing={Keyboard.dismiss}
                                style={[styles.input, isFocused && styles.inputFocused]}
                                underlineColorAndroid="transparent"
                                importantForAutofill="no"
                                disableFullscreenUI={true}
                                caretHidden={true}
                              />
                            </View>
                          </Pressable>
                        )}
                      </SpatialNavigationFocusableView>
                    </DefaultFocus>

                    <FocusablePressable
                      focusKey="server-connect"
                      text="Connect"
                      onSelect={handleSaveServer}
                      loading={isSavingServer}
                      style={styles.tvButton}
                      focusedStyle={styles.tvButtonFocused}
                      textStyle={styles.tvButtonText}
                      focusedTextStyle={styles.tvButtonTextFocused}
                    />
                  </View>
                ) : (
                  <View style={styles.formContainer}>
                    <DefaultFocus>
                      <SpatialNavigationFocusableView
                        focusKey="login-username"
                        onSelect={() => usernameRef.current?.focus()}
                        onBlur={() => usernameRef.current?.blur()}
                      >
                        {({ isFocused }: { isFocused: boolean }) => (
                          <Pressable tvParallaxProperties={{ enabled: false }}>
                            <View style={styles.inputContainer}>
                              <Text style={styles.inputLabel}>Username</Text>
                              <TextInput
                                ref={usernameRef}
                                defaultValue={username}
                                onChangeText={(text) => {
                                  tempUsernameRef.current = text;
                                }}
                                onFocus={handleUsernameFocus}
                                onBlur={handleUsernameBlur}
                                placeholder="Enter username"
                                placeholderTextColor="#aaaaaa"
                                autoCapitalize="none"
                                autoCorrect={false}
                                autoComplete="off"
                                textContentType="none"
                                returnKeyType="next"
                                onSubmitEditing={() => passwordRef.current?.focus()}
                                style={[styles.input, isFocused && styles.inputFocused]}
                                underlineColorAndroid="transparent"
                                importantForAutofill="no"
                                disableFullscreenUI={true}
                                caretHidden={true}
                              />
                            </View>
                          </Pressable>
                        )}
                      </SpatialNavigationFocusableView>
                    </DefaultFocus>

                    <SpatialNavigationFocusableView
                      focusKey="login-password"
                      onSelect={() => passwordRef.current?.focus()}
                      onBlur={() => passwordRef.current?.blur()}
                    >
                      {({ isFocused }: { isFocused: boolean }) => (
                        <Pressable tvParallaxProperties={{ enabled: false }}>
                          <View style={styles.inputContainer}>
                            <Text style={styles.inputLabel}>Password</Text>
                            <TextInput
                              ref={passwordRef}
                              defaultValue={password}
                              onChangeText={(text) => {
                                tempPasswordRef.current = text;
                              }}
                              onFocus={handlePasswordFocus}
                              onBlur={handlePasswordBlur}
                              placeholder="Enter password"
                              placeholderTextColor="#aaaaaa"
                              secureTextEntry
                              autoComplete="off"
                              textContentType="none"
                              returnKeyType="done"
                              onSubmitEditing={Keyboard.dismiss}
                              style={[styles.input, isFocused && styles.inputFocused]}
                              underlineColorAndroid="transparent"
                              importantForAutofill="no"
                              disableFullscreenUI={true}
                              caretHidden={true}
                            />
                          </View>
                        </Pressable>
                      )}
                    </SpatialNavigationFocusableView>

                    <FocusablePressable
                      focusKey="login-submit"
                      text="Sign In"
                      onSelect={handleLogin}
                      loading={isLoading}
                      style={styles.tvButton}
                      focusedStyle={styles.tvButtonFocused}
                      textStyle={styles.tvButtonText}
                      focusedTextStyle={styles.tvButtonTextFocused}
                    />

                    <FocusablePressable
                      focusKey="login-change-server"
                      text="Change Server"
                      onSelect={() => setShowServerConfig(true)}
                      style={styles.tvSecondaryButton}
                      focusedStyle={styles.tvSecondaryButtonFocused}
                      textStyle={styles.tvButtonText}
                      focusedTextStyle={styles.tvButtonTextFocused}
                    />
                  </View>
                )}
              </SpatialNavigationNode>
            </View>
          </Animated.View>
        </FixedSafeAreaView>
      </SpatialNavigationRoot>
    );
  }

  // Mobile render
  const serverConfigContent = (
    <View style={styles.container}>
      <View style={styles.card}>
        <View style={styles.header}>
          {backendUrl ? (
            <Image
              source={{ uri: `${backendUrl}/static/app-logo-wide.png` }}
              style={styles.logoImage}
              contentFit="contain"
            />
          ) : (
            <Text style={styles.title}>strmr</Text>
          )}
          <Text style={styles.subtitle}>Configure Server</Text>
        </View>

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

          <Pressable
            onPress={handleSaveServer}
            disabled={isSavingServer}
            style={({ pressed }) => [styles.button, pressed && styles.buttonPressed]}
          >
            {isSavingServer ? (
              <ActivityIndicator size="small" color={theme.colors.text.primary} />
            ) : (
              <Text style={styles.buttonText}>Connect</Text>
            )}
          </Pressable>
        </View>
      </View>
    </View>
  );

  const loginContent = (
    <View style={styles.container}>
      <View style={styles.card}>
        <View style={styles.header}>
          {backendUrl ? (
            <Image
              source={{ uri: `${backendUrl}/static/app-logo-wide.png` }}
              style={styles.logoImage}
              contentFit="contain"
            />
          ) : (
            <Text style={styles.title}>strmr</Text>
          )}
          <Text style={styles.subtitle}>Sign in to your account</Text>
          {backendUrl ? (
            <Pressable onPress={() => setShowServerConfig(true)}>
              <Text style={styles.serverInfo} numberOfLines={1}>
                {backendUrl.replace(/\/api$/, '')} (change)
              </Text>
            </Pressable>
          ) : null}
        </View>

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
            autoComplete="off"
            textContentType="oneTimeCode"
            returnKeyType="done"
            onSubmitEditing={handleLogin}
            styles={styles}
            theme={theme}
          />

          <Pressable
            onPress={handleLogin}
            disabled={isLoading}
            style={({ pressed }) => [styles.button, pressed && styles.buttonPressed]}
          >
            {isLoading ? (
              <ActivityIndicator size="small" color={theme.colors.text.primary} />
            ) : (
              <Text style={styles.buttonText}>Sign In</Text>
            )}
          </Pressable>
        </View>
      </View>
    </View>
  );

  const content = showServerConfig ? serverConfigContent : loginContent;

  return (
    <FixedSafeAreaView style={styles.safeArea}>
      {/* Animated background */}
      <View style={StyleSheet.absoluteFill}>
        <RNAnimated.View pointerEvents="none" style={[mobileBgStyles.gradientLayer, { opacity: 1 }]}>
          <LinearGradient
            colors={['#2a1245', '#3d1a5c', theme.colors.background.base]}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 0.85 }}
            style={StyleSheet.absoluteFill}
          />
        </RNAnimated.View>
        <RNAnimated.View pointerEvents="none" style={[mobileBgStyles.gradientLayer, { opacity: 0.75 }]}>
          <LinearGradient
            colors={['rgba(232, 238, 255, 0.55)', 'rgba(40, 44, 54, 0.08)', 'rgba(210, 222, 255, 0.32)']}
            start={{ x: 0, y: 0 }}
            end={{ x: 0.95, y: 1 }}
            style={StyleSheet.absoluteFill}
          />
        </RNAnimated.View>
        {/* Center glow effect */}
        <View style={mobileBgStyles.center}>
          <RNAnimated.View
            pointerEvents="none"
            style={[
              mobileBgStyles.radialBlur,
              {
                transform: [
                  {
                    scale: circlePulse.interpolate({
                      inputRange: [0, 1],
                      outputRange: [1.01, 1.08],
                    }),
                  },
                ],
                opacity: circlePulse.interpolate({
                  inputRange: [0, 1],
                  outputRange: [0.2, 0.4],
                }),
              },
            ]}
          >
            <LinearGradient
              colors={[`${theme.colors.accent.primary}30`, `${theme.colors.accent.primary}00`]}
              start={{ x: 0.5, y: 0 }}
              end={{ x: 0.5, y: 1 }}
              style={StyleSheet.absoluteFill}
            />
            <LinearGradient
              colors={[`${theme.colors.accent.primary}20`, `${theme.colors.accent.primary}00`]}
              start={{ x: 0, y: 0.5 }}
              end={{ x: 1, y: 0.5 }}
              style={[StyleSheet.absoluteFill, { transform: [{ rotate: '45deg' }] }]}
            />
          </RNAnimated.View>
        </View>
      </View>
      <Pressable style={styles.dismissArea} onPress={Keyboard.dismiss}>
        <Animated.View style={[styles.animatedContainer, animatedContainerStyle]}>{content}</Animated.View>
      </Pressable>
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
  textContentType?: 'none' | 'username' | 'password' | 'emailAddress' | 'oneTimeCode';
  returnKeyType?: 'done' | 'next';
  onSubmitEditing?: () => void;
  styles: ReturnType<typeof createStyles>;
  theme: NovaTheme;
}

// Mobile-only component (TV uses inline implementation above)
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

    React.useImperativeHandle(ref, () => inputRef.current as TextInput);

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

const createStyles = (theme: NovaTheme, isTV: boolean) => {
  // Scale factor: tvOS gets larger UI, Android TV gets 30% reduction
  const isTvOS = isTV && Platform.OS === 'ios';
  const isAndroidTV = isTV && Platform.OS === 'android';
  const s = (value: number) =>
    isTvOS ? Math.round(value * 1.2) : isAndroidTV ? Math.round(value * 0.7) : value;
  // Extra 50% scaling for specific text elements on tvOS
  const sText = (value: number) => (isTvOS ? Math.round(s(value) * 1.5) : s(value));

  return StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: theme.colors.background.base,
    },
    dismissArea: {
      flex: 1,
    },
    animatedContainer: {
      flex: 1,
    },
    container: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      padding: s(24),
    },
    card: {
      width: '100%',
      maxWidth: s(400),
      backgroundColor: theme.colors.background.surface,
      borderRadius: s(16),
      padding: s(32),
      shadowColor: '#000',
      shadowOffset: { width: 0, height: s(4) },
      shadowOpacity: 0.3,
      shadowRadius: s(8),
      elevation: s(8),
    },
    header: {
      alignItems: 'center',
      marginBottom: s(24),
    },
    title: {
      fontSize: s(32),
      fontWeight: '700',
      color: theme.colors.accent.primary,
      marginBottom: s(8),
    },
    logoImage: {
      width: isTvOS ? 300 : isTV ? 200 : 140,
      height: isTvOS ? 300 : isTV ? 200 : 140,
      marginBottom: s(8),
    },
    subtitle: {
      fontSize: sText(16),
      color: theme.colors.text.secondary,
    },
    serverInfo: {
      fontSize: sText(12),
      color: theme.colors.text.muted,
      marginTop: 8,
    },
    form: {
      gap: s(16),
    },
    formContainer: {
      gap: s(16),
    },
    inputContainer: {
      marginBottom: s(8),
    },
    inputLabel: {
      fontSize: sText(14),
      color: theme.colors.text.secondary,
      marginBottom: s(8),
    },
    input: {
      backgroundColor: theme.colors.background.elevated,
      borderWidth: 2,
      borderColor: 'transparent',
      borderRadius: s(8),
      padding: s(14),
      fontSize: s(16),
      color: theme.colors.text.primary,
      textAlign: 'left',
    },
    inputFocused: {
      borderColor: theme.colors.accent.primary,
    },
    button: {
      backgroundColor: theme.colors.accent.primary,
      borderRadius: s(8),
      padding: s(16),
      alignItems: 'center',
    },
    buttonSpacing: {
      marginTop: s(16),
    },
    tvButton: {
      backgroundColor: theme.colors.accent.primary,
      alignSelf: 'center',
      width: '60%',
      paddingVertical: s(12),
      paddingHorizontal: s(24),
      minHeight: s(48),
      justifyContent: 'center',
      overflow: 'visible',
      borderWidth: 2,
      borderColor: 'transparent',
    },
    tvButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      alignSelf: 'center',
      width: '60%',
      paddingVertical: s(12),
      paddingHorizontal: s(24),
      minHeight: s(48),
      justifyContent: 'center',
      overflow: 'visible',
      borderWidth: 2,
      borderColor: theme.colors.text.primary,
    },
    tvSecondaryButton: {
      backgroundColor: 'transparent',
      borderWidth: 2,
      borderColor: 'transparent',
      alignSelf: 'center',
      width: '60%',
      paddingVertical: s(10),
      paddingHorizontal: s(24),
      minHeight: s(44),
      justifyContent: 'center',
      overflow: 'visible',
    },
    tvSecondaryButtonFocused: {
      backgroundColor: theme.colors.background.elevated,
      alignSelf: 'center',
      width: '60%',
      paddingVertical: s(10),
      paddingHorizontal: s(24),
      minHeight: s(44),
      justifyContent: 'center',
      overflow: 'visible',
      borderWidth: 2,
      borderColor: theme.colors.text.primary,
    },
    tvButtonText: {
      fontSize: s(18),
      lineHeight: s(22),
      fontWeight: '600',
    },
    tvButtonTextFocused: {
      fontSize: s(18),
      lineHeight: s(22),
      fontWeight: '600',
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
};

// TV background animation styles
const tvBgStyles = StyleSheet.create({
  gradientLayer: {
    ...StyleSheet.absoluteFillObject,
    top: -80,
    bottom: -80,
    left: -80,
    right: -80,
  },
  center: {
    flex: 1,
    paddingHorizontal: 120,
    paddingVertical: 80,
    justifyContent: 'center',
    alignItems: 'center',
    width: '100%',
  },
  radialBlur: {
    position: 'absolute',
    width: 760,
    height: 760,
    borderRadius: 999,
  },
});

// Mobile background animation styles
const mobileBgStyles = StyleSheet.create({
  gradientLayer: {
    ...StyleSheet.absoluteFillObject,
    top: -40,
    bottom: -40,
    left: -40,
    right: -40,
  },
  center: {
    flex: 1,
    paddingHorizontal: 24,
    paddingVertical: 24,
    justifyContent: 'center',
    alignItems: 'center',
    width: '100%',
  },
  radialBlur: {
    position: 'absolute',
    width: 340,
    height: 340,
    borderRadius: 999,
  },
});
