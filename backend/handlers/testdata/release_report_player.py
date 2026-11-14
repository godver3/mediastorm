import argparse
import json
from pathlib import Path
from playwright.sync_api import sync_playwright

parser = argparse.ArgumentParser(description="Exercise source-bound release reporting with synthetic media and an isolated player transport.")
parser.add_argument("--base-url", required=True)
parser.add_argument("--chromium", required=True)
parser.add_argument("--artifacts", required=True)
args = parser.parse_args()
base = args.base_url.rstrip("/")
root = Path(args.artifacts)

with sync_playwright() as p:
    browser = p.chromium.launch(executable_path=args.chromium, headless=True)
    page = browser.new_page(viewport={"width": 1440, "height": 1000})
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    info = page.request.get(base + "/fixture/info").json()
    assert info.get("fixture") == "release-report-player", "Refusing to modify a non-fixture server"
    # Exercise the real player controls and backend selection. Media decoding is
    # covered separately; this adapter records the player handoff deterministically.
    page.add_init_script("""
      window.addEventListener('DOMContentLoaded', () => {
        startWebPlayerFromSource = async options => {
          window.fixtureHandoff = { path: sourcePath, audio: webPlayerSelectedAudioTrack, offset: options.startOffset || 0 };
          document.getElementById('videoHost').innerHTML = '<div style="height:280px;display:grid;place-items:center;background:#15202c;color:#a8c5ff">Synthetic player transport</div>';
        };
        currentAbsoluteTime = () => 42;
      });
    """)
    page.route("https://cdn.jsdelivr.net/**", lambda route: route.fulfill(status=200, body=""))
    page.goto(base + "/watch/playback.html?token=fixture-token&profileId=profile&title=Synthetic%20Movie&sourcePath=%2Fwebdav%2Fbad.mp4&prequeueId=" + info["prequeueId"])
    page.locator("#releaseReportButton").wait_for(state="visible")
    page.locator("#releaseReportButton").click()
    page.locator("#releaseReportReason").wait_for(state="visible")
    page.wait_for_function("releaseReportState !== null")
    page.locator("#releaseReportReason").select_option("missing_language")
    assert "Other languages remain eligible" in page.locator("#releaseReportScope").inner_text()
    page.locator("#releaseReportOnly").click()
    page.wait_for_function("!document.getElementById('releaseReportUndo').hidden")
    status = page.request.get(base + "/fixture/status").json()
    assert status["reports"] == 1 and status["germanExcluded"] and not status["englishExcluded"], status
    page.evaluate("document.body.insertAdjacentHTML('beforeend','<aside style=\"pointer-events:none;position:fixed;left:16px;bottom:16px;padding:10px;background:#15202c;color:white;border:1px solid #64748b;z-index:99999;font:13px system-ui\">LOCAL PREVIEW<br>Synthetic releases</aside>')")
    page.screenshot(path=str(root / "mediastorm-release-report-desktop.png"))
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1"), "Desktop overflow"
    page.set_viewport_size({"width": 390, "height": 844})
    page.screenshot(path=str(root / "mediastorm-release-report-mobile.png"))
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1"), "Mobile overflow"
    page.locator("#releaseReportUndo").click()
    page.wait_for_function("document.getElementById('releaseReportUndo').hidden")
    assert page.request.get(base + "/fixture/status").json()["reports"] == 0
    page.locator("#releaseManualRetry").click()
    page.wait_for_function("window.fixtureHandoff?.path === '/webdav/good.mp4'")
    assert page.request.get(base + "/fixture/status").json()["reports"] == 0, "Manual source change created a permanent report"
    assert page.evaluate("fixtureHandoff.offset") == 42
    page.locator("#releaseReportButton").click()
    page.wait_for_function("releaseReportState?.releaseName.includes('Good')")
    page.locator("#releaseReportReason").select_option("wrong_title")
    page.locator("#releaseReportRetry").click()
    page.wait_for_function("window.fixtureHandoff?.path === '/webdav/bad.mp4' && window.fixtureHandoff.offset === 0")
    assert not page.locator("#releaseReportDialog").is_visible()
    assert not errors, errors
    result = {"passed": True, "report_without_switch": True, "language_scoped": True, "english_unaffected": True, "undo": True, "manual_retry_not_persistent": True, "report_and_retry": True, "wrong_content_resets_position": True, "desktop_mobile_layout": True, "media_transport_stubbed": True, "page_errors": errors}
    (root / "mediastorm-release-report-browser-results.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))
    browser.close()
