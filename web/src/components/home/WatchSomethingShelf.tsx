import { useMutation, useQuery } from '@tanstack/react-query';
import { useNavigate } from '@tanstack/react-router';
import { apiRequest } from '@/api/client';
import { getSurprise } from '@/api/home';
import { detailsLink } from '@/lib/links';
import { Icon, type IconName } from '../icons';
import { Shelf } from '../Shelf';
import { Spinner } from '../Spinner';

interface ServerSettings {
  metadata?: { aiApiKey?: string; geminiApiKey?: string };
}

/** Whether the server has an AI key (masked in /api/settings); false when unreadable. */
function useHasAI() {
  return useQuery({
    queryKey: ['home', 'server-ai'],
    queryFn: async ({ signal }) => {
      try {
        const s = await apiRequest<ServerSettings>('/settings', { signal });
        return [s?.metadata?.aiApiKey, s?.metadata?.geminiApiKey].some((k) => !!k);
      } catch {
        return false;
      }
    },
    staleTime: 10 * 60_000,
  }).data;
}

function ActionTile({
  title,
  subtitle,
  icon,
  gradient,
  onClick,
  busy,
}: {
  title: string;
  subtitle: string;
  icon: IconName;
  gradient: string;
  onClick: () => void;
  busy?: boolean;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={busy}
      className="group/card relative aspect-video w-[260px] shrink-0 snap-start cursor-pointer overflow-hidden rounded-card text-left ring-1 ring-line transition outline-none hover:-translate-y-1 hover:shadow-card hover:ring-line-strong focus-visible:ring-2 focus-visible:ring-accent sm:w-[300px] lg:w-[340px]"
      style={{ background: gradient }}
    >
      <div className="absolute top-4 right-4 flex h-11 w-11 items-center justify-center rounded-2xl bg-white/15 text-white backdrop-blur">
        {busy ? <Spinner /> : <Icon name={icon} size={22} />}
      </div>
      <div className="absolute inset-x-0 bottom-0 p-4 text-white">
        <div className="text-lg font-semibold">{title}</div>
        <div className="text-sm text-white/75">{subtitle}</div>
      </div>
    </button>
  );
}

/** Watch Something: Search, and Surprise Me when the server has an AI key. */
export function WatchSomethingShelf({ title, profileId }: { title: string; profileId: string }) {
  const navigate = useNavigate();
  const hasAI = useHasAI();
  const surprise = useMutation({
    mutationFn: () => getSurprise(profileId),
    onSuccess: (t) => void navigate(detailsLink(t)),
  });
  return (
    <Shelf title={title}>
      <ActionTile
        title="Search"
        subtitle="Find a movie, show or person"
        icon="search"
        gradient="linear-gradient(135deg, color-mix(in srgb, var(--accent) 70%, #000) 0%, #101018 100%)"
        onClick={() => void navigate({ to: '/search' })}
      />
      {hasAI && (
        <ActionTile
          title="Surprise Me"
          subtitle={
            surprise.isError ? 'Couldn’t pick one — try again' : 'A random pick for tonight'
          }
          icon="play"
          gradient="linear-gradient(135deg, #7b3fe4 0%, #1a1030 100%)"
          busy={surprise.isPending}
          onClick={() => surprise.mutate()}
        />
      )}
    </Shelf>
  );
}
