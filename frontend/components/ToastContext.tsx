import { createContext, ReactNode, useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { Platform, StyleSheet, Text, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';

import { useShouldUseTabs } from '../hooks/useShouldUseTabs';
import { NovaTheme, useTheme } from '../theme';
import { isAndroidTV, tvScale } from '../theme/tokens/tvScale';
import { useLoadingScreen } from './LoadingScreenContext';

export type ToastTone = 'info' | 'success' | 'danger' | 'neutral';

export type ToastOptions = {
  tone?: ToastTone;
  duration?: number;
  id?: string;
};

type ToastRecord = {
  id: string;
  message: string;
  tone: ToastTone;
};

type ToastContextValue = {
  showToast: (message: string, options?: ToastOptions) => string;
  hideToast: (id: string) => void;
};

const ToastContext = createContext<ToastContextValue | null>(null);

export function useToast(): ToastContextValue {
  const context = useContext(ToastContext);
  if (!context) {
    throw new Error('useToast must be used within a ToastProvider.');
  }
  return context;
}

type ToastProviderProps = {
  children: ReactNode;
};

export function ToastProvider({ children }: ToastProviderProps) {
  const theme = useTheme();
  const insets = useSafeAreaInsets();
  const shouldUseTabs = useShouldUseTabs();
  const isTV = Platform.isTV;
  const { isLoadingScreenVisible } = useLoadingScreen();
  const styles = useMemo(
    () => createToastStyles(theme, insets, shouldUseTabs, isTV),
    [theme, insets, shouldUseTabs, isTV],
  );
  const [toasts, setToasts] = useState<ToastRecord[]>([]);
  const timersRef = useRef<Map<string, ReturnType<typeof setTimeout>>>(new Map());

  const hideToast = useCallback((id: string) => {
    setToasts((prev) => prev.filter((toast) => toast.id !== id));
    const timers = timersRef.current;
    const timer = timers.get(id);
    if (timer) {
      clearTimeout(timer);
      timers.delete(id);
    }
  }, []);

  const showToast = useCallback(
    (message: string, options?: ToastOptions) => {
      const trimmed = message?.trim();
      if (!trimmed) {
        return '';
      }

      const id = options?.id ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
      const tone = options?.tone ?? 'info';

      setToasts((prev) => {
        const next = [...prev];
        const existingIndex = next.findIndex((toast) => toast.id === id);
        const record: ToastRecord = { id, message: trimmed, tone };
        if (existingIndex >= 0) {
          next[existingIndex] = record;
        } else {
          next.push(record);
        }
        return next;
      });

      const duration = options?.duration ?? 4000;
      const timers = timersRef.current;
      const existingTimer = timers.get(id);
      if (existingTimer) {
        clearTimeout(existingTimer);
      }

      if (duration > 0) {
        const timeout = setTimeout(() => {
          hideToast(id);
        }, duration);
        timers.set(id, timeout);
      } else {
        timers.delete(id);
      }

      return id;
    },
    [hideToast],
  );

  useEffect(() => {
    return () => {
      const timers = timersRef.current;
      timers.forEach((timer) => clearTimeout(timer));
      timers.clear();
    };
  }, []);

  const value = useMemo<ToastContextValue>(
    () => ({
      showToast,
      hideToast,
    }),
    [hideToast, showToast],
  );

  return (
    <ToastContext.Provider value={value}>
      {children}
      {!isLoadingScreenVisible && (
        <View pointerEvents="box-none" style={styles.viewport} accessibilityLiveRegion="polite">
          {toasts.map((toast) => {
            const toneColor = getToneColor(theme, toast.tone);
            return (
              <View
                key={toast.id}
                style={[styles.toast, { borderColor: toneColor, shadowColor: toneColor }]}
                accessibilityRole="alert">
                <View style={[styles.indicator, { backgroundColor: toneColor }]} />
                <Text style={styles.message} numberOfLines={3}>
                  {toast.message}
                </Text>
              </View>
            );
          })}
        </View>
      )}
    </ToastContext.Provider>
  );
}

function getToneColor(theme: NovaTheme, tone: ToastTone) {
  switch (tone) {
    case 'success':
      return theme.colors.status.success;
    case 'danger':
      return theme.colors.status.danger;
    case 'info':
    default:
      return theme.colors.accent.primary;
  }
}

const createToastStyles = (
  theme: NovaTheme,
  insets: { bottom: number; top: number },
  shouldUseTabs: boolean,
  isTV: boolean,
) => {
  // For TV platforms, position at top with larger sizing
  // Use tvScale for proper Android TV vs tvOS scaling
  // Android TV gets 30% larger than default tvScale (0.55 * 1.3 = 0.715)
  if (isTV) {
    const baseFontSize = theme.typography.body.lg.fontSize || 16;
    const baseLineHeight = theme.typography.body.lg.lineHeight || 24;
    // Scale font size appropriately - tvOS gets 1.5x, Android TV gets 0.715x of tvOS
    const androidTvScale = 0.715; // 0.55 * 1.3 (30% larger than default)
    const fontSize = isAndroidTV
      ? Math.round(baseFontSize * 1.5 * androidTvScale)
      : tvScale(baseFontSize * 1.5, baseFontSize);
    const lineHeight = isAndroidTV
      ? Math.round(baseLineHeight * 1.5 * androidTvScale)
      : tvScale(baseLineHeight * 1.5, baseLineHeight);

    // Helper for Android TV 30% boost
    const atvScale = (tvosValue: number, mobileValue: number) =>
      isAndroidTV ? Math.round(tvosValue * androidTvScale) : tvScale(tvosValue, mobileValue);

    return StyleSheet.create({
      viewport: {
        position: 'absolute',
        top: atvScale(theme.spacing.xl * 3, theme.spacing.xl),
        left: atvScale(theme.spacing.xl * 3, theme.spacing.xl),
        right: atvScale(theme.spacing.xl * 3, theme.spacing.xl),
        gap: atvScale(theme.spacing.lg, theme.spacing.md),
        zIndex: 1000,
      },
      toast: {
        flexDirection: 'row',
        alignItems: 'center',
        borderWidth: isAndroidTV ? 2 : 3,
        borderRadius: atvScale(theme.radius.xl * 1.5, theme.radius.lg),
        backgroundColor: theme.colors.background.elevated,
        paddingVertical: atvScale(theme.spacing.xl, theme.spacing.md),
        paddingHorizontal: atvScale(theme.spacing.xl * 1.5, theme.spacing.lg),
        shadowOpacity: 0.4,
        shadowRadius: atvScale(24, 12),
        shadowOffset: { width: 0, height: atvScale(12, 6) },
        elevation: atvScale(12, 6),
        maxWidth: '100%',
      },
      indicator: {
        width: atvScale(theme.spacing.md, theme.spacing.sm),
        alignSelf: 'stretch',
        borderRadius: atvScale(theme.radius.lg, theme.radius.md),
        marginRight: atvScale(theme.spacing.xl, theme.spacing.md),
        flexShrink: 0,
      },
      message: {
        flex: 1,
        flexShrink: 1,
        color: theme.colors.text.primary,
        ...theme.typography.body.lg,
        fontSize,
        lineHeight,
      },
    });
  }

  // Mobile/tablet positioning at bottom
  // Tab bar height is 56px plus the safe area bottom inset
  const tabBarHeight = 56 + Math.max(insets.bottom, theme.spacing.md);
  // When tabs are visible, position toast above the tab bar with some spacing
  const bottomPosition = shouldUseTabs ? tabBarHeight + theme.spacing.sm : theme.spacing.lg;

  return StyleSheet.create({
    viewport: {
      position: 'absolute',
      bottom: bottomPosition,
      left: theme.spacing.lg,
      right: theme.spacing.lg,
      gap: theme.spacing.sm,
      zIndex: 1000,
    },
    toast: {
      flexDirection: 'row',
      alignItems: 'center',
      borderWidth: StyleSheet.hairlineWidth,
      borderRadius: theme.radius.lg,
      backgroundColor: theme.colors.background.elevated,
      paddingVertical: theme.spacing.sm,
      paddingHorizontal: theme.spacing.md,
      shadowOpacity: 0.35,
      shadowRadius: 12,
      shadowOffset: { width: 0, height: 6 },
      elevation: 6,
      maxWidth: '100%',
    },
    indicator: {
      width: theme.spacing.xs,
      alignSelf: 'stretch',
      borderRadius: theme.radius.sm,
      marginRight: theme.spacing.md,
      flexShrink: 0,
    },
    message: {
      flex: 1,
      flexShrink: 1,
      color: theme.colors.text.primary,
      ...theme.typography.body.md,
    },
  });
};
