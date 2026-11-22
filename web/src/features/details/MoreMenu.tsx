import * as Menu from '@radix-ui/react-dropdown-menu';
import { orderingLabel, type SeriesOrdering } from '@/api/details';
import { Icon } from '@/components';
import { cn } from '@/lib/cn';

const itemClass =
  'flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm outline-none data-[highlighted]:bg-surface-strong data-[disabled]:opacity-50';

/**
 * The details page's More Options (native DetailsView+More moreMenu): Hide
 * from this profile / Unhide, and, for series with several, the episode order
 * (DetailsView+Ordering).
 */
export function MoreMenu({
  hidden,
  onToggleHidden,
  hiddenBusy,
  orderings,
  activeOrdering,
  onPickOrdering,
}: {
  hidden: boolean;
  onToggleHidden: () => void;
  hiddenBusy?: boolean;
  orderings?: SeriesOrdering[];
  activeOrdering?: string;
  onPickOrdering: (type: string) => void;
}) {
  const offersOrderings = (orderings?.length ?? 0) > 1;
  const active =
    orderings?.find((o) => o.type === activeOrdering) ?? orderings?.find((o) => o.isOfficial);
  return (
    <Menu.Root>
      <Menu.Trigger
        aria-label="More options"
        title="More options"
        className={cn(
          'glass inline-flex h-12 cursor-pointer items-center gap-2 rounded-xl border border-line px-3.5 text-sm font-medium text-fg transition hover:bg-surface-strong',
          'focus-visible:ring-2 focus-visible:ring-accent focus-visible:outline-none',
        )}
      >
        <Icon name="more" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content
          align="start"
          sideOffset={8}
          className="glass z-50 max-h-[70vh] min-w-60 overflow-y-auto rounded-xl p-1.5 shadow-card"
        >
          <Menu.Item
            className={cn(itemClass, !hidden && 'text-danger')}
            disabled={hiddenBusy}
            onSelect={onToggleHidden}
          >
            <Icon name={hidden ? 'eye' : 'eyeOff'} size={18} />
            {hidden ? 'Unhide' : 'Hide from this profile'}
          </Menu.Item>
          {offersOrderings && (
            <>
              <Menu.Separator className="my-1 h-px bg-line" />
              <Menu.Label className="px-2.5 pt-1.5 text-xs font-semibold text-muted">
                Episode order
              </Menu.Label>
              <Menu.Label className="px-2.5 pb-1.5 text-xs text-faint">
                Non-default orders pause watch-history sync for this show.
              </Menu.Label>
              <Menu.RadioGroup value={active?.type} onValueChange={onPickOrdering}>
                {orderings!.map((o) => (
                  <Menu.RadioItem key={o.type} value={o.type} className={itemClass}>
                    <span className="flex w-[18px] justify-center">
                      <Menu.ItemIndicator>
                        <Icon name="check" size={16} />
                      </Menu.ItemIndicator>
                    </span>
                    {orderingLabel(o)}
                  </Menu.RadioItem>
                ))}
              </Menu.RadioGroup>
            </>
          )}
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  );
}
