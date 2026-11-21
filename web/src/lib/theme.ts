const HEX = /^#?([0-9a-f]{6})$/i;

export const DEFAULT_ACCENT = '#3f66ff';

/** Normalise '#RRGGBB' / 'RRGGBB'; returns null for anything else. */
export function normalizeHex(value: string | undefined | null): string | null {
  const match = value?.trim().match(HEX);
  return match ? `#${match[1]!.toLowerCase()}` : null;
}

/** Apply the accent colour to the document (CSS variable --accent). */
export function applyAccent(value: string | undefined | null): void {
  const hex = normalizeHex(value) ?? DEFAULT_ACCENT;
  document.documentElement.style.setProperty('--accent', hex);
}
