package handlers

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed static/*
var staticAssets embed.FS

// StaticHandler serves embedded static assets
type StaticHandler struct {
	fileServer http.Handler
}

// NewStaticHandler creates a new static assets handler
func NewStaticHandler() *StaticHandler {
	// Get the static subdirectory from the embedded FS
	staticFS, err := fs.Sub(staticAssets, "static")
	if err != nil {
		panic("failed to get static subdirectory: " + err.Error())
	}

	return &StaticHandler{
		fileServer: http.FileServer(http.FS(staticFS)),
	}
}

// ServeHTTP serves static files
func (h *StaticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Set cache headers for static assets (1 year)
	w.Header().Set("Cache-Control", "public, max-age=31536000")

	// Set appropriate content type for images
	path := r.URL.Path
	if strings.HasSuffix(path, ".png") {
		w.Header().Set("Content-Type", "image/png")
	} else if strings.HasSuffix(path, ".svg") {
		w.Header().Set("Content-Type", "image/svg+xml")
	} else if strings.HasSuffix(path, ".jpg") || strings.HasSuffix(path, ".jpeg") {
		w.Header().Set("Content-Type", "image/jpeg")
	}

	h.fileServer.ServeHTTP(w, r)
}
