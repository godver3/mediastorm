package remoteaccess

import (
	"context"
	"testing"
	"time"

	"novastream/models"
)

type fakeInviteRepo struct {
	byID   map[string]models.RemoteAccessInvite
	byHash map[string]string
}

type fakeHost struct {
	invite  string
	running bool
	ensures int
	stops   int
}

func newFakeInviteRepo() *fakeInviteRepo {
	return &fakeInviteRepo{
		byID:   make(map[string]models.RemoteAccessInvite),
		byHash: make(map[string]string),
	}
}

func (h *fakeHost) Ensure(ctx context.Context) (string, error) {
	h.ensures++
	h.running = true
	if h.invite == "" {
		h.invite = "mshost-iroh-direct-test"
	}
	return h.invite, nil
}

func (h *fakeHost) Stop(ctx context.Context) error {
	h.stops++
	h.running = false
	return nil
}

func (h *fakeHost) Status(ctx context.Context) models.RemoteAccessStatus {
	return models.RemoteAccessStatus{
		Enabled:     true,
		Running:     h.running,
		Provider:    "iroh",
		State:       "test",
		ActiveHosts: boolToInt(h.running),
	}
}

func (r *fakeInviteRepo) Get(ctx context.Context, id string) (*models.RemoteAccessInvite, error) {
	inv, ok := r.byID[id]
	if !ok {
		return nil, nil
	}
	return &inv, nil
}

func (r *fakeInviteRepo) GetByTokenHash(ctx context.Context, tokenHash string) (*models.RemoteAccessInvite, error) {
	id, ok := r.byHash[tokenHash]
	if !ok {
		return nil, nil
	}
	return r.Get(ctx, id)
}

func (r *fakeInviteRepo) List(ctx context.Context) ([]models.RemoteAccessInvite, error) {
	result := make([]models.RemoteAccessInvite, 0, len(r.byID))
	for _, inv := range r.byID {
		result = append(result, inv)
	}
	return result, nil
}

func (r *fakeInviteRepo) Create(ctx context.Context, inv *models.RemoteAccessInvite) error {
	r.byID[inv.ID] = *inv
	r.byHash[inv.TokenHash] = inv.ID
	return nil
}

func (r *fakeInviteRepo) ClaimByTokenHash(ctx context.Context, tokenHash string, peerID string, now time.Time) (*models.RemoteAccessInvite, error) {
	inv, err := r.GetByTokenHash(ctx, tokenHash)
	if err != nil || inv == nil {
		return inv, err
	}
	if inv.RevokedAt != nil || (!now.Before(inv.ExpiresAt) && inv.UsedByPeerID != peerID) {
		return nil, nil
	}
	if inv.UsedAt != nil && inv.UsedByPeerID != peerID {
		return nil, nil
	}
	if inv.UsedAt == nil {
		inv.UsedAt = &now
		inv.UsedByPeerID = peerID
		if err := r.Update(ctx, inv); err != nil {
			return nil, err
		}
	}
	return inv, nil
}

func (r *fakeInviteRepo) Update(ctx context.Context, inv *models.RemoteAccessInvite) error {
	r.byID[inv.ID] = *inv
	r.byHash[inv.TokenHash] = inv.ID
	return nil
}

func (r *fakeInviteRepo) Delete(ctx context.Context, id string) error {
	inv, ok := r.byID[id]
	if ok {
		delete(r.byHash, inv.TokenHash)
	}
	delete(r.byID, id)
	return nil
}

func (r *fakeInviteRepo) Count(ctx context.Context) (int64, error) {
	return int64(len(r.byID)), nil
}

func TestCreateInviteStartsSharedIrohHost(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{}
	svc := NewService(repo, host)
	svc.now = func() time.Time { return time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC) }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{PeerName: "iPhone"})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if !host.running || host.ensures != 1 {
		t.Fatalf("host running=%t ensures=%d, want running with one ensure", host.running, host.ensures)
	}
	if inv.ConnectionCode == "" || inv.ConnectionCode == inv.IrohInvite {
		t.Fatalf("connection code = %q, iroh invite = %q; want separate short code and iroh invite", inv.ConnectionCode, inv.IrohInvite)
	}
	if inv.IrohInvite != "mshost-iroh-direct-test" {
		t.Fatalf("iroh invite = %q, want resolved iroh invite", inv.IrohInvite)
	}
}

func TestSuperviseStopsHostWhenNoActiveInvites(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{running: true, invite: "mshost-iroh-direct-test"}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	stored := repo.byID[inv.ID]
	stored.ExpiresAt = now.Add(-time.Minute)
	repo.byID[inv.ID] = stored

	summary, err := svc.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise returned error: %v", err)
	}
	if summary.Active != 0 || !summary.Stopped {
		t.Fatalf("summary = %+v, want stopped with zero active", summary)
	}
	if host.running || host.stops != 1 {
		t.Fatalf("host running=%t stops=%d, want stopped once", host.running, host.stops)
	}
}

func TestSuperviseKeepsHostAfterInviteClaim(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "peer-1"); err != nil {
		t.Fatalf("ClaimInvite returned error: %v", err)
	}

	summary, err := svc.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise returned error: %v", err)
	}
	if summary.Active != 1 || summary.Stopped {
		t.Fatalf("summary = %+v, want claimed invite to remain active", summary)
	}
	if !host.running {
		t.Fatal("expected host to keep running for claimed invite")
	}
}

func TestSuperviseKeepsHostAfterClaimedInviteExpires(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "peer-1"); err != nil {
		t.Fatalf("ClaimInvite returned error: %v", err)
	}
	stored := repo.byID[inv.ID]
	stored.ExpiresAt = now.Add(-time.Minute)
	repo.byID[inv.ID] = stored

	summary, err := svc.Supervise(context.Background())
	if err != nil {
		t.Fatalf("Supervise returned error: %v", err)
	}
	if summary.Active != 1 || summary.Stopped {
		t.Fatalf("summary = %+v, want expired claimed invite to remain active", summary)
	}
	if !host.running {
		t.Fatal("expected host to keep running for expired claimed invite")
	}
}

func TestResolveInviteAllowsClaimedInviteAfterUnusedExpiry(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{invite: "mshost-iroh-direct-new"}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "peer-1"); err != nil {
		t.Fatalf("ClaimInvite returned error: %v", err)
	}
	stored := repo.byID[inv.ID]
	stored.ExpiresAt = now.Add(-time.Minute)
	stored.IrohInvite = "mshost-iroh-direct-old"
	repo.byID[inv.ID] = stored

	resolved, err := svc.ResolveInvite(context.Background(), inv.Token)
	if err != nil {
		t.Fatalf("ResolveInvite returned error: %v", err)
	}
	if resolved.IrohInvite != "mshost-iroh-direct-new" {
		t.Fatalf("resolved Iroh invite = %q, want refreshed host invite", resolved.IrohInvite)
	}
}

func TestResolveClaimedInviteForPeerRecoversConnectionCode(t *testing.T) {
	repo := newFakeInviteRepo()
	host := &fakeHost{invite: "mshost-iroh-direct-new"}
	svc := NewService(repo, host)
	now := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	inv, err := svc.CreateInvite(context.Background(), "account-1", CreateInviteRequest{ExpiresIn: time.Hour})
	if err != nil {
		t.Fatalf("CreateInvite returned error: %v", err)
	}
	if _, err := svc.ClaimInvite(context.Background(), inv.Token, "peer-1"); err != nil {
		t.Fatalf("ClaimInvite returned error: %v", err)
	}
	stored := repo.byID[inv.ID]
	stored.ExpiresAt = now.Add(-time.Minute)
	stored.IrohInvite = "mshost-iroh-direct-old"
	repo.byID[inv.ID] = stored

	resolved, err := svc.ResolveClaimedInviteForPeer(context.Background(), "peer-1")
	if err != nil {
		t.Fatalf("ResolveClaimedInviteForPeer returned error: %v", err)
	}
	if resolved.ConnectionCode != inv.Token {
		t.Fatalf("connection code = %q, want original short code", resolved.ConnectionCode)
	}
	if resolved.IrohInvite != "mshost-iroh-direct-new" {
		t.Fatalf("iroh invite = %q, want refreshed host invite", resolved.IrohInvite)
	}
}
