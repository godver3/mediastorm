import React, { memo, useMemo } from 'react';
import { View, Text, ScrollView, StyleSheet, ActivityIndicator } from 'react-native';
import { Image } from 'expo-image';
import { Ionicons } from '@expo/vector-icons';
import type { NovaTheme } from '@/theme';
import type { Credits, CastMember } from '@/services/api';

interface CastSectionProps {
  credits: Credits | null | undefined;
  isLoading?: boolean;
  theme: NovaTheme;
}

const CastSection = memo(function CastSection({ credits, isLoading, theme }: CastSectionProps) {
  const styles = useMemo(() => createCastStyles(theme), [theme]);

  if (isLoading) {
    return (
      <View style={styles.container}>
        <Text style={styles.heading}>Cast</Text>
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="small" color={theme.colors.text.muted} />
        </View>
      </View>
    );
  }

  if (!credits?.cast?.length) {
    return null;
  }

  // Show top 8 actors
  const topCast = credits.cast.slice(0, 8);

  return (
    <View style={styles.container}>
      <Text style={styles.heading}>Top Billed Cast</Text>
      <ScrollView
        horizontal
        showsHorizontalScrollIndicator={false}
        contentContainerStyle={styles.scrollContent}
        style={styles.scrollView}
      >
        {topCast.map((actor) => (
          <View key={actor.id} style={styles.actorCard}>
            {actor.profileUrl ? (
              <Image
                source={{ uri: actor.profileUrl }}
                style={styles.actorPhoto}
                contentFit="cover"
              />
            ) : (
              <View style={[styles.actorPhoto, styles.actorPhotoPlaceholder]}>
                <Ionicons name="person" size={32} color={theme.colors.text.muted} />
              </View>
            )}
            <Text style={styles.actorName} numberOfLines={2}>
              {actor.name}
            </Text>
            <Text style={styles.characterName} numberOfLines={2}>
              {actor.character}
            </Text>
          </View>
        ))}
      </ScrollView>
    </View>
  );
});

const createCastStyles = (theme: NovaTheme) =>
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
    actorCard: {
      width: 100,
    },
    actorPhoto: {
      width: 100,
      height: 150,
      borderRadius: theme.radius.md,
      backgroundColor: theme.colors.background.surface,
    },
    actorPhotoPlaceholder: {
      justifyContent: 'center',
      alignItems: 'center',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    actorName: {
      ...theme.typography.body.sm,
      color: theme.colors.text.primary,
      fontWeight: '600',
      marginTop: theme.spacing.xs,
    },
    characterName: {
      ...theme.typography.caption.sm,
      color: theme.colors.text.secondary,
      marginTop: 2,
    },
  });

export default CastSection;
