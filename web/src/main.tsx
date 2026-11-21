import { QueryClientProvider } from '@tanstack/react-query';
import { RouterProvider } from '@tanstack/react-router';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { sessionStore } from '@/auth/sessionStore';
import { createAppRouter, createQueryClient } from './router';
import './styles/index.css';

const queryClient = createQueryClient();
const router = createAppRouter(queryClient);

// Re-run route guards whenever sign-in state or the selected profile changes
// (e.g. a failed refresh signs out, a profile_locked deselects the profile).
let last = sessionStore.getState();
sessionStore.subscribe(() => {
  const next = sessionStore.getState();
  if (!!next.session !== !!last.session || next.profileId !== last.profileId) {
    if (!next.session) queryClient.clear();
    void router.invalidate();
  }
  last = next;
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
