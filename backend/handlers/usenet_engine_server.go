package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// UsenetEngineServer exposes this backend's built-in usenet engine to another
// mediastorm instance through a minimal SABnzbd-compatible API (addfile,
// queue, history). The importing instance streams the finished release from
// this backend's WebDAV surface, so every usenet provider account is used
// from a single host.
//
// It is deliberately not an admin setting: it is enabled only when the
// MEDIASTORM_ENGINE_SERVER_KEY environment variable holds a key of at least
// minEngineServerKeyLength characters. Hardening:
//   - the key is accepted only in the X-Api-Key header (never the query string,
//     which ends up in access logs) and compared in constant time against its
//     SHA-256 digest;
//   - requests that went through a reverse proxy (forwarding headers present)
//     are refused, so the API is reachable only directly (e.g. over Tailscale),
//     never through a public hostname;
//   - failed authentications are rate limited per remote address, so a stranger
//     cannot lock the legitimate client out;
//   - only NZB uploads are accepted (no addurl, so no server-side fetches), with
//     body, filename and job-count limits and a bounded import concurrency.
type UsenetEngineServer struct {
	keyDigest [32]byte
	importer  EngineImporter

	mu   sync.Mutex
	jobs map[string]*engineServerJob

	sem chan struct{}

	failMu sync.Mutex
	fails  map[string]*engineServerFailState // keyed by remote IP

	now           func() time.Time
	importTimeout time.Duration
}

// EngineImporter imports an NZB into the built-in engine and returns its
// WebDAV-relative storage path (a media file or release directory).
type EngineImporter func(ctx context.Context, fileName string, nzb []byte) (string, error)

// engineServerFailState tracks failed authentications for one remote address.
type engineServerFailState struct {
	window      time.Time
	count       int
	lockedUntil time.Time
}

const (
	EngineServerKeyEnv         = "MEDIASTORM_ENGINE_SERVER_KEY"
	minEngineServerKeyLength   = 32
	engineServerMaxNZBBytes    = 32 << 20
	engineServerMaxJobs        = 256
	engineServerMaxActive      = 32
	engineServerConcurrency    = 4
	engineServerJobRetention   = 12 * time.Hour
	engineServerFailLimit      = 10
	engineServerFailWindow     = time.Minute
	engineServerLockout        = 5 * time.Minute
	engineServerMaxFailEntries = 4096
	engineServerImportTimeout  = 15 * time.Minute
)

type engineServerJob struct {
	ID          string
	FileName    string
	Status      string // Queued | Downloading | Completed | Failed
	StoragePath string
	Error       string
	SizeBytes   int64
	CreatedAt   time.Time
	FinishedAt  time.Time
	cancel      context.CancelFunc
}

// NewUsenetEngineServer returns nil (feature disabled) when the key is empty,
// and an error when it is set but too short to be safe.
func NewUsenetEngineServer(key string, importer EngineImporter) (*UsenetEngineServer, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	if len(key) < minEngineServerKeyLength {
		return nil, fmt.Errorf("%s must be at least %d characters", EngineServerKeyEnv, minEngineServerKeyLength)
	}
	if importer == nil {
		return nil, errors.New("usenet engine server requires an importer")
	}
	return &UsenetEngineServer{
		keyDigest:     sha256.Sum256([]byte(key)),
		importer:      importer,
		jobs:          make(map[string]*engineServerJob),
		fails:         make(map[string]*engineServerFailState),
		sem:           make(chan struct{}, engineServerConcurrency),
		now:           time.Now,
		importTimeout: engineServerImportTimeout,
	}, nil
}

var engineServerProxyHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Real-Ip", "Forwarded", "Cf-Connecting-Ip", "True-Client-Ip"}

func (s *UsenetEngineServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	for _, header := range engineServerProxyHeaders {
		if r.Header.Get(header) != "" {
			engineServerError(w, http.StatusForbidden, "not available through a proxy")
			return
		}
	}
	remote := engineServerRemote(r)
	if s.locked(remote) {
		engineServerError(w, http.StatusTooManyRequests, "too many failed authentications")
		return
	}
	if !s.authorized(r) {
		s.recordFailure(remote)
		log.Printf("[engine-server] rejected unauthenticated request remote=%q mode=%q", r.RemoteAddr, r.URL.Query().Get("mode"))
		engineServerError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	switch r.URL.Query().Get("mode") {
	case "addfile":
		if r.Method != http.MethodPost {
			engineServerError(w, http.StatusMethodNotAllowed, "addfile requires POST")
			return
		}
		s.handleAddFile(w, r)
	case "queue":
		if r.URL.Query().Get("name") == "delete" {
			if r.Method != http.MethodPost {
				engineServerError(w, http.StatusMethodNotAllowed, "delete requires POST")
				return
			}
			s.handleDelete(w, r)
			return
		}
		s.writeSlots(w, "queue", func(j *engineServerJob) bool { return j.Status == "Queued" || j.Status == "Downloading" }, r)
	case "history":
		s.writeSlots(w, "history", func(j *engineServerJob) bool { return j.Status == "Completed" || j.Status == "Failed" }, r)
	case "version":
		writeEngineJSON(w, http.StatusOK, map[string]any{"version": "mediastorm-engine-1"})
	default:
		engineServerError(w, http.StatusBadRequest, "unsupported mode")
	}
}

func (s *UsenetEngineServer) authorized(r *http.Request) bool {
	provided := strings.TrimSpace(r.Header.Get("X-Api-Key"))
	if provided == "" {
		return false
	}
	digest := sha256.Sum256([]byte(provided))
	return subtle.ConstantTimeCompare(digest[:], s.keyDigest[:]) == 1
}

// engineServerRemote returns the TCP peer address (proxy headers are refused
// before this is used, so RemoteAddr is the real client).
func engineServerRemote(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *UsenetEngineServer) locked(remote string) bool {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	st := s.fails[remote]
	return st != nil && s.now().Before(st.lockedUntil)
}

func (s *UsenetEngineServer) recordFailure(remote string) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	now := s.now()
	if len(s.fails) >= engineServerMaxFailEntries {
		for k, v := range s.fails {
			if now.Sub(v.window) > engineServerFailWindow && !now.Before(v.lockedUntil) {
				delete(s.fails, k)
			}
		}
	}
	st := s.fails[remote]
	if st == nil {
		if len(s.fails) >= engineServerMaxFailEntries {
			return // table full of active entries; stop tracking new addresses
		}
		st = &engineServerFailState{}
		s.fails[remote] = st
	}
	if now.Sub(st.window) > engineServerFailWindow {
		st.window = now
		st.count = 0
	}
	st.count++
	if st.count >= engineServerFailLimit {
		st.lockedUntil = now.Add(engineServerLockout)
		st.count = 0
		log.Printf("[engine-server] locking out authentication from %s for %s after %d failures", remote, engineServerLockout, engineServerFailLimit)
	}
}

func (s *UsenetEngineServer) handleAddFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, engineServerMaxNZBBytes+(1<<20))
	if err := r.ParseMultipartForm(engineServerMaxNZBBytes); err != nil {
		engineServerError(w, http.StatusBadRequest, "invalid multipart upload")
		return
	}
	defer r.MultipartForm.RemoveAll()

	file, header, err := r.FormFile("nzbfile")
	if err != nil {
		engineServerError(w, http.StatusBadRequest, "missing nzbfile")
		return
	}
	defer file.Close()
	nzb, err := io.ReadAll(io.LimitReader(file, engineServerMaxNZBBytes+1))
	if err != nil || len(nzb) == 0 || len(nzb) > engineServerMaxNZBBytes {
		engineServerError(w, http.StatusBadRequest, "nzb missing or too large")
		return
	}
	if !looksLikeNZB(nzb) {
		engineServerError(w, http.StatusBadRequest, "payload is not an NZB")
		return
	}
	fileName := sanitizeEngineFileName(header.Filename)

	job, err := s.enqueue(fileName, int64(len(nzb)))
	if err != nil {
		writeEngineJSON(w, http.StatusOK, map[string]any{"status": false, "error": err.Error()})
		return
	}
	log.Printf("[engine-server] accepted NZB job=%s fileName=%q bytes=%d", job.ID, fileName, len(nzb))
	go s.run(job, nzb)
	writeEngineJSON(w, http.StatusOK, map[string]any{"status": true, "nzo_ids": []string{job.ID}})
}

func (s *UsenetEngineServer) enqueue(fileName string, size int64) (*engineServerJob, error) {
	id, err := newEngineJobID()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked()
	active := 0
	for _, job := range s.jobs {
		if job.Status == "Queued" || job.Status == "Downloading" {
			active++
		}
	}
	if active >= engineServerMaxActive {
		return nil, errors.New("engine queue is full")
	}
	if len(s.jobs) >= engineServerMaxJobs {
		// Finished jobs are kept for retention but clients rarely delete them;
		// evict the oldest finished one rather than refusing new work.
		var oldestID string
		var oldest time.Time
		for id, job := range s.jobs {
			if job.FinishedAt.IsZero() {
				continue
			}
			if oldestID == "" || job.FinishedAt.Before(oldest) {
				oldestID, oldest = id, job.FinishedAt
			}
		}
		if oldestID == "" {
			return nil, errors.New("engine queue is full")
		}
		delete(s.jobs, oldestID)
	}
	job := &engineServerJob{ID: id, FileName: fileName, Status: "Queued", SizeBytes: size, CreatedAt: s.now()}
	s.jobs[id] = job
	return job, nil
}

func (s *UsenetEngineServer) run(job *engineServerJob, nzb []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), s.importTimeout)
	defer cancel()
	s.mu.Lock()
	job.cancel = cancel
	s.mu.Unlock()

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		s.finish(job, "", ctx.Err())
		return
	}
	s.setStatus(job, "Downloading")
	storagePath, err := s.importer(ctx, job.FileName, nzb)
	s.finish(job, storagePath, err)
}

func (s *UsenetEngineServer) setStatus(job *engineServerJob, status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job.Status == "Queued" {
		job.Status = status
	}
}

func (s *UsenetEngineServer) finish(job *engineServerJob, storagePath string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job.FinishedAt = s.now()
	job.cancel = nil
	if err != nil {
		job.Status = "Failed"
		job.Error = err.Error()
		log.Printf("[engine-server] job=%s fileName=%q failed: %v", job.ID, job.FileName, err)
		return
	}
	job.Status = "Completed"
	job.StoragePath = storagePath
	log.Printf("[engine-server] job=%s fileName=%q completed storagePath=%q", job.ID, job.FileName, storagePath)
}

func (s *UsenetEngineServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	ids := requestedEngineJobIDs(r)
	s.mu.Lock()
	for id := range ids {
		if job := s.jobs[id]; job != nil {
			if job.cancel != nil {
				job.cancel()
			}
			delete(s.jobs, id)
		}
	}
	s.mu.Unlock()
	writeEngineJSON(w, http.StatusOK, map[string]any{"status": true})
}

func (s *UsenetEngineServer) writeSlots(w http.ResponseWriter, key string, include func(*engineServerJob) bool, r *http.Request) {
	ids := requestedEngineJobIDs(r)
	s.mu.Lock()
	s.pruneLocked()
	jobs := make([]*engineServerJob, 0, len(s.jobs))
	for _, job := range s.jobs {
		if len(ids) > 0 && !ids[job.ID] {
			continue
		}
		if include(job) {
			jobs = append(jobs, job)
		}
	}
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].CreatedAt.Before(jobs[j].CreatedAt) })
	slots := make([]map[string]any, 0, len(jobs))
	for _, job := range jobs {
		percentage := "0"
		if job.Status == "Completed" {
			percentage = "100"
		}
		slots = append(slots, map[string]any{
			"nzo_id":       job.ID,
			"status":       job.Status,
			"filename":     job.FileName,
			"nzb_name":     job.FileName,
			"percentage":   percentage,
			"bytes":        job.SizeBytes,
			"storage_path": job.StoragePath,
			"fail_message": job.Error,
		})
	}
	s.mu.Unlock()
	writeEngineJSON(w, http.StatusOK, map[string]any{key: map[string]any{"slots": slots}})
}

func (s *UsenetEngineServer) pruneLocked() {
	cutoff := s.now().Add(-engineServerJobRetention)
	for id, job := range s.jobs {
		if !job.FinishedAt.IsZero() && job.FinishedAt.Before(cutoff) {
			delete(s.jobs, id)
		}
	}
}

func requestedEngineJobIDs(r *http.Request) map[string]bool {
	ids := map[string]bool{}
	q := r.URL.Query()
	for _, raw := range append(q["nzo_ids"], q["value"]...) {
		for _, id := range strings.Split(raw, ",") {
			if id = strings.TrimSpace(id); id != "" && len(id) <= 64 {
				ids[id] = true
			}
		}
	}
	return ids
}

func newEngineJobID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return "ms_" + hex.EncodeToString(buf), nil
}

func looksLikeNZB(data []byte) bool {
	head := data
	if len(head) > 4096 {
		head = head[:4096]
	}
	return bytes.Contains(bytes.ToLower(head), []byte("<nzb"))
}

// sanitizeEngineFileName reduces an uploaded file name to a safe base name: no
// directories, no control characters, bounded length, .nzb extension.
func sanitizeEngineFileName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(strings.TrimSpace(name))
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '/' {
			return -1
		}
		return r
	}, name)
	name = strings.Trim(name, ". ")
	if name == "" {
		name = "download"
	}
	if !strings.HasSuffix(strings.ToLower(name), ".nzb") {
		name += ".nzb"
	}
	if runes := []rune(name); len(runes) > 200 {
		name = string(runes[:196]) + ".nzb"
	}
	return name
}

func engineServerError(w http.ResponseWriter, status int, message string) {
	writeEngineJSON(w, status, map[string]any{"status": false, "error": message})
}

func writeEngineJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
