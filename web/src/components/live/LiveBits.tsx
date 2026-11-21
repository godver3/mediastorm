import { Icon } from '@/components/icons';
import { useState } from 'react';
import type { LiveChannel } from '@/api/live';
import type { Recording } from '@/api/recordings';
import { cn } from '@/lib/cn';

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
        <Icon name="clock" size={14} />
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
