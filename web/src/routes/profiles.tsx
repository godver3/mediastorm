import { useDocumentTitle } from '@/lib/documentTitle';
import { createFileRoute, redirect, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import type { UserProfile } from '@/api/types';
import { useLogout, useProfiles } from '@/auth/hooks';
import { sessionStore } from '@/auth/sessionStore';
import { Button, EmptyState, ErrorState, Icon, ProfileAvatar, Skeleton } from '@/components';
import { AuthLayout } from '@/components/AuthLayout';
import { PinDialog } from '@/components/auth/PinDialog';

interface ProfilesSearch {
  redirect?: string;
}

export const Route = createFileRoute('/profiles')({
  validateSearch: (s: Record<string, unknown>): ProfilesSearch => ({
    redirect: typeof s.redirect === 'string' ? s.redirect : undefined,
  }),
  beforeLoad: ({ location }) => {
    if (!sessionStore.getState().session)
      throw redirect({ to: '/login', search: { redirect: location.href } });
  },
  component: ProfilesPage,
});

export function ProfilesPage() {
  useDocumentTitle('Profiles');
  const navigate = useNavigate();
  const { redirect: redirectTo } = Route.useSearch();
  const profiles = useProfiles();
  const logout = useLogout();
  const [pinFor, setPinFor] = useState<UserProfile | null>(null);

  async function enter(profile: UserProfile) {
    // bindQueriesToProfile (main.tsx) cancels and drops the old profile's queries.
    sessionStore.selectProfile(profile.id);
    // Only follow same-app redirects; everything else lands on Home.
    if (redirectTo && redirectTo.startsWith('/') && !redirectTo.startsWith('//')) {
      await navigate({ to: redirectTo });
    } else {
      await navigate({ to: '/' });
    }
  }

  function choose(profile: UserProfile) {
    if (profile.hasPin && !sessionStore.hasValidUnlock(profile.id)) setPinFor(profile);
    else void enter(profile);
  }

  return (
    <AuthLayout title="Who's watching?" subtitle="Choose a profile." wide>
      {profiles.isPending ? (
        <div className="flex flex-wrap justify-center gap-6">
          {[0, 1, 2].map((i) => (
            <Skeleton key={i} className="h-32 w-32 rounded-full" />
          ))}
        </div>
      ) : profiles.isError ? (
        <ErrorState error={profiles.error} onRetry={() => void profiles.refetch()} />
      ) : profiles.data.length === 0 ? (
        <EmptyState
          icon="users"
          title="No profiles yet"
          description="Create a profile in the account settings first."
        />
      ) : (
        <ul className="flex flex-wrap justify-center gap-6 sm:gap-8">
          {profiles.data.map((p) => (
            <li key={p.id}>
              <button
                type="button"
                onClick={() => choose(p)}
                className="group flex w-28 cursor-pointer flex-col items-center gap-3 outline-none sm:w-32"
              >
                <span className="relative rounded-full ring-2 ring-transparent ring-offset-4 ring-offset-bg transition group-hover:scale-105 group-hover:ring-accent group-focus-visible:ring-accent">
                  <ProfileAvatar profile={p} size={112} />
                  {p.hasPin && (
                    <span className="glass absolute right-0 bottom-0 flex h-8 w-8 items-center justify-center rounded-full">
                      <Icon name="lock" size={15} />
                    </span>
                  )}
                </span>
                <span className="max-w-full truncate text-sm font-medium text-muted group-hover:text-fg">
                  {p.name}
                </span>
              </button>
            </li>
          ))}
        </ul>
      )}
      <div className="mt-12">
        <Button
          variant="ghost"
          leftIcon={<Icon name="logout" size={18} />}
          onClick={() => void logout()}
        >
          Sign out
        </Button>
      </div>
      {pinFor && (
        <PinDialog
          profileId={pinFor.id}
          profileName={pinFor.name}
          pinLength={pinFor.pinLength}
          onClose={() => setPinFor(null)}
          onUnlocked={() => {
            const p = pinFor;
            setPinFor(null);
            void enter(p);
          }}
        />
      )}
    </AuthLayout>
  );
}
