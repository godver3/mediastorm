package letterboxd

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClient_GetListItemsParsesPublicList(t *testing.T) {
	client := NewClient()
	requests := 0
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests++
			if r.URL.String() != "https://letterboxd.com/godver3/list/test/" {
				t.Fatalf("unexpected url %s", r.URL.String())
			}
			body := `<!doctype html><html><head><title>Test</title><meta name="description" content="A list of 500 films compiled on Letterboxd, including Patriot (2026)."></head><body>
				<ul class="poster-list">
					<li><div data-item-name="Patriot (2026)" data-item-slug="patriot-2026" data-target-link="/film/patriot-2026/"></div></li>
					<li><div data-item-name="Parasite (2019)" data-item-slug="parasite-2019" data-target-link="/film/parasite-2019/"></div></li>
				</ul>
			</body></html>`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})

	items, err := client.GetListItems(context.Background(), "https://letterboxd.com/godver3/list/test/?sort=rank", 10)
	if err != nil {
		t.Fatalf("GetListItems() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests = %d, want 1", requests)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	if items[0].Title != "Patriot" || items[0].Year != 2026 || items[0].Slug != "patriot-2026" || items[0].MediaType != "movie" {
		t.Fatalf("unexpected first item: %+v", items[0])
	}
	if items[1].Title != "Parasite" || items[1].Year != 2019 || items[1].URL != "https://letterboxd.com/film/parasite-2019/" {
		t.Fatalf("unexpected second item: %+v", items[1])
	}

	cached, err := client.GetListItems(context.Background(), "https://letterboxd.com/godver3/list/test/", 1)
	if err != nil {
		t.Fatalf("cached GetListItems() error = %v", err)
	}
	if requests != 1 {
		t.Fatalf("requests after small cache hit = %d, want 1", requests)
	}
	if len(cached) != 1 || cached[0].Title != "Patriot" {
		t.Fatalf("unexpected cached items: %+v", cached)
	}

	result, err := client.GetListResult(context.Background(), "https://letterboxd.com/godver3/list/test/", 10)
	if err != nil {
		t.Fatalf("GetListResult() error = %v", err)
	}
	if result.Total != 500 {
		t.Fatalf("total = %d, want 500", result.Total)
	}
	if requests != 2 {
		t.Fatalf("requests after larger partial-cache miss = %d, want 2", requests)
	}
}

func TestClient_GetListItemsFollowsPagination(t *testing.T) {
	client := NewClient()
	requests := make([]string, 0, 3)
	client.SetHTTPClientForTest(&http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			requests = append(requests, r.URL.Path)
			var body string
			switch r.URL.Path {
			case "/godver3/list/test/":
				body = `<!doctype html><html><body>
					<div data-item-name="First (2001)" data-target-link="/film/first/"></div>
					<a class="next" href="/godver3/list/test/page/2/">Next</a>
				</body></html>`
			case "/godver3/list/test/page/2/":
				body = `<!doctype html><html><body>
					<div data-item-name="Second (2002)" data-target-link="/film/second/"></div>
					<a class="previous" href="/godver3/list/test/">Previous</a>
					<a href="/godver3/list/test/page/3/">3</a>
					<a class="next" href="/godver3/list/test/page/3/">Next</a>
				</body></html>`
			case "/godver3/list/test/page/3/":
				body = `<!doctype html><html><body>
					<div data-item-name="Third (2003)" data-target-link="/film/third/"></div>
					<a class="previous" href="/godver3/list/test/page/2/">Previous</a>
				</body></html>`
			default:
				t.Fatalf("unexpected path %s", r.URL.Path)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		}),
	})

	items, err := client.GetListItems(context.Background(), "https://letterboxd.com/godver3/list/test/", 10)
	if err != nil {
		t.Fatalf("GetListItems() error = %v", err)
	}
	if len(items) != 3 || items[0].Title != "First" || items[1].Title != "Second" || items[2].Title != "Third" {
		t.Fatalf("unexpected items: %+v", items)
	}
	wantRequests := []string{"/godver3/list/test/", "/godver3/list/test/page/2/", "/godver3/list/test/page/3/"}
	if strings.Join(requests, ",") != strings.Join(wantRequests, ",") {
		t.Fatalf("requests = %+v, want %+v", requests, wantRequests)
	}
}

func TestClient_GetListItemsRejectsNonLetterboxdURL(t *testing.T) {
	client := NewClient()
	if _, err := client.GetListItems(context.Background(), "https://example.com/user/list/test/", 10); err == nil {
		t.Fatal("expected error for non-letterboxd URL")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
