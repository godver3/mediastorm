import { useQueryClient } from '@tanstack/react-query';
import { useEffect, useSyncExternalStore } from 'react';
import { usersKeys } from '@/api/users';
import type { UserProfile } from '@/api/types';
import { pinPrompt } from '@/auth/pinPrompt';
import { PinDialog } from './PinDialog';

/** Renders the PIN dialog when a request hits 403 profile_locked (see auth/pinPrompt). */
export function PinPromptHost() {
  useEffect(() => pinPrompt.registerHost(), []);
  const pending = useSyncExternalStore(pinPrompt.subscribe, pinPrompt.current, pinPrompt.current);
  const queryClient = useQueryClient();
  if (!pending) return null;
  const profile = queryClient
    .getQueryData<UserProfile[]>(usersKeys.list())
    ?.find((p) => p.id === pending.profileId);
  return (
    <PinDialog
      key={pending.profileId}
      profileId={pending.profileId}
      profileName={profile?.name}
      pinLength={profile?.pinLength}
      description="This profile's unlock expired. Enter the PIN to continue."
      onClose={() => pending.settle(false)}
      onUnlocked={() => pending.settle(true)}
    />
  );
}
