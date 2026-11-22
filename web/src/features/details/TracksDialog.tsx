import type { ReactNode } from 'react';
import {
  audioLabel,
  codecLabel,
  languageLabel,
  releaseTitle,
  subtitleStream,
  trackDefaults,
  type PrequeueStatus,
  type PrequeueSubtitleTrack,
  type TrackOverrides,
} from '@/api/playback';
import { Button, Dialog, Icon } from '@/components';
import { cn } from '@/lib/cn';

/** The audio and subtitle Play will use: the picks over the stream's defaults. */
export function effectiveTracks(status: PrequeueStatus, picks: TrackOverrides) {
  const d = trackDefaults(status);
  const audioIndex = picks.audio ?? d.audio;
  const audios = status.audioTracks ?? [];
  const audio = audios.find((a) => a.index === audioIndex) ?? audios[0];
  const subIndex = picks.subtitle === 'off' ? undefined : (picks.subtitle ?? d.subtitle);
  const subtitle = (status.subtitleTracks ?? []).find((s) => subtitleStream(s) === subIndex);
  return { audio, subtitle };
}

/** "English · E-AC-3" / "English" (or "Off") for the info line and button caption. */
export function tracksSummary(status: PrequeueStatus, picks: TrackOverrides) {
  const { audio, subtitle } = effectiveTracks(status, picks);
  return {
    audio: audio ? audioLabel(audio) : undefined,
    subtitle: subtitle ? subtitleName(subtitle) : undefined,
  };
}

function subtitleName(s: PrequeueSubtitleTrack) {
  return [languageLabel(s.language), s.forced ? 'Forced' : ''].filter(Boolean).join(' · ');
}

/**
 * Audio & Subtitles for the details page's warm-up stream (native DetailsView+Tracks /
 * SourceTracksView): the release name, its audio and subtitle tracks with what Play would
 * pick marked, and Play with the picks. Bitmap subtitles (PGS, VobSub) can't be drawn by
 * the web player, so they're listed but disabled.
 */
export function TracksDialog({
  status,
  picks,
  onPicksChange,
  open,
  onOpenChange,
  onPlay,
}: {
  status: PrequeueStatus;
  picks: TrackOverrides;
  onPicksChange: (p: TrackOverrides) => void;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onPlay: () => void;
}) {
  const { audio, subtitle } = effectiveTracks(status, picks);
  const audios = status.audioTracks ?? [];
  const subs = status.subtitleTracks ?? [];
  return (
    <Dialog
      open={open}
      onOpenChange={onOpenChange}
      title="Audio & Subtitles"
      description={<span className="break-all">{releaseTitle(status)}</span>}
      footer={
        <>
          <Button variant="ghost" onClick={() => onPicksChange({})}>
            Reset
          </Button>
          <Button onClick={onPlay} leftIcon={<Icon name="play" size={16} />}>
            Play
          </Button>
        </>
      }
    >
      <div className="grid max-h-[55vh] gap-5 overflow-y-auto sm:grid-cols-2">
        <TrackGroup label="Audio">
          {audios.length === 0 && <Empty>No audio tracks listed</Empty>}
          {audios.map((a) => (
            <TrackOption
              key={a.index}
              name="audio"
              checked={audio?.index === a.index}
              onSelect={() => onPicksChange({ ...picks, audio: a.index })}
              title={audioLabel(a)}
              detail={a.title}
            />
          ))}
        </TrackGroup>
        <TrackGroup label="Subtitles">
          <TrackOption
            name="subtitle"
            checked={!subtitle}
            onSelect={() => onPicksChange({ ...picks, subtitle: 'off' })}
            title="Off"
          />
          {subs.map((s) => (
            <TrackOption
              key={subtitleStream(s)}
              name="subtitle"
              checked={subtitle !== undefined && subtitleStream(subtitle) === subtitleStream(s)}
              disabled={s.isBitmap}
              onSelect={() => onPicksChange({ ...picks, subtitle: subtitleStream(s) })}
              title={subtitleName(s)}
              detail={
                [s.title, codecLabel(s.codec), s.isBitmap ? 'Not supported in the browser' : '']
                  .filter(Boolean)
                  .join(' · ') || undefined
              }
            />
          ))}
        </TrackGroup>
      </div>
    </Dialog>
  );
}

function TrackGroup({ label, children }: { label: string; children: ReactNode }) {
  return (
    <fieldset className="min-w-0">
      <legend className="mb-2 text-xs font-semibold tracking-wide text-muted uppercase">
        {label}
      </legend>
      <div className="space-y-1.5">{children}</div>
    </fieldset>
  );
}

const Empty = ({ children }: { children: ReactNode }) => (
  <p className="text-sm text-muted">{children}</p>
);

function TrackOption({
  name,
  checked,
  onSelect,
  title,
  detail,
  disabled,
}: {
  name: string;
  checked: boolean;
  onSelect: () => void;
  title: string;
  detail?: string;
  disabled?: boolean;
}) {
  return (
    <label
      className={cn(
        'flex cursor-pointer items-center gap-3 rounded-xl border px-3 py-2 text-sm transition',
        'has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-accent',
        checked
          ? 'border-transparent accent-soft'
          : 'border-line bg-surface hover:bg-surface-strong',
        disabled && 'pointer-events-none opacity-50',
      )}
    >
      <input
        type="radio"
        name={name}
        checked={checked}
        disabled={disabled}
        onChange={onSelect}
        className="sr-only"
      />
      <span
        className={cn(
          'h-4 w-4 shrink-0 rounded-full border-2',
          checked ? 'border-accent bg-accent shadow-[inset_0_0_0_2px_var(--bg)]' : 'border-line',
        )}
        aria-hidden="true"
      />
      <span className="min-w-0 flex-1">
        <span className="block truncate font-medium">{title}</span>
        {detail && <span className="block truncate text-xs text-muted">{detail}</span>}
      </span>
    </label>
  );
}
