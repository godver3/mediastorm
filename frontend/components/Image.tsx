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
import { apiService } from '../services/api';

// Use disk-only caching on TV and Android to reduce memory pressure
// Android emulators and lower-end devices struggle with memory-disk caching
const isAndroid = Platform.OS === 'android';
const DEFAULT_CACHE_POLICY = Platform.isTV || isAndroid ? 'disk' : 'memory-disk';

// DEBUG: Set to true to disable all images for performance testing
const DEBUG_DISABLE_IMAGES = __DEV__ && false;

// Image proxy configuration to reduce memory usage and enable error logging
// When enabled, TMDB images are routed through the backend which resizes and caches them
// Enabled for all platforms to provide consistent behavior and backend error visibility
const USE_IMAGE_PROXY = true;
const IMAGE_PROXY_QUALITY = 80; // JPEG quality
// Dynamic max widths based on image type - backdrops/heroes need higher resolution
const IMAGE_PROXY_MAX_WIDTH_POSTER = 780; // Match TMDB w780 poster size
const IMAGE_PROXY_MAX_WIDTH_BACKDROP = 1280; // Backdrops/heroes need HD quality
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
  // Use apiService.getBaseUrl() to get the current backend URL (which may have been
  // updated by the user or network-based switching) rather than the static API_CONFIG
  const currentApiUrl = apiService.getBaseUrl();
  const baseUrl = currentApiUrl.replace(/\/api$/, ''); // Remove /api suffix
  const params = new URLSearchParams({
    url: url,
  });

  // Add target width - use explicit width if available, otherwise default to reasonable size
  // This ensures images are always resized to reduce memory usage
  // Large images (backdrops, w780 posters for backgrounds) should not be resized down
  const isLargeImage = url.includes('/original/') || url.includes('/w1280/') || url.includes('/w780/');

  // Skip proxy entirely for large images used as backgrounds - they need full resolution
  if (isLargeImage && (!targetWidth || targetWidth === 0)) {
    return url;
  }

  const maxWidth = isLargeImage ? IMAGE_PROXY_MAX_WIDTH_BACKDROP : IMAGE_PROXY_MAX_WIDTH_POSTER;

  let proxyWidth: number;
  if (targetWidth && targetWidth > 0) {
    // Request 2x size for retina displays, but cap at type-specific max
    proxyWidth = Math.min(targetWidth * 2, maxWidth);
  } else {
    // Default: use max width for the image type
    proxyWidth = maxWidth;
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

// Debug: Track image load errors (sampled to avoid log spam)
const DEBUG_IMAGE_ERRORS = true;
let imageErrorCount = 0;
const IMAGE_ERROR_LOG_INTERVAL = 10; // Log every Nth error

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
    typeof source === 'string' ? source : source != null && typeof source === 'object' && 'uri' in source ? source.uri : '';

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

  // Wrap onError to add debug logging
  const handleError = React.useCallback(() => {
    if (DEBUG_IMAGE_ERRORS) {
      imageErrorCount++;
      // Log first few errors and then sample to avoid spam
      if (imageErrorCount <= 3 || imageErrorCount % IMAGE_ERROR_LOG_INTERVAL === 0) {
        console.warn(`[Image:Error] Failed to load image (${imageErrorCount} total errors):`, {
          originalUrl: sourceUrl?.substring(0, 100),
          proxyUrl: typeof finalSource === 'string' ? finalSource.substring(0, 150) : '[local]',
          isProxy: USE_IMAGE_PROXY && sourceUrl?.includes('image.tmdb.org'),
        });
      }
    }
    onError?.();
  }, [sourceUrl, finalSource, onError]);

  // Bail for empty URLs — avoids RN's "source.uri should not be an empty string"
  // warning and the wasted render cycle that comes with it.
  // Placed after all hooks to satisfy React's rules of hooks.
  if (!sourceUrl && typeof source !== 'number') {
    return <View style={style} />;
  }

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
        onError={handleError}
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
      onError={handleError}
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
