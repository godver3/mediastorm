import React from 'react';
import {
  Image as RNImage,
  ImageProps as RNImageProps,
  ImageStyle,
  NativeModules,
  Platform,
  StyleProp,
  View,
} from 'react-native';
import { API_CONFIG } from '../config/api';

// Use disk-only caching on TV and Android to reduce memory pressure
// Android emulators and lower-end devices struggle with memory-disk caching
const isAndroid = Platform.OS === 'android';
const DEFAULT_CACHE_POLICY = Platform.isTV || isAndroid ? 'disk' : 'memory-disk';

// DEBUG: Set to true to disable all images for performance testing
const DEBUG_DISABLE_IMAGES = __DEV__ && false;

// Image proxy configuration to reduce memory usage
// When enabled, TMDB images are routed through the backend which resizes and caches them
// Enable for TV (limited memory) and Android (emulator/device performance)
const USE_IMAGE_PROXY = Platform.isTV || isAndroid;
const IMAGE_PROXY_QUALITY = 80; // JPEG quality
const IMAGE_PROXY_MAX_WIDTH = 500; // Max width for resized images
const DEBUG_IMAGE_PROXY = __DEV__ && false; // Log proxy URL conversions

/**
 * Convert TMDB image URL to proxy URL if image proxy is enabled.
 * The proxy resizes images and caches them on the backend, reducing memory usage.
 * @param url Original image URL
 * @param targetWidth Target width to resize to (extracted from style)
 * @returns Proxy URL or original URL if proxy not applicable
 */
function getProxyUrl(url: string, targetWidth?: number): string {
  if (!USE_IMAGE_PROXY || !url) {
    return url;
  }

  // Only proxy TMDB images
  if (!url.includes('image.tmdb.org')) {
    return url;
  }

  // Build proxy URL with resize parameters
  const baseUrl = API_CONFIG.BASE_URL.replace(/\/api$/, ''); // Remove /api suffix
  const params = new URLSearchParams({
    url: url,
  });

  // Add target width - use explicit width if available, otherwise default to reasonable size
  // This ensures images are always resized to reduce memory usage
  let proxyWidth: number;
  if (targetWidth && targetWidth > 0) {
    // Request 2x size for retina displays, but cap at platform-specific max
    proxyWidth = Math.min(targetWidth * 2, IMAGE_PROXY_MAX_WIDTH);
  } else {
    // Default: smaller for posters, larger for backdrops (but capped)
    // Check if it's a backdrop (original size) vs poster (w500)
    proxyWidth = url.includes('/original/') ? IMAGE_PROXY_MAX_WIDTH : Math.min(300, IMAGE_PROXY_MAX_WIDTH);
  }
  params.set('w', proxyWidth.toString());

  params.set('q', IMAGE_PROXY_QUALITY.toString());

  const proxyUrl = `${baseUrl}/api/images/proxy?${params.toString()}`;

  if (DEBUG_IMAGE_PROXY) {
    console.log(`[Image:Proxy] ${url.substring(0, 70)}... -> ${proxyUrl.substring(0, 100)}...`);
  }

  return proxyUrl;
}

/**
 * Extract width from style prop (handles both StyleSheet and inline styles)
 */
function extractWidthFromStyle(style: StyleProp<ImageStyle>): number | undefined {
  if (!style) return undefined;

  // Handle array of styles
  if (Array.isArray(style)) {
    for (let i = 0; i < style.length; i++) {
      const s = style[i];
      if (s && typeof s === 'object' && !Array.isArray(s) && 'width' in s) {
        const width = (s as ImageStyle).width;
        if (typeof width === 'number') {
          return width;
        }
      }
    }
    return undefined;
  }

  // Handle style object
  if (typeof style === 'object' && 'width' in style) {
    const width = (style as ImageStyle).width;
    if (typeof width === 'number') {
      return width;
    }
  }

  return undefined;
}

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
  contentPosition?: 'center' | 'top' | 'bottom' | 'left' | 'right' | 'top left' | 'top right' | 'bottom left' | 'bottom right' | { top?: number; right?: number; bottom?: number; left?: number };
  transition?: number;
  blurRadius?: number;
  cachePolicy?: 'none' | 'disk' | 'memory' | 'memory-disk';
  recyclingKey?: string;
  priority?: 'low' | 'normal' | 'high';
  onError?: () => void;
  onLoad?: () => void;
}

export function Image({
  source,
  style,
  contentFit = 'cover',
  contentPosition,
  transition,
  blurRadius,
  cachePolicy = DEFAULT_CACHE_POLICY,
  recyclingKey,
  priority,
  onError,
  onLoad,
}: ImageWrapperProps) {
  // DEBUG: Return placeholder when images are disabled for performance testing
  if (DEBUG_DISABLE_IMAGES) {
    return <View style={[style, { backgroundColor: '#333' }]} />;
  }

  const sourceUrl =
    typeof source === 'string' ? source : typeof source === 'object' && 'uri' in source ? source.uri : '';

  // Convert to proxy URL if image proxy is enabled (for TMDB images on TV)
  const targetWidth = extractWidthFromStyle(style);
  const finalUrl = React.useMemo(() => getProxyUrl(sourceUrl, targetWidth), [sourceUrl, targetWidth]);

  // Create the final source object for rendering
  const finalSource = React.useMemo(() => {
    if (typeof source === 'number') {
      // Local require() source - don't modify
      return source;
    }
    // Use the proxy URL
    return finalUrl;
  }, [source, finalUrl]);

  if (hasExpoImage && ExpoImageModule) {
    const ExpoImage = ExpoImageModule.Image;
    return (
      <ExpoImage
        source={finalSource}
        style={style}
        contentFit={contentFit}
        contentPosition={contentPosition}
        transition={transition}
        blurRadius={blurRadius}
        cachePolicy={cachePolicy}
        recyclingKey={recyclingKey}
        priority={priority}
        onError={onError}
        onLoad={onLoad}
      />
    );
  }

  // Fallback to React Native Image
  const rnSource = typeof finalSource === 'string' ? { uri: finalSource } : finalSource;
  const resizeMode = contentFit === 'cover' ? 'cover' : contentFit === 'contain' ? 'contain' : 'cover';

  return (
    <RNImage
      source={rnSource as RNImageProps['source']}
      style={style}
      resizeMode={resizeMode}
      blurRadius={blurRadius}
      onError={onError}
      onLoad={onLoad}
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
