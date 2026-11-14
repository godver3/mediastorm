const assert = require('node:assert/strict');
const fs = require('node:fs');
const test = require('node:test');
const vm = require('node:vm');
const html = fs.readFileSync(process.env.MEDIASTORM_PLAYER_HTML, 'utf8');
const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)].at(-1)[1];

function player({response = {ok:false,status:403}, fetcher} = {}) {
  const calls = [], cleared = [], status = {textContent:'',classList:{toggle(){}}};
  const video = {paused:false,currentTime:37,readyState:4,playbackRate:1,dataset:{},src:'blob:media',
    pause(){this.paused=true;}, load(){}, removeAttribute(name){if(name==='src')this.src='';},
    play(){this.paused=false;return Promise.resolve();}};
  let destroyed=0;
  const context = vm.createContext({URL,URLSearchParams,Promise,console:{info(){},warn(){},error(){}},
    setTimeout(){return 81;},clearTimeout(id){cleared.push(id);},clearInterval(id){cleared.push(id);},
    window:{location:{search:'?watchPartyRoomId=room&shareMode=1'},addEventListener(){},localStorage:{getItem(){return null;}},setTimeout(){return 81;},setInterval(){return 91;},clearInterval(id){cleared.push(id);}},
    document:{addEventListener(){},getElementById(id){return id==='webPlayer'?video:id==='status'?status:null;}},
    fetch:async(url,options)=>{calls.push(String(url));return fetcher?fetcher(url,options):response;}});
  vm.runInContext(script,context);
  context.testVideo=video;context.testDestroy=()=>{destroyed++;};
  vm.runInContext(`watchPartyPlaybackAllowed=true;watchPartyReadySent=true;watchPartySyncTimer=11;keepaliveTimer=12;progressTimer=13;hlsSessionId='session';hlsInstance={destroy:testDestroy};`,context);
  return {context,video,status,calls,cleared,destroyed:()=>destroyed,run:code=>vm.runInContext(code,context)};
}

test('terminal room responses stop media, polling and keepalives without stopping a shared server session',async()=>{
  for(const status of [401,403,404,410]) {
    const p=player({response:{ok:false,status}});
    await p.run('syncWatchPartyState()');
    assert.equal(p.video.paused,true,'terminal '+status+' must pause the video');
    assert.equal(p.video.src,'');assert.equal(p.destroyed(),1);
    assert.ok(p.cleared.includes(11));assert.ok(p.cleared.includes(12));assert.ok(p.cleared.includes(13));
    assert.match(p.status.textContent,/no longer available/i);
    assert.equal(p.calls.length,1,'no shared /stop request is permitted');
    await p.run('syncWatchPartyState();startWatchPartySync();startKeepalive();startProgressTracking();attemptAutoplay(testVideo);');
    assert.equal(p.video.paused,true);assert.equal(p.calls.length,1);
    assert.equal(p.run('watchPartySyncTimer'),null);assert.equal(p.run('keepaliveTimer'),null);assert.equal(p.run('progressTimer'),null);
  }
});

test('an ended room snapshot also tears down playback and keeps its final message',async()=>{
  const p=player({response:{ok:true,status:200,json:async()=>({status:'ended',members:[]})}});
  await p.run('syncWatchPartyState()');
  assert.equal(p.video.paused,true);assert.equal(p.video.src,'');assert.equal(p.destroyed(),1);
  assert.match(p.status.textContent,/room has ended/i);
  p.run('setStatus("")');assert.match(p.status.textContent,/room has ended/i);
});

test('rate limiting, service errors and network interruption keep synchronization retryable',async()=>{
  for(const status of [429,500,502,503]) {
    const p=player({response:{ok:false,status}});await p.run('syncWatchPartyState()');
    assert.equal(p.video.paused,false);assert.equal(p.destroyed(),0);assert.equal(p.run('watchPartySyncTimer'),11);
    await p.run('syncWatchPartyState()');assert.equal(p.calls.length,2);
  }
  const p=player({fetcher:async()=>{throw new Error('offline');}});await p.run('syncWatchPartyState()');
  assert.equal(p.video.paused,false);assert.equal(p.run('watchPartySyncTimer'),11);
});

test('a pending autoplay rejection cannot restart a terminated room',async()=>{
  let reject, plays=0;
  const p=player();p.video.play=()=>{plays++;p.video.paused=false;return new Promise((_,no)=>{reject=no;});};
  p.run('attemptAutoplay(testVideo)');await p.run('syncWatchPartyState()');
  reject(new Error('autoplay blocked'));await Promise.resolve();await Promise.resolve();
  assert.equal(plays,1);assert.equal(p.video.paused,true);assert.equal(p.video.src,'');
});

test('an HLS attachment completing after termination cannot revive media',async()=>{
  const p=player();await p.run('syncWatchPartyState()');
  p.run('hlsSessionId="late-session";attachWebPlayer(testVideo,"/late.m3u8");');
  assert.equal(p.video.src,'');assert.equal(p.run('hlsSessionId'),'');
});

test('a paused room can still resume without losing its media',async()=>{
  let status='paused';
  const p=player({fetcher:async()=>({ok:true,status:200,json:async()=>({status,position:37,members:[]})})});
  await p.run('syncWatchPartyState()');assert.equal(p.video.paused,true);assert.equal(p.destroyed(),0);
  status='playing';await p.run('syncWatchPartyState()');assert.equal(p.video.paused,false);assert.equal(p.destroyed(),0);
});

test('metadata finishing after termination cannot allocate a new HLS session',async()=>{
  let release,started;
  const requested=new Promise(resolve=>{started=resolve;});
  const p=player();p.context.testMetadata=()=>{started();return new Promise(resolve=>{release=resolve;});};
  p.context.unexpectedSession=()=>{throw new Error('must not allocate a session');};
  p.run('hlsSessionId="";hlsInstance=null;sourcePath="/fixture.mkv";activeProfileId="profile";loadTrackMetadata=testMetadata;createHlsSession=unexpectedSession;renderLoading=()=>{};');
  const starting=p.run('startWebPlayerFromSource()');await requested;
  await p.run('syncWatchPartyState()');release({});await starting;
  assert.equal(p.video.paused,true);assert.equal(p.video.src,'');assert.equal(p.run('hlsSessionId'),'');
});
