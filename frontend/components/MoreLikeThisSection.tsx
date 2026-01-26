import React, { memo, useMemo } from 'react';
import { View, Text, ScrollView, StyleSheet, ActivityIndicator, Pressable, Platform } from 'react-native';
import { Image } from '@/components/Image';
import type { NovaTheme } from '@/theme';
import type { Title } from '@/services/api';

interface MoreLikeThisSectionProps {
  titles: Title[] | null | undefined;
  isLoading?: boolean;
  theme: NovaTheme;
  onTitlePress?: (title: Title) => void;
}

const MoreLikeThisSection = memo(function MoreLikeThisSection({
  titles,
  isLoading,
  theme,
  onTitlePress,
}: MoreLikeThisSectionProps) {
  const styles = useMemo(() => createStyles(theme), [theme]);

  if (isLoading) {
    return (
      <View style={styles.container}>
        <Text style={styles.heading}>More Like This</Text>
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="small" color={theme.colors.text.muted} />
        </View>
      </View>
    );
  }

  if (!titles?.length) {
    return null;
  }

  return (
    <View style={styles.container}>
      <Text style={styles.heading}>More Like This</Text>
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        contentContainerStyle={styles.scrollContent}
        style={styles.scrollView}>
        {titles.map((title) => {
          const content = (
            <>
              {title.poster?.url ? (
                <Image source={{ uri: title.poster.url }} style={styles.poster} contentFit="cover" />
              ) : (
                <View style={[styles.poster, styles.posterPlaceholder]}>
                  <Text style={styles.placeholderText}>{title.name.charAt(0)}</Text>
                </View>
              )}
              <Text style={styles.titleName} numberOfLines={2}>
                {title.name}
              </Text>
              {title.year > 0 && <Text style={styles.titleYear}>{title.year}</Text>}
            </>
          );

          // On TV platforms, use View to avoid native focus conflicts
          if (Platform.isTV) {
            return (
              <View key={title.id} style={styles.card}>
                {content}
              </View>
            );
          }

          return (
            <Pressable key={title.id} style={styles.card} onPress={() => onTitlePress?.(title)}>
              {content}
            </Pressable>
          );
        })}
      </ScrollView>
    </View>
  );
});

const createStyles = (theme: NovaTheme) =>
  StyleSheet.create({
    container: {
      marginTop: theme.spacing.xl,
    },
    heading: {
      ...theme.typography.title.md,
      color: theme.colors.text.primary,
      marginBottom: theme.spacing.md,
    },
    loadingContainer: {
      height: 200,
      justifyContent: 'center',
      alignItems: 'center',
    },
    scrollView: {
      marginHorizontal: -theme.spacing['3xl'],
    },
    scrollContent: {
      paddingHorizontal: theme.spacing['3xl'],
      gap: theme.spacing.md,
    },
    card: {
      width: 100,
    },
    poster: {
      width: 100,
      height: 150,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
    },
    posterPlaceholder: {
      justifyContent: 'center',
      alignItems: 'center',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    placeholderText: {
      ...theme.typography.title.lg,
      color: theme.colors.text.muted,
    },
    titleName: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      fontWeight: '600',
      marginTop: theme.spacing.xs,
    },
    titleYear: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      marginTop: 2,
    },
  });

export default MoreLikeThisSection;
