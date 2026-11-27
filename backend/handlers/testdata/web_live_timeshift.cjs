const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');
const html = fs.readFileSync(process.env.MEDIASTORM_PLAYER_HTML, 'utf8');
const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].at(-1)[1];

function element(id) {
  const attributes = new Map();
  const classes = new Set();
  return {
    id, hidden: false, value: '', max: '', textContent: '', title: '', disabled: false,
    classList: {
      add: (...names) => names.forEach((name) => classes.add(name)),
      remove: (...names) => names.forEach((name) => classes.delete(name)),
      toggle: (name, on) => { if (on) classes.add(name); else classes.delete(name); },
      contains: (name) => classes.has(name),
    },
    setAttribute: (name, value) => attributes.set(name, String(value)),
    removeAttribute: (name) => attributes.delete(name),
    getAttribute: (name) => attributes.get(name) ?? null,
  };
}

// A live player whose playlist lists [start, end] in 2 s segments, playing at sync (by default
// where the server starts it, 14 s behind the newest segment). Like hls.js, the video's seekable range stays [0, end]: it does not slide.
function player({ start = 0, end = 300, sync = end - 14, currentTime = sync, paused = false, timeshift = true, query = '', globals = {} } = {}) {
  const video = {
    ...element('webPlayer'), currentTime, paused, muted: false, volume: 1,
    seekable: { length: 1, start: () => 0, end: () => video.range[1] },
    range: [start, end],
    play() { this.paused = false; return Promise.resolve(); },
    pause() { this.paused = true; },
    listeners: [],
    addEventListener(name, fn) { this.listeners.push({ name, fn }); },
    fire(name) { this.listeners.filter((l) => l.name === name).forEach((l) => l.fn()); },
  };
  const ids = ['liveSeek', 'liveSpacer', 'time', 'liveLabel', 'videoHost', 'playButton'];
  const elements = Object.fromEntries(ids.map((id) => [id, element(id)]));
  elements.webPlayer = video;
  const dvrButtons = [element('skipBackButton'), element('playButton'), element('skipForwardButton')];
  dvrButtons.forEach((button) => { button.hidden = true; });
  const context = vm.createContext({
    URL, URLSearchParams, setTimeout, clearTimeout, setInterval: () => 0, clearInterval() {},
    window: { location: { search: (timeshift ? '?liveTimeshift=1' : '?') + query }, addEventListener() {}, localStorage: { getItem() { return null; }, setItem() {} } },
    document: {
      addEventListener() {}, getElementById: (id) => elements[id] || null, querySelector() { return null; },
      querySelectorAll: (selector) => (selector === '[data-live-dvr]' ? dvrButtons : []),
    },
    console: { info() {}, warn() {}, error() {} },
    ...globals,
  });
  vm.runInContext(script, context);
  context.sync = sync;
  const details = { targetduration: 2, fragments: [], edge: end };
  const list = () => {
    details.fragments = [];
    for (let t = video.range[0]; t < video.range[1]; t += 2) details.fragments.push({ start: t, duration: Math.min(2, video.range[1] - t) });
    details.edge = video.range[1];
  };
  list();
  context.details = details;
  vm.runInContext('playbackMeta = { isLive: true }; hlsInstance = { liveSyncPosition: sync, currentLevel: -1, levels: [{ details }] };', context);
  return {
    video, elements, dvrButtons,
    run: (code) => vm.runInContext(code, context),
    slide(by) { video.range = [video.range[0] + by, video.range[1] + by]; list(); vm.runInContext(`hlsInstance.liveSyncPosition += ${by}`, context); },
    shrink(start) { video.range = [start, video.range[1]]; list(); },
  };
}

test('a session without timeshift never shows rewind controls, however long it plays', () => {
  // hls.js grows the seekable range with the watch time even though the playlist slides.
  const p = player({ start: 3540, end: 3600, sync: 3594, timeshift: false });
  p.run('updateLiveDvr()');
  assert.equal(p.elements.liveSeek.hidden, true);
  assert.ok(p.dvrButtons.every((button) => button.hidden));
  assert.equal(p.elements.liveLabel.textContent, 'LIVE');
  p.run('togglePlayback(); skipWebPlayer(-30)');
  assert.equal(p.video.paused, false);
  assert.equal(p.video.currentTime, 3594);
});

test('the window is what the playlist lists, not the seekable range from 0', () => {
  const p = player({ start: 2400, end: 3600, sync: 3594 });
  p.run('skipWebPlayer(-3000)');
  assert.equal(p.video.currentTime, 2402, 'not into time the playlist no longer lists');
  assert.equal(p.elements.liveSeek.max, '1184', 'up to the live edge, 14 s behind the newest segment');
});

test('an ordinary live window keeps the plain LIVE badge and no rewind controls', () => {
  const p = player({ start: 0, end: 40, sync: 34 });
  p.run('updateLiveDvr()');
  assert.equal(p.elements.liveSeek.hidden, true);
  assert.equal(p.elements.liveSpacer.hidden, false);
  assert.ok(p.dvrButtons.every((button) => button.hidden));
  assert.equal(p.elements.liveLabel.textContent, 'LIVE');
  assert.equal(p.elements.time.classList.contains('is-dvr'), false);
  // Without a window, pausing and skipping do nothing.
  p.run('togglePlayback(); skipWebPlayer(-30)');
  assert.equal(p.video.paused, false);
  assert.equal(p.video.currentTime, 34);
});

test('a timeshift window shows the seek bar at the live edge', () => {
  const p = player({ start: 1000, end: 4600 });
  p.run('updateLiveDvr()');
  assert.equal(p.elements.liveSeek.hidden, false);
  assert.equal(p.elements.liveSpacer.hidden, true);
  assert.ok(p.dvrButtons.every((button) => !button.hidden));
  assert.equal(p.elements.liveSeek.max, '3584');
  assert.equal(p.elements.liveSeek.value, '3584');
  assert.equal(p.elements.liveLabel.textContent, 'LIVE');
  assert.equal(p.elements.time.classList.contains('is-behind'), false);
});

test('skipping back, seeking and returning to live stay inside the window', () => {
  const p = player({ start: 1000, end: 4600 });
  p.run('skipWebPlayer(-30)');
  assert.equal(p.video.currentTime, 4556);
  assert.equal(p.elements.liveLabel.textContent, '−0:30');
  assert.equal(p.elements.time.classList.contains('is-behind'), true);
  p.run('skipWebPlayer(-7200)');
  assert.equal(p.video.currentTime, 1002, 'clamped inside the oldest listed segment');
  assert.equal(p.elements.liveLabel.textContent, '−59:44');
  p.run('skipWebPlayer(30); skipWebPlayer(9999)');
  assert.equal(p.video.currentTime, 4586, 'clamped to the live edge, not past it');
  p.run('commitLiveSeek(600)');
  assert.equal(p.video.currentTime, 1602);
  p.run('goLiveEdge()');
  assert.equal(p.video.currentTime, 4586);
  assert.equal(p.elements.liveLabel.textContent, 'LIVE');
});

test('pausing live holds the frame while the window moves on, and the badge returns to live', async () => {
  const p = player({ start: 0, end: 600 });
  p.run('togglePlayback()');
  assert.equal(p.video.paused, true);
  p.slide(45);
  p.run('updateLiveDvr()');
  assert.equal(p.video.currentTime, 586);
  assert.equal(p.elements.liveLabel.textContent, '−0:45');
  assert.equal(p.elements.liveSeek.value, '539');
  p.run('goLiveEdge()');
  assert.equal(p.video.paused, false);
  assert.equal(p.video.currentTime, 631);
});

test('dragging the seek bar previews the delay without moving playback', () => {
  const p = player({ start: 0, end: 600 });
  p.run('beginLiveSeekInput(); previewLiveSeek(284)');
  assert.equal(p.video.currentTime, 586);
  assert.equal(p.elements.liveLabel.textContent, '−5:00');
  p.elements.liveSeek.value = 'untouched';
  p.run('updateLiveDvr()');
  assert.equal(p.elements.liveSeek.value, 'untouched', 'the bar is not reset under the pointer');
  p.run('cancelLiveSeekInput()');
  assert.equal(p.elements.liveLabel.textContent, 'LIVE');
});

test('a browser that cannot decode the copy has the session encode, then rejoins once only encoded segments are listed', async () => {
  const requests = [];
  const attached = [];
  const timers = [];
  const playlist = (sequence, count, duration) => ['#EXTM3U', `#EXT-X-MEDIA-SEQUENCE:${sequence}`,
    ...Array.from({ length: count }, (_, i) => [`#EXTINF:${duration},`, `segment${sequence + i}.ts`]).flat()].join('\n') + '\n';
  // Still the copies, then too few encoded segments, then enough.
  const playlists = [playlist(20, 3, 6), playlist(40, 1, 2), playlist(40, 3, 2)];
  let stopped = false;
  const statuses = [];
  const p = player({
    start: 0, end: 600, sync: 594,
    globals: {
      fetch: async (url, options) => {
        requests.push({ url, method: options?.method });
        if (options?.method === 'POST') return { ok: true, json: async () => ({ switched: true, encodedFrom: 40 }) };
        return { ok: true, text: async () => playlists.shift() };
      },
      setTimeout: (fn) => { timers.push(fn); return timers.length; },
      recordAttach: (url, options) => attached.push({ url, options }),
      noteStop: () => { stopped = true; },
      noteStatus: (message) => statuses.push(message),
    },
  });
  p.run('hlsSessionId = "s1"; hlsInstance.url = "/api/video/hls/s1/stream.m3u8"; hlsInstance.stopLoad = () => noteStop(); setStatus = (message) => noteStatus(message); attachWebPlayer = (video, url, options) => recordAttach(url, options);');
  await p.run('requestLiveVideoEncode(document.getElementById("webPlayer"))');
  assert.ok(requests[0].url.endsWith('/video/hls/s1/live-encode'), requests[0].url);
  assert.equal(requests[0].method, 'POST');
  assert.ok(stopped, 'stops loading the undecodable copies');
  // Still the copies, then too few encoded segments, then enough.
  for (let i = 0; i < 3; i++) {
    assert.equal(attached.length, 0, 'not before the playlist lists only encoded segments');
    await timers.shift()();
  }
  assert.equal(attached.length, 1);
  assert.equal(attached[0].url, '/api/video/hls/s1/stream.m3u8');
  assert.equal(attached[0].options.liveEdge, true);
  assert.equal(requests.filter((r) => r.url === '/api/video/hls/s1/stream.m3u8').length, 3);
  // The switching notice goes once the encoded stream plays.
  p.video.fire('playing');
  assert.equal(statuses.at(-1), '');
});

test('a session that does not switch (it already encodes) ends in the error, without rejoining', async () => {
  const attached = [];
  const statuses = [];
  const p = player({
    start: 0, end: 600, sync: 594,
    globals: {
      fetch: async () => ({ ok: true, json: async () => ({ switched: false, encodedFrom: 0 }) }),
      setTimeout: () => { throw new Error('no polling without a switch'); },
      recordAttach: (url, options) => attached.push({ url, options }),
      noteStatus: (message) => statuses.push(message),
    },
  });
  p.run('hlsSessionId = "s1"; hlsInstance.url = "/api/video/hls/s1/stream.m3u8"; hlsInstance.stopLoad = () => { throw new Error("keeps loading"); }; setStatus = (message) => noteStatus(message); attachWebPlayer = (video, url, options) => recordAttach(url, options);');
  await p.run('requestLiveVideoEncode(document.getElementById("webPlayer"))');
  assert.equal(attached.length, 0);
  assert.match(statuses.at(-1), /Playback error/);
});

test('playing ahead of the live edge, skipping forward or going live never jumps back', () => {
  const p = player({ start: 0, end: 600, currentTime: 590 });
  p.run('updateLiveDvr()');
  assert.equal(p.elements.liveLabel.textContent, 'LIVE');
  p.run('skipWebPlayer(30)');
  assert.equal(p.video.currentTime, 590);
  p.run('goLiveEdge()');
  assert.equal(p.video.currentTime, 590);
  p.run('skipWebPlayer(-30)');
  assert.equal(p.video.currentTime, 560);
  p.run('skipWebPlayer(30)');
  assert.equal(p.video.currentTime, 586, 'forward again up to the live edge');
});

test('a paused player can resume after its window shrank below the controls\' minimum', () => {
  const p = player({ start: 0, end: 600, sync: 594 });
  p.run('togglePlayback()');
  assert.equal(p.video.paused, true);
  p.shrink(570); // the backlog withdrawn: only the live part is left
  p.run('updateLiveDvr()');
  const [back, play, forward] = p.dvrButtons;
  assert.equal(play.hidden, false, 'the play button stays while paused');
  assert.ok(back.hidden && forward.hidden);
  assert.equal(p.elements.liveSeek.hidden, true);
  p.run('togglePlayback()');
  assert.equal(p.video.paused, false);
  p.run('updateLiveDvr()');
  assert.equal(play.hidden, true);
  // The badge resumes a paused player too.
  p.video.paused = true;
  p.run('goLiveEdge()');
  assert.equal(p.video.paused, false);
});

test('only codec and decode failures count as a browser that cannot take the copied video', () => {
  const p = player();
  for (const details of ['bufferAddCodecError', 'bufferIncompatibleCodecsError', 'fragParsingError']) {
    assert.equal(p.run(`isLiveCodecFailure({ details: '${details}' }, null)`), true, details);
  }
  assert.equal(p.run('isLiveCodecFailure({ details: "bufferStalledError" }, { error: null })'), false);
  assert.equal(p.run('isLiveCodecFailure({ details: "bufferNudgeOnStall" }, { error: { code: 2 } })'), false);
  assert.equal(p.run('isLiveCodecFailure({ details: "bufferStalledError" }, { error: { code: 3 } })'), true, 'MEDIA_ERR_DECODE');
  assert.equal(p.run('isLiveCodecFailure({}, { error: { code: 4 } })'), true, 'MEDIA_ERR_SRC_NOT_SUPPORTED');
});

test('resuming after a pause longer than the window carries on from its oldest point, not live', () => {
  const p = player({ start: 0, end: 600, sync: 594 });
  p.run('togglePlayback()');
  assert.equal(p.video.paused, true);
  p.slide(700); // the window moved past the paused frame
  p.run('togglePlayback()');
  assert.equal(p.video.paused, false);
  assert.equal(p.video.currentTime, 702, 'the oldest listed point, a target duration inside');
});

test('a watch-party guest cannot pause, resume or jump to live', () => {
  const statuses = [];
  const p = player({ start: 0, end: 600, sync: 594, paused: true, query: '&watchPartyRoomId=r1', globals: { noteStatus: (m) => statuses.push(m) } });
  p.run('setTransientStatus = (message) => noteStatus(message)');
  p.run('togglePlayback(); goLiveEdge()');
  assert.equal(p.video.paused, true);
  assert.equal(p.video.currentTime, 594);
  assert.equal(statuses.length, 2);
});

test('a backlog of long source segments does not move the live edge back', () => {
  // The playlist's TARGETDURATION covers 10 s backlog segments, the live part has 2 s ones:
  // live is 14 s behind the newest segment, not three backlog segments (30 s).
  const p = player({ start: 0, end: 600 });
  p.run('details.targetduration = 10; details.fragments[0].duration = 10; hlsInstance.liveSyncPosition = 570');
  p.run('skipWebPlayer(-30)');
  assert.equal(p.video.currentTime, 556);
  p.run('updateLiveDvr()');
  assert.equal(p.elements.liveLabel.textContent, '−0:30', 'not "LIVE" while 30 s behind');
  p.run('goLiveEdge()');
  assert.equal(p.video.currentTime, 586);
  assert.equal(p.elements.liveLabel.textContent, 'LIVE');
});
