import React, { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { ScrollView as RNScrollView } from 'react-native';
import {
  Animated,
  findNodeHandle,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  UIManager,
  useWindowDimensions,
  View,
} from 'react-native';
import { Image } from '@/components/Image';

import { CategoryFilterModal } from '@/components/CategoryFilterModal';
import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import FocusablePressable from '@/components/FocusablePressable';
import { useLiveCategories, useLiveFavorites, useLiveHiddenChannels } from '@/components/LiveContext';
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
  SpatialNavigationScrollView,
  SpatialNavigationVirtualizedGrid,
  useLockSpatialNavigation,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Direction } from '@bam.tech/lrud';
import { useIsFocused } from '@react-navigation/native';
import { LinearGradient } from 'expo-linear-gradient';
import { Stack, useRouter } from 'expo-router';

import { useLiveChannels, type LiveChannel } from '@/hooks/useLiveChannels';

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
    const channelKey = `live-channel-${channel.id}`;

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
            <Image source={{ uri: channel.logo }} style={styles.channelLogo} contentFit="contain" transition={0} cachePolicy={Platform.isTV ? 'memory-disk' : 'memory'} />
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
      const focusable = (
        <SpatialNavigationFocusableView
          focusKey={channelKey}
          onSelect={handlePress}
          onLongSelect={handleLongPress}
          onFocus={handleCardFocus}>
          {({ isFocused }: { isFocused: boolean }) => renderCardContent(isFocused)}
        </SpatialNavigationFocusableView>
      );

      return isFirstInList ? (
        <DefaultFocus key={channel.id}>{focusable}</DefaultFocus>
      ) : (
        <Fragment key={channel.id}>{focusable}</Fragment>
      );
    }

    return (
      <Pressable key={channel.id} onPress={handlePress} onLongPress={handleLongPress} delayLongPress={500}>
        {renderCardContent(false)}
      </Pressable>
    );
  },
);

function LiveScreen() {
  const theme = useTheme();
  const { width: screenWidth, height: screenHeight } = useWindowDimensions();
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
  const { showToast } = useToast();
  const { pendingPinUserId } = useUserProfiles();
  const [isCategoryModalVisible, setIsCategoryModalVisible] = useState(false);
  const [actionChannel, setActionChannel] = useState<LiveChannel | null>(null);
  const [isActionModalVisible, setIsActionModalVisible] = useState(false);
  const [filterText, setFilterText] = useState('');
  const [isFilterActive, setIsFilterActive] = useState(false);
  const [focusedChannel, setFocusedChannel] = useState<LiveChannel | null>(null);

  const isActive = isFocused && !isMenuOpen && !isCategoryModalVisible && !isActionModalVisible && !isFilterActive && !pendingPinUserId;

  // Guard against duplicate "select" events on tvOS
  const selectGuardRef = useRef(false);
  const filterClosingRef = useRef(false);
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
  const { lock, unlock } = useLockSpatialNavigation();
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

  const onDirectionHandledWithoutMovement = useCallback(
    (movement: Direction) => {
      if (movement === 'left') {
        openMenu();
      }
    },
    [openMenu],
  );

  const handleChannelSelect = useCallback(
    (channel: LiveChannel) => {
      showToast(`Playing: ${channel.name}`, { tone: 'success' });
      const streamTarget = channel.streamUrl ?? channel.url;
      router.push({
        pathname: '/player',
        params: {
          movie: streamTarget,
          headerImage: channel.logo ?? '',
          title: channel.name ?? 'Live Channel',
          preferSystemPlayer: '1',
          mediaType: 'channel',
        },
      });
    },
    [router, showToast],
  );

  const handleFocus = useCallback(() => {
    // Lock spatial navigation to prevent d-pad from navigating away while typing
    lock();
  }, [lock]);

  const handleBlur = useCallback(() => {
    // Unlock spatial navigation to re-enable d-pad navigation
    unlock();
    // Sync filter text from ref on tvOS (like search page does)
    if (Platform.isTV) {
      const finalFilter = tempFilterRef.current;
      setFilterText(finalFilter);
    }
  }, [unlock]);

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
  const filterInterceptorRef = useRef<(() => void) | null>(null);

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

  const handleOpenSettings = useCallback(() => {
    router.push('/settings');
  }, [router]);

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

  // Render item for virtualized grid (TV only)
  const renderChannelGridItem = useCallback(
    ({ item: channel }: { item: LiveChannel }) => {
      const cardKey = `channel-${channel.id}`;
      return (
        <SpatialNavigationFocusableView
          focusKey={cardKey}
          onSelect={() => handleChannelSelect(channel)}
          onLongSelect={() => {
            console.log('[live] onLongSelect triggered for:', channel.name);
            handleChannelLongPress(channel);
          }}
          onFocus={() => {
            setFocusedChannel(channel);
          }}>
          {({ isFocused }: { isFocused: boolean }) => (
            <View style={[styles.gridCard, isFocused && styles.gridCardFocused]}>
              <View style={styles.gridCardImageContainer}>
                {channel.logo ? (
                  <Image
                    key={`img-${cardKey}`}
                    source={{ uri: channel.logo }}
                    style={styles.gridCardImage}
                    contentFit="contain"
                    transition={0}
                    cachePolicy={Platform.isTV ? 'memory-disk' : 'memory'}
                    recyclingKey={cardKey}
                  />
                ) : (
                  <View style={styles.placeholder}>
                    <Text style={styles.placeholderImageText}>{channel.name?.charAt(0)?.toUpperCase() ?? '?'}</Text>
                  </View>
                )}
                {isFavorite(channel.id) && (
                  <View style={styles.badge}>
                    <Text style={styles.badgeText}>★</Text>
                  </View>
                )}
                <View style={styles.cardTextContainer}>
                  <LinearGradient
                    pointerEvents="none"
                    colors={['rgba(0,0,0,0)', 'rgba(0,0,0,0.7)', 'rgba(0,0,0,0.95)']}
                    locations={[0, 0.6, 1]}
                    start={{ x: 0.5, y: 0 }}
                    end={{ x: 0.5, y: 1 }}
                    style={styles.cardTextGradient}
                  />
                  <Text style={styles.cardTitle} numberOfLines={2}>
                    {channel.name}
                  </Text>
                </View>
              </View>
            </View>
          )}
        </SpatialNavigationFocusableView>
      );
    },
    [handleChannelSelect, handleChannelLongPress, isFavorite, styles, combinedChannels],
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

  return (
    <>
      <SpatialNavigationRoot isActive={isActive} onDirectionHandledWithoutMovement={onDirectionHandledWithoutMovement}>
        <Stack.Screen options={{ headerShown: false }} />
        <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
          <View style={styles.container}>
            {/* Fixed header with title and action buttons */}
            <SpatialNavigationNode orientation="vertical">
              <DefaultFocus>
                <SpatialNavigationNode orientation="horizontal">
                  <View style={styles.headerRow}>
                    <Text style={styles.title}>Live TV</Text>
                    <View style={styles.actionsRow}>
                      <FocusablePressable
                        text="Categories"
                        icon={Platform.isTV ? 'albums-outline' : undefined}
                        onSelect={handleOpenCategoryModal}
                        disabled={availableCategories.length === 0}
                        focusKey="live-categories"
                        style={styles.headerActionButton}
                      />
                      <FocusablePressable
                        text={isFilterActive ? 'Close Filter' : 'Filter'}
                        icon={Platform.isTV ? (isFilterActive ? 'close-outline' : 'filter-outline') : undefined}
                        onSelect={handleToggleFilter}
                        focusKey="live-filter"
                        style={styles.headerActionButton}
                      />
                    </View>
                  </View>
                </SpatialNavigationNode>
              </DefaultFocus>
            </SpatialNavigationNode>

            {isFilterActive && !Platform.isTV && (
              <View style={styles.filterContainer}>
                <SpatialNavigationFocusableView
                  focusKey="live-filter-input"
                  onSelect={() => filterInputRef.current?.focus()}>
                  {({ isFocused: filterFocused }: { isFocused: boolean }) => (
                    <TextInput
                      ref={filterInputRef}
                      style={[styles.filterInput, filterFocused && styles.filterInputFocused]}
                      placeholder="Filter channels by name..."
                      placeholderTextColor={theme.colors.text.muted}
                      value={filterText}
                      onChangeText={setFilterText}
                      onFocus={handleFocus}
                      onBlur={handleBlur}
                      clearButtonMode="while-editing"
                      showSoftInputOnFocus={true}
                    />
                  )}
                </SpatialNavigationFocusableView>
              </View>
            )}

            {/* Content area */}
            {!hasPlaylistUrl ? (
              <View style={styles.emptyState}>
                <Text style={styles.emptyTitle}>Add an IPTV playlist</Text>
                <Text style={styles.emptyMessage}>
                  Provide an M3U playlist URL in Settings to load channels for Live TV playback.
                </Text>
                <FocusablePressable text="Open Settings" onSelect={handleOpenSettings} />
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
                    {Platform.isTV ? (
                      // tvOS: Virtualized grid for performance
                      favoriteChannels.length === 0 && regularChannels.length === 0 ? (
                        <View style={styles.emptyPlaylist}>
                          <Text style={styles.emptyMessage}>
                            {filterText
                              ? `No channels match "${filterText}"`
                              : 'No channels found in the configured playlist.'}
                          </Text>
                        </View>
                      ) : (
                        // Single combined grid - favorites first, then regular channels
                        <DefaultFocus>
                          <SpatialNavigationVirtualizedGrid
                            data={combinedChannels}
                            renderItem={renderChannelGridItem}
                            numberOfColumns={6}
                            itemHeight={styles.gridItemHeight}
                            numberOfRenderedRows={16}
                            numberOfRowsVisibleOnScreen={13}
                            rowContainerStyle={styles.gridRowContainer}
                            style={styles.virtualizedGrid}
                          />
                        </DefaultFocus>
                      )
                    ) : (
                      // Mobile/web: existing vertical list
                      <SpatialNavigationNode orientation="vertical">
                        <SpatialNavigationScrollView
                          ref={scrollViewRef}
                          style={styles.scrollView}
                          contentContainerStyle={styles.channelList}
                          showsVerticalScrollIndicator={false}
                          bounces={false}
                          removeClippedSubviews={Platform.isTV}
                          onScroll={(event: { nativeEvent: { contentOffset: { y: number } } }) => {
                            scrollMetricsRef.current.offset = event.nativeEvent.contentOffset.y;
                          }}
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

                          {regularChannels.length > 0 && (
                            <>
                              <Text style={styles.sectionTitle}>All Channels</Text>
                              {regularChannels.map((channel, index) => (
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
                            </>
                          )}

                          {favoriteChannels.length === 0 && regularChannels.length === 0 ? (
                            <View style={styles.emptyPlaylist}>
                              <Text style={styles.emptyMessage}>
                                {filterText
                                  ? `No channels match "${filterText}"`
                                  : 'No channels found in the configured playlist.'}
                              </Text>
                            </View>
                          ) : null}
                        </SpatialNavigationScrollView>
                      </SpatialNavigationNode>
                    )}
                  </View>
                ) : null}
              </>
            )}
          </View>
        </FixedSafeAreaView>
      </SpatialNavigationRoot>
      {/* Action Modal */}
      {isActionModalVisible && (
        <SpatialNavigationRoot isActive={isActionModalVisible}>
          <View style={styles.actionsOverlay}>
            {!Platform.isTV && <Pressable style={styles.actionsBackdrop} onPress={handleCloseActionModal} />}
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
              <SpatialNavigationNode orientation="vertical">
                <DefaultFocus>
                  <FocusablePressable
                    focusKey="channel-action-play"
                    text="Play channel"
                    onSelect={handleActionPlay}
                    style={Platform.isTV ? styles.actionsButton : styles.actionsButtonMobile}
                    focusedStyle={Platform.isTV ? styles.actionsButtonFocused : undefined}
                    textStyle={Platform.isTV ? styles.actionsButtonText : undefined}
                    focusedTextStyle={Platform.isTV ? styles.actionsButtonTextFocused : undefined}
                  />
                </DefaultFocus>
                <FocusablePressable
                  focusKey="channel-action-favorite"
                  text={actionChannelIsFavorite ? 'Remove from favorites' : 'Add to favorites'}
                  onSelect={handleActionToggleFavorite}
                  style={Platform.isTV ? styles.actionsButton : styles.actionsButtonMobile}
                  focusedStyle={Platform.isTV ? styles.actionsButtonFocused : undefined}
                  textStyle={Platform.isTV ? styles.actionsButtonText : undefined}
                  focusedTextStyle={Platform.isTV ? styles.actionsButtonTextFocused : undefined}
                />
                <FocusablePressable
                  focusKey="channel-action-hide"
                  text="Hide channel"
                  onSelect={handleActionHide}
                  style={
                    Platform.isTV
                      ? [styles.actionsButton, styles.actionsDangerButton]
                      : [styles.actionsButtonMobile, styles.actionsDangerButtonMobile]
                  }
                  focusedStyle={
                    Platform.isTV ? [styles.actionsButtonFocused, styles.actionsDangerButtonFocused] : undefined
                  }
                  textStyle={Platform.isTV ? [styles.actionsButtonText, styles.actionsDangerButtonText] : undefined}
                  focusedTextStyle={
                    Platform.isTV ? [styles.actionsButtonTextFocused, styles.actionsDangerButtonTextFocused] : undefined
                  }
                />
                <FocusablePressable
                  focusKey="channel-action-cancel"
                  text="Cancel"
                  onSelect={handleCloseActionModal}
                  style={Platform.isTV ? styles.actionsButton : styles.actionsButtonMobile}
                  focusedStyle={Platform.isTV ? styles.actionsButtonFocused : undefined}
                  textStyle={Platform.isTV ? styles.actionsButtonText : undefined}
                  focusedTextStyle={Platform.isTV ? styles.actionsButtonTextFocused : undefined}
                />
              </SpatialNavigationNode>
            </View>
          </View>
        </SpatialNavigationRoot>
      )}
      {/* Text Filter Modal for tvOS */}
      {Platform.isTV && isFilterActive && (
        <SpatialNavigationRoot isActive={isFilterActive}>
          <View style={styles.filterModalOverlay}>
            <View style={styles.filterModalContainer}>
              <View style={styles.filterModalHeader}>
                <Text style={styles.filterModalTitle}>Filter Channels</Text>
                <Text style={styles.filterModalSubtitle}>Enter a channel name to filter</Text>
              </View>

              <SpatialNavigationNode orientation="vertical">
                <View style={styles.filterModalInputContainer}>
                  <DefaultFocus>
                    <SpatialNavigationFocusableView
                      focusKey="filter-modal-input"
                      onSelect={() => {
                        if (isFilterActive) {
                          filterInputRef.current?.focus();
                        }
                      }}
                      onBlur={() => {
                        // Blur the TextInput when spatial navigation moves away
                        filterInputRef.current?.blur();
                      }}>
                      {({ isFocused: inputFocused }: { isFocused: boolean }) => (
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
                          editable={inputFocused}
                          {...(Platform.OS === 'ios' &&
                            Platform.isTV && {
                              keyboardAppearance: 'dark',
                            })}
                        />
                      )}
                    </SpatialNavigationFocusableView>
                  </DefaultFocus>
                </View>

                <View style={styles.filterModalFooter}>
                  <FocusablePressable
                    focusKey="filter-modal-close"
                    text="Close"
                    onSelect={handleCloseFilter}
                    style={styles.filterModalCloseButton}
                    focusedStyle={styles.filterModalCloseButtonFocused}
                    textStyle={styles.filterModalCloseButtonText}
                    focusedTextStyle={styles.filterModalCloseButtonTextFocused}
                  />
                </View>
              </SpatialNavigationNode>
            </View>
          </View>
        </SpatialNavigationRoot>
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
}

export default React.memo(LiveScreen);

const createStyles = (theme: NovaTheme, screenWidth: number = 1920, screenHeight: number = 1080) => {
  const isTV = Platform.isTV;
  const scaleFactor = isTV ? 1.5 : 1;

  // tvOS grid configuration (replicates Search screen)
  const columnsCount = isTV ? 6 : 1;
  const gap = isTV ? theme.spacing.lg : theme.spacing.md;
  const horizontalPadding = theme.spacing.xl * scaleFactor;
  const availableWidth = screenWidth - horizontalPadding * 2;
  const totalGapWidth = gap * (columnsCount - 1);
  const cardWidth = isTV ? Math.floor((availableWidth - totalGapWidth) / columnsCount) : 0;
  // Use 5:3 landscape to better match TV channel logos
  const cardHeight = isTV ? Math.round(cardWidth * (3 / 5)) : 0;
  // Computed values for virtualized grid (item height includes row gap)
  const gridItemHeight = cardHeight + gap;
  const gridHeaderSize = 80; // Section header height
  // Calculate available height for grids (screen height minus header area)
  const headerAreaHeight = isTV ? 180 : 100; // Title + action buttons
  const availableGridHeight = screenHeight - headerAreaHeight;

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
      gap: theme.spacing.sm,
    },
    headerActionButton: {
      paddingHorizontal: theme.spacing['2xl'],
      backgroundColor: theme.colors.background.surface,
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
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
      width: '100%',
      height: '100%',
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
      width: '100%',
      height: '100%',
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
      backgroundColor: theme.colors.background.elevated,
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
    // Filter Modal styles for tvOS
    filterModalOverlay: {
      ...StyleSheet.absoluteFillObject,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: 'rgba(0, 0, 0, 0.85)',
      zIndex: 1000,
    },
    filterModalContainer: {
      width: '60%',
      maxWidth: 900,
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
    // Match search page input styling
    filterModalInput: {
      fontSize: 32,
      color: theme.colors.text.primary,
      paddingHorizontal: theme.spacing.lg,
      paddingVertical: theme.spacing.md,
      backgroundColor: theme.colors.background.surface,
      borderRadius: theme.radius.md,
      borderWidth: 2,
      borderColor: 'transparent',
      minHeight: 60,
    },
    filterModalInputFocused: {
      borderColor: theme.colors.accent.primary,
      borderWidth: 3,
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
    // Match watchlist filter button styling
    filterModalCloseButton: {
      minWidth: 280,
      minHeight: 64,
      justifyContent: 'center',
      paddingVertical: theme.spacing.md,
      paddingHorizontal: theme.spacing['2xl'],
      borderWidth: 3,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
      borderColor: theme.colors.border.subtle,
      alignItems: 'center',
    },
    filterModalCloseButtonFocused: {
      borderColor: theme.colors.accent.primary,
      backgroundColor: theme.colors.background.elevated,
    },
    filterModalCloseButtonText: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    filterModalCloseButtonTextFocused: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      textAlign: 'center',
    },
    // Virtualized grid styles for TV
    virtualizedGrid: {
      height: availableGridHeight,
    },
    gridSectionHeader: {
      paddingTop: theme.spacing.lg,
      paddingBottom: theme.spacing.xl,
    },
    gridRowContainer: {
      gap: gap,
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
