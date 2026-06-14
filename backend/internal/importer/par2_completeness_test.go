package importer

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"testing"

	metapb "novastream/internal/nzb/metadata/proto"
)

// buildPar2FileDescPacket constructs a valid PAR2 FileDesc packet for the given file.
func buildPar2FileDescPacket(fileID byte, name string, length uint64) []byte {
	body := &bytes.Buffer{}
	var id, md5, first [16]byte
	id[0] = fileID
	body.Write(id[:])
	body.Write(md5[:])
	body.Write(first[:])
	_ = binary.Write(body, binary.LittleEndian, length)
	nameBytes := []byte(name)
	for len(nameBytes)%4 != 0 { // PAR2 4-byte alignment, null-padded
		nameBytes = append(nameBytes, 0)
	}
	body.Write(nameBytes)

	pkt := &bytes.Buffer{}
	pkt.Write([]byte{'P', 'A', 'R', '2', 0, 'P', 'K', 'T'})
	_ = binary.Write(pkt, binary.LittleEndian, uint64(64+body.Len()))
	pkt.Write(make([]byte, 16)) // MD5Hash
	pkt.Write(make([]byte, 16)) // RecoveryID
	pkt.Write([]byte{'P', 'A', 'R', ' ', '2', '.', '0', 0, 'F', 'i', 'l', 'e', 'D', 'e', 's', 'c'})
	pkt.Write(body.Bytes())
	return pkt.Bytes()
}

// buildPar2OtherPacket constructs a non-FileDesc packet (e.g. Main) to exercise skipping.
func buildPar2OtherPacket(bodyLen int) []byte {
	pkt := &bytes.Buffer{}
	pkt.Write([]byte{'P', 'A', 'R', '2', 0, 'P', 'K', 'T'})
	_ = binary.Write(pkt, binary.LittleEndian, uint64(64+bodyLen))
	pkt.Write(make([]byte, 16))
	pkt.Write(make([]byte, 16))
	pkt.Write([]byte{'P', 'A', 'R', ' ', '2', '.', '0', 0, 'M', 'a', 'i', 'n', 0, 0, 0, 0})
	pkt.Write(make([]byte, bodyLen))
	return pkt.Bytes()
}

func TestCollectAllPar2FileDescriptors(t *testing.T) {
	stream := &bytes.Buffer{}
	stream.Write(buildPar2OtherPacket(40)) // Main packet, skipped
	stream.Write(buildPar2FileDescPacket(1, "movie.part001.rar", 1000))
	stream.Write(buildPar2FileDescPacket(2, "movie.part002.rar", 2000))
	stream.Write(buildPar2FileDescPacket(1, "movie.part001.rar", 1000)) // duplicate FileID, deduped

	d := &Deobfuscator{log: slog.Default()}
	descs := d.collectAllPar2FileDescriptors(bytes.NewReader(stream.Bytes()))
	if len(descs) != 2 {
		t.Fatalf("expected 2 unique descriptors, got %d: %+v", len(descs), descs)
	}
	if descs[0].Filename != "movie.part001.rar" || descs[0].FileLength != 1000 {
		t.Errorf("desc[0] wrong: %+v", descs[0])
	}
	if descs[1].Filename != "movie.part002.rar" || descs[1].FileLength != 2000 {
		t.Errorf("desc[1] wrong: %+v", descs[1])
	}
}

func TestCheckPar2Completeness(t *testing.T) {
	expected := []PAR2FileDesc{
		{Filename: "movie.part001.rar", FileLength: 1000},
		{Filename: "movie.part002.rar", FileLength: 1000},
		{Filename: "movie.part003.rar", FileLength: 1000},
	}
	present := []ParsedFile{
		{Filename: "movie.part001.rar", Segments: segsOfSize(600, 400)},     // 1000 total: complete
		{Filename: "/dir/movie.part002.rar", Segments: segsOfSize(500, 100)}, // 600 < 980: truncated
		// part003 entirely absent: missing
	}
	problems := checkPar2Completeness(expected, present)
	if len(problems) != 2 {
		t.Fatalf("expected 2 problems, got %d: %v", len(problems), problems)
	}
	joined := problems[0] + " | " + problems[1]
	if !contains(joined, "truncated") || !contains(joined, "part002") {
		t.Errorf("expected truncated part002 problem, got %v", problems)
	}
	if !contains(joined, "missing") || !contains(joined, "part003") {
		t.Errorf("expected missing part003 problem, got %v", problems)
	}

	// A fully-present set yields no problems.
	if got := checkPar2Completeness(expected[:1], present[:1]); len(got) != 0 {
		t.Errorf("expected no problems for complete file, got %v", got)
	}
}

func TestFindPar2IndexFile(t *testing.T) {
	files := []ParsedFile{
		{Filename: "movie.vol00+10.par2", Segments: segsOfSize(1, 2, 3, 4, 5)}, // recovery: many segs
		{Filename: "movie.par2", Segments: segsOfSize(1, 2)},                    // index: fewest segs
		{Filename: "movie.vol10+20.par2", Segments: segsOfSize(1, 2, 3)},
	}
	idx := findPar2IndexFile(files)
	if idx == nil || idx.Filename != "movie.par2" {
		t.Fatalf("expected movie.par2 as index, got %+v", idx)
	}
	if findPar2IndexFile(nil) != nil {
		t.Error("expected nil for empty input")
	}
}

func TestDownloadParsedFilePrefix(t *testing.T) {
	file := ParsedFile{
		Filename: "movie.par2",
		Segments: []*metapb.SegmentData{{Id: "a"}, {Id: "b"}, {Id: "c"}},
	}
	pool := &fakeBodyPool{bodies: map[string][]byte{
		"a": []byte("hello "),
		"b": []byte("world"),
		// "c" intentionally missing -> read stops after a+b
	}}
	got := downloadParsedFilePrefix(context.Background(), pool, file, par2CompletenessMaxBytes)
	if string(got) != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}

	// Byte cap truncates.
	capped := downloadParsedFilePrefix(context.Background(), pool, file, 4)
	if string(capped) != "hell" {
		t.Fatalf("expected cap to 'hell', got %q", capped)
	}
}

// --- helpers ---

func segsOfSize(sizes ...int64) []*metapb.SegmentData {
	out := make([]*metapb.SegmentData, len(sizes))
	for i, s := range sizes {
		out[i] = &metapb.SegmentData{SegmentSize: s}
	}
	return out
}

func contains(haystack, needle string) bool { return bytes.Contains([]byte(haystack), []byte(needle)) }

type fakeBodyPool struct{ bodies map[string][]byte }

func (f *fakeBodyPool) BodyReader(_ context.Context, msgID string, _ []string) (io.ReadCloser, error) {
	b, ok := f.bodies[msgID]
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
