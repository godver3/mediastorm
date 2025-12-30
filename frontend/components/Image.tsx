import { Image as RNImage, ImageProps as RNImageProps, ImageStyle, StyleProp } from 'react-native';
import { ComponentProps } from 'react';

// Check if expo-image native module is available
let ExpoImageModule: typeof import('expo-image') | null = null;
let hasExpoImage = false;

try {
  ExpoImageModule = require('expo-image');
  // Try to access the Image component to verify native module is loaded
  if (ExpoImageModule?.Image) {
    hasExpoImage = true;
  }
} catch {
  hasExpoImage = false;
}

// Re-export the appropriate Image component
type _ExpoImageProps = ComponentProps<typeof import('expo-image').Image>;

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

export function Image({ source, style, contentFit = 'cover', transition, blurRadius, cachePolicy, recyclingKey, priority, onError }: ImageWrapperProps) {
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
