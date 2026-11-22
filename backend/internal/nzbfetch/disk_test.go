package nzbfetch

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"novastream/models"
)

func TestReleaseURLStripsCredentials(t *testing.T) {
	cases := map[string]string{
		"https://ninja.example/getnzb/abc.nzb&i=94750&r=SECRET":               "https://ninja.example/getnzb/abc.nzb",
		"https://api.nzbgeek.info/api?t=get&id=xyz&apikey=SECRET":             "https://api.nzbgeek.info/api?id=xyz&t=get",
		"https://INDEXER.example/api?apikey=OTHER&t=get&id=xyz":               "https://indexer.example/api?id=xyz&t=get",
		"https://indexer.example/getnzb/abc.nzb?i=1&r=SECRET&extra=keep#frag": "https://indexer.example/getnzb/abc.nzb?extra=keep",
	}
	for in, want := range cases {
		if got := ReleaseURL(in); got != want {
			t.Errorf("ReleaseURL(%q) = %q, want %q", in, got, want)
		}
	}
	if ReleaseURLHash("https://ninja.example/getnzb/abc.nzb&i=1&r=OLD") != ReleaseURLHash("https://ninja.example/getnzb/abc.nzb&i=1&r=NEW") {
		t.Fatal("rotating the API key must keep the release identity")
	}
}

func TestRedactURLDropsPathCredentials(t *testing.T) {
	got := redactURL("https://ninja.example/getnzb/abc.nzb&i=94750&r=SECRET")
	if strings.Contains(got, "SECRET") || strings.Contains(got, "94750") {
		t.Fatalf("redactURL leaked credentials: %q", got)
	}
}

func TestDiskCacheServesRepeatGrabsAcrossFetchers(t *testing.T) {
	var grabs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		grabs.Add(1)
		w.Header().Set("Content-Disposition", `attachment; filename="Release.nzb"`)
		// Indexers watermark each copy, so bytes differ per grab.
		_, _ = w.Write([]byte("<nzb>" + r.URL.Path + string(rune('a'+grabs.Load())) + "</nzb>"))
	}))
	defer srv.Close()

	dir := t.TempDir()
	candidate := models.NZBResult{Title: "Release", Indexer: "Ninja"}
	first := New(nil)
	first.SetDiskCache(dir)
	data1, header, err := first.Fetch(context.Background(), srv.Client(), nil, srv.URL+"/getnzb/abc.nzb&r=OLDKEY", candidate, "")
	if err != nil {
		t.Fatal(err)
	}
	if header.Get("Content-Disposition") == "" {
		t.Fatal("header not returned")
	}

	// A new process (restart) with a rotated API key reuses the stored NZB.
	second := New(nil)
	second.SetDiskCache(dir)
	data2, header2, err := second.Fetch(context.Background(), srv.Client(), nil, srv.URL+"/getnzb/abc.nzb&r=NEWKEY", candidate, "")
	if err != nil {
		t.Fatal(err)
	}
	if grabs.Load() != 1 {
		t.Fatalf("indexer grabs = %d, want 1", grabs.Load())
	}
	if string(data1) != string(data2) {
		t.Fatal("disk cache returned different bytes")
	}
	if header2.Get("Content-Disposition") != header.Get("Content-Disposition") {
		t.Fatal("Content-Disposition not persisted")
	}

	// A different release still grabs.
	if _, _, err := second.Fetch(context.Background(), srv.Client(), nil, srv.URL+"/getnzb/other.nzb&r=NEWKEY", candidate, ""); err != nil {
		t.Fatal(err)
	}
	if grabs.Load() != 2 {
		t.Fatalf("indexer grabs = %d, want 2", grabs.Load())
	}
}

func TestFailedGrabIsNotCached(t *testing.T) {
	var grabs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if grabs.Add(1) == 1 {
			http.Error(w, "grab limit reached", http.StatusForbidden)
			return
		}
		_, _ = w.Write([]byte("<nzb/>"))
	}))
	defer srv.Close()
	f := New(nil)
	f.SetDiskCache(t.TempDir())
	candidate := models.NZBResult{Indexer: "x"}
	if _, _, err := f.Fetch(context.Background(), srv.Client(), nil, srv.URL+"/a.nzb", candidate, ""); err == nil {
		t.Fatal("expected error")
	}
	if _, _, err := f.Fetch(context.Background(), srv.Client(), nil, srv.URL+"/a.nzb", candidate, ""); err != nil {
		t.Fatal(err)
	}
	if grabs.Load() != 2 {
		t.Fatalf("grabs = %d, want 2", grabs.Load())
	}
}
