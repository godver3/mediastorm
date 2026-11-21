import { QueryClient } from '@tanstack/react-query';
import { createRouter } from '@tanstack/react-router';
import { PageSpinner } from '@/components';
import { routeTree } from './routeTree.gen';

export interface RouterContext {
  queryClient: QueryClient;
}

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        retry: (count, error) => {
          const status = (error as { status?: number }).status;
          if (status && status >= 400 && status < 500) return false;
          return count < 2;
        },
        refetchOnWindowFocus: false,
      },
    },
  });
}

/** Router basepath = Vite base without the trailing slash (e.g. '/watch-next'), plus any server base path. */
export function routerBasepath(pathname = window.location.pathname): string {
  const base = import.meta.env.BASE_URL.replace(/\/+$/, '');
  const idx = pathname.indexOf(base);
  return idx > 0 ? pathname.slice(0, idx) + base : base;
}

export function createAppRouter(queryClient: QueryClient) {
  return createRouter({
    routeTree,
    basepath: routerBasepath(),
    context: { queryClient },
    defaultPreload: 'intent',
    defaultPendingComponent: PageSpinner,
    scrollRestoration: true,
  });
}

declare module '@tanstack/react-router' {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
