import { cn } from '@/lib/cn';

export interface SegmentedOption<T extends string> {
  value: T;
  label: string;
}

export interface SegmentedProps<T extends string> {
  label: string;
  value: T;
  options: SegmentedOption<T>[];
  onChange: (value: T) => void;
  size?: 'sm' | 'md';
  className?: string;
}

/** Pill-style single-choice group (radiogroup semantics, arrow keys move). */
export function Segmented<T extends string>({
  label,
  value,
  options,
  onChange,
  size = 'md',
  className,
}: SegmentedProps<T>) {
  const index = options.findIndex((o) => o.value === value);
  return (
    <div
      role="radiogroup"
      aria-label={label}
      className={cn(
        'inline-flex max-w-full overflow-x-auto no-scrollbar rounded-full border border-line bg-surface p-1',
        className,
      )}
      onKeyDown={(e) => {
        if (e.key !== 'ArrowRight' && e.key !== 'ArrowLeft') return;
        e.preventDefault();
        const next = (index + (e.key === 'ArrowRight' ? 1 : -1) + options.length) % options.length;
        onChange(options[next]!.value);
        const buttons = e.currentTarget.querySelectorAll<HTMLButtonElement>('button');
        buttons[next]?.focus();
      }}
    >
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            tabIndex={active ? 0 : -1}
            onClick={() => onChange(o.value)}
            className={cn(
              'shrink-0 cursor-pointer rounded-full font-medium whitespace-nowrap transition',
              size === 'sm' ? 'h-7 px-3 text-xs' : 'h-8 px-4 text-sm',
              active ? 'accent-soft' : 'text-muted hover:text-fg hover:bg-surface',
            )}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}
