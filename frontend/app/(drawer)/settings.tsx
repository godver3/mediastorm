import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Ionicons } from '@expo/vector-icons';
import {
  Clipboard,
  Keyboard,
  Linking,
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
import Animated, { useAnimatedStyle, useSharedValue, withSequence, withTiming } from 'react-native-reanimated';

import {
  useBackendSettings,
  type BackendIndexerConfig,
  type BackendSettings,
  type BackendShelfConfig,
  type BackendTorrentScraperConfig,
  type PlaybackPreference,
  type StreamingServiceMode,
  type StreamingServicePriority,
  type MultiProviderMode,
} from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { useAuth } from '@/components/AuthContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { useMenuContext } from '@/components/MenuContext';
import { useToast } from '@/components/ToastContext';
import { apiService } from '@/services/api';
import { getClientId } from '@/services/clientId';
import { logger } from '@/services/logger';
import { QRCode } from '@/components/QRCode';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { isTV, tvScale } from '@/theme/tokens/tvScale';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import {
  SpatialNavigationRoot,
  SpatialNavigationNode,
  SpatialNavigationScrollView,
  SpatialNavigationFocusableView,
  DefaultFocus,
} from '@/services/tv-navigation';
import { useIsFocused } from '@react-navigation/native';
import { APP_VERSION } from '@/version';
import { Stack, router } from 'expo-router';
import { useKonamiCode, KONAMI_SEQUENCE } from '@/hooks/useKonamiCode';
import { SpaceShooterGame } from '@/components/SpaceShooterGame';

// expo-updates may not be available in all builds (e.g., development builds without it)
// Use a getter to lazily load the module only when actually accessed
const getUpdates = (): typeof import('expo-updates') | null => {
  try {
    return require('expo-updates');
  } catch {
    // Module not available - updates functionality will be disabled
    return null;
  }
};

type SettingsTab = 'connection' | 'content' | 'playback' | 'home' | 'advanced' | 'live' | 'filtering';

interface TabOption {
  key: SettingsTab;
  label: string;
}

// Type to make styles compatible with all component style props
type CompatibleStyles = {
  [K in keyof ReturnType<typeof createStyles>]: ViewStyle & TextStyle;
};

interface TextInputFieldProps {
  label: string;
  value: string;
  onChange: (text: string) => void;
  options?: {
    secureTextEntry?: boolean;
    keyboardType?: 'default' | 'numeric';
    placeholder?: string;
    multiline?: boolean;
  };
  errorMessage?: string;
  styles: CompatibleStyles;
}

function _TextInputField({ label, value, onChange, options, errorMessage, styles }: TextInputFieldProps) {
  const theme = useTheme();
  const inputRef = useRef<TextInput | null>(null);

  return (
    <View style={styles.fieldRow as ViewStyle}>
      <Text style={styles.fieldLabel as TextStyle}>{label}</Text>
      <Pressable
        onPress={() => inputRef.current?.focus()}
        tvParallaxProperties={{ enabled: false }}
        style={({ focused }) => [{ flex: 1 }, focused && { opacity: 1 }]}>
        {({ focused }) => (
          <TextInput
            ref={inputRef}
            value={value}
            onChangeText={onChange}
            style={[
              styles.input as TextStyle,
              focused && (styles.inputFocused as TextStyle),
              options?.multiline && (styles.multiline as TextStyle),
            ]}
            autoCapitalize="none"
            autoCorrect={false}
            autoComplete="off"
            textContentType="none"
            spellCheck={false}
            secureTextEntry={options?.secureTextEntry}
            keyboardType={options?.keyboardType ?? 'default'}
            placeholder={options?.placeholder}
            placeholderTextColor={theme.colors.text.muted}
            multiline={options?.multiline}
            textAlignVertical={options?.multiline ? 'top' : 'center'}
            showSoftInputOnFocus={true}
            editable={Platform.isTV ? focused : true}
            underlineColorAndroid="transparent"
            importantForAutofill="no"
            disableFullscreenUI={true}
          />
        )}
      </Pressable>
      {errorMessage ? <Text style={styles.fieldError as TextStyle}>{errorMessage}</Text> : null}
    </View>
  );
}

interface DropdownFieldProps {
  label: string;
  value: string;
  options: Array<{ label: string; value: string }>;
  onChange: (value: string) => void;
  styles: CompatibleStyles;
}

function _DropdownField({ label, value, options, onChange, styles }: DropdownFieldProps) {
  return (
    <View style={styles.fieldRow as ViewStyle}>
      <Text style={styles.fieldLabel as TextStyle}>{label}</Text>
      <View style={styles.dropdownContainer as ViewStyle}>
        {options.map((option) => (
          <FocusablePressable
            key={option.value}
            text={option.label}
            onSelect={() => onChange(option.value)}
            style={[
              styles.dropdownOption as ViewStyle,
              value === option.value && (styles.dropdownOptionSelected as ViewStyle),
            ]}
            textStyle={styles.dropdownOptionText as TextStyle}
            focusedTextStyle={styles.dropdownOptionTextFocused as TextStyle}
          />
        ))}
      </View>
    </View>
  );
}

type EditableIndexer = BackendIndexerConfig;

type EditableTorrentScraper = BackendTorrentScraperConfig;

interface EditableUsenetProvider {
  name: string;
  host: string;
  port: string;
  ssl: boolean;
  username: string;
  password: string;
  connections: string;
  enabled: boolean;
}

interface EditableDebridProvider {
  name: string;
  provider: string;
  apiKey: string;
  enabled: boolean;
}

interface EditableBackendSettings {
  server: {
    host: string;
    port: string;
  };
  usenet: EditableUsenetProvider[];
  indexers: EditableIndexer[];
  torrentScrapers: EditableTorrentScraper[];
  metadata: {
    tvdbApiKey: string;
    tmdbApiKey: string;
    language: string;
  };
  cache: {
    directory: string;
    metadataTtlHours: string;
  };
  webdav: {
    enabled: boolean;
    prefix: string;
    username: string;
    password: string;
  };
  streaming: {
    serviceMode: StreamingServiceMode;
    servicePriority: StreamingServicePriority;
    multiProviderMode: MultiProviderMode;
    maxDownloadWorkers: string;
    maxCacheSizeMB: string;
    debridProviders: EditableDebridProvider[];
  };
  transmux: {
    enabled: boolean;
    ffmpegPath: string;
    ffprobePath: string;
  };
  playback: {
    preferredPlayer: PlaybackPreference;
    preferredAudioLanguage?: string;
    preferredSubtitleLanguage?: string;
    preferredSubtitleMode?: 'off' | 'on' | 'forced-only';
    useLoadingScreen?: boolean;
    subtitleSize?: number | string; // string during editing, parsed to number on save
  };
  live: {
    playlistUrl: string;
    playlistCacheTtlHours: string;
  };
  homeShelves: {
    shelves: BackendShelfConfig[];
  };
  filtering: {
    maxSizeMovieGb: string;
    maxSizeEpisodeGb: string;
    excludeHdr: boolean;
    prioritizeHdr: boolean;
    filterOutTerms: string;
  };
}

// TV Grid Item Types for virtualized settings
interface TextInputOptions {
  secureTextEntry?: boolean;
  keyboardType?: 'default' | 'numeric';
  placeholder?: string;
  multiline?: boolean;
}

interface DropdownOption {
  label: string;
  value: string;
}

type SettingsGridItem =
  | { type: 'header'; id: string; title: string; description?: string; key?: string }
  | {
      type: 'text-field';
      id: string;
      label: string;
      value: string;
      fieldKey: string;
      options?: TextInputOptions;
      key?: string;
    }
  | { type: 'toggle'; id: string; label: string; value: boolean; fieldKey: string; description?: string; key?: string }
  | {
      type: 'dropdown';
      id: string;
      label: string;
      value: string;
      options: DropdownOption[];
      fieldKey: string;
      key?: string;
    }
  | { type: 'button'; id: string; label: string; action: string; disabled?: boolean; key?: string }
  | {
      type: 'button-row';
      id: string;
      buttons: Array<{ label: string; action: string; disabled?: boolean }>;
      key?: string;
    }
  | { type: 'shelf-item'; id: string; shelf: BackendShelfConfig; index: number; total: number; key?: string }
  | { type: 'version-info'; id: string; key?: string };

// TextInputModal Props for TV text editing
interface TextInputModalProps {
  visible: boolean;
  label: string;
  value: string;
  onSubmit: (value: string) => void;
  onCancel: () => void;
  options?: TextInputOptions;
  styles: CompatibleStyles;
  theme: NovaTheme;
}

// TextInputModal Component for TV - Uses View overlay (not Modal) to avoid tvOS native focus issues
function TextInputModal({
  visible,
  label,
  value,
  onSubmit,
  onCancel: _onCancel,
  options,
  styles,
  theme,
}: TextInputModalProps) {
  const inputRef = useRef<TextInput>(null);
  const [editValue, setEditValue] = useState(value);

  // Reset edit value when modal opens
  useEffect(() => {
    if (visible) {
      setEditValue(value);
    }
  }, [visible, value]);

  const handleSubmit = useCallback(() => {
    onSubmit(editValue);
  }, [editValue, onSubmit]);

  // Refs for back interceptor to avoid stale closures
  const handleSubmitRef = useRef(handleSubmit);
  const removeInterceptorRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    handleSubmitRef.current = handleSubmit;
  }, [handleSubmit]);

  // Register back interceptor to close modal when menu/back button is pressed on tvOS
  useEffect(() => {
    if (!visible) {
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
      // Prevent duplicate handling
      if (isHandling) {
        return true;
      }

      isHandling = true;

      // Call handleSubmit using ref to avoid stale closure
      handleSubmitRef.current();

      // Delay cleanup to swallow duplicate events
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

    return () => {
      // Cleanup on unmount
    };
  }, [visible]);

  // Don't render anything if not visible - avoids native Modal focus issues on tvOS
  if (!visible) {
    return null;
  }

  return (
    <View style={styles.tvModalOverlay as ViewStyle}>
      <View style={[styles.tvModalContent as ViewStyle, { maxHeight: options?.multiline ? '60%' : '40%' }]}>
        <Text style={styles.tvModalTitle as TextStyle}>{label}</Text>
        <Text style={styles.tvModalSubtitle as TextStyle}>
          {options?.multiline ? 'Enter text below' : 'Press select to edit, then use the keyboard'}
        </Text>

        <Pressable
          onPress={() => inputRef.current?.focus()}
          hasTVPreferredFocus={true}
          tvParallaxProperties={{ enabled: false }}
          style={({ focused }) => [{ width: '100%' }, focused && { opacity: 1 }]}>
          {({ focused }) => (
            <TextInput
              ref={inputRef}
              {...(Platform.isTV ? { defaultValue: editValue } : { value: editValue })}
              onChangeText={setEditValue}
              style={[
                styles.tvTextInputModalInput as TextStyle,
                focused && (styles.tvTextInputModalInputFocused as TextStyle),
                options?.multiline && (styles.tvTextInputModalInputMultiline as TextStyle),
              ]}
              placeholder={options?.placeholder}
              placeholderTextColor={theme.colors.text.muted}
              secureTextEntry={options?.secureTextEntry}
              keyboardType={options?.keyboardType ?? 'default'}
              multiline={options?.multiline}
              textAlignVertical={options?.multiline ? 'top' : 'center'}
              autoCapitalize="none"
              autoCorrect={false}
              autoComplete="off"
              textContentType="none"
              spellCheck={false}
              editable={focused}
              underlineColorAndroid="transparent"
              importantForAutofill="no"
              disableFullscreenUI={true}
              {...(Platform.OS === 'ios' &&
                Platform.isTV && {
                  keyboardAppearance: 'dark',
                })}
            />
          )}
        </Pressable>

        <View style={styles.tvModalFooter as ViewStyle}>
          <FocusablePressable
            text="Close"
            onSelect={handleSubmit}
            style={styles.tvModalCloseButton as ViewStyle}
            focusedStyle={styles.tvModalCloseButtonFocused as ViewStyle}
            textStyle={styles.tvModalCloseButtonText as TextStyle}
            focusedTextStyle={styles.tvModalCloseButtonTextFocused as TextStyle}
          />
        </View>
      </View>
    </View>
  );
}

const toEditableSettings = (settings: BackendSettings): EditableBackendSettings => {
  const webdavSettings = settings.webdav ?? { enabled: false, prefix: '/webdav', username: '', password: '' };
  const streamingSettings = settings.streaming ?? {
    serviceMode: 'usenet' as StreamingServiceMode,
    servicePriority: 'none' as StreamingServicePriority,
    maxDownloadWorkers: 15,
    maxCacheSizeMB: 100,
    debridProviders: [],
  };
  const streamingMaxDownloadWorkers =
    typeof streamingSettings.maxDownloadWorkers === 'number' && streamingSettings.maxDownloadWorkers > 0
      ? streamingSettings.maxDownloadWorkers
      : 15;
  const streamingMaxCacheSize =
    typeof streamingSettings.maxCacheSizeMB === 'number' && streamingSettings.maxCacheSizeMB > 0
      ? streamingSettings.maxCacheSizeMB
      : 100;

  return {
    server: {
      host: settings.server.host ?? '',
      port: settings.server.port != null ? String(settings.server.port) : '',
    },
    usenet: (settings.usenet ?? []).map((provider) => ({
      name: provider.name ?? '',
      host: provider.host ?? '',
      port: provider.port != null ? String(provider.port) : '',
      ssl: !!provider.ssl,
      username: provider.username ?? '',
      password: provider.password ?? '',
      connections: provider.connections != null ? String(provider.connections) : '',
      enabled: !!provider.enabled,
    })),
    indexers: (settings.indexers ?? []).map((indexer) => ({
      name: indexer.name ?? '',
      url: indexer.url ?? '',
      apiKey: indexer.apiKey ?? '',
      type: indexer.type ?? '',
      enabled: !!indexer.enabled,
    })),
    torrentScrapers: (settings.torrentScrapers ?? []).map((scraper) => ({
      name: scraper.name ?? '',
      type: scraper.type ?? '',
      url: scraper.url ?? '',
      apiKey: scraper.apiKey ?? '',
      enabled: !!scraper.enabled,
      config: scraper.config ?? {},
    })),
    metadata: {
      tvdbApiKey: settings.metadata.tvdbApiKey ?? '',
      tmdbApiKey: settings.metadata.tmdbApiKey ?? '',
      language: settings.metadata.language ?? '',
    },
    cache: {
      directory: settings.cache.directory ?? '',
      metadataTtlHours: settings.cache.metadataTtlHours != null ? String(settings.cache.metadataTtlHours) : '',
    },
    streaming: {
      serviceMode: (streamingSettings.serviceMode ?? 'usenet') as StreamingServiceMode,
      servicePriority: (streamingSettings.servicePriority ?? 'none') as StreamingServicePriority,
      multiProviderMode: (streamingSettings.multiProviderMode ?? 'fastest') as MultiProviderMode,
      maxDownloadWorkers: String(streamingMaxDownloadWorkers),
      maxCacheSizeMB: String(streamingMaxCacheSize),
      debridProviders: (streamingSettings.debridProviders ?? []).map((provider) => ({
        name: provider.name ?? '',
        provider: provider.provider ?? '',
        apiKey: provider.apiKey ?? '',
        enabled: !!provider.enabled,
      })),
    },
    webdav: {
      enabled: !!webdavSettings.enabled,
      prefix: webdavSettings.prefix ?? '/webdav',
      username: webdavSettings.username ?? '',
      password: webdavSettings.password ?? '',
    },
    transmux: {
      enabled: !!settings.transmux.enabled,
      ffmpegPath: settings.transmux.ffmpegPath ?? '',
      ffprobePath: settings.transmux.ffprobePath ?? '',
    },
    playback: {
      preferredPlayer: settings.playback?.preferredPlayer ?? 'native',
      preferredAudioLanguage: settings.playback?.preferredAudioLanguage ?? '',
      preferredSubtitleLanguage: settings.playback?.preferredSubtitleLanguage ?? '',
      preferredSubtitleMode: settings.playback?.preferredSubtitleMode ?? 'off',
      useLoadingScreen: settings.playback?.useLoadingScreen ?? false,
      subtitleSize: settings.playback?.subtitleSize ?? 1.0,
    },
    live: {
      playlistUrl: settings.live?.playlistUrl ?? '',
      playlistCacheTtlHours:
        settings.live?.playlistCacheTtlHours != null ? String(settings.live.playlistCacheTtlHours) : '24',
    },
    homeShelves: {
      shelves: settings.homeShelves?.shelves ?? [],
    },
    filtering: {
      maxSizeMovieGb: settings.filtering?.maxSizeMovieGb != null ? String(settings.filtering.maxSizeMovieGb) : '0',
      maxSizeEpisodeGb:
        settings.filtering?.maxSizeEpisodeGb != null ? String(settings.filtering.maxSizeEpisodeGb) : '0',
      excludeHdr: settings.filtering?.excludeHdr ?? false,
      prioritizeHdr: settings.filtering?.prioritizeHdr ?? true,
      filterOutTerms: (settings.filtering?.filterOutTerms ?? []).join(', '),
    },
  };
};
const toNumber = (value: string, fallback: number, label: string) => {
  const trimmed = value.trim();
  if (!trimmed) {
    return fallback;
  }
  const parsed = Number.parseInt(trimmed, 10);
  if (Number.isNaN(parsed)) {
    throw new Error(`${label} must be a valid number`);
  }
  return parsed;
};

const toBackendPayload = (editable: EditableBackendSettings, baseline: BackendSettings): BackendSettings => {
  const baselineStreaming = baseline.streaming
    ? baseline.streaming
    : {
        serviceMode: 'usenet' as StreamingServiceMode,
        servicePriority: 'none' as StreamingServicePriority,
        maxDownloadWorkers: 15,
        maxCacheSizeMB: 100,
        debridProviders: [],
      };
  return {
    server: {
      host: editable.server.host.trim(),
      port: toNumber(editable.server.port, baseline.server.port, 'Server port'),
    },
    usenet: editable.usenet.map((provider, idx) => ({
      name: provider.name.trim(),
      host: provider.host.trim(),
      port: toNumber(provider.port, baseline.usenet[idx]?.port || 563, 'Usenet port'),
      ssl: provider.ssl,
      username: provider.username.trim(),
      password: provider.password,
      connections: toNumber(provider.connections, baseline.usenet[idx]?.connections || 8, 'Usenet connections'),
      enabled: !!provider.enabled,
    })),
    indexers: editable.indexers.map((indexer, idx) => ({
      name: indexer.name.trim(),
      url: indexer.url.trim(),
      apiKey: indexer.apiKey.trim(),
      type: indexer.type.trim() || baseline.indexers[idx]?.type || 'newznab',
      enabled: !!indexer.enabled,
    })),
    torrentScrapers: editable.torrentScrapers.map((scraper, idx) => ({
      name: scraper.name.trim(),
      type: scraper.type.trim() || baseline.torrentScrapers[idx]?.type || 'torrentio',
      url: scraper.url.trim(),
      apiKey: scraper.apiKey.trim(),
      enabled: !!scraper.enabled,
      config: scraper.config,
    })),
    metadata: {
      tvdbApiKey: editable.metadata.tvdbApiKey.trim(),
      tmdbApiKey: editable.metadata.tmdbApiKey.trim(),
      language: editable.metadata.language.trim() || baseline.metadata.language || 'en',
    },
    cache: {
      directory: editable.cache.directory.trim() || baseline.cache.directory,
      metadataTtlHours: toNumber(
        editable.cache.metadataTtlHours,
        baseline.cache.metadataTtlHours,
        'Metadata TTL (hours)',
      ),
    },
    webdav: {
      enabled: editable.webdav.enabled,
      prefix: editable.webdav.prefix.trim() || baseline.webdav?.prefix || '/webdav',
      username: editable.webdav.username.trim(),
      password: editable.webdav.password,
    },
    streaming: {
      serviceMode: (editable.streaming.serviceMode ||
        baselineStreaming.serviceMode ||
        'usenet') as StreamingServiceMode,
      servicePriority: (editable.streaming.servicePriority ||
        baselineStreaming.servicePriority ||
        'none') as StreamingServicePriority,
      multiProviderMode: (editable.streaming.multiProviderMode ||
        baselineStreaming.multiProviderMode ||
        'fastest') as MultiProviderMode,
      maxDownloadWorkers: toNumber(
        editable.streaming.maxDownloadWorkers,
        baselineStreaming.maxDownloadWorkers ?? 15,
        'Streaming max download workers',
      ),
      maxCacheSizeMB: toNumber(
        editable.streaming.maxCacheSizeMB,
        baselineStreaming.maxCacheSizeMB ?? 100,
        'Streaming cache size (MB)',
      ),
      debridProviders:
        editable.streaming.debridProviders.length > 0
          ? editable.streaming.debridProviders.map((provider, idx) => ({
              name: provider.name.trim() || baselineStreaming.debridProviders[idx]?.name || '',
              provider: provider.provider.trim() || baselineStreaming.debridProviders[idx]?.provider || '',
              apiKey: provider.apiKey.trim(),
              enabled: !!provider.enabled,
            }))
          : baselineStreaming.debridProviders,
    },
    transmux: {
      enabled: editable.transmux.enabled,
      ffmpegPath: editable.transmux.ffmpegPath.trim() || baseline.transmux.ffmpegPath,
      ffprobePath: editable.transmux.ffprobePath.trim() || baseline.transmux.ffprobePath,
    },
    playback: {
      preferredPlayer: editable.playback?.preferredPlayer || baseline.playback?.preferredPlayer || 'native',
      preferredAudioLanguage:
        editable.playback?.preferredAudioLanguage?.trim() || baseline.playback?.preferredAudioLanguage || undefined,
      preferredSubtitleLanguage:
        editable.playback?.preferredSubtitleLanguage?.trim() ||
        baseline.playback?.preferredSubtitleLanguage ||
        undefined,
      preferredSubtitleMode:
        editable.playback?.preferredSubtitleMode || baseline.playback?.preferredSubtitleMode || undefined,
      useLoadingScreen: editable.playback?.useLoadingScreen ?? baseline.playback?.useLoadingScreen ?? false,
      subtitleSize: (() => {
        const val = editable.playback?.subtitleSize;
        if (typeof val === 'number') return val;
        if (typeof val === 'string') {
          const parsed = parseFloat(val);
          return isNaN(parsed) ? (baseline.playback?.subtitleSize ?? 1.0) : Math.round(parsed * 100) / 100;
        }
        return baseline.playback?.subtitleSize ?? 1.0;
      })(),
    },
    live: {
      playlistUrl: editable.live?.playlistUrl?.trim() || baseline.live?.playlistUrl || '',
      playlistCacheTtlHours: toNumber(
        editable.live?.playlistCacheTtlHours,
        baseline.live?.playlistCacheTtlHours ?? 24,
        'Live playlist cache TTL (hours)',
      ),
    },
    homeShelves: {
      shelves: editable.homeShelves?.shelves ?? baseline.homeShelves?.shelves ?? [],
    },
    filtering: {
      maxSizeMovieGb: toNumber(
        editable.filtering?.maxSizeMovieGb ?? '0',
        baseline.filtering?.maxSizeMovieGb ?? 0,
        'Max movie size (GB)',
      ),
      maxSizeEpisodeGb: toNumber(
        editable.filtering?.maxSizeEpisodeGb ?? '0',
        baseline.filtering?.maxSizeEpisodeGb ?? 0,
        'Max episode size (GB)',
      ),
      excludeHdr: editable.filtering?.excludeHdr ?? baseline.filtering?.excludeHdr ?? false,
      prioritizeHdr: editable.filtering?.prioritizeHdr ?? baseline.filtering?.prioritizeHdr ?? true,
      filterOutTerms: (editable.filtering?.filterOutTerms ?? '')
        .split(',')
        .map((term) => term.trim())
        .filter((term) => term.length > 0),
    },
  };
};

// Animated wrapper for shelf items that pulses when triggered
const AnimatedShelfItem = ({
  children,
  pulseKey,
  pulseIntensity = 'primary',
  style,
}: {
  children: React.ReactNode;
  pulseKey: number;
  pulseIntensity?: 'primary' | 'secondary';
  style: ViewStyle[];
}) => {
  const scale = useSharedValue(1);
  const prevPulseKey = useRef(pulseKey);

  useEffect(() => {
    if (pulseKey > prevPulseKey.current) {
      const targetScale = pulseIntensity === 'primary' ? 1.05 : 1.02;
      scale.value = withSequence(withTiming(targetScale, { duration: 150 }), withTiming(1, { duration: 200 }));
    }
    prevPulseKey.current = pulseKey;
  }, [pulseKey, pulseIntensity, scale]);

  const animatedStyle = useAnimatedStyle(() => ({
    transform: [{ scale: scale.value }],
  }));

  return <Animated.View style={[...style, animatedStyle]}>{children}</Animated.View>;
};

function SettingsScreen() {
  const theme = useTheme();
  const { showToast } = useToast();
  const { account, logout } = useAuth();
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const styles = useMemo(
    () => createStyles(theme, screenWidth, screenHeight) as unknown as CompatibleStyles,
    [theme, screenWidth, screenHeight],
  );
  const isFocused = useIsFocused();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  // TV Text Input Modal state
  const [textInputModal, setTextInputModal] = useState<{
    visible: boolean;
    label: string;
    value: string;
    fieldKey: string;
    options?: TextInputOptions;
  }>({ visible: false, label: '', value: '', fieldKey: '' });
  const [logUrlModalVisible, setLogUrlModalVisible] = useState(false);
  const { pendingPinUserId } = useUserProfiles();
  const isActive = isFocused && !isMenuOpen && !textInputModal.visible && !pendingPinUserId && !logUrlModalVisible;
  const [activeTab, setActiveTab] = useState<SettingsTab>('connection');
  const [backendVersion, setBackendVersion] = useState<string | null>(null);
  const [clientId, setClientId] = useState<string | null>(null);

  // Easter egg: Konami code activates space shooter game
  const [showSpaceShooter, setShowSpaceShooter] = useState(false);
  const KONAMI_DEBUG = false; // Set to true to show debug overlay
  const { onTouchStart, onTouchEnd, debugInfo } = useKonamiCode(() => {
    setShowSpaceShooter(true);
  }, KONAMI_DEBUG);

  // Fetch backend version and client ID on mount
  useEffect(() => {
    // Debug logging for backend connection diagnostics
    const baseUrl = apiService.getBaseUrl();
    const imageProxyBaseUrl = baseUrl.replace(/\/api$/, '');
    console.log('[Settings:Debug] API Configuration:', {
      baseUrl,
      imageProxyBaseUrl,
      authToken: apiService.getAuthToken() ? '[SET]' : '[NOT SET]',
    });

    // Test backend version endpoint
    apiService
      .getBackendVersion()
      .then((res) => {
        console.log('[Settings:Debug] Backend connection SUCCESS:', {
          version: res.version,
          baseUrl,
        });
        setBackendVersion(res.version);
      })
      .catch((err) => {
        console.error('[Settings:Debug] Backend connection FAILED:', {
          error: err?.message || String(err),
          baseUrl,
          status: err?.status,
        });
        setBackendVersion(null);
      });

    // Test image proxy endpoint with a known TMDB image
    const testImageUrl = 'https://image.tmdb.org/t/p/w92/wwemzKWzjKYJFfCeiB57q3r4Bcm.png'; // TMDB logo
    const proxyTestUrl = `${imageProxyBaseUrl}/api/images/proxy?url=${encodeURIComponent(testImageUrl)}&w=92&q=80`;
    console.log('[Settings:Debug] Testing image proxy:', { proxyTestUrl });

    fetch(proxyTestUrl)
      .then((res) => {
        if (res.ok) {
          console.log('[Settings:Debug] Image proxy test SUCCESS:', {
            status: res.status,
            cacheHeader: res.headers.get('X-Cache'),
            contentType: res.headers.get('Content-Type'),
          });
        } else {
          res.text().then((body) => {
            console.warn('[Settings:Debug] Image proxy test FAILED:', {
              status: res.status,
              statusText: res.statusText,
              body: body?.substring(0, 200),
              proxyTestUrl,
            });
          });
        }
      })
      .catch((err) => {
        console.error('[Settings:Debug] Image proxy test ERROR:', {
          error: err?.message || String(err),
          proxyTestUrl,
        });
      });

    getClientId()
      .then((id) => setClientId(id))
      .catch(() => setClientId(null));
  }, []);

  // Ping polling for device identification
  const pingFlashOpacity = useSharedValue(0);
  const pingFlashStyle = useAnimatedStyle(() => ({
    opacity: pingFlashOpacity.value,
  }));

  useEffect(() => {
    if (!isFocused) return;

    let mounted = true;
    let intervalId: ReturnType<typeof setInterval>;

    const checkForPing = async () => {
      try {
        const clientId = await getClientId();
        const response = await apiService.checkClientPing(clientId);
        if (mounted && response.ping) {
          // Flash the screen and show toast
          showToast('📍 This device was pinged from the admin panel!');
          pingFlashOpacity.value = withSequence(
            withTiming(0.4, { duration: 100 }),
            withTiming(0, { duration: 100 }),
            withTiming(0.4, { duration: 100 }),
            withTiming(0, { duration: 100 }),
            withTiming(0.4, { duration: 100 }),
            withTiming(0, { duration: 300 }),
          );
        }
      } catch {
        // Silently ignore ping check errors
      }
    };

    // Poll every 3 seconds
    intervalId = setInterval(checkForPing, 3000);
    // Also check immediately
    checkForPing();

    return () => {
      mounted = false;
      clearInterval(intervalId);
    };
  }, [isFocused, showToast, pingFlashOpacity]);

  const tabs = useMemo<TabOption[]>(
    () => [
      { key: 'connection', label: 'Backend' },
      // { key: 'content', label: 'Content Sources' },  // Hidden for now
      // { key: 'playback', label: 'Playback' },  // Hidden for now
      // { key: 'home', label: 'Home Screen' },  // Hidden for now
      // { key: 'filtering', label: 'Filtering' },  // Hidden for now
      // { key: 'advanced', label: 'Advanced' },  // Hidden for now
      // { key: 'live', label: 'Live TV' },  // Hidden for now
    ],
    [],
  );
  const { backendUrl, loading, saving, settings, refreshSettings, setBackendUrl, updateBackendSettings } =
    useBackendSettings();
  const [backendUrlInput, setBackendUrlInput] = useState(backendUrl);

  // TV inline text input refs and state
  const backendUrlInputRef = useRef<TextInput>(null);
  const tempBackendUrlRef = useRef(backendUrl);

  // Sync temp refs with state
  useEffect(() => {
    tempBackendUrlRef.current = backendUrlInput;
  }, [backendUrlInput]);

  const [editableSettings, setEditableSettings] = useState<EditableBackendSettings | null>(
    settings ? toEditableSettings(settings) : null,
  );
  const [dirty, setDirty] = useState(false);

  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const [isSubmittingLogs, setIsSubmittingLogs] = useState(false);
  const [logUrl, setLogUrl] = useState<string | null>(null);
  const [updateStatus, setUpdateStatus] = useState<'idle' | 'checking' | 'downloading' | 'ready'>('idle');
  const [githubRelease, setGithubRelease] = useState<{ version: string; url: string } | null>(null);
  const [isRefreshing, setIsRefreshing] = useState(false);

  // Handle left key to open menu when spatial nav can't move further left
  const handleDirectionWithoutMovement = useCallback(
    (direction: string) => {
      if (direction === 'left' && !isMenuOpen) {
        openMenu();
      }
    },
    [isMenuOpen, openMenu],
  );

  // Back button interceptor for log URL modal
  const logModalCloseRef = useRef(() => {
    setLogUrlModalVisible(false);
    setLogUrl(null);
  });
  const logModalInterceptorRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (!logUrlModalVisible) {
      // Clean up interceptor when modal is hidden
      if (logModalInterceptorRef.current) {
        logModalInterceptorRef.current();
        logModalInterceptorRef.current = null;
      }
      return;
    }

    // Install interceptor when modal is shown
    let isHandling = false;
    let cleanupScheduled = false;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      if (isHandling) {
        return true;
      }

      isHandling = true;
      logModalCloseRef.current();

      if (!cleanupScheduled) {
        cleanupScheduled = true;
        setTimeout(() => {
          if (logModalInterceptorRef.current) {
            logModalInterceptorRef.current();
            logModalInterceptorRef.current = null;
          }
          isHandling = false;
        }, 750);
      }

      return true;
    });

    logModalInterceptorRef.current = removeInterceptor;

    return () => {
      // Cleanup on unmount
    };
  }, [logUrlModalVisible]);

  const clearFieldError = useCallback((key: string) => {
    setFieldErrors((current) => {
      if (!current[key]) {
        return current;
      }
      const next = { ...current };
      delete next[key];
      return next;
    });
  }, []);

  const clearErrors = useCallback(() => {
    setFieldErrors((current) => (Object.keys(current).length === 0 ? current : {}));
  }, []);

  useEffect(() => {
    setBackendUrlInput(backendUrl);
  }, [backendUrl]);

  const isBackendReachable = !!settings;

  useEffect(() => {
    if (settings) {
      setEditableSettings(toEditableSettings(settings));
      setDirty(false);
      clearErrors();
    }
  }, [settings, clearErrors]);

  const handleBackendConnectionApply = useCallback(async () => {
    try {
      await setBackendUrl(backendUrlInput);
      // URL saved successfully - now try to refresh settings
      try {
        await refreshSettings();
        showToast('Backend connection saved.', { tone: 'success' });
      } catch {
        // URL was saved but couldn't connect - still show success since URL is saved
        showToast('Server URL saved. Connection will be attempted on next login.', { tone: 'success' });
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to save backend URL';
      showToast(message, { tone: 'danger' });
    }
  }, [backendUrlInput, setBackendUrl, refreshSettings, showToast]);

  const handleReloadSettings = useCallback(async () => {
    setIsRefreshing(true);
    try {
      await refreshSettings();
      showToast('Settings refreshed', { tone: 'success' });
    } catch {
      showToast('Failed to refresh settings', { tone: 'danger' });
    } finally {
      setIsRefreshing(false);
    }
  }, [refreshSettings, showToast]);

  // TV Text Input Modal handlers
  const openTextInputModal = useCallback(
    (label: string, value: string, fieldKey: string, options?: TextInputOptions) => {
      setTextInputModal({ visible: true, label, value, fieldKey, options });
    },
    [],
  );

  const closeTextInputModal = useCallback(() => {
    setTextInputModal((prev) => ({ ...prev, visible: false }));
  }, []);

  const handleTextInputSubmit = useCallback(
    (newValue: string) => {
      const { fieldKey } = textInputModal;
      // Handle different field keys
      if (fieldKey === 'backendUrl') {
        setBackendUrlInput(newValue);
      } else if (fieldKey.startsWith('playback.') && editableSettings) {
        const subKey = fieldKey.replace('playback.', '');
        setEditableSettings({
          ...editableSettings,
          playback: { ...editableSettings.playback, [subKey]: newValue },
        });
        setDirty(true);
      } else if (fieldKey.startsWith('filtering.') && editableSettings) {
        const subKey = fieldKey.replace('filtering.', '');
        setEditableSettings({
          ...editableSettings,
          filtering: { ...editableSettings.filtering, [subKey]: newValue },
        });
        setDirty(true);
      }
      closeTextInputModal();
    },
    [textInputModal, editableSettings, closeTextInputModal],
  );

  const handleSaveSettings = useCallback(async () => {
    if (!settings || !editableSettings) {
      return;
    }

    const numericErrors: Record<string, string> = {};

    const validateInteger = (
      value: string | undefined,
      label: string,
      key: string,
      bounds?: { min?: number; max?: number },
    ) => {
      const trimmed = typeof value === 'string' ? value.trim() : '';
      if (!trimmed) {
        numericErrors[key] = `${label} is required`;
        return null;
      }
      const parsed = Number.parseInt(trimmed, 10);
      if (Number.isNaN(parsed)) {
        numericErrors[key] = `${label} must be a number`;
        return null;
      }
      if (bounds?.min !== undefined && parsed < bounds.min) {
        numericErrors[key] = `${label} must be at least ${bounds.min}`;
        return null;
      }
      if (bounds?.max !== undefined && parsed > bounds.max) {
        numericErrors[key] = `${label} must be at most ${bounds.max}`;
        return null;
      }
      return parsed;
    };

    // Save to global settings endpoint
    const serverPort = validateInteger(editableSettings.server.port, 'Server port', 'server.port', {
      min: 1,
      max: 65535,
    });

    // Validate each usenet provider
    let hasInvalidUsenet = false;
    editableSettings.usenet.forEach((provider, idx) => {
      const portResult = validateInteger(provider.port, `Usenet provider ${idx + 1} port`, `usenet.${idx}.port`, {
        min: 1,
        max: 65535,
      });
      const connectionsResult = validateInteger(
        provider.connections,
        `Usenet provider ${idx + 1} connections`,
        `usenet.${idx}.connections`,
        { min: 1, max: 100 },
      );
      if (portResult === null || connectionsResult === null) {
        hasInvalidUsenet = true;
      }
    });

    const metadataTtl = validateInteger(
      editableSettings.cache.metadataTtlHours,
      'Metadata TTL (hours)',
      'cache.metadataTtlHours',
      { min: 1 },
    );
    const maxDownloadWorkers = validateInteger(
      editableSettings.streaming.maxDownloadWorkers,
      'Streaming max download workers',
      'streaming.maxDownloadWorkers',
      { min: 1, max: 100 },
    );
    const maxCacheSize = validateInteger(
      editableSettings.streaming.maxCacheSizeMB,
      'Streaming cache size (MB)',
      'streaming.maxCacheSizeMB',
      { min: 1 },
    );

    if (
      serverPort === null ||
      hasInvalidUsenet ||
      metadataTtl === null ||
      maxDownloadWorkers === null ||
      maxCacheSize === null
    ) {
      setFieldErrors(numericErrors);
      showToast('Fix the highlighted fields and try again.', { tone: 'danger' });
      return;
    }

    clearErrors();

    try {
      const payload = toBackendPayload(editableSettings, settings);
      await updateBackendSettings(payload);
      showToast('Backend settings updated.', { tone: 'success' });
      setDirty(false);
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to update backend settings';
      showToast(message, { tone: 'danger' });
    }
  }, [clearErrors, editableSettings, settings, showToast, updateBackendSettings]);

  const handleSubmitLogs = useCallback(async () => {
    if (isSubmittingLogs) return;

    setIsSubmittingLogs(true);
    showToast('Submitting logs...', { tone: 'info' });

    try {
      const frontendLogs = logger.getLogsAsString();
      const result = await apiService.submitLogs(frontendLogs);

      if (result.error) {
        showToast(`Failed to submit logs: ${result.error}`, { tone: 'danger', duration: 8000 });
      } else if (result.url) {
        setLogUrl(result.url);
        if (Platform.isTV) {
          // On TV, show QR code modal for easy scanning
          setLogUrlModalVisible(true);
        } else {
          showToast('Logs submitted successfully!', { tone: 'success' });
        }
      } else {
        showToast('Logs submitted successfully', { tone: 'success' });
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to submit logs';
      showToast(message, { tone: 'danger', duration: 8000 });
    } finally {
      setIsSubmittingLogs(false);
    }
  }, [isSubmittingLogs, showToast]);

  const handleCheckForUpdates = useCallback(async () => {
    const Updates = getUpdates();
    if (!Updates) {
      showToast('Updates not available in this build', { tone: 'info' });
      return;
    }
    if (__DEV__) {
      showToast('Updates disabled in development mode', { tone: 'info' });
      return;
    }
    if (updateStatus === 'checking' || updateStatus === 'downloading') return;

    setUpdateStatus('checking');
    try {
      const update = await Updates.checkForUpdateAsync();
      if (update.isAvailable) {
        setUpdateStatus('downloading');
        showToast('Downloading update...', { tone: 'info' });
        await Updates.fetchUpdateAsync();
        setUpdateStatus('ready');
        showToast('Update ready - tap to restart', { tone: 'success' });
      } else {
        showToast('App is up to date', { tone: 'success' });
        setUpdateStatus('idle');
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to check for updates';
      showToast(message, { tone: 'danger' });
      setUpdateStatus('idle');
    }
  }, [updateStatus, showToast]);

  const handleApplyUpdate = useCallback(async () => {
    const Updates = getUpdates();
    if (!Updates) {
      showToast('Updates not available in this build', { tone: 'info' });
      return;
    }
    try {
      await Updates.reloadAsync();
    } catch (err) {
      showToast('Failed to restart app', { tone: 'danger' });
    }
  }, [showToast]);

  // Compare semantic versions: returns true if remote > local
  const isNewerVersion = useCallback((local: string, remote: string): boolean => {
    const parseVersion = (v: string) => {
      const match = v.match(/(\d+)\.(\d+)\.(\d+)/);
      if (!match) return [0, 0, 0];
      return [parseInt(match[1], 10), parseInt(match[2], 10), parseInt(match[3], 10)];
    };
    const [localMajor, localMinor, localPatch] = parseVersion(local);
    const [remoteMajor, remoteMinor, remotePatch] = parseVersion(remote);
    if (remoteMajor > localMajor) return true;
    if (remoteMajor < localMajor) return false;
    if (remoteMinor > localMinor) return true;
    if (remoteMinor < localMinor) return false;
    return remotePatch > localPatch;
  }, []);

  // Check GitHub releases for newer version (Android only)
  const handleCheckGitHubReleases = useCallback(async () => {
    if (updateStatus === 'checking') return;
    setUpdateStatus('checking');
    setGithubRelease(null);
    try {
      const response = await fetch('https://api.github.com/repos/godver3/strmr/releases/latest');
      if (!response.ok) {
        throw new Error('Failed to fetch releases');
      }
      const data = await response.json();
      const tagName = data.tag_name || '';
      // Remove 'v' prefix if present
      const remoteVersion = tagName.replace(/^v/, '');
      if (isNewerVersion(APP_VERSION, remoteVersion)) {
        setGithubRelease({ version: remoteVersion, url: data.html_url });
        showToast(`New version ${remoteVersion} available!`, { tone: 'success' });
      } else {
        showToast('App is up to date', { tone: 'success' });
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to check for updates';
      showToast(message, { tone: 'danger' });
    } finally {
      setUpdateStatus('idle');
    }
  }, [updateStatus, showToast, isNewerVersion]);

  const handleOpenGitHubRelease = useCallback(() => {
    if (githubRelease?.url) {
      Linking.openURL(githubRelease.url);
    }
  }, [githubRelease]);

  const _updateServerField = useCallback(
    (field: keyof EditableBackendSettings['server']) => (value: string) => {
      setDirty(true);
      clearFieldError(`server.${field}`);
      setEditableSettings((current) =>
        current
          ? {
              ...current,
              server: {
                ...current.server,
                [field]: value,
              },
            }
          : current,
      );
    },
    [clearFieldError],
  );

  const updateMetadataField = useCallback(
    (field: keyof EditableBackendSettings['metadata']) => (value: string) => {
      setDirty(true);
      setEditableSettings((current) =>
        current
          ? {
              ...current,
              metadata: {
                ...current.metadata,
                [field]: value,
              },
            }
          : current,
      );
    },
    [],
  );

  const updateStreamingNumericField = useCallback(
    (field: 'maxDownloadWorkers' | 'maxCacheSizeMB') => (value: string) => {
      setDirty(true);
      clearFieldError(`streaming.${field}`);
      setEditableSettings((current) =>
        current
          ? {
              ...current,
              streaming: {
                ...current.streaming,
                [field]: value,
              },
            }
          : current,
      );
    },
    [clearFieldError],
  );

  const _updateCacheField = useCallback(
    (field: keyof EditableBackendSettings['cache']) => (value: string) => {
      setDirty(true);
      clearFieldError(`cache.${field}`);
      setEditableSettings((current) =>
        current
          ? {
              ...current,
              cache: {
                ...current.cache,
                [field]: value,
              },
            }
          : current,
      );
    },
    [clearFieldError],
  );

  const _updateWebDavField = useCallback(
    (field: keyof EditableBackendSettings['webdav']) => (value: string | boolean) => {
      setDirty(true);
      setEditableSettings((current) =>
        current
          ? {
              ...current,
              webdav: {
                ...current.webdav,
                [field]: value,
              },
            }
          : current,
      );
    },
    [],
  );

  const _updateTransmuxField = useCallback(
    (field: keyof EditableBackendSettings['transmux']) => (value: string | boolean) => {
      setDirty(true);
      setEditableSettings((current) =>
        current
          ? {
              ...current,
              transmux: {
                ...current.transmux,
                [field]: value,
              },
            }
          : current,
      );
    },
    [],
  );

  const busy = saving || loading;

  // TV Grid Data Builders
  const connectionGridData = useMemo<SettingsGridItem[]>(
    () => [
      {
        type: 'header',
        id: 'connection-header',
        title: 'Server',
        description: `Connected to ${backendUrl || 'backend'}. Server settings can be configured via the web UI at ${backendUrl ? backendUrl.replace(/\/api\/?$/, '/admin') : '<backend-url>/admin'}.`,
      },
      {
        type: 'header',
        id: 'about-header',
        title: 'About',
      },
      {
        type: 'version-info',
        id: 'version-info',
      },
      {
        type: 'header',
        id: 'support-header',
        title: 'Support',
        description: 'Submit logs to help diagnose issues. The URL can be shared with the developer.',
      },
      {
        type: 'button',
        id: 'submit-logs',
        label: isSubmittingLogs ? 'Submitting...' : 'Submit Logs',
        action: 'submit-logs',
        disabled: isSubmittingLogs,
      },
      {
        type: 'header',
        id: 'account-header',
        title: 'Account',
        description: account ? `Signed in as ${account.username}${account.isMaster ? ' (Admin)' : ''}` : undefined,
      },
      {
        type: 'button-row',
        id: 'account-buttons',
        buttons: [
          {
            label: isRefreshing ? 'Reloading...' : 'Reload',
            action: 'reload',
            disabled: isRefreshing,
          },
          {
            label: 'Sign Out',
            action: 'sign-out',
          },
        ],
      },
    ],
    [backendUrl, isSubmittingLogs, account, isRefreshing],
  );

  // Get current tab grid data - only connection tab is active
  const currentTabGridData = useMemo<SettingsGridItem[]>(() => {
    return connectionGridData;
  }, [connectionGridData]);

  // TV Grid action handler
  const handleGridAction = useCallback(
    (action: string) => {
      switch (action) {
        case 'connection-apply':
          void handleBackendConnectionApply();
          break;
        case 'save-settings':
          void handleSaveSettings();
          break;
        case 'submit-logs':
          void handleSubmitLogs();
          break;
        case 'sign-out':
          void (async () => {
            try {
              await logout();
              showToast('Signed out successfully', { tone: 'success' });
            } catch (err) {
              showToast('Failed to sign out', { tone: 'danger' });
            }
          })();
          break;
        case 'reload':
          void handleReloadSettings();
          break;
      }
    },
    [handleBackendConnectionApply, handleSaveSettings, handleSubmitLogs, showToast, logout, handleReloadSettings],
  );

  // TV Grid field update handler - currently unused as connection tab has no field updates
  const handleGridFieldUpdate = useCallback((_fieldKey: string, _value: string | boolean | number) => {
    // No field updates needed for connection tab
  }, []);

  // TV Grid render item
  const renderGridItem = useCallback(
    ({ item }: { item: SettingsGridItem }) => {
      switch (item.type) {
        case 'header': {
          const headerContent = (
            <View style={[styles.tvGridHeader, styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              {item.title ? <Text style={styles.tvGridHeaderTitle}>{item.title}</Text> : null}
              {item.description && <Text style={styles.tvGridHeaderDescription}>{item.description}</Text>}
            </View>
          );
          return headerContent;
        }

        case 'text-field': {
          // Inline text input configuration for backend URL field
          if (item.fieldKey === 'backendUrl') {
            const handleInlineBlur = () => {
              if (Platform.isTV) {
                setBackendUrlInput(tempBackendUrlRef.current);
              }
            };

            const handleInlineChangeText = (text: string) => {
              if (Platform.isTV) {
                tempBackendUrlRef.current = text;
              } else {
                setBackendUrlInput(text);
              }
            };

            return (
              <View style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
                <SpatialNavigationFocusableView
                  onSelect={() => {
                    backendUrlInputRef.current?.focus();
                  }}>
                  {({ isFocused }: { isFocused: boolean }) => (
                    <View style={[styles.tvGridInlineInputRow, isFocused && styles.tvGridInlineInputRowFocused]}>
                      <Text style={styles.tvGridInlineInputLabel}>{item.label}</Text>
                      <TextInput
                        ref={backendUrlInputRef}
                        style={[styles.tvGridInlineInput, isFocused && styles.tvGridInlineInputFocused]}
                        {...(Platform.isTV ? { defaultValue: item.value } : { value: item.value })}
                        onChangeText={handleInlineChangeText}
                        onBlur={handleInlineBlur}
                        placeholder={item.options?.placeholder}
                        placeholderTextColor={theme.colors.text.muted}
                        keyboardType={item.options?.keyboardType ?? 'default'}
                        secureTextEntry={item.options?.secureTextEntry}
                        autoCapitalize="none"
                        autoCorrect={false}
                        autoComplete="off"
                        textContentType="none"
                        spellCheck={false}
                        underlineColorAndroid="transparent"
                        importantForAutofill="no"
                        disableFullscreenUI={true}
                        {...(Platform.OS === 'ios' &&
                          Platform.isTV && {
                            keyboardAppearance: 'dark',
                          })}
                      />
                    </View>
                  )}
                </SpatialNavigationFocusableView>
              </View>
            );
          }

          // For other text fields, keep the modal approach
          return (
            <View style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <SpatialNavigationFocusableView
                onSelect={() => openTextInputModal(item.label, item.value, item.fieldKey, item.options)}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <View style={[styles.tvGridFieldRow, isFocused && styles.tvGridFieldRowFocused]}>
                    <Text style={styles.tvGridFieldLabel}>{item.label}</Text>
                    <Text
                      style={[styles.tvGridFieldValue, !item.value && styles.tvGridFieldValuePlaceholder]}
                      numberOfLines={1}>
                      {item.value || item.options?.placeholder || 'Not set'}
                    </Text>
                  </View>
                )}
              </SpatialNavigationFocusableView>
            </View>
          );
        }

        case 'toggle':
          return (
            <View style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <SpatialNavigationFocusableView
                onSelect={() => handleGridFieldUpdate(item.fieldKey, !item.value)}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <View style={[styles.tvGridToggleRow, isFocused && styles.tvGridToggleRowFocused]}>
                    <Text style={styles.tvGridToggleLabelText}>{item.label}</Text>
                    <View
                      style={[
                        styles.tvGridCustomToggle,
                        {
                          backgroundColor: item.value ? theme.colors.accent.primary : theme.colors.border.emphasis,
                        },
                        isFocused && {
                          transform: [{ scale: 1.1 }],
                          borderWidth: 2,
                          borderColor: theme.colors.text.primary,
                        },
                      ]}>
                      <View
                        style={[styles.tvGridCustomToggleThumb, { alignSelf: item.value ? 'flex-end' : 'flex-start' }]}
                      />
                    </View>
                  </View>
                )}
              </SpatialNavigationFocusableView>
            </View>
          );

        case 'dropdown':
          return (
            <View style={[styles.tvGridDropdownRowInline, styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <Text style={styles.tvGridInlineInputLabel}>{item.label}</Text>
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.tvGridDropdownOptionsInline}>
                  {item.options.map((option) => (
                    <SpatialNavigationFocusableView
                      key={option.value}
                      onSelect={() => handleGridFieldUpdate(item.fieldKey, option.value)}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <View
                          style={[
                            styles.dropdownOption,
                            item.value === option.value && styles.dropdownOptionSelected,
                            isFocused && styles.dropdownOptionFocused,
                          ]}>
                          <Text
                            style={[
                              styles.dropdownOptionText,
                              isFocused && styles.dropdownOptionTextFocused,
                            ]}>
                            {option.label}
                          </Text>
                        </View>
                      )}
                    </SpatialNavigationFocusableView>
                  ))}
                </View>
              </SpatialNavigationNode>
            </View>
          );

        case 'button':
          return (
            <View style={[styles.tvGridButtonRow, styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <SpatialNavigationFocusableView
                onSelect={() => !item.disabled && handleGridAction(item.action)}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <View
                    style={[
                      styles.spatialButton,
                      isFocused && styles.spatialButtonFocused,
                      item.disabled && styles.spatialButtonDisabled,
                    ]}>
                    <Text
                      style={[
                        styles.spatialButtonText,
                        isFocused && styles.spatialButtonTextFocused,
                      ]}
                      numberOfLines={1}>
                      {item.label}
                    </Text>
                  </View>
                )}
              </SpatialNavigationFocusableView>
            </View>
          );

        case 'button-row':
          return (
            <View style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.tvGridButtonRow}>
                  {item.buttons.map((btn) => (
                    <SpatialNavigationFocusableView
                      key={btn.action}
                      onSelect={() => !btn.disabled && handleGridAction(btn.action)}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <View
                          style={[
                            styles.spatialButton,
                            isFocused && styles.spatialButtonFocused,
                            btn.disabled && styles.spatialButtonDisabled,
                          ]}>
                          <Text
                            style={[
                              styles.spatialButtonText,
                              isFocused && styles.spatialButtonTextFocused,
                            ]}
                            numberOfLines={1}>
                            {btn.label}
                          </Text>
                        </View>
                      )}
                    </SpatialNavigationFocusableView>
                  ))}
                </View>
              </SpatialNavigationNode>
            </View>
          );

        case 'version-info': {
          const versionString = APP_VERSION;
          const isAndroid = Platform.OS === 'android';
          const updateButtonText = isAndroid
            ? githubRelease
              ? `Download ${githubRelease.version}`
              : updateStatus === 'checking'
                ? 'Checking...'
                : 'Check for Updates'
            : updateStatus === 'checking'
              ? 'Checking...'
              : updateStatus === 'downloading'
                ? 'Downloading...'
                : updateStatus === 'ready'
                  ? 'Restart to Apply'
                  : 'Check for Frontend Updates';

          const handleUpdatePress = isAndroid
            ? githubRelease
              ? handleOpenGitHubRelease
              : handleCheckGitHubReleases
            : updateStatus === 'ready'
              ? handleApplyUpdate
              : handleCheckForUpdates;

          return (
            <View style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing, styles.versionInfoContainer]}>
              <View style={styles.versionInfoRow}>
                <Text style={styles.versionInfoLabel}>Frontend</Text>
                <Text style={styles.versionInfoValue}>{versionString}</Text>
              </View>
              <View style={styles.versionInfoRow}>
                <Text style={styles.versionInfoLabel}>Backend</Text>
                <Text style={styles.versionInfoValue}>{backendVersion ?? 'Unknown'}</Text>
              </View>
              <View style={styles.versionInfoRow}>
                <Text style={styles.versionInfoLabel}>Device ID</Text>
                <Text style={styles.deviceIdValueTV} numberOfLines={1} adjustsFontSizeToFit minimumFontScale={0.6}>
                  {clientId ?? 'Loading...'}
                </Text>
              </View>
              <View style={styles.versionButtonContainer}>
                <DefaultFocus>
                  <SpatialNavigationFocusableView
                    onSelect={() => {
                      if (updateStatus !== 'checking' && updateStatus !== 'downloading') {
                        handleUpdatePress();
                      }
                    }}>
                    {({ isFocused }: { isFocused: boolean }) => (
                      <View
                        style={[
                          styles.spatialButton,
                          styles.debugButton,
                          isFocused && styles.spatialButtonFocused,
                          (updateStatus === 'checking' || updateStatus === 'downloading') && styles.spatialButtonDisabled,
                        ]}>
                        <Text
                          style={[
                            styles.spatialButtonText,
                            isFocused && styles.spatialButtonTextFocused,
                          ]}
                          numberOfLines={1}>
                          {updateButtonText}
                        </Text>
                      </View>
                    )}
                  </SpatialNavigationFocusableView>
                </DefaultFocus>
                <View style={styles.backendInfoNote}>
                  <Ionicons name="information-circle-outline" size={18} color={theme.colors.text.muted} />
                  <Text style={styles.backendInfoNoteText}>
                    {isAndroid
                      ? 'Backend is updated independently via Docker'
                      : 'App updates via TestFlight. Backend updated via Docker.'}
                  </Text>
                </View>
              </View>
            </View>
          );
        }

        default:
          return null;
      }
    },
    [
      styles,
      theme,
      openTextInputModal,
      handleGridFieldUpdate,
      handleGridAction,
      setBackendUrlInput,
      backendVersion,
      clientId,
      updateStatus,
      handleCheckForUpdates,
      handleApplyUpdate,
      handleCheckGitHubReleases,
      handleOpenGitHubRelease,
      githubRelease,
    ],
  );

  return (
    <>
      {/* Ping flash overlay for device identification */}
      <Animated.View
        style={[
          {
            position: 'absolute',
            top: 0,
            left: 0,
            right: 0,
            bottom: 0,
            backgroundColor: theme.colors.accent.primary,
            zIndex: 9999,
            pointerEvents: 'none',
          },
          pingFlashStyle,
        ]}
      />
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        {/* TV Layout: Header at top, then grid below - wrapped in SpatialNavigationRoot */}
        {Platform.isTV && (
          <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={handleDirectionWithoutMovement}>
            <View style={styles.tvLayoutContainer}>
              {/* Header Section - at top of screen */}
              <View style={styles.tvHeader}>
                <Text style={styles.tvScreenTitle}>Settings</Text>
              </View>

              {/* Grid Content - with edge buffer */}
              <View style={styles.tvContentArea}>
                {currentTabGridData.length > 0 && (
                  <SpatialNavigationScrollView horizontal={false} style={styles.tvGridContainer}>
                    <SpatialNavigationNode orientation="vertical">
                      {currentTabGridData.map((item) => (
                        <View key={item.id}>{renderGridItem({ item })}</View>
                      ))}
                    </SpatialNavigationNode>
                  </SpatialNavigationScrollView>
                )}
              </View>
            </View>
          </SpatialNavigationRoot>
        )}
        {/* Mobile Layout: ScrollView with all content */}
        {!Platform.isTV && (
          <View style={styles.mobileContainer} onTouchStart={onTouchStart} onTouchEnd={onTouchEnd}>
            <ScrollView
              style={styles.container}
              contentContainerStyle={styles.contentContainer}
              contentInsetAdjustmentBehavior="never"
              automaticallyAdjustContentInsets={false}>
              <Text style={styles.screenTitle}>Settings</Text>

              {/* Mobile Content - Connection Tab */}
              {!Platform.isTV && activeTab === 'connection' && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Server</Text>
                  <Text style={styles.sectionDescription}>
                    Change the server URL below if you need to connect to a different backend.
                  </Text>
                  <View style={styles.fieldRow}>
                    <Text style={styles.fieldLabel}>Server URL</Text>
                    <TextInput
                      value={backendUrlInput.replace(/\/api$/, '')}
                      onChangeText={(text) => setBackendUrlInput(text.replace(/\/api$/, '') + '/api')}
                      placeholder="http://192.168.1.100:7777"
                      placeholderTextColor={theme.colors.text.muted}
                      autoCapitalize="none"
                      autoCorrect={false}
                      style={styles.input}
                    />
                  </View>
                  <FocusablePressable
                    text={saving ? 'Connecting...' : 'Apply'}
                    onSelect={handleBackendConnectionApply}
                    disabled={saving || backendUrlInput === backendUrl}
                    style={[styles.debugButton, { marginTop: 12 }]}
                  />
                  <Text style={[styles.sectionDescription, { marginTop: 16 }]}>
                    Server settings can be configured via the web UI at{' '}
                    <Text
                      style={styles.linkText}
                      onPress={() => {
                        const adminUrl = backendUrl ? backendUrl.replace(/\/api\/?$/, '/admin') : null;
                        if (adminUrl) {
                          Linking.openURL(adminUrl);
                        }
                      }}>
                      {backendUrl ? backendUrl.replace(/\/api\/?$/, '/admin') : '<backend-url>/admin'}
                    </Text>
                    .
                  </Text>
                </View>
              )}

              {/* App Version Info - shown on Connection tab */}
              {!Platform.isTV && activeTab === 'connection' && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>About</Text>
                  <View style={styles.versionInfoContainer}>
                    <View style={styles.versionInfoRow}>
                      <Text style={styles.versionInfoLabel}>Frontend</Text>
                      <Text style={styles.versionInfoValue}>{APP_VERSION}</Text>
                    </View>
                    <View style={styles.versionInfoRow}>
                      <Text style={styles.versionInfoLabel}>Backend</Text>
                      <Text style={styles.versionInfoValue}>{backendVersion ?? 'Unknown'}</Text>
                    </View>
                    <Pressable
                      style={styles.deviceIdRowMobile}
                      onPress={() => {
                        if (clientId) {
                          Clipboard.setString(clientId);
                          showToast('Device ID copied to clipboard', { tone: 'success' });
                        }
                      }}>
                      <Text style={styles.versionInfoLabel}>Device ID</Text>
                      <Text style={styles.deviceIdValueMobile} numberOfLines={1}>
                        {clientId ?? 'Loading...'}
                      </Text>
                    </Pressable>
                  </View>
                  {Platform.OS === 'android' ? (
                    <>
                      <FocusablePressable
                        text={
                          githubRelease
                            ? `Download ${githubRelease.version}`
                            : updateStatus === 'checking'
                              ? 'Checking...'
                              : 'Check for Updates'
                        }
                        onSelect={githubRelease ? handleOpenGitHubRelease : handleCheckGitHubReleases}
                        disabled={updateStatus === 'checking'}
                        style={[styles.debugButton, { marginTop: 12 }]}
                      />
                      <View style={styles.backendInfoNoteMobile}>
                        <Ionicons name="information-circle-outline" size={16} color={theme.colors.text.muted} />
                        <Text style={styles.backendInfoNoteTextMobile}>
                          Backend is updated independently via Docker
                        </Text>
                      </View>
                    </>
                  ) : (
                    <>
                      <FocusablePressable
                        text={
                          updateStatus === 'checking'
                            ? 'Checking...'
                            : updateStatus === 'downloading'
                              ? 'Downloading...'
                              : updateStatus === 'ready'
                                ? 'Restart to Apply'
                                : 'Check for Frontend Updates'
                        }
                        onSelect={updateStatus === 'ready' ? handleApplyUpdate : handleCheckForUpdates}
                        disabled={updateStatus === 'checking' || updateStatus === 'downloading'}
                        style={[styles.debugButton, { marginTop: 12 }]}
                      />
                      <View style={styles.backendInfoNoteMobile}>
                        <Ionicons name="information-circle-outline" size={16} color={theme.colors.text.muted} />
                        <Text style={styles.backendInfoNoteTextMobile}>
                          App updates via TestFlight. Backend updated via Docker.
                        </Text>
                      </View>
                    </>
                  )}
                </View>
              )}

              {/* Support section - shown on Connection tab */}
              {!Platform.isTV && activeTab === 'connection' && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Support</Text>
                  <Text style={styles.sectionDescription}>
                    Submit logs to help diagnose issues. The URL can be shared with the developer.
                  </Text>
                  <FocusablePressable
                    text={isSubmittingLogs ? 'Submitting...' : 'Submit Logs'}
                    onSelect={handleSubmitLogs}
                    disabled={isSubmittingLogs}
                    style={styles.debugButton}
                  />
                  {logUrl && (
                    <View style={{ marginTop: 16 }}>
                      <View style={{ flexDirection: 'row', alignItems: 'center', gap: 12 }}>
                        <FocusablePressable
                          text="Copy URL"
                          onSelect={() => {
                            Clipboard.setString(logUrl);
                            showToast('URL copied to clipboard', { tone: 'success' });
                          }}
                          style={[styles.debugButton, { flex: 0 }]}
                        />
                        <FocusablePressable
                          text="Clear"
                          onSelect={() => setLogUrl(null)}
                          style={[styles.debugButton, { flex: 0, opacity: 0.7 }]}
                        />
                      </View>
                      <Text
                        style={{
                          marginTop: 12,
                          fontSize: 12,
                          fontFamily: 'monospace',
                          color: theme.colors.text.secondary,
                        }}
                        selectable>
                        {logUrl}
                      </Text>
                    </View>
                  )}
                </View>
              )}

              {/* Account section - shown on Connection tab */}
              {!Platform.isTV && activeTab === 'connection' && (
                <View style={styles.section}>
                  <Text style={styles.sectionTitle}>Account</Text>
                  {account && (
                    <Text style={styles.sectionDescription}>
                      Signed in as {account.username}
                      {account.isMaster ? ' (Admin)' : ''}
                    </Text>
                  )}
                  <View style={{ flexDirection: 'row', gap: 12, marginTop: 12 }}>
                    <FocusablePressable
                      text={isRefreshing ? 'Reloading...' : 'Reload'}
                      onSelect={handleReloadSettings}
                      disabled={isRefreshing}
                      loading={isRefreshing}
                      style={styles.debugButton}
                    />
                    <FocusablePressable
                      text="Sign Out"
                      onSelect={async () => {
                        try {
                          await logout();
                          showToast('Signed out successfully', { tone: 'success' });
                        } catch (err) {
                          showToast('Failed to sign out', { tone: 'danger' });
                        }
                      }}
                      style={styles.debugButton}
                    />
                  </View>
                </View>
              )}

            </ScrollView>
          </View>
        )}
      </FixedSafeAreaView>

      {/* TV Text Input Modal */}
      {Platform.isTV && (
        <TextInputModal
          visible={textInputModal.visible}
          label={textInputModal.label}
          value={textInputModal.value}
          onSubmit={handleTextInputSubmit}
          onCancel={closeTextInputModal}
          options={textInputModal.options}
          styles={styles}
          theme={theme}
        />
      )}

      {/* Log URL QR Code Modal - TV */}
      {Platform.isTV && logUrlModalVisible && logUrl && (
        <View style={styles.tvModalOverlay}>
          <View style={[styles.tvModalContent, { alignItems: 'center', maxWidth: 600 }]}>
            <Text style={[styles.tvModalTitle, { fontSize: 32 }]}>Logs Submitted</Text>
            <Text style={[styles.tvModalSubtitle, { textAlign: 'center', marginBottom: 28, fontSize: 22 }]}>
              Scan the QR code
            </Text>

            <QRCode value={logUrl} size={286} />

            <Text style={[styles.tvModalSubtitle, { marginTop: 28, fontSize: 20, textAlign: 'center' }]}>URL</Text>
            <Text
              style={[
                styles.tvModalSubtitle,
                { marginTop: 8, fontSize: 18, fontFamily: 'monospace', textAlign: 'center' },
              ]}
              selectable>
              {logUrl}
            </Text>

            <View style={[styles.tvModalFooter, { marginTop: 36 }]}>
              <FocusablePressable
                autoFocus
                text="Close"
                onSelect={() => {
                  setLogUrlModalVisible(false);
                  setLogUrl(null);
                }}
                style={styles.tvModalCloseButton}
                focusedStyle={styles.tvModalCloseButtonFocused}
                textStyle={styles.tvModalCloseButtonText}
                focusedTextStyle={styles.tvModalCloseButtonTextFocused}
              />
            </View>
          </View>
        </View>
      )}

      {/* Easter egg: Debug overlay for Konami code */}
      {!Platform.isTV && KONAMI_DEBUG && (
        <View
          style={{
            position: 'absolute',
            top: 100,
            left: 16,
            right: 16,
            backgroundColor: 'rgba(0, 0, 0, 0.85)',
            padding: 12,
            borderRadius: 8,
            borderWidth: 1,
            borderColor: '#00ff00',
            zIndex: 9999,
          }}
          pointerEvents="none">
          <Text
            style={{
              color: '#00ff00',
              fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
              fontSize: 12,
              marginBottom: 8,
            }}>
            KONAMI CODE DEBUG
          </Text>
          <Text
            style={{
              color: '#ffffff',
              fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
              fontSize: 11,
              marginBottom: 4,
            }}>
            Progress: {debugInfo.currentIndex}/{KONAMI_SEQUENCE.length}
          </Text>
          <Text
            style={{
              color: '#ffffff',
              fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
              fontSize: 11,
              marginBottom: 4,
            }}>
            Sequence:{' '}
            {KONAMI_SEQUENCE.map((d, i) =>
              i < debugInfo.currentIndex ? '\u2713' : i === debugInfo.currentIndex ? `[${d.toUpperCase()}]` : d,
            ).join(' ')}
          </Text>
          <Text
            style={{
              color: '#ffff00',
              fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
              fontSize: 11,
              marginBottom: 4,
            }}>
            Next: {debugInfo.expectedNext.toUpperCase()}
          </Text>
          <Text
            style={{
              color:
                debugInfo.lastInput === debugInfo.expectedNext ||
                (debugInfo.currentIndex > 0 && KONAMI_SEQUENCE[debugInfo.currentIndex - 1] === debugInfo.lastInput)
                  ? '#00ff00'
                  : '#ff6666',
              fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
              fontSize: 11,
              marginBottom: 4,
            }}>
            Last input: {debugInfo.lastInput?.toUpperCase() ?? 'none'}
          </Text>
          {debugInfo.lastDelta && (
            <Text
              style={{ color: '#888888', fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace', fontSize: 10 }}>
              Delta: x={debugInfo.lastDelta.x.toFixed(0)}, y={debugInfo.lastDelta.y.toFixed(0)}
            </Text>
          )}
          <Text
            style={{
              color: '#666666',
              fontFamily: Platform.OS === 'ios' ? 'Courier' : 'monospace',
              fontSize: 9,
              marginTop: 8,
            }}>
            Swipe: min 30px | Tap: max 10px movement
          </Text>
        </View>
      )}

      {/* Easter egg: Space Shooter Game */}
      <SpaceShooterGame visible={showSpaceShooter} onClose={() => setShowSpaceShooter(false)} />
    </>
  );
}

export default React.memo(SettingsScreen);

const createStyles = (theme: NovaTheme, screenWidth = 1920, screenHeight = 1080) => {
  const isTV = Platform.isTV;
  // Non-tvOS TV platforms (Android TV, Fire TV, etc.) need smaller scaling
  const isNonTvosTV = Platform.isTV && Platform.OS !== 'ios';
  // Scale factor for non-tvOS TV - reduce sizes by 30% compared to tvOS
  const atvScale = isNonTvosTV ? 0.7 : 1;
  const tvPadding = isTV ? theme.spacing.xl * 1.5 * atvScale : theme.spacing['2xl'];
  // 10% edge buffer for TV platforms
  const tvEdgeBufferHorizontal = isTV ? screenWidth * 0.1 : 0;
  const tvEdgeBufferVertical = isTV ? screenHeight * 0.1 : 0;

  return StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: isTV ? 'transparent' : theme.colors.background.base,
    },
    container: {
      flex: 1,
      backgroundColor: isTV ? 'transparent' : theme.colors.background.base,
    },
    contentContainer: {
      padding: tvPadding,
      gap: theme.spacing.xl * atvScale,
    },
    // Mobile container
    mobileContainer: {
      flex: 1,
    },
    // TV Layout styles
    tvLayoutContainer: {
      flex: 1,
    },
    tvHeader: {
      paddingHorizontal: tvEdgeBufferHorizontal,
      paddingTop: isNonTvosTV ? theme.spacing['2xl'] : theme.spacing.xl,
      paddingBottom: theme.spacing.lg,
    },
    tvScreenTitle: {
      ...theme.typography.title.xl,
      fontSize: theme.typography.title.xl.fontSize * 1.2,
      color: theme.colors.text.primary,
      marginBottom: isNonTvosTV ? theme.spacing.lg : theme.spacing.md,
    },
    tvTabBar: {
      flexDirection: 'row',
      gap: theme.spacing.md * atvScale,
      marginBottom: isNonTvosTV ? theme.spacing.lg : 0,
    },
    tvTabButton: {
      paddingHorizontal: isNonTvosTV ? theme.spacing['2xl'] * atvScale * 1.2 : theme.spacing['2xl'],
      paddingVertical: isNonTvosTV ? theme.spacing.sm * atvScale * 1.2 : undefined,
      backgroundColor: theme.colors.background.surface,
      borderWidth: isNonTvosTV ? 1.5 : StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      ...(isNonTvosTV && { minHeight: 24, justifyContent: 'center' as const }),
    },
    tvTabButtonActive: {
      borderWidth: isNonTvosTV ? 1.5 : 2,
      borderColor: theme.colors.accent.primary,
    },
    tvContentArea: {
      flex: 1,
      paddingHorizontal: tvEdgeBufferHorizontal,
      overflow: 'hidden',
    },
    // Legacy styles kept for mobile
    tvEdgeBuffer: {
      flex: 1,
      paddingHorizontal: tvEdgeBufferHorizontal,
      paddingVertical: tvEdgeBufferVertical,
    },
    screenTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    tabBar: {
      flexDirection: 'row',
      gap: theme.spacing.sm * atvScale,
      paddingVertical: theme.spacing.md * atvScale,
      flexWrap: 'wrap',
    },
    tab: {
      paddingHorizontal: theme.spacing.md * atvScale,
      paddingVertical: theme.spacing.sm * atvScale,
      borderRadius: theme.radius.md * atvScale,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.surface,
    },
    tabActive: {
      borderWidth: isNonTvosTV ? 1.5 : 2,
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.overlay.button,
    },
    section: {
      padding: theme.spacing.xl * atvScale,
      borderRadius: theme.radius.lg,
      backgroundColor: theme.colors.background.surface,
      gap: theme.spacing.md * atvScale,
    },
    sectionHeader: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
    },
    sectionTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    sectionDescription: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    linkText: {
      ...theme.typography.body.md,
      color: theme.colors.accent.primary,
      textDecorationLine: 'underline' as const,
    },
    debugButton: {
      marginVertical: theme.spacing.sm,
    },
    fieldRow: {
      gap: theme.spacing.xs * atvScale,
    },
    fieldLabel: {
      ...theme.typography.label.md,
      color: theme.colors.text.secondary,
      ...(isNonTvosTV && { fontSize: theme.typography.label.md.fontSize * atvScale }),
    },
    input: {
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.base,
      color: theme.colors.text.primary,
      borderRadius: theme.radius.md * atvScale,
      paddingHorizontal: theme.spacing.md * atvScale,
      paddingVertical: theme.spacing.sm * atvScale,
      minHeight: 44 * atvScale,
      ...(isNonTvosTV && { fontSize: theme.typography.body.md.fontSize * atvScale }),
    },
    inputFocused: {
      borderColor: theme.colors.accent.primary,
      shadowColor: theme.colors.accent.primary,
      shadowOpacity: 0.25,
      shadowRadius: 6,
    },
    multiline: {
      minHeight: 120 * atvScale,
    },
    switch: {
      alignSelf: 'flex-start',
      ...(isNonTvosTV && { transform: [{ scale: 0.8 }] }),
    },
    switchFocused: {
      transform: [{ scale: isNonTvosTV ? 0.9 : 1.05 }],
    },
    fieldError: {
      ...theme.typography.caption.sm,
      color: theme.colors.status.danger,
    },
    buttonRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.md * atvScale,
      alignItems: 'center',
    },
    secondaryButton: {
      backgroundColor: theme.colors.overlay.button,
    },
    statusText: {
      ...theme.typography.body.md,
    },
    statusError: {
      color: theme.colors.status.danger,
    },
    statusSuccess: {
      color: theme.colors.status.success,
    },
    versionInfoContainer: {
      gap: theme.spacing.xs * atvScale,
    },
    versionInfoRow: {
      flexDirection: 'row',
      justifyContent: 'flex-start',
      alignItems: 'center',
      gap: theme.spacing.md,
      paddingVertical: theme.spacing.xs * atvScale,
      paddingHorizontal: theme.spacing.lg * atvScale,
    },
    versionInfoLabel: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
      minWidth: 100,
      ...(isNonTvosTV && { fontSize: theme.typography.title.md.fontSize * 0.9 }),
    },
    versionInfoValue: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
      fontWeight: '600',
      fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
      ...(isNonTvosTV && { fontSize: theme.typography.body.lg.fontSize * atvScale }),
    },
    deviceIdValue: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontWeight: '600',
      fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
      flex: 1,
    },
    deviceIdValueTV: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontWeight: '600',
      fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
      flex: 1,
      ...(isNonTvosTV && { fontSize: theme.typography.body.sm.fontSize * atvScale }),
    },
    deviceIdRowMobile: {
      flexDirection: 'column',
      alignItems: 'flex-start',
      gap: theme.spacing.xs,
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.lg,
    },
    deviceIdValueMobile: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
      fontWeight: '600',
      fontFamily: Platform.OS === 'ios' ? 'Menlo' : 'monospace',
    },
    versionButtonContainer: {
      marginTop: theme.spacing.md * atvScale,
      alignSelf: 'flex-start',
    },
    backendInfoNote: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'flex-start',
      gap: isNonTvosTV ? theme.spacing.xs : theme.spacing.sm,
      marginTop: isNonTvosTV ? theme.spacing.sm : theme.spacing.md,
      paddingTop: isNonTvosTV ? theme.spacing.sm : theme.spacing.md,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
    },
    backendInfoNoteText: {
      ...(isNonTvosTV ? theme.typography.caption.sm : theme.typography.body.md),
      color: theme.colors.text.muted,
    },
    backendInfoNoteMobile: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm,
      marginTop: theme.spacing.lg,
      paddingTop: theme.spacing.md,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
    },
    backendInfoNoteTextMobile: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      flex: 1,
    },
    loadingRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm * atvScale,
    },
    loadingText: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    indexerCard: {
      padding: theme.spacing.lg * atvScale,
      borderRadius: theme.radius.md * atvScale,
      backgroundColor: theme.colors.background.base,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.sm * atvScale,
    },
    indexerHeader: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
    },
    indexerTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
    },
    removeButton: {
      backgroundColor: theme.colors.status.danger,
    },
    playbackOptionsRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      alignItems: 'flex-start',
      gap: theme.spacing.md * atvScale,
    },
    playbackOption: {
      borderWidth: 1,
    },
    playbackOptionSelected: {
      borderWidth: 2,
      borderColor: theme.colors.accent.primary,
    },
    hiddenChannelsList: {
      gap: theme.spacing.sm * atvScale,
    },
    hiddenChannelCard: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      padding: theme.spacing.md,
      backgroundColor: theme.colors.background.base,
      borderRadius: theme.radius.md,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
    },
    hiddenChannelInfo: {
      flex: 1,
      gap: theme.spacing.xs,
    },
    hiddenChannelName: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
    },
    hiddenChannelGroup: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
    },
    unplayableDate: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      marginTop: theme.spacing.xs,
    },
    unhideButton: {
      marginLeft: theme.spacing.md,
      backgroundColor: theme.colors.accent.primary,
      borderRadius: theme.radius.md,
    },
    unhideButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.inverse,
    },
    modalOverlay: {
      flex: 1,
      backgroundColor: 'rgba(0, 0, 0, 0.75)',
      justifyContent: 'center',
      alignItems: 'center',
      padding: theme.spacing['2xl'],
    },
    modalContent: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg,
      padding: theme.spacing.xl,
      width: '100%',
      maxWidth: 800,
      height: '80%',
      overflow: 'hidden',
    },
    modalHeader: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      paddingBottom: theme.spacing.md,
      borderBottomWidth: 1,
      borderBottomColor: theme.colors.border.subtle,
    },
    modalTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    modalCloseButton: {
      backgroundColor: theme.colors.overlay.button,
      borderRadius: theme.radius.md,
    },
    modalCloseButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
    },
    modalScrollView: {
      flex: 1,
    },
    modalScrollContent: {
      gap: theme.spacing.sm,
      paddingVertical: theme.spacing.sm,
    },
    dropdownContainer: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm * atvScale,
    },
    dropdownOption: {
      paddingHorizontal: isNonTvosTV ? theme.spacing.md * atvScale * 1.3 : theme.spacing.md,
      paddingVertical: isNonTvosTV ? theme.spacing.sm * atvScale * 1.3 : theme.spacing.sm,
      borderRadius: theme.radius.md * atvScale,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.base,
    },
    dropdownOptionText: {
      ...(isNonTvosTV && { fontSize: theme.typography.label.md.fontSize * 1.3 }),
    },
    dropdownOptionTextFocused: {
      ...(isNonTvosTV && { fontSize: theme.typography.label.md.fontSize * 1.3 }),
    },
    dropdownOptionSelected: {
      borderWidth: isNonTvosTV ? 1.5 : 2,
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
    },
    dropdownOptionFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    // Spatial navigation button styles (matching TVActionButton)
    spatialButton: {
      paddingHorizontal: theme.spacing.lg * tvScale(1.375, 1),
      paddingVertical: theme.spacing.md * tvScale(1.375, 1),
      borderRadius: theme.radius.md * tvScale(1.375, 1),
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.overlay.button,
      alignItems: 'center',
      alignSelf: 'flex-start',
    },
    spatialButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    spatialButtonDisabled: {
      opacity: 0.6,
    },
    spatialButtonText: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      fontSize: theme.typography.label.md.fontSize * tvScale(1.375, 1),
      lineHeight: theme.typography.label.md.lineHeight * tvScale(1.375, 1),
    },
    spatialButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    shelfManagementRow: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      gap: theme.spacing.md * atvScale,
    },
    shelfInfo: {
      flex: 1,
      gap: theme.spacing.xs,
    },
    shelfName: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    shelfNameDisabled: {
      color: theme.colors.text.secondary,
      opacity: 0.6,
    },
    shelfId: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
    },
    shelfIdDisabled: {
      opacity: 0.5,
    },
    shelfControls: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      flex: 1,
    },
    shelfArrowButtons: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.sm * atvScale,
    },
    shelfArrowButton: {
      paddingHorizontal: theme.spacing.md * atvScale,
      paddingVertical: theme.spacing.sm * atvScale,
      minWidth: 44 * atvScale,
      backgroundColor: theme.colors.background.elevated,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
    },
    shelfToggle: {
      alignSelf: 'center',
    },
    shelfArrowButtonDisabled: {
      opacity: 0.3,
    },
    shelfCard: {
      // Animation handled by LayoutAnimation
    },
    shelfCardDisabled: {
      opacity: 0.7,
      borderColor: theme.colors.border.subtle,
    },

    // TV Modal styles
    tvModalOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    tvModalContent: {
      width: '60%',
      maxWidth: 900,
      maxHeight: '80%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      padding: theme.spacing['2xl'],
      gap: theme.spacing.lg,
    },
    tvModalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    tvModalSubtitle: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
    },
    tvModalScrollView: {
      flex: 1,
      maxHeight: screenHeight * 0.4,
    },
    tvModalScrollContent: {
      gap: theme.spacing.md,
    },
    tvModalItem: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg,
      padding: theme.spacing.lg,
      borderWidth: 1,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.lg,
    },
    tvModalItemInfo: {
      flex: 1,
      gap: theme.spacing.xs,
    },
    tvModalItemTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
    },
    tvModalItemSubtitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    tvModalItemMeta: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
    },
    tvModalItemButton: {
      minWidth: isNonTvosTV ? 140 * 0.6 : 140,
      minHeight: isNonTvosTV ? 48 * 0.6 : 48,
      justifyContent: 'center',
      paddingVertical: theme.spacing.sm * atvScale,
      paddingHorizontal: theme.spacing.lg * atvScale,
      borderWidth: isNonTvosTV ? 1.5 : 2,
      borderRadius: theme.radius.md * atvScale,
      backgroundColor: theme.colors.background.base,
      borderColor: theme.colors.border.subtle,
    },
    tvModalItemButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
    },
    tvModalItemButtonText: {
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    tvModalItemButtonTextFocused: {
      color: theme.colors.text.primary,
    },
    tvModalFooter: {
      flexDirection: 'row',
      justifyContent: 'flex-end',
      gap: theme.spacing.lg,
      marginTop: theme.spacing.lg,
    },
    tvModalCloseButton: {
      minWidth: 180,
      minHeight: 56,
      justifyContent: 'center',
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.xl,
      borderWidth: 3,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderColor: theme.colors.border.subtle,
    },
    tvModalCloseButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
    },
    tvModalCloseButtonText: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    tvModalCloseButtonTextFocused: {
      color: theme.colors.text.primary,
    },
    tvModalDangerButton: {
      borderColor: theme.colors.status.danger,
      backgroundColor: theme.colors.status.danger + '20',
    },
    tvModalDangerButtonFocused: {
      borderColor: theme.colors.status.danger,
      backgroundColor: theme.colors.status.danger + '30',
    },
    tvModalDangerButtonText: {
      color: theme.colors.status.danger,
    },

    // TV Text Input Modal styles
    tvTextInputModalInput: {
      borderWidth: 3,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.background.base,
      color: theme.colors.text.primary,
      borderRadius: theme.radius.lg,
      paddingHorizontal: theme.spacing.xl,
      paddingVertical: theme.spacing.lg,
      minHeight: 64,
      ...theme.typography.body.lg,
    },
    tvTextInputModalInputFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
      shadowColor: theme.colors.accent.primary,
      shadowOpacity: 0.3,
      shadowOffset: { width: 0, height: 4 },
      shadowRadius: 12,
    },
    tvTextInputModalInputMultiline: {
      minHeight: 160,
      textAlignVertical: 'top',
    },

    // TV Grid Item styles for virtualized settings
    tvGridFieldRow: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg * atvScale,
      padding: theme.spacing.lg * atvScale,
      borderWidth: isNonTvosTV ? 1.5 : 2,
      borderColor: theme.colors.border.subtle,
      minHeight: 72 * atvScale,
    },
    tvGridFieldRowFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
      shadowColor: theme.colors.accent.primary,
      shadowOpacity: 0.25,
      shadowOffset: { width: 0, height: 4 },
      shadowRadius: 10,
    },
    tvGridFieldLabel: {
      ...theme.typography.body.lg,
      color: theme.colors.text.primary,
      flex: 1,
      ...(isNonTvosTV && { fontSize: theme.typography.body.lg.fontSize * atvScale }),
    },
    tvGridShelfLabel: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
      marginLeft: theme.spacing.sm * atvScale,
      minWidth: '30%',
      ...(isNonTvosTV && { fontSize: theme.typography.title.md.fontSize * 1.1 }),
    },
    shelfArrowButtonText: {
      fontWeight: '700',
      ...(isNonTvosTV && { fontSize: 16 }),
    },
    tvGridFieldValue: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
      textAlign: 'right',
      maxWidth: '50%',
      ...(isNonTvosTV && { fontSize: theme.typography.body.lg.fontSize * atvScale }),
    },
    tvGridFieldValuePlaceholder: {
      color: theme.colors.text.muted,
      fontStyle: 'italic',
    },
    // TV Grid inline input styles
    tvGridInlineInputRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.lg * atvScale,
      marginTop: theme.spacing.xl * atvScale,
    },
    tvGridInlineInputRowFocused: {
      // No outer focus styling - focus is on the input itself
    },
    tvGridInlineInput: {
      flex: 1,
      fontSize: isNonTvosTV ? 22 * 0.5 : 22,
      color: theme.colors.text.primary,
      backgroundColor: theme.colors.background.base,
      borderRadius: theme.radius.md * atvScale,
      borderWidth: isNonTvosTV ? 1.5 : 2,
      borderColor: theme.colors.border.subtle,
      minHeight: isNonTvosTV ? 56 * 0.6 : 56,
      textAlignVertical: 'center',
    },
    tvGridInlineInputFocused: {
      borderColor: theme.colors.accent.primary,
      borderWidth: isNonTvosTV ? 2 : 3,
      backgroundColor: theme.colors.background.surface,
      shadowColor: theme.colors.accent.primary,
      shadowOpacity: 0.3,
      shadowOffset: { width: 0, height: 2 },
      shadowRadius: 8,
    },
    tvGridInlineInputLabel: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
      minWidth: 220 * atvScale,
      marginLeft: theme.spacing.lg * atvScale,
      ...(isNonTvosTV && { fontSize: theme.typography.title.md.fontSize * 0.9 }),
    },
    tvGridHeader: {
      paddingTop: isNonTvosTV ? theme.spacing.xs : theme.spacing.md,
      paddingBottom: isNonTvosTV ? theme.spacing.sm : theme.spacing.md,
      paddingHorizontal: theme.spacing.sm * atvScale,
    },
    tvGridHeaderTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
      // Keep title size larger on non-tvOS TV
    },
    tvGridHeaderDescription: {
      ...theme.typography.body.lg,
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.xs * atvScale,
      marginBottom: theme.spacing.xs * atvScale,
      // Keep description size larger on non-tvOS TV
    },
    tvGridToggleRow: {
      flexDirection: 'row',
      alignItems: 'center',
      justifyContent: 'space-between',
      marginTop: theme.spacing.xl * atvScale,
      minHeight: 56 * atvScale,
      borderRadius: theme.radius.md * atvScale,
      paddingHorizontal: theme.spacing.md * atvScale,
    },
    tvGridToggleRowFocused: {
      // Focus is now on the switch itself
    },
    tvGridToggleLabel: {
      flex: 1,
      gap: theme.spacing.xs * atvScale,
    },
    tvGridToggleLabelText: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      fontWeight: '600',
      minWidth: 220 * atvScale,
      marginLeft: theme.spacing.lg * atvScale,
      ...(isNonTvosTV && { fontSize: theme.typography.title.md.fontSize * 0.9 }),
    },
    tvGridToggleSwitchFocused: {
      transform: [{ scale: isNonTvosTV ? 0.9 : 1.3 }],
    },
    tvGridToggleDescription: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      ...(isNonTvosTV && { fontSize: theme.typography.body.md.fontSize * atvScale }),
    },
    tvGridDropdownRow: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg * atvScale,
      padding: theme.spacing.lg * atvScale,
      borderWidth: isNonTvosTV ? 1.5 : 2,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.md * atvScale,
    },
    tvGridDropdownLabel: {
      ...theme.typography.body.lg,
      color: theme.colors.text.primary,
      ...(isNonTvosTV && { fontSize: theme.typography.body.lg.fontSize * atvScale }),
    },
    tvGridDropdownOptions: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm * atvScale,
    },
    tvGridDropdownRowInline: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.lg * atvScale,
      marginTop: theme.spacing.xl * atvScale,
    },
    tvGridDropdownOptionsInline: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.sm * atvScale,
      flex: 1,
      justifyContent: 'flex-end',
    },
    tvGridButtonRow: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: theme.spacing.md * atvScale,
      paddingVertical: theme.spacing.sm * atvScale,
    },
    // TV Grid container - fills the entire available space from the top
    tvGridContainer: {
      flex: 1,
    },
    tvScrollContent: {
      paddingBottom: theme.spacing['2xl'],
    },
    // TV Grid title styles (part of virtualized grid)
    tvGridTitleRow: {
      paddingBottom: theme.spacing.md * atvScale,
    },
    tvGridTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      ...(isNonTvosTV && { fontSize: theme.typography.title.xl.fontSize * atvScale }),
    },
    // TV Grid tab row styles (part of virtualized grid)
    tvGridTabRow: {
      paddingBottom: theme.spacing.lg * atvScale,
    },
    tvGridTabBar: {
      flexDirection: 'row',
      gap: theme.spacing.sm * atvScale,
      flexWrap: 'wrap',
    },
    // Row container style for settings grid (used with ScrollView)
    // Calculate width: 60% of available content area
    tvGridRowContainer: isTV
      ? {
          width: (screenWidth - tvEdgeBufferHorizontal * 2 - tvPadding * 2) * 0.6,
          gap: isNonTvosTV ? theme.spacing.sm : theme.spacing.xs,
        }
      : {},
    // Full width style for grid items (needed because virtualized grid item wrappers don't have width)
    tvGridItemFullWidth: isTV
      ? {
          width: (screenWidth - tvEdgeBufferHorizontal * 2 - tvPadding * 2) * 0.6,
        }
      : { width: '100%' },
    // Spacing between grid items
    tvGridItemSpacing: {
      marginBottom: isNonTvosTV ? theme.spacing.xs : theme.spacing.sm,
    },
    tvGridCustomToggle: {
      width: isNonTvosTV ? 50 * atvScale * 0.8 : 50,
      height: isNonTvosTV ? 30 * atvScale * 0.8 : 30,
      borderRadius: isNonTvosTV ? 15 * atvScale * 0.8 : 15,
      justifyContent: 'center',
      padding: 2 * atvScale,
    },
    tvGridCustomToggleThumb: {
      width: isNonTvosTV ? 26 * atvScale * 0.8 : 26,
      height: isNonTvosTV ? 26 * atvScale * 0.8 : 26,
      borderRadius: isNonTvosTV ? 13 * atvScale * 0.8 : 13,
      backgroundColor: theme.colors.text.inverse,
    },
  });
};
