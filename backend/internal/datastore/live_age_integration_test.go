package datastore

import (
	"context"
	"github.com/google/uuid"
	"novastream/models"
	"os"
	"testing"
	"time"
)

func TestLiveAgePostgresRoundTrip(t *testing.T) {
	dsn := os.Getenv("MEDIASTORM_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set MEDIASTORM_TEST_DATABASE_URL to an isolated test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	account := models.Account{ID: uuid.NewString(), Username: "live-age-" + uuid.NewString(), PasswordHash: "synthetic-test-hash", CreatedAt: now, UpdatedAt: now}
	if err = store.Accounts().Create(ctx, &account); err != nil {
		t.Fatal(err)
	}
	defer store.Accounts().Delete(ctx, account.ID)
	age := 6
	user := models.User{ID: uuid.NewString(), AccountID: account.ID, Name: "Child", IsKidsProfile: true, KidsLiveMaxAge: &age, CreatedAt: now, UpdatedAt: now}
	if err = store.Users().Create(ctx, &user); err != nil {
		t.Fatal(err)
	}
	defer store.Users().Delete(ctx, user.ID)
	got, err := store.Users().Get(ctx, user.ID)
	if err != nil || got == nil || got.KidsLiveMaxAge == nil || *got.KidsLiveMaxAge != 6 {
		t.Fatalf("create/get failed: %v", err)
	}
	age = 12
	user.KidsLiveMaxAge = &age
	if err = store.Users().Update(ctx, &user); err != nil {
		t.Fatal(err)
	}
	list, err := store.Users().ListByAccount(ctx, account.ID)
	if err != nil || len(list) != 1 || *list[0].KidsLiveMaxAge != 12 {
		t.Fatalf("update/list failed: %v", err)
	}
	all, err := store.Users().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range all {
		if p.ID == user.ID {
			found = true
			if p.KidsLiveMaxAge == nil || *p.KidsLiveMaxAge != 12 {
				t.Fatal("list lost age")
			}
		}
	}
	if !found {
		t.Fatal("profile not listed")
	}
	user.KidsLiveMaxAge = nil
	if err = store.Users().Update(ctx, &user); err != nil {
		t.Fatal(err)
	}
	got, err = store.Users().Get(ctx, user.ID)
	if err != nil || got.KidsLiveMaxAge != nil {
		t.Fatal("nullable blocked default lost")
	}
}
