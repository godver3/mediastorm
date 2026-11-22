/**
 * In-place PIN prompt for 403 profile_locked (native clients prompt and retry
 * instead of bouncing to the picker). apiRequest calls requestProfileUnlock();
 * <PinPromptHost> (mounted once at the root) renders the dialog and settles the
 * promise. Concurrent locked requests for the same profile share one prompt.
 */
type Listener = () => void;

export interface PendingUnlock {
  profileId: string;
  promise: Promise<boolean>;
  settle: (unlocked: boolean) => void;
}

let pending: PendingUnlock | null = null;
let hosts = 0;
const listeners = new Set<Listener>();
const emit = () => listeners.forEach((l) => l());

export const pinPrompt = {
  /** Resolves true once the profile is unlocked, false if the user cancels (or no host is mounted). */
  request(profileId: string): Promise<boolean> {
    if (hosts === 0) return Promise.resolve(false);
    if (pending?.profileId === profileId) return pending.promise;
    pending?.settle(false);
    let settle!: (ok: boolean) => void;
    const promise = new Promise<boolean>((resolve) => {
      settle = (ok) => {
        if (pending?.promise === promise) pending = null;
        resolve(ok);
        emit();
      };
    });
    pending = { profileId, promise, settle };
    emit();
    return promise;
  },
  current(): PendingUnlock | null {
    return pending;
  },
  subscribe(listener: Listener): () => void {
    listeners.add(listener);
    return () => listeners.delete(listener);
  },
  /** Called by the host on mount/unmount, so requests without a UI fail fast. */
  registerHost(): () => void {
    hosts += 1;
    return () => {
      hosts -= 1;
      if (hosts === 0) pending?.settle(false);
    };
  },
};
