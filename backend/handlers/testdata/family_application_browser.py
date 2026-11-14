"""Exercise a separately started, isolated demo application with synthetic media."""
import argparse
import ipaddress
import json
import os
import re
import stat
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

from playwright.sync_api import TimeoutError, sync_playwright


def safe_path(path):
    path = urllib.parse.urlsplit(path).path
    return re.sub(r"/(users|clients)/[^/]+", r"/\1/{fixture}", path)


def require(condition, message):
    if not condition:
        raise ValueError(message)


class Fixture:
    def __init__(self, base, credentials):
        parsed = urllib.parse.urlsplit(base)
        require(parsed.scheme == "http" and not parsed.username and not parsed.password, "A credential-free local HTTP origin is required")
        require(not parsed.query and not parsed.fragment and parsed.path in ("", "/"), "A plain local origin is required")
        require(parsed.hostname == "localhost" or ipaddress.ip_address(parsed.hostname or "").is_loopback, "A loopback server is required")
        require(credentials.get("fixture") == "mediastorm-synthetic-family-v1", "A synthetic fixture marker is required")
        require(credentials["username"].startswith("synthetic-family-"), "A synthetic account is required")
        self.base, self.credentials = base.rstrip("/"), credentials
        self.token = self.call("POST", "/auth/login", {
            "username": credentials["username"], "password": credentials["password"],
        })["token"]
        require(self.call("GET", "/settings", token=self.token)["demoMode"] is True, "The isolated server must run in demo mode")
        users = self.call("GET", "/users", token=self.token)
        require(any(u["id"] == credentials["profileId"] and u["name"] == "Synthetic Alpha" for u in users), "The synthetic PIN profile must be owned")
        require(any(u["id"] == credentials["secondProfileId"] and u["name"] == "Synthetic No PIN" for u in users), "The second synthetic profile must be owned")
        self.unlock = self.call("POST", "/users/" + credentials["profileId"] + "/pin/verify",
                                {"pin": credentials["pin"]}, token=self.token)["unlockToken"]
        self.progress_path = "/users/" + credentials["profileId"] + "/history/progress/movie/tvdb%3Amovie%3A1831"

    def call(self, method, path, body=None, token=None, unlock=None, expected=200):
        headers = {"Content-Type": "application/json", "X-Client-ID": "synthetic-application-suite"}
        if token:
            headers["Authorization"] = "Bearer " + token
        if unlock:
            headers["X-Profile-Unlock"] = unlock
        request = urllib.request.Request(self.base + "/api" + path,
                                         data=json.dumps(body).encode() if body is not None else None,
                                         headers=headers, method=method)
        try:
            response = urllib.request.urlopen(request, timeout=15)
        except urllib.error.HTTPError as error:
            if error.code == expected:
                return None
            raise RuntimeError(f"{method} {safe_path(path)} returned {error.code}") from None
        with response:
            require(response.status == expected, "Unexpected synthetic API status")
            raw = response.read()
            return json.loads(raw) if raw else None

    def reset_progress(self):
        self.call("DELETE", self.progress_path, token=self.token, unlock=self.unlock, expected=204)

    def progress(self):
        return self.call("GET", self.progress_path, token=self.token, unlock=self.unlock)

    def verify_profile_boundaries(self):
        c = self.credentials
        self.call("GET", "/users/" + c["foreignProfileId"] + "/settings", token=self.token, expected=404)
        self.call("GET", "/users/" + c["secondProfileId"] + "/history/progress/movie/tvdb%3Amovie%3A1831",
                  token=self.token, expected=404)
        self.call("POST", "/users/" + c["profileId"] + "/source-availability/recheck",
                  {"titleId": "tvdb:movie:1831", "mediaType": "movie"}, token=self.token, expected=403)


def run(args):
    path = Path(args.credentials_file)
    require(stat.S_IMODE(path.stat().st_mode) & 0o077 == 0, "Fixture credentials must be private")
    fixture = Fixture(args.base_url, json.loads(path.read_text()))
    fixture.verify_profile_boundaries()
    fixture.reset_progress()
    artifacts = Path(args.artifacts)
    artifacts.mkdir(parents=True, exist_ok=True)
    credentials = fixture.credentials
    step = "open"
    failures, errors = [], []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=args.chromium, headless=True)
        context = browser.new_context(viewport={"width": 1280, "height": 900})
        page = context.new_page()
        page.on("response", lambda r: failures.append({"path": safe_path(r.url), "status": r.status}) if r.status >= 400 else None)
        page.on("pageerror", lambda error: errors.append(type(error).__name__))
        try:
            page.goto(fixture.base + "/watch/", wait_until="domcontentloaded")
            step = "login"
            page.get_by_placeholder("Enter account").fill(credentials["username"])
            page.get_by_placeholder("Enter password").fill(credentials["password"])
            page.get_by_text("Sign In", exact=True).click()
            step = "profile"
            page.get_by_text("Synthetic Alpha", exact=True).wait_for()
            page.screenshot(path=str(artifacts / "mediastorm-family-app-profiles.png"))
            page.get_by_text("Synthetic Alpha", exact=True).click()
            page.get_by_text("Enter PIN", exact=True).wait_for()
            step = "pin"
            for digit in credentials["pin"]:
                page.get_by_text(digit, exact=True).click()
            page.get_by_text("Watch Something", exact=True).wait_for()
            for title, choice in [("Version Mismatch", "Dismiss"), ("Name this device", "Set later")]:
                try:
                    page.get_by_text(title, exact=True).wait_for(timeout=5000)
                    page.get_by_text(choice, exact=True).click()
                except TimeoutError:
                    pass
            step = "details"
            card = page.get_by_text("Night of the Living Dead", exact=True).first
            card.locator('xpath=ancestor::*[@tabindex="0"][1]').click()
            page.get_by_text("Play", exact=True).first.wait_for(timeout=15000)
            step = "play"
            started = time.perf_counter()
            page.get_by_text("Play", exact=True).first.click()
            media, player = [], None
            deadline = time.monotonic() + 25
            while time.monotonic() < deadline and not media:
                for window in context.pages:
                    for frame in window.frames:
                        try:
                            values = frame.locator("video").evaluate_all("items=>items.map(v=>({ready:v.readyState,time:v.currentTime,width:v.videoWidth,height:v.videoHeight,decoded:v.webkitDecodedFrameCount||0,paused:v.paused,error:v.error?.code||null}))")
                        except Exception:
                            continue
                        if any(v["decoded"] > 0 and v["width"] > 0 and v["time"] > 0 for v in values):
                            media, player = values, window
                            break
                    if media:
                        break
                if not media:
                    page.wait_for_timeout(100)
            require(media and player is not None, "No generated-media frame decoded")
            elapsed = time.perf_counter() - started
            player.screenshot(path=str(artifacts / "mediastorm-family-app-desktop.png"))
            step = "report-cancel"
            source = player.locator("video").evaluate("v=>v.currentSrc")
            player.locator("#releaseReportButton").click()
            player.locator("#releaseReportDialog").wait_for(state="visible")
            player.screenshot(path=str(artifacts / "mediastorm-family-app-report.png"))
            player.locator("#releaseReportDialog").get_by_role("button", name="Cancel", exact=True).click()
            player.locator("#releaseReportDialog").wait_for(state="hidden")
            require(player.locator("video").evaluate("v=>v.currentSrc") == source, "Cancel changed the active source")
            step = "pause-seek-resume"
            player.locator("#playButton").click()
            player.wait_for_function('document.querySelector("video").paused===true')
            slider = player.locator("#seek")
            bounds = slider.bounding_box()
            slider.click(position={"x": bounds["width"] / 3, "y": bounds["height"] / 2})
            player.wait_for_function('Number(document.querySelector("#seek").value)>=25')
            player.locator("#playButton").click()
            player.wait_for_function('document.querySelector("video").paused===false&&document.querySelector("video").readyState>=2')
            player.set_viewport_size({"width": 390, "height": 844})
            require(player.evaluate("document.documentElement.scrollWidth<=innerWidth"), "Mobile playback overflows horizontally")
            player.screenshot(path=str(artifacts / "mediastorm-family-app-mobile.png"))
            step = "persisted-progress"
            deadline = time.monotonic() + 15
            progress = fixture.progress()
            while progress.get("position", 0) < 25 and time.monotonic() < deadline:
                player.wait_for_timeout(250)
                progress = fixture.progress()
            require(progress.get("position", 0) >= 25, "The seek position was not persisted")
            fixture.verify_profile_boundaries()
            require(not errors, "The web client raised a page exception")
            known_paths = {"/api/discover/top-ten", "/api/metadata/movies/details"}
            unexpected = [r for r in failures if r["status"] >= 500 and r["path"] not in known_paths]
            require(not unexpected, "Unexpected server errors")
            result = {"passed": True, "actual_full_application": True,
                      "synthetic_accounts_and_generated_media_only": True,
                      "account_login_profile_selection_pin": True, "foreign_profile_404": True,
                      "locked_recheck_403": True, "second_profile_progress_isolated": True,
                      "click_to_decoded_frame_seconds": elapsed, "decoded_media": media,
                      "report_cancel_keeps_source": True, "pause_seek_resume_controls": True,
                      "persisted_progress_seconds": progress["position"],
                      "desktop_mobile_web_rendering": True, "page_errors": errors,
                      "known_demo_metadata_and_netflix_requests": [r for r in failures if r["status"] >= 500 and r["path"] in known_paths],
                      "native_device_acceptance": False, "usenet_performance_acceptance": False}
            (artifacts / "mediastorm-family-app-results.json").write_text(json.dumps(result, indent=2) + "\n")
            print(json.dumps(result))
        except Exception as error:
            page.screenshot(path=str(artifacts / "mediastorm-family-app-error.png"))
            frames = []
            for window in context.pages:
                for frame in window.frames:
                    try:
                        videos = frame.locator("video").evaluate_all("items=>items.map(v=>({ready:v.readyState,time:v.currentTime,width:v.videoWidth,decoded:v.webkitDecodedFrameCount||0,paused:v.paused,error:v.error?.code||null}))")
                        frames.append({"path": safe_path(frame.url), "videos": videos})
                    except Exception:
                        pass
            diagnostic = {"failed_step": step, "error_type": type(error).__name__, "frames": frames, "failed_requests": failures, "page_errors": errors}
            (artifacts / "mediastorm-family-app-error.json").write_text(json.dumps(diagnostic, indent=2) + "\n")
            raise RuntimeError(f"Application browser step {step} failed ({type(error).__name__})") from None
        finally:
            browser.close()


if __name__ == "__main__":
    os.umask(0o077)
    parser = argparse.ArgumentParser()
    for argument in ("base-url", "credentials-file", "chromium", "artifacts"):
        parser.add_argument("--" + argument, required=True)
    run(parser.parse_args())
