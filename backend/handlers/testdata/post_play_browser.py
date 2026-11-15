"""Render the production completion dialog with decoded media and isolated API responses."""
import argparse
import json
from pathlib import Path
from playwright.sync_api import sync_playwright


def check(value, message):
    if not value:
        raise AssertionError(message)


def run(args):
    artifacts = Path(args.artifacts)
    artifacts.mkdir(parents=True, exist_ok=True)
    checks, errors = [], []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=args.chromium, headless=True, args=["--autoplay-policy=no-user-gesture-required"])
        page = browser.new_page(viewport={"width": 1280, "height": 900})
        page.on("pageerror", lambda error: errors.append(str(error)))
        page.goto(args.base_url + "/player?token=test-token", wait_until="domcontentloaded")
        page.wait_for_function("typeof renderPlayerShell === 'function'")
        page.evaluate("""() => {
            activeProfileId='profile-a';activeProfileName='Synthetic profile';
            window.fixture={rating:'none',writes:[],lists:[],requests:[],failSave:false,failLoad:false,delaySuggestions:false,held:[]};
            window.preparePostPlay = (kind='movie', known=true, next=false) => {
                closeWebPostPlay();webPostPlaySeen.clear();webPostPlayDiscarded.clear();webPostPlayLeaving=false;
                webPlayerPlaybackEnded=false;progressCompleted=false;webPlayerCanonicalProgressItemId=null;
                playbackMeta={title:kind==='movie'?'Synthetic completed movie':'Synthetic completed series',itemId:kind==='movie'?'tmdb:movie:1':'tmdb:tv:1',mediaType:kind==='movie'?'movie':'episode',seasonNumber:1,episodeNumber:1};
                nextEpisodeLookupComplete=true;nextEpisodeInfo=next?{seasonNumber:1,episodeNumber:2}:null;webPostPlaySeriesContextKnown=known;
                renderPlayerShell();
                const video=document.getElementById('webPlayer');installVideoEvents(video);video.muted=true;video.src='/media.mp4';video.play();
            };
            window.suggestionReply = () => ({rating:fixture.rating,ratingScope:playbackMeta.mediaType==='episode'?'series':'movie',source:'saved_choices',items:Array.from({length:12},(_,i)=>({title:{id:'tmdb:movie:'+String(i+10),name:'Synthetic suggestion '+String(i+1),mediaType:'movie',tmdbId:i+10,year:2025,backdrop:{url:'data:image/svg+xml,'+encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" width="320" height="180"><rect width="320" height="180" fill="#1b2735"/></svg>')}}}))});
            apiCall=async(path,options={})=>{
                fixture.requests.push({path,method:options.method||'GET'});
                if(path.endsWith('/history/progress'))return {itemId:playbackMeta.mediaType==='episode'?'tmdb:tv:1:s01e01':'tmdb:movie:1',percentWatched:100};
                if(path.includes('/ratings/')){
                    const value=JSON.parse(options.body).rating;fixture.writes.push(value);
                    if(fixture.failSave)throw Error('Synthetic storage unavailable');fixture.rating=value;return {rating:value};
                }
                if(path.endsWith('/watchlist')){fixture.lists.push(JSON.parse(options.body));return {success:true};}
                if(path.includes('/recommendations')){
                    const reply=suggestionReply();if(fixture.delaySuggestions)return await new Promise(resolve=>fixture.held.push(()=>resolve(reply)));return reply;
                }
                if(path.includes('/post-play/')){
                    if(fixture.failLoad)throw Error('Synthetic context unavailable');
                    const series=playbackMeta.mediaType==='episode';return {title:{id:playbackMeta.itemId,mediaType:series?'series':'movie',name:playbackMeta.title},rating:fixture.rating,ratingScope:series?'series':'movie',completion:series?'episode':'movie'};
                }
                return {};
            };
            maybeTriggerNextEpisodePrequeue=()=>{};maybeStartWebCreditsDetection=()=>{};
            preparePostPlay();
        }""")
        page.wait_for_function("document.querySelector('video').webkitDecodedFrameCount > 2")
        check(not page.locator('#postPlayDialog').evaluate('d=>d.open'), 'Question shown before actual completion')
        page.evaluate("document.querySelector('video').currentTime=7.6")
        page.wait_for_function("document.querySelector('#postPlayDialog').open && webPostPlayState?.rating==='none' && !webPostPlayState.busy")
        check(page.get_by_role('button',name='Yes',exact=True).is_enabled(), 'Loaded rating stays disabled')
        check(page.locator(':focus').get_attribute('id')=='postPlayClose', 'Initial focus not on Close')
        checks.append('decoded-generated-movie-natural-end-opens-voluntary-profile-rating')
        page.wait_for_function("webPostPlayState.items.length===6")
        check(page.locator(':focus').get_attribute('id')=='postPlayClose','Asynchronous suggestions stole focus')
        for width in [320,375,390,414,768,1280]:
            page.set_viewport_size({'width':width,'height':900})
            page.wait_for_timeout(120)
            bounds=page.locator('#postPlayDialog').bounding_box()
            check(bounds and bounds['x']>=-1 and bounds['x']+bounds['width']<=width+1,'Dialog overflow')
            check(page.evaluate('document.documentElement.scrollWidth<=innerWidth'),'Horizontal page overflow')
            check(page.locator('#postPlayClose').is_visible(),'Close inaccessible')
            if width in [390,1280]:page.screenshot(path=str(artifacts/f'mediastorm-post-play-{width}.png'))
        checks.append('stable-close-focus-and-six-responsive-widths')
        page.get_by_role('button',name='Skip',exact=True).click()
        check(page.evaluate('fixture.writes.length')==0,'Skip wrote a rating')
        check(not page.get_by_role('button',name='Yes',exact=True).is_visible(),'Skip did not dismiss question')
        page.evaluate("document.querySelector('video').dispatchEvent(new Event('ended'))")
        page.wait_for_timeout(100)
        check(not page.get_by_role('button',name='Yes',exact=True).is_visible(),'Repeated end re-asked skipped question')
        checks.append('skip-never-writes-and-repeated-end-does-not-reask')
        page.get_by_role('button',name='Change rating',exact=True).click()
        page.evaluate('fixture.failSave=true')
        page.get_by_role('button',name='No',exact=True).click()
        page.wait_for_function("!webPostPlayState.busy && !document.querySelector('#postPlayError').hidden")
        check(page.evaluate('webPostPlayState.rating')=='none','Failed save invented confirmed rating')
        page.evaluate('fixture.failSave=false')
        page.get_by_role('button',name='No',exact=True).click()
        page.wait_for_function("webPostPlayState.rating==='dislike' && !webPostPlayState.busy")
        check(page.evaluate('fixture.writes.join(",")')=='dislike,dislike','No did not retry the same existing rating API')
        check(page.locator('#postPlayRatingSummary').inner_text()=='Your rating: Not for me','Confirmed rating missing')
        checks.append('negative-rating-confirms-existing-api-and-lost-save-retries')
        page.wait_for_function('webPostPlayState.items.length===6')
        link=page.locator('#postPlayCards a').first
        href=link.get_attribute('href')
        check('/watch/details?' in href and 'titleId=' in href and 'token' not in href and 'unlock' not in href,'Unsafe or unsupported details link')
        check(link.get_attribute('referrerpolicy')=='no-referrer','Details navigation may send player token in referrer')
        page.get_by_role('button',name='Save to My List',exact=True).first.click()
        page.wait_for_function('fixture.lists.length===1 && webPostPlayState.saved.size===1')
        check(page.get_by_role('button',name='Saved',exact=True).first.is_disabled(),'Saved card allows duplicate list action')
        check(page.evaluate("fixture.requests.filter(x=>/prequeue|stream-prepare|usenet/.test(x.path)).length") == 0,'Completion silently started a source lookup')
        checks.append('safe-real-watch-route-and-confirmed-save-without-source-preload')
        # Async old suggestions must lose after a newer vote.
        page.evaluate("fixture.delaySuggestions=true;void refreshWebPostPlaySuggestions()")
        page.wait_for_function('fixture.held.length===1')
        page.get_by_role('button',name='Change rating',exact=True).click()
        page.get_by_role('button',name='Yes',exact=True).click()
        page.wait_for_function("webPostPlayState.rating==='like' && fixture.held.length===2")
        page.evaluate('fixture.held.shift()()')
        page.wait_for_timeout(100)
        check(page.evaluate('webPostPlayState.items.length')==0,'Old negative suggestions replaced newer positive context')
        page.evaluate('fixture.held.shift()();fixture.delaySuggestions=false')
        page.wait_for_function('webPostPlayState.items.length===6')
        checks.append('late-suggestion-response-cannot-overwrite-newer-vote')
        # Existing vote suppresses the initial question; unknown ordering never claims series completion.
        page.evaluate("preparePostPlay('movie');webPlayerPlaybackEnded=true;maybeOfferWebPostPlay()")
        page.wait_for_function("webPostPlayState?.rating==='like' && !webPostPlayState.busy")
        check(not page.get_by_role('button',name='Yes',exact=True).is_visible(),'Existing rating re-asked')
        page.evaluate("preparePostPlay('series',false);webPlayerPlaybackEnded=true;maybeOfferWebPostPlay()")
        page.wait_for_timeout(100)
        check(not page.locator('#postPlayDialog').evaluate('d=>d.open'),'Unknown local ordering offered whole-series completion')
        page.evaluate("preparePostPlay('series',true,true);webPlayerPlaybackEnded=true;maybeOfferWebPostPlay()")
        page.wait_for_timeout(100)
        check(not page.locator('#postPlayDialog').evaluate('d=>d.open'),'Ordinary episode interrupted by rating question')
        page.evaluate("preparePostPlay('series',true);webPlayerPlaybackEnded=true;maybeOfferWebPostPlay()")
        page.wait_for_function("webPostPlayState?.kind==='series' && webPostPlayState.rating==='like'")
        check(page.locator('#postPlayScope').text_content()=='Your rating is for the whole series','Series rating scope ambiguous')
        checks.append('existing-vote-and-episode-ordering-preserve-next-episode-flow')
        # Leaving or a confirmed wrong-content report during pending completion suppresses the offer.
        page.evaluate("preparePostPlay();webPlayerPlaybackEnded=true;window.pendingCompletion=maybeOfferWebPostPlay(new Promise(r=>window.releaseCompletion=r));discardWebPostPlaySource({profileId:activeProfileId,sourcePath,prequeueId:webPlayerActivePrequeueId});releaseCompletion({itemId:'tmdb:movie:1'})")
        page.wait_for_timeout(100)
        check(not page.locator('#postPlayDialog').evaluate('d=>d.open'),'Reported wrong title got a completion question')
        page.evaluate("preparePostPlay();webPostPlayLeaving=true;webPlayerPlaybackEnded=true;maybeOfferWebPostPlay()")
        page.wait_for_timeout(100)
        check(not page.locator('#postPlayDialog').evaluate('d=>d.open'),'Navigation created a late end dialog')
        checks.append('late-wrong-content-report-and-navigation-suppress-offer')
        check(not errors, 'Uncaught browser errors: '+', '.join(errors))
        browser.close()
    result={'passed':True,'checks':checks,'production_template':True,'generated_media_decoded':True,'profile_api_responses_synthetic':True,'native_ui_rendered':False,'viewports':[320,375,390,414,768,1280]}
    (artifacts/'mediastorm-post-play-player-results.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps({'passed':True,'checks':len(checks)}))


if __name__=='__main__':
    parser=argparse.ArgumentParser()
    for arg in ['base-url','chromium','artifacts']:parser.add_argument('--'+arg,required=True)
    run(parser.parse_args())
