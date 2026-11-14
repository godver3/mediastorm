import argparse,json
from pathlib import Path
from playwright.sync_api import sync_playwright
p=argparse.ArgumentParser();p.add_argument('--base-url',required=True);p.add_argument('--chromium',required=True);p.add_argument('--artifacts',required=True);a=p.parse_args();art=Path(a.artifacts);art.mkdir(parents=True,exist_ok=True)
with sync_playwright() as pw:
 b=pw.chromium.launch(executable_path=a.chromium,headless=True);page=b.new_page(viewport={'width':1200,'height':900});errors=[];page.on('pageerror',lambda e:errors.append(str(e)));page.goto(a.base_url)
 def wait(id,state,reason=None):
  page.wait_for_function('([id,state,reason])=>{const e=document.querySelector("[data-id=\\\""+id+"\\\"]");return e?.dataset.state===state&&(!reason||e.dataset.reason===reason)}',arg=[id,state,reason])
 def count():return page.request.get(a.base_url+'/fixture/info').json()['searches']
 wait('movie-1','found');assert count()==1
 page.locator('#change').click();page.locator('#twice').click();page.wait_for_function('window.doubleStatuses?.length===2');assert page.evaluate('window.doubleStatuses')==[202,202]
 wait('movie-1','not_found');assert count()==2
 page.locator('#recheck').click();page.wait_for_function('document.querySelector("#reply").dataset.status==="429"');assert count()==2
 page.locator('#budget').click();wait('movie-2','unknown','recheck_budget_exhausted');assert count()==2
 page.screenshot(path=str(art/'mediastorm-source-recheck-desktop.png'),full_page=True);page.set_viewport_size({'width':390,'height':844});assert page.evaluate('document.documentElement.scrollWidth<=window.innerWidth');page.screenshot(path=str(art/'mediastorm-source-recheck-mobile.png'),full_page=True)
 assert not errors and page.locator('#error').text_content()==''
 result={'passed':True,'fresh_evidence_explicitly_rechecked':True,'duplicate_requests_one_search':True,'cooldown_429':True,'persisted_provider_budget_preserved':True,'exhaustion_stays_unknown':True,'desktop_mobile_api_fixture':True,'native_rendering_or_playback_verified':False,'page_errors':errors};(art/'mediastorm-source-recheck-browser-results.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result));b.close()
