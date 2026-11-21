import { useId, type ReactNode } from 'react';
import { Brand } from './shell/Sidebar';

/** Centered glass card used by login and the profile picker. */
export function AuthLayout({
  title,
  subtitle,
  wide,
  children,
}: {
  title: string;
  subtitle?: ReactNode;
  wide?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="flex min-h-full flex-col items-center justify-center px-4 py-12">
      <div className="mb-8">
        <Brand />
      </div>
      <div
        className={
          wide
            ? 'w-full max-w-3xl text-center'
            : 'glass w-full max-w-sm rounded-2xl p-7 shadow-card'
        }
      >
        <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="mt-1 mb-6 text-sm text-muted">{subtitle}</p>}
        {children}
      </div>
    </div>
  );
}

export interface FieldProps {
  label: string;
  value: string;
  onChange: (value: string) => void;
  type?: string;
  autoComplete?: string;
  autoFocus?: boolean;
  required?: boolean;
  inputMode?: 'numeric' | 'text';
  maxLength?: number;
}

export function Field({ label, onChange, ...rest }: FieldProps) {
  const id = useId();
  return (
    <div className="flex flex-col gap-1.5 text-left">
      <label htmlFor={id} className="text-sm font-medium text-muted">
        {label}
      </label>
      <input
        id={id}
        {...rest}
        onChange={(e) => onChange(e.target.value)}
        className="h-11 rounded-xl border border-line bg-surface px-3.5 text-fg outline-none transition focus:border-accent focus:bg-surface-strong"
      />
    </div>
  );
}
