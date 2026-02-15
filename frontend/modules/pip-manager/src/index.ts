import { requireNativeModule, Platform, EventEmitter, type EventSubscription } from 'expo-modules-core';

interface PipManagerModule {
  enableAutoPip(): void;
  disableAutoPip(): void;
  isAutoPipEnabled(): boolean;
  enterPip(): boolean;
}

// Safely load the native module - only available on Android with native build
function loadNativeModule(): PipManagerModule | null {
  if (Platform.OS !== 'android') {
    console.log('[PipManager] Not Android, skipping native module');
    return null;
  }
  try {
    const module = requireNativeModule('PipManager');
    console.log('[PipManager] Native module loaded successfully:', !!module);
    return module;
  } catch (e) {
    // Module not available (e.g., Expo Go, web, or native module not linked)
    console.log('[PipManager] Failed to load native module:', e);
    return null;
  }
}

const PipManagerNative = loadNativeModule();
console.log('[PipManager] PipManagerNative is:', PipManagerNative ? 'available' : 'NULL');

// Event emitter for PiP mode change events
const emitter = PipManagerNative
  ? new EventEmitter<{ onPipModeChanged: (event: { isActive: boolean }) => void }>(PipManagerNative as any)
  : null;

/**
 * Enable auto Picture-in-Picture when the app goes to background.
 * Call this when video playback starts.
 */
export function enableAutoPip(): void {
  console.log('[PipManager] enableAutoPip called, native available:', !!PipManagerNative);
  if (PipManagerNative) {
    PipManagerNative.enableAutoPip();
    console.log('[PipManager] Native enableAutoPip() invoked');
  }
}

/**
 * Disable auto Picture-in-Picture when the app goes to background.
 * Call this when video playback stops or the player is unmounted.
 */
export function disableAutoPip(): void {
  if (PipManagerNative) {
    PipManagerNative.disableAutoPip();
  }
}

/**
 * Check if auto PiP is currently enabled.
 */
export function isAutoPipEnabled(): boolean {
  if (PipManagerNative) {
    return PipManagerNative.isAutoPipEnabled();
  }
  return false;
}

/**
 * Programmatically enter Picture-in-Picture mode.
 * Returns true if PiP was entered successfully, false otherwise.
 */
export function enterPip(): boolean {
  if (PipManagerNative) {
    return PipManagerNative.enterPip();
  }
  return false;
}

/**
 * Subscribe to PiP mode changes.
 * Callback receives { isActive: boolean }.
 * Returns a Subscription that should be removed on cleanup.
 */
export function addPipModeListener(callback: (event: { isActive: boolean }) => void): EventSubscription | null {
  if (!emitter) return null;
  return emitter.addListener('onPipModeChanged', callback);
}

export default {
  enableAutoPip,
  disableAutoPip,
  isAutoPipEnabled,
  enterPip,
  addPipModeListener,
};
