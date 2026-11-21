import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState, type FormEvent } from 'react';
import {
  createRegexRule,
  deleteRule,
  fetchRecordingSettings,
  fetchRules,
  recordingKeys,
  ruleSummary,
  saveRecordingSettings,
  updateRule,
  type RecordingRule,
  type RuleUpdate,
} from '@/api/recordings';
import { Button, Dialog, EmptyState, ErrorState, errorMessage, Skeleton } from '@/components';
import { LiveIcon } from '@/components/live/LiveBits';
import { cn } from '@/lib/cn';

const toMin = (s: number) => Math.round(s / 60);

function Toggle({
  checked,
  onChange,
  label,
  disabled,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  label: string;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-label={label}
      disabled={disabled}
      onClick={() => onChange(!checked)}
      className={cn(
        'relative h-6 w-11 shrink-0 cursor-pointer rounded-full transition focus-visible:outline-2 focus-visible:outline-accent disabled:opacity-50',
        checked ? 'bg-accent' : 'bg-surface-strong',
      )}
    >
      <span
        className={cn(
          'absolute top-0.5 left-0.5 h-5 w-5 rounded-full bg-white shadow transition',
          checked && 'translate-x-5',
        )}
      />
    </button>
  );
}

function MinutesField({
  label,
  value,
  onChange,
}: {
  label: string;
  value: number;
  onChange: (v: number) => void;
}) {
  return (
    <label className="flex flex-col gap-1 text-sm">
      <span className="text-muted">{label}</span>
      <span className="flex items-center gap-2">
        <input
          type="number"
          min={0}
          max={240}
          value={value}
          onChange={(e) => onChange(Math.max(0, Number(e.target.value) || 0))}
          className="h-10 w-24 rounded-xl border border-line bg-surface px-3 focus:border-accent focus:outline-none"
        />
        <span className="text-faint">min</span>
      </span>
    </label>
  );
}

export function RulesPanel({
  profileId,
  onNotice,
}: {
  profileId: string;
  onNotice: (t: string, danger?: boolean) => void;
}) {
  const qc = useQueryClient();
  const rules = useQuery({
    queryKey: recordingKeys.rules(profileId),
    queryFn: ({ signal }) => fetchRules(profileId, signal),
  });
  const settings = useQuery({
    queryKey: recordingKeys.settings(profileId),
    queryFn: ({ signal }) => fetchRecordingSettings(profileId, signal),
  });
  const invalidate = () => qc.invalidateQueries({ queryKey: recordingKeys.all(profileId) });
  const onError = (e: unknown) => onNotice(errorMessage(e), true);

  const update = useMutation({
    mutationFn: ({ id, patch }: { id: string; patch: RuleUpdate }) =>
      updateRule(profileId, id, patch),
    onMutate: async ({ id, patch }) => {
      const key = recordingKeys.rules(profileId);
      await qc.cancelQueries({ queryKey: key });
      const prev = qc.getQueryData<RecordingRule[]>(key);
      qc.setQueryData<RecordingRule[]>(key, (list) =>
        list?.map((r) => (r.id === id ? { ...r, ...patch } : r)),
      );
      return { prev };
    },
    onError: (e, _v, ctx) => {
      if (ctx?.prev) qc.setQueryData(recordingKeys.rules(profileId), ctx.prev);
      onError(e);
    },
    onSettled: invalidate,
  });
  const remove = useMutation({
    mutationFn: (id: string) => deleteRule(id),
    onSuccess: () => onNotice('Recording rule deleted.'),
    onError,
    onSettled: invalidate,
  });
  const create = useMutation({
    mutationFn: (pattern: string) => createRegexRule(profileId, pattern),
    onSuccess: () => onNotice('Rule added.'),
    onError,
    onSettled: invalidate,
  });

  const [pattern, setPattern] = useState('');
  const patternError = (() => {
    if (!pattern.trim()) return null;
    try {
      new RegExp(pattern);
      return null;
    } catch {
      return 'Not a valid regular expression.';
    }
  })();
  const submit = (e: FormEvent) => {
    e.preventDefault();
    const p = pattern.trim();
    if (!p || patternError) return;
    create.mutate(p, { onSuccess: () => setPattern('') });
  };

  const [editing, setEditing] = useState<RecordingRule | null>(null);
  const [confirmDelete, setConfirmDelete] = useState<RecordingRule | null>(null);

  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_340px]">
      <section aria-labelledby="rules-h" className="min-w-0">
        <h2 id="rules-h" className="mb-3 text-lg font-semibold">
          Recording rules
        </h2>
        {rules.isPending ? (
          <div className="flex flex-col gap-2">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-20 rounded-2xl" />
            ))}
          </div>
        ) : rules.isError ? (
          <ErrorState error={rules.error} onRetry={() => void rules.refetch()} />
        ) : !rules.data.length ? (
          <EmptyState
            icon="record"
            title="No recording rules"
            description="Use Record Series on a programme in the Live TV guide, or add a title pattern here."
            className="rounded-2xl border border-dashed border-line"
          />
        ) : (
          <ul className="flex flex-col gap-2">
            {rules.data.map((rule) => (
              <li
                key={rule.id}
                className={cn(
                  'flex flex-col gap-3 rounded-2xl border border-line bg-surface p-4 sm:flex-row sm:items-center',
                  !rule.enabled && 'opacity-60',
                )}
              >
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="truncate font-semibold">
                      {rule.matchType === 'regex' ? (
                        <code className="rounded bg-surface-strong px-1.5 py-0.5 text-sm">
                          /{rule.pattern}/
                        </code>
                      ) : (
                        rule.pattern
                      )}
                    </span>
                    <span className="rounded-full bg-surface-strong px-2 py-0.5 text-xs text-muted">
                      {rule.matchType === 'regex' ? 'Pattern' : 'Series'}
                    </span>
                    {!rule.enabled && (
                      <span className="rounded-full bg-surface-strong px-2 py-0.5 text-xs text-muted">
                        Paused
                      </span>
                    )}
                  </div>
                  <p className="mt-0.5 text-sm text-muted">
                    {rule.allChannels ? 'All channels' : (rule.channelName ?? 'One channel')}
                    {' · '}
                    {rule.newOnly ? 'New episodes only' : 'Every airing'}
                    {(rule.paddingBeforeSeconds || rule.paddingAfterSeconds) > 0 &&
                      ` · −${toMin(rule.paddingBeforeSeconds)}/+${toMin(rule.paddingAfterSeconds)} min`}
                  </p>
                </div>
                <div className="flex items-center gap-3">
                  <label className="flex items-center gap-2 text-sm text-muted">
                    New only
                    <Toggle
                      label={`New episodes only for ${rule.pattern}`}
                      checked={rule.newOnly}
                      onChange={(v) => update.mutate({ id: rule.id, patch: { newOnly: v } })}
                    />
                  </label>
                  <label className="flex items-center gap-2 text-sm text-muted">
                    On
                    <Toggle
                      label={`${rule.enabled ? 'Pause' : 'Resume'} ${rule.pattern}`}
                      checked={rule.enabled}
                      onChange={(v) => update.mutate({ id: rule.id, patch: { enabled: v } })}
                    />
                  </label>
                  <button
                    type="button"
                    onClick={() => setEditing(rule)}
                    aria-label={`Edit padding for ${rule.pattern}`}
                    className="flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg text-muted hover:bg-surface-strong hover:text-fg"
                  >
                    <LiveIcon name="pencil" size={16} />
                  </button>
                  <button
                    type="button"
                    onClick={() => setConfirmDelete(rule)}
                    aria-label={`Delete ${rule.pattern}`}
                    className="flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg text-muted hover:bg-surface-strong hover:text-danger"
                  >
                    <LiveIcon name="trash" size={16} />
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </section>

      <aside className="flex flex-col gap-4">
        <form onSubmit={submit} className="rounded-2xl border border-line bg-surface p-4">
          <h2 className="font-semibold">Add a title pattern</h2>
          <p className="mt-1 text-sm text-muted">
            Records every guide programme whose title matches, on any channel. Regular expression,
            case-insensitive.
          </p>
          <div className="mt-3 flex gap-2">
            <input
              value={pattern}
              onChange={(e) => setPattern(e.target.value)}
              placeholder="e.g. ^Formula 1"
              aria-label="Title pattern"
              aria-invalid={!!patternError}
              className="h-10 min-w-0 flex-1 rounded-xl border border-line bg-bg px-3 text-sm focus:border-accent focus:outline-none"
            />
            <Button
              type="submit"
              loading={create.isPending}
              disabled={!pattern.trim() || !!patternError}
              leftIcon={<LiveIcon name="plus" size={16} />}
            >
              Add
            </Button>
          </div>
          {patternError && <p className="mt-2 text-sm text-danger">{patternError}</p>}
        </form>
        <PaddingDefaults
          key={settings.data ? 'loaded' : 'loading'}
          loading={settings.isPending}
          before={toMin(settings.data?.paddingBeforeSeconds ?? 0)}
          after={toMin(settings.data?.paddingAfterSeconds ?? 0)}
          onSave={async (b, a) => {
            try {
              await saveRecordingSettings(profileId, {
                paddingBeforeSeconds: b * 60,
                paddingAfterSeconds: a * 60,
              });
              await qc.invalidateQueries({ queryKey: recordingKeys.settings(profileId) });
              onNotice('Default padding saved.');
            } catch (e) {
              onError(e);
            }
          }}
        />
      </aside>

      {editing && (
        <PaddingDialog
          rule={editing}
          onClose={() => setEditing(null)}
          onSave={(b, a) => {
            update.mutate({
              id: editing.id,
              patch: { paddingBeforeSeconds: b * 60, paddingAfterSeconds: a * 60 },
            });
            setEditing(null);
          }}
        />
      )}
      <Dialog
        open={!!confirmDelete}
        onOpenChange={(o) => !o && setConfirmDelete(null)}
        title="Delete recording rule?"
        description={
          confirmDelete
            ? `${ruleSummary(confirmDelete)}. Upcoming recordings it scheduled are cancelled; one already recording keeps going.`
            : undefined
        }
        size="sm"
        footer={
          <>
            <Button variant="ghost" onClick={() => setConfirmDelete(null)}>
              Keep
            </Button>
            <Button
              variant="danger"
              onClick={() => {
                if (confirmDelete) remove.mutate(confirmDelete.id);
                setConfirmDelete(null);
              }}
            >
              Delete rule
            </Button>
          </>
        }
      />
    </div>
  );
}

function PaddingDefaults({
  loading,
  before,
  after,
  onSave,
}: {
  loading: boolean;
  before: number;
  after: number;
  onSave: (before: number, after: number) => Promise<void>;
}) {
  const [b, setB] = useState(before);
  const [a, setA] = useState(after);
  const [saving, setSaving] = useState(false);
  const dirty = b !== before || a !== after;
  return (
    <div className="rounded-2xl border border-line bg-surface p-4">
      <h2 className="font-semibold">Default padding</h2>
      <p className="mt-1 text-sm text-muted">
        Added around every new recording, so a late start or overrun isn’t cut off.
      </p>
      {loading ? (
        <Skeleton className="mt-3 h-16" />
      ) : (
        <div className="mt-3 flex flex-wrap items-end gap-4">
          <MinutesField label="Start early" value={b} onChange={setB} />
          <MinutesField label="End late" value={a} onChange={setA} />
          <Button
            variant="secondary"
            disabled={!dirty}
            loading={saving}
            onClick={async () => {
              setSaving(true);
              await onSave(b, a);
              setSaving(false);
            }}
          >
            Save
          </Button>
        </div>
      )}
    </div>
  );
}

function PaddingDialog({
  rule,
  onClose,
  onSave,
}: {
  rule: RecordingRule;
  onClose: () => void;
  onSave: (before: number, after: number) => void;
}) {
  const [b, setB] = useState(toMin(rule.paddingBeforeSeconds));
  const [a, setA] = useState(toMin(rule.paddingAfterSeconds));
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title="Rule padding"
      description={ruleSummary(rule)}
      size="sm"
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={() => onSave(b, a)}>Save</Button>
        </>
      }
    >
      <div className="flex gap-4">
        <MinutesField label="Start early" value={b} onChange={setB} />
        <MinutesField label="End late" value={a} onChange={setA} />
      </div>
    </Dialog>
  );
}
