import { SpatialNavigationFocusableView } from '@/services/tv-navigation';
import type { NovaTheme } from '@/theme';
import { useTheme } from '@/theme';
import { tvScale, isTV, isAndroidTV, getTVScaleMultiplier } from '@/theme/tokens/tvScale';
import { Ionicons } from '@expo/vector-icons';
import { useMemo, useRef } from 'react';
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
  style?: StyleProp<ViewStyle>;
  focusedStyle?: StyleProp<ViewStyle>;
  textStyle?: StyleProp<TextStyle>;
  focusedTextStyle?: StyleProp<TextStyle>;
  disabled?: boolean;
  focusKey?: string;
  loading?: boolean;
  /** Show a small indicator pip in the top-right corner (e.g., for prequeue ready) */
  showReadyPip?: boolean;
}

const FocusablePressable = ({
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
  focusKey,
  loading = false,
  showReadyPip = false,
  ...props
}: CustomPressableProps) => {
  const { onPress: _ignoredOnPress, ...restProps } = props;
  void _ignoredOnPress;
  const theme = useTheme();
  const styles = useMemo(() => createStyles(theme, !!icon || invisibleIcon), [theme, icon, invisibleIcon]);

  // Scale icon size for TV platforms
  // Design for tvOS (1.375x), Android TV auto-derives via tvScale
  const scaledIconSize = tvScale(iconSize * 1.375, iconSize);

  // ActivityIndicator "small" is about 20px, scale it to match iconSize
  const spinnerScale = scaledIconSize / 20;

  // Track when onSelect was last handled to prevent double-triggering on TV
  // (both SpatialNavigationFocusableView and Pressable can fire for the same event)
  const lastSelectTimeRef = useRef(0);
  const SELECT_DEBOUNCE_MS = 100;

  // Wrap in a View to position the pip outside the spatial navigation wrapper
  // This prevents clipping on Android TV where the library's internal View clips overflow
  const wrapper = (
    <SpatialNavigationFocusableView
      focusKey={focusKey}
      onSelect={() => {
        if (!disabled) {
          const now = Date.now();
          if (now - lastSelectTimeRef.current > SELECT_DEBOUNCE_MS) {
            lastSelectTimeRef.current = now;
            onSelect();
          }
        }
      }}
      onFocus={onFocus}>
      {({ isFocused }: { isFocused: boolean }) => {
        // Show both icon and text if both are provided
        const showBoth = icon && text;

        const content = (
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
                  color={isFocused ? theme.colors.text.inverse : theme.colors.text.primary}
                />
              ) : !loading && !icon && text ? null : icon ? (
                <View style={{ width: scaledIconSize, height: scaledIconSize }} />
              ) : null}
              {text && (
                <Text
                  style={[
                    isFocused ? styles.watchButtonTextFocused : styles.watchButtonText,
                    isFocused ? focusedTextStyle : textStyle,
                    loading && icon && { opacity: 0 },
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
        );

        const viewStyle = [
          styles.watchButton,
          style,
          isFocused && styles.watchButtonFocused,
          isFocused && focusedStyle,
          disabled && !loading && styles.watchButtonDisabled,
        ];

        // On TV, selection is handled by SpatialNavigationFocusableView's onSelect.
        // We disable the native tvOS parallax/wiggle effect but keep the element focusable
        // so that TVEventHandler can receive remote control events.
        return (
          <Pressable
            {...restProps}
            disabled={disabled || loading}
            style={viewStyle}
            onPress={!Platform.isTV ? onSelect : undefined}
            // Disable native tvOS parallax/motion effects - visual focus is managed by SpatialNavigationFocusableView
            tvParallaxProperties={{ enabled: false }}
            // Use hardware texture on Android TV to improve compositing over SurfaceView video layer
            renderToHardwareTextureAndroid={Platform.isTV && Platform.OS === 'android'}>
            {content}
          </Pressable>
        );
      }}
    </SpatialNavigationFocusableView>
  );

  // If showing the ready pip, wrap in a View so the pip can be positioned outside
  // the spatial navigation wrapper without being clipped
  if (showReadyPip && !loading) {
    return (
      <View style={{ position: 'relative', alignSelf: 'flex-start', overflow: 'visible' }}>
        {wrapper}
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
      </View>
    );
  }

  return wrapper;
};

const createStyles = (theme: NovaTheme, hasIcon: boolean) => {
  // Unified TV scaling - design for tvOS at 1.375x, Android TV at 1.71875x (25% larger)
  const scale = isTV ? (Platform.OS === 'android' ? 1.71875 : 1.375) * TV_SCALE : 1;
  const basePaddingVertical = hasIcon ? theme.spacing.sm : theme.spacing.md;
  const basePaddingHorizontal = hasIcon ? theme.spacing.sm : theme.spacing.lg;

  return StyleSheet.create({
    watchButton: {
      backgroundColor: theme.colors.overlay.button,
      paddingVertical: basePaddingVertical * scale,
      paddingHorizontal: basePaddingHorizontal * scale,
      borderRadius: theme.radius.md * scale,
      alignItems: 'center',
      alignSelf: 'flex-start',
      borderWidth: StyleSheet.hairlineWidth,
      borderColor: theme.colors.border.subtle,
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

export default FocusablePressable;
