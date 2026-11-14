package users

import "testing"

func TestKidsLiveAgePersistsAcrossServiceReload(t *testing.T) {
	dir := t.TempDir()
	s, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	child, err := s.CreateForAccount("household", "Child")
	if err != nil {
		t.Fatal(err)
	}
	age := 6
	if _, err = s.SetKidsLiveMaxAge(child.ID, &age); err != nil {
		t.Fatal(err)
	}
	age = 18
	reloaded, err := NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reloaded.Get(child.ID)
	if got.KidsLiveMaxAge == nil || *got.KidsLiveMaxAge != 6 {
		t.Fatal("age was not persisted independently of the caller")
	}
	invalid := 7
	if _, err = reloaded.SetKidsLiveMaxAge(child.ID, &invalid); err == nil {
		t.Fatal("invalid FSK ceiling accepted")
	}
	got, _ = reloaded.Get(child.ID)
	if *got.KidsLiveMaxAge != 6 {
		t.Fatal("invalid setting changed the policy")
	}
	if _, err = reloaded.SetKidsLiveMaxAge(child.ID, nil); err != nil {
		t.Fatal(err)
	}
	reloaded, err = NewService(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = reloaded.Get(child.ID)
	if got.KidsLiveMaxAge != nil {
		t.Fatal("blocked default was not restored")
	}
}
