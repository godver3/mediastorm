import { Image as RNImage, ImageProps as RNImageProps, ImageStyle, NativeModules, Platform, StyleProp } from 'react-native';

// Use disk-only caching on TV to reduce memory pressure (112MB+ GL memory savings)
const DEFAULT_CACHE_POLICY = Platform.isTV ? 'disk' : 'memory-disk';

// Check if expo-image native module is available BEFORE requiring
// This prevents the require from throwing when native module doesn't exist
// eslint-disable-next-line @typescript-eslint/no-explicit-any
let ExpoImageModule: any = null;
let hasExpoImage = false;

// Only try to require expo-image if the native module exists
if (NativeModules.ExpoImage) {
  try {
    ExpoImageModule = require('expo-image');
    if (ExpoImageModule?.Image) {
      hasExpoImage = true;
    }
  } catch {
    // expo-image JS module not available
    hasExpoImage = false;
  }
}

interface ImageWrapperProps {
  source: string | { uri: string } | number;
  style?: StyleProp<ImageStyle>;
  contentFit?: 'cover' | 'contain' | 'fill' | 'none' | 'scale-down';
  transition?: number;
  blurRadius?: number;
  cachePolicy?: 'none' | 'disk' | 'memory' | 'memory-disk';
  recyclingKey?: string;
  priority?: 'low' | 'normal' | 'high';
  onError?: () => void;
}

export function Image({ source, style, contentFit = 'cover', transition, blurRadius, cachePolicy = DEFAULT_CACHE_POLICY, recyclingKey, priority, onError }: ImageWrapperProps) {
  if (hasExpoImage && ExpoImageModule) {
    const ExpoImage = ExpoImageModule.Image;
    return (
      <ExpoImage
        source={source}
        style={style}
        contentFit={contentFit}
        transition={transition}
        blurRadius={blurRadius}
        cachePolicy={cachePolicy}
        recyclingKey={recyclingKey}
        priority={priority}
        onError={onError}
      />
    );
  }

  // Fallback to React Native Image
  const rnSource = typeof source === 'string' ? { uri: source } : source;
  const resizeMode = contentFit === 'cover' ? 'cover' : contentFit === 'contain' ? 'contain' : 'cover';

  return (
    <RNImage
      source={rnSource as RNImageProps['source']}
      style={style}
      resizeMode={resizeMode}
      blurRadius={blurRadius}
      onError={onError}
    />
  );
}

// Export utilities if available
export const clearMemoryCache = async () => {
  if (hasExpoImage && ExpoImageModule?.Image) {
    return ExpoImageModule.Image.clearMemoryCache();
  }
  return Promise.resolve();
};

export const clearDiskCache = async () => {
  if (hasExpoImage && ExpoImageModule?.Image) {
    return ExpoImageModule.Image.clearDiskCache();
  }
  return Promise.resolve();
};

export { hasExpoImage };
