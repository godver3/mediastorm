package com.strmr.mpvplayer

import com.facebook.react.bridge.ReadableArray
import com.facebook.react.bridge.ReadableMap
import com.facebook.react.common.MapBuilder
import com.facebook.react.uimanager.SimpleViewManager
import com.facebook.react.uimanager.ThemedReactContext
import com.facebook.react.uimanager.annotations.ReactProp

class MpvPlayerViewManager : SimpleViewManager<MpvPlayerView>() {

    companion object {
        private const val REACT_CLASS = "MpvPlayer"
        private const val COMMAND_SEEK = 1
        private const val COMMAND_SET_AUDIO_TRACK = 2
        private const val COMMAND_SET_SUBTITLE_TRACK = 3
    }

    override fun getName(): String = REACT_CLASS

    override fun createViewInstance(reactContext: ThemedReactContext): MpvPlayerView {
        return MpvPlayerView(reactContext)
    }

    // --- Props ---

    @ReactProp(name = "source")
    fun setSource(view: MpvPlayerView, source: ReadableMap?) {
        view.setSource(source)
    }

    @ReactProp(name = "paused", defaultBoolean = true)
    fun setPaused(view: MpvPlayerView, paused: Boolean) {
        view.setPaused(paused)
    }

    @ReactProp(name = "volume", defaultFloat = 1f)
    fun setVolume(view: MpvPlayerView, volume: Float) {
        view.setVolume(volume)
    }

    @ReactProp(name = "rate", defaultFloat = 1f)
    fun setRate(view: MpvPlayerView, rate: Float) {
        view.setRate(rate)
    }

    @ReactProp(name = "audioTrack", defaultInt = -1)
    fun setAudioTrack(view: MpvPlayerView, trackIndex: Int) {
        view.setAudioTrack(trackIndex)
    }

    @ReactProp(name = "subtitleTrack", defaultInt = -1)
    fun setSubtitleTrack(view: MpvPlayerView, trackIndex: Int) {
        view.setSubtitleTrack(trackIndex)
    }

    @ReactProp(name = "subtitleSize", defaultFloat = 0f)
    fun setSubtitleSize(view: MpvPlayerView, size: Float) {
        view.setSubtitleSize(size)
    }

    @ReactProp(name = "subtitleColor")
    fun setSubtitleColor(view: MpvPlayerView, color: String?) {
        view.setSubtitleColor(color)
    }

    @ReactProp(name = "subtitlePosition", defaultFloat = 0f)
    fun setSubtitlePosition(view: MpvPlayerView, position: Float) {
        view.setSubtitlePosition(position)
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

    override fun receiveCommand(view: MpvPlayerView, commandId: String, args: ReadableArray?) {
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

    override fun onDropViewInstance(view: MpvPlayerView) {
        super.onDropViewInstance(view)
        view.destroy()
    }
}
