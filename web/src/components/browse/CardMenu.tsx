import * as Menu from '@radix-ui/react-dropdown-menu';
import type { ReactNode } from 'react';
import { Icon } from '@/components/icons';
import { cn } from '@/lib/cn';
import type { TileMenuItem } from './ListTile';

/**
 * A card with a ⋯ actions menu in its top-right corner (the web's long press, native
 * TitleActionsMenu). The button shows on hover / focus, always on touch-size screens.
 */
export function CardMenu({
  label,
  items,
  children,
  className,
}: {
  /** The card's title, for the button's accessible name. */
  label: string;
  items: TileMenuItem[];
  children: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn('group/cardmenu relative shrink-0', className)}>
      {children}
      {items.length > 0 && (
        <Menu.Root modal={false}>
          <Menu.Trigger
            aria-label={`Actions for ${label}`}
            className={cn(
              'glass absolute top-2 right-2 z-10 flex h-8 w-8 cursor-pointer items-center justify-center rounded-full text-fg transition outline-none',
              'opacity-0 group-hover/cardmenu:opacity-100 focus-visible:opacity-100 data-[state=open]:opacity-100 max-sm:opacity-100',
              'focus-visible:ring-2 focus-visible:ring-accent',
            )}
          >
            <Icon name="more" size={16} />
          </Menu.Trigger>
          <Menu.Portal>
            <Menu.Content
              align="end"
              sideOffset={6}
              className="glass z-50 min-w-52 rounded-xl p-1 shadow-card"
            >
              {items.map((m) => (
                <Menu.Item
                  key={m.label}
                  onSelect={m.onSelect}
                  className={cn(
                    'cursor-pointer rounded-lg px-3 py-2 text-sm outline-none data-highlighted:bg-surface-strong',
                    m.danger && 'text-danger',
                  )}
                >
                  {m.label}
                </Menu.Item>
              ))}
            </Menu.Content>
          </Menu.Portal>
        </Menu.Root>
      )}
    </div>
  );
}
