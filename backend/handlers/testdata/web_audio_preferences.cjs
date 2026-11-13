const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');

const html = fs.readFileSync(process.env.MEDIASTORM_PLAYER_HTML, 'utf8');
const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].at(-1)[1];

function player({ profile = 'profile-a', query = '', media = {}, preferences = new Map(), api } = {}) {
  const calls = [];
  const restarts = [];
  const warnings = [];
  const context = vm.createContext({
    URL, URLSearchParams, setTimeout, clearTimeout,
    window: { location: { search: query }, addEventListener() {}, localStorage: { getItem() { return null; } } },
    document: { addEventListener() {}, getElementById() { return null; } },
    console: { info() {}, warn(...args) { warnings.push(args); }, error() {} },
  });
  vm.runInContext(script, context);
  context.testProfile = profile;
  context.testMedia = { mediaType: 'episode', itemId: 'tmdb:tv:90669', seriesTitle: '1899', tmdbId: '90669', seasonNumber: 1, episodeNumber: 2, ...media };
  context.testRestart = options => restarts.push(options);
  context.testAPI = async (path, options = {}) => {
    calls.push({ path, method: options.method || 'GET', body: options.body ? JSON.parse(options.body) : null });
    if (api) {
      const result = await api(path, options);
      if (result !== undefined) return result;
    }
    if (path.endsWith('/preferences/content') && options.method === 'PUT') {
      const value = JSON.parse(options.body);
      preferences.set(path + '/' + encodeURIComponent(value.contentId), value);
      return value;
    }
    if (path.includes('/preferences/content/')) return { ...(preferences.get(path) || {}) };
    if (path.endsWith('/settings')) return { playback: { preferredAudioLanguage: 'ger' } };
    throw new Error('Unexpected API call: ' + path);
  };
  vm.runInContext(`
    activeProfileId = testProfile;
    playbackMeta = testMedia;
    webPlayerAudioStreams = [{index:1,language:'ger'}, {index:2,language:'eng'}, {index:3,language:'und'}];
    webPlayerSelectedAudioTrack = 1;
    currentAbsoluteTime = () => 37;
    startWebPlayerFromSource = testRestart;
    apiCall = testAPI;
  `, context);
  return {
    context, calls, restarts, warnings, preferences,
    run: code => vm.runInContext(code, context),
    async settle() { await vm.runInContext('typeof webPlayerAudioPreferenceSave === "undefined" ? Promise.resolve() : webPlayerAudioPreferenceSave', context); },
  };
}

const preferenceKey = profile => `/users/${profile}/preferences/content/${encodeURIComponent('tmdb:tv:90669')}`;

test('audio changes persist for the series without replacing subtitle preferences', async () => {
  const preferences = new Map([[preferenceKey('profile-a'), { contentId: 'tmdb:tv:90669', contentType: 'series', audioLanguage: 'ger', subtitleLanguage: 'fra', subtitleMode: 'forced-only' }]]);
  const p = player({ preferences });
  p.run('changeAudioTrack("2")');
  assert.equal(p.restarts.length, 1, 'playback must restart without waiting for the preference API');
  assert.equal(p.restarts[0].startOffset, 37);
  await p.settle();
  assert.equal(preferences.get(preferenceKey('profile-a')).audioLanguage, 'eng');
  assert.equal(preferences.get(preferenceKey('profile-a')).subtitleLanguage, 'fra');
  assert.equal(preferences.get(preferenceKey('profile-a')).subtitleMode, 'forced-only');

  const reopened = player({ preferences, media: { episodeNumber: 3 } });
  await reopened.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(reopened.run('webPlayerPreferences.audioLanguage'), 'eng');
  const other = player({ preferences, profile: 'profile-b' });
  await other.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(other.run('webPlayerPreferences.audioLanguage'), 'deu');
});

test('rapid choices finish with the last selected language', async () => {
  const p = player({ api: async () => { await new Promise(resolve => setTimeout(resolve, 5)); } });
  p.run('changeAudioTrack("2"); changeAudioTrack("1"); changeAudioTrack("2");');
  await p.settle();
  assert.equal(p.preferences.get(preferenceKey('profile-a'))?.audioLanguage, 'eng');
  assert.equal(p.restarts.length, 3);
});

test('movie selection is scoped to that movie', async () => {
  const p = player({ media: { mediaType: 'movie', tmdbId: '27205' } });
  p.run('changeAudioTrack("2")');
  await p.settle();
  const write = p.calls.find(x => x.method === 'PUT');
  assert.equal(write?.body.contentId, 'tmdb:movie:27205');
  assert.equal(write?.body.contentType, 'movie');
});

test('unidentified audio, shared streams and live TV do not write library preferences', async () => {
  for (const options of [{ track: 3 }, { query: '?shareMode=1' }, { query: '?watchPartyRoomId=room' }, { media: { isLive: true } }]) {
    const p = player(options);
    p.run(`changeAudioTrack("${options.track || 2}")`);
    await p.settle();
    assert.equal(p.calls.length, 0);
    assert.equal(p.restarts.length, 1);
  }
});

test('a failed preference read keeps playback usable and never overwrites preferences', async () => {
  const p = player({ api: async () => { throw new Error('offline'); } });
  p.run('changeAudioTrack("2")');
  await p.settle();
  assert.equal(p.restarts.length, 1);
  assert.equal(p.calls.filter(x => x.method === 'PUT').length, 0);
  assert.ok(p.warnings.length, 'saving failure must be reported');
});

test('a prequeue request from the old language cannot replace the new preparation', async () => {
  let release, requested;
  const issued = new Promise(resolve => { requested = resolve; });
  const p = player({ api: async (path, options) => {
    if (path === '/playback/prequeue' && options.method === 'POST') {
      requested();
      return new Promise(resolve => { release = resolve; });
    }
  } });
  p.run('nextEpisodeInfo = {seasonNumber:1,episodeNumber:3};');
  const preparing = p.run('triggerNextEpisodePrequeue()');
  await issued;
  p.run('changeAudioTrack("2")');
  await p.settle();
  release({ prequeueId: 'old-language', status: 'ready' });
  assert.equal(await preparing, null);
  assert.equal(p.run('nextEpisodePrequeue'), null);
});

test('a poll response from the old language cannot revive an invalidated preparation', async () => {
  let release, requested;
  const issued = new Promise(resolve => { requested = resolve; });
  const p = player({ api: async path => {
    if (path === '/playback/prequeue/old-language') {
      requested();
      return new Promise(resolve => { release = resolve; });
    }
  } });
  p.run('nextEpisodePrequeue = {prequeueId:"old-language",status:"probing"};');
  const polling = p.run('pollNextEpisodePrequeue("old-language")');
  await issued;
  p.run('changeAudioTrack("2")');
  await p.settle();
  release({ status: 'ready', streamPath: '/old-language.mkv' });
  assert.equal(await polling, null);
  assert.equal(p.run('nextEpisodePrequeue'), null);
});

test('next episode preparation waits for the saved series language', async () => {
  let release, writing;
  const started = new Promise(resolve => { writing = resolve; });
  let p;
  p = player({ api: async (path, options) => {
    if (options.method === 'PUT') {
      writing();
      await new Promise(resolve => { release = resolve; });
    }
    if (path === '/playback/prequeue' && options.method === 'POST') {
      assert.equal(p.preferences.get(preferenceKey('profile-a')).audioLanguage, 'eng');
      return { prequeueId: 'new-language', status: 'ready' };
    }
    if (path === '/playback/prequeue/new-language') return { status:'ready',streamPath:'/new-language.mkv' };
  } });
  p.run('nextEpisodeInfo={seasonNumber:1,episodeNumber:3}; nextEpisodePrequeue={prequeueId:"old-language",status:"ready",targetEpisode:nextEpisodeInfo,statusResponse:{streamPath:"/old-language.mkv"}};');
  p.run('changeAudioTrack("2")');
  const next = p.run('ensureNextEpisodePrequeueReady()');
  await started;
  assert.equal(p.calls.filter(x => x.path === '/playback/prequeue').length, 0);
  release();
  const ready = await next;
  assert.equal(ready?.streamPath, '/new-language.mkv');
  await p.run('nextEpisodePrequeuePollPromise');
});

test('a language change during the final ready check discards the old source', async () => {
  let release, requested;
  const issued = new Promise(resolve => { requested = resolve; });
  const p = player({ api: async (path, options) => {
    if (path === '/playback/prequeue/old-language') {
      requested();
      return new Promise(resolve => { release = resolve; });
    }
    if (path === '/playback/prequeue' && options.method === 'POST') return { prequeueId: 'new-language', status: 'ready' };
    if (path === '/playback/prequeue/new-language') return { status:'ready', streamPath:'/new-language.mkv' };
  } });
  p.run('nextEpisodeInfo={seasonNumber:1,episodeNumber:3}; nextEpisodePrequeue={prequeueId:"old-language",status:"ready",targetEpisode:nextEpisodeInfo};');
  const ready = p.run('ensureNextEpisodePrequeueReady()');
  await issued;
  p.run('changeAudioTrack("2")');
  await p.settle();
  release({status:'ready',streamPath:'/old-language.mkv'});
  assert.equal((await ready)?.streamPath, '/new-language.mkv');
  await p.run('nextEpisodePrequeuePollPromise');
});

const titleKey = (profile, titleId) => `/users/${profile}/preferences/content/${encodeURIComponent(titleId)}`;

test('audio choices are saved under the title ID that other clients and the server read', async () => {
  // The series was opened by its TVDB title ID; an earlier web build saved a subtitle
  // override under the TMDB form, which must move along with the new audio choice.
  const preferences = new Map([[preferenceKey('profile-a'), { contentId: 'tmdb:tv:90669', contentType: 'series', subtitleLanguage: 'fra', subtitleMode: 'on' }]]);
  const p = player({ preferences, query: '?titleId=tvdb:series:366581' });
  p.run('changeAudioTrack("2")');
  await p.settle();
  const write = p.calls.find(x => x.method === 'PUT');
  assert.equal(write?.body.contentId, 'tvdb:series:366581');
  assert.equal(write?.body.audioLanguage, 'eng');
  assert.equal(write?.body.subtitleLanguage, 'fra');
  assert.equal(write?.body.subtitleMode, 'on');

  const reopened = player({ preferences, query: '?titleId=tvdb:series:366581', media: { episodeNumber: 3 } });
  await reopened.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(reopened.run('webPlayerPreferences.audioLanguage'), 'eng');
  assert.equal(reopened.run('webPlayerPreferences.subtitleLanguage'), 'fra');
});

test('an override saved by the app under the title ID applies in the web player', async () => {
  const preferences = new Map([
    [titleKey('profile-a', 'tvdb:series:366581'), { contentId: 'tvdb:series:366581', contentType: 'series', audioLanguage: 'jpn' }],
    [preferenceKey('profile-a'), { contentId: 'tmdb:tv:90669', contentType: 'series', audioLanguage: 'eng' }],
  ]);
  const p = player({ preferences, query: '?titleId=TVDB:series:366581' });
  await p.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(p.run('webPlayerPreferences.audioLanguage'), 'jpn');
});
