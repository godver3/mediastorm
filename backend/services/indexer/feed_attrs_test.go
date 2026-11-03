package indexer

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"novastream/config"
	"novastream/models"
)

// Newznab/Torznab attributes are namespaced elements; they used to be
// dropped because the struct tags named the prefix ("newznab:attr").
func TestFeedAttributesAreParsed(t *testing.T) {
	const doc = `<rss xmlns:newznab="http://www.newznab.com/DTD/2010/feeds/attributes/" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>
<item><title>A</title><newznab:attr name="size" value="123"/><newznab:attr name="language" value="German"/><torznab:attr name="seeders" value="5"/></item></channel></rss>`
	var feed struct {
		Channel struct {
			Items []rssItem `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal([]byte(doc), &feed); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range feed.Channel.Items[0].Attrs {
		got[a.Name] = a.Value
	}
	if got["size"] != "123" || got["language"] != "German" || got["seeders"] != "5" {
		t.Fatalf("attributes not parsed: %v", got)
	}
}

func TestFeedInfoHashUsesDebridKey(t *testing.T) {
	for _, name := range []string{"infohash", "infoHash", "INFOHASH", " InfoHash "} {
		attrs := feedAttributes(rssItem{Attrs: []torznabAttr{{Name: name, Value: "0123456789abcdef"}}})
		if attrs["infoHash"] != "0123456789abcdef" || attrs["infohash"] != "" {
			t.Fatalf("attribute %q did not reach debrid's infoHash key: %v", name, attrs)
		}
	}
}

func TestSearchFeedAttributesReachResults(t *testing.T) {
	for _, tc := range []struct {
		kind, prefix, extra, wantURL string
	}{
		{"newznab", "newznab", `<enclosure url="https://indexer.example/download.nzb" length="999" type="application/x-nzb"/>`, "https://indexer.example/download.nzb"},
		{"torznab", "torznab", `<torznab:attr name="infohash" value="0123456789abcdef"/><torznab:attr name="magneturl" value="magnet:?xt=urn:btih:0123456789abcdef"/>`, "magnet:?xt=urn:btih:0123456789abcdef"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			feed := fmt.Sprintf(`<rss xmlns:%[1]s="http://indexer.example/attributes"><channel><item><title>Show.S01E01</title><guid>release-1</guid><link>https://indexer.example/details</link><%[1]s:attr name="size" value="123"/><%[1]s:attr name="category" value="5040"/>%[2]s</item></channel></rss>`, tc.prefix, tc.extra)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, feed)
			}))
			defer server.Close()
			s := &Service{httpc: server.Client()}
			results, err := s.searchTorznab(t.Context(), config.IndexerConfig{Name: "Attribute fixture " + tc.kind, URL: server.URL, Type: tc.kind}, SearchOptions{Query: "Show"})
			if err != nil || len(results) != 1 {
				t.Fatalf("search results=%v err=%v", results, err)
			}
			got := results[0]
			if got.SizeBytes != 123 || got.DownloadURL != tc.wantURL || got.Attributes["indexerCategory"] != "5040" || got.Attributes["category"] != "" {
				t.Fatalf("feed attributes did not reach result correctly: %+v", got)
			}
			if tc.kind == "torznab" && (got.Attributes["infoHash"] != "0123456789abcdef" || got.ServiceType != models.ServiceTypeDebrid) {
				t.Fatalf("torznab result cannot supply debrid hash: %+v", got)
			}
		})
	}
}

// Attributes whose names mediastorm uses itself are prefixed: a newznab
// category ("5040") must not replace the download client category.
func TestFeedAttributesKeepInternalKeysFree(t *testing.T) {
	item := rssItem{Attrs: []torznabAttr{
		{Name: "category", Value: "5000"}, {Name: "category", Value: "5040"},
		{Name: "resolution", Value: "1920x1080"}, {Name: "group", Value: "alt.binaries.hdtv"},
		{Name: "Size", Value: "42"}, {Name: "seeders", Value: "3"},
	}}
	got := feedAttributes(item)
	for _, key := range []string{"category", "resolution", "group"} {
		if _, ok := got[key]; ok {
			t.Errorf("%q must not be set from the feed: %v", key, got)
		}
	}
	want := map[string]string{"indexerCategory": "5040", "indexerResolution": "1920x1080", "newsgroup": "alt.binaries.hdtv", "size": "42", "seeders": "3"}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, got[k], v, got)
		}
	}
}
