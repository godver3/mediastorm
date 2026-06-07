package mdblist

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestListsClient_GetExternalLists(t *testing.T) {
	client := NewListsClient("api-key")
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/external/lists/user" {
				t.Fatalf("path = %q", r.URL.Path)
			}
			if r.URL.Query().Get("apikey") != "api-key" {
				t.Fatalf("missing apikey query")
			}
			body := `[{"id":26639,"name":"My Letterboxd List","source":"letterboxd","items":42,"url":"/x"}]`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})

	lists, err := client.GetExternalLists(context.Background())
	if err != nil {
		t.Fatalf("GetExternalLists() error = %v", err)
	}
	if len(lists) != 1 || lists[0].ID != 26639 || lists[0].Source != "letterboxd" || lists[0].Items != 42 {
		t.Fatalf("unexpected lists: %+v", lists)
	}
}

func TestListsClient_GetExternalListItems(t *testing.T) {
	client := NewListsClient("api-key")
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/external/lists/26639/items" {
				t.Fatalf("path = %q", r.URL.Path)
			}
			body := `{"movies":[
				{"title":"Parasite","release_year":2019,"mediatype":"movie","imdb_id":"tt6751668","tvdb_id":0,"ids":{"imdb":"tt6751668","tmdb":496243}}
			],"shows":[
				{"title":"Severance","release_year":2022,"mediatype":"show","imdb_id":"tt11280740","tvdb_id":371980,"ids":{"imdb":"tt11280740","tmdb":95396,"tvdb":371980}}
			],"pagination":{}}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})

	items, err := client.GetExternalListItems(context.Background(), "26639")
	if err != nil {
		t.Fatalf("GetExternalListItems() error = %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if items[0].Title != "Parasite" || items[0].MediaType != "movie" || items[0].TMDBID != 496243 || items[0].IMDBID != "tt6751668" {
		t.Fatalf("unexpected movie item: %+v", items[0])
	}
	if items[1].MediaType != "show" || items[1].TVDBID != 371980 || items[1].TMDBID != 95396 {
		t.Fatalf("unexpected show item: %+v", items[1])
	}
}

func TestListsClient_RequiresAPIKey(t *testing.T) {
	client := NewListsClient("")
	if client.IsConfigured() {
		t.Fatal("expected IsConfigured to be false without a key")
	}
	if _, err := client.GetExternalLists(context.Background()); err == nil {
		t.Fatal("expected error without api key")
	}
	if _, err := client.GetExternalListItems(context.Background(), "1"); err == nil {
		t.Fatal("expected error without api key")
	}
}
