package slogutil

import (
	"bytes"
	"strings"
	"testing"
)

func TestRedactingWriterRemovesCredentialForms(t *testing.T) {
	var output bytes.Buffer
	writer := NewRedactingWriter(&output)
	input := `GET /download?apikey=secret&x=1 %3Ftoken%3Dencoded%26x%3D1 {"token":"json-secret","xtreamUsername":"iptv-viewer","homeWifiSSID":"Private Network"} {"path":"network.homeWifiSSID","before":"Old Network","after":"New Network"} Authorization: Bearer bearer-secret`
	if _, err := writer.Write([]byte(input)); err != nil {
		t.Fatal(err)
	}
	got := output.String()
	for _, secret := range []string{"secret", "encoded", "json-secret", "bearer-secret", "iptv-viewer", "Private Network", "Old Network", "New Network"} {
		if strings.Contains(got, secret) {
			t.Fatalf("redacted log still contains %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "[redacted]") {
		t.Fatalf("redacted marker missing: %s", got)
	}
}

func TestRedactingWriterRemovesParentPasswords(t *testing.T) {
	for _, input := range []string{`X-Account-Password: fixture secret with spaces`, `{"X-Account-Password":"fixture secret with spaces"}`, `{"X-Account-Password":["fixture secret with spaces"]}`, `{"X-Account-Password":"fixture \"quoted\" secret with spaces"}`} {
		var output bytes.Buffer
		_, _ = NewRedactingWriter(&output).Write([]byte(input))
		if strings.Contains(output.String(), "fixture") || strings.Contains(output.String(), "spaces") {
			t.Fatal("parent password remained in log")
		}
	}
}
