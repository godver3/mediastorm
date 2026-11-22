import { useState } from 'react';
import { getPath, sortedShelves, type RawSettings, type ShelfConfig } from '@/api/settings';
import {
  FILTER_TERM_LISTS,
  MENU_TABS,
  calendarShelves,
  criterionLabel,
  isMdblistUrl,
  isRemovableShelf,
  moveItem,
  navTabs,
  newListShelf,
  rankingCriteria,
  setNavTab,
  shelfCalendarEnabled,
  type RankingCriterion,
} from '@/api/settingsExtras';
import { Button } from '@/components/Button';
import { IconButton } from '@/components/IconButton';
import { cn } from '@/lib/cn';
import { SettingRow, SettingsCard, Switch, ToggleRow } from './SettingsControls';
import { TermsEditor } from './TermsEditor';

export type SectionProps = {
  data: RawSettings;
  update: (...pairs: [string, unknown][]) => void;
};

/** Filter terms (filtering.*Terms) and result ranking (ranking.criteria), as Expo's Filtering page. */
export function FilteringSection({ data, update }: SectionProps) {
  const criteria = rankingCriteria(getPath<RankingCriterion[]>(data, 'ranking.criteria'));
  const saveCriteria = (next: typeof criteria) => update(['ranking.criteria', next]);
  return (
    <div className="flex flex-col gap-6">
      <SettingsCard
        title="Filter terms"
        description="Matched case-insensitively against result names when sources are searched."
      >
        {FILTER_TERM_LISTS.map((l) => (
          <div key={l.key} className="flex flex-col gap-2 px-5 py-4">
            <div>
              <p className="text-sm font-medium">{l.label}</p>
              <p className="mt-0.5 text-xs text-muted">{l.description}</p>
            </div>
            <TermsEditor
              label={l.label}
              terms={getPath<string[]>(data, `filtering.${l.key}`) ?? []}
              onChange={(next) => update([`filtering.${l.key}`, next])}
            />
          </div>
        ))}
      </SettingsCard>
      <SettingsCard
        title="Ranking"
        description="How results are ordered: earlier criteria win ties of later ones."
      >
        <ol>
          {criteria.map((c, i) => (
            <li
              key={c.id}
              className={cn(
                'flex items-center gap-3 border-t border-line px-5 py-3 first:border-t-0',
                !c.enabled && 'opacity-60',
              )}
            >
              <span className="w-6 text-xs text-faint tabular-nums">{i + 1}</span>
              <span className="min-w-0 flex-1 truncate text-sm font-medium">
                {criterionLabel(c.id)}
              </span>
              <IconButton
                icon="chevronDown"
                label={`Move ${criterionLabel(c.id)} up`}
                size="sm"
                className="rotate-180"
                disabled={i === 0}
                onClick={() => saveCriteria(moveItem(criteria, i, -1))}
              />
              <IconButton
                icon="chevronDown"
                label={`Move ${criterionLabel(c.id)} down`}
                size="sm"
                disabled={i === criteria.length - 1}
                onClick={() => saveCriteria(moveItem(criteria, i, 1))}
              />
              <Switch
                label={`Use ${criterionLabel(c.id)}`}
                checked={c.enabled}
                onChange={(v) =>
                  saveCriteria(criteria.map((x) => (x.id === c.id ? { ...x, enabled: v } : x)))
                }
              />
            </li>
          ))}
        </ol>
        <ToggleRow
          label="Newest release first"
          description="Prefer the latest release when results otherwise tie."
          checked={!!getPath<boolean>(data, 'ranking.newestReleaseFirst')}
          onChange={(v) => update(['ranking.newestReleaseFirst', v])}
        />
      </SettingsCard>
    </div>
  );
}

const SPOILERS: { key: string; label: string; description: string; parent?: string }[] = [
  {
    key: 'blurUnwatchedEpisodeThumbnails',
    label: 'Blur unwatched episode thumbnails',
    description: 'Hides stills of episodes after the one up next.',
  },
  {
    key: 'blurUnwatchedEpisodeThumbnailsIncludeCurrent',
    label: 'Include the next episode',
    description: 'Blur the up-next episode too.',
    parent: 'blurUnwatchedEpisodeThumbnails',
  },
  {
    key: 'blurUnwatchedEpisodeOverviews',
    label: 'Hide unwatched episode descriptions',
    description: 'Hides synopses of episodes after the one up next.',
  },
  {
    key: 'blurUnwatchedEpisodeOverviewsIncludeCurrent',
    label: 'Include the next episode',
    description: 'Hide the up-next episode’s description too.',
    parent: 'blurUnwatchedEpisodeOverviews',
  },
];

/** Menu tab visibility (display.navigationTabVisibility) and spoiler options (display.blur*). */
export function MenuSpoilersSection({ data, update }: SectionProps) {
  const tabs = navTabs(getPath(data, 'display.navigationTabVisibility'));
  const bool = (k: string) => !!getPath<boolean>(data, `display.${k}`);
  return (
    <div className="flex flex-col gap-6">
      <SettingsCard
        title="Menu"
        description="Which pages the sidebar shows. Home and Settings always show. Shared with this profile’s other devices."
      >
        {MENU_TABS.map((t) => (
          <ToggleRow
            key={t.key}
            label={t.label}
            checked={tabs.includes(t.key)}
            onChange={(v) => update(['display.navigationTabVisibility', setNavTab(tabs, t.key, v)])}
          />
        ))}
      </SettingsCard>
      <SettingsCard title="Spoilers" description="What episode lists hide until you watch.">
        {SPOILERS.filter((s) => !s.parent || bool(s.parent)).map((s) => (
          <ToggleRow
            key={s.key}
            label={s.label}
            description={s.description}
            checked={bool(s.key)}
            onChange={(v) => update([`display.${s.key}`, v])}
          />
        ))}
      </SettingsCard>
    </div>
  );
}

/** Home shelves: order, show/hide, rename, card limit, add/remove MDBList shelves. */
export function HomeShelvesEditor({ data, update }: SectionProps) {
  const shelves = sortedShelves(getPath<ShelfConfig[]>(data, 'homeShelves.shelves'));
  const save = (next: ShelfConfig[]) => update(['homeShelves.shelves', next]);
  const patch = (id: string, p: Partial<ShelfConfig>) =>
    save(shelves.map((x) => (x.id === id ? { ...x, ...p } : x)));
  const [editing, setEditing] = useState<string>();
  return (
    <div className="flex flex-col gap-6">
      <SettingsCard
        title="Home shelves"
        description="Choose which rows appear on Home, in what order, and how many cards each shows."
      >
        {shelves.length === 0 && (
          <p className="px-5 py-6 text-sm text-muted">This profile has no shelves configured.</p>
        )}
        <ol>
          {shelves.map((s, i) => (
            <li key={s.id} className="border-t border-line first:border-t-0">
              <div className={cn('flex items-center gap-3 px-5 py-3', !s.enabled && 'opacity-60')}>
                <span className="w-6 text-xs text-faint tabular-nums">{i + 1}</span>
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium">{s.name || s.id}</span>
                  {s.type && s.type !== 'builtin' && (
                    <span className="block text-xs text-faint capitalize">{s.type}</span>
                  )}
                </span>
                <IconButton
                  icon="edit"
                  label={`Edit ${s.name}`}
                  size="sm"
                  aria-expanded={editing === s.id}
                  onClick={() => setEditing(editing === s.id ? undefined : s.id)}
                />
                <IconButton
                  icon="chevronDown"
                  label={`Move ${s.name} up`}
                  size="sm"
                  className="rotate-180"
                  disabled={i === 0}
                  onClick={() => save(moveItem(shelves, i, -1))}
                />
                <IconButton
                  icon="chevronDown"
                  label={`Move ${s.name} down`}
                  size="sm"
                  disabled={i === shelves.length - 1}
                  onClick={() => save(moveItem(shelves, i, 1))}
                />
                <Switch
                  label={`Show ${s.name}`}
                  checked={s.enabled}
                  onChange={(v) => patch(s.id, { enabled: v })}
                />
              </div>
              {editing === s.id && (
                <ShelfOptions
                  shelf={s}
                  onPatch={(p) => patch(s.id, p)}
                  onRemove={
                    isRemovableShelf(s)
                      ? () => {
                          setEditing(undefined);
                          save(
                            shelves
                              .filter((x) => x.id !== s.id)
                              .map((x, j) => ({ ...x, order: j })),
                          );
                        }
                      : undefined
                  }
                />
              )}
            </li>
          ))}
        </ol>
      </SettingsCard>
      <AddListShelf onAdd={(shelf) => save([...shelves, shelf])} count={shelves.length} />
    </div>
  );
}

function ShelfOptions({
  shelf,
  onPatch,
  onRemove,
}: {
  shelf: ShelfConfig;
  onPatch: (p: Partial<ShelfConfig>) => void;
  onRemove?: () => void;
}) {
  const [name, setName] = useState(shelf.name ?? '');
  const limit = typeof shelf.limit === 'number' ? shelf.limit : 0;
  return (
    <div className="flex flex-col gap-3 bg-elevated/50 px-5 pt-1 pb-4 sm:flex-row sm:items-end">
      <label className="flex flex-1 flex-col gap-1 text-xs text-muted">
        Name
        <input
          value={name}
          maxLength={60}
          onChange={(e) => setName(e.target.value)}
          onBlur={() => name.trim() && name.trim() !== shelf.name && onPatch({ name: name.trim() })}
          className="h-9 rounded-lg border border-line bg-elevated px-3 text-sm text-fg outline-none focus:border-accent"
        />
      </label>
      <label className="flex flex-col gap-1 text-xs text-muted">
        Cards
        <select
          value={String(limit)}
          onChange={(e) => onPatch({ limit: Number(e.target.value) || undefined })}
          className="h-9 cursor-pointer rounded-lg border border-line bg-elevated px-2 text-sm text-fg outline-none focus:border-accent"
        >
          <option value="0">Default</option>
          {[10, 15, 20, 30, 50].map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
      </label>
      {onRemove && (
        <Button variant="ghost" size="sm" className="text-danger" onClick={onRemove}>
          Remove shelf
        </Button>
      )}
    </div>
  );
}

function AddListShelf({ onAdd, count }: { onAdd: (s: ShelfConfig) => void; count: number }) {
  const [name, setName] = useState('');
  const [url, setUrl] = useState('');
  const valid = isMdblistUrl(url);
  return (
    <SettingsCard
      title="Add a list shelf"
      description="Show an MDBList list as a row on Home (and, if enabled, in the Calendar)."
    >
      <form
        className="flex flex-col gap-3 px-5 py-4 sm:flex-row sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          if (!valid) return;
          onAdd(newListShelf(name, url, count));
          setName('');
          setUrl('');
        }}
      >
        <label className="flex flex-col gap-1 text-xs text-muted sm:w-48">
          Name
          <input
            value={name}
            maxLength={60}
            placeholder="My list"
            onChange={(e) => setName(e.target.value)}
            className="h-10 rounded-xl border border-line bg-elevated px-3 text-sm text-fg outline-none placeholder:text-faint focus:border-accent"
          />
        </label>
        <label className="flex flex-1 flex-col gap-1 text-xs text-muted">
          MDBList URL
          <input
            value={url}
            type="url"
            placeholder="https://mdblist.com/lists/user/list-name"
            onChange={(e) => setUrl(e.target.value)}
            className="h-10 rounded-xl border border-line bg-elevated px-3 text-sm text-fg outline-none placeholder:text-faint focus:border-accent"
          />
        </label>
        <Button type="submit" variant="secondary" disabled={!valid}>
          Add shelf
        </Button>
      </form>
    </SettingsCard>
  );
}

const CAL_SOURCES: { key: string; label: string; description: string }[] = [
  {
    key: 'watchlist',
    label: 'Watchlist',
    description: 'Episodes and movie releases from your watchlist.',
  },
  { key: 'history', label: 'Watching', description: 'New episodes of series you are watching.' },
  { key: 'topTrending', label: 'Top trending', description: 'The top 20 of the trending lists.' },
  { key: 'trending', label: 'More trending', description: 'The rest of the trending lists.' },
  {
    key: 'mdblists',
    label: 'MDBList shelves',
    description: 'Titles from your MDBList home shelves.',
  },
];

/** Calendar sources plus per-MDBList-shelf toggles (calendar.mdblistShelves). */
export function CalendarSourcesSection({ data, update }: SectionProps) {
  const lists = calendarShelves(getPath<ShelfConfig[]>(data, 'homeShelves.shelves'));
  const map = getPath<Record<string, boolean>>(data, 'calendar.mdblistShelves');
  const mdblistsOn = getPath<boolean>(data, 'calendar.mdblists') !== false;
  return (
    <div className="flex flex-col gap-6">
      <SettingsCard title="Calendar sources" description="What feeds the Calendar page.">
        {CAL_SOURCES.map((s) => (
          <ToggleRow
            key={s.key}
            label={s.label}
            description={s.description}
            checked={getPath<boolean>(data, `calendar.${s.key}`) !== false}
            onChange={(v) => update([`calendar.${s.key}`, v])}
          />
        ))}
      </SettingsCard>
      {mdblistsOn && (
        <SettingsCard
          title="MDBList shelves in the calendar"
          description="Pick which of your MDBList home shelves feed the calendar."
        >
          {lists.length === 0 ? (
            <SettingRow
              label="No MDBList shelves"
              description="Add one under Home shelves to see its releases here."
            >
              <span />
            </SettingRow>
          ) : (
            lists.map((s) => (
              <ToggleRow
                key={s.id}
                label={s.name || s.id}
                checked={shelfCalendarEnabled(map, s.id)}
                onChange={(v) => update([`calendar.mdblistShelves`, { ...(map ?? {}), [s.id]: v }])}
              />
            ))
          )}
        </SettingsCard>
      )}
    </div>
  );
}
