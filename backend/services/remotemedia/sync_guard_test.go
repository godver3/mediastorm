package remotemedia

import (
	"context"
	"errors"
	"testing"
)

func TestSyncRejectsOverlap(t *testing.T) {
	s := &Service{}
	s.syncing.Store("lib", struct{}{})
	if _, err := s.Sync(context.Background(), "lib"); !errors.Is(err, ErrSyncInProgress) {
		t.Fatalf("err = %v, want ErrSyncInProgress", err)
	}
}
