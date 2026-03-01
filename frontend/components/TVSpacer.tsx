import React from 'react';
import { Platform, View } from 'react-native';

import { useTVDimensions } from '@/hooks/useTVDimensions';
import { TV_REFERENCE_HEIGHT } from '@/theme/tokens/tvScale';

interface TVSpacerProps {
  /** Height in pixels, designed for tvOS 1080p. Scales proportionally on other TV platforms. */
  size: number;
  /** Optional mobile fallback height. Defaults to 0 (no spacer on mobile). */
  mobileSize?: number;
}

/**
 * Viewport-height-scaled spacer for TV pages.
 *
 * Design the `size` for tvOS at 1080p — it scales proportionally on Android TV
 * (and any other TV resolution) using the viewport height ratio.
 *
 * On tvOS 1080p: renders at exactly `size` px.
 * On Android TV ~540p: renders at ~`size * 0.5` px.
 * On mobile: renders at `mobileSize` px (default 0 = hidden).
 *
 * @example
 * <TVSpacer size={24} />                   // 24px on tvOS, ~12px on Android TV, hidden on mobile
 * <TVSpacer size={24} mobileSize={16} />   // 24px on tvOS, ~12px on Android TV, 16px on mobile
 */
export function TVSpacer({ size, mobileSize = 0 }: TVSpacerProps) {
  const { height: screenHeight } = useTVDimensions();

  if (!Platform.isTV) {
    if (mobileSize <= 0) return null;
    return <View style={{ height: mobileSize }} />;
  }

  const effectiveHeight = screenHeight > 0 ? screenHeight : 1080;
  const vh = effectiveHeight / TV_REFERENCE_HEIGHT;
  const scaledHeight = Math.round(size * vh);

  return <View style={{ height: scaledHeight }} />;
}
