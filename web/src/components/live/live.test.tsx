import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import type { EPGProgram, LiveChannel } from '@/api/live';
import { mapRecording } from '@/api/recordings';
import { RecordingRow } from '@/components/recordings/RecordingRow';
import { HOUR } from '@/lib/guide';
import { GuideGrid } from './GuideGrid';

const now = Date.parse('2026-11-21T18:10:00Z');
const start = Date.parse('2026-11-21T17:30:00Z');
const channel: LiveChannel = { id: 'c1', name: 'BBC One', url: 'u', tvgId: 'bbc1' };
const prog = (title: string, s: number, e: number): EPGProgram => ({
  title,
  start: s,
  stop: e,
  isNew: false,
  isRerun: false,
});

describe('GuideGrid', () => {
  it('renders programme blocks and routes clicks', async () => {
    const onProgram = vi.fn();
    const onTune = vi.fn();
    render(
      <GuideGrid
        channels={[channel]}
        schedules={{
          bbc1: [
            prog('News', start, now + 20 * 60_000),
            prog('Film', now + 20 * 60_000, now + 2 * HOUR),
          ],
        }}
        scheduleLoading={false}
        start={start}
        now={now}
        favorites={[]}
        recordings={[]}
        tuningId={null}
        onTune={onTune}
        onProgram={onProgram}
        onChannelMenu={() => {}}
      />,
    );
    await userEvent.click(screen.getByText('Film'));
    expect(onProgram).toHaveBeenCalledWith(channel, expect.objectContaining({ title: 'Film' }));
    await userEvent.click(screen.getByRole('button', { name: 'Watch BBC One' }));
    expect(onTune).toHaveBeenCalledWith(channel);
  });

  it('shows an empty row without guide data', () => {
    render(
      <GuideGrid
        channels={[channel]}
        schedules={{ bbc1: [] }}
        scheduleLoading={false}
        start={start}
        now={now}
        favorites={[]}
        recordings={[]}
        tuningId={null}
        onTune={() => {}}
        onProgram={() => {}}
        onChannelMenu={() => {}}
      />,
    );
    expect(screen.getByText('No guide data')).toBeInTheDocument();
  });
});

describe('RecordingRow', () => {
  it('offers play and delete for a completed recording', async () => {
    const r = mapRecording({
      id: 'r1',
      status: 'completed',
      channelName: 'BBC One',
      title: 'Doctor Who',
      startAt: '2026-11-21T18:00:00Z',
      endAt: '2026-11-21T19:00:00Z',
      outputPath: '/x.ts',
    })!;
    const onPlay = vi.fn();
    const onDelete = vi.fn();
    render(
      <RecordingRow
        recording={r}
        now={now}
        onPlay={onPlay}
        onCancel={() => {}}
        onDelete={onDelete}
      />,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Play' }));
    expect(onPlay).toHaveBeenCalledWith(r);
    await userEvent.click(screen.getByRole('button', { name: 'Delete Doctor Who' }));
    expect(onDelete).toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Stop' })).toBeNull();
  });
});
