package com.strmr.mpvplayer

import android.content.Context
import android.content.res.ColorStateList
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.os.Handler
import android.os.Looper
import android.util.TypedValue
import android.view.Gravity
import android.view.KeyEvent
import android.view.View
import android.widget.FrameLayout
import android.widget.LinearLayout
import android.widget.SeekBar
import android.widget.TextView

/**
 * TV controls overlay matching the React Native player design.
 *
 * Layout:
 *   Top: title text (with gradient scrim behind it)
 *   Center: seek indicator / pause indicator (independent of controls visibility)
 *   Bottom: rounded container with seek bar, time labels, track buttons
 *
 * Auto-hides after 3 seconds of inactivity.
 */
class PlayerControlsView(context: Context) : FrameLayout(context) {

    interface Listener {
        fun onPlayPauseToggle()
        fun onSeekTo(positionSeconds: Int)
        fun onAudioTrackClicked()
        fun onSubtitleTrackClicked()
    }

    companion object {
        private const val AUTO_HIDE_MS = 3000L
        private const val SEEK_DEBOUNCE_MS = 700L

        // Theme colors matching RN dark theme
        private const val SCRIM_COLOR = 0xB80B0B0F.toInt()       // rgba(11,11,15,0.72)
        private const val ACCENT_COLOR = 0xFF3F66FF.toInt()       // Blue accent
        private const val TRACK_BG_COLOR = 0xFF2B2F3C.toInt()     // Seek bar track
        private const val TEXT_PRIMARY = 0xFFFFFFFF.toInt()        // White
        private const val TEXT_SECONDARY = 0xFFC7CAD6.toInt()      // Muted white
        private const val TEXT_DISABLED = 0xFF555866.toInt()        // Disabled
        private const val SEEK_TARGET_COLOR = 0xFF4CAF50.toInt()   // Green
        private const val BUTTON_BG = 0x33FFFFFF.toInt()           // 20% white
        private const val BUTTON_BG_FOCUSED = 0x55FFFFFF.toInt()   // 33% white
        private const val CORNER_RADIUS_DP = 16f
    }

    var listener: Listener? = null

    // Top area
    private val topBar: View
    private val titleText: TextView

    // Bottom container (rounded)
    private val bottomContainer: LinearLayout
    private val seekBar: SeekBar
    private val currentTimeText: TextView
    private val durationText: TextView
    private val audioButton: TextView
    private val audioLabel: TextView
    private val subtitleButton: TextView
    private val subtitleLabel: TextView

    // Center indicators
    private val seekIndicator: TextView
    private val pauseIndicator: TextView

    private val handler = Handler(Looper.getMainLooper())
    private var controlsVisible = false
    private var durationSeconds = 0
    private var isSeeking = false

    private val hideRunnable = Runnable { hide() }
    private var pendingSeekPosition = 0
    private val seekApplyRunnable = Runnable {
        isSeeking = false
        listener?.onSeekTo(pendingSeekPosition)
    }

    private fun dp(value: Float): Int =
        TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, value, resources.displayMetrics).toInt()

    init {
        layoutParams = LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.MATCH_PARENT)

        // === Top bar with gradient scrim ===
        val topGradient = View(context).apply {
            background = GradientDrawable(
                GradientDrawable.Orientation.TOP_BOTTOM,
                intArrayOf(0xCC000000.toInt(), 0x00000000)
            )
            visibility = View.GONE
        }
        addView(topGradient, LayoutParams(LayoutParams.MATCH_PARENT, dp(120f), Gravity.TOP))

        val topLayout = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(dp(32f), dp(24f), dp(32f), dp(16f))
            gravity = Gravity.CENTER_VERTICAL
            visibility = View.GONE
        }
        titleText = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 24f)
            typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
            maxLines = 1
            isSingleLine = true
            setShadowLayer(4f, 0f, 2f, 0x88000000.toInt())
        }
        topLayout.addView(titleText, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))
        addView(topLayout, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.WRAP_CONTENT, Gravity.TOP))
        topBar = topLayout
        // Store gradient ref for show/hide
        tag = topGradient

        // === Bottom container (rounded, scrim background) ===
        bottomContainer = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            background = GradientDrawable().apply {
                setColor(SCRIM_COLOR)
                cornerRadius = dp(CORNER_RADIUS_DP).toFloat()
            }
            setPadding(dp(20f), dp(16f), dp(20f), dp(20f))
            visibility = View.GONE
        }

        // -- Seek bar row --
        seekBar = SeekBar(context).apply {
            max = 0
            keyProgressIncrement = 10
            isFocusable = true
            isFocusableInTouchMode = true
            progressTintList = ColorStateList.valueOf(ACCENT_COLOR)
            progressBackgroundTintList = ColorStateList.valueOf(TRACK_BG_COLOR)
            thumbTintList = ColorStateList.valueOf(ACCENT_COLOR)
            minimumHeight = dp(6f)
            maxHeight = dp(6f)
            setPadding(0, dp(8f), 0, dp(8f))
            setOnSeekBarChangeListener(object : SeekBar.OnSeekBarChangeListener {
                override fun onProgressChanged(sb: SeekBar, progress: Int, fromUser: Boolean) {
                    if (fromUser) {
                        isSeeking = true
                        pendingSeekPosition = progress
                        currentTimeText.text = formatTime(progress)
                        resetAutoHide()
                        handler.removeCallbacks(seekApplyRunnable)
                        handler.postDelayed(seekApplyRunnable, SEEK_DEBOUNCE_MS)
                    }
                }
                override fun onStartTrackingTouch(sb: SeekBar) {
                    isSeeking = true
                    handler.removeCallbacks(hideRunnable)
                }
                override fun onStopTrackingTouch(sb: SeekBar) {
                    handler.removeCallbacks(seekApplyRunnable)
                    isSeeking = false
                    listener?.onSeekTo(sb.progress)
                    resetAutoHide()
                }
            })
            // Focus highlight for D-pad
            onFocusChangeListener = OnFocusChangeListener { _, hasFocus ->
                thumbTintList = ColorStateList.valueOf(
                    if (hasFocus) Color.WHITE else ACCENT_COLOR
                )
            }
        }
        bottomContainer.addView(seekBar, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))

        // -- Time row --
        val timeRow = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
            setPadding(dp(2f), 0, dp(2f), 0)
        }
        currentTimeText = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 15f)
            typeface = Typeface.create("sans-serif", Typeface.NORMAL)
            text = "0:00"
        }
        durationText = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 15f)
            typeface = Typeface.create("sans-serif", Typeface.NORMAL)
            text = "0:00"
        }
        val timeSpacer = View(context)
        timeRow.addView(currentTimeText, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))
        timeRow.addView(timeSpacer, LinearLayout.LayoutParams(
            0, 0, 1f
        ))
        timeRow.addView(durationText, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))
        bottomContainer.addView(timeRow, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { topMargin = dp(4f) })

        // -- Track buttons row --
        val buttonRow = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.START or Gravity.CENTER_VERTICAL
        }

        // Audio button group
        val audioGroup = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        audioButton = makeTrackButton(context, "\uD83D\uDD0A") { // 🔊
            listener?.onAudioTrackClicked()
            resetAutoHide()
        }
        audioLabel = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            text = "Audio"
            setPadding(dp(6f), 0, 0, 0)
        }
        audioGroup.addView(audioButton)
        audioGroup.addView(audioLabel)

        // Subtitle button group
        val subtitleGroup = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        subtitleButton = makeTrackButton(context, "\uD83D\uDCAC") { // 💬
            listener?.onSubtitleTrackClicked()
            resetAutoHide()
        }
        subtitleLabel = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            text = "Subtitles"
            setPadding(dp(6f), 0, 0, 0)
        }
        subtitleGroup.addView(subtitleButton)
        subtitleGroup.addView(subtitleLabel)

        buttonRow.addView(audioGroup, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { marginEnd = dp(24f) })
        buttonRow.addView(subtitleGroup, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))

        bottomContainer.addView(buttonRow, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { topMargin = dp(12f) })

        // Position bottom container with margins
        addView(bottomContainer, LayoutParams(
            LayoutParams.MATCH_PARENT, LayoutParams.WRAP_CONTENT, Gravity.BOTTOM
        ).apply {
            setMargins(dp(24f), 0, dp(24f), dp(24f))
        })

        // === Center seek indicator ===
        seekIndicator = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 36f)
            typeface = Typeface.create("sans-serif", Typeface.BOLD)
            letterSpacing = 0.05f
            background = GradientDrawable().apply {
                setColor(0xCC0B0B0F.toInt())
                cornerRadius = dp(12f).toFloat()
            }
            setPadding(dp(28f), dp(14f), dp(28f), dp(14f))
            visibility = View.GONE
        }
        addView(seekIndicator, LayoutParams(
            LayoutParams.WRAP_CONTENT, LayoutParams.WRAP_CONTENT, Gravity.CENTER
        ))

        // === Center pause indicator ===
        pauseIndicator = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 52f)
            typeface = Typeface.DEFAULT_BOLD
            background = GradientDrawable().apply {
                setColor(0xCC0B0B0F.toInt())
                cornerRadius = dp(16f).toFloat()
            }
            setPadding(dp(36f), dp(18f), dp(36f), dp(18f))
            visibility = View.GONE
        }
        addView(pauseIndicator, LayoutParams(
            LayoutParams.WRAP_CONTENT, LayoutParams.WRAP_CONTENT, Gravity.CENTER
        ))
    }

    private fun makeTrackButton(context: Context, icon: String, onClick: () -> Unit): TextView {
        return TextView(context).apply {
            text = icon
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 18f)
            gravity = Gravity.CENTER
            val size = dp(40f)
            minimumWidth = size
            minimumHeight = size
            background = GradientDrawable().apply {
                setColor(BUTTON_BG)
                cornerRadius = dp(8f).toFloat()
                setStroke(dp(1f), 0x33FFFFFF.toInt())
            }
            setPadding(dp(8f), dp(6f), dp(8f), dp(6f))
            isFocusable = true
            isFocusableInTouchMode = true
            setOnClickListener { onClick() }
            setOnKeyListener { _, keyCode, event ->
                if (event.action == KeyEvent.ACTION_DOWN &&
                    (keyCode == KeyEvent.KEYCODE_DPAD_CENTER || keyCode == KeyEvent.KEYCODE_ENTER)) {
                    onClick()
                    true
                } else false
            }
            onFocusChangeListener = OnFocusChangeListener { v, hasFocus ->
                val btn = v as TextView
                btn.background = GradientDrawable().apply {
                    setColor(if (hasFocus) BUTTON_BG_FOCUSED else BUTTON_BG)
                    cornerRadius = dp(8f).toFloat()
                    if (hasFocus) {
                        setStroke(dp(2f), ACCENT_COLOR)
                    } else {
                        setStroke(dp(1f), 0x33FFFFFF.toInt())
                    }
                }
            }
        }
    }

    // === Public API ===

    fun setTitle(title: String) {
        titleText.text = title
    }

    fun setDuration(seconds: Int) {
        durationSeconds = seconds
        seekBar.max = seconds
        durationText.text = formatTime(seconds)
    }

    fun updateTime(currentSeconds: Int) {
        if (!isSeeking) {
            seekBar.progress = currentSeconds
            currentTimeText.text = formatTime(currentSeconds)
        }
    }

    fun setAudioTrackLabel(label: String) {
        audioLabel.text = label.ifEmpty { "Audio" }
    }

    fun setSubtitleTrackLabel(label: String) {
        subtitleLabel.text = label.ifEmpty { "Subtitles" }
    }

    private val hidePauseRunnable = Runnable { pauseIndicator.visibility = View.GONE }

    fun showPauseIndicator(paused: Boolean) {
        pauseIndicator.text = if (paused) "\u23F8" else "\u25B6"
        pauseIndicator.visibility = View.VISIBLE
        handler.removeCallbacks(hidePauseRunnable)
        handler.postDelayed(hidePauseRunnable, 800)
    }

    private val hideSeekIndicatorRunnable = Runnable { seekIndicator.visibility = View.GONE }

    fun showSeekIndicator(text: String) {
        seekIndicator.text = text
        seekIndicator.visibility = View.VISIBLE
        handler.removeCallbacks(hideSeekIndicatorRunnable)
        handler.postDelayed(hideSeekIndicatorRunnable, 1200)
    }

    fun hideSeekIndicator() {
        seekIndicator.visibility = View.GONE
    }

    fun show() {
        if (controlsVisible) {
            resetAutoHide()
            return
        }
        controlsVisible = true
        val topGradient = tag as? View
        topGradient?.visibility = View.VISIBLE
        topBar.visibility = View.VISIBLE
        bottomContainer.visibility = View.VISIBLE
        seekBar.requestFocus()
        resetAutoHide()
    }

    fun hide() {
        controlsVisible = false
        val topGradient = tag as? View
        topGradient?.visibility = View.GONE
        topBar.visibility = View.GONE
        bottomContainer.visibility = View.GONE
        handler.removeCallbacks(hideRunnable)
    }

    fun toggle() {
        if (controlsVisible) hide() else show()
    }

    fun isShowing(): Boolean = controlsVisible

    private fun resetAutoHide() {
        handler.removeCallbacks(hideRunnable)
        handler.postDelayed(hideRunnable, AUTO_HIDE_MS)
    }

    fun cleanup() {
        handler.removeCallbacksAndMessages(null)
    }

    private fun formatTime(seconds: Int): String {
        val s = seconds.coerceAtLeast(0)
        val h = s / 3600
        val m = (s % 3600) / 60
        val sec = s % 60
        return if (h > 0) String.format("%d:%02d:%02d", h, m, sec)
        else String.format("%d:%02d", m, sec)
    }
}
