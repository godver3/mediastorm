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

data class TrackInfo(
    val mpvId: Int,
    val title: String,
    val language: String,
    val codec: String,
    val selected: Boolean
)

object TrackPickerDialog {

    // Colors matching RN TrackSelectionModal.tsx
    private const val SCRIM_COLOR = 0xD9000000.toInt()         // rgba(0,0,0,0.85)
    private const val MODAL_BG = 0xFF1F1F2A.toInt()            // background.elevated
    private const val BORDER_COLOR = 0xFF2B2F3C.toInt()        // border.subtle
    private const val ACCENT_COLOR = 0xFF3F66FF.toInt()        // accent.primary
    private const val TEXT_PRIMARY = 0xFFFFFFFF.toInt()
    private const val TEXT_SECONDARY = 0xFFC7CAD6.toInt()
    private const val ITEM_BG = 0x14FFFFFF.toInt()             // rgba(255,255,255,0.08) — overlay.medium
    private const val ITEM_SELECTED_BG = 0x1FFFFFFF.toInt()    // rgba(255,255,255,0.12) — overlay.button
    private const val ITEM_FOCUSED_BG = 0xFF3F66FF.toInt()     // accent fill
    private const val SURFACE_BG = 0xFF16161F.toInt()          // background.surface
    private const val SELECTED_BADGE_BG = 0x4D000000.toInt()   // rgba(0,0,0,0.3) — unfocused badge
    private const val SELECTED_BADGE_FOCUSED_BG = 0x33000000.toInt() // rgba(0,0,0,0.2) — focused badge
    private const val SELECTED_BADGE_TAG = "selected_badge"

    private fun dp(view: View, value: Float): Int =
        TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, value, view.resources.displayMetrics).toInt()

    private fun dp(activity: Activity, value: Float): Int =
        TypedValue.applyDimension(TypedValue.COMPLEX_UNIT_DIP, value, activity.resources.displayMetrics).toInt()

    fun show(
        activity: Activity,
        title: String,
        subtitle: String,
        tracks: List<TrackInfo>,
        allowOff: Boolean,
        onSelect: (Int?) -> Unit,
        onDismiss: (() -> Unit)? = null
    ) {
        val decorView = activity.window.decorView as ViewGroup
        var overlay: FrameLayout? = null

        fun dismiss() {
            overlay?.let { decorView.removeView(it) }
            overlay = null
            onDismiss?.invoke()
        }

        // Root scrim overlay
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

        // Modal container
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
            setPadding(dp(this, 24f), dp(this, 24f), dp(this, 24f), dp(this, 16f))
        }
        val headerTitle = TextView(activity).apply {
            text = title
            setTextColor(TEXT_PRIMARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 24f)
            typeface = Typeface.create("sans-serif", Typeface.BOLD)
        }
        headerContainer.addView(headerTitle, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ))

        val headerSubtitle = TextView(activity).apply {
            text = "Current: $subtitle"
            setTextColor(TEXT_SECONDARY)
            setTextSize(TypedValue.COMPLEX_UNIT_SP, 14f)
        }
        headerContainer.addView(headerSubtitle, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
        ).apply { topMargin = dp(activity, 4f) })

        modal.addView(headerContainer)

        // Divider
        modal.addView(View(activity).apply {
            setBackgroundColor(BORDER_COLOR)
        }, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, dp(activity, 1f)
        ))

        // Track list in ScrollView
        val scrollView = ScrollView(activity).apply {
            isFocusable = false
        }
        val listContainer = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(this, 8f), dp(this, 12f), dp(this, 8f), dp(this, 8f))
        }

        // Build items list
        data class TrackItem(val label: String, val description: String, val mpvId: Int?, val isSelected: Boolean)
        val items = mutableListOf<TrackItem>()

        if (allowOff) {
            val isOffSelected = tracks.none { it.selected }
            items.add(TrackItem("Off", "", null, isOffSelected))
        }

        for (track in tracks) {
            val label = buildString {
                if (track.title.isNotEmpty()) {
                    append(track.title)
                }
                if (track.language.isNotEmpty()) {
                    if (isNotEmpty()) append(" (${track.language})")
                    else append(track.language)
                }
                if (isEmpty()) append("Track ${track.mpvId}")
            }
            val desc = if (track.codec.isNotEmpty()) track.codec.uppercase() else ""
            items.add(TrackItem(label, desc, track.mpvId, track.selected))
        }

        var firstSelectedView: View? = null

        for (item in items) {
            val row = LinearLayout(activity).apply {
                orientation = LinearLayout.HORIZONTAL
                gravity = Gravity.CENTER_VERTICAL
                val rowDp = dp(this, 12f)
                setPadding(dp(this, 16f), rowDp, dp(this, 16f), rowDp)
                isFocusable = true
                isFocusableInTouchMode = true

                val normalBg = if (item.isSelected) ITEM_SELECTED_BG else ITEM_BG
                background = GradientDrawable().apply {
                    setColor(normalBg)
                    cornerRadius = dp(activity, 8f).toFloat()
                    if (item.isSelected) {
                        setStroke(dp(activity, 1f), ACCENT_COLOR)
                    } else {
                        setStroke(dp(activity, 1f), BORDER_COLOR)
                    }
                }

                val clickAction = {
                    onSelect(item.mpvId)
                    dismiss()
                }

                setOnClickListener { clickAction() }
                setOnKeyListener { _, keyCode, event ->
                    if (event.action == KeyEvent.ACTION_DOWN &&
                        (keyCode == KeyEvent.KEYCODE_DPAD_CENTER || keyCode == KeyEvent.KEYCODE_ENTER)) {
                        clickAction()
                        true
                    } else false
                }

                onFocusChangeListener = View.OnFocusChangeListener { v, hasFocus ->
                    val bg = if (hasFocus) ITEM_FOCUSED_BG else normalBg
                    val borderColor = when {
                        hasFocus && item.isSelected -> TEXT_PRIMARY  // selected+focused = white border
                        hasFocus -> ACCENT_COLOR                     // focused = accent border
                        item.isSelected -> ACCENT_COLOR              // selected = accent border
                        else -> BORDER_COLOR                         // default = subtle border
                    }
                    v.background = GradientDrawable().apply {
                        setColor(bg.toInt())
                        cornerRadius = dp(v, 8f).toFloat()
                        setStroke(dp(v, if (hasFocus || item.isSelected) 2f else 1f), borderColor)
                    }
                    // Update text colors and badge bg
                    fun updateTextColors(vg: ViewGroup) {
                        for (i in 0 until vg.childCount) {
                            val child = vg.getChildAt(i)
                            if (child is ViewGroup) updateTextColors(child)
                            if (child is TextView) {
                                child.setTextColor(if (hasFocus) TEXT_PRIMARY else child.tag as? Int ?: TEXT_PRIMARY)
                                // Update selected badge bg when focused
                                if (child.tag == SELECTED_BADGE_TAG) {
                                    child.background = GradientDrawable().apply {
                                        setColor(if (hasFocus) SELECTED_BADGE_FOCUSED_BG else SELECTED_BADGE_BG)
                                        cornerRadius = dp(child, 4f).toFloat()
                                    }
                                }
                            }
                        }
                    }
                    updateTextColors(v as LinearLayout)
                }
            }

            // Label column
            val labelColumn = LinearLayout(activity).apply {
                orientation = LinearLayout.VERTICAL
            }
            val labelText = TextView(activity).apply {
                text = item.label
                setTextColor(TEXT_PRIMARY)
                tag = TEXT_PRIMARY
                setTextSize(TypedValue.COMPLEX_UNIT_SP, 17f)
                typeface = Typeface.create("sans-serif", Typeface.BOLD)
            }
            labelColumn.addView(labelText)
            if (item.description.isNotEmpty()) {
                val descText = TextView(activity).apply {
                    text = item.description
                    setTextColor(TEXT_SECONDARY)
                    tag = TEXT_SECONDARY
                    setTextSize(TypedValue.COMPLEX_UNIT_SP, 12f)
                }
                labelColumn.addView(descText, LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
                ).apply { topMargin = dp(activity, 2f) })
            }
            row.addView(labelColumn, LinearLayout.LayoutParams(0, LinearLayout.LayoutParams.WRAP_CONTENT, 1f))

            // Selected badge
            if (item.isSelected) {
                val badge = TextView(activity).apply {
                    text = "SELECTED"
                    setTextColor(TEXT_PRIMARY)
                    tag = SELECTED_BADGE_TAG
                    setTextSize(TypedValue.COMPLEX_UNIT_SP, 11f)
                    typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
                    letterSpacing = 0.04f
                    background = GradientDrawable().apply {
                        setColor(SELECTED_BADGE_BG)
                        cornerRadius = dp(activity, 4f).toFloat()
                    }
                    setPadding(dp(this, 8f), dp(this, 4f), dp(this, 8f), dp(this, 4f))
                }
                row.addView(badge, LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.WRAP_CONTENT, LinearLayout.LayoutParams.WRAP_CONTENT
                ).apply { marginStart = dp(activity, 12f) })
            }

            listContainer.addView(row, LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, LinearLayout.LayoutParams.WRAP_CONTENT
            ).apply { bottomMargin = dp(activity, 6f) })

            if (item.isSelected) {
                firstSelectedView = row
            }
        }

        scrollView.addView(listContainer, FrameLayout.LayoutParams(
            FrameLayout.LayoutParams.MATCH_PARENT, FrameLayout.LayoutParams.WRAP_CONTENT
        ))

        modal.addView(scrollView, LinearLayout.LayoutParams(
            LinearLayout.LayoutParams.MATCH_PARENT, 0, 1f
        ))

        // Footer divider + close button
        val footer = LinearLayout(activity).apply {
            orientation = LinearLayout.VERTICAL
            gravity = Gravity.CENTER_HORIZONTAL
            setPadding(dp(this, 24f), dp(this, 12f), dp(this, 24f), dp(this, 16f))
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
            minimumWidth = dp(this, 120f)
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

        // Add modal to overlay
        overlay!!.addView(modal, FrameLayout.LayoutParams(
            dp(modal, 700f).coerceAtMost(activity.resources.displayMetrics.widthPixels - dp(modal, 48f)),
            FrameLayout.LayoutParams.WRAP_CONTENT,
            Gravity.CENTER
        ).apply {
            val maxH = (activity.resources.displayMetrics.heightPixels * 0.8).toInt()
            height = FrameLayout.LayoutParams.WRAP_CONTENT
            // Limit modal height
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

        // Focus the selected item (or first item)
        val focusTarget = firstSelectedView ?: listContainer.getChildAt(0)
        focusTarget?.post { focusTarget.requestFocus() }
    }
}
