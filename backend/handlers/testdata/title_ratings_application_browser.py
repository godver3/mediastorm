"""Run preference requests through Chromium against the isolated demo app.

This checks the real authenticated application API. Native Compose controls are
covered by their controller tests and APK builds, not rendered by this browser.
"""
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
    credentials_path = Path(args.credentials_file)
    check(stat.S_IMODE(credentials_path.stat().st_mode) & 0o077 == 0, "Fixture credentials must be private")
    fixture = Fixture(args.base_url, json.loads(credentials_path.read_text()))
    c = fixture.credentials
    # Use the owned non-playing fixture profile so ongoing playback saves cannot
    # invalidate the assertions about independent history and progress state.
    profile = c["secondProfileId"]
    title_id = "tvdb:movie:1831"
    rating_path = "/users/" + profile + "/ratings/movie/" + urllib.parse.quote(title_id, safe="")
    checks = []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=args.chromium, headless=True)
        page = browser.new_page()
        page.goto(fixture.base + "/watch/", wait_until="domcontentloaded")

        def api(method, path, body=None, expected=200, unlock=None, client="synthetic-application-suite"):
            result = page.evaluate("""async data => {
                const headers = {'Content-Type':'application/json','Authorization':'Bearer '+data.token,'X-Client-ID':data.client};
                if (data.unlock) headers['X-Profile-Unlock']=data.unlock;
                const response=await fetch('/api'+data.path,{method:data.method,headers,body:data.body===null?undefined:JSON.stringify(data.body)});
                return {status:response.status, cache:response.headers.get('Cache-Control'), value:response.ok?await response.json():null};
            }""", {"token": fixture.token, "unlock": unlock, "client": client, "method": method, "path": path, "body": body})
            check(result["status"] == expected, f"Unexpected synthetic rating API status: expected {expected}, got {result['status']} at {safe_path(path)}")
            return result

        def catalogue(profile_id, unlock=None):
            query = urllib.parse.urlencode({"type": "movie", "userId": profile_id, "limit": 20, "hideWatched": "false"})
            reply = api("GET", "/discover/new?" + query, unlock=unlock)
            titles = [item["title"] for item in reply["value"]["items"] if item["title"]["id"] == title_id]
            check(len(titles) == 1, "Synthetic demo title missing")
            return titles[0]

        # All boundaries run before the first preference write.
        api("PUT", "/users/" + c["foreignProfileId"] + "/ratings/movie/" + urllib.parse.quote(title_id, safe=""), {"rating": "love"}, expected=404)
        api("PUT", "/users/" + c["profileId"] + "/ratings/movie/" + urllib.parse.quote(title_id, safe=""), {"rating": "love"}, expected=403)
        catalogue(profile)
        api("PUT", rating_path, {"rating": "love"}, expected=409, client="synthetic-other-client")
        checks.append("foreign-profile-locked-profile-and-undelivered-client-rejected")
        original = api("GET", rating_path)["value"]["rating"]
        other_before = catalogue(c["profileId"], fixture.unlock).get("personalRating")
        snapshots = {path: api("GET", "/users/" + profile + path)["value"] for path in ["/watchlist", "/hidden-items", "/history/watched", "/history/progress"]}
        manifest_path = "/users/" + profile + "/home/manifest"
        try:
            api("PUT", rating_path, {"rating": "none"})
            previous_revision = api("GET", manifest_path)["value"]["recommendationsRevision"]
            for rating in ["like", "love", "dislike", "none"]:
                saved = api("PUT", rating_path, {"rating": rating})
                loaded = api("GET", rating_path)
                check(saved["value"]["rating"] == rating and loaded["value"]["rating"] == rating, "Preference did not round-trip")
                check(saved["cache"] == "no-store" and loaded["cache"] == "no-store", "Personal response is cacheable")
                title = catalogue(profile)
                check(title.get("personalRating", "none") == rating, "Catalogue preference annotation is stale")
                check(catalogue(c["profileId"], fixture.unlock).get("personalRating") == other_before, "Preference crossed profile boundary")
                revision = api("GET", manifest_path)["value"]["recommendationsRevision"]
                check(revision != previous_revision, "Home preference revision did not change")
                previous_revision = revision
                checks.append("round-trip-and-profile-annotation-" + rating)
            api("PUT", rating_path, {"rating": "like", "name": "forged"}, expected=400)
            api("PUT", rating_path, {"rating": "watched"}, expected=400)
            check(api("GET", rating_path)["value"]["rating"] == "none", "Rejected input changed the preference")
            checks.append("forged-metadata-and-unsupported-rating-rejected")
            for path, before in snapshots.items():
                check(api("GET", "/users/" + profile + path)["value"] == before, "Rating changed an independent profile feature")
            checks.append("watched-progress-watchlist-hidden-state-unchanged")
        finally:
            api("PUT", rating_path, {"rating": original})
        browser.close()
    result = {"passed": True, "checks": checks, "fixture_only": True, "real_application_api_through_chromium": True, "native_ui_rendered": False, "private_values_in_artifact": False}
    artifacts = Path(args.artifacts)
    artifacts.mkdir(parents=True, exist_ok=True)
    (artifacts / "mediastorm-title-ratings-application-results.json").write_text(json.dumps(result, indent=2) + "\n")
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
        # Never print arguments, credentials, response bodies or traceback locals.
        raise SystemExit("Synthetic rating application check failed: " + (str(error) if isinstance(error,SyntheticCheckFailed) else type(error).__name__)) from None
