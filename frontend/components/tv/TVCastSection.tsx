/**
 * TV Cast Section - Horizontal scrollable cast gallery with D-pad focus support
 * Uses native Pressable focus with FlatList.scrollToOffset pattern
 */

import React, { memo, useCallback, useMemo, useRef } from 'react';
import {
  Dimensions,
  FlatList,
  Platform,
  Pressable,
  StyleSheet,
  Text,
  View,
  findNodeHandle,
} from 'react-native';
import { Image } from '../Image';
import { Ionicons } from '@expo/vector-icons';
import type { NovaTheme } from '@/theme';
import type { Credits, CastMember } from '@/services/api';
import { useTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';
import MarqueeText from './MarqueeText';

const isAppleTV = Platform.isTV && Platform.OS === 'ios';
const isAndroidTV = Platform.isTV && Platform.OS === 'android';

// Card dimensions - scaled for TV viewing distance
const CARD_WIDTH = tvScale(170);
const CARD_HEIGHT = tvScale(260);
const PHOTO_HEIGHT = tvScale(195);
const CARD_GAP = tvScale(18);

interface TVCastSectionProps {
  credits: Credits | null | undefined;
  isLoading?: boolean;
  maxCast?: number;
  onFocus?: () => void;
  /** Native tag of the element to focus when navigating up */
  nextFocusUp?: number;
  /** Reduce top margin (e.g. for movies where cast follows action bar directly) */
  compactMargin?: boolean;
}

const TVCastSection = memo(function TVCastSection({
  credits,
  isLoading,
  maxCast = 12,
  onFocus,
  nextFocusUp,
  compactMargin,
}: TVCastSectionProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const listRef = useRef<FlatList>(null);
  const cardRefs = useRef<Map<number, View | null>>(new Map());

  const itemSize = CARD_WIDTH + CARD_GAP;

  // Get cast to display
  const castToShow = useMemo(() => {
    if (!credits?.cast?.length) return [];
    return credits.cast.slice(0, maxCast);
  }, [credits, maxCast]);

  // Scroll handler using same pattern as home screen shelves
  // Snaps to card boundaries so items are never cut off
  const scrollToIndex = useCallback(
    (index: number) => {
      if (!Platform.isTV || !listRef.current) return;

      const { width: screenWidth } = Dimensions.get('window');
      // Keep 1 full card visible to the left
      const targetCardIndex = Math.max(0, index - 1);
      let targetX = targetCardIndex * itemSize;

      const maxScroll = Math.max(0, castToShow.length * itemSize - screenWidth);
      targetX = Math.max(0, Math.min(targetX, maxScroll));

      listRef.current.scrollToOffset({ offset: targetX, animated: true });
    },
    [castToShow.length, itemSize]
  );

  const renderCastCard = useCallback(
    ({ item: actor, index }: { item: CastMember; index: number }) => {
      const isFirst = index === 0;
      const isLast = index === castToShow.length - 1;

      // Get refs for focus containment
      const firstRef = cardRefs.current.get(0);
      const lastRef = cardRefs.current.get(castToShow.length - 1);

      return (
        <Pressable
          ref={(ref) => {
            cardRefs.current.set(index, ref);
          }}
          onFocus={() => {
            scrollToIndex(index);
            onFocus?.();
          }}
          tvParallaxProperties={{ enabled: false }}
          nextFocusLeft={isFirst && firstRef ? findNodeHandle(firstRef) ?? undefined : undefined}
          nextFocusRight={isLast && lastRef ? findNodeHandle(lastRef) ?? undefined : undefined}
          nextFocusUp={nextFocusUp}
          // @ts-ignore - Android TV performance optimization
          renderToHardwareTextureAndroid={isAndroidTV}
          style={({ focused }) => [
            styles.card,
            focused && styles.cardFocused,
          ]}
        >
          {({ focused }) => (
            <>
              {actor.profileUrl ? (
                <Image
                  source={{ uri: actor.profileUrl }}
                  style={styles.photo}
                  contentFit="cover"
                />
              ) : (
                <View style={[styles.photo, styles.photoPlaceholder]}>
                  <Ionicons
                    name="person"
                    size={tvScale(48)}
                    color={theme.colors.text.muted}
                  />
                </View>
              )}
              <View style={styles.textContainer}>
                <MarqueeText
                  style={[styles.actorName, focused && styles.textFocused]}
                  focused={focused}
                >
                  {actor.name}
                </MarqueeText>
                {actor.character && (
                  <MarqueeText
                    style={styles.characterName}
                    focused={focused}
                  >
                    {actor.character}
                  </MarqueeText>
                )}
              </View>
            </>
          )}
        </Pressable>
      );
    },
    [castToShow.length, scrollToIndex, styles, theme, onFocus, nextFocusUp]
  );

  // Render skeleton cards while loading
  const renderSkeletonCards = useCallback(() => {
    const skeletonCount = 6;
    return (
      <View style={styles.skeletonRow}>
        {Array.from({ length: skeletonCount }).map((_, index) => (
          <View key={`skeleton-${index}`} style={styles.skeletonCard}>
            <View style={styles.skeletonPhoto} />
            <View style={styles.skeletonTextContainer}>
              <View style={styles.skeletonName} />
              <View style={styles.skeletonCharacter} />
            </View>
          </View>
        ))}
      </View>
    );
  }, [styles]);

  if (!castToShow.length && !isLoading) {
    return null;
  }

  return (
    <View style={[styles.container, compactMargin && { marginTop: tvScale(4) }]}>
      <Text style={styles.heading}>Cast</Text>
      {isLoading ? (
        renderSkeletonCards()
      ) : (
        <FlatList
          ref={listRef}
          data={castToShow}
          renderItem={renderCastCard}
          keyExtractor={(item) => `cast-${item.id}`}
          horizontal
          showsHorizontalScrollIndicator={false}
          scrollEnabled={!Platform.isTV}
          getItemLayout={(_, index) => ({
            length: itemSize,
            offset: itemSize * index,
            index,
          })}
          contentContainerStyle={styles.listContent}
          initialNumToRender={isAndroidTV ? 6 : 8}
          maxToRenderPerBatch={4}
          windowSize={3}
          removeClippedSubviews={Platform.isTV}
          extraData={nextFocusUp}
        />
      )}
    </View>
  );
});

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    container: {
      marginTop: tvScale(24),
    },
    heading: {
      fontSize: tvScale(24),
      fontWeight: '700',
      color: theme.colors.text.primary,
      marginBottom: tvScale(16),
      marginLeft: tvScale(48),
    },
    skeletonRow: {
      flexDirection: 'row',
      paddingHorizontal: tvScale(48),
      gap: CARD_GAP,
    },
    skeletonCard: {
      width: CARD_WIDTH,
      height: CARD_HEIGHT,
      borderRadius: tvScale(8),
      backgroundColor: theme.colors.background.surface,
      overflow: 'hidden',
    },
    skeletonPhoto: {
      width: '100%',
      height: PHOTO_HEIGHT,
      backgroundColor: theme.colors.background.elevated,
    },
    skeletonTextContainer: {
      flex: 1,
      padding: tvScale(8),
      gap: tvScale(6),
    },
    skeletonName: {
      height: tvScale(14),
      width: '80%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: tvScale(4),
    },
    skeletonCharacter: {
      height: tvScale(12),
      width: '60%',
      backgroundColor: theme.colors.background.elevated,
      borderRadius: tvScale(4),
    },
    listContent: {
      paddingHorizontal: tvScale(48),
      gap: CARD_GAP,
    },
    card: {
      width: CARD_WIDTH,
      height: CARD_HEIGHT,
      borderRadius: tvScale(8),
      backgroundColor: theme.colors.background.surface,
      borderWidth: tvScale(3),
      borderColor: 'transparent',
      overflow: 'hidden',
    },
    cardFocused: {
      // No zoom on focus - just border highlight
      borderColor: theme.colors.accent.primary,
    },
    photo: {
      width: '100%',
      height: PHOTO_HEIGHT,
      backgroundColor: theme.colors.background.elevated,
    },
    photoPlaceholder: {
      justifyContent: 'center',
      alignItems: 'center',
    },
    textContainer: {
      flex: 1,
      padding: tvScale(10),
      justifyContent: 'flex-start',
    },
    actorName: {
      fontSize: tvScale(17),
      fontWeight: '600',
      color: theme.colors.text.primary,
      lineHeight: tvScale(20),
    },
    textFocused: {
      color: theme.colors.text.primary,
    },
    characterName: {
      fontSize: tvScale(15),
      color: theme.colors.text.secondary,
      marginTop: tvScale(3),
      lineHeight: tvScale(18),
    },
  });

export default TVCastSection;
