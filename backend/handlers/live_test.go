package handlers

import (
	"testing"
)

func TestSplitM3ULine(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		wantMetadata string
		wantName     string
	}{
		{
			name:         "standard format",
			input:        `-1 tvg-id="test" tvg-name="Test Channel",Test Channel`,
			wantMetadata: `-1 tvg-id="test" tvg-name="Test Channel"`,
			wantName:     "Test Channel",
		},
		{
			name:         "comma in attribute value",
			input:        `-1 tvg-name="News, Sports & More" group-title="Entertainment",Channel Name`,
			wantMetadata: `-1 tvg-name="News, Sports & More" group-title="Entertainment"`,
			wantName:     "Channel Name",
		},
		{
			name:         "multiple commas in attributes",
			input:        `-1 tvg-name="A, B, C" tvg-logo="http://example.com/logo,test.png",Final Name`,
			wantMetadata: `-1 tvg-name="A, B, C" tvg-logo="http://example.com/logo,test.png"`,
			wantName:     "Final Name",
		},
		{
			name:         "no comma - metadata only",
			input:        `-1 tvg-id="test"`,
			wantMetadata: `-1 tvg-id="test"`,
			wantName:     "",
		},
		{
			name:         "simple duration and name",
			input:        `-1,Simple Channel`,
			wantMetadata: `-1`,
			wantName:     "Simple Channel",
		},
		{
			name:         "empty input",
			input:        ``,
			wantMetadata: ``,
			wantName:     "",
		},
		{
			name:         "real world example",
			input:        `-1 tvg-id="aande.us" tvg-name="US | A&E" tvg-logo="https://example.com/logo.png" group-title="US - Entertainment",US | A&E`,
			wantMetadata: `-1 tvg-id="aande.us" tvg-name="US | A&E" tvg-logo="https://example.com/logo.png" group-title="US - Entertainment"`,
			wantName:     "US | A&E",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotMetadata, gotName := splitM3ULine(tt.input)
			if gotMetadata != tt.wantMetadata {
				t.Errorf("splitM3ULine() metadata = %q, want %q", gotMetadata, tt.wantMetadata)
			}
			if gotName != tt.wantName {
				t.Errorf("splitM3ULine() name = %q, want %q", gotName, tt.wantName)
			}
		})
	}
}

func TestParseM3UPlaylist(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected []LiveChannel
	}{
		{
			name:     "empty input",
			input:    "",
			expected: nil,
		},
		{
			name: "standard channel",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="test" tvg-name="Test Channel" tvg-logo="http://logo.png" group-title="News",Test Channel
http://stream.example.com/live.m3u8`,
			expected: []LiveChannel{
				{
					ID:      "test",
					Name:    "Test Channel",
					URL:     "http://stream.example.com/live.m3u8",
					Logo:    "http://logo.png",
					Group:   "News",
					TvgID:   "test",
					TvgName: "Test Channel",
				},
			},
		},
		{
			name: "channel with comma in tvg-name",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="sports" tvg-name="Sports, News & More" group-title="Entertainment",Sports, News & More
http://stream.example.com/sports.m3u8`,
			expected: []LiveChannel{
				{
					ID:      "sports",
					Name:    "Sports, News & More",
					URL:     "http://stream.example.com/sports.m3u8",
					Group:   "Entertainment",
					TvgID:   "sports",
					TvgName: "Sports, News & More",
				},
			},
		},
		{
			name: "fallback to tvg-name when no display name",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="test" tvg-name="Fallback Name"
http://stream.example.com/live.m3u8`,
			expected: []LiveChannel{
				{
					ID:      "test",
					Name:    "Fallback Name",
					URL:     "http://stream.example.com/live.m3u8",
					TvgID:   "test",
					TvgName: "Fallback Name",
				},
			},
		},
		{
			name: "multiple channels",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="ch1" tvg-name="Channel 1",Channel 1
http://stream1.example.com
#EXTINF:-1 tvg-id="ch2" tvg-name="Channel 2",Channel 2
http://stream2.example.com`,
			expected: []LiveChannel{
				{
					ID:      "ch1",
					Name:    "Channel 1",
					URL:     "http://stream1.example.com",
					TvgID:   "ch1",
					TvgName: "Channel 1",
				},
				{
					ID:      "ch2",
					Name:    "Channel 2",
					URL:     "http://stream2.example.com",
					TvgID:   "ch2",
					TvgName: "Channel 2",
				},
			},
		},
		{
			name: "duplicate IDs get unique suffixes",
			input: `#EXTM3U
#EXTINF:-1 tvg-id="same" tvg-name="First",First
http://stream1.example.com
#EXTINF:-1 tvg-id="same" tvg-name="Second",Second
http://stream2.example.com`,
			expected: []LiveChannel{
				{
					ID:      "same",
					Name:    "First",
					URL:     "http://stream1.example.com",
					TvgID:   "same",
					TvgName: "First",
				},
				{
					ID:      "same-1",
					Name:    "Second",
					URL:     "http://stream2.example.com",
					TvgID:   "same",
					TvgName: "Second",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseM3UPlaylist(tt.input)
			if len(got) != len(tt.expected) {
				t.Fatalf("parseM3UPlaylist() returned %d channels, want %d", len(got), len(tt.expected))
			}
			for i, ch := range got {
				exp := tt.expected[i]
				if ch.ID != exp.ID {
					t.Errorf("channel[%d].ID = %q, want %q", i, ch.ID, exp.ID)
				}
				if ch.Name != exp.Name {
					t.Errorf("channel[%d].Name = %q, want %q", i, ch.Name, exp.Name)
				}
				if ch.URL != exp.URL {
					t.Errorf("channel[%d].URL = %q, want %q", i, ch.URL, exp.URL)
				}
				if ch.Logo != exp.Logo {
					t.Errorf("channel[%d].Logo = %q, want %q", i, ch.Logo, exp.Logo)
				}
				if ch.Group != exp.Group {
					t.Errorf("channel[%d].Group = %q, want %q", i, ch.Group, exp.Group)
				}
				if ch.TvgID != exp.TvgID {
					t.Errorf("channel[%d].TvgID = %q, want %q", i, ch.TvgID, exp.TvgID)
				}
				if ch.TvgName != exp.TvgName {
					t.Errorf("channel[%d].TvgName = %q, want %q", i, ch.TvgName, exp.TvgName)
				}
			}
		})
	}
}
