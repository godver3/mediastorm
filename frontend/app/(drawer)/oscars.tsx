import { FixedSafeAreaView } from '@/components/FixedSafeAreaView';
import { ListCard } from '@/components/ListCard';
import { useMenuContext } from '@/components/MenuContext';
import { useUserProfiles } from '@/components/UserProfilesContext';
import { OSCARS_2026_CATEGORIES } from '@/constants/oscars2026';
import {
  DefaultFocus,
  SpatialNavigationFocusableView,
  SpatialNavigationNode,
  SpatialNavigationRoot,
} from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { responsiveSize, isAndroidTV } from '@/theme/tokens/tvScale';
import { useTVDimensions } from '@/hooks/useTVDimensions';
import { useIsFocused } from '@react-navigation/native';
import { Stack, useRouter } from 'expo-router';
import React, { useCallback, useMemo } from 'react';
import { Image, Platform, Pressable, ScrollView, StyleSheet, Text, View } from 'react-native';

const OSCAR_TINT = 'rgba(245,158,11,0.12)';
const OSCAR_SHEEN = 'rgba(255,215,0,0.13)';
const OSCAR_PORTRAIT_URL =
  'https://snworksceo.imgix.net/cds/4448688e-9197-40eb-98f3-126855c4001c.sized-1000x1000.jpg?w=1000&dpr=2';
const TV_PORTRAIT_WIDTH = responsiveSize(400, 300);

export default function OscarsScreen() {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme), [theme]);
  const router = useRouter();
  const { isOpen: isMenuOpen } = useMenuContext();
  const { pendingPinUserId, profileSelectorVisible } = useUserProfiles();
  const isFocused = useIsFocused();
  const isActive = isFocused && !isMenuOpen && !pendingPinUserId && !profileSelectorVisible;
  const { width: screenWidth } = useTVDimensions();

  const handleCategoryPress = useCallback(
    (categoryId: string) => {
      router.push({
        pathname: '/shelf',
        params: { shelf: `oscar-${categoryId}` },
      } as any);
    },
    [router],
  );

  const cardWidth = Platform.isTV ? responsiveSize(280, 220) : 175;
  const cardSpacing = Platform.isTV ? responsiveSize(16, 12) : 12;

  // Calculate columns that fit the available width (reserving space for portrait on TV)
  const columnsPerRow = useMemo(() => {
    if (!Platform.isTV) return 2;
    return 4;
  }, [screenWidth, cardWidth, cardSpacing]);

  // Chunk categories into rows based on available columns
  const rows = useMemo(
    () => chunkArray(OSCARS_2026_CATEGORIES, columnsPerRow),
    [columnsPerRow],
  );

  const content = (
    <FixedSafeAreaView style={styles.safeArea} edges={['top']}>
      <View style={styles.container}>
        <View style={styles.titleRow}>
          <Text style={styles.title}>Oscars 2026</Text>
          <Text style={styles.subtitle}>98th Academy Awards</Text>
        </View>

        {Platform.isTV ? (
          <View style={styles.tvRow}>
            <View style={styles.scrollClip}>
              <ScrollView
                showsVerticalScrollIndicator={false}
                style={{ overflow: 'visible' }}
                contentContainerStyle={{ overflow: 'visible' }}
              >
                <SpatialNavigationNode orientation="vertical" alignInGrid>
                  {rows.map((row, rowIdx) => (
                    <SpatialNavigationNode orientation="horizontal" key={`row-${rowIdx}`}>
                      <View style={[styles.gridRow, { gap: cardSpacing }]}>
                        {row.map((category, colIdx) => {
                          const card = (
                            <SpatialNavigationFocusableView
                              key={category.id}
                              onSelect={() => handleCategoryPress(category.id)}
                            >
                              {({ isFocused: cardFocused }: { isFocused: boolean }) => (
                                <View style={{ width: cardWidth }}>
                                  <ListCard
                                    variant="gradient"
                                    title={category.name}
                                    iconName={category.icon}
                                    tintColor={OSCAR_TINT}
                                    sheenColor={OSCAR_SHEEN}
                                    isFocused={cardFocused}
                                    aspectRatio={16 / 7}
                                  />
                                </View>
                              )}
                            </SpatialNavigationFocusableView>
                          );
                          return rowIdx === 0 && colIdx === 0 ? (
                            <DefaultFocus key={category.id}>{card}</DefaultFocus>
                          ) : card;
                        })}
                      </View>
                    </SpatialNavigationNode>
                  ))}
                </SpatialNavigationNode>
              </ScrollView>
            </View>
            <View style={styles.portraitContainer}>
              <Image
                source={{ uri: OSCAR_PORTRAIT_URL }}
                style={styles.portraitImage}
                resizeMode="cover"
              />
            </View>
          </View>
        ) : (
          <ScrollView showsVerticalScrollIndicator={false}>
            <View style={styles.grid}>
              {OSCARS_2026_CATEGORIES.map((category) => (
                <Pressable
                  key={category.id}
                  onPress={() => handleCategoryPress(category.id)}
                  style={{ width: cardWidth }}
                >
                  <ListCard
                    variant="gradient"
                    title={category.name}
                    iconName={category.icon}
                    tintColor={OSCAR_TINT}
                    sheenColor={OSCAR_SHEEN}
                    aspectRatio={16 / 7}
                  />
                </Pressable>
              ))}
            </View>
          </ScrollView>
        )}
      </View>
    </FixedSafeAreaView>
  );

  return (
    <SpatialNavigationRoot isActive={isActive}>
      <Stack.Screen options={{ title: 'Oscars 2026' }} />
      {content}
    </SpatialNavigationRoot>
  );
}

function chunkArray<T>(arr: T[], size: number): T[][] {
  const chunks: T[][] = [];
  for (let i = 0; i < arr.length; i += size) {
    chunks.push(arr.slice(i, i + size));
  }
  return chunks;
}

const TV_LEFT_MARGIN = responsiveSize(48, 36);

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    safeArea: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      ...(Platform.isTV && { overflow: 'visible' as const }),
    },
    container: {
      flex: 1,
      backgroundColor: Platform.isTV ? 'transparent' : theme.colors.background.base,
      paddingHorizontal: Platform.isTV ? 0 : theme.spacing.xl,
      marginLeft: Platform.isTV ? TV_LEFT_MARGIN : 0,
      paddingTop: Platform.isTV ? responsiveSize(32, 24) : theme.spacing.xl,
      ...(Platform.isTV && { overflow: 'visible' as const }),
    },
    titleRow: {
      marginBottom: Platform.isTV ? responsiveSize(24, 18) : theme.spacing.lg,
      ...(Platform.isTV && { zIndex: 1, backgroundColor: 'transparent' }),
    },
    title: {
      ...theme.typography.title.xl,
      color: theme.colors.text.primary,
      ...(Platform.isTV && { fontSize: responsiveSize(36, 28) }),
    },
    subtitle: {
      ...theme.typography.body.md,
      color: theme.colors.text.muted,
      marginTop: 4,
      ...(isAndroidTV ? {
        fontSize: Math.round(theme.typography.body.md.fontSize * 1.4),
        lineHeight: Math.round(theme.typography.body.md.lineHeight * 1.4),
      } : null),
    },
    tvRow: {
      flex: 1,
      flexDirection: 'row',
      overflow: 'visible' as const,
    },
    scrollClip: {
      flex: 1,
      overflow: 'hidden',
      marginLeft: Platform.isTV ? -responsiveSize(12, 10) : 0,
      paddingLeft: Platform.isTV ? responsiveSize(12, 10) : 0,
      marginTop: Platform.isTV ? -responsiveSize(12, 10) : 0,
      paddingTop: Platform.isTV ? responsiveSize(12, 10) : 0,
    },
    portraitContainer: {
      width: TV_PORTRAIT_WIDTH,
      marginRight: responsiveSize(48, 36),
      alignItems: 'center',
      justifyContent: 'flex-start',
      paddingTop: responsiveSize(8, 4),
    },
    portraitImage: {
      width: TV_PORTRAIT_WIDTH,
      height: TV_PORTRAIT_WIDTH * 1.35,
      borderRadius: responsiveSize(16, 12),
      opacity: 0.85,
    },
    gridRow: {
      flexDirection: 'row',
      flexWrap: 'nowrap',
      marginBottom: Platform.isTV ? responsiveSize(16, 12) : 12,
      overflow: 'visible' as const,
    },
    grid: {
      flexDirection: 'row',
      flexWrap: 'wrap',
      gap: 12,
      paddingBottom: 40,
    },
  });
