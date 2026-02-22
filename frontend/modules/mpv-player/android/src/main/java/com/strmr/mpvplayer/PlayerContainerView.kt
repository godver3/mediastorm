package com.strmr.mpvplayer

import android.util.Log
import android.widget.FrameLayout
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.ReactContext
import com.facebook.react.bridge.ReadableMap
import com.facebook.react.bridge.WritableMap
import com.facebook.react.uimanager.ThemedReactContext
import com.facebook.react.uimanager.events.RCTEventEmitter

/**
 * Container view that routes between MpvPlayerView (default/HDR10) and
 * ExoPlayerView (Dolby Vision) based on the isDV prop.
 *
 * Player creation is deferred via post() so that ALL props in the React Native
 * batch (isDV, isHDR, source, etc.) are applied before the routing decision.
 * React Native calls @ReactProp setters synchronously in JSX order — source
 * often arrives before isDV/isHDR. The post() runs after the full batch.
 */
class PlayerContainerView(private val reactContext: ThemedReactContext) :
    FrameLayout(reactContext) {

    companion object {
        private const val TAG = "PlayerContainerView"
    }

    /**
     * Force-layout children to fill this container.
     * React Native calls measure()+layout() on this view, but children added
     * programmatically via post() miss the initial layout pass. This ensures
     * they always fill the container when a layout occurs.
     */
    override fun onLayout(changed: Boolean, left: Int, top: Int, right: Int, bottom: Int) {
        val w = right - left
        val h = bottom - top
        for (i in 0 until childCount) {
            val child = getChildAt(i)
            child.measure(
                MeasureSpec.makeMeasureSpec(w, MeasureSpec.EXACTLY),
                MeasureSpec.makeMeasureSpec(h, MeasureSpec.EXACTLY)
            )
            child.layout(0, 0, w, h)
        }
    }

    // Routing flags — set via React props (may arrive before or after source)
    var isDV = false
        set(value) {
            Log.i(TAG, "isDV set to $value (activePlayer=${activePlayer?.javaClass?.simpleName})")
            field = value
            // Forward to active player if it exists (late prop update)
            // isDV can't change player type after creation — only matters at creation time
        }
    var isHDR = false
        set(value) {
            Log.i(TAG, "isHDR set to $value (activePlayer=${activePlayer?.javaClass?.simpleName})")
            field = value
            // Forward to active player if it already exists (late prop update)
            activePlayer?.isHDR = value
        }

    // Active player (created lazily after prop batch completes)
    private var activePlayer: PlayerViewDelegate? = null
    private var destroyed = false
    private var creationPosted = false

    // Buffered props (stored until player is created)
    private var bufferedSource: ReadableMap? = null
    private var bufferedPaused: Boolean = true
    private var bufferedVolume: Float = 1f
    private var bufferedRate: Float = 1f
    private var bufferedAudioTrack: Int = -1
    private var bufferedSubtitleTrack: Int = -1
    private var bufferedSubtitleSize: Float = 0f
    private var bufferedSubtitleColor: String? = null
    private var bufferedSubtitlePosition: Float = 0f
    private var bufferedSubtitleStyle: ReadableMap? = null
    private var bufferedControlsVisible: Boolean = false
    private var bufferedExternalSubUrl: String? = null

    /**
     * Event emitter lambda — routes events through this container's React view ID,
     * since it's the view registered with RN (not the child player view).
     */
    private val eventEmitter: (String, WritableMap?) -> Unit = { eventName, data ->
        val ctx = context as? ReactContext
        ctx?.getJSModule(RCTEventEmitter::class.java)
            ?.receiveEvent(id, eventName, data)
    }

    fun setSource(source: ReadableMap?) {
        bufferedSource = source
        if (source == null) return

        val uri = source.getString("uri") ?: "null"
        Log.i(TAG, "setSource called: isDV=$isDV, isHDR=$isHDR, activePlayer=${activePlayer?.javaClass?.simpleName}, uri=${uri.takeLast(60)}")

        if (activePlayer != null) {
            // Player already created — just forward the new source
            activePlayer?.setSource(source)
            return
        }

        // Defer player creation to after the current prop batch completes.
        // React Native calls all @ReactProp setters synchronously on the UI thread
        // in JSX order. source often arrives before isDV/isHDR. By posting to the
        // message queue, we ensure all props are set before we read isDV/isHDR.
        if (!creationPosted && !destroyed) {
            creationPosted = true
            post {
                if (activePlayer == null && !destroyed) {
                    Log.i(TAG, "Deferred creation: isDV=$isDV, isHDR=$isHDR")
                    createPlayer()
                    activePlayer?.setSource(bufferedSource)
                }
            }
        }
    }

    private fun createPlayer() {
        if (destroyed) return

        val playerType: String
        val player: PlayerViewDelegate = if (isDV) {
            playerType = "ExoPlayerView"
            Log.i(TAG, "Creating ExoPlayerView for Dolby Vision content")
            ExoPlayerView(reactContext, eventEmitter)
        } else {
            playerType = "MpvPlayerView"
            Log.i(TAG, "Creating MpvPlayerView (isDV=false, isHDR=$isHDR)")
            MpvPlayerView(reactContext, eventEmitter)
        }

        // Emit routing decision to JS console via onDebugLog
        val debugData = Arguments.createMap().apply {
            putString("message", "[PlayerContainer] Routed to $playerType (isDV=$isDV, isHDR=$isHDR)")
        }
        eventEmitter("onDebugLog", debugData)

        // Set HDR flag before source
        player.isHDR = isHDR

        // Add child view
        val childView = player as FrameLayout
        addView(childView, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT))

        // Force-layout the child immediately — this post() runs after React Native's
        // initial layout pass, so the child has 0x0 dimensions. Without this, the
        // SurfaceView never creates its surface and video stays blank.
        val w = width
        val h = height
        if (w > 0 && h > 0) {
            childView.measure(
                MeasureSpec.makeMeasureSpec(w, MeasureSpec.EXACTLY),
                MeasureSpec.makeMeasureSpec(h, MeasureSpec.EXACTLY)
            )
            childView.layout(0, 0, w, h)
            Log.i(TAG, "Forced child layout: ${w}x${h}")
        } else {
            Log.w(TAG, "Container has 0x0 dimensions, requesting layout")
            requestLayout()
        }

        activePlayer = player

        // Replay buffered props
        replayBufferedProps()
    }

    private fun replayBufferedProps() {
        val player = activePlayer ?: return

        player.setPaused(bufferedPaused)
        player.setVolume(bufferedVolume)
        player.setRate(bufferedRate)

        if (bufferedAudioTrack >= 0) player.setAudioTrack(bufferedAudioTrack)
        if (bufferedSubtitleTrack >= 0) player.setSubtitleTrack(bufferedSubtitleTrack)
        if (bufferedSubtitleSize > 0) player.setSubtitleSize(bufferedSubtitleSize)
        bufferedSubtitleColor?.let { player.setSubtitleColor(it) }
        if (bufferedSubtitlePosition != 0f) player.setSubtitlePosition(bufferedSubtitlePosition)
        bufferedSubtitleStyle?.let { player.setSubtitleStyle(it) }
        player.setControlsVisible(bufferedControlsVisible)
        bufferedExternalSubUrl?.let { player.setExternalSubtitleUrl(it) }
    }

    // ========== Prop setters (called from ViewManager) ==========

    fun setPaused(paused: Boolean) {
        bufferedPaused = paused
        activePlayer?.setPaused(paused)
    }

    fun setVolume(volume: Float) {
        bufferedVolume = volume
        activePlayer?.setVolume(volume)
    }

    fun setRate(rate: Float) {
        bufferedRate = rate
        activePlayer?.setRate(rate)
    }

    fun setAudioTrack(rnIndex: Int) {
        bufferedAudioTrack = rnIndex
        activePlayer?.setAudioTrack(rnIndex)
    }

    fun setSubtitleTrack(rnIndex: Int) {
        bufferedSubtitleTrack = rnIndex
        activePlayer?.setSubtitleTrack(rnIndex)
    }

    fun setSubtitleSize(size: Float) {
        bufferedSubtitleSize = size
        activePlayer?.setSubtitleSize(size)
    }

    fun setSubtitleColor(color: String?) {
        bufferedSubtitleColor = color
        activePlayer?.setSubtitleColor(color)
    }

    fun setSubtitlePosition(position: Float) {
        bufferedSubtitlePosition = position
        activePlayer?.setSubtitlePosition(position)
    }

    fun setSubtitleStyle(style: ReadableMap?) {
        bufferedSubtitleStyle = style
        activePlayer?.setSubtitleStyle(style)
    }

    fun setControlsVisible(visible: Boolean) {
        bufferedControlsVisible = visible
        activePlayer?.setControlsVisible(visible)
    }

    fun setExternalSubtitleUrl(url: String?) {
        bufferedExternalSubUrl = url
        activePlayer?.setExternalSubtitleUrl(url)
    }

    fun seekTo(time: Double) {
        activePlayer?.seekTo(time)
    }

    fun destroy() {
        if (destroyed) return
        destroyed = true
        activePlayer?.destroy()
        activePlayer = null
        removeAllViews()
    }
}
