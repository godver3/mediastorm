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
      for(const [key,value] of Object.entries(patch)) existing[key==='intro'?'introAutoSkip':key==='recap'?'recapAutoSkip':'creditsAutoSkip']=value;
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
