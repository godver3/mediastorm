package com.strmr.mpvplayer

import android.content.pm.ActivityInfo
import android.os.Build
import android.util.Log
import android.view.Display
import android.view.WindowManager
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod

class HdrColorModeModule(private val reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    companion object {
        private const val TAG = "HdrColorMode"
    }

    private var previousDisplayModeId: Int = -1

    override fun getName(): String = "HdrColorModeModule"

    @ReactMethod
    fun enableHDR(promise: Promise) {
        val activity = reactContext.currentActivity
        if (activity == null) {
            promise.reject("NO_ACTIVITY", "No current activity")
            return
        }

        activity.runOnUiThread {
            try {
                // Set activity color mode to HDR (enables wide color gamut rendering)
                activity.window.colorMode = ActivityInfo.COLOR_MODE_HDR
                Log.i(TAG, "HDR color mode enabled on activity window")

                // Request HDR-compatible display mode via preferredDisplayModeId.
                // This triggers HDMI output mode switch on Android TV / Fire Stick.
                requestHdrDisplayMode(activity.windowManager, activity.window)

                val caps = checkDisplayHDRSupport()
                promise.resolve(caps)
            } catch (e: Exception) {
                Log.e(TAG, "Failed to enable HDR color mode", e)
                promise.reject("HDR_ERROR", "Failed to enable HDR: ${e.message}")
            }
        }
    }

    @ReactMethod
    fun disableHDR(promise: Promise) {
        val activity = reactContext.currentActivity
        if (activity == null) {
            promise.reject("NO_ACTIVITY", "No current activity")
            return
        }

        activity.runOnUiThread {
            try {
                activity.window.colorMode = ActivityInfo.COLOR_MODE_DEFAULT
                Log.i(TAG, "HDR color mode disabled (reset to default)")

                // Restore previous display mode
                restoreDisplayMode(activity.window)

                promise.resolve(true)
            } catch (e: Exception) {
                Log.e(TAG, "Failed to disable HDR color mode", e)
                promise.reject("HDR_ERROR", "Failed to disable HDR: ${e.message}")
            }
        }
    }

    /**
     * Request an HDR-compatible display mode via preferredDisplayModeId.
     * On Android TV and Fire Stick, this triggers the HDMI output to switch
     * to the current resolution at the current refresh rate — the key step
     * that enables the display to accept HDR metadata from the video pipeline.
     *
     * Note: We match the current mode's resolution and refresh rate. The actual
     * HDR activation happens when COLOR_MODE_HDR is set AND the video pipeline
     * outputs HDR metadata (via mediacodec_embed or EGL BT.2020/PQ surface).
     */
    private fun requestHdrDisplayMode(windowManager: WindowManager, window: android.view.Window) {
        try {
            val display = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                reactContext.currentActivity?.display
            } else {
                @Suppress("DEPRECATION")
                windowManager.defaultDisplay
            }

            if (display == null) {
                Log.w(TAG, "No display available for mode switch")
                return
            }

            val currentMode = display.mode
            previousDisplayModeId = currentMode.modeId
            val supportedModes = display.supportedModes

            Log.i(TAG, "Current display mode: ${currentMode.physicalWidth}x${currentMode.physicalHeight}@${currentMode.refreshRate}Hz (id=${currentMode.modeId})")
            Log.i(TAG, "Supported modes: ${supportedModes.size}")

            // Find the best matching mode at current resolution — prefer highest refresh rate
            // The mode itself doesn't encode HDR type; HDR is signaled through COLOR_MODE_HDR
            // and the video surface metadata. But requesting the mode via preferredDisplayModeId
            // ensures the display negotiation happens (required on some Fire TV devices).
            var bestMode: Display.Mode? = null
            for (mode in supportedModes) {
                if (mode.physicalWidth == currentMode.physicalWidth &&
                    mode.physicalHeight == currentMode.physicalHeight) {
                    // Match resolution, prefer same or higher refresh rate
                    if (bestMode == null ||
                        Math.abs(mode.refreshRate - currentMode.refreshRate) <
                        Math.abs(bestMode.refreshRate - currentMode.refreshRate)) {
                        bestMode = mode
                    }
                }
            }

            if (bestMode != null && bestMode.modeId != currentMode.modeId) {
                val attrs = window.attributes
                attrs.preferredDisplayModeId = bestMode.modeId
                window.attributes = attrs
                Log.i(TAG, "Requested display mode switch: id=${bestMode.modeId} (${bestMode.physicalWidth}x${bestMode.physicalHeight}@${bestMode.refreshRate}Hz)")
            } else {
                // Even if no better mode, explicitly set the current mode to trigger
                // HDMI re-negotiation on some devices (Fire Stick needs this)
                val attrs = window.attributes
                attrs.preferredDisplayModeId = currentMode.modeId
                window.attributes = attrs
                Log.i(TAG, "Set preferredDisplayModeId to current mode ${currentMode.modeId} to trigger HDMI negotiation")
            }
        } catch (e: Exception) {
            Log.w(TAG, "Display mode switch failed (non-fatal)", e)
        }
    }

    /**
     * Restore the display mode to what it was before HDR was enabled.
     */
    private fun restoreDisplayMode(window: android.view.Window) {
        try {
            if (previousDisplayModeId >= 0) {
                val attrs = window.attributes
                // Setting to 0 means "no preference" — let the system choose
                attrs.preferredDisplayModeId = 0
                window.attributes = attrs
                Log.i(TAG, "Display mode preference cleared (restored to system default)")
                previousDisplayModeId = -1
            }
        } catch (e: Exception) {
            Log.w(TAG, "Display mode restore failed (non-fatal)", e)
        }
    }

    private fun checkDisplayHDRSupport(): com.facebook.react.bridge.WritableMap {
        val result = Arguments.createMap()
        val activity = reactContext.currentActivity

        if (activity == null) {
            result.putBoolean("supported", false)
            return result
        }

        val display = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
            activity.display
        } else {
            @Suppress("DEPRECATION")
            activity.windowManager.defaultDisplay
        }

        if (display == null) {
            result.putBoolean("supported", false)
            return result
        }

        val hdrCaps = display.hdrCapabilities
        if (hdrCaps == null) {
            result.putBoolean("supported", false)
            return result
        }

        val types = hdrCaps.supportedHdrTypes
        val hasHDR10 = types.contains(Display.HdrCapabilities.HDR_TYPE_HDR10)
        val hasDV = types.contains(Display.HdrCapabilities.HDR_TYPE_DOLBY_VISION)
        val hasHLG = types.contains(Display.HdrCapabilities.HDR_TYPE_HLG)
        val hasHDR10Plus = Build.VERSION.SDK_INT >= 29 &&
            types.contains(Display.HdrCapabilities.HDR_TYPE_HDR10_PLUS)

        result.putBoolean("supported", types.isNotEmpty())
        result.putBoolean("hdr10", hasHDR10)
        result.putBoolean("dolbyVision", hasDV)
        result.putBoolean("hlg", hasHLG)
        result.putBoolean("hdr10Plus", hasHDR10Plus)
        result.putDouble("maxLuminance", hdrCaps.desiredMaxLuminance.toDouble())
        result.putDouble("minLuminance", hdrCaps.desiredMinLuminance.toDouble())

        Log.i(TAG, "Display HDR caps: HDR10=$hasHDR10, DV=$hasDV, HLG=$hasHLG, HDR10+=$hasHDR10Plus, " +
            "maxLum=${hdrCaps.desiredMaxLuminance}, minLum=${hdrCaps.desiredMinLuminance}")

        return result
    }
}
