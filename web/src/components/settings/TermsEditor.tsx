import { useState } from 'react';
import { Icon } from '@/components/icons';

/** Comma/enter separated chips for a list of filter terms. */
export function TermsEditor({
  label,
  terms,
  onChange,
  placeholder,
}: {
  label: string;
  terms: string[];
  onChange: (next: string[]) => void;
  placeholder?: string;
}) {
  const [draft, setDraft] = useState('');
  const commit = () => {
    const added = draft
      .split(',')
      .map((t) => t.trim())
      .filter(Boolean)
      .filter((t) => !terms.some((x) => x.toLowerCase() === t.toLowerCase()));
    setDraft('');
    if (added.length) onChange([...terms, ...added]);
  };
  return (
    <div className="flex flex-wrap items-center gap-1.5 rounded-xl border border-line bg-elevated p-2 focus-within:border-accent">
      {terms.map((t) => (
        <span
          key={t}
          className="inline-flex h-7 items-center gap-1 rounded-full bg-surface-strong pr-1 pl-2.5 text-xs"
        >
          {t}
          <button
            type="button"
            aria-label={`Remove ${t}`}
            onClick={() => onChange(terms.filter((x) => x !== t))}
            className="flex h-5 w-5 cursor-pointer items-center justify-center rounded-full text-muted hover:bg-surface hover:text-fg"
          >
            <Icon name="close" size={12} />
          </button>
        </span>
      ))}
      <input
        aria-label={label}
        value={draft}
        placeholder={terms.length ? '' : (placeholder ?? 'Add a term')}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ',') {
            e.preventDefault();
            commit();
          } else if (e.key === 'Backspace' && !draft && terms.length) {
            onChange(terms.slice(0, -1));
          }
        }}
        onBlur={commit}
        className="h-7 min-w-32 flex-1 bg-transparent px-1 text-sm outline-none placeholder:text-faint"
      />
    </div>
  );
}
