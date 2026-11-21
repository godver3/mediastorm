import { Icon } from '@/components/icons';
import { versionOrigin, versionQuality, type LibraryVersion } from '@/api/details';
import { Button, Dialog } from '@/components';
import { formatBytes } from './format';

/**
 * A title's copies in the profile's local / Plex / Jellyfin libraries (native
 * DetailsView+Library versionsPicker). Opened from Play when there are several, it
 * also offers Stream instead.
 */
export function VersionsDialog({
  versions,
  open,
  onOpenChange,
  onPick,
  onStreamInstead,
}: {
  versions: LibraryVersion[];
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onPick: (v: LibraryVersion) => void;
  onStreamInstead?: () => void;
}) {
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Versions"
      description="Copies of this title in your libraries."
      footer={
        onStreamInstead && (
          <Button variant="secondary" onClick={onStreamInstead}>
            Stream instead
          </Button>
        )
      }
    >
      <ul className="space-y-1.5">
        {versions.map((v, i) => (
          <li key={v.item.id}>
            <button
              type="button"
              onClick={() => onPick(v)}
              className="flex w-full cursor-pointer items-center gap-3 rounded-xl border border-line bg-surface px-3 py-2.5 text-left transition hover:bg-surface-strong focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none"
            >
              <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg accent-soft">
                <Icon name="drive" size={18} />
              </span>
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">
                  {versionQuality(v)}
                  {i === 0 && <span className="ml-2 text-xs font-normal text-muted">Default</span>}
                </div>
                <div className="truncate text-xs text-muted">
                  {versionOrigin(v)}
                  {v.item.fileName ? ` · ${v.item.fileName}` : ''}
                </div>
              </div>
              {formatBytes(v.item.sizeBytes ?? 0) && (
                <span className="shrink-0 text-xs text-muted">
                  {formatBytes(v.item.sizeBytes ?? 0)}
                </span>
              )}
            </button>
          </li>
        ))}
      </ul>
    </Dialog>
  );
}
