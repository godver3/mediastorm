package com.strmr.mpvplayer

import android.util.Log
import android.view.KeyEvent
import android.view.Window
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import com.facebook.react.modules.core.DeviceEventManagerModule

/**
 * Forwards Android TV D-pad and media key events to React Native via DeviceEventEmitter.
 *
 * When enabled, wraps the Activity's Window.Callback to intercept dispatchKeyEvent.
 * Emits "onHWKeyEvent" with { eventType, eventKeyAction } matching the format
 * expected by RemoteControlManager.ts.
 *
 * Key events are still passed through to the original callback so that the native
 * Android focus system (used by react-tv-space-navigation) continues to work.
 *
 * KEYCODE_BACK is excluded — BackHandler already handles it on Android.
 */
class TvKeyEventModule(private val reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    companion object {
        private const val TAG = "TvKeyEvent"

        // Map Android KeyEvent codes to event type strings matching TV_EVENT_KEY_MAPPING
        // in RemoteControlManager.ts
        private val KEY_MAPPING = mapOf(
            KeyEvent.KEYCODE_DPAD_LEFT to "left",
            KeyEvent.KEYCODE_DPAD_RIGHT to "right",
            KeyEvent.KEYCODE_DPAD_UP to "up",
            KeyEvent.KEYCODE_DPAD_DOWN to "down",
            KeyEvent.KEYCODE_DPAD_CENTER to "select",
            KeyEvent.KEYCODE_ENTER to "select",
            KeyEvent.KEYCODE_NUMPAD_ENTER to "select",
            KeyEvent.KEYCODE_MEDIA_PLAY_PAUSE to "playPause",
            KeyEvent.KEYCODE_MEDIA_PLAY to "playPause",
            KeyEvent.KEYCODE_MEDIA_PAUSE to "playPause",
            KeyEvent.KEYCODE_MEDIA_FAST_FORWARD to "fastForward",
            KeyEvent.KEYCODE_MEDIA_REWIND to "rewind",
            KeyEvent.KEYCODE_MEDIA_NEXT to "fastForward",
            KeyEvent.KEYCODE_MEDIA_PREVIOUS to "rewind",
            KeyEvent.KEYCODE_MENU to "menu",
        )
    }

    private var originalCallback: Window.Callback? = null
    private var enabled = false

    override fun getName(): String = "TvKeyEventModule"

    @ReactMethod
    fun enable() {
        if (enabled) return
        val activity = reactContext.currentActivity ?: return
        activity.runOnUiThread {
            val window = activity.window ?: return@runOnUiThread
            val original = window.callback ?: return@runOnUiThread
            // Don't double-wrap
            if (original is KeyEventCallbackWrapper) return@runOnUiThread

            originalCallback = original
            window.callback = KeyEventCallbackWrapper(original) { eventType, action ->
                emitKeyEvent(eventType, action)
            }
            enabled = true
            Log.d(TAG, "Key event forwarding enabled")
        }
    }

    @ReactMethod
    fun disable() {
        if (!enabled) return
        val activity = reactContext.currentActivity ?: return
        activity.runOnUiThread {
            val window = activity.window ?: return@runOnUiThread
            val current = window.callback
            if (current is KeyEventCallbackWrapper) {
                window.callback = current.original
            }
            originalCallback = null
            enabled = false
            Log.d(TAG, "Key event forwarding disabled")
        }
    }

    private fun emitKeyEvent(eventType: String, action: Int) {
        try {
            val map = Arguments.createMap()
            map.putString("eventType", eventType)
            map.putInt("eventKeyAction", action)
            reactContext
                .getJSModule(DeviceEventManagerModule.RCTDeviceEventEmitter::class.java)
                .emit("onHWKeyEvent", map)
        } catch (e: Exception) {
            Log.w(TAG, "Failed to emit key event", e)
        }
    }

    /**
     * Window.Callback wrapper that intercepts key events and emits them to JS,
     * then passes them through to the original callback for normal focus handling.
     */
    private class KeyEventCallbackWrapper(
        val original: Window.Callback,
        private val onKeyEvent: (eventType: String, action: Int) -> Unit,
    ) : Window.Callback by original {

        override fun dispatchKeyEvent(event: KeyEvent): Boolean {
            val eventType = KEY_MAPPING[event.keyCode]
            if (eventType != null) {
                // Map ACTION_DOWN=0, ACTION_UP=1 (matches RN convention)
                val action = when (event.action) {
                    KeyEvent.ACTION_DOWN -> 0
                    KeyEvent.ACTION_UP -> 1
                    else -> -1
                }
                onKeyEvent(eventType, action)
            }
            // Always pass through to original so focus navigation still works
            return original.dispatchKeyEvent(event)
        }
    }
}
