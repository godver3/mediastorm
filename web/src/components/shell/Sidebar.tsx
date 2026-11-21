import { Link } from '@tanstack/react-router';
import { cn } from '@/lib/cn';
import { useBranding } from '@/lib/branding';
import { APP_NAME } from '@/lib/documentTitle';
import { Icon } from '../icons';
import { NAV_ITEMS, SETTINGS_ITEM, type NavItem } from './nav';

function NavLink({
  item,
  collapsed,
  onNavigate,
}: {
  item: NavItem;
  collapsed: boolean;
  onNavigate?: () => void;
}) {
  return (
    <Link
      to={item.to}
      onClick={onNavigate}
      activeOptions={{ exact: item.to === '/' }}
      title={collapsed ? item.label : undefined}
      className={cn(
        'group flex h-10 items-center gap-3 rounded-xl px-3 text-sm font-medium text-muted transition',
        'hover:bg-surface hover:text-fg',
        'data-[status=active]:accent-soft',
        collapsed && 'justify-center px-0',
      )}
    >
      <Icon name={item.icon} size={20} />
      {!collapsed && <span className="truncate">{item.label}</span>}
    </Link>
  );
}

function BrandMark({ iconUrl }: { iconUrl?: string }) {
  if (iconUrl) {
    return <img src={iconUrl} alt="" className="h-8 w-8 shrink-0 rounded-xl object-cover" />;
  }
  return (
    <span className="flex h-8 w-8 shrink-0 items-center justify-center rounded-xl bg-accent text-accent-contrast shadow-[0_6px_20px_-8px_var(--accent)]">
      <Icon name="play" size={15} fill="currentColor" />
    </span>
  );
}

/**
 * The server's custom branding (uploaded logo / web icon) when set, else the
 * default mark + wordmark. Renders nothing until branding has loaded so a
 * configured logo doesn't flash the default first.
 */
export function Brand({ collapsed }: { collapsed?: boolean }) {
  const { logoUrl, iconUrl, isLoading } = useBranding();
  if (isLoading) return <div className="h-8" aria-hidden />;
  if (!collapsed && logoUrl) {
    return (
      <div className="flex items-center px-2">
        <img
          src={logoUrl}
          alt={APP_NAME}
          className="h-9 max-w-[180px] object-contain object-left"
        />
      </div>
    );
  }
  return (
    <div className={cn('flex items-center gap-2.5 px-2', collapsed && 'justify-center px-0')}>
      <BrandMark iconUrl={iconUrl} />
      {!collapsed && <span className="text-[15px] font-semibold tracking-tight">{APP_NAME}</span>}
    </div>
  );
}

export interface SidebarProps {
  collapsed: boolean;
  onNavigate?: () => void;
  className?: string;
}

export function Sidebar({ collapsed, onNavigate, className }: SidebarProps) {
  return (
    <nav aria-label="Main" className={cn('flex h-full flex-col gap-1 p-3', className)}>
      <div className="mb-4 flex h-12 items-center">
        <Brand collapsed={collapsed} />
      </div>
      <div className="flex flex-1 flex-col gap-1">
        {NAV_ITEMS.map((item) => (
          <NavLink key={item.to} item={item} collapsed={collapsed} onNavigate={onNavigate} />
        ))}
      </div>
      <NavLink item={SETTINGS_ITEM} collapsed={collapsed} onNavigate={onNavigate} />
    </nav>
  );
}
