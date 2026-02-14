package expo.modules.pipmanager

import android.app.Activity
import android.app.PictureInPictureParams
import android.content.Context
import android.graphics.Color
import android.graphics.drawable.ColorDrawable
import android.graphics.drawable.Drawable
import android.os.Build
import android.util.Log
import android.util.Rational
import android.view.SurfaceView
import android.view.View
import android.view.ViewGroup
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition

class PipManagerModule : Module() {
  companion object {
    private const val TAG = "PipManagerModule"
    private const val PREFS_NAME = "pip_manager_prefs"
    private const val KEY_AUTO_PIP_ENABLED = "auto_pip_enabled"

    // Static callback for MainActivity to notify PiP mode changes
    private var pipModeCallback: ((Boolean) -> Unit)? = null

    // Saved backgrounds for restoration after PiP
    private val savedBackgrounds = mutableMapOf<View, Drawable?>()
    private var isPipActive = false

    fun notifyPipModeChanged(isInPip: Boolean) {
      Log.d(TAG, "notifyPipModeChanged: isInPip=$isInPip")
      pipModeCallback?.invoke(isInPip)
    }

    /**
     * Called from MainActivity.onPictureInPictureModeChanged with the Activity reference.
     * Makes all ancestor views of the mpv SurfaceView transparent during PiP so the
     * SurfaceView's "hole" in the window surface works correctly — the video surface
     * behind the window becomes visible. Also sets setZOrderOnTop as a fallback.
     * This avoids reparenting which destroys the EGL rendering context.
     */
    fun notifyPipModeChanged(isInPip: Boolean, activity: Activity) {
      Log.d(TAG, "notifyPipModeChanged: isInPip=$isInPip (with activity)")
      pipModeCallback?.invoke(isInPip)
      handlePipVisibility(activity, isInPip)
    }

    private fun handlePipVisibility(activity: Activity, isInPip: Boolean) {
      val rootView = activity.findViewById<ViewGroup>(android.R.id.content) ?: run {
        Log.w(TAG, "Root content view not found")
        return
      }

      val surfaceView = findTaggedSurfaceView(rootView)
      if (surfaceView == null) {
        Log.d(TAG, "No tagged mpv SurfaceView found, skipping PiP visibility")
        return
      }

      if (isInPip) {
        isPipActive = true
        savedBackgrounds.clear()

        // Set z-order on top (works on some devices/API levels)
        surfaceView.setZOrderOnTop(true)
        Log.d(TAG, "Set SurfaceView z-order on top")

        // Walk up the view hierarchy and make all ancestors transparent
        // This ensures the SurfaceView "hole" isn't covered by opaque backgrounds
        var view: View? = surfaceView.parent as? View
        var depth = 0
        while (view != null) {
          val bg = view.background
          if (bg != null) {
            savedBackgrounds[view] = bg
            view.background = null
            Log.d(TAG, "  Cleared background on ${view.javaClass.simpleName} at depth $depth")
          }
          // Also clear any background color set directly
          view.setBackgroundColor(Color.TRANSPARENT)
          depth++
          view = view.parent as? View
        }

        // Also make the window background transparent
        activity.window.setBackgroundDrawable(ColorDrawable(Color.TRANSPARENT))
        Log.d(TAG, "Made $depth ancestor views transparent for PiP")
      } else {
        if (!isPipActive) return
        isPipActive = false

        // Restore z-order
        surfaceView.setZOrderOnTop(false)

        // Restore saved backgrounds
        for ((view, bg) in savedBackgrounds) {
          if (bg != null) {
            view.background = bg
          }
        }
        savedBackgrounds.clear()

        // Restore window background
        activity.window.setBackgroundDrawable(ColorDrawable(Color.BLACK))
        Log.d(TAG, "Restored ancestor backgrounds after PiP")
      }
    }

    /**
     * Recursively find a SurfaceView tagged as "mpv_player_surface" in the view hierarchy.
     */
    private fun findTaggedSurfaceView(viewGroup: ViewGroup): SurfaceView? {
      for (i in 0 until viewGroup.childCount) {
        val child = viewGroup.getChildAt(i)
        if (child is SurfaceView && child.tag == "mpv_player_surface") return child
        if (child is ViewGroup) {
          val found = findTaggedSurfaceView(child)
          if (found != null) return found
        }
      }
      return null
    }
  }

  private val context: Context
    get() = requireNotNull(appContext.reactContext)

  override fun definition() = ModuleDefinition {
    Name("PipManager")

    Events("onPipModeChanged")

    OnCreate {
      pipModeCallback = { isInPip ->
        Log.d(TAG, "Sending onPipModeChanged event: isActive=$isInPip")
        sendEvent("onPipModeChanged", mapOf("isActive" to isInPip))
      }
      Log.d(TAG, "PiP mode callback registered")
    }

    OnDestroy {
      pipModeCallback = null
      Log.d(TAG, "PiP mode callback cleared")
    }

    Function("enableAutoPip") {
      Log.d(TAG, "enableAutoPip called")
      val prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
      prefs.edit().putBoolean(KEY_AUTO_PIP_ENABLED, true).apply()
      Log.d(TAG, "auto_pip_enabled set to true")
    }

    Function("disableAutoPip") {
      Log.d(TAG, "disableAutoPip called")
      val prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
      prefs.edit().putBoolean(KEY_AUTO_PIP_ENABLED, false).apply()
      Log.d(TAG, "auto_pip_enabled set to false")
    }

    Function("isAutoPipEnabled") {
      val prefs = context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)
      val enabled = prefs.getBoolean(KEY_AUTO_PIP_ENABLED, false)
      Log.d(TAG, "isAutoPipEnabled: $enabled")
      enabled
    }

    Function("enterPip") {
      Log.d(TAG, "enterPip called")
      if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
        Log.w(TAG, "PiP not supported on this API level")
        return@Function false
      }
      val activity = appContext.currentActivity
      if (activity == null) {
        Log.w(TAG, "No current activity")
        return@Function false
      }
      try {
        val params = PictureInPictureParams.Builder()
          .setAspectRatio(Rational(16, 9))
          .build()
        activity.enterPictureInPictureMode(params)
        Log.d(TAG, "enterPictureInPictureMode called successfully")
        true
      } catch (e: Exception) {
        Log.e(TAG, "Failed to enter PiP: ${e.message}")
        false
      }
    }
  }
}
