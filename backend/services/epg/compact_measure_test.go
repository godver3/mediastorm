package epg

import (
	"encoding/json"
	"os"
	"runtime"
	"testing"

	"novastream/models"
)

// TestMeasureCompactRealCache reports heap use for a real guide cache.
// Run with EPG_MEASURE_FILE=/path/to/epg.json go test -run TestMeasureCompactRealCache -v
func TestMeasureCompactRealCache(t *testing.T) {
	path := os.Getenv("EPG_MEASURE_FILE")
	if path == "" {
		t.Skip("EPG_MEASURE_FILE not set")
	}
	heap := func() uint64 {
		runtime.GC()
		runtime.GC()
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		return ms.HeapInuse
	}
	base := heap()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var s models.EPGSchedule
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		t.Fatal(err)
	}
	f.Close()
	loaded := heap()
	compactSchedule(&s)
	compacted := heap()
	t.Logf("decoded: %d MB, compacted: %d MB (programs=%d)", (loaded-base)>>20, (compacted-base)>>20, countSchedulePrograms(&s))
	runtime.KeepAlive(&s)
}
