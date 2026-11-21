import { describe, expect, it } from 'vitest';
import { serverBasePath } from './basePath';

describe('serverBasePath', () => {
  it('returns everything before /watch', () => {
    expect(serverBasePath('/watch-next/')).toBe('');
    expect(serverBasePath('/media/watch-next/search')).toBe('/media');
    expect(serverBasePath('/a/b/watch/details')).toBe('/a/b');
    expect(serverBasePath('/')).toBe('');
  });
});
