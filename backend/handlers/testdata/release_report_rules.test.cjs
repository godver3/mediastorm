const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const fs = require('node:fs');
const path = require('node:path');
const source = fs.readFileSync(path.join(__dirname, '../static/release-reports-v2.js'), 'utf8');
const flush = () => new Promise(resolve => setImmediate(resolve));
function deferred() { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; }

function fixture(t, options = {}) {
  const elements = new Map();
  const monitors = new Map();
  const entries = new Map();
  const calls = [], deleted = [], starts = [], stops = [];
  let position = 42, next = 0, monitorId = 0;
  const element = id => {
    if (!elements.has(id)) elements.set(id, { hidden: true, disabled: false, value: '', textContent: '', open: false,
      showModal() { this.open = true; }, close() { this.open = false; }, focus() {}, addEventListener() {},
      replaceChildren() {}, add(option) { if (!this.value) this.value = option.value; } });
    return elements.get(id);
  };
  function reply(id = 'alternative-' + (++next), report = false) {
    entries.set(id, { prequeueId: id, status: 'ready', streamPath: '/fixture/' + id + '.mkv', displayName: 'Synthetic alternative', duration: 600,
      selectedAudioTrack: 2, selectedSubtitleTrack: -1, audioTracks: [{ index: 2, language: options.audioLanguage || 'fra', codec: 'aac' }] });
    return { replacement: { prequeueId: id }, ...(report ? { report: { id: 'report-' + id } } : {}) };
  }
  async function gateway(url, init = {}) {
    calls.push({ url, ...init });
    if (options.intercept) {
      const intercepted = options.intercept(url, init, reply);
      if (intercepted !== undefined) return intercepted;
    }
    if (init.method === 'POST') {
      if (url.endsWith('/report') && !JSON.parse(init.body).retry) return { report: { id: 'report-only-' + (++next) } };
      return reply(undefined, url.endsWith('/report'));
    }
    if (init.method === 'DELETE') {
      deleted.push(url);
      if (url.endsWith('/retry')) entries.delete(url.split('/').at(-2));
      return { deleted: true };
    }
    if (url.endsWith('/report')) return { sourceVersion: 'verified-fixture', releaseName: 'Synthetic.Movie.1080p', audioLanguage: options.audioLanguage || 'fra',
      audioTracks: [{ index: 2, language: options.audioLanguage || 'fra', codec: 'aac' }], selectedAudioTrack: 2, hasEpisode: true, canShare: false };
    return JSON.parse(JSON.stringify(entries.get(url.split('/').at(-1)) || { status: 'expired' }));
  }
  const context = vm.createContext({
    document: { documentElement: { lang: 'en' }, getElementById: element, querySelector: element, addEventListener() {} },
    window: { addEventListener() {} }, navigator: { language: 'en' }, Intl, Option: class { constructor(text, value) { this.text = text; this.value = value; } },
    setTimeout: (fn, delay) => setTimeout(fn, options.fastTimeout && delay >= 15000 ? 5 : delay), clearTimeout,
    setInterval: fn => { monitors.set(++monitorId, fn); return monitorId; }, clearInterval: id => monitors.delete(id),
    Date, SHARE_MODE: false, WATCH_PARTY_MODE: false, CLIENT_ID: 'fixture-device', activeProfileId: 'fixture-profile',
    playbackMeta: { isLive: false }, webPlayerActivePrequeueId: 'playing', sourcePath: '/fixture/playing.mkv',
    webPlayerAudioPreferenceGeneration: 0, webPlayerAudioPreferenceSaveFailed: false, webPlayerAudioPreferenceSave: Promise.resolve(),
    webPlayerMigrationInProgress: false, webPlayerMigrationProbation: null, apiCall: gateway,
    currentAbsoluteTime: () => position, setStatus() {}, stopHlsSession: value => stops.push(value), syncPlaybackUrlToHistory() {}, renderHeader() {},
    initializeWebMigrationState: async () => {}, startWebPlayerFromSource: async value => { starts.push({ path: context.sourcePath, ...value }); if (options.failStart) throw new Error('synthetic transport failure'); },
  });
  vm.runInContext(source, context);
  t.after(() => context.closeReleaseReport());
  return { context, element, calls, deleted, entries, starts, stops, reply, monitors: () => [...monitors.values()].forEach(fn => fn()),
    setPosition: value => { position = value; }, get: name => vm.runInContext(name, context),
    async open(reason = '') { await context.openReleaseReport(); element('releaseReportReason').value = reason; context.updateReleaseReportScope(); },
    async prepare(report = false, reason = '') { await this.open(reason); await context.performReleaseAction(report, true); },
  };
}

test('preparation leaves the source open until explicit adoption and samples current position then', async t => {
  const f = fixture(t); await f.prepare();
  assert.equal(f.context.sourcePath, '/fixture/playing.mkv'); assert.equal(f.starts.length, 0); assert.equal(f.stops.length, 0);
  assert.equal(f.element('releaseReplacementReady').hidden, false);
  assert.match(f.element('releaseReportScope').textContent, /creates no permanent report/);
  assert.equal(f.element('releaseReplacementResume').value, 'rewind');
  f.setPosition(95); await f.context.startPreparedRelease();
  assert.equal(f.starts[0].startOffset, 85); assert.equal(f.context.sourcePath, '/fixture/alternative-1.mkv');
  assert.equal(f.element('releaseReportDialog').open, false);
  assert.equal(f.deleted.length, 0);
});

test('resume modes handle beginning, current time, rewind, negatives and shorter alternatives', t => {
  const f = fixture(t);
  for (const [mode, current, wrong, duration, expected] of [
    ['beginning', 70, false, 600, 0], ['current', 70, false, 600, 70], ['rewind', 70, false, 600, 60],
    ['rewind', 5, false, 600, 0], ['current', -4, false, 600, 0], ['current', 700, false, 600, 0], ['current', 70, true, 600, 0],
  ]) assert.equal(f.context.preparedReleaseOffset(mode, current, wrong, duration), expected);
});

test('wrong movie and wrong episode force the beginning even without a permanent report', async t => {
  for (const reason of ['wrong_title', 'wrong_episode']) {
    const f = fixture(t); await f.prepare(false, reason);
    assert.equal(f.element('releaseReplacementResume').disabled, true);
    f.element('releaseReplacementResume').value = 'current'; f.setPosition(250);
    await f.context.startPreparedRelease();
    assert.equal(f.starts[0].startOffset, 0); assert.equal(f.stops[0].skipProgress, true);
    assert.equal(f.calls.filter(c => c.method === 'POST')[0].url.endsWith('/retry'), true);
  }
});

test('manual preparation has no report reason and duplicate submissions and starts are ignored', async t => {
  const f = fixture(t); await f.prepare();
  await f.context.retryReleaseWithoutReport();
  const posts = f.calls.filter(c => c.method === 'POST'); assert.equal(posts.length, 1);
  const body = JSON.parse(posts[0].body); assert.equal(body.startOffset, 0); assert.equal(body.skipHLS, true); assert.equal(body.reason, undefined);
  await Promise.all([f.context.startPreparedRelease(), f.context.startPreparedRelease()]);
  assert.equal(f.starts.length, 1);
});

test('report-only preserves playback and forwards five effective languages', async t => {
  for (const language of ['fra', 'spa', 'eng', 'jpn', 'deu']) {
    const f = fixture(t, { audioLanguage: language }); await f.open('missing_language');
    await f.context.submitReleaseReport(false);
    const body = JSON.parse(f.calls.find(c => c.method === 'POST').body);
    assert.equal(body.audioLanguage, language); assert.equal(body.retry, false);
    assert.equal(f.starts.length, 0); assert.equal(f.stops.length, 0);
    assert.equal(f.element('releaseReportUndo').hidden, false);
    await f.context.undoReleaseReport();
    assert.equal(f.element('releaseReportUndo').hidden, true);
  }
});

test('canceling a prepared alternative keeps the source and deletes only its own preparation', async t => {
  const f = fixture(t); await f.prepare(); f.context.closeReleaseReport(); await flush();
  assert.equal(f.context.sourcePath, '/fixture/playing.mkv'); assert.equal(f.starts.length, 0);
  assert.deepEqual(f.deleted, ['/playback/prequeue/alternative-1/retry']);
});

test('a late POST after close cannot change playback or delete a newer preparation', async t => {
  const gate = deferred(); let posts = 0;
  const f = fixture(t, { intercept: (url, init) => init.method === 'POST' && ++posts === 1 ? gate.promise : undefined });
  await f.open(); const old = f.context.retryReleaseWithoutReport(); await flush(); f.context.closeReleaseReport();
  await f.prepare(); assert.equal(f.get('releaseReportAction').replacementId, 'alternative-1');
  gate.resolve(f.reply('late-old')); await old; await flush();
  assert.equal(f.get('releaseReportAction').replacementId, 'alternative-1'); assert.equal(f.starts.length, 0);
  assert.deepEqual(f.deleted, ['/playback/prequeue/late-old/retry']);
});

test('a late failed poll cannot clear busy state or delete a newer pending preparation', async t => {
  const oldPoll = deferred(), newPost = deferred(); let posts = 0;
  const f = fixture(t, { intercept: (url, init) => {
    if (init.method === 'POST' && ++posts === 2) return newPost.promise;
    if (!init.method && url.endsWith('/alternative-1')) return oldPoll.promise;
  } });
  await f.open(); const old = f.context.retryReleaseWithoutReport(); await flush(); f.context.closeReleaseReport();
  await f.open(); const next = f.context.retryReleaseWithoutReport(); await flush();
  oldPoll.reject(new Error('synthetic old request failed')); await old;
  assert.equal(f.get('releaseReportBusy'), true); assert.equal(f.get('releaseReportAction').canceled, false);
  newPost.resolve(f.reply('next')); await next;
  assert.equal(f.get('releaseReportAction').replacementId, 'next');
  assert.equal(f.deleted.some(url => url.includes('/next/')), false);
});

test('profile, source and explicit audio changes invalidate a prepared handoff', async t => {
  for (const change of ['profile', 'source', 'audio']) {
    const f = fixture(t); await f.prepare();
    if (change === 'profile') f.context.activeProfileId = 'another-profile';
    if (change === 'source') f.context.sourcePath = '/fixture/different.mkv';
    if (change === 'audio') f.context.webPlayerAudioPreferenceGeneration++;
    f.monitors(); await f.context.startPreparedRelease(); await flush();
    assert.equal(f.starts.length, 0); assert.equal(f.get('releaseReportAction'), null);
    assert.equal(f.element('releaseReplacementReady').hidden, true);
    assert.equal(f.deleted.length, change === 'profile' ? 0 : 1);
  }
});

test('closing during final validation prevents handoff and cannot unlock a newer dialog load', async t => {
  const check = deferred(), options = deferred(); let statusCalls = 0, loads = 0;
  const f = fixture(t, { intercept: (url, init) => {
    if (!init.method && url.endsWith('/alternative-1') && ++statusCalls === 2) return check.promise;
    if (!init.method && url.endsWith('/report') && ++loads === 2) return options.promise;
  } });
  await f.prepare(); const taking = f.context.startPreparedRelease(); await flush(); f.context.closeReleaseReport();
  const opening = f.context.openReleaseReport(); await flush();
  check.resolve(f.entries.get('alternative-1') || { status: 'expired' }); await taking;
  assert.equal(f.get('releaseReportBusy'), true); assert.equal(f.starts.length, 0);
  options.resolve({ sourceVersion: 'new', audioLanguage: 'spa', audioTracks: [] }); await opening;
});

test('an expired preparation is checked again before stopping the old source', async t => {
  const f = fixture(t); await f.prepare(); f.entries.delete('alternative-1');
  await f.context.startPreparedRelease();
  assert.equal(f.stops.length, 0); assert.equal(f.starts.length, 0);
  assert.equal(f.element('releaseReplacementReady').hidden, true);
  assert.match(f.element('releaseReportError').textContent, /could not be verified/);
});

test('a changed prepared source requires a second review', async t => {
  const f = fixture(t); await f.prepare(); f.entries.get('alternative-1').streamPath = '/fixture/updated.mkv';
  await f.context.startPreparedRelease(); assert.equal(f.starts.length, 0);
  assert.match(f.element('releaseReportError').textContent, /source changed/);
  await f.context.startPreparedRelease(); assert.equal(f.starts[0].path, '/fixture/updated.mkv');
});

test('media startup failure remains visible and keeps undo available', async t => {
  const f = fixture(t, { failStart: true }); await f.prepare(true, 'missing_language');
  await f.context.startPreparedRelease();
  assert.equal(f.element('releaseReportDialog').open, true);
  assert.match(f.element('releaseReportError').textContent, /could not start/);
  assert.equal(f.element('releaseReportUndo').hidden, false);
  assert.equal(f.element('releaseManualRetry').disabled, true);
  assert.equal(f.deleted.length, 0);
});

test('failed or stalled language persistence prevents reports and preparation', async t => {
  const f = fixture(t); f.context.webPlayerAudioPreferenceSaveFailed = true;
  await f.open('missing_language'); await f.context.submitReleaseReport(true);
  assert.equal(f.calls.length, 0); assert.match(f.element('releaseReportError').textContent, /language could not be saved/);
  const stalled = fixture(t, { fastTimeout: true });
  const save = deferred(); stalled.context.webPlayerAudioPreferenceSave = save.promise;
  await stalled.open('missing_language'); await stalled.context.submitReleaseReport(true);
  assert.equal(stalled.calls.length, 0); assert.equal(stalled.get('releaseReportBusy'), false);
  save.resolve(); await flush(); assert.equal(stalled.calls.length, 0);
});

test('watch parties allow reporting only and share sessions expose no action', async t => {
  const party = fixture(t); party.context.WATCH_PARTY_MODE = true; await party.open('missing_language');
  await party.context.submitReleaseReport(true); await party.context.retryReleaseWithoutReport(); assert.equal(party.calls.filter(c => c.method === 'POST').length, 0);
  await party.context.submitReleaseReport(false); assert.equal(party.calls.filter(c => c.method === 'POST').length, 1);
  const share = fixture(t); share.context.SHARE_MODE = true; await share.open(); assert.equal(share.calls.length, 0);
});

test('timeouts release the UI and a later response still cancels its preparation', async t => {
  const gate = deferred();
  const f = fixture(t, { fastTimeout: true, intercept: (url, init) => init.method === 'POST' ? gate.promise : undefined });
  await f.prepare(); assert.equal(f.get('releaseReportBusy'), false); assert.equal(f.get('releaseReportAction'), null);
  gate.resolve(f.reply('late-timeout')); await flush();
  assert.deepEqual(f.deleted, ['/playback/prequeue/late-timeout/retry']); assert.equal(f.starts.length, 0);
});

test('automatic migration and the report dialog cannot start competing handoffs', async t => {
  const f = fixture(t); f.context.webPlayerMigrationInProgress = true; await f.open();
  assert.equal(f.calls.length, 0); assert.equal(f.element('releaseReportDialog').open, false);
  f.context.webPlayerMigrationInProgress = false;
  await f.open();
  // Exercise the actual migration entry point as well: a timer armed before
  // opening the dialog must not bypass the preparation ownership guard.
  const template = fs.readFileSync(path.join(__dirname, '../web_templates/playback.html'), 'utf8');
  const start = template.indexOf('    async function migrateWebStream(reason) {');
  const end = template.indexOf('\n    function ', start + 1);
  assert.ok(start >= 0 && end > start);
  vm.runInContext(template.slice(start, end), f.context);
  assert.equal(await f.context.migrateWebStream('already armed timer'), false);
  assert.equal(f.context.webPlayerMigrationInProgress, false);
});


test('an old undo response cannot unlock or overwrite a newer submission', async t => {
  const undo = deferred(), post = deferred(); let posts = 0;
  const f = fixture(t, { intercept: (url, init) => {
    if (init.method === 'DELETE' && url.includes('/bad-streams/')) return undo.promise;
    if (init.method === 'POST' && ++posts === 2) return post.promise;
  } });
  await f.prepare(true, 'missing_language');
  const removing = f.context.undoReleaseReport(); await flush(); f.context.closeReleaseReport();
  await f.open('quality_problem'); const submitting = f.context.submitReleaseReport(false); await flush();
  undo.resolve({ deleted: true }); await removing;
  assert.equal(f.get('releaseReportBusy'), true);
  assert.equal(f.element('releaseReportError').textContent, '');
  post.resolve({ report: { id: 'new-report' } }); await submitting;
  assert.equal(f.get('lastReleaseReportId'), 'new-report');
  assert.equal(f.element('releaseReportUndo').hidden, false);
});


test('a late response cannot delete a deduplicated replacement owned by the newer action', async t => {
  const gate = deferred(); let posts = 0;
  const f = fixture(t, { intercept: (url, init) => init.method === 'POST' && ++posts === 1 ? gate.promise : undefined });
  await f.open(); const old = f.context.retryReleaseWithoutReport(); await flush(); f.context.closeReleaseReport();
  await f.prepare();
  gate.resolve({ replacement: { prequeueId: 'alternative-1' } }); await old; await flush();
  assert.equal(f.deleted.length, 0); assert.equal(f.get('releaseReportAction').replacementId, 'alternative-1');
  await f.context.startPreparedRelease(); assert.equal(f.starts.length, 1);
});

test('cleanup waits until a newer in-flight request reveals whether it shares the old preparation', async t => {
  const first = deferred(), second = deferred(); let posts = 0;
  const f = fixture(t, { intercept: (url, init) => init.method === 'POST' ? (++posts === 1 ? first.promise : second.promise) : undefined });
  await f.open(); const old = f.context.retryReleaseWithoutReport(); await flush(); f.context.closeReleaseReport();
  await f.open(); const next = f.context.retryReleaseWithoutReport(); await flush();
  first.resolve(f.reply('shared')); await old;
  assert.equal(f.deleted.length, 0); assert.equal(f.get('releaseReportBusy'), true);
  second.resolve({ replacement: { prequeueId: 'shared' } }); await next;
  assert.equal(f.deleted.length, 0); assert.equal(f.get('releaseReportAction').replacementId, 'shared');
  f.context.closeReleaseReport(); await flush();
  assert.deepEqual(f.deleted, ['/playback/prequeue/shared/retry']);
});

test('a delayed response cannot delete the replacement after the newer action has started it', async t => {
  const gate = deferred(); let posts = 0;
  const f = fixture(t, { intercept: (url, init) => init.method === 'POST' && ++posts === 1 ? gate.promise : undefined });
  await f.open(); const old = f.context.retryReleaseWithoutReport(); await flush(); f.context.closeReleaseReport();
  await f.prepare(); await f.context.startPreparedRelease();
  gate.resolve({ replacement: { prequeueId: 'alternative-1' } }); await old; await flush();
  assert.equal(f.context.webPlayerActivePrequeueId, 'alternative-1'); assert.equal(f.deleted.length, 0);
});
