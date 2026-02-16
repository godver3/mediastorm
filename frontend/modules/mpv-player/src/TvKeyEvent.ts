import { NativeModules, Platform } from 'react-native';

const { TvKeyEventModule } = NativeModules;

/**
 * Enable Android TV key event forwarding to JS via DeviceEventEmitter.
 * Wraps the Activity's Window.Callback to intercept D-pad and media key events,
 * emitting them as "onHWKeyEvent" events that RemoteControlManager listens for.
 */
export function enableTvKeyForwarding(): void {
  if (Platform.OS !== 'android' || !Platform.isTV || !TvKeyEventModule) return;
  TvKeyEventModule.enable();
}

/**
 * Disable Android TV key event forwarding (unwraps the Window.Callback).
 */
export function disableTvKeyForwarding(): void {
  if (Platform.OS !== 'android' || !Platform.isTV || !TvKeyEventModule) return;
  TvKeyEventModule.disable();
}
