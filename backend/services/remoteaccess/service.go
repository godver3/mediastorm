package remoteaccess

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"novastream/internal/datastore"
	"novastream/models"
)

var (
	ErrInviteNotFound = errors.New("remote access invite not found")
	ErrInviteExpired  = errors.New("remote access invite has expired")
	ErrInviteUsed     = errors.New("remote access invite has already been used")
	ErrInviteRevoked  = errors.New("remote access invite has been revoked")
	ErrInvalidToken   = errors.New("invalid remote access invite token")
)

const (
	DefaultInviteExpiration = 24 * time.Hour
	MaxInviteExpiration     = 30 * 24 * time.Hour
)

type HostManager interface {
	Ensure(ctx context.Context) (string, error)
	Stop(ctx context.Context) error
	Status(ctx context.Context) models.RemoteAccessStatus
}

// RendezvousPublisher is an optional capability implemented by hosts that publish
// connection codes to the public DHT, letting clients resolve an invite without a
// reachable backend URL. When the host implements it, the service mirrors the set of
// active connection codes into the returned file path whenever invites change, and the
// host watches that file to keep a DHT record live for each code.
type RendezvousPublisher interface {
	RendezvousFilePath() string
}

type CreateInviteRequest struct {
	PeerName  string
	ExpiresIn time.Duration
}

type SyncSummary struct {
	Active  int
	Started bool
	Stopped bool
	Updated int
}

type Service struct {
	invites datastore.RemoteAccessInviteRepository
	host    HostManager
	now     func() time.Time
}

func NewService(invites datastore.RemoteAccessInviteRepository, host HostManager) *Service {
	return &Service{
		invites: invites,
		host:    host,
		now:     func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) Status(ctx context.Context) models.RemoteAccessStatus {
	status := models.RemoteAccessStatus{
		Enabled:  false,
		Running:  false,
		Provider: "iroh",
		State:    "not_configured",
	}
	if s.host != nil {
		status = s.host.Status(ctx)
	}
	invites, err := s.invites.List(ctx)
	if err == nil {
		status.ActiveInvites = countActiveInvites(invites, s.now())
	}
	return status
}

func (s *Service) CreateInvite(ctx context.Context, createdBy string, req CreateInviteRequest) (models.RemoteAccessInvite, error) {
	createdBy = strings.TrimSpace(createdBy)
	if createdBy == "" {
		return models.RemoteAccessInvite{}, fmt.Errorf("created by account id is required")
	}
	if s.host == nil {
		return models.RemoteAccessInvite{}, errors.New("iroh host manager not configured")
	}
	expiresIn := req.ExpiresIn
	if expiresIn <= 0 {
		expiresIn = DefaultInviteExpiration
	}
	if expiresIn > MaxInviteExpiration {
		expiresIn = MaxInviteExpiration
	}

	irohInvite, err := s.host.Ensure(ctx)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	token, err := generateToken()
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	now := s.now()
	inv := models.RemoteAccessInvite{
		ID:             uuid.NewString(),
		TokenHash:      HashInviteToken(token),
		ConnectionCode: token,
		IrohInvite:     irohInvite,
		CreatedBy:      createdBy,
		PeerName:       strings.TrimSpace(req.PeerName),
		ExpiresAt:      now.Add(expiresIn),
		CreatedAt:      now,
	}
	if err := s.invites.Create(ctx, &inv); err != nil {
		return models.RemoteAccessInvite{}, err
	}
	// Best-effort: the host re-reads the file on a timer and Supervise rewrites it
	// every minute, so a transient failure here self-heals.
	s.trySyncRendezvousCodes(ctx)
	inv.Token = token
	return inv, nil
}

func (s *Service) ListInvites(ctx context.Context) ([]models.RemoteAccessInvite, error) {
	invites, err := s.invites.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range invites {
		invites[i].Token = ""
	}
	return invites, nil
}

func (s *Service) RevokeInvite(ctx context.Context, id string) error {
	inv, err := s.invites.Get(ctx, strings.TrimSpace(id))
	if err != nil {
		return err
	}
	if inv == nil {
		return ErrInviteNotFound
	}
	now := s.now()
	inv.RevokedAt = &now
	if err := s.invites.Update(ctx, inv); err != nil {
		return err
	}
	_, err = s.Supervise(ctx)
	return err
}

// (Supervise rewrites the rendezvous file, so RevokeInvite relies on it via the call above.)

func (s *Service) ClaimInvite(ctx context.Context, token, peerID string) (models.RemoteAccessInvite, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return models.RemoteAccessInvite{}, ErrInvalidToken
	}
	tokenHash := HashInviteToken(token)
	now := s.now()
	inv, err := s.invites.ClaimByTokenHash(ctx, tokenHash, strings.TrimSpace(peerID), now)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	if inv == nil {
		existing, err := s.invites.GetByTokenHash(ctx, tokenHash)
		if err != nil {
			return models.RemoteAccessInvite{}, err
		}
		if existing == nil {
			return models.RemoteAccessInvite{}, ErrInviteNotFound
		}
		if existing.RevokedAt != nil {
			return models.RemoteAccessInvite{}, ErrInviteRevoked
		}
		if !now.Before(existing.ExpiresAt) {
			return models.RemoteAccessInvite{}, ErrInviteExpired
		}
		return models.RemoteAccessInvite{}, ErrInviteUsed
	}
	inv.Token = ""
	return *inv, nil
}

func (s *Service) ResolveInvite(ctx context.Context, token string) (models.RemoteAccessInvite, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return models.RemoteAccessInvite{}, ErrInvalidToken
	}
	inv, err := s.invites.GetByTokenHash(ctx, HashInviteToken(token))
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	if inv == nil {
		return models.RemoteAccessInvite{}, ErrInviteNotFound
	}
	now := s.now()
	if inv.RevokedAt != nil {
		return models.RemoteAccessInvite{}, ErrInviteRevoked
	}
	if inv.UsedAt == nil && !now.Before(inv.ExpiresAt) {
		return models.RemoteAccessInvite{}, ErrInviteExpired
	}
	if s.host == nil {
		return models.RemoteAccessInvite{}, errors.New("iroh host manager not configured")
	}
	irohInvite, err := s.host.Ensure(ctx)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	if inv.IrohInvite != irohInvite {
		inv.IrohInvite = irohInvite
		if err := s.invites.Update(ctx, inv); err != nil {
			return models.RemoteAccessInvite{}, err
		}
	}
	inv.Token = ""
	return *inv, nil
}

func (s *Service) ResolveClaimedInviteForPeer(ctx context.Context, peerID string) (models.RemoteAccessInvite, error) {
	peerID = strings.TrimSpace(peerID)
	if peerID == "" {
		return models.RemoteAccessInvite{}, ErrInvalidToken
	}
	invites, err := s.invites.List(ctx)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	var matched *models.RemoteAccessInvite
	for i := range invites {
		inv := &invites[i]
		if inv.RevokedAt != nil || inv.UsedAt == nil || inv.UsedByPeerID != peerID {
			continue
		}
		if matched == nil || inv.UsedAt.After(*matched.UsedAt) {
			matched = inv
		}
	}
	if matched == nil {
		return models.RemoteAccessInvite{}, ErrInviteNotFound
	}
	if s.host == nil {
		return models.RemoteAccessInvite{}, errors.New("iroh host manager not configured")
	}
	irohInvite, err := s.host.Ensure(ctx)
	if err != nil {
		return models.RemoteAccessInvite{}, err
	}
	if matched.IrohInvite != irohInvite {
		matched.IrohInvite = irohInvite
		if err := s.invites.Update(ctx, matched); err != nil {
			return models.RemoteAccessInvite{}, err
		}
	}
	matched.Token = ""
	return *matched, nil
}

func (s *Service) Supervise(ctx context.Context) (SyncSummary, error) {
	invites, err := s.invites.List(ctx)
	if err != nil {
		return SyncSummary{}, err
	}
	active := filterActiveInvites(invites, s.now())
	summary := SyncSummary{Active: len(active)}
	// Keep the host's rendezvous file in sync with the active set on every supervise
	// pass (startup + the 1-minute ticker + after revoke), covering both newly added
	// codes and emptying the file once the last invite lapses.
	s.trySyncRendezvousCodes(ctx)
	if len(active) == 0 {
		if s.host != nil {
			if status := s.host.Status(ctx); status.Running {
				if err := s.host.Stop(ctx); err != nil {
					return summary, err
				}
				summary.Stopped = true
			}
		}
		return summary, nil
	}
	if s.host == nil {
		return summary, errors.New("iroh host manager not configured")
	}
	wasRunning := s.host.Status(ctx).Running
	irohInvite, err := s.host.Ensure(ctx)
	if err != nil {
		return summary, err
	}
	summary.Started = !wasRunning
	for i := range active {
		if active[i].IrohInvite == irohInvite {
			continue
		}
		active[i].IrohInvite = irohInvite
		if err := s.invites.Update(ctx, &active[i]); err != nil {
			return summary, err
		}
		summary.Updated++
	}
	return summary, nil
}

// trySyncRendezvousCodes runs syncRendezvousCodes and logs (but does not propagate) any
// error, for call sites where the rendezvous file is a side effect rather than the result.
func (s *Service) trySyncRendezvousCodes(ctx context.Context) {
	if err := s.syncRendezvousCodes(ctx); err != nil {
		log.Printf("[remote-access] failed to sync rendezvous codes: %v", err)
	}
}

// syncRendezvousCodes mirrors the active connection codes into the host's rendezvous
// file (if the host supports DHT publishing). Best-effort: failures are non-fatal to the
// invite operation that triggered the sync, since the host re-reads the file on a timer.
func (s *Service) syncRendezvousCodes(ctx context.Context) error {
	publisher, ok := s.host.(RendezvousPublisher)
	if !ok {
		return nil
	}
	path := strings.TrimSpace(publisher.RendezvousFilePath())
	if path == "" {
		return nil
	}
	invites, err := s.invites.List(ctx)
	if err != nil {
		return err
	}
	active := filterActiveInvites(invites, s.now())

	var b strings.Builder
	b.WriteString("# strmr active connection codes — managed by remoteaccess.Service; do not edit\n")
	for i := range active {
		code := strings.TrimSpace(active[i].ConnectionCode)
		if code != "" {
			b.WriteString(code)
			b.WriteByte('\n')
		}
	}

	// Write atomically so the host never reads a half-written file.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// RendezvousFilePath returns the path the host watches for active codes, or "" if the
// configured host does not support DHT rendezvous publishing.
func (s *Service) RendezvousFilePath() string {
	if publisher, ok := s.host.(RendezvousPublisher); ok {
		if path := strings.TrimSpace(publisher.RendezvousFilePath()); path != "" {
			return filepath.Clean(path)
		}
	}
	return ""
}

func HashInviteToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func generateToken() (string, error) {
	max := big.NewInt(1_000_000_000_000)
	value, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("generate remote access invite token: %w", err)
	}
	digits := fmt.Sprintf("%012d", value.Int64())
	return "mshost-" + digits[:6] + "-" + digits[6:], nil
}

func countActiveInvites(invites []models.RemoteAccessInvite, now time.Time) int {
	return len(filterActiveInvites(invites, now))
}

func filterActiveInvites(invites []models.RemoteAccessInvite, now time.Time) []models.RemoteAccessInvite {
	active := make([]models.RemoteAccessInvite, 0, len(invites))
	for _, inv := range invites {
		if inv.IsActive(now) {
			active = append(active, inv)
		}
	}
	return active
}
