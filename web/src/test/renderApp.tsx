import { QueryClientProvider } from '@tanstack/react-query';
import { createMemoryHistory, createRouter, RouterProvider } from '@tanstack/react-router';
import { render } from '@testing-library/react';
import { brandingKeys } from '@/api/branding';
import { createQueryClient } from '@/router';
import { routeTree } from '@/routeTree.gen';

/** Render the full app at `path` with an in-memory router (no basepath). */
export function renderApp(path: string) {
  const queryClient = createQueryClient();
  queryClient.setDefaultOptions({ queries: { retry: false } });
  // Default (unbranded) server, so tests' fetch mocks needn't answer /api/branding.
  queryClient.setQueryData(brandingKeys.all, { branding: {} });
  const router = createRouter({
    routeTree,
    history: createMemoryHistory({ initialEntries: [path] }),
    context: { queryClient },
  });
  const utils = render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  );
  return { ...utils, router, queryClient };
}
