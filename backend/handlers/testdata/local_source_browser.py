import argparse,json
from pathlib import Path
from playwright.sync_api import sync_playwright
p=argparse.ArgumentParser();p.add_argument('--base-url',required=True);p.add_argument('--chromium',required=True);p.add_argument('--artifacts',required=True);a=p.parse_args();art=Path(a.artifacts);art.mkdir(parents=True,exist_ok=True)
with sync_playwright() as pw:
 browser=pw.chromium.launch(executable_path=a.chromium,headless=True);page=browser.new_page(viewport={'width':1200,'height':800});errors=[];page.on('pageerror',lambda e:errors.append(str(e)));page.goto(a.base_url)
 def state(id,want,source=''):
  page.wait_for_function("([id,want,source])=>{const x=document.querySelector('[data-title=\"'+id+'\"] .state');return x?.dataset.state===want&&x.dataset.source===source}",arg=[id,want,source])
 state('tmdb:movie:123','found','local');state('missing-movie','not_found')
 info=lambda:page.request.get(a.base_url+'/fixture/info').json()['searches']
 assert 'tmdb:movie:123' not in info(),info()
 page.screenshot(path=str(art/'mediastorm-local-source-desktop.png'),full_page=True);page.set_viewport_size({'width':390,'height':844});assert page.evaluate('document.documentElement.scrollWidth<=window.innerWidth');page.screenshot(path=str(art/'mediastorm-local-source-mobile.png'),full_page=True)
 page.locator('#deny').click();state('tmdb:movie:123','not_found');assert info().get('tmdb:movie:123')==1
 page.locator('#allow').click();state('tmdb:movie:123','found','local');assert info().get('tmdb:movie:123')==1
 page.locator('#spanish').click();state('tmdb:movie:123','unknown');page.wait_for_function("document.querySelector('[data-title=\"tmdb:movie:123\"] .state').textContent.includes('local_source_unconfirmed')")
 page.locator('#french').click();state('tmdb:movie:123','found','local')
 assert not errors and page.locator('#error').text_content()==''
 result={'passed':True,'real_generated_media_probe':True,'local_proof_avoids_provider_search':True,'revoked_access_removes_positive':True,'restored_access_reuses_remote_evidence':True,'missing_requested_spanish_stays_unknown':True,'french_intent_restores_local_hint':True,'desktop_mobile_fixture_layout':True,'page_errors':errors,'native_rendering_or_playback_verified':False}
 (art/'mediastorm-local-source-browser-results.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result));browser.close()
