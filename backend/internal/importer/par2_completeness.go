package importer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	metapb "novastream/internal/nzb/metadata/proto"

	"github.com/javi11/nntppool"
	"github.com/javi11/nzbparser"
)

// par2CompletenessMaxBytes caps how much of the PAR2 index we download to enumerate
// the recovery set. FileDesc packets are small and written near the front of the
// index (after the Main packet, before the large slice-checksum packets), so a few
// MB captures them even for releases with hundreds of volumes.
const par2CompletenessMaxBytes = 16 << 20 // 16 MiB

// par2CompletenessShortfallRatio is the fraction of a file's PAR2-declared length that
// must be present in the NZB for it to count as complete. A file whose referenced
// segments sum to less than this is treated as truncated (missing segments in the NZB).
const par2CompletenessShortfallRatio = 0.98

// par2IdentityFile builds a minimal ParsedFile carrying only the identity needed to
// download and identify a PAR2 file (no yEnc normalization). Used to retain PAR2 files
// that are otherwise filtered out of the playable file set.
func par2IdentityFile(file nzbparser.NzbFile) ParsedFile {
	segs := make([]*metapb.SegmentData, 0, len(file.Segments))
	for _, s := range file.Segments {
		if strings.TrimSpace(s.ID) == "" {
			continue
		}
		segs = append(segs, &metapb.SegmentData{Id: s.ID, SegmentSize: int64(s.Bytes)})
	}
	return ParsedFile{
		Subject:  file.Subject,
		Filename: file.Filename,
		Segments: segs,
		Groups:   file.Groups,
	}
}

// findPar2IndexFile picks the PAR2 index file from a set of PAR2 files: the index
// carries no recovery slices, so it has the fewest segments. Returns nil if none.
func findPar2IndexFile(par2Files []ParsedFile) *ParsedFile {
	var best *ParsedFile
	for i := range par2Files {
		f := &par2Files[i]
		if len(f.Segments) == 0 {
			continue
		}
		if best == nil || len(f.Segments) < len(best.Segments) {
			best = f
		}
	}
	return best
}

// normalizePar2Name reduces a filename to a comparable key: lower-cased basename.
func normalizePar2Name(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "/")
	return strings.ToLower(path.Base(name))
}

// checkPar2Completeness compares the recovery set declared by PAR2 (expected) against
// the files actually present in the NZB. It returns a human-readable problem for every
// expected file that is missing entirely or whose present segments fall short of the
// PAR2-declared length (truncated). An empty result means the NZB is structurally
// complete with respect to PAR2. This detects NZB-level incompleteness (missing or
// truncated files); it cannot detect provider-side purged article bodies, since those
// segments are still referenced by the NZB.
func checkPar2Completeness(expected []PAR2FileDesc, present []ParsedFile) []string {
	bySize := make(map[string]int64, len(present))
	for _, f := range present {
		key := normalizePar2Name(f.Filename)
		if key == "" {
			continue
		}
		var total int64
		for _, s := range f.Segments {
			total += s.SegmentSize
		}
		// A file may appear under more than one entry; keep the largest available total.
		if total > bySize[key] {
			bySize[key] = total
		}
	}

	var problems []string
	for _, exp := range expected {
		key := normalizePar2Name(exp.Filename)
		if key == "" {
			continue
		}
		total, ok := bySize[key]
		if !ok {
			problems = append(problems, fmt.Sprintf("missing file %q", exp.Filename))
			continue
		}
		if exp.FileLength > 0 && float64(total) < float64(exp.FileLength)*par2CompletenessShortfallRatio {
			problems = append(problems, fmt.Sprintf("truncated file %q (have %d of %d bytes)", exp.Filename, total, exp.FileLength))
		}
	}
	return problems
}

// downloadParsedFilePrefix fetches up to maxBytes of a file's decoded content by reading
// its segments in order. It stops at the byte cap or when a segment is unavailable,
// returning whatever was gathered (so a partially-available PAR2 index still yields the
// FileDesc packets at its front).
func downloadParsedFilePrefix(ctx context.Context, cp par2BodyPool, file ParsedFile, maxBytes int64) []byte {
	buf := make([]byte, 0, 1<<20)
	for _, seg := range file.Segments {
		if int64(len(buf)) >= maxBytes {
			break
		}
		r, err := cp.BodyReader(ctx, seg.Id, file.Groups)
		if err != nil {
			break
		}
		chunk, _ := io.ReadAll(io.LimitReader(r, maxBytes-int64(len(buf))))
		_ = r.Close()
		buf = append(buf, chunk...)
	}
	return buf
}

// par2BodyPool is the minimal connection-pool surface needed to read PAR2 article bodies.
type par2BodyPool interface {
	BodyReader(ctx context.Context, msgID string, nntpGroups []string) (io.ReadCloser, error)
}

// poolBodyAdapter adapts a full nntppool connection pool to par2BodyPool.
type poolBodyAdapter struct{ cp nntppool.UsenetConnectionPool }

func (p poolBodyAdapter) BodyReader(ctx context.Context, msgID string, groups []string) (io.ReadCloser, error) {
	return p.cp.BodyReader(ctx, msgID, groups)
}

// verifyPar2Completeness downloads the PAR2 index for a parsed NZB and verifies that
// every file the recovery set declares is present and untruncated. It is fail-open:
// any error obtaining or parsing PAR2 returns no problems (the caller proceeds), so the
// check can only reject confirmed-incomplete releases, never block healthy ones.
func (proc *Processor) verifyPar2Completeness(ctx context.Context, parsed *ParsedNzb) []string {
	index := findPar2IndexFile(parsed.Par2Files)
	if index == nil {
		return nil // no PAR2 — nothing to verify against
	}
	if proc.poolManager == nil {
		return nil
	}
	cp, err := proc.poolManager.GetPool()
	if err != nil {
		proc.log.Debug("par2 completeness: no pool", "error", err)
		return nil
	}

	checkCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	data := downloadParsedFilePrefix(checkCtx, poolBodyAdapter{cp}, *index, par2CompletenessMaxBytes)
	if len(data) == 0 {
		proc.log.Warn("par2 completeness: could not download index; skipping check", "index", index.Filename)
		return nil
	}

	expected := proc.parser.deobfuscator.collectAllPar2FileDescriptors(bytes.NewReader(data))
	if len(expected) == 0 {
		proc.log.Warn("par2 completeness: no file descriptors parsed; skipping check", "index", index.Filename)
		return nil
	}

	problems := checkPar2Completeness(expected, parsed.Files)
	proc.log.Info("par2 completeness check",
		"expected_files", len(expected), "present_files", len(parsed.Files), "problems", len(problems))
	return problems
}
