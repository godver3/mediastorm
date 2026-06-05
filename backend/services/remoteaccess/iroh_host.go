package remoteaccess

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"novastream/models"
)

const (
	defaultIrohBind   = "0.0.0.0:0"
	defaultIrohOrigin = "http://127.0.0.1:7777"
)

// defaultRendezvousFileName is the file (under the OS temp dir) the host watches for the
// set of active connection codes to publish to the DHT. Lives in temp so it is writable
// in Docker regardless of whether the iroh workdir is read-only.
const defaultRendezvousFileName = "mediastorm_rendezvous_codes.txt"

// defaultSecretFileName holds the host's persistent iroh secret key. Unlike the rendezvous
// file (codes are re-derived), this must survive restarts/redeploys so the host keeps a
// stable node ID — letting paired clients reconnect with a cached invite without a DHT
// lookup — so it lives in the persistent data dir, not temp.
const defaultSecretFileName = "iroh_host_secret.key"

var debugIrohProxyLogs = strings.EqualFold(strings.TrimSpace(os.Getenv("STRMR_IROH_PROXY_LOGS")), "1") ||
	strings.EqualFold(strings.TrimSpace(os.Getenv("STRMR_IROH_PROXY_LOGS")), "true")

type IrohHostManager struct {
	mu             sync.RWMutex
	workDir        string
	bind           string
	origin         string
	rendezvousFile string
	secretFile     string

	cmd     *exec.Cmd
	cancel  context.CancelFunc
	state   string
	invite  string
	lastErr string
	ready   chan struct{}
}

// NewIrohHostManager builds a host manager. dataDir is a persistent directory (the app
// cache dir) used to store the host's stable iroh secret key; pass "" to keep the legacy
// ephemeral identity (a new node ID every start).
func NewIrohHostManager(workDir, dataDir string) *IrohHostManager {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		workDir = discoverIrohWorkDir()
	}
	bind := strings.TrimSpace(os.Getenv("MEDIASTORM_IROH_BIND"))
	if bind == "" {
		bind = defaultIrohBind
	}
	origin := strings.TrimSpace(os.Getenv("MEDIASTORM_IROH_ORIGIN"))
	if origin == "" {
		origin = strings.TrimSpace(os.Getenv("REMOTE_ACCESS_ORIGIN"))
	}
	if origin == "" {
		origin = defaultIrohOrigin
	}
	rendezvousFile := strings.TrimSpace(os.Getenv("MEDIASTORM_IROH_RENDEZVOUS_FILE"))
	if rendezvousFile == "" {
		rendezvousFile = filepath.Join(os.TempDir(), defaultRendezvousFileName)
	}
	secretFile := strings.TrimSpace(os.Getenv("MEDIASTORM_IROH_SECRET_FILE"))
	if secretFile == "" && strings.TrimSpace(dataDir) != "" {
		secretFile = filepath.Join(strings.TrimSpace(dataDir), defaultSecretFileName)
	}
	// The host runs with cmd.Dir set to the spike workDir, so a relative path (e.g. the
	// default "cache" data dir) would resolve under that dir instead of the backend's cache.
	// Pin it to an absolute path against the backend's cwd at construction time.
	if secretFile != "" {
		if abs, err := filepath.Abs(secretFile); err == nil {
			secretFile = abs
		}
	}
	return &IrohHostManager{
		workDir:        workDir,
		bind:           bind,
		origin:         origin,
		rendezvousFile: rendezvousFile,
		secretFile:     secretFile,
		state:          "stopped",
	}
}

// RendezvousFilePath implements remoteaccess.RendezvousPublisher: the service writes the
// active connection codes here and the host watches it to publish DHT records.
func (m *IrohHostManager) RendezvousFilePath() string {
	return m.rendezvousFile
}

func (m *IrohHostManager) PublishRendezvousRecords(ctx context.Context, codes []string, invite string) error {
	invite = strings.TrimSpace(invite)
	if invite == "" || len(codes) == 0 {
		return nil
	}
	if err := m.validateWorkDirForPublish(); err != nil {
		return err
	}
	for _, code := range codes {
		code = strings.TrimSpace(code)
		if code == "" {
			continue
		}
		if err := m.publishRendezvousRecord(ctx, code, invite); err != nil {
			return err
		}
	}
	return nil
}

func (m *IrohHostManager) publishRendezvousRecord(ctx context.Context, code, invite string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := m.buildRendezvousPublishCommand(ctx, code, invite)
	output, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(output))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("publish rendezvous code %s: %s", code, msg)
	}
	log.Printf("[remote-access][iroh] rendezvous published code=%s", code)
	return nil
}

func (m *IrohHostManager) Ensure(ctx context.Context) (string, error) {
	m.mu.Lock()
	if m.isRunningLocked() {
		invite := m.invite
		ready := m.ready
		m.mu.Unlock()
		if invite != "" {
			return invite, nil
		}
		return m.waitForInvite(ctx, ready)
	}
	if err := m.validateWorkDirLocked(); err != nil {
		m.mu.Unlock()
		return "", err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	cmd := m.buildCommand(runCtx)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		m.mu.Unlock()
		return "", err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		m.mu.Unlock()
		return "", err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		m.lastErr = err.Error()
		m.state = "error"
		m.mu.Unlock()
		return "", err
	}

	m.cmd = cmd
	m.cancel = cancel
	m.state = "starting"
	m.invite = ""
	m.ready = make(chan struct{})
	ready := m.ready

	go m.scanOutput(stdout, false)
	go m.scanOutput(stderr, true)
	go m.wait(cmd)
	m.mu.Unlock()

	return m.waitForInvite(ctx, ready)
}

func (m *IrohHostManager) Stop(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.stopLocked()
}

func (m *IrohHostManager) Status(ctx context.Context) models.RemoteAccessStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	running := m.isRunningLocked()
	state := m.state
	if m.workDir == "" {
		state = "not_configured"
	}
	return models.RemoteAccessStatus{
		Enabled:     m.workDir != "",
		Running:     running,
		Provider:    "iroh",
		State:       state,
		LastError:   m.lastErr,
		ActiveHosts: boolToInt(running),
	}
}

func (m *IrohHostManager) waitForInvite(ctx context.Context, ready <-chan struct{}) (string, error) {
	if ready == nil {
		return "", errors.New("iroh host readiness channel missing")
	}
	timeout := time.NewTimer(20 * time.Second)
	defer timeout.Stop()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-ready:
	case <-timeout.C:
		return "", errors.New("timed out waiting for iroh invite")
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.invite == "" {
		if m.lastErr != "" {
			return "", errors.New(m.lastErr)
		}
		return "", errors.New("iroh host exited before publishing invite")
	}
	return m.invite, nil
}

func (m *IrohHostManager) scanOutput(output io.Reader, isErr bool) {
	scanner := bufio.NewScanner(output)
	for scanner.Scan() {
		line := scanner.Text()
		if shouldLogIrohLine(line, isErr) {
			log.Printf("[remote-access][iroh] %s", line)
		}
		m.mu.Lock()
		// Only the host's "invite=<blob>" line carries the invite. Match the prefix
		// explicitly: a bare `value != line` check also fires for any other stdout line
		// that TrimSpace alters (e.g. the publisher's "rendezvous_published ..." log has a
		// trailing space), which would store a log line as the invite.
		if strings.HasPrefix(line, "invite=") {
			if value := strings.TrimSpace(strings.TrimPrefix(line, "invite=")); value != "" {
				m.invite = value
				m.state = "running"
				m.closeReadyLocked()
			}
		}
		if isErr && shouldRecordIrohError(line) {
			m.lastErr = line
		}
		m.mu.Unlock()
	}
}

func shouldLogIrohLine(line string, isErr bool) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if debugIrohProxyLogs || strings.HasPrefix(line, "invite=") {
		return true
	}
	if isErr && shouldRecordIrohError(line) {
		return true
	}
	return false
}

func shouldRecordIrohError(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	// Stream errors with "sending stopped by peer" are expected when clients
	// cancel range reads or background the app; they are per-stream closes, not
	// host failures.
	if strings.Contains(line, "stream_error") && strings.Contains(line, "sending stopped by peer") {
		return false
	}
	if strings.Contains(line, "proxy_error") && (strings.Contains(line, "sending stopped by peer") || strings.Contains(line, "connection lost")) {
		return false
	}
	if strings.Contains(line, "stream_error") && strings.Contains(line, "connection lost") {
		return false
	}
	if strings.Contains(line, "connection_stream_accept_closed") && strings.Contains(line, "timed out") {
		return false
	}
	return true
}

func (m *IrohHostManager) wait(cmd *exec.Cmd) {
	err := cmd.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cmd != cmd {
		return
	}
	m.cmd = nil
	m.cancel = nil
	if err != nil {
		m.state = "error"
		m.lastErr = err.Error()
		m.closeReadyLocked()
		return
	}
	m.state = "stopped"
	m.closeReadyLocked()
}

func (m *IrohHostManager) validateWorkDirLocked() error {
	if m.workDir == "" {
		m.lastErr = "iroh-direct-spike directory not found"
		m.state = "not_configured"
		return errors.New(m.lastErr)
	}
	if stat, err := os.Stat(m.workDir); err != nil || !stat.IsDir() {
		m.lastErr = fmt.Sprintf("iroh-direct-spike directory unavailable: %s", m.workDir)
		m.state = "not_configured"
		return errors.New(m.lastErr)
	}
	return nil
}

func (m *IrohHostManager) validateWorkDirForPublish() error {
	m.mu.RLock()
	workDir := m.workDir
	m.mu.RUnlock()
	if workDir == "" {
		return errors.New("iroh-direct-spike directory not found")
	}
	if stat, err := os.Stat(workDir); err != nil || !stat.IsDir() {
		return fmt.Errorf("iroh-direct-spike directory unavailable: %s", workDir)
	}
	return nil
}

// irohBinaryName is the compiled host binary produced by the Rust spike.
const irohBinaryName = "iroh-direct-spike"

// irohBinaryCandidates lists, in priority order, where a prebuilt host binary may live
// under workDir. The target/release and target/debug paths cover a local `cargo build`;
// the bare workDir/<name> path covers container images that drop only the compiled binary
// into the work dir (see backend/Dockerfile's iroh build stage) without the cargo target tree.
func irohBinaryCandidates(workDir string) []string {
	return []string{
		filepath.Join(workDir, "target", "release", irohBinaryName),
		filepath.Join(workDir, "target", "debug", irohBinaryName),
		filepath.Join(workDir, irohBinaryName),
	}
}

// resolveIrohBinary returns the first existing prebuilt host binary under workDir, if any.
func resolveIrohBinary(workDir string) (string, bool) {
	for _, candidate := range irohBinaryCandidates(workDir) {
		if stat, err := os.Stat(candidate); err == nil && !stat.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func (m *IrohHostManager) buildCommand(ctx context.Context) *exec.Cmd {
	args := []string{"host", "--bind", m.bind, "--origin", m.origin}
	if m.rendezvousFile != "" {
		args = append(args, "--rendezvous-file", m.rendezvousFile)
	}
	if m.secretFile != "" {
		args = append(args, "--secret-file", m.secretFile)
	}
	if binary, ok := resolveIrohBinary(m.workDir); ok {
		// Log the resolved binary so a stale pick (e.g. an old target/release
		// out-ranking current source) is visible at launch rather than only
		// surfacing as an opaque "exit status 2" from clap arg rejection.
		log.Printf("[remote-access][iroh] using host binary: %s", binary)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = m.workDir
		return cmd
	}
	// No prebuilt binary (local dev without a build) — fall back to `cargo run`.
	log.Printf("[remote-access][iroh] no prebuilt host binary under %s; falling back to `cargo run`", m.workDir)
	cmd := exec.CommandContext(ctx, "cargo", append([]string{"run", "--"}, args...)...)
	cmd.Dir = m.workDir
	return cmd
}

func (m *IrohHostManager) buildRendezvousPublishCommand(ctx context.Context, code, invite string) *exec.Cmd {
	m.mu.RLock()
	workDir := m.workDir
	m.mu.RUnlock()
	args := []string{"rendezvous-publish", "--code", code, "--invite", invite}
	if binary, ok := resolveIrohBinary(workDir); ok {
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Dir = workDir
		return cmd
	}
	cmd := exec.CommandContext(ctx, "cargo", append([]string{"run", "--"}, args...)...)
	cmd.Dir = workDir
	return cmd
}

func (m *IrohHostManager) stopLocked() error {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	var err error
	if m.cmd != nil && m.cmd.Process != nil {
		err = m.cmd.Process.Kill()
	}
	m.cmd = nil
	m.invite = ""
	m.state = "stopped"
	m.closeReadyLocked()
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}

func (m *IrohHostManager) isRunningLocked() bool {
	return m.cmd != nil && m.cmd.Process != nil && (m.state == "starting" || m.state == "running")
}

func (m *IrohHostManager) closeReadyLocked() {
	if m.ready == nil {
		return
	}
	select {
	case <-m.ready:
	default:
		close(m.ready)
	}
}

func discoverIrohWorkDir() string {
	if override := strings.TrimSpace(os.Getenv("MEDIASTORM_IROH_DIRECT_DIR")); override != "" {
		return override
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	candidates := []string{
		filepath.Join(cwd, "experiments", "iroh-direct-spike"),
		filepath.Join(cwd, "..", "experiments", "iroh-direct-spike"),
	}
	for _, candidate := range candidates {
		if stat, err := os.Stat(candidate); err == nil && stat.IsDir() {
			abs, err := filepath.Abs(candidate)
			if err == nil {
				return abs
			}
			return candidate
		}
	}
	return ""
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
