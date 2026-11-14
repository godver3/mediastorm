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
          if (window.fixtureFailStart) throw new Error('Synthetic transport failure');
          window.fixtureHandoff = { path: sourcePath, audio: webPlayerSelectedAudioTrack, offset: options.startOffset || 0 };
          document.getElementById('videoHost').innerHTML = '<div style="height:280px;display:grid;place-items:center;background:#15202c;color:#a8c5ff">Synthetic player transport</div>';
        };
        window.fixturePosition = 42;
        currentAbsoluteTime = () => window.fixturePosition;
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

    page.locator("#releaseReportUndo").click()
    page.wait_for_function("document.getElementById('releaseReportUndo').hidden")
    assert page.request.get(base + "/fixture/status").json()["reports"] == 0
    page.locator("#releaseManualRetry").click()
    page.wait_for_function("Boolean(releaseReportAction?.ready) && !releaseReportBusy")
    assert page.evaluate("fixtureHandoff.path") == "/webdav/bad.mp4", "Preparation changed the source before confirmation"
    assert page.locator("#releaseReplacementResume").input_value() == "rewind"
    assert page.request.get(base + "/fixture/status").json()["reports"] == 0
    page.evaluate("document.body.insertAdjacentHTML('beforeend','<aside style=\"pointer-events:none;position:fixed;left:16px;bottom:16px;padding:10px;background:#15202c;color:white;border:1px solid #64748b;z-index:99999;font:13px system-ui\">LOCAL PREVIEW<br>Synthetic releases</aside>')")
    page.screenshot(path=str(root / "mediastorm-web-prepared-desktop.png"))
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1"), "Desktop overflow"
    page.set_viewport_size({"width": 390, "height": 844})
    page.screenshot(path=str(root / "mediastorm-web-prepared-mobile.png"))
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1"), "Mobile overflow"
    page.evaluate("window.fixturePosition = 95")
    page.locator("#releaseReplacementResume").select_option("current")
    page.locator("#releaseReplacementStart").click()
    page.wait_for_function("fixtureHandoff.path === '/webdav/good.mp4' && fixtureHandoff.offset === 95")
    assert not page.locator("#releaseReportDialog").is_visible()

    page.locator("#releaseReportButton").click()
    page.wait_for_function("releaseReportState?.releaseName.includes('Good')")
    page.locator("#releaseReportReason").select_option("wrong_title")
    page.locator("#releaseReportRetry").click()
    page.wait_for_function("Boolean(releaseReportAction?.ready) && !releaseReportBusy")
    assert page.evaluate("fixtureHandoff.path") == "/webdav/good.mp4"
    assert page.locator("#releaseReplacementResume").is_disabled()
    page.locator("#releaseReplacementStart").click()
    page.wait_for_function("fixtureHandoff.path === '/webdav/bad.mp4' && fixtureHandoff.offset === 0")

    page.locator("#releaseReportButton").click()
    page.wait_for_function("releaseReportState?.releaseName.includes('Bad')")
    page.locator("#releaseReportUndo").click()
    page.wait_for_function("document.getElementById('releaseReportUndo').hidden")
    assert page.request.get(base + "/fixture/status").json()["reports"] == 0
    page.locator("#releaseManualRetry").click()
    page.wait_for_function("Boolean(releaseReportAction?.ready) && !releaseReportBusy")
    page.evaluate("window.fixturePosition = 5")
    page.locator("#releaseReplacementStart").click()
    page.wait_for_function("fixtureHandoff.path === '/webdav/good.mp4' && fixtureHandoff.offset === 0")

    page.locator("#releaseReportButton").click()
    page.wait_for_function("releaseReportState?.releaseName.includes('Good')")
    page.locator("#releaseManualRetry").click()
    page.wait_for_function("Boolean(releaseReportAction?.ready) && !releaseReportBusy")
    page.locator("#releaseReplacementResume").select_option("beginning")
    page.evaluate("window.fixturePosition = 222")
    page.locator("#releaseReplacementStart").click()
    page.wait_for_function("fixtureHandoff.path === '/webdav/bad.mp4' && fixtureHandoff.offset === 0")

    page.locator("#releaseReportButton").click()
    page.wait_for_function("releaseReportState?.releaseName.includes('Bad')")
    page.locator("#releaseManualRetry").click()
    page.wait_for_function("Boolean(releaseReportAction?.ready) && !releaseReportBusy")
    canceled = page.evaluate("releaseReportAction.replacementId")
    page.locator('#releaseReportDialog button[onclick="closeReleaseReport()"]').click()
    page.wait_for_function("!document.getElementById('releaseReportDialog').open")
    assert page.evaluate("fixtureHandoff.path") == "/webdav/bad.mp4"
    page.wait_for_function("""async id => (await fetch('/api/playback/prequeue/' + encodeURIComponent(id))).status === 404""", arg=canceled)

    # Delay a real retry response after the backend has created its replacement.
    # The second request deduplicates to the same entry; the old response must
    # not delete the preparation now owned by the newer dialog.
    page.evaluate("""() => {
      window.fixtureOriginalApiCall = apiCall;
      window.fixtureHoldRetry = true;
      window.fixtureDeleteRequests = [];
      apiCall = async (path, options = {}) => {
        if (options.method === 'DELETE') window.fixtureDeleteRequests.push(path);
        const response = await window.fixtureOriginalApiCall(path, options);
        if (options.method === 'POST' && path.endsWith('/retry') && window.fixtureHoldRetry) {
          window.fixtureHoldRetry = false;
          window.fixtureSharedId = response.replacement.prequeueId;
          await new Promise(resolve => { window.fixtureReleaseRetry = resolve; });
          window.fixtureDelayedFinished = true;
        }
        return response;
      };
    }""")
    page.locator("#releaseReportButton").click()
    page.wait_for_function("releaseReportState?.releaseName.includes('Bad')")
    page.locator("#releaseManualRetry").click()
    page.wait_for_function("typeof window.fixtureReleaseRetry === 'function'")
    page.locator('#releaseReportDialog button[onclick="closeReleaseReport()"]').click()
    page.locator("#releaseReportButton").click()
    page.wait_for_function("releaseReportState?.releaseName.includes('Bad')")
    page.locator("#releaseManualRetry").click()
    page.wait_for_function("Boolean(releaseReportAction?.ready) && !releaseReportBusy")
    assert page.evaluate("releaseReportAction.replacementId === window.fixtureSharedId"), "Backend did not exercise replacement deduplication"
    page.evaluate("window.fixtureReleaseRetry()")
    page.wait_for_function("window.fixtureDelayedFinished === true")
    assert page.evaluate("!window.fixtureDeleteRequests.includes('/playback/prequeue/' + window.fixtureSharedId + '/retry')"), "Late response deleted the newer preparation"
    page.evaluate("window.fixturePosition = 80")
    page.locator("#releaseReplacementStart").click()
    page.wait_for_function("fixtureHandoff.path === '/webdav/good.mp4' && fixtureHandoff.offset === 70")
    assert page.request.get(base + "/api/playback/prequeue/" + page.evaluate("webPlayerActivePrequeueId")).status == 200

    page.locator("#releaseReportButton").click()
    page.wait_for_function("releaseReportState?.releaseName.includes('Good')")
    page.locator("#releaseReportReason").select_option("missing_language")
    page.locator("#releaseReportRetry").click()
    page.wait_for_function("Boolean(releaseReportAction?.ready) && !releaseReportBusy")
    page.evaluate("window.fixtureFailStart = true")
    page.locator("#releaseReplacementStart").click()
    page.wait_for_function("document.getElementById('releaseReportError').textContent.includes('could not start')")
    assert page.locator("#releaseReportDialog").is_visible()
    assert page.locator("#releaseReportUndo").is_visible()
    page.locator("#releaseReportUndo").click()
    page.wait_for_function("document.getElementById('releaseReportUndo').hidden")
    assert page.request.get(base + "/fixture/status").json()["reports"] == 0
    assert not errors, errors
    result = {"passed": True, "report_without_switch": True, "language_scoped": True, "english_unaffected": True, "undo": True,
        "manual_retry_not_persistent": True, "preparation_does_not_switch": True, "explicit_adoption": True, "latest_position": True,
        "rewind_clamped_at_zero": True, "beginning_choice": True, "wrong_content_resets_position": True, "cancel_cleans_preparation": True,
        "startup_failure_visible": True, "late_shared_preparation_survives": True, "desktop_mobile_layout": True, "media_transport_stubbed": True, "page_errors": errors}
    (root / "mediastorm-web-prepared-browser-results.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))
    browser.close()
