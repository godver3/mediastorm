package livecapacity

import (
	"errors"
	"sync"
	"testing"
)

func TestConcurrentLastSlotAcrossConsumers(t *testing.T) {
	for _, limit := range []int{1, 2} {
		m := NewManager()
		start := make(chan struct{})
		leases := make(chan *Lease, 64)
		var wg sync.WaitGroup
		for range 64 {
			wg.Go(func() {
				<-start
				lease, err := m.Acquire("provider-account", "", limit)
				if err == nil {
					leases <- lease
				} else if !errors.Is(err, ErrLimit) {
					t.Error(err)
				}
			})
		}
		close(start)
		wg.Wait()
		close(leases)
		if len(leases) != limit {
			t.Fatalf("limit %d admitted %d inputs", limit, len(leases))
		}
		for lease := range leases {
			lease.Release()
			lease.Release()
		}
		if m.Current("provider-account") != 0 {
			t.Fatal("slot leaked")
		}
		lease, err := m.Acquire("provider-account", "", limit)
		if err != nil {
			t.Fatal(err)
		}
		lease.Release()
	}
}

func TestSharedViewersKeepSlotUntilLastInputCloses(t *testing.T) {
	m := NewManager()
	a, _ := m.Acquire("account", "channel", 1)
	b, err := m.Acquire("account", "channel", 1)
	if err != nil {
		t.Fatal(err)
	}
	a.Release()
	if _, err := m.Acquire("account", "", 1); !errors.Is(err, ErrLimit) {
		t.Fatal("released another viewer's slot")
	}
	b.Release()
	c, err := m.Acquire("account", "different-channel", 1)
	if err != nil {
		t.Fatal(err)
	}
	c.Release()
}

func TestUnexpectedHLSCannotOpenUncountedPrivateInput(t *testing.T) {
	for _, limit := range []int{1, 2} {
		m := NewManager()
		a, _ := m.Acquire("account", "looks-like-ts", limit)
		b, _ := m.Acquire("account", "looks-like-ts", limit)
		err := a.MakePrivate()
		if limit == 1 && !errors.Is(err, ErrLimit) {
			t.Fatal("HLS exceeded limit")
		}
		if limit == 2 && err != nil {
			t.Fatal(err)
		}
		if limit == 1 {
			a.Release()
		} else {
			defer a.Release()
		}
		if err := b.MakePrivate(); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Acquire("account", "looks-like-ts", limit); !errors.Is(err, ErrLimit) {
			t.Fatal("private input still advertised as shared")
		}
		b.Release()
	}
}

func TestAliasesUseStricterLimitAndSeparateAccountsRemainIndependent(t *testing.T) {
	m := NewManager()
	a, _ := m.Acquire("account", "", 3)
	if _, err := m.Acquire("account", "", 1); !errors.Is(err, ErrLimit) {
		t.Fatal("alias increased capacity")
	}
	if _, err := m.Acquire("account", "", 3); !errors.Is(err, ErrLimit) {
		t.Fatal("weaker alias bypassed limit")
	}
	b, err := m.Acquire("other-account", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	b.Release()
	a.Release()
}
