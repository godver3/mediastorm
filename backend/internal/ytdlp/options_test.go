package ytdlp

import (
	"reflect"
	"testing"
)

func TestAppendProxyArgs(t *testing.T) {
	tests := []struct {
		name     string
		proxyURL string
		want     []string
	}{
		{
			name:     "empty",
			proxyURL: "",
			want:     []string{"--dump-json"},
		},
		{
			name:     "trimmed",
			proxyURL: " http://gluetun:8888 ",
			want:     []string{"--dump-json", "--proxy", "http://gluetun:8888"},
		},
		{
			name:     "socks",
			proxyURL: "socks5://127.0.0.1:1080",
			want:     []string{"--dump-json", "--proxy", "socks5://127.0.0.1:1080"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AppendProxyArgs([]string{"--dump-json"}, tt.proxyURL)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("AppendProxyArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}
