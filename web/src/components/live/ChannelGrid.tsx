import { guideKey, programProgress, type EPGNowPlaying, type LiveChannel } from '@/api/live';
import { recordingFor, type Recording } from '@/api/recordings';
import { cn } from '@/lib/cn';
import { formatClock } from '@/lib/guide';
import { ChannelLogo, LiveIcon, ProgressLine, RecordingBadge } from './LiveBits';

export interface ChannelGridProps {
  channels: LiveChannel[];
  nowNext: Record<string, EPGNowPlaying>;
  now: number;
  favorites: string[];
  hidden: string[];
  recordings: Recording[];
  tuningId: string | null;
  onTune: (channel: LiveChannel) => void;
  onChannelMenu: (channel: LiveChannel) => void;
  onToggleFavorite: (channel: LiveChannel) => void;
}

/** Channel cards with now / next (native LiveChannelGrid). */
export function ChannelGrid({
  channels,
  nowNext,
  now,
  favorites,
  hidden,
  recordings,
  tuningId,
  onTune,
  onChannelMenu,
  onToggleFavorite,
}: ChannelGridProps) {
  return (
    <ul className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3 2xl:grid-cols-4">
      {channels.map((channel) => {
        const nn = nowNext[guideKey(channel)];
        const cur = nn?.current && nn.current.stop > now ? nn.current : undefined;
        const next = nn?.next;
        const fav = favorites.includes(channel.id);
        const rec = recordingFor(recordings, channel, undefined, now);
        return (
          <li
            key={channel.id}
            className={cn(
              'group relative flex gap-3 rounded-2xl border border-line bg-surface p-3 transition hover:border-line-strong hover:bg-surface-strong',
              hidden.includes(channel.id) && 'opacity-50',
            )}
          >
            <button
              type="button"
              onClick={() => onTune(channel)}
              aria-label={`Watch ${channel.name}`}
              className="absolute inset-0 cursor-pointer rounded-2xl focus-visible:outline-2 focus-visible:outline-accent"
            />
            <ChannelLogo
              channel={channel}
              className={cn(
                'pointer-events-none h-16 w-16',
                tuningId === channel.id && 'animate-pulse',
              )}
            />
            <div className="pointer-events-none min-w-0 flex-1">
              <div className="flex items-center gap-1.5">
                <span className="truncate font-medium">{channel.name}</span>
                {rec && <RecordingBadge recording={rec} />}
              </div>
              {cur ? (
                <>
                  <p className="mt-0.5 truncate text-sm text-fg/90">{cur.title}</p>
                  <ProgressLine value={programProgress(cur, now)} className="mt-1.5" />
                  {next && (
                    <p className="mt-1.5 truncate text-xs text-faint">
                      {formatClock(next.start)} · {next.title}
                    </p>
                  )}
                </>
              ) : (
                <p className="mt-0.5 truncate text-sm text-faint">
                  {channel.group ?? channel.sourceName ?? 'Live'}
                </p>
              )}
            </div>
            <div className="relative z-10 flex flex-col items-center gap-1">
              <button
                type="button"
                onClick={() => onToggleFavorite(channel)}
                aria-pressed={fav}
                aria-label={
                  fav ? `Remove ${channel.name} from favorites` : `Add ${channel.name} to favorites`
                }
                className={cn(
                  'flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg hover:bg-surface',
                  fav
                    ? 'text-warning'
                    : 'text-faint opacity-0 group-hover:opacity-100 focus-visible:opacity-100',
                )}
              >
                <LiveIcon name="star" filled={fav} />
              </button>
              <button
                type="button"
                onClick={() => onChannelMenu(channel)}
                aria-label={`${channel.name} options`}
                className="flex h-8 w-8 cursor-pointer items-center justify-center rounded-lg text-faint opacity-0 group-hover:opacity-100 hover:bg-surface hover:text-fg focus-visible:opacity-100 max-sm:opacity-100"
              >
                ⋯
              </button>
            </div>
          </li>
        );
      })}
    </ul>
  );
}
