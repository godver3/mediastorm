package peartube

import "testing"

// The companion ingest job id is blake2b over the canonical JSON of the request,
// and the RELAY computes it over its own normalized form. So this process has to
// serialize a request into exactly that form or the two derive different ids and
// the relay answers "mismatched ingest job" — which is precisely what a granted
// remote source did in production: the relay's normalizeExpected always emits
// sha256 (null when a source cannot state one) and emits etag only when present,
// while this side omitted an empty sha256 and always emitted etag.
//
// The golden value below was produced by the relay itself:
//
//	ingestJobIdForRequest('idem-orville-s3e7', request)  ->  ing_188899a7b161a1499f7ccdc77e21bcbf
//
// If either implementation changes how a request is canonicalized, this fails
// instead of a live archive being refused.
func TestCompanionIngestJobIDMatchesTheRelayDerivation(t *testing.T) {
	const (
		idempotencyKey = "idem-orville-s3e7"
		relayJobID     = "ing_188899a7b161a1499f7ccdc77e21bcbf"
		byteLength     = int64(4294967296)
	)

	request := companionIngestRequest{
		RetentionClass: "contribution-cache",
		MediaContext: companionIngestMediaContext{
			Kind:             "episode",
			SeriesNamespace:  "tmdb",
			SeriesIdentifier: "71738",
			SeasonNumber:     3,
			EpisodeNumber:    7,
		},
		MeasuredFacts: companionIngestMeasuredFacts{
			Title:      "The Orville",
			ByteLength: byteLength,
			Container:  "mkv",
		},
		Expected: companionIngestExpected{
			// A granted remote source cannot state a digest without pulling the
			// whole title through this process, so this is the shape production
			// actually sends.
			ByteLength: byteLength,
			SHA256:     optionalDigest(""),
			ETag:       "remote-sha256-abc123",
		},
	}

	jobID, err := companionIngestJobID(idempotencyKey, request)
	if err != nil {
		t.Fatalf("derive job id: %v", err)
	}
	if jobID != relayJobID {
		t.Fatalf("job id diverged from the relay derivation:\n  got  %s\n  want %s", jobID, relayJobID)
	}
}
