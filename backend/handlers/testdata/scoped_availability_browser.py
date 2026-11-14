import argparse
import json
from pathlib import Path
from playwright.sync_api import sync_playwright

parser=argparse.ArgumentParser()
parser.add_argument('--base-url',required=True)
parser.add_argument('--chromium',required=True)
parser.add_argument('--artifacts',required=True)
args=parser.parse_args()
artifacts=Path(args.artifacts);artifacts.mkdir(parents=True,exist_ok=True)
with sync_playwright() as p:
 browser=p.chromium.launch(executable_path=args.chromium,headless=True)
 page=browser.new_page(viewport={'width':1200,'height':900})
 errors=[];page.on('pageerror',lambda error:errors.append(str(error)))
 page.goto(args.base_url)
 def state(profile,wanted):
  page.wait_for_function("([profile,wanted])=>document.querySelector('article[data-profile=\"'+profile+'\"] .state')?.dataset.state===wanted",arg=[profile,wanted])
 def click(id):
  page.locator('#'+id).click()
  page.wait_for_function("!busy")
  assert page.locator('#error').text_content()==''
 for profile in ['profile','shared-french','english']:state(profile,'found')
 initial=page.request.get(args.base_url+'/fixture/info').json()['searches']
 before=page.request.get(args.base_url+'/api/users/profile/home/manifest?clientId=fixture-device').json()
 click('reportA')
 for profile in ['profile','shared-french','english']:state(profile,'found')
 click('reportB')
 for profile in ['profile','shared-french']:state(profile,'not_found')
 state('english','found')
 after=page.request.get(args.base_url+'/api/users/profile/home/manifest?clientId=fixture-device').json()
 assert before['revision']!=after['revision']
 assert before['sourceAvailabilityRevision']!=after['sourceAvailabilityRevision']
 page.screenshot(path=str(artifacts/'mediastorm-scoped-availability-desktop.png'),full_page=True)
 page.set_viewport_size({'width':390,'height':844})
 assert page.evaluate('document.documentElement.scrollWidth<=window.innerWidth')
 page.screenshot(path=str(artifacts/'mediastorm-scoped-availability-mobile.png'),full_page=True)
 click('undo')
 for profile in ['profile','shared-french','english']:state(profile,'found')
 assert page.request.get(args.base_url+'/fixture/reports').json()==[]
 assert page.request.get(args.base_url+'/fixture/info').json()['searches']==initial
 assert not errors,errors
 result={'passed':True,'healthy_alternative_retained':True,'language_scoped':True,'shared_profiles_updated':True,'manifest_invalidated':True,'undo_restores_hint':True,'no_additional_indexer_searches':True,'desktop_mobile_layout':True,'synthetic_backend_fixture':True,'native_layout_or_media_playback_tested':False,'page_errors':errors}
 (artifacts/'mediastorm-scoped-availability-browser-results.json').write_text(json.dumps(result,indent=2)+'\n')
 print(json.dumps(result));browser.close()
