import { Platform, TextStyle } from 'react-native';

import { Breakpoint, breakpointScaleMultiplier, getTVScaleFactor, TABLET_SCALE_FACTOR } from './breakpoints';

export type TypographyScale = {
  fontSize: number;
  lineHeight: number;
  fontWeight?: TextStyle['fontWeight'];
  letterSpacing?: number;
  fontFamily?: TextStyle['fontFamily'];
};

export type TypographyTokens = {
  title: {
    xl: TypographyScale;
    lg: TypographyScale;
    md: TypographyScale;
  };
  body: {
    lg: TypographyScale;
    md: TypographyScale;
    sm: TypographyScale;
  };
  caption: {
    sm: TypographyScale;
  };
  label: {
    md: TypographyScale;
  };
  shelf: {
    title: TypographyScale;
  };
};

const systemFont: TextStyle['fontFamily'] = 'System';

const baseTypography: TypographyTokens = {
  title: {
    xl: { fontSize: 28, lineHeight: 34, fontWeight: '700', fontFamily: systemFont },
    lg: { fontSize: 24, lineHeight: 30, fontWeight: '700', fontFamily: systemFont },
    md: { fontSize: 20, lineHeight: 26, fontWeight: '600', fontFamily: systemFont },
  },
  body: {
    lg: { fontSize: 18, lineHeight: 26, fontWeight: '400', fontFamily: systemFont },
    md: { fontSize: 16, lineHeight: 24, fontWeight: '400', fontFamily: systemFont },
    sm: { fontSize: 14, lineHeight: 20, fontWeight: '400', fontFamily: systemFont },
  },
  caption: {
    sm: { fontSize: 12, lineHeight: 16, fontWeight: '500', letterSpacing: 0.4, fontFamily: systemFont },
  },
  label: {
    md: { fontSize: 15, lineHeight: 20, fontWeight: '600', letterSpacing: 0.2, fontFamily: systemFont },
  },
  shelf: {
    title: { fontSize: 24, lineHeight: 30, fontWeight: '700', fontFamily: systemFont },
  },
};

const round = (value: number) => Math.round(value * 10) / 10;

const scale = (scale: TypographyScale, factor: number): TypographyScale => ({
  ...scale,
  fontSize: round(scale.fontSize * factor),
  lineHeight: round(scale.lineHeight * factor),
});

export function getTypographyForBreakpoint(
  breakpoint: Breakpoint,
  isTV: boolean = false,
  isTablet: boolean = false,
): TypographyTokens {
  const baseFactor = breakpointScaleMultiplier[breakpoint];
  const tvFactor = isTV ? getTVScaleFactor(Platform.OS) : 1;
  const tabletFactor = isTablet ? TABLET_SCALE_FACTOR : 1;
  const factor = baseFactor * tvFactor * tabletFactor;

  // Apply additional 20% scale for shelf titles on tvOS
  const shelfTitleFactor = isTV ? factor * 1.2 : factor;

  return {
    title: {
      xl: scale(baseTypography.title.xl, factor),
      lg: scale(baseTypography.title.lg, factor),
      md: scale(baseTypography.title.md, factor),
    },
    body: {
      lg: scale(baseTypography.body.lg, factor),
      md: scale(baseTypography.body.md, factor),
      sm: scale(baseTypography.body.sm, factor),
    },
    caption: {
      sm: scale(baseTypography.caption.sm, factor),
    },
    label: {
      md: scale(baseTypography.label.md, factor),
    },
    shelf: {
      title: scale(baseTypography.shelf.title, shelfTitleFactor),
    },
  };
}
