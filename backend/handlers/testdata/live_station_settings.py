import argparse
import json
from pathlib import Path
from playwright.sync_api import sync_playwright
parser=argparse.ArgumentParser(description="Exercise the real Live TV settings UI against its isolated Go test fixture.")
parser.add_argument('--base-url',required=True)
parser.add_argument('--chromium',required=True)
parser.add_argument('--artifacts',required=True)
args=parser.parse_args()
root=Path(args.artifacts)
base=args.base_url.rstrip('/')
with sync_playwright() as p:
    browser=p.chromium.launch(executable_path=args.chromium,headless=True)
    page=browser.new_page(viewport={'width':1440,'height':1100})
    errors=[]
    page.on('pageerror',lambda e: errors.append(str(e)))
    fixture=page.request.get(base+'/fixture/info')
    assert fixture.ok and fixture.json().get('fixture')=='live-station-settings', 'Refusing to modify a non-fixture server'
    page.goto(base+'/admin/settings')
    page.wait_for_function('typeof currentSettings !== "undefined" && currentSettings.live !== undefined')
    page.wait_for_timeout(400)
    if page.locator('#adminWalkthroughSkip').is_visible(): page.locator('#adminWalkthroughSkip').click()
    page.locator('#settingsAdvancedBtn').click()
    page.evaluate("setSettingsGroup('sources')")
    page.evaluate("openSettingsSection('live',{focus:false})")
    group=page.locator('#live_groupChannelsByStation')
    failover=page.locator('#live_autoChannelFailover')
    group.wait_for(state='attached')
    assert not group.is_checked(), 'grouping was not default off'
    assert not failover.is_checked(), 'failover was not default off'
    group.locator('..').click()
    failover.locator('..').click()
    assert group.is_checked() and failover.is_checked()
    page.evaluate("openSettingsSection('live.stationMappings',{focus:false})")
    for i,source in enumerate(['german-a','german-b']):
        section=page.locator('[id="section-live.stationMappings"]')
        section.locator('.add-item-btn').click()
        page.evaluate("openSettingsSection('live.stationMappings',{focus:false})")
        for field,value in {'sourceId':source,'channelId':source+':RTL-de','stationId':'rtl-fixture','name':'RTL (fixture correction)'}.items():
            item=page.locator('#live_stationMappings_'+str(i)+'_'+field)
            item.fill(value)
            item.press('Tab')
    with page.expect_response(lambda r: r.url.endswith('/admin/api/settings') and r.request.method=='PUT') as saved:
        page.evaluate('saveAllSettings()')
    assert saved.value.status==200, saved.value.text()
    stored=page.request.get(base+'/admin/api/settings').json()['live']
    assert stored['groupChannelsByStation'] and stored['autoChannelFailover'], stored
    assert len(stored['stationMappings'])==2 and stored['stationMappings'][1]['stationId']=='rtl-fixture', stored
    page.reload()
    page.wait_for_function('currentSettings.live.groupChannelsByStation === true && currentSettings.live.autoChannelFailover === true')
    assert page.evaluate('currentSettings.live.stationMappings.length')==2
    page.wait_for_timeout(300)
    if page.locator('#adminWalkthroughSkip').is_visible(): page.locator('#adminWalkthroughSkip').click()
    page.locator('#settingsAdvancedBtn').click()
    page.evaluate("setSettingsGroup('sources')")
    page.evaluate("openSettingsSection('live',{focus:false})")
    page.locator('#live_groupChannelsByStation').locator('..').scroll_into_view_if_needed()
    page.evaluate("document.body.insertAdjacentHTML('beforeend','<aside style=\"position:fixed;right:18px;top:18px;background:#15202c;color:white;border:1px solid #64748b;padding:12px;z-index:99999;font:13px system-ui\">LOCAL PREVIEW<br>Real settings UI, synthetic playlists</aside>')")
    page.screenshot(path=str(root/'mediastorm-stations-settings-desktop.png'))
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth+1'), 'desktop overflow'
    page.set_viewport_size({'width':390,'height':844})
    page.wait_for_timeout(300)
    page.locator('#live_groupChannelsByStation').locator('..').scroll_into_view_if_needed()
    page.screenshot(path=str(root/'mediastorm-stations-settings-mobile.png'))
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth+1'), 'mobile overflow'
    assert not errors,errors
    result={'passed':True,'default_off':True,'grouping_and_failover_saved':True,'persisted_after_reload':True,'manual_mappings_saved':True,'desktop_mobile_layout':True,'page_errors':errors,'preview_url':base+'/admin/settings'}
    (root/'mediastorm-stations-browser-results.json').write_text(json.dumps(result,indent=2)+'\n')
    print(json.dumps(result))
    browser.close()
