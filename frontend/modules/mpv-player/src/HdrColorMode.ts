import { NativeModules, Platform } from 'react-native';

export interface HdrCapabilities {
  supported: boolean;
  hdr10: boolean;
  dolbyVision: boolean;
  hlg: boolean;
  hdr10Plus: boolean;
  maxLuminance: number;
  minLuminance: number;
}

const { HdrColorModeModule } = NativeModules;

/**
 * Enable HDR color mode (COLOR_MODE_HDR) on the hosting activity.
 * Only works on Android. Returns display HDR capabilities.
 */
export async function enableHDR(): Promise<HdrCapabilities | null> {
  if (Platform.OS !== 'android' || !HdrColorModeModule) {
    return null;
  }
  return HdrColorModeModule.enableHDR();
}

/**
 * Disable HDR color mode (reset to COLOR_MODE_DEFAULT) on the hosting activity.
 * Only works on Android.
 */
export async function disableHDR(): Promise<boolean> {
  if (Platform.OS !== 'android' || !HdrColorModeModule) {
    return false;
  }
  return HdrColorModeModule.disableHDR();
}
