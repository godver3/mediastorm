package trash

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"sync"
)

// bundledArchive is a compact snapshot of TRaSH-Guides docs/json (custom
// formats, CF groups, quality profiles, quality sizes, conflicts) so
// profiles can be imported without network access. Refresh it with
//
//	go run ./tools/arrparity bundle -trash <Guides>/docs/json -commit <sha> -out internal/trash/bundle/trash-guides.tar.gz
//
// TRaSH-Guides is MIT licensed, Copyright (c) TRaSH; see bundle/LICENSE.
//
//go:embed bundle/trash-guides.tar.gz
var bundledArchive []byte

// BundledLicense is TRaSH-Guides' MIT licence. It is embedded with the
// snapshot so every copy of the binary carries the notice, and the quality
// presets page shows it.
//
//go:embed bundle/LICENSE
var BundledLicense string

var (
	bundleOnce sync.Once
	bundleSnap *Snapshot
	bundleErr  error
)

// Bundled returns the embedded snapshot. The result is shared; callers must
// not modify it.
func Bundled() (*Snapshot, error) {
	bundleOnce.Do(func() {
		bundleSnap, bundleErr = LoadArchive(bytes.NewReader(bundledArchive), "")
		if bundleErr == nil {
			bundleSnap.Source = "bundled"
		}
	})
	return bundleSnap, bundleErr
}

// LoadArchiveFile reads a snapshot archive from disk.
func LoadArchiveFile(path string) (*Snapshot, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	snap, err := LoadArchive(f, "")
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return snap, nil
}
