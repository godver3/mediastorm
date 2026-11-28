package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDebridProviderEffectiveProxyURL(t *testing.T) {
	cases := []struct {
		name    string
		p       DebridProviderSettings
		want    string
		wantErr bool
	}{
		{"off", DebridProviderSettings{}, "", false},
		{"off keeps URL unused", DebridProviderSettings{ProxyURL: "http://p:1"}, "", false},
		{"on", DebridProviderSettings{ProxyEnabled: true, ProxyURL: " socks5h://u:p@h:1080 "}, "socks5h://u:p@h:1080", false},
		{"on without URL fails closed", DebridProviderSettings{Name: "RD", ProxyEnabled: true}, "", true},
		{"on with blank URL fails closed", DebridProviderSettings{ProxyEnabled: true, ProxyURL: "  "}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.p.EffectiveProxyURL()
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("got (%q, %v), want (%q, err=%v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestDebridProviderProxyJSONRoundTrip(t *testing.T) {
	in := DebridProviderSettings{Name: "RD", Provider: "realdebrid", ProxyEnabled: true, ProxyURL: "http://p:1"}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"proxyEnabled":true`) || !strings.Contains(string(b), `"proxyUrl":"http://p:1"`) {
		t.Fatalf("json = %s", b)
	}
	var out DebridProviderSettings
	if err := json.Unmarshal(b, &out); err != nil || out.ProxyEnabled != true || out.ProxyURL != "http://p:1" {
		t.Fatalf("round trip = %+v, %v", out, err)
	}
	// Existing settings without the fields stay direct.
	var legacy DebridProviderSettings
	_ = json.Unmarshal([]byte(`{"name":"RD","provider":"realdebrid","apiKey":"k","enabled":true}`), &legacy)
	if u, err := legacy.EffectiveProxyURL(); u != "" || err != nil {
		t.Fatalf("legacy provider should be direct, got %q %v", u, err)
	}
}
