const HEX = /^#?([0-9a-f]{6})$/i;

/** The brand amber the native clients use (Android TV Theme.kt Accent, tvOS tint). */
export const DEFAULT_ACCENT = '#f5b400';

/** Normalise '#RRGGBB' / 'RRGGBB'; returns null for anything else. */
export function normalizeHex(value: string | undefined | null): string | null {
  const match = value?.trim().match(HEX);
  return match ? `#${match[1]!.toLowerCase()}` : null;
}

/** Apply the accent colour to the document (CSS variable --accent). */
export function applyAccent(value: string | undefined | null): void {
  const hex = normalizeHex(value) ?? DEFAULT_ACCENT;
  const style = document.documentElement.style;
  style.setProperty('--accent', hex);
  style.setProperty('--accent-contrast', accentContrast(hex));
}

/** Black text on light accents (the default amber), white on dark ones. */
export function accentContrast(hex: string): string {
  const n = parseInt(hex.slice(1), 16);
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map((c) => {
    const v = c / 255;
    return v <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4;
  }) as [number, number, number];
  const luminance = 0.2126 * r + 0.7152 * g + 0.0722 * b;
  return luminance > 0.4 ? '#111111' : '#ffffff';
}
