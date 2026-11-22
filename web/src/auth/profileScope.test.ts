import { QueryClient } from '@tanstack/react-query';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { bindQueriesToProfile } from './profileScope';
import { sessionStore } from './sessionStore';

describe('bindQueriesToProfile', () => {
  let qc: QueryClient;
  let unbind: () => void;
  beforeEach(() => {
    sessionStore.selectProfile('kids');
    qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    unbind = bindQueriesToProfile(qc);
  });
  afterEach(() => {
    unbind();
    qc.clear();
    sessionStore.selectProfile(null);
  });

  it('aborts in-flight queries of the previous profile and drops them', async () => {
    let signal: AbortSignal | undefined;
    const pending = qc.fetchQuery({
      queryKey: ['live', 'channels', 'kids'],
      queryFn: ({ signal: s }) => {
        signal = s;
        return new Promise((_, reject) =>
          s.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError'))),
        );
      },
    });
    qc.setQueryData(['home', 'kids'], { shelves: [] });
    qc.setQueryData(['users', 'list'], [{ id: 'kids' }]);
    qc.setQueryData(['branding'], { branding: {} });

    sessionStore.selectProfile('adult');

    expect(signal?.aborted).toBe(true);
    await pending.catch(() => undefined);
    expect(qc.getQueryCache().find({ queryKey: ['live', 'channels', 'kids'] })).toBeUndefined();
    expect(qc.getQueryData(['home', 'kids'])).toBeUndefined();
    // Account / server queries are kept.
    expect(qc.getQueryData(['users', 'list'])).toBeDefined();
    expect(qc.getQueryData(['branding'])).toBeDefined();
  });

  it('leaves the cache alone when the same profile is re-selected or unlocks change', () => {
    qc.setQueryData(['home', 'kids'], { shelves: [] });
    sessionStore.selectProfile('kids');
    sessionStore.setUnlock('kids', 't');
    expect(qc.getQueryData(['home', 'kids'])).toBeDefined();
  });
});
