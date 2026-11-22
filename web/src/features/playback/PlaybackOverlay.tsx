import * as RadixDialog from '@radix-ui/react-dialog';
import { Button, Icon, Spinner } from '@/components';

export type PlaybackOverlayState =
  | { kind: 'idle' }
  | {
      kind: 'resolving';
      label: string;
      stage: string;
      detail?: string;
      imageUrl?: string;
    }
  | { kind: 'error'; label: string; message: string };

/**
 * Full-screen "finding a stream" card shown while a play request resolves (native
 * PlaybackCoordinator's loading art), with Cancel; and the failure message after.
 */
export function PlaybackOverlay({
  state,
  onCancel,
  onDismiss,
}: {
  state: PlaybackOverlayState;
  onCancel: () => void;
  onDismiss: () => void;
}) {
  const open = state.kind !== 'idle';
  return (
    <RadixDialog.Root
      open={open}
      onOpenChange={(o) => {
        if (o) return;
        if (state.kind === 'resolving') onCancel();
        else onDismiss();
      }}
    >
      <RadixDialog.Portal>
        <RadixDialog.Overlay className="fixed inset-0 z-50 bg-black/75 backdrop-blur-md" />
        <RadixDialog.Content
          aria-describedby={undefined}
          className="fixed top-1/2 left-1/2 z-50 w-[calc(100vw-2rem)] max-w-md -translate-x-1/2 -translate-y-1/2 overflow-hidden rounded-2xl glass shadow-card focus:outline-none"
        >
          {state.kind === 'resolving' && (
            <>
              <div className="relative aspect-video w-full bg-surface-strong">
                {state.imageUrl && (
                  <img src={state.imageUrl} alt="" className="h-full w-full object-cover" />
                )}
                <div className="absolute inset-0 bg-gradient-to-t from-black/90 via-black/40 to-transparent" />
                <div className="absolute inset-x-0 bottom-0 p-5 text-white">
                  <RadixDialog.Title className="text-lg font-semibold">
                    {state.label}
                  </RadixDialog.Title>
                </div>
              </div>
              <div className="flex items-center gap-4 p-5">
                <Spinner size={22} className="text-accent" />
                <div className="min-w-0 flex-1" aria-live="polite">
                  <div className="text-sm font-medium">{state.stage}</div>
                  {state.detail && (
                    <div className="truncate text-xs text-muted" title={state.detail}>
                      {state.detail}
                    </div>
                  )}
                </div>
                <Button variant="secondary" size="sm" onClick={onCancel}>
                  Cancel
                </Button>
              </div>
            </>
          )}
          {state.kind === 'error' && (
            <div className="flex flex-col items-center gap-3 p-6 text-center">
              <div className="flex h-12 w-12 items-center justify-center rounded-2xl bg-[color-mix(in_srgb,var(--danger)_16%,transparent)] text-danger">
                <Icon name="alert" size={24} />
              </div>
              <RadixDialog.Title className="text-lg font-semibold">
                Couldn’t play {state.label}
              </RadixDialog.Title>
              <p className="text-sm text-muted" role="alert">
                {state.message}
              </p>
              <Button variant="secondary" onClick={onDismiss} className="mt-2">
                Close
              </Button>
            </div>
          )}
        </RadixDialog.Content>
      </RadixDialog.Portal>
    </RadixDialog.Root>
  );
}
