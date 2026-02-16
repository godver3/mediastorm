package com.strmr.mpvplayer

import android.content.pm.ActivityInfo
import android.os.Build
import android.util.Log
import android.view.Display
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
                activity.window.colorMode = ActivityInfo.COLOR_MODE_HDR
                Log.i(TAG, "HDR color mode enabled")

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
                promise.resolve(true)
            } catch (e: Exception) {
                Log.e(TAG, "Failed to disable HDR color mode", e)
                promise.reject("HDR_ERROR", "Failed to disable HDR: ${e.message}")
            }
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

        Log.i(TAG, "Display HDR caps: HDR10=$hasHDR10, DV=$hasDV, HLG=$hasHLG, HDR10+=$hasHDR10Plus")

        return result
    }
}
