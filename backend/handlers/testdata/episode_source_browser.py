import argparse,json
from pathlib import Path
from playwright.sync_api import sync_playwright
parser=argparse.ArgumentParser();parser.add_argument('--base-url',required=True);parser.add_argument('--chromium',required=True);parser.add_argument('--artifacts',required=True);args=parser.parse_args()
artifacts=Path(args.artifacts);artifacts.mkdir(parents=True,exist_ok=True)
with sync_playwright() as p:
 browser=p.chromium.launch(executable_path=args.chromium,headless=True);page=browser.new_page(viewport={'width':1200,'height':800});errors=[];page.on('pageerror',lambda error:errors.append(str(error)))
 assert page.request.get(args.base_url+'/fixture/info').json()['fixture']=='episode-source-hints'
 page.goto(args.base_url)
 def state(ep,value):page.wait_for_function("([ep,value])=>document.querySelector('article[data-episode=\"'+ep+'\"] .state')?.dataset.state===value",arg=[ep,value])
 def click(id):page.wait_for_function('!busy');page.locator('#'+id).click();page.wait_for_function('!busy')
 state('ep-1','found');state('ep-2','unknown');state('ep-3','not_found')
 baseline=page.request.get(args.base_url+'/fixture/info').json()['searches'];assert baseline==3
 page.screenshot(path=str(artifacts/'mediastorm-episode-hints-desktop.png'),full_page=True)
 page.set_viewport_size({'width':390,'height':844});assert page.evaluate('document.documentElement.scrollWidth<=window.innerWidth')
 page.screenshot(path=str(artifacts/'mediastorm-episode-hints-mobile.png'),full_page=True)
 click('report');state('ep-1','not_found');state('ep-2','unknown')
 click('undo');state('ep-1','found')
 click('hide');assert 'Demand released' in page.locator('#metrics').text_content()
 click('show');state('ep-1','found');state('ep-2','unknown');state('ep-3','not_found')
 assert page.request.get(args.base_url+'/fixture/info').json()['searches']==baseline
 assert page.locator('#error').text_content()=='' and not errors
 result={'passed':True,'exact_episode_found':True,'unresolved_pack_unknown':True,'separate_episode_absence':True,'report_and_undo_reuse_evidence':True,'view_release_and_return':True,'no_duplicate_searches':True,'desktop_mobile_fixture_layout':True,'native_rendering_or_playback_verified':False,'page_errors':errors}
 (artifacts/'mediastorm-episode-hints-browser-results.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result));browser.close()
