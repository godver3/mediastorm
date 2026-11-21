import { useEffect, useLayoutEffect, useRef, type ReactNode } from 'react';
import { guideKey, type EPGProgram, type LiveChannel } from '@/api/live';
import { recordingFor, type Recording } from '@/api/recordings';
import { Skeleton } from '@/components';
import { cn } from '@/lib/cn';
import {
  blockGeometry,
  formatClock,
  formatDay,
  GUIDE_HOURS,
  halfHourTicks,
  HOUR,
  xFor,
} from '@/lib/guide';
import { ChannelLogo, LiveIcon, RecordingBadge } from './LiveBits';

const ROW_H = 68;

export interface GuideGridProps {
  channels: LiveChannel[];
  schedules: Record<string, EPGProgram[]>;
  scheduleLoading: boolean;
  start: number;
  now: number;
  favorites: string[];
  recordings: Recording[];
  tuningId: string | null;
  onTune: (channel: LiveChannel) => void;
  onProgram: (channel: LiveChannel, program: EPGProgram) => void;
  onChannelMenu: (channel: LiveChannel) => void;
  footer?: ReactNode;
}

/** Desktop EPG: sticky time axis and channel column, programme blocks, a now line. */
export function GuideGrid({
  channels,
  schedules,
  scheduleLoading,
  start,
  now,
  favorites,
  recordings,
  tuningId,
  onTune,
  onProgram,
  onChannelMenu,
  footer,
}: GuideGridProps) {
  const end = start + GUIDE_HOURS * HOUR;
  const width = xFor(end, start);
  const ticks = halfHourTicks(start, end);
  const scroller = useRef<HTMLDivElement>(null);
  const nowX = xFor(now, start);

  const scrollToTime = (t: number, smooth = true) => {
    const el = scroller.current;
    if (!el || typeof el.scrollTo !== 'function') return;
    el.scrollTo({
      left: Math.max(0, xFor(t, start) - 60),
      behavior: smooth ? 'smooth' : 'auto',
    });
  };

  useLayoutEffect(() => {
    scrollToTime(now - 30 * 60_000, false);
    // Only on mount / when the guide start changes.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [start]);

  // Keyboard paging by time when the grid has focus.
  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const onKey = (e: KeyboardEvent) => {
      if (e.target !== el) return;
      if (e.key === 'ArrowRight') el.scrollBy({ left: 30 * 6 * 2, behavior: 'smooth' });
      if (e.key === 'ArrowLeft') el.scrollBy({ left: -30 * 6 * 2, behavior: 'smooth' });
    };
    el.addEventListener('keydown', onKey);
    return () => el.removeEventListener('keydown', onKey);
  }, []);

  const page = (dir: 1 | -1) =>
    scroller.current?.scrollBy?.({ left: dir * xFor(start + 2 * HOUR, start), behavior: 'smooth' });

  return (
    <section
      aria-label="Programme guide"
      className="overflow-hidden rounded-2xl border border-line bg-elevated [--chan-w:104px] sm:[--chan-w:232px]"
    >
      <div className="flex items-center justify-between gap-2 border-b border-line px-3 py-2">
        <span className="text-sm font-medium">{formatDay(now, now)}</span>
        <div className="flex items-center gap-1">
          <button
            type="button"
            onClick={() => scrollToTime(now - 30 * 60_000)}
            className="h-8 cursor-pointer rounded-lg px-3 text-sm font-medium text-accent hover:bg-surface"
          >
            Now
          </button>
          <button
            type="button"
            aria-label="Earlier"
            onClick={() => page(-1)}
            className="flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg text-muted hover:bg-surface hover:text-fg"
          >
            <LiveIcon name="chevronLeft" />
          </button>
          <button
            type="button"
            aria-label="Later"
            onClick={() => page(1)}
            className="flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg text-muted hover:bg-surface hover:text-fg"
          >
            <LiveIcon name="chevronRight" />
          </button>
        </div>
      </div>
      <div
        ref={scroller}
        tabIndex={0}
        aria-label="Guide timeline, use arrow keys to scroll"
        className="relative max-h-[calc(100dvh-260px)] min-h-[320px] overflow-auto overscroll-contain focus:outline-none"
      >
        <div className="relative" style={{ width: `calc(var(--chan-w) + ${width}px)` }}>
          {/* Time axis */}
          <div className="sticky top-0 z-20 flex h-10 border-b border-line bg-elevated">
            <div className="sticky left-0 z-30 w-[var(--chan-w)] shrink-0 border-r border-line bg-elevated" />
            <div className="relative" style={{ width }}>
              {ticks.map((t) => (
                <div
                  key={t}
                  className="absolute top-0 flex h-full items-center border-l border-line pl-2 text-xs text-muted"
                  style={{ left: xFor(t, start) }}
                >
                  {formatClock(t)}
                </div>
              ))}
              {nowX >= 0 && nowX <= width && (
                <div
                  className="absolute bottom-0 z-10 -translate-x-1/2 rounded-t bg-accent px-1.5 text-[10px] font-semibold text-accent-contrast"
                  style={{ left: nowX }}
                >
                  {formatClock(now)}
                </div>
              )}
            </div>
          </div>

          {channels.map((channel) => {
            const programs = schedules[guideKey(channel)];
            const fav = favorites.includes(channel.id);
            return (
              <div key={channel.id} className="flex border-b border-line" style={{ height: ROW_H }}>
                <div className="group sticky left-0 z-10 flex w-[var(--chan-w)] shrink-0 items-center gap-2 border-r border-line bg-elevated px-2">
                  <button
                    type="button"
                    onClick={() => onTune(channel)}
                    className="flex min-w-0 flex-1 cursor-pointer items-center gap-2.5 rounded-lg p-1 text-left hover:bg-surface focus-visible:outline-2 focus-visible:outline-accent"
                    aria-label={`Watch ${channel.name}`}
                  >
                    <ChannelLogo
                      channel={channel}
                      className={cn('h-11 w-11', tuningId === channel.id && 'animate-pulse')}
                    />
                    <span className="hidden min-w-0 sm:block">
                      <span className="flex items-center gap-1 truncate text-sm font-medium">
                        {fav && (
                          <LiveIcon
                            name="star"
                            size={12}
                            filled
                            className="shrink-0 text-warning"
                          />
                        )}
                        <span className="truncate">{channel.name}</span>
                      </span>
                      {channel.group && (
                        <span className="block truncate text-xs text-faint">{channel.group}</span>
                      )}
                    </span>
                  </button>
                  <button
                    type="button"
                    onClick={() => onChannelMenu(channel)}
                    aria-label={`${channel.name} options`}
                    className="hidden h-7 w-7 shrink-0 cursor-pointer items-center justify-center rounded-md text-faint opacity-0 group-hover:opacity-100 hover:bg-surface hover:text-fg focus-visible:opacity-100 sm:flex"
                  >
                    ⋯
                  </button>
                </div>
                <div className="relative" style={{ width }}>
                  {programs === undefined && scheduleLoading ? (
                    <Skeleton className="absolute inset-y-2 left-2 w-[60%]" />
                  ) : programs && programs.length ? (
                    programs.map((p) => {
                      const g = blockGeometry(p, start, end);
                      if (!g) return null;
                      const airing = p.start <= now && now < p.stop;
                      const past = p.stop <= now;
                      const rec = recordingFor(recordings, channel, p, now);
                      return (
                        <button
                          key={p.start}
                          type="button"
                          onClick={() => onProgram(channel, p)}
                          className={cn(
                            'absolute top-1 bottom-1 cursor-pointer overflow-hidden rounded-lg border px-2.5 py-1.5 text-left transition',
                            'focus-visible:z-10 focus-visible:outline-2 focus-visible:outline-accent',
                            airing
                              ? 'border-[color-mix(in_srgb,var(--accent)_45%,transparent)] bg-[color-mix(in_srgb,var(--accent)_16%,transparent)] hover:bg-[color-mix(in_srgb,var(--accent)_24%,transparent)]'
                              : 'border-line bg-surface hover:bg-surface-strong',
                            past && 'opacity-55',
                          )}
                          style={{ left: g.left + 2, width: Math.max(g.width - 4, 4) }}
                          title={`${p.title} · ${formatClock(p.start)}`}
                        >
                          <span className="flex items-center gap-1.5">
                            {rec && <RecordingBadge recording={rec} />}
                            <span className="truncate text-sm font-medium">{p.title}</span>
                            {p.isNew && (
                              <span className="shrink-0 rounded bg-accent px-1 text-[9px] font-bold text-accent-contrast">
                                NEW
                              </span>
                            )}
                          </span>
                          <span className="block truncate text-xs text-muted">
                            {formatClock(p.start)}
                            {p.episode ? ` · ${p.episode}` : ''}
                          </span>
                        </button>
                      );
                    })
                  ) : (
                    <div className="absolute inset-y-1 left-1 right-1 flex items-center rounded-lg border border-dashed border-line px-3 text-xs text-faint">
                      No guide data
                    </div>
                  )}
                </div>
              </div>
            );
          })}

          {nowX >= 0 && nowX <= width && (
            <div
              aria-hidden="true"
              className="pointer-events-none absolute top-10 bottom-0 z-[5] w-0.5 bg-accent shadow-[0_0_8px_var(--accent)]"
              style={{ left: `calc(var(--chan-w) + ${nowX}px)` }}
            />
          )}
        </div>
        {footer}
      </div>
    </section>
  );
}
