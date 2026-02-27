package com.strmr.mpvplayer

import android.app.ActivityManager
import android.content.ComponentCallbacks2
import android.content.Context
import android.content.res.Configuration
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.util.Log
import android.view.Gravity
import android.view.SurfaceHolder
import android.view.SurfaceView
import android.widget.FrameLayout
import androidx.annotation.OptIn
import androidx.media3.common.C
import androidx.media3.common.MediaItem
import androidx.media3.common.MimeTypes
import androidx.media3.common.PlaybackException
import androidx.media3.common.Player
import androidx.media3.common.TrackSelectionOverride
import androidx.media3.common.Tracks
import androidx.media3.common.VideoSize
import androidx.media3.common.text.CueGroup
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.DefaultHttpDataSource
import androidx.media3.exoplayer.DefaultRenderersFactory
import androidx.media3.exoplayer.ExoPlayer
import androidx.media3.exoplayer.DefaultLoadControl
import androidx.media3.exoplayer.source.DefaultMediaSourceFactory
import androidx.media3.exoplayer.trackselection.DefaultTrackSelector
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.ReadableMap
import com.facebook.react.bridge.WritableMap
import com.facebook.react.uimanager.ThemedReactContext

/**
 * ExoPlayer-based player for Dolby Vision content on Android.
 *
 * Uses Media3 ExoPlayer with DefaultRenderersFactory which auto-selects
 * the Dolby decoder (c2.dolby.decoder.hevc) when available, enabling
 * native DV output that mpv cannot provide.
 */
@OptIn(UnstableApi::class)
class ExoPlayerView(
    context: ThemedReactContext,
    private val eventEmitter: (String, WritableMap?) -> Unit
) : FrameLayout(context), PlayerViewDelegate, ComponentCallbacks2 {

    companion object {
        private const val TAG = "ExoPlayerView"
        private const val PROGRESS_INTERVAL_MS = 500L

        // Memory tier thresholds — match MpvPlayerView
        private const val LOW_RAM_THRESHOLD_MB = 2048L   // <= 2 GB
        private const val MID_RAM_THRESHOLD_MB = 3072L   // <= 3 GB
    }

    private val isLowRamDevice: Boolean
    private val totalMb: Long

    override var isHDR: Boolean = false

    private val mainHandler = Handler(Looper.getMainLooper())
    private var player: ExoPlayer? = null
    private var trackSelector: DefaultTrackSelector? = null
    private var destroyed = false
    private var currentUri: String? = null

    // Track mapping: RN 0-based index -> ExoPlayer TrackGroup/track index pair
    private data class TrackRef(val groupIndex: Int, val trackIndex: Int, val rendererIndex: Int)
    private val audioIndexToTrackRef = mutableMapOf<Int, TrackRef>()
    private val subtitleIndexToTrackRef = mutableMapOf<Int, TrackRef>()

    // Pending track selections
    private var pendingAudioTrack: Int? = null
    private var pendingSubtitleTrack: Int? = null
    private var tracksAvailable = false

    // Progress timer
    private val progressRunnable = object : Runnable {
        override fun run() {
            emitProgressUpdate()
            mainHandler.postDelayed(this, PROGRESS_INTERVAL_MS)
        }
    }

    // Buffered state — applied after ExoPlayer is created in initializePlayer().
    // Container calls replayBufferedProps() (setPaused, setVolume, etc.) BEFORE
    // setSource() creates the ExoPlayer instance, so these store the values.
    private var bufferedPaused: Boolean = true
    private var bufferedVolume: Float = 1f
    private var bufferedRate: Float = 1f

    // Auth headers for HTTP requests
    private var currentHeaders: Map<String, String>? = null

    // Video aspect ratio — used to letterbox the SurfaceView
    private var videoWidth = 0
    private var videoHeight = 0
    private var videoPixelRatio = 1f

    // External subtitle URL
    private var pendingExternalSubUrl: String? = null

    // Surface state — managed explicitly via SurfaceHolder.Callback (like MpvPlayerView)
    private var surfaceReady = false

    // Pending source — set before surface is ready, loaded once surface arrives
    private var pendingInitUri: String? = null
    private var pendingInitHeaders: Map<String, String>? = null

    private val surfaceView = SurfaceView(context).apply {
        // Match mpv's z-order so PiP can find and reparent this surface
        setZOrderMediaOverlay(true)
    }

    private val surfaceCallback = object : SurfaceHolder.Callback {
        override fun surfaceCreated(holder: SurfaceHolder) {
            Log.i(TAG, "surfaceCreated")
            emitDebugLog("surfaceCreated")
            surfaceReady = true
            // Attach surface to existing player
            player?.setVideoSurface(holder.surface)
            // If source was set before surface, initialize now
            pendingInitUri?.let { uri ->
                val headers = pendingInitHeaders
                pendingInitUri = null
                pendingInitHeaders = null
                initializePlayer(uri, headers)
            }
        }

        override fun surfaceChanged(holder: SurfaceHolder, format: Int, width: Int, height: Int) {
            Log.d(TAG, "surfaceChanged: ${width}x${height}")
        }

        override fun surfaceDestroyed(holder: SurfaceHolder) {
            Log.i(TAG, "surfaceDestroyed")
            surfaceReady = false
            player?.setVideoSurface(null)
        }
    }

    init {
        // Detect device memory — match MpvPlayerView's tiered approach
        val am = context.getSystemService(Context.ACTIVITY_SERVICE) as ActivityManager
        val memInfo = ActivityManager.MemoryInfo()
        am.getMemoryInfo(memInfo)
        totalMb = memInfo.totalMem / (1024 * 1024)
        isLowRamDevice = am.isLowRamDevice || totalMb <= LOW_RAM_THRESHOLD_MB

        // Tag identically to mpv so PipManagerModule can find it
        surfaceView.tag = "mpv_player_surface"
        surfaceView.holder.addCallback(surfaceCallback)
        addView(surfaceView, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT).apply {
            gravity = Gravity.CENTER
        })

        // Register for system memory pressure callbacks
        context.applicationContext.registerComponentCallbacks(this)
    }

    /**
     * Directly measure and layout the SurfaceView to maintain the video's aspect ratio,
     * centered with letterboxing (black bars).
     *
     * We bypass LayoutParams + requestLayout() because React Native's Yoga layout engine
     * intercepts requestLayout() from programmatically-added children and may never trigger
     * a standard Android layout pass. Direct measure()+layout() is the reliable approach.
     */
    override fun onLayout(changed: Boolean, left: Int, top: Int, right: Int, bottom: Int) {
        val containerW = right - left
        val containerH = bottom - top

        if (videoWidth > 0 && videoHeight > 0 && containerW > 0 && containerH > 0) {
            val displayWidth = videoWidth * videoPixelRatio
            val videoAspect = displayWidth / videoHeight
            val containerAspect = containerW.toFloat() / containerH

            val (targetW, targetH) = if (videoAspect > containerAspect) {
                containerW to (containerW / videoAspect).toInt()
            } else {
                (containerH * videoAspect).toInt() to containerH
            }

            val childLeft = (containerW - targetW) / 2
            val childTop = (containerH - targetH) / 2
            surfaceView.measure(
                MeasureSpec.makeMeasureSpec(targetW, MeasureSpec.EXACTLY),
                MeasureSpec.makeMeasureSpec(targetH, MeasureSpec.EXACTLY)
            )
            surfaceView.layout(childLeft, childTop, childLeft + targetW, childTop + targetH)
            Log.d(TAG, "onLayout: surface=${targetW}x${targetH} at ($childLeft,$childTop) (video=${videoWidth}x${videoHeight}, container=${containerW}x${containerH})")
        } else {
            surfaceView.measure(
                MeasureSpec.makeMeasureSpec(containerW, MeasureSpec.EXACTLY),
                MeasureSpec.makeMeasureSpec(containerH, MeasureSpec.EXACTLY)
            )
            surfaceView.layout(0, 0, containerW, containerH)
        }
    }

    /**
     * Force a layout pass to resize the SurfaceView after video dimensions change.
     * Cannot rely on requestLayout() in RN's view hierarchy, so directly call onLayout().
     */
    private fun applySurfaceSize() {
        val containerW = width
        val containerH = height
        if (containerW > 0 && containerH > 0) {
            onLayout(true, left, top, right, bottom)
        }
    }

    /**
     * Build a LoadControl with buffer limits tuned to device RAM.
     * DV content is high bitrate — default 50s buffer can easily exhaust
     * low-RAM devices like Fire Stick.
     */
    private fun buildLoadControl(): DefaultLoadControl {
        val (minBufferMs, maxBufferMs, backBufferMs) = when {
            isLowRamDevice -> {
                Log.d(TAG, "Low-RAM device (${totalMb}MB) — tight buffer limits")
                Triple(5_000, 15_000, 0)
            }
            totalMb <= MID_RAM_THRESHOLD_MB -> {
                Log.d(TAG, "Mid-RAM device (${totalMb}MB) — moderate buffer limits")
                Triple(10_000, 30_000, 10_000)
            }
            else -> {
                Log.d(TAG, "High-RAM device (${totalMb}MB) — standard buffer limits")
                Triple(15_000, 50_000, 30_000)
            }
        }

        return DefaultLoadControl.Builder()
            .setBufferDurationsMs(
                minBufferMs,
                maxBufferMs,
                DefaultLoadControl.DEFAULT_BUFFER_FOR_PLAYBACK_MS,
                DefaultLoadControl.DEFAULT_BUFFER_FOR_PLAYBACK_AFTER_REBUFFER_MS
            )
            .setBackBuffer(backBufferMs, false)
            .build()
    }

    private fun initializePlayer(uri: String, headers: Map<String, String>?) {
        if (destroyed) return

        if (!surfaceReady) {
            // Surface not ready yet — defer until surfaceCreated fires
            Log.i(TAG, "Surface not ready, deferring init for: ${uri.takeLast(60)}")
            emitDebugLog("Surface not ready, deferring player init")
            pendingInitUri = uri
            pendingInitHeaders = headers
            return
        }

        // Clean up existing player
        releasePlayer()

        val ctx = context.applicationContext

        trackSelector = DefaultTrackSelector(ctx).apply {
            setParameters(
                buildUponParameters()
                    .setPreferredVideoMimeType(MimeTypes.VIDEO_DOLBY_VISION)
            )
        }

        val renderersFactory = DefaultRenderersFactory(ctx)
            .setExtensionRendererMode(DefaultRenderersFactory.EXTENSION_RENDERER_MODE_PREFER)

        // HTTP data source with auth headers
        val httpFactory = DefaultHttpDataSource.Factory()
        if (!headers.isNullOrEmpty()) {
            httpFactory.setDefaultRequestProperties(headers)
        }

        val mediaSourceFactory = DefaultMediaSourceFactory(httpFactory)

        val loadControl = buildLoadControl()

        player = ExoPlayer.Builder(ctx, renderersFactory)
            .setTrackSelector(trackSelector!!)
            .setMediaSourceFactory(mediaSourceFactory)
            .setLoadControl(loadControl)
            .build()
            .also { exo ->
                // Explicitly connect to the surface (not setVideoSurfaceView)
                exo.setVideoSurface(surfaceView.holder.surface)
                exo.addListener(playerListener)

                // Build media item with optional external subtitle
                val mediaItemBuilder = MediaItem.Builder().setUri(uri)

                pendingExternalSubUrl?.let { subUrl ->
                    if (subUrl.isNotEmpty()) {
                        val subConfig = MediaItem.SubtitleConfiguration.Builder(Uri.parse(subUrl))
                            .setMimeType(MimeTypes.TEXT_VTT)
                            .setSelectionFlags(C.SELECTION_FLAG_DEFAULT)
                            .build()
                        mediaItemBuilder.setSubtitleConfigurations(listOf(subConfig))
                        Log.d(TAG, "Added external subtitle: $subUrl")
                    }
                    pendingExternalSubUrl = null
                }

                exo.setMediaItem(mediaItemBuilder.build())
                exo.prepare()

                // Apply buffered state — these may have been set before ExoPlayer existed
                exo.playWhenReady = !bufferedPaused
                exo.volume = bufferedVolume.coerceIn(0f, 1f)
                exo.setPlaybackSpeed(bufferedRate)
                keepScreenOn = !bufferedPaused

                Log.i(TAG, "ExoPlayer initialized: surface=${surfaceView.holder.surface}, paused=$bufferedPaused")
                emitDebugLog("ExoPlayer initialized (surface=${surfaceView.holder.surface.isValid}, paused=$bufferedPaused)")
            }
    }

    private val playerListener = object : Player.Listener {
        override fun onPlaybackStateChanged(playbackState: Int) {
            when (playbackState) {
                Player.STATE_BUFFERING -> {
                    emitBuffering(true)
                }
                Player.STATE_READY -> {
                    emitBuffering(false)
                    val p = player ?: return
                    val duration = p.duration / 1000.0
                    val format = p.videoFormat
                    val width = format?.width ?: 0
                    val height = format?.height ?: 0
                    val codecName = format?.codecs ?: "unknown"
                    emitDebugLog("STATE_READY: ${width}x${height}, dur=${duration}s, codec=$codecName")
                    emitLoad(duration, width, height)
                    startProgressTimer()
                }
                Player.STATE_ENDED -> {
                    stopProgressTimer()
                    emitEnd()
                }
                Player.STATE_IDLE -> {
                    // Nothing
                }
            }
        }

        override fun onPlayerError(error: PlaybackException) {
            Log.e(TAG, "ExoPlayer error: ${error.message}", error)
            emitError("ExoPlayer error: ${error.message}")
        }

        override fun onTracksChanged(tracks: Tracks) {
            buildTrackList(tracks)
        }

        override fun onVideoSizeChanged(videoSize: VideoSize) {
            videoWidth = videoSize.width
            videoHeight = videoSize.height
            videoPixelRatio = videoSize.pixelWidthHeightRatio
            Log.d(TAG, "Video size: ${videoWidth}x${videoHeight}, pixelRatio=$videoPixelRatio")
            applySurfaceSize()
        }

        override fun onCues(cueGroup: CueGroup) {
            val text = cueGroup.cues.joinToString("\n") { it.text?.toString() ?: "" }
            emitSubtitleText(text)
        }
    }

    // ========== PlayerViewDelegate implementation ==========

    override fun setSource(source: ReadableMap?) {
        source ?: return
        val uri = source.getString("uri") ?: return
        if (uri == currentUri) return
        currentUri = uri

        // Parse headers
        var headerMap: Map<String, String>? = null
        if (source.hasKey("headers")) {
            val headers = source.getMap("headers")
            if (headers != null) {
                val map = mutableMapOf<String, String>()
                val hashMap = headers.toHashMap()
                for ((key, value) in hashMap) {
                    map[key] = value.toString()
                }
                if (map.isNotEmpty()) {
                    headerMap = map
                }
            }
        }

        currentHeaders = headerMap
        tracksAvailable = false
        audioIndexToTrackRef.clear()
        subtitleIndexToTrackRef.clear()

        initializePlayer(uri, headerMap)
    }

    override fun setPaused(paused: Boolean) {
        bufferedPaused = paused
        val p = player ?: return
        p.playWhenReady = !paused
        keepScreenOn = !paused
        if (paused) {
            stopProgressTimer()
        } else if (p.playbackState == Player.STATE_READY) {
            startProgressTimer()
        }
    }

    override fun setVolume(volume: Float) {
        bufferedVolume = volume
        player?.volume = volume.coerceIn(0f, 1f)
    }

    override fun setRate(rate: Float) {
        bufferedRate = rate
        player?.setPlaybackSpeed(rate)
    }

    override fun setAudioTrack(rnIndex: Int) {
        if (rnIndex < 0) return

        if (!tracksAvailable) {
            pendingAudioTrack = rnIndex
            return
        }

        val ref = audioIndexToTrackRef[rnIndex] ?: run {
            Log.w(TAG, "No ExoPlayer track ref for audio index $rnIndex")
            return
        }

        val ts = trackSelector ?: return
        val p = player ?: return
        val trackGroups = p.currentTracks.groups
        if (ref.groupIndex < trackGroups.size) {
            val group = trackGroups[ref.groupIndex]
            ts.setParameters(
                ts.buildUponParameters()
                    .addOverride(TrackSelectionOverride(group.mediaTrackGroup, ref.trackIndex))
            )
            Log.d(TAG, "Set audio track: rnIndex=$rnIndex -> group=${ref.groupIndex}, track=${ref.trackIndex}")
        }
    }

    override fun setSubtitleTrack(rnIndex: Int) {
        if (rnIndex < 0) {
            // Disable subtitles
            val ts = trackSelector ?: return
            ts.setParameters(
                ts.buildUponParameters()
                    .setRendererDisabled(getTextRendererIndex(), true)
            )
            return
        }

        if (!tracksAvailable) {
            pendingSubtitleTrack = rnIndex
            return
        }

        val ref = subtitleIndexToTrackRef[rnIndex] ?: run {
            Log.w(TAG, "No ExoPlayer track ref for subtitle index $rnIndex")
            return
        }

        val ts = trackSelector ?: return
        val p = player ?: return
        val trackGroups = p.currentTracks.groups
        if (ref.groupIndex < trackGroups.size) {
            val group = trackGroups[ref.groupIndex]
            ts.setParameters(
                ts.buildUponParameters()
                    .setRendererDisabled(getTextRendererIndex(), false)
                    .addOverride(TrackSelectionOverride(group.mediaTrackGroup, ref.trackIndex))
            )
            Log.d(TAG, "Set subtitle track: rnIndex=$rnIndex -> group=${ref.groupIndex}, track=${ref.trackIndex}")
        }
    }

    override fun setSubtitleSize(size: Float) {
        // ExoPlayer subtitle styling is limited — handled by RN overlay
    }

    override fun setSubtitleColor(color: String?) {
        // Handled by RN subtitle overlay
    }

    override fun setSubtitlePosition(position: Float) {
        // Handled by RN subtitle overlay
    }

    override fun setSubtitleStyle(style: ReadableMap?) {
        // Handled by RN subtitle overlay
    }

    override fun setControlsVisible(visible: Boolean) {
        // No subtitle margin adjustment needed — RN overlay handles positioning
    }

    override fun setExternalSubtitleUrl(url: String?) {
        val effectiveUrl = if (url.isNullOrEmpty()) null else url

        if (player != null && currentUri != null) {
            // Player already initialized — rebuild media item with subtitle
            val p = player ?: return
            val currentPos = p.currentPosition
            val wasPlaying = p.playWhenReady

            val mediaItemBuilder = MediaItem.Builder().setUri(currentUri!!)
            if (effectiveUrl != null) {
                val subConfig = MediaItem.SubtitleConfiguration.Builder(Uri.parse(effectiveUrl))
                    .setMimeType(MimeTypes.TEXT_VTT)
                    .setSelectionFlags(C.SELECTION_FLAG_DEFAULT)
                    .build()
                mediaItemBuilder.setSubtitleConfigurations(listOf(subConfig))
            }

            p.setMediaItem(mediaItemBuilder.build())
            p.prepare()
            p.seekTo(currentPos)
            p.playWhenReady = wasPlaying
            Log.d(TAG, "Reloaded with external subtitle: $effectiveUrl")
        } else {
            // Buffer for when player is created
            pendingExternalSubUrl = effectiveUrl
        }
    }

    override fun seekTo(time: Double) {
        player?.seekTo((time * 1000).toLong())
    }

    override fun destroy() {
        if (destroyed) return
        destroyed = true
        Log.d(TAG, "Destroying ExoPlayerView")
        stopProgressTimer()
        surfaceView.holder.removeCallback(surfaceCallback)
        try {
            context.applicationContext.unregisterComponentCallbacks(this)
        } catch (_: Exception) {}
        releasePlayer()
    }

    // ========== Internal helpers ==========

    private fun releasePlayer() {
        player?.let { p ->
            p.removeListener(playerListener)
            p.release()
        }
        player = null
        trackSelector = null
    }

    private fun getTextRendererIndex(): Int {
        val p = player ?: return 2 // sensible default
        for (i in 0 until p.rendererCount) {
            if (p.getRendererType(i) == C.TRACK_TYPE_TEXT) return i
        }
        return 2
    }

    private fun buildTrackList(tracks: Tracks) {
        val audioTracks = Arguments.createArray()
        val subtitleTracks = Arguments.createArray()
        val newAudioMap = mutableMapOf<Int, TrackRef>()
        val newSubtitleMap = mutableMapOf<Int, TrackRef>()
        var audioIndex = 0
        var subtitleIndex = 0

        for ((groupIndex, group) in tracks.groups.withIndex()) {
            val trackType = group.type
            for (trackIndex in 0 until group.length) {
                val format = group.getTrackFormat(trackIndex)
                val isSelected = group.isTrackSelected(trackIndex)
                val rendererIndex = when (trackType) {
                    C.TRACK_TYPE_AUDIO -> 1
                    C.TRACK_TYPE_TEXT -> getTextRendererIndex()
                    else -> continue
                }

                when (trackType) {
                    C.TRACK_TYPE_AUDIO -> {
                        val track = Arguments.createMap().apply {
                            putInt("id", audioIndex)
                            putString("type", "audio")
                            putString("title", format.label ?: "")
                            putString("language", format.language ?: "")
                            putString("codec", format.codecs ?: format.sampleMimeType ?: "")
                            putBoolean("selected", isSelected)
                        }
                        audioTracks.pushMap(track)
                        newAudioMap[audioIndex] = TrackRef(groupIndex, trackIndex, rendererIndex)
                        audioIndex++
                    }
                    C.TRACK_TYPE_TEXT -> {
                        val track = Arguments.createMap().apply {
                            putInt("id", subtitleIndex)
                            putString("type", "subtitle")
                            putString("title", format.label ?: "")
                            putString("language", format.language ?: "")
                            putString("codec", format.codecs ?: format.sampleMimeType ?: "")
                            putBoolean("selected", isSelected)
                        }
                        subtitleTracks.pushMap(track)
                        newSubtitleMap[subtitleIndex] = TrackRef(groupIndex, trackIndex, rendererIndex)
                        subtitleIndex++
                    }
                }
            }
        }

        audioIndexToTrackRef.clear()
        audioIndexToTrackRef.putAll(newAudioMap)
        subtitleIndexToTrackRef.clear()
        subtitleIndexToTrackRef.putAll(newSubtitleMap)
        tracksAvailable = true

        emitDebugLog("Tracks: $audioIndex audio, $subtitleIndex subtitle")

        val data = Arguments.createMap().apply {
            putArray("audioTracks", audioTracks)
            putArray("subtitleTracks", subtitleTracks)
        }
        emitEvent("onTracksChanged", data)

        // Apply pending tracks
        pendingAudioTrack?.let { idx ->
            pendingAudioTrack = null
            setAudioTrack(idx)
        }
        pendingSubtitleTrack?.let { idx ->
            pendingSubtitleTrack = null
            setSubtitleTrack(idx)
        }
    }

    // ========== Progress timer ==========

    private fun startProgressTimer() {
        mainHandler.removeCallbacks(progressRunnable)
        mainHandler.post(progressRunnable)
    }

    private fun stopProgressTimer() {
        mainHandler.removeCallbacks(progressRunnable)
    }

    private fun emitProgressUpdate() {
        val p = player ?: return
        if (p.playbackState != Player.STATE_READY && p.playbackState != Player.STATE_BUFFERING) return
        val currentTime = p.currentPosition / 1000.0
        val duration = p.duration / 1000.0
        emitProgress(currentTime, duration)
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
            putString("message", "[ExoPlayer-DV] $message")
        }
        emitEvent("onDebugLog", data)
    }

    // ========== ComponentCallbacks2 (memory pressure) ==========

    override fun onTrimMemory(level: Int) {
        if (destroyed) return
        // Log memory pressure but do NOT pause playback — on low-RAM Android TV devices,
        // CRITICAL trim callbacks fire during normal 4K DV playback. The tiered
        // DefaultLoadControl buffer limits (set in buildLoadControl) are the real
        // OOM protection. Pausing here would prevent video from ever playing.
        when {
            level >= ComponentCallbacks2.TRIM_MEMORY_RUNNING_CRITICAL -> {
                Log.w(TAG, "CRITICAL memory pressure (level=$level) — LoadControl buffer limits active")
                emitDebugLog("Memory pressure CRITICAL (level=$level), buffer limits active")
            }
            level >= ComponentCallbacks2.TRIM_MEMORY_RUNNING_LOW -> {
                Log.w(TAG, "Low memory pressure (level=$level)")
            }
        }
    }

    override fun onConfigurationChanged(newConfig: Configuration) {
        // Required by ComponentCallbacks2, no action needed
    }

    override fun onLowMemory() {
        onTrimMemory(ComponentCallbacks2.TRIM_MEMORY_RUNNING_CRITICAL)
    }
}
