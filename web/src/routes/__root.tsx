import { createRootRouteWithContext, Outlet } from '@tanstack/react-router';
import { EmptyState, ErrorState, Page } from '@/components';
import { PinPromptHost } from '@/components/auth/PinPromptHost';
import { useBrandingFavicon } from '@/lib/branding';
import type { RouterContext } from '@/router';

function RootComponent() {
  useBrandingFavicon();
  return (
    <>
      <Outlet />
      <PinPromptHost />
    </>
  );
}

export const Route = createRootRouteWithContext<RouterContext>()({
  component: RootComponent,
  notFoundComponent: () => (
    <Page>
      <EmptyState icon="alert" title="Page not found" description="That page doesn't exist." />
    </Page>
  ),
  errorComponent: ({ error, reset }) => <ErrorState error={error} onRetry={reset} />,
});
