package com.strmr.mpvplayer

import android.app.ActivityManager
import android.content.ComponentCallbacks2
import android.content.Context
import android.content.res.Configuration
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.RectF
import android.graphics.Typeface
import android.util.Log
import android.util.TypedValue
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
import androidx.media3.common.text.Cue
import androidx.media3.common.text.CueGroup
import androidx.media3.common.util.UnstableApi
import androidx.media3.ui.CaptionStyleCompat
import androidx.media3.ui.SubtitleView
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

        // Base subtitle size in SP — matches RN overlay's Android TV size
        private const val BASE_SUBTITLE_SIZE_SP = 26f

        // PGS authoring reference height — PGS mastered at this resolution
        // gets a 1.0 base scale when displayed on a matching video.
        private const val PGS_REFERENCE_HEIGHT = 1080f
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

    // Audio renderer error recovery — ExoPlayer's MediaCodecAudioRenderer can fail when
    // switching between audio codecs (e.g., EAC3 → AC3 commentary track). We recover by
    // re-preparing at the current position, which forces codec re-initialization.
    private var audioErrorRecoveryAttempted = false

    // Last user-requested audio track — survives error recovery cycles.
    // When prepare() is called during recovery, all TrackSelectionOverrides become stale
    // (they reference old MediaTrackGroup objects). We re-apply via pendingAudioTrack.
    private var lastRequestedAudioTrack: Int? = null

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

    // Subtitle styling state
    private var currentFgColor = Color.WHITE
    private var currentBgColor = 0x99000000.toInt()
    private var baseSubtitleMarginY = 50
    private var controlsVisible = false
    private var userBitmapMultiplier = 1.0f

    // PGS bitmap cue cache — avoids per-frame Bitmap allocation + Canvas draw.
    // Invalidated when source bitmap, scale, or background color changes.
    private var cachedSrcBitmap: Bitmap? = null
    private var cachedPaddedBitmap: Bitmap? = null
    private var cachedPaddedCue: Cue? = null
    private var cachedBitmapScale = 0f
    private var cachedBitmapBgColor = currentBgColor
    private val bgPaint = Paint(Paint.ANTI_ALIAS_FLAG)

    // External subtitle URL
    private var pendingExternalSubUrl: String? = null

    // Surface state — managed explicitly via SurfaceHolder.Callback (like MpvPlayerView)
    private var surfaceReady = false

    // Pending source — set before surface is ready, loaded once surface arrives
    private var pendingInitUri: String? = null
    private var pendingInitHeaders: Map<String, String>? = null

    private val surfaceView = SurfaceView(context)

    private val subtitleView = SubtitleView(context).apply {
        setStyle(
            CaptionStyleCompat(
                Color.WHITE,
                0x99000000.toInt(), // 60% black background
                Color.TRANSPARENT,
                CaptionStyleCompat.EDGE_TYPE_OUTLINE,
                Color.BLACK,
                Typeface.DEFAULT_BOLD
            )
        )
        setFixedTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
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
        addView(subtitleView, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT).apply {
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
            subtitleView.measure(
                MeasureSpec.makeMeasureSpec(targetW, MeasureSpec.EXACTLY),
                MeasureSpec.makeMeasureSpec(targetH, MeasureSpec.EXACTLY)
            )
            subtitleView.layout(childLeft, childTop, childLeft + targetW, childTop + targetH)
            Log.d(TAG, "onLayout: surface=${targetW}x${targetH} at ($childLeft,$childTop) (video=${videoWidth}x${videoHeight}, container=${containerW}x${containerH})")
        } else {
            surfaceView.measure(
                MeasureSpec.makeMeasureSpec(containerW, MeasureSpec.EXACTLY),
                MeasureSpec.makeMeasureSpec(containerH, MeasureSpec.EXACTLY)
            )
            surfaceView.layout(0, 0, containerW, containerH)
            subtitleView.measure(
                MeasureSpec.makeMeasureSpec(containerW, MeasureSpec.EXACTLY),
                MeasureSpec.makeMeasureSpec(containerH, MeasureSpec.EXACTLY)
            )
            subtitleView.layout(0, 0, containerW, containerH)
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
                Triple(5_000, 10_000, 0)
            }
            totalMb <= MID_RAM_THRESHOLD_MB -> {
                Log.d(TAG, "Mid-RAM device (${totalMb}MB) — moderate buffer limits")
                Triple(8_000, 20_000, 5_000)
            }
            else -> {
                Log.d(TAG, "High-RAM device (${totalMb}MB) — reduced buffer limits for DV")
                Triple(10_000, 30_000, 10_000)
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
                    // Reset recovery flag — playback resumed successfully after any prior error
                    audioErrorRecoveryAttempted = false
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

            // Attempt recovery for audio renderer errors (common when switching codecs).
            // ExoPlayer's MediaCodecAudioRenderer can fail to seamlessly transition between
            // different audio codecs (e.g., EAC3 main → AC3 commentary). Re-preparing at
            // the current position forces codec re-initialization.
            val isAudioRendererError = error.message?.contains("AudioRenderer") == true ||
                error.message?.contains("audio") == true
            if (isAudioRendererError && !audioErrorRecoveryAttempted) {
                audioErrorRecoveryAttempted = true
                val p = player ?: run {
                    emitError("ExoPlayer error: ${error.message}")
                    return
                }
                val pos = p.currentPosition
                val wasPlaying = p.playWhenReady
                Log.i(TAG, "Attempting audio error recovery at ${pos}ms, lastRequestedAudioTrack=$lastRequestedAudioTrack")
                emitDebugLog("Audio codec error — attempting recovery at ${pos}ms")

                // prepare() creates new MediaTrackGroup objects, making any existing
                // TrackSelectionOverride stale. Re-queue the user's track selection so
                // buildTrackList() re-applies it after the new track groups arrive.
                lastRequestedAudioTrack?.let { track ->
                    pendingAudioTrack = track
                    tracksAvailable = false
                    Log.d(TAG, "Re-queued audio track $track as pending for post-recovery")
                }

                p.prepare()
                p.seekTo(pos)
                p.playWhenReady = wasPlaying
                return
            }

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
            updateBitmapScale()
            applySurfaceSize()
        }

        override fun onCues(cueGroup: CueGroup) {
            val hasBitmapCues = cueGroup.cues.any { it.bitmap != null }
            val cues = if (hasBitmapCues) {
                cueGroup.cues.map { cue ->
                    if (cue.bitmap != null) scaleBitmapCue(cue) else cue
                }
            } else {
                cueGroup.cues
            }
            subtitleView.setCues(cues)
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
        lastRequestedAudioTrack = null
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

        // Reset recovery flag so recovery is available for this new track switch
        audioErrorRecoveryAttempted = false
        // Remember the user's selection — survives error recovery cycles
        lastRequestedAudioTrack = rnIndex

        if (!tracksAvailable) {
            Log.d(TAG, "setAudioTrack($rnIndex): tracks not yet available, buffering as pending")
            pendingAudioTrack = rnIndex
            return
        }

        val ref = audioIndexToTrackRef[rnIndex] ?: run {
            Log.w(TAG, "setAudioTrack($rnIndex): no ExoPlayer track ref in map (available: ${audioIndexToTrackRef.keys})")
            return
        }

        val ts = trackSelector ?: return
        val p = player ?: return

        val state = p.playbackState
        Log.d(TAG, "setAudioTrack($rnIndex): playerState=${stateToString(state)}, group=${ref.groupIndex}, track=${ref.trackIndex}")

        // If player is in error or idle state, track overrides won't take effect until prepare().
        // Buffer as pending and re-prepare so the override applies after new track groups are created.
        if (state == Player.STATE_IDLE) {
            Log.i(TAG, "setAudioTrack($rnIndex): player is IDLE — queueing as pending and re-preparing")
            pendingAudioTrack = rnIndex
            tracksAvailable = false
            val pos = p.currentPosition
            val wasPlaying = p.playWhenReady
            p.prepare()
            p.seekTo(pos)
            p.playWhenReady = wasPlaying
            return
        }

        val trackGroups = p.currentTracks.groups
        if (ref.groupIndex < trackGroups.size) {
            val group = trackGroups[ref.groupIndex]
            // Clear all audio overrides first — audio tracks can span different MediaTrackGroups,
            // and addOverride only replaces within the same group, leaving stale overrides on others
            ts.setParameters(
                ts.buildUponParameters()
                    .clearOverridesOfType(C.TRACK_TYPE_AUDIO)
                    .addOverride(TrackSelectionOverride(group.mediaTrackGroup, ref.trackIndex))
            )
            Log.d(TAG, "setAudioTrack($rnIndex): override applied — group=${ref.groupIndex}, track=${ref.trackIndex}")
        } else {
            Log.w(TAG, "setAudioTrack($rnIndex): groupIndex ${ref.groupIndex} out of range (${trackGroups.size} groups)")
        }
    }

    override fun setSubtitleTrack(rnIndex: Int) {
        if (rnIndex < 0) {
            // Disable subtitles
            subtitleView.setCues(emptyList())
            val ts = trackSelector ?: return
            ts.setParameters(
                ts.buildUponParameters()
                    .clearOverridesOfType(C.TRACK_TYPE_TEXT)
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
            // Clear all text overrides first (same rationale as audio)
            ts.setParameters(
                ts.buildUponParameters()
                    .setRendererDisabled(getTextRendererIndex(), false)
                    .clearOverridesOfType(C.TRACK_TYPE_TEXT)
                    .addOverride(TrackSelectionOverride(group.mediaTrackGroup, ref.trackIndex))
            )
            Log.d(TAG, "Set subtitle track: rnIndex=$rnIndex -> group=${ref.groupIndex}, track=${ref.trackIndex}")
        }
    }

    override fun setSubtitleSize(size: Float) {
        if (size > 0) {
            subtitleView.setFixedTextSize(TypedValue.COMPLEX_UNIT_SP, size)
        }
    }

    override fun setSubtitleColor(color: String?) {
        if (color.isNullOrEmpty()) return
        try {
            currentFgColor = Color.parseColor(color)
            applyCaptionStyle()
        } catch (_: IllegalArgumentException) {
            Log.w(TAG, "Invalid subtitle color: $color")
        }
    }

    private fun applyCaptionStyle() {
        subtitleView.setStyle(
            CaptionStyleCompat(
                currentFgColor,
                currentBgColor,
                Color.TRANSPARENT,
                CaptionStyleCompat.EDGE_TYPE_OUTLINE,
                Color.BLACK,
                Typeface.DEFAULT_BOLD
            )
        )
    }

    override fun setSubtitlePosition(position: Float) {
        subtitleView.setBottomPaddingFraction(position.coerceIn(0f, 1f))
    }

    override fun setSubtitleStyle(style: ReadableMap?) {
        if (style == null) return

        if (style.hasKey("fontSize")) {
            val multiplier = style.getDouble("fontSize")
            if (multiplier > 0) {
                subtitleView.setFixedTextSize(
                    TypedValue.COMPLEX_UNIT_SP,
                    BASE_SUBTITLE_SIZE_SP * multiplier.toFloat()
                )
                userBitmapMultiplier = multiplier.toFloat()
                updateBitmapScale()
            }
        }

        if (style.hasKey("textColor")) {
            val color = style.getString("textColor")
            if (color != null) {
                try {
                    currentFgColor = Color.parseColor(color)
                } catch (_: IllegalArgumentException) {
                    Log.w(TAG, "Invalid subtitle textColor: $color")
                }
            }
        }

        if (style.hasKey("backgroundColor")) {
            val color = style.getString("backgroundColor")
            if (color != null) {
                try {
                    currentBgColor = parseCssColor(color)
                } catch (_: IllegalArgumentException) {
                    Log.w(TAG, "Invalid subtitle backgroundColor: $color")
                }
            }
        }

        applyCaptionStyle()

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
        val marginDp = baseSubtitleMarginY + if (controlsVisible) 125 else 0
        val marginPx = TypedValue.applyDimension(
            TypedValue.COMPLEX_UNIT_DIP,
            marginDp.toFloat(),
            resources.displayMetrics
        ).toInt()
        subtitleView.setPadding(0, 0, 0, marginPx)
    }

    /**
     * Parse a CSS hex color (#RRGGBB or #RRGGBBAA) to Android's ARGB int.
     * Android's Color.parseColor reads 8-char hex as #AARRGGBB, but CSS/RN
     * uses #RRGGBBAA — this converts between the two formats.
     */
    private fun parseCssColor(color: String): Int {
        val hex = color.removePrefix("#")
        return when (hex.length) {
            6 -> Color.parseColor(color) // #RRGGBB — same in both formats
            8 -> Color.parseColor("#${hex.substring(6, 8)}${hex.substring(0, 6)}") // #RRGGBBAA → #AARRGGBB
            else -> Color.parseColor(color)
        }
    }

    /**
     * Recompute the PGS bitmap scale from video height and user multiplier.
     *
     * PGS is almost universally authored at 1080p (even on UHD Blu-ray). The cue
     * fractions are relative to the video resolution, so 1080p PGS on 4K video
     * appears at half the intended size without correction.
     *
     * Uses a blended denominator (2:1 reference:video) to soften the ratio.
     * Scale is precomputed here (not per-cue) since it only depends on video
     * height and user multiplier — NOT on individual bitmap dimensions.
     *
     * Results at userMultiplier=1.0:
     * - 4K video: 2160 / ((1080*2+2160)/3) = 1.5
     * - 1080p video: 1080 / ((1080*2+1080)/3) = 1.0
     */
    private var computedBitmapScale = 1f

    private fun updateBitmapScale() {
        val vidH = if (videoHeight > 0) videoHeight.toFloat() else PGS_REFERENCE_HEIGHT
        val denominator = (PGS_REFERENCE_HEIGHT * 2f + vidH) / 3f
        computedBitmapScale = (vidH / denominator) * userBitmapMultiplier
    }

    /**
     * Scale a bitmap (PGS/VOBSUB) subtitle cue with resolution-aware scaling and
     * composite a translucent background box behind it (matching KSPlayer).
     *
     * The padded bitmap is cached — only rebuilt when the source bitmap object,
     * computed scale, or background color changes.
     */
    private fun scaleBitmapCue(cue: Cue): Cue {
        val srcBitmap = cue.bitmap ?: return cue

        // Cache hit — same source bitmap, scale, and bg color
        if (srcBitmap === cachedSrcBitmap &&
            computedBitmapScale == cachedBitmapScale &&
            currentBgColor == cachedBitmapBgColor) {
            val cached = cachedPaddedCue
            if (cached != null) return cached
        }

        // Cache miss — rebuild the padded bitmap.
        // Padding based on bitmap width so it scales consistently.
        val w = srcBitmap.width.toFloat()
        val hPad = maxOf((w * 0.025f).toInt(), 16)
        val vPad = maxOf((w * 0.012f).toInt(), 10)
        val cornerR = maxOf(w * 0.015f, 12f)

        val paddedW = srcBitmap.width + hPad * 2
        val paddedH = srcBitmap.height + vPad * 2

        // Reuse the cached bitmap buffer if dimensions match, otherwise allocate
        val padded = if (cachedPaddedBitmap?.width == paddedW && cachedPaddedBitmap?.height == paddedH) {
            cachedPaddedBitmap!!.also { it.eraseColor(Color.TRANSPARENT) }
        } else {
            cachedPaddedBitmap?.recycle()
            Bitmap.createBitmap(paddedW, paddedH, Bitmap.Config.ARGB_8888)
        }

        val canvas = Canvas(padded)
        bgPaint.color = currentBgColor
        canvas.drawRoundRect(RectF(0f, 0f, paddedW.toFloat(), paddedH.toFloat()), cornerR, cornerR, bgPaint)
        canvas.drawBitmap(srcBitmap, hPad.toFloat(), vPad.toFloat(), null)

        val builder = cue.buildUpon().setBitmap(padded)

        // Apply precomputed resolution-aware scale to viewport dimensions
        val scale = computedBitmapScale
        if (cue.size != Cue.DIMEN_UNSET) {
            val newSize = cue.size * scale
            builder.setSize(newSize)
            if (cue.position != Cue.DIMEN_UNSET) {
                builder.setPosition(cue.position + (cue.size - newSize) / 2f)
            }
        }

        if (cue.bitmapHeight != Cue.DIMEN_UNSET) {
            builder.setBitmapHeight(cue.bitmapHeight * scale)
        }

        // Override vertical position — place near bottom of video (95% down),
        // anchored at the bottom edge of the subtitle
        builder.setLine(0.95f, Cue.LINE_TYPE_FRACTION)
        builder.setLineAnchor(Cue.ANCHOR_TYPE_END)

        val result = builder.build()

        // Update cache
        cachedSrcBitmap = srcBitmap
        cachedPaddedBitmap = padded
        cachedPaddedCue = result
        cachedBitmapScale = computedBitmapScale
        cachedBitmapBgColor = currentBgColor

        return result
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

    private fun stateToString(state: Int): String = when (state) {
        Player.STATE_IDLE -> "IDLE"
        Player.STATE_BUFFERING -> "BUFFERING"
        Player.STATE_READY -> "READY"
        Player.STATE_ENDED -> "ENDED"
        else -> "UNKNOWN($state)"
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
                        val mime = format.sampleMimeType ?: ""
                        val isBitmap = mime.contains("pgs") || mime.contains("dvbsub") ||
                            mime.contains("vobsub") || mime.contains("dvb_teletext")
                        val track = Arguments.createMap().apply {
                            putInt("id", subtitleIndex)
                            putString("type", "subtitle")
                            putString("title", format.label ?: "")
                            putString("language", format.language ?: "")
                            putString("codec", format.codecs ?: mime)
                            putBoolean("selected", isSelected)
                            putBoolean("isBitmap", isBitmap)
                            if (format.width > 0) putInt("width", format.width)
                            if (format.height > 0) putInt("height", format.height)
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

        // Apply pending tracks (set before tracks were available, or re-queued after error recovery)
        pendingAudioTrack?.let { idx ->
            pendingAudioTrack = null
            Log.d(TAG, "buildTrackList: applying pending audio track $idx")
            setAudioTrack(idx)
        }
        pendingSubtitleTrack?.let { idx ->
            pendingSubtitleTrack = null
            Log.d(TAG, "buildTrackList: applying pending subtitle track $idx")
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
