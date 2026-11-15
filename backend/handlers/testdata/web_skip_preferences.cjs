const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');
const html = fs.readFileSync(process.env.MEDIASTORM_PLAYER_HTML, 'utf8');
const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].at(-1)[1];

function player(store, profile='adult', query='', api) {
  const calls=[], warnings=[];
  const context=vm.createContext({URL,URLSearchParams,setTimeout,clearTimeout,
    window:{location:{search:query},addEventListener(){},localStorage:{getItem(){return 'true';},setItem(){throw Error('must not write browser preferences');}}},
    document:{addEventListener(){},getElementById(){return null;},querySelector(){return null;}},
    console:{info(){},warn(){},error(){}}});
  vm.runInContext(script,context);
  context.profile=profile;
  context.api=async(path,options={})=>{
    calls.push({path,...options});
    if(api) { const value=await api(path,options); if(value!==undefined)return value; }
    if(path.endsWith('/settings/skipping')) {
      const target=path.split('/')[2];const patch=JSON.parse(options.body);
      const existing=store.get(target)||{};
      for(const [key,value] of Object.entries(patch)) existing[key==='intro'?'introAutoSkip':key==='recap'?'recapAutoSkip':key==='nextEpisode'?'nextEpisodeAutoPlay':'creditsAutoSkip']=value;
      store.set(target,existing);return patch;
    }
    if(path.endsWith('/settings'))return {playback:{...(store.get(profile)||{})}};
    return {};
  };
  context.warn=message=>warnings.push(message);
  vm.runInContext(`activeProfileId=profile;apiCall=api;setTransientStatus=warn;updateWebSegmentPrompts=()=>{};updateUpNextVisibility=()=>{};`,context);
  return {calls,warnings,run:code=>vm.runInContext(code,context),settle:()=>vm.runInContext('webPlayerSkipPreferenceSave',context)};
}

test('profile settings override old browser choices and stay separate on another device',async()=>{
  const store=new Map([['adult',{introAutoSkip:false,recapAutoSkip:true,creditsAutoSkip:false,preferredAudioLanguage:'ger'}]]);
  const first=player(store);await first.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(first.run('webAutoSkipEnabled("intro")'),false);
  assert.equal(first.run('webAutoSkipEnabled("recap")'),true);
  first.run('setWebAutoSkipPreference(true,"intro");setWebAutoSkipPreference(true,"credits");');await first.settle();
  const reopened=player(store);await reopened.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(reopened.run('webAutoSkipEnabled("intro")'),true);
  assert.equal(reopened.run('webAutoSkipEnabled("credits")'),true);
  assert.equal(store.get('adult').preferredAudioLanguage,'ger');
  const child=player(store,'child');await child.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(child.run('webAutoSkipEnabled("intro")'),false);
});

test('rapid changes preserve their order and send only the changed segment',async()=>{
  const store=new Map();const p=player(store);
  p.run('setWebAutoSkipPreference(true,"intro");setWebAutoSkipPreference(false,"intro");setWebAutoSkipPreference(true,"recap");');
  await p.settle();
  assert.deepEqual(store.get('adult'),{introAutoSkip:false,recapAutoSkip:true});
  assert.ok(p.calls.every(x=>Object.keys(JSON.parse(x.body)).length===1));
});

test('a failed save is visible and leaves playback controls usable',async()=>{
  const p=player(new Map(),'adult','',async()=>{throw Error('offline');});
  p.run('setWebAutoSkipPreference(true,"intro")');await p.settle();
  assert.equal(p.run('webAutoSkipEnabled("intro")'),true);assert.equal(p.warnings.length,1);
});

test('guest and shared playback never save account preferences',async()=>{
  for(const query of ['?shareMode=1','?watchPartyRoomId=room']) {
    const p=player(new Map(),'adult',query);
    p.run('setWebAutoSkipPreference(true,"intro")');await p.settle();assert.equal(p.calls.length,0);
  }
});

test('a late settings response cannot replace a newer toggle',async()=>{
  let release,requested;const issued=new Promise(resolve=>requested=resolve);
  const p=player(new Map(),'adult','',async(path)=>{
    if(path.endsWith('/settings')){requested();return new Promise(resolve=>release=resolve);}
  });
  const loading=p.run('loadEffectiveWebPlaybackSettings()');await issued;
  p.run('setWebAutoSkipPreference(true,"intro")');await p.settle();
  release({playback:{introAutoSkip:false}});await loading;
  assert.equal(p.run('webAutoSkipEnabled("intro")'),true);
});

test('toggling one segment while settings load keeps the loaded values for the others',async()=>{
  let release,requested;const issued=new Promise(resolve=>requested=resolve);
  const p=player(new Map(),'adult','',async(path)=>{
    if(path.endsWith('/settings')){requested();return new Promise(resolve=>release=resolve);}
  });
  const loading=p.run('loadEffectiveWebPlaybackSettings()');await issued;
  p.run('setWebAutoSkipPreference(false,"intro")');await p.settle();
  release({playback:{introAutoSkip:true,recapAutoSkip:true,creditsAutoSkip:true}});await loading;
  assert.equal(p.run('webAutoSkipEnabled("intro")'),false);
  assert.equal(p.run('webAutoSkipEnabled("recap")'),true);
  assert.equal(p.run('webAutoSkipEnabled("credits")'),true);
});

test('a failed settings load keeps the current auto-skip choices',async()=>{
  const store=new Map([['adult',{introAutoSkip:true,recapAutoSkip:true}]]);
  let offline=false;
  const p=player(store,'adult','',async(path)=>{if(offline&&path.endsWith('/settings'))throw Error('offline');});
  await p.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(p.run('webAutoSkipEnabled("intro")'),true);
  offline=true;await p.run('loadEffectiveWebPlaybackSettings()');
  assert.equal(p.run('webAutoSkipEnabled("intro")'),true);
  assert.equal(p.run('webAutoSkipEnabled("recap")'),true);
});

test('next episode is independent, profile bound, legacy compatible and fail closed on initial load',async()=>{
 const store=new Map([['adult',{creditsAutoSkip:true,nextEpisodeAutoPlay:false}]]);
 const first=player(store);await first.run('loadEffectiveWebPlaybackSettings()');
 assert.equal(first.run('webAutoSkipEnabled("nextEpisode")'),false);
 assert.equal(first.run('webAutoSkipEnabled("credits")'),true);
 first.run('setWebAutoSkipPreference(true,"nextEpisode")');await first.settle();
 const reopened=player(store);await reopened.run('loadEffectiveWebPlaybackSettings()');
 assert.equal(reopened.run('webAutoSkipEnabled("nextEpisode")'),true);
 first.run('setWebAutoSkipPreference(false,"nextEpisode")');await first.settle();
 assert.equal(store.get('adult').creditsAutoSkip,true);
 const child=player(store,'child');await child.run('loadEffectiveWebPlaybackSettings()');
 assert.equal(child.run('webAutoSkipEnabled("nextEpisode")'),true);
 const failed=player(store,'adult','',async()=>{throw Error('offline')});await failed.run('loadEffectiveWebPlaybackSettings()');
 assert.equal(failed.run('webAutoSkipEnabled("nextEpisode")'),false);
});

function countdown(query='') {
 let now=0, callback, advances=0;
 const elements={webPlayer:{paused:false},upNextCountdown:{textContent:''},pauseNextCountdown:{textContent:'',hidden:true},videoHost:{classList:{remove(){}}}};
 const context=vm.createContext({URL,URLSearchParams,setTimeout,clearTimeout,performance:{now:()=>now},clearInterval(){callback=null;},
  window:{location:{search:query},addEventListener(){},setInterval(fn){callback=fn;return 1;},localStorage:{getItem(){return null;}}},
  document:{addEventListener(){},querySelector(){return null;},getElementById(id){return elements[id]||null;}},console:{info(){},warn(){},error(){}}});
 vm.runInContext(script,context);
 context.advance=()=>{advances++;};
 vm.runInContext('playNextEpisode=advance;webPlayerNextEpisodeAutoPlayPreference=true;',context);
 return {run:code=>vm.runInContext(code,context),tick:ms=>{now=ms;callback?.();},saved:()=>callback,advances:()=>advances,elements};
}

test('a genuine end counts down without skipping credits and advances only once',()=>{
 const p=countdown();p.run('webPlayerPlaybackEnded=true;ensureWatchNextCountdown()');
 assert.match(p.elements.upNextCountdown.textContent,/5s/);p.tick(4_000);assert.equal(p.advances(),0);
 p.tick(5_000);p.tick(6_000);assert.equal(p.advances(),1);
});

test('autoplay off blocks both automatic paths and watch-party never advances independently',()=>{
 for(const query of ['', '?watchPartyRoomId=synthetic-room']) {
  const p=countdown(query);p.run('webPlayerNextEpisodeAutoPlayPreference=false;webPlayerAutoSkipCreditsPreference=true;webPlayerPromptConfirmed=true;webPlayerPlaybackEnded=true;ensureWatchNextCountdown()');
  p.tick(90_000);assert.equal(p.advances(),0);assert.ok(!p.saved());
 }
 const guest=countdown('?watchPartyRoomId=synthetic-room');guest.run('webPlayerPlaybackEnded=true;ensureWatchNextCountdown()');guest.tick(90_000);assert.equal(guest.advances(),0);
});

test('credits need both choices and do not advance merely because time is near the end',()=>{
 const p=countdown();p.run('webPlayerPromptConfirmed=true;ensureWatchNextCountdown()');p.tick(90_000);assert.equal(p.advances(),0);
 p.run('webPlayerAutoSkipCreditsPreference=true;ensureWatchNextCountdown()');p.tick(96_000);assert.equal(p.advances(),0);p.tick(97_000);assert.equal(p.advances(),1);
});

test('pausing video holds credits countdown and explicit pause also holds natural end',()=>{
 const p=countdown();p.run('webPlayerPromptConfirmed=true;webPlayerAutoSkipCreditsPreference=true;ensureWatchNextCountdown()');p.tick(2_000);
 p.elements.webPlayer.paused=true;p.tick(90_000);assert.match(p.elements.upNextCountdown.textContent,/paused.*5s/);
 p.elements.webPlayer.paused=false;p.tick(91_000);assert.match(p.elements.upNextCountdown.textContent,/4s/);
 p.run('webPlayerPlaybackEnded=true;toggleWatchNextCountdownPause()');p.tick(92_000);p.tick(120_000);assert.equal(p.advances(),0);
 p.run('toggleWatchNextCountdownPause()');p.tick(124_000);assert.equal(p.advances(),1);
});

test('cancel survives a late end, credits toggle and an already queued timer callback',()=>{
 const p=countdown();p.run('webPlayerPlaybackEnded=true;ensureWatchNextCountdown()');const late=p.saved();
 p.run('dismissUpNext();webPlayerAutoSkipCreditsPreference=true;ensureWatchNextCountdown()');p.tick(90_000);late();assert.equal(p.advances(),0);
 assert.equal(p.run('webPlayerWatchNextAutoAdvanceBlocked'),true);
});

test('clearing a countdown invalidates queued work from the previous episode',()=>{
 const p=countdown();p.run('webPlayerPlaybackEnded=true;ensureWatchNextCountdown()');const old=p.saved();
 p.run('clearWatchNextCountdown();webPlayerPlaybackEnded=false;');p.tick(90_000);old();assert.equal(p.advances(),0);
});

test('malformed autoplay and missing playback settings do not enable automatic transitions',async()=>{
 const malformed=player(new Map([['adult',{nextEpisodeAutoPlay:'false'}]]));await malformed.run('loadEffectiveWebPlaybackSettings()');
 assert.equal(malformed.run('webAutoSkipEnabled("nextEpisode")'),false);
 const missing=player(new Map(),'adult','',async()=>({}));await missing.run('loadEffectiveWebPlaybackSettings()');
 assert.equal(missing.run('webAutoSkipEnabled("nextEpisode")'),false);
});

test('seeking just before timer expiry cancels a queued automatic transition',()=>{
 const p=countdown();p.run('webPlayerPlaybackEnded=true;ensureWatchNextCountdown()');
 p.elements.webPlayer.seeking=true;p.tick(5_000);assert.equal(p.advances(),0);
 assert.equal(p.run('webPlayerWatchNextAutoAdvanceBlocked'),true);
 p.elements.webPlayer.seeking=false;p.run('ensureWatchNextCountdown()');p.tick(90_000);assert.equal(p.advances(),0);
});
