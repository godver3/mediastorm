# Independent next-episode autoplay validation

The profile setting `playback.nextEpisodeAutoPlay` controls automatic episode transitions independently of credits skipping. PATCH `/api/users/{userId}/settings/skipping` with `nextEpisode` changes only that choice. General settings PUTs preserve the stored choice, including false. Existing profiles default to on; a failed initial player settings read does not enable automatic transitions.

`TestWebPlayerSkipPreferences` executes the rendered production JavaScript in Node, including per-profile persistence, explicit false, failed reads, malformed data, monotonic countdown timing, pause, cancellation and queued callbacks. `TestEpisodeAutoplayRenderedPlayerBrowser` uses Chromium and generated H.264/AAC media, real media end events and the actual transition error/retry flow. Only the unavailable next-source response is replaced. It checks 320, 375, 390, 414, 768 and 1280 pixel viewports.

`TestEpisodeAutoplayApplicationBrowser` uses the guarded loopback demo fixture to check the real application API, foreign and PIN-locked profiles, another device, independent credits preferences and a stale full settings save. Its private synthetic credentials remain outside the repository. It restores the original autoplay choice.

Set `MEDIASTORM_BROWSER_PYTHON`, `MEDIASTORM_CHROMIUM` and `MEDIASTORM_BROWSER_ARTIFACTS` to run the rendered-player check. The real application check additionally requires `MEDIASTORM_FULL_APPLICATION_BASE_URL` and `MEDIASTORM_FULL_APPLICATION_CREDENTIALS` pointing to the documented synthetic fixture. Other PostgreSQL and full-application regressions keep their existing environment contracts.

The evidence page reports completed tests and their boundaries. Browser images are not rendered native screens. This package does not add post-play ratings/recommendations or edit the separately distributed Watch application's source.
