import { useCallback, useEffect, useRef } from 'react';
import { Platform } from 'react-native';
import RemoteControlManager from '@/services/remote-control/RemoteControlManager';
import { SupportedKeys } from '@/services/remote-control/SupportedKeys';

type Direction = 'up' | 'down' | 'left' | 'right';

export interface TVFocusBoundaryOptions {
  /** Called when user presses a direction at a boundary */
  onBoundaryReached?: (direction: Direction) => void;
  /** Enable/disable the hook (defaults to Platform.isTV) */
  enabled?: boolean;
}

export interface TVFocusBoundaryResult {
  /** Report current position in a list — auto-sets left/right boundaries */
  reportFocusIndex: (index: number, total: number) => void;
  /** Manually set boundary directions (for non-list layouts) */
  setBoundaries: (boundaries: Direction[]) => void;
}

const KEY_TO_DIRECTION: Partial<Record<SupportedKeys, Direction>> = {
  [SupportedKeys.Left]: 'left',
  [SupportedKeys.Right]: 'right',
  [SupportedKeys.Up]: 'up',
  [SupportedKeys.Down]: 'down',
};

/**
 * Detects when the user presses a direction key at a boundary.
 *
 * Uses RemoteControlManager (which fires key events even when focus is
 * trapped by TVFocusGuideView) to detect boundary presses.
 */
export function useTVFocusBoundary({
  onBoundaryReached,
  enabled,
}: TVFocusBoundaryOptions = {}): TVFocusBoundaryResult {
  const isEnabled = enabled ?? Platform.isTV;
  const boundariesRef = useRef<Set<Direction>>(new Set());
  const callbackRef = useRef(onBoundaryReached);
  callbackRef.current = onBoundaryReached;

  useEffect(() => {
    if (!isEnabled) return;

    const unsubscribe = RemoteControlManager.addKeydownListener((key) => {
      const direction = KEY_TO_DIRECTION[key];
      if (direction && boundariesRef.current.has(direction)) {
        callbackRef.current?.(direction);
      }
    });

    return unsubscribe;
  }, [isEnabled]);

  const reportFocusIndex = useCallback((index: number, total: number) => {
    const next = new Set<Direction>();
    if (index === 0) next.add('left');
    if (index === total - 1) next.add('right');
    boundariesRef.current = next;
  }, []);

  const setBoundaries = useCallback((boundaries: Direction[]) => {
    boundariesRef.current = new Set(boundaries);
  }, []);

  return { reportFocusIndex, setBoundaries };
}
