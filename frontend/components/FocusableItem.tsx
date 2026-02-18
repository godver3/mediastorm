import { useFocusScrolling } from '@/hooks/useFocusScrolling';
import React, { forwardRef } from 'react';
import { Pressable, View, ViewProps } from 'react-native';

interface FocusableItemProps extends Omit<ViewProps, 'children'> {
  itemKey: string;
  scrollViewRef: React.RefObject<any>;
  onSelect?: () => void;
  onFocus?: () => void;
  /** Set to true to give this item initial focus on TV */
  autoFocus?: boolean;
  children: (props: { isFocused: boolean }) => React.ReactNode;
}

export const FocusableItem = forwardRef<any, FocusableItemProps>(
  ({ itemKey, scrollViewRef, onSelect, onFocus, autoFocus = false, children, style, ...props }, ref) => {
    const { createFocusHandler, createLayoutHandler } = useFocusScrolling({ scrollViewRef });

    const handleFocus = () => {
      createFocusHandler(itemKey)();
      onFocus?.();
    };

    return (
      <Pressable
        ref={ref}
        onPress={onSelect}
        onFocus={handleFocus}
        android_disableSound
        hasTVPreferredFocus={autoFocus}
        tvParallaxProperties={{ enabled: false }}>
        {({ focused }) => (
          <View style={style} onLayout={createLayoutHandler(itemKey)} {...props}>
            {children({ isFocused: focused })}
          </View>
        )}
      </Pressable>
    );
  },
);

FocusableItem.displayName = 'FocusableItem';
