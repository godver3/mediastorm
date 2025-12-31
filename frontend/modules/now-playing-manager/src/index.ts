import { requireNativeModule, Platform } from 'expo-modules-core';

export interface NowPlayingInfo {
  title: string;
  subtitle?: string;
  artist?: string;
  duration?: number;
  currentTime?: number;
  playbackRate?: number;
  imageUri?: string;
}

interface NowPlayingManagerModule {
  updateNowPlaying(info: NowPlayingInfo): Promise<void>;
  updatePlaybackPosition(currentTime: number, duration: number, playbackRate: number): Promise<void>;
  clearNowPlaying(): Promise<void>;
  setupRemoteCommands(): Promise<void>;
}

// Safely load the native module - only available on iOS with native build
function loadNativeModule(): NowPlayingManagerModule | null {
  if (Platform.OS !== 'ios') {
    return null;
  }
  try {
    return requireNativeModule('NowPlayingManager');
  } catch {
    // Module not available (e.g., Expo Go, web, or native module not linked)
    return null;
  }
}

const NowPlayingManagerNative = loadNativeModule();

// Export wrapper functions that safely handle non-iOS platforms
export async function updateNowPlaying(info: NowPlayingInfo): Promise<void> {
  if (NowPlayingManagerNative) {
    await NowPlayingManagerNative.updateNowPlaying(info);
  }
}

export async function updatePlaybackPosition(
  currentTime: number,
  duration: number,
  playbackRate: number
): Promise<void> {
  if (NowPlayingManagerNative) {
    await NowPlayingManagerNative.updatePlaybackPosition(currentTime, duration, playbackRate);
  }
}

export async function clearNowPlaying(): Promise<void> {
  if (NowPlayingManagerNative) {
    await NowPlayingManagerNative.clearNowPlaying();
  }
}

export async function setupRemoteCommands(): Promise<void> {
  if (NowPlayingManagerNative) {
    await NowPlayingManagerNative.setupRemoteCommands();
  }
}

export default {
  updateNowPlaying,
  updatePlaybackPosition,
  clearNowPlaying,
  setupRemoteCommands,
};
