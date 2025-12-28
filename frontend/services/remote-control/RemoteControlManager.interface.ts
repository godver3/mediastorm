import { SupportedKeys } from './SupportedKeys';

// Define a type for the listener function
export type KeydownListener = (event: SupportedKeys) => void;

// Interceptor invoked for high-priority Back handling. Return true to consume the event.
export type BackInterceptor = () => boolean | void;

export interface RemoteControlManagerInterface {
  addKeydownListener(listener: KeydownListener): () => void;
  removeKeydownListener(listener: KeydownListener): void;
  emitKeyDown(key: SupportedKeys): void;

  // Back interception helpers (LIFO priority). When any interceptor returns true, the Back event
  // will NOT be propagated to normal listeners.
  pushBackInterceptor(interceptor: BackInterceptor): () => void;
  removeBackInterceptor(interceptor: BackInterceptor): void;

  // Temporarily disable/enable TV event handling to allow spatial navigation to handle events
  disableTvEventHandling(): void;
  enableTvEventHandling(): void;

  // Control tvOS menu key handling - disable to let menu button minimize app
  setTvMenuKeyEnabled(enabled: boolean): void;
}
