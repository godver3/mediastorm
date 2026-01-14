import { useEffect, useState } from 'react';
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

  // For Android, use state to track screen dimensions reactively
  const [androidDimensions, setAndroidDimensions] = useState(() => {
    const screen = Dimensions.get('screen');
    return { width: screen.width, height: screen.height };
  });

  useEffect(() => {
    if (!isAndroid) return;

    const subscription = Dimensions.addEventListener('change', ({ screen }) => {
      setAndroidDimensions({ width: screen.width, height: screen.height });
    });

    // Also re-check on mount in case initial value was 0
    const screen = Dimensions.get('screen');
    if (screen.width > 0 && screen.height > 0) {
      setAndroidDimensions({ width: screen.width, height: screen.height });
    }

    return () => subscription?.remove();
  }, []);

  if (isAndroid) {
    return androidDimensions;
  }

  return {
    width: windowDimensions.width,
    height: windowDimensions.height,
  };
}
