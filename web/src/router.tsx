import { QueryClient } from '@tanstack/react-query';
import { createRouter } from '@tanstack/react-router';
import { serverBasePath } from '@/api/basePath';
import { PageSpinner } from '@/components';
import { APP_PATH } from '@/lib/appPath';
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

/** Router basepath: the server base path (if any) + the app path, e.g. '/media/watch'. */
export function routerBasepath(pathname = window.location.pathname): string {
  return `${serverBasePath(pathname)}${APP_PATH}`;
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
