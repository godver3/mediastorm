import { createRootRouteWithContext, Outlet } from '@tanstack/react-router';
import { EmptyState, ErrorState, Page } from '@/components';
import type { RouterContext } from '@/router';

export const Route = createRootRouteWithContext<RouterContext>()({
  component: Outlet,
  notFoundComponent: () => (
    <Page>
      <EmptyState icon="alert" title="Page not found" description="That page doesn't exist." />
    </Page>
  ),
  errorComponent: ({ error, reset }) => <ErrorState error={error} onRetry={reset} />,
});
