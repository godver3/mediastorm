import mitt from 'mitt';
import { BackHandler, DeviceEventEmitter, EventSubscription, HWEvent, Platform, TVEventHandler } from 'react-native';

// TVMenuControl and TVEventControl are available on tvOS but not typed in RN types
// eslint-disable-next-line @typescript-eslint/no-var-requires
const TVMenuControl: { enableTVMenuKey?: () => void; disableTVMenuKey?: () => void } | undefined = Platform.isTV
  ? require('react-native').TVMenuControl
  : undefined;

// eslint-disable-next-line @typescript-eslint/no-var-requires
const TVEventControl: { enableTVPanGesture?: () => void; disableTVPanGesture?: () => void } | undefined =
  Platform.isTV && Platform.OS === 'ios' ? require('react-native').TVEventControl : undefined;

// Pan gesture constants for tvOS Siri Remote
const PAN_GRID_SIZE = 1920; // Virtual grid size for pan gesture
const PAN_GRID_COLUMNS = 5; // Number of columns in the grid (more = more sensitive)
const PAN_EMIT_INTERVAL = 30; // ms between emitting consecutive key events
const PAN_THROTTLE_DELAY = 30; // ms throttle for pan event processing

// Key hold acceleration constants
const ACCEL_START_DELAY = 400; // ms before acceleration kicks in
const ACCEL_BASE_INTERVAL = 80; // ms between acceleration checks
const ACCEL_MAX_MULTIPLIER = 5; // max events to emit at once
const ACCEL_RAMP_EVENTS = 8; // number of events to reach max multiplier

import { BackInterceptor, RemoteControlManagerInterface } from './RemoteControlManager.interface';
import { SupportedKeys } from './SupportedKeys';

const TV_EVENT_KEY_MAPPING: Record<string, SupportedKeys> = {
  blur: SupportedKeys.Back,
  right: SupportedKeys.Right,
  swipeRight: SupportedKeys.Right,
  left: SupportedKeys.Left,
  swipeLeft: SupportedKeys.Left,
  up: SupportedKeys.Up,
  swipeUp: SupportedKeys.Up,
  down: SupportedKeys.Down,
  swipeDown: SupportedKeys.Down,
  select: SupportedKeys.Enter,
  enter: SupportedKeys.Enter,
  dpadCenter: SupportedKeys.Enter,
  selectDown: SupportedKeys.Enter,
  longSelect: SupportedKeys.LongEnter,
  playPause: SupportedKeys.PlayPause,
  longPlayPause: SupportedKeys.PlayPause,
  fastForward: SupportedKeys.FastForward,
  rewind: SupportedKeys.Rewind,
  menu: SupportedKeys.Back,
  longMenu: SupportedKeys.Back,
  back: SupportedKeys.Back,
  goBack: SupportedKeys.Back,
};

const WEB_KEY_MAPPING: Record<string, SupportedKeys> = {
  ArrowRight: SupportedKeys.Right,
  ArrowLeft: SupportedKeys.Left,
  ArrowUp: SupportedKeys.Up,
  ArrowDown: SupportedKeys.Down,
  Enter: SupportedKeys.Enter,
  NumpadEnter: SupportedKeys.Enter,
  Return: SupportedKeys.Enter,
  Backspace: SupportedKeys.Back,
  GoBack: SupportedKeys.Back, // For LG WebOS Magic Remote
  Escape: SupportedKeys.Back,
  MediaPlayPause: SupportedKeys.PlayPause,
  MediaTrackNext: SupportedKeys.FastForward,
  MediaTrackPrevious: SupportedKeys.Rewind,
};

const isTvEnvironment = (): boolean => {
  if (Platform.isTV) {
    return true;
  }

  const constants = (Platform.constants ?? {}) as {
    uiMode?: string;
    interfaceIdiom?: string;
  };

  if (Platform.OS === 'android' && constants.uiMode === 'tv') {
    return true;
  }
  if (Platform.OS === 'ios' && constants.interfaceIdiom === 'tv') {
    return true;
  }

  const envFlag = (() => {
    if (typeof globalThis === 'undefined') {
      return null;
    }
    const { process } = globalThis as { process?: { env?: Record<string, unknown> } };
    const envValue = process?.env?.EXPO_TV;
    return typeof envValue === 'string' ? envValue.toLowerCase() : null;
  })();

  if (envFlag === '1' || envFlag === 'true') {
    return true;
  }

  const globalFlag = typeof globalThis !== 'undefined' ? (globalThis as { EXPO_TV?: unknown }).EXPO_TV : undefined;
  return globalFlag === true || globalFlag === '1' || globalFlag === 'true';
};

const isKeyDownAction = (action: HWEvent['eventKeyAction'] | string): boolean => {
  // On tvOS and Android TV emulator, RN often reports only keyup (1).
  // Treat all events as actionable and rely on dedup.
  if (Platform.isTV) {
    return true;
  }

  // Some platforms don't provide an action; treat as keydown for compatibility
  if (action === null || action === undefined) {
    return true;
  }

  if (typeof action === 'number') {
    // 0 represents keydown, 1 represents keyup (per RN TV docs)
    return action === 0;
  }

  if (typeof action === 'string') {
    const normalized = action.toLowerCase();
    // Only treat "down" as a keydown event; ignore "up"
    return normalized === 'down';
  }

  return true;
};

class RemoteControlManager implements RemoteControlManagerInterface {
  private eventEmitter = mitt<{ keyDown: SupportedKeys }>();
  private tvEventSubscription?: EventSubscription;
  private backHandlerSubscription?: { remove: () => void };
  private webListener?: (event: KeyboardEvent) => void;
  private androidEventSubscription?: { remove: () => void };
  private hasEnabledMenuKey = false;
  private hasEnabledPanGesture = false;
  private lastEmittedKey?: SupportedKeys;
  private lastEmittedAt = 0;
  private backInterceptors: BackInterceptor[] = [];
  // Track longSelect state: only emit LongEnter on action 0, reset on action 1
  private longSelectPressed = false;

  // Pan gesture state for tvOS velocity-based navigation
  private panOrientation: 'x' | 'y' | undefined = undefined;
  private panLastIndex = 0;
  private panThrottleWait = false;

  // Key hold acceleration state
  private accelKey?: SupportedKeys;
  private accelStartTime = 0;
  private accelEventCount = 0;

  constructor() {
    if (isTvEnvironment()) {
      this.attachTvListeners();
    } else if (Platform.OS === 'web') {
      this.attachWebListeners();
    }
  }

  private attachTvListeners = () => {
    this.enableTvMenuKey();
    this.enableTvPanGesture();
    if (Platform.OS === 'android') {
      this.androidEventSubscription = DeviceEventEmitter.addListener('onHWKeyEvent', this.handleTvEvent);
    } else {
      this.tvEventSubscription = TVEventHandler.addListener(this.handleTvEvent);
    }

    if (Platform.OS === 'android') {
      this.backHandlerSubscription = BackHandler.addEventListener('hardwareBackPress', this.handleHardwareBackPress);
    }
  };

  private attachWebListeners = () => {
    if (typeof window === 'undefined') {
      return;
    }

    this.webListener = (event: KeyboardEvent) => {
      const mappedKey = WEB_KEY_MAPPING[event.code] ?? WEB_KEY_MAPPING[event.key];
      if (!mappedKey) {
        return;
      }
      event.preventDefault();
      if (this.shouldEmit(mappedKey)) {
        if (!this.interceptIfNeeded(mappedKey)) {
          this.eventEmitter.emit('keyDown', mappedKey);
        }
      }
    };

    window.addEventListener('keydown', this.webListener);
  };

  private enableTvMenuKey = () => {
    if (this.hasEnabledMenuKey) {
      return;
    }

    if (Platform.OS === 'ios' && Platform.isTV && typeof TVMenuControl?.enableTVMenuKey === 'function') {
      try {
        TVMenuControl.enableTVMenuKey();
        this.hasEnabledMenuKey = true;
      } catch (error) {
        console.warn('[remote-control] Failed to enable TV menu key:', error);
      }
    }
  };

  private disableTvMenuKey = () => {
    if (!this.hasEnabledMenuKey) {
      return;
    }

    if (Platform.OS === 'ios' && Platform.isTV && typeof TVMenuControl?.disableTVMenuKey === 'function') {
      try {
        TVMenuControl.disableTVMenuKey();
        this.hasEnabledMenuKey = false;
      } catch (error) {
        console.warn('[remote-control] Failed to disable TV menu key:', error);
      }
    }
  };

  // Public methods for controlling tvOS menu key handling
  // Used to let menu button minimize app when drawer is open
  setTvMenuKeyEnabled = (enabled: boolean): void => {
    if (enabled) {
      this.enableTvMenuKey();
    } else {
      this.disableTvMenuKey();
    }
  };

  private enableTvPanGesture = () => {
    if (this.hasEnabledPanGesture) {
      return;
    }

    if (Platform.OS === 'ios' && Platform.isTV && typeof TVEventControl?.enableTVPanGesture === 'function') {
      try {
        TVEventControl.enableTVPanGesture();
        this.hasEnabledPanGesture = true;
        console.log('[remote-control] TV pan gesture enabled for velocity-based navigation');
      } catch (error) {
        console.warn('[remote-control] Failed to enable TV pan gesture:', error);
      }
    }
  };

  private resetPanState = () => {
    this.panOrientation = undefined;
    this.panLastIndex = 0;
  };

  private getPanGridIndex = (x: number, y: number): { xIndex: number; yIndex: number } => {
    const gridElementSize = PAN_GRID_SIZE / PAN_GRID_COLUMNS;
    const xIndex = Math.floor((x + gridElementSize / 2) / gridElementSize);
    const yIndex = Math.floor((y + gridElementSize / 2) / gridElementSize);
    return { xIndex, yIndex };
  };

  private emitRepeatedKeys = (key: SupportedKeys, count: number) => {
    if (count <= 0) return;

    let remaining = count;
    const emitNext = () => {
      if (remaining <= 0) return;
      this.eventEmitter.emit('keyDown', key);
      remaining--;
      if (remaining > 0) {
        setTimeout(emitNext, PAN_EMIT_INTERVAL);
      }
    };
    emitNext();
  };

  private handlePanEvent = (event: HWEvent): void => {
    if (!event.body) return;

    // Throttle pan event processing
    if (this.panThrottleWait) return;
    this.panThrottleWait = true;
    setTimeout(() => {
      this.panThrottleWait = false;
    }, PAN_THROTTLE_DELAY);

    const { state, x, y } = event.body as { state?: string; x?: number; y?: number };

    if (state === 'Began') {
      this.resetPanState();
      return;
    }

    if (state === 'Changed' && typeof x === 'number' && typeof y === 'number') {
      const { xIndex, yIndex } = this.getPanGridIndex(x, y);

      // Lock orientation after first significant movement
      if (!this.panOrientation) {
        if (xIndex !== this.panLastIndex) {
          this.panOrientation = 'x';
        } else if (yIndex !== this.panLastIndex) {
          this.panOrientation = 'y';
        } else {
          return; // No significant movement yet
        }
      }

      // Calculate movement and emit appropriate keys
      if (this.panOrientation === 'x') {
        const diff = xIndex - this.panLastIndex;
        if (diff !== 0) {
          const key = diff > 0 ? SupportedKeys.Right : SupportedKeys.Left;
          this.emitRepeatedKeys(key, Math.abs(diff));
          this.panLastIndex = xIndex;
        }
      } else if (this.panOrientation === 'y') {
        const diff = yIndex - this.panLastIndex;
        if (diff !== 0) {
          const key = diff > 0 ? SupportedKeys.Down : SupportedKeys.Up;
          this.emitRepeatedKeys(key, Math.abs(diff));
          this.panLastIndex = yIndex;
        }
      }
    }

    if (state === 'Ended' || state === 'Cancelled') {
      this.resetPanState();
    }
  };

  private handleTvEvent = (event: HWEvent): void => {
    if (!event || typeof event.eventType !== 'string') {
      return;
    }

    // Handle pan events for tvOS velocity-based navigation
    if (event.eventType === 'pan') {
      this.handlePanEvent(event);
      return;
    }

    // Ignore blur events without key action
    if (event.eventType === 'blur' && event.eventKeyAction === -1) {
      return;
    }

    // Special handling for longSelect - use eventKeyAction directly for dedup
    if (event.eventType === 'longSelect') {
      const action = event.eventKeyAction;
      // action === 0 is key down, action === 1 is key up
      if (action === 0) {
        // Key down - only emit if not already pressed
        if (!this.longSelectPressed) {
          this.longSelectPressed = true;
          this.eventEmitter.emit('keyDown', SupportedKeys.LongEnter);
        }
      } else if (action === 1) {
        // Key up - reset the pressed state
        this.longSelectPressed = false;
      }
      return;
    }

    const isKeyDown = isKeyDownAction(event.eventKeyAction);
    if (!isKeyDown) {
      return;
    }

    const mappedKey = TV_EVENT_KEY_MAPPING[event.eventType];
    // Ignore focus events without a mapped key
    if (!mappedKey && event.eventType === 'focus' && event.eventKeyAction === -1) {
      return;
    }

    // Debug logging for PlayPause events
    if (event.eventType === 'playPause' || mappedKey === SupportedKeys.PlayPause) {
      console.log('[RemoteControl] PlayPause event received:', { eventType: event.eventType, eventKeyAction: event.eventKeyAction, mappedKey, isKeyDown });
    }

    if (mappedKey) {
      const willEmit = this.shouldEmit(mappedKey);
      // Debug logging for PlayPause events
      if (mappedKey === SupportedKeys.PlayPause) {
        console.log('[RemoteControl] PlayPause shouldEmit:', willEmit, { lastEmittedKey: this.lastEmittedKey, lastEmittedAt: this.lastEmittedAt });
      }
      if (willEmit) {
        if (!this.interceptIfNeeded(mappedKey)) {
          this.eventEmitter.emit('keyDown', mappedKey);
        }
      }
    }
  };

  private handleHardwareBackPress = (): boolean => {
    if (this.shouldEmit(SupportedKeys.Back)) {
      const handled = this.interceptIfNeeded(SupportedKeys.Back);
      if (handled) {
        // An interceptor handled the back press
        return true;
      }
      // No interceptor handled it - return false to let system minimize the app
      return false;
    }
    return false;
  };

  private isDirectionalKey = (key: SupportedKeys): boolean => {
    return key === SupportedKeys.Up || key === SupportedKeys.Down ||
           key === SupportedKeys.Left || key === SupportedKeys.Right;
  };

  private shouldEmit = (key: SupportedKeys): boolean => {
    const now = Date.now();

    // Skip deduplication for Back button to ensure all back presses are handled
    if (key === SupportedKeys.Back) {
      this.accelKey = undefined; // Reset acceleration on non-directional key
      this.lastEmittedKey = key;
      this.lastEmittedAt = now;
      return true;
    }

    // For directional keys, implement acceleration when held
    if (this.isDirectionalKey(key)) {
      const timeSinceLastEmit = now - this.lastEmittedAt;
      const isSameKey = this.lastEmittedKey === key;
      const isHeld = isSameKey && timeSinceLastEmit < 200; // Consider "held" if within 200ms

      if (isHeld && this.accelKey === key) {
        // Key is being held - check dedup
        if (timeSinceLastEmit < ACCEL_BASE_INTERVAL) {
          return false;
        }

        const holdDuration = now - this.accelStartTime;

        if (holdDuration < ACCEL_START_DELAY) {
          // Before acceleration kicks in, emit single event
          this.lastEmittedKey = key;
          this.lastEmittedAt = now;
          return true;
        } else {
          // Acceleration active - emit multiple events
          this.accelEventCount++;
          const progress = Math.min(this.accelEventCount / ACCEL_RAMP_EVENTS, 1);
          const multiplier = Math.round(1 + progress * (ACCEL_MAX_MULTIPLIER - 1));

          // Emit extra events (multiplier - 1 extra, since we return true for the first one)
          for (let i = 1; i < multiplier; i++) {
            setTimeout(() => {
              this.eventEmitter.emit('keyDown', key);
            }, i * 10); // Small stagger to allow focus updates
          }

          this.lastEmittedKey = key;
          this.lastEmittedAt = now;
          return true;
        }
      } else {
        // New key or key changed - reset acceleration
        this.accelKey = key;
        this.accelStartTime = now;
        this.accelEventCount = 0;

        // Apply normal dedup for first press
        if (isSameKey && timeSinceLastEmit < 50) {
          return false;
        }
      }

      this.lastEmittedKey = key;
      this.lastEmittedAt = now;
      return true;
    }

    // Non-directional, non-back keys: reset acceleration and use normal dedup
    this.accelKey = undefined;
    const dedupWindow = 50;

    if (this.lastEmittedKey === key && now - this.lastEmittedAt < dedupWindow) {
      return false;
    }

    this.lastEmittedKey = key;
    this.lastEmittedAt = now;
    return true;
  };

  addKeydownListener = (listener: (event: SupportedKeys) => void): (() => void) => {
    this.eventEmitter.on('keyDown', listener);
    return () => this.removeKeydownListener(listener);
  };

  removeKeydownListener = (listener: (event: SupportedKeys) => void): void => {
    this.eventEmitter.off('keyDown', listener);
  };

  emitKeyDown = (key: SupportedKeys): void => {
    if (this.shouldEmit(key)) {
      if (!this.interceptIfNeeded(key)) {
        this.eventEmitter.emit('keyDown', key);
      }
    }
  };

  pushBackInterceptor = (interceptor: BackInterceptor): (() => void) => {
    this.backInterceptors.push(interceptor);
    return () => this.removeBackInterceptor(interceptor);
  };

  removeBackInterceptor = (interceptor: BackInterceptor): void => {
    this.backInterceptors = this.backInterceptors.filter((fn) => fn !== interceptor);
  };

  private interceptIfNeeded = (key: SupportedKeys): boolean => {
    if (key !== SupportedKeys.Back) {
      return false;
    }

    // Call the most recently added interceptor first
    for (let i = this.backInterceptors.length - 1; i >= 0; i -= 1) {
      try {
        const handled = this.backInterceptors[i]?.();
        if (handled) {
          return true;
        }
      } catch (error) {
        console.warn('[remote-control] Back interceptor threw', error);
      }
    }
    return false;
  };

  disableTvEventHandling = (): void => {
    if (this.tvEventSubscription) {
      this.tvEventSubscription.remove();
      this.tvEventSubscription = undefined;
    }
    if (this.androidEventSubscription) {
      this.androidEventSubscription.remove();
      this.androidEventSubscription = undefined;
    }
  };

  enableTvEventHandling = (): void => {
    if (isTvEnvironment() && !this.tvEventSubscription && !this.androidEventSubscription) {
      if (Platform.OS === 'android') {
        this.androidEventSubscription = DeviceEventEmitter.addListener('onHWKeyEvent', this.handleTvEvent);
      } else {
        this.tvEventSubscription = TVEventHandler.addListener(this.handleTvEvent);
      }
    }
  };

  cleanup = (): void => {
    this.tvEventSubscription?.remove();
    this.tvEventSubscription = undefined;

    this.backHandlerSubscription?.remove?.();
    this.backHandlerSubscription = undefined;

    this.androidEventSubscription?.remove?.();
    this.androidEventSubscription = undefined;

    if (this.webListener && typeof window !== 'undefined') {
      window.removeEventListener('keydown', this.webListener);
      this.webListener = undefined;
    }
  };
}

// Helper function to map TV event types to supported keys (for debugging)
export const getTvEventMappedKey = (eventType: string): string | undefined => {
  return TV_EVENT_KEY_MAPPING[eventType];
};

export default new RemoteControlManager();
