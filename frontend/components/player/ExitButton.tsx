import React, { useMemo } from 'react';
import { Platform, Pressable, StyleSheet, Text } from 'react-native';
import FocusablePressable from '@/components/FocusablePressable';
import { useTheme } from '@/theme';
import type { NovaTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';

interface GoBackButtonProps {
  onSelect: () => void;
  onFocus?: () => void;
  disabled?: boolean;
}

const isAndroidTV = Platform.isTV && Platform.OS === 'android';

const ExitButton: React.FC<GoBackButtonProps> = ({ onSelect, onFocus, disabled }) => {
  const theme = useTheme();
  const styles = useMemo(() => useExitButtonStyles(theme), [theme]);

  // Android TV: Use native Pressable with tvScale sizing
  if (isAndroidTV) {
    return (
      <Pressable
        onPress={onSelect}
        onFocus={onFocus}
        disabled={disabled}
        focusable={disabled ? false : undefined}
        android_disableSound
        style={({ focused }) => [styles.exitBtn, styles.androidTvButton, focused && styles.androidTvButtonFocused]}>
        {({ focused }) => (
          <Text style={[styles.androidTvText, focused && styles.androidTvTextFocused]}>Exit</Text>
        )}
      </Pressable>
    );
  }

  // tvOS and mobile: Use FocusablePressable
  return (
    <FocusablePressable
      text={'Exit'}
      focusKey="exit-button"
      onSelect={onSelect}
      onFocus={onFocus}
      disabled={disabled}
      style={styles.exitBtn}
    />
  );
};

const useExitButtonStyles = (theme: NovaTheme) => {
  return StyleSheet.create({
    exitBtn: {
      position: 'absolute',
      top: theme.spacing.lg,
      left: theme.spacing.lg,
    },
    androidTvButton: {
      paddingVertical: tvScale(14, 8),
      paddingHorizontal: tvScale(24, 16),
      backgroundColor: theme.colors.overlay.button,
      borderRadius: tvScale(theme.radius.md * 1.375, theme.radius.md),
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
    },
    androidTvButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    androidTvText: {
      fontSize: tvScale(20, 14),
      lineHeight: tvScale(28, 20),
      fontWeight: '500',
      color: theme.colors.text.primary,
    },
    androidTvTextFocused: {
      color: theme.colors.text.inverse,
    },
  });
};

export default ExitButton;
