/** Time math for the Live TV guide grid. */

export const HALF_HOUR = 30 * 60_000;
export const HOUR = 60 * 60_000;

/** Width of one minute in the guide, px. */
export const PX_PER_MIN = 6;
/** Hours shown in the guide from its start. */
export const GUIDE_HOURS = 12;

export const floorHalfHour = (t: number) => Math.floor(t / HALF_HOUR) * HALF_HOUR;

/** The guide starts half an hour before the current half hour (native guideBase). */
export const guideStart = (now: number) => floorHalfHour(now) - HALF_HOUR;

export const xFor = (t: number, start: number) => ((t - start) / 60_000) * PX_PER_MIN;

/** Left/width of a programme block clipped to [start, end]; null when outside. */
export function blockGeometry(
  p: { start: number; stop: number },
  start: number,
  end: number,
): { left: number; width: number } | null {
  const s = Math.max(p.start, start);
  const e = Math.min(p.stop, end);
  if (e <= s) return null;
  return { left: xFor(s, start), width: xFor(e, start) - xFor(s, start) };
}

/** Half-hour ticks in [start, end). */
export function halfHourTicks(start: number, end: number): number[] {
  const out: number[] = [];
  for (let t = floorHalfHour(start); t < end; t += HALF_HOUR) if (t >= start) out.push(t);
  return out;
}

export const formatClock = (t: number) =>
  new Date(t).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });

export function formatRange(start: number, stop: number) {
  return `${formatClock(start)} – ${formatClock(stop)}`;
}

/** "Today", "Tomorrow", or a short weekday + date. */
export function formatDay(t: number, now = Date.now()): string {
  const d = new Date(t);
  const today = new Date(now);
  const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((startOf(d) - startOf(today)) / 86_400_000);
  if (diff === 0) return 'Today';
  if (diff === 1) return 'Tomorrow';
  if (diff === -1) return 'Yesterday';
  return d.toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });
}

/** Split into chunks of `size`. */
export function chunk<T>(items: T[], size: number): T[][] {
  const out: T[][] = [];
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size));
  return out;
}
