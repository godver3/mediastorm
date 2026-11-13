package handlers

import (
	"context"
	"net/http"

	"novastream/internal/auth"
)

func (h *AdminUIHandler) requireParentAuthorization(w http.ResponseWriter, r *http.Request) bool {
	if session := adminSessionFromContext(r.Context()); session != nil {
		r = r.WithContext(context.WithValue(r.Context(), auth.ContextKeySession, *session))
	}
	return h.parentAuthorization.Require(w, r)
}
