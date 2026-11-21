import { describe, expect, it } from 'vitest';
import { applyAccent, DEFAULT_ACCENT, normalizeHex } from './theme';

describe('theme', () => {
  it('normalises hex colours', () => {
    expect(normalizeHex('FF00aa')).toBe('#ff00aa');
    expect(normalizeHex('red')).toBeNull();
  });
  it('falls back to the default accent', () => {
    applyAccent('nope');
    expect(document.documentElement.style.getPropertyValue('--accent')).toBe(DEFAULT_ACCENT);
    expect(document.documentElement.style.getPropertyValue('--accent-contrast')).toBe('#111111');
    applyAccent('#3f66ff');
    expect(document.documentElement.style.getPropertyValue('--accent-contrast')).toBe('#ffffff');
  });
});
