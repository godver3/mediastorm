import argparse
import json
from pathlib import Path
from playwright.sync_api import sync_playwright

parser = argparse.ArgumentParser(description="Exercise local playback preferences against a synthetic settings fixture.")
parser.add_argument("--base-url", required=True)
parser.add_argument("--chromium", required=True)
parser.add_argument("--artifacts", required=True)
args = parser.parse_args()
base = args.base_url.rstrip("/")
artifacts = Path(args.artifacts)

with sync_playwright() as p:
    browser = p.chromium.launch(executable_path=args.chromium, headless=True)
    page = browser.new_page(viewport={"width": 1440, "height": 1000})
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    info = page.request.get(base + "/fixture/info").json()
    assert info.get("fixture") == "local-preference-settings", "Refusing to modify a non-fixture server"
    first, second = info["first"], info["second"]
    initial = page.request.get(base + "/account/api/user-settings?profileId=" + first)
    assert initial.ok, initial.text()
    page.goto(base + "/account/settings?profileId=" + first)
    control = page.locator("#preferLocalFiles")
    control.wait_for(state="attached")
    assert not control.is_checked(), "Local-first must be off by default"
    control.locator("..").click()
    with page.expect_response(lambda r: "/account/api/user-settings" in r.url and r.request.method == "PUT") as response:
        page.evaluate("saveSettings()")
    assert response.value.status == 200, response.value.text()
    assert response.value.json()["playback"]["preferredAudioLanguage"] == "deu", "Saving source preference reset audio language"
    page.reload()
    control.wait_for(state="attached")
    assert control.is_checked(), "Preference was lost after reloading"
    page.locator(f'[data-profile-id="{second}"]').click()
    page.wait_for_function("currentProfileId === " + json.dumps(second) + " && document.querySelector('#preferLocalFiles')?.checked === false")
    assert not page.request.get(base + "/account/api/user-settings?profileId=" + second).json()["playback"]["preferLocalFiles"], "Preference leaked to another profile"
    page.locator(f'[data-profile-id="{first}"]').click()
    page.wait_for_function("currentProfileId === " + json.dumps(first) + " && document.querySelector('#preferLocalFiles')?.checked === true")
    page.locator("#preferLocalFiles").locator("..").scroll_into_view_if_needed()
    page.evaluate("document.body.insertAdjacentHTML('beforeend','<aside style=\"position:fixed;right:16px;top:16px;background:#15202c;color:white;border:1px solid #64748b;padding:10px;z-index:99999;font:13px system-ui\">LOCAL PREVIEW<br>Synthetic profiles</aside>')")
    page.screenshot(path=str(artifacts / "mediastorm-local-preference-desktop.png"))
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1"), "Desktop overflow"
    page.set_viewport_size({"width": 390, "height": 844})
    page.locator("#preferLocalFiles").locator("..").scroll_into_view_if_needed()
    page.screenshot(path=str(artifacts / "mediastorm-local-preference-mobile.png"))
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1"), "Mobile overflow"

    page.set_viewport_size({"width": 1440, "height": 1000})
    page.goto(base + "/admin/settings?category=playback")
    page.wait_for_function("typeof selectSettingsScope === 'function'")
    if page.locator("#adminWalkthroughSkip").is_visible():
        page.locator("#adminWalkthroughSkip").click()
    page.wait_for_function("Array.from(document.querySelector('#userSelector')?.options || []).some(option => option.value === " + json.dumps(first) + ")")
    page.evaluate("selectSettingsScope('person', " + json.dumps(first) + ")")
    page.wait_for_function("selectedUserId === " + json.dumps(first) + " && document.querySelector('#playback_preferLocalFiles')?.checked === true")
    page.evaluate("setSettingsGroup('playback'); openSettingsSection('playback', {focus:false})")
    admin_control = page.locator("#playback_preferLocalFiles")
    admin_control.wait_for(state="attached")
    assert admin_control.is_checked(), "Admin settings did not read the stored profile preference"
    behavior = page.locator('details[data-settings-disclosure="playback.behavior"]')
    if not behavior.evaluate("element => element.open"):
        behavior.locator("summary").click()
    admin_control.locator("..").wait_for(state="visible")
    admin_control.locator("..").click()
    with page.expect_response(lambda r: "/admin/api/user-settings" in r.url and r.request.method == "PUT") as response:
        page.evaluate("saveAllSettings()")
    assert response.value.status == 200, response.value.text()
    assert not page.request.get(base + "/account/api/user-settings?profileId=" + first).json()["playback"]["preferLocalFiles"], "Admin update did not reach profile playback"
    assert not errors, errors
    result = {"passed": True, "default_search_first": True, "profile_preference_saved": True, "persists_after_reload": True, "profile_isolation": True, "admin_profile_control_saved": True, "desktop_mobile_layout": True, "page_errors": errors}
    (artifacts / "mediastorm-local-preference-browser-results.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))
    browser.close()
