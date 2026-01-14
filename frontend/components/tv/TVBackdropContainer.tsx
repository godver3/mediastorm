/**
 * TV Backdrop Container - Full-screen backdrop overlay for TV details page
 * Shows large artwork behind content, similar to mobile parallax but adapted for TV
 */

import React, { memo, type ReactNode } from 'react';
import { Platform, StyleSheet, useWindowDimensions, View } from 'react-native';
import { Image } from '../Image';
import { LinearGradient } from 'expo-linear-gradient';
import type { NovaTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';

const isAppleTV = Platform.isTV && Platform.OS === 'ios';
const isAndroidTV = Platform.isTV && Platform.OS === 'android';

// Backdrop covers 70% of screen height
const BACKDROP_HEIGHT_PERCENT = 0.70;
// Content starts at 35% from top (overlapping backdrop)
const CONTENT_START_PERCENT = 0.35;

interface TVBackdropContainerProps {
  backdropUrl: string | null;
  posterUrl: string | null;
  children: ReactNode;
  theme: NovaTheme;
}

const TVBackdropContainer = memo(function TVBackdropContainer({
  backdropUrl,
  posterUrl,
  children,
  theme,
}: TVBackdropContainerProps) {
  const { height: windowHeight, width: windowWidth } = useWindowDimensions();

  const backdropHeight = windowHeight * BACKDROP_HEIGHT_PERCENT;
  const contentStartY = windowHeight * CONTENT_START_PERCENT;

  // Use backdrop if available, fall back to poster
  const imageUrl = backdropUrl || posterUrl;

  // Determine if the image is portrait (poster) vs landscape (backdrop)
  const isPortraitImage = !backdropUrl && posterUrl;

  return (
    <View style={styles.container}>
      {/* Full-screen backdrop image */}
      {imageUrl && (
        <View style={[styles.backdropContainer, { height: backdropHeight }]}>
          {isPortraitImage ? (
            // For portrait images: show blurred background + centered image
            <>
              {/* Blurred background fill */}
              {!isAndroidTV && (
                <Image
                  source={imageUrl}
                  style={StyleSheet.absoluteFill}
                  contentFit="cover"
                  blurRadius={50}
                />
              )}
              {/* Centered portrait image */}
              <Image
                source={imageUrl}
                style={styles.backdropImage}
                contentFit="contain"
                transition={0}
              />
            </>
          ) : (
            // For landscape images: cover the area
            <Image
              source={imageUrl}
              style={styles.backdropImage}
              contentFit="cover"
              transition={0}
            />
          )}

          {/* Gradient overlay for readability */}
          <LinearGradient
            colors={[
              'transparent',
              `${theme.colors.background.base}40`,
              `${theme.colors.background.base}B3`,
              theme.colors.background.base,
            ]}
            locations={[0, 0.4, 0.7, 1]}
            style={styles.gradientOverlay}
            pointerEvents="none"
          />
        </View>
      )}

      {/* Content area - positioned to overlap backdrop */}
      <View
        style={[
          styles.contentContainer,
          {
            top: contentStartY,
            minHeight: windowHeight - contentStartY,
          },
        ]}
      >
        {children}
      </View>
    </View>
  );
});

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  backdropContainer: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    zIndex: 0,
  },
  backdropImage: {
    width: '100%',
    height: '100%',
  },
  gradientOverlay: {
    position: 'absolute',
    left: 0,
    right: 0,
    bottom: 0,
    height: '60%',
  },
  contentContainer: {
    position: 'absolute',
    left: 0,
    right: 0,
    bottom: 0,
    zIndex: 1,
  },
});

export default TVBackdropContainer;
