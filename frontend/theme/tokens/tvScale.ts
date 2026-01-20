import { Dimensions, Platform } from 'react-native';
import { isTablet as deviceIsTablet } from 'react-native-device-info';

/**
 * Unified TV and Tablet Scaling Utility
 *
 * tvOS is the reference platform for TV. All sizing should be designed for tvOS first.
 * Android TV automatically scales based on the ratio between platforms.
 * Tablets get a 1.2x scale factor for larger UI while maintaining touch interactions.
 *
 * Why this approach:
 * - tvOS and Android TV have different effective DPIs/densities
 * - Android TV typically renders at roughly 2x density compared to Apple TV
 * - Instead of maintaining two sets of values, we derive Android TV from tvOS
 * - Tablets need larger UI than phones but should still use touch-based navigation
 */

// Platform detection (cached for performance)
export const isTV = Platform.isTV;
export const isTVOS = Platform.isTV && Platform.OS === 'ios';
export const isAndroidTV = Platform.isTV && Platform.OS === 'android';

// Tablet detection (iPad + Android tablets, but not TV)
export const isTablet = !Platform.isTV && deviceIsTablet();
export const isIPad = Platform.OS === 'ios' && isTablet;
export const isAndroidTablet = Platform.OS === 'android' && isTablet;

/**
 * Whether to use TV-style layout (immersive breakpoint, 6 columns, TV components).
 * Only true for actual TV devices. Tablets use scaled mobile UI instead.
 */
export const useTVLayout = isTV;

/**
 * Whether the device supports touch interactions.
 * True for phones, tablets, and touch-enabled devices.
 * False for TV devices that use D-pad/remote navigation.
 */
export const hasTouchSupport = !isTV;

/**
 * The ratio between Android TV and tvOS effective sizes.
 * Android TV content appears larger at the same pixel values,
 * so we scale down by this factor.
 *
 * This single value controls all Android TV sizing relative to tvOS.
 * Adjust this if Android TV looks consistently too large or too small.
 */
export const ANDROID_TV_TO_TVOS_RATIO = 0.55;

/**
 * Tablet scaling factor (+20% as requested).
 * Tablets get larger UI elements than phones while maintaining touch interactions.
 */
export const TABLET_SCALE_FACTOR = 1.2;

/**
 * Scale a value for TV and tablet platforms.
 *
 * @param tvosValue - The value designed for tvOS (this is your baseline)
 * @param mobileValue - Optional fallback for mobile (defaults to tvosValue)
 * @returns The appropriately scaled value for the current platform
 *
 * @example
 * // Design for tvOS, automatically scales for Android TV and tablets
 * const iconSize = tvScale(48); // tvOS: 48, Android TV: ~26, Tablet: ~58, Mobile: 48
 *
 * @example
 * // Different mobile value
 * const cardWidth = tvScale(320, 160); // tvOS: 320, Android TV: ~176, Tablet: ~192, Mobile: 160
 */
export function tvScale(tvosValue: number, mobileValue?: number): number {
  if (isTV) {
    if (isAndroidTV) {
      return Math.round(tvosValue * ANDROID_TV_TO_TVOS_RATIO);
    }
    // tvOS - use the value as-is (it's our baseline)
    return tvosValue;
  }

  if (isTablet) {
    const baseValue = mobileValue ?? tvosValue;
    return Math.round(baseValue * TABLET_SCALE_FACTOR);
  }

  return mobileValue ?? tvosValue;
}

/**
 * Scale a value with platform-specific overrides.
 * Use this when you need fine-grained control.
 *
 * @param options.tvos - Value for tvOS (baseline)
 * @param options.androidTv - Optional explicit Android TV value (skips auto-scaling)
 * @param options.mobile - Optional mobile value
 *
 * @example
 * const menuWidth = tvScaleExplicit({
 *   tvos: 400,
 *   androidTv: 288, // Override auto-scaling for this specific case
 *   mobile: 320,
 * });
 */
export function tvScaleExplicit(options: { tvos: number; androidTv?: number; mobile?: number }): number {
  if (!isTV) {
    return options.mobile ?? options.tvos;
  }

  if (isAndroidTV) {
    // Use explicit Android TV value if provided, otherwise derive from tvOS
    return options.androidTv ?? Math.round(options.tvos * ANDROID_TV_TO_TVOS_RATIO);
  }

  return options.tvos;
}

/**
 * Get a scale multiplier for the current TV or tablet platform.
 * Useful when you need to apply scaling to multiple values.
 *
 * @returns 1.0 for tvOS (baseline), ANDROID_TV_TO_TVOS_RATIO for Android TV,
 *          TABLET_SCALE_FACTOR for tablets, 1.0 for mobile phones
 *
 * @example
 * const scale = getTVScaleMultiplier();
 * const styles = {
 *   fontSize: 24 * scale,
 *   padding: 16 * scale,
 * };
 */
export function getTVScaleMultiplier(): number {
  if (isTV) {
    if (isAndroidTV) {
      return ANDROID_TV_TO_TVOS_RATIO;
    }
    return 1; // tvOS baseline
  }

  if (isTablet) {
    return TABLET_SCALE_FACTOR;
  }

  return 1; // Mobile phone
}

/**
 * Conditional TV value helper.
 * Returns different values based on platform.
 *
 * @example
 * const padding = tvValue({ tv: 24, mobile: 12 });
 */
export function tvValue<T>(options: { tv: T; mobile: T }): T {
  return isTV ? options.tv : options.mobile;
}

/**
 * Reference width for responsive TV sizing.
 * All TV designs should target this width (1080p tvOS).
 * Values will scale proportionally to actual screen width.
 */
export const TV_REFERENCE_WIDTH = 1920;

/**
 * Screen-width-based responsive sizing for TV platforms.
 * Design everything for 1920px width (tvOS 1080p), and it automatically
 * scales to match the actual screen width on any TV platform.
 *
 * This is the PREFERRED approach for new code - it eliminates the need
 * for platform-specific multipliers entirely.
 *
 * @param designValue - The value designed for 1920px width
 * @param mobileValue - Optional fallback for mobile (defaults to designValue * 0.5)
 * @returns The proportionally scaled value for the current screen
 *
 * @example
 * // Same code works on tvOS and Android TV
 * const fontSize = responsiveSize(32);     // 32px at 1920w, scales proportionally
 * const padding = responsiveSize(24);      // 24px at 1920w
 * const iconSize = responsiveSize(48);     // 48px at 1920w
 *
 * @example
 * // With mobile fallback
 * const cardWidth = responsiveSize(320, 160); // 320px at 1920w TV, 160px on mobile
 */
export function responsiveSize(designValue: number, mobileValue?: number): number {
  if (!isTV) {
    return mobileValue ?? Math.round(designValue * 0.5);
  }

  const { width } = Dimensions.get('window');
  return Math.round(designValue * (width / TV_REFERENCE_WIDTH));
}

/**
 * Get the responsive scale multiplier for the current screen.
 * Useful when applying scaling to multiple values at once.
 *
 * @returns Scale factor based on screen width relative to reference (1920px)
 *
 * @example
 * const scale = getResponsiveScale();
 * const styles = {
 *   fontSize: 24 * scale,
 *   padding: 16 * scale,
 *   iconSize: 48 * scale,
 * };
 */
export function getResponsiveScale(): number {
  if (!isTV) {
    return 0.5; // Mobile default
  }

  const { width } = Dimensions.get('window');
  return width / TV_REFERENCE_WIDTH;
}
