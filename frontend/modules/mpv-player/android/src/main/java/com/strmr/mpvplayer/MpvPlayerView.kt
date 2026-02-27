package com.strmr.mpvplayer

import android.app.ActivityManager
import android.content.ComponentCallbacks2
import android.content.Context
import android.content.res.Configuration
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.os.SystemClock
import android.util.Log
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.widget.FrameLayout
import java.io.File
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.ReadableMap
import com.facebook.react.bridge.WritableArray
import com.facebook.react.bridge.WritableMap
import com.facebook.react.uimanager.ThemedReactContext
import dev.jdtech.mpv.MPVLib

class MpvPlayerView(
    context: ThemedReactContext,
    private val eventEmitter: (String, WritableMap?) -> Unit
) :
    FrameLayout(context), PlayerViewDelegate, MPVLib.EventObserver, MPVLib.LogObserver, SurfaceHolder.Callback, ComponentCallbacks2 {

    companion object {
        private const val TAG = "MpvPlayerView"

        // mpv property format constants (from mpv/client.h — stable across versions)
        private const val MPV_FORMAT_FLAG = 3
        private const val MPV_FORMAT_INT64 = 4
        private const val MPV_FORMAT_DOUBLE = 5
        private const val MPV_FORMAT_STRING = 1

        // mpv event ID constants
        private const val MPV_EVENT_END_FILE = 7
        private const val MPV_EVENT_FILE_LOADED = 8

        // Memory tier thresholds
        private const val LOW_RAM_THRESHOLD_MB = 2048L   // <= 2 GB
        private const val MID_RAM_THRESHOLD_MB = 3072L   // <= 3 GB
    }

    private val surfaceView = SurfaceView(context).apply {
        // NOTE: setZOrderMediaOverlay(true) was removed — it creates a separate overlay
        // surface that can leave stale compositing artifacts when the player unmounts,
        // causing the underlying details page to render as a ~20px clipped strip.
        // Without the flag, the SurfaceView renders in the normal view hierarchy order.
    }
    private val mainHandler = Handler(Looper.getMainLooper())

    // Dedicated thread for mpv property/command calls (matching old PlayerActivity pattern).
    // MPVLib calls from the UI thread can race with mpv's internal event processing.
    private val mpvThread = HandlerThread("mpv-cmd").also { it.start() }
    private val mpvHandler = Handler(mpvThread.looper)

    private var initialized = false
    private var destroyed = false
    private var surfaceReady = false
    private var fileLoaded = false

    // HDR mode — set via React prop before source is loaded
    override var isHDR = false

    // Source state
    private var pendingUri: String? = null
    private var currentUri: String? = null
    private var pendingHeaders: List<String>? = null

    // Track mapping: RN 0-based index -> mpv track ID
    private val audioIndexToMpvId = mutableMapOf<Int, Int>()
    private val subtitleIndexToMpvId = mutableMapOf<Int, Int>()
    private var tracksAvailable = false

    // Pending track selections (set before track-list arrives)
    private var pendingAudioTrack: Int? = null
    private var pendingSubtitleTrack: Int? = null

    // Last applied subtitle mpv ID — re-applied after FILE_LOADED since mpv resets sid
    private var lastAppliedSubtitleMpvId: String? = null

    // Subtitle positioning
    private var baseSubtitleMarginY = 0
    private var controlsVisible = false

    // Buffered subtitle style — applied after initializeMpv() since replayBufferedProps()
    // runs before init (initialized=false), causing setSubtitleStyle to silently fail
    private var pendingSubtitleStyle: ReadableMap? = null

    // External subtitle state
    private var currentExternalSubUrl: String? = null
    private var pendingExternalSubUrl: String? = null

    // Progress throttling
    private var lastProgressEmitTime = 0L
    private var lastSubTextCheckTime = 0L
    private var currentDuration = 0.0

    private val isLowRamDevice: Boolean
    private val totalMb: Long
    private val isSystemLowRam: Boolean

    init {
        addView(surfaceView, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT))
        surfaceView.holder.addCallback(this)
        // Tag the SurfaceView so PipManagerModule can find and reparent it during PiP
        surfaceView.tag = "mpv_player_surface"

        val am = context.getSystemService(Context.ACTIVITY_SERVICE) as ActivityManager
        val memInfo = ActivityManager.MemoryInfo()
        am.getMemoryInfo(memInfo)
        totalMb = memInfo.totalMem / (1024 * 1024)
        isSystemLowRam = am.isLowRamDevice
        isLowRamDevice = isSystemLowRam || totalMb <= LOW_RAM_THRESHOLD_MB
    }

    /**
     * Initialize MPV with the appropriate configuration.
     * Called lazily when the first source is set, so that isHDR prop is available.
     */
    private fun initializeMpv() {
        if (initialized || destroyed) return

        try {
            MPVLib.create(context.applicationContext)

            // Detect device memory and choose cache sizes
            val (demuxerMax, demuxerBack) = getDemuxerCacheSizes(totalMb, isSystemLowRam)

            // Use mpv's built-in fast profile (simpler scaling, fewer shader passes)
            MPVLib.setOptionString("profile", "fast")

            MPVLib.setOptionString("hwdec-codecs", "h264,hevc,mpeg4,mpeg2video,vp8,vp9,av1")

            // Workaround for mpv issue #14651
            MPVLib.setOptionString("vd-lavc-film-grain", "cpu")

            if (isHDR) {
                // HDR mode: try mediacodec_embed first for true HDR passthrough
                // (MediaCodec outputs directly to SurfaceView, preserving HDR metadata
                // for Android's display pipeline → auto HDMI HDR switching).
                // Falls back to gpu-next/gpu if mediacodec_embed isn't available.
                MPVLib.setOptionString("vo", "mediacodec_embed,gpu-next,gpu")
                MPVLib.setOptionString("gpu-context", "android")
                MPVLib.setOptionString("hwdec", "mediacodec")
                // Hint mpv to signal the display about content color space.
                // Works with mediacodec_embed natively; on gpu-next, depends on
                // EGL BT.2020/PQ extension support (not available on all devices).
                MPVLib.setOptionString("target-colorspace-hint", "yes")
                // Do NOT force target-trc/target-prim — let mpv auto-negotiate.
                // If the display accepts HDR (via hint or mediacodec_embed), mpv
                // outputs PQ/BT.2020 natively. If not, it tonemaps to SDR.
                MPVLib.setOptionString("tone-mapping", "mobius")
                MPVLib.setOptionString("tone-mapping-param", "0.5")
                MPVLib.setOptionString("hdr-compute-peak", "yes")
                Log.i(TAG, "MPV configured for HDR (vo=mediacodec_embed,gpu-next,gpu)")
            } else {
                // Video output: GPU with Android OpenGL ES context
                MPVLib.setOptionString("vo", "gpu")
                MPVLib.setOptionString("gpu-context", "android")
                MPVLib.setOptionString("opengl-es", "yes")
                // Hardware decode with fallback chain
                MPVLib.setOptionString("hwdec", "mediacodec,mediacodec-copy")
            }

            MPVLib.setOptionString("ao", "audiotrack,opensles")
            MPVLib.setOptionString("save-position-on-quit", "no")
            MPVLib.setOptionString("ytdl", "no")
            MPVLib.setOptionString("force-window", "no")

            // Cache/demuxer limits — tighter on low-RAM devices
            MPVLib.setOptionString("cache", "yes")
            MPVLib.setOptionString("demuxer-max-bytes", demuxerMax)
            MPVLib.setOptionString("demuxer-max-back-bytes", demuxerBack)
            MPVLib.setOptionString("demuxer-readahead-secs", if (isLowRamDevice) "15" else "30")

            // Limit MediaCodec output buffer count on low-RAM to reduce memory pressure
            if (isLowRamDevice) {
                MPVLib.setOptionString("hwdec-extra-frames", "2")
                MPVLib.setOptionString("video-latency-hacks", "yes")
            }

            // Subtitle rendering — defaults match KSPlayer's rounded-box appearance
            // libass on Android has no fontconfig font provider, so we must add fonts
            // manually via sub-fonts-dir. Copy a single system font to avoid scanning
            // the entire /system/fonts directory (200+ files).
            val fontsDir = ensureSubtitleFont(context.applicationContext)
            if (fontsDir != null) {
                MPVLib.setOptionString("sub-fonts-dir", fontsDir)
                Log.d(TAG, "sub-fonts-dir set to: $fontsDir")
            }
            MPVLib.setOptionString("sub-font", "Roboto")
            MPVLib.setOptionString("sub-visibility", "yes")
            // sub-scale=0.667 reduces PGS/image subs to 2/3 size (they're ~1.5x too big
            // vs tvOS KSPlayer). Compensate text sub base: 55 / 0.667 ≈ 82.
            MPVLib.setOptionString("sub-scale", "0.667")
            MPVLib.setOptionString("sub-font-size", "82")
            MPVLib.setOptionString("sub-use-margins", "yes")
            // Force our styling on ASS subs (consistent sizing with SRT).
            // Also required for sub-scale to apply to image subs (sd_lavc.c check).
            MPVLib.setOptionString("sub-ass-override", "force")
            // background-box = BorderStyle 4 — translucent box behind text (like KSPlayer)
            MPVLib.setOptionString("sub-border-style", "background-box")
            MPVLib.setOptionString("sub-back-color", "#99000000")    // 60% black background box
            MPVLib.setOptionString("sub-border-size", "3")           // padding inside background box
            MPVLib.setOptionString("sub-shadow-offset", "0")

            MPVLib.init()
            MPVLib.addObserver(this)
            MPVLib.addLogObserver(this)

            // Observe properties
            MPVLib.observeProperty("time-pos", MPV_FORMAT_DOUBLE)
            MPVLib.observeProperty("duration", MPV_FORMAT_DOUBLE)
            MPVLib.observeProperty("track-list/count", MPV_FORMAT_INT64)
            MPVLib.observeProperty("eof-reached", MPV_FORMAT_FLAG)
            MPVLib.observeProperty("paused-for-cache", MPV_FORMAT_FLAG)
            MPVLib.observeProperty("sub-text", MPV_FORMAT_STRING)
            MPVLib.observeProperty("sub-delay", MPV_FORMAT_DOUBLE)

            // Register for system memory pressure callbacks
            context.applicationContext.registerComponentCallbacks(this)

            initialized = true
            val voMode = if (isHDR) "mediacodec_embed,gpu-next,gpu (HDR)" else "gpu"
            Log.d(TAG, "MPV initialized (vo=$voMode, RAM=${totalMb}MB, lowRam=$isLowRamDevice, cache: $demuxerMax / $demuxerBack)")

            // Apply subtitle style that was buffered before init
            pendingSubtitleStyle?.let { style ->
                pendingSubtitleStyle = null
                setSubtitleStyle(style)
            }
        } catch (e: Exception) {
            Log.e(TAG, "Failed to initialize MPV", e)
            mainHandler.post { emitError("Failed to initialize MPV: ${e.message}") }
        }
    }

    /**
     * Copy a system font (Roboto) to the app's cache/fonts directory so libass
     * can load it without scanning all of /system/fonts. Returns the directory
     * path on success, or null on failure.
     */
    private fun ensureSubtitleFont(appContext: Context): String? {
        return try {
            val fontsDir = File(appContext.cacheDir, "mpv-fonts")
            val destFont = File(fontsDir, "Roboto-Regular.ttf")
            if (!destFont.exists()) {
                fontsDir.mkdirs()
                val srcFont = File("/system/fonts/Roboto-Regular.ttf")
                if (srcFont.exists()) {
                    srcFont.copyTo(destFont, overwrite = true)
                    Log.d(TAG, "Copied Roboto font to ${destFont.absolutePath}")
                } else {
                    // Fallback: try DroidSans (older devices)
                    val fallback = File("/system/fonts/DroidSans.ttf")
                    if (fallback.exists()) {
                        fallback.copyTo(destFont, overwrite = true)
                        Log.d(TAG, "Copied DroidSans font to ${destFont.absolutePath}")
                    } else {
                        Log.w(TAG, "No system font found to copy for subtitles")
                        return null
                    }
                }
            }
            fontsDir.absolutePath
        } catch (e: Exception) {
            Log.e(TAG, "Failed to set up subtitle font", e)
            null
        }
    }

    /**
     * Determine demuxer cache sizes based on device total RAM.
     * Fire Stick (1.7 GB) and similar low-RAM devices get much smaller caches
     * to avoid being killed by the LowMemoryKiller.
     */
    private fun getDemuxerCacheSizes(totalMb: Long, isSystemLowRam: Boolean): Pair<String, String> {
        Log.d(TAG, "Device RAM: ${totalMb}MB, isLowRamDevice: $isSystemLowRam")

        return when {
            isSystemLowRam || totalMb <= LOW_RAM_THRESHOLD_MB -> {
                Log.d(TAG, "Low-RAM device detected — using minimal demuxer cache")
                Pair("4MiB", "2MiB")
            }
            totalMb <= MID_RAM_THRESHOLD_MB -> {
                Log.d(TAG, "Mid-RAM device detected — using reduced demuxer cache")
                Pair("16MiB", "16MiB")
            }
            else -> {
                Pair("32MiB", "32MiB")
            }
        }
    }

    // ========== SurfaceHolder.Callback ==========

    override fun surfaceCreated(holder: SurfaceHolder) {
        if (!initialized || destroyed) return
        emitDebugLog("surfaceCreated")
        MPVLib.attachSurface(holder.surface)
        MPVLib.setOptionString("force-window", "yes")
        if (isHDR) {
            MPVLib.setOptionString("vo", "mediacodec_embed,gpu-next,gpu")
        } else {
            MPVLib.setOptionString("vo", "gpu")
        }
        surfaceReady = true

        // Load pending file if source was set before surface was ready
        pendingUri?.let { uri ->
            loadFile(uri, pendingHeaders)
            pendingUri = null
            pendingHeaders = null
        }
    }

    override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {
        if (!initialized || destroyed) return
        emitDebugLog("surfaceChanged: ${width}x${height}")
        mpvHandler.post {
            MPVLib.setPropertyString("android-surface-size", "${width}x${height}")
        }
    }

    override fun surfaceDestroyed(holder: SurfaceHolder) {
        if (!initialized || destroyed) return
        emitDebugLog("surfaceDestroyed")
        surfaceReady = false
        // Disable VO before detaching surface to avoid rendering to a dead surface
        MPVLib.setOptionString("vo", "null")
        MPVLib.setOptionString("force-window", "no")
        MPVLib.detachSurface()
    }

    // ========== Property setters (called from ViewManager) ==========

    override fun setSource(source: ReadableMap?) {
        source ?: return
        val uri = source.getString("uri") ?: return
        if (uri == currentUri) return

        // Initialize MPV lazily on first source set (so isHDR prop is available)
        if (!initialized && !destroyed) {
            initializeMpv()
        }

        // Parse headers
        var headerList: List<String>? = null
        if (source.hasKey("headers")) {
            val headers = source.getMap("headers")
            if (headers != null) {
                val list = mutableListOf<String>()
                val map = headers.toHashMap()
                for ((key, value) in map) {
                    list.add("$key: $value")
                }
                if (list.isNotEmpty()) {
                    headerList = list
                }
            }
        }

        if (surfaceReady && initialized) {
            loadFile(uri, headerList)
        } else {
            pendingUri = uri
            pendingHeaders = headerList
        }
    }

    private fun loadFile(uri: String, headers: List<String>?) {
        if (!initialized || destroyed) return

        // Reset state for new file
        currentUri = uri
        fileLoaded = false
        tracksAvailable = false
        audioIndexToMpvId.clear()
        subtitleIndexToMpvId.clear()
        currentDuration = 0.0
        currentExternalSubUrl = null
        lastAppliedSubtitleMpvId = null

        // Set headers and load file on mpv thread
        mpvHandler.post {
            try {
                MPVLib.command(arrayOf("set", "http-header-fields", ""))
            } catch (e: Exception) {
                Log.w(TAG, "Failed to clear headers", e)
            }

            if (headers != null) {
                for (header in headers) {
                    try {
                        MPVLib.command(arrayOf("change-list", "http-header-fields", "append", header))
                    } catch (e: Exception) {
                        Log.w(TAG, "Failed to set header: $header", e)
                    }
                }
            }

            Log.d(TAG, "Loading file: $uri")
            try {
                MPVLib.command(arrayOf("loadfile", uri, "replace"))
            } catch (e: Exception) {
                Log.e(TAG, "Failed to load file", e)
                mainHandler.post { emitError("Failed to load file: ${e.message}") }
            }
        }
    }

    override fun setPaused(paused: Boolean) {
        if (!initialized || destroyed) return
        // Keep screen awake while playing (prevents screensaver on Android TV)
        keepScreenOn = !paused
        mpvHandler.post {
            try {
                MPVLib.setPropertyBoolean("pause", paused)
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set pause", e)
            }
        }
    }

    override fun setVolume(volume: Float) {
        if (!initialized || destroyed) return
        val mpvVolume = (volume.coerceIn(0f, 1f) * 100).toInt()
        mpvHandler.post {
            try {
                MPVLib.setPropertyInt("volume", mpvVolume)
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set volume", e)
            }
        }
    }

    override fun setRate(rate: Float) {
        if (!initialized || destroyed) return
        mpvHandler.post {
            try {
                MPVLib.setPropertyDouble("speed", rate.toDouble())
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set rate", e)
            }
        }
    }

    override fun setAudioTrack(rnIndex: Int) {
        if (!initialized || destroyed) return
        if (rnIndex < 0) return

        if (!tracksAvailable) {
            pendingAudioTrack = rnIndex
            return
        }

        val mpvId = audioIndexToMpvId[rnIndex]
        if (mpvId != null) {
            mpvHandler.post {
                try {
                    MPVLib.setPropertyString("aid", mpvId.toString())
                    Log.d(TAG, "Set audio track: rnIndex=$rnIndex -> mpvId=$mpvId")
                } catch (e: Exception) {
                    Log.w(TAG, "Failed to set audio track", e)
                }
            }
        } else {
            Log.w(TAG, "No mpv track ID for audio index $rnIndex")
        }
    }

    override fun setSubtitleTrack(rnIndex: Int) {
        emitDebugLog("setSubtitleTrack called: rnIndex=$rnIndex, initialized=$initialized, destroyed=$destroyed, tracksAvailable=$tracksAvailable, fileLoaded=$fileLoaded")

        if (!initialized || destroyed) {
            emitDebugLog("setSubtitleTrack: SKIPPED (initialized=$initialized, destroyed=$destroyed)")
            return
        }

        if (rnIndex < 0) {
            lastAppliedSubtitleMpvId = null
            mpvHandler.post {
                try {
                    MPVLib.setPropertyString("sid", "no")
                    mainHandler.post { emitDebugLog("setSubtitleTrack: disabled subtitles (sid=no)") }
                } catch (e: Exception) {
                    mainHandler.post { emitDebugLog("setSubtitleTrack: FAILED to disable subtitles: ${e.message}") }
                }
            }
            return
        }

        if (!tracksAvailable) {
            pendingSubtitleTrack = rnIndex
            emitDebugLog("setSubtitleTrack: tracks not available yet, queued pending=$rnIndex")
            return
        }

        val mpvId = subtitleIndexToMpvId[rnIndex]
        emitDebugLog("setSubtitleTrack: map lookup rnIndex=$rnIndex -> mpvId=$mpvId, map=$subtitleIndexToMpvId")
        if (mpvId != null) {
            applySubtitleMpvId(mpvId.toString())
        } else {
            emitDebugLog("setSubtitleTrack: NO mpv track ID for rnIndex=$rnIndex")
        }
    }

    /**
     * Apply a subtitle track by mpv ID, storing it for re-application after FILE_LOADED
     * (mpv resets sid when a file finishes loading).
     * Runs on the dedicated mpv thread to match PlayerActivity's working pattern.
     */
    private fun applySubtitleMpvId(mpvId: String) {
        lastAppliedSubtitleMpvId = mpvId
        mpvHandler.post {
            try {
                MPVLib.setPropertyString("sid", mpvId)
                MPVLib.setPropertyString("sub-visibility", "yes")
                val actualSid = try { MPVLib.getPropertyString("sid") } catch (_: Exception) { "error" }
                val subVis = try { MPVLib.getPropertyString("sub-visibility") } catch (_: Exception) { "error" }
                mainHandler.post {
                    emitDebugLog("applySubtitleMpvId: SET sid=$mpvId, readback sid=$actualSid, sub-visibility=$subVis")
                }
            } catch (e: Exception) {
                mainHandler.post {
                    emitDebugLog("applySubtitleMpvId: FAILED to set sid=$mpvId: ${e.message}")
                }
            }
        }
    }

    override fun setSubtitleSize(size: Float) {
        if (!initialized || destroyed || size <= 0) return
        mpvHandler.post {
            try {
                MPVLib.setPropertyString("sub-font-size", size.toInt().toString())
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set subtitle size", e)
            }
        }
    }

    override fun setSubtitleColor(color: String?) {
        if (!initialized || destroyed || color == null) return
        mpvHandler.post {
            try {
                MPVLib.setPropertyString("sub-color", color)
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set subtitle color", e)
            }
        }
    }

    override fun setSubtitlePosition(position: Float) {
        if (!initialized || destroyed) return
        mpvHandler.post {
            try {
                MPVLib.setPropertyString("sub-margin-y", position.toInt().toString())
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set subtitle position", e)
            }
        }
    }

    override fun setSubtitleStyle(style: ReadableMap?) {
        if (style == null) return

        // Buffer for replay after initializeMpv() if not yet initialized
        if (!initialized) {
            pendingSubtitleStyle = Arguments.makeNativeMap(style.toHashMap())
        }

        if (style.hasKey("fontSize")) {
            val multiplier = style.getDouble("fontSize")
            if (multiplier > 0) {
                val size = (82 * multiplier).toInt()
                if (initialized && !destroyed) {
                    mpvHandler.post {
                        try {
                            MPVLib.setPropertyString("sub-font-size", size.toString())
                        } catch (e: Exception) {
                            Log.w(TAG, "Failed to set subtitle font size", e)
                        }
                    }
                }
            }
        }

        if (style.hasKey("textColor")) {
            val color = style.getString("textColor")
            if (color != null && initialized && !destroyed) {
                mpvHandler.post {
                    try {
                        MPVLib.setPropertyString("sub-color", convertHexToMpvColor(color))
                    } catch (e: Exception) {
                        Log.w(TAG, "Failed to set subtitle text color", e)
                    }
                }
            }
        }

        if (style.hasKey("backgroundColor")) {
            val color = style.getString("backgroundColor")
            if (color != null && initialized && !destroyed) {
                mpvHandler.post {
                    try {
                        MPVLib.setPropertyString("sub-back-color", convertHexToMpvColor(color))
                    } catch (e: Exception) {
                        Log.w(TAG, "Failed to set subtitle background color", e)
                    }
                }
            }
        }

        if (style.hasKey("bottomMargin")) {
            baseSubtitleMarginY = style.getInt("bottomMargin")
            updateSubtitlePosition()
        }
    }

    override fun setControlsVisible(visible: Boolean) {
        controlsVisible = visible
        updateSubtitlePosition()
    }

    private fun updateSubtitlePosition() {
        if (!initialized || destroyed) return
        val margin = baseSubtitleMarginY + if (controlsVisible) 125 else 0
        mpvHandler.post {
            try {
                MPVLib.setPropertyString("sub-margin-y", margin.toString())
            } catch (e: Exception) {
                Log.w(TAG, "Failed to update subtitle position", e)
            }
        }
    }

    /**
     * Convert CSS/KSPlayer hex color to mpv's #AARRGGBB format.
     * Input: #RRGGBB or #RRGGBBAA
     * Output: #FFRRGGBB or #AARRGGBB
     */
    private fun convertHexToMpvColor(color: String): String {
        val hex = color.removePrefix("#")
        return when (hex.length) {
            6 -> "#FF$hex"                    // #RRGGBB → #FFRRGGBB
            8 -> "#${hex.substring(6, 8)}${hex.substring(0, 6)}"  // #RRGGBBAA → #AARRGGBB
            else -> color                     // pass through as-is
        }
    }

    override fun setExternalSubtitleUrl(url: String?) {
        val effectiveUrl = if (url.isNullOrEmpty()) null else url

        // No change
        if (effectiveUrl == currentExternalSubUrl) return

        if (!initialized || destroyed || !fileLoaded) {
            // Defer until file is loaded
            pendingExternalSubUrl = effectiveUrl
            return
        }

        applyExternalSubtitle(effectiveUrl)
    }

    private fun applyExternalSubtitle(url: String?) {
        if (!initialized || destroyed) return

        val prevUrl = currentExternalSubUrl
        currentExternalSubUrl = url
        mpvHandler.post {
            try {
                // Remove previous external subtitle if any
                if (prevUrl != null) {
                    MPVLib.command(arrayOf("sub-remove"))
                    Log.d(TAG, "Removed previous external subtitle")
                }

                if (url != null) {
                    MPVLib.command(arrayOf("sub-add", url, "select"))
                    Log.d(TAG, "Added external subtitle: $url")
                }
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set external subtitle: $url", e)
            }
        }
    }

    override fun seekTo(time: Double) {
        if (!initialized || destroyed) return
        mpvHandler.post {
            try {
                MPVLib.command(arrayOf("seek", time.toString(), "absolute"))
            } catch (e: Exception) {
                Log.w(TAG, "Failed to seek", e)
            }
        }
    }

    // ========== MPVLib.EventObserver ==========

    override fun eventProperty(property: String) {
        // Called for properties observed with MPV_FORMAT_NONE
    }

    override fun eventProperty(property: String, value: Long) {
        when (property) {
            "track-list/count" -> {
                val count = value.toInt()
                Log.d(TAG, "track-list/count changed: $count")
                val result = buildTrackList(count)
                mainHandler.post {
                    tracksAvailable = true
                    emitDebugLog("track-list/count=$count -> tracksAvailable=true, emitting tracks + applying pending")
                    emitTracksChanged(result)
                    applyPendingTracks()
                }
            }
        }
    }

    override fun eventProperty(property: String, value: Double) {
        when (property) {
            "time-pos" -> {
                val now = SystemClock.elapsedRealtime()
                if (now - lastProgressEmitTime < 500) return
                lastProgressEmitTime = now
                if (value < 0) return
                val dur = currentDuration
                mainHandler.post { emitProgress(value, dur) }
            }
            "duration" -> {
                currentDuration = value
            }
        }
    }

    override fun eventProperty(property: String, value: Boolean) {
        when (property) {
            "eof-reached" -> {
                if (value) {
                    mainHandler.post { emitEnd() }
                }
            }
            "paused-for-cache" -> {
                mainHandler.post { emitBuffering(value) }
            }
        }
    }

    override fun eventProperty(property: String, value: String) {
        when (property) {
            "sub-text" -> {
                mainHandler.post {
                    if (value.isNotEmpty()) {
                        emitDebugLog("sub-text: \"${value.take(80)}\"")
                    } else {
                        emitDebugLog("sub-text: (cleared)")
                    }
                    // Emit subtitle text for RN overlay rendering (needed when
                    // mediacodec_embed is active since mpv OSD can't render subs)
                    emitSubtitleText(value)
                }
            }
        }
    }

    // ========== MPVLib.LogObserver — capture mpv's internal log messages ==========

    override fun logMessage(prefix: String, level: Int, text: String) {
        // level: 0=fatal, 10=error, 20=warn, 30=info, 40=verbose, 50=debug, 60=trace
        // Only forward sub/font/ass related messages + errors/warnings
        val msg = text.trimEnd()
        if (msg.isEmpty()) return
        val isSubRelated = prefix == "sub" || prefix == "ass" || prefix == "sd" ||
            prefix == "fontselect" || prefix == "osd" ||
            msg.contains("sub", ignoreCase = true) ||
            msg.contains("font", ignoreCase = true) ||
            msg.contains("ass", ignoreCase = true)
        val isHdrRelated = prefix == "vo" || prefix == "vd" || prefix == "cplayer" ||
            prefix == "hwdec" || prefix == "display" ||
            msg.contains("hdr", ignoreCase = true) ||
            msg.contains("colorspace", ignoreCase = true) ||
            msg.contains("mediacodec", ignoreCase = true) ||
            msg.contains("vulkan", ignoreCase = true) ||
            msg.contains("gpu-next", ignoreCase = true)
        if (level <= 20 || isSubRelated || (isHDR && isHdrRelated)) {
            mainHandler.post {
                emitDebugLog("mpv[$prefix/$level]: $msg")
            }
        }
    }

    override fun event(eventId: Int) {
        when (eventId) {
            MPV_EVENT_FILE_LOADED -> {
                val duration = MPVLib.getPropertyDouble("duration") ?: 0.0
                val width = MPVLib.getPropertyInt("width") ?: 0
                val height = MPVLib.getPropertyInt("height") ?: 0
                val sid = try { MPVLib.getPropertyString("sid") } catch (_: Exception) { "error" }
                val subVis = try { MPVLib.getPropertyString("sub-visibility") } catch (_: Exception) { "error" }
                val currentVo = try { MPVLib.getPropertyString("current-vo") } catch (_: Exception) { "error" }
                val subText = try { MPVLib.getPropertyString("sub-text") } catch (_: Exception) { "n/a" }
                val hwdecCurrent = try { MPVLib.getPropertyString("hwdec-current") } catch (_: Exception) { "n/a" }
                val videoColorParams = try { MPVLib.getPropertyString("video-params/primaries") } catch (_: Exception) { "n/a" }
                val videoColorTrc = try { MPVLib.getPropertyString("video-params/gamma") } catch (_: Exception) { "n/a" }
                currentDuration = duration
                mainHandler.post {
                    fileLoaded = true
                    emitDebugLog("FILE_LOADED: ${width}x${height}, dur=$duration, sid=$sid, sub-visibility=$subVis, vo=$currentVo, hwdec=$hwdecCurrent, tracksAvailable=$tracksAvailable, pendingSub=$pendingSubtitleTrack")
                    emitDebugLog("FILE_LOADED: color=$videoColorParams/$videoColorTrc, isHDR=$isHDR")
                    emitDebugLog("FILE_LOADED: sub-text=${if (subText.isNullOrEmpty()) "(empty)" else "\"${subText.take(60)}\""}")
                    emitLoad(duration, width, height)

                    // Re-apply subtitle track — mpv resets sid on file load
                    lastAppliedSubtitleMpvId?.let { mpvId ->
                        emitDebugLog("FILE_LOADED: re-applying subtitle sid=$mpvId")
                        applySubtitleMpvId(mpvId)
                    }

                    // Apply deferred external subtitle if set before file was loaded
                    pendingExternalSubUrl?.let { url ->
                        pendingExternalSubUrl = null
                        applyExternalSubtitle(url)
                    }
                }
            }
            MPV_EVENT_END_FILE -> {
                mainHandler.post { emitEnd() }
            }
        }
    }

    // ========== Internal helpers ==========

    private fun buildTrackList(count: Int): Pair<WritableArray, WritableArray> {
        val audioTracks = Arguments.createArray()
        val subtitleTracks = Arguments.createArray()
        val newAudioMap = mutableMapOf<Int, Int>()
        val newSubtitleMap = mutableMapOf<Int, Int>()
        var audioIndex = 0
        var subtitleIndex = 0

        for (i in 0 until count) {
            val type = MPVLib.getPropertyString("track-list/$i/type") ?: continue
            val mpvId = MPVLib.getPropertyInt("track-list/$i/id") ?: continue
            val title = MPVLib.getPropertyString("track-list/$i/title") ?: ""
            val lang = MPVLib.getPropertyString("track-list/$i/lang") ?: ""
            val codec = MPVLib.getPropertyString("track-list/$i/codec") ?: ""
            val selected = MPVLib.getPropertyBoolean("track-list/$i/selected") ?: false

            when (type) {
                "audio" -> {
                    val track = Arguments.createMap().apply {
                        putInt("id", audioIndex)
                        putString("type", "audio")
                        putString("title", title)
                        putString("language", lang)
                        putString("codec", codec)
                        putBoolean("selected", selected)
                    }
                    audioTracks.pushMap(track)
                    newAudioMap[audioIndex] = mpvId
                    audioIndex++
                }
                "sub" -> {
                    Log.d(TAG, "buildTrackList: sub track i=$i mpvId=$mpvId title=$title lang=$lang codec=$codec selected=$selected -> subtitleIndex=$subtitleIndex")
                    val track = Arguments.createMap().apply {
                        putInt("id", subtitleIndex)
                        putString("type", "subtitle")
                        putString("title", title)
                        putString("language", lang)
                        putString("codec", codec)
                        putBoolean("selected", selected)
                    }
                    subtitleTracks.pushMap(track)
                    newSubtitleMap[subtitleIndex] = mpvId
                    subtitleIndex++
                }
            }
        }

        audioIndexToMpvId.clear()
        audioIndexToMpvId.putAll(newAudioMap)
        subtitleIndexToMpvId.clear()
        subtitleIndexToMpvId.putAll(newSubtitleMap)

        mainHandler.post {
            emitDebugLog("buildTrackList: ${audioIndex} audio, ${subtitleIndex} subtitle tracks. subMap=$newSubtitleMap")
        }

        return Pair(audioTracks, subtitleTracks)
    }

    private fun applyPendingTracks() {
        emitDebugLog("applyPendingTracks: pendingAudio=$pendingAudioTrack, pendingSub=$pendingSubtitleTrack")
        pendingAudioTrack?.let { index ->
            pendingAudioTrack = null
            setAudioTrack(index)
        }
        pendingSubtitleTrack?.let { index ->
            pendingSubtitleTrack = null
            setSubtitleTrack(index)
        }
    }

    // ========== Event emission ==========

    private fun emitEvent(eventName: String, data: WritableMap?) {
        eventEmitter(eventName, data)
    }

    private fun emitLoad(duration: Double, width: Int, height: Int) {
        val data = Arguments.createMap().apply {
            putDouble("duration", duration)
            putInt("width", width)
            putInt("height", height)
        }
        emitEvent("onLoad", data)
    }

    private fun emitProgress(currentTime: Double, duration: Double) {
        val data = Arguments.createMap().apply {
            putDouble("currentTime", currentTime)
            putDouble("duration", duration)
        }
        emitEvent("onProgress", data)
    }

    private fun emitEnd() {
        val data = Arguments.createMap().apply {
            putBoolean("ended", true)
        }
        emitEvent("onEnd", data)
    }

    private fun emitError(message: String) {
        val data = Arguments.createMap().apply {
            putString("error", message)
        }
        emitEvent("onError", data)
    }

    private fun emitTracksChanged(tracks: Pair<WritableArray, WritableArray>) {
        val data = Arguments.createMap().apply {
            putArray("audioTracks", tracks.first)
            putArray("subtitleTracks", tracks.second)
        }
        emitEvent("onTracksChanged", data)
    }

    private fun emitBuffering(buffering: Boolean) {
        val data = Arguments.createMap().apply {
            putBoolean("buffering", buffering)
        }
        emitEvent("onBuffering", data)
    }

    private fun emitSubtitleText(text: String) {
        val data = Arguments.createMap().apply {
            putString("text", text)
        }
        emitEvent("onSubtitleText", data)
    }

    private fun emitDebugLog(message: String) {
        Log.d(TAG, message)
        val data = Arguments.createMap().apply {
            putString("message", "[MpvPlayer-PiP] $message")
        }
        emitEvent("onDebugLog", data)
    }

    // ========== ComponentCallbacks2 (memory pressure) ==========

    override fun onTrimMemory(level: Int) {
        if (!initialized || destroyed) return
        when {
            level >= ComponentCallbacks2.TRIM_MEMORY_RUNNING_CRITICAL -> {
                Log.w(TAG, "CRITICAL memory pressure (level=$level) — dropping demuxer cache to minimum")
                try {
                    MPVLib.setPropertyString("demuxer-max-bytes", "2MiB")
                    MPVLib.setPropertyString("demuxer-max-back-bytes", "1MiB")
                    MPVLib.setPropertyString("demuxer-readahead-secs", "5")
                } catch (e: Exception) {
                    Log.w(TAG, "Failed to reduce cache on trim", e)
                }
            }
            level >= ComponentCallbacks2.TRIM_MEMORY_RUNNING_LOW -> {
                Log.w(TAG, "Low memory pressure (level=$level) — reducing demuxer cache")
                try {
                    MPVLib.setPropertyString("demuxer-max-bytes", if (isLowRamDevice) "2MiB" else "8MiB")
                    MPVLib.setPropertyString("demuxer-max-back-bytes", if (isLowRamDevice) "1MiB" else "4MiB")
                    MPVLib.setPropertyString("demuxer-readahead-secs", if (isLowRamDevice) "8" else "15")
                } catch (e: Exception) {
                    Log.w(TAG, "Failed to reduce cache on trim", e)
                }
            }
        }
    }

    override fun onConfigurationChanged(newConfig: Configuration) {
        // Required by ComponentCallbacks2, no action needed
    }

    override fun onLowMemory() {
        // Fallback for pre-API-14 — onTrimMemory handles this on modern devices
        onTrimMemory(ComponentCallbacks2.TRIM_MEMORY_RUNNING_CRITICAL)
    }

    // ========== Cleanup ==========

    override fun destroy() {
        if (destroyed) return
        destroyed = true
        keepScreenOn = false
        Log.d(TAG, "Destroying MpvPlayerView")

        mainHandler.removeCallbacksAndMessages(null)
        mpvHandler.removeCallbacksAndMessages(null)
        mpvThread.quitSafely()

        try {
            context.applicationContext.unregisterComponentCallbacks(this)
        } catch (e: Exception) {
            Log.w(TAG, "Failed to unregister component callbacks", e)
        }

        if (initialized) {
            try {
                MPVLib.removeObserver(this)
            } catch (e: Exception) {
                Log.w(TAG, "Failed to remove observer", e)
            }
            try {
                MPVLib.removeLogObserver(this)
            } catch (e: Exception) {
                Log.w(TAG, "Failed to remove log observer", e)
            }
            try {
                MPVLib.destroy()
            } catch (e: Exception) {
                Log.w(TAG, "Failed to destroy MPV", e)
            }
        }

        surfaceView.holder.removeCallback(this)
        initialized = false
    }
}
