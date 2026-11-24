package trash

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

// A repository archive is cut down to docs/json before it is cached.
func TestCompactArchiveKeepsOnlyDocsJSON(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, body string) {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	add("Guides-abc/docs/json/sonarr/cf/german.json", `{ "trash_id": "g", "name": "German",
	  "specifications": [] }`)
	add("Guides-abc/docs/images/big.png", strings.Repeat("x", 1<<16))
	add("Guides-abc/README.md", "readme")
	_ = tw.Close()
	_ = gz.Close()

	compact, err := CompactArchive(buf.Bytes(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(compact) >= buf.Len() {
		t.Errorf("compact archive is not smaller: %d >= %d", len(compact), buf.Len())
	}
	files, err := readArchive(bytes.NewReader(compact))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files["sonarr/cf/german.json"] == nil || string(files["COMMIT"]) != "abc\n" {
		t.Errorf("files %v", files)
	}
	if strings.Contains(string(files["sonarr/cf/german.json"]), "\n") {
		t.Error("JSON should be compacted")
	}
}

func TestBundledLicenseIsEmbedded(t *testing.T) {
	if !strings.Contains(BundledLicense, "MIT License") || !strings.Contains(BundledLicense, "TRaSH") {
		t.Fatalf("licence not embedded: %q", BundledLicense)
	}
}
