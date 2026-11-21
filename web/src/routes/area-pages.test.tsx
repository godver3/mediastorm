import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { toDateKey } from '@/api/calendar';
import { sessionStore } from '@/auth/sessionStore';
import { renderApp } from '@/test/renderApp';

const fetchMock = vi.fn<typeof fetch>();
const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const profile = {
  id: 'p1',
  accountId: 'a',
  name: 'Alice',
  hasPin: false,
  hasIcon: false,
  isKidsProfile: false,
  allowShareLinks: false,
  createdAt: '',
  updatedAt: '',
};

type Handler = (init?: RequestInit) => Response;
function route(handlers: Record<string, Handler>) {
  fetchMock.mockImplementation(async (input, init) => {
    const path = String(input).split('?')[0]!;
    const key = `${init?.method ?? 'GET'} ${path}`;
    const h = handlers[key] ?? handlers[path];
    return h ? h(init) : json(404, { error: 'not found' });
  });
}

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock);
  sessionStore.login({
    token: 't',
    expiresAt: '2099-01-01T00:00:00Z',
    accountId: 'a',
    username: 'u',
    isMaster: true,
  });
  sessionStore.selectProfile('p1');
});
afterEach(() => {
  fetchMock.mockReset();
  vi.unstubAllGlobals();
  sessionStore.logout();
  document.documentElement.style.removeProperty('--accent');
});

describe('settings', () => {
  it('changes the accent live and saves the whole settings document', async () => {
    let saved: Record<string, unknown> | undefined;
    route({
      '/api/users': () => json(200, [profile]),
      'GET /api/users/p1/settings': () =>
        json(200, { display: { appearance: { accentColor: '#3f66ff' } }, liveTV: { keep: 1 } }),
      'PUT /api/users/p1/settings': (init) => {
        saved = JSON.parse(String(init!.body));
        return json(200, saved);
      },
    });
    renderApp('/settings?section=appearance');
    await userEvent.click(await screen.findByRole('radio', { name: '#e0567a' }));
    await waitFor(() =>
      expect(document.documentElement.style.getPropertyValue('--accent')).toBe('#e0567a'),
    );
    await waitFor(() => expect(saved).toBeDefined());
    expect(saved).toEqual({
      display: { appearance: { accentColor: '#e0567a' } },
      liveTV: { keep: 1 },
    });
    expect(screen.getByRole('link', { name: /Admin/ })).toHaveAttribute('href', '/admin');
  });
});

describe('calendar', () => {
  it('shows releases and the selected release detail', async () => {
    const today = toDateKey(new Date());
    route({
      '/api/users': () => json(200, [profile]),
      '/api/users/p1/settings': () => json(200, {}),
      '/api/users/p1/calendar': () =>
        json(200, {
          items: [
            {
              title: 'Severance',
              mediaType: 'series',
              airDate: today,
              airTime: '21:00',
              seasonNumber: 2,
              episodeNumber: 4,
              episodeTitle: 'Woe’s Hollow',
              externalIds: { tmdb: '95396' },
            },
          ],
        }),
      '/api/metadata/series/details': () =>
        json(200, {
          title: { id: 'x', name: 'Severance', mediaType: 'series', genres: ['Drama'] },
        }),
    });
    renderApp('/calendar?view=agenda');
    expect(await screen.findByRole('heading', { name: 'Severance' })).toBeInTheDocument();
    expect(await screen.findByText('Drama')).toBeInTheDocument();
    const link = screen.getByRole('link', { name: /View details/ });
    expect(link.getAttribute('href')).toContain('titleId=tmdb%3Atv%3A95396');
  });
});
