import { useDocumentTitle } from '@/lib/documentTitle';
import { createFileRoute, redirect, useNavigate } from '@tanstack/react-router';
import { useState, type FormEvent } from 'react';
import { login } from '@/api/auth';
import { ApiError } from '@/api/client';
import { sessionStore } from '@/auth/sessionStore';
import { Button } from '@/components';
import { AuthLayout, Field } from '@/components/AuthLayout';

interface LoginSearch {
  redirect?: string;
}

export const Route = createFileRoute('/login')({
  validateSearch: (s: Record<string, unknown>): LoginSearch => ({
    redirect: typeof s.redirect === 'string' ? s.redirect : undefined,
  }),
  beforeLoad: ({ search }) => {
    if (sessionStore.getState().session)
      throw redirect({ to: '/profiles', search: { redirect: search.redirect } });
  },
  component: LoginPage,
});

function messageFor(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === 'invalid_credentials') return 'Incorrect username or password.';
    if (err.code === 'account_expired') return 'This account has expired.';
    if (err.status === 429) return 'Too many sign-in attempts. Please wait a moment and try again.';
    return err.message;
  }
  return 'Could not reach the server. Check your connection and try again.';
}

export function LoginPage() {
  useDocumentTitle('Sign in');
  const navigate = useNavigate();
  const { redirect: redirectTo } = Route.useSearch();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [needsNewPassword, setNeedsNewPassword] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      const session = await login({
        username: username.trim(),
        password,
        ...(needsNewPassword ? { newPassword } : {}),
      });
      sessionStore.login(session);
      await navigate({ to: '/profiles', search: { redirect: redirectTo } });
    } catch (err) {
      if (err instanceof ApiError && err.code === 'default_password') {
        setNeedsNewPassword(true);
        setError(
          'This server still uses the default admin password. Choose a new one to continue.',
        );
      } else {
        setError(messageFor(err));
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <AuthLayout title="Sign in" subtitle="Use your MediaStorm account.">
      <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
        <Field
          label="Username"
          autoComplete="username"
          value={username}
          onChange={setUsername}
          autoFocus
          required
        />
        <Field
          label="Password"
          type="password"
          autoComplete="current-password"
          value={password}
          onChange={setPassword}
          required
        />
        {needsNewPassword && (
          <Field
            label="New admin password"
            type="password"
            autoComplete="new-password"
            value={newPassword}
            onChange={setNewPassword}
            required
          />
        )}
        {error && (
          <p
            role="alert"
            className="rounded-xl bg-[color-mix(in_srgb,var(--danger)_14%,transparent)] px-3 py-2 text-sm text-danger"
          >
            {error}
          </p>
        )}
        <Button type="submit" size="lg" loading={busy} disabled={!username.trim() || !password}>
          Sign in
        </Button>
      </form>
    </AuthLayout>
  );
}
