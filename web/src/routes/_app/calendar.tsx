import { useQuery } from '@tanstack/react-query';
import { createFileRoute, useNavigate } from '@tanstack/react-router';
import { useMemo } from 'react';
import {
  CALENDAR_SOURCES,
  addDays,
  calendarItemKey,
  calendarKeys,
  formatAirTime,
  fromDateKey,
  getCalendar,
  groupCalendarDays,
  itemSubtitle,
  monthGrid,
  toDateKey,
  weekDays,
  type CalendarItem,
  type CalendarSource,
  type CalendarType,
} from '@/api/calendar';
import { useSessionState } from '@/auth/sessionStore';
import { CalendarDetail } from '@/components/calendar/CalendarDetail';
import { EmptyState, ErrorState } from '@/components/EmptyState';
import { IconButton } from '@/components/IconButton';
import { Page } from '@/components/Page';
import { Segmented } from '@/components/Segmented';
import { Skeleton } from '@/components/Spinner';
import { cn } from '@/lib/cn';

type CalendarView = 'month' | 'week' | 'agenda';

interface CalendarSearch {
  view?: CalendarView;
  type?: CalendarType;
  source?: CalendarSource;
  date?: string;
  item?: string;
}

const DATE_KEY = /^\d{4}-\d{2}-\d{2}$/;

export const Route = createFileRoute('/_app/calendar')({
  validateSearch: (raw: Record<string, unknown>): CalendarSearch => ({
    view: raw.view === 'week' || raw.view === 'agenda' ? raw.view : undefined,
    type: raw.type === 'series' || raw.type === 'movies' ? raw.type : undefined,
    source:
      raw.source !== 'all' && CALENDAR_SOURCES.some((s) => s.value === raw.source)
        ? (raw.source as CalendarSource)
        : undefined,
    date: typeof raw.date === 'string' && DATE_KEY.test(raw.date) ? raw.date : undefined,
    item: typeof raw.item === 'string' ? raw.item : undefined,
  }),
  component: CalendarPage,
});

function CalendarPage() {
  const raw = Route.useSearch();
  const search = {
    ...raw,
    view: raw.view ?? 'month',
    type: raw.type ?? 'all',
    source: raw.source ?? 'all',
  };
  const navigate = useNavigate({ from: Route.fullPath });
  const { profileId } = useSessionState();
  const today = toDateKey(new Date());

  const query = useQuery({
    queryKey: calendarKeys.list(profileId ?? '', search.source),
    queryFn: ({ signal }) => getCalendar(profileId!, search.source, signal),
    enabled: !!profileId,
    staleTime: 5 * 60_000,
  });

  const days = useMemo(
    () => groupCalendarDays(query.data ?? [], search.type),
    [query.data, search.type],
  );
  const byDay = useMemo(() => new Map(days.map((d) => [d.key, d.items])), [days]);

  // Selection: chosen item if still listed, else first of chosen day, else the next day with releases.
  const selection = useMemo(() => {
    const pick = (key: string) => byDay.get(key);
    if (search.date && pick(search.date)) {
      const items = pick(search.date)!;
      const item = items.find((i) => calendarItemKey(i) === search.item) ?? items[0]!;
      return { date: search.date, item };
    }
    if (search.date) return { date: search.date, item: undefined };
    const next = days.find((d) => d.key >= today) ?? days[days.length - 1];
    return next ? { date: next.key, item: next.items[0] } : { date: today, item: undefined };
  }, [byDay, days, search.date, search.item, today]);

  const anchor = search.date ?? today;
  const set = (patch: Partial<CalendarSearch>) =>
    navigate({ search: (prev) => ({ ...prev, ...patch }), replace: true });
  const select = (date: string, item?: CalendarItem) =>
    set({ date, item: item ? calendarItemKey(item) : undefined });

  const step = (dir: 1 | -1) => {
    if (search.view === 'week') return set({ date: addDays(anchor, 7 * dir), item: undefined });
    const d = fromDateKey(anchor);
    d.setDate(1);
    d.setMonth(d.getMonth() + dir);
    set({ date: toDateKey(d), item: undefined });
  };

  const rangeLabel =
    search.view === 'week'
      ? weekLabel(anchor)
      : fromDateKey(anchor).toLocaleDateString(undefined, { month: 'long', year: 'numeric' });

  return (
    <Page
      title="Calendar"
      subtitle="Upcoming episodes and movie releases from your watchlist and what you're watching."
    >
      <div className="mb-5 flex flex-wrap items-center gap-3">
        <Segmented
          label="View"
          value={search.view}
          onChange={(view) => set({ view: view === 'month' ? undefined : view })}
          options={[
            { value: 'month', label: 'Month' },
            { value: 'week', label: 'Week' },
            { value: 'agenda', label: 'Agenda' },
          ]}
        />
        <Segmented
          label="Type"
          value={search.type}
          onChange={(type) => set({ type: type === 'all' ? undefined : type, item: undefined })}
          options={[
            { value: 'all', label: 'All' },
            { value: 'series', label: 'TV' },
            { value: 'movies', label: 'Movies' },
          ]}
        />
        <label className="flex items-center gap-2 text-sm text-muted">
          <span className="sr-only sm:not-sr-only">Source</span>
          <select
            value={search.source}
            onChange={(e) => {
              const source = e.target.value as CalendarSource;
              set({ source: source === 'all' ? undefined : source, item: undefined });
            }}
            className="h-10 cursor-pointer rounded-full border border-line bg-surface px-4 text-sm text-fg outline-none hover:bg-surface-strong focus:border-accent"
            aria-label="Source"
          >
            {CALENDAR_SOURCES.map((s) => (
              <option key={s.value} value={s.value}>
                {s.label}
              </option>
            ))}
          </select>
        </label>
        {search.view !== 'agenda' && (
          <div className="ml-auto flex items-center gap-1">
            <IconButton icon="chevronLeft" label="Previous" size="sm" onClick={() => step(-1)} />
            <button
              type="button"
              onClick={() => set({ date: undefined, item: undefined })}
              className="h-8 cursor-pointer rounded-lg px-3 text-sm font-medium text-muted hover:bg-surface hover:text-fg"
            >
              Today
            </button>
            <IconButton icon="chevronRight" label="Next" size="sm" onClick={() => step(1)} />
            <h2 className="ml-2 min-w-40 text-base font-semibold" aria-live="polite">
              {rangeLabel}
            </h2>
          </div>
        )}
      </div>

      {query.isError ? (
        <ErrorState
          title="Couldn't load the calendar"
          error={query.error}
          onRetry={() => query.refetch()}
        />
      ) : query.isLoading ? (
        <CalendarSkeleton />
      ) : days.length === 0 ? (
        <EmptyState
          icon="calendar"
          title="Nothing scheduled"
          description="Add shows and movies to your watchlist and their upcoming releases appear here."
        />
      ) : (
        <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_380px]">
          <div className="min-w-0">
            {search.view === 'month' && (
              <MonthGrid
                anchor={anchor}
                today={today}
                byDay={byDay}
                selected={selection.date}
                onSelect={select}
              />
            )}
            {search.view === 'week' && (
              <WeekView
                anchor={anchor}
                today={today}
                byDay={byDay}
                selectedItem={selection.item}
                onSelect={select}
              />
            )}
            {/* Agenda: always on small screens, and the selected day's list under month view. */}
            <Agenda
              className={cn(search.view === 'month' && 'mt-6', search.view === 'week' && 'hidden')}
              days={search.view === 'agenda' ? days : days.filter((d) => d.key === selection.date)}
              today={today}
              selectedItem={selection.item}
              onSelect={select}
              emptyLabel={search.view === 'month' ? 'No releases on this day.' : undefined}
              selectedDate={selection.date}
            />
          </div>
          {selection.item && profileId ? (
            <div className="xl:sticky xl:top-20 xl:self-start">
              <CalendarDetail profileId={profileId} item={selection.item} />
            </div>
          ) : null}
        </div>
      )}
    </Page>
  );
}

function weekLabel(anchor: string) {
  const days = weekDays(anchor);
  const a = fromDateKey(days[0]!);
  const b = fromDateKey(days[6]!);
  const fmt = (d: Date) => d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
  return `${fmt(a)} – ${fmt(b)}, ${b.getFullYear()}`;
}

const WEEKDAYS = ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'];

function MonthGrid({
  anchor,
  today,
  byDay,
  selected,
  onSelect,
}: {
  anchor: string;
  today: string;
  byDay: Map<string, CalendarItem[]>;
  selected: string;
  onSelect: (date: string, item?: CalendarItem) => void;
}) {
  const cells = monthGrid(anchor);
  const month = fromDateKey(anchor).getMonth();
  return (
    <div
      className="overflow-hidden rounded-2xl border border-line bg-surface"
      role="grid"
      aria-label="Month"
    >
      <div className="grid grid-cols-7 border-b border-line" role="row">
        {WEEKDAYS.map((d) => (
          <div
            key={d}
            role="columnheader"
            className="px-2 py-2 text-xs font-semibold text-faint uppercase"
          >
            <span className="sm:hidden">{d[0]}</span>
            <span className="hidden sm:inline">{d}</span>
          </div>
        ))}
      </div>
      <div className="grid grid-cols-7">
        {cells.map((key) => {
          const items = byDay.get(key) ?? [];
          const inMonth = fromDateKey(key).getMonth() === month;
          const isSel = key === selected;
          return (
            <button
              key={key}
              type="button"
              role="gridcell"
              aria-selected={isSel}
              aria-label={`${fromDateKey(key).toDateString()}, ${items.length} releases`}
              onClick={() => onSelect(key, items[0])}
              className={cn(
                'group relative flex min-h-14 cursor-pointer flex-col gap-1 border-r border-b border-line p-1.5 text-left transition sm:min-h-28 sm:p-2',
                '[&:nth-child(7n)]:border-r-0',
                !inMonth && 'opacity-40',
                isSel
                  ? 'bg-[color-mix(in_srgb,var(--accent)_10%,transparent)]'
                  : 'hover:bg-surface-strong',
              )}
            >
              <span
                className={cn(
                  'flex h-6 w-6 items-center justify-center rounded-full text-xs font-semibold',
                  key === today
                    ? 'bg-accent text-accent-contrast'
                    : isSel
                      ? 'text-accent'
                      : 'text-muted',
                )}
              >
                {fromDateKey(key).getDate()}
              </span>
              {/* Phone: dots. Desktop: chips. */}
              {items.length > 0 && (
                <span className="flex gap-0.5 sm:hidden">
                  {items.slice(0, 3).map((i) => (
                    <span key={calendarItemKey(i)} className="h-1.5 w-1.5 rounded-full bg-accent" />
                  ))}
                </span>
              )}
              <span className="hidden flex-col gap-1 sm:flex">
                {items.slice(0, 3).map((i) => (
                  <span
                    key={calendarItemKey(i)}
                    className={cn(
                      'truncate rounded-md px-1.5 py-0.5 text-[11px] font-medium',
                      i.mediaType === 'movie'
                        ? 'bg-[color-mix(in_srgb,var(--warning)_18%,transparent)] text-fg'
                        : 'accent-soft',
                    )}
                  >
                    {i.title}
                  </span>
                ))}
                {items.length > 3 && (
                  <span className="px-1.5 text-[11px] text-faint">+{items.length - 3} more</span>
                )}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

function WeekView({
  anchor,
  today,
  byDay,
  selectedItem,
  onSelect,
}: {
  anchor: string;
  today: string;
  byDay: Map<string, CalendarItem[]>;
  selectedItem?: CalendarItem;
  onSelect: (date: string, item?: CalendarItem) => void;
}) {
  const selKey = selectedItem ? calendarItemKey(selectedItem) : undefined;
  return (
    <div className="grid gap-3 md:grid-cols-7">
      {weekDays(anchor).map((key) => {
        const items = byDay.get(key) ?? [];
        const d = fromDateKey(key);
        return (
          <section key={key} aria-label={d.toDateString()} className="min-w-0">
            <h3
              className={cn(
                'mb-2 flex items-baseline gap-2 text-sm font-semibold',
                key === today ? 'text-accent' : 'text-muted',
              )}
            >
              {d.toLocaleDateString(undefined, { weekday: 'short' })}
              <span className="text-lg text-fg">{d.getDate()}</span>
            </h3>
            <div className="flex flex-col gap-2">
              {items.length === 0 && (
                <div className="rounded-xl border border-dashed border-line py-4 text-center text-xs text-faint">
                  —
                </div>
              )}
              {items.map((item) => (
                <ReleaseCard
                  key={calendarItemKey(item)}
                  item={item}
                  compact
                  active={calendarItemKey(item) === selKey}
                  onClick={() => onSelect(key, item)}
                />
              ))}
            </div>
          </section>
        );
      })}
    </div>
  );
}

function Agenda({
  days,
  today,
  selectedItem,
  selectedDate,
  onSelect,
  emptyLabel,
  className,
}: {
  days: { key: string; items: CalendarItem[] }[];
  today: string;
  selectedItem?: CalendarItem;
  selectedDate: string;
  onSelect: (date: string, item?: CalendarItem) => void;
  emptyLabel?: string;
  className?: string;
}) {
  const selKey = selectedItem ? calendarItemKey(selectedItem) : undefined;
  if (days.length === 0 && emptyLabel) {
    return (
      <p
        className={cn(
          'rounded-xl border border-dashed border-line py-6 text-center text-sm text-faint',
          className,
        )}
      >
        {fromDateKey(selectedDate).toLocaleDateString(undefined, {
          weekday: 'long',
          month: 'long',
          day: 'numeric',
        })}{' '}
        — {emptyLabel}
      </p>
    );
  }
  return (
    <div className={cn('flex flex-col gap-6', className)}>
      {days.map((day) => (
        <section key={day.key} aria-label={fromDateKey(day.key).toDateString()}>
          <h3 className="mb-3 flex items-center gap-2 text-sm font-semibold">
            <span className={day.key === today ? 'text-accent' : ''}>
              {dayHeading(day.key, today)}
            </span>
            <span className="text-faint">
              {fromDateKey(day.key).toLocaleDateString(undefined, {
                month: 'short',
                day: 'numeric',
              })}
            </span>
          </h3>
          <div className="grid gap-2 sm:grid-cols-2 2xl:grid-cols-3">
            {day.items.map((item) => (
              <ReleaseCard
                key={calendarItemKey(item)}
                item={item}
                active={calendarItemKey(item) === selKey}
                onClick={() => onSelect(day.key, item)}
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

function dayHeading(key: string, today: string) {
  if (key === today) return 'Today';
  if (key === addDays(today, 1)) return 'Tomorrow';
  if (key === addDays(today, -1)) return 'Yesterday';
  return fromDateKey(key).toLocaleDateString(undefined, { weekday: 'long' });
}

function ReleaseCard({
  item,
  active,
  compact,
  onClick,
}: {
  item: CalendarItem;
  active: boolean;
  compact?: boolean;
  onClick: () => void;
}) {
  const time = formatAirTime(item.airTime);
  const poster = item.posterUrl;
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={cn(
        'flex w-full cursor-pointer items-center gap-3 rounded-xl border p-2 text-left transition',
        active
          ? 'border-[color-mix(in_srgb,var(--accent)_60%,transparent)] bg-[color-mix(in_srgb,var(--accent)_12%,transparent)]'
          : 'border-line bg-surface hover:bg-surface-strong hover:border-line-strong',
        compact && 'flex-col items-stretch gap-2 md:p-1.5',
      )}
    >
      <div
        className={cn(
          'shrink-0 overflow-hidden rounded-lg bg-surface-strong',
          compact ? 'hidden md:block aspect-[2/3] w-full' : 'h-16 w-11',
        )}
      >
        {poster ? (
          <img src={poster} alt="" loading="lazy" className="h-full w-full object-cover" />
        ) : null}
      </div>
      <div className="min-w-0 flex-1">
        <p className="truncate text-sm font-medium">{item.title}</p>
        <p className="truncate text-xs text-muted">{itemSubtitle(item)}</p>
        {(time || item.network) && !compact && (
          <p className="truncate text-xs text-faint">
            {[time, item.network].filter(Boolean).join(' · ')}
          </p>
        )}
        {compact && time && <p className="text-[11px] text-faint">{time}</p>}
      </div>
    </button>
  );
}

function CalendarSkeleton() {
  return (
    <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_380px]">
      <div className="grid grid-cols-7 gap-px overflow-hidden rounded-2xl border border-line">
        {Array.from({ length: 35 }, (_, i) => (
          <Skeleton key={i} className="h-14 rounded-none sm:h-28" />
        ))}
      </div>
      <Skeleton className="hidden h-[480px] rounded-2xl xl:block" />
    </div>
  );
}
