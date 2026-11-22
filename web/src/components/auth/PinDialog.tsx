import { useState, type FormEvent } from 'react';
import { isWrongPinError } from '@/api/client';
import { verifyPin } from '@/api/users';
import { sessionStore } from '@/auth/sessionStore';
import { Field } from '../AuthLayout';
import { Button } from '../Button';
import { Dialog } from '../Dialog';
import { errorMessage } from '../EmptyState';

/** PIN entry for a profile: verifies, stores the unlock token, then calls onUnlocked. */
export function PinDialog({
  profileId,
  profileName,
  pinLength,
  description,
  onClose,
  onUnlocked,
}: {
  profileId: string;
  profileName?: string;
  pinLength?: number;
  description?: string;
  onClose: () => void;
  onUnlocked: () => void;
}) {
  const [pin, setPin] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const result = await verifyPin(profileId, pin);
      if (!result.valid) throw new Error('Incorrect PIN.');
      if (result.unlockToken)
        sessionStore.setUnlock(profileId, result.unlockToken, result.expiresAt);
      onUnlocked();
    } catch (err) {
      setError(isWrongPinError(err) ? 'Incorrect PIN.' : errorMessage(err));
      setPin('');
    } finally {
      setBusy(false);
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={profileName ? `Enter PIN for ${profileName}` : 'Enter profile PIN'}
      description={description}
      size="sm"
    >
      <form onSubmit={submit} className="flex flex-col gap-4">
        <Field
          label="PIN"
          type="password"
          inputMode="numeric"
          autoComplete="off"
          autoFocus
          value={pin}
          maxLength={pinLength || 12}
          onChange={(v) => setPin(v.replace(/\D/g, ''))}
        />
        {error && (
          <p role="alert" className="text-sm text-danger">
            {error}
          </p>
        )}
        <Button type="submit" loading={busy} disabled={!pin}>
          Unlock
        </Button>
      </form>
    </Dialog>
  );
}
