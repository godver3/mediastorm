"""Read the existing Love preferences through an isolated real application."""
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
    root = "/users/" + c["secondProfileId"]
    loved_path = root + "/display-list?source=loved"
    title_id = "tvdb:movie:1831"
    rating_path = root + "/ratings/movie/" + urllib.parse.quote(title_id, safe="")
    checks = []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=args.chromium, headless=True)
        page = browser.new_page()
        page.goto(fixture.base + "/watch/", wait_until="domcontentloaded")

        def api(method, route, body=None, expected=200, client="synthetic-loved-suite"):
            result = page.evaluate("""async data => {
                const headers={'Content-Type':'application/json','Authorization':'Bearer '+data.token,'X-Client-ID':data.client};
                const r=await fetch('/api'+data.path,{method:data.method,headers,body:data.body===null?undefined:JSON.stringify(data.body)});
                return {status:r.status,cache:r.headers.get('Cache-Control'),value:r.ok?await r.json():null};
            }""", {"token": fixture.token, "client": client, "method": method, "path": route, "body": body})
            check(result["status"] == expected, f"Expected {expected}, got {result['status']} at {safe_path(route)}")
            return result

        # All fixture accounts and profile ownership are verified before mutation.
        api("GET", "/users/" + c["foreignProfileId"] + "/display-list?source=loved", expected=404)
        api("GET", "/users/" + c["profileId"] + "/display-list?source=loved", expected=403)
        api("GET", loved_path + "&userId=" + c["foreignProfileId"], expected=404)
        checks.append("foreign-pin-and-conflicting-profile-selectors-rejected")
        candidates = api("GET", root + "/taste-onboarding/candidates?kind=movie")["value"]["items"]
        check(any(item["title"]["id"] == title_id for item in candidates), "Synthetic catalogue title missing")
        original_rating = api("GET", rating_path)["value"]["rating"]
        snapshots = {route: api("GET", root + route)["value"] for route in ["/history/watched", "/history/progress", "/watchlist", "/hidden-items", "/settings", "/taste-onboarding"]}

        def contains(result):
            return any(item["title"]["id"] == title_id for item in result["value"]["items"])

        try:
            api("PUT", rating_path, {"rating": "like"})
            check(not contains(api("GET", loved_path)), "Like was incorrectly included in Loved Titles")
            api("PUT", rating_path, {"rating": "love"})
            loved = api("GET", loved_path)
            check(contains(loved), "Love was not reflected in the existing preference view")
            check("no-store" in loved["cache"] and "private" in loved["cache"], "Private response caching boundary was lost")
            selected = next(item["title"] for item in loved["value"]["items"] if item["title"]["id"] == title_id)
            check(selected["personalRating"] == "love", "Favorite annotation missing")
            checks.append("love-view-uses-existing-rating-and-private-cache-boundary")
            api("GET", loved_path, client="synthetic-loved-second-device")
            check(api("GET", rating_path, client="synthetic-loved-second-device")["value"]["rating"] == "love", "Returned favorite was not editable on another device")
            checks.append("owned-favorite-delivery-works-on-another-device")
            check(not contains(api("GET", loved_path + "&filterMediaType=series")), "Movie crossed the series filter")
            check(not api("GET", loved_path + "&offset=500&limit=1")["value"]["items"], "List cap was bypassed")
            checks.append("kind-filter-and-bounded-paging")
            for rating in ["dislike", "none"]:
                api("PUT", rating_path, {"rating": rating})
                check(not contains(api("GET", loved_path)), "Removed or negative preference remained a favorite")
            checks.append("negative-and-cleared-preferences-leave-the-loved-view")
            for route, before in snapshots.items():
                check(api("GET", root + route)["value"] == before, "Favorite view changed another profile dataset")
            checks.append("history-progress-watchlist-hidden-settings-and-starting-interests-unchanged")
        finally:
            api("PUT", rating_path, {"rating": original_rating})
        browser.close()
    result = {"passed": True, "checks": checks, "real_application_api_through_chromium": True, "existing_preference_dataset_only": True, "native_ui_rendered": False, "private_values_in_artifact": False}
    folder = Path(args.artifacts)
    folder.mkdir(parents=True, exist_ok=True)
    (folder / "mediastorm-loved-application-results.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps({"passed": True, "checks": len(checks)}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    for argument in ["base-url", "credentials-file", "chromium", "artifacts"]:
        parser.add_argument("--" + argument, required=True)
    try:
        run(parser.parse_args())
    except Exception as error:
        raise SystemExit("Synthetic loved-title application check failed: " + (str(error) if isinstance(error, SyntheticCheckFailed) else type(error).__name__)) from None
