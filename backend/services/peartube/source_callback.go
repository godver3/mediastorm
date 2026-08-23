package peartube

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	CompanionCallbackClientEnv     = "PEARTUBE_COMPANION_CALLBACK_CLIENT"
	DefaultCompanionCallbackClient = "peartube-companion"

	sourceCallbackPrefix         = "/internal/peartube/v2/sources/"
	SourceCallbackRoute          = sourceCallbackPrefix + "{sourceCapability:[A-Za-z0-9_-]{43}}"
	sourceCapabilityDigestDomain = "peartube.mediastorm.source-capability.v1"
	defaultSourceGrantTTL        = 30 * time.Minute
	defaultSourceGrantCapacity   = 128
	// The largest range the callback will serve, and the ceiling a companion's
	// own request size has to stay under: an over-large Range is refused with
	// 416 rather than trimmed, so this must never drop below the relay's
	// MAX_SOURCE_CHUNK_BYTES (packages/cli/src/constants.js), which is the same
	// 16 MiB. The response is streamed in sourceServeChunkBytes pieces, so the
	// figure costs MediaStorm no resident memory; it is the relay that holds a
	// range whole, and the relay that picked the size.
	defaultSourceMaxRangeBytes = 16 * 1024 * 1024
	defaultSourceNonceCapacity = 4096
	sourceCapabilityBytes      = 32
	sourceHashBufferBytes      = 256 * 1024
	sourceServeChunkBytes      = 32 * 1024
	maxSourceContentTypeBytes  = 128
	maxSourceJobIDBytes        = 128
)

// SourceGrantOptions controls the bounded in-memory source registry. Callback
// credentials are static process configuration and are never copied into a
// grant, response, or durable record.
type SourceGrantOptions struct {
	Now             func() time.Time
	Random          io.Reader
	TTL             time.Duration
	MaxEntries      int
	MaxRangeBytes   int64
	MaxClockSkew    time.Duration
	MaxNonces       int
	CompanionID     string
	CompanionSecret [32]byte
}

// SourceGrantScope binds one opaque grant to one authenticated companion job.
type SourceGrantScope struct {
	CompanionID string
	JobID       string
	ExpiresAt   time.Time
	PolicyEpoch uint64
}

// IssuedSourceGrant is the only handoff material the contribution client needs.
// File identity and the open local file remain inside the in-memory registry.
type IssuedSourceGrant struct {
	Capability  string
	Length      int64
	SHA256      string
	ETag        string
	ContentType string
	ExpiresAt   time.Time
}

// errSourceDrift marks a backing whose bytes are no longer the bytes the grant
// was prepared from and which cannot be recovered by re-resolving: a local file
// replaced under an already-open descriptor.
var errSourceDrift = errors.New("source changed under the grant")

// ErrSourceUnavailable marks a range read that failed for a reason a later
// attempt can recover from — an upstream address being re-resolved, a provider
// throttling, a dropped connection. The callback answers 503 so the companion
// keeps the progress it has already confirmed and asks again.
var ErrSourceUnavailable = errors.New("source range is temporarily unavailable")

// ErrSourceGone marks a backing whose content no longer exists, or whose
// upstream stopped describing the same bytes. The callback answers 410 so the
// companion stops rather than splicing two different sources together.
var ErrSourceGone = errors.New("source content is gone")

// sourceBacking supplies the bytes behind one grant. It is the seam that lets a
// grant be backed by something other than an open local file: a file backing
// pins one exact open descriptor, while a remote backing re-resolves an
// expiring upstream address underneath every range it serves.
type sourceBacking interface {
	// verify reports whether the backing still names the bytes it was prepared
	// from. It runs before every request, so it must not block on an upstream.
	verify() error
	// open returns exactly count bytes starting at start. It is called before
	// any response status is written, so a range that cannot be served becomes
	// a status rather than a truncated body.
	open(ctx context.Context, start, count int64) (io.ReadCloser, error)
	close() error
}

// fileBacking is the original local-file source: one open descriptor whose
// exact identity is pinned at Prepare and re-checked on every request.
type fileBacking struct {
	file *os.File
	info os.FileInfo
}

func (b *fileBacking) verify() error {
	if b.file == nil {
		return errSourceDrift
	}
	current, err := b.file.Stat()
	if err != nil || !sameSourceIdentity(b.info, current) {
		return errSourceDrift
	}
	return nil
}

func (b *fileBacking) open(_ context.Context, start, count int64) (io.ReadCloser, error) {
	if b.file == nil {
		return nil, errSourceDrift
	}
	return io.NopCloser(io.NewSectionReader(b.file, start, count)), nil
}

func (b *fileBacking) close() error {
	if b.file == nil {
		return nil
	}
	file := b.file
	b.file = nil
	return file.Close()
}

// sourceBackingStatus maps a backing failure onto the callback status the
// companion keys its retry decision on. An unclassified failure is retryable:
// the companion keeps its confirmed progress and asks again, which is the safe
// direction, because a genuinely permanent fault still ends at the grant's own
// expiry instead of destroying hours of confirmed transfer.
func sourceBackingStatus(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, errSourceDrift):
		return http.StatusConflict
	case errors.Is(err, ErrSourceGone):
		return http.StatusGone
	default:
		return http.StatusServiceUnavailable
	}
}

// PreparedSource holds a prepared, identified source before its job ID is
// known. Issue transfers ownership to the registry; otherwise callers must
// Close it.
type PreparedSource struct {
	backing     sourceBacking
	length      int64
	sha256      string
	etag        string
	contentType string
	closed      bool
}

func (p *PreparedSource) Close() error {
	if p == nil || p.closed || p.backing == nil {
		return nil
	}
	p.closed = true
	return p.backing.close()
}

// facts are the source facts an ingest submission states before any capability
// exists. A remote source leaves SHA256 empty: see PrepareRemote.
func (p *PreparedSource) facts() IssuedSourceGrant {
	return IssuedSourceGrant{
		Length:      p.length,
		SHA256:      p.sha256,
		ETag:        p.etag,
		ContentType: p.contentType,
	}
}

type sourceGrant struct {
	backing     sourceBacking
	companionID string
	jobID       string
	length      int64
	sha256      string
	etag        string
	contentType string
	expiresAt   time.Time
	active      int
	revoked     bool
	revokedCh   chan struct{}
}

type sourceNonce struct {
	timestamp time.Time
}

// SourceGrantRegistry authenticates and serves one-job local source grants.
// The primary map key is only the domain-separated SHA-256 capability digest.
type SourceGrantRegistry struct {
	mu sync.Mutex

	grants map[[32]byte]*sourceGrant
	byJob  map[string]map[[32]byte]struct{}
	nonces map[string]sourceNonce
	// revoked remembers the capabilities that ended terminally, so a job that is
	// really over answers 410 while everything else the registry has simply
	// forgotten — a lapsed grant, or every grant this process held before a
	// restart — answers the re-attachable 401. Getting that the wrong way round
	// is what makes a companion destroy an unfinished transfer.
	revoked map[[32]byte]time.Time

	now           func() time.Time
	random        io.Reader
	ttl           time.Duration
	maxEntries    int
	maxRangeBytes int64
	maxClockSkew  time.Duration
	maxNonces     int
	companionID   string
	secret        [32]byte
	policyEpoch   uint64
	authError     error
	closed        bool
}

// NewSourceGrantRegistry builds an authenticated bounded registry.
func NewSourceGrantRegistry(options SourceGrantOptions) (*SourceGrantRegistry, error) {
	now := options.Now
	if now == nil {
		now = time.Now
	}
	randomSource := options.Random
	if randomSource == nil {
		randomSource = rand.Reader
	}
	ttl := options.TTL
	if ttl == 0 {
		ttl = defaultSourceGrantTTL
	}
	capacity := options.MaxEntries
	if capacity == 0 {
		capacity = defaultSourceGrantCapacity
	}
	maxRange := options.MaxRangeBytes
	if maxRange == 0 {
		maxRange = defaultSourceMaxRangeBytes
	}
	clockSkew := options.MaxClockSkew
	if clockSkew == 0 {
		clockSkew = 30 * time.Second
	}
	maxNonces := options.MaxNonces
	if maxNonces == 0 {
		maxNonces = defaultSourceNonceCapacity
	}
	if ttl <= 0 || capacity <= 0 || maxRange <= 0 || clockSkew <= 0 || maxNonces <= 0 {
		return nil, errors.New("source grant bounds must be positive")
	}
	if !validCompanionCallbackID(options.CompanionID) {
		return nil, errors.New("source callback companion identity is invalid")
	}
	return &SourceGrantRegistry{
		grants:        make(map[[32]byte]*sourceGrant),
		byJob:         make(map[string]map[[32]byte]struct{}),
		nonces:        make(map[string]sourceNonce),
		revoked:       make(map[[32]byte]time.Time),
		now:           now,
		random:        randomSource,
		ttl:           ttl,
		maxEntries:    capacity,
		maxRangeBytes: maxRange,
		maxClockSkew:  clockSkew,
		maxNonces:     maxNonces,
		companionID:   options.CompanionID,
		secret:        options.CompanionSecret,
	}, nil
}

// NewSourceGrantRegistryFromEnv uses the reciprocal callback identity and the
// existing companion shared secret. Invalid optional configuration keeps the
// route fail-closed without preventing MediaStorm startup.
func NewSourceGrantRegistryFromEnv() *SourceGrantRegistry {
	clientID, secret, authErr := sourceCallbackCredentials(os.Getenv)
	registry, err := NewSourceGrantRegistry(SourceGrantOptions{
		CompanionID:     clientID,
		CompanionSecret: secret,
	})
	if err != nil {
		registry, _ = NewSourceGrantRegistry(SourceGrantOptions{
			CompanionID: DefaultCompanionCallbackClient,
		})
		authErr = err
	}
	registry.authError = authErr
	return registry
}

func sourceCallbackCredentials(getenv func(string) string) (string, [32]byte, error) {
	var key [32]byte
	clientID := getenv(CompanionCallbackClientEnv)
	if clientID == "" {
		clientID = DefaultCompanionCallbackClient
	}
	if !validCompanionCallbackID(clientID) {
		return DefaultCompanionCallbackClient, key, fmt.Errorf("%s must be 1 to 128 identifier characters", CompanionCallbackClientEnv)
	}
	secret := getenv(CompanionSharedSecretEnv)
	if len(secret) != 64 || secret != strings.ToLower(secret) {
		return clientID, key, fmt.Errorf("%s must be 64 lowercase hexadecimal characters", CompanionSharedSecretEnv)
	}
	if _, err := hex.Decode(key[:], []byte(secret)); err != nil {
		return clientID, [32]byte{}, fmt.Errorf("%s must be 64 lowercase hexadecimal characters", CompanionSharedSecretEnv)
	}
	return clientID, key, nil
}

func validCompanionCallbackID(value string) bool {
	if len(value) < 1 || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		character := value[i]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// prepareGuard reports whether the registry can hand out a new prepared source
// at all. Prepare and PrepareRemote share it, so a closed registry or an
// unconfigured callback identity fails closed for every kind of source.
func (r *SourceGrantRegistry) prepareGuard() error {
	if r == nil {
		return errors.New("source grants are unavailable")
	}
	r.mu.Lock()
	closed := r.closed
	authErr := r.authError
	r.mu.Unlock()
	if closed {
		return errors.New("source grants are closed")
	}
	if authErr != nil {
		return errors.New("source callback authentication is not configured")
	}
	return nil
}

func normalizeSourceContentType(contentType string) (string, error) {
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if len(contentType) > maxSourceContentTypeBytes || !validHeaderText(contentType) {
		return "", errors.New("source content type is invalid")
	}
	return contentType, nil
}

// Prepare opens one regular file, hashes it without buffering the media, and
// retains that exact open file identity for a later grant.
func (r *SourceGrantRegistry) Prepare(filePath, contentType string) (*PreparedSource, error) {
	if err := r.prepareGuard(); err != nil {
		return nil, err
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, errors.New("open source file")
	}
	backing := &fileBacking{file: file}
	prepared := &PreparedSource{backing: backing}
	success := false
	defer func() {
		if !success {
			_ = prepared.Close()
		}
	}()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() <= 0 {
		return nil, errors.New("source must be a non-empty regular file")
	}
	hasher := sha256.New()
	buffer := make([]byte, sourceHashBufferBytes)
	defer func() {
		for index := range buffer {
			buffer[index] = 0
		}
	}()
	if _, err = io.CopyBuffer(hasher, file, buffer); err != nil {
		return nil, errors.New("hash source file")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, errors.New("rewind source file")
	}
	after, err := file.Stat()
	if err != nil || !sameSourceIdentity(before, after) {
		return nil, errors.New("source changed while preparing grant")
	}
	contentType, err = normalizeSourceContentType(contentType)
	if err != nil {
		return nil, err
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	backing.info = after
	prepared.length = after.Size()
	prepared.sha256 = digest
	prepared.etag = `"sha256-` + digest + `"`
	prepared.contentType = contentType
	success = true
	return prepared, nil
}

func sameSourceIdentity(expected, actual os.FileInfo) bool {
	return expected != nil && actual != nil &&
		os.SameFile(expected, actual) &&
		expected.Mode() == actual.Mode() &&
		expected.Size() == actual.Size() &&
		expected.ModTime().Equal(actual.ModTime())
}

// Issue binds a prepared source to one job and transfers ownership of its
// backing to the registry. Only the opaque token leaves this process in the
// companion request.
func (r *SourceGrantRegistry) Issue(prepared *PreparedSource, scope SourceGrantScope) (IssuedSourceGrant, error) {
	if r == nil || prepared == nil || prepared.backing == nil || prepared.closed {
		return IssuedSourceGrant{}, errors.New("prepared source is invalid")
	}
	if scope.CompanionID != r.companionID || !validSourceJobID(scope.JobID) {
		return IssuedSourceGrant{}, errors.New("source grant scope is invalid")
	}
	now := r.now()
	expiresAt := scope.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = now.Add(r.ttl)
	}
	if !expiresAt.After(now) || expiresAt.After(now.Add(r.ttl)) {
		return IssuedSourceGrant{}, errors.New("source grant expiry is invalid")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return IssuedSourceGrant{}, errors.New("source grants are closed")
	}
	if scope.PolicyEpoch != r.policyEpoch {
		return IssuedSourceGrant{}, errors.New("source grant policy changed")
	}
	r.pruneLocked(now)
	if len(r.grants) >= r.maxEntries {
		return IssuedSourceGrant{}, errors.New("source grant capacity exhausted")
	}
	var capability string
	var digest [32]byte
	for range 8 {
		randomBytes := make([]byte, sourceCapabilityBytes)
		_, randomErr := io.ReadFull(r.random, randomBytes)
		if randomErr == nil {
			capability = base64.RawURLEncoding.EncodeToString(randomBytes)
		}
		for index := range randomBytes {
			randomBytes[index] = 0
		}
		if randomErr != nil {
			return IssuedSourceGrant{}, errors.New("create source capability")
		}
		digest = sourceCapabilityDigest(capability)
		if _, exists := r.grants[digest]; !exists {
			break
		}
		capability = ""
	}
	if capability == "" {
		return IssuedSourceGrant{}, errors.New("source capability collision limit reached")
	}
	grant := &sourceGrant{
		backing:     prepared.backing,
		companionID: scope.CompanionID,
		jobID:       scope.JobID,
		length:      prepared.length,
		sha256:      prepared.sha256,
		etag:        prepared.etag,
		contentType: prepared.contentType,
		expiresAt:   expiresAt,
		revokedCh:   make(chan struct{}),
	}
	r.grants[digest] = grant
	if r.byJob[scope.JobID] == nil {
		r.byJob[scope.JobID] = make(map[[32]byte]struct{})
	}
	r.byJob[scope.JobID][digest] = struct{}{}
	prepared.closed = true
	prepared.backing = nil
	return IssuedSourceGrant{
		Capability:  capability,
		Length:      grant.length,
		SHA256:      grant.sha256,
		ETag:        grant.etag,
		ContentType: grant.contentType,
		ExpiresAt:   expiresAt,
	}, nil
}

func validSourceJobID(value string) bool {
	if len(value) < 1 || len(value) > maxSourceJobIDBytes || !validHeaderText(value) {
		return false
	}
	for i := range len(value) {
		character := value[i]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func sourceCapabilityDigest(capability string) [32]byte {
	return sha256.Sum256([]byte(sourceCapabilityDigestDomain + "\x00" + capability))
}

func validSourceCapability(capability string) bool {
	if len(capability) != 43 {
		return false
	}
	for i := range len(capability) {
		character := capability[i]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

// revokedSourceGrace is how long a terminally revoked capability keeps saying so
// before it becomes indistinguishable from one the registry never held. It only
// has to outlive requests already in flight for a job the companion has been
// told is finished.
const revokedSourceGrace = 10 * time.Minute

// sourceRevocationReason separates a grant that ended because its job did from
// one that merely ran out of time.
type sourceRevocationReason int

const (
	// sourceRevokedTerminal is a finished job or a withdrawn consent: stop.
	sourceRevokedTerminal sourceRevocationReason = iota
	// sourceRevokedLapsed is a grant that outlived its TTL mid-transfer: the
	// companion should re-attach, not give up.
	sourceRevokedLapsed
)

func (r *SourceGrantRegistry) pruneLocked(now time.Time) {
	for digest, grant := range r.grants {
		if now.Before(grant.expiresAt) {
			continue
		}
		r.revokeLocked(digest, grant, sourceRevokedLapsed)
	}
	for digest, at := range r.revoked {
		if at.Add(revokedSourceGrace).Before(now) {
			delete(r.revoked, digest)
		}
	}
	if len(r.revoked) > r.maxEntries {
		// Falling back to 401 for a forgotten terminal job costs one wasted
		// re-attach; growing this map without bound costs the process.
		clear(r.revoked)
	}
	minimumNonceTime := now.Add(-r.maxClockSkew)
	for key, nonce := range r.nonces {
		if nonce.timestamp.Before(minimumNonceTime) {
			delete(r.nonces, key)
		}
	}
}

func (r *SourceGrantRegistry) retireLocked(digest [32]byte, grant *sourceGrant) {
	if current := r.grants[digest]; current == grant {
		delete(r.grants, digest)
	}
	if grant.backing != nil {
		_ = grant.backing.close()
		grant.backing = nil
	}
}

func (r *SourceGrantRegistry) revokeLocked(digest [32]byte, grant *sourceGrant, reason sourceRevocationReason) {
	if grant == nil || grant.revoked {
		return
	}
	grant.revoked = true
	close(grant.revokedCh)
	if reason == sourceRevokedTerminal {
		r.revoked[digest] = r.now()
	}
	if jobs := r.byJob[grant.jobID]; jobs != nil {
		delete(jobs, digest)
		if len(jobs) == 0 {
			delete(r.byJob, grant.jobID)
		}
	}
	if grant.active == 0 {
		r.retireLocked(digest, grant)
	}
}

// acquire pins a live grant, or reports the status its absence deserves.
//
// Only a terminally revoked capability is 410. A grant that lapsed, and a
// capability this registry simply does not hold — every capability it held
// before a restart, for instance — is 401: the companion's transfer is
// unfinished rather than unwanted, and it must be able to obtain a fresh
// capability for the same job instead of discarding hours of confirmed
// progress.
func (r *SourceGrantRegistry) acquire(capability string) ([32]byte, *sourceGrant, func(), int) {
	var zero [32]byte
	noRelease := func() {}
	if !validSourceCapability(capability) {
		return zero, nil, noRelease, http.StatusGone
	}
	digest := sourceCapabilityDigest(capability)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return zero, nil, noRelease, http.StatusGone
	}
	now := r.now()
	r.pruneLocked(now)
	grant := r.grants[digest]
	if grant == nil || grant.revoked || !now.Before(grant.expiresAt) {
		_, terminal := r.revoked[digest]
		r.mu.Unlock()
		if terminal {
			return zero, nil, noRelease, http.StatusGone
		}
		return zero, nil, noRelease, http.StatusUnauthorized
	}
	grant.active++
	r.mu.Unlock()
	return digest, grant, func() {
		r.mu.Lock()
		grant.active--
		if grant.active == 0 && grant.revoked {
			r.retireLocked(digest, grant)
		}
		r.mu.Unlock()
	}, 0
}

// RevokeJob removes every outstanding capability for one terminal job.
func (r *SourceGrantRegistry) RevokeJob(jobID string) {
	if r == nil || jobID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for digest := range r.byJob[jobID] {
		r.revokeLocked(digest, r.grants[digest], sourceRevokedTerminal)
	}
}

// RevokeAll stops every live source callback without closing the registry.
// PolicyEpoch returns the generation that a caller must bind to a new source
// grant. RevokeAll advances it atomically, so a prepared stale handoff cannot
// appear after a consent downgrade raced with file preparation.
func (r *SourceGrantRegistry) PolicyEpoch() uint64 {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.policyEpoch
}

// Policy downgrade uses this before asynchronous companion cancellation so no
// more bytes can cross the boundary after consent is withdrawn.
func (r *SourceGrantRegistry) RevokeAll() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.policyEpoch++
	for digest, grant := range r.grants {
		r.revokeLocked(digest, grant, sourceRevokedTerminal)
	}
}

// Close revokes every grant, closes retained files, and zeroes the live secret.
func (r *SourceGrantRegistry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	for digest, grant := range r.grants {
		r.revokeLocked(digest, grant, sourceRevokedTerminal)
	}
	clear(r.nonces)
	for index := range r.secret {
		r.secret[index] = 0
	}
}

func singleSourceHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	returnValue := ""
	if len(values) == 1 {
		returnValue = values[0]
	}
	return returnValue, len(values) == 1 && returnValue != ""
}

func validSourceNonce(value string) bool {
	if len(value) < 8 || len(value) > 128 {
		return false
	}
	for i := range len(value) {
		character := value[i]
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func (r *SourceGrantRegistry) authenticate(request *http.Request) (string, int) {
	if r.authError != nil {
		return "", http.StatusServiceUnavailable
	}
	if request.ContentLength > 0 || len(request.TransferEncoding) != 0 {
		return "", http.StatusBadRequest
	}
	clientID, okClient := singleSourceHeader(request.Header, "X-PearTube-Client")
	timestampText, okTimestamp := singleSourceHeader(request.Header, "X-PearTube-Timestamp")
	nonce, okNonce := singleSourceHeader(request.Header, "X-PearTube-Nonce")
	macText, okMAC := singleSourceHeader(request.Header, "X-PearTube-MAC")
	if !okClient || !okTimestamp || !okNonce || !okMAC {
		return "", http.StatusUnauthorized
	}
	if clientID != r.companionID || !validSourceNonce(nonce) || len(macText) != 64 {
		return "", http.StatusUnauthorized
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil || strconv.FormatInt(timestamp, 10) != timestampText {
		return "", http.StatusUnauthorized
	}
	now := r.now()
	requestTime := time.UnixMilli(timestamp)
	if requestTime.Before(now.Add(-r.maxClockSkew)) || requestTime.After(now.Add(r.maxClockSkew)) {
		return "", http.StatusUnauthorized
	}
	providedMAC, err := hex.DecodeString(macText)
	if err != nil || len(providedMAC) != 32 {
		return "", http.StatusUnauthorized
	}
	target := request.URL.EscapedPath()
	if query := encodeCompanionQuery(request.URL.Query()); query != "" {
		target += "?" + query
	}
	expectedText := companionRequestMACWithBodyHash(request.Method, target, timestampText, nonce, emptyCompanionBodyHash, r.secret[:])
	expectedMAC, _ := hex.DecodeString(expectedText)
	if !hmac.Equal(providedMAC, expectedMAC) {
		return "", http.StatusUnauthorized
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.pruneLocked(now)
	nonceKey := clientID + "\x00" + nonce
	if _, exists := r.nonces[nonceKey]; exists {
		return "", http.StatusConflict
	}
	if len(r.nonces) >= r.maxNonces {
		return "", http.StatusServiceUnavailable
	}
	r.nonces[nonceKey] = sourceNonce{timestamp: requestTime}
	return clientID, 0
}

func parseSourceRange(value string, length, maximum int64) (int64, int64, bool) {
	if !strings.HasPrefix(value, "bytes=") || strings.Contains(value, ",") {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimPrefix(value, "bytes="), "-")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, 0, false
	}
	start, startErr := strconv.ParseInt(parts[0], 10, 64)
	end, endErr := strconv.ParseInt(parts[1], 10, 64)
	if startErr != nil || endErr != nil || start < 0 || end < start || end >= length || end-start+1 > maximum {
		return 0, 0, false
	}
	if strconv.FormatInt(start, 10) != parts[0] || strconv.FormatInt(end, 10) != parts[1] {
		return 0, 0, false
	}
	return start, end, true
}

// sourceRetryAfterSeconds paces a companion around a transient upstream fault.
// It is deliberately short: the companion is resuming a transfer, not polling a
// queue, and the grant it holds has a bounded life.
const sourceRetryAfterSeconds = 5

func sourceError(response http.ResponseWriter, status int) {
	message := "source request refused"
	switch status {
	case http.StatusGone:
		message = "source grant unavailable"
	case http.StatusUnauthorized:
		// A lapsed grant is not a withdrawn one. Saying so distinctly is what
		// lets a companion re-attach to the same job instead of destroying an
		// unfinished transfer it has already confirmed most of.
		message = "source grant expired"
	case http.StatusServiceUnavailable:
		response.Header().Set("Retry-After", strconv.Itoa(sourceRetryAfterSeconds))
		message = "source range temporarily unavailable"
	}
	http.Error(response, message, status)
}

// ServeHTTP serves the exact internal HEAD, bounded GET range, and terminal
// DELETE contract. Authentication is checked before grant lookup.
func (r *SourceGrantRegistry) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	clientID, authStatus := r.authenticate(request)
	if authStatus != 0 {
		sourceError(response, authStatus)
		return
	}
	if request.URL.RawQuery != "" {
		sourceError(response, http.StatusBadRequest)
		return
	}
	capability := strings.TrimPrefix(request.URL.EscapedPath(), sourceCallbackPrefix)
	if sourceCallbackPrefix+capability != request.URL.EscapedPath() || !validSourceCapability(capability) {
		sourceError(response, http.StatusGone)
		return
	}
	jobID, hasJobID := singleSourceHeader(request.Header, "X-PearTube-Job-ID")
	if !hasJobID || !validSourceJobID(jobID) {
		sourceError(response, http.StatusForbidden)
		return
	}
	digest, grant, release, acquireStatus := r.acquire(capability)
	defer release()
	if grant == nil {
		sourceError(response, acquireStatus)
		return
	}
	if grant.companionID != clientID || grant.jobID != jobID {
		sourceError(response, http.StatusForbidden)
		return
	}
	if status := sourceBackingStatus(grant.backing.verify()); status != 0 {
		sourceError(response, status)
		return
	}

	if request.Method == http.MethodDelete {
		if request.Header.Get("Range") != "" || request.Header.Get("If-Match") != "" {
			sourceError(response, http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.revokeLocked(digest, grant, sourceRevokedTerminal)
		r.mu.Unlock()
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if ifMatch, ok := singleSourceHeader(request.Header, "If-Match"); !ok || ifMatch != grant.etag {
		sourceError(response, http.StatusPreconditionFailed)
		return
	}
	response.Header().Set("Accept-Ranges", "bytes")
	response.Header().Set("ETag", grant.etag)
	response.Header().Set("Content-Type", grant.contentType)
	response.Header().Set("Content-Encoding", "identity")
	if request.Method == http.MethodHead {
		if request.Header.Get("Range") != "" {
			sourceError(response, http.StatusRequestedRangeNotSatisfiable)
			return
		}
		response.Header().Set("Content-Length", strconv.FormatInt(grant.length, 10))
		response.WriteHeader(http.StatusOK)
		return
	}
	if request.Method != http.MethodGet {
		response.Header().Set("Allow", "HEAD, GET, DELETE")
		sourceError(response, http.StatusMethodNotAllowed)
		return
	}
	start, end, ok := parseSourceRange(request.Header.Get("Range"), grant.length, r.maxRangeBytes)
	if !ok {
		response.Header().Set("Content-Range", "bytes */"+strconv.FormatInt(grant.length, 10))
		sourceError(response, http.StatusRequestedRangeNotSatisfiable)
		return
	}
	count := end - start + 1
	// The range is opened before any status is written, so an upstream that is
	// mid-re-resolution becomes a clean retryable status instead of a 206 with a
	// short body, which no companion can tell from real content.
	reader, openErr := grant.backing.open(request.Context(), start, count)
	if openErr != nil {
		sourceError(response, sourceBackingStatus(openErr))
		return
	}
	defer reader.Close()
	response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, grant.length))
	response.Header().Set("Content-Length", strconv.FormatInt(count, 10))
	response.WriteHeader(http.StatusPartialContent)
	buffer := make([]byte, sourceServeChunkBytes)
	defer func() {
		for index := range buffer {
			buffer[index] = 0
		}
	}()
	remaining := count
	for remaining > 0 {
		select {
		case <-grant.revokedCh:
			return
		default:
		}
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		read, readErr := reader.Read(buffer[:chunk])
		if read > 0 {
			select {
			case <-grant.revokedCh:
				return
			default:
			}
			written, writeErr := response.Write(buffer[:read])
			remaining -= int64(written)
			if writeErr != nil || written != read {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}
