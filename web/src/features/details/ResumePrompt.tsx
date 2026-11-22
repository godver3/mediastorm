import { Button, Dialog, Icon } from '@/components';
import type { StartPoint } from '@/lib/playback';

/** "1:04:34", or "38%" when only a percentage is known (native StartPoint.label). */
export function startLabel(start: NonNullable<StartPoint>, clock: (s: number) => string) {
  return 'seconds' in start ? clock(start.seconds) : `${Math.round(start.percent)}%`;
}

/**
 * Resume / Start over after picking a version or a source (native
 * LibraryVersionsPresentation's confirmation and the source picker's pendingSource).
 */
export function ResumePrompt({
  heading,
  resumeAt,
  clock,
  onChoose,
  onCancel,
}: {
  heading: string;
  resumeAt: NonNullable<StartPoint>;
  clock: (s: number) => string;
  onChoose: (start: StartPoint) => void;
  onCancel: () => void;
}) {
  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onCancel()}
      title={heading}
      description="Pick up where you left off, or play from the beginning."
      size="sm"
    >
      <div className="flex flex-col gap-2">
        <Button leftIcon={<Icon name="play" size={16} />} onClick={() => onChoose(resumeAt)}>
          Resume from {startLabel(resumeAt, clock)}
        </Button>
        <Button
          variant="secondary"
          leftIcon={<Icon name="restart" size={16} />}
          onClick={() => onChoose(undefined)}
        >
          Start over
        </Button>
        <Button variant="ghost" onClick={onCancel}>
          Cancel
        </Button>
      </div>
    </Dialog>
  );
}
