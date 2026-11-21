import { Icon } from '@/components/icons';
import { cn } from '@/lib/cn';

/** Follow / Following toggle (star), used for teams and competitions. */
export function FollowButton({
  followed,
  onToggle,
  name,
  compact,
  disabled,
  text,
}: {
  followed: boolean;
  onToggle: (next: boolean) => void;
  name: string;
  compact?: boolean;
  disabled?: boolean;
  /** Shown after the star instead of Follow/Following. */
  text?: string;
}) {
  const label = followed ? `Unfollow ${name}` : `Follow ${name}`;
  return (
    <button
      type="button"
      aria-pressed={followed}
      aria-label={label}
      title={label}
      disabled={disabled}
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        onToggle(!followed);
      }}
      className={cn(
        'inline-flex shrink-0 cursor-pointer items-center justify-center gap-1.5 rounded-full border text-sm font-medium transition disabled:opacity-50',
        compact ? 'h-8 w-8' : 'h-8 px-3',
        followed
          ? 'border-transparent accent-soft'
          : 'border-line text-muted hover:bg-surface-strong hover:text-fg',
      )}
    >
      <Icon name="star" size={15} filled={followed} />
      {!compact && (text ?? (followed ? 'Following' : 'Follow'))}
    </button>
  );
}
