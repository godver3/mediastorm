package com.strmr.mpvplayer

import android.app.Activity
import android.app.ActivityManager
import android.content.Context
import android.content.pm.ActivityInfo
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.HandlerThread
import android.os.Looper
import android.util.Log
import android.view.Display
import android.view.KeyEvent
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.view.WindowManager
import android.widget.FrameLayout
import dev.jdtech.mpv.MPVLib

class PlayerActivity : Activity(), MPVLib.EventObserver, MPVLib.LogObserver,
    SurfaceHolder.Callback, PlayerControlsView.Listener {

    companion object {
        private const val TAG = "PlayerActivity"

        // mpv property format constants
        private const val MPV_FORMAT_FLAG = 3
        private const val MPV_FORMAT_INT64 = 4
        private const val MPV_FORMAT_DOUBLE = 5

        // mpv event IDs: use MPVLib.MPV_EVENT_* constants directly

        // Memory tier thresholds (matches MpvPlayerView)
        private const val LOW_RAM_THRESHOLD_MB = 2048L
        private const val MID_RAM_THRESHOLD_MB = 3072L

        // Seek accumulation
        private const val SEEK_APPLY_DELAY_MS = 500L
        private const val SEEK_STEP_SECONDS = 10

        /** Static result read by PlayerLauncherModule after this Activity is destroyed. */
        @Volatile var lastResult: Bundle? = null
    }

    // Intent extras
    private lateinit var streamUrl: String
    private var title = ""
    private var authToken = ""
    private var userId = ""
    private var mediaType = ""
    private var itemId = ""
    private var startOffset = 0L
    private var durationHint = 0L
    private var preselectedAudioTrack = -1
    private var preselectedSubtitleTrack = -1
    private var backendUrl = ""
    private var isHDR = false
    private var isDolbyVision = false

    // Video/audio metadata for controls display
    private var resolution = ""
    private var dolbyVisionProfile = ""
    private var videoCodec = ""
    private var videoBitrate = 0L
    private var frameRate = ""
    private var audioCodec = ""
    private var audioChannels = ""
    private var audioBitrate = 0L
    private var sourcePath = ""
    private var passthroughName = ""
    private var passthroughDescription = ""
    private var colorTransfer = ""
    private var colorPrimaries = ""
    private var colorSpace = ""
    private var year = 0
    private var seasonNumber = 0
    private var episodeNumber = 0
    private var seriesName = ""
    private var episodeName = ""

    // HDR display state
    private var displaySupportsHDR = false
    private var hdrPassthroughActive = false

    // UI
    private lateinit var surfaceView: SurfaceView
    private lateinit var controlsView: PlayerControlsView

    // State
    private val mainHandler = Handler(Looper.getMainLooper())
    private val mpvThread = HandlerThread("mpv-cmd").also { it.start() }
    private val mpvHandler = Handler(mpvThread.looper)
    private var initialized = false
    private var destroyed = false
    private var surfaceReady = false
    private var fileLoaded = false
    private var paused = false
    private var currentPosition = 0.0
    private var currentDuration = 0.0
    private var seekedToStart = false
    private var tracksApplied = false

    // Seek accumulation (for D-pad when controls hidden)
    private var seekAccumulator = 0
    private val applySeekRunnable = Runnable { applyAccumulatedSeek() }

    // Track data
    private val audioTracks = mutableListOf<TrackInfo>()
    private val subtitleTracks = mutableListOf<TrackInfo>()

    // Progress reporting
    private var progressReporter: ProgressReporter? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)

        // Full-screen immersive
        window.addFlags(
            WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON or
                WindowManager.LayoutParams.FLAG_FULLSCREEN
        )
        window.decorView.systemUiVisibility = (
            android.view.View.SYSTEM_UI_FLAG_FULLSCREEN or
                android.view.View.SYSTEM_UI_FLAG_HIDE_NAVIGATION or
                android.view.View.SYSTEM_UI_FLAG_IMMERSIVE_STICKY or
                android.view.View.SYSTEM_UI_FLAG_LAYOUT_FULLSCREEN or
                android.view.View.SYSTEM_UI_FLAG_LAYOUT_HIDE_NAVIGATION or
                android.view.View.SYSTEM_UI_FLAG_LAYOUT_STABLE
            )

        // Extract intent extras
        intent?.let { i ->
            streamUrl = i.getStringExtra("streamUrl") ?: ""
            title = i.getStringExtra("title") ?: ""
            authToken = i.getStringExtra("authToken") ?: ""
            userId = i.getStringExtra("userId") ?: ""
            mediaType = i.getStringExtra("mediaType") ?: ""
            itemId = i.getStringExtra("itemId") ?: ""
            startOffset = i.getLongExtra("startOffset", 0L)
            durationHint = i.getLongExtra("durationHint", 0L)
            preselectedAudioTrack = i.getIntExtra("preselectedAudioTrack", -1)
            preselectedSubtitleTrack = i.getIntExtra("preselectedSubtitleTrack", -1)
            backendUrl = i.getStringExtra("backendUrl") ?: ""
            isHDR = i.getBooleanExtra("isHDR", false)
            isDolbyVision = i.getBooleanExtra("isDolbyVision", false)

            // Video/audio metadata
            resolution = i.getStringExtra("resolution") ?: ""
            dolbyVisionProfile = i.getStringExtra("dolbyVisionProfile") ?: ""
            videoCodec = i.getStringExtra("videoCodec") ?: ""
            videoBitrate = i.getLongExtra("videoBitrate", 0L)
            frameRate = i.getStringExtra("frameRate") ?: ""
            audioCodec = i.getStringExtra("audioCodec") ?: ""
            audioChannels = i.getStringExtra("audioChannels") ?: ""
            audioBitrate = i.getLongExtra("audioBitrate", 0L)
            sourcePath = i.getStringExtra("sourcePath") ?: ""
            passthroughName = i.getStringExtra("passthroughName") ?: ""
            passthroughDescription = i.getStringExtra("passthroughDescription") ?: ""
            colorTransfer = i.getStringExtra("colorTransfer") ?: ""
            colorPrimaries = i.getStringExtra("colorPrimaries") ?: ""
            colorSpace = i.getStringExtra("colorSpace") ?: ""
            year = i.getIntExtra("year", 0)
            seasonNumber = i.getIntExtra("seasonNumber", 0)
            episodeNumber = i.getIntExtra("episodeNumber", 0)
            seriesName = i.getStringExtra("seriesName") ?: ""
            episodeName = i.getStringExtra("episodeName") ?: ""
        }

        if (streamUrl.isEmpty()) {
            Log.e(TAG, "No streamUrl provided")
            finish()
            return
        }

        // Detect display HDR capabilities and enable HDR output mode
        displaySupportsHDR = checkDisplayHDRSupport()
        hdrPassthroughActive = isHDR && displaySupportsHDR
        if (hdrPassthroughActive) {
            window.colorMode = ActivityInfo.COLOR_MODE_HDR
            Log.i(TAG, "HDR passthrough enabled: colorMode=HDR (isDV=$isDolbyVision)")
        }

        // Build UI programmatically
        val root = FrameLayout(this)
        surfaceView = SurfaceView(this)
        surfaceView.holder.addCallback(this)
        root.addView(surfaceView, FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT
        ))

        controlsView = PlayerControlsView(this)
        controlsView.listener = this
        controlsView.setTitle(title)
        if (durationHint > 0) {
            controlsView.setDuration(durationHint.toInt())
        }
        controlsView.setMetadata(
            resolution = resolution,
            dvProfile = dolbyVisionProfile,
            videoCodec = videoCodec,
            videoBitrate = videoBitrate,
            frameRate = frameRate,
            audioCodec = audioCodec,
            audioChannels = audioChannels,
            audioBitrate = audioBitrate,
            isHDR = isHDR,
            isDV = isDolbyVision,
            colorTransfer = colorTransfer,
            colorPrimaries = colorPrimaries,
            colorSpace = colorSpace,
            seasonNumber = seasonNumber,
            episodeNumber = episodeNumber,
            seriesName = seriesName,
            episodeName = episodeName,
        )
        root.addView(controlsView, FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT
        ))

        setContentView(root)

        // Initialize MPV
        initializeMpv()

        // Initialize progress reporter
        if (backendUrl.isNotEmpty() && userId.isNotEmpty() && authToken.isNotEmpty() && itemId.isNotEmpty()) {
            val extra = buildProgressMetadata()
            progressReporter = ProgressReporter(backendUrl, userId, authToken, mediaType, itemId, extra)
            progressReporter?.start()
        }

        Log.d(TAG, "PlayerActivity created: title=$title, startOffset=$startOffset, duration=$durationHint")
    }

    private fun buildProgressMetadata(): Map<String, Any?> {
        val map = mutableMapOf<String, Any?>()
        intent?.let { i ->
            map["seasonNumber"] = i.getIntExtra("seasonNumber", 0).let { if (it > 0) it else null }
            map["episodeNumber"] = i.getIntExtra("episodeNumber", 0).let { if (it > 0) it else null }
            map["seriesId"] = i.getStringExtra("seriesId")
            map["seriesName"] = i.getStringExtra("seriesName")
            map["episodeName"] = i.getStringExtra("episodeName")
            map["titleId"] = i.getStringExtra("titleId")
            map["imdbId"] = i.getStringExtra("imdbId")
            map["tvdbId"] = i.getStringExtra("tvdbId")
        }
        return map
    }

    private fun initializeMpv() {
        try {
            MPVLib.create(applicationContext)

            val am = getSystemService(Context.ACTIVITY_SERVICE) as ActivityManager
            val memInfo = ActivityManager.MemoryInfo()
            am.getMemoryInfo(memInfo)
            val totalMb = memInfo.totalMem / (1024 * 1024)
            val isLowRam = am.isLowRamDevice || totalMb <= LOW_RAM_THRESHOLD_MB
            val (demuxerMax, demuxerBack) = getDemuxerCacheSizes(totalMb, am.isLowRamDevice)

            MPVLib.setOptionString("profile", "fast")
            MPVLib.setOptionString("hwdec-codecs", "h264,hevc,mpeg4,mpeg2video,vp8,vp9,av1")
            MPVLib.setOptionString("vd-lavc-film-grain", "cpu")

            if (hdrPassthroughActive) {
                // HDR passthrough: use gpu-next (Vulkan) for proper HDR surface support,
                // falling back to gpu (OpenGL ES) if Vulkan is unavailable.
                // target-colorspace-hint tells mpv to signal the display about the
                // content's color space so Android switches HDMI output to HDR mode.
                MPVLib.setOptionString("vo", "gpu-next,gpu")
                MPVLib.setOptionString("gpu-context", "android")
                MPVLib.setOptionString("hwdec", "mediacodec")
                MPVLib.setOptionString("target-colorspace-hint", "yes")
                // Avoid tone-mapping — output PQ/BT.2020 directly for the display to handle
                MPVLib.setOptionString("target-trc", "pq")
                MPVLib.setOptionString("target-prim", "bt.2020")
                MPVLib.setOptionString("target-peak", "10000")
                MPVLib.setOptionString("tone-mapping", "clip")
                MPVLib.setOptionString("hdr-compute-peak", "no")
                Log.i(TAG, "MPV configured for HDR passthrough (vo=gpu-next,gpu)")
            } else {
                MPVLib.setOptionString("vo", "gpu")
                MPVLib.setOptionString("gpu-context", "android")
                MPVLib.setOptionString("opengl-es", "yes")
                MPVLib.setOptionString("hwdec", "mediacodec,mediacodec-copy")
            }
            MPVLib.setOptionString("ao", "audiotrack,opensles")
            MPVLib.setOptionString("save-position-on-quit", "no")
            MPVLib.setOptionString("ytdl", "no")
            MPVLib.setOptionString("force-window", "no")
            MPVLib.setOptionString("cache", "yes")
            MPVLib.setOptionString("demuxer-max-bytes", demuxerMax)
            MPVLib.setOptionString("demuxer-max-back-bytes", demuxerBack)
            MPVLib.setOptionString("demuxer-readahead-secs", if (isLowRam) "15" else "30")

            if (isLowRam) {
                MPVLib.setOptionString("hwdec-extra-frames", "2")
                MPVLib.setOptionString("video-latency-hacks", "yes")
            }

            MPVLib.setOptionString("sub-visibility", "yes")
            MPVLib.setOptionString("sub-font", "sans-serif")
            MPVLib.setOptionString("sub-font-size", "55")
            MPVLib.setOptionString("sub-use-margins", "yes")

            MPVLib.init()
            MPVLib.addObserver(this)
            MPVLib.addLogObserver(this)

            MPVLib.observeProperty("time-pos", MPV_FORMAT_DOUBLE)
            MPVLib.observeProperty("duration", MPV_FORMAT_DOUBLE)
            MPVLib.observeProperty("track-list/count", MPV_FORMAT_INT64)
            MPVLib.observeProperty("eof-reached", MPV_FORMAT_FLAG)
            MPVLib.observeProperty("pause", MPV_FORMAT_FLAG)

            initialized = true
            Log.d(TAG, "MPV initialized (RAM=${totalMb}MB, lowRam=$isLowRam, cache=$demuxerMax/$demuxerBack, " +
                "hdr=$isHDR, dv=$isDolbyVision, hdrPassthrough=$hdrPassthroughActive)")
        } catch (e: Exception) {
            Log.e(TAG, "Failed to initialize MPV", e)
            finishWithResult()
        }
    }

    private fun checkDisplayHDRSupport(): Boolean {
        val display = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            display
        } else {
            @Suppress("DEPRECATION")
            windowManager.defaultDisplay
        }
        if (display == null) {
            Log.w(TAG, "No display available for HDR capability check")
            return false
        }

        val hdrCaps = display.hdrCapabilities
        if (hdrCaps == null) {
            Log.i(TAG, "Display does not report HDR capabilities")
            return false
        }

        val types = hdrCaps.supportedHdrTypes
        val hasHDR10 = types.contains(Display.HdrCapabilities.HDR_TYPE_HDR10)
        val hasDV = types.contains(Display.HdrCapabilities.HDR_TYPE_DOLBY_VISION)
        val hasHLG = types.contains(Display.HdrCapabilities.HDR_TYPE_HLG)
        val hasHDR10Plus = Build.VERSION.SDK_INT >= 29 &&
            types.contains(Display.HdrCapabilities.HDR_TYPE_HDR10_PLUS)

        Log.i(TAG, "Display HDR capabilities: HDR10=$hasHDR10, DV=$hasDV, HLG=$hasHLG, HDR10+=$hasHDR10Plus, " +
            "maxLuminance=${hdrCaps.desiredMaxLuminance}, minLuminance=${hdrCaps.desiredMinLuminance}")

        return types.isNotEmpty()
    }

    private fun getDemuxerCacheSizes(totalMb: Long, isSystemLowRam: Boolean): Pair<String, String> {
        return when {
            isSystemLowRam || totalMb <= LOW_RAM_THRESHOLD_MB -> Pair("4MiB", "2MiB")
            totalMb <= MID_RAM_THRESHOLD_MB -> Pair("16MiB", "16MiB")
            else -> Pair("32MiB", "32MiB")
        }
    }

    // ========== SurfaceHolder.Callback ==========

    override fun surfaceCreated(holder: SurfaceHolder) {
        if (!initialized || destroyed) return
        Log.i(TAG, "surfaceCreated — attaching surface to MPV (hdrPassthrough=$hdrPassthroughActive)")
        MPVLib.attachSurface(holder.surface)
        MPVLib.setOptionString("force-window", "yes")
        // Re-enable the VO that was configured in initializeMpv
        if (hdrPassthroughActive) {
            MPVLib.setOptionString("vo", "gpu-next,gpu")
        } else {
            MPVLib.setOptionString("vo", "gpu")
        }
        surfaceReady = true
        loadFile()
    }

    override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {
        if (!initialized || destroyed) return
        Log.i(TAG, "surfaceChanged: ${width}x${height} format=$format")
        mpvSetProperty("android-surface-size", "${width}x${height}")
    }

    override fun surfaceDestroyed(holder: SurfaceHolder) {
        if (!initialized || destroyed) return
        Log.i(TAG, "surfaceDestroyed")
        surfaceReady = false
        MPVLib.setOptionString("vo", "null")
        MPVLib.setOptionString("force-window", "no")
        MPVLib.detachSurface()
    }

    private fun loadFile() {
        if (!initialized || destroyed || streamUrl.isEmpty()) return
        Log.d(TAG, "Loading: $streamUrl")
        try {
            MPVLib.command(arrayOf("loadfile", streamUrl, "replace"))
        } catch (e: Exception) {
            Log.e(TAG, "Failed to load file", e)
            finishWithResult()
        }
    }

    // ========== MPVLib.EventObserver ==========

    override fun eventProperty(property: String) {}

    override fun eventProperty(property: String, value: Long) {
        when (property) {
            "track-list/count" -> {
                val count = value.toInt()
                mainHandler.post { onTracksAvailable(count) }
            }
        }
    }

    override fun eventProperty(property: String, value: Double) {
        when (property) {
            "time-pos" -> {
                if (value < 0) return
                currentPosition = value
                progressReporter?.updatePosition(value, currentDuration)
                mainHandler.post {
                    controlsView.updateTime(value.toInt())
                }
            }
            "duration" -> {
                currentDuration = value
                mainHandler.post {
                    controlsView.setDuration(value.toInt())
                }
            }
        }
    }

    override fun eventProperty(property: String, value: Boolean) {
        when (property) {
            "eof-reached" -> {
                if (value) {
                    mainHandler.post { finishWithResult(completed = true) }
                }
            }
            "pause" -> {
                paused = value
                mainHandler.post {
                    controlsView.updatePlayPauseState(value)
                }
            }
        }
    }

    override fun eventProperty(property: String, value: String) {}

    override fun event(eventId: Int) {
        Log.d(TAG, "event($eventId)")
        try {
            when (eventId) {
                MPVLib.MPV_EVENT_FILE_LOADED -> {
                    val duration = try { MPVLib.getPropertyDouble("duration") } catch (_: Exception) { 0.0 } ?: 0.0
                    currentDuration = duration
                    Log.i(TAG, "File loaded: duration=${duration}s, startOffset=$startOffset")
                    mainHandler.post {
                        fileLoaded = true
                        controlsView.setDuration(duration.toInt())
                    }

                    // Seek to start offset
                    if (startOffset > 0 && !seekedToStart) {
                        seekedToStart = true
                        Log.i(TAG, "Seeking to start offset: ${startOffset}s")
                        mpvCommand("seek", startOffset.toString(), "absolute")
                    }

                    // Unpause
                    paused = false
                    mpvSetProperty("pause", false)
                }
                MPVLib.MPV_EVENT_END_FILE -> {
                    Log.i(TAG, "End of file")
                    mainHandler.post { finishWithResult(completed = true) }
                }
            }
        } catch (e: Exception) {
            Log.e(TAG, "Error in event($eventId)", e)
        }
    }

    // ========== MPVLib.LogObserver ==========

    override fun logMessage(prefix: String, level: Int, text: String) {
        // Forward MPV's internal log messages to logcat under our tag
        val msg = "[mpv/$prefix] ${text.trimEnd()}"
        when {
            level <= MPVLib.MPV_LOG_LEVEL_ERROR -> Log.e(TAG, msg)
            level <= MPVLib.MPV_LOG_LEVEL_WARN -> Log.w(TAG, msg)
            level <= MPVLib.MPV_LOG_LEVEL_INFO -> Log.i(TAG, msg)
            // Skip verbose/debug/trace to avoid flooding logcat
        }
    }

    /** Run an MPV command off the main thread to avoid ANR. */
    private fun mpvCommand(vararg args: String) {
        mpvHandler.post {
            try {
                MPVLib.command(arrayOf(*args))
            } catch (e: Exception) {
                Log.w(TAG, "mpvCommand failed: ${args.toList()}", e)
            }
        }
    }

    /** Set an MPV property off the main thread. */
    private fun mpvSetProperty(name: String, value: Boolean) {
        mpvHandler.post {
            try {
                MPVLib.setPropertyBoolean(name, value)
            } catch (e: Exception) {
                Log.w(TAG, "mpvSetProperty failed: $name=$value", e)
            }
        }
    }

    private fun mpvSetProperty(name: String, value: String) {
        mpvHandler.post {
            try {
                MPVLib.setPropertyString(name, value)
            } catch (e: Exception) {
                Log.w(TAG, "mpvSetProperty failed: $name=$value", e)
            }
        }
    }

    // ========== Track management ==========

    private fun onTracksAvailable(count: Int) {
        audioTracks.clear()
        subtitleTracks.clear()

        for (i in 0 until count) {
            val type = MPVLib.getPropertyString("track-list/$i/type") ?: continue
            val mpvId = MPVLib.getPropertyInt("track-list/$i/id") ?: continue
            val trackTitle = MPVLib.getPropertyString("track-list/$i/title") ?: ""
            val lang = MPVLib.getPropertyString("track-list/$i/lang") ?: ""
            val codec = MPVLib.getPropertyString("track-list/$i/codec") ?: ""
            val selected = MPVLib.getPropertyBoolean("track-list/$i/selected") ?: false

            val info = TrackInfo(mpvId, trackTitle, lang, codec, selected)
            when (type) {
                "audio" -> audioTracks.add(info)
                "sub" -> subtitleTracks.add(info)
            }
        }

        Log.d(TAG, "Tracks: ${audioTracks.size} audio, ${subtitleTracks.size} subtitle")

        // Apply preselected tracks (0-based index → mpv ID)
        if (!tracksApplied) {
            tracksApplied = true
            if (preselectedAudioTrack >= 0 && preselectedAudioTrack < audioTracks.size) {
                val mpvId = audioTracks[preselectedAudioTrack].mpvId
                Log.i(TAG, "Applying preselected audio track: index=$preselectedAudioTrack, mpvId=$mpvId")
                mpvSetProperty("aid", mpvId.toString())
            }
            if (preselectedSubtitleTrack >= 0 && preselectedSubtitleTrack < subtitleTracks.size) {
                val mpvId = subtitleTracks[preselectedSubtitleTrack].mpvId
                Log.i(TAG, "Applying preselected subtitle track: index=$preselectedSubtitleTrack, mpvId=$mpvId")
                mpvSetProperty("sid", mpvId.toString())
            } else if (preselectedSubtitleTrack < 0) {
                mpvSetProperty("sid", "no")
            }
        }
    }

    // ========== Key handling ==========

    override fun onKeyDown(keyCode: Int, event: KeyEvent): Boolean {
        Log.d(TAG, "onKeyDown: keyCode=$keyCode controlsShowing=${controlsView.isShowing()}")
        when (keyCode) {
            // Play/Pause
            KeyEvent.KEYCODE_DPAD_CENTER,
            KeyEvent.KEYCODE_ENTER,
            KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE -> {
                if (controlsView.isShowing()) {
                    // Let controls handle focus-based interaction
                    return super.onKeyDown(keyCode, event)
                }
                togglePlayPause()
                return true
            }

            KeyEvent.KEYCODE_MEDIA_PLAY -> {
                if (paused) togglePlayPause()
                return true
            }

            KeyEvent.KEYCODE_MEDIA_PAUSE -> {
                if (!paused) togglePlayPause()
                return true
            }

            // Seek left/right
            KeyEvent.KEYCODE_DPAD_LEFT -> {
                if (controlsView.isShowing()) {
                    // Let controls handle seek bar / button navigation
                    return super.onKeyDown(keyCode, event)
                }
                accumulateSeek(-SEEK_STEP_SECONDS)
                return true
            }

            KeyEvent.KEYCODE_DPAD_RIGHT -> {
                if (controlsView.isShowing()) {
                    return super.onKeyDown(keyCode, event)
                }
                accumulateSeek(SEEK_STEP_SECONDS)
                return true
            }

            KeyEvent.KEYCODE_MEDIA_REWIND,
            KeyEvent.KEYCODE_MEDIA_SKIP_BACKWARD,
            KeyEvent.KEYCODE_MEDIA_STEP_BACKWARD -> {
                accumulateSeek(-SEEK_STEP_SECONDS)
                return true
            }

            KeyEvent.KEYCODE_MEDIA_FAST_FORWARD,
            KeyEvent.KEYCODE_MEDIA_SKIP_FORWARD,
            KeyEvent.KEYCODE_MEDIA_STEP_FORWARD -> {
                accumulateSeek(SEEK_STEP_SECONDS)
                return true
            }

            // Show/hide controls
            KeyEvent.KEYCODE_DPAD_UP,
            KeyEvent.KEYCODE_DPAD_DOWN -> {
                if (!controlsView.isShowing()) {
                    controlsView.show()
                    return true
                }
                return super.onKeyDown(keyCode, event)
            }

            KeyEvent.KEYCODE_MENU -> {
                controlsView.toggle()
                return true
            }

            // Back
            KeyEvent.KEYCODE_BACK -> {
                if (controlsView.isShowing()) {
                    controlsView.hide()
                    return true
                }
                finishWithResult()
                return true
            }

            KeyEvent.KEYCODE_MEDIA_STOP -> {
                finishWithResult()
                return true
            }
        }

        return super.onKeyDown(keyCode, event)
    }

    // ========== Seek accumulation ==========

    private fun accumulateSeek(stepSeconds: Int) {
        seekAccumulator += stepSeconds
        val label = if (seekAccumulator >= 0) "+${seekAccumulator}s" else "${seekAccumulator}s"
        controlsView.showSeekIndicator(label)
        mainHandler.removeCallbacks(applySeekRunnable)
        mainHandler.postDelayed(applySeekRunnable, SEEK_APPLY_DELAY_MS)
    }

    private fun applyAccumulatedSeek() {
        if (seekAccumulator == 0) return
        val targetPos = (currentPosition + seekAccumulator).coerceIn(0.0, currentDuration)
        seekAccumulator = 0
        controlsView.hideSeekIndicator()
        Log.d(TAG, "Seeking to ${targetPos}s")
        mpvCommand("seek", targetPos.toString(), "absolute")
    }

    private fun togglePlayPause() {
        paused = !paused
        Log.d(TAG, "togglePlayPause: paused=$paused")
        mpvSetProperty("pause", paused)
        controlsView.showPauseIndicator(paused)
        controlsView.updatePlayPauseState(paused)
    }

    // ========== PlayerControlsView.Listener ==========

    override fun onPlayPauseToggle() {
        togglePlayPause()
    }

    override fun onSeekTo(positionSeconds: Int) {
        Log.d(TAG, "onSeekTo: ${positionSeconds}s")
        mpvCommand("seek", positionSeconds.toString(), "absolute")
    }

    override fun onAudioTrackClicked() {
        refreshTrackSelections()
        val currentAudio = audioTracks.find { it.selected }
        val subtitle = if (currentAudio != null) {
            currentAudio.title.ifEmpty { currentAudio.language.ifEmpty { "Track ${currentAudio.mpvId}" } }
        } else "None"
        controlsView.hide()
        TrackPickerDialog.show(this, "Audio Track", subtitle, audioTracks, false, { mpvId ->
            if (mpvId != null) {
                Log.d(TAG, "Audio track selected: mpvId=$mpvId")
                mpvSetProperty("aid", mpvId.toString())
            }
        }) {
            controlsView.show()
        }
    }

    override fun onSubtitleTrackClicked() {
        refreshTrackSelections()
        val currentSub = subtitleTracks.find { it.selected }
        val subtitle = if (currentSub != null) {
            currentSub.title.ifEmpty { currentSub.language.ifEmpty { "Track ${currentSub.mpvId}" } }
        } else "None"
        controlsView.hide()
        TrackPickerDialog.show(this, "Subtitle Track", subtitle, subtitleTracks, true, { mpvId ->
            Log.d(TAG, "Subtitle track selected: mpvId=${mpvId ?: "off"}")
            mpvSetProperty("sid", mpvId?.toString() ?: "no")
        }) {
            controlsView.show()
        }
    }

    override fun onSkipBackward() {
        val targetPos = (currentPosition - 10).coerceAtLeast(0.0)
        Log.d(TAG, "skipBackward: seeking to ${targetPos}s")
        mpvCommand("seek", targetPos.toString(), "absolute")
        controlsView.showSeekIndicator("-10s")
    }

    override fun onSkipForward() {
        val targetPos = (currentPosition + 30).coerceAtMost(currentDuration)
        Log.d(TAG, "skipForward: seeking to ${targetPos}s")
        mpvCommand("seek", targetPos.toString(), "absolute")
        controlsView.showSeekIndicator("+30s")
    }

    override fun onInfoClicked() {
        controlsView.hide()
        val data = StreamInfoDialog.StreamInfoData(
            title = title,
            seasonNumber = seasonNumber,
            episodeNumber = episodeNumber,
            seriesName = seriesName,
            episodeName = episodeName,
            resolution = resolution,
            videoCodec = videoCodec,
            videoBitrate = videoBitrate,
            frameRate = frameRate,
            audioCodec = audioCodec,
            audioChannels = audioChannels,
            audioBitrate = audioBitrate,
            colorTransfer = colorTransfer,
            colorPrimaries = colorPrimaries,
            colorSpace = colorSpace,
            isHDR = isHDR,
            isDolbyVision = isDolbyVision,
            dolbyVisionProfile = dolbyVisionProfile,
            sourcePath = sourcePath,
            passthroughName = passthroughName,
            passthroughDescription = passthroughDescription,
        )
        StreamInfoDialog.show(this, data) {
            controlsView.show()
        }
    }

    override fun onExitClicked() {
        finishWithResult()
    }

    private fun refreshTrackSelections() {
        val currentAid = try { MPVLib.getPropertyInt("aid") } catch (_: Exception) { null }
        val currentSid = try { MPVLib.getPropertyInt("sid") } catch (_: Exception) { null }

        for (i in audioTracks.indices) {
            val t = audioTracks[i]
            audioTracks[i] = t.copy(selected = t.mpvId == currentAid)
        }
        for (i in subtitleTracks.indices) {
            val t = subtitleTracks[i]
            subtitleTracks[i] = t.copy(selected = t.mpvId == currentSid)
        }
    }

    // ========== Finish and result ==========

    private fun finishWithResult(completed: Boolean = false) {
        progressReporter?.sendFinalUpdate()
        progressReporter?.stop()

        val percentWatched = if (currentDuration > 0) (currentPosition / currentDuration * 100.0) else 0.0
        lastResult = Bundle().apply {
            putDouble("lastPosition", currentPosition)
            putBoolean("completed", completed || percentWatched >= 90.0)
            putDouble("percentWatched", percentWatched)
        }
        finish()
    }

    // ========== Lifecycle ==========

    override fun onDestroy() {
        Log.i(TAG, "onDestroy: position=${currentPosition}s, duration=${currentDuration}s")
        destroyed = true

        // Ensure result is saved even if finishWithResult wasn't called
        if (lastResult == null) {
            val percentWatched = if (currentDuration > 0) (currentPosition / currentDuration * 100.0) else 0.0
            lastResult = Bundle().apply {
                putDouble("lastPosition", currentPosition)
                putBoolean("completed", percentWatched >= 90.0)
                putDouble("percentWatched", percentWatched)
            }
        }

        progressReporter?.stop()
        controlsView.cleanup()
        mainHandler.removeCallbacksAndMessages(null)
        mpvHandler.removeCallbacksAndMessages(null)
        mpvThread.quitSafely()

        if (initialized) {
            try { MPVLib.removeLogObserver(this) } catch (_: Exception) {}
            try { MPVLib.removeObserver(this) } catch (_: Exception) {}
            try { MPVLib.destroy() } catch (_: Exception) {}
        }
        surfaceView.holder.removeCallback(this)

        super.onDestroy()
    }
}
