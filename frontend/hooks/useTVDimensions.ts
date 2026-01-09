import { Dimensions, Platform, useWindowDimensions } from 'react-native';

const isAndroid = Platform.OS === 'android';

/**
 * Returns screen dimensions, using the correct source for each platform.
 *
 * On Android (both TV and mobile), useWindowDimensions can return incorrect values
 * (especially in emulators/simulators), so we fall back to Dimensions.get('screen')
 * which is more reliable.
 */
export function useTVDimensions() {
  const windowDimensions = useWindowDimensions();

  if (isAndroid) {
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
