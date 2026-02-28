import { useEffect, useRef } from 'react';
import { Platform } from 'react-native';

import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';
import { Directions, SpatialNavigation } from '@/services/tv-navigation';

// Direction type matching @bam.tech/lrud
type Direction = 'right' | 'left' | 'up' | 'down' | 'enter' | 'long_enter' | '*';

// Global flag to ensure we only configure once
let isConfigured = false;

export default function ConfigureRemoteControl() {
  const configuredRef = useRef(false);

  useEffect(() => {
    // Prevent multiple configurations
    if (isConfigured || configuredRef.current) {
      return;
    }

    // Skip on non-TV platforms
    if (!Platform.isTV) {
      isConfigured = true;
      configuredRef.current = true;
      return;
    }

    isConfigured = true;
    configuredRef.current = true;

    // Configure remote control for spatial navigation on all TV platforms
    SpatialNavigation.configureRemoteControl({
      remoteControlSubscriber: (callback: (direction: Direction | null) => void) => {
        const mapping: { [key in SupportedKeys]: Direction | null } = {
          [SupportedKeys.Right]: Directions.RIGHT as Direction,
          [SupportedKeys.Left]: Directions.LEFT as Direction,
          [SupportedKeys.Up]: Directions.UP as Direction,
          [SupportedKeys.Down]: Directions.DOWN as Direction,
          [SupportedKeys.Enter]: Directions.ENTER as Direction,
          [SupportedKeys.LongEnter]: Directions.LONG_ENTER as Direction,
          [SupportedKeys.Back]: null,
          [SupportedKeys.PlayPause]: null,
          [SupportedKeys.FastForward]: null,
          [SupportedKeys.Rewind]: null,
        };

        const remoteControlListener = (keyEvent: SupportedKeys) => {
          const direction = mapping[keyEvent] ?? null;
          if (keyEvent === SupportedKeys.Enter || direction === Directions.ENTER) {
            console.log('[ConfigureRemoteControl][SELECT-DEBUG] Enter key → direction:', direction, '→ passing to spatial nav callback');
          }
          callback(direction);
        };

        return RemoteControlManager.addKeydownListener(remoteControlListener);
      },

      remoteControlUnsubscriber: (unsubscribe: () => void) => {
        unsubscribe();
      },
    });

    return () => {
      // Don't reset on unmount - we want this to stay configured
      // isConfigured remains true
    };
  }, []);

  return null;
}
