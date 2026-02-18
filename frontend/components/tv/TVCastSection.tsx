/**
 * TV Cast Section - Horizontal scrollable cast gallery with D-pad focus support
 * Uses native React Native Pressable focus for proper TV navigation
 */

import React, { memo, useCallback, useMemo, useRef, useState } from 'react';
import { FlatList, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { Image } from '../Image';
import { Ionicons } from '@expo/vector-icons';
import type { NovaTheme } from '@/theme';
import type { Credits, CastMember } from '@/services/api';
import { useTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';
import MarqueeText from './MarqueeText';

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
  /** Reduce top margin (e.g. for movies where cast follows action bar directly) */
  compactMargin?: boolean;
  /** Called when a cast member is selected */
  onCastMemberPress?: (actor: CastMember) => void;
}

// Separate component for cast photo with error handling
const CastPhoto = memo(function CastPhoto({
  profileUrl,
  styles,
  theme,
}: {
  profileUrl?: string;
  styles: ReturnType<typeof createStyles>;
  theme: NovaTheme;
}) {
  const [imageError, setImageError] = useState(false);
  const handleImageError = useCallback(() => setImageError(true), []);

  const showPlaceholder = !profileUrl || imageError;

  if (showPlaceholder) {
    return (
      <View style={[styles.photo, styles.photoPlaceholder]}>
        <Ionicons name="person" size={tvScale(48)} color={theme.colors.text.muted} />
      </View>
    );
  }

  return (
    <Image
      source={{ uri: profileUrl }}
      style={styles.photo}
      contentFit="cover"
      onError={handleImageError}
    />
  );
});

const TVCastSection = memo(function TVCastSection({
  credits,
  isLoading,
  maxCast = 12,
  onFocus,
  compactMargin,
  onCastMemberPress,
}: TVCastSectionProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const flatListRef = useRef<FlatList<CastMember>>(null);

  // Get cast to display
  const castToShow = useMemo(() => {
    if (!credits?.cast?.length) return [];
    return credits.cast.slice(0, maxCast);
  }, [credits, maxCast]);

  const handleItemFocus = useCallback(
    (index: number) => {
      onFocus?.();
      flatListRef.current?.scrollToIndex({
        index,
        animated: true,
        viewPosition: 0.3,
      });
    },
    [onFocus],
  );

  const getItemLayout = useCallback(
    (_data: ArrayLike<CastMember> | null | undefined, index: number) => ({
      length: CARD_WIDTH + CARD_GAP,
      offset: (CARD_WIDTH + CARD_GAP) * index,
      index,
    }),
    [],
  );

  const renderCastCard = useCallback(
    ({ item: actor, index }: { item: CastMember; index: number }) => {
      return (
        <Pressable
          onFocus={() => handleItemFocus(index)}
          onPress={() => onCastMemberPress?.(actor)}
          android_disableSound
          renderToHardwareTextureAndroid={true}
        >
          {({ focused }: { focused: boolean }) => (
            <View style={[styles.card, focused && styles.cardFocused]}>
              <CastPhoto profileUrl={actor.profileUrl} styles={styles} theme={theme} />
              <View style={styles.textContainer}>
                <MarqueeText style={styles.actorName} focused={focused}>
                  {actor.name}
                </MarqueeText>
                {actor.character && (
                  <MarqueeText style={styles.characterName} focused={focused}>
                    {actor.character}
                  </MarqueeText>
                )}
              </View>
            </View>
          )}
        </Pressable>
      );
    },
    [styles, theme, handleItemFocus, onCastMemberPress],
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

  const keyExtractor = useCallback(
    (actor: CastMember) => actor.name + actor.character,
    [],
  );

  if (!castToShow.length && !isLoading) {
    return null;
  }

  return (
    <View style={[styles.container, compactMargin && { marginTop: tvScale(4) }]}>
      <Text style={styles.heading}>Cast</Text>
      {isLoading ? (
        renderSkeletonCards()
      ) : (
        <View style={styles.listContainer}>
          <FlatList
            ref={flatListRef}
            data={castToShow}
            renderItem={renderCastCard}
            keyExtractor={keyExtractor}
            horizontal
            showsHorizontalScrollIndicator={false}
            getItemLayout={getItemLayout}
            ItemSeparatorComponent={ItemSeparator}
            contentContainerStyle={styles.listContent}
          />
        </View>
      )}
    </View>
  );
});

// Static separator component - avoids re-creation on each render
const ItemSeparator = () => <View style={{ width: CARD_GAP }} />;

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
    listContainer: {
      height: CARD_HEIGHT + tvScale(8),
      paddingLeft: tvScale(48),
    },
    listContent: {
      paddingRight: tvScale(48),
    },
    smallCastRow: {
      flexDirection: 'row',
      gap: CARD_GAP,
    },
    skeletonRow: {
      flexDirection: 'row',
      height: CARD_HEIGHT + tvScale(8),
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
    characterName: {
      fontSize: tvScale(15),
      color: theme.colors.text.secondary,
      marginTop: tvScale(3),
      lineHeight: tvScale(18),
    },
  });

export default TVCastSection;
