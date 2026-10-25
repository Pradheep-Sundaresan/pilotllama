// Package backendprocess owns one explicitly configured local inference child.
package backendprocess

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	pb "inferno.local/controlplane/gen/inferno/v1"
	"inferno.local/controlplane/internal/rpcbackend"
)

type Config struct {
	Executable, Model, ModelID, StateDir, Batching string
	ContextTokens, MaxOutputTokens                 uint32
	StartupTimeout, ShutdownTimeout                time.Duration
	QualifyContinuous                              bool
}

func (c Config) budgets() (uint32, uint32) {
	contextTokens, maxOutputTokens := c.ContextTokens, c.MaxOutputTokens
	if contextTokens == 0 {
		contextTokens = 4096
	}
	if maxOutputTokens == 0 {
		maxOutputTokens = 512
	}
	return contextTokens, maxOutputTokens
}

func (c Config) Validate() error {
	if c.Batching != "" && c.Batching != "queue" && c.Batching != "continuous" {
		return errors.New("backend batching must be queue or continuous")
	}
	contextTokens, maxOutputTokens := c.budgets()
	if contextTokens < 32 || contextTokens > 1048576 || maxOutputTokens > contextTokens {
		return errors.New("backend context must be 32..1048576 tokens and output must fit inside it")
	}
	if c.Batching == "continuous" && (contextTokens < 64 || maxOutputTokens > contextTokens/2) {
		return errors.New("continuous output budget must fit each half of the shared context")
	}
	for _, path := range []string{c.Executable, c.Model} {
		if !filepath.IsAbs(path) {
			return errors.New("backend executable and model must be absolute local paths")
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("backend executable and model must be regular files")
		}
		if path == c.Executable && info.Mode().Perm()&0111 == 0 {
			return errors.New("backend executable is not executable")
		}
	}
	if c.StateDir == "" {
		return errors.New("backend state directory is required")
	}
	if len(c.ModelID) == 0 || len(c.ModelID) > 128 || strings.IndexFunc(c.ModelID, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("._-", r))
	}) >= 0 {
		return errors.New("model ID must use 1..128 ASCII letters, digits, dot, underscore or hyphen")
	}
	if c.StartupTimeout <= 0 || c.StartupTimeout > time.Hour || c.ShutdownTimeout <= 0 || c.ShutdownTimeout > time.Minute {
		return errors.New("startup timeout must be in (0,1h], shutdown timeout in (0,1m]")
	}
	return nil
}

func ready(ctx context.Context, target, modelID string) bool {
	client, err := rpcbackend.New(target)
	if err != nil {
		return false
	}
	defer client.Close()
	status, err := client.Status(ctx)
	return err == nil && status.Response.State == pb.BackendState_BACKEND_STATE_READY && status.Response.GetModelId() == modelID
}

// Run starts a child on an OS-assigned loopback port, verifies live readiness,
// and runs serve. Cancellation stops HTTP first, then signals only our child.
// A child that misses its grace period is killed and reaped, never orphaned.
func Run(ctx context.Context, c Config, serve func(context.Context, string) error) error {
	return run(ctx, c, serve, func(path string, args ...string) *exec.Cmd { return exec.Command(path, args...) }, ready)
}

func run(ctx context.Context, c Config, serve func(context.Context, string) error,
	command func(string, ...string) *exec.Cmd, probe func(context.Context, string, string) bool) (result error) {
	if err := c.Validate(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(c.StateDir, 0700); err != nil {
		return err
	}
	dir, err := os.MkdirTemp(c.StateDir, "backend-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	readyFile := filepath.Join(dir, "ready")
	contextTokens, maxOutputTokens := c.budgets()
	cmd := command(c.Executable, "--model", c.Model, "--model-id", c.ModelID,
		"--listen", "127.0.0.1:0", "--ready-file", readyFile,
		"--context-tokens", strconv.FormatUint(uint64(contextTokens), 10),
		"--max-output-tokens", strconv.FormatUint(uint64(maxOutputTokens), 10))
	if c.Batching == "continuous" {
		cmd.Args = append(cmd.Args, "--batching", "continuous")
		if c.QualifyContinuous {
			cmd.Args = append(cmd.Args, "--qualify-continuous", "true")
		}
	}
	// Keep terminal signals from bypassing Go's HTTP-before-native shutdown order.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Inherit the user's output streams; no retained, unbounded log buffer.
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start backend: %w", err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	defer func() {
		select {
		case <-done:
			return
		default:
		}
		_ = cmd.Process.Signal(syscall.SIGTERM)
		timer := time.NewTimer(c.ShutdownTimeout)
		defer timer.Stop()
		select {
		case <-done:
			if waitErr != nil {
				result = errors.Join(result, fmt.Errorf("backend shutdown: %w", waitErr))
			}
		case <-timer.C:
			_ = cmd.Process.Kill()
			<-done
			result = errors.Join(result, errors.New("backend exceeded shutdown grace period; owned child killed and reaped"))
		}
	}()
	startup, cancelStartup := context.WithTimeout(ctx, c.StartupTimeout)
	defer cancelStartup()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var target string
	for target == "" {
		select {
		case <-done:
			return fmt.Errorf("backend exited before readiness: %v", waitErr)
		case <-startup.Done():
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("backend readiness: %w", startup.Err())
		case <-ticker.C:
			info, err := os.Stat(readyFile)
			if err != nil || !info.Mode().IsRegular() || info.Size() > 16 {
				continue
			}
			data, err := os.ReadFile(readyFile)
			if err != nil {
				continue
			}
			port, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || port < 1 || port > 65535 {
				continue
			}
			candidate := "127.0.0.1:" + strconv.Itoa(port)
			if probe(startup, candidate, c.ModelID) {
				target = candidate
			}
		}
	}
	cancelStartup()
	serviceCtx, cancelService := context.WithCancel(ctx)
	defer cancelService()
	served := make(chan error, 1)
	go func() { served <- serve(serviceCtx, target) }()
	select {
	case err := <-served:
		return err
	case <-done:
		cancelService()
		serveErr := <-served
		if ctx.Err() != nil {
			return serveErr
		}
		return errors.Join(fmt.Errorf("backend exited unexpectedly: %v", waitErr), serveErr)
	case <-ctx.Done():
		cancelService()
		return <-served
	}
}
