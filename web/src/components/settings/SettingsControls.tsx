import { useId, type ReactNode } from 'react';
import { cn } from '@/lib/cn';

export function SettingsCard({
  title,
  description,
  children,
}: {
  title: string;
  description?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="rounded-2xl border border-line bg-surface">
      <header className="border-b border-line px-5 py-4">
        <h2 className="text-base font-semibold">{title}</h2>
        {description && <p className="mt-0.5 text-sm text-muted">{description}</p>}
      </header>
      <div className="divide-y divide-line">{children}</div>
    </section>
  );
}

export function SettingRow({
  label,
  description,
  htmlFor,
  children,
}: {
  label: string;
  description?: ReactNode;
  htmlFor?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-3 px-5 py-4 sm:flex-row sm:items-center sm:justify-between sm:gap-6">
      <div className="min-w-0">
        <label htmlFor={htmlFor} className="text-sm font-medium">
          {label}
        </label>
        {description && <p className="mt-0.5 text-xs text-muted">{description}</p>}
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}

export function Switch({
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
        'relative inline-flex h-6 w-11 shrink-0 cursor-pointer items-center rounded-full transition disabled:opacity-50',
        checked ? 'bg-accent' : 'bg-surface-strong border border-line-strong',
      )}
    >
      <span
        className={cn(
          'inline-block h-5 w-5 rounded-full bg-white shadow transition-transform',
          checked ? 'translate-x-[22px]' : 'translate-x-0.5',
        )}
      />
    </button>
  );
}

export function ToggleRow(props: {
  label: string;
  description?: ReactNode;
  checked: boolean;
  onChange: (v: boolean) => void;
}) {
  return (
    <SettingRow label={props.label} description={props.description}>
      <Switch checked={props.checked} onChange={props.onChange} label={props.label} />
    </SettingRow>
  );
}

export function SelectRow({
  label,
  description,
  value,
  options,
  onChange,
}: {
  label: string;
  description?: ReactNode;
  value: string;
  options: { value: string; label: string }[];
  onChange: (v: string) => void;
}) {
  const id = useId();
  return (
    <SettingRow label={label} description={description} htmlFor={id}>
      <select
        id={id}
        value={value}
        onChange={(e) => onChange(e.target.value)}
        className="h-10 w-full min-w-48 cursor-pointer rounded-xl border border-line bg-elevated px-3 text-sm text-fg outline-none hover:border-line-strong focus:border-accent sm:w-auto"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
    </SettingRow>
  );
}
