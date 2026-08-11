package peartube

import (
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
	defaultSourceMaxRangeBytes   = 4 * 1024 * 1024
	defaultSourceNonceCapacity   = 4096
	sourceCapabilityBytes        = 32
	sourceHashBufferBytes        = 256 * 1024
	sourceServeChunkBytes        = 32 * 1024
	maxSourceContentTypeBytes    = 128
	maxSourceJobIDBytes          = 128
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

// PreparedSource holds an open, hashed local file before its job ID is known.
// Issue transfers ownership to the registry; otherwise callers must Close it.
type PreparedSource struct {
	file        *os.File
	info        os.FileInfo
	length      int64
	sha256      string
	etag        string
	contentType string
	closed      bool
}

func (p *PreparedSource) Close() error {
	if p == nil || p.closed || p.file == nil {
		return nil
	}
	p.closed = true
	return p.file.Close()
}

type sourceGrant struct {
	file        *os.File
	info        os.FileInfo
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

// Prepare opens one regular file, hashes it without buffering the media, and
// retains that exact open file identity for a later grant.
func (r *SourceGrantRegistry) Prepare(filePath, contentType string) (*PreparedSource, error) {
	if r == nil {
		return nil, errors.New("source grants are unavailable")
	}
	r.mu.Lock()
	closed := r.closed
	authErr := r.authError
	r.mu.Unlock()
	if closed {
		return nil, errors.New("source grants are closed")
	}
	if authErr != nil {
		return nil, errors.New("source callback authentication is not configured")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, errors.New("open source file")
	}
	prepared := &PreparedSource{file: file}
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
	contentType = strings.TrimSpace(strings.ToLower(contentType))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if len(contentType) > maxSourceContentTypeBytes || !validHeaderText(contentType) {
		return nil, errors.New("source content type is invalid")
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	prepared.info = after
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

// Issue binds a prepared file to one job and transfers file ownership to the
// registry. Only the opaque token leaves this process in the companion request.
func (r *SourceGrantRegistry) Issue(prepared *PreparedSource, scope SourceGrantScope) (IssuedSourceGrant, error) {
	if r == nil || prepared == nil || prepared.file == nil || prepared.closed {
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
		file:        prepared.file,
		info:        prepared.info,
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
	prepared.file = nil
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

func (r *SourceGrantRegistry) pruneLocked(now time.Time) {
	for digest, grant := range r.grants {
		if now.Before(grant.expiresAt) {
			continue
		}
		r.revokeLocked(digest, grant)
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
	if grant.file != nil {
		_ = grant.file.Close()
		grant.file = nil
	}
}

func (r *SourceGrantRegistry) revokeLocked(digest [32]byte, grant *sourceGrant) {
	if grant == nil || grant.revoked {
		return
	}
	grant.revoked = true
	close(grant.revokedCh)
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

func (r *SourceGrantRegistry) acquire(capability string) ([32]byte, *sourceGrant, func()) {
	var zero [32]byte
	if !validSourceCapability(capability) {
		return zero, nil, func() {}
	}
	digest := sourceCapabilityDigest(capability)
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return zero, nil, func() {}
	}
	now := r.now()
	r.pruneLocked(now)
	grant := r.grants[digest]
	if grant == nil || grant.revoked || !now.Before(grant.expiresAt) {
		r.mu.Unlock()
		return zero, nil, func() {}
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
	}
}

// RevokeJob removes every outstanding capability for one terminal job.
func (r *SourceGrantRegistry) RevokeJob(jobID string) {
	if r == nil || jobID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for digest := range r.byJob[jobID] {
		r.revokeLocked(digest, r.grants[digest])
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
		r.revokeLocked(digest, grant)
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
		r.revokeLocked(digest, grant)
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

func sourceError(response http.ResponseWriter, status int) {
	message := "source request refused"
	if status == http.StatusGone {
		message = "source grant unavailable"
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
	digest, grant, release := r.acquire(capability)
	defer release()
	if grant == nil {
		sourceError(response, http.StatusGone)
		return
	}
	if grant.companionID != clientID || grant.jobID != jobID {
		sourceError(response, http.StatusForbidden)
		return
	}
	current, err := grant.file.Stat()
	if err != nil || !sameSourceIdentity(grant.info, current) {
		sourceError(response, http.StatusConflict)
		return
	}

	if request.Method == http.MethodDelete {
		if request.Header.Get("Range") != "" || request.Header.Get("If-Match") != "" {
			sourceError(response, http.StatusBadRequest)
			return
		}
		r.mu.Lock()
		r.revokeLocked(digest, grant)
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
	response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, grant.length))
	response.Header().Set("Content-Length", strconv.FormatInt(count, 10))
	response.WriteHeader(http.StatusPartialContent)
	reader := io.NewSectionReader(grant.file, start, count)
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
