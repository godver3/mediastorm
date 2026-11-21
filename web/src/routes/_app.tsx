import { createFileRoute, redirect } from '@tanstack/react-router';
import { sessionStore } from '@/auth/sessionStore';
import { AppShell } from '@/components/shell/AppShell';

/** Pathless layout for every signed-in page: guards, then renders the shell. */
export const Route = createFileRoute('/_app')({
  beforeLoad: ({ location }) => {
    const { session, profileId } = sessionStore.getState();
    if (!session) {
      throw redirect({ to: '/login', search: { redirect: location.href } });
    }
    if (!profileId) {
      throw redirect({ to: '/profiles', search: { redirect: location.href } });
    }
  },
  component: AppShell,
});
