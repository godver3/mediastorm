import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import type { ReactNode } from 'react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { EPGProgram, LiveChannel } from '@/api/live';
import { HOUR } from '@/lib/guide';
import { GuideGrid } from './GuideGrid';
import { LiveActionDialog } from './LiveActionDialog';
import { StremioSourcePicker } from './StremioSourcePicker';
import type { useRecordActions } from './useLiveActions';

const now = Date.parse('2026-11-21T18:10:00Z');
const catchupChannel: LiveChannel = {
  id: 'cu:1',
  name: 'Catch-up Test',
  url: 'http://provider/live/1.ts',
  tvgId: 'cu1',
  catchup: { days: 2 },
};
const stremio: LiveChannel = {
  id: 'st:1',
  name: 'Sports 1',
  url: 'https://addon.example/stream/tv/sports1.json',
};
const past: EPGProgram = {
  title: 'Morning News',
  start: now - 3 * HOUR,
  stop: now - 2 * HOUR,
  isNew: false,
  isRerun: false,
};
const actions = { isPending: false, mutate: vi.fn() } as unknown as ReturnType<
  typeof useRecordActions
>;

const wrap = (ui: ReactNode) =>
  render(
    <QueryClientProvider
      client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}
    >
      {ui}
    </QueryClientProvider>,
  );

const dialogProps = {
  onClose: () => {},
  now,
  recordings: [],
  rules: [],
  favorite: false,
  hidden: false,
  epgEnabled: true,
  onTune: () => {},
  onToggleFavorite: () => {},
  onToggleHidden: () => {},
  actions,
};

afterEach(() => vi.restoreAllMocks());

describe('LiveActionDialog parity', () => {
  it('offers catch-up for a past programme in the archive', async () => {
    const onReplay = vi.fn();
    render(
      <LiveActionDialog
        {...dialogProps}
        target={{ channel: catchupChannel, program: past }}
        onReplay={onReplay}
      />,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Watch from catch-up' }));
    expect(onReplay).toHaveBeenCalledWith(catchupChannel, past);
  });

  it('explains when a past programme cannot be replayed', () => {
    render(
      <LiveActionDialog
        {...dialogProps}
        target={{ channel: { ...catchupChannel, catchup: undefined }, program: past }}
        onReplay={() => {}}
      />,
    );
    expect(screen.queryByRole('button', { name: /catch-up/ })).toBeNull();
    expect(screen.getByText(/no catch-up/)).toBeTruthy();
  });

  it('offers Choose source on a Stremio channel menu only', async () => {
    const onChooseSource = vi.fn();
    const { unmount } = render(
      <LiveActionDialog
        {...dialogProps}
        target={{ channel: stremio }}
        onChooseSource={onChooseSource}
      />,
    );
    await userEvent.click(screen.getByRole('button', { name: 'Choose source' }));
    expect(onChooseSource).toHaveBeenCalledWith(stremio);
    unmount();
    render(
      <LiveActionDialog
        {...dialogProps}
        target={{ channel: catchupChannel }}
        onChooseSource={onChooseSource}
      />,
    );
    expect(screen.queryByRole('button', { name: 'Choose source' })).toBeNull();
  });
});

describe('StremioSourcePicker', () => {
  it('lists the addon streams and picks one', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          streams: [
            { index: 0, label: 'Feed A 1080p' },
            { index: 3, label: 'Feed B 720p' },
          ],
        }),
        { status: 200, headers: { 'content-type': 'application/json' } },
      ),
    );
    const onPick = vi.fn();
    wrap(
      <StremioSourcePicker profileId="p1" channel={stremio} onClose={() => {}} onPick={onPick} />,
    );
    await waitFor(() => expect(screen.getByText('Feed B 720p')).toBeTruthy());
    await userEvent.click(screen.getByText('Feed B 720p'));
    expect(onPick).toHaveBeenCalledWith(stremio, expect.objectContaining({ index: 3 }));
  });
});

describe('GuideGrid catch-up paging', () => {
  it('moves the window back at the left edge and marks replayable programmes', async () => {
    const onShift = vi.fn();
    const start = now - 6 * HOUR;
    render(
      <GuideGrid
        channels={[catchupChannel]}
        schedules={{ cu1: [past] }}
        scheduleLoading={false}
        start={start}
        now={now}
        favorites={[]}
        recordings={[]}
        tuningId={null}
        onTune={() => {}}
        onProgram={() => {}}
        onChannelMenu={() => {}}
        canEarlier
        onShift={onShift}
      />,
    );
    expect(screen.getByLabelText('Catch-up available')).toBeTruthy();
    await userEvent.click(screen.getByRole('button', { name: 'Earlier' }));
    expect(onShift).toHaveBeenCalledWith(-1);
  });
});
