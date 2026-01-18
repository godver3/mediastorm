# TODO: Issues to Investigate

## 1. Dashboard Session Tracking - userId="default"

**Problem:** Active streams are not showing in the dashboard because `profileID="default"` is being passed instead of the actual user profile ID.

**Evidence from logs:**
```
[admin-ui] Stream check: id=c33392181e696fd609ef157e3f263ed8 profileID="default" profileName="" bytes=11608728213
[admin-ui] Filtered: default profile with no valid name
```

**Root cause:** The frontend is sending `userId=default` in prequeue requests:
```
[prequeue] Received request: titleId=tvdb:movie:370 titleName="Ponyo" userId=default clientId=... mediaType=movie
```

**Investigation needed:**
- Check why `activeUserId` in the frontend resolves to the literal string "default"
- Check if there's a user profile with ID "default" being created
- Check `UserProfilesContext.tsx` for any "default" fallback logic
- The HLS session created via prequeue inherits this "default" profileID

**Filtering logic in admin_ui.go (lines 1041-1047):**
```go
isDefaultProfile := strings.ToLower(session.ProfileID) == "default" || session.ProfileID == ""
hasValidProfileName := session.ProfileName != "" && strings.ToLower(session.ProfileName) != "default"
if isDefaultProfile && !hasValidProfileName {
    continue  // Stream filtered out from dashboard
}
```

---

## 2. Defer Rewind to Allow Multiple Button Presses

**Problem:** Session recreation happens immediately on rewind, need to defer/debounce to allow for multiple button presses before triggering session recreation.

**Investigation needed:**
- Look at rewind/seek handling in player
- Add debounce/delay before recreating HLS session on rewind
- Allow user to press rewind multiple times (e.g., 10s + 10s + 10s) before the session is actually recreated

---

## 3. Rewind Sessions Fail After a Bit (Cleanup Issue)

**Problem:** After rewinding, the new session seems to fail after some time - likely being cleaned up prematurely.

**Possible causes:**
- Old session cleanup interfering with new session
- Session ID collision or stale references
- Cleanup goroutine targeting wrong session
- Race condition between session creation and cleanup

**Investigation needed:**
- Check HLS session cleanup logic in `hls.go`
- Look at `cleanupOldSessions()` and idle timeout handling
- Verify session replacement logic when rewinding
- Check if old session's cancel context affects new session
