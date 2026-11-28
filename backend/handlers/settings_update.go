package handlers

import (
	"errors"
	"net/http"

	"novastream/config"
)

// errSettingsUpdateAborted is returned from a config.Manager.Update callback
// once the callback has written an error response itself (validation failed,
// not found, forbidden): nothing is saved and the handler just returns.
var errSettingsUpdateAborted = errors.New("settings update aborted")

// updateSettings applies fn to the stored settings under mgr.Update, so the
// load, change and save cannot interleave with another writer. It reports
// whether the change was saved. A load failure is answered with a 500
// "Failed to load settings: …" and a save failure with saveMsg + the error;
// when fn returns errSettingsUpdateAborted it has already responded.
//
// fn must not call Load, Save or Update on mgr (the save lock is held).
func updateSettings(w http.ResponseWriter, mgr *config.Manager, saveMsg string, fn func(*config.Settings) error) bool {
	return updateSettingsOr(mgr, fn,
		func(err error) {
			jsonError(w, "Failed to load settings: "+err.Error(), http.StatusInternalServerError)
		},
		func(err error) {
			jsonError(w, saveMsg+err.Error(), http.StatusInternalServerError)
		})
}

// updateSettingsPlain is updateSettings for handlers answering with plain
// http.Error text: loadMsg on a load failure, saveMsg on a save failure.
func updateSettingsPlain(w http.ResponseWriter, mgr *config.Manager, loadMsg, saveMsg string, fn func(*config.Settings) error) bool {
	return updateSettingsOr(mgr, fn,
		func(error) { http.Error(w, loadMsg, http.StatusInternalServerError) },
		func(error) { http.Error(w, saveMsg, http.StatusInternalServerError) })
}

// updateSettingsOr runs fn under mgr.Update and calls onLoadErr or onSaveErr
// for a failure before or after fn; errSettingsUpdateAborted from fn means fn
// already responded. It reports whether the change was saved.
func updateSettingsOr(mgr *config.Manager, fn func(*config.Settings) error, onLoadErr, onSaveErr func(error)) bool {
	loaded := false
	err := mgr.Update(func(s *config.Settings) error {
		loaded = true
		return fn(s)
	})
	switch {
	case err == nil:
		return true
	case errors.Is(err, errSettingsUpdateAborted):
	case !loaded:
		onLoadErr(err)
	default:
		onSaveErr(err)
	}
	return false
}
