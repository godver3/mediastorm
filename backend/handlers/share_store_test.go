package handlers

import (
	"testing"
	"time"
)

func TestShareStoreConsumeIsSingleUse(t *testing.T) {
	store := NewShareStore()
	rec, err := store.Create("acct1", false, map[string]string{"sourcePath": "/movies/a.mkv"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if rec.Token == "" {
		t.Fatal("expected a token")
	}

	got, ok := store.Consume(rec.Token)
	if !ok {
		t.Fatal("first Consume should succeed")
	}
	if got.Params["sourcePath"] != "/movies/a.mkv" {
		t.Fatalf("params not preserved: %v", got.Params)
	}

	if _, ok := store.Consume(rec.Token); ok {
		t.Fatal("second Consume should fail (single use)")
	}
}

func TestShareStoreConsumeExpired(t *testing.T) {
	store := NewShareStore()
	rec, err := store.Create("acct1", false, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	store.mu.Lock()
	store.shares[rec.Token].ExpiresAt = time.Now().Add(-time.Minute)
	store.mu.Unlock()

	if _, ok := store.Consume(rec.Token); ok {
		t.Fatal("expired share should not be consumable")
	}
}

func TestShareStoreConsumeUnknown(t *testing.T) {
	store := NewShareStore()
	if _, ok := store.Consume("does-not-exist"); ok {
		t.Fatal("unknown token should not be consumable")
	}
	if _, ok := store.Consume(""); ok {
		t.Fatal("empty token should not be consumable")
	}
}

func TestShareStorePurge(t *testing.T) {
	store := NewShareStore()
	expired, _ := store.Create("acct1", false, nil)
	consumed, _ := store.Create("acct1", false, nil)
	live, _ := store.Create("acct1", false, nil)

	store.mu.Lock()
	store.shares[expired.Token].ExpiresAt = time.Now().Add(-time.Minute)
	store.shares[consumed.Token].Consumed = true
	store.mu.Unlock()

	store.purge()

	store.mu.Lock()
	defer store.mu.Unlock()
	if _, ok := store.shares[expired.Token]; ok {
		t.Fatal("expired share should be purged")
	}
	if _, ok := store.shares[consumed.Token]; ok {
		t.Fatal("consumed share should be purged")
	}
	if _, ok := store.shares[live.Token]; !ok {
		t.Fatal("live share should survive purge")
	}
}
