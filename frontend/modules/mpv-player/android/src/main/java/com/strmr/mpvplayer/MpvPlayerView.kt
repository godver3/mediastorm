package com.strmr.mpvplayer

import android.app.ActivityManager
import android.content.ComponentCallbacks2
import android.content.Context
import android.content.res.Configuration
import android.os.Handler
import android.os.Looper
import android.os.SystemClock
import android.util.Log
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.widget.FrameLayout
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.ReactContext
import com.facebook.react.bridge.ReadableMap
import com.facebook.react.bridge.WritableArray
import com.facebook.react.bridge.WritableMap
import com.facebook.react.uimanager.ThemedReactContext
import com.facebook.react.uimanager.events.RCTEventEmitter
import dev.jdtech.mpv.MPVLib

class MpvPlayerView(context: ThemedReactContext) :
    FrameLayout(context), MPVLib.EventObserver, SurfaceHolder.Callback, ComponentCallbacks2 {

    companion object {
        private const val TAG = "MpvPlayerView"

        // mpv property format constants (from mpv/client.h — stable across versions)
        private const val MPV_FORMAT_FLAG = 3
        private const val MPV_FORMAT_INT64 = 4
        private const val MPV_FORMAT_DOUBLE = 5

        // mpv event ID constants
        private const val MPV_EVENT_END_FILE = 7
        private const val MPV_EVENT_FILE_LOADED = 8

        // Memory tier thresholds
        private const val LOW_RAM_THRESHOLD_MB = 2048L   // <= 2 GB
        private const val MID_RAM_THRESHOLD_MB = 3072L   // <= 3 GB
    }

    private val surfaceView = SurfaceView(context)
    private val mainHandler = Handler(Looper.getMainLooper())

    private var initialized = false
    private var destroyed = false
    private var surfaceReady = false
    private var fileLoaded = false

    // HDR mode — set via React prop before source is loaded
    var isHDR = false

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

    // Progress throttling
    private var lastProgressEmitTime = 0L
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

            // Subtitle rendering
            MPVLib.setOptionString("sub-visibility", "yes")
            MPVLib.setOptionString("sub-font", "sans-serif")
            MPVLib.setOptionString("sub-font-size", "55")
            MPVLib.setOptionString("sub-use-margins", "yes")

            MPVLib.init()
            MPVLib.addObserver(this)

            // Observe properties
            MPVLib.observeProperty("time-pos", MPV_FORMAT_DOUBLE)
            MPVLib.observeProperty("duration", MPV_FORMAT_DOUBLE)
            MPVLib.observeProperty("track-list/count", MPV_FORMAT_INT64)
            MPVLib.observeProperty("eof-reached", MPV_FORMAT_FLAG)
            MPVLib.observeProperty("paused-for-cache", MPV_FORMAT_FLAG)

            // Register for system memory pressure callbacks
            context.applicationContext.registerComponentCallbacks(this)

            initialized = true
            val voMode = if (isHDR) "gpu-next,gpu (HDR)" else "gpu"
            Log.d(TAG, "MPV initialized (vo=$voMode, RAM=${totalMb}MB, lowRam=$isLowRamDevice, cache: $demuxerMax / $demuxerBack)")
        } catch (e: Exception) {
            Log.e(TAG, "Failed to initialize MPV", e)
            mainHandler.post { emitError("Failed to initialize MPV: ${e.message}") }
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
            MPVLib.setOptionString("vo", "gpu-next,gpu")
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
        MPVLib.setPropertyString("android-surface-size", "${width}x${height}")
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

    fun setSource(source: ReadableMap?) {
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

        // Set headers before loading
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
            emitError("Failed to load file: ${e.message}")
        }
    }

    fun setPaused(paused: Boolean) {
        if (!initialized || destroyed) return
        try {
            MPVLib.setPropertyBoolean("pause", paused)
        } catch (e: Exception) {
            Log.w(TAG, "Failed to set pause", e)
        }
    }

    fun setVolume(volume: Float) {
        if (!initialized || destroyed) return
        val mpvVolume = (volume.coerceIn(0f, 1f) * 100).toInt()
        try {
            MPVLib.setPropertyInt("volume", mpvVolume)
        } catch (e: Exception) {
            Log.w(TAG, "Failed to set volume", e)
        }
    }

    fun setRate(rate: Float) {
        if (!initialized || destroyed) return
        try {
            MPVLib.setPropertyDouble("speed", rate.toDouble())
        } catch (e: Exception) {
            Log.w(TAG, "Failed to set rate", e)
        }
    }

    fun setAudioTrack(rnIndex: Int) {
        if (!initialized || destroyed) return
        if (rnIndex < 0) return

        if (!tracksAvailable) {
            pendingAudioTrack = rnIndex
            return
        }

        val mpvId = audioIndexToMpvId[rnIndex]
        if (mpvId != null) {
            try {
                MPVLib.setPropertyString("aid", mpvId.toString())
                Log.d(TAG, "Set audio track: rnIndex=$rnIndex -> mpvId=$mpvId")
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set audio track", e)
            }
        } else {
            Log.w(TAG, "No mpv track ID for audio index $rnIndex")
        }
    }

    fun setSubtitleTrack(rnIndex: Int) {
        if (!initialized || destroyed) return

        if (rnIndex < 0) {
            try {
                MPVLib.setPropertyString("sid", "no")
                Log.d(TAG, "Disabled subtitles")
            } catch (e: Exception) {
                Log.w(TAG, "Failed to disable subtitles", e)
            }
            return
        }

        if (!tracksAvailable) {
            pendingSubtitleTrack = rnIndex
            return
        }

        val mpvId = subtitleIndexToMpvId[rnIndex]
        if (mpvId != null) {
            try {
                MPVLib.setPropertyString("sid", mpvId.toString())
                Log.d(TAG, "Set subtitle track: rnIndex=$rnIndex -> mpvId=$mpvId")
            } catch (e: Exception) {
                Log.w(TAG, "Failed to set subtitle track", e)
            }
        } else {
            Log.w(TAG, "No mpv track ID for subtitle index $rnIndex")
        }
    }

    fun setSubtitleSize(size: Float) {
        if (!initialized || destroyed || size <= 0) return
        try {
            MPVLib.setPropertyString("sub-font-size", size.toInt().toString())
        } catch (e: Exception) {
            Log.w(TAG, "Failed to set subtitle size", e)
        }
    }

    fun setSubtitleColor(color: String?) {
        if (!initialized || destroyed || color == null) return
        try {
            MPVLib.setPropertyString("sub-color", color)
        } catch (e: Exception) {
            Log.w(TAG, "Failed to set subtitle color", e)
        }
    }

    fun setSubtitlePosition(position: Float) {
        if (!initialized || destroyed) return
        try {
            MPVLib.setPropertyString("sub-margin-y", position.toInt().toString())
        } catch (e: Exception) {
            Log.w(TAG, "Failed to set subtitle position", e)
        }
    }

    fun seekTo(time: Double) {
        if (!initialized || destroyed) return
        try {
            MPVLib.command(arrayOf("seek", time.toString(), "absolute"))
        } catch (e: Exception) {
            Log.w(TAG, "Failed to seek", e)
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
                val result = buildTrackList(count)
                mainHandler.post {
                    tracksAvailable = true
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
        // Not used currently
    }

    override fun event(eventId: Int) {
        when (eventId) {
            MPV_EVENT_FILE_LOADED -> {
                val duration = MPVLib.getPropertyDouble("duration") ?: 0.0
                val width = MPVLib.getPropertyInt("width") ?: 0
                val height = MPVLib.getPropertyInt("height") ?: 0
                currentDuration = duration
                mainHandler.post {
                    fileLoaded = true
                    emitLoad(duration, width, height)
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

        return Pair(audioTracks, subtitleTracks)
    }

    private fun applyPendingTracks() {
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
        val reactContext = context as? ReactContext ?: return
        reactContext.getJSModule(RCTEventEmitter::class.java)
            .receiveEvent(id, eventName, data)
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

    fun destroy() {
        if (destroyed) return
        destroyed = true
        Log.d(TAG, "Destroying MpvPlayerView")

        mainHandler.removeCallbacksAndMessages(null)

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
                MPVLib.destroy()
            } catch (e: Exception) {
                Log.w(TAG, "Failed to destroy MPV", e)
            }
        }

        surfaceView.holder.removeCallback(this)
        initialized = false
    }
}
