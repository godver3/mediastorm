package com.strmr.mpvplayer

import android.app.Activity
import android.app.Application
import android.content.ComponentCallbacks2
import android.os.Bundle
import android.content.Intent
import android.os.Handler
import android.os.Looper
import android.util.Log
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import com.facebook.react.bridge.ReadableMap

/**
 * React Native NativeModule that launches PlayerActivity and returns
 * the playback result (position, completed, percentWatched) via a Promise.
 *
 * Uses startActivity (NOT startActivityForResult) to avoid crashes when the
 * OS kills and restarts the process — ReactActivityDelegate.onActivityResult
 * NPEs if ReactDelegate isn't initialized yet on process restart.
 *
 * Instead, PlayerActivity stores its result in a static companion field,
 * and we pick it up via Application.ActivityLifecycleCallbacks.
 */
class PlayerLauncherModule(private val reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {

    companion object {
        private const val TAG = "PlayerLauncher"
    }

    private var pendingPromise: Promise? = null

    private val lifecycleCallbacks = object : Application.ActivityLifecycleCallbacks {
        override fun onActivityDestroyed(activity: Activity) {
            if (activity !is PlayerActivity) return
            val promise = pendingPromise ?: return
            pendingPromise = null

            val savedResult = PlayerActivity.lastResult
            PlayerActivity.lastResult = null

            val result = Arguments.createMap().apply {
                putDouble("lastPosition", savedResult?.getDouble("lastPosition") ?: 0.0)
                putBoolean("completed", savedResult?.getBoolean("completed") ?: false)
                putDouble("percentWatched", savedResult?.getDouble("percentWatched") ?: 0.0)
            }
            promise.resolve(result)
        }

        override fun onActivityCreated(activity: Activity, savedInstanceState: Bundle?) {}
        override fun onActivityStarted(activity: Activity) {}
        override fun onActivityResumed(activity: Activity) {}
        override fun onActivityPaused(activity: Activity) {}
        override fun onActivityStopped(activity: Activity) {}
        override fun onActivitySaveInstanceState(activity: Activity, outState: Bundle) {}
    }

    init {
        (reactContext.applicationContext as Application)
            .registerActivityLifecycleCallbacks(lifecycleCallbacks)
    }

    override fun getName(): String = "PlayerLauncherModule"

    @ReactMethod
    fun launch(params: ReadableMap, promise: Promise) {
        val activity = reactContext.currentActivity
        if (activity == null) {
            promise.reject("NO_ACTIVITY", "No current activity")
            return
        }

        if (pendingPromise != null) {
            promise.reject("ALREADY_LAUNCHING", "A player launch is already in progress")
            return
        }

        pendingPromise = promise
        PlayerActivity.lastResult = null

        val intent = Intent(activity, PlayerActivity::class.java)
        intent.putExtra("streamUrl", params.getString("streamUrl") ?: "")
        intent.putExtra("title", params.getString("title") ?: "")
        intent.putExtra("authToken", params.getString("authToken") ?: "")
        intent.putExtra("userId", params.getString("userId") ?: "")
        intent.putExtra("mediaType", params.getString("mediaType") ?: "")
        intent.putExtra("itemId", params.getString("itemId") ?: "")
        intent.putExtra("backendUrl", params.getString("backendUrl") ?: "")

        // Numeric extras
        if (params.hasKey("startOffset")) {
            intent.putExtra("startOffset", params.getDouble("startOffset").toLong())
        }
        if (params.hasKey("durationHint")) {
            intent.putExtra("durationHint", params.getDouble("durationHint").toLong())
        }
        if (params.hasKey("preselectedAudioTrack")) {
            intent.putExtra("preselectedAudioTrack", params.getInt("preselectedAudioTrack"))
        }
        if (params.hasKey("preselectedSubtitleTrack")) {
            intent.putExtra("preselectedSubtitleTrack", params.getInt("preselectedSubtitleTrack"))
        }

        // Episode metadata
        if (params.hasKey("seasonNumber")) intent.putExtra("seasonNumber", params.getInt("seasonNumber"))
        if (params.hasKey("episodeNumber")) intent.putExtra("episodeNumber", params.getInt("episodeNumber"))
        intent.putExtra("seriesId", params.getString("seriesId") ?: "")
        intent.putExtra("seriesName", params.getString("seriesName") ?: "")
        intent.putExtra("episodeName", params.getString("episodeName") ?: "")

        // External IDs
        intent.putExtra("titleId", params.getString("titleId") ?: "")
        intent.putExtra("imdbId", params.getString("imdbId") ?: "")
        intent.putExtra("tvdbId", params.getString("tvdbId") ?: "")

        // HDR flags
        if (params.hasKey("isHDR")) intent.putExtra("isHDR", params.getBoolean("isHDR"))
        if (params.hasKey("isDolbyVision")) intent.putExtra("isDolbyVision", params.getBoolean("isDolbyVision"))

        // Video/audio metadata for controls display
        intent.putExtra("resolution", params.getString("resolution") ?: "")
        intent.putExtra("dolbyVisionProfile", params.getString("dolbyVisionProfile") ?: "")
        intent.putExtra("videoCodec", params.getString("videoCodec") ?: "")
        intent.putExtra("frameRate", params.getString("frameRate") ?: "")
        intent.putExtra("audioCodec", params.getString("audioCodec") ?: "")
        intent.putExtra("audioChannels", params.getString("audioChannels") ?: "")
        intent.putExtra("sourcePath", params.getString("sourcePath") ?: "")
        intent.putExtra("passthroughName", params.getString("passthroughName") ?: "")
        intent.putExtra("passthroughDescription", params.getString("passthroughDescription") ?: "")
        intent.putExtra("colorTransfer", params.getString("colorTransfer") ?: "")
        intent.putExtra("colorPrimaries", params.getString("colorPrimaries") ?: "")
        intent.putExtra("colorSpace", params.getString("colorSpace") ?: "")
        if (params.hasKey("videoBitrate")) intent.putExtra("videoBitrate", params.getDouble("videoBitrate").toLong())
        if (params.hasKey("audioBitrate")) intent.putExtra("audioBitrate", params.getDouble("audioBitrate").toLong())
        if (params.hasKey("year")) intent.putExtra("year", params.getInt("year"))

        // onTrimMemory triggers component callbacks (e.g. expo-image clearing GL textures)
        // that must run on the main thread. startActivity should also be on the UI thread.
        // @ReactMethod runs on the NativeModules thread, so dispatch to main.
        Log.d(TAG, "Launching PlayerActivity: title=${params.getString("title")}")
        Handler(Looper.getMainLooper()).post {
            (activity.applicationContext as? Application)?.onTrimMemory(ComponentCallbacks2.TRIM_MEMORY_RUNNING_CRITICAL)
            System.gc()
            activity.startActivity(intent)
        }
    }
}
