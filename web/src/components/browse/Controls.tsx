import { useEffect, useRef, useState, type ReactNode } from 'react';
import { cn } from '@/lib/cn';
import { Glyph } from './Glyph';

/** Labelled native <select> styled as a glass pill (keyboard + screen-reader friendly). */
export function SelectPill<T extends string>({
  label,
  value,
  options,
  onChange,
  className,
}: {
  label: string;
  value: T;
  options: { value: T; label: string }[];
  onChange: (value: T) => void;
  className?: string;
}) {
  return (
    <label
      className={cn(
        'relative inline-flex h-9 items-center rounded-full border border-line bg-surface pr-8 pl-3 text-sm',
        'transition hover:border-line-strong hover:bg-surface-strong focus-within:ring-2 focus-within:ring-accent',
        className,
      )}
    >
      <span className="mr-1.5 text-muted">{label}</span>
      <select
        aria-label={label}
        value={value}
        onChange={(e) => onChange(e.target.value as T)}
        className="cursor-pointer appearance-none bg-transparent font-medium text-fg outline-none [&>option]:bg-elevated"
      >
        {options.map((o) => (
          <option key={o.value} value={o.value}>
            {o.label}
          </option>
        ))}
      </select>
      <svg
        className="pointer-events-none absolute right-3 text-muted"
        width="14"
        height="14"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        aria-hidden="true"
      >
        <path d="M6 9l6 6 6-6" />
      </svg>
    </label>
  );
}

/** Segmented control (radio group) for a handful of exclusive options. */
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: T;
  options: { value: T; label: string }[];
  onChange: (value: T) => void;
}) {
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className="inline-flex h-9 items-center rounded-full border border-line bg-surface p-0.5"
    >
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            onClick={() => onChange(o.value)}
            className={cn(
              'h-8 cursor-pointer rounded-full px-3.5 text-sm font-medium transition outline-none',
              'focus-visible:ring-2 focus-visible:ring-accent',
              active ? 'accent-soft text-fg' : 'text-muted hover:text-fg',
            )}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

/** Sort direction toggle. */
export function DirectionToggle({
  ascending,
  onToggle,
  disabled,
}: {
  ascending: boolean;
  onToggle: () => void;
  disabled?: boolean;
}) {
  const label = ascending ? 'Ascending' : 'Descending';
  return (
    <button
      type="button"
      onClick={onToggle}
      disabled={disabled}
      aria-label={`Sort direction: ${label}`}
      title={label}
      className={cn(
        'inline-flex h-9 w-9 cursor-pointer items-center justify-center rounded-full border border-line bg-surface text-muted transition',
        'hover:border-line-strong hover:text-fg focus-visible:ring-2 focus-visible:ring-accent outline-none',
        'disabled:pointer-events-none disabled:opacity-40',
      )}
    >
      <Glyph name={ascending ? 'sortAsc' : 'sortDesc'} />
    </button>
  );
}

/** Toggleable chip (genre facets). */
export function Chip({
  active,
  onClick,
  children,
}: {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        'h-8 shrink-0 cursor-pointer rounded-full border px-3 text-[13px] font-medium transition outline-none',
        'focus-visible:ring-2 focus-visible:ring-accent',
        active
          ? 'accent-soft border-transparent text-fg'
          : 'border-line text-muted hover:border-line-strong hover:text-fg',
      )}
    >
      {children}
    </button>
  );
}

/** A–Z index under a Name sort. Letters missing from `available` are disabled. */
export function LetterIndex({
  letters,
  value,
  onChange,
}: {
  letters: string[];
  value?: string;
  onChange: (letter: string | undefined) => void;
}) {
  if (!letters.length) return null;
  const all = ['#', ...'ABCDEFGHIJKLMNOPQRSTUVWXYZ'];
  const have = new Set(letters.map((l) => l.toUpperCase()));
  return (
    <nav aria-label="Jump to letter" className="flex flex-wrap gap-1">
      <LetterButton active={!value} onClick={() => onChange(undefined)}>
        All
      </LetterButton>
      {all.map((l) => (
        <LetterButton
          key={l}
          active={value?.toUpperCase() === l}
          disabled={!have.has(l) && value?.toUpperCase() !== l}
          onClick={() => onChange(l)}
        >
          {l}
        </LetterButton>
      ))}
    </nav>
  );
}

function LetterButton({
  active,
  disabled,
  onClick,
  children,
}: {
  active: boolean;
  disabled?: boolean;
  onClick: () => void;
  children: ReactNode;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      disabled={disabled}
      onClick={onClick}
      className={cn(
        'h-7 min-w-7 cursor-pointer rounded-md px-1.5 text-xs font-semibold transition outline-none',
        'focus-visible:ring-2 focus-visible:ring-accent disabled:cursor-default disabled:opacity-25',
        active ? 'bg-accent text-accent-contrast' : 'text-muted hover:bg-surface hover:text-fg',
      )}
    >
      {children}
    </button>
  );
}

/** Search field that reports its value after a short pause. */
export function DebouncedSearch({
  value,
  onChange,
  placeholder,
  label,
}: {
  value: string;
  onChange: (value: string) => void;
  placeholder: string;
  label: string;
}) {
  const [text, setText] = useState(value);
  const last = useRef(value);
  useEffect(() => {
    if (value !== last.current) {
      last.current = value;
      setText(value);
    }
  }, [value]);
  useEffect(() => {
    if (text === last.current) return;
    const t = setTimeout(() => {
      last.current = text;
      onChange(text);
    }, 300);
    return () => clearTimeout(t);
  }, [text, onChange]);
  return (
    <label className="relative inline-flex h-9 w-full items-center sm:w-64">
      <span className="sr-only">{label}</span>
      <svg
        className="pointer-events-none absolute left-3 text-muted"
        width="16"
        height="16"
        viewBox="0 0 24 24"
        fill="none"
        stroke="currentColor"
        strokeWidth="2"
        aria-hidden="true"
      >
        <path d="M11 4a7 7 0 1 0 0 14 7 7 0 0 0 0-14zm9 16-4.35-4.35" />
      </svg>
      <input
        type="search"
        value={text}
        placeholder={placeholder}
        onChange={(e) => setText(e.target.value)}
        className="h-9 w-full rounded-full border border-line bg-surface pr-3 pl-9 text-sm outline-none transition placeholder:text-faint hover:border-line-strong focus:ring-2 focus:ring-accent"
      />
    </label>
  );
}
