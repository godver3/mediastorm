import React, { useCallback, useEffect, useRef, useState } from 'react';
import {
  ActivityIndicator,
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

// Local logo asset with fallback chain
const localLogoAsset = require('@/assets/app-logo-wide.png');
const GITHUB_LOGO_URL = 'https://raw.githubusercontent.com/godver3/strmr/refs/heads/master/frontend/assets/tv_icons/icon-1280x768.png';

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

  // Logo source with fallback chain: local → backend → github
  const [logoSource, setLogoSource] = useState<'local' | 'backend' | 'github'>('local');
  const getLogoSource = useCallback(() => {
    if (logoSource === 'local') return localLogoAsset;
    if (logoSource === 'backend' && backendUrl) return { uri: `${backendUrl}/static/app-logo-wide.png` };
    return { uri: GITHUB_LOGO_URL };
  }, [logoSource, backendUrl]);
  const handleLogoError = useCallback(() => {
    if (logoSource === 'local' && backendUrl) {
      setLogoSource('backend');
    } else if (logoSource === 'local' || logoSource === 'backend') {
      setLogoSource('github');
    }
  }, [logoSource, backendUrl]);

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
      // Only animate up for lower fields (password, server URL)
      if (lowerFieldFocused.current) {
        setKeyboardVisible(true);
        if (Platform.isTV) {
          tvPasswordFocused.value = 1;
        }
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

  // Don't lock spatial navigation on login - let user navigate freely between fields
  // This is simpler UX than requiring keyboard dismissal between each field
  const handleUsernameFocus = useCallback(() => {
    lowerFieldFocused.current = true;
  }, []);
  const handleUsernameBlur = useCallback(() => {
    lowerFieldFocused.current = false;
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
          {/* Static gradient background */}
          <LinearGradient
            colors={['#2a1245', '#3d1a5c', theme.colors.background.base]}
            start={{ x: 0, y: 0 }}
            end={{ x: 1, y: 0.85 }}
            style={StyleSheet.absoluteFill}
          />
          {/* Login card overlay */}
          <Animated.View style={[styles.container, tvAnimatedStyle]}>
            <View style={styles.card}>
              <View style={styles.tvImageHeaderContainer}>
                <Image
                  source={getLogoSource()}
                  style={styles.tvLogoImage}
                  contentFit="cover"
                  onError={handleLogoError}
                />
                <LinearGradient
                  colors={['transparent', theme.colors.background.surface]}
                  style={styles.tvImageGradientOverlay}
                />
              </View>
              <View style={styles.header}>
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
        <View style={styles.imageHeaderContainer}>
          <Image
            source={getLogoSource()}
            style={styles.mobileLogoImage}
            contentFit="cover"
            onError={handleLogoError}
          />
          <LinearGradient
            colors={['transparent', theme.colors.background.surface]}
            style={styles.imageGradientOverlay}
          />
        </View>
        <View style={styles.header}>
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
            onFocus={() => {
              lowerFieldFocused.current = true;
              // Android: keyboard event fires before focus, so set directly
              setKeyboardVisible(true);
            }}
            onBlur={() => {
              lowerFieldFocused.current = false;
              setKeyboardVisible(false);
            }}
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
        <View style={styles.imageHeaderContainer}>
          <Image
            source={getLogoSource()}
            style={styles.mobileLogoImage}
            contentFit="cover"
            onError={handleLogoError}
          />
          <LinearGradient
            colors={['transparent', theme.colors.background.surface]}
            style={styles.imageGradientOverlay}
          />
        </View>
        <View style={styles.header}>
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
            onFocus={() => {
              lowerFieldFocused.current = true;
              setKeyboardVisible(true);
            }}
            onBlur={() => {
              lowerFieldFocused.current = false;
              setKeyboardVisible(false);
            }}
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
            autoCapitalize="none"
            autoCorrect={false}
            autoComplete="off"
            textContentType="oneTimeCode"
            returnKeyType="done"
            onSubmitEditing={handleLogin}
            onFocus={() => {
              lowerFieldFocused.current = true;
              // Android: keyboard event fires before focus, so set directly
              setKeyboardVisible(true);
            }}
            onBlur={() => {
              lowerFieldFocused.current = false;
              setKeyboardVisible(false);
            }}
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
      {/* Static gradient background */}
      <LinearGradient
        colors={['#2a1245', '#3d1a5c', theme.colors.background.base]}
        start={{ x: 0, y: 0 }}
        end={{ x: 1, y: 0.85 }}
        style={StyleSheet.absoluteFill}
      />
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
  onFocus?: () => void;
  onBlur?: () => void;
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
      onFocus,
      onBlur,
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
          onFocus={onFocus}
          onBlur={onBlur}
          style={styles.input}
        />
      </View>
    );
  },
);

LoginTextInput.displayName = 'LoginTextInput';

const createStyles = (theme: NovaTheme, isTV: boolean) => {
  // Scale factor: tvOS gets larger UI, Android TV gets smaller UI
  const isTvOS = isTV && Platform.OS === 'ios';
  const isAndroidTV = isTV && Platform.OS === 'android';
  const s = (value: number) =>
    isTvOS ? Math.round(value * 1.2) : isAndroidTV ? Math.round(value * 0.55) : value;
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
      overflow: 'hidden',
      shadowColor: '#000',
      shadowOffset: { width: 0, height: s(4) },
      shadowOpacity: 0.3,
      shadowRadius: s(8),
      elevation: s(8),
    },
    imageHeaderContainer: {
      width: '100%',
      height: 210,
      overflow: 'hidden',
    },
    mobileLogoImage: {
      width: '100%',
      height: '100%',
    },
    imageGradientOverlay: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: '30%',
    },
    tvImageHeaderContainer: {
      width: '100%',
      height: s(280),
      overflow: 'hidden',
    },
    tvLogoImage: {
      width: '100%',
      height: '100%',
    },
    tvImageGradientOverlay: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      height: '30%',
    },
    header: {
      alignItems: 'center',
      marginBottom: s(24),
      paddingHorizontal: s(32),
      paddingTop: s(16),
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
      fontSize: sText(14),
      color: theme.colors.text.muted,
      marginTop: 8,
    },
    form: {
      gap: s(16),
      paddingHorizontal: s(32),
      paddingBottom: s(32),
    },
    formContainer: {
      gap: s(16),
      paddingHorizontal: s(32),
      paddingBottom: s(32),
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

