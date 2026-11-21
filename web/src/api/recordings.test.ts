import { describe, expect, it } from 'vitest';
import {
  canCancel,
  canDelete,
  canPlay,
  displayTitle,
  durationText,
  mapRecording,
  mapRule,
  matchesFilter,
  normalizedTitle,
  recordingFor,
  recordingPlaybackParams,
  ruleCovers,
  seriesRuleFor,
  sizeText,
  sortRecordings,
  visibleError,
  type Recording,
} from './recordings';

const rec = (over: Partial<Recording> = {}): Recording => ({
  ...mapRecording({
    id: 'r1',
    status: 'completed',
    channelId: 'c1',
    channelName: 'BBC One',
    title: 'Doctor Who',
    startAt: '2026-11-21T18:00:00Z',
    endAt: '2026-11-21T19:30:00Z',
    outputPath: '/rec/BBC One - 2026-11-21 1800-1930 - Doctor Who.ts',
  })!,
  ...over,
});

describe('recording mappers', () => {
  it('maps unknown statuses and defaults', () => {
    const r = mapRecording({ id: 'x', status: 'weird' })!;
    expect(r.status).toBe('unknown');
    expect(r.channelName).toBe('');
    expect(mapRecording({})).toBeNull();
  });

  it('maps rules with defaults', () => {
    expect(mapRule({ id: 'r', pattern: 'News' })).toEqual(
      expect.objectContaining({
        matchType: 'title',
        enabled: true,
        newOnly: false,
        allChannels: false,
      }),
    );
  });

  it('derives the title from the file name when the title is the channel', () => {
    expect(displayTitle(rec())).toBe('Doctor Who');
    expect(displayTitle(rec({ title: 'BBC One' }))).toBe('Doctor Who');
    expect(displayTitle(rec({ title: '', outputPath: undefined }))).toBe('BBC One');
    expect(displayTitle(rec({ title: 'Match+of+the+Day' }))).toBe('Match of the Day');
  });

  it('computes capabilities', () => {
    expect(canPlay(rec())).toBe(true);
    expect(canPlay(rec({ status: 'pending' }))).toBe(false);
    expect(canCancel(rec({ status: 'running' }))).toBe(true);
    expect(canDelete(rec({ status: 'pending', ruleId: 'rule' }))).toBe(false);
    expect(canDelete(rec({ status: 'pending' }))).toBe(true);
  });

  it('formats duration, size and errors', () => {
    expect(durationText(rec())).toBe('1h 30m');
    expect(sizeText(1_500_000_000)).toBe('1.5 GB');
    expect(visibleError({ error: 'recording rule paused' })).toBeUndefined();
    expect(visibleError({ error: 'ffmpeg exited' })).toBe('ffmpeg exited');
  });

  it('filters and sorts like native', () => {
    expect(matchesFilter('upcoming', rec({ status: 'pending' }))).toBe(true);
    expect(matchesFilter('history', rec({ status: 'failed' }))).toBe(true);
    expect(matchesFilter('recording', rec({ status: 'starting' }))).toBe(true);
    const sorted = sortRecordings([
      rec({ id: 'done', status: 'completed' }),
      rec({ id: 'later', status: 'pending', startAt: '2026-11-22T10:00:00Z' }),
      rec({ id: 'soon', status: 'pending', startAt: '2026-11-21T20:00:00Z' }),
      rec({ id: 'live', status: 'running' }),
    ]);
    expect(sorted.map((r) => r.id)).toEqual(['live', 'soon', 'later', 'done']);
  });

  it('hands the recording to playback.html', () => {
    expect(recordingPlaybackParams(rec({ status: 'running' }))).toEqual({
      sourcePath: 'recording:r1/recording.ts',
      mediaType: 'movie',
      title: 'Doctor Who',
      displayName: 'Doctor Who.ts',
      growingRecording: '1',
      recordingEndsAt: '2026-11-21T19:30:00Z',
    });
  });
});

describe('rule matching', () => {
  const channel = { id: 'c1', tvgId: 'bbc1.uk' };
  it('normalizes titles like the server', () => {
    expect(normalizedTitle("  Doctor Who: The Star's End! ")).toBe('doctor who the star s end');
  });

  it('matches title rules on the channel and regex rules anywhere', () => {
    const title = mapRule({ id: 't', pattern: 'doctor who', channelId: 'C1' })!;
    const regex = mapRule({ id: 'x', matchType: 'regex', pattern: '^doctor', allChannels: true })!;
    const other = mapRule({ id: 'o', pattern: 'Doctor Who', channelId: 'c2' })!;
    expect(ruleCovers(title, 'Doctor Who', channel)).toBe(true);
    expect(ruleCovers(other, 'Doctor Who', channel)).toBe(false);
    expect(ruleCovers(regex, 'Doctor Who', channel)).toBe(true);
    expect(seriesRuleFor([regex, title], 'Doctor Who', channel)?.id).toBe('t');
  });

  it('finds the active recording overlapping a programme', () => {
    const r = rec({ status: 'pending' });
    const start = Date.parse('2026-11-21T18:30:00Z');
    expect(recordingFor([r], channel, { start, stop: start + 3600_000 })?.id).toBe('r1');
    expect(
      recordingFor([r], channel, { start: start + 7200_000, stop: start + 9000_000 }),
    ).toBeUndefined();
    expect(
      recordingFor([rec({ status: 'completed' })], channel, { start, stop: start + 1 }),
    ).toBeUndefined();
  });
});
