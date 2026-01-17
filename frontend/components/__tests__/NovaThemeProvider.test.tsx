import renderer, { act } from 'react-test-renderer';

import type { NovaTheme } from '@/theme';
import { NovaThemeProvider, useTheme } from '@/theme';

jest.mock('react-native', () => {
  const actual = jest.requireActual('react-native');
  return {
    ...actual,
    useColorScheme: jest.fn(),
    Platform: {
      ...actual.Platform,
      isTV: false,
      OS: 'android',
    },
    useWindowDimensions: jest.fn(() => ({ width: 800, height: 600 })),
  };
});

const { useColorScheme, Platform } = jest.requireMock('react-native') as {
  useColorScheme: jest.Mock;
  Platform: { isTV: boolean };
  useWindowDimensions: jest.Mock;
};

const ThemeProbe = ({ capture }: { capture: (theme: NovaTheme) => void }) => {
  const theme = useTheme();
  capture(theme);
  return null;
};

describe('NovaThemeProvider', () => {
  it('provides the dark palette by default', () => {
    useColorScheme.mockReturnValue(null);

    let capturedTheme = null as NovaTheme | null;
    act(() => {
      renderer.create(
        <NovaThemeProvider>
          <ThemeProbe
            capture={(theme) => {
              capturedTheme = theme;
            }}
          />
        </NovaThemeProvider>,
      );
    });

    expect(capturedTheme!.colors).toMatchSnapshot('dark-colors');
    expect(capturedTheme!.isDark).toBe(true);
  });

  it('enforces dark theme regardless of system preference', () => {
    useColorScheme.mockReturnValue('light');

    let capturedTheme = null as NovaTheme | null;
    act(() => {
      renderer.create(
        <NovaThemeProvider>
          <ThemeProbe
            capture={(theme) => {
              capturedTheme = theme;
            }}
          />
        </NovaThemeProvider>,
      );
    });

    expect(capturedTheme!.colors).toMatchSnapshot('dark-colors');
    expect(capturedTheme!.isDark).toBe(true);
  });

  it('applies TV scaling when Platform.isTV is true', () => {
    Platform.isTV = true;
    useColorScheme.mockReturnValue('dark');

    let capturedTheme = null as NovaTheme | null;
    act(() => {
      renderer.create(
        <NovaThemeProvider>
          <ThemeProbe
            capture={(theme) => {
              capturedTheme = theme;
            }}
          />
        </NovaThemeProvider>,
      );
    });

    // Check that spacing is scaled down for TV
    expect(capturedTheme!.spacing.md).toBe(6); // 12 * 0.5
    expect(capturedTheme!.spacing.lg).toBe(8); // 16 * 0.5

    // Check that typography is scaled down for TV
    // With Platform.isTV true, we use 'immersive' breakpoint (1.2 multiplier) * TV scale (0.5) = 0.6
    expect(capturedTheme!.typography.body.md.fontSize).toBe(9.6); // 16 * 0.6
    expect(capturedTheme!.typography.title.lg.fontSize).toBe(14.4); // 24 * 0.6
  });

  it('uses normal scaling when Platform.isTV is false', () => {
    Platform.isTV = false;
    useColorScheme.mockReturnValue('dark');

    let capturedTheme = null as NovaTheme | null;
    act(() => {
      renderer.create(
        <NovaThemeProvider>
          <ThemeProbe
            capture={(theme) => {
              capturedTheme = theme;
            }}
          />
        </NovaThemeProvider>,
      );
    });

    // Check that spacing is not scaled for non-TV
    expect(capturedTheme!.spacing.md).toBe(12);
    expect(capturedTheme!.spacing.lg).toBe(16);

    // Non-TV mobile devices (iOS/Android) always use 'compact' breakpoint
    // regardless of screen width, so no multiplier is applied
    expect(capturedTheme!.typography.body.md.fontSize).toBe(16); // 16 * 1.0 (compact)
    expect(capturedTheme!.typography.title.lg.fontSize).toBe(24); // 24 * 1.0 (compact)
  });
});
