import { useMemo, useState } from 'react';
import { Button, Dialog, Icon } from '@/components';
import { cn } from '@/lib/cn';

export interface CategoryPickerProps {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  categories: string[];
  selected: string[];
  onChange: (next: string[]) => void;
}

/**
 * Every category, searchable, multi-select (the natives' Categories menu: toggles
 * keep it open). Real lineups have 150+ categories, too many for a pill row.
 */
export function CategoryPicker({
  open,
  onOpenChange,
  categories,
  selected,
  onChange,
}: CategoryPickerProps) {
  const [query, setQuery] = useState('');
  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return q ? categories.filter((c) => c.toLowerCase().includes(q)) : categories;
  }, [categories, query]);
  const toggle = (c: string) =>
    onChange(selected.includes(c) ? selected.filter((x) => x !== c) : [...selected, c]);

  return (
    <Dialog
      open={open}
      onOpenChange={(o) => {
        if (!o) setQuery('');
        onOpenChange(o);
      }}
      title="Categories"
      description={
        selected.length
          ? `${selected.length} selected of ${categories.length}`
          : `${categories.length} categories`
      }
      footer={
        <>
          <Button variant="ghost" disabled={!selected.length} onClick={() => onChange([])}>
            Clear
          </Button>
          <Button onClick={() => onOpenChange(false)}>Done</Button>
        </>
      }
    >
      <label className="relative mb-3 flex items-center">
        <span className="sr-only">Search categories</span>
        <Icon name="search" size={16} className="pointer-events-none absolute left-3 text-faint" />
        <input
          type="search"
          autoFocus
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder="Search categories"
          className="h-10 w-full rounded-xl border border-line bg-surface pr-3 pl-9 text-sm placeholder:text-faint focus:border-accent focus:outline-none"
        />
      </label>
      <ul
        aria-label="Categories"
        className="-mx-2 max-h-[min(55vh,28rem)] overflow-y-auto overscroll-contain"
      >
        {shown.map((c) => {
          const on = selected.includes(c);
          return (
            <li key={c}>
              <button
                type="button"
                role="checkbox"
                aria-checked={on}
                onClick={() => toggle(c)}
                className={cn(
                  'flex w-full cursor-pointer items-center gap-3 rounded-lg px-2 py-2 text-left text-sm transition hover:bg-surface-strong focus-visible:outline-2 focus-visible:outline-accent',
                  on ? 'text-fg' : 'text-muted',
                )}
              >
                <span
                  className={cn(
                    'flex h-5 w-5 shrink-0 items-center justify-center rounded-md border',
                    on ? 'border-accent bg-accent text-white' : 'border-line',
                  )}
                >
                  {on && <Icon name="check" size={14} />}
                </span>
                <span className="min-w-0 flex-1 truncate">{c}</span>
              </button>
            </li>
          );
        })}
        {!shown.length && (
          <li className="px-2 py-6 text-center text-sm text-faint">No categories match.</li>
        )}
      </ul>
    </Dialog>
  );
}
