import * as Menu from '@radix-ui/react-dropdown-menu';
import { useQueryClient } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { useActiveProfile, useLogout } from '@/auth/hooks';
import { ProfileAvatar } from '../Avatar';
import { Icon } from '../icons';

const itemClass =
  'flex cursor-pointer items-center gap-2.5 rounded-lg px-2.5 py-2 text-sm outline-none data-[highlighted]:bg-surface-strong';

export function ProfileMenu() {
  const { profile } = useActiveProfile();
  const logout = useLogout();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  if (!profile) return null;
  return (
    <Menu.Root>
      <Menu.Trigger
        className="flex cursor-pointer items-center gap-2 rounded-full p-1 pr-2 transition hover:bg-surface"
        aria-label={`Profile: ${profile.name}`}
      >
        <ProfileAvatar profile={profile} size={32} />
        <Icon name="chevronDown" size={16} className="text-muted max-sm:hidden" />
      </Menu.Trigger>
      <Menu.Portal>
        <Menu.Content
          align="end"
          sideOffset={8}
          className="glass z-50 min-w-56 rounded-xl p-1.5 shadow-card"
        >
          <div className="flex items-center gap-2.5 px-2.5 py-2">
            <ProfileAvatar profile={profile} size={36} />
            <div className="min-w-0">
              <div className="truncate text-sm font-semibold">{profile.name}</div>
              <div className="text-xs text-muted">
                {profile.isKidsProfile ? 'Kids profile' : 'Profile'}
              </div>
            </div>
          </div>
          <Menu.Separator className="my-1 h-px bg-line" />
          <Menu.Item
            className={itemClass}
            onSelect={() => {
              queryClient.clear();
              void navigate({ to: '/profiles' });
            }}
          >
            <Icon name="users" size={18} /> Switch profile
          </Menu.Item>
          <Menu.Item className={itemClass} onSelect={() => void navigate({ to: '/settings' })}>
            <Icon name="settings" size={18} /> Settings
          </Menu.Item>
          <Menu.Separator className="my-1 h-px bg-line" />
          <Menu.Item className={itemClass + ' text-danger'} onSelect={() => void logout()}>
            <Icon name="logout" size={18} /> Sign out
          </Menu.Item>
        </Menu.Content>
      </Menu.Portal>
    </Menu.Root>
  );
}
