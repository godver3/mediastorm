package importer

import (
	"context"
	"testing"

	"novastream/internal/nzb/metadata"
	metapb "novastream/internal/nzb/metadata/proto"
)

// Scene releases packed inside an obfuscated outer archive use old-style
// volumes (.rar + .r00...). Every volume must be recognised as RAR so the
// nested set is unpacked instead of being materialized as plain files.
func TestIsRarFileRecognisesAllVolumeStyles(t *testing.T) {
	proc := &Processor{}
	for name, want := range map[string]bool{
		"cakes-lwt.s13e28.rar":         true,
		"cakes-lwt.s13e28.r00":         true,
		"cakes-lwt.s13e28.r14":         true,
		"cakes-lwt.s13e28.R99":         true,
		"cakes-lwt.s13e28.r100":        true,
		"cakes-lwt.s13e28.s00":         true,
		"Show.S01E01.part01.rar":       true,
		"Show.S01E01.part12.rar":       true,
		"cakes-lwt.s13e28.sfv":         false,
		"cakes-lwt.s13e28.nfo":         false,
		"Show.S01E01.1080p.mkv":        false,
		"Show.S01E01.1080p.rarbg.mkv":  false,
		"Show.S01E01.srt":              false,
		"Show.S01E01.7z.001":           false,
		"Show.S01E01.vol00+01.par2":    false,
		"Sample/cakes-lwt-sample.mkv":  false,
		"cakes-lwt.s13e28.r00.partial": false,
	} {
		if got := proc.isRarFile(name); got != want {
			t.Errorf("isRarFile(%q) = %t, want %t", name, got, want)
		}
	}
}

func TestForgetResolvedNZBDropsEntryAndMetadata(t *testing.T) {
	ctx := context.Background()
	metaSvc := metadata.NewMetadataService(t.TempDir())
	svc := &Service{resolvedCache: newResolvedNZBCache(metaSvc)}
	nzb := []byte("<nzb>broken import</nzb>")

	dir := "/123_Show.S13E28.1080p.WEB.H264-CAKES"
	if err := metaSvc.WriteFileMetadata(dir+"/Show/show.r00", &metapb.FileMetadata{FileSize: 10, Status: metapb.FileStatus_FILE_STATUS_HEALTHY}); err != nil {
		t.Fatal(err)
	}
	if err := svc.resolvedCache.put(ctx, resolvedNZBHash(nzb), "show.nzb", dir, ResolvedNZBSource{}); err != nil {
		t.Fatal(err)
	}
	if !svc.HasResolvedNZB(ctx, nzb) {
		t.Fatal("expected cached import to be reported")
	}

	if err := svc.ForgetResolvedNZB(ctx, nzb); err != nil {
		t.Fatalf("ForgetResolvedNZB() error = %v", err)
	}
	if svc.HasResolvedNZB(ctx, nzb) {
		t.Fatal("cached import still reported after ForgetResolvedNZB")
	}
	if metaSvc.DirectoryExists(dir) {
		t.Fatal("broken import metadata should be removed so a re-import starts clean")
	}
	// Forgetting an import that is not cached is a no-op.
	if err := svc.ForgetResolvedNZB(ctx, nzb); err != nil {
		t.Fatalf("second ForgetResolvedNZB() error = %v", err)
	}
}
