import type React from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router';
import { useState } from 'react';
import { serverBasePath } from '@/api/basePath';
import {
  ACCENT_PRESETS,
  BADGES,
  TRACK_LANGUAGES,
  clearProfilePin,
  getPath,
  getSettings,
  putSettings,
  renameProfile,
  setPath,
  setProfileColor,
  setProfilePin,
  settingsKeys,
  type RawSettings,
} from '@/api/settings';
import { usersKeys } from '@/api/users';
import { useActiveProfile } from '@/auth/hooks';
import { useSessionState } from '@/auth/sessionStore';
import { Button } from '@/components/Button';
import { ErrorState, errorMessage } from '@/components/EmptyState';
import { Icon, type IconName } from '@/components/icons';
import { Page } from '@/components/Page';
import { Skeleton } from '@/components/Spinner';
import {
  SelectRow,
  SettingRow,
  SettingsCard,
  Switch,
  ToggleRow,
} from '@/components/settings/SettingsControls';
import {
  CalendarSourcesSection,
  FilteringSection,
  HomeShelvesEditor,
  MenuSpoilersSection,
  type SectionProps,
} from '@/components/settings/MoreSections';
import { cn } from '@/lib/cn';
import { DEFAULT_ACCENT, applyAccent, normalizeHex } from '@/lib/theme';

type Section = 'playback' | 'filtering' | 'appearance' | 'menu' | 'home' | 'calendar' | 'profile';
const SECTIONS: { id: Section; label: string; icon: IconName }[] = [
  { id: 'playback', label: 'Playback', icon: 'play' },
  { id: 'filtering', label: 'Filtering', icon: 'sortDesc' },
  { id: 'appearance', label: 'Appearance', icon: 'settings' },
  { id: 'menu', label: 'Menu & spoilers', icon: 'menu' },
  { id: 'home', label: 'Home shelves', icon: 'home' },
  { id: 'calendar', label: 'Calendar', icon: 'calendar' },
  { id: 'profile', label: 'Profile', icon: 'users' },
];

export const Route = createFileRoute('/_app/settings')({
  validateSearch: (raw: Record<string, unknown>): { section?: Section } => ({
    section: SECTIONS.some((s) => s.id === raw.section) ? (raw.section as Section) : undefined,
  }),
  component: SettingsPage,
});

function useProfileSettings(profileId: string) {
  const queryClient = useQueryClient();
  const key = settingsKeys.profile(profileId);
  const query = useQuery({
    queryKey: key,
    queryFn: ({ signal }) => getSettings(profileId, signal) as Promise<RawSettings>,
    staleTime: 5 * 60_000,
  });
  const mutation = useMutation({
    mutationFn: (next: RawSettings) => putSettings(profileId, next),
    onMutate: async (next) => {
      await queryClient.cancelQueries({ queryKey: key });
      const previous = queryClient.getQueryData<RawSettings>(key);
      queryClient.setQueryData(key, next);
      return { previous };
    },
    onError: (_e, _n, ctx) => {
      if (ctx?.previous) queryClient.setQueryData(key, ctx.previous);
    },
    onSuccess: (saved) => {
      if (saved && typeof saved === 'object') queryClient.setQueryData(key, saved);
    },
  });
  const update = (...pairs: [string, unknown][]) => {
    const current = queryClient.getQueryData<RawSettings>(key);
    if (!current) return;
    mutation.mutate(pairs.reduce((acc, [p, v]) => setPath(acc, p, v), current));
  };
  return { query, mutation, update, data: query.data };
}

function SettingsPage() {
  const section = Route.useSearch().section ?? 'playback';
  const navigate = useNavigate({ from: Route.fullPath });
  const { profileId, profile } = useActiveProfile();
  const { session } = useSessionState();
  const settings = useProfileSettings(profileId ?? '');

  const status = settings.mutation.isPending
    ? 'Saving…'
    : settings.mutation.isError
      ? `Couldn't save: ${errorMessage(settings.mutation.error)}`
      : settings.mutation.isSuccess
        ? 'Saved'
        : '';

  return (
    <Page
      title="Settings"
      subtitle={
        profile
          ? `Preferences for ${profile.name}. They follow this profile to every device.`
          : undefined
      }
      actions={
        <span
          aria-live="polite"
          className={cn('text-sm', settings.mutation.isError ? 'text-danger' : 'text-muted')}
        >
          {status}
        </span>
      }
    >
      <div className="grid gap-6 lg:grid-cols-[220px_minmax(0,1fr)]">
        <nav
          aria-label="Settings sections"
          className="flex gap-1 overflow-x-auto no-scrollbar lg:flex-col"
        >
          {SECTIONS.map((s) => (
            <button
              key={s.id}
              type="button"
              aria-current={section === s.id ? 'page' : undefined}
              onClick={() => navigate({ search: { section: s.id }, replace: true })}
              className={cn(
                'flex h-10 shrink-0 cursor-pointer items-center gap-3 rounded-xl px-3 text-sm font-medium transition',
                section === s.id ? 'accent-soft' : 'text-muted hover:bg-surface hover:text-fg',
              )}
            >
              <Icon name={s.icon} size={18} />
              {s.label}
            </button>
          ))}
          {session?.isMaster && (
            <a
              href={`${serverBasePath()}/admin`}
              className="flex h-10 shrink-0 items-center gap-3 rounded-xl px-3 text-sm font-medium text-muted transition hover:bg-surface hover:text-fg lg:mt-4 lg:border-t lg:border-line lg:pt-4 lg:h-auto"
            >
              <Icon name="lock" size={18} />
              Admin
              <Icon name="chevronRight" size={14} className="ml-auto hidden lg:block" />
            </a>
          )}
        </nav>
        <div className="min-w-0 max-w-3xl">
          {settings.query.isError ? (
            <ErrorState
              title="Couldn't load settings"
              error={settings.query.error}
              onRetry={() => settings.query.refetch()}
            />
          ) : !settings.data ? (
            <div className="space-y-3">
              <Skeleton className="h-16 rounded-2xl" />
              <Skeleton className="h-48 rounded-2xl" />
            </div>
          ) : section === 'playback' ? (
            <PlaybackSection data={settings.data} update={settings.update} />
          ) : section === 'appearance' ? (
            <AppearanceSection data={settings.data} update={settings.update} />
          ) : section === 'filtering' ? (
            <FilteringSection data={settings.data} update={settings.update} />
          ) : section === 'menu' ? (
            <MenuSpoilersSection data={settings.data} update={settings.update} />
          ) : section === 'home' ? (
            <HomeShelvesEditor data={settings.data} update={settings.update} />
          ) : section === 'calendar' ? (
            <CalendarSourcesSection data={settings.data} update={settings.update} />
          ) : (
            <ProfileSection />
          )}
        </div>
      </div>
    </Page>
  );
}

const LANG_OPTIONS = [
  { value: '', label: 'Default (original)' },
  ...TRACK_LANGUAGES.map((l) => ({ value: l.code, label: l.name })),
];

function PlaybackSection({ data, update }: SectionProps) {
  const str = (p: string) => getPath<string>(data, p) ?? '';
  return (
    <div className="flex flex-col gap-6">
      <SettingsCard
        title="Audio & subtitles"
        description="Tracks picked automatically when playback starts."
      >
        <SelectRow
          label="Audio language"
          value={str('playback.preferredAudioLanguage')}
          options={LANG_OPTIONS}
          onChange={(v) => update(['playback.preferredAudioLanguage', v || undefined])}
        />
        <SelectRow
          label="Subtitles"
          value={str('playback.preferredSubtitleMode') || 'off'}
          options={[
            { value: 'off', label: 'Off' },
            { value: 'on', label: 'On' },
            { value: 'forced-only', label: 'Forced only' },
          ]}
          onChange={(v) => update(['playback.preferredSubtitleMode', v])}
        />
        <SelectRow
          label="Subtitle language"
          value={str('playback.preferredSubtitleLanguage')}
          options={[
            { value: '', label: 'Same as audio' },
            ...TRACK_LANGUAGES.map((l) => ({ value: l.code, label: l.name })),
          ]}
          onChange={(v) => update(['playback.preferredSubtitleLanguage', v || undefined])}
        />
      </SettingsCard>
      <SettingsCard title="Quality" description="Limits applied when sources are searched.">
        <SelectRow
          label="Maximum resolution"
          value={str('filtering.maxResolution')}
          options={[
            { value: '', label: 'No limit' },
            { value: '2160p', label: '4K (2160p)' },
            { value: '1080p', label: '1080p' },
            { value: '720p', label: '720p' },
          ]}
          onChange={(v) => update(['filtering.maxResolution', v || undefined])}
        />
        <SelectRow
          label="HDR / Dolby Vision"
          value={str('filtering.hdrDvPolicy') || 'none'}
          options={[
            { value: 'none', label: 'Include everything' },
            { value: 'hdr', label: 'HDR and DV 7/8 only' },
            { value: 'hdr_dv', label: 'All HDR and DV' },
          ]}
          onChange={(v) => update(['filtering.hdrDvPolicy', v])}
        />
      </SettingsCard>
      <SettingsCard title="Behaviour">
        <ToggleRow
          label="Prepare streams when a details page opens"
          description="Starts resolving the best source in the background so play starts faster."
          checked={!getPath<boolean>(data, 'playback.disablePrequeue')}
          onChange={(v) => update(['playback.disablePrequeue', !v])}
        />
        <ToggleRow
          label="Skip credits automatically"
          description="Plays the next episode as soon as the credits start."
          checked={!!getPath<boolean>(data, 'playback.creditsAutoSkip')}
          onChange={(v) => update(['playback.creditsAutoSkip', v])}
        />
      </SettingsCard>
    </div>
  );
}

function AppearanceSection({ data, update }: SectionProps) {
  const current =
    normalizeHex(getPath<string>(data, 'display.appearance.accentColor')) ?? DEFAULT_ACCENT;
  const [custom, setCustom] = useState(current);
  const badges = getPath<string[]>(data, 'display.badgeVisibility') ?? [];
  const setAccent = (hex: string) => {
    applyAccent(hex);
    setCustom(hex);
    update(['display.appearance.accentColor', hex]);
  };
  return (
    <div className="flex flex-col gap-6">
      <SettingsCard
        title="Accent colour"
        description="Used for highlights, buttons and the active page."
      >
        <div
          className="flex flex-wrap items-center gap-3 px-5 py-5"
          role="radiogroup"
          aria-label="Accent colour"
        >
          {ACCENT_PRESETS.map((hex) => (
            <button
              key={hex}
              type="button"
              role="radio"
              aria-checked={current === hex}
              aria-label={hex}
              onClick={() => setAccent(hex)}
              className={cn(
                'h-10 w-10 cursor-pointer rounded-full ring-offset-2 ring-offset-[var(--bg)] transition hover:scale-110',
                current === hex && 'ring-2 ring-fg',
              )}
              style={{ background: hex }}
            />
          ))}
          <label className="ml-1 flex items-center gap-2 text-sm text-muted">
            <input
              type="color"
              value={custom}
              aria-label="Custom accent colour"
              onChange={(e) => {
                setCustom(e.target.value);
                applyAccent(e.target.value);
              }}
              onBlur={(e) => normalizeHex(e.target.value) !== current && setAccent(e.target.value)}
              className="h-10 w-10 cursor-pointer rounded-full border border-line bg-transparent"
            />
            Custom
          </label>
          <Button
            variant="ghost"
            size="sm"
            className="ml-auto"
            onClick={() => {
              applyAccent(null);
              setCustom(DEFAULT_ACCENT);
              update(['display.appearance.accentColor', undefined]);
            }}
          >
            Reset
          </Button>
        </div>
        <div className="flex items-center gap-3 px-5 py-4">
          <Button size="sm">Preview</Button>
          <span className="rounded-full accent-soft px-3 py-1 text-xs font-medium">
            Active pill
          </span>
          <Switch checked onChange={() => undefined} label="Preview switch" />
        </div>
      </SettingsCard>
      <SettingsCard title="Card badges" description="What poster and episode cards show.">
        {BADGES.map((b) => (
          <ToggleRow
            key={b.id}
            label={b.label}
            checked={badges.includes(b.id)}
            onChange={(v) =>
              update([
                'display.badgeVisibility',
                v ? [...badges, b.id] : badges.filter((x) => x !== b.id),
              ])
            }
          />
        ))}
      </SettingsCard>
    </div>
  );
}

const PROFILE_COLORS = [
  '#3f66ff',
  '#e0567a',
  '#2ecc71',
  '#ff8a3d',
  '#7c4dff',
  '#14b8a6',
  '#f1c40f',
  '#ef5350',
];

function ProfileSection() {
  const { profileId, profile } = useActiveProfile();
  const queryClient = useQueryClient();
  const [name, setName] = useState(profile?.name ?? '');
  const [pin, setPin] = useState('');
  const refresh = () => queryClient.invalidateQueries({ queryKey: usersKeys.list() });
  const rename = useMutation({
    mutationFn: (n: string) => renameProfile(profileId!, n),
    onSuccess: refresh,
  });
  const color = useMutation({
    mutationFn: (c: string) => setProfileColor(profileId!, c),
    onSuccess: refresh,
  });
  const savePin = useMutation({
    mutationFn: (p: string) => setProfilePin(profileId!, p),
    onSuccess: () => {
      setPin('');
      return refresh();
    },
  });
  const removePin = useMutation({
    mutationFn: () => clearProfilePin(profileId!),
    onSuccess: refresh,
  });
  if (!profile) return <Skeleton className="h-48 rounded-2xl" />;
  const err = rename.error ?? color.error ?? savePin.error ?? removePin.error;
  return (
    <div className="flex flex-col gap-6">
      <SettingsCard title="This profile">
        <SettingRow label="Name" htmlFor="profile-name">
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (name.trim() && name.trim() !== profile.name) rename.mutate(name.trim());
            }}
          >
            <input
              id="profile-name"
              value={name}
              maxLength={40}
              onChange={(e) => setName(e.target.value)}
              className="h-10 w-full rounded-xl border border-line bg-elevated px-3 text-sm outline-none focus:border-accent sm:w-56"
            />
            <Button
              type="submit"
              variant="secondary"
              loading={rename.isPending}
              disabled={!name.trim() || name.trim() === profile.name}
            >
              Save
            </Button>
          </form>
        </SettingRow>
        <SettingRow label="Colour">
          <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Profile colour">
            {PROFILE_COLORS.map((c) => (
              <button
                key={c}
                type="button"
                role="radio"
                aria-checked={profile.color?.toLowerCase() === c}
                aria-label={c}
                onClick={() => color.mutate(c)}
                className={cn(
                  'h-8 w-8 cursor-pointer rounded-full ring-offset-2 ring-offset-[var(--bg)] transition hover:scale-110',
                  profile.color?.toLowerCase() === c && 'ring-2 ring-fg',
                )}
                style={{ background: c }}
              />
            ))}
          </div>
        </SettingRow>
        <SettingRow
          label="PIN"
          description={
            profile.hasPin
              ? 'A PIN is required to open this profile.'
              : 'Anyone on this account can open this profile.'
          }
          htmlFor="profile-pin"
        >
          <form
            className="flex gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              if (/^\d{4,8}$/.test(pin)) savePin.mutate(pin);
            }}
          >
            <input
              id="profile-pin"
              inputMode="numeric"
              // Masked text, not type="password": keeps password managers / iOS
              // AutoFill from treating the PIN as a credential.
              type="text"
              autoComplete="off"
              style={{ WebkitTextSecurity: 'disc' } as React.CSSProperties}
              data-1p-ignore
              data-lpignore="true"
              data-bwignore
              data-form-type="other"
              placeholder={profile.hasPin ? 'New PIN' : '4–8 digits'}
              value={pin}
              onChange={(e) => setPin(e.target.value.replace(/\D/g, '').slice(0, 8))}
              className="h-10 w-32 rounded-xl border border-line bg-elevated px-3 text-sm tracking-widest outline-none focus:border-accent"
            />
            <Button
              type="submit"
              variant="secondary"
              loading={savePin.isPending}
              disabled={!/^\d{4,8}$/.test(pin)}
            >
              {profile.hasPin ? 'Change' : 'Set'}
            </Button>
            {profile.hasPin && (
              <Button
                type="button"
                variant="ghost"
                loading={removePin.isPending}
                onClick={() => removePin.mutate()}
              >
                Remove
              </Button>
            )}
          </form>
        </SettingRow>
      </SettingsCard>
      {err ? (
        <p role="alert" className="text-sm text-danger">
          {errorMessage(err)}
        </p>
      ) : null}
      <SettingsCard title="Profiles">
        <SettingRow label="Switch profile" description="Pick another profile on this account.">
          <Link
            to="/profiles"
            className="inline-flex h-10 items-center gap-2 rounded-xl border border-line bg-surface-strong px-4 text-sm font-medium hover:brightness-110"
          >
            <Icon name="users" size={16} /> Profiles
          </Link>
        </SettingRow>
      </SettingsCard>
    </div>
  );
}
