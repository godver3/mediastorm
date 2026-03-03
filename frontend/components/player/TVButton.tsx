import React, { forwardRef, useMemo } from 'react';
import { StyleSheet, Text, View, type ViewStyle, type TextStyle, type StyleProp } from 'react-native';
import { SpatialNavigationNode } from '@/services/tv-navigation';
import type { SpatialNavigationNodeRef } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { Ionicons } from '@expo/vector-icons';
import { getTVScaleMultiplier } from '@/theme/tokens/tvScale';

const TV_SCALE = getTVScaleMultiplier();

interface TVButtonProps {
  icon?: keyof typeof Ionicons.glyphMap;
  iconSize?: number;
  text?: string;
  onSelect: () => void;
  onFocus?: () => void;
  disabled?: boolean;
  variant?: 'primary' | 'secondary';
  style?: StyleProp<ViewStyle>;
  textStyle?: StyleProp<TextStyle>;
}

/**
 * TV-only button that uses SpatialNavigationNode for focus management.
 * Unlike FocusablePressable (which uses native Pressable focus), this integrates
 * with react-tv-space-navigation so DefaultFocus and spatial nav focus work properly.
 */
const TVButton = forwardRef<SpatialNavigationNodeRef, TVButtonProps>(
  ({ icon, iconSize = 24, text, onSelect, onFocus, disabled = false, variant = 'secondary', style, textStyle }, ref) => {
    const theme = useTheme();
    const styles = useMemo(() => createTVButtonStyles(theme, !!icon, variant), [theme, icon, variant]);
    const scaledIconSize = iconSize * 1.375 * TV_SCALE;

    return (
      <SpatialNavigationNode
        isFocusable
        ref={ref}
        onSelect={disabled ? undefined : onSelect}
        onFocus={onFocus}
      >
        {({ isFocused }: { isFocused: boolean }) => (
          <View style={[styles.button, style, isFocused && styles.buttonFocused, disabled && styles.buttonDisabled]}>
            {text && icon ? (
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: theme.spacing.sm }}>
                <Ionicons
                  name={icon}
                  size={scaledIconSize}
                  color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary}
                />
                <Text numberOfLines={1} style={[isFocused ? styles.textFocused : styles.text, textStyle]}>
                  {text}
                </Text>
              </View>
            ) : (
              <>
                {icon && (
                  <Ionicons
                    name={icon}
                    size={scaledIconSize}
                    color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary}
                  />
                )}
                {text && (
                  <Text numberOfLines={1} style={[isFocused ? styles.textFocused : styles.text, textStyle]}>
                    {text}
                  </Text>
                )}
              </>
            )}
          </View>
        )}
      </SpatialNavigationNode>
    );
  },
);
TVButton.displayName = 'TVButton';

const createTVButtonStyles = (theme: NovaTheme, hasIcon: boolean, variant: 'primary' | 'secondary') => {
  const scale = 1.375 * TV_SCALE;
  const basePaddingVertical = hasIcon ? theme.spacing.sm : theme.spacing.md;
  const basePaddingHorizontal = hasIcon ? theme.spacing.sm : theme.spacing.lg;
  const isPrimary = variant === 'primary';

  return StyleSheet.create({
    button: {
      backgroundColor: isPrimary ? 'transparent' : theme.colors.overlay.button,
      paddingVertical: basePaddingVertical * scale,
      paddingHorizontal: basePaddingHorizontal * scale,
      borderRadius: theme.radius.md * scale,
      alignItems: 'center',
      alignSelf: 'flex-start',
      borderWidth: isPrimary ? 2 * scale : StyleSheet.hairlineWidth,
      borderColor: isPrimary ? theme.colors.accent.primary : theme.colors.border.subtle,
    },
    buttonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    buttonDisabled: {
      opacity: 0.6,
    },
    text: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      fontSize: theme.typography.label.md.fontSize * scale,
      lineHeight: theme.typography.label.md.lineHeight * scale,
    },
    textFocused: {
      ...theme.typography.label.md,
      color: theme.colors.text.inverse,
      fontSize: theme.typography.label.md.fontSize * scale,
      lineHeight: theme.typography.label.md.lineHeight * scale,
    },
  });
};

export default TVButton;
