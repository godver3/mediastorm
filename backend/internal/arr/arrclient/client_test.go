package arrclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"novastream/internal/arr"
)

func TestNewValidatesInput(t *testing.T) {
	for _, tc := range []struct{ url, key string }{
		{"ftp://host", "k"}, {"http://", "k"}, {"not a url", "k"}, {"http://host:8989", " "},
	} {
		if _, err := New(tc.url, tc.key, nil); err == nil {
			t.Errorf("New(%q, %q) should fail", tc.url, tc.key)
		}
	}
	c, err := New(" http://host:8989/sonarr/ ", " key ", nil)
	if err != nil || c.BaseURL != "http://host:8989/sonarr" || c.APIKey != "key" {
		t.Fatalf("New: %+v %v", c, err)
	}
}

func TestClientAgainstFakeInstance(t *testing.T) {
	var gotBody map[string]any
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"message":"Unauthorized"}`)
			return
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch {
		case r.URL.Path == "/base/api/v3/system/status":
			io.WriteString(w, `{"appName":"Sonarr","version":"4.0.0"}`)
		case r.URL.Path == "/base/api/v3/customformat" && r.Method == http.MethodGet:
			io.WriteString(w, `[{"id":7,"name":"German","includeCustomFormatWhenRenaming":false,"specifications":[{"name":"de","implementation":"LanguageSpecification","negate":false,"required":true,"fields":[{"name":"value","value":4}]}]}]`)
		case r.URL.Path == "/base/api/v3/customformat" && r.Method == http.MethodPost:
			json.NewDecoder(r.Body).Decode(&gotBody)
			io.WriteString(w, `{"id":42}`)
		case strings.HasPrefix(r.URL.Path, "/base/api/v3/customformat/"):
			if r.Method == http.MethodPut {
				json.NewDecoder(r.Body).Decode(&gotBody)
			}
		case r.URL.Path == "/base/api/v3/qualityprofile":
			io.WriteString(w, `[{"id":3,"name":"HD","items":[{"quality":{"id":3,"name":"WEBDL-1080p"},"items":[],"allowed":true}],"formatItems":[{"format":7,"name":"German","score":1000}]}]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, strings.Repeat("x", 500))
		}
	}))
	defer srv.Close()
	ctx := context.Background()

	c, _ := New(srv.URL+"/base", "secret", srv.Client())
	st, err := c.SystemStatus(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if app, err := st.App(); err != nil || app != arr.AppSonarr {
		t.Errorf("app %v %v", app, err)
	}

	cfs, err := c.ListCustomFormats(ctx)
	if err != nil || len(cfs) != 1 || cfs[0].ID != 7 || cfs[0].Format.Name != "German" {
		t.Fatalf("custom formats %+v %v", cfs, err)
	}
	if v := cfs[0].Format.Specifications[0].Fields["value"]; v == nil {
		t.Error("array-form fields must decode")
	}

	id, err := c.CreateCustomFormat(ctx, cfs[0].Format)
	if err != nil || id != 42 {
		t.Fatalf("create: %d %v", id, err)
	}
	specs, _ := gotBody["specifications"].([]any)
	if len(specs) != 1 {
		t.Fatalf("create body %v", gotBody)
	}
	if fields, _ := specs[0].(map[string]any)["fields"].([]any); len(fields) != 1 {
		t.Errorf("fields must be sent in API (array) form: %v", specs[0])
	}
	if err := c.UpdateCustomFormat(ctx, 42, cfs[0].Format); err != nil || gotBody["id"] != float64(42) {
		t.Errorf("update: %v %v", err, gotBody["id"])
	}
	if err := c.DeleteCustomFormat(ctx, 42); err != nil {
		t.Errorf("delete: %v", err)
	}

	profiles, err := c.ListQualityProfiles(ctx)
	if err != nil || len(profiles) != 1 || profiles[0].FormatItems[0].Score != 1000 || profiles[0].Items[0].Quality.Name != "WEBDL-1080p" {
		t.Fatalf("profiles %+v %v", profiles, err)
	}

	_, err = c.ListQualityDefinitions(ctx)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound || len(apiErr.Error()) > 400 {
		t.Errorf("a 404 must surface as a truncated APIError, got %v", err)
	}

	bad, _ := New(srv.URL+"/base", "wrong", srv.Client())
	if _, err := bad.SystemStatus(ctx); !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Errorf("wrong key: %v", err)
	}
	if want := "DELETE /base/api/v3/customformat/42"; !strings.Contains(strings.Join(calls, ","), want) {
		t.Errorf("calls %v missing %s", calls, want)
	}
}

func TestResponseSizeIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("["))
		chunk := []byte(strings.Repeat(" ", 1<<20))
		for i := 0; i <= maxResponseBytes>>20; i++ {
			w.Write(chunk)
		}
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "k", srv.Client())
	if _, err := c.ListQualityProfiles(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("oversized response must be rejected, got %v", err)
	}
}

// The API key must not follow a redirect to another host.
func TestRedirectToAnotherHostIsRefused(t *testing.T) {
	leaked := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked = r.Header.Get("X-Api-Key") != ""
		io.WriteString(w, `{"version":"4.0"}`)
	}))
	defer other.Close()
	instance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer instance.Close()
	c, err := New(instance.URL, "secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SystemStatus(context.Background()); err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Errorf("expected the redirect to be refused, got %v", err)
	}
	if leaked {
		t.Error("the API key reached another host")
	}
}
