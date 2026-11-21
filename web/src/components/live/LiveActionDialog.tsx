import type { ReactNode } from 'react';
import {
  canReplay,
  hasStremioSources,
  isAiring,
  type EPGProgram,
  type LiveChannel,
} from '@/api/live';
import { recordingFor, seriesRuleFor, type Recording, type RecordingRule } from '@/api/recordings';
import { Button, Dialog, Icon, type IconName } from '@/components';
import { cn } from '@/lib/cn';
import { formatDay, formatRange, HOUR } from '@/lib/guide';
import { ChannelLogo } from './LiveBits';
import type { useRecordActions } from './useLiveActions';

export interface LiveActionTarget {
  channel: LiveChannel;
  /** A guide programme; without one this is the channel menu (acting on what is on now). */
  program?: EPGProgram;
}

export interface LiveActionDialogProps {
  target: LiveActionTarget | null;
  onClose: () => void;
  now: number;
  /** What is on the channel now (for the channel menu). */
  current?: EPGProgram;
  recordings: Recording[];
  rules: RecordingRule[];
  favorite: boolean;
  hidden: boolean;
  epgEnabled: boolean;
  onTune: (channel: LiveChannel, programTitle?: string) => void;
  onToggleFavorite: (channel: LiveChannel) => void;
  onToggleHidden: (channel: LiveChannel) => void;
  /** Choose Source for a Stremio-backed channel. */
  onChooseSource?: (channel: LiveChannel) => void;
  /** Catch-up: play `program` from its start from the provider's archive. */
  onReplay?: (channel: LiveChannel, program: EPGProgram) => void;
  actions: ReturnType<typeof useRecordActions>;
}

function ActionRow({
  icon,
  label,
  onClick,
  danger,
  disabled,
}: {
  icon: IconName;
  label: ReactNode;
  onClick: () => void;
  danger?: boolean;
  disabled?: boolean;
}) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className={cn(
        'flex w-full cursor-pointer items-center gap-3 rounded-xl px-3 py-2.5 text-left text-sm transition hover:bg-surface-strong focus-visible:outline-2 focus-visible:outline-accent disabled:pointer-events-none disabled:opacity-50',
        danger ? 'text-danger' : 'text-fg',
      )}
    >
      <Icon size={18} name={icon} className={danger ? '' : 'text-muted'} />
      {label}
    </button>
  );
}

/** Programme details + recording actions (native LiveProgramRecordMenu / LiveChannelRecordMenu). */
export function LiveActionDialog({
  target,
  onClose,
  now,
  current,
  recordings,
  rules,
  favorite,
  hidden,
  epgEnabled,
  onTune,
  onToggleFavorite,
  onToggleHidden,
  onChooseSource,
  onReplay,
  actions,
}: LiveActionDialogProps) {
  if (!target) return null;
  const { channel } = target;
  const program = target.program ?? (current && current.stop > now ? current : undefined);
  const isMenu = !target.program;
  const ended = program ? program.stop <= now : false;
  const airing = program ? isAiring(program, now) : false;
  const replayable = !!onReplay && !!program && canReplay(channel, program, now);
  const choosable = !!onChooseSource && hasStremioSources(channel);
  const recording = program ? recordingFor(recordings, channel, program, now) : undefined;
  const rule = program ? seriesRuleFor(rules, program.title, channel) : undefined;
  const busy = actions.isPending;
  const run = (a: Parameters<typeof actions.mutate>[0]) =>
    actions.mutate(a, { onSuccess: onClose });

  return (
    <Dialog
      open
      onOpenChange={(o) => !o && onClose()}
      title={
        <span className="flex items-center gap-3">
          <ChannelLogo channel={channel} className="h-10 w-10" />
          <span className="min-w-0">
            <span className="block truncate">
              {program && !isMenu ? program.title : channel.name}
            </span>
            <span className="block truncate text-sm font-normal text-muted">
              {program && !isMenu
                ? channel.name
                : (channel.group ?? channel.sourceName ?? 'Live TV')}
            </span>
          </span>
        </span>
      }
      size="md"
    >
      {program && (
        <div className="mb-4 rounded-xl bg-surface p-3">
          {isMenu && <p className="font-medium">{program.title}</p>}
          <p className="text-sm text-muted">
            {formatDay(program.start, now)} · {formatRange(program.start, program.stop)}
            {program.episode ? ` · ${program.episode}` : ''}
            {airing && <span className="ml-2 font-semibold text-accent">On now</span>}
          </p>
          {program.description && (
            <p className="mt-2 line-clamp-4 text-sm text-fg/85">{program.description}</p>
          )}
          {(program.isNew || program.isRerun || program.categories?.length) && (
            <div className="mt-2 flex flex-wrap gap-1.5">
              {program.isNew && <Chip>New</Chip>}
              {program.isRerun && <Chip>Rerun</Chip>}
              {program.categories?.slice(0, 4).map((c) => (
                <Chip key={c}>{c}</Chip>
              ))}
            </div>
          )}
        </div>
      )}

      <div className="flex flex-col gap-0.5">
        {(isMenu || airing) && (
          <div className="mb-2">
            <Button
              className="w-full"
              leftIcon={<Icon name="play" size={16} />}
              onClick={() => onTune(channel, airing ? program?.title : undefined)}
            >
              Watch {channel.name}
            </Button>
          </div>
        )}
        {replayable && program && (
          <div className="mb-2">
            <Button
              className="w-full"
              variant={ended ? 'primary' : 'secondary'}
              leftIcon={<Icon name="restart" size={16} />}
              onClick={() => onReplay?.(channel, program)}
            >
              {ended ? 'Watch from catch-up' : 'Watch from the start'}
            </Button>
          </div>
        )}
        {ended && program && !replayable && (
          <p className="mb-2 rounded-xl bg-surface px-3 py-2 text-sm text-muted">
            {channel.catchup
              ? `This programme is older than the ${channel.catchup.days}-day catch-up window.`
              : 'This programme has ended. The channel has no catch-up.'}
          </p>
        )}
        {choosable && isMenu && (
          <ActionRow
            icon="sources"
            label="Choose source"
            onClick={() => onChooseSource?.(channel)}
          />
        )}

        {program && !ended && epgEnabled && (
          <>
            {recording ? (
              <ActionRow
                icon="stop"
                danger
                disabled={busy}
                label={
                  recording.status === 'pending' ? 'Cancel scheduled recording' : 'Stop recording'
                }
                onClick={() => run({ kind: 'cancel', recording })}
              />
            ) : (
              <ActionRow
                icon="clock"
                disabled={busy}
                label={isMenu ? 'Record current programme' : airing ? 'Record' : 'Record once'}
                onClick={() => run({ kind: 'record', program, channel })}
              />
            )}
            {rule ? (
              <>
                <ActionRow
                  icon={rule.newOnly ? 'repeat' : 'sparkle'}
                  disabled={busy}
                  label={
                    rule.newOnly
                      ? 'Record every airing instead'
                      : 'Record new episodes only instead'
                  }
                  onClick={() => run({ kind: 'setSeries', rule, newOnly: !rule.newOnly })}
                />
                <ActionRow
                  icon="trash"
                  danger
                  disabled={busy}
                  label={
                    rule.matchType === 'regex'
                      ? `Delete rule “${rule.pattern}”`
                      : 'Cancel series recording'
                  }
                  onClick={() => run({ kind: 'cancelSeries', rule })}
                />
              </>
            ) : (
              <>
                <ActionRow
                  icon="repeat"
                  disabled={busy}
                  label={isMenu ? 'Record every airing' : 'Record series'}
                  onClick={() => run({ kind: 'series', program, channel, newOnly: false })}
                />
                {!isMenu && (
                  <ActionRow
                    icon="sparkle"
                    disabled={busy}
                    label="Record new episodes only"
                    onClick={() => run({ kind: 'series', program, channel, newOnly: true })}
                  />
                )}
              </>
            )}
          </>
        )}

        {isMenu && (
          <>
            <ActionRow
              icon="clock"
              disabled={busy}
              label="Record 2 hour block"
              onClick={() => run({ kind: 'block', channel, durationMs: 2 * HOUR })}
            />
            <div className="my-1 border-t border-line" />
            <ActionRow
              icon="star"
              label={favorite ? 'Remove from favorites' : 'Add to favorites'}
              onClick={() => onToggleFavorite(channel)}
            />
            <ActionRow
              icon={hidden ? 'eye' : 'eyeOff'}
              label={hidden ? 'Unhide channel' : 'Hide channel'}
              onClick={() => {
                onToggleHidden(channel);
                onClose();
              }}
            />
          </>
        )}
      </div>
    </Dialog>
  );
}

function Chip({ children }: { children: ReactNode }) {
  return (
    <span className="rounded-md bg-surface-strong px-2 py-0.5 text-xs text-muted">{children}</span>
  );
}
