import {
  canCancel,
  canDelete,
  canPlay,
  displayTitle,
  durationText,
  sizeText,
  startMs,
  endMs,
  STATUS_LABELS,
  visibleError,
  type Recording,
} from '@/api/recordings';
import { Button, Icon } from '@/components';
import { cn } from '@/lib/cn';
import { formatDay, formatRange } from '@/lib/guide';

const statusTone: Record<Recording['status'], string> = {
  pending: 'bg-[color-mix(in_srgb,var(--accent)_18%,transparent)] text-accent',
  starting: 'bg-[color-mix(in_srgb,var(--danger)_18%,transparent)] text-danger',
  running: 'bg-[color-mix(in_srgb,var(--danger)_18%,transparent)] text-danger',
  completed: 'bg-[color-mix(in_srgb,var(--success)_18%,transparent)] text-success',
  cancelled: 'bg-surface-strong text-muted',
  failed: 'bg-[color-mix(in_srgb,var(--danger)_14%,transparent)] text-danger',
  unknown: 'bg-[color-mix(in_srgb,var(--danger)_14%,transparent)] text-danger',
};

/** Deterministic hue from a string, for the channel tile. */
function hue(s: string) {
  let h = 0;
  for (const ch of s) h = (h * 31 + ch.charCodeAt(0)) % 360;
  return h;
}

export function StatusPill({ recording }: { recording: Recording }) {
  const live = recording.status === 'running' || recording.status === 'starting';
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2 py-0.5 text-xs font-semibold',
        statusTone[recording.status],
      )}
    >
      {live && <span className="h-1.5 w-1.5 animate-pulse rounded-full bg-danger" />}
      {STATUS_LABELS[recording.status]}
    </span>
  );
}

export interface RecordingRowProps {
  recording: Recording;
  now: number;
  busy?: boolean;
  onPlay: (r: Recording) => void;
  onCancel: (r: Recording) => void;
  onDelete: (r: Recording) => void;
}

export function RecordingRow({
  recording: r,
  now,
  busy,
  onPlay,
  onCancel,
  onDelete,
}: RecordingRowProps) {
  const title = displayTitle(r);
  const playable = canPlay(r);
  const err = visibleError(r);
  const s = startMs(r);
  const e = endMs(r);
  const progress =
    r.status === 'running' && e > s ? Math.min(Math.max((now - s) / (e - s), 0), 1) : undefined;
  const meta = [
    s ? `${formatDay(s, now)} · ${formatRange(s, e)}` : null,
    durationText(r) || null,
    sizeText(r.outputSizeBytes) ?? null,
  ].filter(Boolean);
  const h = hue(r.channelName || r.channelId);

  return (
    <li className="group flex flex-col gap-3 rounded-2xl border border-line bg-surface p-3 transition hover:border-line-strong sm:flex-row sm:items-center">
      <button
        type="button"
        disabled={!playable}
        onClick={() => onPlay(r)}
        aria-label={playable ? `Play ${title}` : title}
        className="relative flex aspect-video w-full shrink-0 cursor-pointer items-center justify-center overflow-hidden rounded-xl text-white disabled:cursor-default sm:w-44"
        style={{
          background: `linear-gradient(135deg, hsl(${h} 55% 32%), hsl(${(h + 40) % 360} 60% 18%))`,
        }}
      >
        <span className="px-3 text-center text-sm font-semibold opacity-90 drop-shadow">
          {r.channelName || 'Recording'}
        </span>
        {playable && (
          <span className="absolute inset-0 flex items-center justify-center bg-black/40 opacity-0 transition group-hover:opacity-100">
            <span className="flex h-11 w-11 items-center justify-center rounded-full bg-white/90 text-black">
              <Icon name="play" size={18} />
            </span>
          </span>
        )}
        {progress !== undefined && (
          <span className="absolute inset-x-0 bottom-0 h-1 bg-black/40">
            <span className="block h-full bg-danger" style={{ width: `${progress * 100}%` }} />
          </span>
        )}
      </button>
      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <h3 className="truncate font-semibold">{title}</h3>
          <StatusPill recording={r} />
          {r.ruleId && (
            <span title="From a recording rule" className="text-faint">
              <Icon name="repeat" size={14} />
            </span>
          )}
        </div>
        <p className="mt-0.5 truncate text-sm text-muted">
          {r.channelName}
          {meta.length ? ` · ${meta.join(' · ')}` : ''}
        </p>
        {r.description && <p className="mt-1 line-clamp-2 text-sm text-faint">{r.description}</p>}
        {err && <p className="mt-1 text-sm text-danger">{err}</p>}
      </div>
      <div className="flex shrink-0 flex-wrap gap-2">
        {playable && (
          <Button size="sm" leftIcon={<Icon name="play" size={14} />} onClick={() => onPlay(r)}>
            {r.status === 'running' ? 'Watch' : 'Play'}
          </Button>
        )}
        {canCancel(r) && (
          <Button size="sm" variant="secondary" disabled={busy} onClick={() => onCancel(r)}>
            {r.status === 'pending' ? 'Cancel' : 'Stop'}
          </Button>
        )}
        {canDelete(r) && (
          <Button
            size="sm"
            variant="ghost"
            disabled={busy}
            aria-label={`Delete ${title}`}
            onClick={() => onDelete(r)}
            leftIcon={<Icon name="trash" size={15} />}
          >
            <span className="sm:sr-only">Delete</span>
          </Button>
        )}
      </div>
    </li>
  );
}
