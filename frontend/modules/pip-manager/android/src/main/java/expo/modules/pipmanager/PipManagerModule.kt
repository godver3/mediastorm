package expo.modules.pipmanager

import android.content.Context
import android.util.Log
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition

class PipManagerModule : Module() {
  companion object {
    private const val TAG = "PipManagerModule"
    private const val PREFS_NAME = "pip_manager_prefs"
    private const val KEY_AUTO_PIP_ENABLED = "auto_pip_enabled"
  }

  private val context: Context
    get() = requireNotNull(appContext.reactContext)

  override fun definition() = ModuleDefinition {
    Name("PipManager")

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
  }
}
