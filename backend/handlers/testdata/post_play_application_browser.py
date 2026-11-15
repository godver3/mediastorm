"""Verify completion and existing preferences through an isolated real application."""
import argparse
import json
import stat
import traceback
import uuid
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
    client_id = "synthetic-post-play-" + uuid.uuid4().hex[:12]
    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=args.chromium, headless=True)
        page = browser.new_page()
        page.goto(fixture.base + "/watch/", wait_until="domcontentloaded")

        def api(method, route, body=None, expected=200, client=None):
            result = page.evaluate("""async data => {
                const headers={'Content-Type':'application/json','Authorization':'Bearer '+data.token,'X-Client-ID':data.client};
                const r=await fetch('/api'+data.path,{method:data.method,headers,body:data.body===null?undefined:JSON.stringify(data.body)});
                return {status:r.status,cache:r.headers.get('Cache-Control'),value:r.ok?await r.json():null};
            }""", {"token": fixture.token, "client": client or client_id, "method": method, "path": route, "body": body})
            check(result["status"] == expected, f"Expected {expected}, got {result['status']} at {safe_path(route)}")
            return result

        encoded = urllib.parse.quote(title_id, safe="")
        context_path = root + "/post-play/movie/" + encoded
        api("GET", "/users/" + c["foreignProfileId"] + "/post-play/movie/" + encoded, expected=404)
        api("GET", "/users/" + c["profileId"] + "/post-play/movie/" + encoded, expected=403)
        api("GET", context_path, expected=409, client=client_id+"-unseen")
        checks.append("foreign-pin-and-undelivered-client-rejected-before-mutation")
        details = root + "/details-shell?" + urllib.parse.urlencode({"type":"movie","titleId":title_id,"name":"Night of the Living Dead","imdbId":"tt0063350","tvdbId":1831})
        resolved = api("GET", details)["value"]["movieDetails"]
        # Demo fixtures have no external metadata credentials. When details are
        # unavailable, only a genuine server catalogue delivery can grant context.
        catalogue = "/discover/new?" + urllib.parse.urlencode({"type":"movie","userId":c["secondProfileId"],"hideWatched":"false","limit":20})
        if resolved is None:
            check(any(i["title"]["id"]==title_id for i in api("GET",catalogue)["value"]["items"]),"Synthetic server catalogue title missing")
        else:
            check(resolved["id"]==title_id,"Resolved server title changed")
        original_rating = api("GET", rating_path)["value"]["rating"]
        # Only this verified synthetic fixture's own completion is recorded.
        completed = api("POST",root+"/history/progress",{"mediaType":"movie","itemId":title_id,"movieName":"Night of the Living Dead","externalIds":{"imdb":"tt0063350","tvdb":"1831"},"duration":90,"position":90,"playbackEnded":True,"isPaused":True})["value"]
        canonical = completed["itemId"]
        target = context_path+"?"+urllib.parse.urlencode({"itemId":canonical})
        api("GET",context_path+"?itemId=synthetic-unrecorded",expected=409)
        snapshots = {route:api("GET",root+route)["value"] for route in ["/watchlist","/hidden-items","/history/watched","/history/progress","/taste-onboarding"]}
        try:
            for rating in ["none","like","love","dislike"]:
                api("PUT",rating_path,{"rating":rating})
                context = api("GET",target)
                check(context["value"]["rating"] == rating and context["value"]["completion"]=="movie", "Completion did not use confirmed personal rating")
                check(context["value"]["ratingScope"]=="movie" and context["value"]["title"]["id"]==title_id,"Completion title scope changed")
                check("private" in context["cache"] and "no-store" in context["cache"],"Private completion can be cached")
                suggestions=api("GET",context_path+"/recommendations?"+urllib.parse.urlencode({"itemId":canonical}))["value"]
                check(suggestions["rating"]==rating and suggestions["ratingScope"]=="movie","Suggestion generation used stale preference")
                check(len(suggestions["items"])<=12 and not any(i["title"]["id"]==title_id for i in suggestions["items"]),"Completed title suggested again")
                check(not any(i["title"].get("personalRating")=="dislike" for i in suggestions["items"]),"Negative preference remained a suggestion")
            checks.append("real-canonical-completion-existing-rating-and-negative-aware-suggestions")
            api("GET",target,expected=409,client=client_id+"-second")
            delivered=api("GET",details,client=client_id+"-second")["value"]["movieDetails"]
            if delivered is None:
                api("GET",catalogue,client=client_id+"-second")
            check(api("GET",target,client=client_id+"-second")["value"]["rating"]=="dislike","Second-device details delivery lost existing rating")
            checks.append("actual-catalogue-delivery-registers-client-scoped-completion")
            for route,before in snapshots.items():
                check(api("GET",root+route)["value"]==before,"Completion reads or rating changed an independent profile dataset")
            checks.append("completion-reads-and-ratings-preserve-other-profile-datasets")
        finally:
            api("PUT",rating_path,{"rating":original_rating})
        browser.close()
    result = {"passed": True, "checks": checks, "real_application_api_through_chromium": True, "existing_preference_dataset_only": True, "external_details_available": resolved is not None, "synthetic_completion_history_written": True, "native_ui_rendered": False, "private_values_in_artifact": False}
    folder = Path(args.artifacts)
    folder.mkdir(parents=True, exist_ok=True)
    (folder / "mediastorm-post-play-application-results.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps({"passed": True, "checks": len(checks)}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    for argument in ["base-url", "credentials-file", "chromium", "artifacts"]:
        parser.add_argument("--" + argument, required=True)
    try:
        run(parser.parse_args())
    except Exception as error:
        raise SystemExit("Synthetic post-play application check failed: " + (str(error) if isinstance(error, SyntheticCheckFailed) else type(error).__name__ + " at fixture line " + str(traceback.extract_tb(error.__traceback__)[-1].lineno))) from None
