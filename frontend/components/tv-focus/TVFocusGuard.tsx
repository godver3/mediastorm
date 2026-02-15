import React from 'react';
import { Platform, View, TVFocusGuideView } from 'react-native';
import type { StyleProp, ViewStyle, FocusDestination, FocusGuideMethods } from 'react-native';

type Direction = 'up' | 'down' | 'left' | 'right';

export interface TVFocusGuardProps {
  children: React.ReactNode;
  /** Directions to trap — prevents focus from leaving in these directions */
  trapFocus?: Direction[];
  /** Specific views to redirect focus to when entering this region */
  destinations?: FocusDestination[];
  /** Auto-focus first focusable child, remember last focused on re-entry */
  autoFocus?: boolean;
  /** Toggle the guard on/off */
  enabled?: boolean;
  style?: StyleProp<ViewStyle>;
}

export interface TVFocusGuardRef extends FocusGuideMethods {}

/**
 * Declarative focus trapping component.
 *
 * On TV: renders TVFocusGuideView with trapFocus* boolean props.
 * On non-TV: renders a plain View (no-op).
 */
export const TVFocusGuard = React.forwardRef<TVFocusGuardRef, TVFocusGuardProps>(
  function TVFocusGuard(
    { children, trapFocus, destinations, autoFocus, enabled = true, style },
    ref,
  ) {
    if (!Platform.isTV) {
      return <View style={style}>{children}</View>;
    }

    const has = (dir: Direction) => trapFocus?.includes(dir) ?? false;

    return (
      <TVFocusGuideView
        ref={ref as React.Ref<View & FocusGuideMethods>}
        style={style}
        enabled={enabled}
        destinations={destinations}
        autoFocus={autoFocus}
        trapFocusUp={has('up')}
        trapFocusDown={has('down')}
        trapFocusLeft={has('left')}
        trapFocusRight={has('right')}
      >
        {children}
      </TVFocusGuideView>
    );
  },
);
