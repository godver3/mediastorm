"""Exercise independent starting interests against an isolated synthetic demo app."""
import argparse
import json
import stat
import urllib.parse
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
    profile = c["secondProfileId"]
    root = "/users/" + profile
    setup_path = root + "/taste-onboarding"
    title_id = "tvdb:movie:1831"
    rating_path = root + "/ratings/movie/" + urllib.parse.quote(title_id, safe="")
    coordinate = {"titleId": title_id, "mediaType": "movie"}
    checks = []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=args.chromium, headless=True)
        page = browser.new_page()
        page.goto(fixture.base + "/watch/", wait_until="domcontentloaded")

        def api(method, route, body=None, expected=200, unlock=None, client="synthetic-application-suite"):
            result = page.evaluate("""async data => {
                const headers={'Content-Type':'application/json','Authorization':'Bearer '+data.token,'X-Client-ID':data.client};
                if(data.unlock) headers['X-Profile-Unlock']=data.unlock;
                const r=await fetch('/api'+data.path,{method:data.method,headers,body:data.body===null?undefined:JSON.stringify(data.body)});
                return {status:r.status,cache:r.headers.get('Cache-Control'),value:r.ok?await r.json():null};
            }""", {"token": fixture.token, "unlock": unlock, "client": client, "method": method, "path": route, "body": body})
            check(result["status"] == expected, f"Expected {expected}, got {result['status']} at {safe_path(route)}")
            return result

        # Verify all fixture and scope boundaries before the first mutation.
        initial = api("GET", setup_path)["value"]
        check(initial["status"] == "new" and not initial["titles"], "An unused synthetic setup profile is required")
        api("PUT", "/users/" + c["foreignProfileId"] + "/taste-onboarding", {"action": "skip"}, expected=404)
        api("PUT", "/users/" + c["profileId"] + "/taste-onboarding", {"action": "skip"}, expected=403)
        candidates = api("GET", setup_path + "/candidates?kind=movie")["value"]["items"]
        check(any(item["title"]["id"] == title_id for item in candidates), "Synthetic candidate missing")
        api("PUT", setup_path, {"action": "save", "titles": [coordinate]}, expected=409, client="synthetic-undelivered-client")
        original_rating = api("GET", rating_path)["value"]["rating"]
        snapshots = {route: api("GET", root + route)["value"] for route in ["/history/watched", "/history/progress", "/watchlist", "/hidden-items", "/settings"]}
        other_path = "/users/" + c["profileId"] + "/taste-onboarding"
        other_before = api("GET", other_path, unlock=fixture.unlock)["value"]
        checks.append("foreign-pin-and-undelivered-client-boundaries")
        try:
            api("PUT", rating_path, {"rating": "dislike"})
            skipped = api("PUT", setup_path, {"action": "skip"})
            check(skipped["value"]["status"] == "skipped" and not skipped["value"]["titles"], "Skip created interests")
            check(api("GET", setup_path, client="synthetic-second-device")["value"]["status"] == "skipped", "Skip did not persist for another device")
            checks.append("skip-persists-without-creating-preferences")
            before_revision = api("GET", root + "/home/manifest")["value"]["recommendationsRevision"]
            saved = api("PUT", setup_path, {"action": "save", "titles": [coordinate]})
            check(saved["cache"] == "no-store" and saved["value"]["status"] == "completed" and saved["value"]["source"] == "manual" and len(saved["value"]["titles"]) == 1, "Manual selection did not save")
            check(api("GET", root + "/home/manifest")["value"]["recommendationsRevision"] != before_revision, "Home revision did not follow starting interests")
            reopened = api("GET", setup_path, client="synthetic-second-device")["value"]
            check(len(reopened["titles"]) == 1 and reopened["titles"][0]["id"] == title_id, "Selection did not reopen on another device")
            check(api("GET", rating_path)["value"]["rating"] == "dislike", "Selection changed an explicit rating")
            check(api("GET", other_path, unlock=fixture.unlock)["value"] == other_before, "Starting interests crossed profiles")
            checks.append("manual-selection-reopen-and-independent-rating")
            api("PUT", setup_path, {"action": "save", "titles": [{**coordinate, "name": "forged"}]}, expected=400)
            check(not saved["value"]["canUseHistory"], "Unused synthetic profile unexpectedly has history")
            api("PUT", setup_path, {"action": "history"}, expected=409)
            check(len(api("GET", setup_path)["value"]["titles"]) == 1, "Unavailable history erased existing choices")
            checks.append("forged-metadata-and-empty-history-cannot-change-selection")
            skipped = api("PUT", setup_path, {"action": "skip"})["value"]
            check(skipped["status"] == "skipped" and len(skipped["titles"]) == 1, "Skip erased earlier interests")
            cleared = api("PUT", setup_path, {"action": "save", "titles": []})["value"]
            check(cleared["status"] == "completed" and not cleared["titles"], "Empty edit did not clear interests")
            check(api("GET", rating_path)["value"]["rating"] == "dislike", "Clearing interests deleted a rating")
            check(api("DELETE", setup_path)["value"]["status"] == "new", "Reset did not remove setup data")
            checks.append("skip-preserves-and-clear-reset-are-independent")
            for route, before in snapshots.items():
                check(api("GET", root + route)["value"] == before, "Setup changed history, progress, lists or settings")
            checks.append("history-progress-lists-hidden-settings-unchanged")
        finally:
            api("PUT", rating_path, {"rating": original_rating})
            api("DELETE", setup_path)
        browser.close()
    result = {"passed": True, "checks": checks, "synthetic_unused_profile_guard": True, "real_application_api_through_chromium": True, "native_ui_rendered": False, "private_values_in_artifact": False}
    folder = Path(args.artifacts)
    folder.mkdir(parents=True, exist_ok=True)
    (folder / "mediastorm-taste-application-results.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps({"passed": True, "checks": len(checks)}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--credentials-file", required=True)
    parser.add_argument("--chromium", required=True)
    parser.add_argument("--artifacts", required=True)
    args = parser.parse_args()
    try:
        run(args)
    except Exception as error:
        raise SystemExit("Synthetic taste application check failed: " + (str(error) if isinstance(error, SyntheticCheckFailed) else type(error).__name__)) from None
