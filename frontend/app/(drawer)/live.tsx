import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ScrollView as RNScrollView } from 'react-native';
import {
  Animated,
  findNodeHandle,
  Platform,
  Pressable,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  UIManager,
  View,
} from 'react-native';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { Image } from '@/components/Image';

import { CategoryFilterModal } from '@/components/CategoryFilterModal';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import { useBackendSettings } from '@/components/BackendSettingsContext';
import { useLiveCategories, useLiveFavorites, useLiveHiddenChannels } from '@/components/LiveContext';
import { useMultiscreen } from '@/components/MultiscreenContext';
import LoadingIndicator from '@/components/LoadingIndicator';
import { useMenuContext } from '@/components/MenuContext';
import { useToast } from '@/components/ToastContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { isTV as isTVDevice, responsiveSize, tvScale } from '@/theme/tokens/tvScale';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { LinearGradient } from 'expo-linear-gradient';
import { Stack, useRouter } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';

import { useLiveChannels, type LiveChannel } from '@/hooks/useLiveChannels';
import apiService from '@/services/api';

// Spatial navigation header button for TV - matches TVActionButton styling
const SpatialHeaderButton = ({
  text,
  icon,
  onSelect,
  loading,
  disabled,
  isActive,
  theme,
}: {
  text?: string;
  icon: keyof typeof Ionicons.glyphMap;
  onSelect: () => void;
  loading?: boolean;
  disabled?: boolean;
  isActive?: boolean;
  theme: NovaTheme;
}) => {
  // Match TVActionButton sizing exactly
  const scale = tvScale(1.375, 1);
  const baseIconSize = 24;
  const scaledIconSize = tvScale(baseIconSize * 1.375, baseIconSize);
  const paddingH = theme.spacing.md * scale;
  const paddingV = theme.spacing.sm * scale;
  const borderRadius = theme.radius.md * scale;
  const fontSize = theme.typography.label.md.fontSize * scale;
  const lineHeight = theme.typography.label.md.lineHeight * scale;
  const gap = theme.spacing.sm * scale;
  // Consistent border width to prevent button resizing when active state changes
  const borderWidth = 2 * scale;

  return (
    <SpatialNavigationFocusableView onSelect={disabled || loading ? undefined : onSelect}>
      {({ isFocused }: { isFocused: boolean }) => (
        <View
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            gap,
            paddingHorizontal: paddingH,
            paddingVertical: paddingV,
            borderRadius,
            backgroundColor: isFocused
              ? theme.colors.accent.primary
              : isActive
                ? 'transparent'
                : theme.colors.overlay.button,
            borderWidth,
            borderColor: isFocused || isActive ? theme.colors.accent.primary : 'transparent',
            opacity: disabled || loading ? 0.5 : 1,
            alignSelf: 'flex-start',
          }}>
          <Ionicons
            name={icon}
            size={scaledIconSize}
            color={isFocused ? theme.colors.text.inverse : isActive ? theme.colors.accent.primary : theme.colors.text.primary}
          />
          {text && (
            <Text
              numberOfLines={1}
              style={{
                ...theme.typography.label.md,
                color: isFocused ? theme.colors.text.inverse : isActive ? theme.colors.accent.primary : theme.colors.text.primary,
                fontSize,
                lineHeight,
              }}>
              {text}
            </Text>
          )}
        </View>
      )}
    </SpatialNavigationFocusableView>
  );
};

interface ChannelCardProps {
  channel: LiveChannel;
  isFavorite: boolean;
  isFirstInList: boolean;
  onSelect: (channel: LiveChannel) => void;
  onToggleFavorite: (channel: LiveChannel) => void;
  onLongPress: (channel: LiveChannel) => void;
  onFocus: (channel: LiveChannel) => void;
  registerCardRef: (channelId: string, ref: unknown) => void;
}

const ChannelCard: React.FC<ChannelCardProps> = React.memo(
  ({
    channel,
    isFavorite: channelIsFavorite,
    isFirstInList,
    onSelect,
    onToggleFavorite,
    onLongPress,
    onFocus,
    registerCardRef,
  }) => {
    const theme = useTheme();
    const styles = useMemo(() => createStyles(theme), [theme]);
    const isTV = Platform.isTV;
    const _channelKey = `live-channel-${channel.id}`;

    const borderFlashAnim = useRef(new Animated.Value(0)).current;
    const starFlashAnim = useRef(new Animated.Value(1)).current;

    const flashBorder = useCallback(() => {
      borderFlashAnim.setValue(1);
      Animated.timing(borderFlashAnim, {
        toValue: 0,
        duration: 400,
        useNativeDriver: false,
      }).start();
    }, [borderFlashAnim]);

    const flashStar = useCallback(() => {
      Animated.sequence([
        Animated.timing(starFlashAnim, {
          toValue: 1.5,
          duration: 150,
          useNativeDriver: true,
        }),
        Animated.timing(starFlashAnim, {
          toValue: 1,
          duration: 150,
          useNativeDriver: true,
        }),
      ]).start();
    }, [starFlashAnim]);

    const handlePress = useCallback(() => {
      flashBorder();
      onSelect(channel);
    }, [flashBorder, channel, onSelect]);

    const handleFavoritePress = useCallback(() => {
      flashStar();
      onToggleFavorite(channel);
    }, [flashStar, channel, onToggleFavorite]);

    const handleLongPress = useCallback(() => {
      onLongPress(channel);
    }, [channel, onLongPress]);

    const handleCardRef = useCallback(
      (node: unknown) => {
        registerCardRef(channel.id, node);
      },
      [channel.id, registerCardRef],
    );

    const handleCardFocus = useCallback(() => {
      onFocus(channel);
    }, [channel, onFocus]);

    const borderColor = borderFlashAnim.interpolate({
      inputRange: [0, 1],
      outputRange: [theme.colors.border.subtle, theme.colors.accent.primary],
    });

    const renderCardContent = (isFocused: boolean) => (
      <Animated.View
        ref={(node) => handleCardRef(node)}
        style={[
          styles.channelCardWrapper,
          styles.channelCard,
          isFocused && styles.channelCardFocused,
          { borderColor },
        ]}>
        <View style={styles.channelAvatar}>
          {channel.logo ? (
            <Image
              source={{ uri: channel.logo }}
              style={styles.channelLogo}
              contentFit="contain"
              transition={0}
              cachePolicy="disk"
            />
          ) : (
            <View style={styles.channelPlaceholder}>
              <Text style={styles.channelPlaceholderText}>{channel.name?.charAt(0)?.toUpperCase() ?? '?'}</Text>
            </View>
          )}
        </View>
        <View style={styles.channelMeta}>
          <Text style={[styles.channelName, isFocused && styles.channelNameFocused]}>{channel.name}</Text>
          {channel.group ? <Text style={styles.channelGroup}>{channel.group}</Text> : null}
          <Text style={styles.channelUrl} numberOfLines={1}>
            {channel.url}
          </Text>
          {isTV ? <Text style={styles.channelHint}>Long press for options</Text> : null}
        </View>
        <View style={styles.channelActions}>
          {isTV ? (
            channelIsFavorite ? (
              <View style={styles.favoriteBadge}>
                <Text style={styles.favoriteBadgeText}>Favorite</Text>
              </View>
            ) : null
          ) : (
            <Pressable onPress={handleFavoritePress}>
              <View style={[styles.actionButton, channelIsFavorite && styles.favoriteButtonActive]}>
                <Animated.Text style={[styles.actionIcon, { transform: [{ scale: starFlashAnim }] }]}>
                  {channelIsFavorite ? '★' : '☆'}
                </Animated.Text>
              </View>
            </Pressable>
          )}
        </View>
      </Animated.View>
    );

    if (isTV) {
      const cardContent = (
        <SpatialNavigationFocusableView
          onSelect={handlePress}
          onLongSelect={handleLongPress}
          onFocus={handleCardFocus}>
          {({ isFocused }: { isFocused: boolean }) => renderCardContent(isFocused)}
        </SpatialNavigationFocusableView>
      );

      if (isFirstInList) {
        return <DefaultFocus key={channel.id}>{cardContent}</DefaultFocus>;
      }
      return cardContent;
    }

    return (
      <Pressable key={channel.id} onPress={handlePress} onLongPress={handleLongPress} delayLongPress={500}>
        {renderCardContent(false)}
      </Pressable>
    );
  },
);

// Static styles for ultra-minimal TV grid card - avoids object creation per render
const tvGridCardStyles = {
  card: {
    borderRadius: 12,
    backgroundColor: '#1a1a2e',
    borderWidth: 3,
    borderColor: 'transparent',
    overflow: 'hidden' as const,
  },
  cardFocused: {
    borderRadius: 12,
    backgroundColor: '#252542',
    borderWidth: 3,
    borderColor: '#8b5cf6',
    overflow: 'hidden' as const,
  },
  imageContainer: {
    width: '100%' as const,
    aspectRatio: 5 / 3,
    position: 'relative' as const,
  },
  image: {
    width: '100%' as const,
    height: '100%' as const,
  },
  placeholder: {
    width: '100%' as const,
    height: '100%' as const,
    backgroundColor: '#2a2a4a',
    justifyContent: 'center' as const,
    alignItems: 'center' as const,
  },
  placeholderText: {
    fontSize: 24,
    fontWeight: '600' as const,
    color: 'rgba(255,255,255,0.5)',
  },
  badge: {
    position: 'absolute' as const,
    top: 8,
    right: 8,
    backgroundColor: '#8b5cf6',
    borderRadius: 4,
    paddingHorizontal: 6,
    paddingVertical: 2,
  },
  badgeText: {
    color: '#fff',
    fontSize: 12,
    fontWeight: '600' as const,
  },
  gradient: {
    position: 'absolute' as const,
    bottom: 0,
    left: 0,
    right: 0,
    height: '50%' as const,
  },
  textContainer: {
    position: 'absolute' as const,
    bottom: 0,
    left: 0,
    right: 0,
    padding: 8,
  },
  text: {
    color: '#fff',
    fontSize: 14,
    fontWeight: '500' as const,
  },
};

// Ultra-minimal TV Grid Card - callbacks via context to avoid prop changes
interface TVGridHandlers {
  onSelect: (channelId: string) => void;
  onLongPress: (channelId: string) => void;
  onFocus: (channelId: string, rowIndex: number) => void;
}

const TVGridHandlersContext = React.createContext<TVGridHandlers | null>(null);

const TVChannelGridCard = React.memo(
  function TVChannelGridCard({
    channel,
    isFavorite,
    rowIndex,
    cardWidth,
  }: {
    channel: LiveChannel;
    isFavorite: boolean;
    rowIndex: number;
    cardWidth: number;
  }) {
    const handlers = React.useContext(TVGridHandlersContext);

    const handleSelect = useCallback(() => {
      handlers?.onSelect(channel.id);
    }, [handlers, channel.id]);

    const handleLongSelect = useCallback(() => {
      handlers?.onLongPress(channel.id);
    }, [handlers, channel.id]);

    const handleFocus = useCallback(() => {
      handlers?.onFocus(channel.id, rowIndex);
    }, [handlers, channel.id, rowIndex]);

    return (
      <SpatialNavigationFocusableView onSelect={handleSelect} onLongSelect={handleLongSelect} onFocus={handleFocus}>
        {({ isFocused }: { isFocused: boolean }) => (
          <View style={[isFocused ? tvGridCardStyles.cardFocused : tvGridCardStyles.card, { width: cardWidth }]}>
            <View style={tvGridCardStyles.imageContainer}>
              {channel.logo ? (
                <Image
                  source={{ uri: channel.logo }}
                  style={tvGridCardStyles.image}
                  contentFit="contain"
                  transition={0}
                  cachePolicy="disk"
                  recyclingKey={`ch-${channel.id}`}
                />
              ) : (
                <View style={tvGridCardStyles.placeholder}>
                  <Text style={tvGridCardStyles.placeholderText}>{channel.name?.charAt(0)?.toUpperCase() ?? '?'}</Text>
                </View>
              )}
              {isFavorite && (
                <View style={tvGridCardStyles.badge}>
                  <Text style={tvGridCardStyles.badgeText}>★</Text>
                </View>
              )}
              <LinearGradient
                pointerEvents="none"
                colors={['transparent', 'rgba(0,0,0,0.85)']}
                style={tvGridCardStyles.gradient}
              />
              <View style={tvGridCardStyles.textContainer}>
                <Text style={tvGridCardStyles.text} numberOfLines={2}>
                  {channel.name}
                </Text>
              </View>
            </View>
          </View>
        )}
      </SpatialNavigationFocusableView>
    );
  },
  (prevProps, nextProps) =>
    prevProps.channel.id === nextProps.channel.id &&
    prevProps.channel.logo === nextProps.channel.logo &&
    prevProps.channel.name === nextProps.channel.name &&
    prevProps.isFavorite === nextProps.isFavorite &&
    prevProps.rowIndex === nextProps.rowIndex &&
    prevProps.cardWidth === nextProps.cardWidth,
);

function LiveScreen() {
  const theme = useTheme();
  const { width: screenWidth, height: screenHeight } = useTVDimensions();
  const styles = useMemo(() => createStyles(theme, screenWidth, screenHeight), [theme, screenWidth, screenHeight]);
  const router = useRouter();
  const { isOpen: isMenuOpen, openMenu } = useMenuContext();
  const isFocused = useIsFocused();
  const { selectedCategories, toggleCategory, setSelectedCategories } = useLiveCategories();
  const { isFavorite, toggleFavorite, favorites } = useLiveFavorites();
  const { channels, loading, error, refresh, hasPlaylistUrl, availableCategories } = useLiveChannels(
    selectedCategories,
    favorites,
  );
  const { isHidden, hideChannel } = useLiveHiddenChannels();
  const {
    hasSavedSession,
    isSelectionMode,
    selectedChannels,
    enterSelectionMode,
    exitSelectionMode,
    toggleChannelSelection,
    getChannelSelectionOrder,
    launchMultiscreen,
    resumeSession,
  } = useMultiscreen();
  const { showToast } = useToast();
  const { refreshSettings } = useBackendSettings();
  const { pendingPinUserId } = useUserProfiles();
  const [isCategoryModalVisible, setIsCategoryModalVisible] = useState(false);
  const [actionChannel, setActionChannel] = useState<LiveChannel | null>(null);
  const [isActionModalVisible, setIsActionModalVisible] = useState(false);
  const [filterText, setFilterText] = useState('');
  const [isFilterActive, setIsFilterActive] = useState(false);
  const [focusedChannel, setFocusedChannel] = useState<LiveChannel | null>(null);
  const [isSelectionConfirmVisible, setIsSelectionConfirmVisible] = useState(false);

  // Debounce ref for focus updates - prevents re-renders during rapid grid navigation
  const focusDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Cleanup debounce timeout on unmount
  useEffect(() => {
    return () => {
      if (focusDebounceRef.current) {
        clearTimeout(focusDebounceRef.current);
      }
    };
  }, []);

  // Mobile infinite scroll state
  const INITIAL_VISIBLE_COUNT = 50;
  const LOAD_MORE_INCREMENT = 30;
  const [visibleChannelCount, setVisibleChannelCount] = useState(INITIAL_VISIBLE_COUNT);

  const isActive =
    isFocused &&
    !isMenuOpen &&
    !isCategoryModalVisible &&
    !isActionModalVisible &&
    !isFilterActive &&
    !pendingPinUserId &&
    !isSelectionConfirmVisible;

  // Guard against duplicate "select" events on tvOS
  const selectGuardRef = useRef(false);
  const filterClosingRef = useRef(false);
  const filterInterceptorRef = useRef<(() => void) | null>(null);
  const withSelectGuard = useCallback((fn: () => void) => {
    if (Platform.isTV) {
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

  useEffect(() => {
    console.log('[live] isActive state:', {
      isActive,
      isFocused,
      isMenuOpen,
      isCategoryModalVisible,
      isActionModalVisible,
      isFilterActive,
    });
  }, [isActive, isFocused, isMenuOpen, isCategoryModalVisible, isActionModalVisible, isFilterActive]);
  const filterInputRef = useRef<TextInput>(null);
  const tempFilterRef = useRef('');
  const scrollViewRef = useRef<RNScrollView | null>(null);
  const channelRefs = useRef<Record<string, unknown>>({});
  const scrollDebounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const scrollMetricsRef = useRef({ offset: 0, viewportHeight: 0 });

  const registerChannelRef = useCallback((channelId: string, ref: unknown) => {
    if (!Platform.isTV) {
      return;
    }

    if (ref) {
      channelRefs.current[channelId] = ref;
    } else {
      delete channelRefs.current[channelId];
    }
  }, []);

  const scrollToChannel = useCallback((channelId: string) => {
    if (!Platform.isTV) {
      return;
    }

    if (scrollDebounceRef.current) {
      clearTimeout(scrollDebounceRef.current);
    }

    scrollDebounceRef.current = setTimeout(() => {
      const scrollViewInstance = scrollViewRef.current;
      const cardInstance = channelRefs.current[channelId];

      if (!scrollViewInstance || !cardInstance) {
        return;
      }

      const cardNode = findNodeHandle(cardInstance as any);
      const scrollViewNode = findNodeHandle(scrollViewInstance);

      if (!cardNode || !scrollViewNode) {
        return;
      }

      const scrollResponder = (scrollViewInstance as any).getScrollResponder?.();
      if (scrollResponder?.scrollResponderScrollNativeHandleToKeyboard) {
        scrollResponder.scrollResponderScrollNativeHandleToKeyboard(cardNode, 120, true);
        return;
      }

      UIManager.measureLayout(
        cardNode,
        scrollViewNode,
        () => {
          console.debug('Unable to measure channel card layout');
        },
        (_x, y, _width, height) => {
          const { offset, viewportHeight } = scrollMetricsRef.current;
          let targetOffset = offset;

          const topThreshold = 80;
          const bottomThreshold = 40;
          const topRelative = y;
          const bottomRelative = y + height;

          if (topRelative < topThreshold) {
            targetOffset = Math.max(0, offset + topRelative - topThreshold);
          } else if (viewportHeight > 0 && bottomRelative > viewportHeight - bottomThreshold) {
            targetOffset = offset + (bottomRelative - viewportHeight) + bottomThreshold;
          }

          if (targetOffset !== offset) {
            (scrollViewInstance as any)?.scrollTo?.({ y: targetOffset, animated: true });
          }
        },
      );
    }, 16);
  }, []);

  useEffect(() => {
    return () => {
      if (scrollDebounceRef.current) {
        clearTimeout(scrollDebounceRef.current);
      }
    };
  }, []);

  const handleChannelFocus = useCallback(
    (channel: LiveChannel) => {
      scrollToChannel(channel.id);
    },
    [scrollToChannel],
  );

  // Split channels into favorites and non-favorites, filtering out hidden channels and applying filter text
  // Note: Favorites are always included even if they don't match the filter
  const { favoriteChannels, regularChannels } = useMemo(() => {
    const favorites: LiveChannel[] = [];
    const regular: LiveChannel[] = [];
    const filterLower = filterText.toLowerCase().trim();

    channels.forEach((channel) => {
      // Skip hidden channels
      if (isHidden(channel.id)) {
        return;
      }

      const matchesFilter = !filterLower || channel.name?.toLowerCase().includes(filterLower);
      const isChannelFavorite = isFavorite(channel.id);

      // Always include favorites, even if they don't match the filter
      // For non-favorites, only include if they match the filter
      if (isChannelFavorite) {
        favorites.push(channel);
      } else if (matchesFilter) {
        regular.push(channel);
      }
    });

    return { favoriteChannels: favorites, regularChannels: regular };
  }, [channels, isFavorite, isHidden, filterText]);

  // Reset visible count when filters change (mobile infinite scroll)
  useEffect(() => {
    if (!Platform.isTV) {
      setVisibleChannelCount(INITIAL_VISIBLE_COUNT);
    }
  }, [filterText, selectedCategories]);

  // Sliced channels for mobile infinite scroll
  const displayedRegularChannels = useMemo(() => {
    if (Platform.isTV) {
      return regularChannels;
    }
    return regularChannels.slice(0, visibleChannelCount);
  }, [regularChannels, visibleChannelCount]);

  const hasMoreChannels = !Platform.isTV && visibleChannelCount < regularChannels.length;

  // Handle scroll for mobile infinite scroll
  const handleInfiniteScroll = useCallback(
    (event: {
      nativeEvent: {
        contentOffset: { y: number };
        contentSize: { height: number };
        layoutMeasurement: { height: number };
      };
    }) => {
      scrollMetricsRef.current.offset = event.nativeEvent.contentOffset.y;

      if (Platform.isTV || !hasMoreChannels) {
        return;
      }

      const { contentOffset, contentSize, layoutMeasurement } = event.nativeEvent;
      const distanceFromBottom = contentSize.height - (contentOffset.y + layoutMeasurement.height);

      // Load more when within 200px of bottom
      if (distanceFromBottom < 200) {
        setVisibleChannelCount((prev) => Math.min(prev + LOAD_MORE_INCREMENT, regularChannels.length));
      }
    },
    [hasMoreChannels, regularChannels.length],
  );

  // Handle left navigation at edge to open menu (spatial navigation)
  const onDirectionHandledWithoutMovement = useCallback(
    (direction: Direction) => {
      if (direction === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

  const handleChannelSelect = useCallback(
    async (channel: LiveChannel) => {
      // In selection mode, toggle channel selection instead of playing
      if (isSelectionMode) {
        const selectionOrder = getChannelSelectionOrder(channel.id);
        toggleChannelSelection({
          id: channel.id,
          name: channel.name ?? 'Unknown Channel',
          url: channel.url,
          streamUrl: channel.streamUrl ?? channel.url,
          logo: channel.logo,
        });
        if (selectionOrder) {
          showToast(`Removed: ${channel.name}`, { tone: 'info' });
        } else if (selectedChannels.length < 5) {
          showToast(`Selected: ${channel.name} (#${selectedChannels.length + 1})`, { tone: 'success' });
        } else {
          showToast('Maximum 5 channels selected', { tone: 'info' });
        }
        return;
      }

      showToast(`Playing: ${channel.name}`, { tone: 'success' });

      // Start HLS session for live TV (iOS native player requires HLS)
      try {
        const hlsSession = await apiService.startLiveHlsSession(channel.url);
        const authToken = apiService.getAuthToken();
        const hlsPlaylistUrl = `${apiService.getBaseUrl()}${hlsSession.playlistUrl}${authToken ? `?token=${encodeURIComponent(authToken)}` : ''}`;

        router.push({
          pathname: '/player',
          params: {
            movie: hlsPlaylistUrl,
            headerImage: channel.logo ?? '',
            title: channel.name ?? 'Live Channel',
            mediaType: 'channel',
            preferSystemPlayer: '1',
          },
        });
      } catch (error) {
        console.error('[live] Failed to start HLS session:', error);
        showToast('Failed to start live stream', { tone: 'danger' });
      }
    },
    [router, showToast, isSelectionMode, getChannelSelectionOrder, toggleChannelSelection, selectedChannels.length],
  );

  // Convert channels to use HLS streaming
  const startHlsSessionsForChannels = useCallback(
    async (channels: typeof selectedChannels): Promise<typeof selectedChannels | null> => {
      try {
        const authToken = apiService.getAuthToken();
        const hlsChannels = await Promise.all(
          channels.map(async (channel) => {
            const hlsSession = await apiService.startLiveHlsSession(channel.url);
            const hlsPlaylistUrl = `${apiService.getBaseUrl()}${hlsSession.playlistUrl}${authToken ? `?token=${encodeURIComponent(authToken)}` : ''}`;
            return { ...channel, streamUrl: hlsPlaylistUrl };
          }),
        );
        return hlsChannels;
      } catch (error) {
        console.error('[live] Failed to start HLS sessions for multiscreen:', error);
        showToast('Failed to start live streams', { tone: 'danger' });
        return null;
      }
    },
    [showToast],
  );

  // Handle multiscreen button press
  const handleMultiscreenPress = useCallback(() => {
    withSelectGuard(async () => {
      if (isSelectionMode) {
        // Already in selection mode
        if (selectedChannels.length >= 2) {
          // Launch if we have enough channels
          const channels = launchMultiscreen();
          if (channels) {
            showToast(`Starting ${channels.length} channels...`, { tone: 'success' });
            const hlsChannels = await startHlsSessionsForChannels(channels);
            if (hlsChannels) {
              router.push({
                pathname: '/multiscreen',
                params: { channels: JSON.stringify(hlsChannels) },
              });
            }
          }
        } else if (selectedChannels.length === 0) {
          // Exit selection mode if no channels selected
          exitSelectionMode();
          showToast('Selection cancelled', { tone: 'info' });
        } else {
          showToast('Select at least 2 channels', { tone: 'info' });
        }
      } else {
        // Enter selection mode
        enterSelectionMode();
        showToast('Select up to 5 channels for multiscreen', { tone: 'info' });
      }
    });
  }, [
    withSelectGuard,
    isSelectionMode,
    selectedChannels.length,
    launchMultiscreen,
    showToast,
    router,
    enterSelectionMode,
    exitSelectionMode,
    startHlsSessionsForChannels,
  ]);

  // Handle resume button press
  const handleResumePress = useCallback(() => {
    withSelectGuard(async () => {
      const channels = resumeSession();
      if (channels) {
        showToast(`Starting ${channels.length} channels...`, { tone: 'success' });
        const hlsChannels = await startHlsSessionsForChannels(channels);
        if (hlsChannels) {
          router.push({
            pathname: '/multiscreen',
            params: { channels: JSON.stringify(hlsChannels) },
          });
        }
      }
    });
  }, [withSelectGuard, resumeSession, showToast, router, startHlsSessionsForChannels]);

  const handleFocus = useCallback(() => {
    // Native focus handles navigation locking automatically
  }, []);

  const handleBlur = useCallback(() => {
    // Native focus handles navigation unlocking automatically
    // Sync filter text from ref on tvOS (like search page does)
    if (Platform.isTV) {
      const finalFilter = tempFilterRef.current;
      setFilterText(finalFilter);
    }
  }, []);

  const handleFilterChangeText = useCallback((text: string) => {
    if (Platform.isTV) {
      // On tvOS, store in ref to avoid controlled input issues
      tempFilterRef.current = text;
    } else {
      // On mobile, use normal controlled input
      setFilterText(text);
    }
  }, []);

  const handleToggleFilter = useCallback(() => {
    // Prevent toggling if we're currently closing
    if (filterClosingRef.current) return;

    withSelectGuard(() => {
      setIsFilterActive((prev) => {
        if (prev) {
          // Closing - set flag and clear it after delay
          filterClosingRef.current = true;
          filterInputRef.current?.blur();
          // Don't clear filter text on close - keep it active
          setTimeout(() => {
            filterClosingRef.current = false;
          }, 500);
          return false;
        }
        // Opening - sync tempFilterRef with current filterText
        if (Platform.isTV) {
          tempFilterRef.current = filterText;
        }
        return true;
      });
    });
  }, [withSelectGuard, filterText]);

  const handleCloseFilter = useCallback(() => {
    // Prevent multiple close attempts
    if (filterClosingRef.current) return;

    withSelectGuard(() => {
      filterClosingRef.current = true;
      // Blur the text input first to prevent keyboard from briefly showing
      filterInputRef.current?.blur();

      // Explicitly remove back interceptor before closing
      if (filterInterceptorRef.current) {
        console.log('[live] Removing filter back interceptor (Close button pressed)');
        filterInterceptorRef.current();
        filterInterceptorRef.current = null;
      }

      // Defer the state update to ensure blur completes
      setTimeout(() => {
        setIsFilterActive(false);
        // Don't clear filter text on close - keep it active
        setTimeout(() => {
          filterClosingRef.current = false;
        }, 500);
      }, 50);
    });
  }, [withSelectGuard]);

  // Register back interceptor for filter modal
  const handleCloseFilterRef = useRef(handleCloseFilter);

  useEffect(() => {
    handleCloseFilterRef.current = handleCloseFilter;
  }, [handleCloseFilter]);

  useEffect(() => {
    if (!isFilterActive) {
      // Clean up interceptor when filter is hidden
      if (filterInterceptorRef.current) {
        console.log('[live] Removing filter back interceptor (filter hidden)');
        filterInterceptorRef.current();
        filterInterceptorRef.current = null;
      }
      return;
    }

    // Install interceptor when filter is shown
    console.log('[live] ========== INSTALLING FILTER BACK INTERCEPTOR ==========');
    let isHandling = false;
    let cleanupScheduled = false;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      console.log('[live] ========== FILTER INTERCEPTOR CALLED ==========');

      // Prevent duplicate handling
      if (isHandling) {
        console.log('[live] Already handling back press, ignoring duplicate');
        return true;
      }

      isHandling = true;
      console.log('[live] Filter back interceptor called, closing filter');

      // Call handleCloseFilter using ref to avoid stale closure
      handleCloseFilterRef.current();

      // Delay cleanup to swallow duplicate events
      if (!cleanupScheduled) {
        cleanupScheduled = true;
        setTimeout(() => {
          if (filterInterceptorRef.current) {
            console.log('[live] Removing filter back interceptor (delayed cleanup)');
            filterInterceptorRef.current();
            filterInterceptorRef.current = null;
          }
          isHandling = false;
        }, 750);
      }

      console.log('[live] ========== FILTER INTERCEPTOR RETURNING TRUE (HANDLED) ==========');
      return true; // Handled - prevents further interceptors from running
    });

    filterInterceptorRef.current = removeInterceptor;
    console.log('[live] ========== FILTER INTERCEPTOR INSTALLED ==========');

    return () => {
      console.log('[live] Unmount cleanup - filter interceptor will be removed by delayed cleanup if scheduled');
    };
  }, [isFilterActive]);

  // Register back interceptor for action modal
  const handleCloseActionModalRef = useRef<(() => void) | null>(null);
  const actionModalInterceptorRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (!isActionModalVisible) {
      // Clean up interceptor when action modal is hidden
      if (actionModalInterceptorRef.current) {
        console.log('[live] Removing action modal back interceptor (modal hidden)');
        actionModalInterceptorRef.current();
        actionModalInterceptorRef.current = null;
      }
      return;
    }

    // Install interceptor when action modal is shown
    console.log('[live] ========== INSTALLING ACTION MODAL BACK INTERCEPTOR ==========');
    let isHandling = false;
    let cleanupScheduled = false;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      console.log('[live] ========== ACTION MODAL INTERCEPTOR CALLED ==========');

      // Prevent duplicate handling
      if (isHandling) {
        console.log('[live] Already handling back press, ignoring duplicate');
        return true;
      }

      isHandling = true;
      console.log('[live] Action modal back interceptor called, closing modal');

      // Call handleCloseActionModal using ref to avoid stale closure
      handleCloseActionModalRef.current?.();

      // Delay cleanup to swallow duplicate events
      if (!cleanupScheduled) {
        cleanupScheduled = true;
        setTimeout(() => {
          if (actionModalInterceptorRef.current) {
            console.log('[live] Removing action modal back interceptor (delayed cleanup)');
            actionModalInterceptorRef.current();
            actionModalInterceptorRef.current = null;
          }
          isHandling = false;
        }, 750);
      }

      console.log('[live] ========== ACTION MODAL INTERCEPTOR RETURNING TRUE (HANDLED) ==========');
      return true; // Handled - prevents further interceptors from running
    });

    actionModalInterceptorRef.current = removeInterceptor;
    console.log('[live] ========== ACTION MODAL INTERCEPTOR INSTALLED ==========');

    return () => {
      console.log('[live] Unmount cleanup - action modal interceptor will be removed by delayed cleanup if scheduled');
    };
  }, [isActionModalVisible]);

  // Refs for selection confirm modal back interceptor (initialized later after handleSelectionConfirmClose is defined)
  const selectionConfirmCloseRef = useRef<(() => void) | null>(null);
  const selectionConfirmInterceptorRef = useRef<(() => void) | null>(null);

  // Register back interceptor for selection mode
  const selectionModeInterceptorRef = useRef<(() => void) | null>(null);

  useEffect(() => {
    if (!isSelectionMode) {
      // Clean up interceptor when selection mode is exited
      if (selectionModeInterceptorRef.current) {
        console.log('[live] Removing selection mode back interceptor');
        selectionModeInterceptorRef.current();
        selectionModeInterceptorRef.current = null;
      }
      return;
    }

    // Install interceptor when selection mode is active
    console.log('[live] Installing selection mode back interceptor');
    let isHandling = false;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      if (isHandling) {
        return true;
      }

      isHandling = true;
      console.log('[live] Selection mode back interceptor called, showing confirmation modal');
      // Show confirmation modal instead of immediately canceling
      setIsSelectionConfirmVisible(true);

      setTimeout(() => {
        isHandling = false;
      }, 500);

      return true;
    });

    selectionModeInterceptorRef.current = removeInterceptor;

    return () => {
      // Cleanup on unmount
    };
  }, [isSelectionMode]);

  // Handlers for selection confirmation modal
  const handleSelectionConfirmCancel = useCallback(() => {
    setIsSelectionConfirmVisible(false);
    // Explicitly remove both interceptors to avoid timing issues
    if (selectionConfirmInterceptorRef.current) {
      selectionConfirmInterceptorRef.current();
      selectionConfirmInterceptorRef.current = null;
    }
    if (selectionModeInterceptorRef.current) {
      selectionModeInterceptorRef.current();
      selectionModeInterceptorRef.current = null;
    }
    exitSelectionMode();
    showToast('Selection cancelled', { tone: 'info' });
  }, [exitSelectionMode, showToast]);

  const handleSelectionConfirmLaunch = useCallback(() => {
    setIsSelectionConfirmVisible(false);
    // Explicitly remove both interceptors to avoid timing issues
    if (selectionConfirmInterceptorRef.current) {
      selectionConfirmInterceptorRef.current();
      selectionConfirmInterceptorRef.current = null;
    }
    if (selectionModeInterceptorRef.current) {
      selectionModeInterceptorRef.current();
      selectionModeInterceptorRef.current = null;
    }
    const channels = launchMultiscreen();
    if (channels) {
      showToast(`Launching ${channels.length} channels`, { tone: 'success' });
      router.push({
        pathname: '/multiscreen',
        params: { channels: JSON.stringify(channels) },
      });
    }
  }, [launchMultiscreen, showToast, router]);

  const handleSelectionConfirmClose = useCallback(() => {
    // Just close modal, keep selection mode active
    setIsSelectionConfirmVisible(false);
    // Explicitly remove modal interceptor
    if (selectionConfirmInterceptorRef.current) {
      selectionConfirmInterceptorRef.current();
      selectionConfirmInterceptorRef.current = null;
    }
  }, []);

  // Register back interceptor for selection confirm modal (same pattern as CategoryFilterModal)
  useEffect(() => {
    selectionConfirmCloseRef.current = handleSelectionConfirmClose;
  }, [handleSelectionConfirmClose]);

  useEffect(() => {
    if (!isSelectionConfirmVisible) {
      if (selectionConfirmInterceptorRef.current) {
        selectionConfirmInterceptorRef.current();
        selectionConfirmInterceptorRef.current = null;
      }
      return;
    }

    let isHandling = false;
    let cleanupScheduled = false;

    const removeInterceptor = RemoteControlManager.pushBackInterceptor(() => {
      if (isHandling) return true;
      isHandling = true;
      selectionConfirmCloseRef.current?.();

      if (!cleanupScheduled) {
        cleanupScheduled = true;
        setTimeout(() => {
          if (selectionConfirmInterceptorRef.current) {
            selectionConfirmInterceptorRef.current();
            selectionConfirmInterceptorRef.current = null;
          }
          isHandling = false;
        }, 750);
      }
      return true;
    });

    selectionConfirmInterceptorRef.current = removeInterceptor;

    return () => {
      // Cleanup handled by delayed cleanup
    };
  }, [isSelectionConfirmVisible]);

  const _handleOpenSettings = useCallback(() => {
    router.push('/settings');
  }, [router]);

  const [isRefreshing, setIsRefreshing] = useState(false);
  const handleRefreshSettings = useCallback(async () => {
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

  const [isRefreshingPlaylist, setIsRefreshingPlaylist] = useState(false);
  const handleRefreshPlaylist = useCallback(async () => {
    setIsRefreshingPlaylist(true);
    try {
      await apiService.clearLivePlaylistCache();
      await refresh();
      showToast('Playlist refreshed', { tone: 'success' });
    } catch {
      showToast('Failed to refresh playlist', { tone: 'danger' });
    } finally {
      setIsRefreshingPlaylist(false);
    }
  }, [refresh, showToast]);

  const handleHideChannel = useCallback(
    async (channel: LiveChannel) => {
      await hideChannel(channel.id);
      showToast(`Hidden: ${channel.name}`, { tone: 'info' });
    },
    [hideChannel, showToast],
  );

  const handleOpenCategoryModal = useCallback(() => {
    withSelectGuard(() => {
      if (availableCategories.length > 0) {
        setIsCategoryModalVisible(true);
      }
    });
  }, [availableCategories.length, withSelectGuard]);

  const handleCloseCategoryModal = useCallback(() => {
    withSelectGuard(() => {
      setIsCategoryModalVisible(false);
    });
  }, [withSelectGuard]);

  const handleSelectAllCategories = useCallback(() => {
    void setSelectedCategories(availableCategories);
  }, [availableCategories, setSelectedCategories]);

  const handleClearAllCategories = useCallback(() => {
    void setSelectedCategories([]);
  }, [setSelectedCategories]);

  const handleToggleFavorite = useCallback(
    (channel: LiveChannel) => {
      const wasAlreadyFavorite = isFavorite(channel.id);
      toggleFavorite(channel.id);
      if (wasAlreadyFavorite) {
        showToast(`Removed from favorites: ${channel.name}`, { tone: 'info' });
      } else {
        showToast(`Added to favorites: ${channel.name}`, { tone: 'success' });
      }
    },
    [isFavorite, toggleFavorite, showToast],
  );

  const handleChannelLongPress = useCallback(
    (channel: LiveChannel) => {
      withSelectGuard(() => {
        console.log('[live] handleChannelLongPress called for channel:', channel.name);
        setActionChannel(channel);
        setIsActionModalVisible(true);
        console.log('[live] Modal state set to visible, actionChannel:', channel.name);
      });
    },
    [withSelectGuard],
  );

  // Combined data for the grid
  const combinedChannels = useMemo(
    () => [...favoriteChannels, ...regularChannels],
    [favoriteChannels, regularChannels],
  );

  // TV: Progressive rendering for performance
  const TV_COLUMNS = 6;
  const INITIAL_ROW_COUNT = 10; // 10 rows initially
  const LOAD_MORE_ROWS = 5; // 5 more rows per batch
  const [renderedRowCount, setRenderedRowCount] = useState(INITIAL_ROW_COUNT);

  // TV: Channel lookup map for O(1) access
  const channelMap = useMemo(() => {
    const map = new Map<string, LiveChannel>();
    combinedChannels.forEach((ch) => map.set(ch.id, ch));
    return map;
  }, [combinedChannels]);

  // TV: Stable handlers via ref (context value stays same reference)
  const scrollToRowRef = useRef<(rowIndex: number) => void>(() => {});

  const tvGridHandlers = useMemo<TVGridHandlers>(
    () => ({
      onSelect: (channelId: string) => {
        const channel = channelMap.get(channelId);
        if (channel) handleChannelSelect(channel);
      },
      onLongPress: (channelId: string) => {
        const channel = channelMap.get(channelId);
        if (channel) handleChannelLongPress(channel);
      },
      onFocus: (channelId: string, rowIndex: number) => {
        // Scroll immediately for responsiveness
        scrollToRowRef.current(rowIndex);

        // Debounce channel info update to prevent re-renders during rapid navigation
        if (focusDebounceRef.current) {
          clearTimeout(focusDebounceRef.current);
        }
        focusDebounceRef.current = setTimeout(() => {
          const channel = channelMap.get(channelId);
          if (channel) setFocusedChannel(channel);
        }, 100);
      },
    }),
    [channelMap, handleChannelSelect, handleChannelLongPress],
  );

  // TV: Calculate card width for grid (matches createStyles calculation)
  const tvCardWidth = useMemo(() => {
    const effectiveWidth = screenWidth > 0 ? screenWidth : 1920;
    const gap = theme.spacing.lg;
    const horizontalPadding = theme.spacing.xl * 1.5;
    const availableWidth = effectiveWidth - horizontalPadding * 2;
    const totalGapWidth = gap * (TV_COLUMNS - 1);
    return Math.floor((availableWidth - totalGapWidth) / TV_COLUMNS);
  }, [screenWidth, theme.spacing.lg, theme.spacing.xl]);

  // Chunk channels into rows
  const allRows = useMemo(() => {
    const rows: LiveChannel[][] = [];
    for (let i = 0; i < combinedChannels.length; i += TV_COLUMNS) {
      rows.push(combinedChannels.slice(i, i + TV_COLUMNS));
    }
    return rows;
  }, [combinedChannels]);

  // Reset rendered count when channels change
  useEffect(() => {
    setRenderedRowCount(INITIAL_ROW_COUNT);
  }, [combinedChannels.length]);

  // Rows to actually render (progressive loading)
  const visibleRows = useMemo(() => {
    return allRows.slice(0, renderedRowCount);
  }, [allRows, renderedRowCount]);

  // Refs for programmatic scrolling
  const tvGridScrollRef = useRef<ScrollView>(null);
  const tvRowRefs = useRef<{ [key: string]: View | null }>({});

  // Scroll to row when item receives focus
  const scrollToRow = useCallback(
    (rowIndex: number) => {
      if (!Platform.isTV || !tvGridScrollRef.current) return;

      const rowRef = tvRowRefs.current[`row-${rowIndex}`];
      if (!rowRef) return;

      rowRef.measureLayout(
        tvGridScrollRef.current as any,
        (_left, top) => {
          const topOffset = 20;
          const targetY = Math.max(0, top - topOffset);
          tvGridScrollRef.current?.scrollTo({ y: targetY, animated: true });
        },
        () => {
          // Silently ignore measurement failures
        },
      );

      // Load more rows if focusing near the end
      if (rowIndex >= renderedRowCount - 3 && renderedRowCount < allRows.length) {
        setRenderedRowCount((prev) => Math.min(prev + LOAD_MORE_ROWS, allRows.length));
      }
    },
    [renderedRowCount, allRows.length],
  );

  // Keep scrollToRowRef in sync
  scrollToRowRef.current = scrollToRow;

  // Load more rows when scrolling near bottom (fallback for non-focus scrolling)
  const handleGridScroll = useCallback(
    (event: {
      nativeEvent: {
        layoutMeasurement: { height: number };
        contentOffset: { y: number };
        contentSize: { height: number };
      };
    }) => {
      const { layoutMeasurement, contentOffset, contentSize } = event.nativeEvent;
      const paddingToBottom = 200;
      const isNearBottom = layoutMeasurement.height + contentOffset.y >= contentSize.height - paddingToBottom;

      if (isNearBottom && renderedRowCount < allRows.length) {
        setRenderedRowCount((prev) => Math.min(prev + LOAD_MORE_ROWS, allRows.length));
      }
    },
    [renderedRowCount, allRows.length],
  );

  // TV: Listen for LongEnter to open action modal for focused channel
  useEffect(() => {
    if (
      !Platform.isTV ||
      !isFocused ||
      isMenuOpen ||
      isActionModalVisible ||
      isCategoryModalVisible ||
      isFilterActive
    ) {
      return;
    }

    const handleLongEnter = (key: SupportedKeys) => {
      if (key !== SupportedKeys.LongEnter) {
        return;
      }

      if (!focusedChannel) {
        console.log('[live] LongEnter ignored - no focused channel');
        return;
      }

      console.log('[live] LongEnter detected on channel:', focusedChannel.name);
      handleChannelLongPress(focusedChannel);
    };

    RemoteControlManager.addKeydownListener(handleLongEnter);

    return () => {
      RemoteControlManager.removeKeydownListener(handleLongEnter);
    };
  }, [
    isFocused,
    isMenuOpen,
    isActionModalVisible,
    isCategoryModalVisible,
    isFilterActive,
    focusedChannel,
    handleChannelLongPress,
  ]);

  useEffect(() => {
    console.log('[live] isActionModalVisible changed to:', isActionModalVisible);
    console.log('[live] actionChannel:', actionChannel?.name ?? 'null');
  }, [isActionModalVisible, actionChannel]);

  const handleCloseActionModal = useCallback(() => {
    withSelectGuard(() => {
      setIsActionModalVisible(false);
      setActionChannel(null);
    });
  }, [withSelectGuard]);

  // Keep ref updated for back interceptor
  useEffect(() => {
    handleCloseActionModalRef.current = handleCloseActionModal;
  }, [handleCloseActionModal]);

  const handleActionPlay = useCallback(() => {
    withSelectGuard(() => {
      if (!actionChannel) {
        return;
      }
      handleChannelSelect(actionChannel);
      setIsActionModalVisible(false);
      setActionChannel(null);
    });
  }, [actionChannel, handleChannelSelect, withSelectGuard]);

  const handleActionToggleFavorite = useCallback(() => {
    withSelectGuard(() => {
      if (!actionChannel) {
        return;
      }
      handleToggleFavorite(actionChannel);
      setIsActionModalVisible(false);
      setActionChannel(null);
    });
  }, [actionChannel, handleToggleFavorite, withSelectGuard]);

  const handleActionHide = useCallback(() => {
    withSelectGuard(() => {
      if (!actionChannel) {
        return;
      }
      void (async () => {
        await handleHideChannel(actionChannel);
        setIsActionModalVisible(false);
        setActionChannel(null);
      })();
    });
  }, [actionChannel, handleHideChannel, withSelectGuard]);

  const actionChannelIsFavorite = actionChannel ? isFavorite(actionChannel.id) : false;

  // TV header buttons using spatial navigation
  const renderTVHeaderButtons = () => (
    <SpatialNavigationNode orientation="horizontal">
      <View style={styles.actionsRow}>
        <DefaultFocus>
          <SpatialHeaderButton
            text="Refresh"
            icon="refresh-outline"
            onSelect={handleRefreshPlaylist}
            loading={isRefreshingPlaylist}
            theme={theme}
          />
        </DefaultFocus>
        <SpatialHeaderButton
          text="Categories"
          icon="albums-outline"
          onSelect={handleOpenCategoryModal}
          disabled={availableCategories.length === 0}
          theme={theme}
        />
        <SpatialHeaderButton
          text={isFilterActive ? 'Close Filter' : 'Filter'}
          icon={isFilterActive ? 'close-outline' : 'filter-outline'}
          onSelect={handleToggleFilter}
          theme={theme}
        />
        {hasSavedSession && !isSelectionMode && (
          <SpatialHeaderButton
            text="Resume"
            icon="play-circle-outline"
            onSelect={handleResumePress}
            theme={theme}
          />
        )}
        <SpatialHeaderButton
          text={isSelectionMode ? `Start (${selectedChannels.length})` : 'Multiscreen'}
          icon={isSelectionMode ? 'checkmark-circle-outline' : 'grid-outline'}
          onSelect={handleMultiscreenPress}
          isActive={isSelectionMode}
          theme={theme}
        />
      </View>
    </SpatialNavigationNode>
  );

  // Mobile header buttons using native focus
  const renderMobileHeaderButtons = () => (
    <View style={styles.actionsRow}>
      <FocusablePressable
        icon="refresh-outline"
        onSelect={handleRefreshPlaylist}
        loading={isRefreshingPlaylist}
        style={styles.headerActionButton}
      />
      <FocusablePressable
        icon="albums-outline"
        onSelect={handleOpenCategoryModal}
        disabled={availableCategories.length === 0}
        style={styles.headerActionButton}
      />
      <FocusablePressable
        icon={isFilterActive ? 'close-outline' : 'filter-outline'}
        onSelect={handleToggleFilter}
        style={styles.headerActionButton}
      />
      {hasSavedSession && !isSelectionMode && (
        <FocusablePressable
          icon="play-circle-outline"
          onSelect={handleResumePress}
          style={styles.headerActionButton}
        />
      )}
      <FocusablePressable
        icon={isSelectionMode ? 'checkmark-circle-outline' : 'grid-outline'}
        onSelect={handleMultiscreenPress}
        style={[styles.headerActionButton, isSelectionMode && styles.headerActionButtonActive]}
      />
      {isSelectionMode && selectedChannels.length > 0 && (
        <View style={styles.selectionCountBadge}>
          <Text style={styles.selectionCountText}>{selectedChannels.length}</Text>
        </View>
      )}
    </View>
  );

  const pageContent = (
    <>
      <Stack.Screen options={{ headerShown: false }} />
      <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
        <View style={styles.container}>
          {Platform.isTV ? (
            /* TV: Wrap header and content in single vertical node for proper navigation */
            <SpatialNavigationNode orientation="vertical">
              {/* Fixed header with title and action buttons */}
              <View style={styles.headerRow} key={`header-buttons-${hasSavedSession}-${isSelectionMode}`}>
                <Text style={styles.title}>Live TV</Text>
              </View>
              {/* Action buttons as direct child of vertical node for proper navigation back from grid */}
              {renderTVHeaderButtons()}

              {/* Content area for TV */}
              {!hasPlaylistUrl ? (
                <View style={styles.emptyState}>
                  <Text style={styles.emptyTitle}>Add an IPTV playlist</Text>
                  <Text style={styles.emptyMessage}>
                    Provide an M3U playlist URL in Settings to load channels for Live TV playback.
                  </Text>
                </View>
              ) : loading ? (
                <LoadingIndicator />
              ) : error ? (
                <View style={styles.errorContainer}>
                  <Text style={styles.errorText}>{error}</Text>
                </View>
              ) : favoriteChannels.length === 0 && regularChannels.length === 0 ? (
                <View style={styles.emptyPlaylist}>
                  <Text style={styles.emptyMessage}>
                    {filterText
                      ? `No channels match "${filterText}"`
                      : 'No channels found in the configured playlist.'}
                  </Text>
                </View>
              ) : (
                <View style={styles.scrollWrapper}>
                  <TVGridHandlersContext.Provider value={tvGridHandlers}>
                    <ScrollView
                      ref={tvGridScrollRef}
                      style={styles.virtualizedGrid}
                      showsVerticalScrollIndicator={false}
                      // TV: disable native scroll - use programmatic scrolling only to prevent
                      // native focus from moving the grid when drawer is open
                      scrollEnabled={false}
                      bounces={false}
                      contentInsetAdjustmentBehavior="never"
                      automaticallyAdjustContentInsets={false}
                      removeClippedSubviews={true}
                      scrollEventThrottle={16}
                      onScroll={handleGridScroll}
                      // @ts-ignore - TV-specific prop
                      focusable={false}
                      // @ts-ignore - TV-specific prop
                      isTVSelectable={false}
                      // @ts-ignore - TV-specific prop
                      tvRemoveGestureEnabled={true}>
                      <SpatialNavigationNode
                        orientation="vertical"
                        alignInGrid
                        key={`grid-${favoriteChannels.map((c) => c.id).join(',')}`}>
                        {visibleRows.map((rowChannels, rowIndex) => (
                          <View
                            key={`row-${rowIndex}`}
                            ref={(ref) => {
                              tvRowRefs.current[`row-${rowIndex}`] = ref;
                            }}
                            style={styles.gridRowContainer}>
                            <SpatialNavigationNode orientation="horizontal">
                              {rowChannels.map((channel) => (
                                <TVChannelGridCard
                                  key={channel.id}
                                  channel={channel}
                                  isFavorite={isFavorite(channel.id)}
                                  rowIndex={rowIndex}
                                  cardWidth={tvCardWidth}
                                />
                              ))}
                            </SpatialNavigationNode>
                          </View>
                        ))}
                      </SpatialNavigationNode>
                    </ScrollView>
                  </TVGridHandlersContext.Provider>
                </View>
              )}
            </SpatialNavigationNode>
          ) : (
            /* Mobile: Fixed header with title and action buttons */
            <View style={styles.headerRow} key={`header-buttons-${hasSavedSession}-${isSelectionMode}`}>
              <Text style={styles.title}>Live TV</Text>
              {renderMobileHeaderButtons()}
            </View>
          )}

          {isFilterActive && !Platform.isTV && (
            <View style={styles.filterContainer}>
              <TextInput
                ref={filterInputRef}
                style={styles.filterInput}
                placeholder="Filter channels by name..."
                placeholderTextColor={theme.colors.text.muted}
                value={filterText}
                onChangeText={setFilterText}
                onFocus={handleFocus}
                onBlur={handleBlur}
                clearButtonMode="while-editing"
                showSoftInputOnFocus={true}
              />
            </View>
          )}

          {/* Content area - Mobile only (TV content is rendered above in the SpatialNavigationNode) */}
          {!Platform.isTV && (
            <>
              {!hasPlaylistUrl ? (
                <View style={styles.emptyState}>
                  <Text style={styles.emptyTitle}>Add an IPTV playlist</Text>
                  <Text style={styles.emptyMessage}>
                    Provide an M3U playlist URL in Settings to load channels for Live TV playback.
                  </Text>
                  <FocusablePressable
                    text="Refresh"
                    onSelect={handleRefreshSettings}
                    loading={isRefreshing}
                    wrapperStyle={{ alignSelf: 'center' }}
                  />
                </View>
              ) : (
                <>
                  {loading ? <LoadingIndicator /> : null}
                  {error ? (
                    <View style={styles.errorContainer}>
                      <Text style={styles.errorText}>{error}</Text>
                      <FocusablePressable text="Try again" onSelect={() => refresh()} />
                    </View>
                  ) : null}
                  {!loading && !error ? (
                    <View style={styles.scrollWrapper}>
                      <ScrollView
                      ref={scrollViewRef}
                      style={styles.scrollView}
                      contentContainerStyle={styles.channelList}
                      showsVerticalScrollIndicator={false}
                      bounces={false}
                      removeClippedSubviews={Platform.isTV}
                      onScroll={handleInfiniteScroll}
                      scrollEventThrottle={16}
                      onLayout={(event: { nativeEvent: { layout: { height: number } } }) => {
                        scrollMetricsRef.current.viewportHeight = event.nativeEvent.layout.height;
                      }}>
                      {favoriteChannels.length > 0 && (
                        <>
                          <Text style={styles.sectionTitle}>Favorites</Text>
                          {favoriteChannels.map((channel, index) => (
                            <ChannelCard
                              key={channel.id}
                              channel={channel}
                              isFavorite={isFavorite(channel.id)}
                              isFirstInList={index === 0 && regularChannels.length === 0}
                              onSelect={handleChannelSelect}
                              onToggleFavorite={handleToggleFavorite}
                              onLongPress={handleChannelLongPress}
                              onFocus={handleChannelFocus}
                              registerCardRef={registerChannelRef}
                            />
                          ))}
                        </>
                      )}

                      {displayedRegularChannels.length > 0 && (
                        <>
                          <Text style={styles.sectionTitle}>
                            All Channels
                            {hasMoreChannels ? ` (${displayedRegularChannels.length}/${regularChannels.length})` : ''}
                          </Text>
                          {displayedRegularChannels.map((channel, index) => (
                            <ChannelCard
                              key={channel.id}
                              channel={channel}
                              isFavorite={isFavorite(channel.id)}
                              isFirstInList={index === 0 && favoriteChannels.length === 0}
                              onSelect={handleChannelSelect}
                              onToggleFavorite={handleToggleFavorite}
                              onLongPress={handleChannelLongPress}
                              onFocus={handleChannelFocus}
                              registerCardRef={registerChannelRef}
                            />
                          ))}
                          {hasMoreChannels && (
                            <View style={styles.loadingMoreContainer}>
                              <LoadingIndicator />
                              <Text style={styles.loadingMoreText}>Loading more channels...</Text>
                            </View>
                          )}
                        </>
                      )}

                      {favoriteChannels.length === 0 && displayedRegularChannels.length === 0 ? (
                        <View style={styles.emptyPlaylist}>
                          <Text style={styles.emptyMessage}>
                            {filterText
                              ? `No channels match "${filterText}"`
                              : 'No channels found in the configured playlist.'}
                          </Text>
                        </View>
                      ) : null}
                    </ScrollView>
                  </View>
                ) : null}
                </>
              )}
            </>
          )}
        </View>
      </FixedSafeAreaView>
      {/* Action Modal - Mobile only, TV version rendered outside SpatialNavigationRoot */}
      {isActionModalVisible && !Platform.isTV && (
        <View style={styles.actionsOverlay}>
          <Pressable style={styles.actionsBackdrop} onPress={handleCloseActionModal} />
          <View style={styles.actionsContainer}>
            <View style={styles.actionsHeader}>
              <Text style={styles.actionsTitle}>{actionChannel?.name ?? 'Channel options'}</Text>
              {actionChannel?.group ? <Text style={styles.actionsSubtitle}>{actionChannel.group}</Text> : null}
              {actionChannel?.url ? (
                <Text style={styles.actionsSubtitleSecondary} numberOfLines={1} ellipsizeMode="tail">
                  {actionChannel.url}
                </Text>
              ) : null}
            </View>
            <View style={styles.mobileModalActions}>
              <Pressable
                onPress={handleActionPlay}
                style={[styles.mobileModalButton, styles.mobileModalButtonPrimary]}>
                <Text style={[styles.mobileModalButtonText, styles.mobileModalButtonPrimaryText]}>
                  Play channel
                </Text>
              </Pressable>
              <Pressable
                onPress={handleActionToggleFavorite}
                style={styles.mobileModalButton}>
                <Text style={styles.mobileModalButtonText}>
                  {actionChannelIsFavorite ? 'Remove from favorites' : 'Add to favorites'}
                </Text>
              </Pressable>
              <Pressable
                onPress={handleActionHide}
                style={[styles.mobileModalButton, styles.mobileModalButtonDanger]}>
                <Text style={[styles.mobileModalButtonText, styles.mobileModalButtonDangerText]}>
                  Hide channel
                </Text>
              </Pressable>
              <Pressable onPress={handleCloseActionModal} style={styles.mobileModalButton}>
                <Text style={styles.mobileModalButtonText}>Cancel</Text>
              </Pressable>
            </View>
          </View>
        </View>
      )}
      <CategoryFilterModal
        visible={isCategoryModalVisible}
        onClose={handleCloseCategoryModal}
        categories={availableCategories}
        selectedCategories={selectedCategories}
        onToggleCategory={toggleCategory}
        onSelectAll={handleSelectAllCategories}
        onClearAll={handleClearAllCategories}
      />
    </>
  );

  // Selection Confirmation Modal - rendered outside SpatialNavigationRoot for native focus
  // Uses same pattern as CategoryFilterModal (raw Pressable with focused render prop)
  // Use a ref-based key that increments each time modal opens to force remount and reset hasTVPreferredFocus
  const modalKeyRef = useRef(0);
  const wasModalVisibleRef = useRef(false);
  if (isSelectionConfirmVisible && !wasModalVisibleRef.current) {
    modalKeyRef.current += 1;
  }
  wasModalVisibleRef.current = isSelectionConfirmVisible;

  // Refs for focus trapping within modal (up to 3 buttons)
  const modalButton1Ref = useRef<View>(null);
  const modalButton2Ref = useRef<View>(null);
  const modalButton3Ref = useRef<View>(null);
  const [modalButton1Handle, setModalButton1Handle] = useState<number | null>(null);
  const [modalButton2Handle, setModalButton2Handle] = useState<number | null>(null);
  const [modalButton3Handle, setModalButton3Handle] = useState<number | null>(null);

  // Get node handles for focus trapping - use setTimeout to ensure refs are set after mount
  useEffect(() => {
    if (isSelectionConfirmVisible) {
      // Small delay to ensure refs are populated after modal mounts
      const timer = setTimeout(() => {
        const handle1 = modalButton1Ref.current ? findNodeHandle(modalButton1Ref.current) : null;
        const handle2 = modalButton2Ref.current ? findNodeHandle(modalButton2Ref.current) : null;
        const handle3 = modalButton3Ref.current ? findNodeHandle(modalButton3Ref.current) : null;
        setModalButton1Handle(handle1);
        setModalButton2Handle(handle2);
        setModalButton3Handle(handle3);
      }, 50);
      return () => clearTimeout(timer);
    } else {
      // Reset handles when modal closes
      setModalButton1Handle(null);
      setModalButton2Handle(null);
      setModalButton3Handle(null);
    }
  }, [isSelectionConfirmVisible]);

  const selectionConfirmModal = isSelectionConfirmVisible ? (
    <View key={`modal-${modalKeyRef.current}`} style={styles.selectionModalOverlay} focusable={false}>
      <View style={styles.tvModalContainer} focusable={false}>
        <Text style={styles.tvModalTitle}>
          {selectedChannels.length >= 2 ? 'Launch Multiscreen?' : 'Cancel Selection?'}
        </Text>
        <Text style={styles.tvModalSubtitle}>
          {selectedChannels.length >= 2
            ? `You have ${selectedChannels.length} channel${selectedChannels.length > 1 ? 's' : ''} selected. Launch multiscreen or continue selecting?`
            : selectedChannels.length === 1
              ? 'You have 1 channel selected. Select at least 2 channels to launch multiscreen.'
              : 'No channels selected. Cancel selection mode?'}
        </Text>
        <View style={styles.tvModalActions} focusable={false}>
          {/* Cancel - exits selection mode */}
          <Pressable
            ref={modalButton1Ref}
            onPress={() => withSelectGuard(handleSelectionConfirmCancel)}
            tvParallaxProperties={{ enabled: false }}
            nextFocusUp={modalButton1Handle}
            nextFocusDown={modalButton2Handle}
            nextFocusLeft={modalButton1Handle}
            nextFocusRight={modalButton1Handle}
            style={({ focused }) => [styles.tvModalButton, focused && styles.tvModalButtonFocused]}>
            {({ focused }) => (
              <Text style={[styles.tvModalButtonText, focused && styles.tvModalButtonTextFocused]}>
                Cancel
              </Text>
            )}
          </Pressable>
          {/* Continue Selecting - just closes modal */}
          <Pressable
            ref={modalButton2Ref}
            onPress={() => withSelectGuard(handleSelectionConfirmClose)}
            hasTVPreferredFocus={selectedChannels.length < 2}
            tvParallaxProperties={{ enabled: false }}
            nextFocusUp={modalButton1Handle}
            nextFocusDown={selectedChannels.length >= 2 ? modalButton3Handle : modalButton2Handle}
            nextFocusLeft={modalButton2Handle}
            nextFocusRight={modalButton2Handle}
            style={({ focused }) => [styles.tvModalButton, focused && styles.tvModalButtonFocused]}>
            {({ focused }) => (
              <Text style={[styles.tvModalButtonText, focused && styles.tvModalButtonTextFocused]}>
                Continue Selecting
              </Text>
            )}
          </Pressable>
          {/* Launch - only when >= 2 channels selected */}
          {selectedChannels.length >= 2 && (
            <Pressable
              ref={modalButton3Ref}
              onPress={() => withSelectGuard(handleSelectionConfirmLaunch)}
              hasTVPreferredFocus={true}
              tvParallaxProperties={{ enabled: false }}
              nextFocusUp={modalButton2Handle}
              nextFocusDown={modalButton3Handle}
              nextFocusLeft={modalButton3Handle}
              nextFocusRight={modalButton3Handle}
              style={({ focused }) => [styles.tvModalButton, focused && styles.tvModalButtonFocused]}>
              {({ focused }) => (
                <Text style={[styles.tvModalButtonText, focused && styles.tvModalButtonTextFocused]}>
                  {`Launch (${selectedChannels.length})`}
                </Text>
              )}
            </Pressable>
          )}
        </View>
      </View>
    </View>
  ) : null;

  // Text Filter Modal for tvOS - rendered outside SpatialNavigationRoot for native focus
  // Uses same pattern as CategoryFilterModal (raw Pressable with focused render prop)
  // Refs for focus navigation between input and close button
  const filterInputWrapperRef = useRef<View>(null);
  const filterCloseButtonRef = useRef<View>(null);
  const [filterInputHandle, setFilterInputHandle] = useState<number | null>(null);
  const [filterCloseHandle, setFilterCloseHandle] = useState<number | null>(null);

  useEffect(() => {
    if (isFilterActive && Platform.isTV) {
      const timer = setTimeout(() => {
        const inputHandle = filterInputWrapperRef.current ? findNodeHandle(filterInputWrapperRef.current) : null;
        const closeHandle = filterCloseButtonRef.current ? findNodeHandle(filterCloseButtonRef.current) : null;
        setFilterInputHandle(inputHandle);
        setFilterCloseHandle(closeHandle);
      }, 50);
      return () => clearTimeout(timer);
    } else {
      setFilterInputHandle(null);
      setFilterCloseHandle(null);
    }
  }, [isFilterActive]);

  const textFilterModal = Platform.isTV && isFilterActive ? (
    <View style={styles.filterModalOverlay} focusable={false}>
      <View style={styles.filterModalContainer} focusable={false}>
        <View style={styles.filterModalHeader}>
          <Text style={styles.filterModalTitle}>Filter Channels</Text>
          <Text style={styles.filterModalSubtitle}>Enter a channel name to filter</Text>
        </View>

        <View style={styles.filterModalInputContainer} focusable={false}>
          <Pressable
            ref={filterInputWrapperRef}
            onPress={() => {
              filterInputRef.current?.focus();
            }}
            hasTVPreferredFocus={true}
            tvParallaxProperties={{ enabled: false }}
            nextFocusUp={filterInputHandle}
            nextFocusDown={filterCloseHandle}
            nextFocusLeft={filterInputHandle}
            nextFocusRight={filterInputHandle}
            style={({ focused }) => [
              styles.filterModalInputWrapper,
              focused && styles.filterModalInputWrapperFocused,
            ]}>
            {({ focused: inputFocused }: { focused: boolean }) => (
              <TextInput
                ref={filterInputRef}
                style={[styles.filterModalInput, inputFocused && styles.filterModalInputFocused]}
                placeholder="Type to filter channels..."
                placeholderTextColor={theme.colors.text.muted}
                {...(Platform.isTV ? { defaultValue: filterText } : { value: filterText })}
                onChangeText={handleFilterChangeText}
                onFocus={handleFocus}
                onBlur={handleBlur}
                autoCorrect={false}
                autoCapitalize="none"
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
                editable={true}
                {...(Platform.OS === 'ios' &&
                  Platform.isTV && {
                    keyboardAppearance: 'dark',
                  })}
              />
            )}
          </Pressable>
        </View>

        <View style={styles.filterModalFooter}>
          <Pressable
            ref={filterCloseButtonRef}
            onPress={handleCloseFilter}
            tvParallaxProperties={{ enabled: false }}
            nextFocusUp={filterInputHandle}
            nextFocusDown={filterCloseHandle}
            nextFocusLeft={filterCloseHandle}
            nextFocusRight={filterCloseHandle}
            style={({ focused }) => [styles.filterModalCloseButton, focused && styles.filterModalCloseButtonFocused]}>
            {({ focused }) => (
              <Text style={[styles.filterModalCloseButtonText, focused && styles.filterModalCloseButtonTextFocused]}>
                Close
              </Text>
            )}
          </Pressable>
        </View>
      </View>
    </View>
  ) : null;

  // Action Modal for TV - rendered outside SpatialNavigationRoot for native focus
  // Uses same pattern as CategoryFilterModal (raw Pressable with focused render prop)
  const tvActionModal = Platform.isTV && isActionModalVisible ? (
    <View style={styles.actionsOverlay}>
      <View style={styles.tvActionModalContainer}>
        <View style={styles.tvActionModalHeader}>
          <Text style={styles.tvActionModalTitle}>{actionChannel?.name ?? 'Channel options'}</Text>
          {actionChannel?.group ? (
            <Text style={styles.tvActionModalSubtitle}>{actionChannel.group}</Text>
          ) : null}
        </View>
        <View style={styles.tvActionModalButtons}>
          <Pressable
            onPress={handleActionPlay}
            hasTVPreferredFocus={true}
            tvParallaxProperties={{ enabled: false }}
            style={({ focused }) => [styles.tvActionModalButton, focused && styles.tvActionModalButtonFocused]}>
            {({ focused }) => (
              <Text style={[styles.tvActionModalButtonText, focused && styles.tvActionModalButtonTextFocused]}>
                Play channel
              </Text>
            )}
          </Pressable>
          <Pressable
            onPress={handleActionToggleFavorite}
            tvParallaxProperties={{ enabled: false }}
            style={({ focused }) => [styles.tvActionModalButton, focused && styles.tvActionModalButtonFocused]}>
            {({ focused }) => (
              <Text style={[styles.tvActionModalButtonText, focused && styles.tvActionModalButtonTextFocused]}>
                {actionChannelIsFavorite ? 'Remove from favorites' : 'Add to favorites'}
              </Text>
            )}
          </Pressable>
          <Pressable
            onPress={handleActionHide}
            tvParallaxProperties={{ enabled: false }}
            style={({ focused }) => [
              styles.tvActionModalButton,
              styles.tvActionModalButtonDanger,
              focused && styles.tvActionModalButtonFocused,
              focused && styles.tvActionModalButtonDangerFocused,
            ]}>
            {({ focused }) => (
              <Text
                style={[
                  styles.tvActionModalButtonText,
                  styles.tvActionModalButtonDangerText,
                  focused && styles.tvActionModalButtonTextFocused,
                ]}>
                Hide channel
              </Text>
            )}
          </Pressable>
          <Pressable
            onPress={handleCloseActionModal}
            tvParallaxProperties={{ enabled: false }}
            style={({ focused }) => [styles.tvActionModalButton, focused && styles.tvActionModalButtonFocused]}>
            {({ focused }) => (
              <Text style={[styles.tvActionModalButtonText, focused && styles.tvActionModalButtonTextFocused]}>
                Cancel
              </Text>
            )}
          </Pressable>
        </View>
      </View>
    </View>
  ) : null;

  // Wrap in SpatialNavigationRoot for TV
  if (Platform.isTV) {
    return (
      <>
        <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
          {pageContent}
        </SpatialNavigationRoot>
        {selectionConfirmModal}
        {textFilterModal}
        {tvActionModal}
      </>
    );
  }

  return (
    <>
      {pageContent}
      {selectionConfirmModal}
    </>
  );
}

export default React.memo(LiveScreen);

const createStyles = (theme: NovaTheme, screenWidth: number = 1920, screenHeight: number = 1080) => {
  const isTV = Platform.isTV;
  const scaleFactor = isTV ? 1.5 : 1;

  // Ensure we have valid screen dimensions (fallback to 1920x1080 for TV)
  const effectiveWidth = screenWidth > 0 ? screenWidth : isTV ? 1920 : 375;
  const effectiveHeight = screenHeight > 0 ? screenHeight : isTV ? 1080 : 812;

  // tvOS grid configuration (replicates Search screen)
  const columnsCount = isTV ? 6 : 1;
  const gap = isTV ? theme.spacing.lg : theme.spacing.md;
  const horizontalPadding = theme.spacing.xl * scaleFactor;
  const availableWidth = effectiveWidth - horizontalPadding * 2;
  const totalGapWidth = gap * (columnsCount - 1);
  const cardWidth = isTV ? Math.floor((availableWidth - totalGapWidth) / columnsCount) : 0;
  // Use 5:3 landscape to better match TV channel logos
  const cardHeight = isTV ? Math.round(cardWidth * (3 / 5)) : 0;
  // Computed values for virtualized grid (item height includes row gap)
  const gridItemHeight = cardHeight + gap;
  const gridHeaderSize = 80; // Section header height
  // Calculate available height for grids (screen height minus header area)
  const headerAreaHeight = isTV ? 180 : 100; // Title + action buttons
  const availableGridHeight = effectiveHeight - headerAreaHeight;

  const styles = StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
    },
    container: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: horizontalPadding,
      paddingTop: theme.spacing.xl * scaleFactor,
    },
    headerRow: {
      flexDirection: 'row',
      justifyContent: 'space-between',
      alignItems: 'center',
      marginBottom: theme.spacing.lg * scaleFactor,
    },
    actionsRow: {
      flexDirection: 'row',
      alignItems: 'center',
      gap: theme.spacing.lg,
      marginBottom: theme.spacing.lg,
    },
    headerActionButton: {
      paddingHorizontal: isTV ? theme.spacing['2xl'] : theme.spacing.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: 2,
      borderColor: 'transparent',
    },
    headerActionButtonActive: {
      backgroundColor: theme.colors.accent.primary + '30',
      borderColor: theme.colors.accent.primary,
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
    },
    refreshChip: {
      paddingHorizontal: theme.spacing.lg * scaleFactor,
      paddingVertical: theme.spacing.sm * scaleFactor,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    refreshChipFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    refreshChipText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
    },
    refreshChipTextFocused: {
      ...theme.typography.body.sm,
      color: theme.colors.text.inverse,
    },
    refreshChipDisabled: {
      opacity: 0.5,
    },
    refreshChipTextDisabled: {
      color: theme.colors.text.muted,
    },
    emptyState: {
      flex: 1,
      alignItems: 'center',
      justifyContent: 'center',
      gap: theme.spacing.md * scaleFactor,
    },
    emptyTitle: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
    },
    emptyMessage: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
      textAlign: 'center',
      maxWidth: 480 * scaleFactor,
    },
    errorContainer: {
      alignItems: 'center',
      gap: theme.spacing.md * scaleFactor,
      paddingVertical: theme.spacing.xl * scaleFactor,
    },
    errorText: {
      ...theme.typography.body.md,
      color: theme.colors.status.danger,
      textAlign: 'center',
    },
    scrollWrapper: {
      flex: 1,
      overflow: 'hidden',
    },
    scrollView: {
      flex: 1,
    },
    channelList: {
      paddingBottom: theme.spacing['2xl'] * scaleFactor,
    },
    // tvOS grid styles (borrowed from Search)
    gridContent: {
      paddingBottom: theme.spacing['3xl'],
    },
    rowContainer: {
      marginBottom: gap,
    },
    gridRow: {
      flexDirection: 'row',
      gap: gap,
      flexWrap: 'wrap',
    },
    gridCard: {
      width: cardWidth,
      height: cardHeight,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.surface,
      borderWidth: 3,
      borderColor: 'transparent',
    },
    gridCardFocused: {
      borderColor: theme.colors.accent.primary,
    },
    gridCardImageContainer: {
      width: '100%',
      height: '100%',
      backgroundColor: theme.colors.background.elevated,
      position: 'relative',
    },
    gridCardImage: {
      width: cardWidth,
      height: cardHeight,
    },
    badge: {
      position: 'absolute',
      top: theme.spacing.sm,
      right: theme.spacing.sm,
      backgroundColor: 'rgba(0, 0, 0, 0.8)',
      paddingHorizontal: theme.spacing.md,
      paddingVertical: theme.spacing.xs,
      borderRadius: theme.radius.sm,
      borderWidth: 2,
      borderColor: theme.colors.accent.primary,
    },
    badgeText: {
      ...theme.typography.caption.sm,
      color: theme.colors.accent.primary,
      fontWeight: '700',
      fontSize: 16,
      letterSpacing: 0.5,
    },
    selectionBadge: {
      position: 'absolute',
      top: theme.spacing.sm,
      right: theme.spacing.sm,
      width: isTV ? 48 : 32,
      height: isTV ? 48 : 32,
      borderRadius: isTV ? 24 : 16,
      backgroundColor: theme.colors.accent.primary,
      alignItems: 'center',
      justifyContent: 'center',
      borderWidth: 2,
      borderColor: theme.colors.text.inverse,
    },
    selectionBadgeText: {
      ...theme.typography.title.md,
      color: theme.colors.text.inverse,
      fontWeight: '700',
      fontSize: isTV ? 24 : 18,
    },
    gridCardSelected: {
      borderColor: theme.colors.accent.primary,
      borderWidth: 4,
    },
    selectionCountBadge: {
      backgroundColor: theme.colors.accent.primary,
      width: 24,
      height: 24,
      borderRadius: 12,
      alignItems: 'center',
      justifyContent: 'center',
      marginLeft: -theme.spacing.sm,
    },
    selectionCountText: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.inverse,
      fontWeight: '700',
    },
    cardTextContainer: {
      position: 'absolute',
      bottom: 0,
      left: 0,
      right: 0,
      padding: theme.spacing.md,
      gap: theme.spacing.xs,
      alignItems: 'center',
      justifyContent: 'flex-end',
      minHeight: '40%',
    },
    cardTextGradient: {
      ...StyleSheet.absoluteFillObject,
    },
    cardTitle: {
      ...theme.typography.body.lg,
      color: theme.colors.text.primary,
      textAlign: 'center',
      zIndex: 1,
      fontWeight: '600',
    },
    placeholder: {
      flex: 1,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: theme.colors.background.elevated,
    },
    placeholderImageText: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      textAlign: 'center',
    },
    // existing styles
    sectionHeaderContainer: {
      backgroundColor: isTV ? 'transparent' : theme.colors.background.base,
      paddingTop: theme.spacing.lg * scaleFactor,
      paddingBottom: theme.spacing.xl * scaleFactor,
      marginBottom: theme.spacing.md * scaleFactor,
      zIndex: 10,
    },
    sectionTitle: {
      ...(isTV ? theme.typography.shelf.title : theme.typography.title.md),
      color: theme.colors.text.primary,
    },
    channelCardWrapper: {
      marginBottom: theme.spacing.md * scaleFactor,
    },
    channelCard: {
      flexDirection: 'row',
      alignItems: 'center',
      backgroundColor: theme.colors.background.surface,
      padding: theme.spacing.lg * scaleFactor,
      borderRadius: theme.radius.lg,
      borderWidth: isTV ? 3 : StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      gap: theme.spacing.lg * scaleFactor,
      position: 'relative',
    },
    channelCardFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
      borderWidth: isTV ? 4 : StyleSheet.hairlineWidth,
    },
    channelAvatar: {
      width: 72 * scaleFactor,
      height: 72 * scaleFactor,
      borderRadius: theme.radius.md,
      overflow: 'hidden',
      backgroundColor: theme.colors.background.elevated,
      alignItems: 'center',
      justifyContent: 'center',
    },
    channelLogo: {
      width: 72 * scaleFactor,
      height: 72 * scaleFactor,
      resizeMode: 'contain',
    },
    channelPlaceholder: {
      width: '100%',
      height: '100%',
      alignItems: 'center',
      justifyContent: 'center',
    },
    channelPlaceholderText: {
      ...theme.typography.title.md,
      color: theme.colors.text.muted,
    },
    channelMeta: {
      flex: 1,
      gap: theme.spacing.xs * scaleFactor,
    },
    channelName: {
      ...theme.typography.body.lg,
      color: theme.colors.text.primary,
      fontWeight: '600',
    },
    channelNameFocused: {
      color: theme.colors.accent.primary,
    },
    channelGroup: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    channelUrl: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
    },
    channelHint: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
      marginTop: theme.spacing.xs * scaleFactor,
    },
    emptyPlaylist: {
      alignItems: 'center',
      paddingVertical: theme.spacing.xl * scaleFactor,
    },
    loadingMoreContainer: {
      alignItems: 'center',
      justifyContent: 'center',
      paddingVertical: theme.spacing.xl,
      gap: theme.spacing.sm,
    },
    loadingMoreText: {
      ...theme.typography.body.sm,
      color: theme.colors.text.muted,
    },
    channelActions: {
      marginLeft: 'auto',
      alignItems: 'center',
      justifyContent: 'center',
    },
    actionButton: {
      width: 56 * scaleFactor,
      height: 56 * scaleFactor,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderWidth: 2,
      borderColor: 'transparent',
      alignItems: 'center',
      justifyContent: 'center',
    },
    favoriteButtonActive: {
      backgroundColor: theme.colors.accent.primary + '20',
      borderColor: theme.colors.accent.primary,
    },
    actionIcon: {
      fontSize: 28 * scaleFactor,
      color: theme.colors.accent.primary,
    },
    favoriteBadge: {
      paddingHorizontal: theme.spacing.md * scaleFactor,
      paddingVertical: theme.spacing.xs * scaleFactor,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.accent.primary,
    },
    favoriteBadgeText: {
      ...theme.typography.label.md,
      color: theme.colors.text.inverse,
      fontWeight: '600',
    },
    filterContainer: {
      marginBottom: theme.spacing.md * scaleFactor,
    },
    filterInput: {
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      borderRadius: theme.radius.md,
      paddingHorizontal: theme.spacing.lg * scaleFactor,
      paddingTop: 0,
      paddingBottom: 8 * scaleFactor,
      ...theme.typography.body.md,
      color: theme.colors.text.primary,
      textAlignVertical: 'center',
      includeFontPadding: false,
      height: 40 * scaleFactor,
      justifyContent: 'center',
    },
    filterInputFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
    },
    // tvOS search-style filter input
    filterInputWrapperTV: {
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.lg,
      paddingHorizontal: theme.spacing.xl,
      borderWidth: 2,
      borderColor: 'transparent',
      height: 84,
      justifyContent: 'center',
      ...(Platform.isTV
        ? {
            shadowColor: '#000',
            shadowOpacity: 0.35,
            shadowOffset: { width: 0, height: 8 },
            shadowRadius: 16,
          }
        : null),
    },
    filterInputWrapperTVFocused: {
      borderColor: theme.colors.accent.primary,
      borderWidth: 3,
      backgroundColor: theme.colors.background.elevated,
      ...(Platform.isTV
        ? {
            shadowOpacity: 0.6,
          }
        : null),
    },
    filterInputTV: {
      flex: 1,
      fontSize: 32,
      color: theme.colors.text.primary,
      padding: 0,
      textAlignVertical: 'center',
      includeFontPadding: false,
    },
    actionsOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    actionsBackdrop: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
    },
    actionsContainer: {
      width: isTV ? '65%' : '90%',
      maxWidth: 720,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      padding: theme.spacing.xl,
      gap: theme.spacing.lg,
    },
    actionsHeader: {
      gap: theme.spacing.xs,
    },
    actionsTitle: {
      ...theme.typography.title.lg,
      color: theme.colors.text.primary,
    },
    actionsSubtitle: {
      ...theme.typography.body.sm,
      color: theme.colors.text.secondary,
    },
    actionsSubtitleSecondary: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.muted,
    },
    actionsButton: {
      alignSelf: 'stretch',
      marginBottom: theme.spacing.sm,
      minHeight: 64,
      justifyContent: 'center',
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing['2xl'],
      borderWidth: 3,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderColor: theme.colors.border.subtle,
    },
    actionsButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
    },
    actionsButtonText: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    actionsButtonTextFocused: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    actionsButtonMobile: {
      alignSelf: 'stretch',
      marginBottom: theme.spacing.sm,
    },
    // Mobile modal button styles (matching profiles page)
    mobileModalActions: {
      gap: theme.spacing.sm,
    },
    mobileModalButton: {
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing.lg,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.elevated,
      alignItems: 'center',
    },
    mobileModalButtonPrimary: {
      backgroundColor: theme.colors.accent.primary,
    },
    mobileModalButtonDanger: {
      backgroundColor: theme.colors.status.danger,
    },
    mobileModalButtonText: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
    },
    mobileModalButtonPrimaryText: {
      color: 'white',
    },
    mobileModalButtonDangerText: {
      color: 'white',
    },
    actionsDangerButton: {
      borderColor: theme.colors.status.danger,
      backgroundColor: theme.colors.status.danger + '20',
    },
    actionsDangerButtonFocused: {
      borderColor: theme.colors.status.danger,
      backgroundColor: theme.colors.status.danger + '30',
    },
    actionsDangerButtonText: {
      color: theme.colors.status.danger,
    },
    actionsDangerButtonTextFocused: {
      color: theme.colors.status.danger,
    },
    actionsDangerButtonMobile: {
      borderColor: theme.colors.status.danger,
      backgroundColor: theme.colors.status.danger + '1A',
    },
    // TV Action Modal styles (channel options on long press)
    tvActionModalContainer: {
      width: '50%',
      maxWidth: 700,
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
    },
    tvActionModalHeader: {
      padding: theme.spacing['2xl'],
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
      alignItems: 'center',
    },
    tvActionModalTitle: {
      ...theme.typography.title.xl,
      fontSize: Math.round(theme.typography.title.xl.fontSize * 1.4),
      lineHeight: Math.round(theme.typography.title.xl.lineHeight * 1.4),
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    tvActionModalSubtitle: {
      ...theme.typography.body.md,
      fontSize: Math.round(theme.typography.body.md.fontSize * 1.2),
      color: theme.colors.text.secondary,
      marginTop: theme.spacing.sm,
      textAlign: 'center',
    },
    tvActionModalButtons: {
      padding: theme.spacing.xl,
      gap: theme.spacing.md,
    },
    tvActionModalButton: {
      // TVActionButton consistent scaling using tvScale
      paddingVertical: theme.spacing.md * tvScale(1.375, 1),
      paddingHorizontal: theme.spacing.lg * tvScale(1.375, 1),
      borderRadius: theme.radius.md * tvScale(1.375, 1),
      backgroundColor: theme.colors.overlay.button,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      alignItems: 'center',
      alignSelf: 'center',
      width: '60%',
    },
    tvActionModalButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    tvActionModalButtonText: {
      ...theme.typography.label.md,
      fontSize: theme.typography.label.md.fontSize * tvScale(1.375, 1),
      lineHeight: theme.typography.label.md.lineHeight * tvScale(1.375, 1),
      color: theme.colors.text.primary,
    },
    tvActionModalButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    tvActionModalButtonDanger: {
      backgroundColor: theme.colors.status.danger + '20',
      borderColor: theme.colors.status.danger + '40',
    },
    tvActionModalButtonDangerFocused: {
      backgroundColor: theme.colors.status.danger,
      borderColor: theme.colors.status.danger,
    },
    tvActionModalButtonDangerText: {
      color: theme.colors.status.danger,
    },
    // Filter Modal styles for tvOS
    filterModalOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    filterModalContainer: {
      width: '45%',
      maxWidth: 600,
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      overflow: 'hidden',
    },
    filterModalHeader: {
      padding: theme.spacing.xl,
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: theme.colors.border.subtle,
    },
    filterModalTitle: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.xs,
    },
    filterModalSubtitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.secondary,
    },
    filterModalInputContainer: {
      padding: theme.spacing.xl,
    },
    filterModalInputWrapper: {
      borderRadius: theme.radius.md,
      borderWidth: 2,
      borderColor: 'transparent',
    },
    filterModalInputWrapperFocused: {
      borderColor: theme.colors.accent.primary,
    },
    filterModalInput: {
      fontSize: theme.typography.body.lg.fontSize * tvScale(1.375, 1),
      color: theme.colors.text.primary,
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      borderWidth: 2,
      borderColor: 'transparent',
    },
    filterModalInputFocused: {
      borderColor: theme.colors.accent.primary,
      shadowColor: theme.colors.accent.primary,
      shadowOpacity: 0.4,
      shadowOffset: { width: 0, height: 4 },
      shadowRadius: 12,
    },
    filterModalFooter: {
      padding: theme.spacing.xl,
      borderTopWidth: StyleSheet.hairlineWidth,
      borderTopColor: theme.colors.border.subtle,
      alignItems: 'center',
    },
    filterModalCloseButton: {
      paddingVertical: theme.spacing.md * tvScale(1.375, 1),
      paddingHorizontal: theme.spacing.lg * tvScale(1.375, 1),
      borderRadius: theme.radius.md * tvScale(1.375, 1),
      backgroundColor: theme.colors.overlay.button,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      alignItems: 'center',
      minWidth: '30%',
    },
    filterModalCloseButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    filterModalCloseButtonText: {
      ...theme.typography.label.md,
      fontSize: theme.typography.label.md.fontSize * tvScale(1.375, 1),
      lineHeight: theme.typography.label.md.lineHeight * tvScale(1.375, 1),
      color: theme.colors.text.primary,
    },
    filterModalCloseButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    // TV Modal styles (for selection confirmation) - TVActionButton styling
    selectionModalOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    tvModalContainer: {
      backgroundColor: theme.colors.background.elevated,
      borderRadius: theme.radius.xl,
      borderWidth: 2,
      borderColor: theme.colors.border.subtle,
      padding: theme.spacing['2xl'],
      minWidth: 400,
      maxWidth: 500,
      gap: theme.spacing.xl,
      alignItems: 'center',
    },
    tvModalTitle: {
      ...theme.typography.title.xl,
      fontSize: theme.typography.title.xl.fontSize * tvScale(1.375, 1),
      lineHeight: theme.typography.title.xl.lineHeight * tvScale(1.375, 1),
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    tvModalSubtitle: {
      ...theme.typography.body.md,
      fontSize: theme.typography.body.md.fontSize * tvScale(1.375, 1),
      lineHeight: theme.typography.body.md.lineHeight * tvScale(1.375, 1),
      color: theme.colors.text.secondary,
      textAlign: 'center',
    },
    tvModalActions: {
      gap: theme.spacing.md,
      alignItems: 'center',
    },
    tvModalButton: {
      paddingHorizontal: theme.spacing.lg * tvScale(1.375, 1),
      paddingVertical: theme.spacing.md * tvScale(1.375, 1),
      borderRadius: theme.radius.md * tvScale(1.375, 1),
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
      backgroundColor: theme.colors.overlay.button,
      alignItems: 'center',
      justifyContent: 'center',
      alignSelf: 'center',
      minWidth: '30%',
    },
    tvModalButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.accent.primary,
    },
    tvModalButtonDanger: {
      borderColor: theme.colors.status.danger,
      backgroundColor: theme.colors.status.danger + '20',
    },
    tvModalButtonDangerFocused: {
      borderColor: theme.colors.status.danger,
      backgroundColor: theme.colors.status.danger + '30',
    },
    tvModalButtonPrimary: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    tvModalButtonPrimaryFocused: {
      borderColor: theme.colors.text.inverse,
    },
    tvModalButtonPrimaryText: {
      color: theme.colors.text.inverse,
    },
    tvModalButtonText: {
      ...theme.typography.label.md,
      fontSize: theme.typography.label.md.fontSize * tvScale(1.375, 1),
      lineHeight: theme.typography.label.md.lineHeight * tvScale(1.375, 1),
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    tvModalButtonTextFocused: {
      color: theme.colors.text.inverse,
    },
    tvModalButtonDangerText: {
      color: theme.colors.status.danger,
    },
    tvModalButtonDangerTextFocused: {
      color: theme.colors.status.danger,
    },
    // Virtualized grid styles for TV
    virtualizedGrid: {
      flex: 1,
      height: availableGridHeight,
    },
    gridSectionHeader: {
      paddingTop: theme.spacing.lg,
      paddingBottom: theme.spacing.xl,
    },
    gridRowContainer: {
      flexDirection: 'row',
      gap: gap,
      width: '100%',
    },
    gridFlexContainer: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: gap,
      width: '100%',
    },
    dualGridContainer: {
      height: availableGridHeight,
    },
    favoritesGridWrapper: {
      height: gridItemHeight * 2 + gridHeaderSize + gap, // 2 rows of favorites + header
    },
    regularGridWrapper: {
      height: availableGridHeight - (gridItemHeight * 2 + gridHeaderSize + gap), // Remaining space
    },
    favoritesGrid: {
      height: gridItemHeight * 2, // 2 rows max for favorites
    },
    regularGrid: {
      height: availableGridHeight - (gridItemHeight * 2 + gridHeaderSize + gap) - gridHeaderSize, // Remaining minus header
    },
  });

  // Return styles with computed values for virtualized grid
  return {
    ...styles,
    gridItemHeight,
    gridHeaderSize,
    availableGridHeight,
  };
};
