import { Dimensions, useWindowDimensions } from 'react-native';
import { isAndroidTV } from '@/theme/tokens/tvScale';

/**
 * Returns screen dimensions, using the correct source for each platform.
 *
 * On Android TV, useWindowDimensions can return incorrect values (especially in emulators),
 * so we fall back to Dimensions.get('screen') which is more reliable.
 */
export function useTVDimensions() {
  const windowDimensions = useWindowDimensions();

  if (isAndroidTV) {
    const screen = Dimensions.get('screen');
    return {
      width: screen.width,
      height: screen.height,
    };
  }

  return {
    width: windowDimensions.width,
    height: windowDimensions.height,
  };
}
