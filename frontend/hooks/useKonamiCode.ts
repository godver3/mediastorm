import { useCallback, useRef, useState } from 'react';
import { type GestureResponderEvent } from 'react-native';

// Konami Code: Up, Up, Down, Down, Left, Right, Left, Right, B, A
// We'll detect swipes for directions and taps for B and A
// The sequence is: up, up, down, down, left, right, left, right, then two taps (B, A)
export type Direction = 'up' | 'down' | 'left' | 'right' | 'tap';

export const KONAMI_SEQUENCE: Direction[] = [
  'up',
  'up',
  'down',
  'down',
  'left',
  'right',
  'left',
  'right',
  'tap', // B
  'tap', // A
];

const SWIPE_THRESHOLD = 30; // Minimum distance to register a swipe
const TAP_THRESHOLD = 10; // Maximum movement to register as a tap
const SEQUENCE_TIMEOUT = 5000; // Reset sequence after 5 seconds of inactivity

interface TouchState {
  startX: number;
  startY: number;
  startTime: number;
}

export interface KonamiDebugInfo {
  lastInput: Direction | null;
  currentIndex: number;
  expectedNext: Direction;
  lastDelta: { x: number; y: number } | null;
}

export function useKonamiCode(onActivate: () => void, debug = false) {
  const sequenceIndex = useRef(0);
  const lastInputTime = useRef(0);
  const touchState = useRef<TouchState | null>(null);

  // Debug state
  const [debugInfo, setDebugInfo] = useState<KonamiDebugInfo>({
    lastInput: null,
    currentIndex: 0,
    expectedNext: KONAMI_SEQUENCE[0],
    lastDelta: null,
  });

  const updateDebug = useCallback(
    (input: Direction | null, index: number, delta: { x: number; y: number } | null) => {
      if (debug) {
        setDebugInfo({
          lastInput: input,
          currentIndex: index,
          expectedNext: KONAMI_SEQUENCE[index] || KONAMI_SEQUENCE[0],
          lastDelta: delta,
        });
      }
    },
    [debug],
  );

  const resetSequence = useCallback(() => {
    sequenceIndex.current = 0;
    updateDebug(null, 0, null);
  }, [updateDebug]);

  const checkInput = useCallback(
    (input: Direction, delta: { x: number; y: number } | null) => {
      const now = Date.now();

      // Reset if too much time has passed
      if (now - lastInputTime.current > SEQUENCE_TIMEOUT) {
        resetSequence();
      }

      lastInputTime.current = now;

      // Check if input matches expected
      if (KONAMI_SEQUENCE[sequenceIndex.current] === input) {
        sequenceIndex.current++;
        updateDebug(input, sequenceIndex.current, delta);

        // Check if sequence is complete
        if (sequenceIndex.current === KONAMI_SEQUENCE.length) {
          resetSequence();
          onActivate();
        }
      } else {
        // Wrong input - check if it could be the start of a new sequence
        if (input === KONAMI_SEQUENCE[0]) {
          sequenceIndex.current = 1;
          updateDebug(input, 1, delta);
        } else {
          updateDebug(input, 0, delta);
          resetSequence();
        }
      }
    },
    [onActivate, resetSequence, updateDebug],
  );

  const onTouchStart = useCallback((event: GestureResponderEvent) => {
    const { pageX, pageY } = event.nativeEvent;
    touchState.current = {
      startX: pageX,
      startY: pageY,
      startTime: Date.now(),
    };
  }, []);

  const onTouchEnd = useCallback(
    (event: GestureResponderEvent) => {
      if (!touchState.current) return;

      const { pageX, pageY } = event.nativeEvent;
      const { startX, startY } = touchState.current;

      const deltaX = pageX - startX;
      const deltaY = pageY - startY;
      const absX = Math.abs(deltaX);
      const absY = Math.abs(deltaY);
      const delta = { x: deltaX, y: deltaY };

      let input: Direction | null = null;

      // Determine if it's a swipe or tap
      if (absX < TAP_THRESHOLD && absY < TAP_THRESHOLD) {
        input = 'tap';
      } else if (absX > absY && absX > SWIPE_THRESHOLD) {
        input = deltaX > 0 ? 'right' : 'left';
      } else if (absY > absX && absY > SWIPE_THRESHOLD) {
        input = deltaY > 0 ? 'down' : 'up';
      }

      if (input) {
        checkInput(input, delta);
      } else if (debug) {
        // Show delta even if no input detected
        setDebugInfo((prev) => ({ ...prev, lastDelta: delta, lastInput: null }));
      }

      touchState.current = null;
    },
    [checkInput, debug],
  );

  return {
    onTouchStart,
    onTouchEnd,
    handleInput: checkInput,
    resetSequence,
    debugInfo,
  };
}
