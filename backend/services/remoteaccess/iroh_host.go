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

type IrohHostManager struct {
	mu      sync.RWMutex
	workDir string
	bind    string
	origin  string

	cmd     *exec.Cmd
	cancel  context.CancelFunc
	state   string
	invite  string
	lastErr string
	ready   chan struct{}
}

func NewIrohHostManager(workDir string) *IrohHostManager {
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
	return &IrohHostManager{
		workDir: workDir,
		bind:    bind,
		origin:  origin,
		state:   "stopped",
	}
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
		log.Printf("[remote-access][iroh] %s", line)
		m.mu.Lock()
		if value := strings.TrimSpace(strings.TrimPrefix(line, "invite=")); value != line && value != "" {
			m.invite = value
			m.state = "running"
			m.closeReadyLocked()
		}
		if isErr && shouldRecordIrohError(line) {
			m.lastErr = line
		}
		m.mu.Unlock()
	}
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

func (m *IrohHostManager) buildCommand(ctx context.Context) *exec.Cmd {
	binary := filepath.Join(m.workDir, "target", "debug", "iroh-direct-spike")
	if stat, err := os.Stat(binary); err == nil && !stat.IsDir() {
		cmd := exec.CommandContext(ctx, binary, "host", "--bind", m.bind, "--origin", m.origin)
		cmd.Dir = m.workDir
		return cmd
	}
	cmd := exec.CommandContext(ctx, "cargo", "run", "--", "host", "--bind", m.bind, "--origin", m.origin)
	cmd.Dir = m.workDir
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
