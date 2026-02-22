import { requireNativeModule, Platform } from 'expo-modules-core';

interface AppExitModule {
  exitApp(): void;
}

function loadNativeModule(): AppExitModule | null {
  if (Platform.OS !== 'ios' && Platform.OS !== 'android') {
    return null;
  }
  try {
    return requireNativeModule('AppExit');
  } catch {
    return null;
  }
}

const AppExitNative = loadNativeModule();

export function exitApp(): void {
  if (AppExitNative) {
    AppExitNative.exitApp();
  }
}

export default { exitApp };
