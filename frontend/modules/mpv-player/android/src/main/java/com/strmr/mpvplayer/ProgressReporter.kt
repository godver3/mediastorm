package com.strmr.mpvplayer

import android.os.Handler
import android.os.Looper
import android.util.Log
import okhttp3.Call
import okhttp3.Callback
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import org.json.JSONObject
import java.io.IOException
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale
import java.util.TimeZone
import java.util.concurrent.TimeUnit
import kotlin.math.abs

/**
 * Reports playback progress to the backend via HTTP POST.
 * Sends updates every 10 seconds when position has changed by at least 5 seconds.
 */
class ProgressReporter(
    private val backendUrl: String,
    private val userId: String,
    private val authToken: String,
    private val mediaType: String,
    private val itemId: String,
    private val extraFields: Map<String, Any?>
) {
    companion object {
        private const val TAG = "ProgressReporter"
        private const val INTERVAL_MS = 10_000L
        private const val MIN_POSITION_CHANGE = 5.0
    }

    private val client = OkHttpClient.Builder()
        .connectTimeout(10, TimeUnit.SECONDS)
        .writeTimeout(10, TimeUnit.SECONDS)
        .readTimeout(10, TimeUnit.SECONDS)
        .build()
    private val handler = Handler(Looper.getMainLooper())
    private val jsonMediaType = "application/json; charset=utf-8".toMediaType()
    private val isoFormat = SimpleDateFormat("yyyy-MM-dd'T'HH:mm:ss.SSS'Z'", Locale.US).apply {
        timeZone = TimeZone.getTimeZone("UTC")
    }

    private var lastReportedPosition = -1.0
    private var currentPosition = 0.0
    private var currentDuration = 0.0
    private var running = false

    private val tickRunnable = object : Runnable {
        override fun run() {
            if (!running) return
            maybeSendReport()
            handler.postDelayed(this, INTERVAL_MS)
        }
    }

    fun start() {
        running = true
        handler.postDelayed(tickRunnable, INTERVAL_MS)
    }

    fun stop() {
        running = false
        handler.removeCallbacks(tickRunnable)
    }

    fun updatePosition(position: Double, duration: Double) {
        currentPosition = position
        if (duration > 0) currentDuration = duration
    }

    fun sendFinalUpdate() {
        sendReport(currentPosition, currentDuration)
    }

    private fun maybeSendReport() {
        if (currentDuration <= 0) return
        if (abs(currentPosition - lastReportedPosition) < MIN_POSITION_CHANGE) return
        sendReport(currentPosition, currentDuration)
    }

    private fun sendReport(position: Double, duration: Double) {
        if (duration <= 0) return
        lastReportedPosition = position

        val body = JSONObject().apply {
            put("mediaType", mediaType)
            put("itemId", itemId)
            put("position", position)
            put("duration", duration)
            put("timestamp", isoFormat.format(Date()))
            for ((key, value) in extraFields) {
                when (value) {
                    is Int -> put(key, value)
                    is Long -> put(key, value.toInt())
                    is String -> if (value.isNotEmpty()) put(key, value)
                }
            }
            // Build externalIds sub-object
            val externalIds = JSONObject()
            extraFields["imdbId"]?.let { if (it is String && it.isNotEmpty()) externalIds.put("imdb", it) }
            extraFields["tvdbId"]?.let { if (it is String && it.isNotEmpty()) externalIds.put("tvdb", it) }
            if (externalIds.length() > 0) put("externalIds", externalIds)
        }

        val url = "${backendUrl.trimEnd('/')}/api/users/$userId/history/progress"
        val request = Request.Builder()
            .url(url)
            .post(body.toString().toRequestBody(jsonMediaType))
            .addHeader("Authorization", "Bearer $authToken")
            .addHeader("Content-Type", "application/json")
            .build()

        client.newCall(request).enqueue(object : Callback {
            override fun onFailure(call: Call, e: IOException) {
                Log.w(TAG, "Progress report failed: ${e.message}")
            }

            override fun onResponse(call: Call, response: Response) {
                response.close()
                if (!response.isSuccessful) {
                    Log.w(TAG, "Progress report returned ${response.code}")
                }
            }
        })
    }
}
