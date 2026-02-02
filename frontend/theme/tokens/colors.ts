export type SupportedColorScheme = 'light' | 'dark';

type ColorPalette = {
  background: {
    base: string;
    surface: string;
    elevated: string;
    contrast: string;
  };
  text: {
    primary: string;
    secondary: string;
    muted: string;
    disabled: string;
    inverse: string;
  };
  accent: {
    primary: string;
    secondary: string;
  };
  border: {
    subtle: string;
    emphasis: string;
  };
  status: {
    success: string;
    warning: string;
    danger: string;
  };
  warning: {
    muted: string;
    text: string;
  };
  overlay: {
    scrim: string;
    button: string;
    medium: string;
  };
};

const darkPalette: ColorPalette = {
  background: {
    base: '#0b0b0f',
    surface: '#16161f',
    elevated: '#1f1f2a',
    contrast: '#000000',
  },
  text: {
    primary: '#ffffff',
    secondary: '#c7cad6',
    muted: '#8c90a6',
    disabled: '#555866',
    inverse: '#ffffff',
  },
  accent: {
    primary: '#3f66ff',
    secondary: '#ff9f1a',
  },
  border: {
    subtle: '#2b2f3c',
    emphasis: '#4a4f5e',
  },
  status: {
    success: '#2ecc71',
    warning: '#f1c40f',
    danger: '#e74c3c',
  },
  warning: {
    muted: 'rgba(241, 196, 15, 0.15)',
    text: '#f1c40f',
  },
  overlay: {
    scrim: 'rgba(11, 11, 15, 0.72)',
    button: 'rgba(255, 255, 255, 0.12)',
    medium: 'rgba(255, 255, 255, 0.08)',
  },
};

const lightPalette: ColorPalette = {
  background: {
    base: '#f5f6fa',
    surface: '#ffffff',
    elevated: '#eef1f8',
    contrast: '#000000',
  },
  text: {
    primary: '#11181c',
    secondary: '#475569',
    muted: '#64748b',
    disabled: '#9ca3af',
    inverse: '#ffffff',
  },
  accent: {
    primary: '#335dff',
    secondary: '#ff8f00',
  },
  border: {
    subtle: '#d6dae8',
    emphasis: '#9aa0b5',
  },
  status: {
    success: '#1d9a5f',
    warning: '#d97706',
    danger: '#d83145',
  },
  warning: {
    muted: 'rgba(217, 119, 6, 0.15)',
    text: '#d97706',
  },
  overlay: {
    scrim: 'rgba(10, 12, 16, 0.68)',
    button: 'rgba(15, 23, 42, 0.08)',
    medium: 'rgba(15, 23, 42, 0.06)',
  },
};

export const colorTokens: Record<SupportedColorScheme, ColorPalette> = {
  dark: darkPalette,
  light: lightPalette,
};

export type ColorTokens = ColorPalette;

export function getColorTokens(scheme: SupportedColorScheme | null | undefined): ColorTokens {
  if (scheme === 'light') {
    return colorTokens.light;
  }

  return colorTokens.dark;
}
