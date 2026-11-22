import { Icon } from '@/components/icons';
import { Dialog } from '@/components';
import type { QualityPreset } from '@/api/qualityPresets';
import { cn } from '@/lib/cn';

/**
 * Pick the quality preset Play and Choose source rank this title's releases with (native
 * QualityPresetDialog). The server remembers the pick for the title on this device; picking
 * the default clears that memory.
 */
export function QualityPresetDialog({
  presets,
  active,
  defaultId,
  open,
  onOpenChange,
  onPick,
}: {
  presets: QualityPreset[];
  /** The preset Play uses now. */
  active: string;
  /** The preset Play would use without a remembered pick. */
  defaultId: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onPick: (id: string) => void;
}) {
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Quality"
      description="The quality preset Play and Choose source use for this title. It is remembered for this title on this device."
    >
      <ul className="space-y-1.5" aria-label="Quality presets">
        {presets.map((p) => {
          const selected = p.id === active;
          return (
            <li key={p.id}>
              <button
                type="button"
                aria-pressed={selected}
                onClick={() => onPick(p.id)}
                className={cn(
                  'flex w-full cursor-pointer items-center gap-3 rounded-xl border px-3 py-2.5 text-left transition',
                  'focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none',
                  selected
                    ? 'border-accent bg-surface-strong'
                    : 'border-line bg-surface hover:bg-surface-strong',
                )}
              >
                <span
                  className={cn(
                    'flex h-9 w-9 shrink-0 items-center justify-center rounded-lg',
                    selected ? 'accent-soft' : 'bg-surface-strong text-muted',
                  )}
                >
                  <Icon name={selected ? 'check' : 'quality'} size={18} />
                </span>
                <span className="min-w-0 flex-1 truncate text-sm font-medium">
                  {p.name}
                  {p.id === defaultId && (
                    <span className="ml-2 text-xs font-normal text-muted">Default</span>
                  )}
                </span>
              </button>
            </li>
          );
        })}
      </ul>
    </Dialog>
  );
}
