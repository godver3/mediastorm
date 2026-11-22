import { describe, expect, it } from 'vitest';
import { serverBasePath } from './basePath';

describe('serverBasePath', () => {
  it('returns everything before /watch', () => {
    expect(serverBasePath('/watch/')).toBe('');
    expect(serverBasePath('/media/watch/search')).toBe('/media');
    expect(serverBasePath('/a/b/watch/details')).toBe('/a/b');
    expect(serverBasePath('/')).toBe('');
  });
});

describe('routerBasepath', () => {
  it('is the server base path plus /watch', async () => {
    const { routerBasepath } = await import('@/router');
    expect(routerBasepath('/watch/details')).toBe('/watch');
    expect(routerBasepath('/media/watch/live')).toBe('/media/watch');
    expect(routerBasepath('/media/watch')).toBe('/media/watch');
  });
});
