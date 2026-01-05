import AsyncStorage from '@react-native-async-storage/async-storage';
import { Platform } from 'react-native';
import { NetworkInfo } from 'react-native-network-info';

import type { BackendNetworkSettings } from '@/components/BackendSettingsContext';

// Dynamically import expo-location for backward compatibility with older builds
let Location: typeof import('expo-location') | null = null;
try {
  // eslint-disable-next-line @typescript-eslint/no-require-imports
  Location = require('expo-location');
} catch {
  console.log('[NetworkUrl] expo-location not available, location permission features disabled');
}

const NETWORK_SETTINGS_KEY = 'strmr.networkSettings';
const LAST_DETECTED_NETWORK_KEY = 'strmr.lastDetectedNetwork';

// Network settings can come from client, user, or global level
// This interface represents the common shape
export interface NetworkSettingsLike {
  homeWifiSSID?: string;
  homeBackendUrl?: string;
  remoteBackendUrl?: string;
}

export interface CachedNetworkSettings extends BackendNetworkSettings {
  cachedAt: number;
  source?: 'client' | 'user' | 'global'; // Track where settings came from
}

export interface NetworkUrlResult {
  url: string | null;
  isHomeNetwork: boolean | null; // null = unknown (no settings or detection failed)
  currentSSID: string | null;
  source: 'network-detection' | 'cached' | 'none';
}

/**
 * Cache the network settings from the backend for offline use.
 * Called when settings are fetched from the backend.
 * @param settings - Network settings to cache
 * @param source - Where the settings came from (client, user, or global)
 */
export async function cacheNetworkSettings(
  settings: NetworkSettingsLike | undefined,
  source: 'client' | 'user' | 'global' = 'global',
): Promise<void> {
  if (!settings || (!settings.homeWifiSSID && !settings.homeBackendUrl && !settings.remoteBackendUrl)) {
    // No network settings configured, remove any cached settings
    await AsyncStorage.removeItem(NETWORK_SETTINGS_KEY);
    return;
  }

  const cached: CachedNetworkSettings = {
    homeWifiSSID: settings.homeWifiSSID || '',
    homeBackendUrl: settings.homeBackendUrl || '',
    remoteBackendUrl: settings.remoteBackendUrl || '',
    cachedAt: Date.now(),
    source,
  };

  await AsyncStorage.setItem(NETWORK_SETTINGS_KEY, JSON.stringify(cached));
}

/**
 * Get cached network settings from AsyncStorage.
 */
export async function getCachedNetworkSettings(): Promise<CachedNetworkSettings | null> {
  try {
    const stored = await AsyncStorage.getItem(NETWORK_SETTINGS_KEY);
    if (!stored) return null;
    return JSON.parse(stored) as CachedNetworkSettings;
  } catch (err) {
    console.warn('[NetworkUrl] Failed to read cached network settings:', err);
    return null;
  }
}

/**
 * Request location permission on iOS (required for WiFi SSID access).
 * Returns true if permission was granted or if expo-location is not available.
 */
async function requestLocationPermissionForSSID(): Promise<boolean> {
  if (Platform.OS !== 'ios') {
    // Android doesn't require location for WiFi SSID in most cases
    return true;
  }

  // If expo-location isn't available (older build), skip permission check
  // and let NetworkInfo.getSSID() try anyway - it may work or return unknown
  if (!Location) {
    console.log('[NetworkUrl] requestLocationPermissionForSSID: expo-location not available, skipping permission check');
    return true;
  }

  try {
    console.log('[NetworkUrl] requestLocationPermissionForSSID: Checking current permission status...');
    const { status: existingStatus } = await Location.getForegroundPermissionsAsync();
    console.log('[NetworkUrl] requestLocationPermissionForSSID: Current status:', existingStatus);

    if (existingStatus === 'granted') {
      console.log('[NetworkUrl] requestLocationPermissionForSSID: Already granted');
      return true;
    }

    console.log('[NetworkUrl] requestLocationPermissionForSSID: Requesting permission...');
    const { status } = await Location.requestForegroundPermissionsAsync();
    console.log('[NetworkUrl] requestLocationPermissionForSSID: Request result:', status);

    return status === 'granted';
  } catch (err) {
    console.warn('[NetworkUrl] requestLocationPermissionForSSID: Error:', err);
    return false;
  }
}

/**
 * Get the current WiFi SSID.
 * Returns null on web, if not connected to WiFi, or if detection fails.
 */
export async function getCurrentSSID(): Promise<string | null> {
  // Web doesn't have access to WiFi SSID
  if (Platform.OS === 'web') {
    console.log('[NetworkUrl] getCurrentSSID: Platform is web, returning null');
    return null;
  }

  // tvOS doesn't support WiFi SSID detection
  if (Platform.isTV) {
    console.log('[NetworkUrl] getCurrentSSID: Platform is TV, returning null');
    return null;
  }

  try {
    // On iOS, we need location permission to read WiFi SSID
    if (Platform.OS === 'ios') {
      const hasPermission = await requestLocationPermissionForSSID();
      if (!hasPermission) {
        console.log('[NetworkUrl] getCurrentSSID: Location permission not granted, cannot read SSID');
        return null;
      }
    }

    console.log('[NetworkUrl] getCurrentSSID: Calling NetworkInfo.getSSID()...');
    const ssid = await NetworkInfo.getSSID();
    console.log('[NetworkUrl] getCurrentSSID: Raw SSID result:', JSON.stringify(ssid));

    // iOS returns "<unknown ssid>" when permission not granted or not on WiFi
    // Android may return null or empty string
    if (!ssid || ssid === '<unknown ssid>' || ssid === 'error') {
      console.log('[NetworkUrl] getCurrentSSID: SSID is invalid/unknown, returning null');
      return null;
    }
    console.log('[NetworkUrl] getCurrentSSID: Valid SSID detected:', ssid);
    return ssid;
  } catch (err) {
    console.warn('[NetworkUrl] getCurrentSSID: Failed to get SSID:', err);
    return null;
  }
}

/**
 * Cache the last detected network state for quick startup.
 */
export async function cacheLastDetectedNetwork(isHome: boolean, ssid: string | null): Promise<void> {
  try {
    await AsyncStorage.setItem(
      LAST_DETECTED_NETWORK_KEY,
      JSON.stringify({ isHome, ssid, detectedAt: Date.now() }),
    );
  } catch (err) {
    console.warn('[NetworkUrl] Failed to cache last detected network:', err);
  }
}

/**
 * Get the last detected network state.
 */
export async function getLastDetectedNetwork(): Promise<{ isHome: boolean; ssid: string | null; detectedAt: number } | null> {
  try {
    const stored = await AsyncStorage.getItem(LAST_DETECTED_NETWORK_KEY);
    if (!stored) return null;
    return JSON.parse(stored);
  } catch (err) {
    return null;
  }
}

/**
 * Determine the appropriate backend URL based on current network.
 *
 * Logic:
 * 1. Get current SSID
 * 2. Compare with cached network settings
 * 3. If on home WiFi (SSID matches), return homeBackendUrl
 * 4. If not on home WiFi (or on mobile data), return remoteBackendUrl
 * 5. Cache the detection result for quick startup
 *
 * @param settings - Network settings (from client, user, or global level)
 * @returns NetworkUrlResult with the URL to use and detection metadata
 */
export async function getNetworkBasedUrl(
  settings?: NetworkSettingsLike | null,
): Promise<NetworkUrlResult> {
  console.log('[NetworkUrl] getNetworkBasedUrl: Starting, settings provided:', !!settings);

  // If no settings provided, try to load from cache
  let effectiveSettings = settings;
  if (!effectiveSettings) {
    console.log('[NetworkUrl] getNetworkBasedUrl: No settings provided, loading from cache...');
    effectiveSettings = await getCachedNetworkSettings();
    console.log('[NetworkUrl] getNetworkBasedUrl: Cached settings:', JSON.stringify(effectiveSettings));
  } else {
    console.log('[NetworkUrl] getNetworkBasedUrl: Using provided settings:', JSON.stringify(settings));
  }

  // No network settings configured
  if (!effectiveSettings?.homeWifiSSID || (!effectiveSettings.homeBackendUrl && !effectiveSettings.remoteBackendUrl)) {
    console.log('[NetworkUrl] getNetworkBasedUrl: No network settings configured, returning none');
    console.log('[NetworkUrl] getNetworkBasedUrl: homeWifiSSID:', effectiveSettings?.homeWifiSSID);
    console.log('[NetworkUrl] getNetworkBasedUrl: homeBackendUrl:', effectiveSettings?.homeBackendUrl);
    console.log('[NetworkUrl] getNetworkBasedUrl: remoteBackendUrl:', effectiveSettings?.remoteBackendUrl);
    return {
      url: null,
      isHomeNetwork: null,
      currentSSID: null,
      source: 'none',
    };
  }

  console.log('[NetworkUrl] getNetworkBasedUrl: Settings valid - homeWifiSSID:', effectiveSettings.homeWifiSSID);
  console.log('[NetworkUrl] getNetworkBasedUrl: homeBackendUrl:', effectiveSettings.homeBackendUrl);
  console.log('[NetworkUrl] getNetworkBasedUrl: remoteBackendUrl:', effectiveSettings.remoteBackendUrl);

  // Get current SSID
  const currentSSID = await getCurrentSSID();
  console.log('[NetworkUrl] getNetworkBasedUrl: Current SSID detected:', JSON.stringify(currentSSID));

  // On web or if SSID detection failed, use last known state or default to remote
  if (currentSSID === null) {
    console.log('[NetworkUrl] getNetworkBasedUrl: SSID is null, checking last known state...');
    const lastKnown = await getLastDetectedNetwork();
    console.log('[NetworkUrl] getNetworkBasedUrl: Last known state:', JSON.stringify(lastKnown));
    if (lastKnown) {
      // Use cached detection if recent (within 24 hours)
      const isRecent = Date.now() - lastKnown.detectedAt < 24 * 60 * 60 * 1000;
      console.log('[NetworkUrl] getNetworkBasedUrl: Last known is recent:', isRecent);
      if (isRecent) {
        const url = (lastKnown.isHome ? effectiveSettings.homeBackendUrl : effectiveSettings.remoteBackendUrl) || null;
        console.log('[NetworkUrl] getNetworkBasedUrl: Using cached detection, url:', url);
        return {
          url,
          isHomeNetwork: lastKnown.isHome,
          currentSSID: lastKnown.ssid,
          source: 'cached',
        };
      }
    }
    // Default to remote when on web or detection fails
    console.log('[NetworkUrl] getNetworkBasedUrl: Defaulting to remote URL:', effectiveSettings.remoteBackendUrl);
    return {
      url: effectiveSettings.remoteBackendUrl ?? null,
      isHomeNetwork: false,
      currentSSID: null,
      source: 'cached',
    };
  }

  // Compare SSID with home network
  const isHomeNetwork = currentSSID.toLowerCase() === effectiveSettings.homeWifiSSID.toLowerCase();
  const url = isHomeNetwork ? effectiveSettings.homeBackendUrl : effectiveSettings.remoteBackendUrl;

  console.log('[NetworkUrl] getNetworkBasedUrl: SSID comparison:');
  console.log('[NetworkUrl] getNetworkBasedUrl:   current:', currentSSID.toLowerCase());
  console.log('[NetworkUrl] getNetworkBasedUrl:   home:', effectiveSettings.homeWifiSSID.toLowerCase());
  console.log('[NetworkUrl] getNetworkBasedUrl:   isHomeNetwork:', isHomeNetwork);
  console.log('[NetworkUrl] getNetworkBasedUrl:   selected URL:', url);

  // Cache this detection for quick startup
  await cacheLastDetectedNetwork(isHomeNetwork, currentSSID);

  return {
    url: url || null,
    isHomeNetwork,
    currentSSID,
    source: 'network-detection',
  };
}

/**
 * Check if network-based URL switching is configured and active.
 */
export async function isNetworkUrlSwitchingEnabled(): Promise<boolean> {
  const settings = await getCachedNetworkSettings();
  return !!(
    settings?.homeWifiSSID &&
    (settings.homeBackendUrl || settings.remoteBackendUrl)
  );
}
