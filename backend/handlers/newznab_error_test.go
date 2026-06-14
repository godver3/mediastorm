package handlers

import "testing"

func TestParseNewznabError(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		wantMsg string
	}{
		{
			name:    "request limit reached",
			body:    `<?xml version="1.0" encoding="UTF-8"?><error code="500" description="Request limit reached." />`,
			wantErr: true,
			wantMsg: "API error 500: Request limit reached.",
		},
		{
			name:    "incorrect credentials",
			body:    `<error code="100" description="Incorrect user credentials"/>`,
			wantErr: true,
			wantMsg: "API error 100: Incorrect user credentials",
		},
		{
			name:    "description only",
			body:    `<error description="Account suspended"/>`,
			wantErr: true,
			wantMsg: "Account suspended",
		},
		{
			name:    "code only",
			body:    `<error code="200"/>`,
			wantErr: true,
			wantMsg: "API error 200",
		},
		{
			name:    "bare error element",
			body:    `<error/>`,
			wantErr: true,
			wantMsg: "indexer returned an API error",
		},
		{
			name:    "valid rss feed is not an error",
			body:    `<?xml version="1.0"?><rss version="2.0"><channel><item><title>Moana 2016 1080p</title></item></channel></rss>`,
			wantErr: false,
		},
		{
			name:    "empty rss feed is not an error",
			body:    `<rss version="2.0"><channel></channel></rss>`,
			wantErr: false,
		},
		{
			name:    "non-xml body is not an error",
			body:    `not xml at all`,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg, isErr := parseNewznabError([]byte(tt.body))
			if isErr != tt.wantErr {
				t.Fatalf("parseNewznabError() isErr = %v, want %v (msg=%q)", isErr, tt.wantErr, msg)
			}
			if tt.wantErr && msg != tt.wantMsg {
				t.Errorf("parseNewznabError() msg = %q, want %q", msg, tt.wantMsg)
			}
		})
	}
}
