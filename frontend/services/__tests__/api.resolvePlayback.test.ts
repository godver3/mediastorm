import { ApiService, type NZBResult, type PlaybackResolution } from '../api';

describe('ApiService.resolvePlayback', () => {
  const baseUrl = 'http://localhost:7777/api';
  const sampleResult: NZBResult = {
    title: 'Sample Release',
    indexer: 'TestIndexer',
    guid: 'guid-123',
    link: 'https://example.com/nzb/123',
    downloadUrl: 'https://example.com/nzb/123/download',
    sizeBytes: 1024,
    publishDate: new Date().toISOString(),
  };

  const okJsonResponse = (payload: unknown) => ({
    ok: true,
    status: 200,
    statusText: 'OK',
    headers: new Map(),
    text: jest.fn().mockResolvedValue(JSON.stringify(payload)),
  });

  const errorJsonResponse = (status: number, body: string) => ({
    ok: false,
    status,
    statusText: 'Error',
    headers: new Map(),
    text: jest.fn().mockResolvedValue(body),
  });

  let delaySpy: jest.SpyInstance;

  beforeEach(() => {
    jest.spyOn(console, 'log').mockImplementation(() => {});
    jest.spyOn(console, 'error').mockImplementation(() => {});
    jest.spyOn(console, 'warn').mockImplementation(() => {});
    delaySpy = jest
      .spyOn(ApiService.prototype as unknown as { delay: () => Promise<void> }, 'delay')
      .mockResolvedValue();
  });

  afterEach(() => {
    jest.restoreAllMocks();
  });

  it('returns playback immediately when the backend responds with a ready stream', async () => {
    const playback: PlaybackResolution = {
      queueId: 42,
      webdavPath: '/webdav/streams/test-file.mkv',
      healthStatus: 'healthy',
    };

    global.fetch = jest.fn().mockResolvedValue(okJsonResponse(playback));

    const api = new ApiService(baseUrl);
    const result = await api.resolvePlayback(sampleResult);

    expect(result).toEqual(playback);
    expect(global.fetch as jest.Mock).toHaveBeenCalledTimes(1);
    expect(delaySpy).not.toHaveBeenCalled();
  });

  it('polls the queue until playback is ready', async () => {
    const queueId = 7;
    const responses = [
      { queueId, healthStatus: 'queued' },
      { queueId, healthStatus: 'processing' },
      {
        queueId,
        healthStatus: 'healthy',
        webdavPath: '/webdav/streams/test-file.mkv',
        fileSize: 2_048,
      },
    ];

    global.fetch = jest.fn().mockImplementation((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.endsWith('/playback/resolve')) {
        return Promise.resolve(okJsonResponse(responses[0]));
      }

      if (url.includes(`/playback/queue/${queueId}`)) {
        responses.shift();
        const next = responses[0];
        if (!next) {
          throw new Error('Queue responses exhausted');
        }
        return Promise.resolve(okJsonResponse(next));
      }

      throw new Error(`Unexpected fetch to ${url}`);
    });

    const api = new ApiService(baseUrl);
    const result = await api.resolvePlayback(sampleResult);

    expect(result.webdavPath).toBe('/webdav/streams/test-file.mkv');
    expect(result.healthStatus).toBe('healthy');
    expect(result.queueId).toBe(queueId);
    expect(result.fileSize).toBe(2_048);
    expect(global.fetch as jest.Mock).toHaveBeenCalledTimes(3);
    expect(delaySpy).toHaveBeenCalledTimes(2);
  });

  it('invokes onStatus callback with each queue update', async () => {
    const queueId = 21;
    const responses = [
      { queueId, healthStatus: 'queued' },
      { queueId, healthStatus: 'processing' },
      {
        queueId,
        healthStatus: 'healthy',
        webdavPath: '/webdav/streams/test-file.mkv',
        fileSize: 3_072,
      },
    ];

    global.fetch = jest.fn().mockImplementation((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.endsWith('/playback/resolve')) {
        return Promise.resolve(okJsonResponse(responses[0]));
      }

      if (url.includes(`/playback/queue/${queueId}`)) {
        responses.shift();
        const next = responses[0];
        if (!next) {
          throw new Error('Queue responses exhausted');
        }
        return Promise.resolve(okJsonResponse(next));
      }

      throw new Error(`Unexpected fetch to ${url}`);
    });

    const onStatus = jest.fn();
    const api = new ApiService(baseUrl);
    const result = await api.resolvePlayback(sampleResult, { onStatus });

    expect(onStatus).toHaveBeenCalledTimes(3);
    expect(onStatus).toHaveBeenNthCalledWith(1, expect.objectContaining({ healthStatus: 'queued', queueId }));
    expect(onStatus).toHaveBeenNthCalledWith(2, expect.objectContaining({ healthStatus: 'processing', queueId }));
    expect(onStatus).toHaveBeenNthCalledWith(
      3,
      expect.objectContaining({ healthStatus: 'healthy', webdavPath: result.webdavPath }),
    );
    expect(result.healthStatus).toBe('healthy');
    expect(result.queueId).toBe(queueId);
    expect(global.fetch as jest.Mock).toHaveBeenCalledTimes(3);
  });

  it('emits status updates when playback is ready immediately', async () => {
    const playback: PlaybackResolution = {
      queueId: 0,
      webdavPath: '/debrid/realdebrid/123',
      healthStatus: 'cached',
    };

    global.fetch = jest.fn().mockResolvedValue(okJsonResponse(playback));

    const onStatus = jest.fn();
    const api = new ApiService(baseUrl);
    const result = await api.resolvePlayback(sampleResult, { onStatus });

    expect(onStatus).toHaveBeenCalledTimes(1);
    expect(onStatus).toHaveBeenCalledWith(expect.objectContaining({ webdavPath: playback.webdavPath }));
    expect(result).toEqual(playback);
  });

  it('throws a friendly error when the backend reports a failed status', async () => {
    const failureResponse = { queueId: 9, healthStatus: 'failed' };

    global.fetch = jest.fn().mockResolvedValue(okJsonResponse(failureResponse));

    const api = new ApiService(baseUrl);
    await expect(api.resolvePlayback(sampleResult)).rejects.toMatchObject({
      code: 'NZB_HEALTH_FAILED',
    });
  });

  it('treats not_available as a health failure', async () => {
    const failureResponse = { queueId: 11, healthStatus: 'not_available' };

    global.fetch = jest.fn().mockResolvedValue(okJsonResponse(failureResponse));

    const api = new ApiService(baseUrl);
    await expect(api.resolvePlayback(sampleResult)).rejects.toMatchObject({
      code: 'NZB_HEALTH_FAILED',
    });
  });

  it('surfaces queue processing failures returned by the backend', async () => {
    const queueId = 13;

    global.fetch = jest.fn().mockImplementation((input: RequestInfo | URL) => {
      const url = typeof input === 'string' ? input : input.toString();
      if (url.endsWith('/playback/resolve')) {
        return Promise.resolve(okJsonResponse({ queueId, healthStatus: 'queued' }));
      }

      if (url.includes(`/playback/queue/${queueId}`)) {
        return Promise.resolve(errorJsonResponse(502, 'playback queue item failed: unable to extract media file'));
      }

      throw new Error(`Unexpected fetch to ${url}`);
    });

    const api = new ApiService(baseUrl);
    await expect(api.resolvePlayback(sampleResult)).rejects.toMatchObject({
      code: 'NZB_HEALTH_FAILED',
    });
    expect(global.fetch as jest.Mock).toHaveBeenCalledTimes(2);
  });
});
