import { useQuery } from '@tanstack/react-query';
import {
  fetchStremioStreams,
  liveKeys,
  type LiveChannel,
  type LiveStremioStream,
} from '@/api/live';
import { Dialog, ErrorState, Icon, Skeleton } from '@/components';
import { ChannelLogo } from './LiveBits';

export interface StremioSourcePickerProps {
  profileId: string;
  channel: LiveChannel | null;
  onClose: () => void;
  onPick: (channel: LiveChannel, stream: LiveStremioStream) => void;
}

/**
 * Choose Source for a Stremio-backed channel (native LiveSourceChoice): the addon's streams in
 * its order; the pick is tuned with `stremioStreamIndex`.
 */
export function StremioSourcePicker({
  profileId,
  channel,
  onClose,
  onPick,
}: StremioSourcePickerProps) {
  const streams = useQuery({
    queryKey: liveKeys.stremioStreams(profileId, channel?.url ?? ''),
    queryFn: ({ signal }) => fetchStremioStreams(profileId, channel!, signal),
    enabled: !!channel,
    staleTime: 60_000,
    retry: false,
  });
  if (!channel) return null;

  let body;
  if (streams.isPending) {
    body = (
      <div className="flex flex-col gap-2" aria-label="Loading sources">
        {Array.from({ length: 4 }, (_, i) => (
          <Skeleton key={i} className="h-12 rounded-xl" />
        ))}
      </div>
    );
  } else if (streams.isError) {
    body = <ErrorState error={streams.error} onRetry={() => void streams.refetch()} />;
  } else if (!streams.data.length) {
    body = <p className="py-6 text-center text-sm text-muted">No playable sources found.</p>;
  } else {
    body = (
      <ul className="flex flex-col gap-1">
        {streams.data.map((s) => (
          <li key={s.index}>
            <button
              type="button"
              onClick={() => onPick(channel, s)}
              className="flex w-full cursor-pointer items-start gap-3 rounded-xl px-3 py-2.5 text-left transition hover:bg-surface-strong focus-visible:outline-2 focus-visible:outline-accent"
            >
              <Icon name="play" size={16} className="mt-0.5 shrink-0 text-muted" />
              <span className="min-w-0">
                {/* The label is the whole release line; wrap rather than truncate (native). */}
                <span className="line-clamp-3 text-sm font-medium break-words whitespace-pre-line">
                  {s.label}
                </span>
                {s.description && s.description !== s.label && (
                  <span className="mt-0.5 line-clamp-2 block text-xs text-muted">
                    {s.description}
                  </span>
                )}
              </span>
            </button>
          </li>
        ))}
      </ul>
    );
  }

  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={
        <span className="flex items-center gap-3">
          <ChannelLogo channel={channel} className="h-10 w-10" />
          <span className="min-w-0">
            <span className="block truncate">{channel.name}</span>
            <span className="block truncate text-sm font-normal text-muted">
              Choose stream source
            </span>
          </span>
        </span>
      }
      size="md"
    >
      {body}
    </Dialog>
  );
}
