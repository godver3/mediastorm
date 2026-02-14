package com.strmr.mpvplayer

import android.app.AlertDialog
import android.content.Context

data class TrackInfo(
    val mpvId: Int,
    val title: String,
    val language: String,
    val codec: String,
    val selected: Boolean
)

object TrackPickerDialog {

    fun show(
        context: Context,
        dialogTitle: String,
        tracks: List<TrackInfo>,
        allowOff: Boolean,
        onSelect: (Int?) -> Unit // null = off, otherwise mpv track ID
    ) {
        val labels = mutableListOf<String>()
        val ids = mutableListOf<Int?>()
        var checkedIndex = 0

        if (allowOff) {
            labels.add("Off")
            ids.add(null)
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
                if (track.codec.isNotEmpty()) append(" [${track.codec}]")
            }
            labels.add(label)
            ids.add(track.mpvId)
            if (track.selected) {
                checkedIndex = labels.size - 1
            }
        }

        AlertDialog.Builder(context)
            .setTitle(dialogTitle)
            .setSingleChoiceItems(labels.toTypedArray(), checkedIndex) { dialog, which ->
                onSelect(ids[which])
                dialog.dismiss()
            }
            .setNegativeButton("Cancel", null)
            .show()
    }
}
