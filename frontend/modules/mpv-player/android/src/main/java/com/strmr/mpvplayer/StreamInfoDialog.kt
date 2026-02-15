package com.strmr.mpvplayer

import android.app.Activity
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.util.TypedValue
import android.view.Gravity
import android.view.KeyEvent
import android.view.View
import android.view.ViewGroup
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

object StreamInfoDialog {

    data class StreamInfoData(
        val title: String = "",
        val seasonNumber: Int = 0,
        val episodeNumber: Int = 0,
        val seriesName: String = "",
        val episodeName: String = "",
        val resolution: String = "",
        val videoCodec: String = "",
        val videoBitrate: Long = 0,
        val frameRate: String = "",
        val audioCodec: String = "",
        val audioChannels: String = "",
        val audioBitrate: Long = 0,
        val colorTransfer: String = "",
        val colorPrimaries: String = "",
        val colorSpace: String = "",
        val isHDR: Boolean = false,
        val isDolbyVision: Boolean = false,
        val dolbyVisionProfile: String = "",
        val sourcePath: String = "",
        val passthroughName: String = "",
        val passthroughDescription: String = "",
    )

    // Colors matching RN StreamInfoModal.tsx
    private const val SCRIM_COLOR = 0xD9000000.toInt()         // rgba(0,0,0,0.85)
    private const val MODAL_BG = 0xFF1F1F2A.toInt()            // background.elevated
    private const val BORDER_COLOR = 0xFF2B2F3C.toInt()        // border.subtle
    private const val ACCENT_COLOR = 0xFF3F66FF.toInt()        // accent.primary
    private const val TEXT_PRIMARY = 0xFFFFFFFF.toInt()
    private const val TEXT_SECONDARY = 0xFFC7CAD6.toInt()
    private const val SECTION_BG = 0x08FFFFFF.toInt()          // rgba(255,255,255,0.03) — section bg
    private const val ROW_BG = 0x0DFFFFFF.toInt()              // rgba(255,255,255,0.05) — row bg
    private const val SURFACE_BG = 0xFF16161F.toInt()          // background.surface

    private fun dp(view: View, value: Float): Int =
        TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, value, view.resources.displayMetrics).toInt()

    private fun dp(activity: Activity, value: Float): Int =
        TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, value, activity.resources.displayMetrics).toInt()

    fun show(activity: Activity, data: StreamInfoData, onDismiss: (() -> Unit)? = null) {
        val decorView = activity.window.decorView as ViewGroup
        var overlay: FrameLayout? = null

        fun dismiss() {
            overlay?.let { decorView.removeView(it) }
            overlay = null
            onDismiss?.invoke()
        }

        overlay = FrameLayout(activity).apply {
            setBackgroundColor(SCRIM_COLOR)
            isFocusable = true
            isFocusableInTouchMode = true
            setOnKeyListener { _, keyCode, event ->
                if (event.action == KeyEvent.ACTION_DOWN && keyCode == KeyEvent.KEYCODE_BACK) {
                    dismiss()
                    true
                } else false
            }
        }

        val modal = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            background = GradientDrawable().apply {
                setColor(MODAL_BG)
                cornerRadius = dp(activity, 24f).toFloat()
                setStroke(dp(activity, 2f), BORDER_COLOR)
            }
            clipToOutline = true
        }

        // Header
        val headerContainer = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(this, 32f), dp(this, 20f), dp(this, 32f), dp(this, 16f))
        }
        val headerTitle = TextView(activity).apply {
            text = "Stream Information"
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 24f)
            typeface = Typeface.create("sans-serif", Typeface.BOLD)
        }
        headerContainer.addView(headerTitle, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))
        modal.addView(headerContainer)

        // Divider
        modal.addView(View(activity).apply {
            setBackgroundColor(BORDER_COLOR)
        }, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, dp(activity, 1f)
        ))

        // Scrollable content
        val scrollView = ScrollView(activity).apply { isFocusable = false }
        val content = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(this, 16f), dp(this, 12f), dp(this, 16f), dp(this, 8f))
        }

        // Media section
        val mediaRows = mutableListOf<Pair<String, String>>()
        if (data.title.isNotEmpty()) {
            mediaRows.add("Title" to data.title)
        }
        if (data.seasonNumber > 0 && data.episodeNumber > 0) {
            val code = "S${data.seasonNumber.toString().padStart(2, '0')}E${data.episodeNumber.toString().padStart(2, '0')}"
            mediaRows.add("Episode" to code)
        }
        if (data.episodeName.isNotEmpty()) {
            mediaRows.add("Episode Name" to data.episodeName)
        }
        if (mediaRows.isNotEmpty()) {
            addSection(activity, content, "Media", mediaRows)
        }

        // Source section (AIOStreams passthrough)
        val sourceRows = mutableListOf<Pair<String, String>>()
        if (data.passthroughName.isNotEmpty()) {
            sourceRows.add("Source" to data.passthroughName)
        }
        if (data.passthroughDescription.isNotEmpty()) {
            sourceRows.add("Description" to data.passthroughDescription)
        }
        if (sourceRows.isNotEmpty()) {
            addSection(activity, content, "Source", sourceRows)
        }

        // File section
        if (data.sourcePath.isNotEmpty()) {
            val filename = extractFileName(data.sourcePath)
            if (filename.isNotEmpty()) {
                addSection(activity, content, "File", listOf("Filename" to filename))
            }
        }

        // Video section
        val videoRows = mutableListOf<Pair<String, String>>()
        if (data.resolution.isNotEmpty()) {
            videoRows.add("Resolution" to formatResolutionDisplay(data.resolution))
        }
        if (data.videoCodec.isNotEmpty()) {
            videoRows.add("Codec" to data.videoCodec.uppercase())
        }
        if (data.videoBitrate > 0) {
            videoRows.add("Bitrate" to formatBitrate(data.videoBitrate))
        }
        if (data.frameRate.isNotEmpty()) {
            videoRows.add("Frame Rate" to formatFrameRate(data.frameRate))
        }
        if (videoRows.isNotEmpty()) {
            addSection(activity, content, "Video", videoRows)
        }

        // Audio section
        val audioRows = mutableListOf<Pair<String, String>>()
        if (data.audioCodec.isNotEmpty()) {
            audioRows.add("Codec" to data.audioCodec.uppercase())
        }
        if (data.audioChannels.isNotEmpty()) {
            audioRows.add("Channels" to data.audioChannels)
        }
        if (data.audioBitrate > 0) {
            audioRows.add("Bitrate" to formatBitrate(data.audioBitrate))
        }
        if (audioRows.isNotEmpty()) {
            addSection(activity, content, "Audio", audioRows)
        }

        // Color section
        val colorRows = mutableListOf<Pair<String, String>>()
        if (data.isDolbyVision) {
            val profileLabel = if (data.dolbyVisionProfile.isNotEmpty()) {
                val numMatch = Regex("\\d+").find(data.dolbyVisionProfile)
                if (numMatch != null) "Dolby Vision Profile ${numMatch.value.trimStart('0').ifEmpty { "0" }}"
                else "Dolby Vision"
            } else "Dolby Vision"
            colorRows.add("HDR Format" to profileLabel)
        } else if (data.isHDR) {
            colorRows.add("HDR Format" to "HDR10")
        }
        if (data.colorTransfer.isNotEmpty()) {
            colorRows.add("Transfer" to formatColorInfo(data.colorTransfer))
        }
        if (data.colorPrimaries.isNotEmpty()) {
            colorRows.add("Primaries" to formatColorInfo(data.colorPrimaries))
        }
        if (data.colorSpace.isNotEmpty()) {
            colorRows.add("Color Space" to formatColorInfo(data.colorSpace))
        }
        if (colorRows.isNotEmpty()) {
            addSection(activity, content, "Color", colorRows)
        }

        // Playback section
        addSection(activity, content, "Playback", listOf("Player" to "MPV (Native)"))

        scrollView.addView(content, FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.WRAP_CONTENT
        ))
        modal.addView(scrollView, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f
        ))

        // Footer
        val footer = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(dp(this, 32f), dp(this, 12f), dp(this, 32f), dp(this, 16f))
        }
        footer.addView(View(activity).apply {
            setBackgroundColor(BORDER_COLOR)
        }, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, dp(activity, 1f)
        ).apply { bottomMargin = dp(activity, 16f) })

        val closeButton = TextView(activity).apply {
            text = "Close"
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 16f)
            typeface = Typeface.create("sans-serif", Typeface.BOLD)
            gravity = Gravity.CENTER
            minimumWidth = dp(this, 200f)
            setPadding(dp(this, 32f), dp(this, 12f), dp(this, 32f), dp(this, 12f))
            isFocusable = true
            isFocusableInTouchMode = true
            background = GradientDrawable().apply {
                setColor(SURFACE_BG)
                cornerRadius = dp(activity, 8f).toFloat()
                setStroke(dp(activity, 2f), BORDER_COLOR)
            }
            setOnClickListener { dismiss() }
            setOnKeyListener { _, keyCode, event ->
                if (event.action == KeyEvent.ACTION_DOWN &&
                    (keyCode == KeyEvent.KEYCODE_DPAD_CENTER || keyCode == KeyEvent.KEYCODE_ENTER)) {
                    dismiss()
                    true
                } else false
            }
            // Matches RN FocusablePressable: focused = solid accent fill
            onFocusChangeListener = View.OnFocusChangeListener { v, hasFocus ->
                v.background = GradientDrawable().apply {
                    setColor(if (hasFocus) ACCENT_COLOR else SURFACE_BG)
                    cornerRadius = dp(v, 8f).toFloat()
                    setStroke(dp(v, 2f), if (hasFocus) ACCENT_COLOR else BORDER_COLOR)
                }
            }
        }
        footer.addView(closeButton, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))
        modal.addView(footer)

        overlay!!.addView(modal, FrameLayout.LayoutParams(
            dp(modal, 700f).coerceAtMost(activity.resources.displayMetrics.widthPixels - dp(modal, 48f)),
            FrameLayout.LayoutParams.WRAP_CONTENT,
            Gravity.CENTER
        ).apply {
            val maxH = (activity.resources.displayMetrics.heightPixels * 0.8).toInt()
            modal.post {
                if (modal.height > maxH) {
                    val lp = modal.layoutParams as FrameLayout.LayoutParams
                    lp.height = maxH
                    modal.layoutParams = lp
                }
            }
        })

        decorView.addView(overlay, FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.MATCH_PARENT
        ))

        closeButton.post { closeButton.requestFocus() }
    }

    private fun addSection(activity: Activity, parent: LinearLayout, title: String, rows: List<Pair<String, String>>) {
        // Section container with subtle bg (matches RN rgba(255,255,255,0.03))
        val sectionContainer = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            background = GradientDrawable().apply {
                setColor(SECTION_BG)
                cornerRadius = dp(activity, 16f).toFloat()
            }
            setPadding(dp(this, 16f), dp(this, 12f), dp(this, 16f), dp(this, 12f))
        }

        val header = TextView(activity).apply {
            text = title.uppercase()
            setTextColor(TEXT_SECONDARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 13f)
            typeface = Typeface.create("sans-serif-medium", Typeface.BOLD)
            letterSpacing = 0.08f
        }
        sectionContainer.addView(header, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { bottomMargin = dp(activity, 8f) })

        for ((label, value) in rows) {
            // Info row with subtle bg (matches RN rgba(255,255,255,0.05))
            val row = LinearLayout(activity).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
                background = GradientDrawable().apply {
                    setColor(ROW_BG)
                    cornerRadius = dp(activity, 4f).toFloat()
                }
                setPadding(dp(this, 8f), dp(this, 6f), dp(this, 8f), dp(this, 6f))
            }

            val labelView = TextView(activity).apply {
                text = label
                setTextColor(TEXT_SECONDARY)
                setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            }
            row.addView(labelView, LinearLayout.LayoutParams(
                0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f
            ))

            val valueView = TextView(activity).apply {
                text = value
                setTextColor(TEXT_PRIMARY)
                setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
                typeface = Typeface.create("sans-serif", Typeface.NORMAL)
                maxLines = 2
                gravity = Gravity.END
            }
            row.addView(valueView, LinearLayout.LayoutParams(
                0, LinearLayout.LayoutParams.WRAP_CONTENT, 2f
            ))

            sectionContainer.addView(row, LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
            ).apply { bottomMargin = dp(activity, 4f) })
        }

        parent.addView(sectionContainer, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { bottomMargin = dp(activity, 12f) })
    }

    // === Formatting helpers (ported from StreamInfoModal.tsx) ===

    private fun formatBitrate(bps: Long): String {
        return when {
            bps >= 1_000_000 -> String.format("%.1f Mbps", bps / 1_000_000.0)
            bps >= 1_000 -> "${bps / 1_000} kbps"
            else -> "$bps bps"
        }
    }

    private fun formatColorInfo(value: String): String {
        return when (value.lowercase()) {
            "smpte2084" -> "PQ (HDR)"
            "arib-std-b67" -> "HLG"
            "bt2020" -> "BT.2020"
            "bt2020nc" -> "BT.2020 NCL"
            "bt709" -> "BT.709"
            "smpte170m" -> "SMPTE 170M"
            "bt470bg" -> "BT.470 BG"
            "iec61966-2-1" -> "sRGB"
            "smpte240m" -> "SMPTE 240M"
            else -> value
        }
    }

    private fun formatResolutionDisplay(resolution: String): String {
        val parts = resolution.split("x")
        if (parts.size == 2) {
            val width = parts[0].toIntOrNull()
            val height = parts[1].toIntOrNull()
            if (width != null && height != null) {
                return "${width}x${height} (${height}p)"
            }
        }
        return resolution
    }

    private fun formatFrameRate(frameRate: String): String {
        // Handle fraction format like "24000/1001"
        val parts = frameRate.split("/")
        if (parts.size == 2) {
            val num = parts[0].toDoubleOrNull()
            val den = parts[1].toDoubleOrNull()
            if (num != null && den != null && den > 0) {
                return String.format("%.3f fps", num / den)
            }
        }
        // Handle decimal format
        val asDouble = frameRate.toDoubleOrNull()
        if (asDouble != null) {
            return String.format("%.3f fps", asDouble)
        }
        return frameRate
    }

    private fun extractFileName(path: String): String {
        if (path.isEmpty()) return ""
        val segments = path.split("/", "\\")
        return segments.lastOrNull()?.takeIf { it.isNotEmpty() } ?: path
    }
}
