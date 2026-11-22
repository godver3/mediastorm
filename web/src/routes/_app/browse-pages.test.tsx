import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
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

type Handler = (url: URL, init?: RequestInit) => Response | undefined;
let handler: Handler = () => undefined;

beforeEach(() => {
  vi.stubGlobal('fetch', fetchMock);
  fetchMock.mockImplementation(async (input, init) => {
    const url = new URL(String(input), 'http://localhost');
    if (url.pathname === '/api/users') return json(200, [profile]);
    if (url.pathname.endsWith('/settings')) return json(200, {});
    return handler(url, init) ?? json(404, { error: 'not found' });
  });
  sessionStore.login({
    token: 't',
    expiresAt: '2099-01-01T00:00:00Z',
    accountId: 'a',
    username: 'u',
    isMaster: false,
  });
  sessionStore.selectProfile('p1');
});
afterEach(() => {
  fetchMock.mockReset();
  vi.unstubAllGlobals();
  sessionStore.logout();
});

describe('watchlist page', () => {
  it('lists titles, filters via the URL and removes a title', async () => {
    const items = [
      {
        id: 'tmdb:movie:949',
        mediaType: 'movie',
        name: 'Heat',
        year: 1995,
        watchState: 'complete',
      },
      { id: 'tmdb:tv:1396', mediaType: 'series', name: 'Breaking Bad', year: 2008 },
    ];
    const seen: URL[] = [];
    handler = (url, init) => {
      if (url.pathname === '/api/users/p1/display-list') {
        seen.push(url);
        const type = url.searchParams.get('filterMediaType');
        const visible = items.filter((i) => !type || i.mediaType === type);
        return json(200, { items: visible, total: visible.length, genres: ['Crime'] });
      }
      if (init?.method === 'DELETE' && url.pathname.startsWith('/api/users/p1/watchlist/')) {
        items.splice(0, 1);
        return new Response(null, { status: 204 });
      }
      return undefined;
    };
    const { router } = renderApp('/watchlist');
    expect(await screen.findByRole('link', { name: 'Heat' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Breaking Bad' })).toBeInTheDocument();
    expect(seen[0]!.searchParams.get('source')).toBe('watchlist');
    expect(screen.getByTitle('Watched')).toBeInTheDocument();

    await userEvent.click(screen.getByRole('radio', { name: 'Shows' }));
    await waitFor(() => expect(router.state.location.search).toMatchObject({ type: 'series' }));
    await waitFor(() =>
      expect(screen.queryByRole('link', { name: 'Heat' })).not.toBeInTheDocument(),
    );

    await userEvent.click(screen.getByRole('radio', { name: 'All' }));
    await screen.findByRole('link', { name: 'Heat' });
    await userEvent.click(screen.getByRole('button', { name: 'Remove from watchlist: Heat' }));
    await waitFor(() =>
      expect(screen.queryByRole('link', { name: 'Heat' })).not.toBeInTheDocument(),
    );
    expect(
      fetchMock.mock.calls.some(
        ([u, i]) =>
          i?.method === 'DELETE' && String(u).includes('/watchlist/movie/tmdb%3Amovie%3A949'),
      ),
    ).toBe(true);
  });

  it('shows the empty state', async () => {
    handler = (url) =>
      url.pathname.endsWith('/display-list') ? json(200, { items: [], total: 0 }) : undefined;
    renderApp('/watchlist');
    expect(await screen.findByText('Your watchlist is empty')).toBeInTheDocument();
  });
});

describe('lists page', () => {
  it('shows custom lists and creates one', async () => {
    const lists = [
      { id: 'l1', name: 'Friday Night', itemCount: 4 },
      { id: 'sa', name: 'Set Aside', itemCount: 1 },
    ];
    handler = (url, init) => {
      if (url.pathname === '/api/users/p1/custom-lists') {
        if (init?.method === 'POST') {
          const body = JSON.parse(String(init.body)) as { name: string };
          const created = { id: 'l2', name: body.name, itemCount: 0 };
          lists.push(created);
          return json(200, created);
        }
        return json(200, lists);
      }
      if (url.pathname === '/api/users/p1/display-list') return json(200, { items: [], total: 0 });
      return undefined;
    };
    const { router } = renderApp('/lists');
    expect(await screen.findByRole('link', { name: /Friday Night/ })).toBeInTheDocument();
    // Set Aside is a personal tile, not one of "My lists".
    const mine = screen.getByRole('heading', { name: 'My lists' }).closest('section')!;
    expect(within(mine).queryByText('Set Aside')).not.toBeInTheDocument();

    await userEvent.click(screen.getAllByRole('button', { name: /New list/ })[0]!);
    await userEvent.type(screen.getByLabelText('Name'), 'Horror Month');
    await userEvent.click(screen.getByRole('button', { name: 'Create' }));
    await waitFor(() => expect(router.state.location.pathname).toBe('/lists/custom.l2'));
  });
});
