import { describe, expect, it } from 'vitest';
import { resolveBranding, resolveBrandingUrl } from './branding';

describe('resolveBrandingUrl', () => {
  it('serves uploads under /api and passes absolute URLs through', () => {
    expect(resolveBrandingUrl('/branding/images/web-icon?v=1')).toBe(
      '/api/branding/images/web-icon?v=1',
    );
    expect(resolveBrandingUrl('/api/branding/images/x')).toBe('/api/branding/images/x');
    expect(resolveBrandingUrl('https://cdn.example/logo.png')).toBe('https://cdn.example/logo.png');
    expect(resolveBrandingUrl('  ')).toBeUndefined();
  });
});

describe('resolveBranding', () => {
  it('falls back like the native clients', () => {
    expect(resolveBranding({ homeMobileLogoUrl: '/m' })).toEqual({
      logoUrl: '/api/m',
      profilePickerUrl: '/api/m',
      iconUrl: undefined,
    });
    expect(
      resolveBranding({ loadingLogoUrl: '/l', homeTvImageUrl: '/t', webIconUrl: '/i' }),
    ).toEqual({
      logoUrl: '/api/l',
      profilePickerUrl: '/api/t',
      iconUrl: '/api/i',
    });
  });
});
