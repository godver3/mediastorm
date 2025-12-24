import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  ActivityIndicator,
  Keyboard,
  Modal,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Switch,
  Text,
  TextInput,
  useWindowDimensions,
  View,
  type TextStyle,
  type ViewStyle,
} from 'react-native';
import Animated, { Layout, useAnimatedStyle, useSharedValue, withSequence, withTiming } from 'react-native-reanimated';

import {
  useBackendSettings,
  type BackendIndexerConfig,
  type BackendPlaybackSettings,
  type BackendSettings,
  type BackendShelfConfig,
  type BackendTorrentScraperConfig,
  type PlaybackPreference,
  type StreamingServiceMode,
  type StreamingServicePriority,
  type TrendingMovieSource,
} from '@/components/BackendSettingsContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import { useLiveHiddenChannels, useLiveFavorites, useLiveCategories } from '@/components/LiveContext';
import { useMenuContext } from '@/components/MenuContext';
import { useToast } from '@/components/ToastContext';
import { useLiveChannels } from '@/hooks/useLiveChannels';
import useUnplayableReleases from '@/hooks/useUnplayableReleases';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
  SpatialNavigationScrollView,
  SpatialNavigationVirtualizedGrid,
  useLockSpatialNavigation,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { router, Stack } from 'expo-router';

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

function TextInputField({ label, value, onChange, options, errorMessage, styles }: TextInputFieldProps) {
  const theme = useTheme();
  const inputRef = useRef<TextInput | null>(null);
  const { lock, unlock } = useLockSpatialNavigation();

  const handleFocus = useCallback(() => {
    // Lock spatial navigation to prevent d-pad from navigating away while typing
    lock();
  }, [lock]);

  const handleBlur = useCallback(() => {
    // Unlock spatial navigation to re-enable d-pad navigation
    unlock();
  }, [unlock]);

  return (
    <View style={styles.fieldRow as ViewStyle}>
      <Text style={styles.fieldLabel as TextStyle}>{label}</Text>
      <SpatialNavigationFocusableView
        onSelect={() => {
          // Programmatically focus the TextInput to show keyboard on TV
          inputRef.current?.focus();
        }}
        onBlur={() => {
          // Blur the TextInput when spatial navigation moves away
          inputRef.current?.blur();
        }}>
        {({ isFocused }: { isFocused: boolean }) => (
          <TextInput
            ref={inputRef}
            value={value}
            onChangeText={onChange}
            onFocus={handleFocus}
            onBlur={handleBlur}
            style={[
              styles.input as TextStyle,
              isFocused && (styles.inputFocused as TextStyle),
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
            editable={Platform.isTV ? isFocused : true}
            underlineColorAndroid="transparent"
            importantForAutofill="no"
            disableFullscreenUI={true}
          />
        )}
      </SpatialNavigationFocusableView>
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

function DropdownField({ label, value, options, onChange, styles }: DropdownFieldProps) {
  return (
    <View style={styles.fieldRow as ViewStyle}>
      <Text style={styles.fieldLabel as TextStyle}>{label}</Text>
      <SpatialNavigationNode orientation="horizontal">
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
      </SpatialNavigationNode>
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
    apiKey: string; // Deprecated: kept for migration compatibility
    pin: string; // 6-digit PIN for authentication
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
    trendingMovieSource: TrendingMovieSource;
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
  | { type: 'shelf-item'; id: string; shelf: BackendShelfConfig; index: number; total: number; key?: string };

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
  const { lock, unlock } = useLockSpatialNavigation();

  // Reset edit value when modal opens
  useEffect(() => {
    if (visible) {
      setEditValue(value);
    }
  }, [visible, value]);

  const handleFocus = useCallback(() => {
    lock();
  }, [lock]);

  const handleBlur = useCallback(() => {
    unlock();
  }, [unlock]);

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
    <SpatialNavigationRoot isActive={visible}>
      <View style={styles.tvModalOverlay as ViewStyle}>
        <View style={[styles.tvModalContent as ViewStyle, { maxHeight: options?.multiline ? '60%' : '40%' }]}>
          <Text style={styles.tvModalTitle as TextStyle}>{label}</Text>
          <Text style={styles.tvModalSubtitle as TextStyle}>
            {options?.multiline ? 'Enter text below' : 'Press select to edit, then use the keyboard'}
          </Text>

          <SpatialNavigationNode orientation="vertical">
            <DefaultFocus>
              <SpatialNavigationFocusableView
                focusKey="text-input-modal-input"
                onSelect={() => {
                  inputRef.current?.focus();
                }}
                onBlur={() => {
                  inputRef.current?.blur();
                }}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <TextInput
                    ref={inputRef}
                    {...(Platform.isTV ? { defaultValue: editValue } : { value: editValue })}
                    onChangeText={setEditValue}
                    onFocus={handleFocus}
                    onBlur={handleBlur}
                    style={[
                      styles.tvTextInputModalInput as TextStyle,
                      isFocused && (styles.tvTextInputModalInputFocused as TextStyle),
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
                    editable={isFocused}
                    underlineColorAndroid="transparent"
                    importantForAutofill="no"
                    disableFullscreenUI={true}
                    {...(Platform.OS === 'ios' &&
                      Platform.isTV && {
                        keyboardAppearance: 'dark',
                      })}
                  />
                )}
              </SpatialNavigationFocusableView>
            </DefaultFocus>

            <View style={styles.tvModalFooter as ViewStyle}>
              <FocusablePressable
                focusKey="text-input-modal-close"
                text="Close"
                onSelect={handleSubmit}
                style={styles.tvModalCloseButton as ViewStyle}
                focusedStyle={styles.tvModalCloseButtonFocused as ViewStyle}
                textStyle={styles.tvModalCloseButtonText as TextStyle}
                focusedTextStyle={styles.tvModalCloseButtonTextFocused as TextStyle}
              />
            </View>
          </SpatialNavigationNode>
        </View>
      </View>
    </SpatialNavigationRoot>
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
      apiKey: settings.server.apiKey ?? '',
      pin: settings.server.pin ?? '',
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
      trendingMovieSource: settings.homeShelves?.trendingMovieSource ?? 'released',
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
      apiKey: editable.server.apiKey.trim(),
      pin: editable.server.pin.trim(),
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
      trendingMovieSource:
        editable.homeShelves?.trendingMovieSource ?? baseline.homeShelves?.trendingMovieSource ?? 'released',
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
  const { width: screenWidth, height: screenHeight } = useWindowDimensions();
  const styles = useMemo(
    () => createStyles(theme, screenWidth, screenHeight) as unknown as CompatibleStyles,
    [theme, screenWidth, screenHeight],
  );
  const isFocused = useIsFocused();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const [isHiddenChannelsModalOpen, setIsHiddenChannelsModalOpen] = useState(false);
  const [isUnplayableReleasesModalOpen, setIsUnplayableReleasesModalOpen] = useState(false);
  // TV Text Input Modal state
  const [textInputModal, setTextInputModal] = useState<{
    visible: boolean;
    label: string;
    value: string;
    fieldKey: string;
    options?: TextInputOptions;
  }>({ visible: false, label: '', value: '', fieldKey: '' });
  const { activeUserId, pendingPinUserId } = useUserProfiles();
  const isActive =
    isFocused && !isMenuOpen && !isHiddenChannelsModalOpen && !isUnplayableReleasesModalOpen && !textInputModal.visible && !pendingPinUserId;
  const [activeTab, setActiveTab] = useState<SettingsTab>('connection');

  const tabs = useMemo<TabOption[]>(
    () => [
      { key: 'connection', label: 'Backend' },
      // { key: 'content', label: 'Content Sources' },  // Hidden for now
      { key: 'playback', label: 'Playback' },
      { key: 'home', label: 'Home Screen' },
      { key: 'filtering', label: 'Filtering' },
      // { key: 'advanced', label: 'Advanced' },  // Hidden for now
      { key: 'live', label: 'Live TV' },
    ],
    [],
  );
  const {
    backendUrl,
    backendApiKey,
    isReady,
    loading,
    saving,
    error,
    settings,
    refreshSettings,
    setBackendUrl,
    setBackendApiKey,
    updateBackendSettings,
    userSettings,
    userSettingsLoading,
    loadUserSettings,
    updateUserSettings,
  } = useBackendSettings();
  const { hiddenChannels, unhideChannel } = useLiveHiddenChannels();
  const { favorites } = useLiveFavorites();
  const { selectedCategories } = useLiveCategories();
  const { channels } = useLiveChannels();

  const [backendUrlInput, setBackendUrlInput] = useState(backendUrl);
  const [backendApiKeyInput, setBackendApiKeyInput] = useState(backendApiKey);

  // TV inline text input refs and state
  const { lock: lockNavigation, unlock: unlockNavigation } = useLockSpatialNavigation();
  const backendUrlInputRef = useRef<TextInput>(null);
  const backendPinInputRef = useRef<TextInput>(null);
  const audioLangInputRef = useRef<TextInput>(null);
  const subtitleLangInputRef = useRef<TextInput>(null);
  const playlistUrlInputRef = useRef<TextInput>(null);
  const maxMovieSizeInputRef = useRef<TextInput>(null);
  const maxEpisodeSizeInputRef = useRef<TextInput>(null);
  const filterTermsInputRef = useRef<TextInput>(null);
  const tempBackendUrlRef = useRef(backendUrl);
  const tempBackendPinRef = useRef(backendApiKey);
  const tempAudioLangRef = useRef('');
  const tempSubtitleLangRef = useRef('');
  const tempPlaylistUrlRef = useRef('');
  const tempMaxMovieSizeRef = useRef('');
  const tempMaxEpisodeSizeRef = useRef('');
  const tempFilterTermsRef = useRef('');
  const [activeInlineInput, setActiveInlineInput] = useState<string | null>(null);
  const [shelfPulses, setShelfPulses] = useState<Record<string, { key: number; intensity: 'primary' | 'secondary' }>>(
    {},
  );

  // Sync temp refs with state
  useEffect(() => {
    tempBackendUrlRef.current = backendUrlInput;
  }, [backendUrlInput]);

  useEffect(() => {
    tempBackendPinRef.current = backendApiKeyInput;
  }, [backendApiKeyInput]);

  const [editableSettings, setEditableSettings] = useState<EditableBackendSettings | null>(
    settings ? toEditableSettings(settings) : null,
  );
  const [dirty, setDirty] = useState(false);

  // Sync playback temp refs with editableSettings (after editableSettings is declared)
  useEffect(() => {
    tempAudioLangRef.current = editableSettings?.playback.preferredAudioLanguage || '';
  }, [editableSettings?.playback.preferredAudioLanguage]);

  useEffect(() => {
    tempSubtitleLangRef.current = editableSettings?.playback.preferredSubtitleLanguage || '';
  }, [editableSettings?.playback.preferredSubtitleLanguage]);

  useEffect(() => {
    tempPlaylistUrlRef.current = editableSettings?.live.playlistUrl || '';
  }, [editableSettings?.live.playlistUrl]);

  useEffect(() => {
    tempMaxMovieSizeRef.current = editableSettings?.filtering.maxSizeMovieGb || '';
  }, [editableSettings?.filtering.maxSizeMovieGb]);

  useEffect(() => {
    tempMaxEpisodeSizeRef.current = editableSettings?.filtering.maxSizeEpisodeGb || '';
  }, [editableSettings?.filtering.maxSizeEpisodeGb]);

  useEffect(() => {
    tempFilterTermsRef.current = editableSettings?.filtering.filterOutTerms || '';
  }, [editableSettings?.filtering.filterOutTerms]);
  const [fieldErrors, setFieldErrors] = useState<Record<string, string>>({});
  const { releases: unplayableReleases, unmarkUnplayable, clearAll: clearUnplayableReleases } = useUnplayableReleases();
  const playbackOptions = useMemo<
    {
      label: string;
      value: PlaybackPreference;
    }[]
  >(
    () =>
      Platform.OS === 'android'
        ? [{ label: 'Native', value: 'native' }]
        : [
            { label: 'Native', value: 'native' },
            { label: 'Infuse', value: 'infuse' },
          ],
    [],
  );
  const streamingModeOptions = useMemo(
    () => [
      { value: 'usenet' as StreamingServiceMode, label: 'Usenet' },
      { value: 'debrid' as StreamingServiceMode, label: 'Debrid' },
      { value: 'hybrid' as StreamingServiceMode, label: 'Both' },
    ],
    [],
  );

  const servicePriorityOptions = useMemo(
    () => [
      { value: 'none' as StreamingServicePriority, label: 'None' },
      { value: 'usenet' as StreamingServicePriority, label: 'Usenet' },
      { value: 'debrid' as StreamingServicePriority, label: 'Debrid' },
    ],
    [],
  );

  const hiddenChannelsList = useMemo(() => {
    const hiddenIds = Array.from(hiddenChannels);
    return channels.filter((channel) => hiddenIds.includes(channel.id));
  }, [channels, hiddenChannels]);
  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      if (movement === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

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

  useEffect(() => {
    setBackendApiKeyInput(backendApiKey);
  }, [backendApiKey]);

  const isBackendReachable = !!settings;

  // Load user settings when active user changes
  useEffect(() => {
    if (activeUserId && isBackendReachable) {
      loadUserSettings(activeUserId).catch((err) => {
        console.warn('Failed to load user settings:', err);
      });
    }
  }, [activeUserId, isBackendReachable, loadUserSettings]);

  useEffect(() => {
    if (settings) {
      // Merge user settings over global settings for per-user fields
      const merged = toEditableSettings(settings);
      if (userSettings) {
        merged.playback = {
          preferredPlayer:
            (userSettings.playback?.preferredPlayer as PlaybackPreference) ?? merged.playback.preferredPlayer,
          preferredAudioLanguage:
            userSettings.playback?.preferredAudioLanguage ?? merged.playback.preferredAudioLanguage,
          preferredSubtitleLanguage:
            userSettings.playback?.preferredSubtitleLanguage ?? merged.playback.preferredSubtitleLanguage,
          preferredSubtitleMode:
            (userSettings.playback?.preferredSubtitleMode as 'off' | 'on' | 'forced-only' | undefined) ??
            merged.playback.preferredSubtitleMode,
          useLoadingScreen: userSettings.playback?.useLoadingScreen ?? merged.playback.useLoadingScreen,
          subtitleSize: userSettings.playback?.subtitleSize ?? merged.playback.subtitleSize,
        };
        merged.homeShelves = {
          shelves:
            userSettings.homeShelves?.shelves?.map((s) => ({
              id: s.id,
              name: s.name,
              enabled: s.enabled,
              order: s.order,
            })) ?? merged.homeShelves.shelves,
          trendingMovieSource:
            (userSettings.homeShelves?.trendingMovieSource as TrendingMovieSource) ??
            merged.homeShelves.trendingMovieSource,
        };
        merged.filtering = {
          maxSizeMovieGb:
            userSettings.filtering?.maxSizeMovieGb != null
              ? String(userSettings.filtering.maxSizeMovieGb)
              : merged.filtering.maxSizeMovieGb,
          maxSizeEpisodeGb:
            userSettings.filtering?.maxSizeEpisodeGb != null
              ? String(userSettings.filtering.maxSizeEpisodeGb)
              : merged.filtering.maxSizeEpisodeGb,
          excludeHdr: userSettings.filtering?.excludeHdr ?? merged.filtering.excludeHdr,
          prioritizeHdr: userSettings.filtering?.prioritizeHdr ?? merged.filtering.prioritizeHdr,
          filterOutTerms: userSettings.filtering?.filterOutTerms?.join(', ') ?? merged.filtering.filterOutTerms,
        };
      }
      setEditableSettings(merged);
      setDirty(false);
      clearErrors();
    }
  }, [settings, userSettings, clearErrors]);

  const handleBackendConnectionApply = useCallback(async () => {
    try {
      await setBackendApiKey(backendApiKeyInput);
      await setBackendUrl(backendUrlInput);
      await refreshSettings();
      showToast('Backend connection saved and settings reloaded.', { tone: 'success' });
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to update backend connection';
      showToast(message, { tone: 'danger' });
    }
  }, [backendApiKeyInput, backendUrlInput, setBackendApiKey, setBackendUrl, refreshSettings, showToast]);

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
      } else if (fieldKey === 'backendApiKey') {
        setBackendApiKeyInput(newValue);
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

  // Per-user settings tabs
  const isPerUserTab = activeTab === 'playback' || activeTab === 'home' || activeTab === 'filtering';

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

    // For per-user tabs (playback, home, filtering), save to user settings endpoint
    if (isPerUserTab) {
      if (!activeUserId) {
        showToast('No active user profile selected.', { tone: 'danger' });
        return;
      }

      clearErrors();

      try {
        const userSettingsPayload = {
          playback: {
            preferredPlayer: editableSettings.playback?.preferredPlayer || 'native',
            preferredAudioLanguage: editableSettings.playback?.preferredAudioLanguage?.trim() || undefined,
            preferredSubtitleLanguage: editableSettings.playback?.preferredSubtitleLanguage?.trim() || undefined,
            preferredSubtitleMode: editableSettings.playback?.preferredSubtitleMode || undefined,
            useLoadingScreen: editableSettings.playback?.useLoadingScreen ?? false,
            subtitleSize: (() => {
              const val = editableSettings.playback?.subtitleSize;
              if (typeof val === 'number') return val;
              if (typeof val === 'string') {
                const parsed = parseFloat(val);
                return isNaN(parsed) ? 1.0 : Math.round(parsed * 100) / 100;
              }
              return 1.0;
            })(),
          },
          homeShelves: {
            shelves:
              editableSettings.homeShelves?.shelves?.map((s) => ({
                id: s.id,
                name: s.name,
                enabled: s.enabled,
                order: s.order,
              })) ?? [],
            trendingMovieSource: editableSettings.homeShelves?.trendingMovieSource,
          },
          filtering: {
            maxSizeMovieGb: parseFloat(editableSettings.filtering?.maxSizeMovieGb ?? '0') || 0,
            maxSizeEpisodeGb: parseFloat(editableSettings.filtering?.maxSizeEpisodeGb ?? '0') || 0,
            excludeHdr: editableSettings.filtering?.excludeHdr ?? false,
            prioritizeHdr: editableSettings.filtering?.prioritizeHdr ?? true,
            filterOutTerms: (editableSettings.filtering?.filterOutTerms ?? '')
              .split(',')
              .map((term) => term.trim())
              .filter((term) => term.length > 0),
          },
          liveTV: {
            hiddenChannels: Array.from(hiddenChannels),
            favoriteChannels: Array.from(favorites),
            selectedCategories: selectedCategories,
          },
        };

        await updateUserSettings(activeUserId, userSettingsPayload);
        showToast('User settings updated.', { tone: 'success' });
        setDirty(false);
      } catch (err) {
        const message = err instanceof Error ? err.message : 'Failed to update user settings';
        showToast(message, { tone: 'danger' });
      }
      return;
    }

    // For global tabs (connection, content, live, advanced), save to global settings endpoint
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
  }, [
    activeTab,
    activeUserId,
    clearErrors,
    editableSettings,
    isPerUserTab,
    settings,
    showToast,
    updateBackendSettings,
    updateUserSettings,
  ]);

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

  const updateUsenetProvider = useCallback(
    (index: number, field: keyof EditableUsenetProvider, value: string | boolean) => {
      setDirty(true);
      setEditableSettings((current) => {
        if (!current) {
          return current;
        }
        const nextProviders = current.usenet.map((existing, idx) =>
          idx === index
            ? {
                ...existing,
                [field]: value,
              }
            : existing,
        );
        return {
          ...current,
          usenet: nextProviders,
        };
      });
    },
    [],
  );

  const handleAddUsenetProvider = useCallback(() => {
    setDirty(true);
    setEditableSettings((current) => {
      if (!current) {
        return current;
      }
      return {
        ...current,
        usenet: [
          ...current.usenet,
          { name: '', host: '', port: '563', ssl: true, username: '', password: '', connections: '8', enabled: true },
        ],
      };
    });
  }, []);

  const handleRemoveUsenetProvider = useCallback((index: number) => {
    setDirty(true);
    setEditableSettings((current) => {
      if (!current) {
        return current;
      }
      return {
        ...current,
        usenet: current.usenet.filter((_, idx) => idx !== index),
      };
    });
  }, []);

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

  const updateStreamingServiceMode = useCallback((mode: StreamingServiceMode) => {
    setDirty(true);
    setEditableSettings((current) =>
      current
        ? {
            ...current,
            streaming: {
              ...current.streaming,
              serviceMode: mode,
            },
          }
        : current,
    );
  }, []);

  const updateStreamingServicePriority = useCallback((priority: StreamingServicePriority) => {
    setDirty(true);
    setEditableSettings((current) =>
      current
        ? {
            ...current,
            streaming: {
              ...current.streaming,
              servicePriority: priority,
            },
          }
        : current,
    );
  }, []);

  const updateDebridProvider = useCallback(
    (index: number, field: keyof EditableDebridProvider, value: string | boolean) => {
      setDirty(true);
      setEditableSettings((current) => {
        if (!current) {
          return current;
        }
        const nextProviders = current.streaming.debridProviders.map((existing, idx) => {
          if (idx !== index) {
            return existing;
          }
          const nextValue = field === 'enabled' ? Boolean(value) : typeof value === 'string' ? value : String(value);
          return {
            ...existing,
            [field]: nextValue as EditableDebridProvider[keyof EditableDebridProvider],
          };
        }) as EditableDebridProvider[];
        return {
          ...current,
          streaming: {
            ...current.streaming,
            debridProviders: nextProviders,
          },
        };
      });
    },
    [],
  );

  const handleAddDebridProvider = useCallback(() => {
    setDirty(true);
    setEditableSettings((current) => {
      if (!current) {
        return current;
      }
      return {
        ...current,
        streaming: {
          ...current.streaming,
          debridProviders: [
            ...current.streaming.debridProviders,
            { name: '', provider: 'realdebrid', apiKey: '', enabled: false },
          ],
        },
      };
    });
  }, []);

  const handleRemoveDebridProvider = useCallback((index: number) => {
    setDirty(true);
    setEditableSettings((current) => {
      if (!current) {
        return current;
      }
      return {
        ...current,
        streaming: {
          ...current.streaming,
          debridProviders: current.streaming.debridProviders.filter((_, idx) => idx !== index),
        },
      };
    });
  }, []);

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

  const updatePlaybackMode = useCallback((value: PlaybackPreference) => {
    setDirty(true);
    setEditableSettings((current) =>
      current
        ? {
            ...current,
            playback: {
              ...current.playback,
              preferredPlayer: value,
            },
          }
        : current,
    );
  }, []);

  const updatePlaybackField = useCallback(
    (field: keyof BackendPlaybackSettings) => (value: string | boolean) => {
      setDirty(true);
      setEditableSettings((current) =>
        current
          ? {
              ...current,
              playback: {
                ...current.playback,
                [field]: value,
              },
            }
          : current,
      );
    },
    [],
  );

  const _updateLiveField = useCallback(
    (field: keyof EditableBackendSettings['live']) => (value: string) => {
      setDirty(true);
      setEditableSettings((current) =>
        current
          ? {
              ...current,
              live: {
                ...current.live,
                [field]: value,
              },
            }
          : current,
      );
    },
    [],
  );

  const updateFilteringField = useCallback(
    (field: keyof EditableBackendSettings['filtering']) => (value: string | boolean) => {
      setDirty(true);
      setEditableSettings((current) =>
        current
          ? {
              ...current,
              filtering: {
                ...current.filtering,
                [field]: value,
              },
            }
          : current,
      );
    },
    [],
  );

  const updateShelf = useCallback(
    (index: number, field: keyof BackendShelfConfig, value: string | boolean | number) => {
      setDirty(true);
      setEditableSettings((current) => {
        if (!current) {
          return current;
        }
        const nextShelves = current.homeShelves.shelves.map((shelf, idx) =>
          idx === index ? { ...shelf, [field]: value } : shelf,
        );
        return {
          ...current,
          homeShelves: {
            ...current.homeShelves,
            shelves: nextShelves,
          },
        };
      });
    },
    [],
  );

  const updateTrendingMovieSource = useCallback((source: TrendingMovieSource) => {
    setDirty(true);
    setEditableSettings((current) =>
      current
        ? {
            ...current,
            homeShelves: {
              ...current.homeShelves,
              trendingMovieSource: source,
            },
          }
        : current,
    );
  }, []);

  const triggerShelfPulse = useCallback((primaryId: string, secondaryId: string) => {
    setShelfPulses((prev) => ({
      ...prev,
      [primaryId]: { key: (prev[primaryId]?.key || 0) + 1, intensity: 'primary' },
      [secondaryId]: { key: (prev[secondaryId]?.key || 0) + 1, intensity: 'secondary' },
    }));
  }, []);

  const moveShelfUp = useCallback(
    (index: number) => {
      if (index === 0) {
        return;
      }
      setEditableSettings((current) => {
        if (!current) {
          return current;
        }
        const shelves = [...current.homeShelves.shelves];
        const movedShelf = shelves[index];
        const displacedShelf = shelves[index - 1];
        shelves[index - 1] = { ...movedShelf, order: index - 1 };
        shelves[index] = { ...displacedShelf, order: index };
        triggerShelfPulse(movedShelf.id, displacedShelf.id);
        return {
          ...current,
          homeShelves: { ...current.homeShelves, shelves },
        };
      });
      setDirty(true);
    },
    [triggerShelfPulse],
  );

  const moveShelfDown = useCallback(
    (index: number) => {
      setEditableSettings((current) => {
        if (!current || index >= current.homeShelves.shelves.length - 1) {
          return current;
        }
        const shelves = [...current.homeShelves.shelves];
        const movedShelf = shelves[index];
        const displacedShelf = shelves[index + 1];
        shelves[index + 1] = { ...movedShelf, order: index + 1 };
        shelves[index] = { ...displacedShelf, order: index };
        triggerShelfPulse(movedShelf.id, displacedShelf.id);
        return {
          ...current,
          homeShelves: { ...current.homeShelves, shelves },
        };
      });
      setDirty(true);
    },
    [triggerShelfPulse],
  );

  const updateIndexer = useCallback((index: number, field: keyof EditableIndexer, value: string | boolean) => {
    setDirty(true);
    setEditableSettings((current) => {
      if (!current) {
        return current;
      }
      const nextIndexers = current.indexers.map((existing, idx) =>
        idx === index
          ? {
              ...existing,
              [field]: value,
            }
          : existing,
      );
      return {
        ...current,
        indexers: nextIndexers,
      };
    });
  }, []);

  const handleAddIndexer = useCallback(() => {
    setDirty(true);
    setEditableSettings((current) => {
      if (!current) {
        return current;
      }
      return {
        ...current,
        indexers: [...current.indexers, { name: '', url: '', apiKey: '', type: 'newznab', enabled: true }],
      };
    });
  }, []);

  const handleRemoveIndexer = useCallback((index: number) => {
    setDirty(true);
    setEditableSettings((current) => {
      if (!current) {
        return current;
      }
      return {
        ...current,
        indexers: current.indexers.filter((_, idx) => idx !== index),
      };
    });
  }, []);

  const updateTorrentScraper = useCallback(
    (index: number, field: keyof EditableTorrentScraper, value: string | boolean) => {
      setDirty(true);
      setEditableSettings((current) => {
        if (!current) {
          return current;
        }
        const nextScrapers = current.torrentScrapers.map((existing, idx) =>
          idx === index
            ? {
                ...existing,
                [field]: value,
              }
            : existing,
        );
        return {
          ...current,
          torrentScrapers: nextScrapers,
        };
      });
    },
    [],
  );

  const handleAddTorrentScraper = useCallback(() => {
    setDirty(true);
    setEditableSettings((current) => {
      if (!current) {
        return current;
      }
      return {
        ...current,
        torrentScrapers: [
          ...current.torrentScrapers,
          { name: '', type: 'torrentio', url: '', apiKey: '', enabled: true },
        ],
      };
    });
  }, []);

  const handleRemoveTorrentScraper = useCallback((index: number) => {
    setDirty(true);
    setEditableSettings((current) => {
      if (!current) {
        return current;
      }
      return {
        ...current,
        torrentScrapers: current.torrentScrapers.filter((_, idx) => idx !== index),
      };
    });
  }, []);

  const renderSwitch = (label: string, value: boolean, onValueChange: (next: boolean) => void) => (
    <View style={styles.fieldRow}>
      <Text style={styles.fieldLabel}>{label}</Text>
      <SpatialNavigationFocusableView onSelect={() => onValueChange(!value)}>
        {({ isFocused }: { isFocused: boolean }) => (
          <Switch
            value={value}
            onValueChange={onValueChange}
            trackColor={{ true: theme.colors.accent.primary, false: theme.colors.border.subtle }}
            thumbColor="#FFFFFF"
            style={[styles.switch, isFocused && styles.switchFocused]}
          />
        )}
      </SpatialNavigationFocusableView>
    </View>
  );

  const renderUsenetProvider = (provider: EditableUsenetProvider, index: number) => (
    <View key={`usenet-${index}`} style={styles.indexerCard}>
      <View style={styles.indexerHeader}>
        <Text style={styles.indexerTitle}>{provider.name || `Usenet Provider ${index + 1}`}</Text>
        <FocusablePressable
          text="Remove"
          onSelect={() => handleRemoveUsenetProvider(index)}
          style={styles.removeButton}
        />
      </View>
      <TextInputField
        label="Name"
        value={provider.name}
        onChange={(text) => updateUsenetProvider(index, 'name', text)}
        styles={styles}
      />
      <TextInputField
        label="Host"
        value={provider.host}
        onChange={(text) => updateUsenetProvider(index, 'host', text)}
        styles={styles}
      />
      <TextInputField
        label="Port"
        value={provider.port}
        onChange={(text) => updateUsenetProvider(index, 'port', text)}
        options={{ keyboardType: 'numeric' }}
        styles={styles}
      />
      {renderSwitch('SSL', provider.ssl, (next) => updateUsenetProvider(index, 'ssl', next))}
      <TextInputField
        label="Username"
        value={provider.username}
        onChange={(text) => updateUsenetProvider(index, 'username', text)}
        styles={styles}
      />
      <TextInputField
        label="Password"
        value={provider.password}
        onChange={(text) => updateUsenetProvider(index, 'password', text)}
        options={{ secureTextEntry: true }}
        styles={styles}
      />
      <TextInputField
        label="Connections"
        value={provider.connections}
        onChange={(text) => updateUsenetProvider(index, 'connections', text)}
        options={{ keyboardType: 'numeric' }}
        styles={styles}
      />
      {renderSwitch('Enabled', provider.enabled, (next) => updateUsenetProvider(index, 'enabled', next))}
    </View>
  );

  const renderDebridProvider = (provider: EditableDebridProvider, index: number) => (
    <View key={`debrid-${index}`} style={styles.indexerCard}>
      <View style={styles.indexerHeader}>
        <Text style={styles.indexerTitle}>{provider.name || `Debrid Provider ${index + 1}`}</Text>
        <FocusablePressable
          text="Remove"
          onSelect={() => handleRemoveDebridProvider(index)}
          style={styles.removeButton}
        />
      </View>
      <TextInputField
        label="Display Name"
        value={provider.name}
        onChange={(text) => updateDebridProvider(index, 'name', text)}
        styles={styles}
      />
      <DropdownField
        label="Provider Type"
        value={provider.provider}
        options={[{ label: 'RealDebrid', value: 'realdebrid' }]}
        onChange={(value) => updateDebridProvider(index, 'provider', value)}
        styles={styles}
      />
      <TextInputField
        label="API Key"
        value={provider.apiKey}
        onChange={(text) => updateDebridProvider(index, 'apiKey', text)}
        options={{ secureTextEntry: true }}
        styles={styles}
      />
      {renderSwitch('Enabled', provider.enabled, (next) => updateDebridProvider(index, 'enabled', next))}
    </View>
  );

  const renderIndexer = (indexer: EditableIndexer, index: number) => (
    <View key={`indexer-${index}`} style={styles.indexerCard}>
      <View style={styles.indexerHeader}>
        <Text style={styles.indexerTitle}>{indexer.name || `Indexer ${index + 1}`}</Text>
        <FocusablePressable text="Remove" onSelect={() => handleRemoveIndexer(index)} style={styles.removeButton} />
      </View>
      <TextInputField
        label="Name"
        value={indexer.name}
        onChange={(text) => updateIndexer(index, 'name', text)}
        styles={styles}
      />
      <TextInputField
        label="URL"
        value={indexer.url}
        onChange={(text) => updateIndexer(index, 'url', text)}
        styles={styles}
      />
      <TextInputField
        label="API Key"
        value={indexer.apiKey}
        onChange={(text) => updateIndexer(index, 'apiKey', text)}
        styles={styles}
      />
      <DropdownField
        label="Type"
        value={indexer.type}
        options={[{ label: 'Newznab', value: 'newznab' }]}
        onChange={(value) => updateIndexer(index, 'type', value)}
        styles={styles}
      />
      {renderSwitch('Enabled', indexer.enabled, (next) => updateIndexer(index, 'enabled', next))}
    </View>
  );

  const renderTorrentScraper = (scraper: EditableTorrentScraper, index: number) => {
    const isTorrentio = scraper.type === 'torrentio';
    return (
      <View key={`scraper-${index}`} style={styles.indexerCard}>
        <View style={styles.indexerHeader}>
          <Text style={styles.indexerTitle}>{scraper.name || `Scraper ${index + 1}`}</Text>
          <FocusablePressable
            text="Remove"
            onSelect={() => handleRemoveTorrentScraper(index)}
            style={styles.removeButton}
          />
        </View>
        <TextInputField
          label="Name"
          value={scraper.name}
          onChange={(text) => updateTorrentScraper(index, 'name', text)}
          styles={styles}
        />
        <DropdownField
          label="Type"
          value={scraper.type}
          options={[{ label: 'Torrentio', value: 'torrentio' }]}
          onChange={(value) => updateTorrentScraper(index, 'type', value)}
          styles={styles}
        />
        {!isTorrentio && (
          <>
            <TextInputField
              label="URL"
              value={scraper.url}
              onChange={(text) => updateTorrentScraper(index, 'url', text)}
              options={{ placeholder: 'http://localhost:9696' }}
              styles={styles}
            />
            <TextInputField
              label="API Key"
              value={scraper.apiKey}
              onChange={(text) => updateTorrentScraper(index, 'apiKey', text)}
              styles={styles}
            />
          </>
        )}
        {isTorrentio && (
          <Text style={styles.sectionDescription}>Torrentio is a public scraper and requires no configuration.</Text>
        )}
        {renderSwitch('Enabled', scraper.enabled, (next) => updateTorrentScraper(index, 'enabled', next))}
      </View>
    );
  };

  const busy = saving || loading || userSettingsLoading;

  // TV Grid Data Builders
  const connectionGridData = useMemo<SettingsGridItem[]>(
    () => [
      {
        type: 'header',
        id: 'connection-header',
        title: 'Backend Connection',
        description:
          'Enter the backend URL and the 6-digit PIN shown when the backend starts. Make sure to append /api to the URL.',
      },
      {
        type: 'text-field',
        id: 'backend-url',
        label: 'Backend URL',
        value: backendUrlInput,
        fieldKey: 'backendUrl',
        options: { placeholder: 'http://localhost:7777/api' },
      },
      {
        type: 'text-field',
        id: 'backend-pin',
        label: 'API PIN (6 digits)',
        value: backendApiKeyInput,
        fieldKey: 'backendApiKey',
        options: { keyboardType: 'numeric', placeholder: '123456' },
      },
      {
        type: 'button-row',
        id: 'connection-buttons',
        buttons: [
          { label: 'Apply', action: 'connection-apply', disabled: !isReady || busy },
        ],
      },
    ],
    [backendUrlInput, backendApiKeyInput, isReady, busy],
  );

  const playbackGridData = useMemo<SettingsGridItem[]>(() => {
    if (!editableSettings) return [];
    return [
      {
        type: 'header',
        id: 'playback-player-header',
        title: 'Player Preference',
        description:
          'Choose which video player to use for playback. Native uses the built-in player, or select an external app.',
      },
      {
        type: 'dropdown',
        id: 'playback-player',
        label: 'Video Player',
        value: editableSettings.playback.preferredPlayer || 'native',
        options:
          Platform.OS === 'android'
            ? [{ label: 'Native', value: 'native' }]
            : [
                { label: 'Native', value: 'native' },
                { label: 'Infuse', value: 'infuse' },
              ],
        fieldKey: 'playback.preferredPlayer',
      },
      {
        type: 'header',
        id: 'playback-lang-header',
        title: 'Audio & Subtitle Preferences',
        description: 'Set your preferred languages using ISO 639-2 codes (e.g., "eng" for English, "spa" for Spanish).',
      },
      {
        type: 'text-field',
        id: 'playback-audio-lang',
        label: 'Audio Language',
        value: editableSettings.playback.preferredAudioLanguage || '',
        fieldKey: 'playback.preferredAudioLanguage',
        options: { placeholder: 'eng' },
      },
      {
        type: 'text-field',
        id: 'playback-subtitle-lang',
        label: 'Subtitle Language',
        value: editableSettings.playback.preferredSubtitleLanguage || '',
        fieldKey: 'playback.preferredSubtitleLanguage',
        options: { placeholder: 'eng' },
      },
      {
        type: 'dropdown',
        id: 'playback-subtitle-mode',
        label: 'Subtitle Mode',
        value: editableSettings.playback.preferredSubtitleMode || 'off',
        options: [
          { label: 'Off', value: 'off' },
          { label: 'On', value: 'on' },
          { label: 'Forced Only', value: 'forced-only' },
        ],
        fieldKey: 'playback.preferredSubtitleMode',
      },
      {
        type: 'text-field',
        id: 'playback-subtitle-size',
        label: 'Subtitle Size',
        value: String(editableSettings.playback.subtitleSize ?? 1.0),
        fieldKey: 'playback.subtitleSize',
        options: { placeholder: '1.0', keyboardType: 'numeric' as const },
      },
      {
        type: 'header',
        id: 'playback-loading-header',
        title: 'Loading Screen',
        description: 'Display a themed loading screen while content buffers.',
      },
      {
        type: 'toggle',
        id: 'playback-loading-screen',
        label: 'Use Loading Screen',
        value: editableSettings.playback.useLoadingScreen ?? false,
        fieldKey: 'playback.useLoadingScreen',
        description: 'Show a loading screen while video buffers',
      },
      {
        type: 'button',
        id: 'playback-preview-loading',
        label: 'Preview Loading Screen',
        action: 'preview-loading-screen',
      },
      {
        type: 'button',
        id: 'playback-save',
        label: dirty ? 'Save Changes' : 'Saved',
        action: 'save-settings',
        disabled: !dirty || busy,
      },
    ];
  }, [editableSettings, dirty, busy]);

  const filteringGridData = useMemo<SettingsGridItem[]>(() => {
    if (!editableSettings) return [];
    return [
      {
        type: 'header',
        id: 'filtering-size-header',
        title: 'Size Limits',
        description: 'Set maximum file sizes for movies and episodes. Use 0 for no limit.',
      },
      {
        type: 'text-field',
        id: 'filtering-max-movie',
        label: 'Max Movie Size (GB)',
        value: editableSettings.filtering.maxSizeMovieGb || '',
        fieldKey: 'filtering.maxSizeMovieGb',
        options: { keyboardType: 'numeric', placeholder: '0' },
      },
      {
        type: 'text-field',
        id: 'filtering-max-episode',
        label: 'Max Episode Size (GB)',
        value: editableSettings.filtering.maxSizeEpisodeGb || '',
        fieldKey: 'filtering.maxSizeEpisodeGb',
        options: { keyboardType: 'numeric', placeholder: '0' },
      },
      {
        type: 'header',
        id: 'filtering-quality-header',
        title: 'Quality Filters',
        description: 'Control which releases appear in search results based on quality and keywords.',
      },
      {
        type: 'toggle',
        id: 'filtering-exclude-hdr',
        label: 'Exclude HDR',
        value: editableSettings.filtering.excludeHdr ?? false,
        fieldKey: 'filtering.excludeHdr',
        description: 'Filter out HDR content from search results',
      },
      {
        type: 'text-field',
        id: 'filtering-filter-terms',
        label: 'Filter Out Terms',
        value: editableSettings.filtering.filterOutTerms || '',
        fieldKey: 'filtering.filterOutTerms',
        options: { placeholder: 'cam, screener, telesync' },
      },
      {
        type: 'header',
        id: 'filtering-unplayable-header',
        title: 'Unplayable Releases',
        description: 'Manage releases that failed to play and were marked as unplayable.',
      },
      {
        type: 'button-row',
        id: 'filtering-unplayable-buttons',
        buttons: [
          { label: `Manage (${unplayableReleases.length})`, action: 'manage-unplayable' },
          ...(unplayableReleases.length > 0 ? [{ label: 'Clear All', action: 'clear-unplayable' }] : []),
        ],
      },
      {
        type: 'button',
        id: 'filtering-save',
        label: dirty ? 'Save Changes' : 'Saved',
        action: 'save-settings',
        disabled: !dirty || busy,
      },
    ];
  }, [editableSettings, dirty, busy, unplayableReleases.length]);

  const homeGridData = useMemo<SettingsGridItem[]>(() => {
    if (!editableSettings) return [];
    const items: SettingsGridItem[] = [
      {
        type: 'header',
        id: 'home-shelves-header',
        title: 'Home Screen Shelves',
        description: 'Reorder and enable/disable shelves on the home screen',
      },
    ];
    // Add shelf items
    editableSettings.homeShelves.shelves.forEach((shelf, index) => {
      items.push({
        type: 'shelf-item',
        id: `shelf-${shelf.id}`,
        key: `shelf-${shelf.id}`,
        shelf,
        index,
        total: editableSettings.homeShelves.shelves.length,
      });
    });
    // Trending source (only if not demo mode)
    if (!settings?.demoMode) {
      items.push({
        type: 'dropdown',
        id: 'home-trending-source',
        label: 'Trending Movies Source',
        value: editableSettings.homeShelves.trendingMovieSource || 'released',
        options: [
          { label: 'Released Only', value: 'released' },
          { label: 'All Trending', value: 'all' },
        ],
        fieldKey: 'homeShelves.trendingMovieSource',
      });
    }
    items.push({
      type: 'button',
      id: 'home-save',
      label: dirty ? 'Save Changes' : 'Saved',
      action: 'save-settings',
      disabled: !dirty || busy,
    });
    return items;
  }, [editableSettings, settings?.demoMode, dirty, busy]);

  const liveGridData = useMemo<SettingsGridItem[]>(
    () => [
      {
        type: 'header',
        id: 'live-hidden-header',
        title: 'Hidden Channels',
        description: 'Manage channels hidden from the Live TV screen',
      },
      {
        type: 'button',
        id: 'live-manage-hidden',
        label: `Manage Hidden Channels (${hiddenChannelsList.length})`,
        action: 'manage-hidden-channels',
      },
    ],
    [hiddenChannelsList.length],
  );

  // Get current tab grid data
  const currentTabGridData = useMemo<SettingsGridItem[]>(() => {
    // Title and tabs are now rendered in a separate header section on TV
    switch (activeTab) {
      case 'connection':
        return connectionGridData;
      case 'playback':
        return playbackGridData;
      case 'home':
        return homeGridData;
      case 'filtering':
        return filteringGridData;
      case 'live':
        return liveGridData;
      default:
        return [];
    }
  }, [activeTab, connectionGridData, playbackGridData, homeGridData, filteringGridData, liveGridData]);

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
        case 'preview-loading-screen':
          router.push('/strmr-loading');
          break;
        case 'manage-unplayable':
          setIsUnplayableReleasesModalOpen(true);
          break;
        case 'clear-unplayable':
          clearUnplayableReleases();
          showToast('Cleared all unplayable releases', { tone: 'success' });
          break;
        case 'manage-hidden-channels':
          setIsHiddenChannelsModalOpen(true);
          break;
      }
    },
    [handleBackendConnectionApply, handleSaveSettings, clearUnplayableReleases, showToast],
  );

  // TV Grid field update handler
  const handleGridFieldUpdate = useCallback(
    (fieldKey: string, value: string | boolean | number) => {
      if (!editableSettings) return;

      if (fieldKey.startsWith('playback.')) {
        const subKey = fieldKey.replace('playback.', '') as keyof EditableBackendSettings['playback'];
        // Store raw value - subtitleSize will be parsed to number on save
        setEditableSettings({
          ...editableSettings,
          playback: { ...editableSettings.playback, [subKey]: value },
        });
        setDirty(true);
      } else if (fieldKey.startsWith('filtering.')) {
        const subKey = fieldKey.replace('filtering.', '') as keyof EditableBackendSettings['filtering'];
        setEditableSettings({
          ...editableSettings,
          filtering: { ...editableSettings.filtering, [subKey]: value },
        });
        setDirty(true);
      } else if (fieldKey.startsWith('homeShelves.')) {
        const subKey = fieldKey.replace('homeShelves.', '') as keyof EditableBackendSettings['homeShelves'];
        setEditableSettings({
          ...editableSettings,
          homeShelves: { ...editableSettings.homeShelves, [subKey]: value },
        });
        setDirty(true);
      }
    },
    [editableSettings],
  );

  // TV Grid render item
  const renderGridItem = useCallback(
    ({ item }: { item: SettingsGridItem }) => {
      switch (item.type) {
        case 'header': {
          // Only wrap topmost headers (first header of each tab) with focusable view for scroll
          const topmostHeaders = [
            'connection-header',
            'playback-player-header',
            'filtering-size-header',
            'home-shelves-header',
            'live-hidden-header',
          ];
          const isTopmost = topmostHeaders.includes(item.id);

          const headerContent = (
            <View style={[styles.tvGridHeader, styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <Text style={styles.tvGridHeaderTitle}>{item.title}</Text>
              {item.description && <Text style={styles.tvGridHeaderDescription}>{item.description}</Text>}
            </View>
          );

          if (isTopmost) {
            return (
              <SpatialNavigationFocusableView
                style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}
                focusKey={`grid-${item.id}`}>
                {() => headerContent}
              </SpatialNavigationFocusableView>
            );
          }
          return headerContent;
        }

        case 'text-field': {
          // Inline text input configuration for supported fields
          const inlineFieldConfig: Record<
            string,
            {
              inputRef: React.RefObject<TextInput | null>;
              tempRef: React.MutableRefObject<string>;
              setValue: (value: string) => void;
            }
          > = {
            backendUrl: {
              inputRef: backendUrlInputRef,
              tempRef: tempBackendUrlRef,
              setValue: setBackendUrlInput,
            },
            backendApiKey: {
              inputRef: backendPinInputRef,
              tempRef: tempBackendPinRef,
              setValue: setBackendApiKeyInput,
            },
            'playback.preferredAudioLanguage': {
              inputRef: audioLangInputRef,
              tempRef: tempAudioLangRef,
              setValue: (value: string) => {
                if (editableSettings) {
                  setEditableSettings({
                    ...editableSettings,
                    playback: { ...editableSettings.playback, preferredAudioLanguage: value },
                  });
                  setDirty(true);
                }
              },
            },
            'playback.preferredSubtitleLanguage': {
              inputRef: subtitleLangInputRef,
              tempRef: tempSubtitleLangRef,
              setValue: (value: string) => {
                if (editableSettings) {
                  setEditableSettings({
                    ...editableSettings,
                    playback: { ...editableSettings.playback, preferredSubtitleLanguage: value },
                  });
                  setDirty(true);
                }
              },
            },
            'live.playlistUrl': {
              inputRef: playlistUrlInputRef,
              tempRef: tempPlaylistUrlRef,
              setValue: (value: string) => {
                if (editableSettings) {
                  setEditableSettings({
                    ...editableSettings,
                    live: { ...editableSettings.live, playlistUrl: value },
                  });
                  setDirty(true);
                }
              },
            },
            'filtering.maxSizeMovieGb': {
              inputRef: maxMovieSizeInputRef,
              tempRef: tempMaxMovieSizeRef,
              setValue: (value: string) => {
                if (editableSettings) {
                  setEditableSettings({
                    ...editableSettings,
                    filtering: { ...editableSettings.filtering, maxSizeMovieGb: value },
                  });
                  setDirty(true);
                }
              },
            },
            'filtering.maxSizeEpisodeGb': {
              inputRef: maxEpisodeSizeInputRef,
              tempRef: tempMaxEpisodeSizeRef,
              setValue: (value: string) => {
                if (editableSettings) {
                  setEditableSettings({
                    ...editableSettings,
                    filtering: { ...editableSettings.filtering, maxSizeEpisodeGb: value },
                  });
                  setDirty(true);
                }
              },
            },
            'filtering.filterOutTerms': {
              inputRef: filterTermsInputRef,
              tempRef: tempFilterTermsRef,
              setValue: (value: string) => {
                if (editableSettings) {
                  setEditableSettings({
                    ...editableSettings,
                    filtering: { ...editableSettings.filtering, filterOutTerms: value },
                  });
                  setDirty(true);
                }
              },
            },
          };

          const fieldConfig = inlineFieldConfig[item.fieldKey];
          if (fieldConfig) {
            const { inputRef, tempRef, setValue } = fieldConfig;

            const handleInlineFocus = () => {
              lockNavigation();
              setActiveInlineInput(item.fieldKey);
            };

            const handleInlineBlur = () => {
              unlockNavigation();
              setActiveInlineInput(null);
              // Apply the value from temp ref on blur
              if (Platform.isTV) {
                setValue(tempRef.current);
              }
            };

            const handleInlineChangeText = (text: string) => {
              if (Platform.isTV) {
                tempRef.current = text;
              } else {
                setValue(text);
              }
            };

            return (
              <SpatialNavigationFocusableView
                style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}
                focusKey={`grid-${item.id}`}
                onSelect={() => {
                  inputRef.current?.focus();
                }}
                onBlur={() => {
                  inputRef.current?.blur();
                  Keyboard.dismiss();
                }}>
                {({ isFocused }: { isFocused: boolean }) => (
                  <Pressable tvParallaxProperties={{ enabled: false }}>
                    <View style={[styles.tvGridInlineInputRow, isFocused && styles.tvGridInlineInputRowFocused]}>
                      <Text style={styles.tvGridInlineInputLabel}>{item.label}</Text>
                      <TextInput
                        ref={inputRef}
                        style={[styles.tvGridInlineInput, isFocused && styles.tvGridInlineInputFocused]}
                        {...(Platform.isTV ? { defaultValue: item.value } : { value: item.value })}
                        onChangeText={handleInlineChangeText}
                        onFocus={handleInlineFocus}
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
                        editable={isFocused}
                        underlineColorAndroid="transparent"
                        importantForAutofill="no"
                        disableFullscreenUI={true}
                        {...(Platform.OS === 'ios' &&
                          Platform.isTV && {
                            keyboardAppearance: 'dark',
                          })}
                      />
                    </View>
                  </Pressable>
                )}
              </SpatialNavigationFocusableView>
            );
          }

          // For other text fields, keep the modal approach
          return (
            <SpatialNavigationFocusableView
              style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}
              focusKey={`grid-${item.id}`}
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
          );
        }

        case 'toggle':
          return (
            <SpatialNavigationFocusableView
              style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}
              focusKey={`grid-${item.id}`}
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
          );

        case 'dropdown':
          return (
            <View style={[styles.tvGridDropdownRowInline, styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <Text style={styles.tvGridInlineInputLabel}>{item.label}</Text>
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.tvGridDropdownOptionsInline}>
                  {item.options.map((option) => (
                    <FocusablePressable
                      key={option.value}
                      focusKey={`grid-${item.id}-${option.value}`}
                      text={option.label}
                      onSelect={() => handleGridFieldUpdate(item.fieldKey, option.value)}
                      style={[styles.dropdownOption, item.value === option.value && styles.dropdownOptionSelected]}
                      textStyle={styles.dropdownOptionText as TextStyle}
                      focusedTextStyle={styles.dropdownOptionTextFocused as TextStyle}
                    />
                  ))}
                </View>
              </SpatialNavigationNode>
            </View>
          );

        case 'button':
          return (
            <View style={[styles.tvGridButtonRow, styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <FocusablePressable
                focusKey={`grid-${item.id}`}
                text={item.label}
                onSelect={() => handleGridAction(item.action)}
                disabled={item.disabled}
              />
            </View>
          );

        case 'button-row':
          return (
            <View style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <SpatialNavigationNode orientation="horizontal">
                <View style={styles.tvGridButtonRow}>
                  {item.buttons.map((btn) => (
                    <FocusablePressable
                      key={btn.action}
                      focusKey={`grid-${item.id}-${btn.action}`}
                      text={btn.label}
                      onSelect={() => handleGridAction(btn.action)}
                      disabled={btn.disabled}
                    />
                  ))}
                </View>
              </SpatialNavigationNode>
            </View>
          );

        case 'shelf-item': {
          const { shelf, index, total } = item;
          return (
            <AnimatedShelfItem
              pulseKey={shelfPulses[shelf.id]?.key || 0}
              pulseIntensity={shelfPulses[shelf.id]?.intensity || 'primary'}
              style={[styles.tvGridItemFullWidth, styles.tvGridItemSpacing]}>
              <SpatialNavigationNode orientation="horizontal">
                <View style={[styles.tvGridFieldRow, { opacity: shelf.enabled ? 1 : 0.6 }]}>
                  <Text style={styles.tvGridShelfLabel}>{shelf.name}</Text>
                  <View style={styles.shelfControls}>
                    <View style={styles.shelfArrowButtons}>
                      <FocusablePressable
                        focusKey={`shelf-up-${shelf.id}`}
                        text="↑"
                        onSelect={() => moveShelfUp(index)}
                        disabled={index === 0}
                        style={[styles.tvModalItemButton, { justifyContent: 'center', alignItems: 'center' }]}
                        focusedStyle={styles.tvModalItemButtonFocused}
                        textStyle={styles.shelfArrowButtonText as TextStyle}
                        focusedTextStyle={styles.shelfArrowButtonText as TextStyle}
                      />
                      <FocusablePressable
                        focusKey={`shelf-down-${shelf.id}`}
                        text="↓"
                        onSelect={() => moveShelfDown(index)}
                        disabled={index === total - 1}
                        style={[styles.tvModalItemButton, { justifyContent: 'center', alignItems: 'center' }]}
                        focusedStyle={styles.tvModalItemButtonFocused}
                        textStyle={styles.shelfArrowButtonText as TextStyle}
                        focusedTextStyle={styles.shelfArrowButtonText as TextStyle}
                      />
                    </View>
                    <SpatialNavigationFocusableView
                      focusKey={`shelf-toggle-${shelf.id}`}
                      onSelect={() => updateShelf(index, 'enabled', !shelf.enabled)}>
                      {({ isFocused }: { isFocused: boolean }) => (
                        <View
                          style={[
                            styles.tvGridCustomToggle,
                            {
                              backgroundColor: shelf.enabled
                                ? theme.colors.accent.primary
                                : theme.colors.border.emphasis,
                            },
                            isFocused && {
                              transform: [{ scale: 1.1 }],
                              borderWidth: 2,
                              borderColor: theme.colors.text.primary,
                            },
                          ]}>
                          <View
                            style={[
                              styles.tvGridCustomToggleThumb,
                              { alignSelf: shelf.enabled ? 'flex-end' : 'flex-start' },
                            ]}
                          />
                        </View>
                      )}
                    </SpatialNavigationFocusableView>
                  </View>
                </View>
              </SpatialNavigationNode>
            </AnimatedShelfItem>
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
      moveShelfUp,
      moveShelfDown,
      updateShelf,
      setActiveTab,
      lockNavigation,
      unlockNavigation,
      activeInlineInput,
      setBackendUrlInput,
      setBackendApiKeyInput,
      editableSettings,
      setDirty,
      shelfPulses,
    ],
  );

  return (
    <>
      <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
        <Stack.Screen options={{ headerShown: false }} />
        <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
          {/* TV Layout: Header at top, then grid below */}
          {Platform.isTV && (
            <View style={styles.tvLayoutContainer}>
              {/* Header Section - at top of screen */}
              <View style={styles.tvHeader}>
                <Text style={styles.tvScreenTitle}>Settings</Text>
                <SpatialNavigationNode orientation="horizontal">
                  <View style={styles.tvTabBar}>
                    {tabs.map((tab) => {
                      const requiresBackend = ['playback', 'home', 'filtering', 'live'].includes(tab.key);
                      const isDisabled = requiresBackend && !isBackendReachable;
                      const isActiveTab = activeTab === tab.key;
                      const tabButton = (
                        <FocusablePressable
                          key={tab.key}
                          text={tab.label}
                          onSelect={() => setActiveTab(tab.key)}
                          style={[styles.tvTabButton, isActiveTab && styles.tvTabButtonActive]}
                          disabled={isDisabled}
                        />
                      );
                      if (tab.key === 'connection') {
                        return <DefaultFocus key={tab.key}>{tabButton}</DefaultFocus>;
                      }
                      return tabButton;
                    })}
                  </View>
                </SpatialNavigationNode>
              </View>

              {/* Grid Content - with edge buffer */}
              <View style={styles.tvContentArea}>
                {currentTabGridData.length > 0 && (
                  <View style={styles.tvGridContainer}>
                    <SpatialNavigationVirtualizedGrid
                      data={currentTabGridData}
                      renderItem={renderGridItem}
                      numberOfColumns={1}
                      itemHeight={(styles.tvGridItemHeight as { height: number }).height}
                      numberOfRenderedRows={Math.max(currentTabGridData.length, 10)}
                      numberOfRowsVisibleOnScreen={Math.min(8, currentTabGridData.length)}
                      rowContainerStyle={styles.tvGridRowContainer}
                    />
                  </View>
                )}
              </View>
            </View>
          )}
          {/* Mobile Layout: ScrollView with all content */}
          {!Platform.isTV && (
            <View style={styles.mobileContainer}>
              <SpatialNavigationScrollView
                style={styles.container}
                contentContainerStyle={styles.contentContainer}
                contentInsetAdjustmentBehavior="never"
                automaticallyAdjustContentInsets={false}>
                <Text style={styles.screenTitle}>Settings</Text>

                {/* Tab Bar */}
                <SpatialNavigationNode orientation="horizontal">
                  <View style={styles.tabBar}>
                    {tabs.map((tab) => {
                      const requiresBackend = ['playback', 'home', 'filtering', 'live'].includes(tab.key);
                      const isDisabled = requiresBackend && !isBackendReachable;
                      const tabButton = (
                        <FocusablePressable
                          key={tab.key}
                          text={tab.label}
                          onSelect={() => setActiveTab(tab.key)}
                          style={[styles.tab, activeTab === tab.key && styles.tabActive]}
                          disabled={isDisabled}
                        />
                      );
                      // Set default focus to Backend tab on TV
                      if (tab.key === 'connection' && Platform.isTV) {
                        return <DefaultFocus key={tab.key}>{tabButton}</DefaultFocus>;
                      }
                      return tabButton;
                    })}
                  </View>
                </SpatialNavigationNode>

                {/* Mobile Tab Content */}
                {/* Connection Tab */}
                {!Platform.isTV && activeTab === 'connection' && (
                  <View style={styles.section}>
                    <Text style={styles.sectionTitle}>Backend Connection</Text>
                    <Text style={styles.sectionDescription}>
                      Enter the backend URL and the 6-digit PIN shown when the backend starts. Make sure to append /api
                      to the URL.
                    </Text>
                    <TextInputField
                      label="Backend URL"
                      value={backendUrlInput}
                      onChange={setBackendUrlInput}
                      options={{ placeholder: 'http://localhost:7777/api' }}
                      styles={styles}
                    />
                    <TextInputField
                      label="API PIN (6 digits)"
                      value={backendApiKeyInput}
                      onChange={setBackendApiKeyInput}
                      options={{ secureTextEntry: false, placeholder: '123456', keyboardType: 'numeric' }}
                      styles={styles}
                    />
                    <SpatialNavigationNode orientation="horizontal">
                      <View style={styles.buttonRow}>
                        <FocusablePressable
                          text="Apply"
                          onSelect={handleBackendConnectionApply}
                          disabled={!isReady || busy}
                        />
                      </View>
                    </SpatialNavigationNode>
                    {error && <Text style={[styles.statusText, styles.statusError]}>{error}</Text>}
                  </View>
                )}

                {/* Content Sources Tab */}
                {!Platform.isTV && activeTab === 'content' && editableSettings && (
                  <>
                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Streaming Mode</Text>
                      <Text style={styles.sectionDescription}>
                        Choose which streaming services to use when resolving content.
                      </Text>
                      <SpatialNavigationNode orientation="horizontal">
                        <View style={styles.playbackOptionsRow}>
                          {streamingModeOptions.map((option) => {
                            const isSelected = editableSettings?.streaming?.serviceMode === option.value;
                            return (
                              <FocusablePressable
                                key={option.value}
                                text={option.label}
                                onSelect={() => updateStreamingServiceMode(option.value)}
                                style={[styles.playbackOption, isSelected && styles.playbackOptionSelected]}
                                disabled={!editableSettings}
                              />
                            );
                          })}
                        </View>
                      </SpatialNavigationNode>
                      <View style={{ marginTop: 16 }}>
                        <DropdownField
                          label="Service Priority"
                          value={editableSettings.streaming.servicePriority}
                          options={servicePriorityOptions}
                          onChange={(val) => updateStreamingServicePriority(val as StreamingServicePriority)}
                          styles={styles}
                        />
                      </View>
                    </View>

                    <View style={styles.section}>
                      <View style={styles.sectionHeader}>
                        <Text style={styles.sectionTitle}>Usenet Providers</Text>
                        <FocusablePressable text="Add" onSelect={handleAddUsenetProvider} />
                      </View>
                      {editableSettings.usenet.length === 0 && (
                        <Text style={styles.sectionDescription}>No usenet providers configured yet.</Text>
                      )}
                      {editableSettings.usenet.map((provider, index) => renderUsenetProvider(provider, index))}
                    </View>

                    <View style={styles.section}>
                      <View style={styles.sectionHeader}>
                        <Text style={styles.sectionTitle}>Indexers</Text>
                        <FocusablePressable text="Add" onSelect={handleAddIndexer} />
                      </View>
                      {editableSettings.indexers.length === 0 && (
                        <Text style={styles.sectionDescription}>No indexers configured yet.</Text>
                      )}
                      {editableSettings.indexers.map((indexer, index) => renderIndexer(indexer, index))}
                    </View>

                    <View style={styles.section}>
                      <View style={styles.sectionHeader}>
                        <Text style={styles.sectionTitle}>Debrid Providers</Text>
                        <FocusablePressable text="Add" onSelect={handleAddDebridProvider} />
                      </View>
                      {(editableSettings?.streaming?.debridProviders?.length ?? 0) === 0 && (
                        <Text style={styles.sectionDescription}>No debrid providers configured yet.</Text>
                      )}
                      {(editableSettings?.streaming?.debridProviders ?? []).map((provider, index) =>
                        renderDebridProvider(provider, index),
                      )}
                    </View>

                    <View style={styles.section}>
                      <View style={styles.sectionHeader}>
                        <Text style={styles.sectionTitle}>Scrapers</Text>
                        <FocusablePressable text="Add" onSelect={handleAddTorrentScraper} />
                      </View>
                      <Text style={styles.sectionDescription}>
                        Configure torrent search providers. Torrentio is enabled by default and requires no
                        configuration.
                      </Text>
                      {editableSettings.torrentScrapers.length === 0 && (
                        <Text style={styles.sectionDescription}>No torrent scrapers configured yet.</Text>
                      )}
                      {editableSettings.torrentScrapers.map((scraper, index) => renderTorrentScraper(scraper, index))}
                    </View>
                  </>
                )}

                {/* Playback Tab */}
                {!Platform.isTV && activeTab === 'playback' && editableSettings && (
                  <>
                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Player Preference</Text>
                      <Text style={styles.sectionDescription}>
                        Choose which player to launch after resolving a search result.
                      </Text>
                      <SpatialNavigationNode orientation="horizontal">
                        <View style={styles.playbackOptionsRow}>
                          {playbackOptions.map((option) => {
                            const isSelected = editableSettings.playback.preferredPlayer === option.value;
                            return (
                              <FocusablePressable
                                key={option.value}
                                text={option.label}
                                onSelect={() => updatePlaybackMode(option.value)}
                                style={[styles.playbackOption, isSelected && styles.playbackOptionSelected]}
                              />
                            );
                          })}
                        </View>
                      </SpatialNavigationNode>
                    </View>

                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Audio & Subtitle Preferences</Text>
                      <Text style={styles.sectionDescription}>
                        Set default audio and subtitle track preferences. Use 3-letter language codes (e.g., eng, spa,
                        fra) or full names.
                      </Text>
                      <TextInputField
                        label="Preferred Audio Language"
                        value={editableSettings.playback?.preferredAudioLanguage ?? ''}
                        onChange={updatePlaybackField('preferredAudioLanguage')}
                        options={{ placeholder: 'e.g., eng, English' }}
                        styles={styles}
                      />
                      <TextInputField
                        label="Preferred Subtitle Language"
                        value={editableSettings.playback?.preferredSubtitleLanguage ?? ''}
                        onChange={updatePlaybackField('preferredSubtitleLanguage')}
                        options={{ placeholder: 'e.g., eng, English' }}
                        styles={styles}
                      />
                      <DropdownField
                        label="Subtitle Mode"
                        value={editableSettings.playback?.preferredSubtitleMode ?? 'off'}
                        options={[
                          { label: 'Off', value: 'off' },
                          { label: 'On', value: 'on' },
                          { label: 'Forced Only', value: 'forced-only' },
                        ]}
                        onChange={(value) => updatePlaybackField('preferredSubtitleMode')(value)}
                        styles={styles}
                      />
                      <TextInputField
                        label="Subtitle Size"
                        value={String(editableSettings.playback?.subtitleSize ?? 1.0)}
                        onChange={(value) => {
                          // Store raw string value to allow typing decimals like "0.5"
                          // Will be parsed to number on save
                          updatePlaybackField('subtitleSize')(value);
                        }}
                        options={{ placeholder: '1.0', keyboardType: 'numeric' }}
                        styles={styles}
                      />
                    </View>

                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Loading Screen</Text>
                      <Text style={styles.sectionDescription}>
                        Enable the custom loading screen during playback initialization.
                      </Text>
                      <View style={styles.fieldRow}>
                        <Text style={styles.fieldLabel}>Use Loading Screen</Text>
                        <Switch
                          value={editableSettings.playback?.useLoadingScreen ?? false}
                          onValueChange={(next) => {
                            setDirty(true);
                            setEditableSettings((current) =>
                              current
                                ? {
                                    ...current,
                                    playback: {
                                      ...current.playback,
                                      useLoadingScreen: next,
                                    },
                                  }
                                : current,
                            );
                          }}
                          trackColor={{ true: theme.colors.accent.primary, false: theme.colors.border.subtle }}
                          thumbColor="#FFFFFF"
                        />
                      </View>
                      <FocusablePressable
                        text="Preview Loading Screen"
                        onSelect={() => router.push('/strmr-loading')}
                        style={styles.secondaryButton}
                      />
                    </View>
                  </>
                )}

                {/* Home Screen Tab */}
                {!Platform.isTV && activeTab === 'home' && editableSettings && (
                  <>
                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Home Screen Shelves</Text>
                      <Text style={styles.sectionDescription}>
                        Control which content shelves appear on your home screen and their order. Disabled shelves will
                        be hidden.
                      </Text>
                      {editableSettings.homeShelves.shelves.map((shelf, index) => (
                        <Animated.View
                          key={shelf.id}
                          layout={Layout.springify().damping(45).stiffness(250)}
                          style={[styles.indexerCard, styles.shelfCard, !shelf.enabled && styles.shelfCardDisabled]}>
                          <View style={styles.shelfManagementRow}>
                            <View style={styles.shelfInfo}>
                              <Text style={[styles.shelfName, !shelf.enabled && styles.shelfNameDisabled]}>
                                {shelf.name}
                              </Text>
                            </View>
                            <SpatialNavigationNode orientation="horizontal">
                              <View style={styles.shelfControls}>
                                <FocusablePressable
                                  text="↑"
                                  onSelect={() => moveShelfUp(index)}
                                  disabled={index === 0}
                                  style={[styles.shelfArrowButton, index === 0 && styles.shelfArrowButtonDisabled]}
                                />
                                <FocusablePressable
                                  text="↓"
                                  onSelect={() => moveShelfDown(index)}
                                  disabled={index === editableSettings.homeShelves.shelves.length - 1}
                                  style={[
                                    styles.shelfArrowButton,
                                    index === editableSettings.homeShelves.shelves.length - 1 &&
                                      styles.shelfArrowButtonDisabled,
                                  ]}
                                />
                                <SpatialNavigationFocusableView
                                  onSelect={() => updateShelf(index, 'enabled', !shelf.enabled)}>
                                  {({ isFocused }: { isFocused: boolean }) => (
                                    <Switch
                                      value={shelf.enabled}
                                      onValueChange={(next) => updateShelf(index, 'enabled', next)}
                                      trackColor={{
                                        true: theme.colors.accent.primary,
                                        false: theme.colors.border.subtle,
                                      }}
                                      thumbColor="#FFFFFF"
                                      style={[styles.shelfToggle, isFocused && styles.switchFocused]}
                                    />
                                  )}
                                </SpatialNavigationFocusableView>
                              </View>
                            </SpatialNavigationNode>
                          </View>
                        </Animated.View>
                      ))}
                    </View>

                    {!settings?.demoMode && (
                      <View style={styles.section}>
                        <Text style={styles.sectionTitle}>Trending Movies Source</Text>
                        <Text style={styles.sectionDescription}>
                          Choose which source to use for the Trending Movies shelf.
                        </Text>
                        <SpatialNavigationNode orientation="horizontal">
                          <View style={styles.playbackOptionsRow}>
                            <FocusablePressable
                              text="Released Only"
                              onSelect={() => updateTrendingMovieSource('released')}
                              style={[
                                styles.playbackOption,
                                editableSettings.homeShelves.trendingMovieSource === 'released' &&
                                  styles.playbackOptionSelected,
                              ]}
                            />
                            <FocusablePressable
                              text="All Trending"
                              onSelect={() => updateTrendingMovieSource('all')}
                              style={[
                                styles.playbackOption,
                                editableSettings.homeShelves.trendingMovieSource === 'all' &&
                                  styles.playbackOptionSelected,
                              ]}
                            />
                          </View>
                        </SpatialNavigationNode>
                        <Text style={styles.sectionDescription}>
                          "Released Only" shows top movies of the week (already released). "All Trending" includes
                          upcoming movies from TMDB.
                        </Text>
                      </View>
                    )}
                  </>
                )}

                {/* Advanced Tab */}
                {!Platform.isTV && activeTab === 'advanced' && editableSettings && (
                  <>
                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Streaming</Text>
                      <TextInputField
                        label="Max download workers"
                        value={editableSettings?.streaming?.maxDownloadWorkers ?? ''}
                        onChange={updateStreamingNumericField('maxDownloadWorkers')}
                        options={{ keyboardType: 'numeric' }}
                        errorMessage={fieldErrors['streaming.maxDownloadWorkers']}
                        styles={styles}
                      />
                      <TextInputField
                        label="Cache size (MB)"
                        value={editableSettings?.streaming?.maxCacheSizeMB ?? ''}
                        onChange={updateStreamingNumericField('maxCacheSizeMB')}
                        options={{ keyboardType: 'numeric' }}
                        errorMessage={fieldErrors['streaming.maxCacheSizeMB']}
                        styles={styles}
                      />
                    </View>

                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Metadata</Text>
                      <TextInputField
                        label="TVDB API Key"
                        value={editableSettings.metadata.tvdbApiKey}
                        onChange={updateMetadataField('tvdbApiKey')}
                        styles={styles}
                      />
                      <TextInputField
                        label="TMDB API Key"
                        value={editableSettings.metadata.tmdbApiKey}
                        onChange={updateMetadataField('tmdbApiKey')}
                        styles={styles}
                      />
                      <TextInputField
                        label="Language"
                        value={editableSettings.metadata.language}
                        onChange={updateMetadataField('language')}
                        styles={styles}
                      />
                    </View>

                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Developer Tools</Text>
                      <Text style={styles.sectionDescription}>Debug and testing tools for development purposes.</Text>
                      <FocusablePressable
                        text="MP4Box Debug Player"
                        onSelect={() => router.push('/mp4box-debug')}
                        style={styles.debugButton}
                      />
                      <Text style={styles.sectionDescription}>
                        Test Dolby Vision and HDR streaming using MP4Box instead of FFmpeg. Enter a direct media URL to
                        probe and play.
                      </Text>
                    </View>
                  </>
                )}

                {/* Filtering Tab */}
                {!Platform.isTV && activeTab === 'filtering' && editableSettings && (
                  <>
                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Size Limits</Text>
                      <Text style={styles.sectionDescription}>
                        Set maximum file sizes for content. Use 0 to disable size filtering.
                      </Text>
                      <TextInputField
                        label="Max Movie Size (GB)"
                        value={editableSettings.filtering.maxSizeMovieGb}
                        onChange={updateFilteringField('maxSizeMovieGb')}
                        options={{ keyboardType: 'numeric', placeholder: '0 = no limit' }}
                        styles={styles}
                      />
                      <TextInputField
                        label="Max Episode Size (GB)"
                        value={editableSettings.filtering.maxSizeEpisodeGb}
                        onChange={updateFilteringField('maxSizeEpisodeGb')}
                        options={{ keyboardType: 'numeric', placeholder: '0 = no limit' }}
                        styles={styles}
                      />
                    </View>

                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Quality Filters</Text>
                      <Text style={styles.sectionDescription}>Filter content based on quality attributes.</Text>
                      <View style={styles.fieldRow}>
                        <Text style={styles.fieldLabel}>Exclude HDR</Text>
                        <Switch
                          value={editableSettings.filtering.excludeHdr}
                          onValueChange={updateFilteringField('excludeHdr')}
                          trackColor={{ true: theme.colors.accent.primary, false: theme.colors.border.subtle }}
                          thumbColor="#FFFFFF"
                        />
                      </View>
                      <Text style={styles.sectionDescription}>
                        When enabled, HDR content (Dolby Vision, HDR10, HDR10+) will be filtered out from search
                        results.
                      </Text>
                    </View>

                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Filter Out Terms</Text>
                      <Text style={styles.sectionDescription}>
                        Exclude results containing specific terms. Enter a comma-separated list of terms to filter out.
                      </Text>
                      <TextInputField
                        label="Terms to Filter Out"
                        value={editableSettings.filtering.filterOutTerms}
                        onChange={updateFilteringField('filterOutTerms')}
                        options={{ placeholder: 'e.g., CAM, HDTS, Telesync' }}
                        styles={styles}
                      />
                      <Text style={styles.sectionDescription}>
                        Results containing any of these terms (case-insensitive) will be excluded from search results.
                      </Text>
                    </View>

                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Unplayable Releases</Text>
                      <Text style={styles.sectionDescription}>
                        Releases marked as unplayable are filtered from manual selection and autoplay. These are
                        typically releases that failed to stream due to errors.
                      </Text>
                      <FocusablePressable
                        text={`Manage Unplayable Releases (${unplayableReleases.length})`}
                        onSelect={() => setIsUnplayableReleasesModalOpen(true)}
                      />
                      {unplayableReleases.length > 0 && (
                        <FocusablePressable
                          text="Clear All Unplayable Releases"
                          onSelect={clearUnplayableReleases}
                          style={styles.secondaryButton}
                        />
                      )}
                    </View>
                  </>
                )}

                {/* Live TV Tab - M3U playlist is configured via admin web UI */}
                {!Platform.isTV && activeTab === 'live' && editableSettings && (
                  <>
                    <View style={styles.section}>
                      <Text style={styles.sectionTitle}>Hidden Channels</Text>
                      <Text style={styles.sectionDescription}>
                        Channels you've hidden from the Live TV page. Long press a channel card to hide it.
                      </Text>
                      <FocusablePressable
                        text={`Manage Hidden Channels (${hiddenChannelsList.length})`}
                        onSelect={() => setIsHiddenChannelsModalOpen(true)}
                      />
                    </View>
                  </>
                )}

                {/* Save button - shown on all non-connection tabs (mobile only) */}
                {!Platform.isTV && activeTab !== 'connection' && editableSettings && (
                  <View style={styles.section}>
                    {busy && (
                      <View style={styles.loadingRow}>
                        <ActivityIndicator size="small" color={theme.colors.accent.primary} />
                        <Text style={styles.loadingText}>{saving ? 'Saving settings…' : 'Loading settings…'}</Text>
                      </View>
                    )}
                    <FocusablePressable text="Save" onSelect={handleSaveSettings} disabled={busy || !dirty} />
                  </View>
                )}
              </SpatialNavigationScrollView>
            </View>
          )}

          {/* Hidden Channels Modal - Mobile */}
          {!Platform.isTV && (
            <Modal
              visible={isHiddenChannelsModalOpen}
              transparent={true}
              animationType="fade"
              onRequestClose={() => setIsHiddenChannelsModalOpen(false)}>
              <View style={styles.modalOverlay}>
                <View style={[styles.modalContent, { flexDirection: 'column' }]}>
                  <View style={[styles.modalHeader, { flexShrink: 0 }]}>
                    <Text style={styles.modalTitle}>Hidden Channels</Text>
                    <Pressable
                      onPress={() => setIsHiddenChannelsModalOpen(false)}
                      style={[styles.modalCloseButton, { paddingHorizontal: 16, paddingVertical: 8 }]}>
                      <Text style={styles.modalCloseButtonText}>Close</Text>
                    </Pressable>
                  </View>

                  {hiddenChannelsList.length === 0 ? (
                    <Text style={styles.sectionDescription}>No hidden channels.</Text>
                  ) : (
                    <ScrollView style={styles.modalScrollView} contentContainerStyle={styles.modalScrollContent}>
                      {hiddenChannelsList.map((channel) => (
                        <View key={channel.id} style={styles.hiddenChannelCard}>
                          <View style={styles.hiddenChannelInfo}>
                            <Text style={styles.hiddenChannelName}>{channel.name}</Text>
                            {channel.group && <Text style={styles.hiddenChannelGroup}>{channel.group}</Text>}
                          </View>
                          <Pressable
                            onPress={() => unhideChannel(channel.id)}
                            style={[styles.unhideButton, { paddingHorizontal: 12, paddingVertical: 6 }]}>
                            <Text style={styles.unhideButtonText}>Unhide</Text>
                          </Pressable>
                        </View>
                      ))}
                    </ScrollView>
                  )}
                </View>
              </View>
            </Modal>
          )}

          {/* Unplayable Releases Modal - Mobile */}
          {!Platform.isTV && (
            <Modal
              visible={isUnplayableReleasesModalOpen}
              transparent={true}
              animationType="fade"
              onRequestClose={() => setIsUnplayableReleasesModalOpen(false)}>
              <View style={styles.modalOverlay}>
                <View style={[styles.modalContent, { flexDirection: 'column' }]}>
                  <View style={[styles.modalHeader, { flexShrink: 0 }]}>
                    <Text style={styles.modalTitle}>Unplayable Releases</Text>
                    <Pressable
                      onPress={() => setIsUnplayableReleasesModalOpen(false)}
                      style={[styles.modalCloseButton, { paddingHorizontal: 16, paddingVertical: 8 }]}>
                      <Text style={styles.modalCloseButtonText}>Close</Text>
                    </Pressable>
                  </View>

                  {unplayableReleases.length === 0 ? (
                    <Text style={styles.sectionDescription}>No unplayable releases.</Text>
                  ) : (
                    <ScrollView style={styles.modalScrollView} contentContainerStyle={styles.modalScrollContent}>
                      {unplayableReleases.map((release) => (
                        <View key={release.sourcePath} style={styles.hiddenChannelCard}>
                          <View style={styles.hiddenChannelInfo}>
                            <Text style={styles.hiddenChannelName}>{release.title || release.sourcePath}</Text>
                            {release.reason && <Text style={styles.hiddenChannelGroup}>{release.reason}</Text>}
                            <Text style={styles.unplayableDate}>
                              Marked {new Date(release.markedAt).toLocaleDateString()}
                            </Text>
                          </View>
                          <Pressable
                            onPress={() => unmarkUnplayable(release.sourcePath)}
                            style={[styles.unhideButton, { paddingHorizontal: 12, paddingVertical: 6 }]}>
                            <Text style={styles.unhideButtonText}>Remove</Text>
                          </Pressable>
                        </View>
                      ))}
                    </ScrollView>
                  )}
                </View>
              </View>
            </Modal>
          )}
        </FixedSafeAreaView>
      </SpatialNavigationRoot>

      {/* Hidden Channels Modal - TV */}
      {Platform.isTV && isHiddenChannelsModalOpen && (
        <SpatialNavigationRoot isActive={isHiddenChannelsModalOpen}>
          <View style={styles.tvModalOverlay}>
            <View style={styles.tvModalContent}>
              <Text style={styles.tvModalTitle}>Hidden Channels</Text>
              <Text style={styles.tvModalSubtitle}>
                {hiddenChannelsList.length === 0
                  ? 'No hidden channels.'
                  : `${hiddenChannelsList.length} hidden channel${hiddenChannelsList.length === 1 ? '' : 's'}`}
              </Text>

              <SpatialNavigationNode orientation="vertical">
                <SpatialNavigationScrollView
                  style={styles.tvModalScrollView}
                  contentContainerStyle={styles.tvModalScrollContent}>
                  {hiddenChannelsList.map((channel, index) => (
                    <View key={channel.id} style={styles.tvModalItem}>
                      <View style={styles.tvModalItemInfo}>
                        <Text style={styles.tvModalItemTitle}>{channel.name}</Text>
                        {channel.group && <Text style={styles.tvModalItemSubtitle}>{channel.group}</Text>}
                      </View>
                      {index === 0 ? (
                        <DefaultFocus>
                          <FocusablePressable
                            focusKey={`unhide-channel-${channel.id}`}
                            text="Unhide"
                            onSelect={() => unhideChannel(channel.id)}
                            style={styles.tvModalItemButton}
                            focusedStyle={styles.tvModalItemButtonFocused}
                            textStyle={styles.tvModalItemButtonText}
                            focusedTextStyle={styles.tvModalItemButtonTextFocused}
                          />
                        </DefaultFocus>
                      ) : (
                        <FocusablePressable
                          focusKey={`unhide-channel-${channel.id}`}
                          text="Unhide"
                          onSelect={() => unhideChannel(channel.id)}
                          style={styles.tvModalItemButton}
                          focusedStyle={styles.tvModalItemButtonFocused}
                          textStyle={styles.tvModalItemButtonText}
                          focusedTextStyle={styles.tvModalItemButtonTextFocused}
                        />
                      )}
                    </View>
                  ))}
                </SpatialNavigationScrollView>

                <View style={styles.tvModalFooter}>
                  {hiddenChannelsList.length === 0 ? (
                    <DefaultFocus>
                      <FocusablePressable
                        focusKey="close-hidden-channels"
                        text="Close"
                        onSelect={() => setIsHiddenChannelsModalOpen(false)}
                        style={styles.tvModalCloseButton}
                        focusedStyle={styles.tvModalCloseButtonFocused}
                        textStyle={styles.tvModalCloseButtonText}
                        focusedTextStyle={styles.tvModalCloseButtonTextFocused}
                      />
                    </DefaultFocus>
                  ) : (
                    <FocusablePressable
                      focusKey="close-hidden-channels"
                      text="Close"
                      onSelect={() => setIsHiddenChannelsModalOpen(false)}
                      style={styles.tvModalCloseButton}
                      focusedStyle={styles.tvModalCloseButtonFocused}
                      textStyle={styles.tvModalCloseButtonText}
                      focusedTextStyle={styles.tvModalCloseButtonTextFocused}
                    />
                  )}
                </View>
              </SpatialNavigationNode>
            </View>
          </View>
        </SpatialNavigationRoot>
      )}

      {/* Unplayable Releases Modal - TV */}
      {Platform.isTV && isUnplayableReleasesModalOpen && (
        <SpatialNavigationRoot isActive={isUnplayableReleasesModalOpen}>
          <View style={styles.tvModalOverlay}>
            <View style={styles.tvModalContent}>
              <Text style={styles.tvModalTitle}>Unplayable Releases</Text>
              <Text style={styles.tvModalSubtitle}>
                {unplayableReleases.length === 0
                  ? 'No unplayable releases.'
                  : `${unplayableReleases.length} unplayable release${unplayableReleases.length === 1 ? '' : 's'}`}
              </Text>

              <SpatialNavigationNode orientation="vertical">
                <SpatialNavigationScrollView
                  style={styles.tvModalScrollView}
                  contentContainerStyle={styles.tvModalScrollContent}>
                  {unplayableReleases.map((release, index) => (
                    <View key={release.sourcePath} style={styles.tvModalItem}>
                      <View style={styles.tvModalItemInfo}>
                        <Text style={styles.tvModalItemTitle} numberOfLines={1}>
                          {release.title || release.sourcePath}
                        </Text>
                        {release.reason && <Text style={styles.tvModalItemSubtitle}>{release.reason}</Text>}
                        <Text style={styles.tvModalItemMeta}>
                          Marked {new Date(release.markedAt).toLocaleDateString()}
                        </Text>
                      </View>
                      {index === 0 ? (
                        <DefaultFocus>
                          <FocusablePressable
                            focusKey={`unmark-release-${index}`}
                            text="Remove"
                            onSelect={() => unmarkUnplayable(release.sourcePath)}
                            style={styles.tvModalItemButton}
                            focusedStyle={styles.tvModalItemButtonFocused}
                            textStyle={styles.tvModalItemButtonText}
                            focusedTextStyle={styles.tvModalItemButtonTextFocused}
                          />
                        </DefaultFocus>
                      ) : (
                        <FocusablePressable
                          focusKey={`unmark-release-${index}`}
                          text="Remove"
                          onSelect={() => unmarkUnplayable(release.sourcePath)}
                          style={styles.tvModalItemButton}
                          focusedStyle={styles.tvModalItemButtonFocused}
                          textStyle={styles.tvModalItemButtonText}
                          focusedTextStyle={styles.tvModalItemButtonTextFocused}
                        />
                      )}
                    </View>
                  ))}
                </SpatialNavigationScrollView>

                <View style={styles.tvModalFooter}>
                  {unplayableReleases.length > 0 && (
                    <FocusablePressable
                      focusKey="clear-all-releases"
                      text="Clear All"
                      onSelect={clearUnplayableReleases}
                      style={[styles.tvModalCloseButton, styles.tvModalDangerButton]}
                      focusedStyle={[styles.tvModalCloseButtonFocused, styles.tvModalDangerButtonFocused]}
                      textStyle={[styles.tvModalCloseButtonText, styles.tvModalDangerButtonText]}
                      focusedTextStyle={styles.tvModalCloseButtonTextFocused}
                    />
                  )}
                  {unplayableReleases.length === 0 ? (
                    <DefaultFocus>
                      <FocusablePressable
                        focusKey="close-unplayable-releases"
                        text="Close"
                        onSelect={() => setIsUnplayableReleasesModalOpen(false)}
                        style={styles.tvModalCloseButton}
                        focusedStyle={styles.tvModalCloseButtonFocused}
                        textStyle={styles.tvModalCloseButtonText}
                        focusedTextStyle={styles.tvModalCloseButtonTextFocused}
                      />
                    </DefaultFocus>
                  ) : (
                    <FocusablePressable
                      focusKey="close-unplayable-releases"
                      text="Close"
                      onSelect={() => setIsUnplayableReleasesModalOpen(false)}
                      style={styles.tvModalCloseButton}
                      focusedStyle={styles.tvModalCloseButtonFocused}
                      textStyle={styles.tvModalCloseButtonText}
                      focusedTextStyle={styles.tvModalCloseButtonTextFocused}
                    />
                  )}
                </View>
              </SpatialNavigationNode>
            </View>
          </View>
        </SpatialNavigationRoot>
      )}

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
      paddingTop: theme.spacing.md * atvScale,
      paddingBottom: theme.spacing['2xl'] * atvScale,
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
      marginBottom: theme.spacing.xl * atvScale * 0.5,
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
    // TV Grid item height for virtualized list (includes spacing between items)
    tvGridItemHeight: { height: 100 * atvScale },
    tvGridHeaderHeight: { height: 80 * atvScale },
    tvGridDropdownHeight: { height: 120 * atvScale },
    // Row container style for settings grid
    // Calculate width: 60% of available content area
    tvGridRowContainer: isTV
      ? {
          width: (screenWidth - tvEdgeBufferHorizontal * 2 - tvPadding * 2) * 0.6,
          gap: theme.spacing.md * atvScale,
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
      marginBottom: theme.spacing.sm * atvScale,
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
