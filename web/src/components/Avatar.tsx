import { useState } from 'react';
import { profileIconUrl } from '@/api/users';
import type { UserProfile } from '@/api/types';
import { cn } from '@/lib/cn';
import { normalizeHex } from '@/lib/theme';

export interface ProfileAvatarProps {
  profile: Pick<UserProfile, 'id' | 'name' | 'color' | 'hasIcon' | 'iconUrl'>;
  size?: number;
  className?: string;
}

/** Profile image, or initial on the profile colour. */
export function ProfileAvatar({ profile, size = 40, className }: ProfileAvatarProps) {
  const [failed, setFailed] = useState(false);
  const icon = profileIconUrl(profile);
  const color = normalizeHex(profile.color) ?? 'var(--accent)';
  return (
    <span
      className={cn(
        'inline-flex shrink-0 items-center justify-center overflow-hidden rounded-full font-semibold text-white',
        className,
      )}
      style={{ width: size, height: size, background: color, fontSize: size * 0.42 }}
      aria-hidden="true"
    >
      {icon && !failed ? (
        <img
          src={icon}
          alt=""
          className="h-full w-full object-cover"
          onError={() => setFailed(true)}
        />
      ) : (
        (profile.name.trim()[0] ?? '?').toUpperCase()
      )}
    </span>
  );
}
