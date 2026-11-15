"""Verify profile autoplay through an isolated real application."""
import argparse
import json
import stat
from pathlib import Path

from playwright.sync_api import sync_playwright
from family_application_browser import Fixture, safe_path


class SyntheticCheckFailed(Exception):
    pass


def check(condition, message):
    if not condition:
        raise SyntheticCheckFailed(message)


def run(args):
    path = Path(args.credentials_file)
    check(stat.S_IMODE(path.stat().st_mode) & 0o077 == 0, "Fixture credentials must be private")
    fixture = Fixture(args.base_url, json.loads(path.read_text()))
    c = fixture.credentials
    root = "/users/" + c["secondProfileId"]
    checks = []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=args.chromium, headless=True)
        page = browser.new_page()
        page.goto(fixture.base + "/watch/", wait_until="domcontentloaded")

        def api(method, route, body=None, expected=200, client="synthetic-autoplay-suite"):
            result = page.evaluate("""async data => {
                const headers={'Content-Type':'application/json','Authorization':'Bearer '+data.token,'X-Client-ID':data.client};
                const r=await fetch('/api'+data.path,{method:data.method,headers,body:data.body===null?undefined:JSON.stringify(data.body)});
                return {status:r.status,cache:r.headers.get('Cache-Control'),value:r.ok?await r.json():null};
            }""", {"token": fixture.token, "client": client, "method": method, "path": route, "body": body})
            check(result["status"] == expected, f"Expected {expected}, got {result['status']} at {safe_path(route)}")
            return result

        api("PATCH", "/users/" + c["foreignProfileId"] + "/settings/skipping", {"nextEpisode": False}, expected=404)
        api("PATCH", "/users/" + c["profileId"] + "/settings/skipping", {"nextEpisode": False}, expected=403)
        original = api("GET", root + "/settings")["value"]
        check(original["playback"].get("nextEpisodeAutoPlay") is not None, "Autoplay default missing")
        before = original["playback"]["nextEpisodeAutoPlay"]
        credits = original["playback"].get("creditsAutoSkip")
        try:
            for value in [False, True, False]:
                result = api("PATCH", root + "/settings/skipping", {"nextEpisode": value})
                check(result["value"]["nextEpisode"] is value, "Patch did not confirm choice")
                check("no-store" in result["cache"], "Profile response can be cached")
                current = api("GET", root + "/settings", client="synthetic-autoplay-second-device")["value"]
                check(current["playback"]["nextEpisodeAutoPlay"] is value, "Another device lost profile choice")
                check(current["playback"].get("creditsAutoSkip") == credits, "Autoplay changed credits preference")
            checks.append("profile-toggle-persists-on-another-device-independent-of-credits")
            api("PUT", root + "/settings", original)
            check(api("GET", root + "/settings")["value"]["playback"]["nextEpisodeAutoPlay"] is False, "Cached settings reset autoplay")
            checks.append("stale-full-settings-save-cannot-reset-autoplay")
            checks.append("foreign-and-pin-locked-profile-writes-rejected")
        finally:
            api("PATCH", root + "/settings/skipping", {"nextEpisode": before})
        browser.close()
    result = {"passed": True, "checks": checks, "real_application_api_through_chromium": True, "profile_and_credits_independent": True, "native_ui_rendered": False, "private_values_in_artifact": False}
    folder = Path(args.artifacts)
    folder.mkdir(parents=True, exist_ok=True)
    (folder / "mediastorm-autoplay-application-results.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps({"passed": True, "checks": len(checks)}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    for argument in ["base-url", "credentials-file", "chromium", "artifacts"]:
        parser.add_argument("--" + argument, required=True)
    try:
        run(parser.parse_args())
    except Exception as error:
        raise SystemExit("Synthetic autoplay application check failed: " + (str(error) if isinstance(error, SyntheticCheckFailed) else type(error).__name__)) from None
