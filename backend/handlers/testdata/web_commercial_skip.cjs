const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');
const html = fs.readFileSync(process.env.MEDIASTORM_PLAYER_HTML, 'utf8');
const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].at(-1)[1];

// A player with a fake page: a host whose classes are tracked, the skip button, and a seek that
// moves the playhead. api answers /video/segments and the settings endpoints.
function player({ source = 'recording:rec-1/recording.ts', segments = [], settings = {} } = {}) {
  const calls = [];
  const classes = new Set();
  const host = { classList: { toggle(name, on) { if (on) classes.add(name); else classes.delete(name); }, remove(name) { classes.delete(name); } } };
  const button = { textContent: '', disabled: false };
  const toggle = { dataset: {}, checked: false };
  const context = vm.createContext({
    URL, URLSearchParams, setTimeout, clearTimeout, setInterval: () => 0,
    window: { location: { search: '' }, addEventListener() {}, localStorage: { getItem() { return null; }, setItem() {} }, setTimeout: () => 0 },
    document: {
      addEventListener() {},
      getElementById(id) { return id === 'videoHost' ? host : id === 'skipSegmentButton' ? button : null; },
      querySelector(selector) { return selector.startsWith('#skipSegmentPrompt') ? toggle : null; },
    },
    console: { info() {}, warn() {}, error() {} },
  });
  vm.runInContext(script, context);
  context.api = async (path, options = {}) => {
    calls.push({ path, ...options });
    if (path === '/video/segments') return { segments };
    if (path.endsWith('/settings')) return { playback: settings };
    return {};
  };
  vm.runInContext(`activeProfileId='adult';apiCall=api;sourcePath=${JSON.stringify(source)};hlsDuration=3600;
    var playhead=0;currentAbsoluteTime=()=>playhead;seekWebPlayer=async(t)=>{playhead=t;return true;};
    updateUpNextVisibility=()=>{};setTransientStatus=()=>{};`, context);
  return {
    calls, classes, button, toggle,
    run: (code) => vm.runInContext(code, context),
    at: (t) => vm.runInContext(`playhead=${t};updateWebSegmentPrompts()`, context),
  };
}

const breaks = [
  { type: 'commercial', start: 600, end: 780, source: 'comskip' },
  { type: 'intro', start: 0, end: 30, source: 'chapter' },
  { type: 'commercial', start: 1500, end: 1680, source: 'comskip' },
  { type: 'commercial', start: 'x', end: 10 },
  { type: 'commercial', start: 3700, end: 3800 },
];

test('a recording asks for its commercial breaks and lists only valid ones in order', async () => {
  const p = player({ segments: breaks });
  await p.run('loadWebPlayerSegments({durationSeconds:3600})');
  const call = p.calls.find((c) => c.path === '/video/segments');
  assert.equal(call.method, 'POST');
  assert.deepEqual(JSON.parse(call.body), { recordingId: 'rec-1', duration: 3600 });
  assert.deepEqual(JSON.parse(p.run('JSON.stringify(webPlayerCommercials.map(b=>[b.start,b.end,b.key]))')),
    [[600, 780, 'commercial:600'], [1500, 1680, 'commercial:1500']]);
});

test('other sources do not ask for commercial breaks', async () => {
  const p = player({ source: '/webdav/movie.mkv', segments: breaks });
  await p.run('loadWebPlayerSegments({durationSeconds:3600})');
  assert.equal(p.calls.filter((c) => c.path === '/video/segments').length, 0);
  assert.equal(p.run('webPlayerCommercials.length'), 0);
});

test('Skip Commercial skips one break; the next still offers its own', async () => {
  const p = player({ segments: breaks });
  await p.run('loadWebPlayerSegments({durationSeconds:3600})');
  p.at(650);
  assert.ok(p.classes.has('skip-segment-visible'));
  assert.equal(p.button.textContent, 'Skip Commercial');
  assert.equal(p.toggle.dataset.skipType, 'commercial');
  await p.run('skipActiveWebSegment()');
  assert.equal(p.run('playhead'), 780);
  p.at(700);
  assert.ok(!p.classes.has('skip-segment-visible'), 'a skipped break is not offered again');
  p.at(1550);
  assert.ok(p.classes.has('skip-segment-visible'), 'the next break is offered');
  p.at(1000);
  assert.ok(!p.classes.has('skip-segment-visible'), 'nothing between breaks');
});

test('with auto-skip on, every break is skipped by itself', async () => {
  const p = player({ segments: breaks, settings: { commercialAutoSkip: true } });
  await p.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(p.run('webAutoSkipEnabled("commercial")'), true);
  assert.equal(p.run('webAutoSkipEnabled("intro")'), false);
  await p.run('loadWebPlayerSegments({durationSeconds:3600})');
  p.at(610);
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(p.run('playhead'), 780);
});

test('the Auto toggle saves the commercial preference to the profile', async () => {
  const p = player();
  p.run('setWebAutoSkipPreference(true,"commercial")');
  await p.run('webPlayerSkipPreferenceSave');
  const save = p.calls.find((c) => c.path.endsWith('/settings/skipping'));
  assert.deepEqual(JSON.parse(save.body), { commercial: true });
  assert.equal(p.run('webAutoSkipEnabled("commercial")'), true);
  assert.equal(p.run('webAutoSkipEnabled("intro")'), false);
});
