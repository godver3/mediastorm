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
 * TV controls overlay for the native Android TV player.
 *
 * Layout:
 *   Top-left:  Exit button
 *   Top-right: Title, HDR/resolution badges, color info
 *   Bottom:    Rounded container with play/skip/seek controls, time labels, track+info buttons
 *   Center:    Seek indicator / pause indicator (independent of controls visibility)
 *
 * Auto-hides after 3 seconds of inactivity.
 */
class PlayerControlsView(context: Context) : FrameLayout(context) {

    interface Listener {
        fun onPlayPauseToggle()
        fun onSeekTo(positionSeconds: Int)
        fun onSkipBackward()
        fun onSkipForward()
        fun onAudioTrackClicked()
        fun onSubtitleTrackClicked()
        fun onInfoClicked()
        fun onExitClicked()
    }

    companion object {
        private const val AUTO_HIDE_MS = 3000L
        private const val SEEK_DEBOUNCE_MS = 700L

        // Theme colors matching RN dark theme (FocusablePressable + Controls.tsx)
        private const val SCRIM_COLOR = 0xB80B0B0F.toInt()       // rgba(11,11,15,0.72)
        private const val ACCENT_COLOR = 0xFF3F66FF.toInt()       // Blue accent
        private const val TRACK_BG_COLOR = 0xFF2B2F3C.toInt()     // Seek bar track / border subtle
        private const val TEXT_PRIMARY = 0xFFFFFFFF.toInt()
        private const val TEXT_SECONDARY = 0xFFC7CAD6.toInt()
        private const val BUTTON_BG = 0x1FFFFFFF.toInt()          // rgba(255,255,255,0.12) — overlay.button
        private const val BUTTON_BORDER = 0xFF2B2F3C.toInt()      // border.subtle
        private const val CORNER_RADIUS_DP = 16f
        private const val INFO_PANEL_BG = 0xA6000000.toInt()      // rgba(0,0,0,0.65) — MediaInfoDisplay bg
        private const val TEXT_INVERSE = 0xFF000000.toInt()        // Black text on focused accent bg

        // Badge colors from MediaInfoDisplay.tsx
        private const val HDR_BADGE_BG = 0xD9FFD700.toInt()
        private const val SDR_BADGE_BG = 0xD99CA3AF.toInt()
        private const val RES_4K_BG = 0xD98A2BE2.toInt()
        private const val RES_1080_BG = 0xD93B82F6.toInt()
        private const val RES_720_BG = 0xD914B8A6.toInt()
        private const val RES_480_BG = 0xD96B7280.toInt()
        private const val BADGE_TEXT_DARK = 0xFF000000.toInt()
    }

    var listener: Listener? = null

    // Top area
    private val topGradient: View
    private val topBar: LinearLayout
    private val exitButton: TextView
    private val titleText: TextView
    private val badgesRow: LinearLayout
    private val hdrBadge: TextView
    private val resBadge: TextView

    // Bottom container
    private val bottomContainer: LinearLayout
    private val playPauseButton: TextView
    private val skipBackButton: TextView
    private val seekBar: SeekBar
    private val skipForwardButton: TextView
    private val currentTimeText: TextView
    private val durationText: TextView
    private val audioButton: TextView
    private val audioLabel: TextView
    private val subtitleButton: TextView
    private val subtitleLabel: TextView
    private val infoButton: TextView
    private val infoLabel: TextView

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

        // === Top gradient scrim ===
        topGradient = View(context).apply {
            background = GradientDrawable(
                GradientDrawable.Orientation.TOP_BOTTOM,
                intArrayOf(0xCC000000.toInt(), 0x00000000)
            )
            visibility = View.GONE
        }
        addView(topGradient, LayoutParams(LayoutParams.MATCH_PARENT, dp(140f), Gravity.TOP))

        // === Top bar ===
        topBar = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            setPadding(dp(32f), dp(24f), dp(32f), dp(16f))
            gravity = Gravity.TOP
            visibility = View.GONE
        }

        // Exit button (left)
        exitButton = makeTextButton(context, "Exit") {
            listener?.onExitClicked()
        }

        topBar.addView(exitButton, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))

        // Spacer
        topBar.addView(View(context), LinearLayout.LayoutParams(0, 0, 1f))

        // Right panel: title + badges (matches MediaInfoDisplay.tsx)
        val rightPanel = LinearLayout(context).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.END
            background = GradientDrawable().apply {
                setColor(INFO_PANEL_BG)
                cornerRadius = dp(12f).toFloat()
            }
            setPadding(dp(16f), dp(10f), dp(16f), dp(10f))
        }

        titleText = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 22f)
            typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
            maxLines = 1
            isSingleLine = true
            gravity = Gravity.END
            setShadowLayer(4f, 0f, 2f, 0x88000000.toInt())
        }
        rightPanel.addView(titleText, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))

        // Badges row
        badgesRow = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.END or Gravity.CENTER_VERTICAL
            visibility = View.GONE
        }

        hdrBadge = makeBadge(context)
        resBadge = makeBadge(context)

        badgesRow.addView(hdrBadge, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { marginEnd = dp(6f) })
        badgesRow.addView(resBadge, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))

        rightPanel.addView(badgesRow, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { topMargin = dp(4f) })

        topBar.addView(rightPanel, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))

        addView(topBar, LayoutParams(LayoutParams.MATCH_PARENT, LayoutParams.WRAP_CONTENT, Gravity.TOP))

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

        // -- Main controls row: play/pause, skip back, seek bar, skip forward --
        val mainRow = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }

        playPauseButton = makeControlButton(context, "\u25B6") { // ▶
            listener?.onPlayPauseToggle()
            resetAutoHide()
        }
        mainRow.addView(playPauseButton, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { marginEnd = dp(8f) })

        // Skip back button (text button, matches RN FocusablePressable layout)
        skipBackButton = makeTextButton(context, "\u25C0 10s") { // ◀ 10s
            listener?.onSkipBackward()
            resetAutoHide()
        }
        mainRow.addView(skipBackButton, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { marginEnd = dp(8f) })

        // Skip forward button (text button)
        skipForwardButton = makeTextButton(context, "30s \u25B6") { // 30s ▶
            listener?.onSkipForward()
            resetAutoHide()
        }
        mainRow.addView(skipForwardButton, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { marginEnd = dp(8f) })

        // Seek bar (right of all buttons, fills remaining space — matches RN TV layout)
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
            onFocusChangeListener = OnFocusChangeListener { _, hasFocus ->
                thumbTintList = ColorStateList.valueOf(
                    if (hasFocus) Color.WHITE else ACCENT_COLOR
                )
            }
        }
        mainRow.addView(seekBar, LinearLayout.LayoutParams(
            0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f
        ))

        bottomContainer.addView(mainRow, LinearLayout.LayoutParams(
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
        timeRow.addView(currentTimeText, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))
        timeRow.addView(View(context), LinearLayout.LayoutParams(0, 0, 1f))
        timeRow.addView(durationText, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))
        bottomContainer.addView(timeRow, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { topMargin = dp(4f) })

        // -- Secondary row: Audio, Subtitle, Info buttons (text buttons matching RN) --
        val secondaryRow = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.START or Gravity.CENTER_VERTICAL
        }

        // Audio button + track label
        val audioGroup = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        audioButton = makeTextButton(context, "Audio") {
            listener?.onAudioTrackClicked()
            resetAutoHide()
        }
        audioLabel = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            text = ""
            setPadding(dp(8f), 0, 0, 0)
        }
        audioGroup.addView(audioButton)
        audioGroup.addView(audioLabel)

        // Subtitle button + track label
        val subtitleGroup = LinearLayout(context).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.CENTER_VERTICAL
        }
        subtitleButton = makeTextButton(context, "Subtitles") {
            listener?.onSubtitleTrackClicked()
            resetAutoHide()
        }
        subtitleLabel = TextView(context).apply {
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            text = ""
            setPadding(dp(8f), 0, 0, 0)
        }
        subtitleGroup.addView(subtitleButton)
        subtitleGroup.addView(subtitleLabel)

        // Info button (text button, no separate label needed)
        infoButton = makeTextButton(context, "Info") {
            listener?.onInfoClicked()
            resetAutoHide()
        }
        infoLabel = TextView(context) // Initialized for field declaration, not displayed

        secondaryRow.addView(audioGroup, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { marginEnd = dp(16f) })
        secondaryRow.addView(subtitleGroup, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { marginEnd = dp(16f) })
        secondaryRow.addView(infoButton, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))

        bottomContainer.addView(secondaryRow, LinearLayout.LayoutParams(
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

        // === Focus navigation wiring ===
        wireUpFocusNavigation()
    }

    private fun makeControlButton(context: Context, icon: String, onClick: () -> Unit): TextView {
        return TextView(context).apply {
            id = View.generateViewId()
            text = icon
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 18f)
            gravity = Gravity.CENTER
            val size = dp(44f)
            minimumWidth = size
            minimumHeight = size
            // Matches RN FocusablePressable secondary variant unfocused
            background = GradientDrawable().apply {
                setColor(BUTTON_BG)
                cornerRadius = dp(8f).toFloat()
                setStroke(dp(1f), BUTTON_BORDER)
            }
            setPadding(dp(8f), dp(8f), dp(8f), dp(8f))
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
            // Matches RN FocusablePressable: focused = solid accent fill + inverted text
            onFocusChangeListener = OnFocusChangeListener { v, hasFocus ->
                (v as TextView).setTextColor(if (hasFocus) TEXT_INVERSE else TEXT_PRIMARY)
                v.background = GradientDrawable().apply {
                    setColor(if (hasFocus) ACCENT_COLOR else BUTTON_BG)
                    cornerRadius = dp(8f).toFloat()
                    setStroke(dp(if (hasFocus) 2f else 1f), if (hasFocus) ACCENT_COLOR else BUTTON_BORDER)
                }
            }
        }
    }

    private fun makeTextButton(context: Context, label: String, onClick: () -> Unit): TextView {
        return TextView(context).apply {
            id = View.generateViewId()
            text = label
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
            typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
            gravity = Gravity.CENTER
            setPadding(dp(14f), dp(8f), dp(14f), dp(8f))
            background = GradientDrawable().apply {
                setColor(BUTTON_BG)
                cornerRadius = dp(8f).toFloat()
                setStroke(dp(1f), BUTTON_BORDER)
            }
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
            // Matches RN FocusablePressable: focused = accent fill + inverse text
            onFocusChangeListener = OnFocusChangeListener { v, hasFocus ->
                (v as TextView).setTextColor(if (hasFocus) TEXT_INVERSE else TEXT_PRIMARY)
                v.background = GradientDrawable().apply {
                    setColor(if (hasFocus) ACCENT_COLOR else BUTTON_BG)
                    cornerRadius = dp(8f).toFloat()
                    setStroke(dp(if (hasFocus) 2f else 1f), if (hasFocus) ACCENT_COLOR else BUTTON_BORDER)
                }
            }
        }
    }

    private fun makeBadge(context: Context): TextView {
        return TextView(context).apply {
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 11f)
            typeface = Typeface.create("sans-serif-medium", Typeface.BOLD)
            letterSpacing = 0.04f
            setPadding(dp(8f), dp(3f), dp(8f), dp(3f))
            gravity = Gravity.CENTER
            visibility = View.GONE
        }
    }

    private fun wireUpFocusNavigation() {
        // Generate ID for seekBar so it can be referenced
        seekBar.id = View.generateViewId()

        // Exit → DOWN → playPause
        exitButton.nextFocusDownId = playPauseButton.id

        // Main row: playPause → skipBack → skipForward → seekBar (all buttons left of seekbar)
        playPauseButton.nextFocusRightId = skipBackButton.id
        skipBackButton.nextFocusRightId = skipForwardButton.id
        skipBackButton.nextFocusLeftId = playPauseButton.id
        skipForwardButton.nextFocusRightId = seekBar.id
        skipForwardButton.nextFocusLeftId = skipBackButton.id
        seekBar.nextFocusLeftId = skipForwardButton.id

        // Main row → DOWN → secondary row
        playPauseButton.nextFocusDownId = audioButton.id
        skipBackButton.nextFocusDownId = audioButton.id
        skipForwardButton.nextFocusDownId = subtitleButton.id
        seekBar.nextFocusDownId = infoButton.id

        // Secondary row → UP → main row
        audioButton.nextFocusUpId = playPauseButton.id
        subtitleButton.nextFocusUpId = skipForwardButton.id
        infoButton.nextFocusUpId = seekBar.id

        // Secondary row left/right
        audioButton.nextFocusRightId = subtitleButton.id
        subtitleButton.nextFocusLeftId = audioButton.id
        subtitleButton.nextFocusRightId = infoButton.id
        infoButton.nextFocusLeftId = subtitleButton.id

        // Main row → UP → exit
        playPauseButton.nextFocusUpId = exitButton.id
        skipBackButton.nextFocusUpId = exitButton.id
        seekBar.nextFocusUpId = exitButton.id
        skipForwardButton.nextFocusUpId = exitButton.id
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
        audioLabel.text = label
    }

    fun setSubtitleTrackLabel(label: String) {
        subtitleLabel.text = label
    }

    fun setMetadata(
        resolution: String,
        dvProfile: String,
        videoCodec: String,
        videoBitrate: Long,
        frameRate: String,
        audioCodec: String,
        audioChannels: String,
        audioBitrate: Long,
        isHDR: Boolean,
        isDV: Boolean,
        colorTransfer: String,
        colorPrimaries: String,
        colorSpace: String,
        seasonNumber: Int,
        episodeNumber: Int,
        seriesName: String,
        episodeName: String,
    ) {
        // Update title with episode info if available
        if (seasonNumber > 0 && episodeNumber > 0) {
            val epCode = "S${seasonNumber.toString().padStart(2, '0')}E${episodeNumber.toString().padStart(2, '0')}"
            val displayTitle = if (seriesName.isNotEmpty()) {
                "$seriesName - $epCode"
            } else {
                "${titleText.text} - $epCode"
            }
            titleText.text = displayTitle
        }

        // HDR/SDR badge (always shown, matching RN MediaInfoDisplay)
        var hasAnyBadge = false
        if (isDV) {
            val profileLabel = formatDvProfile(dvProfile)
            hdrBadge.text = profileLabel
            hdrBadge.setTextColor(BADGE_TEXT_DARK)
            hdrBadge.background = GradientDrawable().apply {
                setColor(HDR_BADGE_BG)
                cornerRadius = dp(4f).toFloat()
            }
            hdrBadge.visibility = View.VISIBLE
            hasAnyBadge = true
        } else if (isHDR) {
            hdrBadge.text = "HDR10"
            hdrBadge.setTextColor(BADGE_TEXT_DARK)
            hdrBadge.background = GradientDrawable().apply {
                setColor(HDR_BADGE_BG)
                cornerRadius = dp(4f).toFloat()
            }
            hdrBadge.visibility = View.VISIBLE
            hasAnyBadge = true
        } else {
            // SDR fallback badge
            hdrBadge.text = "SDR"
            hdrBadge.setTextColor(TEXT_PRIMARY)
            hdrBadge.background = GradientDrawable().apply {
                setColor(SDR_BADGE_BG)
                cornerRadius = dp(4f).toFloat()
            }
            hdrBadge.visibility = View.VISIBLE
            hasAnyBadge = true
        }

        // Resolution badge
        if (resolution.isNotEmpty()) {
            val resLabel = formatResolution(resolution)
            val resBg = getResolutionColor(resolution)
            resBadge.text = resLabel
            resBadge.setTextColor(TEXT_PRIMARY)
            resBadge.background = GradientDrawable().apply {
                setColor(resBg)
                cornerRadius = dp(4f).toFloat()
            }
            resBadge.visibility = View.VISIBLE
            hasAnyBadge = true
        }

        if (hasAnyBadge) {
            badgesRow.visibility = View.VISIBLE
        }
    }

    fun updatePlayPauseState(paused: Boolean) {
        playPauseButton.text = if (paused) "\u25B6" else "\u275A\u275A" // ▶ or ❚❚
    }

    private val hidePauseRunnable = Runnable { pauseIndicator.visibility = View.GONE }

    fun showPauseIndicator(paused: Boolean) {
        pauseIndicator.text = if (paused) "\u275A\u275A" else "\u25B6" // ❚❚ or ▶
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
        topGradient.visibility = View.VISIBLE
        topBar.visibility = View.VISIBLE
        bottomContainer.visibility = View.VISIBLE
        playPauseButton.requestFocus()
        resetAutoHide()
    }

    fun hide() {
        controlsVisible = false
        topGradient.visibility = View.GONE
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

    private fun formatDvProfile(profile: String): String {
        if (profile.isEmpty()) return "Dolby Vision"
        // Handle formats like "dvhe.05", "5", "dv-hevc" — matches RN MediaInfoDisplay format
        val numMatch = Regex("\\d+").find(profile)
        return if (numMatch != null) {
            "Dolby Vision Profile ${numMatch.value.trimStart('0').ifEmpty { "0" }}"
        } else {
            "Dolby Vision"
        }
    }

    private fun formatResolution(resolution: String): String {
        // Categorize by height bucket, matching RN MediaInfoDisplay
        val parts = resolution.split("x")
        if (parts.size == 2) {
            val height = parts[1].toIntOrNull() ?: return resolution
            return when {
                height > 1080 -> "2160p"
                height > 720 -> "1080p"
                height == 720 -> "720p"
                else -> "480p"
            }
        }
        return resolution
    }

    private fun getResolutionColor(resolution: String): Int {
        val parts = resolution.split("x")
        if (parts.size == 2) {
            val height = parts[1].toIntOrNull() ?: return RES_480_BG
            return when {
                height >= 2160 -> RES_4K_BG
                height >= 1080 -> RES_1080_BG
                height >= 720 -> RES_720_BG
                else -> RES_480_BG
            }
        }
        return RES_480_BG
    }
}
