import { useState, type SVGProps } from 'react';
import type { LiveChannel } from '@/api/live';
import type { Recording } from '@/api/recordings';
import { cn } from '@/lib/cn';

const paths = {
  star: 'M12 3l2.8 5.7 6.2.9-4.5 4.4 1.1 6.2L12 17.3 6.4 20.2l1.1-6.2L3 9.6l6.2-.9z',
  eyeOff:
    'M3 3l18 18M10.6 10.6a2 2 0 0 0 2.8 2.8M9.9 5.1A10 10 0 0 1 12 5c5 0 9 4.5 10 7a13 13 0 0 1-3.2 4.3M6.6 6.6C4.4 8 2.8 10.2 2 12c1 2.5 5 7 10 7a10 10 0 0 0 4.4-1',
  eye: 'M2 12c1-2.5 5-7 10-7s9 4.5 10 7c-1 2.5-5 7-10 7S3 14.5 2 12zM12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6z',
  grid: 'M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z',
  rows: 'M3 5h18M3 10h18M3 15h18M3 20h18',
  clock: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM12 7v5l3 2',
  repeat: 'M17 2l4 4-4 4M3 11V9a3 3 0 0 1 3-3h15M7 22l-4-4 4-4M21 13v2a3 3 0 0 1-3 3H3',
  trash: 'M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3',
  chevronLeft: 'M15 5l-7 7 7 7',
  chevronRight: 'M9 5l7 7-7 7',
  plus: 'M12 5v14M5 12h14',
  stop: 'M7 7h10v10H7z',
  sparkle:
    'M12 3l1.8 5.2L19 10l-5.2 1.8L12 17l-1.8-5.2L5 10l5.2-1.8zM19 16l.8 2.2L22 19l-2.2.8L19 22l-.8-2.2L16 19l2.2-.8z',
  pencil: 'M4 20h4L19 9l-4-4L4 16zM14 6l4 4',
} as const;

export type LiveIconName = keyof typeof paths;

export function LiveIcon({
  name,
  size = 18,
  filled,
  ...rest
}: { name: LiveIconName; size?: number; filled?: boolean } & SVGProps<SVGSVGElement>) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill={filled ? 'currentColor' : 'none'}
      stroke="currentColor"
      strokeWidth={1.8}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      {...rest}
    >
      <path d={paths[name]} />
    </svg>
  );
}

/** Channel logo, falling back to initials on a tinted tile. */
export function ChannelLogo({
  channel,
  className,
}: {
  channel: Pick<LiveChannel, 'name' | 'logo'>;
  className?: string;
}) {
  const [failed, setFailed] = useState(false);
  const initials = channel.name
    .replace(/[^\p{L}\p{Nd} ]/gu, '')
    .split(' ')
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0])
    .join('')
    .toUpperCase();
  return (
    <div
      className={cn(
        'flex shrink-0 items-center justify-center overflow-hidden rounded-lg bg-surface-strong',
        className,
      )}
    >
      {channel.logo && !failed ? (
        <img
          src={channel.logo}
          alt=""
          loading="lazy"
          className="h-full w-full object-contain p-1.5"
          onError={() => setFailed(true)}
        />
      ) : (
        <span className="text-xs font-semibold text-muted">{initials || 'TV'}</span>
      )}
    </div>
  );
}

/** Red dot while recording, a clock when scheduled. */
export function RecordingBadge({ recording }: { recording: Recording }) {
  const running = recording.status === 'running' || recording.status === 'starting';
  return (
    <span
      title={running ? 'Recording' : 'Scheduled'}
      aria-label={running ? 'Recording' : 'Scheduled'}
      className={cn('inline-flex shrink-0', running ? 'text-danger' : 'text-accent')}
    >
      {running ? (
        <span className="mt-0.5 inline-block h-2.5 w-2.5 animate-pulse rounded-full bg-danger" />
      ) : (
        <LiveIcon name="clock" size={14} />
      )}
    </span>
  );
}

export function ProgressLine({ value, className }: { value: number; className?: string }) {
  return (
    <div className={cn('h-1 overflow-hidden rounded-full bg-surface-strong', className)}>
      <div className="h-full rounded-full bg-accent" style={{ width: `${value * 100}%` }} />
    </div>
  );
}

export function Notice({
  tone,
  text,
  onClose,
}: {
  tone: 'success' | 'danger';
  text: string;
  onClose?: () => void;
}) {
  return (
    <div
      role={tone === 'danger' ? 'alert' : 'status'}
      className={cn(
        'fixed bottom-6 left-1/2 z-50 flex max-w-[calc(100vw-2rem)] -translate-x-1/2 items-center gap-3 rounded-xl px-4 py-3 text-sm shadow-card glass',
        tone === 'danger' ? 'text-danger' : 'text-fg',
      )}
    >
      <span>{text}</span>
      {onClose && (
        <button
          type="button"
          onClick={onClose}
          className="cursor-pointer text-muted hover:text-fg"
          aria-label="Dismiss"
        >
          ✕
        </button>
      )}
    </div>
  );
}
