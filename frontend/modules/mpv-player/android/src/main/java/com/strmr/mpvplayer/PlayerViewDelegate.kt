package com.strmr.mpvplayer

import com.facebook.react.bridge.ReadableMap

/**
 * Common interface for player implementations (MpvPlayerView, ExoPlayerView).
 * The PlayerContainerView delegates all props/commands through this interface.
 */
interface PlayerViewDelegate {
    var isHDR: Boolean
    fun setSource(source: ReadableMap?)
    fun setPaused(paused: Boolean)
    fun setVolume(volume: Float)
    fun setRate(rate: Float)
    fun setAudioTrack(rnIndex: Int)
    fun setSubtitleTrack(rnIndex: Int)
    fun setSubtitleSize(size: Float)
    fun setSubtitleColor(color: String?)
    fun setSubtitlePosition(position: Float)
    fun setSubtitleStyle(style: ReadableMap?)
    fun setControlsVisible(visible: Boolean)
    fun setExternalSubtitleUrl(url: String?)
    fun seekTo(time: Double)
    fun destroy()
}
