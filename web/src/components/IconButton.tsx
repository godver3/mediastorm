import { forwardRef, type ButtonHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';
import { Icon, type IconName } from './icons';

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon: IconName;
  /** Required: used as aria-label and title. */
  label: string;
  size?: 'sm' | 'md' | 'lg';
  variant?: 'ghost' | 'glass' | 'solid';
}

const sizes = { sm: 'h-8 w-8', md: 'h-10 w-10', lg: 'h-12 w-12' };
const iconSizes = { sm: 16, md: 20, lg: 22 };
const variants = {
  ghost: 'text-muted hover:text-fg hover:bg-surface',
  glass: 'glass text-fg hover:bg-surface-strong',
  solid: 'bg-accent text-accent-contrast hover:brightness-110',
};

export const IconButton = forwardRef<HTMLButtonElement, IconButtonProps>(function IconButton(
  { icon, label, size = 'md', variant = 'ghost', className, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type="button"
      aria-label={label}
      title={label}
      className={cn(
        'inline-flex shrink-0 items-center justify-center rounded-full transition cursor-pointer',
        'disabled:opacity-40 disabled:pointer-events-none',
        sizes[size],
        variants[variant],
        className,
      )}
      {...rest}
    >
      <Icon name={icon} size={iconSizes[size]} />
    </button>
  );
});
