import { useEffect, useMemo, useState } from 'react';
import type { LiveSource } from '@/api/live';
import { Icon } from '@/components';
import { cn } from '@/lib/cn';
import { CategoryPicker } from './CategoryPicker';

/** Categories shown as quick pills before the rest go behind the Categories dialog. */
export const QUICK_CATEGORIES = 8;

export type LiveView = 'guide' | 'channels';

export interface LiveToolbarProps {
  view: LiveView;
  guideAvailable: boolean;
  search: string;
  favoritesOnly: boolean;
  categories: string[];
  selected: string[];
  sources: LiveSource[];
  sourceId?: string;
  showHidden: boolean;
  hiddenCount: number;
  onView: (v: LiveView) => void;
  onSearch: (q: string) => void;
  onFavorites: (on: boolean) => void;
  onCategories: (c: string[]) => void;
  onSource: (id?: string) => void;
  onShowHidden: (on: boolean) => void;
}

const pill =
  'inline-flex h-8 shrink-0 cursor-pointer items-center gap-1.5 rounded-full border px-3 text-sm transition focus-visible:outline-2 focus-visible:outline-accent';
const pillOff = 'border-line bg-surface text-muted hover:bg-surface-strong hover:text-fg';
const pillOn =
  'border-[color-mix(in_srgb,var(--accent)_45%,transparent)] bg-[color-mix(in_srgb,var(--accent)_16%,transparent)] text-fg';

export function LiveToolbar(p: LiveToolbarProps) {
  const [text, setText] = useState(p.search);
  useEffect(() => setText(p.search), [p.search]);
  useEffect(() => {
    if (text === p.search) return;
    const id = window.setTimeout(() => p.onSearch(text.trim()), 300);
    return () => window.clearTimeout(id);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [text]);

  const [pickerOpen, setPickerOpen] = useState(false);
  const toggleCat = (c: string) =>
    p.onCategories(p.selected.includes(c) ? p.selected.filter((x) => x !== c) : [...p.selected, c]);
  // One scrolling row: the selected categories first, then the first few others (server
  // order); everything else is in the dialog, so 150+ categories never wall the page.
  const quick = useMemo(
    () => [
      ...p.selected,
      ...p.categories.filter((c) => !p.selected.includes(c)).slice(0, QUICK_CATEGORIES),
    ],
    [p.categories, p.selected],
  );
  const more = p.categories.length > QUICK_CATEGORIES;

  return (
    <div className="mb-4 flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <label className="relative flex min-w-0 flex-1 basis-60 items-center">
          <span className="sr-only">Search channels</span>
          <Icon
            name="search"
            size={16}
            className="pointer-events-none absolute left-3 text-faint"
          />
          <input
            type="search"
            value={text}
            onChange={(e) => setText(e.target.value)}
            placeholder="Search channels"
            className="h-10 w-full rounded-xl border border-line bg-surface pr-3 pl-9 text-sm placeholder:text-faint focus:border-accent focus:outline-none"
          />
        </label>
        {p.sources.length > 1 && (
          <label className="flex items-center">
            <span className="sr-only">Source</span>
            <select
              value={p.sourceId ?? ''}
              onChange={(e) => p.onSource(e.target.value || undefined)}
              className="h-10 cursor-pointer rounded-xl border border-line bg-surface px-3 text-sm focus:border-accent focus:outline-none"
            >
              <option value="">All sources</option>
              {p.sources.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </select>
          </label>
        )}
        <div
          role="group"
          aria-label="View"
          className="flex h-10 rounded-xl border border-line bg-surface p-1"
        >
          {(
            [
              ['guide', 'Guide', 'rows'],
              ['channels', 'Channels', 'grid'],
            ] as const
          ).map(([v, label, icon]) => (
            <button
              key={v}
              type="button"
              aria-pressed={p.view === v}
              disabled={v === 'guide' && !p.guideAvailable}
              onClick={() => p.onView(v)}
              className={cn(
                'flex cursor-pointer items-center gap-1.5 rounded-lg px-3 text-sm transition disabled:cursor-not-allowed disabled:opacity-40',
                p.view === v ? 'bg-surface-strong text-fg' : 'text-muted hover:text-fg',
              )}
            >
              <Icon name={icon} size={15} />
              {label}
            </button>
          ))}
        </div>
      </div>
      <div className="no-scrollbar -mx-4 flex gap-2 overflow-x-auto px-4 pb-1 sm:mx-0 sm:px-0">
        <button
          type="button"
          aria-pressed={!p.favoritesOnly && p.selected.length === 0}
          onClick={() => {
            p.onFavorites(false);
            p.onCategories([]);
          }}
          className={cn(pill, !p.favoritesOnly && p.selected.length === 0 ? pillOn : pillOff)}
        >
          All channels
        </button>
        <button
          type="button"
          aria-pressed={p.favoritesOnly}
          onClick={() => p.onFavorites(!p.favoritesOnly)}
          className={cn(pill, p.favoritesOnly ? pillOn : pillOff)}
        >
          <Icon name="star" size={14} filled={p.favoritesOnly} className="text-warning" />
          Favorites
        </button>
        {more && (
          <button
            type="button"
            aria-haspopup="dialog"
            onClick={() => setPickerOpen(true)}
            className={cn(pill, p.selected.length ? pillOn : pillOff)}
          >
            <Icon name="list" size={14} />
            {p.selected.length ? `Categories · ${p.selected.length}` : 'Categories'}
            <span className="text-faint">{p.categories.length}</span>
          </button>
        )}
        {quick.map((c) => (
          <button
            key={c}
            type="button"
            aria-pressed={p.selected.includes(c)}
            onClick={() => toggleCat(c)}
            className={cn(pill, p.selected.includes(c) ? pillOn : pillOff)}
          >
            {c}
          </button>
        ))}
        {p.hiddenCount > 0 && (
          <button
            type="button"
            aria-pressed={p.showHidden}
            onClick={() => p.onShowHidden(!p.showHidden)}
            className={cn(pill, p.showHidden ? pillOn : pillOff, 'ml-auto')}
          >
            <Icon name={p.showHidden ? 'eye' : 'eyeOff'} size={14} />
            {p.showHidden ? 'Showing hidden' : `${p.hiddenCount} hidden`}
          </button>
        )}
      </div>
      <CategoryPicker
        open={pickerOpen}
        onOpenChange={setPickerOpen}
        categories={p.categories}
        selected={p.selected}
        onChange={p.onCategories}
      />
    </div>
  );
}
