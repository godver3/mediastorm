import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { tvScale, isTV, getTVScaleMultiplier } from '@/theme/tokens/tvScale';
import { Ionicons } from '@expo/vector-icons';
import { forwardRef, memo, useMemo } from 'react';
import {
  ActivityIndicator,
  Platform,
  Pressable,
  PressableProps,
  StyleProp,
  StyleSheet,
  Text,
  View,
  ViewStyle,
  TextStyle,
} from 'react-native';
import { SpatialNavigationFocusableView } from '@/services/tv-navigation';

// Unified TV scaling - tvOS is the baseline, Android TV auto-derives
// tvOS icon scale is 1.375, mobile is 1.0
const TV_SCALE = getTVScaleMultiplier();

interface CustomPressableProps extends PressableProps {
  text?: string;
  icon?: keyof typeof Ionicons.glyphMap;
  iconSize?: number;
  /** Add invisible icon spacer to match height of buttons with icons */
  invisibleIcon?: boolean;
  onSelect: () => void;
  onFocus?: () => void;
  /** Long press handler (mobile only, TV uses remote buttons) */
  onLongPress?: () => void;
  style?: StyleProp<ViewStyle>;
  focusedStyle?: StyleProp<ViewStyle>;
  textStyle?: StyleProp<TextStyle>;
  focusedTextStyle?: StyleProp<TextStyle>;
  disabled?: boolean;
  /** @deprecated No longer used - native focus handles focus keys automatically */
  focusKey?: string;
  /** Set to true to give this button initial focus on TV */
  autoFocus?: boolean;
  loading?: boolean;
  /** Show a small indicator pip in the top-right corner (e.g., for prequeue ready) */
  showReadyPip?: boolean;
  /** Badge text to show in the bottom-right corner (e.g., "Unreleased") */
  badge?: string;
  /** Style applied to the outer wrapper View (use to override alignSelf for centering) */
  wrapperStyle?: StyleProp<ViewStyle>;
  /** Native tag to focus when pressing Right (Android TV focus trapping) */
  nextFocusRight?: number;
  /** Native tag to focus when pressing Left (Android TV focus trapping) */
  nextFocusLeft?: number;
  /** Native tag to focus when pressing Up (Android TV focus trapping) */
  nextFocusUp?: number;
  /** Native tag to focus when pressing Down (Android TV focus trapping) */
  nextFocusDown?: number;
  /** Visual variant: 'primary' shows accent border unfocused / accent fill focused,
   *  'secondary' (default) shows subtle background unfocused / accent fill focused */
  variant?: 'primary' | 'secondary';
}

const FocusablePressable = forwardRef<View, CustomPressableProps>(
  (
    {
      text,
      icon,
      iconSize = 24,
      invisibleIcon = false,
      onSelect,
      onFocus,
      style,
      focusedStyle,
      textStyle,
      focusedTextStyle,
      disabled,
      focusKey: _focusKey, // deprecated, ignored
      autoFocus = false,
      loading = false,
      showReadyPip = false,
      badge,
      wrapperStyle,
      nextFocusRight,
      nextFocusLeft,
      nextFocusUp,
      variant = 'secondary',
      nextFocusDown,
      ...props
    },
    ref,
  ) => {
    const { onPress: _ignoredOnPress, ...restProps } = props;
    void _ignoredOnPress;
    void _focusKey;
    const theme = useTheme();
    const styles = useMemo(() => createStyles(theme, !!icon || invisibleIcon, variant), [theme, icon, invisibleIcon, variant]);

    // Scale icon size for TV platforms
    // Design for tvOS (1.375x), Android TV auto-derives via tvScale
    const scaledIconSize = tvScale(iconSize * 1.375, iconSize);

    // Native Pressable with focus styling - unified for tvOS and Android TV
    if (Platform.isTV) {
      return (
        <View style={[{ position: 'relative', alignSelf: 'flex-start', overflow: 'visible' }, wrapperStyle]}>
          <SpatialNavigationFocusableView
            focusKey={_focusKey}
            onSelect={onSelect}
            onFocus={onFocus}
            disabled={disabled || loading}
            testID={props.testID}>
            {({ isFocused }) => {
              const showBoth = icon && text;

              return (
                <View
                  style={[
                    styles.watchButton,
                    style,
                    isFocused && !disabled && styles.watchButtonFocused,
                    isFocused && !disabled && focusedStyle,
                    disabled && !loading && styles.watchButtonDisabled,
                  ]}>
                  <View style={{ position: 'relative' }}>
                    <View
                      style={[
                        showBoth ? { flexDirection: 'row', alignItems: 'center', gap: theme.spacing.sm } : undefined,
                        invisibleIcon && !icon && { minHeight: scaledIconSize, justifyContent: 'center' },
                      ]}>
                      {icon && !loading ? (
                        <Ionicons
                          name={icon}
                          size={scaledIconSize}
                          color={isFocused && !disabled ? theme.colors.text.inverse : theme.colors.text.primary}
                        />
                      ) : !loading && !icon && text ? null : icon ? (
                        <View style={{ width: scaledIconSize, height: scaledIconSize }} />
                      ) : null}
                      {text && (
                        <Text
                          numberOfLines={1}
                          style={[
                            isFocused && !disabled ? styles.watchButtonTextFocused : styles.watchButtonText,
                            isFocused && !disabled ? focusedTextStyle : textStyle,
                            loading && { opacity: 0 },
                          ]}>
                          {text}
                        </Text>
                      )}
                    </View>
                    {loading && (
                      <View
                        style={{
                          position: 'absolute',
                          top: 0,
                          left: 0,
                          right: 0,
                          bottom: 0,
                          justifyContent: 'center',
                          alignItems: 'center',
                        }}>
                        <ActivityIndicator
                          size="small"
                          color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary}
                        />
                      </View>
                    )}
                  </View>
                </View>
              );
            }}
          </SpatialNavigationFocusableView>
          {showReadyPip && !loading && (
            <View
              style={{
                position: 'absolute',
                top: -3,
                right: -3,
                width: 10,
                height: 10,
                borderRadius: 5,
                backgroundColor: theme.colors.status.success,
                zIndex: 10,
              }}
              pointerEvents="none"
            />
          )}
          {badge && !loading && (
            <View
              style={{
                position: 'absolute',
                top: -4,
                right: -4,
                padding: 2,
                borderRadius: 8,
                backgroundColor: theme.colors.status.warning,
                zIndex: 10,
              }}
              pointerEvents="none">
              <Ionicons name="time" size={10} color="#000000" />
            </View>
          )}
        </View>
      );
    }

    return (
      <View style={[{ position: 'relative', alignSelf: 'flex-start', overflow: 'visible' }, wrapperStyle]}>
        <Pressable
          ref={ref}
          {...restProps}
          disabled={disabled || loading}
          focusable={disabled ? false : undefined}
          onPress={onSelect}
          onFocus={onFocus}
          android_disableSound
          hasTVPreferredFocus={autoFocus}
          tvParallaxProperties={{ enabled: false }}
          renderToHardwareTextureAndroid={Platform.isTV && Platform.OS === 'android'}
          nextFocusRight={nextFocusRight}
          nextFocusLeft={nextFocusLeft}
          nextFocusUp={nextFocusUp}
          nextFocusDown={nextFocusDown}
          style={({ focused }) => [
            styles.watchButton,
            style,
            focused && styles.watchButtonFocused,
            focused && focusedStyle,
            disabled && !loading && styles.watchButtonDisabled,
          ]}>
          {({ focused }) => {
            // Show both icon and text if both are provided
            const showBoth = icon && text;

            return (
              <View style={{ position: 'relative' }}>
                <View
                  style={[
                    showBoth ? { flexDirection: 'row', alignItems: 'center', gap: theme.spacing.sm } : undefined,
                    invisibleIcon && !icon && { minHeight: scaledIconSize, justifyContent: 'center' },
                  ]}>
                  {icon && !loading ? (
                    <Ionicons
                      name={icon}
                      size={scaledIconSize}
                      color={focused ? theme.colors.text.inverse : theme.colors.text.primary}
                    />
                  ) : !loading && !icon && text ? null : icon ? (
                    <View style={{ width: scaledIconSize, height: scaledIconSize }} />
                  ) : null}
                  {text && (
                    <Text
                      numberOfLines={1}
                      style={[
                        focused ? styles.watchButtonTextFocused : styles.watchButtonText,
                        focused ? focusedTextStyle : textStyle,
                        loading && { opacity: 0 },
                      ]}>
                      {text}
                    </Text>
                  )}
                </View>
                {loading && (
                  <View
                    style={{
                      position: 'absolute',
                      top: 0,
                      left: 0,
                      right: 0,
                      bottom: 0,
                      justifyContent: 'center',
                      alignItems: 'center',
                    }}>
                    <ActivityIndicator
                      size="small"
                      color={focused ? theme.colors.text.inverse : theme.colors.text.primary}
                    />
                  </View>
                )}
              </View>
            );
          }}
        </Pressable>
        {showReadyPip && !loading && (
          <View
            style={{
              position: 'absolute',
              top: -3,
              right: -3,
              width: 10,
              height: 10,
              borderRadius: 5,
              backgroundColor: theme.colors.status.success,
              zIndex: 10,
            }}
            pointerEvents="none"
          />
        )}
        {badge && !loading && (
          <View
            style={{
              position: 'absolute',
              top: -4,
              right: -4,
              padding: 2,
              borderRadius: 8,
              backgroundColor: theme.colors.status.warning,
              zIndex: 10,
            }}
            pointerEvents="none">
            <Ionicons name="time" size={10} color="#000000" />
          </View>
        )}
      </View>
    );
  },
);

const createStyles = (theme: NovaTheme, hasIcon: boolean, variant: 'primary' | 'secondary' = 'secondary') => {
  // Unified TV scaling: tvOS buttons 1.375x larger, Android TV auto-scales from tvOS
  // tvOS: 1.375, Android TV: 1.375 * 0.55 ≈ 0.76, Mobile: 1
  const scale = isTV ? 1.375 * TV_SCALE : 1;
  const basePaddingVertical = hasIcon ? theme.spacing.sm : theme.spacing.md;
  const basePaddingHorizontal = hasIcon ? theme.spacing.sm : theme.spacing.lg;
  const isPrimary = variant === 'primary';

  return StyleSheet.create({
    watchButton: {
      backgroundColor: isPrimary ? 'transparent' : theme.colors.overlay.button,
      paddingVertical: basePaddingVertical * scale,
      paddingHorizontal: basePaddingHorizontal * scale,
      borderRadius: theme.radius.md * scale,
      alignItems: 'center',
      alignSelf: 'flex-start',
      borderWidth: isPrimary ? 2 * (isTV ? scale : 1) : StyleSheet.hairlineWidth,
      borderColor: isPrimary ? theme.colors.accent.primary : theme.colors.border.subtle,
    },
    watchButtonFocused: {
      backgroundColor: theme.colors.accent.primary,
      borderColor: theme.colors.accent.primary,
    },
    watchButtonText: {
      ...theme.typography.label.md,
      color: theme.colors.text.primary,
      ...(scale !== 1
        ? {
            fontSize: theme.typography.label.md.fontSize * scale,
            lineHeight: theme.typography.label.md.lineHeight * scale,
          }
        : {}),
    },
    watchButtonTextFocused: {
      ...theme.typography.label.md,
      color: theme.colors.text.inverse,
      ...(scale !== 1
        ? {
            fontSize: theme.typography.label.md.fontSize * scale,
            lineHeight: theme.typography.label.md.lineHeight * scale,
          }
        : {}),
    },
    watchButtonDisabled: {
      opacity: 0.6,
    },
  });
};

// Memoize to prevent re-renders when parent re-renders but props haven't changed
// Critical for Android TV performance in the player controls
export default memo(FocusablePressable);
