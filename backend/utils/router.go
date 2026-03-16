package utils

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/gorilla/mux"
)

// CORS middleware to allow cross-origin requests from local/private origins
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && IsAllowedOrigin(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, PATCH, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-PIN, X-Client-ID")
		}

		// Handle preflight requests
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// BasePathHandler wraps a handler to strip a URL path prefix.
// Requests with the prefix get it stripped before routing; requests without
// the prefix are passed through unchanged so direct access still works.
func BasePathHandler(basePath string, next http.Handler) http.Handler {
	basePath = "/" + strings.Trim(basePath, "/")
	if basePath == "/" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, basePath+"/") || r.URL.Path == basePath {
			r2 := new(http.Request)
			*r2 = *r
			r2.URL = new(url.URL)
			*r2.URL = *r.URL
			r2.URL.Path = strings.TrimPrefix(r.URL.Path, basePath)
			if r2.URL.Path == "" {
				r2.URL.Path = "/"
			}
			if r2.URL.RawPath != "" {
				r2.URL.RawPath = strings.TrimPrefix(r2.URL.RawPath, basePath)
				if r2.URL.RawPath == "" {
					r2.URL.RawPath = "/"
				}
			}
			next.ServeHTTP(w, r2)
			return
		}
		// No prefix match — serve normally (allows direct access)
		next.ServeHTTP(w, r)
	})
}

// NewRouter constructs the base mux router with common routes.
func NewRouter() *mux.Router {
	r := mux.NewRouter()

	// Add CORS middleware
	r.Use(corsMiddleware)

	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}).Methods(http.MethodGet)
	return r
}
