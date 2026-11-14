import argparse,json,time
from pathlib import Path
from playwright.sync_api import sync_playwright
p=argparse.ArgumentParser();p.add_argument('--base-url',required=True);p.add_argument('--chromium',required=True);p.add_argument('--artifacts',required=True);a=p.parse_args();art=Path(a.artifacts);art.mkdir(parents=True,exist_ok=True)
with sync_playwright() as pw:
 browser=pw.chromium.launch(executable_path=a.chromium,headless=True);page=browser.new_page(viewport={'width':1100,'height':1000});errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
 page.goto(a.base_url);page.wait_for_function("document.querySelectorAll('article').length===10")
 def info():return page.request.get(a.base_url+'/fixture/info').json()['searches']
 def wait_for(predicate):
  deadline=time.monotonic()+15
  while time.monotonic()<deadline:
   if predicate():return
   page.wait_for_timeout(100)
  raise AssertionError('Timed out waiting for viewport evidence')
 wait_for(lambda:page.locator('[data-title="movie-1"] .state').get_attribute('data-state')=='found')
 wait_for(lambda:page.locator('[data-title="movie-2"] .state').get_attribute('data-state')=='not_found')
 wait_for(lambda:'search_incomplete' in page.locator('[data-title="movie-3"] .state').text_content())
 initial=info();assert initial and not any('movie-'+str(n) in initial for n in range(5,11)),initial
 page.screenshot(path=str(art/'mediastorm-visible-titles-desktop.png'),full_page=False)
 page.locator('[data-title="movie-9"]').scroll_into_view_if_needed();wait_for(lambda:'movie-9' in info());assert 'movie-5' not in info(),info()
 page.locator('#hide').click();page.wait_for_function("document.querySelector('#metrics').textContent==='Demand released'");hidden=info();page.wait_for_timeout(2200);assert info()==hidden
 page.locator('#show').click();page.evaluate('window.scrollTo(0,0)');wait_for(lambda:page.locator('[data-title="movie-1"] .state').get_attribute('data-state')=='found')
 page.wait_for_timeout(2200);assert all(n==1 for n in info().values()),info()
 page.set_viewport_size({'width':390,'height':844});page.wait_for_timeout(350);assert page.evaluate('document.documentElement.scrollWidth<=window.innerWidth');page.screenshot(path=str(art/'mediastorm-visible-titles-mobile.png'),full_page=False)
 assert not errors and page.locator('#error').text_content()==''
 result={'passed':True,'actual_intersection_scroll':True,'delivered_hidden_titles_not_searched':True,'hidden_view_stops_work':True,'cache_revisit_no_duplicate_searches':True,'incomplete_is_unknown':True,'desktop_mobile_layout':True,'searches':info(),'page_errors':errors,'native_rendering_or_playback_verified':False}
 (art/'mediastorm-visible-titles-browser-results.json').write_text(json.dumps(result,indent=2)+'\n');print(json.dumps(result));browser.close()
