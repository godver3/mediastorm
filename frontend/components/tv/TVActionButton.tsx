/**
 * TV Action Button - Button component using spatial navigation
 * Designed to work within SpatialNavigationNode hierarchy
 */

import React, { memo, useMemo } from 'react';
import { ActivityIndicator, Platform, StyleSheet, Text, View } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { tvScale } from '@/theme/tokens/tvScale';
import { SpatialNavigationFocusableView, DefaultFocus } from '@/services/tv-navigation';

interface TVActionButtonProps {
  text?: string;
  icon?: keyof typeof Ionicons.glyphMap;
  iconSize?: number;
  onSelect: () => void;
  onLongSelect?: () => void;
  onFocus?: () => void;
  disabled?: boolean;
  loading?: boolean;
  /** Show a small indicator pip in the top-right corner */
  showReadyPip?: boolean;
  /** Set to true to give this button initial focus */
  autoFocus?: boolean;
  /** Style variant */
  variant?: 'primary' | 'secondary';
  /** Badge text to show in the bottom-right corner (e.g., "Unreleased") */
  badge?: string;
}

const TVActionButton = memo(function TVActionButton({
  text,
  icon,
  iconSize = 24,
  onSelect,
  onLongSelect,
  onFocus,
  disabled = false,
  loading = false,
  showReadyPip = false,
  autoFocus = false,
  variant = 'secondary',
  badge,
}: TVActionButtonProps) {
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme, !!icon, variant), [theme, icon, variant]);

  const scaledIconSize = tvScale(iconSize * 1.375, iconSize);

  // Primary variant uses accent color when unfocused (outlined style)
  const getIconColor = (isFocused: boolean) => {
    if (isFocused) return theme.colors.text.inverse;
    return variant === 'primary' ? theme.colors.accent.primary : theme.colors.text.primary;
  };

  const buttonContent = (
    <SpatialNavigationFocusableView
      onSelect={disabled || loading ? undefined : () => {
        console.log(`[TVActionButton DEBUG] onSelect fired for button: ${text || icon}`);
        onSelect();
      }}
      onLongSelect={disabled || loading ? undefined : onLongSelect}
      onFocus={onFocus}>
      {({ isFocused }: { isFocused: boolean }) => (
        <View style={{ position: 'relative', alignSelf: 'flex-start', overflow: 'visible' }}>
          <View
            style={[styles.button, isFocused && styles.buttonFocused, disabled && !loading && styles.buttonDisabled]}>
            <View style={{ position: 'relative' }}>
              <View
                style={
                  icon && text ? { flexDirection: 'row', alignItems: 'center', gap: theme.spacing.sm } : undefined
                }>
                {icon && !loading ? (
                  <Ionicons
                    name={icon}
                    size={scaledIconSize}
                    color={getIconColor(isFocused)}
                  />
                ) : loading && icon ? (
                  <View style={{ width: scaledIconSize, height: scaledIconSize }} />
                ) : null}
                {text && (
                  <Text
                    numberOfLines={1}
                    style={[
                      isFocused ? styles.textFocused : variant === 'primary' ? styles.textPrimary : styles.text,
                      loading && { opacity: 0 },
                    ]}>
                    {text}
                  </Text>
                )}
              </View>
              {loading && (
                <View style={styles.loadingOverlay}>
                  <ActivityIndicator
                    size="small"
                    color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary}
                  />
                </View>
              )}
            </View>
          </View>
          {showReadyPip && !loading && <View style={[styles.pip, { backgroundColor: theme.colors.status.success }]} />}
          {badge && !loading && (
            <View style={[styles.badge, { backgroundColor: theme.colors.status.warning }]}>
              <Ionicons name="time" size={12 * tvScale(1.375, 1)} color="#000000" />
            </View>
          )}
        </View>
      )}
    </SpatialNavigationFocusableView>
  );

  if (autoFocus) {
    return <DefaultFocus>{buttonContent}</DefaultFocus>;
  }

  return buttonContent;
});

const createStyles = (theme: NovaTheme, hasIcon: boolean, variant: 'primary' | 'secondary') => {
  const scale = tvScale(1.375, 1);
  const basePaddingVertical = hasIcon ? theme.spacing.sm : theme.spacing.md;
  // Add horizontal padding for comfortable spacing between content and button edges
  const basePaddingHorizontal = hasIcon ? theme.spacing.md : theme.spacing.xl;

  return StyleSheet.create({
    button: {
      // Primary variant: outlined style when unfocused, filled when focused
      // Secondary variant: subtle background when unfocused, filled when focused
      backgroundColor: variant === 'primary' ? 'transparent' : theme.colors.overlay.button,
      paddingVertical: basePaddingVertical * scale,
      paddingHorizontal: basePaddingHorizontal * scale,
      borderRadius: theme.radius.md * scale,
      alignItems: 'center',
      alignSelf: 'flex-start',
      borderWidth: variant === 'primary' ? 2 * scale : StyleSheet.hairlineWidth,
      borderColor: variant === 'primary' ? theme.colors.accent.primary : theme.colors.border.subtle,
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
    textPrimary: {
      ...theme.typography.label.md,
      color: theme.colors.accent.primary,
      fontSize: theme.typography.label.md.fontSize * scale,
      lineHeight: theme.typography.label.md.lineHeight * scale,
    },
    textFocused: {
      ...theme.typography.label.md,
      color: theme.colors.text.inverse,
      fontSize: theme.typography.label.md.fontSize * scale,
      lineHeight: theme.typography.label.md.lineHeight * scale,
    },
    loadingOverlay: {
      position: 'absolute',
      top: 0,
      left: 0,
      right: 0,
      bottom: 0,
      justifyContent: 'center',
      alignItems: 'center',
    },
    pip: {
      position: 'absolute',
      top: -3,
      right: -3,
      width: 10,
      height: 10,
      borderRadius: 5,
      zIndex: 10,
    },
    badge: {
      position: 'absolute',
      top: -4 * scale,
      right: -4 * scale,
      padding: 2 * scale,
      borderRadius: 10 * scale,
      zIndex: 10,
    },
  });
};

export default TVActionButton;
