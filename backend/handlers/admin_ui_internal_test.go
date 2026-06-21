package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/config"
)

func TestUsenetEngineStatusProbeJobIDUsesGUIDForNZBDav(t *testing.T) {
	for _, engineType := range []string{"nzbdav", "nzbdavex"} {
		t.Run(engineType, func(t *testing.T) {
			got := usenetEngineStatusProbeJobID(config.UsenetEngineSettings{Type: engineType})
			if got != "00000000-0000-4000-8000-000000000000" {
				t.Fatalf("probe job id = %q, want GUID-shaped placeholder", got)
			}
		})
	}

	got := usenetEngineStatusProbeJobID(config.UsenetEngineSettings{Type: "altmount"})
	if !strings.HasPrefix(got, "strmr-connection-test-") {
		t.Fatalf("altmount probe job id = %q, want legacy prefix", got)
	}
}

func TestExplainUsenetEngineRemoteConfigMismatchDetectsDecypharrCustomFolder(t *testing.T) {
	webdav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/webdav/mediastorm/</d:href>
    <d:propstat>
      <d:prop>
        <d:resourcetype><d:collection/></d:resourcetype>
        <d:displayname>mediastorm</d:displayname>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`))
	}))
	defer webdav.Close()

	message, err := explainUsenetEngineRemoteConfigMismatch(context.Background(), config.UsenetEngineSettings{
		Type:          "decypharr",
		WebDAVBaseURL: webdav.URL,
		Category:      "mediastorm",
	})
	if err != nil {
		t.Fatalf("explainUsenetEngineRemoteConfigMismatch: %v", err)
	}
	if !strings.Contains(message, "custom folder") || !strings.Contains(message, "Category will still be sent") {
		t.Fatalf("message = %q", message)
	}
}

func TestInferAdminWebDAVPathPrefixFromRootFolder(t *testing.T) {
	webdav := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("method = %s, want PROPFIND", r.Method)
		}
		w.Header().Set("Content-Type", "application/xml")
		switch r.URL.Path {
		case "/webdav/":
			w.WriteHeader(http.StatusMultiStatus)
			w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<d:multistatus xmlns:d="DAV:">
  <d:response>
    <d:href>/webdav/</d:href>
    <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
  <d:response>
    <d:href>/webdav/mediastorm/</d:href>
    <d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype><d:displayname>mediastorm</d:displayname></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat>
  </d:response>
</d:multistatus>`))
		case "/webdav/mediastorm/strmr-connection-test-1":
			w.WriteHeader(http.StatusMultiStatus)
		default:
			http.NotFound(w, r)
		}
	}))
	defer webdav.Close()

	prefix, mappedURL, ok := inferAdminWebDAVPathPrefix(context.Background(), config.UsenetEngineSettings{
		Type:          "decypharr",
		WebDAVBaseURL: webdav.URL + "/webdav",
	}, "/mnt/debrid/decypharr_downloads/mediastorm/strmr-connection-test-1")
	if !ok {
		t.Fatal("expected prefix inference to succeed")
	}
	if prefix != "/mnt/debrid/decypharr_downloads" {
		t.Fatalf("prefix = %q, want /mnt/debrid/decypharr_downloads", prefix)
	}
	wantURL := webdav.URL + "/webdav/mediastorm/strmr-connection-test-1"
	if mappedURL != wantURL {
		t.Fatalf("mappedURL = %q, want %q", mappedURL, wantURL)
	}
}
