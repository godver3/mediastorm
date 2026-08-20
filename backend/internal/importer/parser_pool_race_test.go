package importer

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"

	"github.com/javi11/nntpcli"
	"github.com/javi11/nntppool"
	"github.com/javi11/nzbparser"
)

// fakePoolReader wraps an ArticleBodyReader so we can inject the pathological
// behaviors surfaced by upstream nntppool's BodyReader race.
type fakePoolReader struct {
	panicOnHeaders bool
}

func (f *fakePoolReader) Read(p []byte) (int, error) { return 0, io.EOF }
func (f *fakePoolReader) Close() error               { return nil }
func (f *fakePoolReader) GetYencHeaders() (nntpcli.YencHeaders, error) {
	if f.panicOnHeaders {
		// Mimic nntppool v1.5.5 returning a wrapper whose inner reader is a
		// typed nil: calling a method on it dereferences a nil pointer (addr
		// 0x20) and would crash the whole process without containment.
		var r *fakePoolReader
		return r.GetYencHeaders()
	}
	return nntpcli.YencHeaders{}, errors.New("unexpected headers")
}

type fakeUsenetPool struct {
	reader      nntpcli.ArticleBodyReader
	returnNilOk bool
	bodyCalls   atomic.Int32
}

func (f *fakeUsenetPool) BodyReader(_ context.Context, _ string, _ []string) (nntpcli.ArticleBodyReader, error) {
	f.bodyCalls.Add(1)
	if f.returnNilOk {
		return nil, nil // mimic the upstream race returning a nil reader with no error
	}
	return f.reader, nil
}

func (f *fakeUsenetPool) GetConnection(context.Context, []string, bool) (nntppool.PooledConnection, error) {
	return nil, errors.New("not implemented")
}
func (f *fakeUsenetPool) Body(context.Context, string, io.Writer, []string) (int64, error) {
	return 0, errors.New("not implemented")
}
func (f *fakeUsenetPool) Post(context.Context, io.Reader) error { return errors.New("not implemented") }
func (f *fakeUsenetPool) Stat(context.Context, string, []string) (int, error) {
	return 0, errors.New("not implemented")
}
func (f *fakeUsenetPool) GetProvidersInfo() []nntppool.ProviderInfo { return nil }
func (f *fakeUsenetPool) GetProviderStatus(string) (*nntppool.ProviderInfo, bool) {
	return nil, false
}
func (f *fakeUsenetPool) Reconfigure(...nntppool.Config) error { return errors.New("not implemented") }
func (f *fakeUsenetPool) GetReconfigurationStatus(string) (*nntppool.ReconfigurationStatus, bool) {
	return nil, false
}
func (f *fakeUsenetPool) GetActiveReconfigurations() map[string]*nntppool.ReconfigurationStatus {
	return nil
}
func (f *fakeUsenetPool) GetMetrics() *nntppool.PoolMetrics { return nil }
func (f *fakeUsenetPool) GetMetricsSnapshot() nntppool.PoolMetricsSnapshot {
	return nntppool.PoolMetricsSnapshot{}
}
func (f *fakeUsenetPool) Quit() {}

type fakePoolManager struct{ pool *fakeUsenetPool }

func (m *fakePoolManager) GetPool() (nntppool.UsenetConnectionPool, error) { return m.pool, nil }
func (m *fakePoolManager) SetProviders([]nntppool.UsenetProviderConfig) error {
	return nil
}
func (m *fakePoolManager) ClearPool() error { return nil }
func (m *fakePoolManager) HasPool() bool    { return m.pool != nil }

func TestFetchYencHeadersNilReaderNoPanic(t *testing.T) {
	pool := &fakeUsenetPool{returnNilOk: true}
	p := NewParser(&fakePoolManager{pool: pool})

	_, err := p.fetchYencHeaders(context.Background(), nzbparser.NzbSegment{ID: "seg1", Bytes: 100}, []string{"alt.binaries.test"})

	if err == nil {
		t.Fatal("expected an error when the pool returns a nil body reader")
	}
	if IsNonRetryable(err) {
		t.Fatalf("nil-reader race must stay retryable so the retry loop can recover, got non-retryable: %v", err)
	}
	if pool.bodyCalls.Load() != 3 {
		t.Fatalf("expected the retry loop to attempt 3 times, got %d", pool.bodyCalls.Load())
	}
}

func TestFetchYencHeadersPanickingReaderNoCrash(t *testing.T) {
	pool := &fakeUsenetPool{reader: &fakePoolReader{panicOnHeaders: true}}
	p := NewParser(&fakePoolManager{pool: pool})

	// Historical behavior: the nil deref in nntppool's GetYencHeaders panicked
	// out of the server (SIGSEGV). It must now surface as a retryable error.
	_, err := p.fetchYencHeaders(context.Background(), nzbparser.NzbSegment{ID: "seg1", Bytes: 100}, nil)

	if err == nil {
		t.Fatal("expected an error from the panicking reader")
	}
	if IsNonRetryable(err) {
		t.Fatalf("pool-reader panic must stay retryable, got non-retryable: %v", err)
	}
	if pool.bodyCalls.Load() != 3 {
		t.Fatalf("expected the retry loop to attempt 3 times, got %d", pool.bodyCalls.Load())
	}
}

func TestFetchActualFileSizeNilReaderNoPanic(t *testing.T) {
	pool := &fakeUsenetPool{returnNilOk: true}
	p := NewParser(&fakePoolManager{pool: pool})

	file := nzbparser.NzbFile{
		Segments: []nzbparser.NzbSegment{{ID: "seg1", Bytes: 100}},
	}
	_, err := p.fetchActualFileSizeFromYencHeader(file)
	if err == nil {
		t.Fatal("expected an error when the pool returns a nil body reader")
	}
}

func TestFetchActualFileSizePanickingReaderNoCrash(t *testing.T) {
	pool := &fakeUsenetPool{reader: &fakePoolReader{panicOnHeaders: true}}
	p := NewParser(&fakePoolManager{pool: pool})

	file := nzbparser.NzbFile{
		Segments: []nzbparser.NzbSegment{{ID: "seg1", Bytes: 100}},
	}
	_, err := p.fetchActualFileSizeFromYencHeader(file)
	if err == nil {
		t.Fatal("expected an error from the panicking reader")
	}
}
