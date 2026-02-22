package com.strmr.mpvplayer

import com.facebook.react.bridge.ReadableArray
import com.facebook.react.bridge.ReadableMap
import com.facebook.react.common.MapBuilder
import com.facebook.react.uimanager.SimpleViewManager
import com.facebook.react.uimanager.ThemedReactContext
import com.facebook.react.uimanager.annotations.ReactProp

class MpvPlayerViewManager : SimpleViewManager<PlayerContainerView>() {

    companion object {
        private const val REACT_CLASS = "MpvPlayer"
        private const val COMMAND_SEEK = 1
        private const val COMMAND_SET_AUDIO_TRACK = 2
        private const val COMMAND_SET_SUBTITLE_TRACK = 3
    }

    override fun getName(): String = REACT_CLASS

    override fun createViewInstance(reactContext: ThemedReactContext): PlayerContainerView {
        return PlayerContainerView(reactContext)
    }

    // --- Props ---

    @ReactProp(name = "source")
    fun setSource(view: PlayerContainerView, source: ReadableMap?) {
        view.setSource(source)
    }

    @ReactProp(name = "paused", defaultBoolean = true)
    fun setPaused(view: PlayerContainerView, paused: Boolean) {
        view.setPaused(paused)
    }

    @ReactProp(name = "volume", defaultFloat = 1f)
    fun setVolume(view: PlayerContainerView, volume: Float) {
        view.setVolume(volume)
    }

    @ReactProp(name = "rate", defaultFloat = 1f)
    fun setRate(view: PlayerContainerView, rate: Float) {
        view.setRate(rate)
    }

    @ReactProp(name = "audioTrack", defaultInt = -1)
    fun setAudioTrack(view: PlayerContainerView, trackIndex: Int) {
        view.setAudioTrack(trackIndex)
    }

    @ReactProp(name = "subtitleTrack", defaultInt = -1)
    fun setSubtitleTrack(view: PlayerContainerView, trackIndex: Int) {
        view.setSubtitleTrack(trackIndex)
    }

    @ReactProp(name = "subtitleSize", defaultFloat = 0f)
    fun setSubtitleSize(view: PlayerContainerView, size: Float) {
        view.setSubtitleSize(size)
    }

    @ReactProp(name = "subtitleColor")
    fun setSubtitleColor(view: PlayerContainerView, color: String?) {
        view.setSubtitleColor(color)
    }

    @ReactProp(name = "subtitlePosition", defaultFloat = 0f)
    fun setSubtitlePosition(view: PlayerContainerView, position: Float) {
        view.setSubtitlePosition(position)
    }

    @ReactProp(name = "subtitleStyle")
    fun setSubtitleStyle(view: PlayerContainerView, style: ReadableMap?) {
        view.setSubtitleStyle(style)
    }

    @ReactProp(name = "controlsVisible", defaultBoolean = false)
    fun setControlsVisible(view: PlayerContainerView, visible: Boolean) {
        view.setControlsVisible(visible)
    }

    @ReactProp(name = "externalSubtitleUrl")
    fun setExternalSubtitleUrl(view: PlayerContainerView, url: String?) {
        view.setExternalSubtitleUrl(url)
    }

    @ReactProp(name = "isHDR", defaultBoolean = false)
    fun setIsHDR(view: PlayerContainerView, hdr: Boolean) {
        view.isHDR = hdr
    }

    @ReactProp(name = "isDV", defaultBoolean = false)
    fun setIsDV(view: PlayerContainerView, dv: Boolean) {
        view.isDV = dv
    }

    // --- Events ---

    override fun getExportedCustomDirectEventTypeConstants(): Map<String, Any>? {
        return MapBuilder.builder<String, Any>()
            .put("onLoad", MapBuilder.of("registrationName", "onLoad"))
            .put("onProgress", MapBuilder.of("registrationName", "onProgress"))
            .put("onEnd", MapBuilder.of("registrationName", "onEnd"))
            .put("onError", MapBuilder.of("registrationName", "onError"))
            .put("onTracksChanged", MapBuilder.of("registrationName", "onTracksChanged"))
            .put("onBuffering", MapBuilder.of("registrationName", "onBuffering"))
            .put("onDebugLog", MapBuilder.of("registrationName", "onDebugLog"))
            .put("onSubtitleText", MapBuilder.of("registrationName", "onSubtitleText"))
            .build()
    }

    // --- Commands ---

    override fun getCommandsMap(): Map<String, Int> {
        return mapOf(
            "seek" to COMMAND_SEEK,
            "setAudioTrack" to COMMAND_SET_AUDIO_TRACK,
            "setSubtitleTrack" to COMMAND_SET_SUBTITLE_TRACK
        )
    }

    override fun receiveCommand(view: PlayerContainerView, commandId: String, args: ReadableArray?) {
        // commandId arrives as the integer ID (from getCommandsMap) stringified
        when (commandId) {
            "seek", COMMAND_SEEK.toString() -> {
                val time = args?.getDouble(0) ?: return
                view.seekTo(time)
            }
            "setAudioTrack", COMMAND_SET_AUDIO_TRACK.toString() -> {
                val trackId = args?.getInt(0) ?: return
                view.setAudioTrack(trackId)
            }
            "setSubtitleTrack", COMMAND_SET_SUBTITLE_TRACK.toString() -> {
                val trackId = args?.getInt(0) ?: return
                view.setSubtitleTrack(trackId)
            }
        }
    }

    override fun onDropViewInstance(view: PlayerContainerView) {
        super.onDropViewInstance(view)
        view.destroy()
    }
}
