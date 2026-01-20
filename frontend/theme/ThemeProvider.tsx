import { createContext, ReactNode, useContext, useMemo } from 'react';
import { Platform, useWindowDimensions } from 'react-native';

import { Breakpoint, discoveryColumnMap, getBreakpoint, isTablet } from './tokens/breakpoints';
import { colorTokens, ColorTokens, getColorTokens, SupportedColorScheme } from './tokens/colors';
import { radiusTokens, RadiusTokens } from './tokens/radius';
import { getSpacingForTV, spacingTokens, SpacingTokens, ScaledSpacingTokens } from './tokens/spacing';
import { getTypographyForBreakpoint, TypographyTokens } from './tokens/typography';

type DiscoveryLayoutConfig = {
  columns: Record<Breakpoint, number>;
  gap: Record<Breakpoint, number>;
};

type LayoutConfig = {
  discovery: DiscoveryLayoutConfig;
};

export type NovaTheme = {
  colors: ColorTokens;
  spacing: SpacingTokens | ScaledSpacingTokens;
  radius: RadiusTokens;
  typography: TypographyTokens;
  breakpoint: Breakpoint;
  layout: LayoutConfig;
  colorScheme: SupportedColorScheme;
  isDark: boolean;
};

const discoveryGapMap: DiscoveryLayoutConfig['gap'] = {
  compact: spacingTokens.sm,
  cozy: spacingTokens.md,
  spacious: spacingTokens.lg,
  immersive: spacingTokens.xl,
};

const layoutConfig: LayoutConfig = {
  discovery: {
    columns: discoveryColumnMap,
    gap: discoveryGapMap,
  },
};

const defaultTypography = getTypographyForBreakpoint('compact');
const defaultColorScheme: SupportedColorScheme = 'dark';

const defaultTheme: NovaTheme = {
  colors: colorTokens[defaultColorScheme],
  spacing: spacingTokens,
  radius: radiusTokens,
  typography: defaultTypography,
  breakpoint: 'compact',
  layout: layoutConfig,
  colorScheme: defaultColorScheme,
  isDark: true,
};

const ThemeContext = createContext<NovaTheme>(defaultTheme);

export function NovaThemeProvider({ children }: { children: ReactNode }) {
  const { width } = useWindowDimensions();
  // Enforce dark theme regardless of system preference
  const colorScheme: SupportedColorScheme = 'dark';
  // TV platforms use fixed 'immersive' breakpoint to avoid text size flicker
  // Mobile devices (phones + tablets) use 'compact' for mobile layout
  // Tablets get 1.2x scaling via typography/spacing functions
  // Web uses width-based breakpoints for responsive columns
  const isMobileDevice = (Platform.OS === 'ios' || Platform.OS === 'android') && !Platform.isTV;
  const breakpoint = Platform.isTV ? 'immersive' : isMobileDevice ? 'compact' : getBreakpoint(width);
  const isTV = Platform.isTV;
  const typography = useMemo(() => getTypographyForBreakpoint(breakpoint, isTV, isTablet), [breakpoint, isTV]);
  const spacing = useMemo(() => getSpacingForTV(isTV, isTablet), [isTV]);
  const colors = useMemo(() => getColorTokens(colorScheme), [colorScheme]);
  const isDark = colorScheme === 'dark';

  const value = useMemo<NovaTheme>(
    () => ({
      colors,
      spacing,
      radius: radiusTokens,
      typography,
      breakpoint,
      layout: layoutConfig,
      colorScheme,
      isDark,
    }),
    [colors, spacing, typography, breakpoint, colorScheme, isDark],
  );

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme() {
  return useContext(ThemeContext);
}
