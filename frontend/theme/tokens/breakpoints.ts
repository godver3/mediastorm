export type Breakpoint = 'compact' | 'cozy' | 'spacious' | 'immersive';

export const breakpointThresholds: Record<Exclude<Breakpoint, 'compact'>, number> = {
  cozy: 600,
  spacious: 1024,
  immersive: 1440,
};

export const discoveryColumnMap: Record<Breakpoint, number> = {
  compact: 2,
  cozy: 3,
  spacious: 5,
  immersive: 6,
};

export const breakpointScaleMultiplier: Record<Breakpoint, number> = {
  compact: 1,
  cozy: 1.05,
  spacious: 1.12,
  immersive: 1.2,
};

// Re-export unified TV and tablet scaling utilities
export {
  tvScale,
  tvScaleExplicit,
  getTVScaleMultiplier,
  tvValue,
  isTV,
  isTVOS,
  isAndroidTV,
  ANDROID_TV_TO_TVOS_RATIO,
  isTablet,
  isIPad,
  isAndroidTablet,
  TABLET_SCALE_FACTOR,
  useTVLayout,
  hasTouchSupport,
} from './tvScale';

// ============================================================================
// DEPRECATED: Legacy TV scaling factors
// These are kept for backwards compatibility during migration.
// Use tvScale() from './tvScale' instead for new code.
// ============================================================================

// Apple TV (tvOS) needs less aggressive scaling due to different DPI/viewport
/** @deprecated Use tvScale() instead */
export const APPLE_TV_SCALE_FACTOR = 0.85;
// Android TV uses more aggressive scaling
/** @deprecated Use tvScale() instead */
export const ANDROID_TV_SCALE_FACTOR = 0.5;
// Fallback for other TV platforms
/** @deprecated Use tvScale() instead */
export const TV_SCALE_FACTOR = 0.5;

/**
 * Get the appropriate TV scale factor based on the platform
 * @deprecated Use tvScale() or getTVScaleMultiplier() instead
 */
export function getTVScaleFactor(platformOS?: string): number {
  if (platformOS === 'ios') {
    return APPLE_TV_SCALE_FACTOR;
  }
  if (platformOS === 'android') {
    return ANDROID_TV_SCALE_FACTOR;
  }
  return TV_SCALE_FACTOR;
}

export function getBreakpoint(width: number): Breakpoint {
  if (width < breakpointThresholds.cozy) {
    return 'compact';
  }
  if (width < breakpointThresholds.spacious) {
    return 'cozy';
  }
  if (width < breakpointThresholds.immersive) {
    return 'spacious';
  }
  return 'immersive';
}
