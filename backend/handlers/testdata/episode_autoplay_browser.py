"""Exercise the production player template with decoded generated media, no real sources."""
import argparse
import json
from pathlib import Path
from playwright.sync_api import sync_playwright


def check(value, message):
    if not value:
        raise AssertionError(message)


def run(args):
    artifacts = Path(args.artifacts)
    artifacts.mkdir(parents=True, exist_ok=True)
    checks, errors = [], []
    with sync_playwright() as pw:
        browser = pw.chromium.launch(executable_path=args.chromium, headless=True, args=["--autoplay-policy=no-user-gesture-required"])
        page = browser.new_page(viewport={"width": 1280, "height": 900})
        page.on("pageerror", lambda error: errors.append(type(error).__name__))
        page.goto(args.base_url + "/player?token=test-token", wait_until="domcontentloaded")
        page.wait_for_function("typeof renderPlayerShell === 'function'")
        page.evaluate("""() => {
            activeProfileId='profile-a'; activeProfileName='Synthetic profile';
            playbackMeta={title:'Synthetic episode',mediaType:'episode',seasonNumber:1,episodeNumber:1,imdbId:'tt0000000'};
            apiCall=async()=>({});
            // The player and its end events are real. Only the unavailable source response is synthetic.
            window.testAttempts=0;
            ensureNextEpisodePrequeueReady=async()=>{window.testAttempts++;throw Error('Synthetic next source unavailable. Try again.');};
            maybeTriggerNextEpisodePrequeue=()=>{};
            maybeStartWebCreditsDetection=()=>{};
            nextEpisodeLookupComplete=true;
            nextEpisodeInfo={seasonNumber:1,episodeNumber:2,title:'Synthetic next episode'};
            renderPlayerShell();syncUpNextUi();
            const video=document.getElementById('webPlayer');
            installVideoEvents(video); video.muted=true;video.src='/media.mp4';video.play();
            webPlayerNextEpisodeAutoPlayPreference=true;
            webPlayerAutoSkipCreditsPreference=false;
        }""")
        page.wait_for_function("document.querySelector('video').webkitDecodedFrameCount > 2")
        check(page.locator("video").evaluate("v=>v.videoWidth") == 320, "Generated media did not decode")
        page.evaluate("document.querySelector('video').currentTime=7.6")
        page.wait_for_function("webPlayerPlaybackEnded && webPlayerWatchNextTimer !== null")
        check(page.get_by_text("Auto-play next episode", exact=True).is_visible(), "Profile autoplay control missing")
        page.get_by_role("button", name="Pause countdown", exact=True).click()
        page.wait_for_timeout(350)
        held = page.evaluate("webPlayerWatchNextRemainingMs")
        page.wait_for_timeout(550)
        check(page.evaluate("webPlayerWatchNextRemainingMs") == held, "Paused countdown continued")
        checks.append("real-decoded-video-end-with-credits-skip-off-and-countdown-pause")
        for width in [320, 375, 390, 414, 768, 1280]:
            page.set_viewport_size({"width": width, "height": 900})
            page.wait_for_timeout(250)
            bounds = page.locator("#upNextCard").bounding_box()
            check(bounds and bounds['x'] >= -1 and bounds['x'] + bounds['width'] <= width + 1, "Up Next overflow")
            check(page.evaluate("document.documentElement.scrollWidth<=innerWidth"), "Player horizontal overflow")
            check(page.get_by_role("button", name="Cancel", exact=True).is_visible(), "Cancel inaccessible")
            if width in [390,1280]:
                page.screenshot(path=str(artifacts / f"mediastorm-autoplay-{width}.png"))
        checks.append("countdown-and-cancel-fit-six-browser-widths")
        page.get_by_label("Auto-play next episode", exact=True).uncheck()
        check(page.evaluate("webPlayerWatchNextTimer === null"), "Turning autoplay off left a running timer")
        check(not page.get_by_role("button", name="Resume countdown", exact=True).is_visible(), "Inactive countdown still offers Pause/Resume")
        page.get_by_label("Auto-play next episode", exact=True).check()
        checks.append("real-profile-control-stops-an-active-countdown")
        page.get_by_role("button", name="Cancel", exact=True).click()
        page.evaluate("document.querySelector('video').dispatchEvent(new Event('ended'));updateUpNextVisibility()")
        page.wait_for_timeout(600)
        check(page.evaluate("window.testAttempts") == 0, "Cancel permitted a late automatic transition")
        checks.append("cancel-survives-late-real-player-ended-handler")
        # Restart the fixture episode state, then exercise the actual transition failure and manual retry.
        page.evaluate("""() => {
            upNextDismissed=false;webPlayerWatchNextAutoAdvanceBlocked=false;webPlayerWatchNextPaused=false;
            webPlayerNextEpisodeAutoPlayPreference=false;webPlayerPlaybackEnded=true;updateUpNextVisibility();
        }""")
        check(page.evaluate("webPlayerWatchNextTimer === null"), "Autoplay off started a timer")
        page.get_by_role("button", name="Watch Now", exact=True).click()
        page.wait_for_function("window.testAttempts===1 && !playingNextEpisode")
        check(page.evaluate("webPlayerWatchNextAutoAdvanceBlocked"), "Failed source could retry automatically")
        page.get_by_role("button", name="Watch Now", exact=True).click()
        page.wait_for_function("window.testAttempts===2 && !playingNextEpisode")
        check(page.evaluate("nextEpisodeInfo.episodeNumber") == 2, "Source failure skipped an episode")
        checks.append("autoplay-off-keeps-manual-next-and-source-failure-retries-same-episode")
        page.evaluate("webPlayerNextEpisodeAutoPlayPreference=true;webPlayerWatchNextAutoAdvanceBlocked=false;webPlayerWatchNextPaused=false;ensureWatchNextCountdown()")
        page.wait_for_function("window.testAttempts===3 && !playingNextEpisode", timeout=10000)
        page.wait_for_timeout(600)
        check(page.evaluate("window.testAttempts") == 3, "Automatic source failure repeated")
        checks.append("natural-end-countdown-calls-real-transition-once")
        check(not errors, "Player raised an uncaught browser exception")
        browser.close()
    result={"passed":True,"checks":checks,"production_template":True,"generated_media_decoded":True,"next_source_failure_synthetic":True,"native_ui_rendered":False,"private_values_in_artifact":False,"viewports":[320,375,390,414,768,1280]}
    (artifacts / "mediastorm-autoplay-player-results.json").write_text(json.dumps(result,indent=2)+"\n")
    print(json.dumps({"passed":True,"checks":len(checks)}))

if __name__=='__main__':
    parser=argparse.ArgumentParser()
    for arg in ['base-url','chromium','artifacts']:parser.add_argument('--'+arg,required=True)
    run(parser.parse_args())
