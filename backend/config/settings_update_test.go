package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestManagerUpdateConcurrentWritersKeepEveryChange(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "settings.json"))
	const writers = 20
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := m.Update(func(s *Settings) error {
				s.MDBList.Accounts = append(s.MDBList.Accounts, MDBListAccount{ID: string(rune('a' + i))})
				return nil
			}); err != nil {
				t.Errorf("update %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	s, err := m.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(s.MDBList.Accounts) != writers {
		t.Fatalf("accounts = %d, want %d", len(s.MDBList.Accounts), writers)
	}
}

func TestManagerUpdateErrNoChangeSkipsWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	m := NewManager(path)
	if err := m.Save(DefaultSettings()); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if err := m.Update(func(s *Settings) error {
		s.Trakt.Username = "changed"
		return ErrNoChange
	}); err != nil {
		t.Fatalf("ErrNoChange should not be reported: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(old) {
		t.Fatal("settings file was rewritten")
	}
	boom := errors.New("boom")
	if err := m.Update(func(*Settings) error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}
