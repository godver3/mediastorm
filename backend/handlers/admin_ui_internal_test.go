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
	if !strings.Contains(message, "custom folder") || !strings.Contains(message, "Category will be ignored") {
		t.Fatalf("message = %q", message)
	}
}
