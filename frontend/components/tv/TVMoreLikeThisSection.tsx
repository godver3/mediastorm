/**
 * TV More Like This Section - Horizontal scrollable similar content gallery with D-pad focus support
 * Uses native React Native FlatList + Pressable for focus handling instead of spatial navigation
 */

import React, { memo, useCallback, useMemo, useRef } from 'react';
import { FlatList, Platform, Pressable, StyleSheet, Text, View } from 'react-native';
import { Image } from '../Image';
import type { NovaTheme } from '@/theme';
import type { Title } from '@/services/api';
import { useTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';
import MarqueeText from './MarqueeText';

const isAndroidTV = Platform.isTV && Platform.OS === 'android';

// Card dimensions - scaled for TV viewing distance (poster aspect ratio 2:3)
const CARD_WIDTH = tvScale(170);
// Android TV needs extra height to fully show year text
const CARD_HEIGHT = isAndroidTV ? tvScale(310) + 5 : tvScale(310);
const POSTER_HEIGHT = tvScale(255);
const CARD_GAP = tvScale(18);

interface TVMoreLikeThisSectionProps {
  titles: Title[] | null | undefined;
  isLoading?: boolean;
  maxTitles?: number;
  onFocus?: () => void;
  onTitlePress?: (title: Title) => void;
}

const TVMoreLikeThisSection = memo(function TVMoreLikeThisSection({
  titles,
  isLoading,
  maxTitles = 20,
  onFocus,
  onTitlePress,
}: TVMoreLikeThisSectionProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const flatListRef = useRef<FlatList<Title>>(null);

  const itemSize = CARD_WIDTH + CARD_GAP;

  // Get titles to display
  const titlesToShow = useMemo(() => {
    if (!titles?.length) return [];
    return titles.slice(0, maxTitles);
  }, [titles, maxTitles]);

  const handleItemFocus = useCallback(
    (index: number) => {
      onFocus?.();
      flatListRef.current?.scrollToIndex({ index, animated: true, viewPosition: 0.3 });
    },
    [onFocus],
  );

  const renderTitleCard = useCallback(
    ({ item: title, index }: { item: Title; index: number }) => {
      return (
        <Pressable
          renderToHardwareTextureAndroid={true}
          onFocus={() => handleItemFocus(index)}
          onPress={() => {
            console.log(`[TVMoreLikeThis DEBUG] onPress fired for title: ${title.name} (id: ${title.id})`);
            onTitlePress?.(title);
          }}>
          {({ focused }: { focused: boolean }) => (
            <View style={[styles.card, focused && styles.cardFocused]}>
              {title.poster?.url ? (
                <Image source={{ uri: title.poster.url }} style={styles.poster} contentFit="cover" />
              ) : (
                <View style={[styles.poster, styles.posterPlaceholder]}>
                  <Text style={styles.placeholderText}>{title.name.charAt(0)}</Text>
                </View>
              )}
              <View style={styles.textContainer}>
                <MarqueeText style={styles.titleName} focused={focused}>
                  {title.name}
                </MarqueeText>
                {title.year > 0 && (
                  <Text style={styles.titleYear}>{title.year}</Text>
                )}
              </View>
            </View>
          )}
        </Pressable>
      );
    },
    [styles, handleItemFocus, onTitlePress],
  );

  // Render skeleton cards while loading
  const renderSkeletonCards = useCallback(() => {
    const skeletonCount = 6;
    return (
      <View style={styles.skeletonRow}>
        {Array.from({ length: skeletonCount }).map((_, index) => (
          <View key={`skeleton-${index}`} style={styles.skeletonCard}>
            <View style={styles.skeletonPoster} />
            <View style={styles.skeletonTextContainer}>
              <View style={styles.skeletonName} />
              <View style={styles.skeletonYear} />
            </View>
          </View>
        ))}
      </View>
    );
  }, [styles]);

  const getItemLayout = useCallback(
    (_data: ArrayLike<Title> | null | undefined, index: number) => ({
      length: itemSize,
      offset: itemSize * index,
      index,
    }),
    [itemSize],
  );

  const keyExtractor = useCallback((item: Title) => String(item.id), []);

  if (!titlesToShow.length && !isLoading) {
    return null;
  }

  return (
    <View style={styles.container}>
      <Text style={styles.heading}>More Like This</Text>
      {isLoading ? (
        renderSkeletonCards()
      ) : (
        <View style={styles.listContainer}>
          <FlatList
            ref={flatListRef}
            data={titlesToShow}
            renderItem={renderTitleCard}
            keyExtractor={keyExtractor}
            horizontal
            showsHorizontalScrollIndicator={false}
            contentContainerStyle={styles.flatListContent}
            getItemLayout={getItemLayout}
            ItemSeparatorComponent={ItemSeparator}
          />
        </View>
      )}
    </View>
  );
});

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
    flatListContent: {
      paddingRight: tvScale(48),
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
    skeletonPoster: {
      width: '100%',
      height: POSTER_HEIGHT,
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
    skeletonYear: {
      height: tvScale(12),
      width: '40%',
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
    poster: {
      width: '100%',
      height: POSTER_HEIGHT,
      backgroundColor: theme.colors.background.elevated,
    },
    posterPlaceholder: {
      justifyContent: 'center',
      alignItems: 'center',
    },
    placeholderText: {
      fontSize: tvScale(48),
      fontWeight: '600',
      color: theme.colors.text.muted,
    },
    textContainer: {
      flex: 1,
      padding: tvScale(10),
      justifyContent: 'flex-start',
    },
    titleName: {
      fontSize: tvScale(17),
      fontWeight: '600',
      color: theme.colors.text.primary,
      lineHeight: tvScale(20),
    },
    titleYear: {
      fontSize: tvScale(15),
      color: theme.colors.text.secondary,
      marginTop: tvScale(3),
    },
  });

export default TVMoreLikeThisSection;
