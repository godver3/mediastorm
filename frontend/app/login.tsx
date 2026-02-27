import React, { useCallback, useEffect, useRef, useState } from 'react';
import { ActivityIndicator, Keyboard, Platform, Pressable, StyleSheet, Text, TextInput, View } from 'react-native';
import { KeyboardAwareScrollView } from 'react-native-keyboard-aware-scroll-view';
import { Image } from '@/components/Image';
import Animated, { useAnimatedStyle, withTiming } from 'react-native-reanimated';
import { LinearGradient } from 'expo-linear-gradient';

import { useAuth } from '@/components/AuthContext';
import { useBackendSettings } from '@/components/BackendSettingsContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { focusTextInputTV, prefocusTextInputTV } from '@/utils/tv-text-input';
import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  SpatialNavigationFocusableView,
  DefaultFocus,
} from '@/services/tv-navigation';
import { useTheme, type NovaTheme } from '@/theme';
import { useToast } from '@/components/ToastContext';

// Local logo asset with fallback chain
const localLogoAsset = require('@/assets/app-logo-wide.png');
const GITHUB_LOGO_URL =
  'https://raw.githubusercontent.com/godver3/mediastorm/refs/heads/master/frontend/assets/tv_icons/icon-1280x768.png';

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
  const [serverReachable, setServerReachable] = useState<boolean | null>(null);

  // Check server reachability
  useEffect(() => {
    if (!backendUrl) {
      setServerReachable(null);
      return;
    }

    let cancelled = false;
    const checkReachability = async () => {
      try {
        const baseUrl = backendUrl.replace(/\/api$/, '');
        const controller = new AbortController();
        const timeout = setTimeout(() => controller.abort(), 5000);
        const res = await fetch(`${baseUrl}/health`, { signal: controller.signal });
        clearTimeout(timeout);
        if (!cancelled) setServerReachable(res.ok);
      } catch {
        if (!cancelled) setServerReachable(false);
      }
    };

    checkReachability();
    const interval = setInterval(checkReachability, 15000);
    return () => {
      cancelled = true;
      clearInterval(interval);
    };
  }, [backendUrl]);

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
      // Only animate up for lower fields (password, server URL) - mobile only
      if (lowerFieldFocused.current && !Platform.isTV) {
        setKeyboardVisible(true);
      }
    });

    const hideSub = Keyboard.addListener(hideEvent, () => {
      keyboardHideTimeout.current = setTimeout(() => {
        setKeyboardVisible(false);
      }, 100);
    });

    return () => {
      showSub.remove();
      hideSub.remove();
      if (keyboardHideTimeout.current) {
        clearTimeout(keyboardHideTimeout.current);
      }
    };
  }, []);

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

    // On TV, read from uncontrolled input refs to avoid re-renders during navigation
    const loginUsername = Platform.isTV ? tempUsernameRef.current : username;
    const loginPassword = Platform.isTV ? tempPasswordRef.current : password;

    if (!loginUsername.trim()) {
      showToast('Username is required', { tone: 'danger' });
      return;
    }
    if (!loginPassword) {
      showToast('Password is required', { tone: 'danger' });
      return;
    }

    try {
      await login(loginUsername.trim(), loginPassword);
      // Refresh settings now that we're authenticated
      try {
        await refreshSettings();
      } catch (err) {
        console.warn('[Login] Failed to refresh settings after login:', err);
        // Don't block login if settings refresh fails
      }
      // Navigation will be handled by the layout detecting auth state change
    } catch {
      // Error is already set in the auth context and shown via useEffect
    }
  }, [username, password, login, clearError, refreshSettings, showToast]);

  const handleSaveServer = useCallback(async () => {
    Keyboard.dismiss();

    const saveUrl = Platform.isTV ? tempServerUrlRef.current : serverUrl;

    if (!saveUrl.trim()) {
      showToast('Server URL is required', { tone: 'danger' });
      return;
    }

    setIsSavingServer(true);
    try {
      // Normalize URL: ensure /api suffix
      let normalizedUrl = saveUrl.trim();
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

  // TV-specific render - using spatial navigation
  if (Platform.isTV) {
    return (
      <FixedSafeAreaView style={styles.safeArea} edges={[]}>
        {/* Static gradient background */}
        <LinearGradient
          colors={['#2a1245', '#3d1a5c', theme.colors.background.base]}
          start={{ x: 0, y: 0 }}
          end={{ x: 1, y: 0.85 }}
          style={StyleSheet.absoluteFill}
        />
        {/* Login card overlay */}
        <SpatialNavigationRoot isActive={true}>
          <KeyboardAwareScrollView
            contentContainerStyle={styles.container}
            enableOnAndroid={true}
            extraScrollHeight={200}
            keyboardShouldPersistTaps="handled">
            <View style={styles.card}>
              <View style={styles.tvImageHeaderContainer}>
                <Image source={getLogoSource()} style={styles.tvLogoImage} contentFit="cover" onError={handleLogoError} />
              </View>
              <View style={styles.header}>
                <Text style={styles.subtitle}>{showServerConfig ? 'Configure Server' : 'Sign in to your account'}</Text>
                {!showServerConfig && backendUrl ? (
                  <View style={styles.serverInfoRow}>
                    <View
                      style={[
                        styles.reachabilityDot,
                        serverReachable === true && styles.reachabilityDotOnline,
                        serverReachable === false && styles.reachabilityDotOffline,
                        serverReachable === null && styles.reachabilityDotChecking,
                      ]}
                    />
                    <Text style={styles.serverInfo} numberOfLines={1}>
                      {backendUrl.replace(/\/api$/, '')}
                    </Text>
                  </View>
                ) : null}
              </View>

              {showServerConfig ? (
                <SpatialNavigationNode orientation="vertical">
                  <View style={styles.form}>
                    <View style={styles.inputContainer}>
                      <Text style={styles.inputLabel}>Server URL</Text>
                      <DefaultFocus>
                        <SpatialNavigationFocusableView onFocus={() => prefocusTextInputTV(serverUrlRef)} onSelect={() => focusTextInputTV(serverUrlRef)}>
                          {({ isFocused }: { isFocused: boolean }) => (
                            <Pressable
                              android_disableSound
                              tvParallaxProperties={{ enabled: false }}
                              style={[styles.tvInputBox, isFocused && styles.tvInputBoxFocused]}>
                              <TextInput
                                ref={serverUrlRef}
                                defaultValue={serverUrl}
                                onChangeText={(text) => {
                                  tempServerUrlRef.current = text;
                                }}
                                placeholder="http://192.168.1.100:7777"
                                placeholderTextColor={theme.colors.text.muted}
                                autoCapitalize="none"
                                autoCorrect={false}
                                autoComplete="off"
                                textContentType="none"
                                returnKeyType="done"
                                onSubmitEditing={Keyboard.dismiss}
                                style={[styles.tvNativeInput, isFocused && styles.tvNativeInputFocused]}
                                underlineColorAndroid="transparent"
                                importantForAutofill="no"
                                {...(Platform.OS === 'ios' && { keyboardAppearance: 'dark' })}
                                {...(Platform.OS === 'android' && Platform.isTV && { caretHidden: true })}
                              />
                            </Pressable>
                          )}
                        </SpatialNavigationFocusableView>
                      </DefaultFocus>
                    </View>

                    <SpatialNavigationFocusableView onSelect={handleSaveServer}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <Pressable
                          android_disableSound
                          tvParallaxProperties={{ enabled: false }}
                          style={[styles.tvButtonWrapper]}>
                          <View style={isFocused ? styles.tvButtonFocused : styles.tvButton}>
                            {isSavingServer ? (
                              <ActivityIndicator size="small" color={theme.colors.text.primary} />
                            ) : (
                              <Text style={isFocused ? styles.tvButtonTextFocused : styles.tvButtonText}>Connect</Text>
                            )}
                          </View>
                        </Pressable>
                      )}
                    </SpatialNavigationFocusableView>
                  </View>
                </SpatialNavigationNode>
              ) : (
                <SpatialNavigationNode orientation="vertical">
                  <View style={styles.form}>
                    <View style={styles.inputContainer}>
                      <Text style={styles.inputLabel}>Username</Text>
                      <DefaultFocus>
                        <SpatialNavigationFocusableView onFocus={() => prefocusTextInputTV(usernameRef)} onSelect={() => focusTextInputTV(usernameRef)}>
                          {({ isFocused }: { isFocused: boolean }) => (
                            <Pressable
                              android_disableSound
                              tvParallaxProperties={{ enabled: false }}
                              style={[styles.tvInputBox, isFocused && styles.tvInputBoxFocused]}>
                              <TextInput
                                ref={usernameRef}
                                defaultValue={username}
                                onChangeText={(text) => {
                                  tempUsernameRef.current = text;
                                }}
                                placeholder="Enter username"
                                placeholderTextColor={theme.colors.text.muted}
                                autoCapitalize="none"
                                autoCorrect={false}
                                autoComplete="off"
                                textContentType="none"
                                returnKeyType="done"
                                onSubmitEditing={Keyboard.dismiss}
                                style={[styles.tvNativeInput, isFocused && styles.tvNativeInputFocused]}
                                underlineColorAndroid="transparent"
                                importantForAutofill="no"
                                {...(Platform.OS === 'ios' && { keyboardAppearance: 'dark' })}
                                {...(Platform.OS === 'android' && Platform.isTV && { caretHidden: true })}
                              />
                            </Pressable>
                          )}
                        </SpatialNavigationFocusableView>
                      </DefaultFocus>
                    </View>

                    <View style={styles.inputContainer}>
                      <Text style={styles.inputLabel}>Password</Text>
                      <SpatialNavigationFocusableView onFocus={() => prefocusTextInputTV(passwordRef)} onSelect={() => focusTextInputTV(passwordRef)}>
                        {({ isFocused }: { isFocused: boolean }) => (
                          <Pressable
                            android_disableSound
                            tvParallaxProperties={{ enabled: false }}
                            style={[styles.tvInputBox, isFocused && styles.tvInputBoxFocused]}>
                            <TextInput
                              ref={passwordRef}
                              defaultValue={password}
                              onChangeText={(text) => {
                                tempPasswordRef.current = text;
                              }}
                              placeholder="Enter password"
                              placeholderTextColor={theme.colors.text.muted}
                              secureTextEntry
                              autoCapitalize="none"
                              autoCorrect={false}
                              autoComplete="off"
                              textContentType="none"
                              returnKeyType="done"
                              onSubmitEditing={handleLogin}
                              style={[styles.tvNativeInput, isFocused && styles.tvNativeInputFocused]}
                              underlineColorAndroid="transparent"
                              importantForAutofill="no"
                              {...(Platform.OS === 'ios' && { keyboardAppearance: 'dark' })}
                              {...(Platform.OS === 'android' && Platform.isTV && { caretHidden: true })}
                            />
                          </Pressable>
                        )}
                      </SpatialNavigationFocusableView>
                    </View>

                    <SpatialNavigationFocusableView onSelect={handleLogin}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <Pressable
                          android_disableSound
                          tvParallaxProperties={{ enabled: false }}
                          style={[styles.tvButtonWrapper]}>
                          <View style={isFocused ? styles.tvButtonFocused : styles.tvButton}>
                            {isLoading ? (
                              <ActivityIndicator size="small" color={theme.colors.text.primary} />
                            ) : (
                              <Text style={isFocused ? styles.tvButtonTextFocused : styles.tvButtonText}>Sign In</Text>
                            )}
                          </View>
                        </Pressable>
                      )}
                    </SpatialNavigationFocusableView>

                    <SpatialNavigationFocusableView onSelect={() => setShowServerConfig(true)}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <Pressable
                          android_disableSound
                          tvParallaxProperties={{ enabled: false }}
                          style={[styles.tvButtonWrapper]}>
                          <View style={isFocused ? styles.tvSecondaryButtonFocused : styles.tvSecondaryButton}>
                            <Text style={isFocused ? styles.tvButtonTextFocused : styles.tvButtonText}>Change Server</Text>
                          </View>
                        </Pressable>
                      )}
                    </SpatialNavigationFocusableView>
                  </View>
                </SpatialNavigationNode>
              )}
            </View>
          </KeyboardAwareScrollView>
        </SpatialNavigationRoot>
      </FixedSafeAreaView>
    );
  }

  // Mobile render
  const serverConfigContent = (
    <View style={styles.container}>
      <View style={styles.card}>
        <View style={styles.imageHeaderContainer}>
          <Image source={getLogoSource()} style={styles.mobileLogoImage} contentFit="cover" onError={handleLogoError} />
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
            style={({ pressed }) => [styles.button, pressed && styles.buttonPressed]}>
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
          <Image source={getLogoSource()} style={styles.mobileLogoImage} contentFit="cover" onError={handleLogoError} />
        </View>
        <View style={styles.header}>
          <Text style={styles.subtitle}>Sign in to your account</Text>
          {backendUrl ? (
            <Pressable onPress={() => setShowServerConfig(true)}>
              <View style={styles.serverInfoRow}>
                <View
                  style={[
                    styles.reachabilityDot,
                    serverReachable === true && styles.reachabilityDotOnline,
                    serverReachable === false && styles.reachabilityDotOffline,
                    serverReachable === null && styles.reachabilityDotChecking,
                  ]}
                />
                <Text style={styles.serverInfo} numberOfLines={1}>
                  {backendUrl.replace(/\/api$/, '')} (change)
                </Text>
              </View>
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
            style={({ pressed }) => [styles.button, pressed && styles.buttonPressed]}>
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

  // On web, don't wrap in Pressable as it intercepts clicks on inputs
  const isWeb = Platform.OS === 'web';

  return (
    <FixedSafeAreaView style={styles.safeArea}>
      {/* Static gradient background */}
      <LinearGradient
        colors={['#2a1245', '#3d1a5c', theme.colors.background.base]}
        start={{ x: 0, y: 0 }}
        end={{ x: 1, y: 0.85 }}
        style={StyleSheet.absoluteFill}
      />
      {isWeb ? (
        <View style={styles.dismissArea}>
          <Animated.View style={[styles.animatedContainer, animatedContainerStyle]}>{content}</Animated.View>
        </View>
      ) : (
        <Pressable style={styles.dismissArea} onPress={Keyboard.dismiss}>
          <Animated.View style={[styles.animatedContainer, animatedContainerStyle]}>{content}</Animated.View>
        </Pressable>
      )}
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
  const isWeb = Platform.OS === 'web';
  const s = (value: number) => (isTvOS ? Math.round(value * 1.2) : isAndroidTV ? Math.round(value * 0.55) : value);
  // Extra 50% scaling for specific text elements on TV platforms
  const sText = (value: number) => (isTV ? Math.round(s(value) * 1.5) : s(value));

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
      maxWidth: isAndroidTV ? 280 : isTV ? s(500) : s(400),
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
    serverInfoRow: {
      flexDirection: 'row',
      alignItems: 'center',
      marginTop: 8,
    },
    serverInfo: {
      fontSize: sText(14),
      color: theme.colors.text.muted,
    },
    reachabilityDot: {
      width: s(8),
      height: s(8),
      borderRadius: s(4),
      marginRight: s(6),
    },
    reachabilityDotOnline: {
      backgroundColor: '#34C759',
    },
    reachabilityDotOffline: {
      backgroundColor: '#FF3B30',
    },
    reachabilityDotChecking: {
      backgroundColor: theme.colors.text.muted,
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
      alignItems: 'center',
    },
    inputContainer: {
      marginBottom: s(8),
      width: '100%',
    },
    inputLabel: {
      fontSize: sText(14),
      color: theme.colors.text.secondary,
      marginBottom: s(8),
      textAlign: 'left',
    },
    input: {
      backgroundColor: theme.colors.background.elevated,
      borderWidth: 2,
      borderColor: 'transparent',
      borderRadius: s(8),
      paddingVertical: s(14),
      paddingLeft: isAndroidTV ? s(12) : isWeb ? s(14) : s(12),
      paddingRight: isWeb ? s(14) : 0,
      fontSize: s(16),
      color: theme.colors.text.primary,
      textAlign: 'left',
      width: '100%',
      height: s(56),
      // Web-specific: ensure outline is visible on focus
      ...(isWeb ? { outlineStyle: 'none' } : {}),
    } as any,
    inputFocused: {
      borderColor: theme.colors.accent.primary,
    },
    tvInputBox: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: s(8),
      borderWidth: 3,
      borderColor: 'transparent',
    },
    tvInputBoxFocused: {
      borderColor: theme.colors.accent.primary,
      ...(isTvOS
        ? {
            shadowColor: theme.colors.accent.primary,
            shadowOpacity: 0.4,
            shadowOffset: { width: 0, height: 4 },
            shadowRadius: 12,
          }
        : null),
      ...(isAndroidTV
        ? {
            elevation: 8,
          }
        : null),
    },
    tvNativeInput: {
      fontSize: s(16),
      color: theme.colors.text.primary,
      textAlign: 'left',
      paddingLeft: s(2),
      paddingRight: s(10),
      paddingVertical: s(14),
      backgroundColor: 'transparent',
    },
    tvNativeInputFocused: {
      // Focus styling handled by tvInputBox
    },
    tvInputWrapper: {
      width: '100%',
    },
    tvInputWrapperFocused: {
      // Focus state handled by input border
    },
    tvButtonWrapper: {
      alignSelf: 'center',
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
      minWidth: s(280),
      paddingVertical: s(12),
      paddingHorizontal: s(32),
      minHeight: s(48),
      justifyContent: 'center',
      alignItems: 'center',
      overflow: 'visible',
      borderWidth: 2,
      borderColor: 'transparent',
      borderRadius: s(8),
    },
    tvButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      minWidth: s(280),
      paddingVertical: s(12),
      paddingHorizontal: s(32),
      minHeight: s(48),
      justifyContent: 'center',
      alignItems: 'center',
      overflow: 'visible',
      borderWidth: 2,
      borderColor: theme.colors.text.primary,
      borderRadius: s(8),
    },
    tvSecondaryButton: {
      backgroundColor: 'transparent',
      borderWidth: 2,
      borderColor: 'transparent',
      minWidth: s(280),
      paddingVertical: s(10),
      paddingHorizontal: s(32),
      minHeight: s(44),
      justifyContent: 'center',
      alignItems: 'center',
      overflow: 'visible',
      borderRadius: s(8),
    },
    tvSecondaryButtonFocused: {
      backgroundColor: theme.colors.background.elevated,
      minWidth: s(280),
      paddingVertical: s(10),
      paddingHorizontal: s(32),
      minHeight: s(44),
      justifyContent: 'center',
      alignItems: 'center',
      overflow: 'visible',
      borderWidth: 2,
      borderColor: theme.colors.text.primary,
      borderRadius: s(8),
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
